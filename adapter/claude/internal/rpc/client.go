package rpc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/lsm/open-agent-protocol/adapter/claude/internal/native"
)

var (
	ErrClosed             = errors.New("claude rpc: client closed")
	ErrControlQueue       = errors.New("claude rpc: reverse control queue is full")
	ErrObservationQueue   = errors.New("claude rpc: observation queue is full")
	ErrWriteQueue         = errors.New("claude rpc: write queue is full")
	ErrReverseResolved    = errors.New("claude rpc: reverse control request already resolved")
	ErrControlNotPending  = errors.New("claude rpc: control response id is not pending")
	ErrDuplicateRequestID = errors.New("claude rpc: request id is already active")
)

// RemoteError is an error-subtype control response to a host request.
type RemoteError struct {
	ID     string
	Detail string
}

func (err *RemoteError) Error() string {
	return fmt.Sprintf("claude rpc error for request %s: %s", err.ID, err.Detail)
}

// IncomingControl is one reverse control_request (the permission ask). It
// must be answered exactly once; the CLI blocks until the answer arrives.
type IncomingControl struct {
	ID      string
	Subtype string
	Raw     json.RawMessage // the full control_request frame
	Value   any             // typed value from native.DecodeControlRequest
	client  *Client
	mu      sync.Mutex
	done    bool
}

// Respond answers with a success payload (e.g. the allow/deny decision).
func (control *IncomingControl) Respond(ctx context.Context, response any) error {
	data, err := marshalOptional(response)
	if err != nil {
		return err
	}
	envelope, err := json.Marshal(map[string]any{"subtype": "success", "request_id": control.ID, "response": decodeRaw(data)})
	if err != nil {
		return err
	}
	return control.respond(ctx, envelope)
}

// RespondError answers with an error payload.
func (control *IncomingControl) RespondError(ctx context.Context, message string) error {
	envelope, err := json.Marshal(map[string]any{"subtype": "error", "request_id": control.ID, "error": message})
	if err != nil {
		return err
	}
	return control.respond(ctx, envelope)
}

func (control *IncomingControl) respond(ctx context.Context, envelope []byte) error {
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.done {
		return ErrReverseResolved
	}
	if err := control.client.write(ctx, ControlResponseMessage(envelope)); err != nil {
		return err
	}
	control.done = true
	control.client.finishIncoming(control.ID, control)
	return nil
}

// CancelNotice reports the CLI withdrawing one of its in-flight reverse
// requests; the pending ask must not be answered afterwards.
type CancelNotice struct {
	RequestID string
}

// ObservationMessage is one message-stream frame in reader order, with the
// typed value from the native vocabulary attached once decoded.
type ObservationMessage struct {
	Type    string
	Subtype string
	Raw     json.RawMessage
	Value   any
}

// InboundMessage preserves the reader's native receive order across reverse
// control requests, observations, and control-call responses. Exactly one
// field is set. A Barrier must be acknowledged after all earlier messages
// have been semantically reduced; only then is the corresponding control
// response delivered to its caller.
type InboundMessage struct {
	Observation *ObservationMessage
	Control     *IncomingControl
	Cancel      *CancelNotice
	Barrier     chan struct{}
}

type controlResult struct {
	response json.RawMessage
	err      error
}

type writeRequest struct {
	ctx     context.Context
	message Message
	result  chan error
}

type Client struct {
	decoder       *Decoder
	encoder       *Encoder
	closer        io.Closer
	queueCapacity int

	mu        sync.Mutex
	pending   map[string]chan controlResult
	incoming  map[string]*IncomingControl
	closed    bool
	err       error
	readerErr error

	counter   atomic.Int64
	writes    chan writeRequest
	inbound   chan InboundMessage
	done      chan struct{}
	closeOnce sync.Once
}

type ClientOptions struct {
	FrameLimit         int
	QueueCapacity      int
	WriteQueueCapacity int
	CloseReadWriter    io.Closer
}

// NewClient starts exactly one reader and one serialized writer over the
// stream-json boundary. A non-nil CloseReadWriter is required if Close or
// cancellation must interrupt blocked I/O.
func NewClient(reader io.Reader, writer io.Writer, options ClientOptions) *Client {
	capacity := options.QueueCapacity
	if capacity <= 0 {
		capacity = 64
	}
	writeCapacity := options.WriteQueueCapacity
	if writeCapacity <= 0 {
		writeCapacity = capacity
	}
	client := &Client{
		decoder: NewDecoder(reader, options.FrameLimit), encoder: NewEncoder(writer, options.FrameLimit),
		closer: options.CloseReadWriter, queueCapacity: capacity,
		pending: make(map[string]chan controlResult), incoming: make(map[string]*IncomingControl),
		writes: make(chan writeRequest, writeCapacity), inbound: make(chan InboundMessage, capacity),
		done: make(chan struct{}),
	}
	go client.writeLoop()
	go client.readLoop()
	return client
}

func (client *Client) Inbound() <-chan InboundMessage { return client.inbound }
func (client *Client) Done() <-chan struct{}          { return client.done }

func (client *Client) Err() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	// A reader error is the specific wire truth (malformed frame, EOF) and
	// wins over the concurrent process-exit or close error.
	if client.readerErr != nil {
		return client.readerErr
	}
	return client.err
}

// Call issues one control_request and waits for its response. Host request
// ids are minted in the reference SDK's form (req_<counter>_<8 hex>).
func (client *Client) Call(ctx context.Context, request any, result any) error {
	data, err := marshalOptional(request)
	if err != nil {
		return err
	}
	id := client.mintID()
	response := make(chan controlResult, 1)
	client.mu.Lock()
	if client.closed {
		err := firstError(client.err, ErrClosed)
		client.mu.Unlock()
		return err
	}
	if _, exists := client.pending[id]; exists {
		client.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrDuplicateRequestID, id)
	}
	client.pending[id] = response
	client.mu.Unlock()

	if err := client.write(ctx, ControlRequestMessage(id, data)); err != nil {
		client.removePending(id)
		return err
	}
	select {
	case outcome := <-response:
		return decodeControlResult(id, outcome, result)
	case <-ctx.Done():
		// Cancellation and response delivery can race; a response already
		// buffered is authoritative and must win over the local deadline.
		select {
		case outcome := <-response:
			return decodeControlResult(id, outcome, result)
		default:
		}
		client.removePending(id)
		// The request was fully written. Without a protocol response its
		// remote outcome is ambiguous; retire the connection so a late
		// response cannot contaminate later correlation.
		client.closeWith(ctx.Err())
		return ctx.Err()
	case <-client.done:
		select {
		case outcome := <-response:
			return decodeControlResult(id, outcome, result)
		default:
			return client.closeError()
		}
	}
}

// WriteUser submits one user turn. The write completing is not admission;
// the CLI's echo of the frame's uuid is.
func (client *Client) WriteUser(ctx context.Context, frame json.RawMessage) error {
	return client.write(ctx, UserTurnMessage(frame))
}

func (client *Client) Close() error {
	client.closeWith(ErrClosed)
	return nil
}

func (client *Client) closeWith(reason error) {
	client.closeOnce.Do(func() {
		if client.closer != nil {
			_ = client.closer.Close()
		}
		client.shutdown(reason)
	})
}

func (client *Client) mintID() string {
	var entropy [4]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		// crypto/rand never fails on the supported platforms; fall back to a
		// deterministic suffix rather than blocking submit entirely.
		return "req_" + strconv.FormatInt(client.counter.Add(1), 10) + "_00000000"
	}
	return "req_" + strconv.FormatInt(client.counter.Add(1), 10) + "_" + hex.EncodeToString(entropy[:])
}

func (client *Client) readLoop() {
	for {
		message, err := client.decoder.Decode()
		if err != nil {
			client.mu.Lock()
			client.readerErr = err
			client.mu.Unlock()
			client.closeWith(err)
			return
		}
		if client.route(message) {
			return
		}
	}
}

func (client *Client) route(message Message) bool {
	switch message.Kind {
	case KindControlResponse:
		// The response is ordered behind wire-earlier observations: the CLI
		// answers while turn frames continue to stream (e.g. an interrupt
		// receipt written before the interrupted turn's result). The barrier
		// is enqueued and acknowledged before the response is delivered, so a
		// caller that returns from Call knows every wire-earlier frame has
		// reached the inbound stream. An unmatched response orders nothing
		// (the reference hosts ignore ids they are not waiting on), so it
		// never barriers.
		client.mu.Lock()
		_, pending := client.pending[message.Response.RequestID]
		client.mu.Unlock()
		if pending && !client.barrier() {
			return true
		}
		outcome := controlResult{response: cloneRaw(message.Response.Response)}
		if !message.Response.Success {
			outcome.err = &RemoteError{ID: message.Response.RequestID, Detail: message.Response.Error}
		}
		return client.deliver(message.Response.RequestID, outcome)
	case KindControlRequest:
		value, err := native.DecodeControlRequest(message.Subtype, message.Raw)
		if err != nil {
			client.closeWith(err)
			return true
		}
		control := &IncomingControl{ID: message.RequestID, Subtype: message.Subtype, Raw: cloneRaw(message.Raw), Value: value, client: client}
		client.mu.Lock()
		_, duplicate := client.incoming[control.ID]
		if !duplicate {
			client.incoming[control.ID] = control
		}
		client.mu.Unlock()
		if duplicate {
			client.closeWith(fmt.Errorf("%w: %s", ErrDuplicateRequestID, control.ID))
			return true
		}
		return !client.enqueue(InboundMessage{Control: control}, ErrControlQueue)
	case KindControlCancel:
		client.mu.Lock()
		pending, exists := client.incoming[message.RequestID]
		if exists {
			delete(client.incoming, message.RequestID)
		}
		client.mu.Unlock()
		if pending != nil {
			pending.mu.Lock()
			pending.done = true // the CLI abandoned the ask; no answer may be written
			pending.mu.Unlock()
		}
		return !client.enqueue(InboundMessage{Cancel: &CancelNotice{RequestID: message.RequestID}}, ErrControlQueue)
	case KindObservation:
		// A known frame type with a violated shape retires the transport,
		// mirroring the reference parser raising MessageParseError into the
		// consumer's stream; unknown types decode tolerantly.
		value, err := native.DecodeObservation(message.Type, message.Subtype, message.Raw)
		if err != nil {
			client.closeWith(err)
			return true
		}
		observation := &ObservationMessage{Type: message.Type, Subtype: message.Subtype, Raw: cloneRaw(message.Raw), Value: value}
		return !client.enqueue(InboundMessage{Observation: observation}, ErrObservationQueue)
	default:
		return false
	}
}

func (client *Client) enqueue(message InboundMessage, overflow error) bool {
	select {
	case client.inbound <- message:
		return true
	default:
		client.closeWith(overflow)
		return false
	}
}

func (client *Client) barrier() bool {
	ack := make(chan struct{})
	if !client.enqueue(InboundMessage{Barrier: ack}, ErrObservationQueue) {
		return false
	}
	select {
	case <-ack:
		return true
	case <-client.done:
		return false
	}
}

func (client *Client) deliver(id string, outcome controlResult) bool {
	client.mu.Lock()
	response, exists := client.pending[id]
	if exists {
		delete(client.pending, id)
	}
	client.mu.Unlock()
	if exists {
		response <- outcome
		return false
	}
	// Unmatched control responses are ignored by the reference hosts (a
	// requester "ignores responses for request_ids it is not waiting on"),
	// so a late or foreign response is recorded, not fatal.
	return false
}

func (client *Client) removePending(id string) {
	client.mu.Lock()
	delete(client.pending, id)
	client.mu.Unlock()
}

func (client *Client) finishIncoming(id string, control *IncomingControl) {
	client.mu.Lock()
	if client.incoming[id] == control {
		delete(client.incoming, id)
	}
	client.mu.Unlock()
}

func (client *Client) write(ctx context.Context, message Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	request := writeRequest{ctx: ctx, message: message, result: make(chan error, 1)}
	// Check shutdown first: a closed client must never accept new writes, and
	// a random select could otherwise enqueue into a pump that already exited.
	select {
	case <-client.done:
		return client.closeError()
	default:
	}
	select {
	case client.writes <- request:
	case <-ctx.Done():
		return ctx.Err()
	case <-client.done:
		return client.closeError()
	default:
		return ErrWriteQueue
	}
	select {
	case err := <-request.result:
		return err
	case <-ctx.Done():
		// Dequeue and cancellation can race. Once accepted by the bounded
		// pump, absence of a write result cannot prove that no bytes were
		// written, so retire the transport rather than risk a partial frame
		// followed by unrelated traffic.
		client.closeWith(ctx.Err())
		return ctx.Err()
	case <-client.done:
		select {
		case err := <-request.result:
			return err
		default:
			return client.closeError()
		}
	}
}

func (client *Client) writeLoop() {
	for {
		select {
		case request := <-client.writes:
			if err := request.ctx.Err(); err != nil {
				request.result <- err
				continue
			}
			err := client.encoder.Encode(request.message)
			request.result <- err
			if err != nil {
				client.closeWith(err)
				return
			}
		case <-client.done:
			// Drain queued writes so no caller is left waiting on a result and
			// no byte is emitted after logical shutdown.
			for {
				select {
				case request := <-client.writes:
					request.result <- client.closeError()
				default:
					return
				}
			}
		}
	}
}

func (client *Client) closeError() error { return firstError(client.Err(), ErrClosed) }

func (client *Client) shutdown(reason error) {
	client.mu.Lock()
	if client.closed {
		client.mu.Unlock()
		return
	}
	client.closed = true
	client.err = reason
	pending := client.pending
	client.pending = make(map[string]chan controlResult)
	close(client.done)
	client.mu.Unlock()
	for _, response := range pending {
		response <- controlResult{err: reason}
	}
}

func decodeControlResult(id string, outcome controlResult, result any) error {
	if outcome.err != nil {
		return outcome.err
	}
	if result == nil || len(outcome.response) == 0 {
		return nil
	}
	if err := json.Unmarshal(outcome.response, result); err != nil {
		return fmt.Errorf("decode control response %s: %w", id, err)
	}
	return nil
}

// decodeRaw keeps pre-marshaled JSON from being double-encoded inside an
// envelope built with json.Marshal.
func decodeRaw(data []byte) json.RawMessage {
	if len(data) == 0 {
		return json.RawMessage("{}")
	}
	return json.RawMessage(data)
}

func marshalOptional(value any) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func firstError(first, fallback error) error {
	if first != nil {
		return first
	}
	return fallback
}

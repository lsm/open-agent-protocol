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

type RemoteError struct {
	ID     string
	Detail string
}

func (err *RemoteError) Error() string {
	return fmt.Sprintf("claude rpc error for request %s: %s", err.ID, err.Detail)
}

type IncomingControl struct {
	ID      string
	Subtype string
	Raw     json.RawMessage
	Value   any
	client  *Client
	mu      sync.Mutex
	done    bool
}

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

type CancelNotice struct {
	RequestID string
}

type ObservationMessage struct {
	Type    string
	Subtype string
	Raw     json.RawMessage
	Value   any
}

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
	started chan struct{}
	encoded chan struct{}
}

type Client struct {
	decoder       *Decoder
	encoder       *Encoder
	closer        io.Closer
	closerOnce    sync.Once
	queueCapacity int

	mu        sync.Mutex
	pending   map[string]chan controlResult
	incoming  map[string]*IncomingControl
	closed    bool
	decoding  bool
	routing   bool
	err       error
	readerErr error

	counter   atomic.Int64
	writes    chan writeRequest
	inbound   chan InboundMessage
	done      chan struct{}
	readDone  chan struct{}
	closeOnce sync.Once
}

type ClientOptions struct {
	FrameLimit         int
	QueueCapacity      int
	WriteQueueCapacity int
	CloseReadWriter    io.Closer
}

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
		done: make(chan struct{}), readDone: make(chan struct{}),
	}
	go client.writeLoop()
	go client.readLoop()
	return client
}

func (client *Client) Inbound() <-chan InboundMessage { return client.inbound }
func (client *Client) Done() <-chan struct{}          { return client.done }

func (client *Client) ReadDone() <-chan struct{} { return client.readDone }

func (client *Client) Err() error {
	client.mu.Lock()
	defer client.mu.Unlock()

	if client.readerErr != nil {
		return client.readerErr
	}
	return client.err
}

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
		select {
		case <-client.done:

			select {
			case outcome := <-response:
				return decodeControlResult(id, outcome, result)
			case <-ctx.Done():
				client.removePending(id)
				return ctx.Err()
			}
		default:
			client.removePending(id)
			return err
		}
	}
	select {
	case outcome := <-response:
		return decodeControlResult(id, outcome, result)
	case <-ctx.Done():

		select {
		case outcome := <-response:
			return decodeControlResult(id, outcome, result)
		default:
		}
		client.removePending(id)

		client.closeWith(ctx.Err())
		return ctx.Err()
	}

}

func (client *Client) WriteUser(ctx context.Context, frame json.RawMessage) error {
	return client.write(ctx, UserTurnMessage(frame))
}

func (client *Client) Close() error {
	client.closeWith(ErrClosed)
	return nil
}

func (client *Client) closeWith(reason error) {
	client.closeOnce.Do(func() {
		client.closeTransport()
		client.shutdown(reason)
	})
}

func (client *Client) mintID() string {
	var entropy [4]byte
	if _, err := rand.Read(entropy[:]); err != nil {

		return "req_" + strconv.FormatInt(client.counter.Add(1), 10) + "_00000000"
	}
	return "req_" + strconv.FormatInt(client.counter.Add(1), 10) + "_" + hex.EncodeToString(entropy[:])
}

func (client *Client) closeTransport() {
	client.closerOnce.Do(func() {
		if client.closer != nil {
			_ = client.closer.Close()
		}
	})
}

func (client *Client) readLoop() {
	defer close(client.readDone)

	defer close(client.inbound)
	for {

		client.mu.Lock()
		client.decoding = true
		client.mu.Unlock()
		message, err := client.decoder.Decode()
		client.mu.Lock()
		client.decoding = false
		client.routing = err == nil
		if err != nil {
			client.readerErr = err
		}
		client.mu.Unlock()
		if err != nil {
			client.closeWith(err)
			client.settleReader()
			return
		}
		stop := client.route(message)
		client.settleReader()
		if stop {
			return
		}
	}
}

func (client *Client) settleReader() {
	client.mu.Lock()
	client.routing = false
	pending := client.retirePendingLocked()
	client.mu.Unlock()
	client.failPending(pending, client.closeError())
}

func (client *Client) retirePendingLocked() map[string]chan controlResult {
	if !client.closed {
		return nil
	}
	pending := client.pending
	client.pending = make(map[string]chan controlResult)
	return pending
}

func (client *Client) failPending(pending map[string]chan controlResult, reason error) {
	for _, response := range pending {
		response <- controlResult{err: reason}
	}
}

func (client *Client) route(message Message) bool {
	switch message.Kind {
	case KindControlResponse:

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
			pending.done = true
			pending.mu.Unlock()
		}
		return !client.enqueue(InboundMessage{Cancel: &CancelNotice{RequestID: message.RequestID}}, ErrControlQueue)
	case KindObservation:

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
	case <-client.done:
	}
	return true
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
	request := writeRequest{ctx: ctx, message: message, started: make(chan struct{}), encoded: make(chan struct{}), result: make(chan error, 1)}

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

		client.closeWith(ctx.Err())
		return ctx.Err()
	case <-client.done:
		select {
		case err := <-request.result:
			return err
		case <-request.started:

			if !client.pumpSettles(request) {
				return client.closeError()
			}
			select {
			case err := <-request.result:
				return err
			case <-ctx.Done():
				client.closeWith(ctx.Err())
				return ctx.Err()
			}
		default:
			return client.closeError()
		}
	}
}

func (client *Client) pumpSettles(request writeRequest) bool {
	select {
	case <-request.encoded:

		return true
	default:

		return client.closer != nil
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
			close(request.started)
			err := client.encoder.Encode(request.message)

			close(request.encoded)
			if err != nil {

				client.closeWith(err)
			}
			request.result <- err
			if err != nil {
				return
			}
		case <-client.done:

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
	close(client.done)

	deferToReader := client.routing || (client.decoding && client.closer != nil)
	var pending map[string]chan controlResult
	if !deferToReader {
		pending = client.retirePendingLocked()
	}
	client.mu.Unlock()

	client.closeTransport()
	client.failPending(pending, reason)
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

package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

var (
	ErrClosed                 = errors.New("acp rpc: client closed")
	ErrResponseNotFound       = errors.New("acp rpc: response id is not pending")
	ErrDuplicateRequestID     = errors.New("acp rpc: request id is already active")
	ErrNotificationQueue      = errors.New("acp rpc: notification queue is full")
	ErrRequestQueue           = errors.New("acp rpc: reverse request queue is full")
	ErrWriteQueue             = errors.New("acp rpc: write queue is full")
	ErrReverseRequestResolved = errors.New("acp rpc: reverse request already resolved")
)

type RemoteError struct {
	ID     RequestID
	Object ErrorObject
}

func (err *RemoteError) Error() string {
	return fmt.Sprintf("acp rpc error %d for request %s: %s", err.Object.Code, err.ID, err.Object.Message)
}

type IncomingRequest struct {
	ID     RequestID
	Method string
	Params json.RawMessage
	client *Client
	mu     sync.Mutex
	done   bool
}

func (request *IncomingRequest) Respond(ctx context.Context, result any) error {
	data, err := marshalValue(result)
	if err != nil {
		return err
	}
	return request.respond(ctx, Response(request.ID, data))
}

func (request *IncomingRequest) RespondError(ctx context.Context, code int64, message string, data any) error {
	raw, err := marshalOptional(data)
	if err != nil {
		return err
	}
	return request.respond(ctx, ErrorResponse(request.ID, ErrorObject{Code: code, Message: message, Data: raw}))
}

func (request *IncomingRequest) respond(ctx context.Context, message Message) error {
	request.mu.Lock()
	defer request.mu.Unlock()
	if request.done {
		return ErrReverseRequestResolved
	}
	if err := request.client.write(ctx, message); err != nil {
		return err
	}
	request.done = true
	request.client.finishIncoming(request.ID, request)
	return nil
}

type NotificationMessage struct {
	Method string
	Params json.RawMessage
}

// InboundMessage preserves the reader's native receive order across reverse
// requests, notifications, and outbound-call responses. Exactly one field is
// set. A Barrier must be acknowledged after all earlier messages have been
// semantically reduced; only then is the corresponding call response delivered.
type InboundMessage struct {
	Request      *IncomingRequest
	Notification *NotificationMessage
	Barrier      chan struct{}
}

type callResult struct {
	result json.RawMessage
	err    error
}

type writeRequest struct {
	ctx     context.Context
	message Message
	started chan struct{}
	result  chan error
}

type Client struct {
	decoder           *Decoder
	encoder           *Encoder
	closer            io.Closer
	strictResponseIDs bool

	mu       sync.Mutex
	pending  map[RequestID]chan callResult
	incoming map[RequestID]*IncomingRequest
	closed   bool
	err      error

	nextID        atomic.Int64
	ordered       atomic.Bool
	writes        chan writeRequest
	requests      chan *IncomingRequest
	notifications chan NotificationMessage
	inbound       chan InboundMessage
	diagnostics   chan error
	done          chan struct{}
	closeOnce     sync.Once
}

type ClientOptions struct {
	FrameLimit         int
	QueueCapacity      int
	WriteQueueCapacity int
	FirstRequestID     int64
	CloseReadWriter    io.Closer
	StrictResponseIDs  bool
}

// NewClient starts exactly one reader and one serialized writer. A non-nil
// CloseReadWriter is required if Close or cancellation must interrupt blocked
// I/O; plain io.Reader/io.Writer values have no portable interrupt operation.
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
		decoder: NewDecoder(reader, options.FrameLimit), encoder: NewEncoder(writer),
		closer: options.CloseReadWriter, strictResponseIDs: options.StrictResponseIDs,
		pending: make(map[RequestID]chan callResult), incoming: make(map[RequestID]*IncomingRequest),
		writes: make(chan writeRequest, writeCapacity), requests: make(chan *IncomingRequest, capacity),
		notifications: make(chan NotificationMessage, capacity), inbound: make(chan InboundMessage, capacity), diagnostics: make(chan error, capacity),
		done: make(chan struct{}),
	}
	client.nextID.Store(options.FirstRequestID)
	go client.writeLoop()
	go client.readLoop()
	return client
}

func (client *Client) Requests() <-chan *IncomingRequest         { return client.requests }
func (client *Client) Notifications() <-chan NotificationMessage { return client.notifications }
func (client *Client) Inbound() <-chan InboundMessage {
	client.ordered.Store(true)
	return client.inbound
}
func (client *Client) Diagnostics() <-chan error { return client.diagnostics }
func (client *Client) Done() <-chan struct{}     { return client.done }

func (client *Client) Err() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.err
}

func (client *Client) Call(ctx context.Context, method string, params any, result any) error {
	return client.callID(ctx, IntegerID(client.nextID.Add(1)), method, params, result, nil)
}

// CallStarted reports exactly once whether the request was completely written.
// A nil report means the remote call is admitted and may still be pending when
// this method eventually returns. This narrow hook lets semantic adapters keep
// admission distinct from a long-lived request's terminal response.
func (client *Client) CallStarted(ctx context.Context, method string, params any, result any, started chan<- error) error {
	if started == nil {
		return client.Call(ctx, method, params, result)
	}
	return client.callID(ctx, IntegerID(client.nextID.Add(1)), method, params, result, started)
}

// CallID supports peers and tests that require either JSON-RPC string or integer
// IDs. The ID must not already identify a pending outbound call.
func (client *Client) CallID(ctx context.Context, id RequestID, method string, params any, result any) error {
	return client.callID(ctx, id, method, params, result, nil)
}

func (client *Client) callID(ctx context.Context, id RequestID, method string, params any, result any, started chan<- error) error {
	report := func(err error) {
		if started != nil {
			started <- err
			close(started)
			started = nil
		}
	}
	if !id.valid() || method == "" {
		report(ErrInvalidMessage)
		return ErrInvalidMessage
	}
	paramsJSON, err := marshalOptional(params)
	if err != nil {
		report(err)
		return err
	}
	response := make(chan callResult, 1)
	client.mu.Lock()
	if client.closed {
		err := firstError(client.err, ErrClosed)
		client.mu.Unlock()
		report(err)
		return err
	}
	if _, exists := client.pending[id]; exists {
		client.mu.Unlock()
		err := fmt.Errorf("%w: %s", ErrDuplicateRequestID, id)
		report(err)
		return err
	}
	client.pending[id] = response
	client.mu.Unlock()

	if err := client.write(ctx, Request(id, method, paramsJSON)); err != nil {
		client.removePending(id)
		report(err)
		return err
	}
	report(nil)
	select {
	case outcome := <-response:
		return decodeCallResult(method, outcome, result)
	case <-ctx.Done():
		client.removePending(id)
		// The request was fully written. Without a protocol response, its remote
		// outcome is ambiguous; retire the connection so a late response cannot
		// contaminate later correlation or session state.
		client.closeWith(ctx.Err())
		return ctx.Err()
	case <-client.done:
		select {
		case outcome := <-response:
			return decodeCallResult(method, outcome, result)
		default:
			return client.closeError()
		}
	}
}

func (client *Client) Notify(ctx context.Context, method string, params any) error {
	if method == "" {
		return ErrInvalidMessage
	}
	data, err := marshalOptional(params)
	if err != nil {
		return err
	}
	return client.write(ctx, Notification(method, data))
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

func (client *Client) readLoop() {
	for {
		message, err := client.decoder.Decode()
		if err != nil {
			client.closeWith(err)
			return
		}
		switch message.Kind {
		case MessageResponse:
			if client.ordered.Load() && !client.barrier() {
				return
			}
			if !client.deliver(message.ID, callResult{result: cloneRaw(message.Result)}) && client.unmatched(message.ID) {
				return
			}
		case MessageError:
			if client.ordered.Load() && !client.barrier() {
				return
			}
			if !client.deliver(message.ID, callResult{err: &RemoteError{ID: message.ID, Object: *message.Error}}) && client.unmatched(message.ID) {
				return
			}
		case MessageRequest:
			incoming := &IncomingRequest{ID: message.ID, Method: message.Method, Params: cloneRaw(message.Params), client: client}
			client.mu.Lock()
			_, duplicate := client.incoming[message.ID]
			if !duplicate {
				client.incoming[message.ID] = incoming
			}
			client.mu.Unlock()
			if duplicate {
				client.closeWith(fmt.Errorf("%w: %s", ErrDuplicateRequestID, message.ID))
				return
			}
			if client.ordered.Load() {
				if !client.enqueueInbound(InboundMessage{Request: incoming}, ErrRequestQueue) {
					return
				}
			} else {
				select {
				case client.requests <- incoming:
				default:
					client.closeWith(ErrRequestQueue)
					return
				}
			}
		case MessageNotification:
			notification := NotificationMessage{Method: message.Method, Params: cloneRaw(message.Params)}
			if client.ordered.Load() {
				if !client.enqueueInbound(InboundMessage{Notification: &notification}, ErrNotificationQueue) {
					return
				}
			} else {
				select {
				case client.notifications <- notification:
				default:
					client.closeWith(ErrNotificationQueue)
					return
				}
			}
		}
	}
}

func (client *Client) enqueueInbound(message InboundMessage, overflow error) bool {
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
	if !client.enqueueInbound(InboundMessage{Barrier: ack}, ErrNotificationQueue) {
		return false
	}
	select {
	case <-ack:
		return true
	case <-client.done:
		return false
	}
}

func (client *Client) unmatched(id RequestID) bool {
	err := fmt.Errorf("%w: %s", ErrResponseNotFound, id)
	if client.strictResponseIDs {
		client.closeWith(err)
		return true
	}
	client.diagnose(err)
	return false
}

func (client *Client) deliver(id RequestID, outcome callResult) bool {
	client.mu.Lock()
	response, exists := client.pending[id]
	if exists {
		delete(client.pending, id)
	}
	client.mu.Unlock()
	if exists {
		response <- outcome
	}
	return exists
}

func (client *Client) removePending(id RequestID) {
	client.mu.Lock()
	delete(client.pending, id)
	client.mu.Unlock()
}

func (client *Client) finishIncoming(id RequestID, request *IncomingRequest) {
	client.mu.Lock()
	if client.incoming[id] == request {
		delete(client.incoming, id)
	}
	client.mu.Unlock()
}

func (client *Client) diagnose(err error) {
	select {
	case client.diagnostics <- err:
	default:
	}
}

func (client *Client) write(ctx context.Context, message Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	request := writeRequest{ctx: ctx, message: message, started: make(chan struct{}), result: make(chan error, 1)}
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
		// Dequeue and cancellation can race. Once accepted by the bounded pump,
		// absence of a write result cannot prove that no bytes were written, so
		// retire the transport rather than risk a partial frame followed by
		// unrelated traffic.
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
			close(request.started)
			err := client.encoder.Encode(request.message)
			request.result <- err
			if err != nil {
				client.closeWith(err)
				return
			}
		case <-client.done:
			return
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
	client.pending = make(map[RequestID]chan callResult)
	close(client.done)
	client.mu.Unlock()
	for _, response := range pending {
		response <- callResult{err: reason}
	}
}

func decodeCallResult(method string, outcome callResult, result any) error {
	if outcome.err != nil {
		return outcome.err
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(outcome.result, result); err != nil {
		return fmt.Errorf("decode %s response: %w", method, err)
	}
	return nil
}
func marshalValue(value any) (json.RawMessage, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return data, nil
}
func marshalOptional(value any) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	return marshalValue(value)
}
func firstError(first, fallback error) error {
	if first != nil {
		return first
	}
	return fallback
}

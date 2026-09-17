package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/lsm/open-agent-protocol/adapter/hermes/internal/native"
)

var (
	ErrClosed                 = errors.New("hermes rpc: client closed")
	ErrResponseNotFound       = errors.New("hermes rpc: response id is not pending")
	ErrDuplicateRequestID     = errors.New("hermes rpc: request id is already active")
	ErrNotificationQueue      = errors.New("hermes rpc: notification queue is full")
	ErrRequestQueue           = errors.New("hermes rpc: reverse request queue is full")
	ErrWriteQueue             = errors.New("hermes rpc: write queue is full")
	ErrReverseRequestResolved = errors.New("hermes rpc: reverse request already resolved")
)

type RemoteError struct {
	ID     RequestID
	Object ErrorObject
}

func (err *RemoteError) Error() string {
	return fmt.Sprintf("hermes rpc error %d for request %s: %s", err.Object.Code, err.ID, err.Object.Message)
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
	Value  any
}

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
	encoded chan struct{}
}

type Client struct {
	decoder       *Decoder
	encoder       *Encoder
	closer        io.Closer
	closerOnce    sync.Once
	queueCapacity int

	mu        sync.Mutex
	routeMu   sync.Mutex
	pending   map[RequestID]chan callResult
	incoming  map[RequestID]*IncomingRequest
	backlog   []InboundMessage
	closed    bool
	decoding  bool
	routing   bool
	err       error
	readerErr error

	nextID        atomic.Int64
	routeMode     atomic.Int32
	writes        chan writeRequest
	requests      chan *IncomingRequest
	notifications chan NotificationMessage
	inbound       chan InboundMessage
	diagnostics   chan error
	done          chan struct{}
	readDone      chan struct{}
	closeOnce     sync.Once
}

type ClientOptions struct {
	FrameLimit         int
	QueueCapacity      int
	WriteQueueCapacity int
	FirstRequestID     int64
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
		pending: make(map[RequestID]chan callResult), incoming: make(map[RequestID]*IncomingRequest),
		writes: make(chan writeRequest, writeCapacity), requests: make(chan *IncomingRequest, capacity),
		notifications: make(chan NotificationMessage, capacity), inbound: make(chan InboundMessage, capacity), diagnostics: make(chan error, capacity),
		done: make(chan struct{}), readDone: make(chan struct{}),
	}
	client.nextID.Store(options.FirstRequestID)
	go client.writeLoop()
	go client.readLoop()
	return client
}

func (client *Client) Requests() <-chan *IncomingRequest {
	client.activateLegacy()
	return client.requests
}
func (client *Client) Notifications() <-chan NotificationMessage {
	client.activateLegacy()
	return client.notifications
}
func (client *Client) Inbound() <-chan InboundMessage {

	if client.routeMode.Load() == 2 {
		return client.inbound
	}
	client.routeMu.Lock()
	defer client.routeMu.Unlock()
	if client.routeMode.Load() == 0 {
		client.routeMode.Store(2)
		for _, message := range client.backlog {
			client.inbound <- message
		}
		client.backlog = nil
	}
	return client.inbound
}

func (client *Client) activateLegacy() {
	client.routeMu.Lock()
	defer client.routeMu.Unlock()
	if client.routeMode.Load() != 0 {
		return
	}
	client.routeMode.Store(-1)
	for _, message := range client.backlog {
		switch {
		case message.Request != nil:
			client.requests <- message.Request
		case message.Notification != nil:
			client.notifications <- *message.Notification
		}
	}
	client.backlog = nil
}

func (client *Client) Diagnostics() <-chan error { return client.diagnostics }
func (client *Client) Done() <-chan struct{}     { return client.done }

func (client *Client) ReadDone() <-chan struct{} { return client.readDone }

func (client *Client) Err() error {
	client.mu.Lock()
	defer client.mu.Unlock()

	if client.readerErr != nil {
		return client.readerErr
	}
	return client.err
}

func (client *Client) Call(ctx context.Context, method string, params any, result any) error {
	return client.callID(ctx, IntegerID(client.nextID.Add(1)), method, params, result)
}

func (client *Client) CallID(ctx context.Context, id RequestID, method string, params any, result any) error {
	return client.callID(ctx, id, method, params, result)
}

func (client *Client) callID(ctx context.Context, id RequestID, method string, params any, result any) error {
	if !id.valid() || method == "" {
		return ErrInvalidMessage
	}
	paramsJSON, err := marshalOptional(params)
	if err != nil {
		return err
	}
	response := make(chan callResult, 1)
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

	if err := client.write(ctx, Request(id, method, paramsJSON)); err != nil {
		select {
		case <-client.done:

			select {
			case outcome := <-response:
				return decodeCallResult(method, outcome, result)
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
		return decodeCallResult(method, outcome, result)
	case <-ctx.Done():

		select {
		case outcome := <-response:
			return decodeCallResult(method, outcome, result)
		default:
		}
		client.removePending(id)

		client.closeWith(ctx.Err())
		return ctx.Err()
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
		client.closeTransport()
		client.shutdown(reason)
	})
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
		client.routeMu.Lock()
		stop := client.route(message)
		client.routeMu.Unlock()
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

func (client *Client) retirePendingLocked() map[RequestID]chan callResult {
	if !client.closed {
		return nil
	}
	pending := client.pending
	client.pending = make(map[RequestID]chan callResult)
	return pending
}

func (client *Client) failPending(pending map[RequestID]chan callResult, reason error) {
	for _, response := range pending {
		response <- callResult{err: reason}
	}
}

func (client *Client) route(message Message) bool {
	mode := client.routeMode.Load()
	switch message.Kind {
	case MessageResponse:
		if mode == 2 && !client.barrier() {
			return true
		}
		return client.deliver(message.ID, callResult{result: cloneRaw(message.Result)})
	case MessageError:
		if mode == 2 && !client.barrier() {
			return true
		}
		return client.deliver(message.ID, callResult{err: &RemoteError{ID: message.ID, Object: *message.Error}})
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
			return true
		}
		observation := InboundMessage{Request: incoming}
		if mode == 2 {
			return !client.enqueueInbound(observation, ErrRequestQueue)
		}
		if mode == 0 {
			if len(client.backlog) >= client.queueCapacity {
				client.closeWith(ErrRequestQueue)
				return true
			}
			client.backlog = append(client.backlog, observation)
			return false
		}
		select {
		case client.requests <- incoming:
			return false
		default:
			client.closeWith(ErrRequestQueue)
			return true
		}
	case MessageNotification:
		value, err := native.DecodeNotification(message.Method, message.Params)
		if err != nil {
			client.closeWith(err)
			return true
		}
		notification := NotificationMessage{Method: message.Method, Params: cloneRaw(message.Params), Value: value}
		observation := InboundMessage{Notification: &notification}
		if mode == 2 {
			return !client.enqueueInbound(observation, ErrNotificationQueue)
		}
		if mode == 0 {
			if len(client.backlog) >= client.queueCapacity {
				client.closeWith(ErrNotificationQueue)
				return true
			}
			client.backlog = append(client.backlog, observation)
			return false
		}
		select {
		case client.notifications <- notification:
			return false
		default:
			client.closeWith(ErrNotificationQueue)
			return true
		}
	default:
		return false
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
	case <-client.done:
	}
	return true
}

func (client *Client) deliver(id RequestID, outcome callResult) bool {
	client.mu.Lock()
	response, exists := client.pending[id]
	if exists {
		delete(client.pending, id)
	}
	closed := client.closed
	client.mu.Unlock()
	if exists {
		response <- outcome
		return false
	}
	if closed {

		return true
	}

	client.closeWith(fmt.Errorf("%w: %s", ErrResponseNotFound, id))
	return true
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
	var pending map[RequestID]chan callResult
	if !deferToReader {
		pending = client.retirePendingLocked()
	}
	client.mu.Unlock()

	client.closeTransport()
	client.failPending(pending, reason)
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

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
	ErrClosed                 = errors.New("codex app-server rpc: client closed")
	ErrResponseNotFound       = errors.New("codex app-server rpc: response id is not pending")
	ErrInboundQueue           = errors.New("codex app-server rpc: inbound queue is full")
	ErrReverseRequestResolved = errors.New("codex app-server rpc: reverse request already resolved")
)

type RemoteError struct {
	ID     RequestID
	Object ErrorObject
}

func (err *RemoteError) Error() string {
	return fmt.Sprintf("codex app-server rpc error %d for request %s: %s", err.Object.Code, err.ID, err.Object.Message)
}

type IncomingRequest struct {
	ID     RequestID
	Method string
	Params json.RawMessage
	Trace  json.RawMessage
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
	return nil
}

type NotificationMessage struct {
	Method string
	Params json.RawMessage
}

type InboundMessage struct {
	Request      *IncomingRequest
	Notification *NotificationMessage
}

type callResult struct {
	result json.RawMessage
	err    error
}

type writeRequest struct {
	ctx     context.Context
	message Message
	result  chan error
	started chan struct{}
	encoded chan struct{}
}

type Client struct {
	decoder           *Decoder
	encoder           *Encoder
	closer            io.Closer
	closerOnce        sync.Once
	strictResponseIDs bool

	mu       sync.Mutex
	pending  map[RequestID]chan callResult
	closed   bool
	decoding bool
	routing  bool
	err      error

	nextID      atomic.Int64
	writes      chan writeRequest
	inbound     chan InboundMessage
	diagnostics chan error
	done        chan struct{}
	readDone    chan struct{}
}

type ClientOptions struct {
	FrameLimit        int
	QueueCapacity     int
	FirstRequestID    int64
	CloseReadWriter   io.Closer
	StrictResponseIDs bool
}

func NewClient(reader io.Reader, writer io.Writer, options ClientOptions) *Client {
	capacity := options.QueueCapacity
	if capacity <= 0 {
		capacity = 64
	}
	client := &Client{
		decoder:           NewDecoder(reader, options.FrameLimit),
		encoder:           NewEncoder(writer),
		closer:            options.CloseReadWriter,
		strictResponseIDs: options.StrictResponseIDs,
		pending:           make(map[RequestID]chan callResult),
		writes:            make(chan writeRequest),
		inbound:           make(chan InboundMessage, capacity),
		diagnostics:       make(chan error, capacity),
		done:              make(chan struct{}),
		readDone:          make(chan struct{}),
	}
	client.nextID.Store(options.FirstRequestID)
	go client.writeLoop()
	go client.readLoop()
	return client
}

func (client *Client) Inbound() <-chan InboundMessage { return client.inbound }
func (client *Client) Diagnostics() <-chan error      { return client.diagnostics }
func (client *Client) Done() <-chan struct{}          { return client.done }

func (client *Client) ReadDone() <-chan struct{} { return client.readDone }

func (client *Client) Err() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.err
}

func (client *Client) Call(ctx context.Context, method string, params any, result any) error {
	if method == "" {
		return ErrInvalidMessage
	}
	id := IntegerID(client.nextID.Add(1))
	paramsJSON, err := marshalOptional(params)
	if err != nil {
		return err
	}
	response := make(chan callResult, 1)
	client.mu.Lock()
	if client.closed {
		err := client.err
		client.mu.Unlock()
		if err != nil {
			return err
		}
		return ErrClosed
	}
	client.pending[id] = response
	client.mu.Unlock()

	settle := func(outcome callResult) error {
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
	if err := client.write(ctx, Request(id, method, paramsJSON)); err != nil {
		select {
		case <-client.done:

			select {
			case outcome := <-response:
				return settle(outcome)
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
		return settle(outcome)
	case <-ctx.Done():
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

	client.shutdown(reason)
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
		client.mu.Unlock()
		if err != nil {
			client.shutdown(err)
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

func (client *Client) route(message Message) bool {
	switch message.Kind {
	case MessageResponse:
		if !client.deliver(message.ID, callResult{result: cloneRaw(message.Result)}) {
			return client.unmatched(message.ID)
		}
	case MessageError:
		if !client.deliver(message.ID, callResult{err: &RemoteError{ID: message.ID, Object: *message.Error}}) {
			return client.unmatched(message.ID)
		}
	case MessageRequest:
		incoming := &IncomingRequest{ID: message.ID, Method: message.Method, Params: cloneRaw(message.Params), Trace: cloneRaw(message.Trace), client: client}
		return !client.enqueue(InboundMessage{Request: incoming})
	case MessageNotification:
		notification := NotificationMessage{Method: message.Method, Params: cloneRaw(message.Params)}
		return !client.enqueue(InboundMessage{Notification: &notification})
	}
	return false
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

func (client *Client) enqueue(message InboundMessage) bool {
	select {
	case client.inbound <- message:
		return true
	default:
		client.shutdown(ErrInboundQueue)
		return false
	}
}

func (client *Client) unmatched(id RequestID) bool {
	client.mu.Lock()
	closed := client.closed
	client.mu.Unlock()
	if closed {

		return true
	}
	err := fmt.Errorf("%w: %s", ErrResponseNotFound, id)
	if client.strictResponseIDs {
		client.shutdown(err)
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
	request := writeRequest{ctx: ctx, message: message, result: make(chan error, 1), started: make(chan struct{}), encoded: make(chan struct{})}
	select {
	case client.writes <- request:
	case <-ctx.Done():
		return ctx.Err()
	case <-client.done:
		return client.closeError()
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

				client.shutdown(err)
			}
			request.result <- err
			if err != nil {
				return
			}
		case <-client.done:
			return
		}
	}
}

func (client *Client) closeError() error {
	if err := client.Err(); err != nil {
		return err
	}
	return ErrClosed
}

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

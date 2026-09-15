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

// InboundMessage is one server-to-client frame in wire order. Exactly one
// field is set. Reverse requests and notifications share a single queue so a
// consumer observes them in the order the reader decoded them. Separate
// queues would let a consumer selecting across both handle a later request
// before an earlier notification, such as a turn's requestUserInput before
// its turn/started.
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
}

type Client struct {
	decoder           *Decoder
	encoder           *Encoder
	closer            io.Closer
	strictResponseIDs bool

	mu      sync.Mutex
	pending map[RequestID]chan callResult
	closed  bool
	routing bool // the reader holds a decoded frame it has not finished routing
	err     error

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

// NewClient starts dedicated reader and writer goroutines. Callers that need
// Close or call cancellation to interrupt blocked I/O must provide
// CloseReadWriter; generic io.Reader/io.Writer values have no interrupt primitive.
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

// Inbound is the ordered stream of reverse requests and notifications. The
// reader goroutine is its sole producer and enqueues frames as it decodes
// them, so a consumer that reads it sequentially sees wire order.
func (client *Client) Inbound() <-chan InboundMessage { return client.inbound }
func (client *Client) Diagnostics() <-chan error      { return client.diagnostics }
func (client *Client) Done() <-chan struct{}          { return client.done }

// ReadDone closes once the reader goroutine has stopped, after every frame
// already buffered on the input has been decoded and delivered. A process
// owner must wait for it before reaping the child or failing pending calls.
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

	if err := client.write(ctx, Request(id, method, paramsJSON)); err != nil {
		client.removePending(id)
		return err
	}
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
	select {
	case outcome := <-response:
		return settle(outcome)
	case <-ctx.Done():
		client.removePending(id)
		client.closeWith(ctx.Err())
		return ctx.Err()
	}
	// There is deliberately no done case. shutdown settles every registered
	// call through its channel — at once, or as soon as the reader has routed
	// the frame it held when the transport died — so waiting here is what
	// lets a response parsed off the ordered stream before the death win
	// over it.
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
	// Record the caller's reason before closing the transport. Closing the
	// closer unblocks the read loop's Decode, which reports the resulting
	// "read/write on closed pipe" through shutdown; because shutdown is
	// first-wins, closing first would let that incidental error overwrite the
	// intended reason (cancellation, ErrClosed, or a shutdown timeout).
	client.shutdown(reason)
	if client.closer != nil {
		_ = client.closer.Close()
	}
}

func (client *Client) readLoop() {
	defer close(client.readDone)
	for {
		message, err := client.decoder.Decode()
		if err != nil {
			client.shutdown(err)
			return
		}
		// The frame is in hand from the moment Decode returns it: a shutdown
		// that lands while it is being routed defers retiring the pending map
		// to settleRouted, so the response it may carry is delivered instead
		// of dropped.
		client.mu.Lock()
		client.routing = true
		client.mu.Unlock()
		stop := client.route(message)
		client.settleRouted()
		if stop {
			return
		}
	}
}

// route dispatches one decoded frame and reports whether the reader must stop.
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

// settleRouted releases the frame the reader had in hand and, when the client
// closed while it was being routed, fails the pending calls shutdown left to it.
func (client *Client) settleRouted() {
	client.mu.Lock()
	client.routing = false
	pending := client.retirePendingLocked()
	client.mu.Unlock()
	client.failPending(pending, client.closeError())
}

// retirePendingLocked takes the pending map once the client is closed. The
// caller holds client.mu.
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

// enqueue hands one inbound frame to the consumer without blocking the
// reader. A full queue is terminal: the consumer is a whole queue behind, and
// blocking here would also stall response delivery for pending calls.
func (client *Client) enqueue(message InboundMessage) bool {
	select {
	case client.inbound <- message:
		return true
	default:
		client.shutdown(ErrInboundQueue)
		return false
	}
}

// unmatched reports whether the reader must stop after a response whose id is
// no longer pending.
func (client *Client) unmatched(id RequestID) bool {
	client.mu.Lock()
	closed := client.closed
	client.mu.Unlock()
	if closed {
		// A concurrent shutdown, or a call that gave up on its context,
		// retired the id. The response is correlated evidence that lost the
		// race with the transport's death, not an unmatched correlation, so
		// it must not raise ErrResponseNotFound over the real cause.
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
	request := writeRequest{ctx: ctx, message: message, result: make(chan error, 1), started: make(chan struct{})}
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
			// The pump took the frame before the close and always hands back
			// the result of a frame it has started encoding, so the bytes may
			// already be on the wire: settle on that result, not on the death.
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
				client.shutdown(err)
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
	// A response parsed off the ordered stream before the transport died must
	// win over the death. While the reader holds a decoded frame, retiring the
	// pending map is deferred to settleRouted so that frame is delivered
	// first; otherwise nothing decoded is outstanding and the calls fail here.
	// Either way the settlement never waits for the reader to stop, which a
	// client without a CloseReadWriter has no way to force.
	var pending map[RequestID]chan callResult
	if !client.routing {
		pending = client.retirePendingLocked()
	}
	client.mu.Unlock()
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

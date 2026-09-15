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
	closerOnce        sync.Once
	strictResponseIDs bool
	queueCapacity     int

	mu       sync.Mutex
	routeMu  sync.Mutex
	pending  map[RequestID]chan callResult
	incoming map[RequestID]*IncomingRequest
	backlog  []InboundMessage
	closed   bool
	decoding bool // the reader is inside Decode
	routing  bool // the reader holds a decoded frame it has not finished routing
	encoding bool // the pump is inside Encode
	err      error

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
		closer: options.CloseReadWriter, strictResponseIDs: options.StrictResponseIDs, queueCapacity: capacity,
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

// ReadDone closes once the reader goroutine has stopped, after every frame
// already buffered on the input has been decoded and routed. A process owner
// must wait for it before reaping the child or closing the pipes, otherwise a
// response written immediately before exit is lost to the closed read end.
func (client *Client) ReadDone() <-chan struct{} { return client.readDone }

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
		select {
		case <-client.done:
			// The client retired while the frame was in the pump, so whether
			// the frame reached the wire is unknowable here — but the response
			// channel is authoritative: shutdown settles every registered call
			// through it, and a response the reader parsed off the ordered
			// stream before the death wins over the write's verdict.
			select {
			case outcome := <-response:
				report(outcome.err)
				return decodeCallResult(method, outcome, result)
			case <-ctx.Done():
				client.removePending(id)
				report(ctx.Err())
				return ctx.Err()
			}
		default:
			client.removePending(id)
			report(err)
			return err
		}
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
	client.closeOnce.Do(func() {
		client.closeTransport()
		client.shutdown(reason)
	})
}

// closeTransport closes the supplied CloseReadWriter at most once per client:
// io.Closer does not promise idempotence, and a retirement can reach the
// closer both from closeWith and from a direct shutdown.
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
		// Flag the reader's state before every blocking step, for shutdown: a
		// reader parked in Decode can be woken only through the closer, and a
		// reader holding a decoded frame always routes it to completion.
		client.mu.Lock()
		client.decoding = true
		client.mu.Unlock()
		message, err := client.decoder.Decode()
		client.mu.Lock()
		client.decoding = false
		client.routing = err == nil
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

// settleReader releases whatever the reader had in hand and, when the client
// closed while shutdown was deferring to the reader, fails the pending calls it
// left behind.
func (client *Client) settleReader() {
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

func (client *Client) route(message Message) bool {
	mode := client.routeMode.Load()
	switch message.Kind {
	case MessageResponse:
		if mode == 2 && !client.barrier() {
			return true
		}
		return !client.deliver(message.ID, callResult{result: cloneRaw(message.Result)}) && client.unmatched(message.ID)
	case MessageError:
		if mode == 2 && !client.barrier() {
			return true
		}
		return !client.deliver(message.ID, callResult{err: &RemoteError{ID: message.ID, Object: *message.Error}}) && client.unmatched(message.ID)
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
		notification := NotificationMessage{Method: message.Method, Params: cloneRaw(message.Params)}
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

// barrier orders a response behind every wire-earlier observation and reports
// whether the response may be delivered. Delivery is refused only when the
// barrier could not be enqueued: the client is retiring on queue overflow with
// earlier observations still unreduced, so the response must not overtake
// them. A barrier that was enqueued but never acknowledged because the
// transport retired first still releases the response — the frame was decoded
// from the wire, and there are no later frames to order against.
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
		case <-request.started:
			// The pump took the frame before the close, so the bytes may
			// already be on the wire. Its result is waited for whenever it is
			// sure to arrive: the pump is past Encode, or the closer will
			// unblock an Encode still in progress. Without a closer a blocked
			// Encode could hold the caller forever, so the death is reported.
			if !client.pumpSettles() {
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

// pumpSettles reports whether a started frame's result is sure to arrive: the
// pump is past Encode, or the closer will unblock an Encode in progress.
func (client *Client) pumpSettles() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return !client.encoding || client.closer != nil
}

func (client *Client) writeLoop() {
	for {
		select {
		case request := <-client.writes:
			if err := request.ctx.Err(); err != nil {
				request.result <- err
				continue
			}
			// encoding is raised before started, so a caller that sees started
			// but not encoding knows the frame is past Encode and its result
			// is imminent.
			client.mu.Lock()
			client.encoding = true
			client.mu.Unlock()
			close(request.started)
			err := client.encoder.Encode(request.message)
			client.mu.Lock()
			client.encoding = false
			client.mu.Unlock()
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
	close(client.done)
	// A response parsed off the ordered stream before the transport died must
	// win over the death, so the calls are settled by the reader whenever
	// shutdown can force it to a settle point: a frame in hand is always
	// routed to completion (nothing in route blocks once done is closed), and
	// a reader parked in Decode is woken by the closer, which shutdown closes
	// below. Without a closer nothing can wake a parked reader, so the calls
	// fail here instead; the only window left is a frame decoded in the
	// instant before this lock was taken, and no client without a closer can
	// close it.
	deferToReader := client.routing || (client.decoding && client.closer != nil)
	var pending map[RequestID]chan callResult
	if !deferToReader {
		pending = client.retirePendingLocked()
	}
	client.mu.Unlock()
	// Done implies the transport is closed: that is what wakes a reader
	// parked in Decode or a pump blocked in Encode.
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

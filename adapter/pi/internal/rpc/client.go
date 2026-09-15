package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/lsm/open-agent-protocol/adapter/pi/internal/native"
)

var (
	ErrClosed             = errors.New("pi rpc: client closed")
	ErrDuplicateRequestID = errors.New("pi rpc: request id is already active")
	ErrResponseNotFound   = errors.New("pi rpc: response id is not pending")
	ErrResponseCommand    = errors.New("pi rpc: response command does not match request")
	ErrInboundQueue       = errors.New("pi rpc: inbound queue is full")
	ErrWriteQueue         = errors.New("pi rpc: write queue is full")
)

type RemoteError struct {
	ID      string
	Command native.CommandType
	Message string
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("pi rpc error for %s (%s): %s", e.ID, e.Command, e.Message)
}

// Inbound preserves stdout receive order across native events, extension UI
// requests, and call responses. A Barrier must be acknowledged only after all
// preceding observations have been semantically reduced. The corresponding
// response is not delivered to Call until then.
type Inbound struct {
	Event            *native.Event
	ExtensionRequest *native.ExtensionUIRequest
	Barrier          chan struct{}
}

type ClientOptions struct {
	FrameLimit         int
	QueueCapacity      int
	WriteQueueCapacity int
	FirstRequestID     uint64
	CloseReadWriter    io.Closer
}

type callResult struct {
	response native.Response
	err      error
}
type pendingCall struct {
	command native.CommandType
	result  chan callResult
}
type writeRequest struct {
	ctx     context.Context
	value   any
	result  chan error
	started chan struct{}
	encoded chan struct{} // closed when this frame leaves Encode
}

type Client struct {
	decoder    *Decoder
	encoder    *Encoder
	closer     io.Closer
	closerOnce sync.Once
	mu         sync.Mutex
	pending    map[string]pendingCall
	sent       map[string]struct{}
	closed     bool
	decoding   bool // the reader is inside Decode
	routing    bool // the reader holds a decoded frame it has not finished routing
	err        error
	nextID     atomic.Uint64
	writes     chan writeRequest
	inbound    chan Inbound
	done       chan struct{}
	readDone   chan struct{}
	closeOnce  sync.Once
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
		decoder: NewDecoder(reader, options.FrameLimit), encoder: NewEncoder(writer, options.FrameLimit), closer: options.CloseReadWriter,
		pending: map[string]pendingCall{}, sent: map[string]struct{}{}, writes: make(chan writeRequest, writeCapacity), inbound: make(chan Inbound, capacity), done: make(chan struct{}), readDone: make(chan struct{}),
	}
	client.nextID.Store(options.FirstRequestID)
	go client.writeLoop()
	go client.readLoop()
	return client
}

func (c *Client) Inbound() <-chan Inbound { return c.inbound }
func (c *Client) Done() <-chan struct{}   { return c.done }
func (c *Client) Err() error              { c.mu.Lock(); defer c.mu.Unlock(); return c.err }

// ReadDone closes once the reader goroutine has stopped, after every frame
// already buffered on the input has been decoded and routed. A process owner
// must wait for it before reaping the child: Cmd.Wait closes the stdout pipe,
// so a response written immediately before exit would otherwise be lost to a
// closed read end and reported as a process-exit failure.
func (c *Client) ReadDone() <-chan struct{} { return c.readDone }

func (c *Client) Call(ctx context.Context, command native.Command, result any) error {
	if command.ID == "" {
		command.ID = "req_" + strconv.FormatUint(c.nextID.Add(1), 10)
	}
	if err := command.Validate(); err != nil {
		return err
	}
	response := make(chan callResult, 1)
	c.mu.Lock()
	if c.closed {
		err := firstError(c.err, ErrClosed)
		c.mu.Unlock()
		return err
	}
	if _, exists := c.sent[command.ID]; exists {
		c.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrDuplicateRequestID, command.ID)
	}
	c.sent[command.ID] = struct{}{}
	c.pending[command.ID] = pendingCall{command: command.Type, result: response}
	c.mu.Unlock()
	settle := func(outcome callResult) error {
		if outcome.err != nil {
			return outcome.err
		}
		if !outcome.response.Success {
			return &RemoteError{ID: outcome.response.ID, Command: outcome.response.Command, Message: outcome.response.Error}
		}
		if result == nil || len(outcome.response.Data) == 0 {
			return nil
		}
		if err := json.Unmarshal(outcome.response.Data, result); err != nil {
			return fmt.Errorf("decode %s response: %w", command.Type, err)
		}
		return nil
	}
	if err := c.write(ctx, command); err != nil {
		select {
		case <-c.done:
			// The client retired while the frame was in the pump, so whether
			// the frame reached the wire is unknowable here — but the response
			// channel is authoritative: shutdown settles every registered call
			// through it, and a response the reader parsed off the ordered
			// stream before the death wins over the write's verdict.
			select {
			case outcome := <-response:
				return settle(outcome)
			case <-ctx.Done():
				c.removePending(command.ID)
				return ctx.Err()
			}
		default:
			c.removePending(command.ID)
			return err
		}
	}
	select {
	case outcome := <-response:
		return settle(outcome)
	case <-ctx.Done():
		c.removePending(command.ID)
		c.closeWith(ctx.Err())
		return ctx.Err()
	}
	// There is deliberately no done case. shutdown settles every registered
	// call through its channel — at once, or as soon as the reader has routed
	// the frame it held when the transport died — so waiting here is what
	// lets a response parsed off the ordered stream before the death win
	// over it.
}

func (c *Client) Respond(ctx context.Context, response native.ExtensionUIResponse) error {
	return c.write(ctx, response)
}
func (c *Client) Close() error { c.closeWith(ErrClosed); return nil }
func (c *Client) closeWith(reason error) {
	c.closeOnce.Do(func() {
		c.closeTransport()
		c.shutdown(reason)
	})
}
func (c *Client) shutdown(reason error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.err = reason
	close(c.done)
	// A response parsed off the ordered stream before the transport died must
	// win over the death, so the calls are settled by the reader whenever
	// shutdown can force it to a settle point: a frame in hand is always
	// routed to completion (nothing in route blocks once done is closed), and
	// a reader parked in Decode is woken by the closer, which shutdown closes
	// below. Without a closer nothing can wake a parked reader, so the calls
	// fail here instead; the only window left is a frame decoded in the
	// instant before this lock was taken, and no client without a closer can
	// close it.
	deferToReader := c.routing || (c.decoding && c.closer != nil)
	var pending map[string]pendingCall
	if !deferToReader {
		pending = c.retirePendingLocked()
	}
	c.mu.Unlock()
	// Done implies the transport is closed: that is what wakes a reader
	// parked in Decode or a pump blocked in Encode.
	c.closeTransport()
	c.failPending(pending, reason)
}
func (c *Client) closeError() error { return firstError(c.Err(), ErrClosed) }
func firstError(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

// closeTransport closes the supplied CloseReadWriter at most once per client:
// io.Closer does not promise idempotence, and a retirement can reach the
// closer both from closeWith and from a direct shutdown.
func (c *Client) closeTransport() {
	c.closerOnce.Do(func() {
		if c.closer != nil {
			_ = c.closer.Close()
		}
	})
}

func (c *Client) readLoop() {
	defer close(c.readDone)
	for {
		// Flag the reader's state before every blocking step, for shutdown: a
		// reader parked in Decode can be woken only through the closer, and a
		// reader holding a decoded frame always routes it to completion.
		c.mu.Lock()
		c.decoding = true
		c.mu.Unlock()
		frame, err := c.decoder.Decode()
		c.mu.Lock()
		c.decoding = false
		c.routing = err == nil
		c.mu.Unlock()
		if err != nil {
			c.closeWith(err)
			c.settleReader()
			return
		}
		err = c.route(frame)
		if err != nil {
			c.closeWith(err)
		}
		c.settleReader()
		if err != nil {
			return
		}
	}
}

// settleReader releases whatever the reader had in hand and, when the client
// closed while shutdown was deferring to the reader, fails the pending calls it
// left behind.
func (c *Client) settleReader() {
	c.mu.Lock()
	c.routing = false
	pending := c.retirePendingLocked()
	c.mu.Unlock()
	c.failPending(pending, c.closeError())
}

// retirePendingLocked takes the pending map once the client is closed. The
// caller holds c.mu.
func (c *Client) retirePendingLocked() map[string]pendingCall {
	if !c.closed {
		return nil
	}
	pending := c.pending
	c.pending = map[string]pendingCall{}
	return pending
}

func (c *Client) failPending(pending map[string]pendingCall, reason error) {
	for _, call := range pending {
		call.result <- callResult{err: reason}
	}
}
func (c *Client) route(frame Frame) error {
	switch {
	case frame.Event != nil:
		return c.enqueue(Inbound{Event: frame.Event})
	case frame.ExtensionRequest != nil:
		return c.enqueue(Inbound{ExtensionRequest: frame.ExtensionRequest})
	case frame.Response != nil:
		response := *frame.Response
		if response.ID == "" {
			return fmt.Errorf("%w: empty", ErrResponseNotFound)
		}
		c.mu.Lock()
		pending, exists := c.pending[response.ID]
		if exists {
			delete(c.pending, response.ID)
		}
		closed := c.closed
		c.mu.Unlock()
		if !exists {
			if closed {
				// A concurrent shutdown, or a call that gave up on its
				// context, retired the id. The response is correlated
				// evidence that lost the race with the transport's death,
				// not an unmatched correlation, so it must not raise
				// ErrResponseNotFound over the real cause.
				return c.closeError()
			}
			return fmt.Errorf("%w: %s", ErrResponseNotFound, response.ID)
		}
		if pending.command != response.Command {
			err := fmt.Errorf("%w: got %s want %s", ErrResponseCommand, response.Command, pending.command)
			pending.result <- callResult{err: err}
			return err
		}
		ack := make(chan struct{})
		if err := c.enqueue(Inbound{Barrier: ack}); err != nil {
			pending.result <- callResult{err: err}
			return err
		}
		closing := false
		select {
		case <-ack:
		case <-c.done:
			// The transport retired before the barrier was acknowledged, but
			// the frame was still decoded from the wire — deliver the
			// evidence rather than dropping it; there are no later frames to
			// order against.
			closing = true
		}
		pending.result <- callResult{response: response}
		if closing {
			return c.closeError()
		}
		return nil
	default:
		return ErrInvalidFrame
	}
}
func (c *Client) enqueue(message Inbound) error {
	select {
	case c.inbound <- message:
		return nil
	default:
		return ErrInboundQueue
	}
}
func (c *Client) removePending(id string) { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }

func (c *Client) write(ctx context.Context, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	request := writeRequest{ctx: ctx, value: value, result: make(chan error, 1), started: make(chan struct{}), encoded: make(chan struct{})}
	select {
	case c.writes <- request:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.closeError()
	default:
		return ErrWriteQueue
	}
	select {
	case err := <-request.result:
		return err
	case <-ctx.Done():
		c.closeWith(ctx.Err())
		return ctx.Err()
	case <-c.done:
		select {
		case err := <-request.result:
			return err
		case <-request.started:
			// The pump took the frame before the close, so the bytes may
			// already be on the wire. Its result is waited for whenever it is
			// sure to arrive: the pump is past Encode, or the closer will
			// unblock an Encode still in progress. Without a closer a blocked
			// Encode could hold the caller forever, so the death is reported.
			if !c.pumpSettles(request) {
				return c.closeError()
			}
			select {
			case err := <-request.result:
				return err
			case <-ctx.Done():
				c.closeWith(ctx.Err())
				return ctx.Err()
			}
		default:
			return c.closeError()
		}
	}
}

// pumpSettles reports whether this frame's result is sure to arrive. The
// question is per request, not per pump: a later frame blocked in Encode says
// nothing about a frame that already left it.
func (c *Client) pumpSettles(request writeRequest) bool {
	select {
	case <-request.encoded:
		// Past Encode: the pump publishes this frame's result next.
		return true
	default:
		// Still inside this frame's Encode, which only the closer can
		// interrupt. closer is fixed at construction, so reading it is safe.
		return c.closer != nil
	}
}

func (c *Client) writeLoop() {
	for {
		select {
		case request := <-c.writes:
			if err := request.ctx.Err(); err != nil {
				request.result <- err
				continue
			}
			close(request.started)
			err := c.encoder.Encode(request.value)
			// Signal this frame's completion before the retirement below, so
			// its caller can tell "past Encode" from "blocked in Encode".
			close(request.encoded)
			if err != nil {
				// Retire before publishing the failure. A caller that sees the
				// write error with done already closed settles on its response
				// channel, so a reply the peer managed to send for the frame
				// is not discarded along with the pending id.
				c.closeWith(err)
			}
			request.result <- err
			if err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

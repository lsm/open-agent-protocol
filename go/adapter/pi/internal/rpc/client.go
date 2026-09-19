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

	"github.com/lsm/open-agent-protocol/go/adapter/pi/internal/native"
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
	encoded chan struct{}
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
	decoding   bool
	routing    bool
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

	deferToReader := c.routing || (c.decoding && c.closer != nil)
	var pending map[string]pendingCall
	if !deferToReader {
		pending = c.retirePendingLocked()
	}
	c.mu.Unlock()

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

func (c *Client) settleReader() {
	c.mu.Lock()
	c.routing = false
	pending := c.retirePendingLocked()
	c.mu.Unlock()
	c.failPending(pending, c.closeError())
}

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

func (c *Client) pumpSettles(request writeRequest) bool {
	select {
	case <-request.encoded:

		return true
	default:

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

			close(request.encoded)
			if err != nil {

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

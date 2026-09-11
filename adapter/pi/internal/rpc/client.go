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
	ctx    context.Context
	value  any
	result chan error
}

type Client struct {
	decoder   *Decoder
	encoder   *Encoder
	closer    io.Closer
	mu        sync.Mutex
	pending   map[string]pendingCall
	sent      map[string]struct{}
	closed    bool
	err       error
	nextID    atomic.Uint64
	writes    chan writeRequest
	inbound   chan Inbound
	done      chan struct{}
	readDone  chan struct{}
	closeOnce sync.Once
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
	if err := c.write(ctx, command); err != nil {
		c.removePending(command.ID)
		return err
	}
	select {
	case outcome := <-response:
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
	case <-ctx.Done():
		c.removePending(command.ID)
		c.closeWith(ctx.Err())
		return ctx.Err()
	case <-c.done:
		select {
		case outcome := <-response:
			return outcome.err
		default:
			return c.closeError()
		}
	}
}

func (c *Client) Respond(ctx context.Context, response native.ExtensionUIResponse) error {
	return c.write(ctx, response)
}
func (c *Client) Close() error { c.closeWith(ErrClosed); return nil }
func (c *Client) closeWith(reason error) {
	c.closeOnce.Do(func() {
		if c.closer != nil {
			_ = c.closer.Close()
		}
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
	pending := c.pending
	c.pending = map[string]pendingCall{}
	close(c.done)
	c.mu.Unlock()
	for _, call := range pending {
		call.result <- callResult{err: reason}
	}
}
func (c *Client) closeError() error { return firstError(c.Err(), ErrClosed) }
func firstError(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

func (c *Client) readLoop() {
	defer close(c.readDone)
	for {
		frame, err := c.decoder.Decode()
		if err != nil {
			c.closeWith(err)
			return
		}
		err = c.route(frame)
		if err != nil {
			c.closeWith(err)
			return
		}
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
		c.mu.Unlock()
		if !exists {
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
		select {
		case <-ack:
		case <-c.done:
			return c.closeError()
		}
		pending.result <- callResult{response: response}
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
	request := writeRequest{ctx: ctx, value: value, result: make(chan error, 1)}
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
		default:
			return c.closeError()
		}
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
			err := c.encoder.Encode(request.value)
			request.result <- err
			if err != nil {
				c.closeWith(err)
				return
			}
		case <-c.done:
			return
		}
	}
}

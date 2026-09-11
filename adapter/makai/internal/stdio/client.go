package stdio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

import "github.com/lsm/open-agent-protocol/adapter/makai/internal/native"

var (
	ErrClosed             = errors.New("makai stdio: client closed")
	ErrDuplicateMessageID = errors.New("makai stdio: duplicate message_id")
	ErrReplyNotPending    = errors.New("makai stdio: in_reply_to is not pending")
	ErrReplySession       = errors.New("makai stdio: reply session does not match request")
	ErrUnexpectedReply    = errors.New("makai stdio: unexpected reply type")
	ErrSequence           = errors.New("makai stdio: invalid incoming sequence")
	ErrInboundQueue       = errors.New("makai stdio: inbound queue is full")
	ErrWriteQueue         = errors.New("makai stdio: write queue is full")
)

type Inbound struct {
	Envelope *native.Envelope
	Barrier  chan struct{}
}
type ClientOptions struct {
	FrameLimit         int
	QueueCapacity      int
	WriteQueueCapacity int
	CloseReadWriter    io.Closer
}
type callResult struct {
	env native.Envelope
	err error
}
type pendingCall struct {
	session native.SessionID
	accept  map[native.Type]bool
	result  chan callResult
}
type writeRequest struct {
	ctx    context.Context
	env    native.Envelope
	result chan error
}

type Client struct {
	decoder     *Decoder
	encoder     *Encoder
	closer      io.Closer
	mu          sync.Mutex
	pending     map[native.MessageID]pendingCall
	seen        map[native.MessageID]struct{}
	sent        map[native.MessageID]native.Envelope
	sequences   map[native.SessionID]uint64
	closed      bool
	err         error
	writes      chan writeRequest
	inbound     chan Inbound
	diagnostics chan error
	done        chan struct{}
	readDone    chan struct{}
	closeOnce   sync.Once
}

func NewClient(reader io.Reader, writer io.Writer, options ClientOptions) *Client {
	return newClient(NewDecoder(reader, options.FrameLimit), writer, options)
}
func newClient(decoder *Decoder, writer io.Writer, options ClientOptions) *Client {
	cap := options.QueueCapacity
	if cap <= 0 {
		cap = 64
	}
	wcap := options.WriteQueueCapacity
	if wcap <= 0 {
		wcap = cap
	}
	c := &Client{decoder: decoder, encoder: NewEncoder(writer, options.FrameLimit), closer: options.CloseReadWriter, pending: map[native.MessageID]pendingCall{}, seen: map[native.MessageID]struct{}{}, sent: map[native.MessageID]native.Envelope{}, sequences: map[native.SessionID]uint64{}, writes: make(chan writeRequest, wcap), inbound: make(chan Inbound, cap), diagnostics: make(chan error, cap), done: make(chan struct{}), readDone: make(chan struct{})}
	go c.writeLoop()
	go c.readLoop()
	return c
}
func (c *Client) Inbound() <-chan Inbound   { return c.inbound }
func (c *Client) Diagnostics() <-chan error { return c.diagnostics }
func (c *Client) Done() <-chan struct{}     { return c.done }
func (c *Client) Err() error                { c.mu.Lock(); defer c.mu.Unlock(); return c.err }

// ReadDone closes once the reader goroutine has stopped, after every frame
// already buffered on the input has been decoded and routed. A process owner
// must wait for it before reaping the child: Cmd.Wait closes the stdout pipe,
// so a response written immediately before exit would otherwise be lost to a
// closed read end and reported as a process-exit failure.
func (c *Client) ReadDone() <-chan struct{} { return c.readDone }

func (c *Client) Call(ctx context.Context, request native.Envelope, accepted ...native.Type) (native.Envelope, error) {
	if len(accepted) == 0 {
		return native.Envelope{}, ErrUnexpectedReply
	}
	accept := make(map[native.Type]bool, len(accepted))
	for _, t := range accepted {
		accept[t] = true
	}
	response := make(chan callResult, 1)
	c.mu.Lock()
	if c.closed {
		err := firstError(c.err, ErrClosed)
		c.mu.Unlock()
		return native.Envelope{}, err
	}
	if _, ok := c.sent[request.MessageID]; ok {
		c.mu.Unlock()
		return native.Envelope{}, fmt.Errorf("%w: %s", ErrDuplicateMessageID, request.MessageID)
	}
	c.sent[request.MessageID] = request
	c.pending[request.MessageID] = pendingCall{session: request.SessionID, accept: accept, result: response}
	c.mu.Unlock()
	if err := c.write(ctx, request); err != nil {
		c.removePending(request.MessageID)
		return native.Envelope{}, err
	}
	select {
	case result := <-response:
		return result.env, result.err
	case <-ctx.Done():
		c.removePending(request.MessageID)
		c.closeWith(ctx.Err())
		return native.Envelope{}, ctx.Err()
	case <-c.done:
		select {
		case result := <-response:
			return result.env, result.err
		default:
			return native.Envelope{}, c.closeError()
		}
	}
}

// Send reports success only after the complete JSONL frame has been written.
func (c *Client) Send(ctx context.Context, env native.Envelope) error {
	c.mu.Lock()
	if c.closed {
		err := firstError(c.err, ErrClosed)
		c.mu.Unlock()
		return err
	}
	if _, duplicate := c.sent[env.MessageID]; duplicate {
		c.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrDuplicateMessageID, env.MessageID)
	}
	c.sent[env.MessageID] = env
	c.mu.Unlock()
	return c.write(ctx, env)
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
	c.pending = map[native.MessageID]pendingCall{}
	close(c.done)
	c.mu.Unlock()
	for _, p := range pending {
		p.result <- callResult{err: reason}
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
		if frame.Ready != nil {
			c.closeWith(fmt.Errorf("%w: duplicate ready frame", ErrInvalidFrame))
			return
		}
		if err := c.route(*frame.Envelope); err != nil {
			c.closeWith(err)
			return
		}
	}
}
func (c *Client) route(env native.Envelope) error {
	c.mu.Lock()
	if _, ok := c.seen[env.MessageID]; ok {
		c.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrDuplicateMessageID, env.MessageID)
	}
	c.seen[env.MessageID] = struct{}{}
	if trackedSequence(env.Type) || (env.Type == native.TypeAgentError && env.InReplyTo == nil) {
		expected := c.sequences[env.SessionID] + 1
		if env.Sequence != expected {
			c.mu.Unlock()
			return fmt.Errorf("%w for %s: got %d want %d", ErrSequence, env.SessionID, env.Sequence, expected)
		}
		c.sequences[env.SessionID] = env.Sequence
	} else if env.Sequence == 0 && (env.Type != native.TypeAgentError || env.InReplyTo == nil) {
		c.mu.Unlock()
		return fmt.Errorf("%w for %s: unexpected zero", ErrSequence, env.SessionID)
	}
	var pending pendingCall
	var matched bool
	var correlatedObservation bool
	if env.InReplyTo != nil {
		pending, matched = c.pending[*env.InReplyTo]
		if matched {
			delete(c.pending, *env.InReplyTo)
		} else if request, sent := c.sent[*env.InReplyTo]; sent &&
			request.Type == native.TypeAgentMessage &&
			env.Type == native.TypeAgentError &&
			env.SessionID == request.SessionID {
			correlatedObservation = true
		}
	}
	c.mu.Unlock()
	if env.InReplyTo != nil {
		if correlatedObservation {
			if !c.enqueue(Inbound{Envelope: &env}) {
				return ErrInboundQueue
			}
			return nil
		}
		if !matched {
			return fmt.Errorf("%w: %s", ErrReplyNotPending, *env.InReplyTo)
		}
		if env.SessionID != pending.session && env.Type != native.TypeAgentStarted {
			pending.result <- callResult{err: ErrReplySession}
			return ErrReplySession
		}
		if !pending.accept[env.Type] {
			err := fmt.Errorf("%w: %s", ErrUnexpectedReply, env.Type)
			pending.result <- callResult{err: err}
			return err
		}
		ack := make(chan struct{})
		if !c.enqueue(Inbound{Barrier: ack}) {
			pending.result <- callResult{err: ErrInboundQueue}
			return ErrInboundQueue
		}
		select {
		case <-ack:
			pending.result <- callResult{env: env}
			return nil
		case <-c.done:
			return c.closeError()
		}
	}
	if !c.enqueue(Inbound{Envelope: &env}) {
		return ErrInboundQueue
	}
	return nil
}
func trackedSequence(typ native.Type) bool {
	switch typ {
	case native.TypeAgentStarted, native.TypeAgentEvent, native.TypeAgentResult,
		native.TypeAgentStopped, native.TypeToolExecute, native.TypeAck, native.TypeNack:
		return true
	default:
		return false
	}
}

func (c *Client) enqueue(msg Inbound) bool {
	select {
	case c.inbound <- msg:
		return true
	default:
		return false
	}
}
func (c *Client) removePending(id native.MessageID) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}
func (c *Client) write(ctx context.Context, env native.Envelope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	req := writeRequest{ctx: ctx, env: env, result: make(chan error, 1)}
	select {
	case c.writes <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.closeError()
	default:
		return ErrWriteQueue
	}
	select {
	case err := <-req.result:
		return err
	case <-ctx.Done():
		c.closeWith(ctx.Err())
		return ctx.Err()
	case <-c.done:
		select {
		case err := <-req.result:
			return err
		default:
			return c.closeError()
		}
	}
}
func (c *Client) writeLoop() {
	for {
		select {
		case req := <-c.writes:
			if err := req.ctx.Err(); err != nil {
				req.result <- err
				continue
			}
			err := c.encoder.Encode(req.env)
			req.result <- err
			if err != nil {
				c.closeWith(err)
				return
			}
		case <-c.done:
			return
		}
	}
}

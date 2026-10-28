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

	"github.com/lsm/open-agent-protocol/go/adapter/copilot/internal/native"
)

var ErrClosed = errors.New("Copilot RPC closed")
var ErrBackpressure = errors.New("Copilot RPC queue is full")

type Client struct {
	transport io.ReadWriteCloser
	codec     *Codec
	ids       atomic.Int64
	mu        sync.Mutex
	pending   map[string]chan native.Message
	notes     chan native.Message
	writes    chan []byte
	done      chan struct{}
	err       error
	once      sync.Once
	wg        sync.WaitGroup
}

type RemoteError struct {
	Code    int64
	Message string
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("Copilot RPC error %d: %s", e.Code, e.Message)
}

func NewClient(transport io.ReadWriteCloser, limit, capacity int) *Client {
	if capacity <= 0 {
		capacity = 64
	}
	c := &Client{transport: transport, codec: NewCodec(transport, transport, limit), pending: map[string]chan native.Message{}, notes: make(chan native.Message, capacity), writes: make(chan []byte, capacity), done: make(chan struct{})}
	c.wg.Add(2)
	go c.read()
	go c.write()
	return c
}

func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id := c.ids.Add(1)
	body, err := json.Marshal(struct {
		Version string `json:"jsonrpc"`
		ID      int64  `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{"2.0", id, method, params})
	if err != nil {
		return err
	}
	key := strconv.FormatInt(id, 10)
	replies := make(chan native.Message, 1)
	c.mu.Lock()
	c.pending[key] = replies
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, key); c.mu.Unlock() }()
	select {
	case c.writes <- body:
	case <-ctx.Done():
		c.fail(ctx.Err())
		return ctx.Err()
	case <-c.done:
		return c.Err()
	}
	select {
	case m := <-replies:
		if m.Kind == native.Failure {
			return &RemoteError{m.Code, m.Error}
		}
		if out != nil {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-ctx.Done():
		c.fail(ctx.Err())
		return ctx.Err()
	case <-c.done:
		return c.Err()
	}
}

func (c *Client) read() {
	defer c.wg.Done()
	for {
		body, err := c.codec.Read()
		if err != nil {
			c.fail(err)
			return
		}
		m, err := native.Parse(body)
		if err != nil {
			c.fail(err)
			return
		}
		switch m.Kind {
		case native.Response, native.Failure:
			c.mu.Lock()
			reply := c.pending[string(m.ID)]
			delete(c.pending, string(m.ID))
			c.mu.Unlock()
			if reply == nil {
				c.fail(native.ErrWire)
				return
			}
			reply <- m
		case native.Notification:
			select {
			case c.notes <- m:
			default:
				c.fail(ErrBackpressure)
				return
			}
		case native.Request:
			refusal, _ := json.Marshal(struct {
				Version string          `json:"jsonrpc"`
				ID      json.RawMessage `json:"id"`
				Error   any             `json:"error"`
			}{"2.0", m.ID, struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			}{-32601, "reverse requests are not enabled"}})
			select {
			case c.writes <- refusal:
			default:
				c.fail(ErrBackpressure)
				return
			}
		}
	}
}

func (c *Client) write() {
	defer c.wg.Done()
	for {
		select {
		case body := <-c.writes:
			if err := c.codec.Write(body); err != nil {
				c.fail(err)
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *Client) fail(err error) {
	c.once.Do(func() { c.mu.Lock(); c.err = err; c.mu.Unlock(); close(c.done); _ = c.transport.Close() })
}

func (c *Client) Notifications() <-chan native.Message { return c.notes }
func (c *Client) Done() <-chan struct{}                { return c.done }
func (c *Client) Err() error                           { c.mu.Lock(); defer c.mu.Unlock(); return c.err }
func (c *Client) Close() error                         { c.fail(ErrClosed); c.wg.Wait(); return nil }

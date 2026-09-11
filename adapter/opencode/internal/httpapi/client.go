// Package httpapi is the strict HTTP+SSE client for the pinned OpenCode
// server surface used by the OAP adapter.
package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/lsm/open-agent-protocol/adapter/opencode/internal/native"
)

// subscribeEstablishGrace bounds how long Subscribe waits for response headers
// before returning the subscription anyway. The pinned server defers
// session-scoped SSE response headers until the stream carries an event, so a
// fresh, silent session would otherwise block the caller until its context
// expires. Immediate failures (HTTP errors, refused connections) still arrive
// well inside this window and surface synchronously.
const subscribeEstablishGrace = 250 * time.Millisecond

const DefaultQueueCapacity = 256

var (
	ErrClosed       = errors.New("opencode httpapi: client closed")
	ErrSubscription = errors.New("opencode httpapi: subscription failed")
)

// Client talks to one OpenCode server. It is safe for concurrent use.
type Client struct {
	endpoint *url.URL
	username string
	password string
	http     *http.Client
	frame    int
	queue    int
	mu       sync.Mutex
	closed   bool
}

type Options struct {
	Username string
	Password string
	HTTP     *http.Client
	// FrameLimit bounds one SSE event; QueueCapacity bounds the delivered
	// event backlog of one subscription.
	FrameLimit    int
	QueueCapacity int
}

func New(endpoint string, options Options) (*Client, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("opencode httpapi: invalid endpoint: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("opencode httpapi: endpoint must be http or https")
	}
	client := options.HTTP
	if client == nil {
		client = &http.Client{}
	}
	frame := options.FrameLimit
	if frame <= 0 {
		frame = DefaultFrameLimit
	}
	queue := options.QueueCapacity
	if queue <= 0 {
		queue = DefaultQueueCapacity
	}
	return &Client{endpoint: parsed, username: options.Username, password: options.Password, http: client, frame: frame, queue: queue}, nil
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return ErrClosed
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	target := c.endpoint.JoinPath(path)
	if len(query) > 0 {
		target.RawQuery = query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	if c.password != "" {
		request.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.username+":"+c.password)))
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
	}()
	if response.StatusCode == http.StatusNoContent {
		if out != nil {
			return fmt.Errorf("opencode httpapi: unexpected 204 for %s", path)
		}
		return nil
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, int64(c.frame)))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return native.DecodeAPIError(response.StatusCode, payload)
	}
	if out == nil {
		return nil
	}
	if err := decodeStrict(payload, out); err != nil {
		return fmt.Errorf("opencode httpapi: decode %s response: %w", path, err)
	}
	return nil
}

// decodeStrict rejects duplicate keys, unknown fields, and trailing values.
func decodeStrict(data []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

// CreateSessionRequest selects an optional caller identity, agent, and model.
type CreateSessionRequest struct {
	ID    string
	Agent string
	Model *native.ModelRef
}

// CreateSession creates a server session and returns its projected record.
func (c *Client) CreateSession(ctx context.Context, request CreateSessionRequest) (native.SessionInfo, error) {
	body := map[string]any{}
	if request.ID != "" {
		body["id"] = request.ID
	}
	if request.Agent != "" {
		body["agent"] = request.Agent
	}
	if request.Model != nil {
		body["model"] = request.Model
	}
	var response struct {
		Data native.SessionInfo `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/session", nil, body, &response); err != nil {
		return native.SessionInfo{}, err
	}
	if err := response.Data.Validate(); err != nil {
		return native.SessionInfo{}, err
	}
	return response.Data, nil
}

// Prompt durably admits one input. The pinned server rejects prompt-id
// conflicts with HTTP 409.
func (c *Client) Prompt(ctx context.Context, session native.SessionID, request native.PromptRequest) (native.Admitted, error) {
	var response struct {
		Data native.Admitted `json:"data"`
	}
	path := "/api/session/" + url.PathEscape(string(session)) + "/prompt"
	if err := c.do(ctx, http.MethodPost, path, nil, request, &response); err != nil {
		return native.Admitted{}, err
	}
	if err := response.Data.Validate(); err != nil {
		return native.Admitted{}, err
	}
	if response.Data.SessionID != session {
		return native.Admitted{}, fmt.Errorf("%w: admitted receipt for foreign session %s", ErrSubscription, response.Data.SessionID)
	}
	return response.Data, nil
}

// Interrupt requests interruption of active execution. Idle interruption is
// a documented no-op.
func (c *Client) Interrupt(ctx context.Context, session native.SessionID) error {
	path := "/api/session/" + url.PathEscape(string(session)) + "/interrupt"
	return c.do(ctx, http.MethodPost, path, nil, struct{}{}, nil)
}

// WaitIdle blocks until the session agent loop is idle.
func (c *Client) WaitIdle(ctx context.Context, session native.SessionID) error {
	path := "/api/session/" + url.PathEscape(string(session)) + "/wait"
	return c.do(ctx, http.MethodPost, path, nil, struct{}{}, nil)
}

// Active returns the set of sessions with running execution.
func (c *Client) Active(ctx context.Context) (map[native.SessionID]bool, error) {
	var response struct {
		Data map[native.SessionID]struct {
			Type string `json:"type"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/session/active", nil, nil, &response); err != nil {
		return nil, err
	}
	active := make(map[native.SessionID]bool, len(response.Data))
	for id, value := range response.Data {
		if value.Type == "running" {
			active[id] = true
		}
	}
	return active, nil
}

// History reads one bounded page of durable events strictly after seq.
func (c *Client) History(ctx context.Context, session native.SessionID, after int64, limit int) (native.HistoryPage, error) {
	query := url.Values{}
	if after >= 0 {
		query.Set("after", strconv.FormatInt(after, 10))
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	path := "/api/session/" + url.PathEscape(string(session)) + "/history"
	var raw struct {
		Data    []json.RawMessage `json:"data"`
		HasMore bool              `json:"hasMore"`
	}
	if err := c.do(ctx, http.MethodGet, path, query, nil, &raw); err != nil {
		return native.HistoryPage{}, err
	}
	page := native.HistoryPage{HasMore: raw.HasMore}
	for _, encoded := range raw.Data {
		event, err := native.DecodeEvent(encoded)
		if err != nil {
			return native.HistoryPage{}, err
		}
		page.Events = append(page.Events, event)
	}
	return page, nil
}

// Subscribe opens the per-session durable SSE stream. Events after the
// cursor are replayed first, then live delivery continues. Exactly one
// subscription per session is expected; a second runs concurrently and is
// not deduplicated.
func (c *Client) Subscribe(ctx context.Context, session native.SessionID, after int64) (*Subscription, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	query := url.Values{}
	if after >= 0 {
		query.Set("after", strconv.FormatInt(after, 10))
	}
	target := c.endpoint.JoinPath("/api/session/" + url.PathEscape(string(session)) + "/event")
	target.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "text/event-stream")
	if c.password != "" {
		request.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.username+":"+c.password)))
	}
	sub := &Subscription{events: make(chan native.Event, c.queue), done: make(chan struct{})}
	type outcome struct {
		response *http.Response
		err      error
	}
	settled := make(chan outcome, 1)
	go func() {
		response, err := c.http.Do(request)
		settled <- outcome{response: response, err: err}
	}()
	consume := func(result outcome) error {
		if result.err != nil {
			return result.err
		}
		if result.response.StatusCode != http.StatusOK {
			payload, _ := io.ReadAll(io.LimitReader(result.response.Body, 1<<20))
			_ = result.response.Body.Close()
			return native.DecodeAPIError(result.response.StatusCode, payload)
		}
		go sub.pump(result.response, c.frame)
		return nil
	}
	timer := time.NewTimer(subscribeEstablishGrace)
	defer timer.Stop()
	select {
	case result := <-settled:
		if err := consume(result); err != nil {
			return nil, err
		}
		return sub, nil
	case <-timer.C:
		// Headers are deferred until the stream has an event; hand the
		// pending request to the pump and let any eventual failure surface
		// through Done/Err rather than blocking the caller.
		go func() {
			if err := consume(<-settled); err != nil {
				sub.fail(err)
			}
		}()
		return sub, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	c.http.CloseIdleConnections()
	return nil
}

// Subscription delivers one session's durable event stream in order.
type Subscription struct {
	events    chan native.Event
	done      chan struct{}
	closeOnce sync.Once
	err       error
	mu        sync.Mutex
}

type subscriptionResult struct {
	Event native.Event
	Err   error
}

func (s *Subscription) Events() <-chan native.Event { return s.events }
func (s *Subscription) Done() <-chan struct{}       { return s.done }

func (s *Subscription) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *Subscription) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

func (s *Subscription) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	s.Close()
}

func (s *Subscription) pump(response *http.Response, frameLimit int) {
	defer func() {
		_ = response.Body.Close()
		s.Close()
	}()
	decoder := NewSSEDecoder(response.Body, frameLimit)
	lastSeq := int64(-1)
	for {
		select {
		case <-s.done:
			return
		default:
		}
		frame, err := decoder.Decode()
		if errors.Is(err, io.EOF) {
			s.fail(io.EOF)
			return
		}
		if err != nil {
			s.fail(err)
			return
		}
		if frame.Name != "" && frame.Name != "message" {
			s.fail(fmt.Errorf("%w: unexpected SSE event name %q", ErrInvalidFrame, frame.Name))
			return
		}
		event, err := native.DecodeEvent(frame.Data)
		if err != nil {
			s.fail(err)
			return
		}
		if event.Durable.Seq <= lastSeq {
			s.fail(fmt.Errorf("%w: non-increasing durable sequence %d after %d", ErrSubscription, event.Durable.Seq, lastSeq))
			return
		}
		lastSeq = event.Durable.Seq
		select {
		case s.events <- event:
		case <-s.done:
			return
		}
	}
}

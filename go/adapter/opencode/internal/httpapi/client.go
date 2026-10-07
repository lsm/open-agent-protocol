package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lsm/open-agent-protocol/go/internal/jsonwalk"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"

	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
)

const DefaultQueueCapacity = 256

var (
	ErrClosed       = errors.New("opencode httpapi: client closed")
	ErrSubscription = errors.New("opencode httpapi: subscription failed")
)

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

	payload, err := io.ReadAll(io.LimitReader(response.Body, int64(c.frame)+1))
	if err != nil {
		return err
	}
	if len(payload) > c.frame {
		return fmt.Errorf("opencode httpapi: %s response exceeds %d bytes", path, c.frame)
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

func decodeStrict(data []byte, out any) error {
	if err := jsonwalk.RejectDuplicateKeys(data); err != nil {
		return err
	}
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

type CreateSessionRequest struct {
	ID    string
	Agent string
	Model *native.ModelRef
}

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

func (c *Client) Session(ctx context.Context, session native.SessionID) (native.SessionInfo, error) {
	var response struct {
		Data native.SessionInfo `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/session/"+url.PathEscape(string(session)), nil, nil, &response); err != nil {
		return native.SessionInfo{}, err
	}
	if err := response.Data.Validate(); err != nil {
		return native.SessionInfo{}, err
	}
	if response.Data.ID != session {
		return native.SessionInfo{}, fmt.Errorf("%w: session record for foreign session %s", ErrSubscription, response.Data.ID)
	}
	return response.Data, nil
}

func (c *Client) SwitchModel(ctx context.Context, session native.SessionID, model native.ModelRef) error {
	path := "/api/session/" + url.PathEscape(string(session)) + "/model"
	return c.do(ctx, http.MethodPost, path, nil, map[string]any{"model": model}, nil)
}

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

func (c *Client) Interrupt(ctx context.Context, session native.SessionID) (bool, error) {
	path := "/api/session/" + url.PathEscape(string(session)) + "/interrupt"
	var response struct {
		Interrupted bool `json:"interrupted"`
	}
	if err := c.do(ctx, http.MethodPost, path, nil, nil, &response); err != nil {
		return false, err
	}
	return response.Interrupted, nil
}

func (c *Client) CancelInbox(ctx context.Context, session native.SessionID, inbox native.MessageID) error {
	path := "/api/session/" + url.PathEscape(string(session)) + "/inbox/" + url.PathEscape(string(inbox))
	return c.do(ctx, http.MethodDelete, path, nil, nil, nil)
}

func (c *Client) Info(ctx context.Context) (native.ServerInfo, error) {
	var response native.ServerInfo
	if err := c.do(ctx, http.MethodGet, "/api/info", nil, nil, &response); err != nil {
		return native.ServerInfo{}, err
	}
	return response, nil
}

type SessionPage struct {
	Data []native.SessionInfo
	Next string
}

func (c *Client) Sessions(ctx context.Context, directory, cursor string, limit int) (SessionPage, error) {
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if cursor != "" {
		query.Set("cursor", cursor)
	} else {
		query.Set("order", "desc")
		query.Set("parentID", "null")
		if directory != "" {
			query.Set("directory", directory)
		}
	}
	var response struct {
		Data   []native.SessionInfo `json:"data"`
		Cursor json.RawMessage      `json:"cursor"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/session", query, nil, &response); err != nil {
		return SessionPage{}, err
	}
	for _, info := range response.Data {
		if err := info.Validate(); err != nil {
			return SessionPage{}, err
		}
	}
	var position struct {
		Next string `json:"next"`
	}
	_ = json.Unmarshal(response.Cursor, &position)
	return SessionPage{Data: response.Data, Next: position.Next}, nil
}

type MessagePage struct {
	Data   []json.RawMessage `json:"data"`
	Cursor struct {
		Previous string `json:"previous,omitempty"`
		Next     string `json:"next,omitempty"`
	} `json:"cursor"`
}

func (c *Client) Messages(ctx context.Context, session native.SessionID, cursor string, limit int, newestFirst bool) (MessagePage, error) {
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	switch {
	case cursor != "":
		query.Set("cursor", cursor)
	case newestFirst:
		query.Set("order", "desc")
	default:
		query.Set("order", "asc")
	}
	var page MessagePage
	err := c.do(ctx, http.MethodGet, "/api/session/"+url.PathEscape(string(session))+"/message", query, nil, &page)
	return page, err
}

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

func (c *Client) Subscribe(ctx context.Context, session native.SessionID) (*Subscription, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	target := c.endpoint.JoinPath("/api/event")

	requestCtx, cancel := context.WithCancel(ctx)
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		cancel()
		return nil, err
	}
	request.Header.Set("Accept", "text/event-stream")
	if c.password != "" {
		request.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.username+":"+c.password)))
	}
	sub := &Subscription{session: session, events: make(chan native.Event, c.queue), done: make(chan struct{}), ready: make(chan struct{}), cancel: cancel}
	response, err := c.http.Do(request)
	if err != nil {
		cancel()
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		cancel()
		return nil, native.DecodeAPIError(response.StatusCode, payload)
	}
	go sub.pump(response, c.frame)
	select {
	case <-sub.ready:
		return sub, nil
	case <-sub.done:
		err := sub.Err()
		if err == nil {
			err = io.EOF
		}
		return nil, fmt.Errorf("%w: the event stream ended before server.connected: %w", ErrSubscription, err)
	case <-ctx.Done():
		sub.Close()
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

type Subscription struct {
	session   native.SessionID
	ready     chan struct{}
	connected bool
	events    chan native.Event
	done      chan struct{}
	cancel    context.CancelFunc
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
	s.closeOnce.Do(func() {
		close(s.done)

		if s.cancel != nil {
			s.cancel()
		}
	})
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
		if err != nil && (event.SessionID == "" || event.SessionID == s.session) {
			s.fail(err)
			return
		}
		if event.Type == native.TypeServerConnected {
			if !s.connected {
				s.connected = true
				close(s.ready)
			}
			continue
		}
		if event.SessionID != s.session {
			continue
		}
		if event.Durable != nil {
			if event.Durable.Seq <= lastSeq {
				s.fail(fmt.Errorf("%w: non-increasing durable sequence %d after %d", ErrSubscription, event.Durable.Seq, lastSeq))
				return
			}
			lastSeq = event.Durable.Seq
		}
		select {
		case s.events <- event:
		case <-s.done:
			return
		}
	}
}

package client

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/validation"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const DefaultParticipant = protocol.ParticipantID("user")

type Client struct {
	base        string
	http        *http.Client
	eventsHTTP  http.Client
	participant protocol.ParticipantID
	strict      bool
	validate    bool

	idPrefix string

	schemaOnce sync.Once
	schema     *jsonschema.Schema
	schemaErr  error
	ids        atomic.Uint64
}

type Option func(*Client)

func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) { c.http = httpClient }
}

func WithParticipant(id protocol.ParticipantID) Option {
	return func(c *Client) { c.participant = id }
}

func WithStrictResume() Option {
	return func(c *Client) { c.strict = true }
}

func WithEnvelopeValidation() Option {
	return func(c *Client) { c.validate = true }
}

func New(addr string, opts ...Option) *Client {
	c := &Client{
		http:        http.DefaultClient,
		participant: DefaultParticipant,
		idPrefix:    uniquePrefix(),
	}
	for _, opt := range opts {
		opt(c)
	}
	c.eventsHTTP = *c.http
	c.eventsHTTP.Timeout = 0
	c.base = addr
	if !strings.Contains(addr, "://") {
		c.base = "http://" + addr
	}
	return c
}

func uniquePrefix() string {
	var buffer [4]byte
	if _, err := crand.Read(buffer[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buffer[:])
}

type AdapterInfo struct {
	Name               string                         `json:"name"`
	CapabilityRevision string                         `json:"capability_revision,omitempty"`
	Capabilities       *protocol.CapabilityDescriptor `json:"capabilities,omitempty"`
	Error              string                         `json:"error,omitempty"`
}

func (c *Client) Adapters(ctx context.Context) ([]AdapterInfo, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/adapters"), nil)
	if err != nil {
		return nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("client: list adapters: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("client: read adapter listing: %w", err)
	}
	if err := c.failureError(response, body); err != nil {
		return nil, err
	}
	var listing struct {
		Adapters []AdapterInfo `json:"adapters"`
	}
	if err := json.Unmarshal(body, &listing); err != nil {
		return nil, fmt.Errorf("client: decode adapter listing: %w", err)
	}
	return listing.Adapters, nil
}

type Capabilities struct {
	Revision string

	Descriptor protocol.CapabilityDescriptor
}

func (c *Client) Capabilities(ctx context.Context, adapter string) (Capabilities, error) {
	var caps Capabilities
	envelope, err := c.exchange(ctx, http.MethodGet, "/adapters/"+url.PathEscape(adapter)+"/capabilities", nil, protocol.TypeCapabilitiesResponse)
	if err != nil {
		return caps, err
	}
	if err := envelope.DecodePayload(&caps.Descriptor); err != nil {
		return caps, err
	}
	caps.Revision = envelope.CapabilityRevision
	return caps, nil
}

type OpenOption func(*protocol.SessionOpenRequest)

func AttachToolSources(sources ...protocol.ToolSourceAttachment) OpenOption {
	return func(request *protocol.SessionOpenRequest) {
		request.ToolSources = append(request.ToolSources, sources...)
	}
}

func OpenAllowDegraded(keys ...string) OpenOption {
	return func(request *protocol.SessionOpenRequest) {
		request.AllowDegradedFeatures = append(request.AllowDegradedFeatures, keys...)
	}
}

func (c *Client) Open(ctx context.Context, adapter string, sessionID protocol.SessionID, options ...OpenOption) (*Session, error) {
	payload := protocol.SessionOpenRequest{SessionID: sessionID}
	for _, option := range options {
		option(&payload)
	}
	request, err := c.envelope(protocol.TypeSessionOpenRequest, payload)
	if err != nil {
		return nil, err
	}
	if sessionID != "" {
		request.SessionID = sessionID
	}
	if len(payload.ToolSources) > 0 {

		capabilities, err := c.Capabilities(ctx, adapter)
		if err != nil {
			return nil, err
		}
		request.CapabilityRevision = capabilities.Revision
	}
	envelope, err := c.exchange(ctx, http.MethodPost, "/adapters/"+url.PathEscape(adapter)+"/sessions", &request, protocol.TypeSessionOpenResponse)
	if err != nil {
		return nil, err
	}
	var opened protocol.SessionOpenResponse
	if err := envelope.DecodePayload(&opened); err != nil {
		return nil, err
	}
	if opened.SessionID == "" {
		return nil, fmt.Errorf("client: open response carries no session id")
	}
	return &Session{client: c, id: opened.SessionID, adapter: adapter}, nil
}

type ServerError struct {
	Status int

	Code string

	Message string

	Envelope protocol.Envelope

	Details map[string]any
}

func (e *ServerError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("client: server error (status %d): %s", e.Status, e.Message)
	}
	return fmt.Sprintf("client: server error %s (status %d): %s", e.Code, e.Status, e.Message)
}

func ErrorCode(err error) (string, bool) {
	var serverErr *ServerError
	if errors.As(err, &serverErr) && serverErr.Code != "" {
		return serverErr.Code, true
	}
	return "", false
}

func (c *Client) envelope(typ protocol.EnvelopeType, payload any) (protocol.Envelope, error) {
	id := protocol.EnvelopeID(fmt.Sprintf("client-%s-%d", c.idPrefix, c.ids.Add(1)))
	envelope, err := protocol.NewEnvelope(typ, id, payload)
	if err != nil {
		return protocol.Envelope{}, fmt.Errorf("client: build %s: %w", typ, err)
	}
	return envelope, nil
}

func (c *Client) exchange(ctx context.Context, method, path string, request *protocol.Envelope, want protocol.EnvelopeType) (protocol.Envelope, error) {
	var body []byte
	if request != nil {
		var err error
		if body, err = json.Marshal(request); err != nil {
			return protocol.Envelope{}, fmt.Errorf("client: encode %s: %w", request.Type, err)
		}
	}
	httpRequest, err := http.NewRequestWithContext(ctx, method, c.url(path), bytes.NewReader(body))
	if err != nil {
		return protocol.Envelope{}, err
	}
	if request != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	httpRequest.Header.Set("Accept", "application/json")
	response, err := c.http.Do(httpRequest)
	if err != nil {
		return protocol.Envelope{}, fmt.Errorf("client: %s %s: %w", method, path, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return protocol.Envelope{}, fmt.Errorf("client: read %s response: %w", path, err)
	}
	if response.StatusCode == http.StatusNoContent {
		return protocol.Envelope{}, fmt.Errorf("client: %s returned no content where %s was expected", path, want)
	}
	if err := c.failureError(response, raw); err != nil {

		var serverErr *ServerError
		if errors.As(err, &serverErr) && serverErr.Envelope.Type == protocol.TypeErrorResponse && request != nil && serverErr.Envelope.InReplyTo != request.ID {
			return protocol.Envelope{}, fmt.Errorf("client: %s error response cites correlation %q, want the request id %q", path, serverErr.Envelope.InReplyTo, request.ID)
		}
		return protocol.Envelope{}, err
	}
	envelope, err := protocol.ParseEnvelope(raw)
	if err != nil {
		return protocol.Envelope{}, fmt.Errorf("client: decode %s response envelope: %w", path, err)
	}
	if err := c.checkEnvelope(envelope, raw); err != nil {
		return protocol.Envelope{}, err
	}
	if envelope.Type != want {
		return protocol.Envelope{}, fmt.Errorf("client: %s returned %s, want %s", path, envelope.Type, want)
	}
	if request != nil && envelope.InReplyTo != request.ID {

		return protocol.Envelope{}, fmt.Errorf("client: %s response cites correlation %q, want the request id %q", path, envelope.InReplyTo, request.ID)
	}
	if request != nil {

		if request.SessionID != "" && envelope.SessionID != request.SessionID {
			return protocol.Envelope{}, fmt.Errorf("client: %s response is scoped to session %q, want %q", path, envelope.SessionID, request.SessionID)
		}
		if request.RunID != "" && envelope.RunID != request.RunID {
			return protocol.Envelope{}, fmt.Errorf("client: %s response is scoped to run %q, want %q", path, envelope.RunID, request.RunID)
		}
	}
	return envelope, nil
}

func (c *Client) failureError(response *http.Response, body []byte) error {
	err := statusError(response, body)
	if err == nil {
		return nil
	}
	var serverErr *ServerError
	if c.validate && errors.As(err, &serverErr) && serverErr.Envelope.Type == protocol.TypeErrorResponse {
		if err := c.checkEnvelope(serverErr.Envelope, body); err != nil {
			return err
		}
	}
	return err
}

func statusError(response *http.Response, body []byte) error {
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	serverErr := &ServerError{Status: response.StatusCode, Message: strings.TrimSpace(string(body))}
	if envelope, err := protocol.ParseEnvelope(body); err == nil && envelope.Type == protocol.TypeErrorResponse {
		serverErr.Envelope = envelope
		serverErr.Message = ""
		var payload protocol.ErrorResponse
		if err := envelope.DecodePayload(&payload); err == nil {
			serverErr.Code, serverErr.Message = payload.Error.Code, payload.Error.Message
			serverErr.Details = payload.Error.Details
		}
	}
	if runes := []rune(serverErr.Message); len(runes) > 300 {
		serverErr.Message = string(runes[:300]) + "…"
	}
	return serverErr
}

func (c *Client) checkEnvelope(envelope protocol.Envelope, raw []byte) error {
	if !c.validate {
		return nil
	}
	c.schemaOnce.Do(func() {

		c.schema, c.schemaErr = validation.CompileSchemasWith(validation.CompileOptions{Mode: validation.ModeTolerant})
	})
	if c.schemaErr != nil {
		return fmt.Errorf("client: compile OAP schema: %w", c.schemaErr)
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("client: decode %s for validation: %w", envelope.Type, err)
	}
	if err := c.schema.Validate(value); err != nil {
		return fmt.Errorf("client: inbound %s failed schema validation: %w", envelope.Type, err)
	}
	return nil
}

func (c *Client) url(path string) string {
	return strings.TrimSuffix(c.base, "/") + path
}

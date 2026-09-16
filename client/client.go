// Package client drives a local `oap serve` daemon over its HTTP + SSE
// surface. It is the far-side conformance counterpart of the daemon: OAP
// operations travel as verbatim schema/v0.1 envelopes, and the event stream is
// consumed through a real text/event-stream parser with invisible cursor
// resume.
//
// The zero-configuration client speaks to the daemon's default loopback
// address and acts as participant "user", the identity the daemon resolves
// interactive gates with:
//
//	c := client.New("127.0.0.1:6270")
//	session, err := c.Open(ctx, "memory", "my-session")
//
// The client is safe for concurrent use, never logs, and carries no
// credentials: the daemon is a single-user local service.
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

// DefaultParticipant is the responder identity the daemon acts as when it
// opens an adapter session, so it is also the identity a client resolves
// interactive gates with.
const DefaultParticipant = protocol.ParticipantID("user")

// Client is one daemon connection. The zero value is not usable; construct
// with New.
type Client struct {
	base        string
	http        *http.Client
	eventsHTTP  http.Client
	participant protocol.ParticipantID
	strict      bool
	validate    bool

	// idPrefix makes this client's envelope ids unique in any trace that
	// combines traffic from several clients: OAP envelope ids are trace-
	// unique, and a per-instance counter alone would collide across clients.
	idPrefix string

	schemaOnce sync.Once
	schema     *jsonschema.Schema
	schemaErr  error
	ids        atomic.Uint64
}

// Option tunes a Client; see New.
type Option func(*Client)

// WithHTTPClient substitutes the HTTP client used for daemon requests. The
// client's Timeout, if any, is dropped for event streams: a blanket timeout
// cannot coexist with a long-lived SSE connection, and stream lifetime is
// governed by the context passed to Session.Events instead.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) { c.http = httpClient }
}

// WithParticipant sets the responder identity written into interactive-gate
// resolutions. It must match the identity the gates declare; against an
// `oap serve` daemon that is always "user", the default.
func WithParticipant(id protocol.ParticipantID) Option {
	return func(c *Client) { c.participant = id }
}

// WithStrictResume turns off invisible cursor resume: a dropped event stream
// is reported as a DisconnectError carrying the last observed sequence
// instead of being reconnected.
func WithStrictResume() Option {
	return func(c *Client) { c.strict = true }
}

// WithEnvelopeValidation turns on dev-mode validation of every inbound
// envelope — responses and stream events alike — against the bundled OAP
// schema. It compiles the schema on first use and is off by default: a
// conforming daemon never sends an invalid envelope.
func WithEnvelopeValidation() Option {
	return func(c *Client) { c.validate = true }
}

// New returns a client for the daemon at addr, which may carry a scheme
// ("http://127.0.0.1:6270") or not ("127.0.0.1:6270").
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

// uniquePrefix mints one per-client component for envelope ids.
func uniquePrefix() string {
	var buffer [4]byte
	if _, err := crand.Read(buffer[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buffer[:])
}

// AdapterInfo is one adapter-listing entry. A probing adapter reports its
// capabilities; a failing one reports Error.
type AdapterInfo struct {
	Name               string                         `json:"name"`
	CapabilityRevision string                         `json:"capability_revision,omitempty"`
	Capabilities       *protocol.CapabilityDescriptor `json:"capabilities,omitempty"`
	Error              string                         `json:"error,omitempty"`
}

// Adapters lists the daemon's registered adapters.
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

// Capabilities is one adapter's capability snapshot.
type Capabilities struct {
	// Revision names the descriptor snapshot; every envelope the adapter
	// emits repeats it.
	Revision string
	// Descriptor is the probed capability descriptor.
	Descriptor protocol.CapabilityDescriptor
}

// Capabilities probes one adapter's descriptor. The daemon's response cites a
// correlation id of its own; the paired request envelope, if a caller needs
// one for a trace, is a capabilities.request citing it.
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

// Open opens one adapter session and returns a Session bound to it. A empty
// sessionID lets the adapter mint one; the returned Session reports whatever
// id the daemon confirmed.
// Options are the open's elective members, sent only when given so an
// unmodified call is byte-identical to one made before they existed.
//
// AttachToolSources names the sources the session resolves for its lifetime.
// Over the daemon's client-facing route a process source is named by id only:
// the daemon fills the command, the arguments, and the environment from its
// own registry, and refuses a wire-supplied command or literal environment.
type OpenOption func(*protocol.SessionOpenRequest)

// AttachToolSources attaches tool sources for the session's lifetime.
func AttachToolSources(sources ...protocol.ToolSourceAttachment) OpenOption {
	return func(request *protocol.SessionOpenRequest) {
		request.ToolSources = append(request.ToolSources, sources...)
	}
}

// OpenAllowDegraded opts into the degraded application of the named
// capability keys for this open.
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
		// An open that attaches sources exercises an optional feature, and this
		// validator requires such an envelope to cite the active descriptor.
		// That rule is deliberately stricter than the wire contract, which says
		// a request "may" pin and evaluates an unpinned one against current
		// capabilities — the daemon accepts both, as it must. This client pins
		// anyway, because an exchange it produces should be a trace this project
		// validates: an unpinned attaching open is rejected as
		// stale_capability_revision, which is how every attaching open this
		// client issued used to fail.
		//
		// Liberal in what the daemon accepts, conservative in what the clients
		// send. The revision is read here rather than taken from the caller
		// because pinning to one you have not read asserts a precondition you
		// never checked. The probe costs one request, on attaching opens only.
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

// --- request plumbing ---

// ServerError is a daemon error.response for one request: a correlated,
// schema-valid envelope describing why it was refused.
type ServerError struct {
	// Status is the HTTP status code.
	Status int
	// Code is the error.response code, e.g. "unknown_session". It is empty
	// when the daemon answered with a non-envelope body.
	Code string
	// Message is the error.response message.
	Message string
	// Envelope is the full error envelope; zero when the body carried none.
	Envelope protocol.Envelope
	// Details are the error's typed details, when it carries any. A refused
	// run control names what to change there: feature and reason on
	// unsupported_feature, feature on capability_degraded, model_id on
	// model_not_found.
	Details map[string]any
}

func (e *ServerError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("client: server error (status %d): %s", e.Status, e.Message)
	}
	return fmt.Sprintf("client: server error %s (status %d): %s", e.Code, e.Status, e.Message)
}

// ErrorCode reports the daemon error code carried by err, if any. It returns
// false when err carries no ServerError or the daemon answered with a
// non-envelope body, in which case there is no code to branch on.
func ErrorCode(err error) (string, bool) {
	var serverErr *ServerError
	if errors.As(err, &serverErr) && serverErr.Code != "" {
		return serverErr.Code, true
	}
	return "", false
}

// envelope mints one request envelope with a fresh correlation id unique to
// this client.
func (c *Client) envelope(typ protocol.EnvelopeType, payload any) (protocol.Envelope, error) {
	id := protocol.EnvelopeID(fmt.Sprintf("client-%s-%d", c.idPrefix, c.ids.Add(1)))
	envelope, err := protocol.NewEnvelope(typ, id, payload)
	if err != nil {
		return protocol.Envelope{}, fmt.Errorf("client: build %s: %w", typ, err)
	}
	return envelope, nil
}

// exchange performs one OAP operation: it sends the request envelope, if any,
// requires the expected response type, validates the inbound envelope when
// dev-mode validation is on, and returns it undecoded.
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
		// A parsed error.response must also cite the request it answers: an
		// envelope correlated elsewhere is a protocol violation, not this
		// operation's answer.
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
		// OAP correlation: a response must cite the request envelope it
		// answers. Anything else is a stale or misrouted envelope, and
		// decoding it would attribute another operation's answer to this one.
		return protocol.Envelope{}, fmt.Errorf("client: %s response cites correlation %q, want the request id %q", path, envelope.InReplyTo, request.ID)
	}
	if request != nil {
		// A correlated response must also stay in the request's scope: the
		// protocol validator rejects responses whose session or run scope
		// differs from the request, and per-envelope schema validation cannot
		// see the pairing.
		if request.SessionID != "" && envelope.SessionID != request.SessionID {
			return protocol.Envelope{}, fmt.Errorf("client: %s response is scoped to session %q, want %q", path, envelope.SessionID, request.SessionID)
		}
		if request.RunID != "" && envelope.RunID != request.RunID {
			return protocol.Envelope{}, fmt.Errorf("client: %s response is scoped to run %q, want %q", path, envelope.RunID, request.RunID)
		}
	}
	return envelope, nil
}

// failureError converts a non-2xx response into a ServerError and applies
// dev-mode validation to a parsed error envelope, so every path that reads a
// daemon response validates what it surfaces. Presence is the envelope's
// type, not its id: statusError only records an envelope that parsed as an
// error response, and one that omits its own required id is exactly the
// invalid envelope validation exists to catch.
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

// statusError converts a non-2xx response into a ServerError, keeping the
// correlated envelope when the body carries one.
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

// checkEnvelope applies dev-mode schema validation to one inbound envelope.
func (c *Client) checkEnvelope(envelope protocol.Envelope, raw []byte) error {
	if !c.validate {
		return nil
	}
	c.schemaOnce.Do(func() {
		// A live client validates tolerantly: the daemon it talks to may be a
		// later revision carrying additive fields, enum values, or envelope
		// types the wire rules already promise are ignored, and a strict
		// compile would fail on the first of them.
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

// url joins the daemon base address with an absolute path.
func (c *Client) url(path string) string {
	return strings.TrimSuffix(c.base, "/") + path
}

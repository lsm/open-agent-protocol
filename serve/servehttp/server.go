// Package servehttp exposes a serve.Hub over single-user local HTTP + SSE:
// the transport of the `oap serve` daemon. OAP operations exchange verbatim
// schema/v0.1 envelopes — every request is validated against the bundled OAP
// schema and every refusal is a correlated error.response — /adapters and
// /sessions are daemon-management surfaces returning plain JSON, and run
// events stream as SSE with cursor resume and the documented gap/overflow
// signals. The codec adds no protocol semantics of its own: each route
// decodes its request, calls the hub, and encodes the result.
package servehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
	"github.com/lsm/open-agent-protocol/validation"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// DefaultAddr binds the daemon to loopback: v0 is a single-user local service
// with no authentication, so it must not listen on an external interface by
// default.
const DefaultAddr = "127.0.0.1:6270"

const maxRequestBytes = 16 << 20

// Options tunes the HTTP surface. The zero value is usable.
type Options struct {
	// HostAllowlist, when non-empty, restricts serving to requests whose
	// Host header names one of these hostnames (port-insensitive). The CLI
	// sets it to the loopback names, which closes the browser-borne
	// cross-origin and DNS-rebinding vectors against the default
	// unauthenticated loopback bind; an operator binding a non-loopback
	// address opts out by leaving it empty.
	HostAllowlist []string
}

// Server serves one hub over local HTTP + SSE. OAP operations exchange
// verbatim schema/v0.1 envelopes; /adapters and /sessions are
// daemon-management surfaces and return plain JSON.
type Server struct {
	hub         *serve.Hub
	schema      *jsonschema.Schema
	allowHosts  map[string]bool
	nextIDValue atomic.Uint64
}

// New compiles the request gate and returns a server over the hub.
func New(hub *serve.Hub, options Options) (*Server, error) {
	schema, err := validation.CompileSchemas()
	if err != nil {
		return nil, fmt.Errorf("servehttp: compile request schema: %w", err)
	}
	allowHosts := make(map[string]bool, len(options.HostAllowlist))
	for _, host := range options.HostAllowlist {
		allowHosts[strings.ToLower(strings.TrimSpace(host))] = true
	}
	return &Server{hub: hub, schema: schema, allowHosts: allowHosts}, nil
}

// Hub returns the hub the server serves.
func (s *Server) Hub() *serve.Hub { return s.hub }

// Handler returns the daemon's HTTP routes, wrapped in the host restriction
// when one is configured.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /adapters", s.handleAdapters)
	mux.HandleFunc("GET /adapters/{name}/capabilities", s.handleCapabilities)
	mux.HandleFunc("POST /adapters/{name}/sessions", s.handleOpen)
	mux.HandleFunc("GET /sessions", s.handleSessions)
	mux.HandleFunc("GET /sessions/{id}/events", s.handleEvents)
	mux.HandleFunc("GET /sessions/{id}/state", s.handleState)
	mux.HandleFunc("POST /sessions/{id}/submit", s.handleSubmit)
	mux.HandleFunc("POST /sessions/{id}/resolve", s.handleResolve)
	mux.HandleFunc("POST /sessions/{id}/cancel", s.handleCancel)
	mux.HandleFunc("POST /sessions/{id}/close", s.handleClose)
	if len(s.allowHosts) == 0 {
		return mux
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(strings.TrimSpace(r.Host))
		if name, _, err := net.SplitHostPort(host); err == nil {
			host = strings.ToLower(name)
		}
		if !s.allowHosts[host] {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "unrecognized Host header; this daemon serves loopback clients only",
			})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) nextID(kind string) protocol.EnvelopeID {
	return protocol.EnvelopeID(fmt.Sprintf("oap-%s-%d", kind, s.nextIDValue.Add(1)))
}

func (s *Server) lookupSession(w http.ResponseWriter, id string) (*serve.Session, bool) {
	entry, err := s.hub.Session(protocol.SessionID(id))
	if err != nil {
		s.writeError(w, http.StatusNotFound, "unknown_session", err.Error(), protocol.Envelope{SessionID: protocol.SessionID(id)})
		return nil, false
	}
	return entry, true
}

// addressBound refuses any addressed part beyond serve.MaxAddressBytes, the
// daemon-wide address budget both transports enforce at their request-shape
// layer with this same address_too_long refusal, so their acceptance sets
// agree by construction rather than by one mirroring the other's limits.
// The refusal echoes no address content — the message stays bounded
// whatever the host sent. Returns false after writing the refusal.
func (s *Server) addressBound(w http.ResponseWriter, addresses ...string) bool {
	for _, address := range addresses {
		if len(address) > serve.MaxAddressBytes {
			s.writeError(w, http.StatusBadRequest, "address_too_long",
				fmt.Sprintf("an addressed session, adapter, or cursor exceeds the daemon's %d-byte address bound", serve.MaxAddressBytes),
				protocol.Envelope{})
			return false
		}
	}
	return true
}

// --- daemon-management surfaces ---

type adapterInfo struct {
	Name               string                         `json:"name"`
	CapabilityRevision string                         `json:"capability_revision,omitempty"`
	Capabilities       *protocol.CapabilityDescriptor `json:"capabilities,omitempty"`
	Error              string                         `json:"error,omitempty"`
}

func (s *Server) handleAdapters(w http.ResponseWriter, r *http.Request) {
	statuses := s.hub.Adapters(r.Context())
	infos := make([]adapterInfo, 0, len(statuses))
	for _, status := range statuses {
		info := adapterInfo{Name: status.Name}
		if status.Err != nil {
			info.Error = trimMessage(status.Err.Error())
		} else {
			info.CapabilityRevision = status.Descriptor.CapabilityRevision
			info.Capabilities = &status.Descriptor.Capabilities
		}
		infos = append(infos, info)
	}
	writeJSON(w, http.StatusOK, map[string]any{"adapters": infos})
}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if !s.addressBound(w, r.PathValue("name")) {
		return
	}
	// GET carries no request envelope; the response cites a daemon-minted
	// correlation id, which a client may pair with its own request envelope.
	correlation := s.nextID("request")
	descriptor, err := s.hub.Probe(r.Context(), r.PathValue("name"))
	if err != nil {
		if errors.Is(err, serve.ErrUnknownAdapter) {
			s.writeError(w, http.StatusNotFound, "unknown_adapter", err.Error(), protocol.Envelope{})
			return
		}
		s.writeError(w, http.StatusInternalServerError, "probe_failed", err.Error(), protocol.Envelope{})
		return
	}
	response, err := protocol.NewEnvelope(protocol.TypeCapabilitiesResponse, s.nextID("response"), descriptor.Capabilities)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal", err.Error(), protocol.Envelope{})
		return
	}
	response.InReplyTo = correlation
	response.CapabilityRevision = descriptor.CapabilityRevision
	writeEnvelope(w, http.StatusOK, response)
}

type sessionInfo struct {
	SessionID   string `json:"session_id"`
	Adapter     string `json:"adapter"`
	Status      string `json:"status"`
	ActiveRunID string `json:"active_run_id,omitempty"`
	CreatedAt   string `json:"created_at"`
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	statuses := s.hub.Sessions(r.Context())
	infos := make([]sessionInfo, 0, len(statuses))
	for _, status := range statuses {
		infos = append(infos, sessionInfo{
			SessionID: string(status.SessionID), Adapter: status.Adapter,
			Status: string(status.Status), ActiveRunID: string(status.ActiveRunID),
			CreatedAt: status.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": infos})
}

// --- OAP operations ---

func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	if !s.addressBound(w, r.PathValue("name")) {
		return
	}
	envelope, ok := s.readRequest(w, r, protocol.TypeSessionOpenRequest)
	if !ok {
		return
	}
	var request protocol.SessionOpenRequest
	if err := envelope.DecodePayload(&request); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_payload", err.Error(), envelope)
		return
	}
	name := r.PathValue("name")
	if _, found := s.hub.Registry().Lookup(name); !found {
		s.writeError(w, http.StatusNotFound, "unknown_adapter", fmt.Sprintf("no adapter %q", name), envelope)
		return
	}
	open := base.OpenRequest{SessionID: request.SessionID, Participant: protocol.Participant{ID: serve.DefaultParticipant}}
	if request.Metadata != nil {
		open.Metadata = make(map[string]any, len(request.Metadata))
		for key, raw := range request.Metadata {
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				s.writeError(w, http.StatusBadRequest, "invalid_payload", fmt.Sprintf("metadata %q: %v", key, err), envelope)
				return
			}
			open.Metadata[key] = value
		}
	}
	_, state, err := s.hub.Open(r.Context(), name, open)
	if err != nil {
		status, code := http.StatusBadGateway, "open_failed"
		switch {
		case errors.Is(err, serve.ErrUnknownAdapter):
			status, code = http.StatusNotFound, "unknown_adapter"
		case errors.Is(err, serve.ErrSessionExists):
			status, code = http.StatusConflict, "session_exists"
		}
		s.writeError(w, status, code, adapterMessage(err), envelope)
		return
	}
	// The response is built from the state the open itself confirmed:
	// re-reading state here could fail after registration (an expired
	// request context, a flaky adapter probe) and report a successful open
	// as 502 while the session stays live in the hub.
	response, err := protocol.NewEnvelope(protocol.TypeSessionOpenResponse, s.nextID("response"), protocol.SessionOpenResponse{
		SessionID: state.SessionID, Status: state.Status,
	})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal", err.Error(), envelope)
		return
	}
	response.InReplyTo = envelope.ID
	response.SessionID = state.SessionID
	response.CapabilityRevision = envelope.CapabilityRevision
	writeEnvelope(w, http.StatusOK, response)
}

func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.addressBound(w, r.PathValue("id")) {
		return
	}
	envelope, ok := s.readRequest(w, r, protocol.TypeSessionMessageSubmitRequest)
	if !ok {
		return
	}
	var request protocol.MessageSubmitRequest
	if err := envelope.DecodePayload(&request); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_payload", err.Error(), envelope)
		return
	}
	entry, ok := s.lookupSession(w, r.PathValue("id"))
	if !ok {
		return
	}
	admission, err := entry.Submit(r.Context(), request)
	if err != nil {
		s.writeSubmitError(w, err, envelope)
		return
	}
	response, err := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitResponse, s.nextID("response"), admission)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal", err.Error(), envelope)
		return
	}
	response.InReplyTo = envelope.ID
	response.SessionID = admission.SessionID
	response.RunID = admission.RunID
	response.CapabilityRevision = envelope.CapabilityRevision
	writeEnvelope(w, http.StatusOK, response)
}

func (s *Server) writeSubmitError(w http.ResponseWriter, err error, envelope protocol.Envelope) {
	status, code := http.StatusInternalServerError, "internal"
	switch {
	case errors.Is(err, serve.ErrScopeMismatch):
		status, code = http.StatusBadRequest, "scope_mismatch"
	case errors.Is(err, base.ErrSessionClosed):
		status, code = http.StatusConflict, "session_closed"
	case errors.Is(err, base.ErrRunActive):
		status, code = http.StatusConflict, "run_active"
	case errors.Is(err, base.ErrInvalidSubmission), errors.Is(err, base.ErrUnsupportedInput):
		status, code = http.StatusBadRequest, "invalid_submission"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, code = http.StatusBadRequest, "request_cancelled"
	}
	s.writeError(w, status, code, adapterMessage(err), envelope)
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	if !s.addressBound(w, r.PathValue("id")) {
		return
	}
	envelope, ok := s.readRequest(w, r, protocol.TypeActionPermissionResolveRequest, protocol.TypeUserInputResolveRequest)
	if !ok {
		return
	}
	entry, ok := s.lookupSession(w, r.PathValue("id"))
	if !ok {
		return
	}
	resolution := base.InteractionResolution{}
	switch envelope.Type {
	case protocol.TypeActionPermissionResolveRequest:
		var request protocol.PermissionResolveRequest
		if err := envelope.DecodePayload(&request); err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_payload", err.Error(), envelope)
			return
		}
		resolution = base.InteractionResolution{RunID: request.RunID, RespondedBy: request.RespondedBy, Permission: &request}
	case protocol.TypeUserInputResolveRequest:
		var request protocol.UserInputResolveRequest
		if err := envelope.DecodePayload(&request); err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_payload", err.Error(), envelope)
			return
		}
		resolution = base.InteractionResolution{RunID: request.RunID, RespondedBy: request.RespondedBy, Input: &request}
	}
	if err := entry.Resolve(r.Context(), resolution); err != nil {
		status, code := http.StatusInternalServerError, "internal"
		switch {
		case errors.Is(err, serve.ErrScopeMismatch):
			status, code = http.StatusBadRequest, "scope_mismatch"
		case errors.Is(err, base.ErrSessionClosed):
			status, code = http.StatusConflict, "session_closed"
		case errors.Is(err, base.ErrRunNotFound):
			status, code = http.StatusNotFound, "run_not_found"
		case errors.Is(err, base.ErrInteractionNotFound), errors.Is(err, base.ErrInteractionResolved), errors.Is(err, base.ErrWrongResponder), errors.Is(err, base.ErrInvalidResolution):
			status, code = http.StatusConflict, "resolution_rejected"
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			status, code = http.StatusBadRequest, "request_cancelled"
		}
		envelope.RunID = resolution.RunID
		s.writeError(w, status, code, adapterMessage(err), envelope)
		return
	}
	var response protocol.Envelope
	var err error
	if envelope.Type == protocol.TypeActionPermissionResolveRequest {
		var request protocol.PermissionResolveRequest
		_ = envelope.DecodePayload(&request)
		response, err = protocol.NewEnvelope(protocol.TypeActionPermissionResolveResponse, s.nextID("response"), protocol.PermissionResolveResponse{
			InteractionID: request.InteractionID, SessionID: request.SessionID, RunID: request.RunID, Accepted: true,
		})
	} else {
		var request protocol.UserInputResolveRequest
		_ = envelope.DecodePayload(&request)
		response, err = protocol.NewEnvelope(protocol.TypeUserInputResolveResponse, s.nextID("response"), protocol.UserInputResolveResponse{
			InteractionID: request.InteractionID, SessionID: request.SessionID, RunID: request.RunID, Accepted: true,
		})
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal", err.Error(), envelope)
		return
	}
	response.InReplyTo = envelope.ID
	response.SessionID = entry.ID()
	response.RunID = resolution.RunID
	response.CapabilityRevision = envelope.CapabilityRevision
	writeEnvelope(w, http.StatusOK, response)
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	if !s.addressBound(w, r.PathValue("id")) {
		return
	}
	envelope, ok := s.readRequest(w, r, protocol.TypeRunCancelRequest)
	if !ok {
		return
	}
	var request protocol.RunCancelRequest
	if err := envelope.DecodePayload(&request); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_payload", err.Error(), envelope)
		return
	}
	entry, ok := s.lookupSession(w, r.PathValue("id"))
	if !ok {
		return
	}
	if request.SessionID != entry.ID() {
		s.writeError(w, http.StatusBadRequest, "scope_mismatch", fmt.Sprintf("payload session_id %q does not match the addressed session %q", request.SessionID, entry.ID()), envelope)
		return
	}
	ack, err := entry.Cancel(r.Context(), request.RunID)
	if err != nil {
		status, code := http.StatusInternalServerError, "internal"
		var terminal *base.RunTerminalError
		switch {
		case errors.As(err, &terminal):
			status, code = http.StatusConflict, "run_terminal"
		case errors.Is(err, base.ErrRunNotFound):
			status, code = http.StatusNotFound, "run_not_found"
		case errors.Is(err, base.ErrSessionClosed):
			status, code = http.StatusConflict, "session_closed"
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			status, code = http.StatusBadRequest, "request_cancelled"
		}
		envelope.RunID = request.RunID
		s.writeError(w, status, code, adapterMessage(err), envelope)
		return
	}
	response, err := protocol.NewEnvelope(protocol.TypeRunCancelResponse, s.nextID("response"), ack)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal", err.Error(), envelope)
		return
	}
	response.InReplyTo = envelope.ID
	response.SessionID = ack.SessionID
	response.RunID = ack.RunID
	response.CapabilityRevision = envelope.CapabilityRevision
	writeEnvelope(w, http.StatusOK, response)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if !s.addressBound(w, r.PathValue("id")) {
		return
	}
	entry, ok := s.lookupSession(w, r.PathValue("id"))
	if !ok {
		return
	}
	state, err := entry.State(r.Context())
	if err != nil && !errors.Is(err, base.ErrSessionClosed) {
		s.writeError(w, http.StatusInternalServerError, "state_failed", adapterMessage(err), protocol.Envelope{SessionID: entry.ID()})
		return
	}
	response, err := protocol.NewEnvelope(protocol.TypeSessionStateResponse, s.nextID("response"), state)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal", err.Error(), protocol.Envelope{SessionID: entry.ID()})
		return
	}
	response.InReplyTo = s.nextID("request")
	response.SessionID = state.SessionID
	writeEnvelope(w, http.StatusOK, response)
}

func (s *Server) handleClose(w http.ResponseWriter, r *http.Request) {
	if !s.addressBound(w, r.PathValue("id")) {
		return
	}
	entry, ok := s.lookupSession(w, r.PathValue("id"))
	if !ok {
		return
	}
	// v0.1 defines no session.close envelope, so a successful close returns
	// no body; failures return the usual correlated error.response.
	if err := entry.Close(r.Context()); err != nil {
		status, code := http.StatusInternalServerError, "internal"
		switch {
		case errors.Is(err, base.ErrRunActive):
			status, code = http.StatusConflict, "run_active"
		case errors.Is(err, base.ErrSessionClosed):
			status, code = http.StatusConflict, "session_closed"
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			status, code = http.StatusBadRequest, "request_cancelled"
		}
		s.writeError(w, status, code, adapterMessage(err), protocol.Envelope{SessionID: entry.ID()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !s.addressBound(w, r.PathValue("id")) {
		return
	}
	entry, ok := s.lookupSession(w, r.PathValue("id"))
	if !ok {
		return
	}
	// A closed session can neither deliver live events nor replay: refusing
	// up front keeps a live connection from parking forever and reports the
	// closed state for cursor requests that would otherwise surface the
	// missing run instead.
	if entry.IsClosed() {
		s.writeError(w, http.StatusConflict, "session_closed", "the session is closed", protocol.Envelope{SessionID: entry.ID()})
		return
	}
	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		s.writeError(w, http.StatusInternalServerError, "internal", "streaming is not supported on this connection", protocol.Envelope{SessionID: entry.ID()})
		return
	}
	cursor := r.URL.Query().Get("after")
	if cursor == "" {
		cursor = r.Header.Get("Last-Event-ID")
	}
	if !s.addressBound(w, cursor) {
		return
	}
	var options []serve.SubscribeOption
	if cursor != "" {
		after, err := strconv.ParseUint(cursor, 10, 64)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_cursor", fmt.Sprintf("cursor %q is not an unsigned sequence", cursor), protocol.Envelope{SessionID: entry.ID()})
			return
		}
		// The wire cursor carries only a sequence: the daemon resolves it
		// onto the session's current run, exactly as documented for
		// Last-Event-ID reconnects.
		options = append(options, serve.After("", after))
	}
	subscription, err := s.hub.Subscribe(r.Context(), entry.ID(), options...)
	var gap *base.ReplayGap
	if errors.As(err, &gap) {
		startSSE(w, flusher)
		writeSSEGap(w, flusher, gap)
		return
	}
	if err != nil {
		status, code := http.StatusInternalServerError, "internal"
		switch {
		// The hub's closed-session refusal unwraps to the adapter sentinel,
		// so one case covers both the hub refusal and an adapter Resume that
		// reports the session closed.
		case errors.Is(err, base.ErrSessionClosed):
			status, code = http.StatusConflict, "session_closed"
		case errors.Is(err, serve.ErrNoRunToResume):
			status, code = http.StatusConflict, "no_run_to_resume"
		case errors.Is(err, base.ErrReplayCursorFuture):
			status, code = http.StatusBadRequest, "replay_cursor_future"
		case errors.Is(err, base.ErrRunNotFound):
			status, code = http.StatusNotFound, "run_not_found"
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			status, code = http.StatusBadRequest, "request_cancelled"
		}
		s.writeError(w, status, code, adapterMessage(err), protocol.Envelope{SessionID: entry.ID()})
		return
	}
	// The subscription owns connection lifetime from here: detaching it on
	// the way out mirrors a client disconnect, and the hub drains whatever
	// adapter stream backs it.
	defer subscription.Close()
	startSSE(w, flusher)
	s.streamSubscription(w, flusher, subscription)
}

// --- request gate and response helpers ---

// readRequest parses one request envelope, validates it against the bundled
// OAP envelope schema, and checks its type against the endpoint. Every
// rejection is written as a correlated error.response.
func (s *Server) readRequest(w http.ResponseWriter, r *http.Request, want ...protocol.EnvelopeType) (protocol.Envelope, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds the daemon limit", protocol.Envelope{})
		} else {
			s.writeError(w, http.StatusBadRequest, "request_read", "request body could not be read", protocol.Envelope{})
		}
		return protocol.Envelope{}, false
	}
	envelope, err := protocol.ParseEnvelope(body)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "malformed_json", err.Error(), protocol.Envelope{})
		return protocol.Envelope{}, false
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		s.writeError(w, http.StatusBadRequest, "malformed_json", err.Error(), envelope)
		return protocol.Envelope{}, false
	}
	if err := s.schema.Validate(value); err != nil {
		s.writeError(w, http.StatusBadRequest, "schema_invalid", trimMessage(err.Error()), envelope)
		return protocol.Envelope{}, false
	}
	if len(want) > 0 && !slices.Contains(want, envelope.Type) {
		s.writeError(w, http.StatusBadRequest, "type_mismatch", fmt.Sprintf("endpoint expects %s, got %s", envelopeTypes(want), envelope.Type), envelope)
		return protocol.Envelope{}, false
	}
	return envelope, true
}

func envelopeTypes(types []protocol.EnvelopeType) string {
	names := make([]string, len(types))
	for index, typ := range types {
		names[index] = string(typ)
	}
	return strings.Join(names, " or ")
}

// writeError emits one schema-valid error.response envelope. Errors that could
// echo request content keep the message bounded; correlation is preserved
// whenever the request itself parsed.
func (s *Server) writeError(w http.ResponseWriter, status int, code, message string, request protocol.Envelope) {
	envelope, err := protocol.NewEnvelope(protocol.TypeErrorResponse, s.nextID("error"), protocol.ErrorResponse{
		Error: protocol.ProtocolError{Code: code, Message: trimMessage(message)},
	})
	if err != nil {
		http.Error(w, "oap: internal error", http.StatusInternalServerError)
		return
	}
	envelope.InReplyTo = request.ID
	if envelope.InReplyTo == "" {
		envelope.InReplyTo = s.nextID("request")
	}
	envelope.SessionID = request.SessionID
	envelope.RunID = request.RunID
	writeEnvelope(w, status, envelope)
}

func writeEnvelope(w http.ResponseWriter, status int, envelope protocol.Envelope) {
	body, err := json.Marshal(envelope)
	if err != nil {
		http.Error(w, "oap: internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "oap: internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// adapterMessage flattens adapter errors into a bounded, credential-free
// string: adapter diagnostics never carry resolved environment values, and
// the bound keeps a runaway native error from flooding the response.
func adapterMessage(err error) string {
	return trimMessage(err.Error())
}

// trimMessage bounds an error message on a rune boundary so the truncated
// string stays valid UTF-8 for JSON marshaling.
func trimMessage(message string) string {
	const limit = 300
	runes := []rune(message)
	if len(runes) <= limit {
		return message
	}
	return string(runes[:limit]) + "…"
}

package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/validation"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	// DefaultAddr binds the daemon to loopback: v0 is a single-user local
	// service with no authentication, so it must not listen on an external
	// interface by default.
	DefaultAddr = "127.0.0.1:6270"
	// DefaultShutdownTimeout bounds the graceful shutdown window.
	DefaultShutdownTimeout = 10 * time.Second

	defaultStreamQueue = 64
	maxRequestBytes    = 16 << 20

	// DaemonParticipant is the responder identity the daemon acts as when it
	// opens an adapter session. The v0.1 session.open envelope carries no
	// participant, so interactive gates resolve with this identifier.
	DaemonParticipant = protocol.ParticipantID("user")
)

// Options tunes daemon behavior. The zero value is usable.
type Options struct {
	// StreamQueue bounds the envelopes buffered per SSE connection before
	// the connection is terminated with the overflow signal.
	StreamQueue int
	// Logger receives lifecycle diagnostics. Envelope payloads and resolved
	// environment values are never written to it.
	Logger *log.Logger
}

// Server serves one adapter registry over local HTTP + SSE. OAP operations
// exchange verbatim schema/v0.1 envelopes; /adapters and /sessions are
// daemon-management surfaces and return plain JSON.
type Server struct {
	registry    *Registry
	sessions    *sessionRegistry
	queue       int
	logger      *log.Logger
	schema      *jsonschema.Schema
	nextIDValue atomic.Uint64
}

// New compiles the request gate and returns a server over the registry.
func New(registry *Registry, options Options) (*Server, error) {
	schema, err := validation.CompileSchemas()
	if err != nil {
		return nil, fmt.Errorf("serve: compile request schema: %w", err)
	}
	queue := options.StreamQueue
	if queue <= 0 {
		queue = defaultStreamQueue
	}
	logger := options.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Server{
		registry: registry, sessions: newSessionRegistry(),
		queue: queue, logger: logger, schema: schema,
	}, nil
}

// Handler returns the daemon's HTTP routes.
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
	return mux
}

// CloseSessions settles and closes every registered session, bounded by ctx:
// active runs refuse Close by contract, so each is cancelled first where the
// adapter supports cancellation.
func (s *Server) CloseSessions(ctx context.Context) {
	for _, entry := range s.sessions.list() {
		if err := entry.close(ctx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, base.ErrSessionClosed) {
			s.logger.Printf("serve: close session %s: %v", entry.id, err)
		}
	}
}

func (s *Server) nextID(kind string) protocol.EnvelopeID {
	return protocol.EnvelopeID(fmt.Sprintf("oap-%s-%d", kind, s.nextIDValue.Add(1)))
}

func (s *Server) lookupSession(w http.ResponseWriter, id string) (*serverSession, bool) {
	entry, ok := s.sessions.get(protocol.SessionID(id))
	if !ok {
		s.writeError(w, http.StatusNotFound, "unknown_session", fmt.Sprintf("no session %q", id), protocol.Envelope{SessionID: protocol.SessionID(id)})
		return nil, false
	}
	return entry, true
}

// --- daemon-management surfaces ---

type adapterInfo struct {
	Name               string                         `json:"name"`
	CapabilityRevision string                         `json:"capability_revision,omitempty"`
	Capabilities       *protocol.CapabilityDescriptor `json:"capabilities,omitempty"`
	Error              string                         `json:"error,omitempty"`
}

func (s *Server) handleAdapters(w http.ResponseWriter, r *http.Request) {
	infos := make([]adapterInfo, 0, s.registry.adaptersLen())
	for _, name := range s.registry.Names() {
		info := adapterInfo{Name: name}
		implementation, _ := s.registry.Lookup(name)
		descriptor, err := implementation.Probe(r.Context())
		if err != nil {
			info.Error = err.Error()
		} else {
			info.CapabilityRevision = descriptor.CapabilityRevision
			info.Capabilities = &descriptor.Capabilities
		}
		infos = append(infos, info)
	}
	writeJSON(w, http.StatusOK, map[string]any{"adapters": infos})
}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	// GET carries no request envelope; the response cites a daemon-minted
	// correlation id, which a client may pair with its own request envelope.
	correlation := s.nextID("request")
	implementation, ok := s.registry.Lookup(r.PathValue("name"))
	if !ok {
		s.writeError(w, http.StatusNotFound, "unknown_adapter", fmt.Sprintf("no adapter %q", r.PathValue("name")), protocol.Envelope{})
		return
	}
	descriptor, err := implementation.Probe(r.Context())
	if err != nil {
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
	entries := s.sessions.list()
	infos := make([]sessionInfo, 0, len(entries))
	for _, entry := range entries {
		info := sessionInfo{
			SessionID: string(entry.id), Adapter: entry.adapterName,
			CreatedAt: entry.created.UTC().Format(time.RFC3339),
		}
		state, err := entry.session.State(r.Context())
		info.Status = string(state.Status)
		info.ActiveRunID = string(state.ActiveRunID)
		if err != nil && !errors.Is(err, base.ErrSessionClosed) {
			info.Status = string(protocol.SessionError)
			info.ActiveRunID = ""
		}
		infos = append(infos, info)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": infos})
}

// --- OAP operations ---

func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
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
	implementation, found := s.registry.Lookup(name)
	if !found {
		s.writeError(w, http.StatusNotFound, "unknown_adapter", fmt.Sprintf("no adapter %q", name), envelope)
		return
	}
	open := base.OpenRequest{SessionID: request.SessionID, Participant: protocol.Participant{ID: DaemonParticipant}}
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
	session, err := implementation.Open(r.Context(), open)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "open_failed", adapterMessage(err), envelope)
		return
	}
	state, err := session.State(r.Context())
	if err != nil && !errors.Is(err, base.ErrSessionClosed) {
		s.writeError(w, http.StatusBadGateway, "open_failed", adapterMessage(err), envelope)
		return
	}
	entry := newServerSession(state.SessionID, name, session)
	if err := s.sessions.add(entry); err != nil {
		_ = session.Close(context.WithoutCancel(r.Context()))
		s.writeError(w, http.StatusConflict, "session_exists", err.Error(), envelope)
		return
	}
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
	if request.SessionID != entry.id {
		s.writeError(w, http.StatusBadRequest, "scope_mismatch", fmt.Sprintf("payload session_id %q does not match the addressed session %q", request.SessionID, entry.id), envelope)
		return
	}
	admission, stream, err := entry.session.Submit(r.Context(), request)
	if err != nil {
		s.writeSubmitError(w, err, envelope)
		return
	}
	entry.startRun(admission.RunID, stream)
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
		if request.SessionID != entry.id {
			s.writeError(w, http.StatusBadRequest, "scope_mismatch", fmt.Sprintf("payload session_id %q does not match the addressed session %q", request.SessionID, entry.id), envelope)
			return
		}
		resolution = base.InteractionResolution{RunID: request.RunID, RespondedBy: request.RespondedBy, Permission: &request}
	case protocol.TypeUserInputResolveRequest:
		var request protocol.UserInputResolveRequest
		if err := envelope.DecodePayload(&request); err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_payload", err.Error(), envelope)
			return
		}
		if request.SessionID != entry.id {
			s.writeError(w, http.StatusBadRequest, "scope_mismatch", fmt.Sprintf("payload session_id %q does not match the addressed session %q", request.SessionID, entry.id), envelope)
			return
		}
		resolution = base.InteractionResolution{RunID: request.RunID, RespondedBy: request.RespondedBy, Input: &request}
	}
	if err := entry.session.Resolve(r.Context(), resolution); err != nil {
		status, code := http.StatusInternalServerError, "internal"
		switch {
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
	response.SessionID = entry.id
	response.RunID = resolution.RunID
	response.CapabilityRevision = envelope.CapabilityRevision
	writeEnvelope(w, http.StatusOK, response)
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
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
	if request.SessionID != entry.id {
		s.writeError(w, http.StatusBadRequest, "scope_mismatch", fmt.Sprintf("payload session_id %q does not match the addressed session %q", request.SessionID, entry.id), envelope)
		return
	}
	ack, err := entry.session.Cancel(r.Context(), request.RunID)
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
	entry, ok := s.lookupSession(w, r.PathValue("id"))
	if !ok {
		return
	}
	state, err := entry.session.State(r.Context())
	if err != nil && !errors.Is(err, base.ErrSessionClosed) {
		s.writeError(w, http.StatusInternalServerError, "state_failed", adapterMessage(err), protocol.Envelope{SessionID: entry.id})
		return
	}
	response, err := protocol.NewEnvelope(protocol.TypeSessionStateResponse, s.nextID("response"), state)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal", err.Error(), protocol.Envelope{SessionID: entry.id})
		return
	}
	response.InReplyTo = s.nextID("request")
	response.SessionID = state.SessionID
	writeEnvelope(w, http.StatusOK, response)
}

func (s *Server) handleClose(w http.ResponseWriter, r *http.Request) {
	entry, ok := s.lookupSession(w, r.PathValue("id"))
	if !ok {
		return
	}
	// v0.1 defines no session.close envelope, so a successful close returns
	// no body; failures return the usual correlated error.response.
	if err := entry.session.Close(r.Context()); err != nil {
		status, code := http.StatusInternalServerError, "internal"
		switch {
		case errors.Is(err, base.ErrRunActive):
			status, code = http.StatusConflict, "run_active"
		case errors.Is(err, base.ErrSessionClosed):
			status, code = http.StatusConflict, "session_closed"
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			status, code = http.StatusBadRequest, "request_cancelled"
		}
		s.writeError(w, status, code, adapterMessage(err), protocol.Envelope{SessionID: entry.id})
		return
	}
	entry.markClosed()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	entry, ok := s.lookupSession(w, r.PathValue("id"))
	if !ok {
		return
	}
	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		s.writeError(w, http.StatusInternalServerError, "internal", "streaming is not supported on this connection", protocol.Envelope{SessionID: entry.id})
		return
	}
	cursor := r.URL.Query().Get("after")
	if cursor == "" {
		cursor = r.Header.Get("Last-Event-ID")
	}
	if cursor == "" {
		sub := entry.subscribe(s.queue)
		defer entry.unsubscribe(sub)
		startSSE(w, flusher)
		runID, _ := entry.currentRun()
		streamLive(w, flusher, sub, r.Context(), runID)
		return
	}
	after, err := strconv.ParseUint(cursor, 10, 64)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_cursor", fmt.Sprintf("cursor %q is not an unsigned sequence", cursor), protocol.Envelope{SessionID: entry.id})
		return
	}
	runID, hasRun := entry.currentRun()
	if !hasRun {
		s.writeError(w, http.StatusConflict, "no_run_to_resume", "the session has no run to replay", protocol.Envelope{SessionID: entry.id})
		return
	}
	_, replay, err := entry.session.Resume(r.Context(), base.ResumeRequest{RunID: runID, AfterSequence: after})
	var gap *base.ReplayGap
	if errors.As(err, &gap) {
		startSSE(w, flusher)
		writeSSEGap(w, flusher, gap)
		return
	}
	if err != nil {
		status, code := http.StatusInternalServerError, "internal"
		switch {
		case errors.Is(err, base.ErrReplayCursorFuture):
			status, code = http.StatusBadRequest, "replay_cursor_future"
		case errors.Is(err, base.ErrRunNotFound):
			status, code = http.StatusNotFound, "run_not_found"
		case errors.Is(err, base.ErrSessionClosed):
			status, code = http.StatusConflict, "session_closed"
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			status, code = http.StatusBadRequest, "request_cancelled"
		}
		s.writeError(w, status, code, adapterMessage(err), protocol.Envelope{SessionID: entry.id})
		return
	}
	startSSE(w, flusher)
	streamReplay(w, flusher, replay, r.Context(), runID)
}

// --- request gate and response helpers ---

// readRequest parses one request envelope, validates it against the bundled
// OAP envelope schema, and checks its type against the endpoint. Every
// rejection is written as a correlated error.response.
func (s *Server) readRequest(w http.ResponseWriter, r *http.Request, want ...protocol.EnvelopeType) (protocol.Envelope, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		s.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds the daemon limit", protocol.Envelope{})
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

func startSSE(w http.ResponseWriter, flusher http.Flusher) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
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

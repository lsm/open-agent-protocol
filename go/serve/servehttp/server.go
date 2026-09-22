package servehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
	"github.com/lsm/open-agent-protocol/go/validation"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const DefaultAddr = "127.0.0.1:6270"

const maxRequestBytes = 16 << 20

type Options struct {
	HostAllowlist []string

	SubscriptionHold time.Duration
}

type Server struct {
	hub         *serve.Hub
	schema      *jsonschema.Schema
	allowHosts  map[string]bool
	nextIDValue atomic.Uint64
	holdFor     time.Duration
	mu          sync.Mutex
	held        map[protocol.SessionID]*heldSubscription
}

func New(hub *serve.Hub, options Options) (*Server, error) {
	schema, err := validation.CompileSchemas()
	if err != nil {
		return nil, fmt.Errorf("servehttp: compile request schema: %w", err)
	}
	allowHosts := make(map[string]bool, len(options.HostAllowlist))
	for _, host := range options.HostAllowlist {
		allowHosts[strings.ToLower(strings.TrimSpace(host))] = true
	}
	holdFor := options.SubscriptionHold
	if holdFor <= 0 {
		holdFor = DefaultSubscriptionHold
	}
	return &Server{hub: hub, schema: schema, allowHosts: allowHosts, holdFor: holdFor,
		held: map[protocol.SessionID]*heldSubscription{}}, nil
}

func (s *Server) Hub() *serve.Hub { return s.hub }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /adapters", s.handleAdapters)
	mux.HandleFunc("GET /adapters/{name}/capabilities", s.handleCapabilities)
	mux.HandleFunc("POST /adapters/{name}/sessions", s.handleOpen)
	mux.HandleFunc("GET /sessions", s.handleSessions)
	mux.HandleFunc("GET /sessions/{id}/events", s.handleEvents)
	mux.HandleFunc("GET /sessions/{id}/state", s.handleState)
	mux.HandleFunc("GET /sessions/{id}/tools", s.handleTools)
	mux.HandleFunc("GET /sessions/{id}/models", s.handleModels)
	mux.HandleFunc("POST /sessions/{id}/submit", s.handleSubmit)
	mux.HandleFunc("POST /sessions/{id}/resolve", s.handleResolve)
	mux.HandleFunc("POST /sessions/{id}/cancel", s.handleCancel)
	mux.HandleFunc("POST /sessions/{id}/close", s.handleClose)
	var handler http.Handler = mux
	if len(s.allowHosts) > 0 {
		routed := handler
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			routed.ServeHTTP(w, r)
		})
	}
	return s.refuseBrowserOrigins(handler)
}

func (s *Server) refuseBrowserOrigins(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header["Origin"]; ok {
			s.writeError(w, http.StatusForbidden, "cross_origin_request", "the daemon does not serve cross-origin requests", protocol.Envelope{})
			return
		}
		next.ServeHTTP(w, r)
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
	SessionID   string               `json:"session_id"`
	Adapter     string               `json:"adapter"`
	Status      string               `json:"status"`
	ActiveRunID string               `json:"active_run_id,omitempty"`
	ActiveRuns  []protocol.ActiveRun `json:"active_runs,omitempty"`
	CreatedAt   string               `json:"created_at"`
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	statuses := s.hub.Sessions(r.Context())
	infos := make([]sessionInfo, 0, len(statuses))
	for _, status := range statuses {
		infos = append(infos, sessionInfo{
			SessionID: string(status.SessionID), Adapter: status.Adapter,
			Status: string(status.Status), ActiveRunID: string(status.ActiveRunID),
			ActiveRuns: status.ActiveRuns,
			CreatedAt:  status.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": infos})
}

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
	if _, found := s.hub.Registry().Lookup(name); !found {
		s.writeError(w, http.StatusNotFound, "unknown_adapter", fmt.Sprintf("no adapter %q", name), envelope)
		return
	}
	open := base.OpenRequest{SessionID: request.SessionID, Participant: protocol.Participant{ID: serve.DefaultParticipant}, AllowDegradedFeatures: request.AllowDegradedFeatures, Tools: request.Tools}

	revision, refusal := serve.AttachmentGate(r.Context(), s.hub, name, envelope.CapabilityRevision, request)
	if refusal == nil {
		var subscribeRevision string
		subscribeRevision, refusal = serve.SubscribeGate(r.Context(), s.hub, name, envelope.CapabilityRevision, request)
		if subscribeRevision != "" {
			revision = subscribeRevision
		}
	}
	if refusal != nil {
		var stale *serve.StaleRevisionError
		if errors.As(refusal, &stale) {
			s.writeErrorDetails(w, http.StatusConflict, "stale_capabilities", stale.Error(), map[string]any{
				"expected_revision": stale.Expected, "current_revision": stale.Current,
			}, envelope)
			return
		}
		code, message, details, typed := serve.ControlRefusal(refusal)
		if !typed {

			s.writeError(w, http.StatusInternalServerError, "probe_failed", adapterMessage(refusal), envelope)
			return
		}
		s.writeErrorDetails(w, http.StatusBadRequest, code, message, details, envelope)
		return
	}
	attachments, unresolvable := serve.ResolveAttachments(s.hub, request.ToolSources)
	if unresolvable != nil {
		s.writeErrorDetails(w, http.StatusBadRequest, "unsupported_feature", unresolvable.Error(), map[string]any{
			"feature": protocol.FeatureToolSourcesAttach, "reason": base.ControlUnsatisfiable, "source": unresolvable.Source,
		}, envelope)
		return
	}
	open.ToolSources = attachments
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
	compound := serve.CompoundOpen{Message: request.Message, RequestID: envelope.ID}
	var cancelHold context.CancelFunc
	holdTaken := false
	if request.Subscribe {
		stream, cancel := context.WithCancel(context.Background())
		cancelHold = cancel
		compound.Subscribe, compound.Stream = true, stream
		defer func() {
			if !holdTaken {
				cancel()
			}
		}()
	}
	opened, err := serve.OpenCompound(r.Context(), s.hub, name, open, compound)
	entry, state := opened.Session, opened.State
	if err != nil {

		if code, message, details, ok := serve.ControlRefusal(err); ok {
			s.writeErrorDetails(w, http.StatusBadRequest, code, message, details, envelope)
			return
		}
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

	response, err := protocol.NewEnvelope(protocol.TypeSessionOpenResponse, s.nextID("response"), state)
	if err != nil {

		if opened.Subscription != nil {
			opened.Subscription.Close()
		}
		s.writeError(w, http.StatusInternalServerError, "internal", rollbackOpen(s.hub, entry, request.SessionID != ""), envelope)
		return
	}
	if opened.Subscription != nil {
		s.holdSubscription(state.SessionID, opened.Subscription, cancelHold)
		holdTaken = true
	}
	response.InReplyTo = envelope.ID
	response.SessionID = state.SessionID

	response.CapabilityRevision = envelope.CapabilityRevision
	if revision != "" {
		response.CapabilityRevision = revision
	}
	writeEnvelope(w, http.StatusOK, response)
}

func rollbackOpen(hub *serve.Hub, entry *serve.Session, named bool) string {
	if named {
		return "the open response could not be encoded; the session is open under the session_id the request supplied"
	}
	rollback, cancel := context.WithTimeout(context.Background(), serve.DefaultShutdownTimeout)
	defer cancel()
	if err := serve.Rollback(rollback, hub, entry); err != nil && !errors.Is(err, base.ErrSessionClosed) {
		return "the open response could not be encoded; rolling the session back failed and it may still be live"
	}
	return "the open response could not be encoded; the session was rolled back"
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

	if code, message, details, ok := serve.ControlRefusal(err); ok {
		s.writeErrorDetails(w, http.StatusBadRequest, code, message, details, envelope)
		return
	}
	s.writeControlError(w, err, http.StatusInternalServerError, "internal", envelope)
}

func (s *Server) writeControlError(w http.ResponseWriter, err error, fallbackStatus int, fallbackCode string, envelope protocol.Envelope) {
	if code, message, details, ok := serve.ControlRefusal(err); ok {
		s.writeErrorDetails(w, http.StatusBadRequest, code, message, details, envelope)
		return
	}
	status, code := fallbackStatus, fallbackCode
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
	envelope, ok := s.readRequest(w, r, protocol.TypeActionPermissionResolveRequest, protocol.TypeUserInputResolveRequest, protocol.TypeActionCallResolveRequest)
	if !ok {
		return
	}
	entry, ok := s.lookupSession(w, r.PathValue("id"))
	if !ok {
		return
	}
	if envelope.Type == protocol.TypeActionCallResolveRequest {
		s.resolveCall(w, r, entry, envelope)
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

func (s *Server) resolveCall(w http.ResponseWriter, r *http.Request, entry *serve.Session, envelope protocol.Envelope) {
	var request protocol.ActionCallResolveRequest
	if err := envelope.DecodePayload(&request); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_payload", err.Error(), envelope)
		return
	}
	answer, err := entry.ResolveCall(r.Context(), base.CallResolution{RequestID: envelope.ID, Request: request})
	if err != nil {
		envelope.RunID = request.RunID

		if code, message, details, typed := serve.ControlRefusal(err); typed {
			s.writeErrorDetails(w, http.StatusBadRequest, code, message, details, envelope)
			return
		}
		status, code := http.StatusInternalServerError, "internal"
		switch {
		case errors.Is(err, serve.ErrScopeMismatch):
			status, code = http.StatusBadRequest, "scope_mismatch"
		case errors.Is(err, base.ErrUnsupportedInput):
			status, code = http.StatusBadRequest, "unsupported_feature"
		case errors.Is(err, base.ErrSessionClosed):
			status, code = http.StatusConflict, "session_closed"
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			status, code = http.StatusBadRequest, "request_cancelled"
		}
		s.writeError(w, status, code, adapterMessage(err), envelope)
		return
	}
	response, err := protocol.NewEnvelope(protocol.TypeActionCallResolveResponse, s.nextID("response"), answer)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal", err.Error(), envelope)
		return
	}
	response.InReplyTo = envelope.ID
	response.SessionID = entry.ID()
	response.RunID = request.RunID
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

func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	entry, ok := s.lookupSession(w, r.PathValue("id"))
	if !ok {
		return
	}
	request := protocol.ToolsListRequest{SessionID: entry.ID(), AllowDegradedFeatures: r.URL.Query()["allow_degraded"]}
	catalog, err := entry.Tools(r.Context(), request)
	if err != nil {
		s.writeToolsError(w, err, protocol.Envelope{SessionID: entry.ID()})
		return
	}
	response, err := protocol.NewEnvelope(protocol.TypeActionToolsListResponse, s.nextID("response"), catalog.Tools)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal", err.Error(), protocol.Envelope{SessionID: entry.ID()})
		return
	}
	response.InReplyTo = s.nextID("request")

	response.SessionID = entry.ID()

	response.CapabilityRevision = catalog.Revision
	writeEnvelope(w, http.StatusOK, response)
}

func (s *Server) writeToolsError(w http.ResponseWriter, err error, request protocol.Envelope) {
	switch {
	case errors.Is(err, base.ErrToolCatalogUnavailable):
		s.writeErrorDetails(w, http.StatusBadRequest, "unsupported_feature", adapterMessage(err), map[string]any{
			"feature": protocol.FeatureToolsList, "reason": base.ControlUnadvertised,
		}, request)
	case errors.Is(err, base.ErrSessionClosed):
		s.writeError(w, http.StatusConflict, "session_closed", adapterMessage(err), request)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		s.writeError(w, http.StatusBadRequest, "request_cancelled", adapterMessage(err), request)
	default:
		s.writeControlError(w, err, http.StatusBadGateway, "tools_failed", request)
	}
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	entry, ok := s.lookupSession(w, r.PathValue("id"))
	if !ok {
		return
	}
	request := protocol.ModelsRequest{SessionID: entry.ID(), AllowDegradedFeatures: r.URL.Query()["allow_degraded"]}
	catalog, err := entry.Models(r.Context(), request)
	if err != nil {
		s.writeModelsError(w, err, protocol.Envelope{SessionID: entry.ID()})
		return
	}
	response, err := protocol.NewEnvelope(protocol.TypeModelsResponse, s.nextID("response"), catalog.Models)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "internal", err.Error(), protocol.Envelope{SessionID: entry.ID()})
		return
	}
	response.InReplyTo = s.nextID("request")

	response.SessionID = entry.ID()

	response.CapabilityRevision = catalog.Revision
	writeEnvelope(w, http.StatusOK, response)
}

func (s *Server) writeModelsError(w http.ResponseWriter, err error, envelope protocol.Envelope) {
	if code, message, details, ok := serve.ControlRefusal(err); ok {
		s.writeErrorDetails(w, http.StatusBadRequest, code, message, details, envelope)
		return
	}
	status, code := http.StatusInternalServerError, "internal"
	switch {
	case errors.Is(err, serve.ErrScopeMismatch):
		status, code = http.StatusBadRequest, "scope_mismatch"
	case errors.Is(err, base.ErrSessionClosed):
		status, code = http.StatusConflict, "session_closed"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, code = http.StatusBadRequest, "request_cancelled"
	}
	s.writeError(w, status, code, adapterMessage(err), envelope)
}

func (s *Server) handleClose(w http.ResponseWriter, r *http.Request) {
	entry, ok := s.lookupSession(w, r.PathValue("id"))
	if !ok {
		return
	}

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
	entry, ok := s.lookupSession(w, r.PathValue("id"))
	if !ok {
		return
	}

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
	run := r.URL.Query().Get("run_id")
	var options []serve.SubscribeOption
	if cursor != "" {
		after, err := strconv.ParseUint(cursor, 10, 64)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_cursor", fmt.Sprintf("cursor %q is not an unsigned sequence", cursor), protocol.Envelope{SessionID: entry.ID()})
			return
		}

		options = append(options, serve.After(protocol.RunID(run), after))
	} else if run != "" {
		s.writeError(w, http.StatusBadRequest, "invalid_cursor", "run_id names the run a cursor belongs to; it has no meaning without after", protocol.Envelope{SessionID: entry.ID()})
		return
	}
	var subscription *serve.Subscription
	if held := s.takeHeld(entry.ID()); held != nil {
		if cursor == "" {
			subscription = held.subscription
			defer held.cancel()
			go func() {
				<-r.Context().Done()
				held.cancel()
			}()
		} else {
			held.discard()
		}
	}
	var err error
	if subscription == nil {
		subscription, err = s.hub.Subscribe(r.Context(), entry.ID(), options...)
	}
	var gap *base.ReplayGap
	if errors.As(err, &gap) {
		startSSE(w, flusher)
		writeSSEGap(w, flusher, gap)
		return
	}
	if err != nil {
		status, code := http.StatusInternalServerError, "internal"
		switch {

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

	defer subscription.Close()
	startSSE(w, flusher)
	s.streamSubscription(w, flusher, subscription)
}

func (s *Server) readRequest(w http.ResponseWriter, r *http.Request, want ...protocol.EnvelopeType) (protocol.Envelope, bool) {
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		s.writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "the daemon requires Content-Type: application/json", protocol.Envelope{})
		return protocol.Envelope{}, false
	}
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

func (s *Server) writeError(w http.ResponseWriter, status int, code, message string, request protocol.Envelope) {
	s.writeErrorDetails(w, status, code, message, nil, request)
}

func (s *Server) writeErrorDetails(w http.ResponseWriter, status int, code, message string, details map[string]any, request protocol.Envelope) {
	envelope, err := protocol.NewEnvelope(protocol.TypeErrorResponse, s.nextID("error"), protocol.ErrorResponse{
		Error: protocol.ProtocolError{Code: code, Message: trimMessage(message), Details: details},
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

func adapterMessage(err error) string {
	return trimMessage(err.Error())
}

func trimMessage(message string) string {
	const limit = 300
	runes := []rune(message)
	if len(runes) <= limit {
		return message
	}
	return string(runes[:limit]) + "…"
}

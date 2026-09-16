package servestdio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
)

// The op names, one per HTTP route of serve/servehttp; semantics are
// identical, only the framing differs. The open and events routes join the
// surface with the registration-ordering slice — until then those ops are
// refused as unknown, and the session-scoped ops here serve sessions the
// embedding host opened on the hub directly.
const (
	opAdapters     = "adapters"
	opCapabilities = "capabilities"
	opSessions     = "sessions"
	opState        = "state"
	opModels       = "models"
	opSubmit       = "submit"
	opResolve      = "resolve"
	opCancel       = "cancel"
	opClose        = "close"
	opTools        = "tools"
)

// responseLine is one daemon → host response, correlated by the request id.
// Result is the HTTP route's success body verbatim — an OAP response envelope
// for the envelope exchanges, the plain JSON document for the listings — and
// the JSON null for the routes HTTP answers without a body (close).
type responseLine struct {
	ID     int64           `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *wireError      `json:"error,omitempty"`
}

// wireError carries the code and message of the error.response payload the
// HTTP route would have returned: the same codes, the same bounded messages.
type wireError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// serveRequest executes one op and writes its response.
func (s *Server) serveRequest(ctx context.Context, request requestLine, lines chan<- outLine) {
	result, werr := s.dispatch(ctx, request)
	s.respond(ctx, lines, request, result, werr)
}

// respond writes one op's correlated response line. A response whose
// encoding exceeds the frame limit is replaced by the bounded
// response_too_large refusal, so an oversized result still gets a
// correlated, framable answer.
func (s *Server) respond(ctx context.Context, lines chan<- outLine, request requestLine, result json.RawMessage, werr *wireError) {
	response := responseLine{ID: *request.ID, OK: werr == nil, Result: result}
	if werr != nil {
		response.Result = json.RawMessage("null")
		response.Error = werr
	}
	if err := s.send(ctx, lines, response); err != nil {
		s.logger.Printf("servestdio: response %d: %v", *request.ID, err)
		// Only a size refusal has a bounded correlated answer. A send
		// abandoned by the context ended the session, and must not emit a
		// refusal that blames the response's size.
		if !errors.Is(err, ErrLineTooLarge) {
			return
		}
		fallback := responseLine{ID: *request.ID, OK: false, Result: json.RawMessage("null"), Error: &wireError{
			Code: "response_too_large", Message: "the encoded response exceeds the frame limit",
		}}
		if fallbackErr := s.send(ctx, lines, fallback); fallbackErr != nil {
			s.logger.Printf("servestdio: response %d: %v", *request.ID, fallbackErr)
		}
	}
}

func (s *Server) dispatch(ctx context.Context, request requestLine) (json.RawMessage, *wireError) {
	switch request.Op {
	case opAdapters:
		if werr := request.only(); werr != nil {
			return nil, werr
		}
		return s.adaptersOp(ctx)
	case opCapabilities:
		if werr := request.only(paramAdapter); werr != nil {
			return nil, werr
		}
		if request.Adapter == "" {
			return nil, &wireError{Code: "invalid_request", Message: "adapter is required"}
		}
		return s.capabilitiesOp(ctx, request.Adapter)
	case opSessions:
		if werr := request.only(); werr != nil {
			return nil, werr
		}
		return s.sessionsOp(ctx)
	case opState:
		if werr := request.only(paramSession); werr != nil {
			return nil, werr
		}
		return s.stateOp(ctx, request.SessionID)
	case opTools:
		if werr := request.only(paramSession, paramAllowDegraded); werr != nil {
			return nil, werr
		}
		return s.toolsOp(ctx, request)
	case opModels:
		// The op takes the degraded opt-in as a field of its own, which is
		// what the HTTP route's repeatable ?allow_degraded= parameter carries;
		// both land on the payload's allow_degraded_features unchanged.
		if werr := request.only(paramSession, paramAllowDegraded); werr != nil {
			return nil, werr
		}
		return s.modelsOp(ctx, request)
	case opSubmit, opResolve, opCancel:
		if werr := request.only(paramSession, paramRequest); werr != nil {
			return nil, werr
		}
		switch request.Op {
		case opSubmit:
			return s.submitOp(ctx, request)
		case opResolve:
			return s.resolveOp(ctx, request)
		default:
			return s.cancelOp(ctx, request)
		}
	case opClose:
		if werr := request.only(paramSession); werr != nil {
			return nil, werr
		}
		return s.closeOp(ctx, request.SessionID)
	default:
		return nil, &wireError{Code: "unknown_op", Message: fmt.Sprintf("no op %q", trimMessage(request.Op))}
	}
}

// Param names of requestLine, shared by the per-op shape checks.
const (
	paramAdapter = "adapter"
	paramSession = "session_id"
	paramRequest = "request"
	paramAfter   = "after"
	// paramAllowDegraded is the degraded opt-in the tools and models ops take,
	// the stdio form of the HTTP routes' repeatable ?allow_degraded= query
	// parameter.
	paramAllowDegraded = "allow_degraded_features"
)

// only refuses a well-formed line that carries params its op does not define.
// Presence is the rule — a supplied-but-empty or null param is still supplied
// — because the protocol is closed: speaking the wrong shape is a request
// error the host can correct, unlike an unknown field, which fails the whole
// frontend closed.
func (request requestLine) only(fields ...string) *wireError {
	allowed := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field] = true
	}
	var extra []string
	for _, param := range []string{paramAdapter, paramSession, paramAfter, paramRequest, paramAllowDegraded} {
		if !allowed[param] && request.present[param] {
			extra = append(extra, param)
		}
	}
	if len(extra) == 0 {
		return nil
	}
	return &wireError{Code: "invalid_request", Message: fmt.Sprintf("op %q accepts no %s parameter", trimMessage(request.Op), strings.Join(extra, ", "))}
}

// --- daemon-management surfaces ---

type adapterInfo struct {
	Name               string                         `json:"name"`
	CapabilityRevision string                         `json:"capability_revision,omitempty"`
	Capabilities       *protocol.CapabilityDescriptor `json:"capabilities,omitempty"`
	Error              string                         `json:"error,omitempty"`
}

func (s *Server) adaptersOp(ctx context.Context) (json.RawMessage, *wireError) {
	statuses := s.hub.Adapters(ctx)
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
	return marshalResult(map[string]any{"adapters": infos})
}

func (s *Server) capabilitiesOp(ctx context.Context, name string) (json.RawMessage, *wireError) {
	// The op carries no request envelope; the response cites a daemon-minted
	// correlation id, which a host may pair with its own request envelope.
	correlation := protocol.EnvelopeID(s.nextID("request"))
	descriptor, err := s.hub.Probe(ctx, name)
	if err != nil {
		if errors.Is(err, serve.ErrUnknownAdapter) {
			return nil, &wireError{Code: "unknown_adapter", Message: trimMessage(err.Error())}
		}
		return nil, &wireError{Code: "probe_failed", Message: trimMessage(err.Error())}
	}
	response, err := protocol.NewEnvelope(protocol.TypeCapabilitiesResponse, protocol.EnvelopeID(s.nextID("response")), descriptor.Capabilities)
	if err != nil {
		return nil, internalError(err)
	}
	response.InReplyTo = correlation
	response.CapabilityRevision = descriptor.CapabilityRevision
	return envelopeResult(response)
}

type sessionInfo struct {
	SessionID   string               `json:"session_id"`
	Adapter     string               `json:"adapter"`
	Status      string               `json:"status"`
	ActiveRunID string               `json:"active_run_id,omitempty"`
	ActiveRuns  []protocol.ActiveRun `json:"active_runs,omitempty"`
	CreatedAt   string               `json:"created_at"`
}

func (s *Server) sessionsOp(ctx context.Context) (json.RawMessage, *wireError) {
	statuses := s.hub.Sessions(ctx)
	infos := make([]sessionInfo, 0, len(statuses))
	for _, status := range statuses {
		infos = append(infos, sessionInfo{
			SessionID: string(status.SessionID), Adapter: status.Adapter,
			Status: string(status.Status), ActiveRunID: string(status.ActiveRunID),
			ActiveRuns: status.ActiveRuns,
			CreatedAt:  status.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return marshalResult(map[string]any{"sessions": infos})
}

// --- OAP operations ---

// submitOp admits one run and acknowledges it with the admission envelope.
// The acknowledgement is framability-checked before it is sent: the run is
// already live, and its identifiers — the run id above all — are minted on
// the daemon's side of the wire, so an unframable acknowledgement cannot be
// a generic response_too_large refusal that leaves the run running behind an
// id the host cannot know. It is rolled back instead — the run cancelled on
// a context the request's own end cannot cut short — and the refusal reports
// what the rollback actually observed: a cancelled run only once its own
// run.cancelled envelope arrived, a run that raced the cancellation to
// completed or failed as settled under that outcome, and an unsettled
// cancellation as possibly still live. The resolve and cancel
// acknowledgements echo identifiers the host itself supplied and close
// answers null, so the generic refusal stays honest for them; nothing they
// leave behind is unknowable.
func (s *Server) submitOp(ctx context.Context, request requestLine) (json.RawMessage, *wireError) {
	envelope, werr := s.gateRequest(request.Request, protocol.TypeSessionMessageSubmitRequest)
	if werr != nil {
		return nil, werr
	}
	var payload protocol.MessageSubmitRequest
	if err := envelope.DecodePayload(&payload); err != nil {
		return nil, &wireError{Code: "invalid_payload", Message: trimMessage(err.Error())}
	}
	entry, werr := s.lookupSession(request.SessionID)
	if werr != nil {
		return nil, werr
	}
	admission, err := entry.Submit(ctx, payload)
	if err != nil {
		return nil, submitError(err)
	}
	response, err := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitResponse, protocol.EnvelopeID(s.nextID("response")), admission)
	if err != nil {
		return nil, internalError(err)
	}
	response.InReplyTo = envelope.ID
	response.SessionID = admission.SessionID
	response.RunID = admission.RunID
	response.CapabilityRevision = envelope.CapabilityRevision
	result, werr := envelopeResult(response)
	if werr != nil {
		return nil, werr
	}
	if !s.fits(responseLine{ID: *request.ID, OK: true, Result: result}) {
		// The rollback runs detached from the request's own end — custody —
		// but bounded by the shutdown window, so a hung adapter cannot wedge
		// the read loop past every bound the frontend promises. Settlement
		// is observed, not assumed: a nil cancel error only acknowledges the
		// request, and an asynchronous adapter answers run.cancelling and
		// delivers the terminal envelope later, so the refusal claims a
		// cancelled run only once the run's own terminal envelope arrived.
		// The refusal messages are fixed-size, so they stay framable even at
		// the frame limit's floor: the underlying error goes to the logger,
		// never the line.
		rollback, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), s.shutdown)
		outcome, err := s.rollbackRun(rollback, entry, admission.RunID)
		cancelRollback()
		if err != nil {
			s.logger.Printf("servestdio: roll back submit %d: %v", *request.ID, err)
			var terminal *base.RunTerminalError
			message := "the submit acknowledgement exceeds the frame limit; rolling the run back failed and the run may still be live"
			if errors.As(err, &terminal) || errors.Is(err, base.ErrRunNotFound) || errors.Is(err, base.ErrSessionClosed) {
				message = "the submit acknowledgement exceeds the frame limit; the run settled before the rollback"
			}
			return nil, &wireError{Code: "response_too_large", Message: message}
		}
		switch outcome {
		case protocol.TypeRunCancelled:
			return nil, &wireError{Code: "response_too_large", Message: "the submit acknowledgement exceeds the frame limit; the run was cancelled"}
		case "":
			return nil, &wireError{Code: "response_too_large", Message: "the submit acknowledgement exceeds the frame limit; the run's cancellation did not settle within the rollback window and may still be live"}
		default:
			// The run raced the cancellation to its own terminal: its
			// effects happened, so the outcome is named rather than
			// reported as the cancellation it is not.
			return nil, &wireError{Code: "response_too_large", Message: "the submit acknowledgement exceeds the frame limit; the run settled before the rollback (" + string(outcome) + ")"}
		}
	}
	return result, nil
}

// rollbackRun cancels the run and reports the terminal envelope that
// settled it within the window, or the empty type when none arrived. The
// subscription is registered before the cancel so a synchronous adapter's
// terminal envelopes cannot slip past the wait, and settlement means the
// run's own terminal envelope was observed — never merely that the cancel
// was acknowledged.
func (s *Server) rollbackRun(ctx context.Context, entry *serve.Session, runID protocol.RunID) (protocol.EnvelopeType, error) {
	subscription, err := s.hub.Subscribe(ctx, entry.ID())
	if err != nil {
		return "", err
	}
	defer subscription.Close()
	if _, err = entry.Cancel(ctx, runID); err != nil {
		return "", err
	}
	for {
		envelope, nextErr := subscription.Next()
		if nextErr != nil {
			return "", nil
		}
		if envelope.RunID != runID {
			continue
		}
		switch envelope.Type {
		case protocol.TypeRunCancelled, protocol.TypeRunCompleted, protocol.TypeRunFailed:
			return envelope.Type, nil
		}
	}
}

// fits reports whether one output line's encoding stays within the frame
// limit, so its send would not be refused.
func (s *Server) fits(value any) bool {
	line, err := json.Marshal(value)
	return err == nil && len(line) <= s.frameLimit
}

func submitError(err error) *wireError {
	// A refused control is reported under its own typed code with the details
	// that say what to change, so a caller learns what to stop sending rather
	// than only that the submission was invalid (decision 0005). The mapping
	// is shared with the HTTP codec so the two frontends cannot diverge.
	if code, message, details, ok := serve.ControlRefusal(err); ok {
		return &wireError{Code: code, Message: trimMessage(message), Details: details}
	}
	code := "internal"
	switch {
	case errors.Is(err, serve.ErrScopeMismatch):
		code = "scope_mismatch"
	case errors.Is(err, base.ErrSessionClosed):
		code = "session_closed"
	case errors.Is(err, base.ErrRunActive):
		code = "run_active"
	case errors.Is(err, base.ErrInvalidSubmission), errors.Is(err, base.ErrUnsupportedInput):
		code = "invalid_submission"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = "request_cancelled"
	}
	return &wireError{Code: code, Message: adapterMessage(err)}
}

func (s *Server) resolveOp(ctx context.Context, request requestLine) (json.RawMessage, *wireError) {
	envelope, werr := s.gateRequest(request.Request, protocol.TypeActionPermissionResolveRequest, protocol.TypeUserInputResolveRequest)
	if werr != nil {
		return nil, werr
	}
	entry, werr := s.lookupSession(request.SessionID)
	if werr != nil {
		return nil, werr
	}
	resolution := base.InteractionResolution{}
	switch envelope.Type {
	case protocol.TypeActionPermissionResolveRequest:
		var payload protocol.PermissionResolveRequest
		if err := envelope.DecodePayload(&payload); err != nil {
			return nil, &wireError{Code: "invalid_payload", Message: trimMessage(err.Error())}
		}
		resolution = base.InteractionResolution{RunID: payload.RunID, RespondedBy: payload.RespondedBy, Permission: &payload}
	case protocol.TypeUserInputResolveRequest:
		var payload protocol.UserInputResolveRequest
		if err := envelope.DecodePayload(&payload); err != nil {
			return nil, &wireError{Code: "invalid_payload", Message: trimMessage(err.Error())}
		}
		resolution = base.InteractionResolution{RunID: payload.RunID, RespondedBy: payload.RespondedBy, Input: &payload}
	}
	if err := entry.Resolve(ctx, resolution); err != nil {
		code := "internal"
		switch {
		case errors.Is(err, serve.ErrScopeMismatch):
			code = "scope_mismatch"
		case errors.Is(err, base.ErrSessionClosed):
			code = "session_closed"
		case errors.Is(err, base.ErrRunNotFound):
			code = "run_not_found"
		case errors.Is(err, base.ErrInteractionNotFound), errors.Is(err, base.ErrInteractionResolved), errors.Is(err, base.ErrWrongResponder), errors.Is(err, base.ErrInvalidResolution):
			code = "resolution_rejected"
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			code = "request_cancelled"
		}
		return nil, &wireError{Code: code, Message: adapterMessage(err)}
	}
	var response protocol.Envelope
	var err error
	if envelope.Type == protocol.TypeActionPermissionResolveRequest {
		var payload protocol.PermissionResolveRequest
		_ = envelope.DecodePayload(&payload)
		response, err = protocol.NewEnvelope(protocol.TypeActionPermissionResolveResponse, protocol.EnvelopeID(s.nextID("response")), protocol.PermissionResolveResponse{
			InteractionID: payload.InteractionID, SessionID: payload.SessionID, RunID: payload.RunID, Accepted: true,
		})
	} else {
		var payload protocol.UserInputResolveRequest
		_ = envelope.DecodePayload(&payload)
		response, err = protocol.NewEnvelope(protocol.TypeUserInputResolveResponse, protocol.EnvelopeID(s.nextID("response")), protocol.UserInputResolveResponse{
			InteractionID: payload.InteractionID, SessionID: payload.SessionID, RunID: payload.RunID, Accepted: true,
		})
	}
	if err != nil {
		return nil, internalError(err)
	}
	response.InReplyTo = envelope.ID
	response.SessionID = entry.ID()
	response.RunID = resolution.RunID
	response.CapabilityRevision = envelope.CapabilityRevision
	return envelopeResult(response)
}

func (s *Server) cancelOp(ctx context.Context, request requestLine) (json.RawMessage, *wireError) {
	envelope, werr := s.gateRequest(request.Request, protocol.TypeRunCancelRequest)
	if werr != nil {
		return nil, werr
	}
	var payload protocol.RunCancelRequest
	if err := envelope.DecodePayload(&payload); err != nil {
		return nil, &wireError{Code: "invalid_payload", Message: trimMessage(err.Error())}
	}
	entry, werr := s.lookupSession(request.SessionID)
	if werr != nil {
		return nil, werr
	}
	if payload.SessionID != entry.ID() {
		message := trimMessage(fmt.Sprintf("payload session_id %q does not match the addressed session %q", payload.SessionID, entry.ID()))
		return nil, &wireError{Code: "scope_mismatch", Message: message}
	}
	ack, err := entry.Cancel(ctx, payload.RunID)
	if err != nil {
		code := "internal"
		var terminal *base.RunTerminalError
		switch {
		case errors.As(err, &terminal):
			code = "run_terminal"
		case errors.Is(err, base.ErrRunNotFound):
			code = "run_not_found"
		case errors.Is(err, base.ErrSessionClosed):
			code = "session_closed"
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			code = "request_cancelled"
		}
		return nil, &wireError{Code: code, Message: adapterMessage(err)}
	}
	response, err := protocol.NewEnvelope(protocol.TypeRunCancelResponse, protocol.EnvelopeID(s.nextID("response")), ack)
	if err != nil {
		return nil, internalError(err)
	}
	response.InReplyTo = envelope.ID
	response.SessionID = ack.SessionID
	response.RunID = ack.RunID
	response.CapabilityRevision = envelope.CapabilityRevision
	return envelopeResult(response)
}

func (s *Server) stateOp(ctx context.Context, sessionID string) (json.RawMessage, *wireError) {
	entry, werr := s.lookupSession(sessionID)
	if werr != nil {
		return nil, werr
	}
	state, err := entry.State(ctx)
	if err != nil && !errors.Is(err, base.ErrSessionClosed) {
		return nil, &wireError{Code: "state_failed", Message: adapterMessage(err)}
	}
	response, err := protocol.NewEnvelope(protocol.TypeSessionStateResponse, protocol.EnvelopeID(s.nextID("response")), state)
	if err != nil {
		return nil, internalError(err)
	}
	response.InReplyTo = protocol.EnvelopeID(s.nextID("request"))
	response.SessionID = state.SessionID
	return envelopeResult(response)
}

// toolsOp mirrors GET /sessions/{id}/tools: the same catalog, the same typed
// refusal when the endpoint serves none, with the degraded opt-in taken
// directly rather than through a query parameter.
func (s *Server) toolsOp(ctx context.Context, request requestLine) (json.RawMessage, *wireError) {
	entry, werr := s.lookupSession(request.SessionID)
	if werr != nil {
		return nil, werr
	}
	catalog, err := entry.Tools(ctx, protocol.ToolsListRequest{SessionID: entry.ID(), AllowDegradedFeatures: request.AllowDegradedFeatures})
	if err != nil {
		return nil, toolsError(err)
	}
	response, err := protocol.NewEnvelope(protocol.TypeActionToolsListResponse, protocol.EnvelopeID(s.nextID("response")), catalog.Tools)
	if err != nil {
		return nil, internalError(err)
	}
	response.InReplyTo = protocol.EnvelopeID(s.nextID("request"))
	// Labelled from the hub's own identity and stamped with the listing's own
	// revision, as on the HTTP route: parity_test.go holds the two bodies
	// byte-equal, so a difference here would be a difference a test reports.
	response.SessionID = entry.ID()
	response.CapabilityRevision = catalog.Revision
	return envelopeResult(response)
}

// toolsError maps a catalog failure onto the same typed refusal the HTTP
// route writes, so a catalog refused over stdio is refused identically over
// HTTP.
func toolsError(err error) *wireError {
	if code, message, details, ok := serve.ControlRefusal(err); ok {
		return &wireError{Code: code, Message: message, Details: details}
	}
	switch {
	case errors.Is(err, base.ErrToolCatalogUnavailable):
		return &wireError{Code: "unsupported_feature", Message: adapterMessage(err), Details: map[string]any{
			"feature": protocol.FeatureToolsList, "reason": base.ControlUnadvertised,
		}}
	case errors.Is(err, base.ErrSessionClosed):
		return &wireError{Code: "session_closed", Message: adapterMessage(err)}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &wireError{Code: "request_cancelled", Message: adapterMessage(err)}
	}
	return &wireError{Code: "tools_failed", Message: adapterMessage(err)}
}

// modelsOp serves one session's model catalog, mirroring the HTTP route: the
// op carries no request envelope, so the response cites a daemon-minted
// correlation id a host may pair with its own request envelope.
func (s *Server) modelsOp(ctx context.Context, request requestLine) (json.RawMessage, *wireError) {
	entry, werr := s.lookupSession(request.SessionID)
	if werr != nil {
		return nil, werr
	}
	catalog, err := entry.Models(ctx, protocol.ModelsRequest{SessionID: entry.ID(), AllowDegradedFeatures: request.AllowDegradedFeatures})
	if err != nil {
		return nil, modelsError(err)
	}
	response, err := protocol.NewEnvelope(protocol.TypeModelsResponse, protocol.EnvelopeID(s.nextID("response")), catalog.Models)
	if err != nil {
		return nil, internalError(err)
	}
	response.InReplyTo = protocol.EnvelopeID(s.nextID("request"))
	// Labelled from the hub's own identity, as on the HTTP route: the hub
	// verified the listing is this session's before returning it.
	response.SessionID = entry.ID()
	// The revision comes back with the listing, as on the HTTP route: a
	// descriptor read at another moment can already be the wrong one by the
	// time the catalog is produced.
	response.CapabilityRevision = catalog.Revision
	return envelopeResult(response)
}

// modelsError reports a refused catalog query under the same shared mapping
// the HTTP codec uses, so a query refused over stdio is refused identically
// over HTTP.
func modelsError(err error) *wireError {
	if code, message, details, ok := serve.ControlRefusal(err); ok {
		return &wireError{Code: code, Message: trimMessage(message), Details: details}
	}
	code := "internal"
	switch {
	case errors.Is(err, serve.ErrScopeMismatch):
		code = "scope_mismatch"
	case errors.Is(err, base.ErrSessionClosed):
		code = "session_closed"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = "request_cancelled"
	}
	return &wireError{Code: code, Message: adapterMessage(err)}
}

func (s *Server) closeOp(ctx context.Context, sessionID string) (json.RawMessage, *wireError) {
	entry, werr := s.lookupSession(sessionID)
	if werr != nil {
		return nil, werr
	}
	// v0.1 defines no session.close envelope, so a successful close carries
	// no result — the null the HTTP route answers with a bodyless 204.
	if err := entry.Close(ctx); err != nil {
		code := "internal"
		switch {
		case errors.Is(err, base.ErrRunActive):
			code = "run_active"
		case errors.Is(err, base.ErrSessionClosed):
			code = "session_closed"
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			code = "request_cancelled"
		}
		return nil, &wireError{Code: code, Message: adapterMessage(err)}
	}
	return json.RawMessage("null"), nil
}

// --- request gate and encoding helpers ---

// gateRequest parses one op's request envelope, validates it against the
// bundled OAP envelope schema, and checks its type against the op — the same
// gate every HTTP route applies, rejections mapped to the same codes. The
// size check budgets the envelope itself, the unit servehttp's body limit
// budgets, so an oversized request is one transport-agnostic refusal rather
// than a framing accident of the line that carried it.
func (s *Server) gateRequest(payload json.RawMessage, want ...protocol.EnvelopeType) (protocol.Envelope, *wireError) {
	if len(payload) == 0 || string(payload) == "null" {
		return protocol.Envelope{}, &wireError{Code: "invalid_request", Message: "the op requires a \"request\" envelope"}
	}
	if len(payload) > maxEnvelopeBytes {
		return protocol.Envelope{}, &wireError{Code: "request_too_large", Message: "request envelope exceeds the daemon limit"}
	}
	envelope, err := protocol.ParseEnvelope(payload)
	if err != nil {
		return protocol.Envelope{}, &wireError{Code: "malformed_json", Message: trimMessage(err.Error())}
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return protocol.Envelope{}, &wireError{Code: "malformed_json", Message: trimMessage(err.Error())}
	}
	if err := s.schema.Validate(value); err != nil {
		return protocol.Envelope{}, &wireError{Code: "schema_invalid", Message: trimMessage(err.Error())}
	}
	if len(want) > 0 && !slices.Contains(want, envelope.Type) {
		return protocol.Envelope{}, &wireError{Code: "type_mismatch", Message: fmt.Sprintf("op expects %s, got %s", envelopeTypes(want), envelope.Type)}
	}
	return envelope, nil
}

func (s *Server) lookupSession(id string) (*serve.Session, *wireError) {
	entry, err := s.hub.Session(protocol.SessionID(id))
	if err != nil {
		return nil, &wireError{Code: "unknown_session", Message: trimMessage(err.Error())}
	}
	return entry, nil
}

// nextID mints the daemon-side correlation ids of envelopes the frontend
// itself builds — the same oap-<kind>-<n> scheme servehttp mints.
func (s *Server) nextID(kind string) string {
	return fmt.Sprintf("oap-%s-%d", kind, s.nextIDValue.Add(1))
}

func envelopeTypes(types []protocol.EnvelopeType) string {
	names := make([]string, len(types))
	for index, typ := range types {
		names[index] = string(typ)
	}
	return strings.Join(names, " or ")
}

func envelopeResult(envelope protocol.Envelope) (json.RawMessage, *wireError) {
	return marshalResult(envelope)
}

func marshalResult(value any) (json.RawMessage, *wireError) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, internalError(err)
	}
	return data, nil
}

func internalError(err error) *wireError {
	return &wireError{Code: "internal", Message: trimMessage(err.Error())}
}

// adapterMessage flattens adapter errors into a bounded, credential-free
// string: adapter diagnostics never carry resolved environment values, and
// the bound keeps a runaway native error from flooding the line.
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

package servestdio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
)

// The op names, one per HTTP route of serve/servehttp; semantics are
// identical, only the framing differs.
const (
	opAdapters     = "adapters"
	opCapabilities = "capabilities"
	opOpen         = "open"
	opEvents       = "events"
	opSessions     = "sessions"
	opState        = "state"
	opSubmit       = "submit"
	opResolve      = "resolve"
	opCancel       = "cancel"
	opClose        = "close"
)

// Signal line names. The overflow and replay-gap names mirror the SSE named
// events verbatim; the session-closed line is stdio's counterpart of the SSE
// response simply ending when a session closes under an open stream — a
// shared stdout has no per-subscription connection close to observe, so the
// end is signalled; the frame-limit line is this framing's own terminal for
// an envelope no line can carry.
const (
	signalEnvelope      = "envelope"
	signalOverflow      = "oap-overflow"
	signalReplayGap     = "oap-replay-gap"
	signalSessionClosed = "oap-session-closed"
	signalFrameLimit    = "oap-frame-limit"
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
	Code    string `json:"code"`
	Message string `json:"message"`
}

// envelopeLine is one run-event delivery, the NDJSON form of the SSE
// data:/id: pair: the envelope JSON with its sequence alongside it. ID
// carries the id of the events request whose subscription produced the line
// — several subscriptions may overlap on one session, and stdout, unlike
// separate SSE connections, needs the correlation stated on every line.
type envelopeLine struct {
	Event     string          `json:"event"`
	ID        int64           `json:"id"`
	SessionID string          `json:"session_id"`
	Sequence  *uint64         `json:"sequence,omitempty"`
	Envelope  json.RawMessage `json:"envelope"`
}

// The signal lines report the conditions SSE carries as named events. The
// string fields — the session address, adapter-minted run ids, the fixed
// messages — are omitted by each line's minimal form, the correlated
// fallback sent when the full line itself would not frame; the numeric
// resume cursor each host cannot know on its own always stays.
type overflowLine struct {
	Event        string `json:"event"`
	ID           int64  `json:"id"`
	SessionID    string `json:"session_id,omitempty"`
	RunID        string `json:"run_id,omitempty"`
	LastSequence uint64 `json:"last_sequence"`
	Message      string `json:"message,omitempty"`
}

type gapLine struct {
	Event           string `json:"event"`
	ID              int64  `json:"id"`
	SessionID       string `json:"session_id,omitempty"`
	RequestedAfter  uint64 `json:"requested_after"`
	OldestAvailable uint64 `json:"oldest_available"`
	LatestAvailable uint64 `json:"latest_available"`
	Message         string `json:"message,omitempty"`
}

type sessionClosedLine struct {
	Event     string `json:"event"`
	ID        int64  `json:"id"`
	SessionID string `json:"session_id,omitempty"`
	Message   string `json:"message,omitempty"`
}

// frameLimitLine is the terminal signal for a subscription whose envelope
// exceeded the frame limit: this wire cannot carry that envelope, so the
// subscription ends naming the position a cursor resumes after. The
// adapter-minted identifiers and the message are omitted in the minimal
// form, which the frame-limit floor guarantees always fits: the correlation
// id and the resume position are the parts the host cannot do without.
type frameLimitLine struct {
	Event     string `json:"event"`
	ID        int64  `json:"id"`
	SessionID string `json:"session_id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	Sequence  uint64 `json:"sequence"`
	Message   string `json:"message,omitempty"`
}

// serveRequest executes one op and writes its response. The registration
// ops are executed right here in the read loop — open registers its session
// and events its subscription before the next line is read — so a host that
// pipelines the canonical open → events → submit cannot race a registration
// and miss the run's first envelope; each events op's pump then serves its
// subscription from its own goroutine. Every other op runs through dispatch
// unchanged.
func (s *Server) serveRequest(ctx context.Context, request requestLine, lines chan<- []byte) {
	switch request.Op {
	case opOpen:
		s.serveOpen(ctx, request, lines)
	case opEvents:
		s.serveEvents(ctx, request, lines)
	default:
		result, werr := s.dispatch(ctx, request)
		s.respond(ctx, lines, request, result, werr)
	}
}

// respond writes one op's correlated response line. A response whose
// encoding exceeds the frame limit is replaced by the bounded
// response_too_large refusal, so an oversized result still gets a
// correlated, framable answer.
func (s *Server) respond(ctx context.Context, lines chan<- []byte, request requestLine, result json.RawMessage, werr *wireError) {
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
	for _, param := range []string{paramAdapter, paramSession, paramAfter, paramRequest} {
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
	SessionID   string `json:"session_id"`
	Adapter     string `json:"adapter"`
	Status      string `json:"status"`
	ActiveRunID string `json:"active_run_id,omitempty"`
	CreatedAt   string `json:"created_at"`
}

func (s *Server) sessionsOp(ctx context.Context) (json.RawMessage, *wireError) {
	statuses := s.hub.Sessions(ctx)
	infos := make([]sessionInfo, 0, len(statuses))
	for _, status := range statuses {
		infos = append(infos, sessionInfo{
			SessionID: string(status.SessionID), Adapter: status.Adapter,
			Status: string(status.Status), ActiveRunID: string(status.ActiveRunID),
			CreatedAt: status.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return marshalResult(map[string]any{"sessions": infos})
}

// --- OAP operations ---

// serveOpen executes one `open` op in the read loop, so the session is
// registered before the next line is read. An open whose acknowledgement
// cannot be framed is rolled back — the session closed again before the
// refusal is sent — so a session the host believes failed never stays live
// behind an id it cannot know. The rollback reports what it actually
// observed: a close that failed leaves the session possibly still live, and
// the refusal says so rather than claiming a rollback that did not happen;
// a session already closed counts as rolled back.
func (s *Server) serveOpen(ctx context.Context, request requestLine, lines chan<- []byte) {
	result, entry, werr := s.openOp(ctx, request)
	if werr == nil && !s.fits(responseLine{ID: *request.ID, OK: true, Result: result}) {
		werr = &wireError{Code: "response_too_large", Message: "the open response exceeds the frame limit; the session was rolled back"}
		if entry != nil {
			// The rollback runs detached from the request's own end —
			// custody — but bounded by the shutdown window, so a hung
			// adapter close cannot wedge the read loop past every bound the
			// frontend promises. The refusal message is fixed-size, so it
			// stays framable even at the frame limit's floor: the underlying
			// error goes to the logger, never the line.
			rollback, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), s.shutdown)
			closeErr := entry.Close(rollback)
			cancelRollback()
			if closeErr != nil && !errors.Is(closeErr, base.ErrSessionClosed) {
				s.logger.Printf("servestdio: roll back open %d: %v", *request.ID, closeErr)
				werr = &wireError{Code: "response_too_large", Message: "the open response exceeds the frame limit; rolling the session back failed and it may still be live"}
			}
		}
		result = nil
	}
	s.respond(ctx, lines, request, result, werr)
}

// openOp mirrors the HTTP POST /adapters/{name}/sessions route: gate the
// request envelope, open through the hub, and answer with the open response
// built from the state the open itself confirmed — re-reading state here
// could fail after registration and report a successful open as failed while
// the session stays live in the hub.
func (s *Server) openOp(ctx context.Context, request requestLine) (json.RawMessage, *serve.Session, *wireError) {
	if werr := request.only(paramAdapter, paramRequest); werr != nil {
		return nil, nil, werr
	}
	if request.Adapter == "" {
		return nil, nil, &wireError{Code: "invalid_request", Message: "adapter is required"}
	}
	envelope, werr := s.gateRequest(request.Request, protocol.TypeSessionOpenRequest)
	if werr != nil {
		return nil, nil, werr
	}
	var payload protocol.SessionOpenRequest
	if err := envelope.DecodePayload(&payload); err != nil {
		return nil, nil, &wireError{Code: "invalid_payload", Message: trimMessage(err.Error())}
	}
	open := base.OpenRequest{SessionID: payload.SessionID, Participant: protocol.Participant{ID: serve.DefaultParticipant}}
	if payload.Metadata != nil {
		open.Metadata = make(map[string]any, len(payload.Metadata))
		for key, raw := range payload.Metadata {
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, nil, &wireError{Code: "invalid_payload", Message: trimMessage(fmt.Sprintf("metadata %q: %v", key, err))}
			}
			open.Metadata[key] = value
		}
	}
	entry, state, err := s.hub.Open(ctx, request.Adapter, open)
	if err != nil {
		code := "open_failed"
		switch {
		case errors.Is(err, serve.ErrUnknownAdapter):
			code = "unknown_adapter"
		case errors.Is(err, serve.ErrSessionExists):
			code = "session_exists"
		}
		return nil, nil, &wireError{Code: code, Message: adapterMessage(err)}
	}
	response, err := protocol.NewEnvelope(protocol.TypeSessionOpenResponse, protocol.EnvelopeID(s.nextID("response")), protocol.SessionOpenResponse{
		SessionID: state.SessionID, Status: state.Status,
	})
	if err != nil {
		return nil, entry, internalError(err)
	}
	response.InReplyTo = envelope.ID
	response.SessionID = state.SessionID
	response.CapabilityRevision = envelope.CapabilityRevision
	result, werr := envelopeResult(response)
	return result, entry, werr
}

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

// --- the events op and its pump ---

// serveEvents executes one `events` op in the read loop, so the
// subscription is registered before the next line is read and a pipelined
// submit cannot race ahead of it — the stdio counterpart of the HTTP
// client's subscribe-before-submit discipline. The cursor mirrors the HTTP
// surface: a bare sequence, carried as a JSON number, resolved onto the
// session's current run; a supplied null is treated as absent.
func (s *Server) serveEvents(ctx context.Context, request requestLine, lines chan<- []byte) {
	id := *request.ID
	fail := func(werr *wireError) {
		s.respond(ctx, lines, request, nil, werr)
	}
	if werr := request.only(paramSession, paramAfter); werr != nil {
		fail(werr)
		return
	}
	entry, werr := s.lookupSession(request.SessionID)
	if werr != nil {
		fail(werr)
		return
	}
	// A closed session can neither deliver live events nor replay: refusing
	// up front keeps a subscription from parking forever and reports the
	// closed state for cursor requests that would otherwise surface the
	// missing run instead.
	if entry.IsClosed() {
		fail(&wireError{Code: "session_closed", Message: "the session is closed"})
		return
	}
	var options []serve.SubscribeOption
	if len(request.After) > 0 && string(request.After) != "null" {
		after, err := strconv.ParseUint(string(request.After), 10, 64)
		if err != nil {
			fail(&wireError{Code: "invalid_cursor", Message: fmt.Sprintf("cursor %q is not an unsigned sequence", trimMessage(string(request.After)))})
			return
		}
		options = append(options, serve.After("", after))
	}
	subscription, err := s.hub.Subscribe(ctx, entry.ID(), options...)
	var gap *base.ReplayGap
	if errors.As(err, &gap) {
		// The gap is delivered inside a successful op, exactly as the SSE
		// route answers 200 and then emits the replay-gap event: the
		// subscription itself is over.
		s.respond(ctx, lines, request, json.RawMessage("null"), nil)
		s.sendSignal(ctx, lines, id, gapLine{
			Event: signalReplayGap, ID: id, SessionID: string(entry.ID()),
			RequestedAfter: gap.RequestedAfter, OldestAvailable: gap.OldestAvailable, LatestAvailable: gap.LatestAvailable,
			Message: "requested replay cursor is no longer retained; resume with a cursor at or after oldest_available - 1",
		}, gapLine{
			Event: signalReplayGap, ID: id,
			RequestedAfter: gap.RequestedAfter, OldestAvailable: gap.OldestAvailable, LatestAvailable: gap.LatestAvailable,
		})
		return
	}
	if err != nil {
		code := "internal"
		switch {
		// The hub's closed-session refusal unwraps to the adapter sentinel,
		// so one case covers both the hub refusal and an adapter Resume that
		// reports the session closed.
		case errors.Is(err, base.ErrSessionClosed):
			code = "session_closed"
		case errors.Is(err, serve.ErrNoRunToResume):
			code = "no_run_to_resume"
		case errors.Is(err, base.ErrReplayCursorFuture):
			code = "replay_cursor_future"
		case errors.Is(err, base.ErrRunNotFound):
			code = "run_not_found"
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			code = "request_cancelled"
		}
		fail(&wireError{Code: code, Message: adapterMessage(err)})
		return
	}
	s.respond(ctx, lines, request, json.RawMessage("null"), nil)
	go s.pump(ctx, entry, subscription, id, lines)
}

// pump serves one subscription through the shared writer until it ends. The
// envelope lines preserve the subscription's per-session order and carry the
// events request's id, so overlapping subscriptions on one session stay
// attributable; the single writer guarantees each line is atomic. Ends
// mirror the SSE stream ends: the overflow signal gets a named line, a
// subscription that ends because the session closed under it gets the
// session-closed line, and the clean end at a run's terminal event produces
// no line — the terminal envelope (run.completed / run.failed /
// run.cancelled) is the marker, exactly as the SSE response simply ends. Any
// other end (a failed run stream, the frontend's own shutdown) also produces
// no line; the documented recovery is a fresh events op with an after
// cursor. An envelope whose line exceeds the frame limit ends the
// subscription with the correlated oap-frame-limit terminal naming the
// position a cursor resumes after: this framing cannot carry the envelope,
// and skipping it would leave a hole a later cursor would double-count.
func (s *Server) pump(ctx context.Context, entry *serve.Session, subscription *serve.Subscription, id int64, lines chan<- []byte) {
	defer subscription.Close()
	for {
		envelope, err := subscription.Next()
		if err == nil {
			data, marshalErr := json.Marshal(envelope)
			if marshalErr != nil {
				s.logger.Printf("servestdio: subscription %d: encode envelope: %v", id, marshalErr)
				return
			}
			if sendErr := s.send(ctx, lines, envelopeLine{
				Event: signalEnvelope, ID: id, SessionID: string(entry.ID()),
				Sequence: envelope.Sequence, Envelope: data,
			}); sendErr != nil {
				s.logger.Printf("servestdio: subscription %d: %v", id, sendErr)
				if !errors.Is(sendErr, ErrLineTooLarge) {
					return
				}
				sequence := uint64(0)
				if envelope.Sequence != nil {
					sequence = *envelope.Sequence
				}
				s.sendSignal(ctx, lines, id, frameLimitLine{
					Event: signalFrameLimit, ID: id, SessionID: string(entry.ID()),
					RunID: string(envelope.RunID), Sequence: sequence,
					Message: "envelope exceeds the frame limit; the subscription ended — resume with a cursor after this sequence to continue past it",
				}, frameLimitLine{Event: signalFrameLimit, ID: id, Sequence: sequence})
				return
			}
			continue
		}
		var overflow *serve.OverflowError
		if errors.As(err, &overflow) {
			s.sendSignal(ctx, lines, id, overflowLine{
				Event: signalOverflow, ID: id, SessionID: string(entry.ID()),
				RunID: string(overflow.RunID), LastSequence: overflow.LastSequence,
				Message: "event stream consumer fell behind; resume with a cursor after this sequence",
			}, overflowLine{Event: signalOverflow, ID: id, LastSequence: overflow.LastSequence})
			return
		}
		if errors.Is(err, io.EOF) && entry.IsClosed() {
			s.sendSignal(ctx, lines, id, sessionClosedLine{
				Event: signalSessionClosed, ID: id, SessionID: string(entry.ID()),
				Message: "the session is closed",
			}, sessionClosedLine{Event: signalSessionClosed, ID: id})
		}
		return
	}
}

// sendSignal sends one signal line, falling back to its minimal form when
// the full line would not frame: the unbounded parts of a signal are the
// session address and adapter-minted identifiers, so a signal built around
// them can cross the same limit that ended the subscription. The minimal
// form keeps the correlation id and the resume-relevant numbers — the parts
// the host cannot do without — which the frame-limit floor guarantees
// always fits.
func (s *Server) sendSignal(ctx context.Context, lines chan<- []byte, id int64, full, minimal any) {
	if err := s.send(ctx, lines, full); err != nil {
		s.logger.Printf("servestdio: subscription %d: %v", id, err)
		if !errors.Is(err, ErrLineTooLarge) {
			return
		}
		if err := s.send(ctx, lines, minimal); err != nil {
			s.logger.Printf("servestdio: subscription %d: %v", id, err)
		}
	}
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

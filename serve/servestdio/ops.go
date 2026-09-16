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
	opOpen         = "open"
	opEvents       = "events"
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
// The named line kinds a subscription emits. An envelope line carries one
// event; the rest are the subscription's own endings, each correlated with
// the events request's id so overlapping subscriptions on one session stay
// attributable. They mirror the SSE stream's named events one for one.
const (
	signalEnvelope      = "envelope"
	signalOverflow      = "oap-overflow"
	signalReplayGap     = "oap-replay-gap"
	signalSessionClosed = "oap-session-closed"
	signalFrameLimit    = "oap-frame-limit"
)

// envelopeLine is one event delivered to a subscription. The sequence is
// repeated outside the envelope so a host can resume without decoding it.
type envelopeLine struct {
	Event     string          `json:"event"`
	ID        int64           `json:"id"`
	SessionID string          `json:"session_id"`
	Sequence  *uint64         `json:"sequence,omitempty"`
	Envelope  json.RawMessage `json:"envelope"`
}

// overflowLine ends a subscription whose consumer fell behind the hub's
// bounded fan-out. The last sequence it did carry is where a cursor resumes.
type overflowLine struct {
	Event        string `json:"event"`
	ID           int64  `json:"id"`
	SessionID    string `json:"session_id"`
	RunID        string `json:"run_id"`
	LastSequence uint64 `json:"last_sequence"`
	Message      string `json:"message"`
}

// gapLine reports a replay cursor the journal no longer retains. It is not a
// failed subscription: the op succeeded and this says what cannot be
// delivered, which is what keeps an expired cursor from becoming fake
// continuity.
type gapLine struct {
	Event           string `json:"event"`
	ID              int64  `json:"id"`
	SessionID       string `json:"session_id"`
	RequestedAfter  uint64 `json:"requested_after"`
	OldestAvailable uint64 `json:"oldest_available"`
	LatestAvailable uint64 `json:"latest_available"`
	Message         string `json:"message"`
}

// sessionClosedLine ends a subscription whose session closed under it.
type sessionClosedLine struct {
	Event     string `json:"event"`
	ID        int64  `json:"id"`
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

// frameLimitLine ends a subscription this framing cannot carry past. Every
// member but the sequence is optional, because the adapter-minted identifiers
// in the full form can themselves push the line past the limit that ended the
// subscription — the minimal form is what the frame-limit floor guarantees
// fits, and the sequence is the one fact a host needs to resume.
type frameLimitLine struct {
	Event     string `json:"event"`
	ID        int64  `json:"id"`
	SessionID string `json:"session_id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	Sequence  uint64 `json:"sequence"`
	Message   string `json:"message,omitempty"`
}

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
//
// The events op is served on its own path rather than through dispatch,
// because it is the only op whose answer is a stream and not a value: it must
// acknowledge, and then keep writing after this worker has returned its
// admission slot. Everything dispatch returns is finished by the time it
// returns it.
func (s *Server) serveRequest(ctx context.Context, run *runState, request requestLine, lines chan<- outLine) {
	if request.Op == opEvents {
		s.serveEvents(ctx, run, request, lines)
		return
	}
	result, werr := s.dispatch(ctx, request)
	s.respond(ctx, lines, request, result, werr)
}

// respond writes one op's correlated response line. A response whose
// encoding exceeds the frame limit is replaced by the bounded
// response_too_large refusal, so an oversized result still gets a
// correlated, framable answer.
// It reports whether the host's own answer reached the writer, which the
// events op needs: a subscription whose acknowledgement was never queued
// would stream envelopes correlated to a request the host has no answer for.
func (s *Server) respond(ctx context.Context, lines chan<- outLine, request requestLine, result json.RawMessage, werr *wireError) bool {
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
			return false
		}
		fallback := responseLine{ID: *request.ID, OK: false, Result: json.RawMessage("null"), Error: &wireError{
			Code: "response_too_large", Message: "the encoded response exceeds the frame limit",
		}}
		if fallbackErr := s.send(ctx, lines, fallback); fallbackErr != nil {
			s.logger.Printf("servestdio: response %d: %v", *request.ID, fallbackErr)
		}
		return false
	}
	return true
}

func (s *Server) dispatch(ctx context.Context, request requestLine) (json.RawMessage, *wireError) {
	switch request.Op {
	case opOpen:
		if werr := request.only(paramAdapter, paramRequest); werr != nil {
			return nil, werr
		}
		if request.Adapter == "" {
			return nil, &wireError{Code: "invalid_request", Message: "adapter is required"}
		}
		return s.openOp(ctx, request)
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

// openOp opens a session on a named adapter, the stdio form of POST
// /adapters/{name}/sessions. The admission it performs is the HTTP route's,
// through the same serve.AttachmentGate and serve.ResolveAttachments, so an
// attaching open is refused for the same reason over either transport and a
// stdio peer gets the daemon's credential rule rather than an exemption the
// pipe never earned.
//
// The endpoint's own disclosure is consulted before the daemon's constraints,
// which is the refusal ladder the HTTP route establishes: telling a caller to
// name an operator-configured source, when the endpoint cannot attach sources
// at all, answers a question it never asked and hides the one it did.
//
// An oversized response takes submitOp's rule, applied to the fact this op
// leaves behind. A session the host cannot learn the id of is not an
// acknowledgement it can act on, so it is rolled back — closed on a context
// the request's own end cannot cut short. A session whose id the host itself
// supplied is not unknowable: the generic refusal stays honest, the session
// stays open, and the host can name it to close or to read its state. The
// distinction is not a special case for open; it is the line resolve, cancel
// and close already sit on the other side of.
func (s *Server) openOp(ctx context.Context, request requestLine) (json.RawMessage, *wireError) {
	envelope, werr := s.gateRequest(request.Request, protocol.TypeSessionOpenRequest)
	if werr != nil {
		return nil, werr
	}
	var payload protocol.SessionOpenRequest
	if err := envelope.DecodePayload(&payload); err != nil {
		return nil, &wireError{Code: "invalid_payload", Message: trimMessage(err.Error())}
	}
	if _, found := s.hub.Registry().Lookup(request.Adapter); !found {
		return nil, &wireError{Code: "unknown_adapter", Message: fmt.Sprintf("no adapter %q", trimMessage(request.Adapter))}
	}
	revision, refusal := serve.AttachmentGate(ctx, s.hub, request.Adapter, envelope.CapabilityRevision, payload)
	if refusal != nil {
		var stale *serve.StaleRevisionError
		if errors.As(refusal, &stale) {
			return nil, &wireError{Code: "stale_capabilities", Message: stale.Error(), Details: map[string]any{
				"expected_revision": stale.Expected, "current_revision": stale.Current,
			}}
		}
		code, message, details, typed := serve.ControlRefusal(refusal)
		if !typed {
			// The descriptor could not be read, so no rung has an answer and
			// none is invented: the open fails rather than falling through to
			// a constraint that would answer the wrong question.
			return nil, &wireError{Code: "probe_failed", Message: adapterMessage(refusal)}
		}
		return nil, &wireError{Code: code, Message: message, Details: details}
	}
	attachments, unresolvable := serve.ResolveAttachments(s.hub, payload.ToolSources)
	if unresolvable != nil {
		return nil, &wireError{Code: "unsupported_feature", Message: unresolvable.Error(), Details: map[string]any{
			"feature": protocol.FeatureToolSourcesAttach, "reason": base.ControlUnsatisfiable, "source": unresolvable.Source,
		}}
	}
	open := base.OpenRequest{
		SessionID:             payload.SessionID,
		Participant:           protocol.Participant{ID: serve.DefaultParticipant},
		AllowDegradedFeatures: payload.AllowDegradedFeatures,
		ToolSources:           attachments,
	}
	if payload.Metadata != nil {
		open.Metadata = make(map[string]any, len(payload.Metadata))
		for key, raw := range payload.Metadata {
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, &wireError{Code: "invalid_payload", Message: fmt.Sprintf("metadata %q: %v", trimMessage(key), trimMessage(err.Error()))}
			}
			open.Metadata[key] = value
		}
	}
	entry, state, err := s.hub.Open(ctx, request.Adapter, open)
	if err != nil {
		// A capability the open elected and the adapter refused is reported
		// under its own typed code with the details that say what to change —
		// the same mapping a refused run control takes — so an open refused
		// for an attachment tells the caller which source to drop rather than
		// only that the open failed.
		if code, message, details, ok := serve.ControlRefusal(err); ok {
			return nil, &wireError{Code: code, Message: message, Details: details}
		}
		code := "open_failed"
		switch {
		case errors.Is(err, serve.ErrUnknownAdapter):
			code = "unknown_adapter"
		case errors.Is(err, serve.ErrSessionExists):
			code = "session_exists"
		}
		return nil, &wireError{Code: code, Message: adapterMessage(err)}
	}
	// The response is built from the state the open itself confirmed:
	// re-reading state here could fail after registration (an abandoned
	// request context, a flaky adapter probe) and report a successful open as
	// a failure while the session stays live in the hub. The state goes on the
	// wire whole, for the reason the HTTP route names — an adapter that
	// reports the session's model at open, the first snapshot a control layer
	// sees, has it discarded by any hand-copied subset.
	response, err := protocol.NewEnvelope(protocol.TypeSessionOpenResponse, protocol.EnvelopeID(s.nextID("response")), state)
	if err != nil {
		return nil, internalError(err)
	}
	response.InReplyTo = envelope.ID
	response.SessionID = state.SessionID
	// The response carries the revision the open was admitted under, which is
	// what the profile asks for in both directions: a pinned request has its
	// revision repeated — the gate verified it is the current one, so the
	// probed value and the caller's are the same string — and an unpinned one
	// is given the revision used for admission, which is how a control layer
	// detects that it was admitted under a newer snapshot than the one it last
	// read. An open that attaches nothing is not probed and keeps the caller's
	// own value.
	response.CapabilityRevision = envelope.CapabilityRevision
	if revision != "" {
		response.CapabilityRevision = revision
	}
	result, werr := envelopeResult(response)
	if werr != nil {
		return nil, werr
	}
	if !s.fits(responseLine{ID: *request.ID, OK: true, Result: result}) {
		return nil, s.refuseOversizedOpen(ctx, request, entry, payload.SessionID != "")
	}
	return result, nil
}

// refuseOversizedOpen answers an open whose response cannot be framed, and
// rolls the session back when the host could not name it.
//
// The rollback runs detached from the request's own end — custody — but
// bounded by the shutdown window, so a hung adapter cannot wedge the read loop
// past every bound the frontend promises. A session opened by this op has no
// run of its own to settle first, so Close is the whole rollback: what it
// reports is whether the session is gone, not whether some effect raced it.
//
// The refusal messages are fixed-size, so they stay framable even at the frame
// limit's floor; the underlying error goes to the logger, never the line.
func (s *Server) refuseOversizedOpen(ctx context.Context, request requestLine, entry *serve.Session, named bool) *wireError {
	if named {
		return &wireError{Code: "response_too_large", Message: "the open response exceeds the frame limit; the session is open under the session_id the request supplied"}
	}
	rollback, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), s.shutdown)
	err := entry.Close(rollback)
	cancelRollback()
	if err == nil || errors.Is(err, base.ErrSessionClosed) {
		return &wireError{Code: "response_too_large", Message: "the open response exceeds the frame limit; the session was rolled back"}
	}
	s.logger.Printf("servestdio: roll back open %d: %v", *request.ID, err)
	return &wireError{Code: "response_too_large", Message: "the open response exceeds the frame limit; rolling the session back failed and it may still be live"}
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

// serveEvents subscribes to a session's events, the stdio form of
// GET /sessions/{id}/events. The op answers once — null, the acknowledgement
// the SSE route gives by starting its response with no body — and the
// envelopes follow as their own lines, each tagged with this request's id so
// overlapping subscriptions on one session stay attributable.
//
// The acknowledgement is queued before the pump starts, so a host never sees
// an envelope for a subscription it has not been told exists. The single
// ordered writer makes queueing order the wire order, so this is a guarantee
// and not a race the host has to tolerate.
//
// Everything that can refuse the subscription is decided here, on the worker,
// so the refusal is the op's own answer rather than a signal arriving after a
// success. The one exception is the replay gap, which is a successful op that
// reports what it cannot deliver — the SSE route answers 200 and then emits
// the gap event, and this is that shape in this framing.
func (s *Server) serveEvents(ctx context.Context, run *runState, request requestLine, lines chan<- outLine) {
	id := *request.ID
	fail := func(werr *wireError) { s.respond(ctx, lines, request, nil, werr) }
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
		// The wire cursor carries only a sequence: the daemon resolves it
		// onto the session's current run, exactly as the SSE route resolves
		// a bare Last-Event-ID.
		after, err := strconv.ParseUint(string(request.After), 10, 64)
		if err != nil {
			fail(&wireError{Code: "invalid_cursor", Message: fmt.Sprintf("cursor %s is not an unsigned sequence", strconv.Quote(trimMessage(string(request.After))))})
			return
		}
		options = append(options, serve.After("", after))
	}
	// Attaching before subscribing, rather than after, so the acknowledgement
	// never promises a subscription teardown would not let start — and so no
	// subscription is opened on the hub for a pump that cannot run.
	pumps, attached := run.attach()
	if !attached {
		fail(&wireError{Code: "request_cancelled", Message: "shutdown began before the subscription started"})
		return
	}
	// The subscription takes the pump's context and not this worker's. It is
	// what Subscription.Next blocks on, and its own contract says a blocked
	// consumer is stopped by cancelling that context and never by racing
	// Close against it, so this is the handle teardown pulls.
	subscription, err := s.hub.Subscribe(pumps, entry.ID(), options...)
	var gap *base.ReplayGap
	if errors.As(err, &gap) {
		run.detach()
		if !s.respond(ctx, lines, request, json.RawMessage("null"), nil) {
			return
		}
		if sendErr := s.send(ctx, lines, gapLine{
			Event: signalReplayGap, ID: id, SessionID: string(entry.ID()),
			RequestedAfter: gap.RequestedAfter, OldestAvailable: gap.OldestAvailable, LatestAvailable: gap.LatestAvailable,
			Message: "requested replay cursor is no longer retained; resume with a cursor at or after oldest_available - 1",
		}); sendErr != nil {
			s.logger.Printf("servestdio: subscription %d: %v", id, sendErr)
		}
		return
	}
	if err != nil {
		run.detach()
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
	if !s.respond(ctx, lines, request, json.RawMessage("null"), nil) {
		// The host has no answer for this request, so it can attribute
		// nothing that follows. The subscription is detached rather than
		// pumped into an output that already failed the acknowledgement.
		run.detach()
		subscription.Close()
		return
	}
	go func() {
		defer run.detach()
		s.pump(pumps, entry, subscription, id, lines)
	}()
}

// pump serves one subscription through the shared writer until it ends. The
// envelope lines preserve the subscription's per-session order and carry the
// events request's id; the single writer guarantees each line is atomic.
//
// Ends mirror the SSE stream's, which is what parity here means: the overflow
// and replay-gap signals get named lines, a subscription that ends because
// the session closed under it gets the session-closed line, and the clean end
// at a run's terminal event produces no line — the terminal envelope
// (run.completed / run.failed / run.cancelled) is the marker, exactly as the
// SSE response simply ends after it.
//
// Any other end — a failed run stream, the frontend's own teardown — also
// produces no line, and the documented recovery is a fresh events op with an
// after cursor. An envelope whose line exceeds the frame limit ends the
// subscription the same way, but says so: this framing cannot carry it, and
// skipping it would leave a sequence hole a later cursor would double-count.
func (s *Server) pump(ctx context.Context, entry *serve.Session, subscription *serve.Subscription, id int64, lines chan<- outLine) {
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
					// The session ended under the subscription. There is no
					// output left to explain that in, and the size is not
					// what stopped it.
					return
				}
				s.endAtFrameLimit(ctx, entry, envelope, id, lines)
				return
			}
			continue
		}
		var overflow *serve.OverflowError
		if errors.As(err, &overflow) {
			if sendErr := s.send(ctx, lines, overflowLine{
				Event: signalOverflow, ID: id, SessionID: string(entry.ID()),
				RunID:        string(overflow.RunID),
				LastSequence: overflow.LastSequence,
				Message:      "event stream consumer fell behind; resume with a cursor after this sequence",
			}); sendErr != nil {
				s.logger.Printf("servestdio: subscription %d: %v", id, sendErr)
			}
			return
		}
		if errors.Is(err, io.EOF) && entry.IsClosed() {
			if sendErr := s.send(ctx, lines, sessionClosedLine{
				Event: signalSessionClosed, ID: id, SessionID: string(entry.ID()),
				Message: "the session is closed",
			}); sendErr != nil {
				s.logger.Printf("servestdio: subscription %d: %v", id, sendErr)
			}
		}
		return
	}
}

// endAtFrameLimit terminates a subscription this framing cannot carry past,
// naming the position a cursor resumes after.
//
// The full signal carries adapter-minted identifiers, which can themselves
// push it past the very limit that ended the subscription. The minimal form
// is what the frame-limit floor guarantees fits, so it is what a failed full
// form falls back to: the sequence is the one fact a host needs to resume,
// and losing the terminal signal entirely would leave the host waiting on a
// subscription that has already stopped.
func (s *Server) endAtFrameLimit(ctx context.Context, entry *serve.Session, envelope protocol.Envelope, id int64, lines chan<- outLine) {
	sequence := uint64(0)
	if envelope.Sequence != nil {
		sequence = *envelope.Sequence
	}
	if err := s.send(ctx, lines, frameLimitLine{
		Event: signalFrameLimit, ID: id, SessionID: string(entry.ID()),
		RunID: string(envelope.RunID), Sequence: sequence,
		Message: "envelope exceeds the frame limit; the subscription ended — resume with a cursor after this sequence to continue past it",
	}); err == nil {
		return
	} else if !errors.Is(err, ErrLineTooLarge) {
		s.logger.Printf("servestdio: subscription %d: %v", id, err)
		return
	}
	if err := s.send(ctx, lines, frameLimitLine{Event: signalFrameLimit, ID: id, Sequence: sequence}); err != nil {
		s.logger.Printf("servestdio: subscription %d: %v", id, err)
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

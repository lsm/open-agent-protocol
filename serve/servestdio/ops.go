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
	"sync"

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
	opState        = "state"
	opSubmit       = "submit"
	opResolve      = "resolve"
	opCancel       = "cancel"
	opClose        = "close"
)

// Signal line names. The first two mirror the SSE named events verbatim; the
// session-closed line is stdio's counterpart of the SSE response simply
// ending when a session closes under an open stream — a shared stdout has no
// per-subscription connection close to observe, so the end is signalled.
const (
	signalEnvelope      = "envelope"
	signalOverflow      = "oap-overflow"
	signalReplayGap     = "oap-replay-gap"
	signalSessionClosed = "oap-session-closed"
)

// responseLine is one daemon → host response, correlated by the request id.
// Result is the HTTP route's success body verbatim — an OAP response envelope
// for the envelope exchanges, the plain JSON document for the listings — and
// the JSON null for the routes HTTP answers without a body (close, events).
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
// data:/id: pair: the envelope JSON with its sequence alongside it.
type envelopeLine struct {
	Event     string          `json:"event"`
	SessionID string          `json:"session_id"`
	Sequence  *uint64         `json:"sequence,omitempty"`
	Envelope  json.RawMessage `json:"envelope"`
}

type overflowLine struct {
	Event        string `json:"event"`
	SessionID    string `json:"session_id"`
	RunID        string `json:"run_id"`
	LastSequence uint64 `json:"last_sequence"`
	Message      string `json:"message"`
}

type gapLine struct {
	Event           string `json:"event"`
	SessionID       string `json:"session_id"`
	RequestedAfter  uint64 `json:"requested_after"`
	OldestAvailable uint64 `json:"oldest_available"`
	LatestAvailable uint64 `json:"latest_available"`
	Message         string `json:"message"`
}

type sessionClosedLine struct {
	Event     string `json:"event"`
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

// serveRequest executes one op and writes its response. The execution ops
// run concurrently, one goroutine per request, exactly as the HTTP server
// runs one handler per connection; the registration ops reach here through
// the read loop instead (see decodeLoop).
func (s *Server) serveRequest(ctx context.Context, request requestLine, lines chan<- []byte) {
	result, werr := s.dispatch(ctx, request)
	s.respond(lines, request, result, werr)
}

// respond writes one op's correlated response line.
func (s *Server) respond(lines chan<- []byte, request requestLine, result json.RawMessage, werr *wireError) {
	response := responseLine{ID: *request.ID, OK: werr == nil, Result: result}
	if werr != nil {
		response.Result = json.RawMessage("null")
		response.Error = werr
	}
	s.send(lines, response)
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
	case opOpen:
		if werr := request.only(paramAdapter, paramRequest); werr != nil {
			return nil, werr
		}
		if request.Adapter == "" {
			return nil, &wireError{Code: "invalid_request", Message: "adapter is required"}
		}
		return s.openOp(ctx, request)
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
		// opEvents and opOpen never reach here concurrently: decodeLoop
		// routes them through its synchronous registration path.
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

// only refuses a well-formed line that carries params its op does not define:
// the protocol is closed, and speaking the wrong shape is a request error the
// host can correct — unlike an unknown field, which fails the whole frontend
// closed.
func (request requestLine) only(fields ...string) *wireError {
	allowed := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field] = true
	}
	var extra []string
	if !allowed[paramAdapter] && request.Adapter != "" {
		extra = append(extra, paramAdapter)
	}
	if !allowed[paramSession] && request.SessionID != "" {
		extra = append(extra, paramSession)
	}
	if !allowed[paramAfter] && len(request.After) > 0 && string(request.After) != "null" {
		extra = append(extra, paramAfter)
	}
	if !allowed[paramRequest] && len(request.Request) > 0 && string(request.Request) != "null" {
		extra = append(extra, paramRequest)
	}
	if len(extra) == 0 {
		return nil
	}
	return &wireError{Code: "invalid_request", Message: fmt.Sprintf("op %q accepts no %s parameter", request.Op, strings.Join(extra, ", "))}
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

// --- OAP operations ---

func (s *Server) openOp(ctx context.Context, request requestLine) (json.RawMessage, *wireError) {
	envelope, werr := s.gateRequest(request.Request, protocol.TypeSessionOpenRequest)
	if werr != nil {
		return nil, werr
	}
	var payload protocol.SessionOpenRequest
	if err := envelope.DecodePayload(&payload); err != nil {
		return nil, &wireError{Code: "invalid_payload", Message: trimMessage(err.Error())}
	}
	name := request.Adapter
	if _, found := s.hub.Registry().Lookup(name); !found {
		return nil, &wireError{Code: "unknown_adapter", Message: fmt.Sprintf("no adapter %q", name)}
	}
	open := base.OpenRequest{SessionID: payload.SessionID, Participant: protocol.Participant{ID: serve.DefaultParticipant}}
	if payload.Metadata != nil {
		open.Metadata = make(map[string]any, len(payload.Metadata))
		for key, raw := range payload.Metadata {
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, &wireError{Code: "invalid_payload", Message: fmt.Sprintf("metadata %q: %v", key, err)}
			}
			open.Metadata[key] = value
		}
	}
	_, state, err := s.hub.Open(ctx, name, open)
	if err != nil {
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
	// re-reading state here could fail after registration and report a
	// successful open as failed while the session stays live in the hub.
	response, err := protocol.NewEnvelope(protocol.TypeSessionOpenResponse, protocol.EnvelopeID(s.nextID("response")), protocol.SessionOpenResponse{
		SessionID: state.SessionID, Status: state.Status,
	})
	if err != nil {
		return nil, internalError(err)
	}
	response.InReplyTo = envelope.ID
	response.SessionID = state.SessionID
	response.CapabilityRevision = envelope.CapabilityRevision
	return envelopeResult(response)
}

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
	return envelopeResult(response)
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
		message := fmt.Sprintf("payload session_id %q does not match the addressed session %q", payload.SessionID, entry.ID())
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

// serveEvents executes one `events` op. decodeLoop calls it synchronously, so
// the subscription is registered before the next line is read and a pipelined
// submit cannot race ahead of it. The cursor mirrors the HTTP surface: a bare
// sequence resolved onto the session's current run.
func (s *Server) serveEvents(ctx context.Context, request requestLine, lines chan<- []byte, work *sync.WaitGroup) {
	id := *request.ID
	fail := func(werr *wireError) {
		s.send(lines, responseLine{ID: id, OK: false, Result: json.RawMessage("null"), Error: werr})
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
		s.send(lines, responseLine{ID: id, OK: true, Result: json.RawMessage("null")})
		s.send(lines, gapLine{
			Event: signalReplayGap, SessionID: string(entry.ID()),
			RequestedAfter: gap.RequestedAfter, OldestAvailable: gap.OldestAvailable, LatestAvailable: gap.LatestAvailable,
			Message: "requested replay cursor is no longer retained; resume with a cursor at or after oldest_available - 1",
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
	s.send(lines, responseLine{ID: id, OK: true, Result: json.RawMessage("null")})
	work.Add(1)
	go func() {
		defer work.Done()
		s.pump(entry, subscription, lines)
	}()
}

// pump serves one subscription through the shared writer until it ends. The
// envelope lines preserve the subscription's per-session order; the single
// writer guarantees each line is atomic. Ends mirror the SSE stream ends: the
// overflow and replay-gap signals get named lines, a subscription that ends
// because the session closed under it gets the session-closed line, and the
// clean end at a run's terminal event produces no line — the terminal
// envelope (run.completed / run.failed / run.cancelled) is the marker, exactly
// as the SSE response simply ends. Any other end (a failed run stream, the
// frontend's own shutdown) also produces no line; the documented recovery is
// a fresh events op with an after cursor.
func (s *Server) pump(entry *serve.Session, subscription *serve.Subscription, lines chan<- []byte) {
	defer subscription.Close()
	for {
		envelope, err := subscription.Next()
		if err == nil {
			data, marshalErr := json.Marshal(envelope)
			if marshalErr != nil {
				s.logger.Printf("servestdio: encode envelope: %v", marshalErr)
				return
			}
			s.send(lines, envelopeLine{
				Event: signalEnvelope, SessionID: string(entry.ID()),
				Sequence: envelope.Sequence, Envelope: data,
			})
			continue
		}
		var overflow *serve.OverflowError
		if errors.As(err, &overflow) {
			s.send(lines, overflowLine{
				Event: signalOverflow, SessionID: string(entry.ID()),
				RunID:        string(overflow.RunID),
				LastSequence: overflow.LastSequence,
				Message:      "event stream consumer fell behind; resume with a cursor after this sequence",
			})
			return
		}
		if errors.Is(err, io.EOF) && entry.IsClosed() {
			s.send(lines, sessionClosedLine{
				Event: signalSessionClosed, SessionID: string(entry.ID()),
				Message: "the session is closed",
			})
		}
		return
	}
}

// --- request gate and encoding helpers ---

// gateRequest parses one op's request envelope, validates it against the
// bundled OAP envelope schema, and checks its type against the op — the same
// gate every HTTP route applies, rejections mapped to the same codes.
func (s *Server) gateRequest(payload json.RawMessage, want ...protocol.EnvelopeType) (protocol.Envelope, *wireError) {
	if len(payload) == 0 || string(payload) == "null" {
		return protocol.Envelope{}, &wireError{Code: "invalid_request", Message: "the op requires a \"request\" envelope"}
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
		return nil, &wireError{Code: "unknown_session", Message: err.Error()}
	}
	return entry, nil
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

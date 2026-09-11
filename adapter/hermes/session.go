package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/hermes/internal/native"
	"github.com/lsm/open-agent-protocol/adapter/hermes/internal/rpc"
	"github.com/lsm/open-agent-protocol/protocol"
)

const streamCapacity = 64

var errTerminalWon = errors.New("hermes adapter: terminal already selected")
var errUnavailable = errors.New("hermes adapter: operation unavailable")

type Session struct {
	mu           sync.Mutex
	reduceMu     sync.Mutex
	promptMu     sync.Mutex
	client       Client
	inbound      <-chan rpc.InboundMessage
	clock        base.Clock
	ids          base.IDGenerator
	capacity     int
	nativeID     string
	participant  protocol.ParticipantID
	state        protocol.SessionState
	closed       bool
	unusable     bool
	pending      *runState
	active       *runState
	runs         map[protocol.RunID]*runState
	tools        map[string]*toolState
	interactions map[protocol.InteractionID]*inputState
	journal      []protocol.Envelope
	lastSeq      int64
	stop         chan struct{}
	stopOnce     sync.Once
}

// runState is reduceMu-domain except terminal/subscribers/started, which are
// mu-domain like the DeepSeek reducer.
type runState struct {
	id       protocol.RunID
	status   protocol.RunStatus
	next     uint64
	started  bool
	terminal bool
	// accepted records the {status: streaming} response; openSeen records
	// the message.start frame. The run starts when both are observed — in
	// either wire order. Run-scoped observations arriving before the
	// convergence point are buffered in wire order and replayed at start:
	// the response barrier orders only wire-earlier events, so a piped
	// burst can deliver turn frames around the response.
	accepted      bool
	openSeen      bool
	buffered      []native.Event
	submittedText string
	messageID     protocol.MessageID
	final         *native.MessageCompletePayload
	terminalKind  string
	// deferred holds an absorbing settlement that arrived while a gate
	// resolution was in flight; Resolve flushes it after publishing the
	// canonical resolution event.
	deferred    *native.MessageCompletePayload
	subscribers []chan base.Result
	startResult chan error
	startOnce   sync.Once
}

type toolState struct {
	nativeID  string
	id        protocol.ToolCallID
	run       *runState
	name      string
	args      json.RawMessage
	requested protocol.EnvelopeID
	started   protocol.EnvelopeID
	terminal  bool
}

// inputState is one native gate (approval/clarify/sudo/secret) surfaced as an
// OAP input interaction.
type inputState struct {
	id        protocol.InteractionID
	kind      string // approval | clarify | sudo | secret
	requestID string // native _block request_id (empty for approvals)
	run       *runState
	questions []protocol.InputQuestion
	resolved  bool
	// settling is set while Resolve is issuing the native answer and before the
	// canonical resolution event is published. An absorbing settlement is
	// parked behind a settling gate so the resolution cannot lose to terminality.
	settling bool
	// answers maps question id → native answer member.
	answers   map[string]string
	requested protocol.EnvelopeID
}

func (r *runState) signalStart(err error) {
	r.startOnce.Do(func() { r.startResult <- err; close(r.startResult) })
}

func (s *Session) Submit(ctx context.Context, req protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	if err := ctx.Err(); err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}
	text, err := submitText(req)
	if err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}
	// promptMu and reduceMu make reservation, response settlement, and the
	// message.start ownership handshake one serialization domain.
	s.promptMu.Lock()
	s.reduceMu.Lock()
	s.mu.Lock()
	if s.closed || s.unusable {
		s.mu.Unlock()
		s.reduceMu.Unlock()
		s.promptMu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, base.ErrSessionClosed
	}
	if req.SessionID != s.state.SessionID {
		s.mu.Unlock()
		s.reduceMu.Unlock()
		s.promptMu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, base.ErrRunNotFound
	}
	if s.pending != nil || (s.active != nil && !s.active.terminal) || s.state.Status != protocol.SessionIdle {
		s.mu.Unlock()
		s.reduceMu.Unlock()
		s.promptMu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, base.ErrRunActive
	}
	run := &runState{status: protocol.RunQueued, next: 1, submittedText: text, messageID: protocol.MessageID(s.ids.NewID("message")), startResult: make(chan error, 1)}
	stream := make(chan base.Result, streamCapacity+1)
	run.subscribers = []chan base.Result{stream}
	s.pending = run
	s.mu.Unlock()
	s.reduceMu.Unlock()
	var result native.PromptSubmitResult
	callDone := make(chan error, 1)
	go func() {
		callDone <- s.client.Call(ctx, native.MethodPromptSubmit, native.PromptSubmitParams{SessionID: s.nativeID, Text: text}, &result)
	}()
	err = <-callDone
	s.reduceMu.Lock()
	if err != nil || result.Status != native.SubmitStreaming {
		if err == nil {
			// A busy status (steered/redirected/queued), voice stop, or
			// turn-isolation surprise: the adapter never requests any of
			// these, so the native state diverged from the contract.
			err = fmt.Errorf("%w: prompt.submit returned status %q", ErrNativeProtocol, result.Status)
		}
		s.reduceMu.Unlock()
		s.promptMu.Unlock()
		s.abortPreStart(run, err)
		return protocol.MessageSubmitResponse{}, nil, err
	}
	// The response barriers behind wire-earlier events, so everything the
	// gateway emitted before answering has already been reduced.
	s.mu.Lock()
	run.accepted = true
	openSeen := run.openSeen
	s.mu.Unlock()
	if openSeen {
		s.startRun(run)
	}
	s.reduceMu.Unlock()
	s.promptMu.Unlock()
	select {
	case startErr := <-run.startResult:
		if startErr != nil {
			return protocol.MessageSubmitResponse{}, nil, startErr
		}
	case <-ctx.Done():
		s.reduceMu.Lock()
		if !run.started && !run.terminal {
			// The gateway already answered prompt.submit with status streaming, so
			// native acceptance is confirmed and the run is authoritative.
			// Cancellation is now ambiguous to this caller; keep the reservation
			// alive for the reducer to settle rather than reverting the session to
			// idle while the accepted native turn may still execute.
			s.reduceMu.Unlock()
			return protocol.MessageSubmitResponse{}, nil, ctx.Err()
		}
		s.reduceMu.Unlock()
		startErr := <-run.startResult
		if startErr != nil {
			return protocol.MessageSubmitResponse{}, nil, startErr
		}
	}
	return protocol.MessageSubmitResponse{SessionID: req.SessionID, Accepted: true, SubmissionID: protocol.SubmissionID(run.messageID), RequestedDelivery: protocol.DeliveryAuto, EffectiveDelivery: protocol.DeliveryStart, DeliveryResolution: "session_idle", Admission: protocol.AdmissionStarted, RunID: run.id, Status: protocol.RunRunning, ModelID: s.state.CurrentModelID, MessageIDs: []protocol.MessageID{run.messageID}}, stream, nil
}

// submitText validates the conservative v1 surface: one user message whose
// content is text or text parts.
func submitText(req protocol.MessageSubmitRequest) (string, error) {
	if req.SessionID == "" || len(req.Messages) != 1 || (req.Delivery != "" && req.Delivery != protocol.DeliveryAuto) || req.Instructions != "" || len(req.ToolChoice) > 0 || len(req.OutputSchema) > 0 {
		return "", base.ErrInvalidSubmission
	}
	if req.ModelID != "" {
		// prompt.submit carries only the session id and text, so a per-submit
		// model cannot be applied; rejecting beats silently running the
		// preconfigured model.
		return "", fmt.Errorf("%w: Hermes fixes a model at session creation", base.ErrUnsupportedInput)
	}
	message := req.Messages[0]
	if message.Role != protocol.RoleUser {
		return "", base.ErrInvalidSubmission
	}
	if text, ok := message.Content.Text(); ok {
		return text, nil
	}
	parts, ok := message.Content.Parts()
	if !ok {
		return "", base.ErrInvalidSubmission
	}
	var builder strings.Builder
	for i, part := range parts {
		if part.Type != protocol.ContentText {
			return "", base.ErrInvalidSubmission
		}
		if i > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(part.Text)
	}
	return builder.String(), nil
}

func (s *Session) dispatch() {
	for {
		select {
		case in := <-s.inbound:
			s.reduceMu.Lock()
			s.reduce(in)
			s.reduceMu.Unlock()
		case <-s.client.Done():
			// The inbound stream's owner (the factory relay on the process
			// path, the harness fake in tests) closes the channel once every
			// already-routed observation is forwarded. Draining to that close
			// before settling makes transport-death ordering deterministic:
			// the failure terminal always follows the full ordered evidence.
			for {
				var in rpc.InboundMessage
				var ok bool
				select {
				case in, ok = <-s.inbound:
				case <-s.stop:
					return
				}
				if !ok {
					s.reduceMu.Lock()
					s.transportFailed()
					s.reduceMu.Unlock()
					return
				}
				s.reduceMu.Lock()
				s.reduce(in)
				s.reduceMu.Unlock()
			}
		case <-s.stop:
			return
		}
	}
}

func (s *Session) reduce(in rpc.InboundMessage) {
	if in.Barrier != nil {
		close(in.Barrier)
		return
	}
	if in.Request != nil {
		s.foreignActivity("reverse request")
		return
	}
	if in.Notification != nil {
		event, ok := in.Notification.Value.(*native.Event)
		if !ok {
			s.foreignActivity("non-event notification")
			return
		}
		s.applyEvent(event)
	}
}

func (s *Session) applyEvent(event *native.Event) {
	if event.SessionID == "" {
		// Session-less frames (globals, ready echoes) carry no seq and no
		// per-session semantics; they are ignorable by contract.
		return
	}
	if event.SessionID != s.nativeID {
		s.foreignActivity(fmt.Sprintf("event for foreign session %q", event.SessionID))
		return
	}
	// The dedicated stream must be contiguous: the seq is stamped under one
	// lock at the gateway's single write choke point.
	if event.Seq != s.lastSeq+1 {
		s.foreignActivity(fmt.Sprintf("non-contiguous seq %d after %d", event.Seq, s.lastSeq))
		return
	}
	s.lastSeq = event.Seq

	s.mu.Lock()
	run := s.pending
	if run == nil {
		run = s.active
	}
	unusable := s.unusable
	terminal := run != nil && run.terminal
	s.mu.Unlock()
	if unusable {
		return
	}
	if run == nil {
		if !native.IsRunScoped(event.Type) {
			// Post-settlement corroboration (settled session.info, status
			// update, usage ticks, trailing subagent frames): the pin
			// guarantees these after message.complete, and the terminal
			// cleanup already released the run, so an idle session may
			// legitimately observe them.
			return
		}
		// The only remaining session-scoped observations on a dedicated
		// session with no reserved run are turns the native opened on its own
		// (queued drain, auto-continue, loop wakeup) — foreign activity.
		s.foreignActivity(fmt.Sprintf("session event %q without a reserved run", event.Type))
		return
	}
	if terminal {
		// Post-terminal frames (settled session.info, status.update) are
		// corroboration; anything run-scoped is impossible after settlement.
		return
	}
	if !run.started {
		s.reserveObservation(run, event)
		return
	}
	s.applyRunEvent(run, event)
}

// reserveObservation applies one run-scoped observation against a reserved,
// not-yet-started run: message.start converges the opening handshake, every
// other observation buffers in wire order for replay at startRun.
func (s *Session) reserveObservation(run *runState, event *native.Event) {
	if event.Type == native.EventMessageStart {
		s.mu.Lock()
		already := run.openSeen
		run.openSeen = true
		accepted := run.accepted
		s.mu.Unlock()
		if already {
			s.failRun(run, "hermes_invalid_grammar", "turn opened twice")
			return
		}
		if accepted {
			s.startRun(run)
		}
		return
	}
	run.buffered = append(run.buffered, *event)
}

// applyRunEvent reduces one run-scoped observation for a started run. The
// per-session seq fence and run lookup happen in applyEvent; replayed buffer
// entries re-enter here without re-fencing.
func (s *Session) applyRunEvent(run *runState, event *native.Event) {
	switch event.Type {
	case native.EventMessageStart:
		s.mu.Lock()
		already := run.openSeen
		run.openSeen = true
		accepted := run.accepted
		s.mu.Unlock()
		if already {
			s.failRun(run, "hermes_invalid_grammar", "turn opened twice")
			return
		}
		if !run.started && accepted {
			s.startRun(run)
		}
	case native.EventMessageComplete:
		var payload native.MessageCompletePayload
		if err := native.DecodeStrict(event.Payload, &payload); err != nil {
			s.failRun(run, "hermes_invalid_event", "invalid settlement")
			return
		}
		s.settleRun(run, &payload)
	case native.EventMessageDelta:
		s.emitDelta(run, event, protocol.ContentText)
	case native.EventReasoningDelta, native.EventThinkingDelta:
		s.emitDelta(run, event, protocol.ContentReasoning)
	case native.EventToolStart:
		var payload native.ToolStartPayload
		if err := native.DecodeStrict(event.Payload, &payload); err != nil {
			s.failRun(run, "hermes_invalid_event", "invalid tool start")
			return
		}
		s.startTool(run, &payload)
	case native.EventToolComplete:
		var payload native.ToolCompletePayload
		if err := native.DecodeStrict(event.Payload, &payload); err != nil {
			s.failRun(run, "hermes_invalid_event", "invalid tool complete")
			return
		}
		s.endTool(run, &payload)
	case native.EventApprovalRequest, native.EventClarifyRequest, native.EventSudoRequest, native.EventSecretRequest:
		s.openInteraction(run, event)
	case native.EventSecretExpire, native.EventSudoExpire, native.EventClarifyExpire:
		var payload native.ExpirePayload
		if err := native.DecodeStrict(event.Payload, &payload); err != nil {
			s.failRun(run, "hermes_invalid_event", "invalid expire")
			return
		}
		s.expireInteraction(run, &payload)
	case native.EventError:
		// Generic, non-settlement error surface: observed only.
	default:
		// Observed-only types (interim commentary, usage ticks, session.info,
		// subagent parent frames, status updates) carry no run projection.
	}
}

func (s *Session) startRun(run *runState) {
	s.mu.Lock()
	if run.started || run.terminal {
		s.mu.Unlock()
		return
	}
	run.started = true
	run.status = protocol.RunRunning
	run.id = protocol.RunID(s.ids.NewID("run"))
	s.pending = nil
	s.active = run
	s.runs[run.id] = run
	s.state.Status = protocol.SessionRunning
	s.state.ActiveRunID = run.id
	replay := run.buffered
	run.buffered = nil
	s.mu.Unlock()
	if err := s.emit(run, protocol.TypeRunStarted, protocol.RunStartedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunRunning, ModelID: s.state.CurrentModelID, StartedAtMS: s.clock.Now().UnixMilli()}, false); err != nil {
		run.signalStart(err)
		return
	}
	run.signalStart(nil)
	// Observations buffered before the convergence point reduce now, in wire
	// order. A replayed settlement is terminal: stop draining.
	for _, event := range replay {
		if run.terminal {
			return
		}
		s.applyRunEvent(run, &event)
	}
}

func (s *Session) emitDelta(run *runState, event *native.Event, kind protocol.ContentPartType) {
	if !run.started || run.terminal {
		return
	}
	var payload native.DeltaPayload
	if err := native.DecodeStrict(event.Payload, &payload); err != nil {
		s.failRun(run, "hermes_invalid_event", "invalid delta")
		return
	}
	part := protocol.ContentPart{Type: kind}
	if kind == protocol.ContentText {
		part.Text = payload.Text
	} else {
		part.Reasoning = payload.Text
	}
	_ = s.emit(run, protocol.TypeContentDelta, protocol.ContentDeltaPayload{SessionID: s.state.SessionID, RunID: run.id, MessageID: run.messageID, Part: part}, false)
}

func (s *Session) startTool(run *runState, payload *native.ToolStartPayload) {
	key := toolKey(run, payload.ToolID)
	if s.tools[key] != nil {
		s.failRun(run, "hermes_tool_lifecycle", "duplicate tool call")
		return
	}
	args, _ := json.Marshal(payload.Args)
	t := &toolState{nativeID: payload.ToolID, id: protocol.ToolCallID(s.ids.NewID("tool-call")), run: run, name: payload.Name, args: args}
	s.tools[key] = t
	p := s.toolPayload(t)
	req, _ := s.emitEnvelope(run, protocol.TypeActionCallRequested, p, false, "")
	p.ArgumentsJSON = nil
	st, _ := s.emitEnvelope(run, protocol.TypeActionCallStarted, p, false, req.ID)
	t.requested = req.ID
	t.started = st.ID
}

func (s *Session) endTool(run *runState, payload *native.ToolCompletePayload) {
	key := toolKey(run, payload.ToolID)
	t := s.tools[key]
	if t == nil || t.terminal {
		s.failRun(run, "hermes_tool_lifecycle", "unmatched tool completion")
		return
	}
	t.terminal = true
	p := s.toolPayload(t)
	p.ArgumentsJSON = nil
	// The pinned gateway has no tool failure frame: failure rides in the
	// free-form result without a discriminator, so every completion projects
	// as completed (recorded as a ledger mismatch).
	p.Result = payload.Result
	// The pinned shape allows result to be omitted; action.call.completed
	// requires it, so a missing value is normalized to JSON null.
	if p.Result == nil {
		p.Result = json.RawMessage("null")
	}
	_, _ = s.emitEnvelope(run, protocol.TypeActionCallCompleted, p, false, t.started)
}

func toolKey(r *runState, id string) string { return fmt.Sprintf("%p\x00%s", r, id) }

func (s *Session) toolPayload(t *toolState) protocol.ActionCallPayload {
	return protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: t.run.id, ToolCallID: t.id, RequestedBy: "agent", ExecutionOwner: "hermes", Name: t.name, ArgumentsJSON: cloneRaw(t.args)}
}

// openInteraction surfaces one native gate as an OAP input interaction.
func (s *Session) openInteraction(run *runState, event *native.Event) {
	if !run.started || run.terminal {
		s.failRun(run, "hermes_interaction", "gate outside the owned run")
		return
	}
	id := protocol.InteractionID(s.ids.NewID("interaction"))
	binding := &inputState{id: id, run: run, answers: map[string]string{}}
	var title, description string
	switch event.Type {
	case native.EventApprovalRequest:
		var payload native.ApprovalRequestPayload
		if err := native.DecodeStrict(event.Payload, &payload); err != nil {
			s.failRun(run, "hermes_invalid_event", "invalid approval gate")
			return
		}
		binding.kind = "approval"
		title = "Command approval"
		description = payload.Command
		options := make([]protocol.InputOption, len(payload.Choices))
		for i, choice := range payload.Choices {
			options[i] = protocol.InputOption{ID: choice, Label: choice}
		}
		binding.questions = []protocol.InputQuestion{{ID: "choice", Prompt: payload.Command, Kind: protocol.InputSingleChoice, Required: true, Options: options}}
	case native.EventClarifyRequest:
		var payload native.ClarifyRequestPayload
		if err := native.DecodeStrict(event.Payload, &payload); err != nil {
			s.failRun(run, "hermes_invalid_event", "invalid clarify gate")
			return
		}
		binding.kind = "clarify"
		binding.requestID = payload.RequestID
		title = "Clarification"
		if len(payload.Questions) > 0 {
			for _, question := range payload.Questions {
				kind := protocol.InputSingleChoice
				if question.MultiSelect {
					kind = protocol.InputMultiChoice
				}
				var options []protocol.InputOption
				if len(question.Choices) == 0 {
					// A choice-less clarify is open-ended; OAP choice questions
					// require at least one option, so it surfaces as text.
					kind = protocol.InputText
				} else {
					options = make([]protocol.InputOption, len(question.Choices))
					for i, choice := range question.Choices {
						options[i] = protocol.InputOption{ID: choice, Label: choice}
					}
				}
				binding.questions = append(binding.questions, protocol.InputQuestion{ID: question.Qid, Prompt: question.Question, Kind: kind, Required: true, Options: options})
			}
		} else {
			kind := protocol.InputSingleChoice
			if payload.MultiSelect {
				kind = protocol.InputMultiChoice
			}
			var options []protocol.InputOption
			if len(payload.Choices) == 0 {
				kind = protocol.InputText
			} else {
				options = make([]protocol.InputOption, len(payload.Choices))
				for i, choice := range payload.Choices {
					options[i] = protocol.InputOption{ID: choice, Label: choice}
				}
			}
			binding.questions = []protocol.InputQuestion{{ID: "answer", Prompt: payload.Question, Kind: kind, Required: true, Options: options}}
		}
	case native.EventSudoRequest:
		binding.kind = "sudo"
		binding.requestID = decodeRequestID(event.Payload)
		title = "Password required"
		binding.questions = []protocol.InputQuestion{{ID: "password", Prompt: "Enter the sudo password", Kind: protocol.InputText, Required: true}}
	case native.EventSecretRequest:
		var payload native.SecretRequestPayload
		if err := native.DecodeStrict(event.Payload, &payload); err != nil {
			s.failRun(run, "hermes_invalid_event", "invalid secret gate")
			return
		}
		binding.kind = "secret"
		binding.requestID = payload.RequestID
		title = "Secret required"
		description = payload.EnvVar
		binding.questions = []protocol.InputQuestion{{ID: "value", Prompt: payload.Prompt, Kind: protocol.InputText, Required: true}}
	}
	if binding.requestID == "" && binding.kind != "approval" {
		s.failRun(run, "hermes_invalid_event", "gate without request_id")
		return
	}
	s.interactions[id] = binding
	requestedEnvelope, emitErr := s.emitEnvelope(run, protocol.TypeUserInputRequested, protocol.UserInputRequestedPayload{InteractionID: id, RequestedBy: "agent", RespondedBy: s.participant, SessionID: s.state.SessionID, RunID: run.id, Title: title, Description: description, Questions: binding.questions, AllowCancel: true}, false, "")
	if emitErr != nil {
		return
	}
	binding.requested = requestedEnvelope.ID
	_ = s.emit(run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunWaitingForInput, PendingUserInputID: id, UpdatedAtMS: s.clock.Now().UnixMilli()}, false)
}

func decodeRequestID(payload json.RawMessage) string {
	var probe struct {
		RequestID string `json:"request_id"`
	}
	_ = json.Unmarshal(payload, &probe)
	return probe.RequestID
}

func (s *Session) expireInteraction(run *runState, payload *native.ExpirePayload) {
	for _, binding := range s.interactions {
		if binding.run != run || binding.resolved || binding.settling || binding.requestID != payload.RequestID {
			continue
		}
		binding.resolved = true
		_, _ = s.emitEnvelope(run, protocol.TypeUserInputResolved, protocol.UserInputResolvedPayload{InteractionID: binding.id, RequestedBy: "agent", RespondedBy: s.participant, SessionID: s.state.SessionID, RunID: run.id, Status: protocol.InputCancelled}, false, binding.requested)
		_ = s.emit(run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunRunning, UpdatedAtMS: s.clock.Now().UnixMilli()}, false)
		return
	}
}

// Resolve answers one open gate through its native respond method. The
// resolution must carry exactly one answer per surfaced question (option or
// text form per the schema); batch clarify becomes one native respond per
// question, carrying the batch question selector.
func (s *Session) Resolve(ctx context.Context, resolution base.InteractionResolution) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if resolution.Input == nil {
		return errUnavailable
	}
	s.reduceMu.Lock()
	binding := s.interactions[resolution.Input.InteractionID]
	if binding == nil || binding.resolved || binding.settling {
		s.reduceMu.Unlock()
		return base.ErrInteractionNotFound
	}
	run := binding.run
	if run.terminal {
		s.reduceMu.Unlock()
		return base.ErrInteractionNotFound
	}
	// Validate the caller-supplied ownership before touching the native gate: a
	// mismatched participant or scope would otherwise resolve it and leave a
	// semantically invalid ownership trail.
	if resolution.RunID != "" && resolution.RunID != run.id {
		s.reduceMu.Unlock()
		return base.ErrInvalidResolution
	}
	if resolution.Input.SessionID != "" && resolution.Input.SessionID != s.state.SessionID {
		s.reduceMu.Unlock()
		return base.ErrInvalidResolution
	}
	if resolution.Input.RunID != "" && resolution.Input.RunID != run.id {
		s.reduceMu.Unlock()
		return base.ErrInvalidResolution
	}
	responder := resolution.RespondedBy
	if responder == "" {
		responder = resolution.Input.RespondedBy
	}
	if responder != "" && responder != s.participant {
		s.reduceMu.Unlock()
		return base.ErrInvalidResolution
	}
	// The pending gate was emitted with requester "agent"; a nested request that
	// names a different requester is ownership-inconsistent and must not reach the
	// native gate (the resolved event also hardcodes the stored requester).
	if resolution.Input.RequestedBy != "" && resolution.Input.RequestedBy != "agent" {
		s.reduceMu.Unlock()
		return base.ErrInvalidResolution
	}
	answers := resolution.Input.Answers
	var approval *native.ApprovalRespondParams
	var calls []native.RespondParams
	switch binding.kind {
	case "approval":
		if len(answers) != 1 || len(binding.questions) != 1 || base.ValidateInputAnswer(binding.questions[0], answers[0]) != nil {
			s.reduceMu.Unlock()
			return base.ErrInvalidResolution
		}
		approval = &native.ApprovalRespondParams{SessionID: s.nativeID, Choice: answers[0].SelectedOptionIDs[0]}
	case "clarify":
		// Exactly one answer per surfaced question; each must satisfy the OAP
		// answer shape, which the shared validator enforces (one form, offered
		// and unique options, non-empty text).
		if len(answers) != len(binding.questions) {
			s.reduceMu.Unlock()
			return base.ErrInvalidResolution
		}
		indexed, err := base.IndexInputAnswers(binding.questions, answers)
		if err != nil {
			s.reduceMu.Unlock()
			return base.ErrInvalidResolution
		}
		for _, question := range binding.questions {
			answer := indexed[question.ID]
			var value string
			switch question.Kind {
			case protocol.InputText:
				value = answer.Text
			case protocol.InputMultiChoice:
				// The native clarify answer field is a single string; the pinned
				// tool decodes a JSON array (or comma list) back into the full
				// selection set.
				encoded, err := json.Marshal(answer.SelectedOptionIDs)
				if err != nil {
					s.reduceMu.Unlock()
					return base.ErrInvalidResolution
				}
				value = string(encoded)
			default:
				value = answer.SelectedOptionIDs[0]
			}
			respond := native.RespondParams{RequestID: binding.requestID, Answer: value}
			if len(binding.questions) > 1 {
				respond.QuestionID = string(question.ID)
			}
			calls = append(calls, respond)
		}
	case "sudo", "secret":
		// The gate surfaces exactly one required text question ("password" or
		// "value"); the shared validator requires the text form and that the
		// answer names it.
		if len(answers) != 1 || len(binding.questions) != 1 || base.ValidateInputAnswer(binding.questions[0], answers[0]) != nil {
			s.reduceMu.Unlock()
			return base.ErrInvalidResolution
		}
		respond := native.RespondParams{RequestID: binding.requestID}
		if binding.kind == "sudo" {
			respond.Password = answers[0].Text
		} else {
			respond.Value = answers[0].Text
		}
		calls = []native.RespondParams{respond}
	default:
		s.reduceMu.Unlock()
		return errUnavailable
	}
	// Mark the gate settling, not resolved: a settlement that arrives while the
	// native answer is in flight must be parked (see settleRun) rather than
	// settled with the resolution still unpublished.
	binding.settling = true
	s.reduceMu.Unlock()

	unresolve := func() {
		s.reduceMu.Lock()
		binding.settling = false
		binding.resolved = false
		if run.deferred != nil && !run.terminal {
			// The answer could not be delivered but a settlement is parked
			// behind this gate; withdraw the gate so the terminal does not
			// strand a pending interaction.
			binding.resolved = true
			_, _ = s.emitEnvelope(run, protocol.TypeUserInputResolved, protocol.UserInputResolvedPayload{InteractionID: binding.id, RequestedBy: "agent", RespondedBy: s.participant, SessionID: s.state.SessionID, RunID: run.id, Status: protocol.InputCancelled}, false, binding.requested)
		}
		s.flushDeferred(run)
		s.reduceMu.Unlock()
	}
	if approval != nil {
		var result native.ApprovalRespondResult
		remoteErr := s.client.Call(ctx, native.MethodApprovalRespond, *approval, &result)
		if remoteErr == nil && !result.Resolved {
			remoteErr = fmt.Errorf("%w: approval.respond did not resolve the gate", ErrNativeProtocol)
		}
		if remoteErr != nil {
			unresolve()
			return remoteErr
		}
	} else {
		for _, call := range calls {
			var result native.RespondResult
			remoteErr := s.client.Call(ctx, respondMethod(binding.kind), call, &result)
			if remoteErr == nil && result.Status != "ok" {
				remoteErr = fmt.Errorf("%w: %s returned status %q", ErrNativeProtocol, respondMethod(binding.kind), result.Status)
			}
			if remoteErr != nil {
				// A mid-batch failure leaves earlier answers delivered
				// natively; the error lets the client retry, and re-sending
				// an earlier answer is the native registry's to judge.
				unresolve()
				return remoteErr
			}
		}
	}
	s.reduceMu.Lock()
	binding.settling = false
	respondedBy := resolution.RespondedBy
	if respondedBy == "" {
		respondedBy = s.participant
	}
	if !run.terminal {
		binding.resolved = true
		_, _ = s.emitEnvelope(run, protocol.TypeUserInputResolved, protocol.UserInputResolvedPayload{InteractionID: binding.id, RequestedBy: "agent", RespondedBy: respondedBy, SessionID: s.state.SessionID, RunID: run.id, Status: protocol.InputSubmitted, Answers: resolution.Input.Answers}, false, binding.requested)
		_ = s.emit(run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunRunning, UpdatedAtMS: s.clock.Now().UnixMilli()}, false)
	}
	// A settlement the gateway emitted around the answer was parked behind this
	// resolution; now that the canonical event is published, project it.
	s.flushDeferred(run)
	s.reduceMu.Unlock()
	return nil
}

// gatesSettling reports whether any gate on the run is mid-resolution. The
// canonical user.input.resolved event must precede the run's absorbing
// terminal, so a settlement arriving in that window is parked until Resolve
// flushes it.
func (s *Session) gatesSettling(run *runState) bool {
	for _, binding := range s.interactions {
		if binding.run == run && binding.settling {
			return true
		}
	}
	return false
}

// flushDeferred projects a settlement parked behind an in-flight resolution,
// once no gate on the run is still settling.
func (s *Session) flushDeferred(run *runState) {
	if run.deferred == nil || s.gatesSettling(run) {
		return
	}
	payload := run.deferred
	run.deferred = nil
	s.settleRun(run, payload)
}

func respondMethod(kind string) string {
	switch kind {
	case "approval":
		return native.MethodApprovalRespond
	case "clarify":
		return native.MethodClarifyRespond
	case "sudo":
		return native.MethodSudoRespond
	case "secret":
		return native.MethodSecretRespond
	}
	return ""
}

// settleRun projects the one absorbing settlement frame.
func (s *Session) settleRun(run *runState, payload *native.MessageCompletePayload) {
	if !run.started {
		// A settlement for a turn whose opening frame never arrived: the
		// reserved run has no started trace to fail. Release the reservation
		// with the native error; this is a protocol-order violation.
		s.abortPreStartUnlocked(run, fmt.Errorf("%w: settlement before the turn opened", ErrNativeProtocol))
		return
	}
	if s.gatesSettling(run) {
		// A gate resolution is mid-flight. Emitting the absorbing terminal now
		// would make the canonical user.input.resolved event unemittable
		// (terminality wins) and strand a pending interaction at terminality.
		// Park the settlement; the resolving Resolve flushes it after its own
		// emissions.
		run.deferred = payload
		return
	}
	if payload.Status == "" {
		s.failRun(run, "hermes_invalid_settlement", "child-mirror settlement on the parent stream")
		return
	}
	if payload.Status == "complete" {
		usage := &protocol.Usage{InputTokens: uint64(payload.Usage.Input), OutputTokens: uint64(payload.Usage.Output), TotalTokens: uint64(payload.Usage.Total)}
		_ = s.emit(run, protocol.TypeRunCompleted, protocol.RunCompletedPayload{SessionID: s.state.SessionID, RunID: run.id, FinalResponse: protocol.Message{ID: run.messageID, Role: protocol.RoleAssistant, Content: protocol.TextContent(payload.Text)}, StopReason: "completed", Usage: usage}, true)
		return
	}
	code := "hermes_" + payload.Status
	if payload.ErrorSurface != nil && payload.ErrorSurface.Code != "" {
		code = "hermes_" + payload.ErrorSurface.Code
	}
	message := payload.Error
	if message == "" {
		message = payload.Text
	}
	if message == "" {
		message = "turn " + payload.Status
	}
	_ = s.emit(run, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: s.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: code, Message: message}}, true)
}

func (s *Session) State(ctx context.Context) (protocol.SessionState, error) {
	if err := ctx.Err(); err != nil {
		return protocol.SessionState{}, err
	}
	s.reduceMu.Lock()
	defer s.reduceMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.unusable {
		return s.state, base.ErrSessionClosed
	}
	return s.state, nil
}

func (s *Session) Cancel(ctx context.Context, id protocol.RunID) (protocol.RunCancelResponse, error) {
	if err := ctx.Err(); err != nil {
		return protocol.RunCancelResponse{}, err
	}
	s.reduceMu.Lock()
	run := s.runs[id]
	valid := run != nil && run.started && !run.terminal
	s.reduceMu.Unlock()
	if !valid {
		return protocol.RunCancelResponse{}, base.ErrRunNotFound
	}
	var result native.InterruptResult
	if err := s.client.Call(ctx, native.MethodSessionInterrupt, native.InterruptParams{SessionID: s.nativeID}, &result); err != nil {
		return protocol.RunCancelResponse{}, err
	}
	// not_interrupted means the native saw no live turn; the owned run's
	// settlement is then missing and the failure surfaces via arbitration.
	return protocol.RunCancelResponse{SessionID: s.state.SessionID, RunID: id, Accepted: true, Status: protocol.RunCancelling}, nil
}

func (s *Session) Resume(ctx context.Context, _ base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	if err := ctx.Err(); err != nil {
		return base.Recovery{}, nil, err
	}
	return base.Recovery{}, nil, errUnavailable
}

func (s *Session) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.reduceMu.Lock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.reduceMu.Unlock()
		return nil
	}
	if (s.pending != nil) || (s.active != nil && !s.active.terminal) {
		s.mu.Unlock()
		s.reduceMu.Unlock()
		return base.ErrRunActive
	}
	s.closed = true
	s.state.Status = protocol.SessionClosed
	subs := s.allSubscribersLocked()
	s.mu.Unlock()
	s.reduceMu.Unlock()
	// Teardown is stdin EOF; the transport owns the drain bounds. Dispatch
	// stays alive through it so late frames reduce before the process exits.
	err := s.client.Close()
	s.stopOnce.Do(func() { close(s.stop) })
	for _, c := range subs {
		close(c)
	}
	return err
}

func (s *Session) foreignActivity(what string) {
	s.mu.Lock()
	run := s.pending
	if run == nil {
		run = s.active
	}
	s.unusable = true
	s.mu.Unlock()
	if run != nil && run.started {
		s.failRun(run, "hermes_external_activity", what)
	} else if run != nil {
		s.abortPreStartUnlocked(run, fmt.Errorf("%w: %s", ErrNativeProtocol, what))
	}
}

func (s *Session) abortPreStart(run *runState, err error) {
	s.reduceMu.Lock()
	defer s.reduceMu.Unlock()
	s.abortPreStartUnlocked(run, err)
}

func (s *Session) abortPreStartUnlocked(run *runState, err error) {
	s.mu.Lock()
	if run.terminal || run.started {
		s.mu.Unlock()
		return
	}
	run.terminal = true
	if s.pending == run {
		s.pending = nil
	}
	s.state.Status = protocol.SessionIdle
	subs := run.subscribers
	run.subscribers = nil
	run.buffered = nil
	s.mu.Unlock()
	run.signalStart(err)
	for _, c := range subs {
		close(c)
	}
}

func (s *Session) failRun(run *runState, code, msg string) {
	if run == nil {
		return
	}
	if !run.started {
		s.abortPreStartUnlocked(run, fmt.Errorf("%w: %s", ErrNativeProtocol, msg))
		return
	}
	_ = s.emit(run, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: s.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: code, Message: msg}}, true)
}

func (s *Session) transportFailed() {
	s.mu.Lock()
	run := s.pending
	if run == nil {
		run = s.active
	}
	closed := s.closed
	if !closed {
		s.unusable = true
	}
	s.mu.Unlock()
	if !closed && run != nil {
		if run.started {
			s.failRun(run, "hermes_process_exit", fmt.Sprint(s.client.Err()))
		} else {
			s.abortPreStartUnlocked(run, fmt.Errorf("%w: %v", ErrNativeProtocol, s.client.Err()))
		}
	}
}

func (s *Session) emit(run *runState, t protocol.EnvelopeType, p any, terminal bool) error {
	_, err := s.emitEnvelope(run, t, p, terminal, "")
	return err
}

func (s *Session) emitEnvelope(run *runState, t protocol.EnvelopeType, p any, terminal bool, reply protocol.EnvelopeID) (protocol.Envelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run.terminal {
		return protocol.Envelope{}, errTerminalWon
	}
	e, err := protocol.NewEnvelope(t, protocol.EnvelopeID(s.ids.NewID("event")), p)
	if err != nil {
		return protocol.Envelope{}, err
	}
	now := s.clock.Now().UnixMilli()
	seq := run.next
	run.next++
	e.Sequence = &seq
	e.TimestampMS = &now
	e.SessionID = s.state.SessionID
	e.RunID = run.id
	e.CapabilityRevision = CapabilityRevision
	e.InReplyTo = reply
	if t == protocol.TypeUserInputRequested || t == protocol.TypeUserInputResolved {
		var payload struct {
			InteractionID protocol.InteractionID `json:"interaction_id"`
		}
		_ = json.Unmarshal(e.Payload, &payload)
		e.TurnID = payload.InteractionID
	}
	if strings.HasPrefix(string(t), "action.call.") {
		var payload struct {
			ToolCallID protocol.ToolCallID `json:"tool_call_id"`
		}
		_ = json.Unmarshal(e.Payload, &payload)
		e.ToolCallID = payload.ToolCallID
	}
	s.journal = append(s.journal, e)
	if len(s.journal) > s.capacity {
		s.journal = append([]protocol.Envelope(nil), s.journal[len(s.journal)-s.capacity:]...)
	}
	s.state.TranscriptCursor = strconv.FormatUint(seq, 10)
	s.state.UpdatedAtMS = now
	if terminal {
		run.terminal = true
		if t == protocol.TypeRunCompleted {
			run.status = protocol.RunCompleted
		} else {
			run.status = protocol.RunFailed
		}
		if s.active == run {
			s.active = nil
		}
		delete(s.runs, run.id)
		s.state.Status = protocol.SessionIdle
		s.state.ActiveRunID = ""
	}
	kept := []chan base.Result{}
	for _, ch := range run.subscribers {
		if len(ch) < cap(ch)-1 {
			ch <- base.Result{Envelope: e}
			if terminal {
				close(ch)
			} else {
				kept = append(kept, ch)
			}
		} else {
			ch <- base.Result{Error: base.ErrEventStreamOverflow}
			close(ch)
		}
	}
	if terminal {
		run.subscribers = nil
	} else {
		run.subscribers = kept
	}
	return e, nil
}

func (s *Session) allSubscribersLocked() []chan base.Result {
	var out []chan base.Result
	for _, r := range s.runs {
		out = append(out, r.subscribers...)
		r.subscribers = nil
	}
	if s.pending != nil {
		out = append(out, s.pending.subscribers...)
		s.pending.subscribers = nil
	}
	return out
}

func cloneRaw(v json.RawMessage) json.RawMessage { return append(json.RawMessage(nil), v...) }

var _ base.Session = (*Session)(nil)

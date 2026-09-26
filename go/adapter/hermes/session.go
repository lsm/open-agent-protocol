package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/hermes/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/hermes/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/adapter/internal/journal"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

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
	journal      *journal.Journal
	lastSeq      int64
	stop         chan struct{}
	stopOnce     sync.Once

	transportDead bool
}

type runState struct {
	id       protocol.RunID
	status   protocol.RunStatus
	next     uint64
	started  bool
	terminal bool

	accepted      bool
	openSeen      bool
	submitting    bool
	buffered      []observation
	submittedText string
	messageID     protocol.MessageID
	final         *native.MessageCompletePayload
	terminalKind  string

	deferred    *native.MessageCompletePayload
	startResult chan error
	startOnce   sync.Once
}

type observation struct {
	event   *native.Event
	request *rpc.IncomingRequest
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

type inputState struct {
	id        protocol.InteractionID
	kind      string
	requestID string
	request   *rpc.IncomingRequest
	run       *runState
	questions []protocol.InputQuestion
	batch     bool
	resolved  bool

	settling  bool
	withdrawn bool

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
	run.submitting = true
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

	run.submitting = false
	if err != nil || result.Status != native.SubmitStreaming {
		if err == nil {

			err = fmt.Errorf("%w: prompt.submit returned status %q", ErrNativeProtocol, result.Status)
		}
		s.abortPreStartUnlocked(run, err)
		s.reduceMu.Unlock()
		s.promptMu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, err
	}

	s.mu.Lock()
	run.accepted = true
	openSeen := run.openSeen
	transportDead := s.transportDead
	s.mu.Unlock()
	if openSeen {
		s.startRun(run)
	}
	if transportDead {

		s.failTransport(run)
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

			s.reduceMu.Unlock()
			return protocol.MessageSubmitResponse{}, nil, ctx.Err()
		}
		s.reduceMu.Unlock()
		startErr := <-run.startResult
		if startErr != nil {
			return protocol.MessageSubmitResponse{}, nil, startErr
		}
	}
	return protocol.MessageSubmitResponse{SessionID: req.SessionID, Accepted: true, SubmissionID: protocol.SubmissionID(run.messageID), RequestedDelivery: protocol.DeliveryAuto, EffectiveDelivery: protocol.DeliveryStart, DeliveryResolution: "session_idle", Admission: protocol.AdmissionStarted, RunID: run.id, Status: protocol.RunRunning, ModelID: s.state.CurrentModelID, MessageIDs: []protocol.MessageID{run.messageID}}, s.journal.Follow(run.id, 0), nil
}

func submitText(req protocol.MessageSubmitRequest) (string, error) {

	if err := base.RefuseUnadvertisedControls(req); err != nil {
		return "", err
	}
	if req.SessionID == "" || len(req.Messages) != 1 || (req.Delivery != "" && req.Delivery != protocol.DeliveryAuto) {
		return "", base.ErrInvalidSubmission
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
		s.applyRequest(in.Request)
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

		return
	}
	if event.SessionID != s.nativeID {
		s.foreignActivity(fmt.Sprintf("event for foreign session %q", event.SessionID))
		return
	}

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

			return
		}

		s.foreignActivity(fmt.Sprintf("session event %q without a reserved run", event.Type))
		return
	}
	if terminal {

		return
	}
	if !run.started {
		s.reserveObservation(run, event)
		return
	}
	s.applyRunEvent(run, event)
}

func (s *Session) applyRequest(request *rpc.IncomingRequest) {
	if _, ok := request.ID.StringValue(); !ok {
		s.foreignActivity(fmt.Sprintf("%s request with a non-string id", request.Method))
		return
	}
	switch request.Method {
	case native.RequestApproval, native.RequestClarify, native.RequestSudo, native.RequestSecret:
	default:
		_ = s.client.RespondError(context.Background(), request, -32601, "method not found")
		return
	}
	if sessionID := native.RequestSessionID(request.Params); sessionID != s.nativeID {
		s.foreignActivity(fmt.Sprintf("%s request for foreign session %q", request.Method, sessionID))
		return
	}
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
	if run == nil || terminal {
		s.foreignActivity(fmt.Sprintf("%s request without an owned run", request.Method))
		return
	}
	if !run.started {
		run.buffered = append(run.buffered, observation{request: request})
		return
	}
	s.openInteraction(run, request)
}

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
	run.buffered = append(run.buffered, observation{event: event})
}

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
	case native.EventReasoningDelta:
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
	case native.EventRequestCancel:
		var payload native.RequestCancelPayload
		if err := native.DecodeStrict(event.Payload, &payload); err != nil {
			s.failRun(run, "hermes_invalid_event", "invalid request cancel")
			return
		}
		s.expireInteraction(run, &payload)
	case native.EventError:

	default:

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

	for _, observed := range replay {
		if run.terminal {
			return
		}
		if observed.request != nil {
			s.openInteraction(run, observed.request)
			continue
		}
		s.applyRunEvent(run, observed.event)
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
	if payload.Text == "" {
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

	p.Result = payload.Result

	if p.Result == nil {
		p.Result = json.RawMessage("null")
	}
	_, _ = s.emitEnvelope(run, protocol.TypeActionCallCompleted, p, false, t.started)
}

func toolKey(r *runState, id string) string { return fmt.Sprintf("%p\x00%s", r, id) }

func (s *Session) toolPayload(t *toolState) protocol.ActionCallPayload {
	return protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: t.run.id, ToolCallID: t.id, RequestedBy: endpointID, ExecutionOwner: "hermes", Name: t.name, ArgumentsJSON: cloneRaw(t.args)}
}

func (s *Session) openInteraction(run *runState, request *rpc.IncomingRequest) {
	if !run.started || run.terminal {
		s.failRun(run, "hermes_interaction", "gate outside the owned run")
		return
	}
	decoded, err := native.DecodeServerRequest(request.Method, request.Params)
	if err != nil || decoded == nil {
		s.failRun(run, "hermes_invalid_event", "invalid "+request.Method+" gate")
		return
	}
	requestID, _ := request.ID.StringValue()
	id := protocol.InteractionID(s.ids.NewID("interaction"))
	binding := &inputState{id: id, kind: request.Method, requestID: requestID, request: request, run: run, answers: map[string]string{}}
	var title, description string
	switch payload := decoded.(type) {
	case *native.ApprovalRequestParams:
		title = "Command approval"
		description = payload.Command
		if description == "" {
			description = payload.Description
		}
		options := make([]protocol.InputOption, len(payload.Choices))
		for i, choice := range payload.Choices {
			options[i] = protocol.InputOption{ID: choice, Label: choice}
		}
		binding.questions = []protocol.InputQuestion{{ID: "choice", Prompt: description, Kind: protocol.InputSingleChoice, Required: true, Options: options}}
	case *native.ClarifyRequestParams:
		title = "Clarification"
		if len(payload.Questions) > 0 {
			binding.batch = true
			for _, question := range payload.Questions {
				binding.questions = append(binding.questions, protocol.InputQuestion{ID: question.Qid, Prompt: question.Question, Kind: choiceKind(question.MultiSelect, question.Choices), Required: true, Options: choiceOptions(question.Choices)})
			}
		} else {
			binding.questions = []protocol.InputQuestion{{ID: "answer", Prompt: payload.Question, Kind: choiceKind(payload.MultiSelect, payload.Choices), Required: true, Options: choiceOptions(payload.Choices)}}
		}
	case *native.SudoRequestParams:
		title = "Password required"
		description = payload.Command
		binding.questions = []protocol.InputQuestion{{ID: "password", Prompt: "Enter the sudo password", Kind: protocol.InputText, Required: true}}
	case *native.SecretRequestParams:
		title = "Secret required"
		description = payload.EnvVar
		binding.questions = []protocol.InputQuestion{{ID: "value", Prompt: payload.Prompt, Kind: protocol.InputText, Required: true}}
	}
	if !everyQuestionAnswerable(binding.questions) {
		s.failRun(run, "hermes_invalid_event", "gate with a question nobody can answer")
		return
	}
	s.interactions[id] = binding
	requestedEnvelope, emitErr := s.emitEnvelope(run, protocol.TypeUserInputRequested, protocol.UserInputRequestedPayload{InteractionID: id, RequestedBy: endpointID, RespondedBy: s.participant, SessionID: s.state.SessionID, RunID: run.id, Title: title, Description: description, Questions: binding.questions, AllowCancel: true}, false, "")
	if emitErr != nil {
		return
	}
	binding.requested = requestedEnvelope.ID
	_ = s.emit(run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunWaitingForInput, PendingUserInputID: id, UpdatedAtMS: s.clock.Now().UnixMilli()}, false)
}

func choiceKind(multi bool, choices []string) protocol.InputQuestionKind {
	switch {
	case len(choices) == 0:
		return protocol.InputText
	case multi:
		return protocol.InputMultiChoice
	}
	return protocol.InputSingleChoice
}

func choiceOptions(choices []string) []protocol.InputOption {
	if len(choices) == 0 {
		return nil
	}
	options := make([]protocol.InputOption, len(choices))
	for i, choice := range choices {
		options[i] = protocol.InputOption{ID: choice, Label: choice}
	}
	return options
}

func everyQuestionAnswerable(questions []protocol.InputQuestion) bool {
	for _, question := range questions {
		if question.ID == "" || question.Prompt == "" {
			return false
		}
		if question.Kind == protocol.InputText {
			continue
		}
		if len(question.Options) == 0 {
			return false
		}
		for _, option := range question.Options {
			if option.ID == "" || option.Label == "" {
				return false
			}
		}
	}
	return true
}

func (s *Session) expireInteraction(run *runState, payload *native.RequestCancelPayload) {
	for _, binding := range s.interactions {
		if binding.run != run || binding.resolved || binding.requestID != payload.ID {
			continue
		}
		if binding.settling {
			binding.withdrawn = true
			return
		}
		binding.resolved = true
		_, _ = s.emitEnvelope(run, protocol.TypeUserInputResolved, protocol.UserInputResolvedPayload{InteractionID: binding.id, RequestedBy: endpointID, RespondedBy: s.participant, SessionID: s.state.SessionID, RunID: run.id, Status: protocol.InputCancelled}, false, binding.requested)
		_ = s.emit(run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunRunning, UpdatedAtMS: s.clock.Now().UnixMilli()}, false)
		return
	}
}

func (s *Session) Resolve(ctx context.Context, resolution base.InteractionResolution) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if resolution.Input == nil {
		return base.ErrInteractionNotFound
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

	if resolution.Input.RequestedBy != "" && resolution.Input.RequestedBy != endpointID {
		s.reduceMu.Unlock()
		return base.ErrInvalidResolution
	}
	answers := resolution.Input.Answers
	var result any
	switch binding.kind {
	case native.RequestApproval:
		if len(answers) != 1 || len(binding.questions) != 1 || base.ValidateInputAnswer(binding.questions[0], answers[0]) != nil {
			s.reduceMu.Unlock()
			return base.ErrInvalidResolution
		}
		result = native.ApprovalResult{Choice: answers[0].SelectedOptionIDs[0]}
	case native.RequestClarify:
		if len(answers) != len(binding.questions) {
			s.reduceMu.Unlock()
			return base.ErrInvalidResolution
		}
		indexed, err := base.IndexInputAnswers(binding.questions, answers)
		if err != nil {
			s.reduceMu.Unlock()
			return base.ErrInvalidResolution
		}
		values := make(map[string]string, len(binding.questions))
		for _, question := range binding.questions {
			answer := indexed[question.ID]
			switch question.Kind {
			case protocol.InputText:
				values[string(question.ID)] = answer.Text
			case protocol.InputMultiChoice:
				encoded, err := json.Marshal(answer.SelectedOptionIDs)
				if err != nil {
					s.reduceMu.Unlock()
					return base.ErrInvalidResolution
				}
				values[string(question.ID)] = string(encoded)
			default:
				values[string(question.ID)] = answer.SelectedOptionIDs[0]
			}
		}
		if binding.batch {
			result = native.ClarifyAnswersResult{Answers: values}
		} else {
			result = native.ClarifyAnswerResult{Answer: values["answer"]}
		}
	case native.RequestSudo, native.RequestSecret:
		if len(answers) != 1 || len(binding.questions) != 1 || base.ValidateInputAnswer(binding.questions[0], answers[0]) != nil {
			s.reduceMu.Unlock()
			return base.ErrInvalidResolution
		}
		result = native.ValueResult{Value: answers[0].Text}
	default:
		s.reduceMu.Unlock()
		return errUnavailable
	}

	binding.settling = true
	s.reduceMu.Unlock()

	if err := s.client.Respond(ctx, binding.request, result); err != nil {
		s.reduceMu.Lock()
		binding.settling = false
		binding.withdrawn = false
		if run.deferred != nil && !run.terminal {
			binding.resolved = true
			_, _ = s.emitEnvelope(run, protocol.TypeUserInputResolved, protocol.UserInputResolvedPayload{InteractionID: binding.id, RequestedBy: endpointID, RespondedBy: s.participant, SessionID: s.state.SessionID, RunID: run.id, Status: protocol.InputCancelled}, false, binding.requested)
		}
		s.flushDeferred(run)
		s.reduceMu.Unlock()
		return err
	}
	s.reduceMu.Lock()
	binding.settling = false
	withdrawn := binding.withdrawn
	respondedBy := resolution.RespondedBy
	if respondedBy == "" {
		respondedBy = s.participant
	}
	if !run.terminal {
		binding.resolved = true
		if withdrawn {
			_, _ = s.emitEnvelope(run, protocol.TypeUserInputResolved, protocol.UserInputResolvedPayload{InteractionID: binding.id, RequestedBy: endpointID, RespondedBy: s.participant, SessionID: s.state.SessionID, RunID: run.id, Status: protocol.InputCancelled}, false, binding.requested)
		} else {
			_, _ = s.emitEnvelope(run, protocol.TypeUserInputResolved, protocol.UserInputResolvedPayload{InteractionID: binding.id, RequestedBy: endpointID, RespondedBy: respondedBy, SessionID: s.state.SessionID, RunID: run.id, Status: protocol.InputSubmitted, Answers: resolution.Input.Answers}, false, binding.requested)
		}
		_ = s.emit(run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunRunning, UpdatedAtMS: s.clock.Now().UnixMilli()}, false)
	}

	s.flushDeferred(run)
	s.reduceMu.Unlock()
	if withdrawn {
		return base.ErrInteractionNotFound
	}
	return nil
}

func (s *Session) gatesSettling(run *runState) bool {
	for _, binding := range s.interactions {
		if binding.run == run && binding.settling {
			return true
		}
	}
	return false
}

func (s *Session) flushDeferred(run *runState) {
	if run.deferred == nil || s.gatesSettling(run) {
		return
	}
	payload := run.deferred
	run.deferred = nil
	s.settleRun(run, payload)
}

func (s *Session) settleChildren(run *runState) {
	for _, binding := range s.interactions {
		if binding.run != run || binding.resolved {
			continue
		}
		binding.resolved = true
		_, _ = s.emitEnvelope(run, protocol.TypeUserInputResolved, protocol.UserInputResolvedPayload{InteractionID: binding.id, RequestedBy: endpointID, RespondedBy: s.participant, SessionID: s.state.SessionID, RunID: run.id, Status: protocol.InputCancelled}, false, binding.requested)
	}
	for _, t := range s.tools {
		if t.run != run || t.terminal {
			continue
		}
		t.terminal = true
		p := s.toolPayload(t)
		p.ArgumentsJSON = nil
		p.Error = &protocol.ProtocolError{Code: "incomplete_tool", Message: "turn settled with an unfinished hermes tool"}
		_, _ = s.emitEnvelope(run, protocol.TypeActionCallFailed, p, false, t.started)
	}
}

func (s *Session) settleRun(run *runState, payload *native.MessageCompletePayload) {
	if !run.started {

		s.abortPreStartUnlocked(run, fmt.Errorf("%w: settlement before the turn opened", ErrNativeProtocol))
		return
	}
	if s.gatesSettling(run) {

		run.deferred = payload
		return
	}
	if payload.Status == "" {
		s.failRun(run, "hermes_invalid_settlement", "child-mirror settlement on the parent stream")
		return
	}
	s.settleChildren(run)
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

	return protocol.RunCancelResponse{SessionID: s.state.SessionID, RunID: id, Accepted: true, Status: protocol.RunCancelling}, nil
}

func (s *Session) Resume(ctx context.Context, request base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	if err := ctx.Err(); err != nil {
		return base.Recovery{}, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return base.Recovery{}, nil, base.ErrSessionClosed
	}
	run := s.runs[request.RunID]
	latest, ended := s.journal.Ended(request.RunID)
	switch {
	case run != nil:
		latest = run.next - 1
	case !ended:
		return base.Recovery{}, nil, base.ErrRunNotFound
	}
	if request.AfterSequence > latest {
		return base.Recovery{}, nil, base.ErrReplayCursorFuture
	}
	return s.journal.Resume(s.state, request.RunID, request.AfterSequence, latest)
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
	s.mu.Unlock()
	s.reduceMu.Unlock()

	err := s.client.Close()
	s.stopOnce.Do(func() { close(s.stop) })
	s.journal.Close()
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
	run.buffered = nil
	s.mu.Unlock()
	run.signalStart(err)
}

func (s *Session) failRun(run *runState, code, msg string) {
	s.failRunSettled(run, code, msg, "")
}

func (s *Session) failRunSettled(run *runState, code, msg, settledBy string) {
	if run == nil {
		return
	}
	if !run.started {
		s.abortPreStartUnlocked(run, fmt.Errorf("%w: %s", ErrNativeProtocol, msg))
		return
	}
	s.settleChildren(run)
	_ = s.emit(run, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: s.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: code, Message: msg}, SettledBy: settledBy}, true)
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
		s.transportDead = true
	}
	s.mu.Unlock()
	if closed || run == nil {
		return
	}
	if !run.started && run.submitting {

		return
	}
	s.failTransport(run)
}

func (s *Session) failTransport(run *runState) {
	if run.started {
		s.failRunSettled(run, "hermes_process_exit", fmt.Sprint(s.client.Err()), protocol.SettledByInferred)
		return
	}
	s.abortPreStartUnlocked(run, fmt.Errorf("%w: %v", ErrNativeProtocol, s.client.Err()))
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
	s.journal.Append(e, terminal)
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
	return e, nil
}

func cloneRaw(v json.RawMessage) json.RawMessage { return append(json.RawMessage(nil), v...) }

var _ base.Session = (*Session)(nil)

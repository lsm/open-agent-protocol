package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/pi/internal/native"
	"github.com/lsm/open-agent-protocol/adapter/pi/internal/rpc"
	"github.com/lsm/open-agent-protocol/protocol"
)

const streamCapacity = 64

var errTerminalWon = errors.New("pi adapter: terminal already selected")

// Session is a concurrently safe Pi-backed OAP session.
type Session struct {
	mu           sync.Mutex
	reduceMu     sync.Mutex
	client       Client
	inbound      <-chan rpc.Inbound
	clock        base.Clock
	ids          base.IDGenerator
	capacity     int
	nativeID     string
	participant  protocol.ParticipantID
	state        protocol.SessionState
	nativeState  native.SessionState
	closed       bool
	unusable     bool
	active       *runState
	runs         map[protocol.RunID]*runState
	tools        map[string]*toolState
	interactions map[protocol.InteractionID]*inputState
	journal      []protocol.Envelope
	stop         chan struct{}
	stopOnce     sync.Once
}
type runState struct {
	id           protocol.RunID
	status       protocol.RunStatus
	next         uint64
	started      bool
	terminal     bool
	cancelIntent bool
	candidate    *agentEnd
	messageID    protocol.MessageID
	text         strings.Builder
	reasoning    strings.Builder
	final        *wireMessage
	subscribers  []chan base.Result
}
type toolState struct {
	nativeID       string
	id             protocol.ToolCallID
	run            *runState
	name           string
	args           json.RawMessage
	progress       json.RawMessage
	result         json.RawMessage
	started        bool
	terminal       bool
	requestedEvent protocol.EnvelopeID
	startedEvent   protocol.EnvelopeID
}
type inputState struct {
	id          protocol.InteractionID
	nativeID    string
	run         *runState
	method      native.ExtensionMethod
	requestedBy protocol.ParticipantID
	respondedBy protocol.ParticipantID
	questions   []protocol.InputQuestion
	resolved    bool
}

type eventHeader struct {
	Type native.EventType `json:"type"`
}
type messageUpdate struct {
	Type                  native.EventType `json:"type"`
	Usage                 json.RawMessage  `json:"usage"`
	AssistantMessageEvent json.RawMessage  `json:"assistantMessageEvent"`
}
type deltaEvent struct {
	Type         string `json:"type"`
	ContentIndex int    `json:"contentIndex"`
	Delta        string `json:"delta"`
}
type toolStart struct {
	Type       native.EventType `json:"type"`
	ToolCallID string           `json:"toolCallId"`
	ToolName   string           `json:"toolName"`
	Args       json.RawMessage  `json:"args"`
}
type toolUpdate struct {
	Type          native.EventType `json:"type"`
	ToolCallID    string           `json:"toolCallId"`
	ToolName      string           `json:"toolName"`
	Args          json.RawMessage  `json:"args"`
	PartialResult json.RawMessage  `json:"partialResult"`
}
type toolEnd struct {
	Type       native.EventType `json:"type"`
	ToolCallID string           `json:"toolCallId"`
	ToolName   string           `json:"toolName"`
	Result     json.RawMessage  `json:"result"`
	IsError    bool             `json:"isError"`
}
type agentEnd struct {
	Type      native.EventType  `json:"type"`
	Messages  []json.RawMessage `json:"messages"`
	WillRetry bool              `json:"willRetry"`
}
type wireMessage struct {
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content"`
	API          string          `json:"api,omitempty"`
	Provider     string          `json:"provider,omitempty"`
	Model        string          `json:"model,omitempty"`
	Usage        json.RawMessage `json:"usage,omitempty"`
	StopReason   string          `json:"stopReason,omitempty"`
	ErrorMessage string          `json:"errorMessage,omitempty"`
	Timestamp    int64           `json:"timestamp,omitempty"`
}
type settledEvent struct {
	Type native.EventType `json:"type"`
}

func (s *Session) Submit(ctx context.Context, req protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	if err := ctx.Err(); err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}
	text, images, messageIDs, err := s.nativePrompt(req)
	if err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}
	s.reduceMu.Lock()
	s.mu.Lock()
	if s.closed || s.unusable {
		s.mu.Unlock()
		s.reduceMu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, base.ErrSessionClosed
	}
	if req.SessionID != s.state.SessionID {
		s.mu.Unlock()
		s.reduceMu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, base.ErrRunNotFound
	}
	if s.active != nil && !s.active.terminal {
		s.mu.Unlock()
		s.reduceMu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, base.ErrRunActive
	}
	run := &runState{id: protocol.RunID(s.ids.NewID("run")), status: protocol.RunQueued, next: 1, messageID: protocol.MessageID(s.ids.NewID("message"))}
	stream := make(chan base.Result, streamCapacity+1)
	run.subscribers = []chan base.Result{stream}
	s.active, s.runs[run.id] = run, run
	s.state.Status, s.state.ActiveRunID, s.state.CurrentModelID, s.state.UpdatedAtMS = protocol.SessionRunning, run.id, req.ModelID, s.clock.Now().UnixMilli()
	s.mu.Unlock()
	s.reduceMu.Unlock()
	command := native.Command{Type: native.CommandPrompt, Message: &text, Images: images, StreamingBehavior: native.StreamingSteer}
	if err := s.callStrict(ctx, command, nil); err != nil {
		s.reduceMu.Lock()
		s.mu.Lock()
		s.unusable = true
		s.mu.Unlock()
		s.failRun(run, "pi_admission_failed", err.Error())
		s.reduceMu.Unlock()
		return protocol.MessageSubmitResponse{}, stream, err
	}
	s.mu.Lock()
	terminal := run.terminal
	started := run.started
	s.mu.Unlock()
	if terminal && !started {
		return protocol.MessageSubmitResponse{}, stream, fmt.Errorf("%w: prompt settled without agent_start", ErrNativeProtocol)
	}
	response := protocol.MessageSubmitResponse{SessionID: req.SessionID, Accepted: true, SubmissionID: protocol.SubmissionID(s.ids.NewID("submission")), RequestedDelivery: protocol.DeliveryAuto, EffectiveDelivery: protocol.DeliveryStart, DeliveryResolution: "session_idle", Admission: protocol.AdmissionStarted, RunID: run.id, Status: protocol.RunRunning, ModelID: req.ModelID, MessageIDs: messageIDs}
	return response, stream, nil
}

func (s *Session) nativePrompt(req protocol.MessageSubmitRequest) (string, []native.ImageContent, []protocol.MessageID, error) {
	if req.SessionID == "" || len(req.Messages) == 0 || (req.Delivery != "" && req.Delivery != protocol.DeliveryAuto) || req.Instructions != "" || len(req.ToolChoice) > 0 || len(req.OutputSchema) > 0 {
		return "", nil, nil, base.ErrInvalidSubmission
	}
	var texts []string
	var images []native.ImageContent
	ids := make([]protocol.MessageID, len(req.Messages))
	for i, m := range req.Messages {
		if m.Role != protocol.RoleUser {
			return "", nil, nil, fmt.Errorf("%w: Pi accepts user content only", ErrUnsupportedInput)
		}
		ids[i] = m.ID
		if ids[i] == "" {
			ids[i] = protocol.MessageID(s.ids.NewID("message"))
		}
		if text, ok := m.Content.Text(); ok {
			texts = append(texts, text)
			continue
		}
		parts, ok := m.Content.Parts()
		if !ok {
			return "", nil, nil, fmt.Errorf("%w: Pi accepts text and inline images only", ErrUnsupportedInput)
		}
		for _, p := range parts {
			switch p.Type {
			case protocol.ContentText:
				texts = append(texts, p.Text)
			case protocol.ContentImage:
				if p.Image == nil || p.Image.Data == "" || p.Image.MediaType == "" {
					return "", nil, nil, ErrUnsupportedInput
				}
				images = append(images, native.ImageContent{Type: "image", Data: p.Image.Data, MimeType: p.Image.MediaType})
			default:
				return "", nil, nil, ErrUnsupportedInput
			}
		}
	}
	if len(texts) == 0 {
		return "", nil, nil, fmt.Errorf("%w: prompt requires text", ErrUnsupportedInput)
	}
	return strings.Join(texts, "\n\n"), images, ids, nil
}

func (s *Session) callStrict(ctx context.Context, command native.Command, dst any) error {
	var raw json.RawMessage
	var target any
	if dst != nil {
		target = &raw
	}
	if err := s.client.Call(ctx, command, target); err != nil {
		return err
	}
	if dst != nil {
		if len(raw) == 0 {
			return fmt.Errorf("%w: %s response omitted data", ErrNativeProtocol, command.Type)
		}
		if err := native.DecodeStrict(raw, dst); err != nil {
			return fmt.Errorf("%w: decode %s response: %v", ErrNativeProtocol, command.Type, err)
		}
	}
	return nil
}

func (s *Session) dispatch() {
	for {
		select {
		case inbound := <-s.inbound:
			s.reduceMu.Lock()
			s.reduce(inbound)
			s.reduceMu.Unlock()
		case <-s.client.Done():
			for {
				select {
				case inbound := <-s.inbound:
					s.reduceMu.Lock()
					s.reduce(inbound)
					s.reduceMu.Unlock()
				default:
					s.reduceMu.Lock()
					s.transportFailed()
					s.reduceMu.Unlock()
					return
				}
			}
		case <-s.stop:
			return
		}
	}
}
func (s *Session) reduce(in rpc.Inbound) {
	if in.Barrier != nil {
		close(in.Barrier)
		return
	}
	if in.Event != nil {
		s.applyEvent(*in.Event)
		return
	}
	if in.ExtensionRequest != nil {
		s.applyExtension(*in.ExtensionRequest)
	}
}
func (s *Session) activeRun() *runState { s.mu.Lock(); defer s.mu.Unlock(); return s.active }
func (s *Session) applyEvent(event native.Event) {
	run := s.activeRun()
	if run == nil {
		return
	}
	s.mu.Lock()
	terminal := run.terminal
	s.mu.Unlock()
	if terminal {
		return
	}
	switch event.Type {
	case native.EventAgentStart:
		var value eventHeader
		if !s.decodeEvent(event, &value) {
			return
		}
		s.mu.Lock()
		if run.started {
			s.mu.Unlock()
			s.failRun(run, "pi_invalid_lifecycle", "duplicate agent_start")
			return
		}
		run.started = true
		run.status = protocol.RunRunning
		s.state.Status = protocol.SessionRunning
		s.mu.Unlock()
		_ = s.emit(run, protocol.TypeRunStarted, protocol.RunStartedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunRunning, ModelID: s.state.CurrentModelID, StartedAtMS: s.clock.Now().UnixMilli()}, false)
	case native.EventMessageUpdate:
		var value messageUpdate
		if !s.decodeEvent(event, &value) {
			return
		}
		var delta deltaEvent
		if err := native.DecodeStrict(value.AssistantMessageEvent, &delta); err != nil {
			s.failRun(run, "pi_invalid_message_update", err.Error())
			return
		}
		var part protocol.ContentPart
		switch delta.Type {
		case "text_delta":
			run.text.WriteString(delta.Delta)
			part = protocol.ContentPart{Type: protocol.ContentText, Text: delta.Delta}
		case "thinking_delta":
			run.reasoning.WriteString(delta.Delta)
			part = protocol.ContentPart{Type: protocol.ContentReasoning, Reasoning: delta.Delta}
		default:
			return
		}
		_ = s.emit(run, protocol.TypeContentDelta, protocol.ContentDeltaPayload{SessionID: s.state.SessionID, RunID: run.id, MessageID: run.messageID, Part: part}, false)
	case native.EventMessageEnd:
		var value struct {
			Type    native.EventType `json:"type"`
			Message json.RawMessage  `json:"message"`
		}
		if !s.decodeEvent(event, &value) {
			return
		}
		var message wireMessage
		if err := native.DecodeStrict(value.Message, &message); err != nil {
			s.failRun(run, "pi_invalid_message_end", err.Error())
			return
		}
		if message.Role == "assistant" {
			run.final = &message
		}
	case native.EventToolExecutionStart:
		var value toolStart
		if !s.decodeEvent(event, &value) {
			return
		}
		s.startTool(run, value)
	case native.EventToolExecutionUpdate:
		var value toolUpdate
		if !s.decodeEvent(event, &value) {
			return
		}
		s.updateTool(run, value)
	case native.EventToolExecutionEnd:
		var value toolEnd
		if !s.decodeEvent(event, &value) {
			return
		}
		s.endTool(run, value)
	case native.EventAgentEnd:
		var value agentEnd
		if !s.decodeEvent(event, &value) {
			return
		}
		if value.WillRetry {
			return
		}
		run.candidate = &value
	case native.EventAgentSettled:
		var value settledEvent
		if !s.decodeEvent(event, &value) {
			return
		}
		s.settleRun(run)
	case native.EventAutoRetryStart, native.EventAutoRetryEnd, native.EventTurnStart, native.EventTurnEnd, native.EventMessageStart, native.EventQueueUpdate, native.EventCompactionStart, native.EventCompactionEnd, native.EventEntryAppended, native.EventSessionInfoChanged, native.EventThinkingLevelChanged, native.EventSummarizationRetryScheduled, native.EventSummarizationRetryAttemptStart, native.EventSummarizationRetryFinished, native.EventBashExecutionUpdate, native.EventExtensionError:
		return
	default:
		s.failRun(run, "pi_unknown_event", fmt.Sprintf("unknown event %q", event.Type))
	}
}
func (s *Session) decodeEvent(event native.Event, dst any) bool {
	if err := native.DecodeStrict(event.Raw, dst); err != nil {
		s.failRun(s.activeRun(), "pi_invalid_event", err.Error())
		return false
	}
	return true
}

func (s *Session) startTool(run *runState, v toolStart) {
	if v.ToolCallID == "" || v.ToolName == "" || !json.Valid(v.Args) {
		s.failRun(run, "pi_invalid_tool_lifecycle", "invalid tool start")
		return
	}
	key := toolKey(run, v.ToolCallID)
	if s.tools[key] != nil {
		s.failRun(run, "pi_invalid_tool_lifecycle", "duplicate tool start")
		return
	}
	t := &toolState{nativeID: v.ToolCallID, id: protocol.ToolCallID(v.ToolCallID), run: run, name: v.ToolName, args: cloneRaw(v.Args), started: true}
	s.tools[key] = t
	requested, _ := s.emitEnvelope(run, protocol.TypeActionCallRequested, s.toolPayload(t), false, "")
	p := s.toolPayload(t)
	p.ArgumentsJSON = nil
	started, _ := s.emitEnvelope(run, protocol.TypeActionCallStarted, p, false, requested.ID)
	t.requestedEvent = requested.ID
	t.startedEvent = started.ID
}
func (s *Session) updateTool(run *runState, v toolUpdate) {
	t := s.tools[toolKey(run, v.ToolCallID)]
	if t == nil || t.terminal || t.name != v.ToolName || !json.Valid(v.PartialResult) {
		s.failRun(run, "pi_invalid_tool_lifecycle", "tool update without matching active start")
		return
	}
	t.progress = cloneRaw(v.PartialResult)
	p := s.toolPayload(t)
	p.ArgumentsJSON = nil
	_, _ = s.emitEnvelope(run, protocol.TypeActionCallProgress, p, false, t.startedEvent)
}
func (s *Session) endTool(run *runState, v toolEnd) {
	t := s.tools[toolKey(run, v.ToolCallID)]
	if t == nil || t.terminal || t.name != v.ToolName || !json.Valid(v.Result) {
		s.failRun(run, "pi_invalid_tool_lifecycle", "tool end without matching active start")
		return
	}
	t.result = cloneRaw(v.Result)
	t.terminal = true
	p := s.toolPayload(t)
	p.ArgumentsJSON = nil
	p.Progress = nil
	if v.IsError {
		p.Result = nil
		p.Error = &protocol.ProtocolError{Code: "pi_tool_failed", Message: "Pi tool execution failed"}
		_, _ = s.emitEnvelope(run, protocol.TypeActionCallFailed, p, false, t.startedEvent)
	} else {
		_, _ = s.emitEnvelope(run, protocol.TypeActionCallCompleted, p, false, t.startedEvent)
	}
}
func toolKey(r *runState, id string) string { return string(r.id) + "\x00" + id }
func (s *Session) toolPayload(t *toolState) protocol.ActionCallPayload {
	return protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: t.run.id, ToolCallID: t.id, RequestedBy: "agent", ExecutionOwner: "pi", Name: t.name, ArgumentsJSON: cloneRaw(t.args), Progress: cloneRaw(t.progress), Result: cloneRaw(t.result)}
}

func (s *Session) settleRun(run *runState) {
	// A non-retrying agent_end proves that natural completion won even when an
	// abort was requested concurrently. Without that candidate, settled after
	// abort is authoritative cancellation evidence.
	cancelled := run.cancelIntent && run.candidate == nil
	s.settleChildren(run, cancelled)
	if cancelled {
		_ = s.emit(run, protocol.TypeRunCancelled, protocol.RunCancelledPayload{SessionID: s.state.SessionID, RunID: run.id, Reason: "Pi settled after abort intent"}, true)
		return
	}
	if run.candidate == nil {
		s.failRun(run, "pi_missing_agent_end", "agent_settled arrived without terminal agent_end")
		return
	}
	final := run.final
	if final == nil {
		for i := len(run.candidate.Messages) - 1; i >= 0; i-- {
			var m wireMessage
			if native.DecodeStrict(run.candidate.Messages[i], &m) == nil && m.Role == "assistant" {
				final = &m
				break
			}
		}
	}
	if final == nil {
		s.failRun(run, "pi_missing_final_message", "agent settlement omitted assistant message")
		return
	}
	if final.ErrorMessage != "" || final.StopReason == "error" {
		message := final.ErrorMessage
		if message == "" {
			message = "Pi agent failed"
		}
		_ = s.emit(run, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: s.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: "pi_agent_failed", Message: message}}, true)
		return
	}
	content, err := wireContent(final.Content, run)
	if err != nil {
		s.failRun(run, "pi_invalid_final_message", err.Error())
		return
	}
	reason := final.StopReason
	if reason == "" {
		reason = "end_turn"
	}
	_ = s.emit(run, protocol.TypeRunCompleted, protocol.RunCompletedPayload{SessionID: s.state.SessionID, RunID: run.id, FinalResponse: protocol.Message{ID: run.messageID, Role: protocol.RoleAssistant, Content: content}, StopReason: reason}, true)
}
func wireContent(raw json.RawMessage, run *runState) (protocol.MessageContent, error) {
	if len(raw) == 0 {
		return fallbackContent(run), nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return protocol.TextContent(text), nil
	}
	var parts []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text,omitempty"`
		Thinking  string          `json:"thinking,omitempty"`
		ID        string          `json:"id,omitempty"`
		Name      string          `json:"name,omitempty"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}
	if err := native.DecodeStrict(raw, &parts); err != nil {
		return protocol.MessageContent{}, err
	}
	out := make([]protocol.ContentPart, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case "text":
			out = append(out, protocol.ContentPart{Type: protocol.ContentText, Text: p.Text})
		case "thinking":
			out = append(out, protocol.ContentPart{Type: protocol.ContentReasoning, Reasoning: p.Thinking})
		case "toolCall", "tool_call":
			out = append(out, protocol.ContentPart{Type: protocol.ContentToolCall, ToolCallID: protocol.ToolCallID(p.ID), Name: p.Name, ArgumentsJSON: cloneRaw(p.Arguments)})
		default:
			return protocol.MessageContent{}, fmt.Errorf("unknown message content %q", p.Type)
		}
	}
	if len(out) == 0 {
		return fallbackContent(run), nil
	}
	return protocol.PartsContent(out), nil
}
func fallbackContent(r *runState) protocol.MessageContent {
	parts := []protocol.ContentPart{}
	if r.reasoning.Len() > 0 {
		parts = append(parts, protocol.ContentPart{Type: protocol.ContentReasoning, Reasoning: r.reasoning.String()})
	}
	if r.text.Len() > 0 {
		parts = append(parts, protocol.ContentPart{Type: protocol.ContentText, Text: r.text.String()})
	}
	if len(parts) == 1 && parts[0].Type == protocol.ContentText {
		return protocol.TextContent(parts[0].Text)
	}
	return protocol.PartsContent(parts)
}

func (s *Session) applyExtension(r native.ExtensionUIRequest) {
	run := s.activeRun()
	if run == nil || !run.started {
		return
	}
	switch r.Method {
	case native.ExtensionSelect, native.ExtensionInput, native.ExtensionEditor, native.ExtensionConfirm:
	default:
		return
	}
	id := protocol.InteractionID(s.ids.NewID("interaction"))
	question := protocol.InputQuestion{ID: "value", Prompt: r.Title, Kind: protocol.InputText, Required: true}
	if r.Method == native.ExtensionSelect {
		question.Kind = protocol.InputSingleChoice
		for i, o := range r.Options {
			question.Options = append(question.Options, protocol.InputOption{ID: fmt.Sprintf("option-%d", i+1), Label: o})
		}
	}
	if r.Method == native.ExtensionConfirm {
		question.Kind = protocol.InputSingleChoice
		question.Prompt = r.Message
		question.Options = []protocol.InputOption{{ID: "yes", Label: "Yes"}, {ID: "no", Label: "No"}}
	}
	binding := &inputState{id: id, nativeID: r.ID, run: run, method: r.Method, requestedBy: "agent", respondedBy: s.participant, questions: []protocol.InputQuestion{question}}
	s.interactions[id] = binding
	run.status = protocol.RunWaitingForInput
	s.state.Status = protocol.SessionWaitingForInput
	_ = s.emit(run, protocol.TypeUserInputRequested, protocol.UserInputRequestedPayload{InteractionID: id, RequestedBy: "agent", RespondedBy: s.participant, SessionID: s.state.SessionID, RunID: run.id, Title: r.Title, Description: r.Message, Questions: binding.questions, AllowCancel: true}, false)
	_ = s.emit(run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunWaitingForInput, PendingUserInputID: id, UpdatedAtMS: s.clock.Now().UnixMilli()}, false)
}

func (s *Session) Resolve(ctx context.Context, res base.InteractionResolution) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if res.Permission != nil || res.Input == nil {
		return base.ErrInvalidResolution
	}
	request := res.Input
	s.reduceMu.Lock()
	binding := s.interactions[request.InteractionID]
	if binding == nil {
		s.reduceMu.Unlock()
		return base.ErrInteractionNotFound
	}
	if binding.resolved {
		s.reduceMu.Unlock()
		return base.ErrInteractionResolved
	}
	if res.RunID != binding.run.id || request.RunID != binding.run.id || request.SessionID != s.state.SessionID || res.RespondedBy != binding.respondedBy || request.RespondedBy != binding.respondedBy || request.RequestedBy != binding.requestedBy {
		s.reduceMu.Unlock()
		return base.ErrWrongResponder
	}
	response, err := extensionResponse(binding, *request)
	if err != nil {
		s.reduceMu.Unlock()
		return err
	}
	binding.resolved = true
	s.reduceMu.Unlock()
	if err := s.client.Respond(ctx, response); err != nil {
		s.reduceMu.Lock()
		s.failRun(binding.run, "pi_interaction_response_failed", err.Error())
		s.reduceMu.Unlock()
		return err
	}
	s.reduceMu.Lock()
	_ = s.emit(binding.run, protocol.TypeUserInputResolved, protocol.UserInputResolvedPayload{InteractionID: binding.id, RequestedBy: binding.requestedBy, RespondedBy: binding.respondedBy, SessionID: s.state.SessionID, RunID: binding.run.id, Status: protocol.InputSubmitted, Answers: request.Answers}, false)
	s.mu.Lock()
	if !binding.run.terminal {
		binding.run.status = protocol.RunRunning
		s.state.Status = protocol.SessionRunning
	}
	s.mu.Unlock()
	_ = s.emit(binding.run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: binding.run.id, Status: protocol.RunRunning, UpdatedAtMS: s.clock.Now().UnixMilli()}, false)
	s.reduceMu.Unlock()
	return nil
}
func extensionResponse(b *inputState, r protocol.UserInputResolveRequest) (native.ExtensionUIResponse, error) {
	if len(r.Answers) != 1 || r.Answers[0].QuestionID != "value" {
		return native.ExtensionUIResponse{}, base.ErrInvalidResolution
	}
	a := r.Answers[0]
	response := native.ExtensionUIResponse{Type: "extension_ui_response", ID: b.nativeID}
	switch b.method {
	case native.ExtensionConfirm:
		if len(a.SelectedOptionIDs) != 1 {
			return response, base.ErrInvalidResolution
		}
		v := a.SelectedOptionIDs[0] == "yes"
		if !v && a.SelectedOptionIDs[0] != "no" {
			return response, base.ErrInvalidResolution
		}
		response.Confirmed = &v
	case native.ExtensionSelect:
		if len(a.SelectedOptionIDs) != 1 {
			return response, base.ErrInvalidResolution
		}
		var n int
		if _, err := fmt.Sscanf(a.SelectedOptionIDs[0], "option-%d", &n); err != nil || n < 1 || n > len(b.questions[0].Options) {
			return response, base.ErrInvalidResolution
		}
		v := b.questions[0].Options[n-1].Label
		response.Value = &v
	default:
		v := a.Text
		response.Value = &v
	}
	return response, nil
}

func (s *Session) State(ctx context.Context) (protocol.SessionState, error) {
	if err := ctx.Err(); err != nil {
		return protocol.SessionState{}, err
	}
	var nativeState native.SessionState
	if err := s.callStrict(ctx, native.Command{Type: native.CommandGetState}, &nativeState); err != nil {
		return protocol.SessionState{}, err
	}
	if err := validateState(nativeState); err != nil {
		return protocol.SessionState{}, fmt.Errorf("%w: get_state: %v", ErrNativeProtocol, err)
	}
	s.reduceMu.Lock()
	defer s.reduceMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if nativeState.SessionID != s.nativeID {
		s.unusable = true
		return s.state, fmt.Errorf("%w: native session changed", ErrNativeProtocol)
	}
	s.nativeState = nativeState
	if s.active != nil && !s.active.terminal && !nativeState.IsStreaming && s.active.started && !s.active.cancelIntent {
		s.unusable = true
		go s.failAfterReconcile(s.active)
	}
	if s.closed || s.unusable {
		return s.state, base.ErrSessionClosed
	}
	return s.state, nil
}
func (s *Session) failAfterReconcile(run *runState) {
	s.reduceMu.Lock()
	defer s.reduceMu.Unlock()
	s.failRun(run, "pi_reconciliation_gap", "get_state reports idle before authoritative agent_settled")
}

func (s *Session) Cancel(ctx context.Context, id protocol.RunID) (protocol.RunCancelResponse, error) {
	if err := ctx.Err(); err != nil {
		return protocol.RunCancelResponse{}, err
	}
	s.reduceMu.Lock()
	s.mu.Lock()
	if s.closed || s.unusable {
		s.mu.Unlock()
		s.reduceMu.Unlock()
		return protocol.RunCancelResponse{}, base.ErrSessionClosed
	}
	run := s.runs[id]
	if run == nil {
		s.mu.Unlock()
		s.reduceMu.Unlock()
		return protocol.RunCancelResponse{}, base.ErrRunNotFound
	}
	if run.terminal {
		status := run.status
		s.mu.Unlock()
		s.reduceMu.Unlock()
		return protocol.RunCancelResponse{}, &base.RunTerminalError{RunID: id, Status: status}
	}
	if run.cancelIntent {
		s.mu.Unlock()
		s.reduceMu.Unlock()
		return protocol.RunCancelResponse{SessionID: s.state.SessionID, RunID: id, Accepted: true, Status: protocol.RunCancelling}, nil
	}
	run.cancelIntent = true
	run.status = protocol.RunCancelling
	s.mu.Unlock()
	_ = s.emit(run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: id, Status: protocol.RunCancelling, UpdatedAtMS: s.clock.Now().UnixMilli()}, false)
	s.reduceMu.Unlock()
	if err := s.callStrict(ctx, native.Command{Type: native.CommandAbort}, nil); err != nil {
		s.reduceMu.Lock()
		s.failRun(run, "pi_abort_failed", err.Error())
		s.reduceMu.Unlock()
		return protocol.RunCancelResponse{}, err
	}
	s.mu.Lock()
	terminal := run.terminal
	status := run.status
	s.mu.Unlock()
	if terminal {
		return protocol.RunCancelResponse{SessionID: s.state.SessionID, RunID: id, Accepted: true, Status: status}, nil
	}
	return protocol.RunCancelResponse{SessionID: s.state.SessionID, RunID: id, Accepted: true, Status: protocol.RunCancelling}, nil
}

func (s *Session) Resume(ctx context.Context, r base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	if err := ctx.Err(); err != nil {
		return base.Recovery{}, nil, err
	}
	s.reduceMu.Lock()
	defer s.reduceMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return base.Recovery{}, nil, base.ErrSessionClosed
	}
	run := s.runs[r.RunID]
	if run == nil {
		return base.Recovery{}, nil, base.ErrRunNotFound
	}
	latest := run.next - 1
	if r.AfterSequence > latest {
		return base.Recovery{}, nil, base.ErrReplayCursorFuture
	}
	var oldest uint64
	var suffix []protocol.Envelope
	for _, e := range s.journal {
		if e.RunID != run.id || e.Sequence == nil {
			continue
		}
		if oldest == 0 {
			oldest = *e.Sequence
		}
		if *e.Sequence > r.AfterSequence {
			suffix = append(suffix, e)
		}
	}
	recovery := base.Recovery{State: s.state, RunID: run.id, RequestedAfter: r.AfterSequence, ReplayedFrom: r.AfterSequence, ReplayedThrough: r.AfterSequence}
	stream := make(chan base.Result, len(suffix)+streamCapacity+1)
	if r.AfterSequence < latest && (oldest == 0 || r.AfterSequence+1 < oldest) {
		recovery.ReplayGap = &base.ReplayGap{RequestedAfter: r.AfterSequence, OldestAvailable: oldest, LatestAvailable: latest}
		close(stream)
		return recovery, stream, recovery.ReplayGap
	}
	if len(suffix) > 0 {
		recovery.ReplayedFrom = *suffix[0].Sequence
		recovery.ReplayedThrough = *suffix[len(suffix)-1].Sequence
	}
	for _, e := range suffix {
		stream <- base.Result{Envelope: e}
	}
	if run.terminal {
		close(stream)
	} else {
		run.subscribers = append(run.subscribers, stream)
	}
	return recovery, stream, nil
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
	if s.active != nil && !s.active.terminal {
		s.mu.Unlock()
		s.reduceMu.Unlock()
		return base.ErrRunActive
	}
	s.closed = true
	s.state.Status = protocol.SessionClosed
	s.state.ActiveRunID = ""
	subs := s.allSubscribersLocked()
	s.mu.Unlock()
	s.stopOnce.Do(func() { close(s.stop) })
	s.reduceMu.Unlock()
	err := s.client.Close()
	for _, c := range subs {
		close(c)
	}
	return err
}

func (s *Session) settleChildren(run *runState, cancel bool) {
	for _, t := range s.tools {
		if t.run != run || t.terminal {
			continue
		}
		t.terminal = true
		p := s.toolPayload(t)
		p.ArgumentsJSON = nil
		p.Progress = nil
		if cancel {
			_, _ = s.emitEnvelope(run, protocol.TypeActionCallCancelled, p, false, t.startedEvent)
		} else {
			p.Error = &protocol.ProtocolError{Code: "pi_incomplete_tool", Message: "Pi run settled before tool completion"}
			_, _ = s.emitEnvelope(run, protocol.TypeActionCallFailed, p, false, t.startedEvent)
		}
	}
	for _, i := range s.interactions {
		if i.run != run || i.resolved {
			continue
		}
		i.resolved = true
		_ = s.client.Respond(context.Background(), native.ExtensionUIResponse{Type: "extension_ui_response", ID: i.nativeID, Cancelled: true})
		_ = s.emit(run, protocol.TypeUserInputResolved, protocol.UserInputResolvedPayload{InteractionID: i.id, RequestedBy: i.requestedBy, RespondedBy: i.respondedBy, SessionID: s.state.SessionID, RunID: run.id, Status: protocol.InputCancelled}, false)
	}
}
func (s *Session) failRun(run *runState, code, message string) {
	if run == nil {
		return
	}
	s.settleChildren(run, true)
	_ = s.emit(run, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: s.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: code, Message: message}}, true)
}
func (s *Session) transportFailed() {
	s.mu.Lock()
	run := s.active
	closed := s.closed
	if !closed {
		s.unusable = true
	}
	s.mu.Unlock()
	if !closed && run != nil {
		s.failRun(run, "pi_process_exit", fmt.Sprint(s.client.Err()))
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
	event, err := protocol.NewEnvelope(t, protocol.EnvelopeID(s.ids.NewID("event")), p)
	if err != nil {
		return protocol.Envelope{}, err
	}
	now := s.clock.Now().UnixMilli()
	seq := run.next
	run.next++
	event.Sequence = &seq
	event.TimestampMS = &now
	event.SessionID = s.state.SessionID
	event.RunID = run.id
	event.CapabilityRevision = CapabilityRevision
	event.InReplyTo = reply
	if strings.HasPrefix(string(t), "action.call.") {
		var a struct {
			ToolCallID protocol.ToolCallID `json:"tool_call_id"`
		}
		_ = json.Unmarshal(event.Payload, &a)
		event.ToolCallID = a.ToolCallID
	}
	s.journal = append(s.journal, event)
	if len(s.journal) > s.capacity {
		s.journal = append([]protocol.Envelope(nil), s.journal[len(s.journal)-s.capacity:]...)
	}
	s.state.TranscriptCursor = strconv.FormatUint(seq, 10)
	s.state.UpdatedAtMS = now
	if terminal {
		run.terminal = true
		switch t {
		case protocol.TypeRunCompleted:
			run.status = protocol.RunCompleted
		case protocol.TypeRunCancelled:
			run.status = protocol.RunCancelled
		default:
			run.status = protocol.RunFailed
		}
		if s.active == run {
			s.active = nil
		}
		s.state.Status = protocol.SessionIdle
		s.state.ActiveRunID = ""
	}
	var kept []chan base.Result
	for _, ch := range run.subscribers {
		if len(ch) < cap(ch)-1 {
			ch <- base.Result{Envelope: event}
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
	return event, nil
}
func (s *Session) allSubscribersLocked() []chan base.Result {
	var out []chan base.Result
	for _, r := range s.runs {
		out = append(out, r.subscribers...)
		r.subscribers = nil
	}
	return out
}
func cloneRaw(v json.RawMessage) json.RawMessage { return append(json.RawMessage(nil), v...) }

var _ base.Session = (*Session)(nil)

package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/pi/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/pi/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const streamCapacity = 64

var errTerminalWon = errors.New("pi adapter: terminal already selected")

type Session struct {
	mu                 sync.Mutex
	reduceMu           sync.Mutex
	commandMu          sync.Mutex
	client             Client
	inbound            <-chan rpc.Inbound
	clock              base.Clock
	ids                base.IDGenerator
	capacity           int
	nativeID           string
	participant        protocol.ParticipantID
	state              protocol.SessionState
	nativeState        native.SessionState
	closed             bool
	unusable           bool
	active             *runState
	runs               map[protocol.RunID]*runState
	tools              map[string]*toolState
	interactions       map[protocol.InteractionID]*inputState
	interactionsOpened uint64
	journal            []protocol.Envelope
	stop               chan struct{}
	stopOnce           sync.Once
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
	startResult  chan error
	startOnce    sync.Once
	pending      []native.Event
	pendingUI    []native.ExtensionUIRequest
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
type interactionPhase uint8

const (
	interactionPending interactionPhase = iota
	interactionResolving
	interactionResolved
)

type inputState struct {
	id          protocol.InteractionID
	nativeID    string
	run         *runState
	method      native.ExtensionMethod
	requestedBy protocol.ParticipantID
	respondedBy protocol.ParticipantID
	questions   []protocol.InputQuestion
	phase       interactionPhase
	opened      uint64
}

type eventHeader struct {
	Type native.EventType `json:"type"`
}
type messageUpdate struct {
	Type                  native.EventType `json:"type"`
	Usage                 json.RawMessage  `json:"usage"`
	AssistantMessageEvent json.RawMessage  `json:"assistantMessageEvent"`
}
type providerEventHeader struct {
	Type string `json:"type"`
}
type deltaEvent struct {
	Type         string `json:"type"`
	ContentIndex int    `json:"contentIndex"`
	Delta        string `json:"delta"`
}
type providerStartEvent struct {
	Type string `json:"type"`
}
type providerIndexedEvent struct {
	Type         string `json:"type"`
	ContentIndex int    `json:"contentIndex"`
}
type providerContentEndEvent struct {
	Type         string `json:"type"`
	ContentIndex int    `json:"contentIndex"`
	Content      string `json:"content"`
}
type providerToolCallStartEvent struct {
	Type         string `json:"type"`
	ContentIndex int    `json:"contentIndex"`
	ID           string `json:"id"`
	ToolName     string `json:"toolName"`
}
type providerToolCallDeltaEvent struct {
	Type         string `json:"type"`
	ContentIndex int    `json:"contentIndex"`
	Delta        string `json:"delta"`
}
type providerToolCallEndEvent struct {
	Type         string              `json:"type"`
	ContentIndex int                 `json:"contentIndex"`
	ToolCall     wireToolCallContent `json:"toolCall"`
}
type providerDoneEvent struct {
	Type    string          `json:"type"`
	Reason  string          `json:"reason"`
	Message json.RawMessage `json:"message"`
}
type providerErrorEvent struct {
	Type   string          `json:"type"`
	Reason string          `json:"reason"`
	Error  json.RawMessage `json:"error"`
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
	Role                  string            `json:"role"`
	Content               json.RawMessage   `json:"content"`
	API                   string            `json:"api"`
	Provider              string            `json:"provider"`
	Model                 string            `json:"model"`
	ResponseModel         string            `json:"responseModel,omitempty"`
	ResponseID            string            `json:"responseId,omitempty"`
	ProviderThinkingLevel string            `json:"providerThinkingLevel,omitempty"`
	Diagnostics           []json.RawMessage `json:"diagnostics,omitempty"`
	Usage                 json.RawMessage   `json:"usage"`
	StopReason            string            `json:"stopReason"`
	Deferred              json.RawMessage   `json:"deferred,omitempty"`
	ErrorMessage          string            `json:"errorMessage,omitempty"`
	RawStopReason         string            `json:"rawStopReason,omitempty"`
	EndTurn               *bool             `json:"endTurn,omitempty"`
	Timestamp             int64             `json:"timestamp"`
}
type wireUserMessage struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Timestamp int64           `json:"timestamp"`
}
type wireToolResultMessage struct {
	Role           string          `json:"role"`
	ToolCallID     string          `json:"toolCallId"`
	ToolName       string          `json:"toolName"`
	Content        json.RawMessage `json:"content"`
	Details        json.RawMessage `json:"details,omitempty"`
	Usage          json.RawMessage `json:"usage,omitempty"`
	AddedToolNames []string        `json:"addedToolNames,omitempty"`
	IsError        bool            `json:"isError"`
	Timestamp      int64           `json:"timestamp"`
}
type settledEvent struct {
	Type native.EventType `json:"type"`
}

func (r *runState) signalStart(err error) {
	r.startOnce.Do(func() { r.startResult <- err; close(r.startResult) })
}

func (s *Session) Submit(ctx context.Context, req protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	if err := ctx.Err(); err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}
	text, images, messageIDs, err := s.nativePrompt(req)
	if err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}

	s.commandMu.Lock()
	s.reduceMu.Lock()
	s.mu.Lock()
	if s.closed || s.unusable {
		s.mu.Unlock()
		s.reduceMu.Unlock()
		s.commandMu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, base.ErrSessionClosed
	}
	if req.SessionID != s.state.SessionID {
		s.mu.Unlock()
		s.reduceMu.Unlock()
		s.commandMu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, base.ErrRunNotFound
	}
	if s.active != nil && !s.active.terminal {
		s.mu.Unlock()
		s.reduceMu.Unlock()
		s.commandMu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, base.ErrRunActive
	}
	run := &runState{id: protocol.RunID(s.ids.NewID("run")), status: protocol.RunQueued, next: 1, messageID: protocol.MessageID(s.ids.NewID("message")), startResult: make(chan error, 1)}
	stream := make(chan base.Result, streamCapacity+1)
	run.subscribers = []chan base.Result{stream}
	s.active, s.runs[run.id] = run, run

	model := s.state.CurrentModelID
	s.state.Status, s.state.ActiveRunID, s.state.UpdatedAtMS = protocol.SessionRunning, run.id, s.clock.Now().UnixMilli()
	s.mu.Unlock()
	s.reduceMu.Unlock()
	command := native.Command{Type: native.CommandPrompt, Message: &text, Images: images, StreamingBehavior: native.StreamingSteer}
	err = s.callStrictLocked(ctx, command, nil)
	s.commandMu.Unlock()
	if err != nil {
		s.reduceMu.Lock()
		s.mu.Lock()
		s.unusable = true
		s.mu.Unlock()
		s.failRun(run, "pi_admission_failed", err.Error())
		s.reduceMu.Unlock()
		return protocol.MessageSubmitResponse{}, stream, err
	}

	select {
	case startErr := <-run.startResult:
		if startErr != nil {
			return protocol.MessageSubmitResponse{}, stream, startErr
		}
	case <-ctx.Done():

		return protocol.MessageSubmitResponse{}, stream, ctx.Err()
	}
	response := protocol.MessageSubmitResponse{SessionID: req.SessionID, Accepted: true, SubmissionID: protocol.SubmissionID(s.ids.NewID("submission")), RequestedDelivery: protocol.DeliveryAuto, EffectiveDelivery: protocol.DeliveryStart, DeliveryResolution: "session_idle", Admission: protocol.AdmissionStarted, RunID: run.id, Status: protocol.RunRunning, ModelID: model, MessageIDs: messageIDs}
	return response, stream, nil
}

func (s *Session) nativePrompt(req protocol.MessageSubmitRequest) (string, []native.ImageContent, []protocol.MessageID, error) {

	if err := base.RefuseUnadvertisedControls(req); err != nil {
		return "", nil, nil, err
	}
	if req.SessionID == "" || len(req.Messages) == 0 || (req.Delivery != "" && req.Delivery != protocol.DeliveryAuto) {
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
	text := strings.Join(texts, "\n\n")
	if strings.HasPrefix(text, "/") {

		return "", nil, nil, fmt.Errorf("%w: slash-command input", ErrUnsupportedInput)
	}
	return text, images, ids, nil
}

func nativeModelID(raw json.RawMessage) string {
	var ref struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Provider string `json:"provider"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &ref) != nil {
		return ""
	}
	switch {
	case ref.ID == "":
		return ref.Name
	case ref.Provider == "":
		return ref.ID
	default:
		return ref.Provider + "/" + ref.ID
	}
}

func (s *Session) callStrict(ctx context.Context, command native.Command, dst any) error {
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	return s.callStrictLocked(ctx, command, dst)
}
func (s *Session) callStrictLocked(ctx context.Context, command native.Command, dst any) error {
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
		run := s.activeRun()
		if run != nil && !run.started {
			run.pendingUI = append(run.pendingUI, *in.ExtensionRequest)
			return
		}
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
	if !run.started && event.Type != native.EventAgentStart {

		if event.Type == native.EventAgentSettled {
			s.terminateBeforeStart(run, fmt.Errorf("%w: agent_settled arrived without agent_start", ErrNativeProtocol))
			return
		}
		run.pending = append(run.pending, event)
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
		cancelIntent := run.cancelIntent
		s.mu.Unlock()
		if err := s.emit(run, protocol.TypeRunStarted, protocol.RunStartedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunRunning, ModelID: s.state.CurrentModelID, StartedAtMS: s.clock.Now().UnixMilli()}, false); err != nil {
			run.signalStart(err)
			return
		}
		if cancelIntent {
			s.mu.Lock()
			run.status = protocol.RunCancelling
			s.mu.Unlock()
			if err := s.emit(run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunCancelling, UpdatedAtMS: s.clock.Now().UnixMilli()}, false); err != nil {
				run.signalStart(err)
				return
			}
		}
		run.signalStart(nil)
		pending := run.pending
		pendingUI := run.pendingUI
		run.pending = nil
		run.pendingUI = nil
		for _, queued := range pending {
			if run.terminal {
				break
			}
			s.applyEvent(queued)
		}
		for _, queued := range pendingUI {
			if run.terminal {
				break
			}
			s.applyExtension(queued)
		}
	case native.EventMessageUpdate:
		var value messageUpdate
		if !s.decodeEvent(event, &value) {
			return
		}
		part, emit, err := decodeProviderEvent(value.AssistantMessageEvent)
		if err != nil {
			s.failRun(run, "pi_invalid_message_update", err.Error())
			return
		}
		if !emit {
			return
		}
		if part.Type == protocol.ContentText {
			run.text.WriteString(part.Text)
		}
		if part.Type == protocol.ContentReasoning {
			run.reasoning.WriteString(part.Reasoning)
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
		message, err := decodeWireMessage(value.Message)
		if err != nil {
			s.failRun(run, "pi_invalid_message_end", err.Error())
			return
		}
		if message != nil {
			run.final = message
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
			run.candidate = nil
			run.final = nil
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
func decodeProviderEvent(raw json.RawMessage) (protocol.ContentPart, bool, error) {
	var object map[string]json.RawMessage
	if err := native.DecodeStrict(raw, &object); err != nil {
		return protocol.ContentPart{}, false, err
	}
	require := func(names ...string) error {
		for _, name := range names {
			if _, ok := object[name]; !ok {
				return fmt.Errorf("assistant message event requires %s", name)
			}
		}
		return nil
	}
	index := func() error {
		var n int
		if err := require("contentIndex"); err != nil {
			return err
		}
		if json.Unmarshal(object["contentIndex"], &n) != nil || n < 0 {
			return errors.New("assistant message event has invalid contentIndex")
		}
		return nil
	}
	var header providerEventHeader
	if value, ok := object["type"]; !ok || json.Unmarshal(value, &header.Type) != nil || header.Type == "" {
		return protocol.ContentPart{}, false, errors.New("assistant message event requires type")
	}
	switch header.Type {
	case "text_delta", "thinking_delta":
		if err := require("delta"); err != nil {
			return protocol.ContentPart{}, false, err
		}
		if err := index(); err != nil {
			return protocol.ContentPart{}, false, err
		}
		var value deltaEvent
		if err := native.DecodeStrict(raw, &value); err != nil {
			return protocol.ContentPart{}, false, err
		}
		if value.Type == "text_delta" {
			return protocol.ContentPart{Type: protocol.ContentText, Text: value.Delta}, true, nil
		}
		return protocol.ContentPart{Type: protocol.ContentReasoning, Reasoning: value.Delta}, true, nil
	case "start":
		var value providerStartEvent
		return protocol.ContentPart{}, false, native.DecodeStrict(raw, &value)
	case "text_start", "thinking_start":
		if err := index(); err != nil {
			return protocol.ContentPart{}, false, err
		}
		var value providerIndexedEvent
		return protocol.ContentPart{}, false, native.DecodeStrict(raw, &value)
	case "text_end", "thinking_end":
		if err := require("content"); err != nil {
			return protocol.ContentPart{}, false, err
		}
		if err := index(); err != nil {
			return protocol.ContentPart{}, false, err
		}
		var value providerContentEndEvent
		return protocol.ContentPart{}, false, native.DecodeStrict(raw, &value)
	case "toolcall_start":
		if err := require("id", "toolName"); err != nil {
			return protocol.ContentPart{}, false, err
		}
		if err := index(); err != nil {
			return protocol.ContentPart{}, false, err
		}
		var value providerToolCallStartEvent
		return protocol.ContentPart{}, false, native.DecodeStrict(raw, &value)
	case "toolcall_end":
		if err := require("toolCall"); err != nil {
			return protocol.ContentPart{}, false, err
		}
		if err := index(); err != nil {
			return protocol.ContentPart{}, false, err
		}
		var value providerToolCallEndEvent
		return protocol.ContentPart{}, false, native.DecodeStrict(raw, &value)
	case "toolcall_delta":
		if err := require("delta"); err != nil {
			return protocol.ContentPart{}, false, err
		}
		if err := index(); err != nil {
			return protocol.ContentPart{}, false, err
		}
		var value providerToolCallDeltaEvent
		return protocol.ContentPart{}, false, native.DecodeStrict(raw, &value)
	case "done":
		if err := require("reason", "message"); err != nil {
			return protocol.ContentPart{}, false, err
		}
		var value providerDoneEvent
		return protocol.ContentPart{}, false, native.DecodeStrict(raw, &value)
	case "error":
		if err := require("reason", "error"); err != nil {
			return protocol.ContentPart{}, false, err
		}
		var value providerErrorEvent
		return protocol.ContentPart{}, false, native.DecodeStrict(raw, &value)
	default:
		return protocol.ContentPart{}, false, fmt.Errorf("unknown assistant message event %q", header.Type)
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
	t := &toolState{nativeID: v.ToolCallID, id: protocol.ToolCallID(s.ids.NewID("tool-call")), run: run, name: v.ToolName, args: cloneRaw(v.Args), started: true}
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
	return protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: t.run.id, ToolCallID: t.id, RequestedBy: endpointID, ExecutionOwner: "pi", Name: t.name, ArgumentsJSON: cloneRaw(t.args), Progress: cloneRaw(t.progress), Result: cloneRaw(t.result)}
}

func (s *Session) settleRun(run *runState) {

	var final *wireMessage
	if run.candidate != nil {
		for i := len(run.candidate.Messages) - 1; i >= 0; i-- {
			m, err := decodeWireMessage(run.candidate.Messages[i])
			if err != nil {
				s.failRun(run, "pi_invalid_final_message", err.Error())
				return
			}
			if m != nil {
				final = m
				break
			}
		}
	}
	if final == nil && run.candidate != nil {
		final = run.final
	}
	cancelled := run.cancelIntent && (run.candidate == nil || (final != nil && final.StopReason == "aborted"))
	s.settleChildren(run, cancelled)
	if cancelled {
		_ = s.emit(run, protocol.TypeRunCancelled, protocol.RunCancelledPayload{SessionID: s.state.SessionID, RunID: run.id, Reason: "Pi settled after abort intent"}, true)
		return
	}
	if run.candidate == nil {
		s.failRun(run, "pi_missing_agent_end", "agent_settled arrived without terminal agent_end")
		return
	}
	if final == nil {
		s.failRun(run, "pi_missing_final_message", "agent settlement omitted assistant message")
		return
	}
	if final.ErrorMessage != "" || final.StopReason == "error" || final.StopReason == "aborted" {
		message := final.ErrorMessage
		if message == "" {
			message = "Pi agent failed"
		}
		_ = s.emit(run, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: s.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: "pi_agent_failed", Message: message}}, true)
		return
	}
	content, err := s.wireContent(final.Content, run)
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
func decodeWireMessage(raw json.RawMessage) (*wireMessage, error) {
	var object map[string]json.RawMessage
	if err := native.DecodeStrict(raw, &object); err != nil {
		return nil, err
	}
	var role string
	if value, ok := object["role"]; !ok || json.Unmarshal(value, &role) != nil {
		return nil, errors.New("message requires role")
	}
	require := func(names ...string) error {
		for _, name := range names {
			if _, ok := object[name]; !ok {
				return fmt.Errorf("%s message requires %s", role, name)
			}
		}
		return nil
	}
	switch role {
	case "assistant":
		if err := require("content", "api", "provider", "model", "usage", "stopReason", "timestamp"); err != nil {
			return nil, err
		}
		var message wireMessage
		if err := native.DecodeStrict(raw, &message); err != nil {
			return nil, err
		}
		if err := validateWireContent(message.Content, false); err != nil {
			return nil, err
		}
		return &message, nil
	case "user":
		if err := require("content", "timestamp"); err != nil {
			return nil, err
		}
		var message wireUserMessage
		if err := native.DecodeStrict(raw, &message); err != nil {
			return nil, err
		}
		if err := validateWireContent(message.Content, true); err != nil {
			return nil, err
		}
		return nil, nil
	case "toolResult":
		if err := require("toolCallId", "toolName", "content", "isError", "timestamp"); err != nil {
			return nil, err
		}
		var message wireToolResultMessage
		if err := native.DecodeStrict(raw, &message); err != nil {
			return nil, err
		}
		if err := validateWireContent(message.Content, true); err != nil {
			return nil, err
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown message role %q", role)
	}
}

func validateWireContent(raw json.RawMessage, allowImage bool) error {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return nil
	}
	var parts []json.RawMessage
	if err := native.DecodeStrict(raw, &parts); err != nil {
		return err
	}
	for _, part := range parts {
		var object map[string]json.RawMessage
		if err := native.DecodeStrict(part, &object); err != nil {
			return err
		}
		var typ string
		if value, ok := object["type"]; !ok || json.Unmarshal(value, &typ) != nil {
			return errors.New("content block requires type")
		}
		switch typ {
		case "text":
			var p wireTextContent
			if err := native.DecodeStrict(part, &p); err != nil {
				return err
			}
			if _, ok := object["text"]; !ok {
				return errors.New("text block requires text")
			}
		case "thinking":
			if allowImage {
				return errors.New("thinking block not valid here")
			}
			var p wireThinkingContent
			if err := native.DecodeStrict(part, &p); err != nil {
				return err
			}
			if _, ok := object["thinking"]; !ok {
				return errors.New("thinking block requires thinking")
			}
		case "toolCall":
			if allowImage {
				return errors.New("toolCall block not valid here")
			}
			var p wireToolCallContent
			if err := native.DecodeStrict(part, &p); err != nil {
				return err
			}
			for _, n := range []string{"id", "name", "arguments"} {
				if _, ok := object[n]; !ok {
					return fmt.Errorf("toolCall requires %s", n)
				}
			}
		case "image":
			if !allowImage {
				return errors.New("image block not valid here")
			}
			var p wireImageContent
			if err := native.DecodeStrict(part, &p); err != nil {
				return err
			}
			for _, n := range []string{"data", "mimeType"} {
				if _, ok := object[n]; !ok {
					return fmt.Errorf("image requires %s", n)
				}
			}
		default:
			return fmt.Errorf("unknown content block %q", typ)
		}
	}
	return nil
}

type wireContentHeader struct {
	Type string `json:"type"`
}
type wireTextContent struct {
	Type          string `json:"type"`
	Text          string `json:"text"`
	TextSignature string `json:"textSignature,omitempty"`
}
type wireImageContent struct {
	Type     string `json:"type"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}
type wireThinkingContent struct {
	Type              string `json:"type"`
	Thinking          string `json:"thinking"`
	ThinkingSignature string `json:"thinkingSignature,omitempty"`
	Redacted          bool   `json:"redacted,omitempty"`
}
type wireToolCallContent struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Arguments        json.RawMessage `json:"arguments"`
	ThoughtSignature string          `json:"thoughtSignature,omitempty"`
	Namespace        string          `json:"namespace,omitempty"`
}

func (s *Session) wireContent(raw json.RawMessage, run *runState) (protocol.MessageContent, error) {
	if len(raw) == 0 {
		return fallbackContent(run), nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return protocol.TextContent(text), nil
	}
	var rawParts []json.RawMessage
	if err := native.DecodeStrict(raw, &rawParts); err != nil {
		return protocol.MessageContent{}, err
	}
	out := make([]protocol.ContentPart, 0, len(rawParts))
	for _, rawPart := range rawParts {
		var object map[string]json.RawMessage
		if err := native.DecodeStrict(rawPart, &object); err != nil {
			return protocol.MessageContent{}, err
		}
		var header wireContentHeader
		if value, ok := object["type"]; !ok || json.Unmarshal(value, &header.Type) != nil {
			return protocol.MessageContent{}, errors.New("content block requires type")
		}
		switch header.Type {
		case "text":
			var p wireTextContent
			if err := native.DecodeStrict(rawPart, &p); err != nil {
				return protocol.MessageContent{}, err
			}
			out = append(out, protocol.ContentPart{Type: protocol.ContentText, Text: p.Text})
		case "thinking":
			var p wireThinkingContent
			if err := native.DecodeStrict(rawPart, &p); err != nil {
				return protocol.MessageContent{}, err
			}
			out = append(out, protocol.ContentPart{Type: protocol.ContentReasoning, Reasoning: p.Thinking})
		case "toolCall":
			var p wireToolCallContent
			if err := native.DecodeStrict(rawPart, &p); err != nil {
				return protocol.MessageContent{}, err
			}
			tool := s.tools[toolKey(run, p.ID)]
			if tool == nil {
				return protocol.MessageContent{}, fmt.Errorf("final message references unknown tool %q", p.ID)
			}
			if p.Name != tool.name {
				return protocol.MessageContent{}, fmt.Errorf("final message calls tool %q %q, which was started as %q", p.ID, p.Name, tool.name)
			}
			out = append(out, protocol.ContentPart{Type: protocol.ContentToolCall, ToolCallID: tool.id, Name: p.Name, ArgumentsJSON: cloneRaw(p.Arguments)})
		default:
			return protocol.MessageContent{}, fmt.Errorf("unknown message content %q", header.Type)
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
	if len(parts) == 0 {

		return protocol.TextContent("")
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

	if r.Method == native.ExtensionSelect {
		if len(r.Options) == 0 {
			s.failRun(run, "pi_invalid_extension", "select extension offered no options")
			return
		}
		for _, option := range r.Options {
			if option == "" {
				s.failRun(run, "pi_invalid_extension", "select extension offered an empty option label")
				return
			}
		}
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
	s.interactionsOpened++
	binding := &inputState{id: id, nativeID: r.ID, run: run, method: r.Method, requestedBy: endpointID, respondedBy: s.participant, questions: []protocol.InputQuestion{question}, opened: s.interactionsOpened}
	s.interactions[id] = binding
	run.status = protocol.RunWaitingForInput
	s.state.Status = protocol.SessionWaitingForInput
	_ = s.emit(run, protocol.TypeUserInputRequested, protocol.UserInputRequestedPayload{InteractionID: id, RequestedBy: endpointID, RespondedBy: s.participant, SessionID: s.state.SessionID, RunID: run.id, Title: r.Title, Description: r.Message, Questions: binding.questions, AllowCancel: true}, false)
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
	if binding.phase != interactionPending {
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
	binding.phase = interactionResolving

	if err := s.client.Respond(ctx, response); err != nil {
		binding.phase = interactionResolved

		s.failRunSettled(binding.run, "pi_interaction_response_failed", err.Error(), protocol.SettledByInferred)
		s.reduceMu.Unlock()
		return err
	}
	if binding.run.terminal {
		s.reduceMu.Unlock()
		return base.ErrInteractionResolved
	}
	binding.phase = interactionResolved
	_ = s.emit(binding.run, protocol.TypeUserInputResolved, protocol.UserInputResolvedPayload{InteractionID: binding.id, RequestedBy: binding.requestedBy, RespondedBy: binding.respondedBy, SessionID: s.state.SessionID, RunID: binding.run.id, Status: protocol.InputSubmitted, Answers: request.Answers}, false)
	status, pending := protocol.RunRunning, protocol.InteractionID("")
	if next := s.oldestPendingInput(binding.run); next != nil {
		status, pending = protocol.RunWaitingForInput, next.id
	}
	s.mu.Lock()
	if !binding.run.terminal {
		binding.run.status = status
		s.state.Status = protocol.SessionRunning
		if status == protocol.RunWaitingForInput {
			s.state.Status = protocol.SessionWaitingForInput
		}
	}
	s.mu.Unlock()
	_ = s.emit(binding.run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: binding.run.id, Status: status, PendingUserInputID: pending, UpdatedAtMS: s.clock.Now().UnixMilli()}, false)
	s.reduceMu.Unlock()
	return nil
}
func (s *Session) oldestPendingInput(run *runState) *inputState {
	var oldest *inputState
	for _, binding := range s.interactions {
		if binding.run != run || binding.phase == interactionResolved {
			continue
		}
		if oldest == nil || binding.opened < oldest.opened {
			oldest = binding
		}
	}
	return oldest
}

func extensionResponse(b *inputState, r protocol.UserInputResolveRequest) (native.ExtensionUIResponse, error) {
	if len(r.Answers) != 1 || len(b.questions) != 1 {
		return native.ExtensionUIResponse{}, base.ErrInvalidResolution
	}
	question, a := b.questions[0], r.Answers[0]
	if err := base.ValidateInputAnswer(question, a); err != nil {
		return native.ExtensionUIResponse{}, err
	}
	response := native.ExtensionUIResponse{Type: "extension_ui_response", ID: b.nativeID}
	switch b.method {
	case native.ExtensionConfirm:
		v := a.SelectedOptionIDs[0] == "yes"
		if !v && a.SelectedOptionIDs[0] != "no" {
			return response, base.ErrInvalidResolution
		}
		response.Confirmed = &v
	case native.ExtensionSelect:
		var n int
		if _, err := fmt.Sscanf(a.SelectedOptionIDs[0], "option-%d", &n); err != nil || n < 1 || n > len(question.Options) {
			return response, base.ErrInvalidResolution
		}
		v := question.Options[n-1].Label
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
	started := run.started
	if started {
		run.status = protocol.RunCancelling
	}
	s.mu.Unlock()
	if started {
		_ = s.emit(run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: id, Status: protocol.RunCancelling, UpdatedAtMS: s.clock.Now().UnixMilli()}, false)
	}
	s.reduceMu.Unlock()
	if err := s.callStrict(ctx, native.Command{Type: native.CommandAbort}, nil); err != nil {
		s.reduceMu.Lock()

		settledBy := protocol.SettledByInferred
		var remote *rpc.RemoteError
		if errors.As(err, &remote) {
			settledBy = ""
		}
		s.failRunSettled(run, "pi_abort_failed", err.Error(), settledBy)
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
		if i.run != run || i.phase == interactionResolved {
			continue
		}
		i.phase = interactionResolved

		_ = s.emit(run, protocol.TypeUserInputResolved, protocol.UserInputResolvedPayload{InteractionID: i.id, RequestedBy: i.requestedBy, RespondedBy: i.respondedBy, SessionID: s.state.SessionID, RunID: run.id, Status: protocol.InputCancelled}, false)
	}
}
func (s *Session) terminateBeforeStart(run *runState, err error) {
	s.mu.Lock()
	if run.terminal {
		s.mu.Unlock()
		return
	}
	run.terminal = true
	run.status = protocol.RunFailed
	if s.active == run {
		s.active = nil
	}
	s.state.Status = protocol.SessionIdle
	s.state.ActiveRunID = ""
	subscribers := run.subscribers
	run.subscribers = nil
	s.mu.Unlock()
	run.signalStart(err)
	for _, stream := range subscribers {
		close(stream)
	}
}

func (s *Session) failRun(run *runState, code, message string) {
	s.failRunSettled(run, code, message, "")
}

func (s *Session) failRunSettled(run *runState, code, message, settledBy string) {
	if run == nil {
		return
	}
	err := fmt.Errorf("%w: %s", ErrNativeProtocol, message)
	if !run.started {
		s.terminateBeforeStart(run, err)
		return
	}
	run.signalStart(err)
	s.settleChildren(run, true)
	_ = s.emit(run, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: s.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: code, Message: message}, SettledBy: settledBy}, true)
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
		s.failRunSettled(run, "pi_process_exit", fmt.Sprint(s.client.Err()), protocol.SettledByInferred)
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

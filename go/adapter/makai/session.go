package makai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/makai/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/makai/internal/stdio"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

var errTerminalWon = errors.New("makai adapter: terminal already selected")

const streamCapacity = 64

type session struct {
	mu             sync.Mutex
	emitMu         sync.Mutex
	transitionMu   sync.Mutex
	opMu           sync.Mutex
	client         Client
	inbound        <-chan stdio.Inbound
	clock          base.Clock
	ids            base.IDGenerator
	capacity       int
	nativeID       native.SessionID
	nativeSequence uint64
	participant    protocol.ParticipantID
	state          protocol.SessionState
	closed         bool
	unusable       bool
	active         *runState
	runs           map[protocol.RunID]*runState
	tools          map[string]*toolState

	provided []protocol.ToolDefinition
	journal  []protocol.Envelope
	stop     chan struct{}
	stopOnce sync.Once
}
type runState struct {
	id              protocol.RunID
	status          protocol.RunStatus
	next            uint64
	terminal        bool
	cancelRequested bool
	messageID       protocol.MessageID
	nativeMessageID native.MessageID
	text            string
	result          *native.Result
	admitted        chan struct{}
	subscribers     []chan base.Result

	call *callState

	calls map[protocol.InteractionID]*callState
}

type callState struct {
	interaction      protocol.InteractionID
	toolCallID       protocol.ToolCallID
	nativeID         string
	name             string
	args             json.RawMessage
	acknowledged     bool
	settledArm       string
	settledResult    json.RawMessage
	settledError     *protocol.ProtocolError
	settledRequestID protocol.EnvelopeID
	settlementID     protocol.EnvelopeID
	startedEvent     protocol.EnvelopeID
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

func (s *session) Submit(ctx context.Context, req protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}
	messageJSON, messageIDs, err := s.messageJSON(req)
	if err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}
	s.mu.Lock()
	if s.closed || s.unusable {
		s.mu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, base.ErrSessionClosed
	}
	if req.SessionID != s.state.SessionID {
		s.mu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, base.ErrRunNotFound
	}
	if s.active != nil && !s.active.terminal {
		s.mu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, base.ErrRunActive
	}
	run := &runState{id: protocol.RunID(s.ids.NewID("run")), status: protocol.RunRunning, next: 1, messageID: protocol.MessageID(s.ids.NewID("message")), admitted: make(chan struct{})}
	stream := make(chan base.Result, streamCapacity+1)
	run.subscribers = []chan base.Result{stream}
	s.active = run
	s.runs[run.id] = run
	s.state.Status = protocol.SessionRunning
	s.state.ActiveRunID = run.id

	s.state.UpdatedAtMS = s.clock.Now().UnixMilli()
	sequence := s.nativeSequence + 1
	s.mu.Unlock()
	env, err := s.newNativeEnvelope(native.TypeAgentMessage, sequence, native.AgentMessage{SessionID: s.nativeID, MessageJSON: string(messageJSON)})
	if err == nil {
		run.nativeMessageID = env.MessageID
		err = s.client.Send(ctx, env)
	}
	if err != nil {
		s.mu.Lock()
		s.unusable = true
		s.mu.Unlock()
		close(run.admitted)

		s.failRunSettled(run, "makai_admission_failed", err.Error(), protocol.SettledByInferred)
		return protocol.MessageSubmitResponse{}, stream, err
	}
	s.mu.Lock()
	s.nativeSequence = sequence
	s.mu.Unlock()
	if err := s.emit(run, protocol.TypeRunStarted, protocol.RunStartedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunRunning, ModelID: protocol.Control(req.ModelID), StartedAtMS: s.clock.Now().UnixMilli()}, false); err != nil {
		close(run.admitted)

		s.failRunSettled(run, "makai_admission_projection_failed", err.Error(), protocol.SettledByInferred)
		return protocol.MessageSubmitResponse{}, stream, err
	}
	response := protocol.MessageSubmitResponse{SessionID: s.state.SessionID, Accepted: true, SubmissionID: protocol.SubmissionID(s.ids.NewID("submission")), RequestedDelivery: protocol.DeliveryAuto, EffectiveDelivery: protocol.DeliveryStart, DeliveryResolution: "session_idle", Admission: protocol.AdmissionStarted, RunID: run.id, Status: protocol.RunRunning, ModelID: protocol.Control(req.ModelID), MessageIDs: messageIDs}
	close(run.admitted)
	return response, stream, nil
}

func (s *session) messageJSON(req protocol.MessageSubmitRequest) ([]byte, []protocol.MessageID, error) {

	if err := base.RefuseUnadvertisedControls(req, protocol.FeatureModelSelection); err != nil {
		return nil, nil, err
	}

	if req.ModelID != nil && *req.ModelID == "" {
		return nil, nil, &base.ModelNotFoundError{}
	}
	if req.SessionID == "" || len(req.Messages) == 0 || (req.Delivery != "" && req.Delivery != protocol.DeliveryAuto) {
		return nil, nil, base.ErrInvalidSubmission
	}
	messages := make([]map[string]any, len(req.Messages))
	ids := make([]protocol.MessageID, len(req.Messages))
	for i, message := range req.Messages {
		if message.Role != protocol.RoleUser {
			return nil, nil, fmt.Errorf("%w: Makai adapter accepts user text only", ErrUnsupportedInput)
		}
		text, ok := message.Content.Text()
		if !ok {
			return nil, nil, fmt.Errorf("%w: Makai adapter accepts user text only", ErrUnsupportedInput)
		}
		ids[i] = message.ID
		if ids[i] == "" {
			ids[i] = protocol.MessageID(s.ids.NewID("message"))
		}
		messages[i] = map[string]any{"role": "user", "content": []map[string]string{{"type": "text", "text": text}}}
	}
	model := protocol.Control(req.ModelID)
	if model == "" {
		model = "default"
	}

	encoded, err := json.Marshal(map[string]any{"model_ref": model, "messages": messages, "tools": s.nativeTools()})
	return encoded, ids, err
}

func (s *session) nativeTools() []native.ToolDefinition {
	s.mu.Lock()
	provided := s.provided
	s.mu.Unlock()
	tools := make([]native.ToolDefinition, 0, len(provided))
	for _, tool := range provided {
		schema := string(tool.InputSchema)
		if schema == "" {
			schema = "{}"
		}
		tools = append(tools, native.ToolDefinition{Name: tool.Name, Description: tool.Description, ParametersSchemaJSON: schema})
	}
	return tools
}

func (s *session) newNativeEnvelope(typ native.Type, sequence uint64, payload any) (native.Envelope, error) {
	id := native.MessageID(s.ids.NewID("makai-frame"))
	if !id.Valid() {
		return native.Envelope{}, errors.New("makai adapter: ID generator must produce a ULID for kind makai-frame")
	}
	return native.NewEnvelope(typ, s.nativeID, id, sequence, s.clock.Now().UnixMilli(), payload)
}

func (s *session) dispatch() {
	for {
		select {
		case inbound := <-s.inbound:
			switch {
			case inbound.Envelope != nil:
				s.handleEnvelope(*inbound.Envelope)
			case inbound.Barrier != nil:
				close(inbound.Barrier)
			}
		case <-s.client.Done():

			for {
				select {
				case inbound := <-s.inbound:
					switch {
					case inbound.Envelope != nil:
						s.handleEnvelope(*inbound.Envelope)
					case inbound.Barrier != nil:
						close(inbound.Barrier)
					}
				default:
					s.transportFailed()
					return
				}
			}
		case <-s.stop:
			return
		}
	}
}

func (s *session) handleEnvelope(env native.Envelope) {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	if env.SessionID != s.nativeID {
		s.mu.Lock()
		run := s.active
		s.mu.Unlock()
		if run != nil {
			s.failRun(run, "makai_foreign_session", "native observation belongs to another session")
		}
		return
	}
	s.mu.Lock()
	run := s.active
	s.mu.Unlock()
	if run == nil {
		return
	}
	<-run.admitted
	s.mu.Lock()
	terminal := run.terminal
	s.mu.Unlock()
	if terminal {
		return
	}
	switch env.Type {
	case native.TypeAgentEvent:
		payload, err := native.DecodePayload[native.AgentEvent](env)
		if err != nil {
			s.failRun(run, "makai_invalid_event", err.Error())
			return
		}
		s.applyEvent(run, payload.EventJSON)
	case native.TypeAgentResult:
		payload, err := native.DecodePayload[native.AgentResult](env)
		if err != nil {
			s.failRun(run, "makai_invalid_result", err.Error())
			return
		}
		result, err := native.DecodeResult(payload.ResultJSON)
		if err != nil {
			s.failRun(run, "makai_invalid_result", err.Error())
			return
		}
		s.mu.Lock()
		duplicate := run.result != nil
		s.mu.Unlock()
		if duplicate {
			s.failRun(run, "makai_duplicate_result", "received more than one agent_result")
			return
		}
		s.mu.Lock()
		run.result = &result
		s.mu.Unlock()
	case native.TypeAgentError:
		if env.InReplyTo != nil && *env.InReplyTo != run.nativeMessageID {
			return
		}
		payload, _ := native.DecodePayload[native.AgentError](env)

		if payload.Code == native.ErrorAgentNotFound {
			s.mu.Lock()
			s.unusable = true
			s.mu.Unlock()
		}
		s.failRun(run, string(payload.Code), payload.Message)
	case native.TypeToolExecute:
		payload, _ := native.DecodePayload[native.ToolExecute](env)
		s.openControlCall(run, payload)
	case native.TypeAgentStopped:
		s.mu.Lock()
		s.unusable = true
		s.mu.Unlock()

		s.failRunSettled(run, "makai_unsolicited_session_stop", "Makai stopped the native session without a pending cancellation", protocol.SettledByInferred)
	case native.TypeToolStreaming, native.TypeToolResult, native.TypeSessionInfo, native.TypeAck, native.TypeNack, native.TypePong, native.TypeGoodbye:
		return
	default:
		s.failRun(run, "makai_unexpected_frame", fmt.Sprintf("unexpected native frame %q during run", env.Type))
	}
}

func (s *session) applyEvent(run *runState, raw string) {
	header, err := native.DecodeEvent(raw, nil)
	if err != nil {
		s.failRun(run, "makai_invalid_event", err.Error())
		return
	}
	switch header.Type {
	case native.EventAgentStart, native.EventTurnStart, native.EventTurnEnd, native.EventMessageStart, native.EventMessageEnd, native.EventContextUsage, native.EventPromptSegmentUsage:
		return
	case native.EventMessageUpdate:
		var event native.MessageUpdateEvent
		if _, err := native.DecodeEvent(raw, &event); err != nil {
			s.failRun(run, "makai_invalid_message_update", err.Error())
			return
		}
		s.applyMessageUpdate(run, event.Event)
	case native.EventToolExecutionStart:
		var event native.ToolExecutionStartEvent
		if _, err := native.DecodeEvent(raw, &event); err != nil || event.ToolCallID == "" || event.ToolName == "" || !json.Valid([]byte(event.ArgsJSON)) {
			s.failRun(run, "makai_invalid_tool_start", "invalid tool_execution_start")
			return
		}
		s.startTool(run, event.ToolCallID, event.ToolName, json.RawMessage(event.ArgsJSON))
	case native.EventToolExecutionUpdate:
		var event native.ToolExecutionUpdateEvent
		if _, err := native.DecodeEvent(raw, &event); err != nil || event.ToolCallID == "" || !json.Valid([]byte(event.PartialResultJSON)) {
			s.failRun(run, "makai_invalid_tool_update", "invalid tool_execution_update")
			return
		}
		s.updateTool(run, event.ToolCallID, json.RawMessage(event.PartialResultJSON))
	case native.EventToolExecutionEnd:
		var event native.ToolExecutionEndEvent
		if _, err := native.DecodeEvent(raw, &event); err != nil || event.ToolCallID == "" || !json.Valid([]byte(event.ResultJSON)) {
			s.failRun(run, "makai_invalid_tool_end", "invalid tool_execution_end")
			return
		}
		s.endTool(run, event.ToolCallID, event.ToolName, json.RawMessage(event.ResultJSON), event.IsError)
	case native.EventAgentEnd:
		var event native.AgentEndEvent
		if _, err := native.DecodeEvent(raw, &event); err != nil {
			s.failRun(run, "makai_invalid_agent_end", err.Error())
			return
		}
		s.finishRun(run, event)
	default:
		s.failRun(run, "makai_unknown_event", fmt.Sprintf("unknown stable Makai event %q", header.Type))
	}
}

func (s *session) applyMessageUpdate(run *runState, raw json.RawMessage) {
	header, err := native.DecodeProviderEvent(raw, nil)
	if err != nil {
		s.failRun(run, "makai_invalid_provider_event", err.Error())
		return
	}
	var delta string
	var part protocol.ContentPart
	switch header.Type {
	case "text_delta":
		var event native.TextDeltaEvent
		_, err = native.DecodeProviderEvent(raw, &event)
		delta, part = event.Delta, protocol.ContentPart{Type: protocol.ContentText, Text: event.Delta}
	case "thinking_delta":
		var event native.ReasoningDeltaEvent
		_, err = native.DecodeProviderEvent(raw, &event)
		delta, part = event.Delta, protocol.ContentPart{Type: protocol.ContentReasoning, Reasoning: event.Delta}
	case "start", "text_start", "text_end", "thinking_start", "thinking_end", "toolcall_start", "toolcall_delta", "toolcall_end", "done", "error", "keepalive":
		return
	default:
		s.failRun(run, "makai_unknown_provider_event", fmt.Sprintf("unknown provider event %q", header.Type))
		return
	}
	if err != nil {
		s.failRun(run, "makai_invalid_provider_event", err.Error())
		return
	}
	if header.Type == "text_delta" {
		s.mu.Lock()
		run.text += delta
		s.mu.Unlock()
	}
	_ = s.emit(run, protocol.TypeContentDelta, protocol.ContentDeltaPayload{SessionID: s.state.SessionID, RunID: run.id, MessageID: run.messageID, Part: part}, false)
}

func (s *session) startTool(run *runState, nativeID, name string, args json.RawMessage) {
	s.mu.Lock()
	tool := s.tools[toolKey(run, nativeID)]
	if tool == nil {
		tool = &toolState{nativeID: nativeID, id: protocol.ToolCallID(s.ids.NewID("tool-call")), run: run, name: name, args: cloneRaw(args)}
		s.tools[toolKey(run, nativeID)] = tool
	}
	if tool.run != run || tool.terminal || tool.started {
		s.mu.Unlock()
		s.failRun(run, "makai_invalid_tool_lifecycle", "duplicate or foreign tool start")
		return
	}
	tool.started = true
	s.mu.Unlock()
	requested, _ := s.emitEnvelope(run, protocol.TypeActionCallRequested, s.toolPayload(tool), false, "")
	payload := s.toolPayload(tool)
	payload.ArgumentsJSON = nil
	payload.Progress = nil
	started, _ := s.emitEnvelope(run, protocol.TypeActionCallStarted, payload, false, requested.ID)
	s.mu.Lock()
	tool.requestedEvent = requested.ID
	tool.startedEvent = started.ID
	s.mu.Unlock()
}
func (s *session) updateTool(run *runState, nativeID string, progress json.RawMessage) {
	s.mu.Lock()
	tool := s.tools[toolKey(run, nativeID)]
	if tool == nil || tool.run != run || !tool.started || tool.terminal {
		s.mu.Unlock()
		s.failRun(run, "makai_invalid_tool_lifecycle", "tool update without active start")
		return
	}
	tool.progress = cloneRaw(progress)
	s.mu.Unlock()
	payload := s.toolPayload(tool)
	payload.ArgumentsJSON = nil
	_, _ = s.emitEnvelope(run, protocol.TypeActionCallProgress, payload, false, tool.startedEvent)
}
func (s *session) endTool(run *runState, nativeID, name string, result json.RawMessage, failed bool) {
	s.mu.Lock()
	tool := s.tools[toolKey(run, nativeID)]
	if tool == nil {
		s.mu.Unlock()
		s.failRun(run, "makai_invalid_tool_lifecycle", "tool end without active start")
		return
	}
	if tool.run != run || tool.terminal {
		s.mu.Unlock()
		s.failRun(run, "makai_invalid_tool_lifecycle", "duplicate or foreign tool end")
		return
	}
	tool.result = cloneRaw(result)
	tool.terminal = true
	s.mu.Unlock()
	payload := s.toolPayload(tool)
	payload.ArgumentsJSON = nil
	payload.Progress = nil
	if failed {
		payload.Result = nil
		payload.Error = &protocol.ProtocolError{Code: "tool_failed", Message: "Makai tool execution failed"}
		_, _ = s.emitEnvelope(run, protocol.TypeActionCallFailed, payload, false, tool.startedEvent)
	} else {
		_, _ = s.emitEnvelope(run, protocol.TypeActionCallCompleted, payload, false, tool.startedEvent)
	}
}

func (s *session) openControlCall(run *runState, payload native.ToolExecute) {
	s.mu.Lock()
	var definition *protocol.ToolDefinition
	for i := range s.provided {
		if s.provided[i].Name == payload.ToolName {
			definition = &s.provided[i]
			break
		}
	}
	if definition == nil {
		s.mu.Unlock()
		s.failRun(run, "makai_tool_executor_unavailable", fmt.Sprintf("client-hosted tool %q (%s) cannot be executed", payload.ToolName, payload.ToolCallID))
		return
	}
	if run.call != nil && run.call.settledArm == "" {

		s.mu.Unlock()
		s.failRun(run, "makai_invalid_tool_lifecycle", "a second tool_execute arrived while one was pending")
		return
	}
	call := &callState{
		interaction: protocol.InteractionID(s.ids.NewID("call")),
		toolCallID:  protocol.ToolCallID(s.ids.NewID("tool-call")),
		nativeID:    payload.ToolCallID,
		name:        payload.ToolName,
		args:        json.RawMessage(payload.ArgsJSON),
	}
	if !json.Valid(call.args) {
		s.mu.Unlock()
		s.failRun(run, "makai_invalid_tool_lifecycle", "tool_execute carried args that are not JSON")
		return
	}
	run.call = call
	if run.calls == nil {
		run.calls = map[protocol.InteractionID]*callState{}
	}
	run.calls[call.interaction] = call
	owner := definition.ExecutionOwner
	s.mu.Unlock()
	requested := s.callPayload(run, call, "")
	requested.ExecutionOwner = owner
	requested.ArgumentsJSON = cloneRaw(call.args)
	_, _ = s.emitEnvelope(run, protocol.TypeActionCallRequested, requested, false, "")
}

func (s *session) ResolveCall(ctx context.Context, resolution base.CallResolution) (protocol.ActionCallResolveResponse, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return protocol.ActionCallResolveResponse{}, err
	}
	request := resolution.Request
	answer := protocol.ActionCallResolveResponse{
		InteractionID: request.InteractionID, SessionID: request.SessionID,
		RunID: request.RunID, ToolCallID: request.ToolCallID,
	}
	refuse := func(reason protocol.ResolveReason, settlement protocol.EnvelopeID) (protocol.ActionCallResolveResponse, error) {
		answer.Accepted, answer.Reason = false, reason
		if reason == protocol.ReasonAlreadyResolved {
			answer.Details = &protocol.ActionCallResolveDetails{SettlementID: settlement}
		}
		return answer, nil
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return protocol.ActionCallResolveResponse{}, base.ErrSessionClosed
	}
	run := s.runs[request.RunID]
	if run == nil || request.SessionID != s.state.SessionID {
		s.mu.Unlock()
		return refuse(protocol.ReasonUnknownInteraction, "")
	}

	call := run.calls[request.InteractionID]
	if call == nil {
		s.mu.Unlock()
		return refuse(protocol.ReasonUnknownInteraction, "")
	}
	if request.RespondedBy != s.participant || request.RequestedBy != endpointID || request.ToolCallID != call.toolCallID {
		s.mu.Unlock()
		return refuse(protocol.ReasonWrongResponder, "")
	}
	arm := request.Arm()
	if arm == "" {
		s.mu.Unlock()
		return refuse(protocol.ReasonUnknownInteraction, "")
	}
	if call.settledArm != "" {
		settlement := call.settlementID
		if settlement == "" {
			settlement = call.settledRequestID
		}
		if arm == protocol.ResolveArmAcknowledge && call.settlementID == "" {
			s.mu.Unlock()
			return refuse(protocol.ReasonLateAcknowledgement, "")
		}
		s.mu.Unlock()
		return refuse(protocol.ReasonAlreadyResolved, settlement)
	}
	if run.terminal || run.call != call {

		s.mu.Unlock()
		return refuse(protocol.ReasonAlreadyResolved, call.settlementID)
	}
	if arm == protocol.ResolveArmAcknowledge && call.acknowledged {
		s.mu.Unlock()
		return refuse(protocol.ReasonRepeatedAcknowledgement, "")
	}

	answer.Accepted = true
	s.mu.Unlock()

	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()

	s.mu.Lock()
	if run.terminal || call.settlementID != "" {
		settlement := call.settlementID
		s.mu.Unlock()
		return refuse(protocol.ReasonAlreadyResolved, settlement)
	}
	if arm == protocol.ResolveArmAcknowledge {
		call.acknowledged = true
		s.mu.Unlock()
		started := s.callPayload(run, call, resolution.RequestID)
		started.ArgumentsJSON = cloneRaw(call.args)
		event, err := s.emitEnvelope(run, protocol.TypeActionCallStarted, started, false, "")
		if err != nil {

			s.mu.Lock()
			call.acknowledged = false
			s.mu.Unlock()
			return protocol.ActionCallResolveResponse{}, err
		}
		s.mu.Lock()
		call.startedEvent = event.ID
		s.mu.Unlock()
		return answer, nil
	}
	call.settledArm = arm
	call.settledRequestID = resolution.RequestID
	call.settledResult = cloneRaw(request.Result)
	call.settledError = request.Error
	acknowledged := call.acknowledged
	sequence := s.nativeSequence + 1
	s.mu.Unlock()

	if err := s.writeToolResult(ctx, call, sequence); err != nil {

		s.mu.Lock()
		call.settledArm, call.settledRequestID = "", ""
		call.settledResult, call.settledError = nil, nil
		s.mu.Unlock()
		return protocol.ActionCallResolveResponse{}, err
	}

	s.mu.Lock()
	s.nativeSequence = sequence
	s.mu.Unlock()
	s.settleControlCall(run, call, acknowledged)
	return answer, nil
}

func (s *session) writeToolResult(ctx context.Context, call *callState, sequence uint64) error {
	result := call.settledResult
	if call.settledArm == protocol.ResolveArmError {
		encoded, err := json.Marshal(call.settledError)
		if err != nil {
			return err
		}
		result = encoded
	}
	if len(result) == 0 {
		result = json.RawMessage("null")
	}
	env, err := s.newNativeEnvelope(native.TypeToolResult, sequence, native.ToolResult{
		ToolCallID: call.nativeID, ResultJSON: string(result), IsError: call.settledArm == protocol.ResolveArmError,
	})
	if err != nil {
		return err
	}
	return s.client.Send(ctx, env)
}

func (s *session) settleControlCall(run *runState, call *callState, acknowledged bool) {
	if !acknowledged {
		started := s.callPayload(run, call, call.settledRequestID)
		started.ArgumentsJSON = cloneRaw(call.args)
		event, err := s.emitEnvelope(run, protocol.TypeActionCallStarted, started, false, "")
		if err != nil {
			return
		}
		s.mu.Lock()
		call.startedEvent = event.ID
		s.mu.Unlock()
	}
	terminal := s.callPayload(run, call, call.settledRequestID)
	typ := protocol.TypeActionCallCompleted
	if call.settledArm == protocol.ResolveArmError {
		typ = protocol.TypeActionCallFailed
		terminal.Error = call.settledError
	} else {
		terminal.Result = cloneRaw(call.settledResult)
		if len(terminal.Result) == 0 {
			terminal.Result = json.RawMessage("null")
		}
	}
	event, err := s.emitEnvelope(run, typ, terminal, false, call.startedEvent)
	if err != nil {

		return
	}
	s.mu.Lock()
	call.settlementID = event.ID
	s.mu.Unlock()
}

func (s *session) callPayload(run *runState, call *callState, requestID protocol.EnvelopeID) protocol.ActionCallPayload {
	return protocol.ActionCallPayload{
		InteractionID: call.interaction, RequestID: requestID,
		SessionID: s.state.SessionID, RunID: run.id, ToolCallID: call.toolCallID,
		RequestedBy: endpointID, RespondedBy: s.participant,
		ExecutionOwner: s.participant, Name: call.name,
	}
}

func toolKey(run *runState, nativeID string) string {
	return string(run.id) + "\x00" + nativeID
}

func (s *session) toolPayload(tool *toolState) protocol.ActionCallPayload {
	return protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: tool.run.id, ToolCallID: tool.id, RequestedBy: endpointID, ExecutionOwner: "makai-agent", Name: tool.name, ArgumentsJSON: cloneRaw(tool.args), Progress: cloneRaw(tool.progress), Result: cloneRaw(tool.result)}
}

func (s *session) finishRun(run *runState, end native.AgentEndEvent) {
	s.mu.Lock()
	result := run.result
	cancelRequested := run.cancelRequested
	s.mu.Unlock()
	stopReason := end.StopReason
	if result != nil && result.StopReason != "" {
		if stopReason != "" && stopReason != result.StopReason {
			s.failRun(run, "makai_terminal_contradiction", "agent_result and agent_end disagree")
			return
		}
		stopReason = result.StopReason
	}
	if end.ErrorMessage != "" || (result != nil && result.ErrorMessage != "") || stopReason == "error" || stopReason == "aborted" || stopReason == "content_filter" {
		message := end.ErrorMessage
		if message == "" && result != nil {
			message = result.ErrorMessage
		}
		if message == "" {
			message = "Makai run failed with stop reason " + stopReason
		}
		s.failRun(run, "makai_run_failed", message)
		return
	}
	s.settleTools(run, stopReason == "cancelled")
	if stopReason == "cancelled" {
		s.mu.Lock()
		s.unusable = true
		s.mu.Unlock()

		reason := "cancelled at the host's request"
		if !cancelRequested {
			reason = "cancelled by Makai without a host request"
		}
		_ = s.emit(run, protocol.TypeRunCancelled, protocol.RunCancelledPayload{SessionID: s.state.SessionID, RunID: run.id, Reason: reason}, true)
		return
	}
	if stopReason == "" {
		s.failRun(run, "makai_missing_stop_reason", "agent_end omitted stop reason")
		return
	}
	message := protocol.Message{ID: run.messageID, Role: protocol.RoleAssistant, Content: protocol.TextContent(run.text)}
	usage := (*protocol.Usage)(nil)
	resultJSON := json.RawMessage(nil)
	if result != nil {
		content, err := s.resultContent(run, *result, run.text)
		if err != nil {
			s.failRun(run, "makai_invalid_result_tool", err.Error())
			return
		}
		message.Content = content
		usage = &protocol.Usage{InputTokens: result.Input, OutputTokens: result.Output, TotalTokens: result.Input + result.Output}

		resultJSON, _ = json.Marshal(map[string]any{"provider": result.Provider, "api": result.API, "model": result.Model, "cache_read": result.CacheRead, "cache_write": result.CacheWrite})
	}
	_ = s.emit(run, protocol.TypeRunCompleted, protocol.RunCompletedPayload{SessionID: s.state.SessionID, RunID: run.id, FinalResponse: message, StopReason: stopReason, Result: resultJSON, Usage: usage}, true)
}

func (s *session) resultContent(run *runState, result native.Result, fallback string) (protocol.MessageContent, error) {
	parts := make([]protocol.ContentPart, 0, len(result.Content))
	for _, content := range result.Content {
		switch content.Type {
		case "text":
			parts = append(parts, protocol.ContentPart{Type: protocol.ContentText, Text: content.Text})
		case "thinking":
			parts = append(parts, protocol.ContentPart{Type: protocol.ContentReasoning, Reasoning: content.Thinking})
		case "tool_call":
			s.mu.Lock()
			tool := s.tools[toolKey(run, content.ID)]
			s.mu.Unlock()
			if tool == nil {
				return protocol.MessageContent{}, fmt.Errorf("result references unobserved tool call %q", content.ID)
			}
			parts = append(parts, protocol.ContentPart{Type: protocol.ContentToolCall, ToolCallID: tool.id, Name: content.Name, ArgumentsJSON: json.RawMessage(content.ArgumentsJSON)})
		case "image":
			parts = append(parts, protocol.ContentPart{Type: protocol.ContentImage, Image: &protocol.ImageContent{Data: content.Data, MediaType: content.MimeType}})
		}
	}
	if len(parts) == 0 {
		return protocol.TextContent(fallback), nil
	}
	return protocol.PartsContent(parts), nil
}

func (s *session) State(ctx context.Context) (protocol.SessionState, error) {
	if err := ctx.Err(); err != nil {
		return protocol.SessionState{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.unusable {
		return s.state, base.ErrSessionClosed
	}
	return s.state, nil
}
func (s *session) Resolve(ctx context.Context, resolution base.InteractionResolution) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return base.ErrInteractionNotFound
}
func (s *session) Cancel(ctx context.Context, id protocol.RunID) (protocol.RunCancelResponse, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return protocol.RunCancelResponse{}, err
	}
	s.mu.Lock()
	if s.closed || s.unusable {
		s.mu.Unlock()
		return protocol.RunCancelResponse{}, base.ErrSessionClosed
	}
	run := s.runs[id]
	if run == nil {
		s.mu.Unlock()
		return protocol.RunCancelResponse{}, base.ErrRunNotFound
	}
	if run.terminal {
		status := run.status
		s.mu.Unlock()
		if status == protocol.RunCancelled {
			return protocol.RunCancelResponse{SessionID: s.state.SessionID, RunID: id, Accepted: true, Status: status}, nil
		}
		return protocol.RunCancelResponse{}, &base.RunTerminalError{RunID: id, Status: status}
	}
	if run.cancelRequested {
		s.mu.Unlock()
		return protocol.RunCancelResponse{SessionID: s.state.SessionID, RunID: id, Accepted: true, Status: protocol.RunCancelling}, nil
	}
	sequence := s.nativeSequence + 1
	s.mu.Unlock()
	request, err := s.newNativeEnvelope(native.TypeAgentStop, sequence, native.AgentStop{SessionID: s.nativeID, Reason: "OAP run cancellation requested"})
	if err != nil {
		return protocol.RunCancelResponse{}, err
	}
	response, err := s.client.Call(ctx, request, native.TypeAgentStopped, native.TypeAgentError)
	if err != nil {
		s.mu.Lock()
		s.unusable = true
		s.mu.Unlock()
		s.failRunSettled(run, "makai_cancellation_ambiguous", err.Error(), protocol.SettledByInferred)
		return protocol.RunCancelResponse{}, err
	}
	if response.Type == native.TypeAgentError {
		payload, _ := native.DecodePayload[native.AgentError](response)
		return protocol.RunCancelResponse{}, fmt.Errorf("Makai agent_stop failed: %s: %s", payload.Code, payload.Message)
	}
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	s.mu.Lock()
	if run.terminal {
		status := run.status
		s.mu.Unlock()
		return protocol.RunCancelResponse{}, &base.RunTerminalError{RunID: id, Status: status}
	}
	run.cancelRequested = true
	run.status = protocol.RunCancelling
	s.unusable = true
	s.nativeSequence = sequence
	s.mu.Unlock()
	_ = s.emit(run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: id, Status: protocol.RunCancelling, UpdatedAtMS: s.clock.Now().UnixMilli()}, false)

	s.settleTools(run, true)
	_ = s.emit(run, protocol.TypeRunCancelled, protocol.RunCancelledPayload{SessionID: s.state.SessionID, RunID: id, Reason: "destructive session stop", SettledBy: protocol.SettledByInferred}, true)
	return protocol.RunCancelResponse{SessionID: s.state.SessionID, RunID: id, Accepted: true, Status: protocol.RunCancelled}, nil
}
func (s *session) Resume(ctx context.Context, request base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	if err := ctx.Err(); err != nil {
		return base.Recovery{}, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return base.Recovery{}, nil, base.ErrSessionClosed
	}
	run := s.runs[request.RunID]
	if run == nil {
		return base.Recovery{}, nil, base.ErrRunNotFound
	}
	latest := run.next - 1
	if request.AfterSequence > latest {
		return base.Recovery{}, nil, base.ErrReplayCursorFuture
	}
	var oldest uint64
	var suffix []protocol.Envelope
	for _, event := range s.journal {
		if event.RunID != run.id || event.Sequence == nil {
			continue
		}
		if oldest == 0 {
			oldest = *event.Sequence
		}
		if *event.Sequence > request.AfterSequence {
			suffix = append(suffix, event)
		}
	}
	recovery := base.Recovery{State: s.state, RunID: run.id, RequestedAfter: request.AfterSequence, ReplayedFrom: request.AfterSequence, ReplayedThrough: request.AfterSequence}
	stream := make(chan base.Result, len(suffix)+streamCapacity+1)
	if request.AfterSequence < latest && (oldest == 0 || request.AfterSequence+1 < oldest) {
		recovery.ReplayGap = &base.ReplayGap{RequestedAfter: request.AfterSequence, OldestAvailable: oldest, LatestAvailable: latest}
		close(stream)
		return recovery, stream, recovery.ReplayGap
	}
	if len(suffix) > 0 {
		recovery.ReplayedFrom = *suffix[0].Sequence
		recovery.ReplayedThrough = *suffix[len(suffix)-1].Sequence
	}
	for _, event := range suffix {
		stream <- base.Result{Envelope: event}
	}
	if run.terminal {
		close(stream)
	} else {
		run.subscribers = append(run.subscribers, stream)
	}
	return recovery, stream, nil
}
func (s *session) Close(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	if s.active != nil && !s.active.terminal {
		s.mu.Unlock()
		return base.ErrRunActive
	}
	s.closed = true
	s.state.Status = protocol.SessionClosed
	s.state.ActiveRunID = ""
	s.state.UpdatedAtMS = s.clock.Now().UnixMilli()
	subscribers := s.allSubscribersLocked()
	s.mu.Unlock()
	s.stopOnce.Do(func() { close(s.stop) })
	err := s.client.Close()
	for _, stream := range subscribers {
		close(stream)
	}
	return err
}

func (s *session) settleTools(run *runState, cancel bool) {
	s.closeControlCall(run)
	s.mu.Lock()
	var tools []*toolState
	for _, tool := range s.tools {
		if tool.run == run && !tool.terminal {
			tool.terminal = true
			tools = append(tools, tool)
		}
	}
	s.mu.Unlock()
	for _, tool := range tools {
		payload := s.toolPayload(tool)
		payload.ArgumentsJSON = nil
		if cancel {
			_, _ = s.emitEnvelope(run, protocol.TypeActionCallCancelled, payload, false, tool.startedEvent)
		} else {
			payload.Result = nil
			payload.Error = &protocol.ProtocolError{Code: "incomplete_tool", Message: "Makai run settled with an unfinished tool"}
			_, _ = s.emitEnvelope(run, protocol.TypeActionCallFailed, payload, false, tool.startedEvent)
		}
	}
}

func (s *session) closeControlCall(run *runState) {
	s.mu.Lock()
	call := run.call

	if call == nil || call.settlementID != "" {
		s.mu.Unlock()
		return
	}
	call.settledArm = protocol.ResolveArmError
	started := call.startedEvent
	s.mu.Unlock()

	payload := s.callPayload(run, call, "")
	event, err := s.emitEnvelope(run, protocol.TypeActionCallCancelled, payload, false, started)
	if err != nil {
		return
	}
	s.mu.Lock()
	call.settlementID = event.ID
	s.mu.Unlock()
}

func (s *session) failRun(run *runState, code, message string) {
	s.failRunSettled(run, code, message, "")
}

func (s *session) failRunSettled(run *runState, code, message, settledBy string) {
	s.settleTools(run, true)
	_ = s.emit(run, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: s.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: code, Message: message}, SettledBy: settledBy}, true)
}
func (s *session) transportFailed() {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	s.mu.Lock()
	run := s.active
	closed := s.closed
	if !closed {
		s.unusable = true
	}
	s.mu.Unlock()
	if !closed && run != nil {
		<-run.admitted
		s.failRunSettled(run, "makai_transport_failure", fmt.Sprint(s.client.Err()), protocol.SettledByInferred)
	}
}
func (s *session) emit(run *runState, typ protocol.EnvelopeType, payload any, terminal bool) error {
	_, err := s.emitEnvelope(run, typ, payload, terminal, "")
	return err
}

func (s *session) emitEnvelope(run *runState, typ protocol.EnvelopeType, payload any, terminal bool, inReplyTo protocol.EnvelopeID) (protocol.Envelope, error) {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if run.terminal {
		return protocol.Envelope{}, errTerminalWon
	}
	event, err := protocol.NewEnvelope(typ, protocol.EnvelopeID(s.ids.NewID("event")), payload)
	if err != nil {
		return protocol.Envelope{}, err
	}
	now := s.clock.Now().UnixMilli()
	sequence := run.next
	run.next++
	event.Sequence = &sequence
	event.TimestampMS = &now
	event.SessionID = s.state.SessionID
	event.RunID = run.id
	event.CapabilityRevision = CapabilityRevision
	event.InReplyTo = inReplyTo
	if typ == protocol.TypeActionCallRequested || typ == protocol.TypeActionCallStarted || typ == protocol.TypeActionCallProgress || typ == protocol.TypeActionCallCompleted || typ == protocol.TypeActionCallFailed || typ == protocol.TypeActionCallCancelled {
		var action struct {
			ToolCallID protocol.ToolCallID `json:"tool_call_id"`
		}
		_ = json.Unmarshal(event.Payload, &action)
		event.ToolCallID = action.ToolCallID
	}
	s.journal = append(s.journal, event)
	if len(s.journal) > s.capacity {
		s.journal = append([]protocol.Envelope(nil), s.journal[len(s.journal)-s.capacity:]...)
	}
	s.state.TranscriptCursor = strconv.FormatUint(sequence, 10)
	s.state.UpdatedAtMS = now
	if terminal {
		run.terminal = true
		switch typ {
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
	var retained []chan base.Result
	for _, stream := range run.subscribers {
		if len(stream) < cap(stream)-1 {
			stream <- base.Result{Envelope: event}
			if terminal {
				close(stream)
			} else {
				retained = append(retained, stream)
			}
		} else {
			stream <- base.Result{Error: base.ErrEventStreamOverflow}
			close(stream)
		}
	}
	if !terminal {
		run.subscribers = retained
	} else {
		run.subscribers = nil
	}
	return event, nil
}
func (s *session) allSubscribersLocked() []chan base.Result {
	var out []chan base.Result
	for _, run := range s.runs {
		out = append(out, run.subscribers...)
		run.subscribers = nil
	}
	return out
}
func cloneRaw(value json.RawMessage) json.RawMessage { return append(json.RawMessage(nil), value...) }

var _ base.Session = (*session)(nil)

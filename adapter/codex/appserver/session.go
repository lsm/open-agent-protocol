package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/codex/appserver/internal/native"
	"github.com/lsm/open-agent-protocol/adapter/codex/appserver/internal/rpc"
	"github.com/lsm/open-agent-protocol/protocol"
)

type session struct {
	mu       sync.Mutex
	opMu     sync.Mutex
	emitMu   sync.Mutex
	client   Client
	clock    adapter.Clock
	ids      adapter.IDGenerator
	capacity int

	participant protocol.ParticipantID
	threadID    string
	model       string
	state       protocol.SessionState
	closed      bool
	active      *runState
	runs        map[protocol.RunID]*runState
	turns       map[string]protocol.RunID
	items       map[string]itemBinding
	journal     []protocol.Envelope
	stop        chan struct{}
}

type runState struct {
	id            protocol.RunID
	turnID        string
	status        protocol.RunStatus
	nextSequence  uint64
	started       bool
	terminal      bool
	cancelPending bool
	messageID     protocol.MessageID
	text          string
	subscribers   []chan adapter.Result
}

type itemBinding struct {
	runID      protocol.RunID
	toolCallID protocol.ToolCallID
	name       string
	arguments  json.RawMessage
	started    bool
	terminal   bool
}

func (session *session) Submit(ctx context.Context, request protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, adapter.EventStream, error) {
	session.opMu.Lock()
	defer session.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}
	if request.SessionID != session.state.SessionID || len(request.Messages) == 0 {
		return protocol.MessageSubmitResponse{}, nil, adapter.ErrInvalidSubmission
	}
	if request.Delivery != "" && request.Delivery != protocol.DeliveryAuto {
		return protocol.MessageSubmitResponse{}, nil, fmt.Errorf("%w: delivery %q", adapter.ErrInvalidSubmission, request.Delivery)
	}
	if request.Instructions != "" || len(request.ToolChoice) != 0 || len(request.OutputSchema) != 0 || len(request.AllowDegradedFeatures) != 0 || len(request.Metadata) != 0 {
		return protocol.MessageSubmitResponse{}, nil, fmt.Errorf("%w: instructions, tool choice, output schema, degraded-feature consent, and metadata are not supported", adapter.ErrInvalidSubmission)
	}
	input, messageIDs, err := session.nativeInput(request.Messages)
	if err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, adapter.ErrSessionClosed
	}
	if session.active != nil && !session.active.terminal {
		session.mu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, adapter.ErrRunActive
	}
	session.mu.Unlock()

	params := native.TurnStartParams{ThreadID: session.threadID, Input: input, Model: request.ModelID}
	if params.Model == "" {
		params.Model = session.model
	}
	run := &runState{
		id: protocol.RunID(session.ids.NewID("run")), status: protocol.RunQueued,
		nextSequence: 1, messageID: protocol.MessageID(session.ids.NewID("message")),
	}
	stream := make(chan adapter.Result, session.capacity+32)
	run.subscribers = append(run.subscribers, stream)
	// Reserve the one active-run slot before calling Codex. The dispatch loop uses
	// opMu too, so notifications cannot overtake response admission and mapping.
	session.mu.Lock()
	session.active = run
	session.state.Status = protocol.SessionQueued
	session.state.ActiveRunID = run.id
	session.state.CurrentModelID = params.Model
	session.state.UpdatedAtMS = session.clock.Now().UnixMilli()
	session.mu.Unlock()
	var nativeResponse native.TurnStartResponse
	if err := session.client.Call(ctx, native.MethodTurnStart, params, &nativeResponse); err != nil {
		session.mu.Lock()
		session.active = nil
		session.state.Status = protocol.SessionIdle
		session.state.ActiveRunID = ""
		session.mu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, fmt.Errorf("start Codex turn: %w", err)
	}
	if nativeResponse.Turn.ID == "" {
		session.mu.Lock()
		session.active = nil
		session.state.Status = protocol.SessionIdle
		session.state.ActiveRunID = ""
		session.mu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, fmt.Errorf("%w: turn/start returned no turn id", ErrNativeProtocol)
	}
	run.turnID = nativeResponse.Turn.ID
	session.mu.Lock()
	session.runs[run.id] = run
	session.turns[run.turnID] = run.id
	session.mu.Unlock()

	return protocol.MessageSubmitResponse{
		SessionID: session.state.SessionID, Accepted: true,
		SubmissionID:      protocol.SubmissionID(session.ids.NewID("submission")),
		RequestedDelivery: protocol.DeliveryAuto, EffectiveDelivery: protocol.DeliveryStart,
		DeliveryResolution: "session_idle", Admission: protocol.AdmissionStarted,
		RunID: run.id, Status: protocol.RunQueued, ModelID: params.Model, MessageIDs: messageIDs,
	}, stream, nil
}

func (session *session) nativeInput(messages []protocol.Message) ([]native.UserInput, []protocol.MessageID, error) {
	input := make([]native.UserInput, 0, len(messages))
	ids := make([]protocol.MessageID, len(messages))
	for index, message := range messages {
		if message.Role != protocol.RoleUser {
			return nil, nil, fmt.Errorf("%w: role %q; turn/start accepts new user input only", ErrUnsupportedInput, message.Role)
		}
		text, ok := message.Content.Text()
		if !ok {
			return nil, nil, fmt.Errorf("%w: only text messages are supported", ErrUnsupportedInput)
		}
		input = append(input, native.UserInput{Type: "text", Text: text, TextElements: []native.TextElement{}})
		ids[index] = message.ID
		if ids[index] == "" {
			ids[index] = protocol.MessageID(session.ids.NewID("message"))
		}
	}
	return input, ids, nil
}

func (session *session) State(ctx context.Context) (protocol.SessionState, error) {
	if err := ctx.Err(); err != nil {
		return protocol.SessionState{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return session.state, adapter.ErrSessionClosed
	}
	return session.state, nil
}

func (session *session) Resolve(context.Context, adapter.InteractionResolution) error {
	return adapter.ErrInteractionNotFound
}

func (session *session) Cancel(ctx context.Context, runID protocol.RunID) (protocol.RunCancelResponse, error) {
	session.opMu.Lock()
	defer session.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return protocol.RunCancelResponse{}, err
	}
	session.mu.Lock()
	run := session.runs[runID]
	if run == nil {
		session.mu.Unlock()
		return protocol.RunCancelResponse{}, adapter.ErrRunNotFound
	}
	if run.terminal {
		status := run.status
		session.mu.Unlock()
		if status == protocol.RunCancelled {
			return protocol.RunCancelResponse{SessionID: session.state.SessionID, RunID: runID, Accepted: true, Status: status}, nil
		}
		return protocol.RunCancelResponse{}, &adapter.RunTerminalError{RunID: runID, Status: status}
	}
	if run.cancelPending {
		session.mu.Unlock()
		return protocol.RunCancelResponse{SessionID: session.state.SessionID, RunID: runID, Accepted: true, Status: protocol.RunCancelling}, nil
	}
	turnID := run.turnID
	session.mu.Unlock()
	if err := session.client.Call(ctx, native.MethodTurnInterrupt, native.TurnInterruptParams{ThreadID: session.threadID, TurnID: turnID}, &native.TurnInterruptResponse{}); err != nil {
		return protocol.RunCancelResponse{}, fmt.Errorf("interrupt Codex turn: %w", err)
	}
	session.mu.Lock()
	terminal := run.terminal
	if !terminal {
		run.cancelPending = true
		run.status = protocol.RunCancelling
	}
	session.mu.Unlock()
	if !terminal {
		_ = session.emit(run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: session.state.SessionID, RunID: run.id, Status: protocol.RunCancelling, UpdatedAtMS: session.clock.Now().UnixMilli()}, false)
	}
	return protocol.RunCancelResponse{SessionID: session.state.SessionID, RunID: runID, Accepted: true, Status: protocol.RunCancelling}, nil
}

func (session *session) Resume(ctx context.Context, request adapter.ResumeRequest) (adapter.Recovery, adapter.EventStream, error) {
	if err := ctx.Err(); err != nil {
		return adapter.Recovery{}, nil, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	run := session.runs[request.RunID]
	if run == nil {
		return adapter.Recovery{}, nil, adapter.ErrRunNotFound
	}
	latest := run.nextSequence - 1
	if request.AfterSequence > latest {
		return adapter.Recovery{}, nil, adapter.ErrReplayCursorFuture
	}
	var oldest uint64
	var suffix []protocol.Envelope
	for _, envelope := range session.journal {
		if envelope.RunID != run.id || envelope.Sequence == nil {
			continue
		}
		if oldest == 0 {
			oldest = *envelope.Sequence
		}
		if *envelope.Sequence > request.AfterSequence {
			suffix = append(suffix, envelope)
		}
	}
	recovery := adapter.Recovery{State: session.state, RunID: run.id, RequestedAfter: request.AfterSequence, ReplayedFrom: request.AfterSequence, ReplayedThrough: request.AfterSequence}
	stream := make(chan adapter.Result, len(suffix)+32)
	if request.AfterSequence < latest && (oldest == 0 || request.AfterSequence+1 < oldest) {
		recovery.ReplayGap = &adapter.ReplayGap{RequestedAfter: request.AfterSequence, OldestAvailable: oldest, LatestAvailable: latest}
		close(stream)
		return recovery, stream, recovery.ReplayGap
	}
	if len(suffix) > 0 {
		recovery.ReplayedFrom = *suffix[0].Sequence
		recovery.ReplayedThrough = *suffix[len(suffix)-1].Sequence
		for _, envelope := range suffix {
			stream <- adapter.Result{Envelope: envelope}
		}
	}
	if run.terminal {
		close(stream)
	} else {
		run.subscribers = append(run.subscribers, stream)
	}
	return recovery, stream, nil
}

func (session *session) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	session.opMu.Lock()
	defer session.opMu.Unlock()
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return nil
	}
	if session.active != nil && !session.active.terminal {
		session.mu.Unlock()
		return adapter.ErrRunActive
	}
	session.closed = true
	session.state.Status = protocol.SessionClosed
	session.state.ActiveRunID = ""
	close(session.stop)
	session.mu.Unlock()
	return session.client.Close()
}

func (session *session) dispatch() {
	for {
		select {
		case notification := <-session.client.Notifications():
			session.handleNotification(notification)
		case request := <-session.client.Requests():
			if request != nil {
				_ = request.RespondError(context.Background(), -32601, "unsupported Codex server request", map[string]string{"method": request.Method})
			}
		case <-session.client.Done():
			session.failActive("native_transport_closed", errorString(session.client.Err()))
			return
		case <-session.stop:
			return
		}
	}
}

func (session *session) handleNotification(notification rpc.NotificationMessage) {
	session.opMu.Lock()
	defer session.opMu.Unlock()
	switch notification.Method {
	case native.MethodTurnStarted:
		var value native.TurnStartedNotification
		if json.Unmarshal(notification.Params, &value) != nil {
			session.failActive("invalid_native_event", "invalid turn/started payload")
			return
		}
		session.onStarted(value)
	case native.MethodAgentDelta:
		var value native.AgentMessageDeltaNotification
		if json.Unmarshal(notification.Params, &value) != nil {
			session.failActive("invalid_native_event", "invalid agent message delta")
			return
		}
		session.onDelta(value)
	case native.MethodItemStarted, native.MethodItemCompleted:
		var value native.ItemNotification
		if json.Unmarshal(notification.Params, &value) != nil {
			session.failActive("invalid_native_event", "invalid item lifecycle payload")
			return
		}
		session.onItem(notification.Method, value)
	case native.MethodTurnCompleted:
		var value native.TurnCompletedNotification
		if json.Unmarshal(notification.Params, &value) != nil {
			session.failActive("invalid_native_event", "invalid turn/completed payload")
			return
		}
		session.onCompleted(value)
	}
}

func (session *session) runFor(threadID, turnID string) *runState {
	if threadID != session.threadID {
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.runs[session.turns[turnID]]
}

func (session *session) prepareEvent(run *runState) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	return !run.terminal
}

func (session *session) onStarted(value native.TurnStartedNotification) {
	run := session.runFor(value.ThreadID, value.Turn.ID)
	if run == nil {
		return
	}
	session.mu.Lock()
	if run.started || run.terminal {
		session.mu.Unlock()
		return
	}
	run.started = true
	run.status = protocol.RunRunning
	session.state.Status = protocol.SessionRunning
	session.state.UpdatedAtMS = session.clock.Now().UnixMilli()
	session.mu.Unlock()
	_ = session.emit(run, protocol.TypeRunStarted, protocol.RunStartedPayload{SessionID: session.state.SessionID, RunID: run.id, Status: protocol.RunRunning, ModelID: session.state.CurrentModelID, StartedAtMS: session.clock.Now().UnixMilli()}, false)
}

func (session *session) onDelta(value native.AgentMessageDeltaNotification) {
	run := session.runFor(value.ThreadID, value.TurnID)
	if run == nil || !run.started || run.terminal {
		return
	}
	session.mu.Lock()
	run.text += value.Delta
	session.mu.Unlock()
	_ = session.emit(run, protocol.TypeContentDelta, protocol.ContentDeltaPayload{SessionID: session.state.SessionID, RunID: run.id, MessageID: run.messageID, Part: protocol.ContentPart{Type: protocol.ContentText, Text: value.Delta}}, false)
}

func (session *session) onItem(method string, value native.ItemNotification) {
	run := session.runFor(value.ThreadID, value.TurnID)
	if run == nil || !run.started || run.terminal {
		return
	}
	name := itemName(value.Item)
	if name == "" || value.Item.ID == "" {
		return
	}
	session.mu.Lock()
	binding, exists := session.items[value.Item.ID]
	if method == native.MethodItemStarted {
		if exists || run.terminal {
			session.mu.Unlock()
			return
		}
		binding = itemBinding{runID: run.id, toolCallID: protocol.ToolCallID(session.ids.NewID("tool-call")), name: name, arguments: itemArguments(value.Item), started: true}
		session.items[value.Item.ID] = binding
		session.mu.Unlock()
		payload := actionPayload(session.state.SessionID, run.id, binding)
		_ = session.emit(run, protocol.TypeActionCallRequested, payload, false)
		_ = session.emit(run, protocol.TypeActionCallStarted, payload, false)
		return
	}
	if !exists || binding.runID != run.id || binding.terminal {
		session.mu.Unlock()
		session.failRun(run, "invalid_native_action", "item/completed arrived without one active item/started")
		return
	}
	binding.terminal = true
	session.items[value.Item.ID] = binding
	session.mu.Unlock()
	payload := actionPayload(session.state.SessionID, run.id, binding)
	if value.Item.Error != nil || value.Item.Status == "failed" {
		message := "native action failed"
		if value.Item.Error != nil && value.Item.Error.Message != "" {
			message = value.Item.Error.Message
		}
		payload.Error = &protocol.ProtocolError{Code: "native_action_failed", Message: message}
		_ = session.emit(run, protocol.TypeActionCallFailed, payload, false)
		return
	}
	payload.Result = value.Item.Result
	if len(payload.Result) == 0 {
		output := ""
		if value.Item.Output != nil {
			output = *value.Item.Output
		}
		payload.Result, _ = json.Marshal(map[string]string{"output": output})
	}
	_ = session.emit(run, protocol.TypeActionCallCompleted, payload, false)
}

func actionPayload(sessionID protocol.SessionID, runID protocol.RunID, binding itemBinding) protocol.ActionCallPayload {
	return protocol.ActionCallPayload{
		SessionID: sessionID, RunID: runID, ToolCallID: binding.toolCallID,
		Name: binding.name, ArgumentsJSON: binding.arguments,
		RequestedBy: "agent", ExecutionOwner: "codex.app-server",
	}
}

func itemArguments(item native.Item) json.RawMessage {
	if len(item.Arguments) != 0 {
		return append(json.RawMessage(nil), item.Arguments...)
	}
	var value any
	switch item.Type {
	case "commandExecution":
		value = map[string]string{"command": item.Command, "cwd": item.Cwd}
	case "fileChange":
		value = map[string]any{"changes": item.Changes}
	default:
		value = map[string]string{"server": item.Server, "tool": item.Tool}
	}
	encoded, _ := json.Marshal(value)
	return encoded
}

func itemName(item native.Item) string {
	switch item.Type {
	case "commandExecution":
		return "codex.command"
	case "fileChange":
		return "codex.file_change"
	case "mcpToolCall":
		if item.Server != "" || item.Tool != "" {
			return "mcp." + item.Server + "." + item.Tool
		}
		return "mcp.tool"
	default:
		return ""
	}
}

func (session *session) onCompleted(value native.TurnCompletedNotification) {
	run := session.runFor(value.ThreadID, value.Turn.ID)
	if run == nil || !session.prepareEvent(run) {
		return
	}
	session.closePendingActions(run, value.Turn.Status)
	switch value.Turn.Status {
	case native.TurnCompleted:
		session.mu.Lock()
		text := run.text
		session.mu.Unlock()
		payload := protocol.RunCompletedPayload{SessionID: session.state.SessionID, RunID: run.id, FinalResponse: protocol.Message{ID: run.messageID, Role: protocol.RoleAssistant, Content: protocol.TextContent(text)}, StopReason: "end_turn"}
		_ = session.emit(run, protocol.TypeRunCompleted, payload, true)
	case native.TurnInterrupted:
		_ = session.emit(run, protocol.TypeRunCancelled, protocol.RunCancelledPayload{SessionID: session.state.SessionID, RunID: run.id, Reason: "native turn interrupted"}, true)
	case native.TurnFailed:
		message := "native turn failed"
		code := "native_turn_failed"
		if value.Turn.Error != nil && value.Turn.Error.Message != "" {
			message = value.Turn.Error.Message
		}
		_ = session.emit(run, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: session.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: code, Message: message}}, true)
	default:
		session.failRun(run, "invalid_native_terminal", "turn/completed did not contain a terminal status")
	}
}

func (session *session) closePendingActions(run *runState, status native.TurnStatus) {
	session.mu.Lock()
	var pending []itemBinding
	for id, binding := range session.items {
		if binding.runID == run.id && binding.started && !binding.terminal {
			binding.terminal = true
			session.items[id] = binding
			pending = append(pending, binding)
		}
	}
	session.mu.Unlock()
	for _, binding := range pending {
		payload := actionPayload(session.state.SessionID, run.id, binding)
		if status == native.TurnInterrupted {
			_ = session.emit(run, protocol.TypeActionCallCancelled, payload, false)
			continue
		}
		payload.Error = &protocol.ProtocolError{Code: "native_action_incomplete", Message: "native turn ended before the action completed"}
		_ = session.emit(run, protocol.TypeActionCallFailed, payload, false)
	}
}

func (session *session) failActive(code, message string) {
	session.mu.Lock()
	run := session.active
	session.mu.Unlock()
	if run != nil {
		session.failRun(run, code, message)
	}
}

func (session *session) failRun(run *runState, code, message string) {
	if message == "" {
		message = code
	}
	session.closePendingActions(run, native.TurnFailed)
	_ = session.emit(run, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: session.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: code, Message: message}}, true)
}

func errorString(err error) string {
	if err == nil {
		return "Codex app-server transport closed before terminal settlement"
	}
	return err.Error()
}

func (session *session) emit(run *runState, typ protocol.EnvelopeType, payload any, terminal bool) error {
	session.emitMu.Lock()
	defer session.emitMu.Unlock()
	session.mu.Lock()
	if run.terminal {
		session.mu.Unlock()
		return errors.New("codex app-server adapter: run is terminal")
	}
	sequence := run.nextSequence
	run.nextSequence++
	session.mu.Unlock()
	envelope, err := protocol.NewEnvelope(typ, protocol.EnvelopeID(session.ids.NewID("event")), payload)
	if err != nil {
		return err
	}
	now := session.clock.Now().UnixMilli()
	envelope.SessionID = session.state.SessionID
	envelope.RunID = run.id
	if toolPayload, ok := payload.(protocol.ActionCallPayload); ok {
		envelope.ToolCallID = toolPayload.ToolCallID
	}
	envelope.Sequence = &sequence
	envelope.TimestampMS = &now
	envelope.CapabilityRevision = CapabilityRevision

	session.mu.Lock()
	session.journal = append(session.journal, envelope)
	if len(session.journal) > session.capacity {
		session.journal = append([]protocol.Envelope(nil), session.journal[len(session.journal)-session.capacity:]...)
	}
	subscribers := append([]chan adapter.Result(nil), run.subscribers...)
	if terminal {
		run.terminal = true
		run.status = terminalStatus(typ)
		session.state.Status = protocol.SessionIdle
		session.state.ActiveRunID = ""
		session.state.TranscriptCursor = strconv.FormatUint(sequence, 10)
		session.state.UpdatedAtMS = now
		if session.active == run {
			session.active = nil
		}
		run.subscribers = nil
	}
	session.mu.Unlock()
	for _, subscriber := range subscribers {
		select {
		case subscriber <- adapter.Result{Envelope: envelope}:
		default:
			// Streams are bounded detach points. A consumer that falls behind must
			// resume from the journal instead of stalling native lifecycle reduction.
		}
		if terminal {
			close(subscriber)
		}
	}
	return nil
}

func terminalStatus(typ protocol.EnvelopeType) protocol.RunStatus {
	switch typ {
	case protocol.TypeRunCompleted:
		return protocol.RunCompleted
	case protocol.TypeRunCancelled:
		return protocol.RunCancelled
	default:
		return protocol.RunFailed
	}
}

var _ adapter.Session = (*session)(nil)

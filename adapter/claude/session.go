package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/claude/internal/native"
	"github.com/lsm/open-agent-protocol/adapter/claude/internal/rpc"
	"github.com/lsm/open-agent-protocol/protocol"
)

const streamCapacity = 64

var errTerminalWon = errors.New("claude adapter: terminal already selected")
var errUnavailable = errors.New("claude adapter: operation unavailable")

type Session struct {
	mu       sync.Mutex
	reduceMu sync.Mutex
	promptMu sync.Mutex
	client   Client
	clock    base.Clock
	ids      base.IDGenerator
	capacity int

	// nativeSessionID is the CLI session UUID, learned from the first frame
	// that carries one. A changed id is conversation-reset evidence, not drift.
	nativeSessionID string
	participant     protocol.ParticipantID
	state           protocol.SessionState
	closed          bool
	unusable        bool

	pending      *runState
	active       *runState
	runs         map[protocol.RunID]*runState
	tools        map[string]*toolState
	interactions map[protocol.InteractionID]*gateState
	children     map[string]*childState
	journal      []protocol.Envelope
	stop         chan struct{}
	stopOnce     sync.Once
}

// runState is reduceMu-domain except terminal/subscribers/started, which are
// mu-domain.
type runState struct {
	id       protocol.RunID
	status   protocol.RunStatus
	next     uint64
	started  bool
	terminal bool

	// submissionUUID is the host-minted turn uuid; the CLI's echo of it (on
	// the first reply frame) converges admission. Observations arriving
	// before convergence buffer in wire order and replay at start.
	submissionUUID string
	echoSeen       bool
	buffered       []rpc.InboundMessage
	submittedText  string
	messageID      protocol.MessageID

	// deferred holds a terminal candidate blocked on deferring children; the
	// closing evidence (child settlement, a successor result, or the idle
	// signal) publishes it.
	deferred     *native.ResultFrame
	children     map[string]bool
	terminalKind string
	subscribers  []chan base.Result
	startResult  chan error
	startOnce    sync.Once
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

// gateState is one can_use_tool ask surfaced as an OAP input interaction.
type gateState struct {
	id        protocol.InteractionID
	control   *rpc.IncomingControl
	ask       *native.CanUseToolRequest
	run       *runState
	resolved  bool
	requested protocol.EnvelopeID
}

// childState tracks a background task to its terminal frame.
type childState struct {
	taskID    string
	toolUseID string
	taskType  string
	settled   bool
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
	submissionUUID := s.ids.NewID("turn")
	if err := native.ValidateTurnUUID(submissionUUID); err != nil {
		s.mu.Unlock()
		s.reduceMu.Unlock()
		s.promptMu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, err
	}
	frame, err := native.NewUserTurn(submissionUUID, text)
	if err != nil {
		s.mu.Unlock()
		s.reduceMu.Unlock()
		s.promptMu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, err
	}
	run := &runState{status: protocol.RunQueued, next: 1, submissionUUID: submissionUUID, submittedText: text, messageID: protocol.MessageID(s.ids.NewID("message")), startResult: make(chan error, 1), children: map[string]bool{}}
	stream := make(chan base.Result, streamCapacity+1)
	run.subscribers = []chan base.Result{stream}
	s.pending = run
	s.mu.Unlock()
	// The write completing is not admission — the CLI's echo of the turn uuid
	// is. The write itself can only fail on transport retirement.
	writeErr := s.client.WriteUser(ctx, frame)
	if writeErr != nil {
		s.abortPreStartUnlocked(run, writeErr)
		s.reduceMu.Unlock()
		s.promptMu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, writeErr
	}
	s.reduceMu.Unlock()
	s.promptMu.Unlock()
	select {
	case startErr := <-run.startResult:
		if startErr != nil {
			return protocol.MessageSubmitResponse{}, nil, startErr
		}
	case <-ctx.Done():
		// Cancellation and the echo can race; a run that already converged is
		// authoritative, otherwise release the reservation.
		s.reduceMu.Lock()
		if !run.started && !run.terminal {
			s.abortPreStartUnlocked(run, ctx.Err())
			s.reduceMu.Unlock()
			return protocol.MessageSubmitResponse{}, nil, ctx.Err()
		}
		s.reduceMu.Unlock()
		startErr := <-run.startResult
		if startErr != nil {
			return protocol.MessageSubmitResponse{}, nil, startErr
		}
	}
	s.mu.Lock()
	model := s.state.CurrentModelID
	s.mu.Unlock()
	return protocol.MessageSubmitResponse{SessionID: req.SessionID, Accepted: true, SubmissionID: protocol.SubmissionID(run.messageID), RequestedDelivery: protocol.DeliveryAuto, EffectiveDelivery: protocol.DeliveryStart, DeliveryResolution: "session_idle", Admission: protocol.AdmissionStarted, RunID: run.id, Status: protocol.RunRunning, ModelID: model, MessageIDs: []protocol.MessageID{run.messageID}}, stream, nil
}

// submitText validates the conservative v1 surface: one user text message.
func submitText(req protocol.MessageSubmitRequest) (string, error) {
	if req.SessionID == "" || len(req.Messages) != 1 || (req.Delivery != "" && req.Delivery != protocol.DeliveryAuto) || req.Instructions != "" || len(req.ToolChoice) > 0 || len(req.OutputSchema) > 0 {
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
		case in, ok := <-s.client.Inbound():
			if !ok {
				// The inbound stream ends only at transport retirement, after
				// every frame the wire delivered has been handed over.
				s.settleTransportDeath()
				return
			}
			s.reduceMu.Lock()
			s.reduce(in)
			s.reduceMu.Unlock()
		case <-s.client.Done():
			// Drain everything the reader already routed to the inbound
			// stream's close before settling the failure: evidence ordering
			// at death is deterministic.
			for {
				var in rpc.InboundMessage
				var ok bool
				select {
				case in, ok = <-s.client.Inbound():
				case <-s.stop:
					return
				}
				if !ok {
					s.settleTransportDeath()
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

// settleTransportDeath projects the retired transport: any live run failed
// with the wire truth. An orderly Close (s.closed already set) settles
// nothing — the run's own terminal evidence owned settlement.
func (s *Session) settleTransportDeath() {
	s.reduceMu.Lock()
	defer s.reduceMu.Unlock()
	s.transportFailed()
}

func (s *Session) reduce(in rpc.InboundMessage) {
	if in.Barrier != nil {
		close(in.Barrier)
		return
	}
	if in.Cancel != nil {
		s.cancelGate(in.Cancel.RequestID)
		return
	}
	if in.Control != nil {
		s.reduceControl(in.Control)
		return
	}
	if in.Observation != nil {
		s.applyObservation(in.Observation)
	}
}

func (s *Session) reduceControl(control *rpc.IncomingControl) {
	ask, ok := control.Value.(*native.CanUseToolRequest)
	if !ok {
		// No hooks, SDK MCP servers, or dialogs are configured, so any other
		// reverse request is unconfigured surface on this boundary.
		s.foreignActivity(fmt.Sprintf("reverse control request %q", control.Subtype))
		return
	}
	s.openGate(control, ask)
}

func (s *Session) applyObservation(observation *rpc.ObservationMessage) {
	s.associate(observation)
	run := s.currentRun()
	unusable := false
	s.mu.Lock()
	if run != nil {
		unusable = s.unusable
	}
	s.mu.Unlock()
	if unusable {
		return
	}
	if run == nil {
		// Session-scope evidence: injected turns, init refreshes, idle
		// corroboration. Nothing here becomes a phantom run (frozen
		// mismatch 7).
		s.observeIdle(observation)
		return
	}
	if run.terminal {
		s.observeIdle(observation)
		return
	}
	if !run.started {
		s.reserveObservation(run, observation)
		return
	}
	s.applyRunObservation(run, observation)
}

// observeIdle records session-scope evidence while no run is owned: the
// per-turn init refresh updates the model truth; everything else (injected
// turns, keep-alives, idle signals, unknown types) carries no projection.
func (s *Session) observeIdle(observation *rpc.ObservationMessage) {
	if init, ok := observation.Value.(*native.InitFrame); ok {
		s.mu.Lock()
		s.state.CurrentModelID = init.Model
		s.mu.Unlock()
	}
}

func (s *Session) currentRun() *runState {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.pending
	if run == nil {
		run = s.active
	}
	return run
}

// associate learns or relearns the CLI session identity from any frame that
// carries one; a changed id is conversation-reset evidence, never drift.
func (s *Session) associate(observation *rpc.ObservationMessage) {
	var frame struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(observation.Raw, &frame) != nil || frame.SessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nativeSessionID = frame.SessionID
}

// reserveObservation applies one observation against a reserved, not-yet-
// echoed run: the turn-uuid echo converges admission; everything else
// buffers in wire order for replay once the run starts. The converging
// frame's own content reduces after run.started — the echo can ride the
// first complete assistant frame, which may carry tool_use blocks.
func (s *Session) reserveObservation(run *runState, observation *rpc.ObservationMessage) {
	if s.echoMatches(run, observation.Value, observation.Raw) {
		s.mu.Lock()
		run.echoSeen = true
		s.mu.Unlock()
		s.startRun(run)
		s.applyRunObservation(run, observation)
		return
	}
	run.buffered = append(run.buffered, rpc.InboundMessage{Observation: observation})
}

// echoMatches reports whether a frame carries the submitted uuid — the
// first reply frame of the turn (a stream event, an assistant frame, a
// thinking-tokens system frame) or the turn's result frames.
func (s *Session) echoMatches(run *runState, value any, raw json.RawMessage) bool {
	uuid := run.submissionUUID
	switch frame := value.(type) {
	case *native.AssistantFrame:
		return frame.UserMessageUUID == uuid || containsUUID(frame.UserMessageUUIDs, uuid)
	case *native.StreamEventFrame:
		return frame.UserMessageUUID == uuid || containsUUID(frame.UserMessageUUIDs, uuid)
	case *native.ResultFrame:
		return frame.UserMessageUUID == uuid || containsUUID(frame.UserMessageUUIDs, uuid)
	default:
		var holder struct {
			UserMessageUUID  string   `json:"user_message_uuid"`
			UserMessageUUIDs []string `json:"user_message_uuids"`
		}
		_ = json.Unmarshal(raw, &holder)
		return holder.UserMessageUUID == uuid || containsUUID(holder.UserMessageUUIDs, uuid)
	}
}

func containsUUID(uuids []string, uuid string) bool {
	for _, candidate := range uuids {
		if candidate == uuid {
			return true
		}
	}
	return false
}

// applyRunObservation reduces one observation for an owned, started run.
func (s *Session) applyRunObservation(run *runState, observation *rpc.ObservationMessage) {
	switch frame := observation.Value.(type) {
	case *native.AssistantFrame:
		if frame.ParentToolUseID != nil {
			return // subagent-produced evidence; children settle via task frames
		}
		for i := range frame.Message.Content {
			block := frame.Message.Content[i]
			if block.Type == "tool_use" && block.ID != "" {
				s.startTool(run, block.ID, block.Name, block.Input)
			}
		}
	case *native.UserFrame:
		if frame.ParentToolUseID != nil {
			return
		}
		if frame.Origin != nil && frame.Origin.Kind != "human" {
			return // injected user-role content inside the turn
		}
		blocks, ok := frame.Blocks()
		if !ok {
			return // synthetic markers (e.g. the interrupt notice) are evidence only
		}
		for _, block := range blocks {
			if block.Type == "tool_result" && block.ToolUseID != "" {
				s.endTool(run, block.ToolUseID, block.Content, block.IsError)
			}
		}
	case *native.StreamEventFrame:
		if frame.ParentToolUseID != nil {
			return
		}
		if kind, text, ok := frame.StreamDelta(); ok {
			s.emitDelta(run, kind, text)
		}
	case *native.ResultFrame:
		s.settleRun(run, frame, observation.Raw)
	case *native.InitFrame:
		s.mu.Lock()
		s.state.CurrentModelID = frame.Model
		s.mu.Unlock()
	case *native.TaskStartedFrame:
		s.trackChild(run, frame.TaskID, frame.ToolUseID, frame.TaskType)
	case *native.TaskNotificationFrame:
		s.settleChild(run, frame.TaskID)
	case *native.TaskUpdatedFrame:
		if frame.Terminal() {
			s.settleChild(run, frame.TaskID)
		}
	default:
		// tool_progress, command_lifecycle, status, session_state_changed,
		// keep_alive, unknown types, and generic system notices are
		// session-scope evidence.
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
	// Observations buffered before the echo reduce now, in wire order. A
	// replayed settlement defers rather than double-settling.
	for _, in := range replay {
		if run.terminal && run.deferred == nil {
			return
		}
		if in.Observation != nil {
			s.applyRunObservation(run, in.Observation)
		}
	}
}

func (s *Session) emitDelta(run *runState, kind, text string) {
	if !run.started || run.terminal {
		return
	}
	part := protocol.ContentPart{Type: protocol.ContentText, Text: text}
	if kind == "thinking" {
		part = protocol.ContentPart{Type: protocol.ContentReasoning, Reasoning: text}
	}
	_ = s.emit(run, protocol.TypeContentDelta, protocol.ContentDeltaPayload{SessionID: s.state.SessionID, RunID: run.id, MessageID: run.messageID, Part: part}, false)
}

func (s *Session) startTool(run *runState, nativeID, name string, input json.RawMessage) {
	if s.tools[nativeID] != nil {
		s.failRun(run, "claude_tool_lifecycle", "duplicate tool call")
		return
	}
	args, _ := json.Marshal(input)
	tool := &toolState{nativeID: nativeID, id: protocol.ToolCallID(s.ids.NewID("tool-call")), run: run, name: name, args: args}
	s.tools[nativeID] = tool
	payload := s.toolPayload(tool)
	requested, _ := s.emitEnvelope(run, protocol.TypeActionCallRequested, payload, false, "")
	payload.ArgumentsJSON = nil
	started, _ := s.emitEnvelope(run, protocol.TypeActionCallStarted, payload, false, requested.ID)
	tool.requested = requested.ID
	tool.started = started.ID
}

func (s *Session) endTool(run *runState, nativeID string, content json.RawMessage, isError *bool) {
	tool := s.tools[nativeID]
	if tool == nil || tool.terminal {
		s.failRun(run, "claude_tool_lifecycle", "unmatched tool completion")
		return
	}
	tool.terminal = true
	payload := s.toolPayload(tool)
	payload.ArgumentsJSON = nil
	if isError != nil && *isError {
		// The failed shape carries error only; result is not a member there.
		payload.Result = nil
		payload.Error = &protocol.ProtocolError{Code: "claude_tool_error", Message: toolResultText(content)}
		_, _ = s.emitEnvelope(run, protocol.TypeActionCallFailed, payload, false, tool.started)
		return
	}
	payload.Result = normalizedToolResult(content)
	_, _ = s.emitEnvelope(run, protocol.TypeActionCallCompleted, payload, false, tool.started)
}

// normalizedToolResult reduces the tool_result content to OAP result JSON:
// string content becomes a JSON string; structured content passes through;
// absent content is the empty string (result is a required member of the
// completed shape).
func normalizedToolResult(content json.RawMessage) json.RawMessage {
	if len(content) == 0 {
		return json.RawMessage(`""`)
	}
	var text string
	if json.Unmarshal(content, &text) == nil {
		encoded, err := json.Marshal(text)
		if err != nil {
			return json.RawMessage(`""`)
		}
		return encoded
	}
	return cloneRaw(content)
}

func toolResultText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil && text != "" {
		return text
	}
	return "tool call failed"
}

func (s *Session) toolPayload(tool *toolState) protocol.ActionCallPayload {
	return protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: tool.run.id, ToolCallID: tool.id, RequestedBy: "agent", ExecutionOwner: "claude-code", Name: tool.name, ArgumentsJSON: cloneRaw(tool.args)}
}

// openGate surfaces one can_use_tool ask as an OAP permission interaction.
func (s *Session) openGate(control *rpc.IncomingControl, ask *native.CanUseToolRequest) {
	run := s.currentRun()
	if run == nil || !run.started || run.terminal {
		// A permission ask outside an owned turn cannot be answered through
		// OAP; deny it so the CLI is never left blocked on a dead host.
		_ = control.RespondError(context.Background(), "claude adapter: permission ask outside an owned run")
		s.foreignActivity("can_use_tool outside an owned run")
		return
	}
	id := protocol.InteractionID(s.ids.NewID("interaction"))
	gate := &gateState{id: id, control: control, ask: ask, run: run}
	s.interactions[id] = gate
	title := ask.Title
	if title == "" {
		title = fmt.Sprintf("Use %s", ask.ToolName)
	}
	description := ask.Description
	if description == "" {
		description = ask.DecisionReason
	}
	prompt := ask.ToolName
	if len(ask.Input) > 0 {
		prompt = fmt.Sprintf("%s %s", ask.ToolName, string(ask.Input))
	}
	questions := []protocol.InputQuestion{{
		ID: "decision", Prompt: prompt, Kind: protocol.InputSingleChoice, Required: true,
		Options: []protocol.InputOption{
			{ID: "allow", Label: "Allow"},
			{ID: "deny", Label: "Deny"},
		},
	}}
	requested, emitErr := s.emitEnvelope(run, protocol.TypeUserInputRequested, protocol.UserInputRequestedPayload{InteractionID: id, RequestedBy: "agent", RespondedBy: s.participant, SessionID: s.state.SessionID, RunID: run.id, Title: title, Description: description, Questions: questions, AllowCancel: true}, false, "")
	if emitErr != nil {
		_ = control.RespondError(context.Background(), "claude adapter: gate could not be surfaced")
		delete(s.interactions, id)
		return
	}
	gate.requested = requested.ID
	_ = s.emit(run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunWaitingForInput, PendingUserInputID: id, UpdatedAtMS: s.clock.Now().UnixMilli()}, false)
}

// Resolve answers one open permission gate with allow or deny. The original
// tool input is what executes on allow (an OAP answer carries no rewritten
// input); deny returns the operator's message to the model.
func (s *Session) Resolve(ctx context.Context, resolution base.InteractionResolution) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if resolution.Input == nil {
		return errUnavailable
	}
	s.reduceMu.Lock()
	gate := s.interactions[resolution.Input.InteractionID]
	if gate == nil || gate.resolved {
		s.reduceMu.Unlock()
		return base.ErrInteractionNotFound
	}
	run := gate.run
	if run.terminal {
		s.reduceMu.Unlock()
		return base.ErrInteractionNotFound
	}
	if len(resolution.Input.Answers) != 1 || len(resolution.Input.Answers[0].SelectedOptionIDs) != 1 || resolution.Input.Answers[0].QuestionID != "decision" {
		s.reduceMu.Unlock()
		return base.ErrInvalidResolution
	}
	decision := resolution.Input.Answers[0].SelectedOptionIDs[0]
	var answer any
	switch decision {
	case "allow":
		answer = native.PermissionAllow{Behavior: "allow", UpdatedInput: gate.ask.Input}
	case "deny":
		answer = native.PermissionDeny{Behavior: "deny", Message: "Denied by the operator"}
	default:
		s.reduceMu.Unlock()
		return base.ErrInvalidResolution
	}
	gate.resolved = true
	s.reduceMu.Unlock()

	if err := gate.control.Respond(ctx, answer); err != nil {
		s.reduceMu.Lock()
		gate.resolved = false
		s.reduceMu.Unlock()
		return err
	}
	s.reduceMu.Lock()
	respondedBy := resolution.RespondedBy
	if respondedBy == "" {
		respondedBy = s.participant
	}
	_, _ = s.emitEnvelope(run, protocol.TypeUserInputResolved, protocol.UserInputResolvedPayload{InteractionID: gate.id, RequestedBy: "agent", RespondedBy: respondedBy, SessionID: s.state.SessionID, RunID: run.id, Status: protocol.InputSubmitted, Answers: resolution.Input.Answers}, false, gate.requested)
	_ = s.emit(run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunRunning, UpdatedAtMS: s.clock.Now().UnixMilli()}, false)
	// A resolved gate is terminal: retire it so later asks are findable.
	delete(s.interactions, gate.id)
	s.reduceMu.Unlock()
	return nil
}

// cancelGate resolves a withdrawn ask as cancelled; the CLI abandoned it and
// no answer may be written.
func (s *Session) cancelGate(requestID string) {
	for _, gate := range s.interactions {
		if gate.control == nil || gate.control.ID != requestID || gate.resolved {
			continue
		}
		gate.resolved = true
		run := gate.run
		if run != nil && run.started && !run.terminal {
			_, _ = s.emitEnvelope(run, protocol.TypeUserInputResolved, protocol.UserInputResolvedPayload{InteractionID: gate.id, RequestedBy: "agent", RespondedBy: s.participant, SessionID: s.state.SessionID, RunID: run.id, Status: protocol.InputCancelled}, false, gate.requested)
			_ = s.emit(run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunRunning, UpdatedAtMS: s.clock.Now().UnixMilli()}, false)
		}
		delete(s.interactions, gate.id)
		return
	}
}

// Cancel issues the interrupt intent. Settlement arrives only as a result
// frame whose terminal_reason is aborted_*; the receipt never settles.
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
	var receipt native.InterruptResult
	if err := s.client.Call(ctx, native.InterruptRequest{Subtype: native.ControlInterrupt}, &receipt); err != nil {
		return protocol.RunCancelResponse{}, err
	}
	return protocol.RunCancelResponse{SessionID: s.state.SessionID, RunID: id, Accepted: true, Status: protocol.RunCancelling}, nil
}

// settleRun projects the turn terminal. A result whose echo does not carry
// the submitted uuid is another turn's terminal (injected or foreign) and is
// observed only. queued_turn_count > 0 means more turns of this submission
// follow; the settlement defers to the closing result. Deferring children
// (local agents/workflows) hold the terminal until they settle or a
// successor turn closes.
func (s *Session) settleRun(run *runState, frame *native.ResultFrame, raw json.RawMessage) {
	if !s.echoMatches(run, frame, raw) {
		return
	}
	if (frame.QueuedTurnCount != nil && *frame.QueuedTurnCount > 0) || s.unsettledDeferringChildren(run) > 0 {
		s.mu.Lock()
		run.deferred = frame
		s.mu.Unlock()
		return
	}
	s.publishTerminal(run, frame)
}

func (s *Session) unsettledDeferringChildren(run *runState) int {
	count := 0
	for _, child := range s.children {
		if !child.settled && (child.taskType == "local_agent" || child.taskType == "local_workflow") {
			count++
		}
	}
	return count
}

func (s *Session) trackChild(run *runState, taskID, toolUseID, taskType string) {
	if taskID == "" {
		return
	}
	s.children[taskID] = &childState{taskID: taskID, toolUseID: toolUseID, taskType: taskType}
}

func (s *Session) settleChild(run *runState, taskID string) {
	child, ok := s.children[taskID]
	if !ok || child.settled {
		return
	}
	child.settled = true
	// A held terminal publishes once the last deferring child settles.
	if run.terminal || run.deferred == nil {
		return
	}
	if s.unsettledDeferringChildren(run) == 0 {
		frame := run.deferred
		s.mu.Lock()
		run.deferred = nil
		s.mu.Unlock()
		s.publishTerminal(run, frame)
	}
}

// publishTerminal emits the one absorbing terminal for the run.
func (s *Session) publishTerminal(run *runState, frame *native.ResultFrame) {
	if !run.started {
		s.abortPreStartUnlocked(run, fmt.Errorf("%w: settlement before the turn echo", ErrNativeProtocol))
		return
	}
	s.mu.Lock()
	if run.terminal {
		s.mu.Unlock()
		return
	}
	run.deferred = nil
	s.mu.Unlock()
	usage := &protocol.Usage{InputTokens: uint64(frame.Usage.InputTokens), OutputTokens: uint64(frame.Usage.OutputTokens), TotalTokens: uint64(frame.Usage.InputTokens + frame.Usage.OutputTokens)}
	switch {
	case frame.Cancelled():
		_ = s.emit(run, protocol.TypeRunCancelled, protocol.RunCancelledPayload{SessionID: s.state.SessionID, RunID: run.id, Reason: "interrupt confirmed by terminal_reason " + frame.TerminalReason, Usage: usage, DurationMS: frame.DurationMS}, true)
	case frame.MaxTurns():
		_ = s.emit(run, protocol.TypeRunCompleted, protocol.RunCompletedPayload{SessionID: s.state.SessionID, RunID: run.id, FinalResponse: protocol.Message{ID: run.messageID, Role: protocol.RoleAssistant, Content: protocol.TextContent(frame.Result)}, StopReason: "max_turns", Usage: usage, DurationMS: frame.DurationMS}, true)
	case !frame.IsError && frame.Subtype == native.ResultSuccess:
		_ = s.emit(run, protocol.TypeRunCompleted, protocol.RunCompletedPayload{SessionID: s.state.SessionID, RunID: run.id, FinalResponse: protocol.Message{ID: run.messageID, Role: protocol.RoleAssistant, Content: protocol.TextContent(frame.Result)}, StopReason: stopReason(frame), Usage: usage, DurationMS: frame.DurationMS}, true)
	default:
		code := "claude_" + frame.Subtype
		if frame.TerminalReason != "" && frame.Subtype == native.ResultSuccess {
			code = "claude_" + frame.TerminalReason
		}
		if frame.APIErrorStatus != nil {
			code = "claude_api_" + strconv.Itoa(*frame.APIErrorStatus)
		}
		_ = s.emit(run, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: s.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: code, Message: errorResultText(frame)}, Usage: usage, DurationMS: frame.DurationMS}, true)
	}
}

// errorResultText follows the reference _error_result_text preference:
// errors[], then result, then a non-success subtype, then the HTTP status.
func errorResultText(frame *native.ResultFrame) string {
	if len(frame.Errors) > 0 {
		return strings.Join(frame.Errors, "; ")
	}
	if strings.TrimSpace(frame.Result) != "" {
		return strings.TrimSpace(frame.Result)
	}
	if frame.Subtype != native.ResultSuccess && frame.Subtype != "" {
		return frame.Subtype
	}
	if frame.APIErrorStatus != nil {
		return fmt.Sprintf("API error (HTTP %d)", *frame.APIErrorStatus)
	}
	return "unknown error"
}

func stopReason(frame *native.ResultFrame) string {
	if frame.TerminalReason != "" {
		return frame.TerminalReason
	}
	if frame.StopReason != nil && *frame.StopReason != "" {
		return *frame.StopReason
	}
	return "completed"
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
	// Teardown is stdin EOF; the exit code never fails Close (the CLI exits
	// non-zero on purpose after error results — the wire evidence settled
	// the run already).
	err := s.client.Close()
	s.stopOnce.Do(func() { close(s.stop) })
	for _, channel := range subs {
		close(channel)
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
		s.failRun(run, "claude_external_activity", what)
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
	subs := run.subscribers
	run.subscribers = nil
	run.buffered = nil
	run.deferred = nil
	s.mu.Unlock()
	run.signalStart(err)
	for _, channel := range subs {
		close(channel)
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
			s.failRun(run, "claude_process_exit", fmt.Sprint(s.client.Err()))
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
		delete(s.runs, run.id)
		s.state.Status = protocol.SessionIdle
		s.state.ActiveRunID = ""
	}
	kept := []chan base.Result{}
	for _, channel := range run.subscribers {
		if len(channel) < cap(channel)-1 {
			channel <- base.Result{Envelope: e}
			if terminal {
				close(channel)
			} else {
				kept = append(kept, channel)
			}
		} else {
			channel <- base.Result{Error: base.ErrEventStreamOverflow}
			close(channel)
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

package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

var errTerminalWon = errors.New("acp adapter: terminal already selected")

const streamCapacity = 64

type session struct {
	mu           sync.Mutex
	emitMu       sync.Mutex
	opMu         sync.Mutex
	client       Client
	inbound      <-chan rpc.InboundMessage
	clock        base.Clock
	ids          base.IDGenerator
	capacity     int
	nativeID     string
	participant  protocol.ParticipantID
	state        protocol.SessionState
	closed       bool
	active       *runState
	runs         map[protocol.RunID]*runState
	tools        map[string]*toolState
	interactions map[protocol.InteractionID]*permissionState
	journal      []protocol.Envelope
	stop         chan struct{}
	stopOnce     sync.Once
}
type runState struct {
	id              protocol.RunID
	status          protocol.RunStatus
	next            uint64
	terminal        bool
	cancelRequested bool
	messageID       protocol.MessageID
	messageTexts    map[protocol.MessageID]string
	nativeMessages  map[string]protocol.MessageID
	admitted        chan struct{}
	subscribers     []chan base.Result
}
type toolState struct {
	nativeID                                    string
	id                                          protocol.ToolCallID
	run                                         *runState
	title, kind, status                         string
	rawInput, rawOutput, jsonContent, locations json.RawMessage
	requested, started, terminal                bool
}
type permissionState struct {
	id             protocol.InteractionID
	run            *runState
	tool           *toolState
	request        *rpc.IncomingRequest
	requestEventID protocol.EnvelopeID
	options        map[string]native.PermissionOption
	resolved       bool
}

func (s *session) Submit(ctx context.Context, req protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}

	if err := base.RefuseUnadvertisedControls(req); err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}
	if req.SessionID == "" || len(req.Messages) == 0 {
		return protocol.MessageSubmitResponse{}, nil, base.ErrInvalidSubmission
	}
	if req.Delivery != "" && req.Delivery != protocol.DeliveryAuto {
		return protocol.MessageSubmitResponse{}, nil, fmt.Errorf("%w: delivery %q", base.ErrInvalidSubmission, req.Delivery)
	}
	prompt, messageIDs, err := s.promptContent(req.Messages)
	if err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}
	s.mu.Lock()
	if s.closed {
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
	run := &runState{id: protocol.RunID(s.ids.NewID("run")), status: protocol.RunRunning, next: 1, messageID: s.newMessageID(), messageTexts: make(map[protocol.MessageID]string), nativeMessages: make(map[string]protocol.MessageID), admitted: make(chan struct{})}
	run.messageTexts[run.messageID] = ""

	stream := make(chan base.Result, streamCapacity+1)
	run.subscribers = []chan base.Result{stream}
	s.active = run
	s.runs[run.id] = run
	s.state.Status = protocol.SessionRunning
	s.state.ActiveRunID = run.id
	s.state.UpdatedAtMS = s.clock.Now().UnixMilli()
	s.mu.Unlock()

	started := make(chan error, 1)
	go s.prompt(run, prompt, started)
	writeErr, writeDone := awaitAdmission(started, ctx)
	if !writeDone {

		close(run.admitted)
		return protocol.MessageSubmitResponse{}, stream, ctx.Err()
	}
	if writeErr != nil {
		s.mu.Lock()
		run.terminal = true
		run.status = protocol.RunFailed
		delete(s.runs, run.id)
		if s.active == run {
			s.active = nil
		}
		s.state.Status = protocol.SessionIdle
		s.state.ActiveRunID = ""
		s.state.UpdatedAtMS = s.clock.Now().UnixMilli()
		run.subscribers = nil
		s.mu.Unlock()
		close(stream)
		return protocol.MessageSubmitResponse{}, stream, writeErr
	}
	if err := s.emit(run, protocol.TypeRunStarted, protocol.RunStartedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunRunning, ModelID: protocol.Control(req.ModelID), StartedAtMS: s.clock.Now().UnixMilli()}, false); err != nil {
		close(run.admitted)
		return protocol.MessageSubmitResponse{}, stream, err
	}
	response := protocol.MessageSubmitResponse{SessionID: s.state.SessionID, Accepted: true, SubmissionID: protocol.SubmissionID(s.ids.NewID("submission")), RequestedDelivery: protocol.DeliveryAuto, EffectiveDelivery: protocol.DeliveryStart, DeliveryResolution: "session_idle", Admission: protocol.AdmissionStarted, RunID: run.id, Status: protocol.RunRunning, ModelID: protocol.Control(req.ModelID), MessageIDs: messageIDs}
	close(run.admitted)
	return response, stream, nil
}

func awaitAdmission(started <-chan error, ctx context.Context) (error, bool) {
	select {
	case err := <-started:
		return err, true
	case <-ctx.Done():
		select {
		case err := <-started:
			return err, true
		default:
			return ctx.Err(), false
		}
	}
}

func (s *session) newMessageID() protocol.MessageID {
	return protocol.MessageID(string(s.state.SessionID) + "/" + s.ids.NewID("message"))
}

func (s *session) promptContent(messages []protocol.Message) ([]native.ContentBlock, []protocol.MessageID, error) {
	out := make([]native.ContentBlock, 0, len(messages))
	ids := make([]protocol.MessageID, len(messages))
	for i, m := range messages {
		if m.Role != protocol.RoleUser {
			return nil, nil, fmt.Errorf("%w: ACP prompt supports user text only", ErrUnsupportedInput)
		}
		text, ok := m.Content.Text()
		if !ok {
			return nil, nil, fmt.Errorf("%w: ACP prompt supports text only", ErrUnsupportedInput)
		}
		out = append(out, native.ContentBlock{Type: "text", Text: text})
		ids[i] = m.ID
		if ids[i] == "" {
			ids[i] = protocol.MessageID(s.ids.NewID("message"))
		}
	}
	return out, ids, nil
}
func (s *session) prompt(run *runState, prompt []native.ContentBlock, started chan<- error) {
	var result native.PromptResult
	err := s.client.CallStarted(context.Background(), native.MethodSessionPrompt, native.PromptParams{SessionID: s.nativeID, Prompt: prompt}, &result, started)
	s.settlePrompt(run, result, err)
}
func (s *session) settlePrompt(run *runState, result native.PromptResult, callErr error) {
	<-run.admitted
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	if run.terminal {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	if callErr != nil {
		var remote *rpc.RemoteError
		if errors.As(callErr, &remote) && remote.Object.Code == -32800 && run.cancelRequested {
			s.settleChildren(run, true)
			_ = s.emit(run, protocol.TypeRunCancelled, protocol.RunCancelledPayload{SessionID: s.state.SessionID, RunID: run.id, Reason: "ACP prompt cancellation confirmed"}, true)
			return
		}

		settledBy := protocol.SettledByInferred
		if remote != nil {
			settledBy = ""
		}
		s.settleChildren(run, true)
		_ = s.emit(run, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: s.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: "acp_prompt_error", Message: callErr.Error()}, SettledBy: settledBy}, true)
		return
	}
	switch result.StopReason {
	case "end_turn", "max_tokens", "max_turn_requests":
		s.settleChildren(run, false)
		s.mu.Lock()
		messageID := run.messageID
		text := run.messageTexts[messageID]
		s.mu.Unlock()
		_ = s.emit(run, protocol.TypeRunCompleted, protocol.RunCompletedPayload{SessionID: s.state.SessionID, RunID: run.id, FinalResponse: protocol.Message{ID: messageID, Role: protocol.RoleAssistant, Content: protocol.TextContent(text)}, StopReason: result.StopReason}, true)
	case "cancelled":
		s.settleChildren(run, true)
		_ = s.emit(run, protocol.TypeRunCancelled, protocol.RunCancelledPayload{SessionID: s.state.SessionID, RunID: run.id, Reason: "ACP prompt returned cancelled"}, true)
	case "refusal":
		s.settleChildren(run, true)
		_ = s.emit(run, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: s.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: "refusal", Message: "agent refused the prompt"}}, true)
	default:
		s.settleChildren(run, true)
		_ = s.emit(run, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: s.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: "acp_invalid_stop_reason", Message: fmt.Sprintf("unsupported ACP stop reason %q", result.StopReason)}}, true)
	}
}

func (s *session) dispatch() {
	for {
		select {
		case message := <-s.inbound:
			switch {
			case message.Notification != nil:
				s.handleNotification(*message.Notification)
			case message.Request != nil:
				s.handleRequest(message.Request)
			case message.Barrier != nil:
				close(message.Barrier)
			}
		case <-s.client.Done():
			s.transportFailed()
			return
		case <-s.stop:
			return
		}
	}
}
func (s *session) handleNotification(n rpc.NotificationMessage) {
	if n.Method != native.MethodSessionUpdate {
		return
	}
	var p native.SessionUpdateParams
	if json.Unmarshal(n.Params, &p) != nil || p.SessionID != s.nativeID {
		s.failActive("acp_invalid_update", "malformed or foreign session/update")
		return
	}
	var h native.UpdateHeader
	if json.Unmarshal(p.Update, &h) != nil {
		s.failActive("acp_invalid_update", "malformed session update")
		return
	}
	s.mu.Lock()
	run := s.active
	s.mu.Unlock()
	if run == nil {
		return
	}

	<-run.admitted
	switch h.SessionUpdate {
	case "agent_message_chunk":
		var u native.AgentMessageChunk
		if json.Unmarshal(p.Update, &u) != nil || u.Content.Type != "text" {
			s.failActive("acp_invalid_message_chunk", "unsupported assistant chunk")
			return
		}
		s.mu.Lock()
		mid := run.messageID
		if u.MessageID != "" {
			var ok bool
			mid, ok = run.nativeMessages[u.MessageID]
			if !ok {

				if len(run.nativeMessages) == 0 {
					mid = run.messageID
				} else {
					mid = s.newMessageID()
				}
				run.nativeMessages[u.MessageID] = mid
			}
			run.messageID = mid
		}
		if !run.terminal {
			run.messageTexts[mid] += u.Content.Text
		}
		s.mu.Unlock()
		_ = s.emit(run, protocol.TypeContentDelta, protocol.ContentDeltaPayload{SessionID: s.state.SessionID, RunID: run.id, MessageID: mid, Part: protocol.ContentPart{Type: protocol.ContentText, Text: u.Content.Text}}, false)
	case "tool_call":
		var u native.ToolCall
		if json.Unmarshal(p.Update, &u) != nil {
			s.failActive("acp_invalid_tool_call", "malformed tool call")
			return
		}
		s.applyToolCall(run, u)
	case "tool_call_update":
		var u native.ToolCallUpdate
		if json.Unmarshal(p.Update, &u) != nil {
			s.failActive("acp_invalid_tool_update", "malformed tool update")
			return
		}
		s.applyToolUpdate(run, u)

	case "user_message_chunk", "agent_thought_chunk", "plan", "plan_update", "plan_removed",
		"available_commands_update", "current_mode_update", "config_option_update",
		"session_info_update", "usage_update":
		return
	default:
		if len(h.SessionUpdate) > 0 && h.SessionUpdate[0] == '_' {
			return
		}
		s.failActive("acp_unknown_update", "unknown stable ACP session update")
	}
}
func (s *session) handleRequest(r *rpc.IncomingRequest) {
	if r.Method != native.MethodSessionRequestPermission {
		_ = r.RespondError(context.Background(), -32601, "method not supported", nil)
		return
	}
	var p native.PermissionRequest
	if json.Unmarshal(r.Params, &p) != nil || p.SessionID != s.nativeID || p.ToolCall.ToolCallID == "" || len(p.Options) == 0 {
		_ = r.RespondError(context.Background(), -32602, "invalid permission request", nil)
		s.failActive("acp_invalid_permission", "malformed permission request")
		return
	}
	s.mu.Lock()
	run := s.active
	s.mu.Unlock()
	if run == nil {
		_ = r.Respond(context.Background(), native.PermissionResponse{Outcome: native.PermissionOutcome{Outcome: "cancelled"}})
		return
	}
	<-run.admitted
	if !s.applyToolCall(run, p.ToolCall) {

		_ = r.RespondError(context.Background(), -32602, "invalid tool call", nil)
		return
	}
	s.mu.Lock()
	tool := s.tools[p.ToolCall.ToolCallID]
	id := protocol.InteractionID(s.ids.NewID("interaction"))
	opts := map[string]native.PermissionOption{}
	choices := make([]protocol.PermissionChoice, 0, len(p.Options))
	for _, o := range p.Options {
		if o.OptionID == "" || o.Name == "" || !definedOptionKind(o.Kind) {
			continue
		}
		opts[o.OptionID] = o
		choices = append(choices, protocol.PermissionChoice{ID: o.OptionID, Label: o.Name, Description: o.Kind})
	}
	if len(choices) == 0 {
		s.mu.Unlock()
		_ = r.RespondError(context.Background(), -32602, "permission options required", nil)
		s.failActive("acp_invalid_permission", "empty permission options")
		return
	}
	ps := &permissionState{id: id, run: run, tool: tool, request: r, options: opts}
	s.interactions[id] = ps
	s.mu.Unlock()

	_, _ = s.emitRecorded(run, protocol.TypeActionPermissionRequested, protocol.PermissionRequestedPayload{InteractionID: id, RequestedBy: endpointID, RespondedBy: s.participant, SessionID: s.state.SessionID, RunID: run.id, ToolCallID: tool.id, Title: p.ToolCall.Title, Choices: choices, ArgumentsJSON: tool.rawInput}, false, "", func(event protocol.Envelope) { ps.requestEventID = event.ID })
}

func definedOptionKind(kind string) bool {
	switch kind {
	case "allow_once", "allow_always", "reject_once", "reject_always":
		return true
	}
	return false
}

func (s *session) applyToolCall(run *runState, u native.ToolCall) bool {
	if u.ToolCallID == "" || u.Title == "" {
		s.failActive("acp_invalid_tool_call", "tool id and title are required")
		return false
	}
	s.mu.Lock()
	t := s.tools[u.ToolCallID]
	if t != nil && t.run != run {
		s.mu.Unlock()
		s.failActive("acp_tool_id_reuse", "tool id reused across prompts")
		return false
	}
	if t == nil {
		t = &toolState{nativeID: u.ToolCallID, id: protocol.ToolCallID(s.ids.NewID("tool-call")), run: run}
		s.tools[u.ToolCallID] = t
	}
	if t.terminal {
		s.mu.Unlock()
		s.failActive("acp_tool_after_terminal", "tool updated after terminal")
		return false
	}
	t.title = u.Title
	t.kind = u.Kind
	t.rawInput = rawClone(u.RawInput)
	t.rawOutput = rawClone(u.RawOutput)
	t.jsonContent = rawClone(u.Content)
	t.locations = rawClone(u.Locations)
	first := !t.requested
	t.requested = true
	status := u.Status
	s.mu.Unlock()
	if first {
		payload := s.toolPayload(t)

		payload.Progress = nil
		payload.Result = nil
		payload.Error = nil

		if payload.ArgumentsJSON == nil {
			payload.ArgumentsJSON = json.RawMessage("null")
		}
		_ = s.emit(run, protocol.TypeActionCallRequested, payload, false)
	}
	s.applyToolStatus(t, status)
	return true
}
func (s *session) applyToolUpdate(run *runState, u native.ToolCallUpdate) {
	s.mu.Lock()
	t := s.tools[u.ToolCallID]
	if t == nil || t.run != run {
		s.mu.Unlock()
		s.failActive("acp_tool_patch_without_call", "tool patch before creation")
		return
	}
	if t.terminal {
		s.mu.Unlock()
		s.failActive("acp_tool_after_terminal", "tool updated after terminal")
		return
	}
	if u.Title != nil && *u.Title != "" {
		t.title = *u.Title
	}
	if u.Kind != nil {
		t.kind = *u.Kind
	}
	if u.RawInput != nil {
		t.rawInput = rawClone(u.RawInput)
	}
	if u.RawOutput != nil {
		t.rawOutput = rawClone(u.RawOutput)
	}
	if u.Content != nil {
		t.jsonContent = rawClone(u.Content)
	}
	if u.Locations != nil {
		t.locations = rawClone(u.Locations)
	}
	started := t.started
	status := ""
	if u.Status != nil {
		status = *u.Status
	}
	s.mu.Unlock()
	if status == "" {

		if started {
			progress := s.toolPayload(t)

			progress.ArgumentsJSON = nil
			progress.Result = nil
			progress.Error = nil
			_ = s.emit(run, protocol.TypeActionCallProgress, progress, false)
		}
		return
	}
	s.applyToolStatus(t, status)
}
func (s *session) applyToolStatus(t *toolState, status string) {
	if status == "" || status == "pending" {
		return
	}
	s.mu.Lock()
	if t.terminal {
		s.mu.Unlock()
		return
	}
	typ := protocol.TypeActionCallProgress
	synthesizeStart := false
	switch status {
	case "in_progress":
		if !t.started {
			typ = protocol.TypeActionCallStarted
			t.started = true
		}
	case "completed":
		typ = protocol.TypeActionCallCompleted
		synthesizeStart = !t.started
		t.started = true
		t.terminal = true
	case "failed":
		typ = protocol.TypeActionCallFailed
		synthesizeStart = !t.started
		t.started = true
		t.terminal = true
	default:
		s.mu.Unlock()
		s.failActive("acp_invalid_tool_status", "unknown tool status")
		return
	}
	t.status = status
	s.mu.Unlock()
	if synthesizeStart {

		started := s.toolPayload(t)
		started.Progress = nil
		started.Result = nil
		started.Error = nil
		_ = s.emit(t.run, protocol.TypeActionCallStarted, started, false)
	}
	payload := s.toolPayload(t)
	switch typ {
	case protocol.TypeActionCallStarted:
		payload.Progress = nil
		payload.Result = nil
		payload.Error = nil
	case protocol.TypeActionCallCompleted:
		payload.ArgumentsJSON = nil

		if payload.Result == nil {
			payload.Result = json.RawMessage("null")
		}
	case protocol.TypeActionCallFailed:
		payload.ArgumentsJSON = nil
		payload.Result = nil
	}
	_ = s.emit(t.run, typ, payload, false)
}
func (s *session) toolPayload(t *toolState) protocol.ActionCallPayload {
	p := protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: t.run.id, ToolCallID: t.id, RequestedBy: endpointID, ExecutionOwner: "acp-agent", Name: t.title, ArgumentsJSON: rawClone(t.rawInput)}
	if t.status == "failed" {
		p.Error = &protocol.ProtocolError{Code: "tool_failed", Message: "ACP tool call failed"}
		p.Result = rawClone(t.rawOutput)
	} else {
		p.Result = rawClone(t.rawOutput)
	}
	if t.status == "in_progress" {
		p.Progress = t.progressJSON()
	}
	return p
}
func (t *toolState) progressJSON() json.RawMessage {
	v, _ := json.Marshal(map[string]any{"content": json.RawMessage(t.jsonContent), "locations": json.RawMessage(t.locations)})
	return v
}

func (s *session) State(ctx context.Context) (protocol.SessionState, error) {
	if err := ctx.Err(); err != nil {
		return protocol.SessionState{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.state, base.ErrSessionClosed
	}
	return s.state, nil
}
func (s *session) Resolve(ctx context.Context, res base.InteractionResolution) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if res.Permission == nil || res.Input != nil {
		return base.ErrInvalidResolution
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return base.ErrSessionClosed
	}
	p := s.interactions[res.Permission.InteractionID]
	if p == nil {
		s.mu.Unlock()
		return base.ErrInteractionNotFound
	}
	if p.resolved {
		s.mu.Unlock()
		return base.ErrInteractionResolved
	}
	if res.RunID != p.run.id || res.Permission.RunID != p.run.id || res.Permission.SessionID != s.state.SessionID {
		s.mu.Unlock()
		return base.ErrInvalidResolution
	}
	if res.RespondedBy != s.participant || res.Permission.RespondedBy != s.participant {
		s.mu.Unlock()
		return base.ErrWrongResponder
	}

	if res.Permission.RequestedBy != "" && res.Permission.RequestedBy != endpointID {
		s.mu.Unlock()
		return base.ErrInvalidResolution
	}
	option, ok := p.options[res.Permission.ChoiceID]
	if !ok {
		s.mu.Unlock()
		return base.ErrInvalidResolution
	}
	granted := option.Kind == "allow_once" || option.Kind == "allow_always"
	if granted != res.Permission.Granted {
		s.mu.Unlock()
		return base.ErrInvalidResolution
	}
	s.mu.Unlock()
	if err := p.request.Respond(ctx, native.PermissionResponse{Outcome: native.PermissionOutcome{Outcome: "selected", OptionID: option.OptionID}}); err != nil {

		s.settleChildren(p.run, true)
		_ = s.emit(p.run, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: s.state.SessionID, RunID: p.run.id, Error: protocol.ProtocolError{Code: "acp_permission_response_failed", Message: err.Error()}, SettledBy: protocol.SettledByInferred}, true)
		return err
	}
	s.mu.Lock()
	if p.run.terminal {
		s.mu.Unlock()
		return errTerminalWon
	}
	p.resolved = true

	requestEventID := p.requestEventID
	s.mu.Unlock()
	out := protocol.InteractionRejected
	if granted {
		out = protocol.InteractionResolved
	}
	_, err := s.emitEnvelope(p.run, protocol.TypeActionPermissionResolved, protocol.PermissionResolvedPayload{InteractionID: p.id, RequestedBy: endpointID, RespondedBy: s.participant, SessionID: s.state.SessionID, RunID: p.run.id, ToolCallID: p.tool.id, Outcome: out, ChoiceID: option.OptionID, Granted: &granted}, false, requestEventID)
	return err
}
func (s *session) Cancel(ctx context.Context, id protocol.RunID) (protocol.RunCancelResponse, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return protocol.RunCancelResponse{}, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return protocol.RunCancelResponse{}, base.ErrSessionClosed
	}
	r := s.runs[id]
	if r == nil {
		s.mu.Unlock()
		return protocol.RunCancelResponse{}, base.ErrRunNotFound
	}
	if r.terminal {
		status := r.status
		s.mu.Unlock()
		if status == protocol.RunCancelled {
			return protocol.RunCancelResponse{SessionID: s.state.SessionID, RunID: id, Accepted: true, Status: status}, nil
		}
		return protocol.RunCancelResponse{}, &base.RunTerminalError{RunID: id, Status: status}
	}
	if r.cancelRequested {
		s.mu.Unlock()
		return protocol.RunCancelResponse{SessionID: s.state.SessionID, RunID: id, Accepted: true, Status: protocol.RunCancelling}, nil
	}
	s.mu.Unlock()
	if err := s.client.Notify(ctx, native.MethodSessionCancel, native.CancelParams{SessionID: s.nativeID}); err != nil {
		return protocol.RunCancelResponse{}, err
	}
	s.mu.Lock()
	if r.terminal {
		status := r.status
		s.mu.Unlock()
		return protocol.RunCancelResponse{}, &base.RunTerminalError{RunID: id, Status: status}
	}
	r.cancelRequested = true
	r.status = protocol.RunCancelling
	s.mu.Unlock()
	_ = s.emit(r, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: id, Status: protocol.RunCancelling, UpdatedAtMS: s.clock.Now().UnixMilli()}, false)
	return protocol.RunCancelResponse{SessionID: s.state.SessionID, RunID: id, Accepted: true, Status: protocol.RunCancelling}, nil
}
func (s *session) Resume(ctx context.Context, q base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	if err := ctx.Err(); err != nil {
		return base.Recovery{}, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return base.Recovery{}, nil, base.ErrSessionClosed
	}
	r := s.runs[q.RunID]
	if r == nil {
		return base.Recovery{}, nil, base.ErrRunNotFound
	}
	latest := r.next - 1
	if q.AfterSequence > latest {
		return base.Recovery{}, nil, base.ErrReplayCursorFuture
	}
	oldest := uint64(0)
	var suffix []protocol.Envelope
	for _, e := range s.journal {
		if e.RunID != r.id || e.Sequence == nil {
			continue
		}
		if oldest == 0 {
			oldest = *e.Sequence
		}
		if *e.Sequence > q.AfterSequence {
			suffix = append(suffix, e)
		}
	}
	recovery := base.Recovery{State: s.state, RunID: r.id, RequestedAfter: q.AfterSequence, ReplayedFrom: q.AfterSequence, ReplayedThrough: q.AfterSequence}
	gap := q.AfterSequence < latest && (oldest == 0 || q.AfterSequence+1 < oldest)

	stream := make(chan base.Result, len(suffix)+streamCapacity+1)
	if gap {
		recovery.ReplayGap = &base.ReplayGap{RequestedAfter: q.AfterSequence, OldestAvailable: oldest, LatestAvailable: latest}
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
	if r.terminal {
		close(stream)
	} else {
		r.subscribers = append(r.subscribers, stream)
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
	subs := s.allSubscribersLocked()
	s.mu.Unlock()
	s.stopOnce.Do(func() { close(s.stop) })
	err := s.client.Close()
	for _, ch := range subs {
		close(ch)
	}
	return err
}

func (s *session) settleChildren(run *runState, cancel bool) {
	s.mu.Lock()
	type settledPermission struct {
		gate           *permissionState
		requestEventID protocol.EnvelopeID
	}
	var permissions []settledPermission
	var tools []*toolState
	for _, p := range s.interactions {
		if p.run == run && !p.resolved {
			p.resolved = true

			permissions = append(permissions, settledPermission{gate: p, requestEventID: p.requestEventID})
		}
	}
	for _, t := range s.tools {
		if t.run == run && !t.terminal {
			t.terminal = true
			tools = append(tools, t)
		}
	}
	s.mu.Unlock()
	for _, pending := range permissions {
		p := pending.gate
		_ = p.request.Respond(context.Background(), native.PermissionResponse{Outcome: native.PermissionOutcome{Outcome: "cancelled"}})
		reason := protocol.ProtocolError{Code: "run_settled", Message: "parent run settled the permission request"}
		if _, err := s.emitEnvelope(run, protocol.TypeActionPermissionResolved, protocol.PermissionResolvedPayload{InteractionID: p.id, RequestedBy: endpointID, RespondedBy: s.participant, SessionID: s.state.SessionID, RunID: run.id, ToolCallID: p.tool.id, Outcome: protocol.InteractionCancelled, Reason: &reason}, false, pending.requestEventID); err != nil {
			s.mu.Lock()
			p.resolved = false
			s.mu.Unlock()
		}
	}
	for _, t := range tools {
		if !t.started && !cancel {
			t.started = true
			started := s.toolPayload(t)
			started.Progress = nil
			started.Result = nil
			started.Error = nil
			_ = s.emit(run, protocol.TypeActionCallStarted, started, false)
		}
		typ := protocol.TypeActionCallFailed
		p := s.toolPayload(t)

		p.ArgumentsJSON = nil
		p.Progress = nil
		p.Result = nil
		if cancel {
			typ = protocol.TypeActionCallCancelled
			p.Error = nil
		} else {
			p.Error = &protocol.ProtocolError{Code: "incomplete_tool", Message: "prompt completed with unfinished ACP tool"}
		}
		if err := s.emit(run, typ, p, false); err != nil {
			s.mu.Lock()
			t.terminal = false
			s.mu.Unlock()
		}
	}
}
func (s *session) transportFailed() {
	s.mu.Lock()
	r := s.active
	closed := s.closed
	s.mu.Unlock()
	if !closed && r != nil {
		s.opMu.Lock()
		s.settleChildren(r, true)

		_ = s.emit(r, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: s.state.SessionID, RunID: r.id, Error: protocol.ProtocolError{Code: "acp_transport_failure", Message: fmt.Sprint(s.client.Err())}, SettledBy: protocol.SettledByInferred}, true)
		s.opMu.Unlock()
	}
}
func (s *session) failActive(code, message string) {
	s.mu.Lock()
	r := s.active
	s.mu.Unlock()
	if r == nil {
		return
	}
	s.opMu.Lock()
	s.settleChildren(r, true)
	_ = s.emit(r, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: s.state.SessionID, RunID: r.id, Error: protocol.ProtocolError{Code: code, Message: message}}, true)
	s.opMu.Unlock()
}
func (s *session) emit(run *runState, typ protocol.EnvelopeType, payload any, terminal bool) error {
	_, err := s.emitEnvelope(run, typ, payload, terminal, "")
	return err
}

func (s *session) emitEnvelope(run *runState, typ protocol.EnvelopeType, payload any, terminal bool, inReplyTo protocol.EnvelopeID) (protocol.Envelope, error) {
	return s.emitRecorded(run, typ, payload, terminal, inReplyTo, nil)
}

func (s *session) emitRecorded(run *runState, typ protocol.EnvelopeType, payload any, terminal bool, inReplyTo protocol.EnvelopeID, record func(protocol.Envelope)) (protocol.Envelope, error) {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	s.mu.Lock()
	if run.terminal {
		s.mu.Unlock()
		return protocol.Envelope{}, errTerminalWon
	}
	e, err := protocol.NewEnvelope(typ, protocol.EnvelopeID(s.ids.NewID("event")), payload)
	if err != nil {
		s.mu.Unlock()
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
	e.InReplyTo = inReplyTo
	switch typ {
	case protocol.TypeActionCallRequested, protocol.TypeActionCallStarted, protocol.TypeActionCallProgress, protocol.TypeActionCallCompleted, protocol.TypeActionCallFailed, protocol.TypeActionCallCancelled, protocol.TypeActionPermissionRequested, protocol.TypeActionPermissionResolved:
		var generic struct {
			ToolCallID protocol.ToolCallID `json:"tool_call_id"`
		}
		_ = json.Unmarshal(e.Payload, &generic)
		e.ToolCallID = generic.ToolCallID
	}
	if record != nil {
		record(e)
	}
	s.journal = append(s.journal, e)
	if len(s.journal) > s.capacity {
		s.journal = append([]protocol.Envelope(nil), s.journal[len(s.journal)-s.capacity:]...)
	}
	s.state.TranscriptCursor = strconv.FormatUint(seq, 10)
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
	subs := append([]chan base.Result(nil), run.subscribers...)
	run.subscribers = run.subscribers[:0]
	var retained []chan base.Result
	for _, ch := range subs {
		if len(ch) < cap(ch)-1 {
			ch <- base.Result{Envelope: e}
			if terminal {
				close(ch)
			} else {
				retained = append(retained, ch)
			}
			continue
		}

		ch <- base.Result{Error: base.ErrEventStreamOverflow}
		close(ch)
	}
	if !terminal {
		run.subscribers = retained
	}
	s.mu.Unlock()
	return e, nil
}
func (s *session) allSubscribersLocked() []chan base.Result {
	var out []chan base.Result
	for _, r := range s.runs {
		out = append(out, r.subscribers...)
		r.subscribers = nil
	}
	return out
}

var _ base.Session = (*session)(nil)

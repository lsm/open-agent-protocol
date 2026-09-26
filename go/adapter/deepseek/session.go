package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/deepseek/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/deepseek/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/adapter/internal/journal"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

var errTerminalWon = errors.New("deepseek adapter: terminal already selected")
var errUnavailable = errors.New("deepseek adapter: operation unavailable")

type Session struct {
	mu       sync.Mutex
	reduceMu sync.Mutex
	promptMu sync.Mutex
	client   Client
	inbound  <-chan rpc.InboundMessage
	clock    base.Clock
	ids      base.IDGenerator
	nativeID string
	model    string
	state    protocol.SessionState
	closed   bool
	unusable bool
	pending  *runState
	active   *runState
	runs     map[protocol.RunID]*runState
	tools    map[string]*toolState
	children map[string]*childState
	journal  *journal.Journal

	lastSeq  int64
	seqSeen  bool
	stop     chan struct{}
	stopOnce sync.Once
}

type runState struct {
	id                   protocol.RunID
	status               protocol.RunStatus
	next                 uint64
	started, terminal    bool
	receipt              string
	messageID            protocol.MessageID
	turn, step           int64
	candidateTurn        int64
	candidateStep        int64
	candidateOpen        bool
	candidateEvents      []native.Event
	pendingNotifications []bufferedNotification
	startResult          chan error
	stream               journal.Reservation
	startOnce            sync.Once
	final                *native.AssistantMessageEvent
	endKind              string
	turnEnded            bool
	idleAfterEnd         bool
}
type bufferedNotification struct {
	message  rpc.NotificationMessage
	afterSeq int64
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
type childState struct {
	id               string
	run              *runState
	terminal, failed bool
}

func (r *runState) signalStart(err error) {
	r.startOnce.Do(func() { r.startResult <- err; close(r.startResult) })
}

func (s *Session) Submit(ctx context.Context, req protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	if err := ctx.Err(); err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}
	blocks, messageIDs, err := s.nativePrompt(req)
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
	run := &runState{status: protocol.RunQueued, next: 1, messageID: protocol.MessageID(s.ids.NewID("message")), startResult: make(chan error, 1)}
	run.stream = s.journal.Reserve()
	s.pending = run
	s.mu.Unlock()
	s.reduceMu.Unlock()
	var result native.SessionPromptResult
	started := make(chan error, 1)
	callDone := make(chan error, 1)
	go func() {
		callDone <- s.client.CallStarted(ctx, native.MethodSessionPrompt, native.SessionPromptParams{SessionID: s.nativeID, ContentBlocks: blocks}, &result, started)
	}()
	if startErr := <-started; startErr != nil {
		s.promptMu.Unlock()
		s.abortPreStart(run, startErr)
		return protocol.MessageSubmitResponse{}, run.stream.Stream(), startErr
	}
	err = <-callDone
	s.reduceMu.Lock()
	if err != nil || result.MessageID == "" {
		if err == nil {

			s.mu.Lock()
			s.unusable = true
			s.mu.Unlock()
			err = fmt.Errorf("%w: prompt response omitted messageId", ErrNativeProtocol)
		}
		s.reduceMu.Unlock()
		s.promptMu.Unlock()
		s.abortPreStart(run, err)
		return protocol.MessageSubmitResponse{}, run.stream.Stream(), err
	}
	run.receipt = result.MessageID
	s.evaluateAdmission(run)
	s.reduceMu.Unlock()
	s.promptMu.Unlock()
	select {
	case startErr := <-run.startResult:
		if startErr != nil {
			return protocol.MessageSubmitResponse{}, run.stream.Stream(), startErr
		}
	case <-ctx.Done():
		s.reduceMu.Lock()
		if !run.started && !run.terminal {

			s.reduceMu.Unlock()
			return protocol.MessageSubmitResponse{}, run.stream.Stream(), ctx.Err()
		}
		s.reduceMu.Unlock()

		startErr := <-run.startResult
		if startErr != nil {
			return protocol.MessageSubmitResponse{}, run.stream.Stream(), startErr
		}
	}
	return protocol.MessageSubmitResponse{SessionID: req.SessionID, Accepted: true, SubmissionID: protocol.SubmissionID(result.MessageID), RequestedDelivery: protocol.DeliveryAuto, EffectiveDelivery: protocol.DeliveryStart, DeliveryResolution: "session_idle", Admission: protocol.AdmissionStarted, RunID: run.id, Status: protocol.RunRunning, ModelID: s.model, MessageIDs: messageIDs}, run.stream.Stream(), nil
}

func (s *Session) nativePrompt(req protocol.MessageSubmitRequest) ([]native.ContentBlock, []protocol.MessageID, error) {

	if err := base.RefuseUnadvertisedControls(req); err != nil {
		return nil, nil, err
	}
	if req.SessionID == "" || len(req.Messages) == 0 || (req.Delivery != "" && req.Delivery != protocol.DeliveryAuto) {
		return nil, nil, base.ErrInvalidSubmission
	}
	var blocks []native.ContentBlock
	ids := make([]protocol.MessageID, len(req.Messages))
	for i, m := range req.Messages {
		if m.Role != protocol.RoleUser {
			return nil, nil, base.ErrInvalidSubmission
		}
		ids[i] = m.ID
		if ids[i] == "" {
			ids[i] = protocol.MessageID(s.ids.NewID("message"))
		}
		if text, ok := m.Content.Text(); ok {
			blocks = append(blocks, native.ContentBlock{Type: "text", Text: text})
			continue
		}
		parts, ok := m.Content.Parts()
		if !ok {
			return nil, nil, base.ErrInvalidSubmission
		}
		for _, p := range parts {
			if p.Type != protocol.ContentText {
				return nil, nil, base.ErrInvalidSubmission
			}
			blocks = append(blocks, native.ContentBlock{Type: "text", Text: p.Text})
		}
	}
	return blocks, ids, nil
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
				select {
				case in := <-s.inbound:
					s.reduceMu.Lock()
					s.reduce(in)
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
func (s *Session) reduce(in rpc.InboundMessage) {
	if in.Barrier != nil {
		close(in.Barrier)
		return
	}
	if in.Request != nil {
		s.externalActivity("reverse request")
		return
	}
	if in.Notification != nil {
		s.applyNotification(*in.Notification)
	}
}
func (s *Session) applyNotification(n rpc.NotificationMessage) {
	s.mu.Lock()
	run := s.pending
	if run == nil {
		run = s.active
	}
	unusable := s.unusable
	s.mu.Unlock()
	if run == nil {

		if !unusable {
			if status, isStatus := n.Value.(*native.SessionStatusNotification); isStatus && status.SessionID == s.nativeID {
				return
			}
		}
		s.externalActivity("notification without reserved run")
		return
	}
	if !run.started {
		run.pendingNotifications = append(run.pendingNotifications, bufferedNotification{message: n, afterSeq: s.lastSeq})
	}
	s.applyNative(run, n)
}
func (s *Session) applyNative(run *runState, n rpc.NotificationMessage) {
	s.mu.Lock()
	terminal := run.terminal
	s.mu.Unlock()
	if terminal {
		return
	}
	switch v := n.Value.(type) {
	case *native.SessionEventNotification:
		if v.SessionID != s.nativeID {
			s.failRun(run, "deepseek_session_mismatch", "session.event for foreign session")
			return
		}
		if s.seqSeen && v.Event.Seq <= s.lastSeq {
			s.failRun(run, "deepseek_invalid_sequence", "non-monotonic native event sequence")
			return
		}
		s.seqSeen = true
		s.lastSeq = v.Event.Seq
		if !run.started {
			s.observeCandidate(run, v.Event)
			if run.receipt != "" {
				s.evaluateAdmission(run)
			}
			return
		}
		s.applyOwnedEvent(run, v.Event)
	case *native.SessionStatusNotification:
		if v.SessionID != s.nativeID {
			s.failRun(run, "deepseek_session_mismatch", "session.status for foreign session")
			return
		}
		if run.started && run.turnEnded && v.Status == "idle" {
			run.idleAfterEnd = true
			s.trySettle(run)
		}
	case *native.SubagentStartedNotification:
		if v.ParentSessionID != s.nativeID || v.ChildSessionID == s.nativeID {
			s.failRun(run, "deepseek_external_activity", "foreign or recursive subagent")
			return
		}
		if !run.started {
			return
		}
		if s.children[v.ChildSessionID] != nil {
			s.failRun(run, "deepseek_child_lifecycle", "duplicate child start")
			return
		}
		s.children[v.ChildSessionID] = &childState{id: v.ChildSessionID, run: run}
	case *native.SubagentFinishedNotification:
		if v.ParentSessionID != s.nativeID {
			s.failRun(run, "deepseek_external_activity", "foreign child finish")
			return
		}
		if !run.started {

			return
		}
		child := s.children[v.ChildSessionID]
		if child == nil || child.run != run || child.terminal {
			s.failRun(run, "deepseek_child_lifecycle", "unmatched child finish")
			return
		}
		child.terminal = true
		child.failed = v.Status != "ok" || v.StopReason == "max-tokens"
		s.trySettle(run)
	default:
		s.failRun(run, "deepseek_unknown_notification", "unknown notification")
	}
}

func (s *Session) observeCandidate(run *runState, e native.Event) {
	if run.candidateOpen {
		run.candidateEvents = append(run.candidateEvents, e)
	}
	switch e.Type {
	case "turn/start":
		var v native.TurnStart
		_ = e.DataAs(&v)
		if run.candidateOpen {
			s.discardCandidate(run)
		}
		run.candidateOpen = true
		run.candidateTurn = v.Turn
		run.candidateStep = 0
		run.candidateEvents = []native.Event{e}
	case "step/start":
		if !run.candidateOpen {
			return
		}
		var v native.StepBoundary
		_ = e.DataAs(&v)
		if v.Turn != run.candidateTurn {
			s.discardCandidate(run)
			return
		}
		if run.turn != 0 {
			return
		}

		run.candidateStep = v.Step
		run.candidateEvents = []native.Event{run.candidateEvents[0], e}
	case "user/message":
		if !run.candidateOpen || run.candidateStep == 0 {
			return
		}
		var v native.UserMessage
		_ = e.DataAs(&v)
		if run.receipt != "" && v.ID == run.receipt && directUser(v.Source) {
			run.turn = run.candidateTurn
			run.step = run.candidateStep
		}
	case "turn/end":
		if !run.candidateOpen {
			return
		}
		var v native.TurnEnd
		_ = e.DataAs(&v)
		if v.Turn == run.candidateTurn {
			s.discardCandidate(run)
		}
	}
}
func directUser(source native.MessageSource) bool {
	return source.DirectUser()
}

func (s *Session) discardCandidate(run *runState) {
	run.candidateOpen = false
	run.candidateTurn = 0
	run.candidateStep = 0
	run.candidateEvents = nil
}
func (s *Session) evaluateAdmission(run *runState) {
	if run.started || run.terminal || run.receipt == "" {
		return
	}

	var matches, turn, step int64
	var turnStart native.Event
	var candidate []native.Event
	entered, closed := false, false
	for _, buffered := range run.pendingNotifications {
		ev, ok := buffered.message.Value.(*native.SessionEventNotification)
		if !ok {
			continue
		}
		switch ev.Event.Type {
		case "agent/inbox/spliced":
			var v native.InboxSpliced
			if ev.Event.DataAs(&v) != nil {
				continue
			}
			for _, m := range v.Inserted {
				if m.ID == run.receipt && directUser(m.Source) {
					matches++
				}
			}
		case "turn/start":
			var v native.TurnStart
			_ = ev.Event.DataAs(&v)
			turn, step, closed = v.Turn, 0, false
			turnStart = ev.Event
			entered = false
			candidate = []native.Event{ev.Event}
		case "step/start":
			var v native.StepBoundary
			_ = ev.Event.DataAs(&v)
			if turn != v.Turn {
				continue
			}
			if entered {
				candidate = append(candidate, ev.Event)
				continue
			}

			step = v.Step
			candidate = []native.Event{turnStart, ev.Event}
		case "user/message":
			if turn == 0 || step == 0 {
				continue
			}
			candidate = append(candidate, ev.Event)
			var v native.UserMessage
			_ = ev.Event.DataAs(&v)
			if !entered && v.ID == run.receipt && directUser(v.Source) {
				entered = true
				run.turn, run.step = turn, step
				run.candidateEvents = append([]native.Event(nil), candidate...)
			}
		case "turn/end":
			var v native.TurnEnd
			_ = ev.Event.DataAs(&v)
			if v.Turn == turn {
				candidate = append(candidate, ev.Event)
				closed = true
				if !entered {
					turn, step, candidate = 0, 0, nil
				}
			}
		default:
			if turn != 0 {
				candidate = append(candidate, ev.Event)
			}
		}
	}
	if matches != 1 || run.turn == 0 {
		if closed || (matches > 1) {
			s.abortPreStartUnlocked(run, fmt.Errorf("%w: submission ownership proof failed", ErrNativeProtocol))
		}
		return
	}
	if closed {
		run.candidateEvents = candidate
	} else if turn == run.turn && len(candidate) > len(run.candidateEvents) {

		run.candidateEvents = candidate
	}
	run.id = protocol.RunID(s.ids.NewID("run"))
	run.stream.Bind(run.id)
	run.started = true
	run.status = protocol.RunRunning
	s.mu.Lock()
	s.pending = nil
	s.active = run
	s.runs[run.id] = run
	s.state.Status = protocol.SessionRunning
	s.state.ActiveRunID = run.id
	s.mu.Unlock()
	if err := s.emit(run, protocol.TypeRunStarted, protocol.RunStartedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunRunning, ModelID: s.model, StartedAtMS: s.clock.Now().UnixMilli()}, false); err != nil {
		run.signalStart(err)
		return
	}
	run.signalStart(nil)
	turnEndSeq := int64(0)
	admittedStep := run.step
	for _, e := range run.candidateEvents {
		if e.Type == "turn/end" {
			turnEndSeq = e.Seq
		}
		if e.Type == "turn/start" || e.Type == "user/message" {
			continue
		}
		if e.Type == "step/start" {
			var v native.StepBoundary
			if e.DataAs(&v) == nil && v.Step <= admittedStep {
				continue
			}
		}
		s.applyOwnedEvent(run, e)
	}

	for _, buffered := range run.pendingNotifications {
		switch v := buffered.message.Value.(type) {
		case *native.SessionStatusNotification:
			if v.SessionID != s.nativeID {
				s.failRun(run, "deepseek_session_mismatch", "session.status for foreign session")
			} else if run.turnEnded && buffered.afterSeq >= turnEndSeq {
				run.idleAfterEnd = v.Status == "idle"
			}
		case *native.SubagentStartedNotification:
			if v.ParentSessionID != s.nativeID || v.ChildSessionID == s.nativeID || s.children[v.ChildSessionID] != nil {
				s.failRun(run, "deepseek_child_lifecycle", "invalid buffered child start")
			} else {
				s.children[v.ChildSessionID] = &childState{id: v.ChildSessionID, run: run}
			}
		case *native.SubagentFinishedNotification:
			child := s.children[v.ChildSessionID]
			if v.ParentSessionID != s.nativeID || child == nil || child.run != run || child.terminal {
				s.failRun(run, "deepseek_child_lifecycle", "invalid buffered child finish")
			} else {
				child.terminal = true
				child.failed = v.Status != "ok" || v.StopReason == "max-tokens"
			}
		}
	}
	run.pendingNotifications = nil
	s.trySettle(run)
	run.candidateEvents = nil
}

func (s *Session) applyOwnedEvent(run *runState, e native.Event) {
	if e.Type == "turn/start" {
		var v native.TurnStart
		_ = e.DataAs(&v)
		if v.Turn != run.turn {
			s.failRun(run, "deepseek_invalid_grammar", "overlapping foreign turn")
			return
		}
		return
	}
	switch e.Type {
	case "step/start":
		var v native.StepBoundary
		_ = e.DataAs(&v)
		if v.Turn != run.turn || v.Step <= run.step {
			s.failRun(run, "deepseek_invalid_grammar", "invalid step start")
			return
		}
		run.step = v.Step
	case "step/end":
		var v native.StepBoundary
		_ = e.DataAs(&v)
		if v.Turn != run.turn || v.Step != run.step {
			s.failRun(run, "deepseek_invalid_grammar", "invalid step end")
		}
	case "assistant/attempt":
		var v native.AssistantAttempt
		_ = e.DataAs(&v)
		if !s.sameStep(run, v.Turn, v.Step) {
			return
		}

	case "assistant/message":
		var v native.AssistantMessageEvent
		_ = e.DataAs(&v)
		if !s.sameStep(run, v.Turn, v.Step) {
			return
		}

		s.emitStreamRecords(run, v.Stream)
		run.final = &v
	case "tool/call":
		var v native.ToolCall
		_ = e.DataAs(&v)
		if !s.sameStep(run, v.Turn, v.Step) {
			return
		}
		s.startTool(run, v)
	case "tool/result":
		var v native.ToolResult
		_ = e.DataAs(&v)
		if !s.sameStep(run, v.Turn, v.Step) {
			return
		}
		s.endTool(run, v)
	case "turn/end":
		var v native.TurnEnd
		_ = e.DataAs(&v)
		if v.Turn != run.turn || run.turnEnded {
			s.failRun(run, "deepseek_invalid_grammar", "invalid turn end")
			return
		}
		var reason struct {
			Kind string `json:"kind"`
		}
		_ = native.DecodeStrict(v.Reason, &reason)
		run.endKind = reason.Kind
		run.turnEnded = true
		s.trySettle(run)
	case "user/message", "agent/inbox/spliced", "todo/write", "request/header", "request/context", "session/end-seed":
		return
	default:

		if native.ObservedOnly(e.Type) || (e.Ignorable != nil && *e.Ignorable) {
			return
		}
		s.failRun(run, "deepseek_unknown_event", fmt.Sprintf("unknown required event %q", e.Type))
	}
}
func (s *Session) sameStep(run *runState, t, st int64) bool {
	if t != run.turn || st != run.step || run.turnEnded {
		s.failRun(run, "deepseek_invalid_grammar", "event outside open owned step")
		return false
	}
	return true
}

func (s *Session) emitStreamRecords(run *runState, records []native.AssistantStreamRecord) {
	for _, record := range records {
		switch record.Type {
		case "text-chunks":
			for _, text := range record.Texts {
				s.emitDelta(run, protocol.ContentPart{Type: protocol.ContentText, Text: text})
			}
		case "reasoning-chunks":
			for _, text := range record.Texts {
				s.emitDelta(run, protocol.ContentPart{Type: protocol.ContentReasoning, Reasoning: text})
			}
		case "chunk":
			if part, ok := chunkPart(record.Chunk); ok {
				s.emitDelta(run, part)
			}
		}
	}
}

func (s *Session) emitDelta(run *runState, part protocol.ContentPart) {
	_ = s.emit(run, protocol.TypeContentDelta, protocol.ContentDeltaPayload{SessionID: s.state.SessionID, RunID: run.id, MessageID: run.messageID, Part: part}, false)
}

func chunkPart(raw json.RawMessage) (protocol.ContentPart, bool) {
	var v struct {
		Type  string `json:"type"`
		Index int64  `json:"index"`
		Text  string `json:"text"`
	}
	if native.DecodeStrict(raw, &v) != nil {
		return protocol.ContentPart{}, false
	}
	switch v.Type {
	case "text-delta":
		return protocol.ContentPart{Type: protocol.ContentText, Text: v.Text}, true
	case "reasoning-delta":
		return protocol.ContentPart{Type: protocol.ContentReasoning, Reasoning: v.Text}, true
	}
	return protocol.ContentPart{}, false
}

func (s *Session) startTool(run *runState, v native.ToolCall) {
	key := toolKey(run, v.CallID)
	if s.tools[key] != nil {
		s.failRun(run, "deepseek_tool_lifecycle", "duplicate tool call")
		return
	}
	t := &toolState{nativeID: v.CallID, id: protocol.ToolCallID(s.ids.NewID("tool-call")), run: run, name: v.Name, args: json.RawMessage(v.Arguments)}
	s.tools[key] = t
	p := s.toolPayload(t)
	req, _ := s.emitEnvelope(run, protocol.TypeActionCallRequested, p, false, "")
	p.ArgumentsJSON = nil
	st, _ := s.emitEnvelope(run, protocol.TypeActionCallStarted, p, false, req.ID)
	t.requested = req.ID
	t.started = st.ID
}
func (s *Session) endTool(run *runState, v native.ToolResult) {
	key := toolKey(run, v.Message.Source.CallID)
	t := s.tools[key]
	if t == nil || t.terminal {
		s.failRun(run, "deepseek_tool_lifecycle", "unmatched tool result")
		return
	}
	t.terminal = true
	p := s.toolPayload(t)
	p.ArgumentsJSON = nil
	raw, _ := json.Marshal(v.Message.Content)
	p.Result = raw
	if v.Error != nil {
		p.Result = nil
		p.Error = &protocol.ProtocolError{Code: v.Error.Code, Message: v.Error.Name}
		if reason, ok := v.Error.ReasonText(); ok && reason != "" {
			p.Error.Message = reason
			p.Error.Details = map[string]any{"name": v.Error.Name}
		}
		_, _ = s.emitEnvelope(run, protocol.TypeActionCallFailed, p, false, t.started)
	} else {
		_, _ = s.emitEnvelope(run, protocol.TypeActionCallCompleted, p, false, t.started)
	}
}
func toolKey(r *runState, id string) string { return fmt.Sprintf("%p\x00%s", r, id) }
func (s *Session) toolPayload(t *toolState) protocol.ActionCallPayload {
	return protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: t.run.id, ToolCallID: t.id, RequestedBy: endpointID, ExecutionOwner: "deepseek-harness", Name: t.name, ArgumentsJSON: cloneRaw(t.args)}
}

func (s *Session) trySettle(run *runState) {
	if !run.turnEnded || !run.idleAfterEnd {
		return
	}
	failedChild := false
	for _, c := range s.children {
		if c.run == run {
			if !c.terminal {
				return
			}
			failedChild = failedChild || c.failed
		}
	}
	s.settleOpenTools(run)
	if failedChild {
		s.failRun(run, "deepseek_child_failed", "subagent failed")
		return
	}
	if run.endKind != "completed" {
		s.failRun(run, "deepseek_"+run.endKind, "native turn ended: "+run.endKind)
		return
	}
	if run.final == nil {
		s.failRun(run, "deepseek_missing_final_message", "completed turn omitted assistant message")
		return
	}
	content, err := s.blocksContent(run.final.Message.Content, run)
	if err != nil {
		s.failRun(run, "deepseek_invalid_final_message", err.Error())
		return
	}
	var usage *protocol.Usage
	if u := run.final.Usage; u != nil {
		usage = &protocol.Usage{InputTokens: uint64(u.InputTokens), OutputTokens: uint64(u.OutputTokens), TotalTokens: uint64(u.InputTokens + u.OutputTokens)}
	}
	_ = s.emit(run, protocol.TypeRunCompleted, protocol.RunCompletedPayload{SessionID: s.state.SessionID, RunID: run.id, FinalResponse: protocol.Message{ID: run.messageID, Role: protocol.RoleAssistant, Content: content}, StopReason: "completed", Usage: usage}, true)
}
func (s *Session) blocksContent(blocks []native.ContentBlock, run *runState) (protocol.MessageContent, error) {
	parts := []protocol.ContentPart{}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, protocol.ContentPart{Type: protocol.ContentText, Text: b.Text})
		case "reasoning":
			parts = append(parts, protocol.ContentPart{Type: protocol.ContentReasoning, Reasoning: b.Text})
		case "tool-call":
			t := s.tools[toolKey(run, b.ID)]
			if t == nil || t.name != b.Name {
				return protocol.MessageContent{}, fmt.Errorf("final message references unknown tool %q", b.ID)
			}
			parts = append(parts, protocol.ContentPart{Type: protocol.ContentToolCall, ToolCallID: t.id, Name: b.Name, ArgumentsJSON: json.RawMessage(b.Arguments)})
		case "tool-result", "image":
			return protocol.MessageContent{}, fmt.Errorf("assistant message contains invalid %s block", b.Type)
		default:
			return protocol.MessageContent{}, fmt.Errorf("unknown assistant content block %q", b.Type)
		}
	}
	if len(parts) == 1 && parts[0].Type == protocol.ContentText {
		return protocol.TextContent(parts[0].Text), nil
	}
	if len(parts) == 0 {

		return protocol.TextContent(""), nil
	}
	return protocol.PartsContent(parts), nil
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
func (s *Session) Resolve(ctx context.Context, _ base.InteractionResolution) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return base.ErrInteractionNotFound
}
func (s *Session) Cancel(ctx context.Context, _ protocol.RunID) (protocol.RunCancelResponse, error) {
	if err := ctx.Err(); err != nil {
		return protocol.RunCancelResponse{}, err
	}
	return protocol.RunCancelResponse{}, errUnavailable
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
func (s *Session) externalActivity(what string) {
	s.mu.Lock()
	run := s.pending
	if run == nil {
		run = s.active
	}
	s.unusable = true
	s.mu.Unlock()
	if run != nil {
		s.failRun(run, "deepseek_external_activity", what)
	}
}
func (s *Session) abortPreStart(run *runState, err error) {
	s.reduceMu.Lock()
	defer s.reduceMu.Unlock()
	s.mu.Lock()
	if run.terminal {
		s.mu.Unlock()
		return
	}
	run.terminal = true
	if s.pending == run {
		s.pending = nil
	}
	s.state.Status = protocol.SessionIdle
	s.mu.Unlock()
	run.stream.Release()
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
	s.settleOpenTools(run)
	_ = s.emit(run, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: s.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: code, Message: msg}, SettledBy: settledBy}, true)
}

func (s *Session) settleOpenTools(run *runState) {
	for _, t := range s.tools {
		if t.run != run || t.terminal {
			continue
		}
		t.terminal = true
		p := s.toolPayload(t)
		p.ArgumentsJSON = nil
		p.Error = &protocol.ProtocolError{Code: "incomplete_tool", Message: "turn settled with an unfinished deepseek tool"}
		_, _ = s.emitEnvelope(run, protocol.TypeActionCallFailed, p, false, t.started)
	}
}
func (s *Session) abortPreStartUnlocked(run *runState, err error) {
	s.mu.Lock()
	if run.terminal {
		s.mu.Unlock()
		return
	}
	run.terminal = true
	if s.pending == run {
		s.pending = nil
	}
	s.mu.Unlock()
	run.stream.Release()
	run.signalStart(err)
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
		s.failRunSettled(run, "deepseek_process_exit", fmt.Sprint(s.client.Err()), protocol.SettledByInferred)
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
	if strings.HasPrefix(string(t), "action.call.") {
		var a struct {
			ToolCallID protocol.ToolCallID `json:"tool_call_id"`
		}
		_ = json.Unmarshal(e.Payload, &a)
		e.ToolCallID = a.ToolCallID
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

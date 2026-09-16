package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/opencode/internal/native"
	"github.com/lsm/open-agent-protocol/protocol"
)

var errTerminalWon = errors.New("opencode adapter: terminal already selected")

const streamCapacity = 64

type session struct {
	mu           sync.Mutex
	emitMu       sync.Mutex
	transitionMu sync.Mutex
	opMu         sync.Mutex
	client       Client
	subscription Subscription
	events       <-chan native.Event
	clock        base.Clock
	ids          base.IDGenerator
	capacity     int
	historyLimit int
	pollMin      time.Duration
	pollMax      time.Duration
	nativeID     native.SessionID
	participant  protocol.ParticipantID
	state        protocol.SessionState
	closed       bool
	unusable     bool
	active       *runState
	runs         map[protocol.RunID]*runState
	pending      map[native.MessageID]*runState
	tools        map[string]*toolState
	reduced      map[int64]bool
	journal      []protocol.Envelope
	lastSeq      int64
	stop         chan struct{}
	stopOnce     sync.Once
	subCancel    context.CancelFunc
	settleCtx    context.Context
	settleCancel context.CancelFunc
}

type runState struct {
	id              protocol.RunID
	status          protocol.RunStatus
	next            uint64
	terminal        bool
	prompted        bool
	cancelRequested bool
	settling        bool
	messageID       protocol.MessageID
	nativeMessageID native.MessageID
	parts           []protocol.ContentPart
	usage           protocol.Usage
	lastFinish      string
	failure         *native.UnknownErrorBlock
	openSteps       int
	admitted        chan struct{}
	subscribers     []chan base.Result
}

type toolState struct {
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
	promptText, delivery, err := s.submitInput(req)
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
	run := &runState{id: protocol.RunID(s.ids.NewID("run")), status: protocol.RunQueued, next: 1, messageID: protocol.MessageID(s.ids.NewID("message")), admitted: make(chan struct{})}
	stream := make(chan base.Result, streamCapacity+1)
	run.subscribers = []chan base.Result{stream}
	s.active = run
	s.runs[run.id] = run
	s.state.Status = protocol.SessionRunning
	s.state.ActiveRunID = run.id
	// The session model was fixed at creation; a per-submit override was
	// rejected by submitInput, so retain the native model instead of clearing it.
	model := s.state.CurrentModelID
	s.state.UpdatedAtMS = s.clock.Now().UnixMilli()
	s.mu.Unlock()
	// The reservation response (decision 0002): the run identity is reserved
	// at admission and nothing is emitted yet. Any pre-start failure path
	// that has already settled the run on the stream still reports this
	// accepted queued reservation — never an error paired with a dangling
	// stream.
	reservation := protocol.MessageSubmitResponse{
		SessionID:    s.state.SessionID,
		Accepted:     true,
		SubmissionID: protocol.SubmissionID(s.ids.NewID("submission")),
		// The zero-value delivery is the accepted spelling of auto, so the
		// response must report the canonical value rather than the empty string
		// (the schema's delivery enum rejects "").
		RequestedDelivery: protocol.DeliveryAuto,
		EffectiveDelivery: protocol.EffectiveDeliveryQueue,
		Admission:         protocol.AdmissionQueued,
		RunID:             run.id,
		Status:            protocol.RunQueued,
		ModelID:           model,
	}
	nativeMessage := native.MessageID(s.ids.NewID("opencode-message"))
	if !nativeMessage.Valid() {
		s.mu.Lock()
		s.unusable = true
		s.mu.Unlock()
		close(run.admitted)
		s.failRun(run, "opencode_invalid_message_id", "ID generator must produce a msg_-prefixed identity for kind opencode-message")
		return reservation, stream, nil
	}
	run.nativeMessageID = nativeMessage
	// Register the reservation before issuing the prompt: the server may
	// publish session.next.prompted on the SSE stream before the prompt HTTP
	// response arrives, and the event is matched by native message id. Every
	// definite admission failure removes the entry so a failed admission never
	// leaves a stale mapping (the success path leaves it for the prompted event
	// to consume).
	s.mu.Lock()
	s.pending[nativeMessage] = run
	s.mu.Unlock()
	admitted, err := s.client.Prompt(ctx, s.nativeID, native.PromptRequest{ID: nativeMessage, Prompt: native.Prompt{Text: promptText}, Delivery: delivery})
	if err != nil {
		s.mu.Lock()
		delete(s.pending, nativeMessage)
		s.unusable = true
		s.mu.Unlock()
		close(run.admitted)
		s.failRun(run, "opencode_admission_ambiguous", err.Error())
		return reservation, stream, nil
	}
	if admitted.ID != nativeMessage {
		s.mu.Lock()
		delete(s.pending, nativeMessage)
		s.unusable = true
		s.mu.Unlock()
		close(run.admitted)
		s.failRun(run, "opencode_foreign_admission", fmt.Sprintf("server admitted %s for request %s", admitted.ID, nativeMessage))
		return reservation, stream, nil
	}
	reservation.MessageIDs = []protocol.MessageID{protocol.MessageID(admitted.ID)}
	if admitted.PromotedSeq != nil {
		reservation.Admission = protocol.AdmissionStarted
		reservation.EffectiveDelivery = protocol.DeliveryStart
		reservation.Status = protocol.RunRunning
		s.mu.Lock()
		run.status = protocol.RunRunning
		s.mu.Unlock()
	}
	close(run.admitted)
	return reservation, stream, nil
}

func (s *session) submitInput(req protocol.MessageSubmitRequest) (string, native.Delivery, error) {
	// OpenCode applies a model when the session is created and the prompt request
	// carries only content, so no per-run control has a native surface here.
	// Each is refused under its own capability key before admission, so a
	// caller learns which control to stop sending (decision 0005).
	if err := base.RefuseUnadvertisedControls(req); err != nil {
		return "", "", err
	}
	if req.SessionID == "" || len(req.Messages) != 1 {
		return "", "", base.ErrInvalidSubmission
	}
	message := req.Messages[0]
	if message.Role != protocol.RoleUser {
		return "", "", fmt.Errorf("%w: OpenCode adapter accepts one user text message", ErrUnsupported)
	}
	text, ok := message.Content.Text()
	if !ok {
		return "", "", fmt.Errorf("%w: OpenCode adapter accepts user text only", ErrUnsupported)
	}
	switch req.Delivery {
	case "", protocol.DeliveryAuto:
		// OpenCode has no auto mode; steer starts immediately when the
		// session is idle, which is the truthful auto projection here.
		return text, native.DeliverySteer, nil
	default:
		// steer and queue are native server deliveries, and decision 0002
		// made an auto request resolving to a queued reservation canonical;
		// but explicit queue/steer delivery requests still exceed the
		// v0.1 subset (deferred by decisions 0001/0002).
		return "", "", fmt.Errorf("%w: delivery %q is outside the v0.1 request subset", ErrUnsupported, req.Delivery)
	}
}

func (s *session) dispatch() {
	for {
		select {
		case event := <-s.events:
			s.transitionMu.Lock()
			s.handleEventLocked(event)
			s.transitionMu.Unlock()
		case <-s.subscription.Done():
			// The subscription may have enqueued valid observations
			// immediately before it ended; reduce that ordered prefix
			// before projecting transport failure.
			for {
				select {
				case event := <-s.events:
					s.transitionMu.Lock()
					s.handleEventLocked(event)
					s.transitionMu.Unlock()
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

func (s *session) handleEventLocked(event native.Event) {
	if event.Durable.AggregateID != string(s.nativeID) {
		s.mu.Lock()
		run := s.active
		s.unusable = true
		s.mu.Unlock()
		if run != nil {
			<-run.admitted
			s.failRun(run, "opencode_foreign_session", "durable event belongs to another session")
		}
		return
	}
	s.mu.Lock()
	if s.reduced[event.Durable.Seq] {
		s.mu.Unlock()
		return
	}
	s.reduced[event.Durable.Seq] = true
	if event.Durable.Seq > s.lastSeq {
		s.lastSeq = event.Durable.Seq
		s.state.TranscriptCursor = formatSeq(s.lastSeq)
	}
	run := s.active
	s.mu.Unlock()
	switch event.Type {
	case native.TypePromptAdmitted:
		var data native.PromptedData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_prompt_event", err.Error())
			return
		}
		// Admission was already corroborated synchronously by the prompt
		// response; foreign inputs on this session are observed-only.
		return
	case native.TypePrompted:
		var data native.PromptedData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_prompt_event", err.Error())
			return
		}
		s.mu.Lock()
		pending := s.pending[data.MessageID]
		delete(s.pending, data.MessageID)
		s.mu.Unlock()
		if pending == nil || pending != run {
			return
		}
		<-pending.admitted
		if err := s.emit(pending, protocol.TypeRunStarted, protocol.RunStartedPayload{SessionID: s.state.SessionID, RunID: pending.id, Status: protocol.RunRunning, ModelID: s.state.CurrentModelID, StartedAtMS: s.clock.Now().UnixMilli()}, false); err != nil {
			return
		}
		// Flag only after the started event exists so Cancel can rely on
		// prompted implying an emitted run.started.
		s.mu.Lock()
		pending.prompted = true
		s.mu.Unlock()
	case native.TypeStepStarted:
		var data native.StepStartedData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_step_event", err.Error())
			return
		}
		if run != nil {
			s.mu.Lock()
			if !run.terminal {
				run.openSteps++
			}
			s.mu.Unlock()
		}
	case native.TypeStepEnded:
		var data native.StepEndedData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_step_event", err.Error())
			return
		}
		if run == nil {
			return
		}
		<-run.admitted
		s.mu.Lock()
		if run.terminal {
			s.mu.Unlock()
			return
		}
		run.openSteps--
		run.lastFinish = data.Finish
		run.usage.InputTokens += uint64(data.Tokens.Input)
		run.usage.OutputTokens += uint64(data.Tokens.Output)
		run.usage.TotalTokens += uint64(data.Tokens.Input) + uint64(data.Tokens.Output)
		open := run.openSteps
		s.mu.Unlock()
		if open <= 0 {
			s.confirmSettlementLocked(run, event.Durable.Seq)
		}
	case native.TypeStepFailed:
		var data native.StepFailedData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_step_event", err.Error())
			return
		}
		if run == nil {
			return
		}
		<-run.admitted
		s.mu.Lock()
		if run.terminal {
			s.mu.Unlock()
			return
		}
		run.openSteps--
		failure := data.Error
		run.failure = &failure
		open := run.openSteps
		s.mu.Unlock()
		if open <= 0 {
			s.confirmSettlementLocked(run, event.Durable.Seq)
		}
	case native.TypeTextEnded:
		var data native.TextEndedData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_text_event", err.Error())
			return
		}
		if run == nil {
			return
		}
		<-run.admitted
		s.mu.Lock()
		terminal := run.terminal
		if !terminal {
			run.parts = append(run.parts, protocol.ContentPart{Type: protocol.ContentText, Text: data.Text})
		}
		s.mu.Unlock()
		if terminal {
			return
		}
		// The durable stream replays full-value boundaries rather than
		// live deltas, so each boundary is one degraded content delta.
		_ = s.emit(run, protocol.TypeContentDelta, protocol.ContentDeltaPayload{SessionID: s.state.SessionID, RunID: run.id, MessageID: run.messageID, Part: protocol.ContentPart{Type: protocol.ContentText, Text: data.Text}}, false)
	case native.TypeReasoningEnded:
		var data native.ReasoningEndedData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_reasoning_event", err.Error())
			return
		}
		if run == nil {
			return
		}
		<-run.admitted
		s.mu.Lock()
		terminal := run.terminal
		if !terminal {
			run.parts = append(run.parts, protocol.ContentPart{Type: protocol.ContentReasoning, Reasoning: data.Text})
		}
		s.mu.Unlock()
		if terminal {
			return
		}
		_ = s.emit(run, protocol.TypeContentDelta, protocol.ContentDeltaPayload{SessionID: s.state.SessionID, RunID: run.id, MessageID: run.messageID, Part: protocol.ContentPart{Type: protocol.ContentReasoning, Reasoning: data.Text}}, false)
	case native.TypeToolCalled:
		var data native.ToolCalledData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_tool_event", err.Error())
			return
		}
		if run == nil || data.CallID == "" || data.Tool == "" {
			return
		}
		<-run.admitted
		args, err := json.Marshal(data.Input)
		if err != nil {
			s.failRun(run, "opencode_invalid_tool_arguments", err.Error())
			return
		}
		s.startTool(run, data.CallID, data.Tool, args)
	case native.TypeToolProgress:
		var data native.ToolProgressData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_tool_event", err.Error())
			return
		}
		if run == nil {
			return
		}
		progress, err := json.Marshal(data.Content)
		if err != nil {
			s.failRun(run, "opencode_invalid_tool_progress", err.Error())
			return
		}
		s.updateTool(run, data.CallID, progress)
	case native.TypeToolSuccess, native.TypeToolFailed:
		failed := event.Type == native.TypeToolFailed
		var failure native.UnknownErrorBlock
		var content json.RawMessage
		if failed {
			var data native.ToolFailedData
			if err := native.DecodeData(event, &data); err != nil {
				s.failActive(run, "opencode_invalid_tool_event", err.Error())
				return
			}
			failure = data.Error
			if raw, err := json.Marshal(data.Error); err == nil {
				content = raw
			}
		} else {
			var data native.ToolSuccessData
			if err := native.DecodeData(event, &data); err != nil {
				s.failActive(run, "opencode_invalid_tool_event", err.Error())
				return
			}
			if raw, err := json.Marshal(data.Content); err == nil {
				content = raw
			}
		}
		if run == nil {
			return
		}
		s.endTool(run, event, failed, failure, content)
	case native.TypeAgentSwitched, native.TypeModelSwitched, native.TypeMoved, native.TypeContextUpdated,
		native.TypeSynthetic, native.TypeShellStarted, native.TypeShellEnded, native.TypeTextStarted,
		native.TypeReasoningStarted, native.TypeToolInputStarted, native.TypeToolInputEnded, native.TypeRetried,
		native.TypeCompactionStarted, native.TypeCompactionEnded, native.TypeRevertStaged, native.TypeRevertCleared,
		native.TypeRevertCommitted:
		// Session-scope or live-only observations with no core OAP claim.
		return
	default:
		s.failActive(run, "opencode_unknown_event", fmt.Sprintf("unknown durable event %q", event.Type))
	}
}

func (s *session) failActive(run *runState, code, message string) {
	if run == nil {
		return
	}
	<-run.admitted
	s.failRun(run, code, message)
}

// awaitQuiescenceLocked blocks until the native agent loop stops reporting
// this session as active, supplying the run-terminal evidence the durable
// inventory lacks.
//
// The server's run coordinator keeps a session in the active set for one
// whole drain, and a drain is one agent loop covering every step of a turn,
// so the set does not flap between steps the way a per-step signal would.
// Work recorded mid-turn installs a successor entry that keeps the key
// present, so a steer accepted during the turn also holds the run open.
//
// It reports whether settlement should continue. A step that opens while we
// poll re-arms settlement on its own terminal event, so the caller stops
// instead of fencing a turn that has visibly resumed.
func (s *session) awaitQuiescenceLocked(run *runState) (bool, error) {
	delay := s.pollMin
	for {
		active, err := s.client.Active(s.settleContext())
		if err != nil {
			return false, err
		}
		if !active[s.nativeID] {
			return true, nil
		}
		// Reduce whatever the subscription delivered while we polled: a step
		// that opened in that window must suppress this settlement. The
		// settling flag makes the nested confirm call a no-op, so the decision
		// stays with this frame.
		s.drainEnqueuedLocked()
		s.mu.Lock()
		terminal, open := run.terminal, run.openSteps
		s.mu.Unlock()
		if terminal || open > 0 {
			return false, nil
		}
		if err := s.sleepSettle(delay); err != nil {
			return false, err
		}
		delay *= 2
		if delay > s.pollMax {
			delay = s.pollMax
		}
	}
}

// sleepSettle waits out one poll interval, returning early when the session is
// torn down so settlement never outlives its client.
func (s *session) sleepSettle(delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	ctx := s.settleContext()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.stop:
		return context.Canceled
	case <-timer.C:
		return nil
	}
}

// confirmSettlementLocked derives the run terminal. The pinned durable
// inventory has no run-terminal event, so settlement is quiescence
// corroboration plus a history fence at the watermark sequence.
func (s *session) confirmSettlementLocked(run *runState, watermark int64) {
	s.mu.Lock()
	if run.terminal || run.settling {
		s.mu.Unlock()
		return
	}
	run.settling = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		run.settling = false
		s.mu.Unlock()
	}()
	proceed, err := s.awaitQuiescenceLocked(run)
	if err != nil {
		s.mu.Lock()
		closed := s.closed
		if !closed {
			s.unusable = true
		}
		s.mu.Unlock()
		if !closed {
			<-run.admitted
			s.failRun(run, "opencode_quiescence_failed", err.Error())
		}
		return
	}
	if !proceed {
		return
	}
	// Drain everything the subscription already delivered before consulting
	// the durable store, then fence with history so settlement never races
	// an event still in flight.
	s.drainEnqueuedLocked()
	after := watermark
	for {
		s.mu.Lock()
		terminal := run.terminal
		s.mu.Unlock()
		if terminal {
			return
		}
		page, err := s.client.History(s.settleContext(), s.nativeID, after, s.historyLimit)
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			if !closed {
				s.unusable = true
			}
			s.mu.Unlock()
			if !closed {
				<-run.admitted
				s.failRun(run, "opencode_history_failed", err.Error())
			}
			return
		}
		for _, event := range page.Events {
			s.handleEventLocked(event)
		}
		if !page.HasMore || len(page.Events) == 0 {
			break
		}
		after = page.Events[len(page.Events)-1].Durable.Seq
	}
	// The fence is a client round trip, so the subscription may have enqueued
	// more of the turn meanwhile. The terminal decision must consult that
	// prefix too: a step.started reduced only after the terminal would be
	// dropped as post-terminal instead of re-arming settlement.
	s.drainEnqueuedLocked()
	s.mu.Lock()
	terminal := run.terminal
	open := run.openSteps
	s.mu.Unlock()
	if terminal {
		return
	}
	if open > 0 {
		// A further step opened during the fence; its own terminal event
		// re-arms settlement.
		return
	}
	<-run.admitted
	s.settleRunLocked(run)
}

// drainEnqueuedLocked reduces every native event the subscription has already
// delivered. It never blocks: the caller is the dispatcher goroutine holding
// transitionMu, so anything not yet enqueued is reduced by dispatch afterwards.
func (s *session) drainEnqueuedLocked() {
	for {
		select {
		case event := <-s.events:
			s.handleEventLocked(event)
		default:
			return
		}
	}
}

func (s *session) settleRunLocked(run *runState) {
	s.mu.Lock()
	cancelRequested := run.cancelRequested
	failure := run.failure
	finish := run.lastFinish
	parts := append([]protocol.ContentPart(nil), run.parts...)
	usage := run.usage
	s.mu.Unlock()
	s.settleTools(run, cancelRequested)
	if cancelRequested {
		_ = s.emit(run, protocol.TypeRunCancelled, protocol.RunCancelledPayload{SessionID: s.state.SessionID, RunID: run.id, Reason: "OpenCode interrupt confirmed idle"}, true)
		return
	}
	if failure != nil {
		s.failRun(run, "opencode_step_failed", failure.Message)
		return
	}
	content := protocol.MessageContent(protocol.TextContent(""))
	if len(parts) > 0 {
		content = protocol.PartsContent(parts)
	}
	_ = s.emit(run, protocol.TypeRunCompleted, protocol.RunCompletedPayload{
		SessionID:     s.state.SessionID,
		RunID:         run.id,
		FinalResponse: protocol.Message{ID: run.messageID, Role: protocol.RoleAssistant, Content: content},
		StopReason:    finish,
		Usage:         &usage,
	}, true)
}

func (s *session) settleContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settleCtx == nil {
		s.settleCtx, s.settleCancel = context.WithCancel(context.Background())
	}
	return s.settleCtx
}

func (s *session) startTool(run *runState, nativeID, name string, args json.RawMessage) {
	s.mu.Lock()
	key := toolKey(run, nativeID)
	tool := s.tools[key]
	if tool == nil {
		tool = &toolState{id: protocol.ToolCallID(s.ids.NewID("tool-call")), run: run, name: name, args: cloneRaw(args)}
		s.tools[key] = tool
	}
	if tool.run != run || tool.terminal || tool.started {
		s.mu.Unlock()
		s.failRun(run, "opencode_invalid_tool_lifecycle", "duplicate or foreign tool start")
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
		s.failRun(run, "opencode_invalid_tool_lifecycle", "tool progress without active start")
		return
	}
	tool.progress = cloneRaw(progress)
	s.mu.Unlock()
	payload := s.toolPayload(tool)
	payload.ArgumentsJSON = nil
	_, _ = s.emitEnvelope(run, protocol.TypeActionCallProgress, payload, false, tool.startedEvent)
}

func (s *session) endTool(run *runState, event native.Event, failed bool, failure native.UnknownErrorBlock, content json.RawMessage) {
	s.mu.Lock()
	var data struct {
		CallID string `json:"callID"`
	}
	_ = native.DecodeData(event, &data)
	tool := s.tools[toolKey(run, data.CallID)]
	if tool == nil {
		s.mu.Unlock()
		s.failRun(run, "opencode_invalid_tool_lifecycle", "tool end without active start")
		return
	}
	if tool.run != run || tool.terminal {
		s.mu.Unlock()
		s.failRun(run, "opencode_invalid_tool_lifecycle", "duplicate or foreign tool end")
		return
	}
	tool.terminal = true
	tool.result = cloneRaw(content)
	s.mu.Unlock()
	payload := s.toolPayload(tool)
	payload.ArgumentsJSON = nil
	payload.Progress = nil
	if failed {
		payload.Result = nil
		payload.Error = &protocol.ProtocolError{Code: "opencode_tool_failed", Message: failure.Message}
		_, _ = s.emitEnvelope(run, protocol.TypeActionCallFailed, payload, false, tool.startedEvent)
		return
	}
	_, _ = s.emitEnvelope(run, protocol.TypeActionCallCompleted, payload, false, tool.startedEvent)
}

func toolKey(run *runState, nativeID string) string {
	return string(run.id) + "\x00" + nativeID
}

func (s *session) toolPayload(tool *toolState) protocol.ActionCallPayload {
	return protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: tool.run.id, ToolCallID: tool.id, RequestedBy: "agent", ExecutionOwner: "opencode-server", Name: tool.name, ArgumentsJSON: cloneRaw(tool.args), Progress: cloneRaw(tool.progress), Result: cloneRaw(tool.result)}
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
	// Serialize with the reducer's prompted transition. The reducer emits
	// run.started and only then records prompted, so a caller that cancels as
	// soon as it observes run.started could otherwise read prompted=false and
	// drop the cancelling status update. Holding the transition lock makes the
	// emit-and-record pair atomic to Cancel.
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
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
		status := run.status
		s.mu.Unlock()
		return protocol.RunCancelResponse{SessionID: s.state.SessionID, RunID: id, Accepted: true, Status: status}, nil
	}
	prompted := run.prompted
	run.cancelRequested = true
	run.status = protocol.RunCancelling
	s.mu.Unlock()
	if err := s.client.Interrupt(ctx, s.nativeID); err != nil {
		s.mu.Lock()
		s.unusable = true
		s.mu.Unlock()
		<-run.admitted
		s.failRun(run, "opencode_cancellation_ambiguous", err.Error())
		return protocol.RunCancelResponse{}, err
	}
	if prompted {
		// Only advertise the cancelling status once run.started exists;
		// before that the intent is recorded and settlement will cancel.
		_ = s.emit(run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: id, Status: protocol.RunCancelling, UpdatedAtMS: s.clock.Now().UnixMilli()}, false)
	}
	return protocol.RunCancelResponse{SessionID: s.state.SessionID, RunID: id, Accepted: true, Status: protocol.RunCancelling}, nil
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
	cancel := s.settleCancel
	subCancel := s.subCancel
	s.mu.Unlock()
	s.stopOnce.Do(func() { close(s.stop) })
	if cancel != nil {
		cancel()
	}
	if subCancel != nil {
		subCancel()
	}
	_ = s.subscription.Close()
	err := s.client.Close()
	for _, stream := range subscribers {
		close(stream)
	}
	return err
}

func (s *session) settleTools(run *runState, cancel bool) {
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
			payload.Error = &protocol.ProtocolError{Code: "incomplete_tool", Message: "OpenCode run settled with an unfinished tool"}
			_, _ = s.emitEnvelope(run, protocol.TypeActionCallFailed, payload, false, tool.startedEvent)
		}
	}
}

func (s *session) failRun(run *runState, code, message string) {
	s.settleTools(run, true)
	_ = s.emit(run, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: s.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: code, Message: message}}, true)
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
	err := s.subscription.Err()
	s.mu.Unlock()
	if !closed && run != nil {
		<-run.admitted
		if err == nil {
			err = io.EOF
		}
		s.failRun(run, "opencode_stream_failed", err.Error())
	}
}

// normalizeModelRef projects OpenCode's native model reference onto OAP's
// opaque model identity: provider/id when both are present, else id.
func normalizeModelRef(ref *native.ModelRef) string {
	if ref == nil || ref.ID == "" {
		return ""
	}
	if ref.ProviderID != "" {
		return ref.ProviderID + "/" + ref.ID
	}
	return ref.ID
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
	switch typ {
	case protocol.TypeActionCallRequested, protocol.TypeActionCallStarted, protocol.TypeActionCallProgress,
		protocol.TypeActionCallCompleted, protocol.TypeActionCallFailed, protocol.TypeActionCallCancelled:
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

func formatCursor(seq int64) string { return strconv.FormatInt(seq, 10) }

var _ base.Session = (*session)(nil)

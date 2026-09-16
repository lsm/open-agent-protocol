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
	// suppressed marks a native turn the server is executing that no OAP run
	// can own — an input this adapter never admitted, or a reservation it
	// cancelled before promotion. Its events reduce into nothing until the
	// next prompted event names one that is owned.
	suppressed bool
	active     *runState
	// reserved is the one queued reservation this adapter admits beside a
	// started run, matching the max_queued_runs_per_session it discloses. It
	// promotes when the server's prompted event names it and the started run
	// has settled, so one run domain executes at a time in admission order.
	reserved     *runState
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

// The admission bounds this pin discloses, and the ones Submit enforces: one
// started run beside one reservation. The descriptor publishes these same
// constants, because a bound a caller is told about and a bound the endpoint
// applies have to be the same number.
const (
	maxActiveRuns = 2
	maxQueuedRuns = 1
)

type runState struct {
	id       protocol.RunID
	status   protocol.RunStatus
	next     uint64
	terminal bool
	prompted bool
	// promotionSeen records that the server's prompted event for this
	// reservation has arrived, so the run this session reduces native events
	// into is now this one. holding says its OAP envelopes are buffered until
	// the earlier-admitted run's terminal reaches the wire, so a
	// later-admitted run never interleaves its execution. The two are
	// separate because routing has to move at the promotion while
	// publication waits: reducing the promoted turn's steps and text into the
	// run that is still finishing would attribute one run's output to
	// another and leave the promoted one unable to settle.
	promotionSeen bool
	holding       bool
	held          []heldEvent
	// publishedSeq is the highest sequence this run has actually published.
	// It trails run.next while envelopes are held, and it is what replay is
	// measured against: an envelope nobody has been shown is not history a
	// cursor can be behind.
	publishedSeq uint64
	// startPublished records that this run's run.started has reached the
	// trace. It is what the state projection reads, because until then the
	// trace knows the run only as the reservation it was admitted as — and
	// the two part company on an idle session, where the run identity exists
	// from admission but the server decides when the turn begins.
	startPublished bool
	// queuedAdmission records that the admission response said queued, which
	// is what the disclosed queue bound counts. A run admitted started is not
	// in the queue even before its start reaches the trace.
	queuedAdmission bool
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

// heldEvent is one envelope a held run has produced: sequenced and journalled
// where it happened, delivered when the earlier run's terminal has been.
type heldEvent struct {
	envelope protocol.Envelope
	terminal bool
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
	// The admission bounds are counted, not inferred from which slot happens
	// to be occupied. A run admitted queued is in the queue until its start
	// reaches the trace, whatever slot holds it, so a session whose only run
	// is an unpromoted reservation is at its disclosed queue bound and the
	// wire's answer for a second submission is run_active.
	live, queued := 0, 0
	for _, r := range []*runState{s.active, s.reserved} {
		if !published(r) {
			continue
		}
		live++
		if r.queuedAdmission && !r.startPublished {
			queued++
		}
	}
	if live >= maxActiveRuns || queued >= maxQueuedRuns || published(s.reserved) {
		s.mu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, base.ErrRunActive
	}
	behind := live > 0
	run := &runState{id: protocol.RunID(s.ids.NewID("run")), status: protocol.RunQueued, next: 1, queuedAdmission: true, messageID: protocol.MessageID(s.ids.NewID("message")), admitted: make(chan struct{})}
	stream := make(chan base.Result, streamCapacity+1)
	run.subscribers = []chan base.Result{stream}
	// Every admission is a reservation until something says otherwise: the
	// started slot is for the run whose run.started has reached the trace, and
	// on this pin that is the server's decision, taken at prompted. A run the
	// prompt response reports scheduled at once takes the slot below.
	s.reserved = run
	s.runs[run.id] = run
	s.refreshStateLocked()
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
		RequestedDelivery: requestedDelivery(req.Delivery),
		EffectiveDelivery: protocol.EffectiveDeliveryQueue,
		Admission:         protocol.AdmissionQueued,
		RunID:             run.id,
		Status:            protocol.RunQueued,
		ModelID:           model,
	}
	if behind {
		// auto resolved to a queue because the session was busy, and the
		// response says so rather than leaving the caller to infer it.
		reservation.DeliveryResolution = "session_busy"
	}
	nativeMessage := native.MessageID(s.ids.NewID("opencode-message"))
	if !nativeMessage.Valid() {
		close(run.admitted)
		s.abandon(run, "opencode_invalid_message_id", "ID generator must produce a msg_-prefixed identity for kind opencode-message")
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
		s.mu.Unlock()
		close(run.admitted)
		s.abandon(run, "opencode_admission_ambiguous", err.Error())
		return reservation, stream, nil
	}
	if admitted.ID != nativeMessage {
		s.mu.Lock()
		delete(s.pending, nativeMessage)
		s.mu.Unlock()
		close(run.admitted)
		s.abandon(run, "opencode_foreign_admission", fmt.Sprintf("server admitted %s for request %s", admitted.ID, nativeMessage))
		return reservation, stream, nil
	}
	reservation.MessageIDs = []protocol.MessageID{protocol.MessageID(admitted.ID)}
	if admitted.PromotedSeq != nil && !behind && req.Delivery != protocol.DeliveryQueue {
		// The server scheduled this input at once, which for an auto request
		// is the started shape. An explicit queue keeps the reservation shape
		// whatever the server did with it — "run after current work reaches a
		// safe boundary" is satisfied trivially on an idle session, and a
		// queue request answered `started` is an illegal transition — so its
		// run.started is published separately when prompted arrives.
		reservation.Admission = protocol.AdmissionStarted
		reservation.EffectiveDelivery = protocol.DeliveryStart
		reservation.Status = protocol.RunRunning
		s.mu.Lock()
		if !run.terminal {
			run.status = protocol.RunRunning
			run.queuedAdmission = false
			if s.reserved == run {
				// This one is the started run: it leaves the reservation slot
				// now, so a second submission behind it queues rather than
				// finding the slot taken. Its run.started still waits for the
				// server's prompted event, and the projection waits with it.
				s.reserved, s.active = nil, run
			}
			s.refreshStateLocked()
		}
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
	case protocol.DeliveryQueue:
		// The queue is a native server delivery and this adapter advertises
		// it, so an explicit request is applied rather than refused.
		return text, native.DeliveryQueue, nil
	default:
		// steer stays outside this subset until its own unit graduates, and
		// btw has no native surface at this pin.
		return "", "", fmt.Errorf("%w: delivery %q is outside the v0.1 request subset", ErrUnsupported, req.Delivery)
	}
}

// requestedDelivery is the canonical spelling of the delivery a submission
// asked for; the zero value is the accepted spelling of auto, which the
// schema's enum does not admit.
func requestedDelivery(delivery protocol.RequestedDeliveryMode) protocol.RequestedDeliveryMode {
	if delivery == "" {
		return protocol.DeliveryAuto
	}
	return delivery
}

// refreshStateLocked recomputes the published session state from the runs the
// session holds. active_runs lists every nonterminal run in admission order,
// which is what a session carrying a reservation beside a started run needs:
// active_run_id alone cannot describe two.
func (s *session) refreshStateLocked() {
	// Which run is started is read from the trace, not from the slot: a run
	// admitted queued holds its identity from admission, and on an idle
	// session the server still decides when its turn begins. Projecting the
	// slot would name a reservation in active_run_id and list it without the
	// queue position it is owed — the shape the queue unit's own state rules
	// reject.
	var entries []protocol.ActiveRun
	position := 0
	started := protocol.RunID("")
	for _, run := range []*runState{s.active, s.reserved} {
		if !published(run) {
			continue
		}
		if !reservationOf(run) {
			entries = append(entries, activeRunEntry(run, 0))
			started = run.id
			continue
		}
		position++
		entries = append(entries, activeRunEntry(run, position))
	}
	s.state.ActiveRuns = entries
	switch {
	case started != "":
		s.state.Status = protocol.SessionRunning
		s.state.ActiveRunID = started
	case len(entries) > 0:
		s.state.Status = protocol.SessionQueued
		s.state.ActiveRunID = ""
	default:
		s.state.Status = protocol.SessionIdle
		s.state.ActiveRunID = ""
	}
}

// published reports whether a run is one the state projection still lists: a
// nonterminal run, or one whose terminal is still buffered behind an earlier
// run's. A snapshot is judged against what the trace has been told, so a run
// whose settlement no subscriber has seen is still outstanding.
func published(run *runState) bool { return run != nil && (!run.terminal || run.holding) }

// reservationOf reports whether the trace still knows a run as a reservation:
// it was admitted queued and its run.started has not been published. That is
// the same test the queue unit's state rules apply, and it has to be, because
// the projection is what those rules judge. A run admitted started is not one,
// even in the window before its start reaches the trace — the admission
// already said which it was.
func reservationOf(run *runState) bool { return run.queuedAdmission && !run.startPublished }

// activeRunEntry describes one nonterminal run at the cursor it has reached.
// This adapter raises no interactions at this pin, so the pending set is
// always empty and the stated position is always one the trace has reached.
//
// A run whose start the trace has not seen is described by what it has
// published: the reservation it was admitted as, at the position it has
// reached. That covers a reservation the server has not promoted, one whose
// envelopes are held behind an earlier run, and one already cancelling before
// it ever began. Its own status may even be terminal — a promoted reservation
// can finish natively while the earlier run is still settling — and
// active_runs is a list of the session's nonterminal runs, so copying that
// status would put a settled run in it and emit a state this adapter's own
// validator rejects. It is the same principle as the queue slot and the
// journal: a snapshot is judged against what the trace has been told.
func activeRunEntry(run *runState, position int) protocol.ActiveRun {
	sequence, status := run.next-1, run.status
	if reservationOf(run) {
		sequence, status = run.publishedSeq, protocol.RunQueued
	}
	entry := protocol.ActiveRun{RunID: run.id, Status: status, Relationship: protocol.RelationshipPrimary, AsOfSequence: &sequence}
	if position > 0 {
		entry.QueuePosition = &position
	}
	return entry
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
		s.abandon(nil, "opencode_foreign_session", "durable event belongs to another session")
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
	run := s.reductionTargetLocked()
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
		var owner *runState
		switch {
		case pending == nil || pending.terminal:
			// This turn belongs to an input no OAP run can own: one this
			// adapter never admitted, or a reservation whose pre-start
			// cancellation already settled its run. The pin has no route that
			// withdraws a queued input, so the server executing one we
			// abandoned is expected rather than exceptional.
		case pending == s.reserved:
			// The server promoted the reservation, so every native event
			// after this one belongs to it. Its own envelopes are buffered
			// until the earlier run's terminal reaches the wire — publication
			// waits, reduction does not. With nothing earlier outstanding
			// there is nothing to wait for, so the promotion publishes at
			// once.
			owner = pending
			pending.promotionSeen = true
			pending.holding = s.active != nil && !s.active.terminal
		case pending == s.active:
			owner = pending
		}
		// A turn with no owner is quarantined rather than left to fall back on
		// the started run: attributing its steps and text there would give one
		// run another's output and could keep the started run from settling,
		// which is the same fault as misrouting a promoted reservation.
		s.suppressed = owner == nil
		reservation := owner != nil && owner == s.reserved
		s.mu.Unlock()
		if owner == nil {
			return
		}
		<-owner.admitted
		if err := s.emit(owner, protocol.TypeRunStarted, protocol.RunStartedPayload{SessionID: s.state.SessionID, RunID: owner.id, Status: protocol.RunRunning, ModelID: s.state.CurrentModelID, StartedAtMS: s.clock.Now().UnixMilli()}, false); err != nil {
			return
		}
		// Flag only after the started event exists so Cancel can rely on
		// prompted implying an emitted run.started. The status it reports
		// moved inside that emission, where the projection is rebuilt.
		s.mu.Lock()
		owner.prompted = true
		s.mu.Unlock()
		if reservation {
			// Already terminal upstream? Then nothing is owed a wait.
			s.promoteReserved()
		}
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
		s.abandon(run, "opencode_quiescence_failed", err.Error())
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
			s.abandon(run, "opencode_history_failed", err.Error())
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
		return s.cloneStateLocked(), base.ErrSessionClosed
	}
	return s.cloneStateLocked(), nil
}

// cloneStateLocked detaches the published snapshot from the session's own
// slices, so a consumer holding one cannot see it change underneath — and
// cannot change it. Every path that hands a SessionState to a caller goes
// through here, State and the recovery snapshot alike: a by-value copy shares
// active_runs' backing array and its pointer fields, so a caller editing what
// it was given would reach into the session's own state.
func (s *session) cloneStateLocked() protocol.SessionState {
	state := s.state
	state.ActiveRuns = append([]protocol.ActiveRun(nil), s.state.ActiveRuns...)
	for i := range state.ActiveRuns {
		entry := state.ActiveRuns[i]
		if entry.AsOfSequence != nil {
			value := *entry.AsOfSequence
			state.ActiveRuns[i].AsOfSequence = &value
		}
		if entry.QueuePosition != nil {
			value := *entry.QueuePosition
			state.ActiveRuns[i].QueuePosition = &value
		}
		if entry.PendingInteractions != nil {
			state.ActiveRuns[i].PendingInteractions = append([]protocol.InteractionID(nil), entry.PendingInteractions...)
		}
		if entry.AdmittedSubmitRequests != nil {
			state.ActiveRuns[i].AdmittedSubmitRequests = append([]protocol.EnvelopeID(nil), entry.AdmittedSubmitRequests...)
		}
	}
	return state
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
	// A reservation the server has already promoted is executing: interrupt
	// targets it, and it has a published run.started to settle against.
	reservation := run == s.reserved && !run.promotionSeen
	run.cancelRequested = true
	run.status = protocol.RunCancelling
	s.mu.Unlock()
	if reservation {
		// The pin offers no route that withdraws one queued input:
		// session interrupt targets the running execution, so sending it
		// here would cancel the wrong work. The reservation is therefore
		// dropped adapter-side and settles pre-start, and a later promotion
		// for it is ignored — the terminal has already absorbed the run.
		<-run.admitted
		_ = s.emit(run, protocol.TypeRunCancelled, protocol.RunCancelledPayload{SessionID: s.state.SessionID, RunID: id, Reason: "reservation cancelled before promotion"}, true)
		return protocol.RunCancelResponse{SessionID: s.state.SessionID, RunID: id, Accepted: true, Status: protocol.RunCancelling}, nil
	}
	if err := s.client.Interrupt(ctx, s.nativeID); err != nil {
		s.abandon(run, "opencode_cancellation_ambiguous", err.Error())
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
	// A held run has allocated sequences nobody has seen: replay is measured
	// against what it has published, so a cursor at its published position is
	// current rather than behind, and one beyond it is in the future.
	latest := run.publishedSeq
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
	recovery := base.Recovery{State: s.cloneStateLocked(), RunID: run.id, RequestedAfter: request.AfterSequence, ReplayedFrom: request.AfterSequence, ReplayedThrough: request.AfterSequence}
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
	if run.terminal && !run.holding {
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
	if published(s.active) || published(s.reserved) {
		// A reservation is admitted work that owes a terminal, and once the
		// started run settles it is the session's only nonterminal run — the
		// interval where the routing and the publication have parted company.
		// Closing there would drop an accepted submission without publishing
		// anything for it, so the close refuses exactly as it does for a
		// started run, and the caller cancels the reservation first.
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
	err := s.subscription.Err()
	s.mu.Unlock()
	if err == nil {
		err = io.EOF
	}
	s.abandon(nil, "opencode_stream_failed", err.Error())
}

// abandon settles every admitted run when the session stops being usable.
//
// A session that cannot be read from again owes a terminal on everything it
// accepted, and settling only the started run is what leaves a client stuck:
// the reservation keeps Close returning run_active while Cancel and State
// answer session_closed, so the caller can neither settle the run nor close
// the session, and the child process outlives both. Every path that marks the
// session unusable goes through here for that reason — a foreign durable
// event, a failed quiescence or history fence, an ambiguous admission or
// cancellation — not only the transport.
//
// origin, where the caller has one, is the run the failure is actually about;
// it keeps the code that names it. A reservation the failure only caught in
// passing lost its queue slot before ever being promoted, which is what
// queue_dropped names — one already promoted lost whatever the failure was,
// and saying queue_dropped for that would name the wrong thing.
func (s *session) abandon(origin *runState, code, message string) {
	s.mu.Lock()
	run, reserved := s.active, s.reserved
	closed := s.closed
	if !closed {
		s.unusable = true
	}
	s.mu.Unlock()
	if closed {
		return
	}
	if run != nil && !run.terminal {
		<-run.admitted
		s.failRun(run, code, message)
	}
	if reserved != nil && !reserved.terminal {
		<-reserved.admitted
		failCode, failMessage := code, message
		if reserved != origin && !reserved.promotionSeen {
			failCode = "queue_dropped"
			failMessage = "the reservation was dropped before promotion: " + message
		}
		s.failRun(reserved, failCode, failMessage)
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

// promoteReserved starts the reservation once the server has promoted it and
// the started run's terminal is on the wire. Until both hold, the reservation
// has published nothing, which is what the reserved identity means.
// reductionTargetLocked is the run native events reduce into: the promoted
// reservation once the server has started executing it, otherwise the started
// run. s.mu must be held.
func (s *session) reductionTargetLocked() *runState {
	if s.suppressed {
		// A quarantined turn is running on the server with no OAP run behind
		// it. Its events reduce into nothing until the next prompted event
		// names an input this adapter owns.
		return nil
	}
	if s.reserved != nil && s.reserved.promotionSeen && !s.reserved.terminal {
		return s.reserved
	}
	return s.active
}

// promoteReserved releases a promoted reservation's buffered envelopes once
// the earlier run's terminal is on the wire, and makes it the started run. It
// is a no-op until both hold: the reservation has been promoted by the server,
// and nothing earlier is still running.
func (s *session) promoteReserved() {
	s.mu.Lock()
	reserved := s.reserved
	if reserved == nil || !reserved.promotionSeen || (s.active != nil && !s.active.terminal) {
		s.mu.Unlock()
		return
	}
	s.reserved = nil
	held := reserved.held
	reserved.held, reserved.holding = nil, false
	if !reserved.terminal {
		// A run that settled while it was held keeps the status its terminal
		// gave it; one still executing is now running, which is what the
		// run.started about to reach the wire says.
		s.active = reserved
		reserved.status = protocol.RunRunning
	}
	for _, event := range held {
		s.publishLocked(reserved, event.envelope, event.terminal)
	}
	s.refreshStateLocked()
	s.mu.Unlock()
}

func (s *session) emit(run *runState, typ protocol.EnvelopeType, payload any, terminal bool) error {
	_, err := s.emitEnvelope(run, typ, payload, terminal, "")
	if err != nil {
		return err
	}
	if terminal {
		// A terminal frees the started slot, so a reservation the server has
		// already promoted starts now — after the terminal it follows.
		s.promoteReserved()
	}
	return nil
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
	s.state.UpdatedAtMS = now
	if typ == protocol.TypeRunStarted && !run.holding {
		// The status moves with the envelope that reports it, inside the same
		// critical section that rebuilds the projection and delivers it: a
		// State read between a published run.started and the next envelope
		// would otherwise describe a run the trace has seen start as still
		// queued. A held start has told the trace nothing yet, and moves the
		// status where it is released.
		run.status = protocol.RunRunning
	}
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
		if s.reserved == run && !run.holding {
			// A held run keeps its reservation slot until its buffer is
			// released: the trace has not seen its terminal, so a state read
			// that dropped it would report a run gone that no subscriber has
			// been told about.
			s.reserved = nil
		}
	}
	if run.holding {
		// Held means held from everyone: a journalled envelope is replayable,
		// and a caller resuming the reserved run would read its start before
		// the earlier run's terminal and then be handed it a second time when
		// the buffer is released. It enters the journal where it is published.
		run.held = append(run.held, heldEvent{envelope: event, terminal: terminal})
		s.refreshStateLocked()
		return event, nil
	}
	s.publishLocked(run, event, terminal)
	// The projection is rebuilt after the publication it describes, because
	// what it describes is what the trace has now been told.
	s.refreshStateLocked()
	return event, nil
}

// publishLocked makes one envelope history: it enters the replay journal,
// advances the run's published position, and reaches the run's subscribers.
// s.mu must be held.
func (s *session) publishLocked(run *runState, event protocol.Envelope, terminal bool) {
	if event.Type == protocol.TypeRunStarted {
		run.startPublished = true
	}
	s.journal = append(s.journal, event)
	if len(s.journal) > s.capacity {
		s.journal = append([]protocol.Envelope(nil), s.journal[len(s.journal)-s.capacity:]...)
	}
	if event.Sequence != nil && *event.Sequence > run.publishedSeq {
		run.publishedSeq = *event.Sequence
	}
	s.deliverLocked(run, event, terminal)
}

// deliverLocked hands one envelope to the run's subscribers and closes them on
// a terminal. s.mu must be held; the sends are to buffered channels and the
// overflow branch is what keeps a slow consumer from blocking the reducer.
func (s *session) deliverLocked(run *runState, event protocol.Envelope, terminal bool) {
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

package opencode

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

var errTerminalWon = errors.New("opencode adapter: terminal already selected")

func tokenCount(value float64) uint64 {
	if !(value > 0) {
		return 0
	}
	if value >= 18446744073709551616.0 {
		return math.MaxUint64
	}
	return uint64(value)
}

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
	timeout      time.Duration
	ids          base.IDGenerator
	capacity     int
	nativeID     native.SessionID
	model        *native.ModelRef
	participant  protocol.ParticipantID
	state        protocol.SessionState
	closed       bool
	unusable     bool

	suppressed bool
	active     *runState

	reserved *runState

	nextToolOrder uint64

	settled   []protocol.SettledRun
	runs      map[protocol.RunID]*runState
	pending   map[native.MessageID]*runState
	tools     map[string]*toolState
	gates     map[string]*permissionGate
	toolNames map[string]string
	reduced   map[int64]bool

	reconciled map[native.MessageID]bool
	replaying  bool

	models    []string
	journal   []protocol.Envelope
	lastSeq   int64
	stop      chan struct{}
	stopOnce  sync.Once
	subCancel context.CancelFunc
}

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

	promotionSeen bool

	publishedSeq uint64

	startPublished bool

	queuedAdmission bool

	answered        bool
	cancelRequested bool
	settling        bool
	messageID       protocol.MessageID
	nativeMessageID native.MessageID
	parts           []protocol.ContentPart
	streamed        map[partKey]*strings.Builder
	endedParts      [2]map[partKey]bool
	steps           [2]map[native.MessageID]bool
	toolSeen        [2]map[toolSight]bool
	usage           protocol.Usage
	cost            float64
	lastFinish      string
	failure         *native.UnknownErrorBlock
	declined        bool
	openSteps       int
	admitted        chan struct{}
	subscribers     []chan base.Result
}

type partKey struct {
	message   native.MessageID
	ordinal   int
	reasoning bool
}

type permissionGate struct {
	id        protocol.InteractionID
	native    string
	run       *runState
	tool      *toolState
	requested protocol.EnvelopeID
	order     uint64
	resolved  bool
	settling  bool
	open      bool
}

type permissionReplier interface {
	ReplyPermission(ctx context.Context, session native.SessionID, request string, reply native.PermissionReply) error
}

var permissionChoices = []protocol.PermissionChoice{
	{ID: "once", Label: "Allow once", Description: "allow this call"},
	{ID: "always", Label: "Always allow", Description: "allow it and save the rule OpenCode offers"},
	{ID: "reject", Label: "Reject", Description: "decline the call; without a reason OpenCode ends the execution"},
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
	order          uint64
	requestedEvent protocol.EnvelopeID
	startedEvent   protocol.EnvelopeID
}

func (s *session) Submit(ctx context.Context, submit base.SubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	req := submit.Request
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

	s.reserved = run
	s.runs[run.id] = run
	s.refreshStateLocked()

	model := s.state.CurrentModelID
	s.state.UpdatedAtMS = s.clock.Now().UnixMilli()
	s.mu.Unlock()

	reservation := protocol.MessageSubmitResponse{
		SessionID:    s.state.SessionID,
		Accepted:     true,
		SubmissionID: protocol.SubmissionID(s.ids.NewID("submission")),

		RequestedDelivery: requestedDelivery(req.Delivery),
		EffectiveDelivery: protocol.EffectiveDeliveryQueue,
		Admission:         protocol.AdmissionQueued,
		RunID:             run.id,
		Status:            protocol.RunQueued,
		ModelID:           model,
	}
	if behind {

		reservation.DeliveryResolution = "session_busy"
	}
	nativeMessage := native.MessageID(s.ids.NewID("opencode-message"))
	if !nativeMessage.Valid() {
		s.answerRun(run)
		s.abandon(run, "opencode_invalid_message_id", "ID generator must produce a msg_-prefixed identity for kind opencode-message", protocol.SettledByInferred)
		return reservation, stream, nil
	}
	run.nativeMessageID = nativeMessage

	s.mu.Lock()
	s.pending[nativeMessage] = run
	s.mu.Unlock()
	if behind {
		delivery = native.DeliveryQueue
	}
	admitted, err := s.client.Prompt(ctx, s.nativeID, native.PromptRequest{ID: nativeMessage, Text: promptText, Delivery: delivery})
	if err != nil {
		s.mu.Lock()
		delete(s.pending, nativeMessage)
		s.mu.Unlock()
		s.answerRun(run)
		s.abandon(run, "opencode_admission_ambiguous", err.Error(), settledByFor(err))
		return reservation, stream, nil
	}
	if admitted.ID != nativeMessage {
		s.mu.Lock()
		delete(s.pending, nativeMessage)
		s.mu.Unlock()
		s.answerRun(run)
		s.abandon(run, "opencode_foreign_admission", fmt.Sprintf("server admitted %s for request %s", admitted.ID, nativeMessage), "")
		return reservation, stream, nil
	}
	reservation.MessageIDs = []protocol.MessageID{protocol.MessageID(admitted.ID)}
	if !behind && req.Delivery != protocol.DeliveryQueue {
		reservation.Admission = protocol.AdmissionStarted
		reservation.EffectiveDelivery = protocol.DeliveryStart
		reservation.Status = protocol.RunRunning
		s.mu.Lock()
		if !run.terminal {
			run.status = protocol.RunRunning
			run.queuedAdmission = false
			if s.reserved == run {
				s.reserved, s.active = nil, run
			}
			s.refreshStateLocked()
		}
		s.mu.Unlock()
	}
	s.answerRun(run)
	return reservation, stream, nil
}

func (s *session) answerRun(run *runState) {
	s.mu.Lock()
	run.answered = true
	s.refreshStateLocked()
	s.mu.Unlock()
	close(run.admitted)
}

func (s *session) submitInput(req protocol.MessageSubmitRequest) (string, native.Delivery, error) {

	if err := base.RefuseUnadvertisedControls(req, protocol.FeatureDeliveryQueue); err != nil {
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

		return text, native.DeliverySteer, nil
	case protocol.DeliveryQueue:

		return text, native.DeliveryQueue, nil
	default:

		return "", "", fmt.Errorf("%w: delivery %q is outside the v0.1 request subset", ErrUnsupported, req.Delivery)
	}
}

func requestedDelivery(delivery protocol.RequestedDeliveryMode) protocol.RequestedDeliveryMode {
	if delivery == "" {
		return protocol.DeliveryAuto
	}
	return delivery
}

func (s *session) refreshStateLocked() {

	var entries []protocol.ActiveRun
	position := 0
	started := protocol.RunID("")
	waiting := false
	for _, run := range []*runState{s.active, s.reserved} {
		if !published(run) || !run.answered {
			continue
		}
		if !reservationOf(run) {
			entry := s.activeRunEntry(run, 0)
			entries = append(entries, entry)
			started, waiting = run.id, len(entry.PendingInteractions) > 0
			continue
		}
		position++
		entries = append(entries, s.activeRunEntry(run, position))
	}
	s.state.ActiveRuns = entries

	if len(s.settled) > 0 {
		s.state.AsOf = &protocol.SessionCapture{Settled: s.settled}
	}
	switch {
	case started != "":
		s.state.Status = protocol.SessionRunning
		if waiting {
			s.state.Status = protocol.SessionWaitingForInput
		}
		s.state.ActiveRunID = started
	case len(entries) > 0:
		s.state.Status = protocol.SessionQueued
		s.state.ActiveRunID = ""
	default:
		s.state.Status = protocol.SessionIdle
		s.state.ActiveRunID = ""
	}
}

func (s *session) UpdateSettings(ctx context.Context, req protocol.SessionSettingsUpdateRequest) (protocol.SessionSettingsUpdateResponse, protocol.SessionState, error) {
	if err := ctx.Err(); err != nil {
		return protocol.SessionSettingsUpdateResponse{}, protocol.SessionState{}, err
	}
	if err := base.RefuseUnadvertisedLiveSettings(req, protocol.CapabilityDescriptor{Features: advertisedFeatures()}); err != nil {
		return protocol.SessionSettingsUpdateResponse{}, protocol.SessionState{}, err
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	switch {
	case s.closed || s.unusable:
		s.mu.Unlock()
		return protocol.SessionSettingsUpdateResponse{}, protocol.SessionState{}, base.ErrSessionClosed
	case req.SessionID != s.state.SessionID:
		s.mu.Unlock()
		return protocol.SessionSettingsUpdateResponse{}, protocol.SessionState{}, base.ErrRunNotFound
	case published(s.active) || published(s.reserved):
		s.mu.Unlock()
		return protocol.SessionSettingsUpdateResponse{}, protocol.SessionState{}, base.ErrRunActive
	}
	current := s.model
	s.mu.Unlock()
	if req.ReasoningLevel != "" {
		if current == nil {
			return protocol.SessionSettingsUpdateResponse{}, protocol.SessionState{}, &base.UnsupportedControlError{Feature: protocol.FeatureSessionReasoning, Reason: base.ControlUnsatisfiable, Field: "reasoning_level", Detail: "a variant rides on a model, and OpenCode recorded none on this session"}
		}
		next := native.ModelRef{ID: current.ID, ProviderID: current.ProviderID, Variant: string(req.ReasoningLevel)}
		if err := s.client.SwitchModel(ctx, s.nativeID, next); err != nil {
			return protocol.SessionSettingsUpdateResponse{}, protocol.SessionState{}, fmt.Errorf("switch OpenCode session variant: %w", err)
		}
		info, err := s.client.Session(ctx, s.nativeID)
		if err != nil {
			return protocol.SessionSettingsUpdateResponse{}, protocol.SessionState{}, fmt.Errorf("read OpenCode session: %w", err)
		}
		if info.Model == nil || info.Model.ID != next.ID || info.Model.ProviderID != next.ProviderID || info.Model.Variant != next.Variant {
			return protocol.SessionSettingsUpdateResponse{}, protocol.SessionState{}, &base.UnsupportedControlError{Feature: protocol.FeatureSessionReasoning, Reason: base.ControlUnsatisfiable, Field: "reasoning_level", Detail: "OpenCode did not record the variant on the session"}
		}
		s.mu.Lock()
		s.model = info.Model
		s.mu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	response := protocol.SessionSettingsUpdateResponse{SessionID: s.state.SessionID}
	if req.ReasoningLevel != "" {
		response.PreviousReasoningLevel = s.state.ReasoningLevel
		s.state.ReasoningLevel = req.ReasoningLevel
		response.ReasoningLevel = req.ReasoningLevel
	}
	s.state.UpdatedAtMS = s.clock.Now().UnixMilli()
	return response, s.state, nil
}

func (s *session) openGates() []protocol.InteractionID {
	var open []*permissionGate
	for _, gate := range s.gates {
		if gate.open {
			open = append(open, gate)
		}
	}
	slices.SortFunc(open, func(a, b *permissionGate) int { return cmp.Compare(a.order, b.order) })
	var ids []protocol.InteractionID
	for _, gate := range open {
		ids = append(ids, gate.id)
	}
	return ids
}

func published(run *runState) bool { return run != nil && !run.terminal }

func reservationOf(run *runState) bool { return run.queuedAdmission && !run.startPublished }

func (s *session) activeRunEntry(run *runState, position int) protocol.ActiveRun {
	sequence, status := run.next-1, run.status
	if reservationOf(run) {
		sequence, status = run.publishedSeq, protocol.RunQueued
	}
	entry := protocol.ActiveRun{RunID: run.id, Status: status, Relationship: protocol.RelationshipPrimary, AsOfSequence: &sequence}
	if position == 0 {
		entry.PendingInteractions = s.openGates()
	}
	if position > 0 {
		entry.QueuePosition = &position
	}
	return entry
}

func (s *session) dispatch() {
	for {
		s.mu.Lock()
		events, done := s.events, s.subscription.Done()
		s.mu.Unlock()
		select {
		case event := <-events:
			s.transitionMu.Lock()
			s.handleEventLocked(event)
			s.transitionMu.Unlock()
		case <-done:
			s.drain(events)
			if s.recover() {
				continue
			}
			s.transportFailed()
			return
		case <-s.stop:
			return
		}
	}
}

func (s *session) drain(events <-chan native.Event) {
	for {
		select {
		case event := <-events:
			s.transitionMu.Lock()
			s.handleEventLocked(event)
			s.transitionMu.Unlock()
		default:
			return
		}
	}
}

func (s *session) handleEventLocked(event native.Event) {
	if event.SessionID != s.nativeID {
		s.abandon(nil, "opencode_foreign_session", "event belongs to another session", "")
		return
	}
	s.mu.Lock()
	if event.Durable != nil {
		if s.reduced[event.Durable.Seq] {
			s.mu.Unlock()
			return
		}
		s.reduced[event.Durable.Seq] = true
		if event.Durable.Seq > s.lastSeq {
			s.lastSeq = event.Durable.Seq
			s.state.TranscriptCursor = formatSeq(s.lastSeq)
		}
	}
	run := s.reductionTargetLocked()
	s.mu.Unlock()
	switch event.Type {
	case native.TypeInboxEnqueued:
		var data native.InboxEnqueuedData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_inbox_event", err.Error())
		}
	case native.TypeInboxDelivered:
		var data native.InboxRefData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_inbox_event", err.Error())
			return
		}
		s.mu.Lock()
		repeated := s.reconciled[data.InboxID]
		if s.replaying {
			if s.reconciled == nil {
				s.reconciled = map[native.MessageID]bool{}
			}
			s.reconciled[data.InboxID] = true
		}
		s.mu.Unlock()
		if !repeated {
			s.delivered(data.InboxID)
		}
	case native.TypeInboxCancelled:
		var data native.InboxRefData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_inbox_event", err.Error())
			return
		}
		s.mu.Lock()
		pending := s.pending[data.InboxID]
		delete(s.pending, data.InboxID)
		s.mu.Unlock()
		if pending == nil || pending.terminal {
			return
		}
		<-pending.admitted
		_ = s.emitWith(pending, protocol.TypeRunCancelled, protocol.RunCancelledPayload{SessionID: s.state.SessionID, RunID: pending.id, Reason: "OpenCode cancelled the input before delivering it"}, true, s.reportedRunCost(pending))
	case native.TypeInboxDeliveryChanged:
		var data native.InboxDeliveryChangedData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_inbox_event", err.Error())
		}
	case native.TypeExecutionStarted:
		var data native.ExecutionData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_execution_event", err.Error())
		}
	case native.TypeExecutionSucceeded:
		var data native.ExecutionData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_execution_event", err.Error())
			return
		}
		if run != nil && run.prompted {
			<-run.admitted
			s.settleRunLocked(run)
		}
	case native.TypeExecutionFailed:
		var data native.ExecutionFailedData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_execution_event", err.Error())
			return
		}
		if run != nil && run.prompted {
			<-run.admitted
			s.mu.Lock()
			stepFailed := run.failure != nil
			s.mu.Unlock()
			if stepFailed {
				s.settleRunLocked(run)
				return
			}
			s.failRun(run, "opencode_execution_failed", data.Error.Message)
		}
	case native.TypeExecutionInterrupted:
		var data native.ExecutionInterruptedData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_execution_event", err.Error())
			return
		}
		if run == nil || !run.prompted {
			return
		}
		<-run.admitted
		s.mu.Lock()
		cancelled := run.cancelRequested
		s.mu.Unlock()
		if cancelled {
			s.settleTools(run, true)
			_ = s.emitWith(run, protocol.TypeRunCancelled, protocol.RunCancelledPayload{SessionID: s.state.SessionID, RunID: run.id, Reason: "OpenCode interrupted the execution"}, true, s.reportedRunCost(run))
			return
		}
		s.mu.Lock()
		declined := run.declined
		s.mu.Unlock()
		if declined {
			s.failRun(run, "opencode_permission_declined", "a declined tool call ended OpenCode's execution")
			return
		}
		s.failRun(run, "opencode_execution_interrupted", "OpenCode interrupted the execution: "+data.Reason)
	case native.TypePermissionAsked:
		var data native.PermissionAskedData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_permission_event", err.Error())
			return
		}
		if run == nil {
			return
		}
		<-run.admitted
		s.askPermission(run, data)
	case native.TypePermissionReplied:
		var data native.PermissionRepliedData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_permission_event", err.Error())
			return
		}
		s.repliedElsewhere(data)
	case native.TypeStepStarted:
		var data native.StepStartedData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_step_event", err.Error())
			return
		}
		s.observeModel(normalizeModelRef(&data.Model))
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
		if !sighted(&run.steps, data.AssistantMessage, s.replaying) {
			s.mu.Unlock()
			return
		}
		if !run.terminal {
			run.lastFinish = data.Finish
			run.cost += data.Cost
			run.usage.InputTokens += tokenCount(data.Tokens.Input)
			run.usage.OutputTokens += tokenCount(data.Tokens.Output)
			run.usage.TotalTokens += tokenCount(data.Tokens.Input) + tokenCount(data.Tokens.Output)
		}
		s.mu.Unlock()
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
		if !sighted(&run.steps, data.AssistantMessage, s.replaying) {
			s.mu.Unlock()
			return
		}
		if !run.terminal && !run.cancelRequested {
			failure := data.Error
			run.failure = &failure
		}
		if data.Cost != nil {
			run.cost += *data.Cost
		}
		s.mu.Unlock()
	case native.TypeTextDelta, native.TypeReasoningDelta:
		var data native.PartDeltaData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_text_event", err.Error())
			return
		}
		if run == nil || data.Delta == "" {
			return
		}
		<-run.admitted
		key := partKey{message: data.AssistantMessage, ordinal: data.Ordinal, reasoning: event.Type == native.TypeReasoningDelta}
		s.mu.Lock()
		terminal := run.terminal
		if !terminal {
			if run.streamed == nil {
				run.streamed = map[partKey]*strings.Builder{}
			}
			if run.streamed[key] == nil {
				run.streamed[key] = &strings.Builder{}
			}
			run.streamed[key].WriteString(data.Delta)
		}
		s.mu.Unlock()
		if terminal {
			return
		}
		_ = s.emit(run, protocol.TypeContentDelta, protocol.ContentDeltaPayload{SessionID: s.state.SessionID, RunID: run.id, MessageID: run.messageID, Part: textPart(key.reasoning, data.Delta)}, false)
	case native.TypeTextEnded, native.TypeReasoningEnded:
		var data native.PartEndedData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_text_event", err.Error())
			return
		}
		if run == nil {
			return
		}
		<-run.admitted
		key := partKey{message: data.AssistantMessage, ordinal: data.Ordinal, reasoning: event.Type == native.TypeReasoningEnded}
		s.mu.Lock()
		if !sighted(&run.endedParts, key, s.replaying) {
			s.mu.Unlock()
			return
		}
		terminal := run.terminal
		rest := data.Text
		if !terminal {
			run.parts = append(run.parts, textPart(key.reasoning, data.Text))
			if streamed := run.streamed[key]; streamed != nil {
				var whole bool
				if rest, whole = strings.CutPrefix(data.Text, streamed.String()); !whole {
					rest = ""
				}
				delete(run.streamed, key)
			}
		}
		s.mu.Unlock()
		if terminal || rest == "" {
			return
		}
		_ = s.emit(run, protocol.TypeContentDelta, protocol.ContentDeltaPayload{SessionID: s.state.SessionID, RunID: run.id, MessageID: run.messageID, Part: textPart(key.reasoning, rest)}, false)
	case native.TypeToolInputStarted:
		var data native.ToolInputStartedData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_tool_event", err.Error())
			return
		}
		s.mu.Lock()
		s.toolNames[data.ID] = data.Name
		s.mu.Unlock()
	case native.TypeToolCalled:
		var data native.ToolCalledData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_tool_event", err.Error())
			return
		}
		s.mu.Lock()
		name := s.toolNames[data.ID]
		s.mu.Unlock()
		if run == nil || data.ID == "" || name == "" {
			return
		}
		<-run.admitted
		s.mu.Lock()
		fresh := sighted(&run.toolSeen, toolSight{id: data.ID}, s.replaying)
		s.mu.Unlock()
		if !fresh {
			return
		}
		args, err := json.Marshal(data.Input)
		if err != nil {
			s.failRun(run, "opencode_invalid_tool_arguments", err.Error())
			return
		}
		s.startTool(run, data.ID, name, args)
	case native.TypeToolProgress:
		var data native.ToolProgressData
		if err := native.DecodeData(event, &data); err != nil {
			s.failActive(run, "opencode_invalid_tool_event", err.Error())
			return
		}
		if run == nil {
			return
		}
		s.mu.Lock()
		ended := run.toolSeen[1][toolSight{id: data.ID, end: true}]
		s.mu.Unlock()
		if ended {
			return
		}
		s.updateTool(run, data.ID, data.Metadata)
	case native.TypeToolSuccess, native.TypeToolFailed:
		failed := event.Type == native.TypeToolFailed
		var failure native.SessionError
		var content json.RawMessage
		var callID string
		if failed {
			var data native.ToolFailedData
			if err := native.DecodeData(event, &data); err != nil {
				s.failActive(run, "opencode_invalid_tool_event", err.Error())
				return
			}
			failure, callID = data.Error, data.ID
			if raw, err := json.Marshal(data.Error); err == nil {
				content = raw
			}
		} else {
			var data native.ToolSuccessData
			if err := native.DecodeData(event, &data); err != nil {
				s.failActive(run, "opencode_invalid_tool_event", err.Error())
				return
			}
			callID = data.ID
			if raw, err := json.Marshal(data.Content); err == nil {
				content = raw
			}
		}
		if run == nil {
			return
		}
		s.mu.Lock()
		fresh := sighted(&run.toolSeen, toolSight{id: callID, end: true}, s.replaying)
		s.mu.Unlock()
		if !fresh {
			return
		}
		s.endTool(run, callID, failed, failure, content)
	default:
		if !event.Type.Supported() {
			s.failActive(run, "opencode_unknown_event", fmt.Sprintf("unknown event %q", event.Type))
		}
	}
}

type toolSight struct {
	id  string
	end bool
}

func sighted[K comparable](seen *[2]map[K]bool, key K, replaying bool) bool {
	own, other := 0, 1
	if replaying {
		own, other = 1, 0
	}
	if seen[other][key] || replaying && seen[own][key] {
		return false
	}
	if seen[own] == nil {
		seen[own] = map[K]bool{}
	}
	seen[own][key] = true
	return true
}

func textPart(reasoning bool, text string) protocol.ContentPart {
	if reasoning {
		return protocol.ContentPart{Type: protocol.ContentReasoning, Reasoning: text}
	}
	return protocol.ContentPart{Type: protocol.ContentText, Text: text}
}

func (s *session) delivered(inbox native.MessageID) {
	s.mu.Lock()
	pending := s.pending[inbox]
	delete(s.pending, inbox)
	var owner, previous *runState
	switch {
	case pending == nil || pending.terminal:
	case pending == s.reserved:
		owner = pending
		pending.promotionSeen = true
		if s.active != nil && !s.active.terminal && s.active != pending {
			previous = s.active
		}
	case pending == s.active:
		owner = pending
	}
	s.suppressed = owner == nil
	if owner == nil && s.active != nil && !s.active.terminal && s.active.prompted {
		previous = s.active
	}
	s.mu.Unlock()
	if owner == nil {
		if previous != nil {
			<-previous.admitted
			s.settleRunLocked(previous)
		}
		return
	}
	if previous != nil {
		<-previous.admitted
		s.settleRunLocked(previous)
	}
	<-owner.admitted
	s.promoteReserved()
	if err := s.emit(owner, protocol.TypeRunStarted, protocol.RunStartedPayload{SessionID: s.state.SessionID, RunID: owner.id, Status: protocol.RunRunning, ModelID: s.state.CurrentModelID, StartedAtMS: s.clock.Now().UnixMilli()}, false); err != nil {
		return
	}
	s.mu.Lock()
	owner.prompted = true
	late := owner.cancelRequested
	s.mu.Unlock()
	if late {
		ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
		_, err := s.client.Interrupt(ctx, s.nativeID)
		cancel()
		if err != nil {
			s.abandon(owner, "opencode_cancellation_ambiguous", err.Error(), settledByFor(err))
		}
	}
}

func (s *session) failActive(run *runState, code, message string) {
	if run == nil {
		return
	}
	<-run.admitted
	s.failRun(run, code, message)
}

func (s *session) settleRunLocked(run *runState) {
	s.mu.Lock()
	cancelRequested := run.cancelRequested
	failure := run.failure
	finish := run.lastFinish
	parts := append([]protocol.ContentPart(nil), run.parts...)
	usage := run.usage
	reported := reportedCost(run.cost)
	s.mu.Unlock()
	s.settleTools(run, cancelRequested)
	if cancelRequested {
		_ = s.emitWith(run, protocol.TypeRunCancelled, protocol.RunCancelledPayload{SessionID: s.state.SessionID, RunID: run.id, Reason: "OpenCode interrupt confirmed idle"}, true, reported)
		return
	}
	if failure != nil {
		s.failRun(run, "opencode_step_failed", failure.Message)
		return
	}
	if finish == "" {
		finish = "unknown"
	}
	content := protocol.MessageContent(protocol.TextContent(""))
	if len(parts) > 0 {
		content = protocol.PartsContent(parts)
	}
	_ = s.emitWith(run, protocol.TypeRunCompleted, protocol.RunCompletedPayload{
		SessionID:     s.state.SessionID,
		RunID:         run.id,
		FinalResponse: protocol.Message{ID: run.messageID, Role: protocol.RoleAssistant, Content: content},
		StopReason:    finish,
		Usage:         &usage,
	}, true, reported)
}

const costExtension = "io.github.anomalyco.opencode.cost"

func reportedCost(total float64) map[string]json.RawMessage {
	value, err := json.Marshal(map[string]float64{"total_cost_usd": total})
	if err != nil {
		return nil
	}
	return map[string]json.RawMessage{costExtension: value}
}

func (s *session) startTool(run *runState, nativeID, name string, args json.RawMessage) {
	s.mu.Lock()
	key := toolKey(run, nativeID)
	tool := s.tools[key]
	if tool == nil {
		tool = &toolState{id: protocol.ToolCallID(s.ids.NewID("tool-call")), run: run, name: name, args: cloneRaw(args), order: s.nextToolOrder}
		s.nextToolOrder++
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

func (s *session) endTool(run *runState, callID string, failed bool, failure native.SessionError, content json.RawMessage) {
	s.mu.Lock()
	tool := s.tools[toolKey(run, callID)]
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
	return protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: tool.run.id, ToolCallID: tool.id, RequestedBy: endpointID, ExecutionOwner: "opencode-server", Name: tool.name, ArgumentsJSON: cloneRaw(tool.args), Progress: cloneRaw(tool.progress), Result: cloneRaw(tool.result)}
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
	if s.state.AsOf != nil {
		capture := *s.state.AsOf
		capture.Settled = append([]protocol.SettledRun(nil), s.state.AsOf.Settled...)
		state.AsOf = &capture
	}
	return state
}

func (s *session) observeModel(model string) {
	if model == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, seen := range s.models {
		if seen == model {
			return
		}
	}
	s.models = append(s.models, model)
}

func (s *session) Models(ctx context.Context, request protocol.ModelsRequest) (base.Catalog, error) {
	if err := ctx.Err(); err != nil {
		return base.Catalog{}, err
	}
	if !request.AllowsDegraded(protocol.FeatureModelsList) {
		return base.Catalog{}, &base.DegradedControlError{Feature: protocol.FeatureModelsList}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.unusable {
		return base.Catalog{}, base.ErrSessionClosed
	}
	if request.SessionID != "" && request.SessionID != s.state.SessionID {
		return base.Catalog{}, base.ErrRunNotFound
	}

	catalog := protocol.ModelsResponse{
		SessionID:      s.state.SessionID,
		CurrentModelID: s.state.CurrentModelID,
		Models:         []protocol.ModelDescriptor{},
	}
	for _, model := range s.models {
		descriptor := protocol.ModelDescriptor{ID: model, Default: model == s.state.CurrentModelID}
		if provider, _, found := strings.Cut(model, "/"); found {

			descriptor.ProviderID = provider
		}
		catalog.Models = append(catalog.Models, descriptor)
	}

	return base.Catalog{Revision: CapabilityRevision, Models: catalog}, nil
}

func (s *session) askPermission(run *runState, data native.PermissionAskedData) {
	s.mu.Lock()
	if run.terminal || !run.prompted || data.ID == "" || s.gates[data.ID] != nil {
		s.mu.Unlock()
		return
	}
	var tool *toolState
	if data.Source != nil {
		tool = s.tools[toolKey(run, data.Source.ID)]
	}
	if tool == nil || tool.terminal {
		s.mu.Unlock()
		s.failRun(run, "opencode_permission_without_tool", "OpenCode asked permission for "+permissionTitle(data)+" outside a tool call this run started")
		return
	}
	gate := &permissionGate{id: protocol.InteractionID(s.ids.NewID("interaction")), native: data.ID, run: run, tool: tool, order: s.nextToolOrder}
	s.nextToolOrder++
	if s.gates == nil {
		s.gates = map[string]*permissionGate{}
	}
	s.gates[data.ID] = gate
	s.mu.Unlock()
	payload := protocol.PermissionRequestedPayload{InteractionID: gate.id, RequestedBy: endpointID, RespondedBy: s.participant, SessionID: s.state.SessionID, RunID: run.id, ToolCallID: tool.id, Title: permissionTitle(data), Description: data.Message, Choices: permissionChoices, ArgumentsJSON: cloneRaw(tool.args)}
	requested, err := s.emitEnvelope(run, protocol.TypeActionPermissionRequested, payload, false, "")
	if err != nil {
		return
	}
	s.mu.Lock()
	gate.requested = requested.ID
	s.mu.Unlock()
}

func permissionTitle(data native.PermissionAskedData) string {
	if len(data.Resources) == 0 {
		return data.Action
	}
	return data.Action + ": " + strings.Join(data.Resources, ", ")
}

func (s *session) repliedElsewhere(data native.PermissionRepliedData) {
	s.mu.Lock()
	gate := s.gates[data.RequestID]
	if gate == nil || gate.resolved || gate.settling || gate.run.terminal {
		s.mu.Unlock()
		return
	}
	gate.resolved = true
	s.mu.Unlock()
	reason := protocol.ProtocolError{Code: "opencode_permission_replied_elsewhere", Message: "OpenCode recorded the reply \"" + data.Reply + "\" from outside this session"}
	_, _ = s.emitEnvelope(gate.run, protocol.TypeActionPermissionResolved, s.gateResolution(gate, protocol.InteractionCancelled, "", nil, &reason), false, gate.requested)
}

func (s *session) gateResolution(gate *permissionGate, outcome protocol.InteractionOutcome, choice string, granted *bool, reason *protocol.ProtocolError) protocol.PermissionResolvedPayload {
	return protocol.PermissionResolvedPayload{InteractionID: gate.id, RequestedBy: endpointID, RespondedBy: s.participant, SessionID: s.state.SessionID, RunID: gate.run.id, ToolCallID: gate.tool.id, Outcome: outcome, ChoiceID: choice, Granted: granted, Reason: reason}
}

func (s *session) settleGates(run *runState) {
	s.mu.Lock()
	var open []*permissionGate
	for _, gate := range s.gates {
		if gate.run == run && !gate.resolved {
			gate.resolved = true
			open = append(open, gate)
		}
	}
	s.mu.Unlock()
	sort.Slice(open, func(i, j int) bool { return open[i].order < open[j].order })
	for _, gate := range open {
		reason := protocol.ProtocolError{Code: "run_settled", Message: "the run ended before the permission was answered"}
		_, _ = s.emitEnvelope(run, protocol.TypeActionPermissionResolved, s.gateResolution(gate, protocol.InteractionCancelled, "", nil, &reason), false, gate.requested)
	}
}

func (s *session) Resolve(ctx context.Context, resolution base.InteractionResolution) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if resolution.Permission == nil || resolution.Input != nil {
		return base.ErrInteractionNotFound
	}
	replier, ok := s.client.(permissionReplier)
	if !ok {
		return base.ErrInteractionNotFound
	}
	answer := resolution.Permission
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	s.mu.Lock()
	var gate *permissionGate
	for _, candidate := range s.gates {
		if candidate.id == answer.InteractionID {
			gate = candidate
		}
	}
	switch {
	case s.closed:
		s.mu.Unlock()
		return base.ErrSessionClosed
	case gate == nil || gate.run.terminal:
		s.mu.Unlock()
		return base.ErrInteractionNotFound
	case gate.resolved || gate.settling:
		s.mu.Unlock()
		return base.ErrInteractionResolved
	}
	run := gate.run
	if (resolution.RunID != "" && resolution.RunID != run.id) || answer.RunID != run.id || answer.SessionID != s.state.SessionID || (answer.RequestedBy != "" && answer.RequestedBy != endpointID) {
		s.mu.Unlock()
		return base.ErrInvalidResolution
	}
	if (resolution.RespondedBy != "" && resolution.RespondedBy != s.participant) || answer.RespondedBy != s.participant {
		s.mu.Unlock()
		return base.ErrWrongResponder
	}
	granted := answer.ChoiceID == "once" || answer.ChoiceID == "always"
	if (!granted && answer.ChoiceID != "reject") || granted != answer.Granted {
		s.mu.Unlock()
		return base.ErrInvalidResolution
	}
	gate.settling = true
	s.mu.Unlock()
	err := replier.ReplyPermission(ctx, s.nativeID, gate.native, native.PermissionReply{Decision: answer.ChoiceID, Message: answer.Reason})
	s.mu.Lock()
	gate.settling = false
	if err != nil || run.terminal {
		s.mu.Unlock()
		if err != nil {
			return err
		}
		return base.ErrInteractionNotFound
	}
	gate.resolved = true
	if !granted && answer.Reason == "" {
		run.declined = true
	}
	s.mu.Unlock()
	outcome := protocol.InteractionRejected
	if granted {
		outcome = protocol.InteractionResolved
	}
	_, err = s.emitEnvelope(run, protocol.TypeActionPermissionResolved, s.gateResolution(gate, outcome, answer.ChoiceID, &granted, nil), false, gate.requested)
	return err
}

func (s *session) Cancel(ctx context.Context, id protocol.RunID) (protocol.RunCancelResponse, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return protocol.RunCancelResponse{}, err
	}

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

	reservation := !prompted && !run.promotionSeen
	run.cancelRequested = true
	run.status = protocol.RunCancelling
	s.mu.Unlock()
	if reservation {
		<-run.admitted
		if run.nativeMessageID != "" {
			if err := s.client.CancelInbox(ctx, s.nativeID, run.nativeMessageID); err != nil {
				s.abandon(run, "opencode_cancellation_ambiguous", err.Error(), settledByFor(err))
				return protocol.RunCancelResponse{}, err
			}
		}
		return protocol.RunCancelResponse{SessionID: s.state.SessionID, RunID: id, Accepted: true, Status: protocol.RunCancelling}, nil
	}
	if _, err := s.client.Interrupt(ctx, s.nativeID); err != nil {
		s.abandon(run, "opencode_cancellation_ambiguous", err.Error(), settledByFor(err))
		return protocol.RunCancelResponse{}, err
	}
	if prompted {

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
	if published(s.active) || published(s.reserved) {

		s.mu.Unlock()
		return base.ErrRunActive
	}
	s.closed = true
	s.state.Status = protocol.SessionClosed
	s.state.ActiveRunID = ""
	s.state.UpdatedAtMS = s.clock.Now().UnixMilli()
	subscribers := s.allSubscribersLocked()
	subCancel, subscription := s.subCancel, s.subscription
	s.mu.Unlock()
	s.stopOnce.Do(func() { close(s.stop) })
	if subCancel != nil {
		subCancel()
	}
	_ = subscription.Close()
	err := s.client.Close()
	for _, stream := range subscribers {
		close(stream)
	}
	return err
}

func (s *session) settleTools(run *runState, cancel bool) {
	s.settleGates(run)
	s.mu.Lock()
	var tools []*toolState
	for _, tool := range s.tools {
		if tool.run == run && !tool.terminal {
			tool.terminal = true
			tools = append(tools, tool)
		}
	}
	s.mu.Unlock()
	sort.Slice(tools, func(i, j int) bool { return tools[i].order < tools[j].order })
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
	s.failRunSettled(run, code, message, "")
}

func (s *session) failRunSettled(run *runState, code, message, settledBy string) {
	s.settleTools(run, true)
	_ = s.emitWith(run, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: s.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: code, Message: message}, SettledBy: settledBy}, true, s.reportedRunCost(run))
}

func (s *session) reportedRunCost(run *runState) map[string]json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return reportedCost(run.cost)
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
	s.abandon(nil, "opencode_stream_failed", err.Error(), protocol.SettledByInferred)
}

func settledByFor(err error) string {
	var api *native.APIError
	if errors.As(err, &api) {
		return ""
	}
	return protocol.SettledByInferred
}

func (s *session) abandon(origin *runState, code, message, settledBy string) {
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
		s.failRunSettled(run, code, message, settledBy)
	}
	if reserved != nil && !reserved.terminal {
		<-reserved.admitted
		failCode, failMessage, failSettledBy := code, message, settledBy
		if reserved != origin && !reserved.promotionSeen {
			failCode = "queue_dropped"
			failMessage = "the reservation was dropped before promotion: " + message

			failSettledBy = protocol.SettledByInferred
		}
		s.failRunSettled(reserved, failCode, failMessage, failSettledBy)
	}
}

func normalizeModelRef(ref *native.ModelRef) string {
	if ref == nil || ref.ID == "" {
		return ""
	}
	if ref.ProviderID != "" {
		return ref.ProviderID + "/" + ref.ID
	}
	return ref.ID
}

func (s *session) reductionTargetLocked() *runState {
	if s.suppressed {

		return nil
	}
	if s.reserved != nil && s.reserved.promotionSeen && !s.reserved.terminal {
		return s.reserved
	}
	return s.active
}

func (s *session) promoteReserved() {
	s.mu.Lock()
	reserved := s.reserved
	if reserved == nil || !reserved.promotionSeen || (s.active != nil && !s.active.terminal) {
		s.mu.Unlock()
		return
	}
	s.reserved = nil
	if !reserved.terminal {

		s.active = reserved
		reserved.status = protocol.RunRunning
	}
	s.refreshStateLocked()
	s.mu.Unlock()
}

func (s *session) emit(run *runState, typ protocol.EnvelopeType, payload any, terminal bool) error {
	return s.emitWith(run, typ, payload, terminal, nil)
}

func (s *session) emitWith(run *runState, typ protocol.EnvelopeType, payload any, terminal bool, extensions map[string]json.RawMessage) error {
	_, err := s.emitEnvelopeWith(run, typ, payload, terminal, "", extensions)
	if err != nil {
		return err
	}
	if terminal {

		s.promoteReserved()
	}
	return nil
}

func (s *session) emitEnvelope(run *runState, typ protocol.EnvelopeType, payload any, terminal bool, inReplyTo protocol.EnvelopeID) (protocol.Envelope, error) {
	return s.emitEnvelopeWith(run, typ, payload, terminal, inReplyTo, nil)
}

func (s *session) emitEnvelopeWith(run *runState, typ protocol.EnvelopeType, payload any, terminal bool, inReplyTo protocol.EnvelopeID, extensions map[string]json.RawMessage) (protocol.Envelope, error) {
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
	event.Extensions = extensions
	event.InReplyTo = inReplyTo
	switch typ {
	case protocol.TypeActionCallRequested, protocol.TypeActionCallStarted, protocol.TypeActionCallProgress,
		protocol.TypeActionCallCompleted, protocol.TypeActionCallFailed, protocol.TypeActionCallCancelled,
		protocol.TypeActionPermissionRequested, protocol.TypeActionPermissionResolved:
		var action struct {
			ToolCallID protocol.ToolCallID `json:"tool_call_id"`
		}
		_ = json.Unmarshal(event.Payload, &action)
		event.ToolCallID = action.ToolCallID
	}
	if typ == protocol.TypeActionPermissionRequested || typ == protocol.TypeActionPermissionResolved {
		var interaction struct {
			InteractionID protocol.InteractionID `json:"interaction_id"`
		}
		_ = json.Unmarshal(event.Payload, &interaction)
		for _, gate := range s.gates {
			if gate.id == interaction.InteractionID {
				gate.open = typ == protocol.TypeActionPermissionRequested
			}
		}
	}
	s.state.UpdatedAtMS = now
	if typ == protocol.TypeRunStarted {

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
		if s.reserved == run {

			s.reserved = nil
		}
	}
	s.publishLocked(run, event, terminal)

	s.refreshStateLocked()
	return event, nil
}

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
	if terminal && event.Sequence != nil {

		s.settled = append(s.settled, protocol.SettledRun{RunID: run.id, Sequence: *event.Sequence})
	}
	s.deliverLocked(run, event, terminal)
}

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

var _ base.Session = (*session)(nil)

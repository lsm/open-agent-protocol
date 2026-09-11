package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lsm/open-agent-protocol/protocol"
)

const defaultJournalCapacity = 64

// CapabilityRevision is the advertised reference-adapter revision. Every
// emitted envelope repeats it so a consumer can bind an event to the
// descriptor snapshot it was produced under.
const CapabilityRevision = "reference-memory-v1"

var errTerminalWon = fmt.Errorf("adapter: terminal event already emitted")

// Clock and IDGenerator make every observable value deterministic in tests.
type Clock interface {
	Now() time.Time
}

type IDGenerator interface {
	NewID(kind string) string
}

type Config struct {
	Clock           Clock
	IDs             IDGenerator
	JournalCapacity int
}

// Memory is a deterministic process-local reference adapter. It executes one
// fixed interaction script; it is not a general model simulation.
type Memory struct {
	clock    Clock
	ids      IDGenerator
	capacity int
}

func NewMemory(config Config) *Memory {
	clock := config.Clock
	if clock == nil {
		clock = wallClock{}
	}
	ids := config.IDs
	if ids == nil {
		ids = &sequenceIDs{}
	}
	capacity := config.JournalCapacity
	if capacity <= 0 {
		capacity = defaultJournalCapacity
	}
	return &Memory{clock: clock, ids: ids, capacity: capacity}
}

func (m *Memory) Probe(context.Context) (Descriptor, error) {
	features := map[string]protocol.FeatureSupport{
		"protocol.initialize":           {Level: protocol.SupportNative},
		"capabilities":                  {Level: protocol.SupportNative},
		"session.open":                  {Level: protocol.SupportNative},
		"session.state":                 {Level: protocol.SupportNative},
		"session.message.submit":        {Level: protocol.SupportNative},
		"session.message.delivery.auto": {Level: protocol.SupportNative},
		"run.streaming":                 {Level: protocol.SupportNative},
		"run.status":                    {Level: protocol.SupportNative},
		"run.cancel":                    {Level: protocol.SupportEmulated, Reason: "run-target API is implemented over a one-active-run session"},
		"run.resume":                    {Level: protocol.SupportDegraded, Reason: "reattachment and replay use a bounded process-memory journal"},
		"run.reconciliation":            {Level: protocol.SupportNative},
		"run.replay":                    {Level: protocol.SupportDegraded, Reason: "older cursors can expire and no cross-process replay is claimed"},
		"action.tools.execute":          {Level: protocol.SupportEmulated, Reason: "the reference adapter executes a fixed deterministic script"},
		"action.permissions":            {Level: protocol.SupportEmulated, Reason: "the reference adapter exposes an interactive scripted gate"},
		"user_input":                    {Level: protocol.SupportEmulated, Reason: "the reference adapter exposes an interactive scripted gate"},
	}
	endpoint := protocol.EndpointDescriptor{ID: "reference.memory", Name: "Deterministic In-Memory Reference Adapter", Version: protocol.Version, Adapter: "process-memory-script"}
	return Descriptor{
		Capabilities: protocol.CapabilityDescriptor{
			Endpoint:         endpoint,
			ProtocolVersions: []string{protocol.Version},
			Profiles:         []string{protocol.Profile},
			Features:         features,
		},
		CapabilityRevision:         CapabilityRevision,
		Journal:                    JournalDescriptor{Scope: "session", Persistence: "process_memory", Replay: protocol.SupportDegraded, Capacity: m.capacity},
		MaxActiveRunsPerSession:    1,
		InteractiveGates:           true,
		CancellationTarget:         "run",
		CancellationImplementation: "session_emulated",
	}, nil
}

func (m *Memory) Open(ctx context.Context, request OpenRequest) (Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The participant is the recorded responder for every permission and
	// user-input gate this adapter raises, so an empty identity would both
	// violate the schema and make those gates unresolvable.
	if request.Participant.ID == "" {
		return nil, fmt.Errorf("%w: open requires a non-empty participant id", ErrInvalidParticipant)
	}
	id := request.SessionID
	if id == "" {
		id = protocol.SessionID(m.ids.NewID("session"))
	}
	now := m.clock.Now().UnixMilli()
	return &memorySession{
		clock: m.clock, ids: m.ids, capacity: m.capacity,
		participant: request.Participant.ID,
		state:       protocol.SessionState{SessionID: id, Status: protocol.SessionIdle, UpdatedAtMS: now},
		runs:        make(map[protocol.RunID]*memoryRun),
	}, nil
}

type memorySession struct {
	mu          sync.Mutex
	emitMu      sync.Mutex
	opMu        sync.Mutex
	clock       Clock
	ids         IDGenerator
	capacity    int
	participant protocol.ParticipantID
	state       protocol.SessionState
	closed      bool
	active      *memoryRun
	runs        map[protocol.RunID]*memoryRun
	journal     []protocol.Envelope
}

type scriptStage uint8

const (
	stagePermission scriptStage = iota
	stageInput
	stageTerminal
)

type memoryRun struct {
	id           protocol.RunID
	status       protocol.RunStatus
	stage        scriptStage
	nextSequence uint64
	terminal     bool
	permissionID protocol.InteractionID
	inputID      protocol.InteractionID
	toolCallID   protocol.ToolCallID
	requestedBy  protocol.ParticipantID
	respondedBy  protocol.ParticipantID
	subscribers  []chan Result
}

func (s *memorySession) Submit(ctx context.Context, request protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, EventStream, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}
	if request.SessionID == "" || len(request.Messages) == 0 {
		return protocol.MessageSubmitResponse{}, nil, ErrInvalidSubmission
	}
	if request.Delivery != "" && request.Delivery != protocol.DeliveryAuto {
		return protocol.MessageSubmitResponse{}, nil, fmt.Errorf("%w: delivery %q", ErrInvalidSubmission, request.Delivery)
	}
	// The fixed deterministic script runs no model and Probe advertises no model
	// selection, so echoing a caller ModelID would attribute the run to a model it
	// never used.
	if request.ModelID != "" {
		return protocol.MessageSubmitResponse{}, nil, fmt.Errorf("%w: the memory adapter selects no model", ErrUnsupportedInput)
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, ErrSessionClosed
	}
	if request.SessionID != s.state.SessionID {
		s.mu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, ErrRunNotFound
	}
	if s.active != nil && !s.active.terminal {
		s.mu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, ErrRunActive
	}
	run := &memoryRun{
		id: protocol.RunID(s.ids.NewID("run")), status: protocol.RunRunning,
		nextSequence: 1, stage: stagePermission,
		permissionID: protocol.InteractionID(s.ids.NewID("permission")),
		inputID:      protocol.InteractionID(s.ids.NewID("input")),
		toolCallID:   protocol.ToolCallID(s.ids.NewID("tool-call")),
		requestedBy:  "agent", respondedBy: s.participant,
	}
	stream := make(chan Result, 32)
	run.subscribers = append(run.subscribers, stream)
	s.active = run
	s.runs[run.id] = run
	s.state.Status = protocol.SessionRunning
	s.state.ActiveRunID = run.id
	s.state.UpdatedAtMS = s.clock.Now().UnixMilli()
	s.mu.Unlock()

	messageIDs := make([]protocol.MessageID, len(request.Messages))
	for i := range request.Messages {
		messageIDs[i] = request.Messages[i].ID
		if messageIDs[i] == "" {
			messageIDs[i] = protocol.MessageID(s.ids.NewID("message"))
		}
	}
	admission := protocol.MessageSubmitResponse{
		SessionID: s.state.SessionID, Accepted: true,
		SubmissionID:      protocol.SubmissionID(s.ids.NewID("submission")),
		RequestedDelivery: protocol.DeliveryAuto, EffectiveDelivery: protocol.DeliveryStart,
		DeliveryResolution: "session_idle", Admission: protocol.AdmissionStarted,
		RunID: run.id, Status: protocol.RunRunning, ModelID: request.ModelID, MessageIDs: messageIDs,
	}

	if err := s.emitInitial(run); err != nil {
		return protocol.MessageSubmitResponse{}, stream, err
	}
	return admission, stream, nil
}

func (s *memorySession) emitInitial(run *memoryRun) error {
	started := protocol.RunStartedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunRunning, ModelID: s.state.CurrentModelID, StartedAtMS: s.clock.Now().UnixMilli()}
	if err := s.emit(run, protocol.TypeRunStarted, started, false); err != nil {
		return err
	}
	delta := protocol.ContentDeltaPayload{SessionID: s.state.SessionID, RunID: run.id, MessageID: protocol.MessageID(s.ids.NewID("message")), Part: protocol.ContentPart{Type: protocol.ContentText, Text: "I will use the scripted tool."}}
	if err := s.emit(run, protocol.TypeContentDelta, delta, false); err != nil {
		return err
	}
	call := protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID, Name: "scripted_tool", ArgumentsJSON: json.RawMessage(`{"operation":"golden"}`), RequestedBy: run.requestedBy, ExecutionOwner: "reference-adapter"}
	if err := s.emit(run, protocol.TypeActionCallRequested, call, false); err != nil {
		return err
	}
	permission := protocol.PermissionRequestedPayload{InteractionID: run.permissionID, SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID, Title: "Allow scripted tool", Description: "The golden script requires approval.", Choices: []protocol.PermissionChoice{{ID: "approve", Label: "Approve"}, {ID: "deny", Label: "Deny"}}, ArgumentsJSON: call.ArgumentsJSON, RequestedBy: run.requestedBy, RespondedBy: run.respondedBy}
	return s.emit(run, protocol.TypeActionPermissionRequested, permission, false)
}

func (s *memorySession) State(ctx context.Context) (protocol.SessionState, error) {
	if err := ctx.Err(); err != nil {
		return protocol.SessionState{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.state, ErrSessionClosed
	}
	return s.state, nil
}

func (s *memorySession) Resolve(ctx context.Context, resolution InteractionResolution) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrSessionClosed
	}
	run := s.runs[resolution.RunID]
	if run == nil {
		s.mu.Unlock()
		return ErrRunNotFound
	}
	if run.terminal {
		s.mu.Unlock()
		return ErrInteractionResolved
	}
	if resolution.RespondedBy != run.respondedBy {
		s.mu.Unlock()
		return ErrWrongResponder
	}
	stage := run.stage
	if stage == stagePermission {
		if resolution.Permission == nil || resolution.Input != nil || resolution.Permission.InteractionID != run.permissionID || resolution.Permission.SessionID != s.state.SessionID || resolution.Permission.RunID != run.id {
			s.mu.Unlock()
			return ErrInvalidResolution
		}
		run.stage = stageInput
	} else if stage == stageInput {
		if resolution.Input == nil || resolution.Permission != nil || resolution.Input.InteractionID != run.inputID || resolution.Input.SessionID != s.state.SessionID || resolution.Input.RunID != run.id {
			s.mu.Unlock()
			return ErrInvalidResolution
		}
		run.stage = stageTerminal
	} else {
		s.mu.Unlock()
		return ErrInteractionResolved
	}
	s.mu.Unlock()

	if stage == stagePermission {
		return s.resolvePermission(run, *resolution.Permission)
	}
	return s.resolveInput(run, *resolution.Input)
}

func (s *memorySession) resolvePermission(run *memoryRun, request protocol.PermissionResolveRequest) error {
	granted := request.Granted
	resolved := protocol.PermissionResolvedPayload{InteractionID: run.permissionID, SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID, Outcome: protocol.InteractionResolved, ChoiceID: request.ChoiceID, Granted: &granted, RequestedBy: run.requestedBy, RespondedBy: run.respondedBy}
	if err := s.emit(run, protocol.TypeActionPermissionResolved, resolved, false); err != nil {
		return err
	}
	if !request.Granted {
		call := protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID, Name: "scripted_tool", RequestedBy: run.requestedBy, RespondedBy: run.respondedBy, ExecutionOwner: "reference-adapter"}
		if err := s.emit(run, protocol.TypeActionCallCancelled, call, false); err != nil {
			return err
		}
		failure := protocol.RunFailedPayload{SessionID: s.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: "permission_denied", Message: "scripted tool permission denied"}}
		return s.emit(run, protocol.TypeRunFailed, failure, true)
	}
	call := protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID, Name: "scripted_tool", ArgumentsJSON: json.RawMessage(`{"operation":"golden"}`), RequestedBy: run.requestedBy, ExecutionOwner: "reference-adapter"}
	if err := s.emit(run, protocol.TypeActionCallStarted, call, false); err != nil {
		return err
	}
	call.Result = json.RawMessage(`{"ok":true}`)
	if err := s.emit(run, protocol.TypeActionCallCompleted, call, false); err != nil {
		return err
	}
	input := protocol.UserInputRequestedPayload{InteractionID: run.inputID, SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID, Title: "Golden input", Description: "Choose the deterministic answer.", Questions: []protocol.InputQuestion{{ID: "choice", Prompt: "Continue?", Kind: protocol.InputSingleChoice, Required: true, Options: []protocol.InputOption{{ID: "yes", Label: "Yes"}}}}, RequestedBy: run.requestedBy, RespondedBy: run.respondedBy}
	if err := s.emit(run, protocol.TypeUserInputRequested, input, false); err != nil {
		return err
	}
	status := protocol.RunStatusUpdatedPayload{
		SessionID:          s.state.SessionID,
		RunID:              run.id,
		Status:             protocol.RunWaitingForInput,
		PendingUserInputID: run.inputID,
		UpdatedAtMS:        s.clock.Now().UnixMilli(),
	}
	if err := s.emit(run, protocol.TypeRunStatusUpdated, status, false); err != nil {
		return err
	}
	s.mu.Lock()
	if !run.terminal {
		run.status = protocol.RunWaitingForInput
		s.state.Status = protocol.SessionWaitingForInput
		s.state.UpdatedAtMS = status.UpdatedAtMS
	}
	s.mu.Unlock()
	return nil
}

func (s *memorySession) resolveInput(run *memoryRun, request protocol.UserInputResolveRequest) error {
	resolved := protocol.UserInputResolvedPayload{InteractionID: run.inputID, SessionID: s.state.SessionID, RunID: run.id, Status: protocol.InputSubmitted, Answers: request.Answers, RequestedBy: run.requestedBy, RespondedBy: run.respondedBy}
	if err := s.emit(run, protocol.TypeUserInputResolved, resolved, false); err != nil {
		return err
	}
	finalText := "The golden script completed."
	delta := protocol.ContentDeltaPayload{SessionID: s.state.SessionID, RunID: run.id, MessageID: protocol.MessageID(s.ids.NewID("message")), Part: protocol.ContentPart{Type: protocol.ContentText, Text: finalText}}
	if err := s.emit(run, protocol.TypeContentDelta, delta, false); err != nil {
		return err
	}
	completed := protocol.RunCompletedPayload{SessionID: s.state.SessionID, RunID: run.id, FinalResponse: protocol.Message{ID: delta.MessageID, Role: protocol.RoleAssistant, Content: protocol.TextContent(finalText)}, StopReason: "end_turn"}
	if err := s.emit(run, protocol.TypeRunCompleted, completed, true); err != nil && err != errTerminalWon {
		return err
	}
	return nil
}

func (s *memorySession) Cancel(ctx context.Context, runID protocol.RunID) (protocol.RunCancelResponse, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return protocol.RunCancelResponse{}, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return protocol.RunCancelResponse{}, ErrSessionClosed
	}
	run := s.runs[runID]
	if run == nil {
		s.mu.Unlock()
		return protocol.RunCancelResponse{}, ErrRunNotFound
	}
	if run.terminal {
		status := run.status
		s.mu.Unlock()
		if status == protocol.RunCancelled {
			return protocol.RunCancelResponse{SessionID: s.state.SessionID, RunID: runID, Accepted: true, Status: status}, nil
		}
		return protocol.RunCancelResponse{}, &RunTerminalError{RunID: runID, Status: status}
	}
	if run.status == protocol.RunCancelling {
		s.mu.Unlock()
		return protocol.RunCancelResponse{SessionID: s.state.SessionID, RunID: runID, Accepted: true, Status: protocol.RunCancelling}, nil
	}
	run.status = protocol.RunCancelling
	s.mu.Unlock()

	status := protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunCancelling, UpdatedAtMS: s.clock.Now().UnixMilli()}
	if err := s.emit(run, protocol.TypeRunStatusUpdated, status, false); err != nil && err != errTerminalWon {
		return protocol.RunCancelResponse{}, err
	}
	// Close pending child lifecycles before settling their parent run.
	if run.stage == stagePermission {
		reason := protocol.ProtocolError{Code: "run_cancelled", Message: "run cancellation closed the permission request"}
		resolved := protocol.PermissionResolvedPayload{InteractionID: run.permissionID, RequestedBy: run.requestedBy, RespondedBy: run.respondedBy, SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID, Outcome: protocol.InteractionCancelled, Reason: &reason}
		_ = s.emit(run, protocol.TypeActionPermissionResolved, resolved, false)
		call := protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID, Name: "scripted_tool", RequestedBy: run.requestedBy, RespondedBy: run.respondedBy, ExecutionOwner: "reference-adapter"}
		_ = s.emit(run, protocol.TypeActionCallCancelled, call, false)
	} else if run.stage == stageInput {
		resolved := protocol.UserInputResolvedPayload{InteractionID: run.inputID, RequestedBy: run.requestedBy, RespondedBy: run.respondedBy, SessionID: s.state.SessionID, RunID: run.id, Status: protocol.InputCancelled}
		_ = s.emit(run, protocol.TypeUserInputResolved, resolved, false)
	}
	cancelled := protocol.RunCancelledPayload{SessionID: s.state.SessionID, RunID: run.id, Reason: "cancel confirmed"}
	_ = s.emit(run, protocol.TypeRunCancelled, cancelled, true)

	// The acknowledgement describes accepted intent, not terminal settlement. The
	// confirmed run.cancelled event above is authoritative.
	return protocol.RunCancelResponse{SessionID: s.state.SessionID, RunID: runID, Accepted: true, Status: protocol.RunCancelling}, nil
}

func (s *memorySession) Resume(ctx context.Context, request ResumeRequest) (Recovery, EventStream, error) {
	if err := ctx.Err(); err != nil {
		return Recovery{}, nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Recovery{}, nil, ErrSessionClosed
	}
	run := s.runs[request.RunID]
	if run == nil {
		s.mu.Unlock()
		return Recovery{}, nil, ErrRunNotFound
	}
	state := s.state
	latest := run.nextSequence - 1
	if request.AfterSequence > latest {
		s.mu.Unlock()
		return Recovery{}, nil, ErrReplayCursorFuture
	}
	oldest := uint64(0)
	var suffix []protocol.Envelope
	for _, envelope := range s.journal {
		if envelope.RunID != request.RunID || envelope.Sequence == nil {
			continue
		}
		if oldest == 0 {
			oldest = *envelope.Sequence
		}
		if *envelope.Sequence > request.AfterSequence {
			suffix = append(suffix, envelope)
		}
	}
	recovery := Recovery{State: state, RunID: run.id, RequestedAfter: request.AfterSequence, ReplayedFrom: request.AfterSequence, ReplayedThrough: request.AfterSequence}
	if len(suffix) > 0 {
		recovery.ReplayedFrom = *suffix[0].Sequence
		recovery.ReplayedThrough = *suffix[len(suffix)-1].Sequence
	}
	gap := request.AfterSequence < latest && (oldest == 0 || request.AfterSequence+1 < oldest)
	if gap {
		recovery.ReplayGap = &ReplayGap{RequestedAfter: request.AfterSequence, OldestAvailable: oldest, LatestAvailable: latest}
		recovery.ReplayedFrom = 0
		recovery.ReplayedThrough = 0
		suffix = nil
	}
	// Capacity includes the complete bounded remainder of this fixed script, so a
	// detached or slow consumer cannot stall execution.
	stream := make(chan Result, len(suffix)+32)
	if !gap {
		for _, envelope := range suffix {
			stream <- Result{Envelope: envelope}
		}
		if !run.terminal {
			run.subscribers = append(run.subscribers, stream)
		} else {
			close(stream)
		}
	} else {
		close(stream)
	}
	s.mu.Unlock()
	if gap {
		return recovery, stream, recovery.ReplayGap
	}
	return recovery, stream, nil
}

func (s *memorySession) Close(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	if s.active != nil && !s.active.terminal {
		s.mu.Unlock()
		return ErrRunActive
	}
	s.closed = true
	s.state.Status = protocol.SessionClosed
	s.state.ActiveRunID = ""
	s.state.UpdatedAtMS = s.clock.Now().UnixMilli()
	var subscribers []chan Result
	for _, run := range s.runs {
		subscribers = append(subscribers, run.subscribers...)
		run.subscribers = nil
	}
	s.mu.Unlock()
	for _, subscriber := range subscribers {
		close(subscriber)
	}
	return nil
}

// emit is the sole sequence allocator and reducer. It appends before publishing,
// serializes publishers, and never sends while the state mutex is held.
func (s *memorySession) emit(run *memoryRun, typ protocol.EnvelopeType, payload any, terminal bool) error {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()

	s.mu.Lock()
	if run.terminal {
		s.mu.Unlock()
		return errTerminalWon
	}
	now := s.clock.Now().UnixMilli()
	envelope, err := protocol.NewEnvelope(typ, protocol.EnvelopeID(s.ids.NewID("event")), payload)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	sequence := run.nextSequence
	run.nextSequence++
	envelope.Sequence = &sequence
	envelope.TimestampMS = &now
	envelope.SessionID = s.state.SessionID
	envelope.RunID = run.id
	envelope.CapabilityRevision = CapabilityRevision
	switch typ {
	case protocol.TypeActionCallRequested, protocol.TypeActionCallStarted, protocol.TypeActionCallProgress, protocol.TypeActionCallCompleted, protocol.TypeActionCallFailed, protocol.TypeActionCallCancelled, protocol.TypeActionPermissionRequested:
		envelope.ToolCallID = run.toolCallID
	}
	s.journal = append(s.journal, envelope)
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
		case protocol.TypeRunFailed:
			run.status = protocol.RunFailed
		case protocol.TypeRunCancelled:
			run.status = protocol.RunCancelled
		}
		if s.active == run {
			s.active = nil
		}
		s.state.Status = protocol.SessionIdle
		s.state.ActiveRunID = ""
	}
	subscribers := append([]chan Result(nil), run.subscribers...)
	if terminal {
		run.subscribers = nil
	}
	s.mu.Unlock()

	for _, subscriber := range subscribers {
		subscriber <- Result{Envelope: envelope}
	}
	if terminal {
		for _, subscriber := range subscribers {
			close(subscriber)
		}
	}
	return nil
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

type sequenceIDs struct{ value atomic.Uint64 }

func (g *sequenceIDs) NewID(kind string) string {
	return kind + "-" + strconv.FormatUint(g.value.Add(1), 10)
}

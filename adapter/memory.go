package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/validation"
)

const defaultJournalCapacity = 64

// The reference adapter's fixed model catalog and structured result. Both are
// disclosed: the catalog through model_not_found for anything outside it, the
// result through run.structured_output's fixed_result constraint. A caller and
// a validator can therefore check a refusal in both directions instead of
// taking the endpoint's word for it.
const (
	ModelPrimary   = "reference-model-a"
	ModelSecondary = "reference-model-b"
	fixedResult    = `{"ok":true}`
	scriptedTool   = "scripted_tool"
	// scriptedToolOwner runs the scripted tool, and is what every emitted
	// action.call payload names as its execution_owner.
	scriptedToolOwner = "reference-adapter"
)

// scriptedCatalog is the reference adapter's effective tool catalog: the one
// tool its script calls, and the one a tool_choice policy can name. The
// descriptor publishes it and the submit gate judges against it, from here, so
// the endpoint cannot advertise one catalog and enforce another — which is the
// contradiction a validator reads as an empty catalog, refusing every policy
// the adapter itself accepts.
func scriptedCatalog() []protocol.ToolDefinition {
	return []protocol.ToolDefinition{{
		Name:           scriptedTool,
		Description:    "The deterministic scripted tool the reference adapter calls.",
		InputSchema:    json.RawMessage(`{"type":"object","properties":{"operation":{"type":"string"}}}`),
		ExecutionOwner: scriptedToolOwner,
	}}
}

// CapabilityRevision is the advertised reference-adapter revision. Every
// emitted envelope repeats it so a consumer can bind an event to the
// descriptor snapshot it was produced under.
// v2 published the scripted tool in the catalog; v1 published none. v3
// advertises models.list and serves the fixed model catalog. A revision
// identifies exactly one descriptor, so a consumer holding an older snapshot
// must see this one as new rather than validate against a descriptor that
// says less than the endpoint does.
const CapabilityRevision = "reference-memory-v3"

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
		"action.tools":                  {Level: protocol.SupportEmulated, Reason: "the reference adapter projects the scripted tool lifecycle"},
		"action.tools.execute":          {Level: protocol.SupportEmulated, Reason: "the reference adapter executes a fixed deterministic script"},
		"action.permissions":            {Level: protocol.SupportEmulated, Reason: "the reference adapter exposes an interactive scripted gate"},
		"user_input":                    {Level: protocol.SupportEmulated, Reason: "the reference adapter exposes an interactive scripted gate"},
		// The run controls, executed deterministically. Each disclosure is
		// machine-readable so a refusal is checkable in both directions: the
		// mode says the session default never moves, the enforced tool_choice
		// modes say which policies a refusal may cite, and fixed_result names
		// the exact object every structured completion carries.
		protocol.FeatureModelSelection: {Level: protocol.SupportEmulated, Mode: protocol.ModePerRun, Reason: "the reference adapter runs no model; it echoes a selection from a fixed catalog for one run"},
		// The catalog the model gate is judged against is served rather than
		// left implicit, so a caller can read the two ids the adapter accepts
		// instead of discovering them one model_not_found at a time. It is
		// fixed for the revision, which is what native means here.
		protocol.FeatureModelsList:   {Level: protocol.SupportNative, Reason: "the reference adapter serves its fixed catalog, which is exactly the set its model gate admits"},
		protocol.FeatureInstructions: {Level: protocol.SupportEmulated, Reason: "instructions are prepended to the scripted text so their effect is observable"},
		protocol.FeatureToolSelection: {
			Level:  protocol.SupportEmulated,
			Modes:  []string{protocol.ToolChoiceAuto, protocol.ToolChoiceNone, protocol.ToolChoiceRequired, protocol.ToolChoiceNamed},
			Reason: "the policy selects whether the scripted tool is called",
		},
		protocol.FeatureStructuredOutput: {
			Level:       protocol.SupportEmulated,
			Constraints: map[string]json.RawMessage{protocol.ConstraintFixedResult: json.RawMessage(fixedResult)},
			Reason:      "the scripted result is fixed, so only a schema that object satisfies is admitted",
		},
	}
	endpoint := protocol.EndpointDescriptor{ID: "reference.memory", Name: "Deterministic In-Memory Reference Adapter", Version: protocol.Version, Adapter: "process-memory-script"}
	return Descriptor{
		Capabilities: protocol.CapabilityDescriptor{
			Endpoint:         endpoint,
			ProtocolVersions: []string{protocol.Version},
			Profiles:         []string{protocol.Profile},
			Features:         features,
			// The catalog a tool_choice is judged against is the descriptor's,
			// so it is published rather than kept private to the session.
			Tools: scriptedCatalog(),
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
	controls     admittedControls
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
	// Every control is judged before any identity is allocated: a refused
	// submission reserves no submission id, no run id, and writes nothing.
	// The gate also runs ahead of ordinary submission validation, because the
	// ladder ranks a control refusal above it: a caller told only that its
	// submission was invalid would fix the messages, resubmit, and be refused
	// for the control anyway. Every adapter here runs the gate in this
	// position, so one request gets one answer whichever endpoint serves it.
	controls, err := s.admitControls(request)
	if err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}
	if request.SessionID == "" || len(request.Messages) == 0 {
		return protocol.MessageSubmitResponse{}, nil, ErrInvalidSubmission
	}
	if request.Delivery != "" && request.Delivery != protocol.DeliveryAuto {
		return protocol.MessageSubmitResponse{}, nil, fmt.Errorf("%w: delivery %q", ErrInvalidSubmission, request.Delivery)
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
		nextSequence: 1, stage: stagePermission, controls: controls,
		permissionID: protocol.InteractionID(s.ids.NewID("permission")),
		inputID:      protocol.InteractionID(s.ids.NewID("input")),
		toolCallID:   protocol.ToolCallID(s.ids.NewID("tool-call")),
		requestedBy:  "agent", respondedBy: s.participant,
	}
	if !controls.callsTool {
		// The policy excludes the scripted tool, so the run never opens a
		// call and its permission gate: the script goes straight to the
		// input stage.
		run.stage = stageInput
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
		RunID: run.id, Status: protocol.RunRunning, ModelID: controls.model, MessageIDs: messageIDs,
	}

	if err := s.emitInitial(run); err != nil {
		return protocol.MessageSubmitResponse{}, stream, err
	}
	return admission, stream, nil
}

func (s *memorySession) emitInitial(run *memoryRun) error {
	// An admitted model is authoritative for the run; absent one the run
	// reports the session default, as before. Either way the default itself
	// does not move: the application is per_run.
	model := run.controls.model
	if model == "" {
		model = s.state.CurrentModelID
	}
	started := protocol.RunStartedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunRunning, ModelID: model, StartedAtMS: s.clock.Now().UnixMilli()}
	if err := s.emit(run, protocol.TypeRunStarted, started, false); err != nil {
		return err
	}
	// Admitted instructions are prepended to the scripted text, so their
	// effect is observable on the wire rather than only asserted.
	text := "I will use the scripted tool."
	if !run.controls.callsTool {
		text = "I will answer without the scripted tool."
	}
	if run.controls.instructions != "" {
		text = run.controls.instructions + " " + text
	}
	delta := protocol.ContentDeltaPayload{SessionID: s.state.SessionID, RunID: run.id, MessageID: protocol.MessageID(s.ids.NewID("message")), Part: protocol.ContentPart{Type: protocol.ContentText, Text: text}}
	if err := s.emit(run, protocol.TypeContentDelta, delta, false); err != nil {
		return err
	}
	if !run.controls.callsTool {
		return s.requestInput(run)
	}
	call := protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID, Name: scriptedTool, ArgumentsJSON: json.RawMessage(`{"operation":"golden"}`), RequestedBy: run.requestedBy, ExecutionOwner: scriptedToolOwner}
	if err := s.emit(run, protocol.TypeActionCallRequested, call, false); err != nil {
		return err
	}
	permission := protocol.PermissionRequestedPayload{InteractionID: run.permissionID, SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID, Title: "Allow scripted tool", Description: "The golden script requires approval.", Choices: []protocol.PermissionChoice{{ID: "approve", Label: "Approve"}, {ID: "deny", Label: "Deny"}}, ArgumentsJSON: call.ArgumentsJSON, RequestedBy: run.requestedBy, RespondedBy: run.respondedBy}
	return s.emit(run, protocol.TypeActionPermissionRequested, permission, false)
}

// requestInput opens the scripted prompt and reports the wait. It is the one
// stage every script reaches, whether or not the tool was called.
func (s *memorySession) requestInput(run *memoryRun) error {
	input := protocol.UserInputRequestedPayload{InteractionID: run.inputID, SessionID: s.state.SessionID, RunID: run.id, Title: "Golden input", Description: "Choose the deterministic answer.", Questions: goldenInputQuestions(), RequestedBy: run.requestedBy, RespondedBy: run.respondedBy}
	if run.controls.callsTool {
		input.ToolCallID = run.toolCallID
	}
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

// admittedControls is the control set one run was admitted with. It is read
// back when the run completes, so the endpoint's execution and its admission
// cannot drift.
type admittedControls struct {
	model        string
	instructions string
	choice       *protocol.ToolChoice
	outputSchema json.RawMessage
	callsTool    bool
}

// modelCatalog is the reference adapter's fixed model catalog: the same two
// ids admitControls admits, published so the gate and the catalog cannot
// disagree. The first is the default, and there is exactly one.
func modelCatalog() []protocol.ModelDescriptor {
	return []protocol.ModelDescriptor{
		{ID: ModelPrimary, DisplayName: "Reference Model A", ProviderID: "reference", ContextWindow: 8192, Default: true},
		{ID: ModelSecondary, DisplayName: "Reference Model B", ProviderID: "reference", ContextWindow: 8192},
	}
}

// Models serves the session's effective catalog. It is deterministic and
// revision-stable: the same list for the life of the descriptor, so a consumer
// can cache it against the capability revision.
//
// The catalog reports no current model because the reference adapter holds no
// session default: a per_run selection binds its own run and leaves the
// default alone, and a session that has never been told which model to use has
// none to report. The default descriptor says which one it would pick.
func (s *memorySession) Models(ctx context.Context, request protocol.ModelsRequest) (Catalog, error) {
	if err := ctx.Err(); err != nil {
		return Catalog{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Catalog{}, ErrSessionClosed
	}
	if request.SessionID != "" && request.SessionID != s.state.SessionID {
		return Catalog{}, fmt.Errorf("%w: catalog query names session %q", ErrInvalidSubmission, request.SessionID)
	}
	// The revision is read here, with the listing, rather than left for a
	// caller to pair with a descriptor it probed separately.
	return Catalog{
		Revision: CapabilityRevision,
		Models: protocol.ModelsResponse{
			SessionID:      s.state.SessionID,
			CurrentModelID: s.state.CurrentModelID,
			Models:         modelCatalog(),
		},
	}, nil
}

// catalog names the effective tool catalog this session's policies are judged
// against, read from the one the descriptor publishes.
func (s *memorySession) catalog() []string {
	names := make([]string, 0, 1)
	for _, tool := range scriptedCatalog() {
		names = append(names, tool.Name)
	}
	return names
}

// admitControls judges every per-submit control against what Probe advertises
// and reports the first refusal in the plan's precedence order: capability,
// then degradation, then unsatisfiability. The reference adapter advertises no
// control `degraded`, so the middle rung never fires here; it is the validator
// and the native adapters that exercise it.
func (s *memorySession) admitControls(request protocol.MessageSubmitRequest) (admittedControls, error) {
	controls := admittedControls{callsTool: true}
	// Refusals are collected rather than returned where they are found. A
	// request can fail several controls at once and one error.response
	// carries one code, so within the unsatisfiability rung the plan ranks
	// them by the lower capability key: returning the first defect found
	// would answer by the order this function happens to read the controls
	// in, and name a different control than the validator names for the same
	// request.
	var refusals []keyedRefusal
	refuse := func(key string, err error) { refusals = append(refusals, keyedRefusal{key: key, err: err}) }
	if request.Instructions != nil {
		controls.instructions = *request.Instructions
	}
	if request.ModelID != nil {
		// A present-but-empty id is a control like any other and, past the
		// gate, a model id like any other: one no catalog can list.
		model := *request.ModelID
		if model != ModelPrimary && model != ModelSecondary {
			refuse(protocol.FeatureModelSelection, &ModelNotFoundError{ModelID: model})
		} else {
			controls.model = model
		}
	}
	policy, err := request.ToolChoicePolicy()
	switch {
	case err != nil:
		refuse(protocol.FeatureToolSelection, &UnsupportedControlError{Feature: protocol.FeatureToolSelection, Reason: ControlUnsatisfiable, Detail: err.Error()})
	case policy != nil:
		if defect := policy.Unsatisfiable(s.catalog(), true); defect != nil {
			refuse(protocol.FeatureToolSelection, &UnsupportedControlError{Feature: protocol.FeatureToolSelection, Reason: ControlUnsatisfiable, Tool: defect.Tool, Detail: defect.Reason})
		} else {
			controls.choice = policy
			controls.callsTool = policy.Permits(scriptedTool, s.catalog(), true)
		}
	}
	if len(request.OutputSchema) > 0 {
		compiled, err := validation.CompileOutputSchema(request.OutputSchema)
		switch {
		case err != nil:
			refuse(protocol.FeatureStructuredOutput, &UnsupportedControlError{Feature: protocol.FeatureStructuredOutput, Reason: ControlUnsatisfiable, Field: "output_schema", Detail: err.Error()})
		// The scripted result is fixed and disclosed as fixed_result, so a
		// schema that object cannot satisfy could only complete
		// nonconforming. Refusing it before admission is the promise the
		// constraint makes, checkable in both directions.
		case compiled.Validate(json.RawMessage(fixedResult)) != nil:
			refuse(protocol.FeatureStructuredOutput, &UnsupportedControlError{Feature: protocol.FeatureStructuredOutput, Reason: ControlUnsatisfiable, Field: "output_schema", Detail: "the fixed result does not satisfy the requested schema"})
		default:
			controls.outputSchema = append(json.RawMessage(nil), request.OutputSchema...)
		}
	}
	if len(refusals) > 0 {
		slices.SortStableFunc(refusals, func(a, b keyedRefusal) int { return strings.Compare(a.key, b.key) })
		return admittedControls{}, refusals[0].err
	}
	return controls, nil
}

// keyedRefusal is one control refusal together with the capability key it
// falls under, which is what ranks it against the others a request earned.
type keyedRefusal struct {
	key string
	err error
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

// goldenInputQuestions is the deterministic prompt the reference adapter
// offers. The same questions validate a resolution, so the advertised and
// accepted shapes cannot drift.
func goldenInputQuestions() []protocol.InputQuestion {
	return []protocol.InputQuestion{{
		ID: "choice", Prompt: "Continue?", Kind: protocol.InputSingleChoice, Required: true,
		Options: []protocol.InputOption{{ID: "yes", Label: "Yes"}},
	}}
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
		// The nested request must preserve the stored ownership, and the choice
		// must be one the scripted gate offered with a matching grant value.
		if resolution.Permission.RequestedBy != run.requestedBy || resolution.Permission.RespondedBy != run.respondedBy {
			s.mu.Unlock()
			return ErrInvalidResolution
		}
		if granted, ok := offeredPermissionChoice(resolution.Permission.ChoiceID); !ok || granted != resolution.Permission.Granted {
			s.mu.Unlock()
			return ErrInvalidResolution
		}
		run.stage = stageInput
	} else if stage == stageInput {
		if resolution.Input == nil || resolution.Permission != nil || resolution.Input.InteractionID != run.inputID || resolution.Input.SessionID != s.state.SessionID || resolution.Input.RunID != run.id {
			s.mu.Unlock()
			return ErrInvalidResolution
		}
		if resolution.Input.RequestedBy != run.requestedBy || resolution.Input.RespondedBy != run.respondedBy {
			s.mu.Unlock()
			return ErrInvalidResolution
		}
		// The scripted prompt offers exactly one required single-choice question
		// ("choice" with the single option "yes"); anything else cannot be
		// reported as a submitted resolution. Validation reuses the offered
		// question so the accepted and advertised shapes cannot drift.
		if len(resolution.Input.Answers) != 1 || ValidateInputAnswer(goldenInputQuestions()[0], resolution.Input.Answers[0]) != nil {
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

// offeredPermissionChoice reports whether the scripted permission request
// offered the choice and, if so, the grant value that choice selects.
func offeredPermissionChoice(choice string) (bool, bool) {
	switch choice {
	case "approve":
		return true, true
	case "deny":
		return false, true
	default:
		return false, false
	}
}

func (s *memorySession) resolvePermission(run *memoryRun, request protocol.PermissionResolveRequest) error {
	granted := request.Granted
	resolved := protocol.PermissionResolvedPayload{InteractionID: run.permissionID, SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID, Outcome: protocol.InteractionResolved, ChoiceID: request.ChoiceID, Granted: &granted, RequestedBy: run.requestedBy, RespondedBy: run.respondedBy}
	if err := s.emit(run, protocol.TypeActionPermissionResolved, resolved, false); err != nil {
		return err
	}
	if !request.Granted {
		call := protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID, Name: scriptedTool, RequestedBy: run.requestedBy, RespondedBy: run.respondedBy, ExecutionOwner: scriptedToolOwner}
		if err := s.emit(run, protocol.TypeActionCallCancelled, call, false); err != nil {
			return err
		}
		failure := protocol.RunFailedPayload{SessionID: s.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: "permission_denied", Message: "scripted tool permission denied"}}
		return s.emit(run, protocol.TypeRunFailed, failure, true)
	}
	call := protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID, Name: scriptedTool, ArgumentsJSON: json.RawMessage(`{"operation":"golden"}`), RequestedBy: run.requestedBy, ExecutionOwner: scriptedToolOwner}
	if err := s.emit(run, protocol.TypeActionCallStarted, call, false); err != nil {
		return err
	}
	// The completion payload forbids the request-only members; clear the
	// arguments the start event carried.
	call.ArgumentsJSON = nil
	call.Result = json.RawMessage(`{"ok":true}`)
	if err := s.emit(run, protocol.TypeActionCallCompleted, call, false); err != nil {
		return err
	}
	return s.requestInput(run)
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
	completed := protocol.RunCompletedPayload{SessionID: s.state.SessionID, RunID: run.id, FinalResponse: protocol.Message{ID: delta.MessageID, Role: protocol.RoleAssistant, Content: protocol.TextContent(finalText)}, StopReason: "end_turn", ModelID: run.controls.model}
	if len(run.controls.outputSchema) > 0 {
		// The admitted schema binds the final response, and the disclosed
		// fixed_result is what every structured completion carries.
		completed.Result = json.RawMessage(fixedResult)
	}
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
		call := protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID, Name: scriptedTool, RequestedBy: run.requestedBy, RespondedBy: run.respondedBy, ExecutionOwner: scriptedToolOwner}
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
			stream <- Result{Envelope: cloneEnvelope(envelope)}
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
	case protocol.TypeActionCallRequested, protocol.TypeActionCallStarted, protocol.TypeActionCallProgress, protocol.TypeActionCallCompleted, protocol.TypeActionCallFailed, protocol.TypeActionCallCancelled, protocol.TypeActionPermissionRequested, protocol.TypeUserInputRequested:
		// A run whose tool_choice excluded the scripted tool opens no call, so
		// its prompt carries no tool binding to name.
		if run.controls.callsTool {
			envelope.ToolCallID = run.toolCallID
		}
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
		subscriber <- Result{Envelope: cloneEnvelope(envelope)}
	}
	if terminal {
		for _, subscriber := range subscribers {
			close(subscriber)
		}
	}
	return nil
}

// cloneEnvelope detaches an envelope handed to a consumer from the retained
// journal: the payload slice and the sequence/timestamp pointers must not be
// shared, or a consumer's edit would corrupt replayed history.
func cloneEnvelope(envelope protocol.Envelope) protocol.Envelope {
	cloned := envelope
	if envelope.Payload != nil {
		cloned.Payload = append(json.RawMessage(nil), envelope.Payload...)
	}
	if envelope.Sequence != nil {
		sequence := *envelope.Sequence
		cloned.Sequence = &sequence
	}
	if envelope.TimestampMS != nil {
		timestamp := *envelope.TimestampMS
		cloned.TimestampMS = &timestamp
	}
	return cloned
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

type sequenceIDs struct{ value atomic.Uint64 }

func (g *sequenceIDs) NewID(kind string) string {
	return kind + "-" + strconv.FormatUint(g.value.Add(1), 10)
}

// The reference adapter implements every optional session capability the
// executable units define, so a unit's wire shape is executable rather than
// prose before any native adapter proves it.
var _ ModelLister = (*memorySession)(nil)

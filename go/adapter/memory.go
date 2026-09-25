package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/validation"
)

const defaultJournalCapacity = 64

const (
	ModelPrimary   = "reference-model-a"
	ModelSecondary = "reference-model-b"
	fixedResult    = `{"ok":true}`
	scriptedTool   = "scripted_tool"

	scriptedToolOwner = "reference-adapter"

	scriptedSource     = "reference-native"
	syntheticMCPSource = "reference-mcp"

	endpointID = "reference.memory"
)

const (
	maxProvidedTools    = 2
	providedNamePattern = "^[a-z][a-z0-9_]*$"
	providedDialect     = "https://json-schema.org/draft/2020-12/schema"
)

var providedNameRE = regexp.MustCompile(providedNamePattern)

var provideSupport = protocol.FeatureSupport{
	Level: protocol.SupportEmulated,
	Limits: map[string]json.RawMessage{
		protocol.LimitMaxTools: json.RawMessage(strconv.Itoa(maxProvidedTools)),
		"name_pattern":         mustJSON(providedNamePattern),
		"schema_dialect":       mustJSON(providedDialect),
	},
	Reason: "provided tools are called by the script and executed by the control layer through the resolve pair",
}

func scriptedCatalog() []protocol.ToolDefinition {
	return []protocol.ToolDefinition{{
		Name:           scriptedTool,
		Description:    "The deterministic scripted tool the reference adapter calls.",
		InputSchema:    json.RawMessage(`{"type":"object","properties":{"operation":{"type":"string"}}}`),
		ExecutionOwner: scriptedToolOwner,
		Source:         scriptedSource,
		Features: map[string]protocol.FeatureSupport{
			"action.tools.execute": {Level: protocol.SupportEmulated, Reason: "the reference adapter executes a fixed deterministic script"},
			"action.permissions":   {Level: protocol.SupportEmulated, Reason: "the scripted call is gated"},
		},
	}}
}

func declaredSources() []protocol.ToolSourceDescriptor {
	return []protocol.ToolSourceDescriptor{
		{ID: scriptedSource, Kind: protocol.ToolSourceNative, DisplayName: "Reference Adapter Script"},
		{
			ID: syntheticMCPSource, Kind: protocol.ToolSourceProcess, Protocol: protocol.ToolSourceMCP,
			DisplayName: "Reference Synthetic MCP Source", Endpoint: "stdio:reference-tool-source",
		},
	}
}

var attachTransports = []string{protocol.ToolSourceProcess, protocol.ToolSourceLocal}

const maxAttachedSources = 2

var attachSupport = protocol.FeatureSupport{
	Level: protocol.SupportEmulated, Modes: []string{protocol.ModeSessionOpen},
	Limits: map[string]json.RawMessage{
		protocol.LimitMaxSources: json.RawMessage(strconv.Itoa(maxAttachedSources)),
		protocol.LimitTransports: mustJSON(attachTransports),
	},
	Reason: "sources are described and published back; the reference adapter runs no client for them",
}

const CapabilityRevision = "reference-memory-v11"

var errTerminalWon = fmt.Errorf("adapter: terminal event already emitted")

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
		protocol.FeatureOpenSubscribe:   {Level: protocol.SupportNative, Reason: "the journal exists from the open, so a subscription registered there misses nothing"},
		"session.state":                 {Level: protocol.SupportNative},
		"session.message.submit":        {Level: protocol.SupportNative},
		"session.message.delivery.auto": {Level: protocol.SupportNative},

		protocol.FeatureDeliveryQueue: {Level: protocol.SupportEmulated, Reason: "a busy session reserves one second run and promotes it when the started run settles"},
		"run.streaming":               {Level: protocol.SupportNative},
		"run.status":                  {Level: protocol.SupportNative},
		"run.cancel":                  {Level: protocol.SupportEmulated, Reason: "run-target API is implemented over a one-active-run session"},
		"run.resume":                  {Level: protocol.SupportDegraded, Reason: "reattachment and replay use a bounded process-memory journal"},
		"run.reconciliation":          {Level: protocol.SupportNative},
		"run.replay":                  {Level: protocol.SupportDegraded, Reason: "older cursors can expire and no cross-process replay is claimed"},
		"action.tools":                {Level: protocol.SupportEmulated, Reason: "the reference adapter projects the scripted tool lifecycle"},
		"action.tools.execute":        {Level: protocol.SupportEmulated, Reason: "the reference adapter executes a fixed deterministic script"},

		protocol.FeatureToolsList:         {Level: protocol.SupportEmulated, Reason: "the reference catalog is the scripted tool plus the session's attached sources"},
		protocol.FeatureToolSourcesAttach: attachSupport,
		protocol.FeatureToolsProvide:      provideSupport,
		"action.permissions":              {Level: protocol.SupportEmulated, Reason: "the reference adapter exposes an interactive scripted gate"},
		"user_input":                      {Level: protocol.SupportEmulated, Reason: "the reference adapter exposes an interactive scripted gate"},

		protocol.FeatureModelSelection:     {Level: protocol.SupportEmulated, Scope: protocol.ScopeRun, Reason: "the reference adapter runs no model; it echoes a selection from a fixed catalog for one run"},
		protocol.FeatureSessionModelSwitch: {Level: protocol.SupportEmulated, Reason: "the reference adapter changes the session default within its fixed catalog"},

		protocol.FeatureModelsList:   {Level: protocol.SupportNative, Reason: "the reference adapter serves its fixed catalog, which is exactly the set its model gate admits"},
		protocol.FeatureInstructions: {Level: protocol.SupportEmulated, Reason: "instructions are prepended to the scripted text so their effect is observable"},
		protocol.FeatureToolSelection: {
			Level:  protocol.SupportEmulated,
			Scope:  protocol.ScopeRun,
			Reason: "the policy filters the scripted tool and is not retained past the run",
		},
		protocol.FeatureStructuredOutput: {
			Level:       protocol.SupportEmulated,
			Constraints: map[string]json.RawMessage{protocol.ConstraintFixedResult: json.RawMessage(fixedResult)},
			Reason:      "the scripted result is fixed, so only a schema that object satisfies is admitted",
		},
	}
	endpoint := protocol.EndpointDescriptor{ID: endpointID, Name: "Deterministic In-Memory Reference Adapter", Version: protocol.Version, Adapter: "process-memory-script"}
	return Descriptor{
		Capabilities: protocol.CapabilityDescriptor{
			Endpoint:         endpoint,
			ProtocolVersions: []string{protocol.Version},
			Profiles:         []string{protocol.Profile},
			Features:         features,

			Tools:   scriptedCatalog(),
			Sources: declaredSources(),

			Limits: &protocol.CapabilityLimits{
				MaxActiveRunsPerSession: protocol.Limit(2),
				MaxQueuedRunsPerSession: protocol.Limit(1),
			},
		},
		CapabilityRevision:         CapabilityRevision,
		Journal:                    JournalDescriptor{Scope: "session", Persistence: "process_memory", Replay: protocol.SupportDegraded, Capacity: m.capacity},
		MaxActiveRunsPerSession:    2,
		InteractiveGates:           true,
		CancellationTarget:         "run",
		CancellationImplementation: "session_emulated",
	}, nil
}

func (m *Memory) Open(ctx context.Context, request OpenRequest) (Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if request.Participant.ID == "" {
		return nil, fmt.Errorf("%w: open requires a non-empty participant id", ErrInvalidParticipant)
	}

	attached, err := admitToolSources(request)
	if err != nil {
		return nil, err
	}

	provided, err := admitProvidedTools(request, attached)
	if err != nil {
		return nil, err
	}
	id := request.SessionID
	if id == "" {
		id = protocol.SessionID(m.ids.NewID("session"))
	}
	now := m.clock.Now().UnixMilli()
	return &memorySession{
		clock: m.clock, ids: m.ids, capacity: m.capacity,
		participant: request.Participant.ID,
		state:       protocol.SessionState{SessionID: id, Status: protocol.SessionIdle, UpdatedAtMS: now, Sources: sessionSources(attached)},
		attached:    attached,
		provided:    provided,
		runs:        make(map[protocol.RunID]*memoryRun),
	}, nil
}

func admitToolSources(request OpenRequest) ([]protocol.ToolSourceAttachment, error) {

	if err := RefuseUnadvertisedToolSources(request, attachSupport); err != nil {
		return nil, err
	}
	if len(request.ToolSources) == 0 {
		return nil, nil
	}
	refuse := func(source, detail string) error {
		return &UnsupportedControlError{Feature: protocol.FeatureToolSourcesAttach, Reason: ControlUnsatisfiable, Source: source, Detail: detail}
	}
	if len(request.ToolSources) > maxAttachedSources {
		return nil, refuse(request.ToolSources[maxAttachedSources].ID, fmt.Sprintf("at most %d sources may be attached", maxAttachedSources))
	}
	declared := map[string]bool{}
	for _, source := range declaredSources() {
		declared[source.ID] = true
	}
	seen := map[string]bool{}
	for _, attachment := range request.ToolSources {
		switch {
		case attachment.ID == "" || attachment.Kind == "":
			return nil, refuse(attachment.ID, "an attachment needs an id and a kind")
		case seen[attachment.ID] || declared[attachment.ID]:

			return nil, refuse(attachment.ID, "the id already resolves to a declared or attached source")
		case !slices.Contains(attachTransports, attachment.Kind):
			return nil, refuse(attachment.ID, "kind "+attachment.Kind+" is outside the disclosed transports")
		case DuplicateEnvironmentName(attachment.Environment) != "":

			return nil, refuse(attachment.ID, "environment names "+DuplicateEnvironmentName(attachment.Environment)+" twice")
		}
		seen[attachment.ID] = true
	}
	return append([]protocol.ToolSourceAttachment(nil), request.ToolSources...), nil
}

func sessionSources(attached []protocol.ToolSourceAttachment) []protocol.ToolSourceDescriptor {
	sources := declaredSources()
	for _, attachment := range attached {
		sources = append(sources, attachment.Descriptor())
	}
	return sources
}

func admitProvidedTools(request OpenRequest, attached []protocol.ToolSourceAttachment) ([]protocol.ToolDefinition, error) {
	if err := RefuseUnadvertisedTools(request, provideSupport); err != nil {
		return nil, err
	}
	if len(request.Tools) == 0 {
		return nil, nil
	}
	refuse := func(tool, detail string) error {
		return &UnsupportedControlError{Feature: protocol.FeatureToolsProvide, Reason: ControlUnsatisfiable, Tool: tool, Detail: detail}
	}
	resolvable := map[string]bool{}
	for _, source := range sessionSources(attached) {
		resolvable[source.ID] = true
	}
	taken := map[string]bool{}
	for _, tool := range scriptedCatalog() {
		taken[tool.Name] = true
	}
	if len(request.Tools) > maxProvidedTools {
		return nil, refuse(request.Tools[maxProvidedTools].Name, fmt.Sprintf("at most %d tools may be provided", maxProvidedTools))
	}
	for _, tool := range request.Tools {
		switch {
		case tool.Name == "":
			return nil, refuse("", "a provided tool needs a name")
		case tool.ExecutionOwner != request.Participant.ID:

			return nil, refuse(tool.Name, "execution_owner must be the opening participant")
		case tool.Source != "" && !resolvable[tool.Source]:
			return nil, refuse(tool.Name, "source "+tool.Source+" resolves to no declared or attached source")
		case taken[tool.Name]:
			return nil, refuse(tool.Name, "the name already resolves to a catalog entry")
		case !providedNameRE.MatchString(tool.Name):
			return nil, refuse(tool.Name, "the name is outside the disclosed name_pattern "+providedNamePattern)
		case !admissibleDialect(tool.InputSchema):

			return nil, refuse(tool.Name, "the input schema declares a dialect outside the disclosed "+providedDialect)
		}
		taken[tool.Name] = true
	}
	return append([]protocol.ToolDefinition(nil), request.Tools...), nil
}

func admissibleDialect(schema json.RawMessage) bool {
	if len(schema) == 0 {
		return true
	}
	var declared struct {
		Schema string `json:"$schema"`
	}
	if err := json.Unmarshal(schema, &declared); err != nil {
		return false
	}
	return declared.Schema == "" || declared.Schema == providedDialect
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("adapter: encode disclosed limit: " + err.Error())
	}
	return encoded
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
	attached    []protocol.ToolSourceAttachment

	provided []protocol.ToolDefinition
	closed   bool
	active   *memoryRun

	reserved *memoryRun
	runs     map[protocol.RunID]*memoryRun
	journal  []protocol.Envelope

	settled []protocol.SettledRun
}

type scriptStage uint8

const (
	stagePermission scriptStage = iota

	stageCall
	stageInput
	stageTerminal
)

type memoryRun struct {
	id     protocol.RunID
	status protocol.RunStatus

	started bool

	queuedAdmission bool

	answered bool

	pendingInteraction protocol.InteractionID
	stage              scriptStage
	controls           admittedControls
	nextSequence       uint64
	terminal           bool
	permissionID       protocol.InteractionID
	inputID            protocol.InteractionID
	toolCallID         protocol.ToolCallID

	callID           protocol.InteractionID
	providedTool     protocol.ToolDefinition
	acknowledged     bool
	settledArm       string
	settledResult    json.RawMessage
	settledError     *protocol.ProtocolError
	settledRequestID protocol.EnvelopeID
	settlementID     protocol.EnvelopeID
	requestedBy      protocol.ParticipantID
	respondedBy      protocol.ParticipantID
	subscribers      []chan Result
}

func (s *memorySession) Submit(ctx context.Context, request protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, EventStream, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}

	controls, err := s.admitControls(request)
	if err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}
	if request.SessionID == "" || len(request.Messages) == 0 {
		return protocol.MessageSubmitResponse{}, nil, ErrInvalidSubmission
	}
	if request.Delivery != "" && request.Delivery != protocol.DeliveryAuto && request.Delivery != protocol.DeliveryQueue {
		if key := deliveryFeature(request.Delivery); key != "" {
			return protocol.MessageSubmitResponse{}, nil, &UnsupportedControlError{Feature: key, Reason: ControlUnadvertised}
		}
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
	busy := s.active != nil && !s.active.terminal
	if busy && s.reserved != nil && !s.reserved.terminal {

		s.mu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, ErrRunActive
	}
	if !busy && request.Delivery != protocol.DeliveryQueue && controls.model == "" {
		controls.model = s.state.CurrentModelID
	}
	run := &memoryRun{
		id: protocol.RunID(s.ids.NewID("run")), status: protocol.RunRunning,
		nextSequence: 1, stage: stagePermission, controls: controls,
		permissionID: protocol.InteractionID(s.ids.NewID("permission")),
		inputID:      protocol.InteractionID(s.ids.NewID("input")),
		toolCallID:   protocol.ToolCallID(s.ids.NewID("tool-call")),
		requestedBy:  endpointID, respondedBy: s.participant,
	}
	if !controls.callsTool {

		run.stage = stageInput
	} else if len(s.provided) > 0 {

		run.stage = stageCall
		run.callID = protocol.InteractionID(s.ids.NewID("call"))
		run.providedTool = controls.elected
	}
	stream := make(chan Result, 32)
	run.subscribers = append(run.subscribers, stream)
	s.runs[run.id] = run

	reservation := busy || request.Delivery == protocol.DeliveryQueue
	run.queuedAdmission = reservation
	if reservation {

		run.status = protocol.RunQueued
	}
	if busy {
		s.reserved = run
	} else {
		s.active = run
	}
	s.refreshStateLocked()
	s.state.UpdatedAtMS = s.clock.Now().UnixMilli()
	s.mu.Unlock()

	messageIDs := make([]protocol.MessageID, len(request.Messages))
	for i := range request.Messages {
		messageIDs[i] = request.Messages[i].ID
		if messageIDs[i] == "" {
			messageIDs[i] = protocol.MessageID(s.ids.NewID("message"))
		}
	}
	requested := request.Delivery
	if requested == "" {
		requested = protocol.DeliveryAuto
	}
	admission := protocol.MessageSubmitResponse{
		SessionID: s.state.SessionID, Accepted: true,
		SubmissionID:      protocol.SubmissionID(s.ids.NewID("submission")),
		RequestedDelivery: requested, EffectiveDelivery: protocol.DeliveryStart,
		DeliveryResolution: "session_idle", Admission: protocol.AdmissionStarted,
		RunID: run.id, Status: protocol.RunRunning, ModelID: controls.model, MessageIDs: messageIDs,
	}
	if reservation {
		admission.EffectiveDelivery = protocol.EffectiveDeliveryQueue
		admission.Admission = protocol.AdmissionQueued
		admission.Status = protocol.RunQueued
		if busy {
			admission.DeliveryResolution = "session_busy"
		}
	}

	defer s.answerRun(run)
	if busy {

		return admission, stream, nil
	}
	if err := s.emitInitial(run); err != nil {
		return protocol.MessageSubmitResponse{}, stream, err
	}
	return admission, stream, nil
}

func (s *memorySession) answerRun(run *memoryRun) {
	s.mu.Lock()
	run.answered = true
	s.refreshStateLocked()
	s.mu.Unlock()
}

func live(run *memoryRun) bool { return run != nil && !run.terminal }

func (s *memorySession) refreshStateLocked() {

	var entries []protocol.ActiveRun
	position := 0
	var started *memoryRun
	for _, run := range []*memoryRun{s.active, s.reserved} {
		if !live(run) || !run.answered {
			continue
		}
		if !reservationOf(run) {
			entries = append(entries, s.entryLocked(run, 0))
			started = run
			continue
		}
		position++
		entries = append(entries, s.entryLocked(run, position))
	}
	s.state.ActiveRuns = entries

	if len(s.settled) > 0 {
		s.state.AsOf = &protocol.SessionCapture{Settled: s.settled}
	}
	switch {
	case started != nil:

		s.state.Status = protocol.SessionRunning
		if started.status == protocol.RunWaitingForInput || started.pendingInteraction != "" {
			s.state.Status = protocol.SessionWaitingForInput
		}
		s.state.ActiveRunID = started.id
	case len(entries) > 0:

		s.state.Status = protocol.SessionQueued
		s.state.ActiveRunID = ""
	default:
		s.state.Status = protocol.SessionIdle
		s.state.ActiveRunID = ""
	}
}

func reservationOf(run *memoryRun) bool { return run.queuedAdmission && !run.started }

func (s *memorySession) entryLocked(run *memoryRun, position int) protocol.ActiveRun {
	sequence := run.nextSequence - 1
	status := run.status
	if reservationOf(run) {

		status = protocol.RunQueued
	}
	entry := protocol.ActiveRun{
		RunID: run.id, Status: status, Relationship: protocol.RelationshipPrimary,
		AsOfSequence: &sequence, PendingInteractions: pendingInteractions(run),
		AcknowledgedInteractions: acknowledgedInteractions(run),
	}
	if position > 0 {
		entry.QueuePosition = &position
	}
	return entry
}

func pendingInteractions(run *memoryRun) []protocol.InteractionID {
	if run.pendingInteraction == "" {
		return nil
	}
	return []protocol.InteractionID{run.pendingInteraction}
}

func acknowledgedInteractions(run *memoryRun) []protocol.InteractionID {
	if !run.acknowledged || run.pendingInteraction == "" || run.pendingInteraction != run.callID {
		return nil
	}
	return []protocol.InteractionID{run.callID}
}

func (s *memorySession) emitInitial(run *memoryRun) error {

	s.mu.Lock()
	model := run.controls.model
	if model == "" {
		model = s.state.CurrentModelID
	}
	run.controls.model = model
	run.started = true
	run.status = protocol.RunRunning
	s.mu.Unlock()
	started := protocol.RunStartedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunRunning, ModelID: model, StartedAtMS: s.clock.Now().UnixMilli()}
	if err := s.emit(run, protocol.TypeRunStarted, started, false); err != nil {
		return err
	}

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
	if run.callID != "" {

		provided := protocol.ActionCallPayload{
			InteractionID: run.callID, SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID,
			Name: run.providedTool.Name, ArgumentsJSON: json.RawMessage(`{"operation":"golden"}`),
			RequestedBy: run.requestedBy, RespondedBy: run.respondedBy,
			ExecutionOwner: run.providedTool.ExecutionOwner, Source: run.providedTool.Source,
		}
		return s.emit(run, protocol.TypeActionCallRequested, provided, false)
	}
	call := protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID, Name: scriptedTool, ArgumentsJSON: json.RawMessage(`{"operation":"golden"}`), RequestedBy: run.requestedBy, ExecutionOwner: scriptedToolOwner, Source: scriptedSource}
	if err := s.emit(run, protocol.TypeActionCallRequested, call, false); err != nil {
		return err
	}
	permission := protocol.PermissionRequestedPayload{InteractionID: run.permissionID, SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID, Title: "Allow scripted tool", Description: "The golden script requires approval.", Choices: []protocol.PermissionChoice{{ID: "approve", Label: "Approve"}, {ID: "deny", Label: "Deny"}}, ArgumentsJSON: call.ArgumentsJSON, RequestedBy: run.requestedBy, RespondedBy: run.respondedBy}
	return s.emit(run, protocol.TypeActionPermissionRequested, permission, false)
}

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

	s.mu.Lock()
	if !run.terminal {
		run.status = protocol.RunWaitingForInput
		s.state.UpdatedAtMS = status.UpdatedAtMS
	}
	s.mu.Unlock()
	return s.emit(run, protocol.TypeRunStatusUpdated, status, false)
}

type admittedControls struct {
	model        string
	instructions string
	choice       *protocol.ToolChoice
	outputSchema json.RawMessage
	elected      protocol.ToolDefinition
	callsTool    bool
}

func modelCatalog() []protocol.ModelDescriptor {
	return []protocol.ModelDescriptor{
		{ID: ModelPrimary, DisplayName: "Reference Model A", ProviderID: "reference", ContextWindow: 8192, Default: true},
		{ID: ModelSecondary, DisplayName: "Reference Model B", ProviderID: "reference", ContextWindow: 8192},
	}
}

func providerCatalog() []protocol.ProviderDescriptor {
	return []protocol.ProviderDescriptor{
		{ID: "reference", DisplayName: "Reference Provider", Wire: protocol.WireOpenAIChatCompletions, Kind: protocol.ProviderDirect},
	}
}

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

	return Catalog{
		Revision: CapabilityRevision,
		Models: protocol.ModelsResponse{
			SessionID:      s.state.SessionID,
			CurrentModelID: s.state.CurrentModelID,
			Models:         modelCatalog(),
			Providers:      providerCatalog(),
		},
	}, nil
}

func (s *memorySession) SwitchModel(ctx context.Context, request protocol.SessionModelSwitchRequest) (protocol.SessionModelSwitchResponse, protocol.SessionState, error) {
	if err := ctx.Err(); err != nil {
		return protocol.SessionModelSwitchResponse{}, protocol.SessionState{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return protocol.SessionModelSwitchResponse{}, protocol.SessionState{}, ErrSessionClosed
	}
	if request.SessionID != s.state.SessionID {
		return protocol.SessionModelSwitchResponse{}, protocol.SessionState{}, fmt.Errorf("%w: switch names session %q", ErrInvalidSubmission, request.SessionID)
	}
	if request.ModelID != ModelPrimary && request.ModelID != ModelSecondary {
		return protocol.SessionModelSwitchResponse{}, protocol.SessionState{}, &ModelNotFoundError{ModelID: request.ModelID}
	}
	previous := s.state.CurrentModelID
	s.state.CurrentModelID = request.ModelID
	s.state.UpdatedAtMS = s.clock.Now().UnixMilli()
	return protocol.SessionModelSwitchResponse{
		SessionID: s.state.SessionID, ModelID: request.ModelID, PreviousModelID: previous,
	}, s.cloneStateLocked(), nil
}

func (s *memorySession) catalog() []string {
	names := make([]string, 0, 1+len(s.provided))
	for _, tool := range s.callable() {
		names = append(names, tool.Name)
	}
	return names
}

func (s *memorySession) callable() []protocol.ToolDefinition {
	tools := make([]protocol.ToolDefinition, 0, 1+len(s.provided))
	tools = append(tools, scriptedCatalog()...)
	return append(tools, s.provided...)
}

func (s *memorySession) elect(policy *protocol.ToolChoice) (protocol.ToolDefinition, bool) {
	candidates := s.callable()
	if len(s.provided) > 0 {
		candidates = s.provided
	}
	for _, tool := range candidates {
		if policy == nil || policy.Permits(tool.Name, s.catalog(), true) {
			return tool, true
		}
	}
	return protocol.ToolDefinition{}, false
}

func (s *memorySession) admitControls(request protocol.MessageSubmitRequest) (admittedControls, error) {
	controls := admittedControls{callsTool: true}

	var refusals []keyedRefusal
	refuse := func(key string, err error) { refusals = append(refusals, keyedRefusal{key: key, err: err}) }
	if request.Instructions != nil {
		controls.instructions = *request.Instructions
	}
	if request.ModelID != nil {

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
		}
	}

	controls.elected, controls.callsTool = s.elect(controls.choice)
	if len(request.OutputSchema) > 0 {
		compiled, err := validation.CompileOutputSchema(request.OutputSchema)
		switch {
		case err != nil:
			refuse(protocol.FeatureStructuredOutput, &UnsupportedControlError{Feature: protocol.FeatureStructuredOutput, Reason: ControlUnsatisfiable, Field: "output_schema", Detail: err.Error()})

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
		return s.cloneStateLocked(), ErrSessionClosed
	}
	return s.cloneStateLocked(), nil
}

func (s *memorySession) cloneStateLocked() protocol.SessionState {
	state := s.state
	state.ActiveRuns = append([]protocol.ActiveRun(nil), s.state.ActiveRuns...)
	for i := range state.ActiveRuns {
		entry := state.ActiveRuns[i]
		if entry.AsOfSequence != nil {
			sequence := *entry.AsOfSequence
			state.ActiveRuns[i].AsOfSequence = &sequence
		}
		if entry.QueuePosition != nil {
			position := *entry.QueuePosition
			state.ActiveRuns[i].QueuePosition = &position
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

func (s *memorySession) Tools(ctx context.Context, request protocol.ToolsListRequest) (ToolCatalog, error) {
	if err := ctx.Err(); err != nil {
		return ToolCatalog{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ToolCatalog{}, ErrSessionClosed
	}
	if request.SessionID != "" && request.SessionID != s.state.SessionID {
		return ToolCatalog{}, ErrRunNotFound
	}
	sources := declaredSources()
	if request.SessionID != "" {
		sources = sessionSources(s.attached)
	}

	tools := scriptedCatalog()
	if request.SessionID != "" {

		tools = append(tools, s.provided...)
	}
	return ToolCatalog{Revision: CapabilityRevision, Tools: protocol.ToolsListResponse{
		SessionID: request.SessionID,
		Sources:   sources,
		Tools:     tools,
	}}, nil
}

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
	if !run.started {
		s.mu.Unlock()
		return ErrInteractionNotFound
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

		if len(resolution.Input.Answers) != 1 || ValidateInputAnswer(goldenInputQuestions()[0], resolution.Input.Answers[0]) != nil {
			s.mu.Unlock()
			return ErrInvalidResolution
		}
		run.stage = stageTerminal
	} else if stage == stageCall {

		s.mu.Unlock()
		return ErrInteractionNotFound
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
		call := protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID, Name: scriptedTool, RequestedBy: run.requestedBy, RespondedBy: run.respondedBy, ExecutionOwner: scriptedToolOwner, Source: scriptedSource}
		if err := s.emit(run, protocol.TypeActionCallCancelled, call, false); err != nil {
			return err
		}
		failure := protocol.RunFailedPayload{SessionID: s.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: "permission_denied", Message: "scripted tool permission denied"}}
		return s.emit(run, protocol.TypeRunFailed, failure, true)
	}
	call := protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID, Name: scriptedTool, ArgumentsJSON: json.RawMessage(`{"operation":"golden"}`), RequestedBy: run.requestedBy, ExecutionOwner: scriptedToolOwner, Source: scriptedSource}
	if err := s.emit(run, protocol.TypeActionCallStarted, call, false); err != nil {
		return err
	}

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

		completed.Result = json.RawMessage(fixedResult)
	}
	if err := s.emit(run, protocol.TypeRunCompleted, completed, true); err != nil && err != errTerminalWon {
		return err
	}
	return nil
}

func (s *memorySession) ResolveCall(ctx context.Context, resolution CallResolution) (protocol.ActionCallResolveResponse, error) {
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
		return protocol.ActionCallResolveResponse{}, ErrSessionClosed
	}
	run := s.runs[request.RunID]

	if run == nil || run.callID == "" || request.InteractionID != run.callID || request.SessionID != s.state.SessionID {
		s.mu.Unlock()
		return refuse(protocol.ReasonUnknownInteraction, "")
	}

	if request.RespondedBy != run.respondedBy || request.RequestedBy != run.requestedBy || request.ToolCallID != run.toolCallID {
		s.mu.Unlock()
		return refuse(protocol.ReasonWrongResponder, "")
	}
	arm := request.Arm()
	if arm == "" {
		s.mu.Unlock()
		return refuse(protocol.ReasonUnknownInteraction, "")
	}

	if run.settledArm != "" {
		settlement := run.settlementID
		if settlement == "" {
			settlement = run.settledRequestID
		}
		if arm == protocol.ResolveArmAcknowledge && run.settlementID == "" {

			s.mu.Unlock()
			return refuse(protocol.ReasonLateAcknowledgement, "")
		}
		s.mu.Unlock()
		return refuse(protocol.ReasonAlreadyResolved, settlement)
	}
	if run.terminal || run.stage != stageCall {
		s.mu.Unlock()
		return refuse(protocol.ReasonAlreadyResolved, run.settlementID)
	}
	if arm == protocol.ResolveArmAcknowledge && run.acknowledged {

		s.mu.Unlock()
		return refuse(protocol.ReasonRepeatedAcknowledgement, "")
	}

	answer.Accepted = true
	if arm == protocol.ResolveArmAcknowledge {
		run.acknowledged = true
		s.mu.Unlock()

		call := s.callPayload(run, resolution.RequestID)
		call.ArgumentsJSON = json.RawMessage(`{"operation":"golden"}`)
		if err := s.emit(run, protocol.TypeActionCallStarted, call, false); err != nil && err != errTerminalWon {
			return protocol.ActionCallResolveResponse{}, err
		}
		return answer, nil
	}

	run.settledArm = arm
	run.settledRequestID = resolution.RequestID
	run.settledResult = request.Result
	run.settledError = request.Error
	acknowledged := run.acknowledged
	run.stage = stageInput
	s.mu.Unlock()

	if err := s.settleCall(run, acknowledged); err != nil && err != errTerminalWon {
		return protocol.ActionCallResolveResponse{}, err
	}
	return answer, nil
}

func (s *memorySession) callPayload(run *memoryRun, requestID protocol.EnvelopeID) protocol.ActionCallPayload {
	return protocol.ActionCallPayload{
		InteractionID: run.callID, RequestID: requestID,
		SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID,
		Name: run.providedTool.Name, RequestedBy: run.requestedBy, RespondedBy: run.respondedBy,
		ExecutionOwner: run.providedTool.ExecutionOwner, Source: run.providedTool.Source,
	}
}

func (s *memorySession) settleCall(run *memoryRun, acknowledged bool) error {
	if !acknowledged {
		started := s.callPayload(run, run.settledRequestID)
		started.ArgumentsJSON = json.RawMessage(`{"operation":"golden"}`)
		if err := s.emit(run, protocol.TypeActionCallStarted, started, false); err != nil {
			return err
		}
	}
	terminal := s.callPayload(run, run.settledRequestID)
	typ := protocol.TypeActionCallCompleted
	if run.settledArm == protocol.ResolveArmError {
		typ = protocol.TypeActionCallFailed
		terminal.Error = run.settledError
	} else {
		terminal.Result = run.settledResult
	}
	if err := s.emit(run, typ, terminal, false); err != nil {
		return err
	}
	return s.requestInput(run)
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
	reservation := !run.started
	run.status = protocol.RunCancelling
	s.mu.Unlock()

	if reservation {

		cancelled := protocol.RunCancelledPayload{SessionID: s.state.SessionID, RunID: run.id, Reason: "reservation cancelled before promotion"}
		_ = s.emit(run, protocol.TypeRunCancelled, cancelled, true)
		return protocol.RunCancelResponse{SessionID: s.state.SessionID, RunID: runID, Accepted: true, Status: protocol.RunCancelling}, nil
	}

	status := protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunCancelling, UpdatedAtMS: s.clock.Now().UnixMilli()}
	if err := s.emit(run, protocol.TypeRunStatusUpdated, status, false); err != nil && err != errTerminalWon {
		return protocol.RunCancelResponse{}, err
	}

	if run.stage == stageCall {

		cancelled := s.callPayload(run, "")
		_ = s.emit(run, protocol.TypeActionCallCancelled, cancelled, false)
	}
	if run.stage == stagePermission {
		reason := protocol.ProtocolError{Code: "run_cancelled", Message: "run cancellation closed the permission request"}
		resolved := protocol.PermissionResolvedPayload{InteractionID: run.permissionID, RequestedBy: run.requestedBy, RespondedBy: run.respondedBy, SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID, Outcome: protocol.InteractionCancelled, Reason: &reason}
		_ = s.emit(run, protocol.TypeActionPermissionResolved, resolved, false)
		call := protocol.ActionCallPayload{SessionID: s.state.SessionID, RunID: run.id, ToolCallID: run.toolCallID, Name: scriptedTool, RequestedBy: run.requestedBy, RespondedBy: run.respondedBy, ExecutionOwner: scriptedToolOwner, Source: scriptedSource}
		_ = s.emit(run, protocol.TypeActionCallCancelled, call, false)
	} else if run.stage == stageInput {
		resolved := protocol.UserInputResolvedPayload{InteractionID: run.inputID, RequestedBy: run.requestedBy, RespondedBy: run.respondedBy, SessionID: s.state.SessionID, RunID: run.id, Status: protocol.InputCancelled}
		_ = s.emit(run, protocol.TypeUserInputResolved, resolved, false)
	}
	cancelled := protocol.RunCancelledPayload{SessionID: s.state.SessionID, RunID: run.id, Reason: "cancel confirmed"}
	_ = s.emit(run, protocol.TypeRunCancelled, cancelled, true)

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
	state := s.cloneStateLocked()
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
	if live(s.active) || live(s.reserved) {

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

func (s *memorySession) emit(run *memoryRun, typ protocol.EnvelopeType, payload any, terminal bool) error {
	promoted, err := s.publish(run, typ, payload, terminal)
	if err != nil {
		return err
	}
	if promoted != nil {
		return s.emitInitial(promoted)
	}
	return nil
}

func (s *memorySession) publish(run *memoryRun, typ protocol.EnvelopeType, payload any, terminal bool) (*memoryRun, error) {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()

	s.mu.Lock()
	if run.terminal {
		s.mu.Unlock()
		return nil, errTerminalWon
	}
	now := s.clock.Now().UnixMilli()
	envelope, err := protocol.NewEnvelope(typ, protocol.EnvelopeID(s.ids.NewID("event")), payload)
	if err != nil {
		s.mu.Unlock()
		return nil, err
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
	switch typ {
	case protocol.TypeActionPermissionRequested:
		run.pendingInteraction = run.permissionID
	case protocol.TypeUserInputRequested:
		run.pendingInteraction = run.inputID
	case protocol.TypeActionPermissionResolved, protocol.TypeUserInputResolved:
		run.pendingInteraction = ""
	case protocol.TypeActionCallRequested:
		if run.callID != "" {
			run.pendingInteraction = run.callID
		}
	case protocol.TypeActionCallCompleted, protocol.TypeActionCallFailed, protocol.TypeActionCallCancelled:
		if run.callID != "" && run.pendingInteraction == run.callID {

			run.pendingInteraction = ""
			run.settlementID = envelope.ID
		}
	}
	var promoted *memoryRun
	if terminal {
		run.terminal = true
		run.pendingInteraction = ""
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
		if s.reserved == run {
			s.reserved = nil
		}
		if s.active == nil && s.reserved != nil && !s.reserved.terminal {
			promoted, s.reserved = s.reserved, nil
			s.active = promoted
		}
		s.settled = append(s.settled, protocol.SettledRun{RunID: run.id, Sequence: sequence})
	}
	s.refreshStateLocked()
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
	return promoted, nil
}

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

var _ ToolLister = (*memorySession)(nil)

var _ ModelLister = (*memorySession)(nil)

var _ CallResolver = (*memorySession)(nil)

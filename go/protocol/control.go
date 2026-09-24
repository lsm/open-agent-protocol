package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

type EndpointDescriptor struct {
	ID      EndpointID `json:"id"`
	Name    string     `json:"name,omitempty"`
	Version string     `json:"version,omitempty"`
	Adapter string     `json:"adapter,omitempty"`
}

type InitializeRequest struct {
	ProtocolVersions []string     `json:"protocol_versions"`
	Profiles         []string     `json:"profiles"`
	Participant      *Participant `json:"participant,omitempty"`
}

type InitializeResponse struct {
	ProtocolVersion string             `json:"protocol_version"`
	Profile         string             `json:"profile"`
	Endpoint        EndpointDescriptor `json:"endpoint"`
}

type Participant struct {
	ID      ParticipantID `json:"id"`
	Name    string        `json:"name,omitempty"`
	Version string        `json:"version,omitempty"`
}

type SupportLevel string

const (
	SupportNative      SupportLevel = "native"
	SupportEmulated    SupportLevel = "emulated"
	SupportDegraded    SupportLevel = "degraded"
	SupportUnavailable SupportLevel = "unavailable"
)

type FeatureSupport struct {
	Level       SupportLevel               `json:"level"`
	Reason      string                     `json:"reason,omitempty"`
	Scope       string                     `json:"scope,omitempty"`
	Modes       []string                   `json:"modes,omitempty"`
	Constraints map[string]json.RawMessage `json:"constraints,omitempty"`
	Limits      map[string]json.RawMessage `json:"limits,omitempty"`
}

const (
	LimitMaxSources = "max_sources"
	LimitTransports = "transports"
)

const (
	LimitMaxTools      = "max_tools"
	LimitNamePattern   = "name_pattern"
	LimitSchemaDialect = "schema_dialect"
)

func (f FeatureSupport) MaxTools() (int, bool) {
	raw, ok := f.Limits[LimitMaxTools]
	if !ok {
		return 0, false
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil || value < 1 {
		return 0, false
	}
	return value, true
}

func (f FeatureSupport) NamePattern() (string, bool) {
	return f.limitString(LimitNamePattern)
}

func (f FeatureSupport) SchemaDialect() (string, bool) {
	return f.limitString(LimitSchemaDialect)
}

func (f FeatureSupport) limitString(name string) (string, bool) {
	raw, ok := f.Limits[name]
	if !ok {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" {
		return "", false
	}
	return value, true
}

func (f FeatureSupport) MaxSources() (int, bool) {
	raw, ok := f.Limits[LimitMaxSources]
	if !ok {
		return 0, false
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil || value < 1 {
		return 0, false
	}
	return value, true
}

func (f FeatureSupport) Transports() ([]string, bool) {
	raw, ok := f.Limits[LimitTransports]
	if !ok {
		return nil, false
	}
	var value []string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	kinds := make([]string, 0, len(value))
	for _, kind := range value {
		if IsToolSourceKind(kind) {
			kinds = append(kinds, kind)
		}
	}
	if len(kinds) == 0 {
		return nil, false
	}
	return kinds, true
}

const (
	FeatureModelSelection = "run.model_selection"
	FeatureInstructions   = "run.instructions"

	FeatureDeliveryQueue    = "session.message.delivery.queue"
	FeatureDeliverySteer    = "session.message.delivery.steer"
	FeatureDeliveryBTW      = "session.message.delivery.btw"
	FeatureToolSelection    = "run.tool_selection"
	FeatureStructuredOutput = "run.structured_output"
)

const (
	FeatureToolsList         = "action.tools.list"
	FeatureToolSourcesAttach = "action.tool_sources.attach"
)

const FeatureOpenSubscribe = "session.open.subscribe"

const FeatureToolsProvide = "action.tools.provide"

const (
	ModeSessionOpen = "session_open"
	ModeRemote      = "remote"
)

func (f FeatureSupport) DisclosesMode(mode string) bool {
	for _, disclosed := range f.Modes {
		if disclosed == mode {
			return true
		}
	}
	return false
}

const (
	ScopeRun     = "run"
	ScopeSession = "session"
	ScopeRestart = "restart"
)

const ConstraintFixedResult = "fixed_result"

type CapabilityLayer struct {
	Features               map[string]FeatureSupport `json:"features,omitempty"`
	RequestedDeliveryModes []RequestedDeliveryMode   `json:"requested_delivery_modes,omitempty"`
	EffectiveDeliveryModes []EffectiveDeliveryMode   `json:"effective_delivery_modes,omitempty"`
	Tools                  []ToolDefinition          `json:"tools,omitempty"`
	Sources                []ToolSourceDescriptor    `json:"sources,omitempty"`
}

type CapabilityDescriptor struct {
	Endpoint         EndpointDescriptor         `json:"endpoint"`
	ProtocolVersions []string                   `json:"protocol_versions,omitempty"`
	Profiles         []string                   `json:"profiles,omitempty"`
	Bindings         []Binding                  `json:"bindings,omitempty"`
	Features         map[string]FeatureSupport  `json:"features,omitempty"`
	Layers           map[string]CapabilityLayer `json:"layers,omitempty"`
	Tools            []ToolDefinition           `json:"tools,omitempty"`
	Sources          []ToolSourceDescriptor     `json:"sources,omitempty"`
	Degradation      []Degradation              `json:"degradation,omitempty"`
	Limits           *CapabilityLimits          `json:"limits,omitempty"`
}

type CapabilityLimits struct {
	MaxActiveRunsPerSession *int `json:"max_active_runs_per_session,omitempty"`
	MaxQueuedRunsPerSession *int `json:"max_queued_runs_per_session,omitempty"`
}

func Limit(value int) *int { return &value }

func (d CapabilityDescriptor) EffectiveSupport(key string) (FeatureSupport, bool) {
	if support, ok := d.Features[key]; ok {
		return support, true
	}
	names := make([]string, 0, len(d.Layers))
	for name := range d.Layers {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if support, ok := d.Layers[name].Features[key]; ok {
			return support, true
		}
	}
	return FeatureSupport{}, false
}

func (d CapabilityDescriptor) EffectiveSources() []ToolSourceDescriptor {
	sources := append([]ToolSourceDescriptor(nil), d.Sources...)
	names := make([]string, 0, len(d.Layers))
	for name := range d.Layers {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		sources = append(sources, d.Layers[name].Sources...)
	}
	return sources
}

type Binding struct {
	Kind          string `json:"kind"`
	Serialization string `json:"serialization,omitempty"`
}

type Degradation struct {
	Feature string       `json:"feature"`
	From    SupportLevel `json:"from,omitempty"`
	To      SupportLevel `json:"to"`
	Mode    string       `json:"mode,omitempty"`
	Reason  string       `json:"reason"`
}

type CapabilitiesRequest struct{}

type CapabilitiesResponse = CapabilityDescriptor

type CapabilitiesUpdated struct {
	PreviousRevision string `json:"previous_revision"`
	Reason           string `json:"reason,omitempty"`
}

const FeatureModelsList = "models.list"

const (
	FeatureSessionModelSwitch = "session.model.switch"
	FeatureProvidersAttach    = "action.providers.attach"
	ModeSessionLive           = "session_live"
)

type ModelsRequest struct {
	SessionID             SessionID `json:"session_id"`
	AllowDegradedFeatures []string  `json:"allow_degraded_features,omitempty"`
}

func (r ModelsRequest) AllowsDegraded(key string) bool {
	for _, allowed := range r.AllowDegradedFeatures {
		if allowed == key {
			return true
		}
	}
	return false
}

type ModelDescriptor struct {
	ID            string                    `json:"id"`
	DisplayName   string                    `json:"display_name,omitempty"`
	ProviderID    string                    `json:"provider_id,omitempty"`
	ContextWindow int64                     `json:"context_window,omitempty"`
	Features      map[string]FeatureSupport `json:"features,omitempty"`
	Default       bool                      `json:"default,omitempty"`
}

type ModelEventPosition struct {
	RunID           RunID      `json:"run_id,omitempty"`
	Sequence        uint64     `json:"sequence,omitempty"`
	SwitchRequestID EnvelopeID `json:"switch_request_id,omitempty"`
}

const (
	WireOpenAIResponses       = "openai-responses"
	WireAnthropicMessages     = "anthropic-messages"
	WireOpenAIChatCompletions = "openai-chat-completions"
)

const (
	ProviderDirect  = "direct"
	ProviderGateway = "gateway"
)

type ProviderDescriptor struct {
	ID                 string `json:"id"`
	DisplayName        string `json:"display_name,omitempty"`
	Wire               string `json:"wire,omitempty"`
	Kind               string `json:"kind,omitempty"`
	Endpoint           string `json:"endpoint,omitempty"`
	ServiceID          string `json:"service_id,omitempty"`
	UpstreamProviderID string `json:"upstream_provider_id,omitempty"`
}

type ModelsResponse struct {
	SessionID      SessionID            `json:"session_id"`
	CurrentModelID string               `json:"current_model_id,omitempty"`
	Models         []ModelDescriptor    `json:"models"`
	Providers      []ProviderDescriptor `json:"providers,omitempty"`
	AsOfModelEvent *ModelEventPosition  `json:"as_of_model_event,omitempty"`
}

type SessionStatus string

const (
	SessionIdle            SessionStatus = "idle"
	SessionQueued          SessionStatus = "queued"
	SessionRunning         SessionStatus = "running"
	SessionWaitingForInput SessionStatus = "waiting_for_input"
	SessionClosed          SessionStatus = "closed"
	SessionError           SessionStatus = "error"
)

type ToolSourceAttachment struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	DisplayName string   `json:"display_name,omitempty"`
	Protocol    string   `json:"protocol,omitempty"`
	Endpoint    string   `json:"endpoint,omitempty"`
	Command     string   `json:"command,omitempty"`
	Args        []string `json:"args,omitempty"`
	Environment []string `json:"environment,omitempty"`
}

func (a ToolSourceAttachment) Descriptor() ToolSourceDescriptor {
	return ToolSourceDescriptor{ID: a.ID, Kind: a.Kind, DisplayName: a.DisplayName, Protocol: a.Protocol, Endpoint: a.Endpoint}
}

type SessionOpenRequest struct {
	SessionID             SessionID                  `json:"session_id,omitempty"`
	Subscribe             bool                       `json:"subscribe,omitempty"`
	Message               *OpenMessage               `json:"message,omitempty"`
	Metadata              map[string]json.RawMessage `json:"metadata,omitempty"`
	ToolSources           []ToolSourceAttachment     `json:"tool_sources,omitempty"`
	Tools                 []ToolDefinition           `json:"tools,omitempty"`
	AllowDegradedFeatures []string                   `json:"allow_degraded_features,omitempty"`
	Recovery              *RecoveryMetadata          `json:"recovery,omitempty"`
}

type OpenMessage struct {
	Messages              []Message                  `json:"messages"`
	Delivery              RequestedDeliveryMode      `json:"delivery"`
	ModelID               *string                    `json:"model_id,omitempty"`
	Instructions          *string                    `json:"instructions,omitempty"`
	ToolChoice            json.RawMessage            `json:"tool_choice,omitempty"`
	OutputSchema          json.RawMessage            `json:"output_schema,omitempty"`
	AllowDegradedFeatures []string                   `json:"allow_degraded_features,omitempty"`
	Metadata              map[string]json.RawMessage `json:"metadata,omitempty"`
}

func (m OpenMessage) Submit(session SessionID) MessageSubmitRequest {
	return MessageSubmitRequest{
		SessionID:             session,
		Messages:              m.Messages,
		Delivery:              m.Delivery,
		ModelID:               m.ModelID,
		Instructions:          m.Instructions,
		ToolChoice:            m.ToolChoice,
		OutputSchema:          m.OutputSchema,
		AllowDegradedFeatures: m.AllowDegradedFeatures,
		Metadata:              m.Metadata,
	}
}

func (r SessionOpenRequest) AllowsDegraded(key string) bool {
	for _, allowed := range r.AllowDegradedFeatures {
		if allowed == key {
			return true
		}
	}
	return false
}

type SessionOpenResponse = SessionState

type SessionStateRequest struct {
	SessionID SessionID `json:"session_id"`
}

type SessionModelSwitchRequest struct {
	SessionID             SessionID `json:"session_id"`
	ModelID               string    `json:"model_id"`
	AllowDegradedFeatures []string  `json:"allow_degraded_features,omitempty"`
}

func (r SessionModelSwitchRequest) AllowsDegraded(key string) bool {
	for _, allowed := range r.AllowDegradedFeatures {
		if allowed == key {
			return true
		}
	}
	return false
}

type SessionModelSwitchResponse struct {
	SessionID       SessionID `json:"session_id"`
	ModelID         string    `json:"model_id"`
	PreviousModelID string    `json:"previous_model_id,omitempty"`
}

type ProviderAttachment struct {
	ID         string `json:"id"`
	ProviderID string `json:"provider_id"`
	ServiceID  string `json:"service_id,omitempty"`
}

type SessionProviderAttachRequest struct {
	SessionID             SessionID          `json:"session_id"`
	Provider              ProviderAttachment `json:"provider"`
	AllowDegradedFeatures []string           `json:"allow_degraded_features,omitempty"`
}

func (r SessionProviderAttachRequest) AllowsDegraded(key string) bool {
	for _, allowed := range r.AllowDegradedFeatures {
		if allowed == key {
			return true
		}
	}
	return false
}

type SessionProviderAttachResponse struct {
	SessionID  SessionID `json:"session_id"`
	ProviderID string    `json:"provider_id"`
}

type SessionState struct {
	SessionID        SessionID                  `json:"session_id"`
	Status           SessionStatus              `json:"status"`
	ActiveRunID      RunID                      `json:"active_run_id,omitempty"`
	ActiveRuns       []ActiveRun                `json:"active_runs,omitempty"`
	CurrentModelID   string                     `json:"current_model_id,omitempty"`
	TranscriptCursor string                     `json:"transcript_cursor,omitempty"`
	UpdatedAtMS      int64                      `json:"updated_at_ms,omitempty"`
	Metadata         map[string]json.RawMessage `json:"metadata,omitempty"`
	Sources          []ToolSourceDescriptor     `json:"sources,omitempty"`
	Recovery         *RecoveryMetadata          `json:"recovery,omitempty"`
	AsOf             *SessionCapture            `json:"as_of,omitempty"`
}

const RelationshipPrimary = "primary"

type ActiveRun struct {
	RunID                  RunID           `json:"run_id"`
	Status                 RunStatus       `json:"status"`
	Relationship           string          `json:"relationship"`
	QueuePosition          *int            `json:"queue_position,omitempty"`
	AsOfSequence           *uint64         `json:"as_of_sequence,omitempty"`
	AdmittedSubmitRequests []EnvelopeID    `json:"admitted_submit_requests,omitempty"`
	PendingInteractions    []InteractionID `json:"pending_interactions,omitempty"`

	AcknowledgedInteractions []InteractionID `json:"acknowledged_interactions,omitempty"`
}

type SessionCapture struct {
	AdmittedSubmitRequests []EnvelopeID `json:"admitted_submit_requests,omitempty"`
	Settled                []SettledRun `json:"settled,omitempty"`
	ModelRunSequence       *RunPosition `json:"model_run_sequence,omitempty"`
}

type SettledRun struct {
	RunID    RunID  `json:"run_id"`
	Sequence uint64 `json:"sequence"`
}

type RunPosition struct {
	RunID    RunID  `json:"run_id"`
	Sequence uint64 `json:"sequence"`
}

func (p RunPosition) Genesis() bool { return p.RunID == "" && p.Sequence == 0 }

func (p RunPosition) MarshalJSON() ([]byte, error) {
	if p.RunID == "" {
		return json.Marshal(struct {
			RunID    *RunID `json:"run_id"`
			Sequence uint64 `json:"sequence"`
		}{nil, p.Sequence})
	}
	return json.Marshal(struct {
		RunID    RunID  `json:"run_id"`
		Sequence uint64 `json:"sequence"`
	}{p.RunID, p.Sequence})
}

func (p *RunPosition) UnmarshalJSON(data []byte) error {
	var wire struct {
		RunID    *RunID `json:"run_id"`
		Sequence uint64 `json:"sequence"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	p.Sequence = wire.Sequence
	p.RunID = ""
	if wire.RunID != nil {
		p.RunID = *wire.RunID
	}
	return nil
}

func DeliveryKey(mode RequestedDeliveryMode) string {
	return "session.message.delivery." + string(mode)
}

type SessionStateResponse = SessionState
type SessionStateUpdated = SessionState

type RequestedDeliveryMode string

type EffectiveDeliveryMode string

const (
	DeliveryAuto  RequestedDeliveryMode = "auto"
	DeliveryQueue RequestedDeliveryMode = "queue"
	DeliverySteer RequestedDeliveryMode = "steer"
	DeliveryBTW   RequestedDeliveryMode = "btw"

	DeliveryStart          EffectiveDeliveryMode = "start"
	EffectiveDeliveryQueue EffectiveDeliveryMode = "queue"
	EffectiveDeliverySteer EffectiveDeliveryMode = "steer"
	EffectiveDeliveryBTW   EffectiveDeliveryMode = "btw"
)

type ToolChoice struct {
	Allowed    []string `json:"allowed,omitempty"`
	Disallowed []string `json:"disallowed,omitempty"`
}

type MessageSubmitRequest struct {
	SessionID             SessionID                  `json:"session_id"`
	Messages              []Message                  `json:"messages"`
	Delivery              RequestedDeliveryMode      `json:"delivery"`
	ModelID               *string                    `json:"model_id,omitempty"`
	Instructions          *string                    `json:"instructions,omitempty"`
	ToolChoice            json.RawMessage            `json:"tool_choice,omitempty"`
	OutputSchema          json.RawMessage            `json:"output_schema,omitempty"`
	AllowDegradedFeatures []string                   `json:"allow_degraded_features,omitempty"`
	Metadata              map[string]json.RawMessage `json:"metadata,omitempty"`
}

func Control(control *string) string {
	if control == nil {
		return ""
	}
	return *control
}

func ControlValue(value string) *string { return &value }

func (r MessageSubmitRequest) AllowsDegraded(key string) bool {
	for _, allowed := range r.AllowDegradedFeatures {
		if allowed == key {
			return true
		}
	}
	return false
}

func (r MessageSubmitRequest) ToolChoicePolicy() (*ToolChoice, error) {
	if len(r.ToolChoice) == 0 {
		return nil, nil
	}
	if string(bytes.TrimSpace(r.ToolChoice)) == "null" {
		return nil, fmt.Errorf("tool_choice is null, which is not the typed policy")
	}
	decoder := json.NewDecoder(bytes.NewReader(r.ToolChoice))
	decoder.DisallowUnknownFields()
	var policy ToolChoice
	if err := decoder.Decode(&policy); err != nil {
		return nil, fmt.Errorf("tool_choice is not the typed policy: %w", err)
	}
	if decoder.More() {
		return nil, fmt.Errorf("tool_choice carries trailing content")
	}

	var members struct {
		Allowed    json.RawMessage `json:"allowed"`
		Disallowed json.RawMessage `json:"disallowed"`
	}
	if err := json.Unmarshal(r.ToolChoice, &members); err != nil {
		return nil, fmt.Errorf("tool_choice is not the typed policy: %w", err)
	}
	if members.Allowed == nil && members.Disallowed == nil {
		return nil, fmt.Errorf("tool_choice carries neither allowed nor disallowed")
	}
	if members.Allowed != nil && members.Disallowed != nil {
		return nil, fmt.Errorf("tool_choice allowed and disallowed are mutually exclusive")
	}
	for name, raw := range map[string]json.RawMessage{"allowed": members.Allowed, "disallowed": members.Disallowed} {
		if raw != nil && string(bytes.TrimSpace(raw)) == "null" {
			return nil, fmt.Errorf("tool_choice %s is null, which is not a list of tool names", name)
		}
	}
	return &policy, nil
}

type ToolChoiceDefect struct {
	Pointer string
	Tool    string
	Reason  string
}

func (c ToolChoice) Unsatisfiable(catalog []string, known bool) *ToolChoiceDefect {
	listed := make(map[string]bool, len(catalog))
	for _, name := range catalog {
		listed[name] = true
	}

	for index, name := range c.Allowed {
		if known && !listed[name] {
			return &ToolChoiceDefect{Pointer: fmt.Sprintf("/payload/tool_choice/allowed/%d", index), Tool: name, Reason: "allowed names a tool outside the catalog"}
		}
	}
	return nil
}

func (c ToolChoice) Filter(catalog []string) []string {
	filtered := make([]string, 0, len(catalog))
	for _, name := range catalog {

		if c.Allowed != nil && !slices.Contains(c.Allowed, name) {
			continue
		}
		if slices.Contains(c.Disallowed, name) {
			continue
		}
		filtered = append(filtered, name)
	}
	return filtered
}

func (c ToolChoice) Permits(name string, catalog []string, known bool) bool {
	if known && !slices.Contains(catalog, name) {
		return false
	}
	if c.Allowed != nil && !slices.Contains(c.Allowed, name) {
		return false
	}
	if slices.Contains(c.Disallowed, name) {
		return false
	}
	return true
}

type Admission string

const (
	AdmissionStarted  Admission = "started"
	AdmissionQueued   Admission = "queued"
	AdmissionSteered  Admission = "steered"
	AdmissionSideRun  Admission = "side_started"
	AdmissionRejected Admission = "rejected"
)

type MessageSubmitResponse struct {
	SessionID          SessionID             `json:"session_id"`
	Accepted           bool                  `json:"accepted"`
	SubmissionID       SubmissionID          `json:"submission_id"`
	RequestedDelivery  RequestedDeliveryMode `json:"requested_delivery"`
	EffectiveDelivery  EffectiveDeliveryMode `json:"effective_delivery"`
	DeliveryResolution string                `json:"delivery_resolution,omitempty"`
	Admission          Admission             `json:"admission"`
	RunID              RunID                 `json:"run_id,omitempty"`
	Status             RunStatus             `json:"status,omitempty"`
	ModelID            string                `json:"model_id,omitempty"`
	MessageIDs         []MessageID           `json:"message_ids,omitempty"`
}

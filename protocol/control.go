package protocol

import "encoding/json"

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
	Level  SupportLevel `json:"level"`
	Reason string       `json:"reason,omitempty"`
	Mode   string       `json:"mode,omitempty"`
}

type CapabilityLayer struct {
	Features               map[string]FeatureSupport `json:"features,omitempty"`
	RequestedDeliveryModes []RequestedDeliveryMode   `json:"requested_delivery_modes,omitempty"`
	EffectiveDeliveryModes []EffectiveDeliveryMode   `json:"effective_delivery_modes,omitempty"`
	Tools                  []ToolDefinition          `json:"tools,omitempty"`
}

type CapabilityDescriptor struct {
	Endpoint         EndpointDescriptor         `json:"endpoint"`
	ProtocolVersions []string                   `json:"protocol_versions,omitempty"`
	Profiles         []string                   `json:"profiles,omitempty"`
	Bindings         []Binding                  `json:"bindings,omitempty"`
	Features         map[string]FeatureSupport  `json:"features,omitempty"`
	Layers           map[string]CapabilityLayer `json:"layers,omitempty"`
	Tools            []ToolDefinition           `json:"tools,omitempty"`
	Degradation      []Degradation              `json:"degradation,omitempty"`
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

type SessionStatus string

const (
	SessionIdle            SessionStatus = "idle"
	SessionQueued          SessionStatus = "queued"
	SessionRunning         SessionStatus = "running"
	SessionWaitingForInput SessionStatus = "waiting_for_input"
	SessionClosed          SessionStatus = "closed"
	SessionError           SessionStatus = "error"
)

type SessionOpenRequest struct {
	SessionID SessionID                  `json:"session_id,omitempty"`
	Metadata  map[string]json.RawMessage `json:"metadata,omitempty"`
	Recovery  *RecoveryMetadata          `json:"recovery,omitempty"`
}

type SessionOpenResponse struct {
	SessionID SessionID                  `json:"session_id"`
	Status    SessionStatus              `json:"status"`
	Metadata  map[string]json.RawMessage `json:"metadata,omitempty"`
	Recovery  *RecoveryMetadata          `json:"recovery,omitempty"`
}

type SessionStateRequest struct {
	SessionID SessionID `json:"session_id"`
}

type SessionState struct {
	SessionID        SessionID                  `json:"session_id"`
	Status           SessionStatus              `json:"status"`
	ActiveRunID      RunID                      `json:"active_run_id,omitempty"`
	CurrentModelID   string                     `json:"current_model_id,omitempty"`
	TranscriptCursor string                     `json:"transcript_cursor,omitempty"`
	UpdatedAtMS      int64                      `json:"updated_at_ms,omitempty"`
	Metadata         map[string]json.RawMessage `json:"metadata,omitempty"`
	Recovery         *RecoveryMetadata          `json:"recovery,omitempty"`
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
	Mode string `json:"mode,omitempty"`
	Name string `json:"name,omitempty"`
}

type MessageSubmitRequest struct {
	SessionID             SessionID                  `json:"session_id"`
	Messages              []Message                  `json:"messages"`
	Delivery              RequestedDeliveryMode      `json:"delivery"`
	ModelID               string                     `json:"model_id,omitempty"`
	Instructions          string                     `json:"instructions,omitempty"`
	ToolChoice            json.RawMessage            `json:"tool_choice,omitempty"`
	OutputSchema          json.RawMessage            `json:"output_schema,omitempty"`
	AllowDegradedFeatures []string                   `json:"allow_degraded_features,omitempty"`
	Metadata              map[string]json.RawMessage `json:"metadata,omitempty"`
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

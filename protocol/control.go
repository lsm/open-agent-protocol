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

// FeatureSupport reports the effective support level of one capability key and
// discloses, machine-readably, how the endpoint applies it.
//
// Mode is the single application mode a key has one of (run.model_selection:
// ModePerRun or ModeSessionMutation). Modes is the set a key can enforce more
// than one of: run.tool_selection lists the tool_choice modes the endpoint
// actually honours, so a refusal is conforming only for a mode outside the
// list and the key cannot promise an empty thing. Constraints carries the
// endpoint-specific limits a caller and a validator must be able to check —
// ConstraintFixedResult for run.structured_output, the exact object every
// run.completed under an accepted output_schema will carry.
type FeatureSupport struct {
	Level       SupportLevel               `json:"level"`
	Reason      string                     `json:"reason,omitempty"`
	Mode        string                     `json:"mode,omitempty"`
	Modes       []string                   `json:"modes,omitempty"`
	Constraints map[string]json.RawMessage `json:"constraints,omitempty"`
}

// Capability keys for the per-submit run controls. A control the endpoint has
// not affirmatively advertised under its key is refused before admission; a
// control it advertises is applied or refused with a typed error, never
// dropped.
const (
	FeatureModelSelection = "run.model_selection"
	FeatureInstructions   = "run.instructions"
	// FeatureDeliveryQueue is the delivery key the queue unit gives
	// executable meaning. Delivery keys are built from the requested mode, so
	// this is the name DeliveryKey(DeliveryQueue) produces; the constant
	// exists so the unit's tables and gates cannot spell it differently.
	FeatureDeliveryQueue    = "session.message.delivery.queue"
	FeatureToolSelection    = "run.tool_selection"
	FeatureStructuredOutput = "run.structured_output"
)

// Application modes FeatureSupport.Mode discloses for run.model_selection.
// ModeRestart is named by the vocabulary but is not offered in this phase.
const (
	ModePerRun          = "per_run"
	ModeSessionMutation = "session_mutation"
	ModeRestart         = "restart"
)

// ConstraintFixedResult is the FeatureSupport.Constraints member for
// run.structured_output: the exact result object every run.completed under an
// accepted output_schema carries. Declaring it makes a fixed-output endpoint's
// refusals checkable in both directions.
//
// The schema requires an object, because only an object is a structured
// result. A null or a scalar would satisfy no object-rooted output_schema, so
// an endpoint declaring one would refuse every structured-output request as
// unsatisfiable while its descriptor read as conformant.
const ConstraintFixedResult = "fixed_result"

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
	Limits           *CapabilityLimits          `json:"limits,omitempty"`
}

// CapabilityLimits are the admission bounds a descriptor discloses (queue
// unit). MaxActiveRunsPerSession bounds the nonterminal set — the started run
// plus every queued reservation, which is what session.state.active_runs
// lists — and is the wire projection of adapter.Descriptor's field of the same
// name. MaxQueuedRunsPerSession bounds the queued subset.
//
// Both are pointers because absence and a value are different claims: an
// endpoint that states no bound has promised nothing about it, while one that
// states a bound is held to the arithmetic it implies. A descriptor
// advertising session.message.delivery.queue above unavailable must disclose a
// positive MaxQueuedRunsPerSession, and any MaxActiveRunsPerSession it
// discloses beside it must leave room for that subset next to a started run;
// otherwise the capability promises a queue no admission could ever reach.
type CapabilityLimits struct {
	MaxActiveRunsPerSession *int `json:"max_active_runs_per_session,omitempty"`
	MaxQueuedRunsPerSession *int `json:"max_queued_runs_per_session,omitempty"`
}

// Limit wraps a bound as a disclosed value, so a caller can state one without
// taking the address of a local.
func Limit(value int) *int { return &value }

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
	ActiveRuns       []ActiveRun                `json:"active_runs,omitempty"`
	CurrentModelID   string                     `json:"current_model_id,omitempty"`
	TranscriptCursor string                     `json:"transcript_cursor,omitempty"`
	UpdatedAtMS      int64                      `json:"updated_at_ms,omitempty"`
	Metadata         map[string]json.RawMessage `json:"metadata,omitempty"`
	Recovery         *RecoveryMetadata          `json:"recovery,omitempty"`
	AsOf             *SessionCapture            `json:"as_of,omitempty"`
}

// RelationshipPrimary is the only relationship an active_runs entry carries in
// this phase: the run is a foreground execution of the session, started or
// reserved. Side runs and subagent lifecycles stay deferred, so no other value
// has rules.
const RelationshipPrimary = "primary"

// ActiveRun is one nonterminal run of a session, in admission order (queue
// unit). A queued reservation carries its 1-based QueuePosition; the started
// run carries none.
//
// AsOfSequence is the last sequence of this run the entry reflects, and it is
// what makes PendingInteractions judgeable: a state read is not serialized
// with lifecycle publication, so an interaction can resolve inside the
// endpoint before capture while the event carrying that sequence is drained
// afterwards. The position makes the read self-describing instead of leaving
// an accurate omission to be diagnosed. An entry carrying PendingInteractions
// must carry it.
//
// AdmittedSubmitRequests names the submit request envelope ids on this run the
// entry reflects as admitted. It is the per-run capture anchor for pending
// sets that a run sequence cannot order, and every id in it must name a submit
// request the trace carries for the run.
type ActiveRun struct {
	RunID                  RunID           `json:"run_id"`
	Status                 RunStatus       `json:"status"`
	Relationship           string          `json:"relationship"`
	QueuePosition          *int            `json:"queue_position,omitempty"`
	AsOfSequence           *uint64         `json:"as_of_sequence,omitempty"`
	AdmittedSubmitRequests []EnvelopeID    `json:"admitted_submit_requests,omitempty"`
	PendingInteractions    []InteractionID `json:"pending_interactions,omitempty"`
}

// SessionCapture is the session-level capture position of a state snapshot
// (queue unit): what the snapshot knew when it was taken, so membership is
// judged against the endpoint's knowledge rather than against the trace's
// current state.
//
// AdmittedSubmitRequests are the submit requests on the session the snapshot
// reflects as admitted; only an admission whose response falls inside the
// state request/response window may be omitted on the strength of being absent
// from it. Settled are the runs the snapshot has already removed, each with
// the sequence of its terminal — a terminal the trace need not have reached
// yet, which is then held and reconciled when it arrives.
//
// ModelRunSequence names the last model-affecting event the snapshot reflects,
// so a model reported during a concurrent promotion is judged at the position
// it was captured at. Its genesis form, {"run_id": null, "sequence": 0}, names
// the position before any model-affecting event — the session's opening model,
// judged against no run at all.
type SessionCapture struct {
	AdmittedSubmitRequests []EnvelopeID `json:"admitted_submit_requests,omitempty"`
	Settled                []SettledRun `json:"settled,omitempty"`
	ModelRunSequence       *RunPosition `json:"model_run_sequence,omitempty"`
}

// SettledRun is one run a snapshot has removed, with the sequence its terminal
// carries.
type SettledRun struct {
	RunID    RunID  `json:"run_id"`
	Sequence uint64 `json:"sequence"`
}

// RunPosition is a stated position in a run's sequence domain. RunID is empty
// in the genesis form, which names the position before the session's first
// model-affecting event; the wire spells that as null.
type RunPosition struct {
	RunID    RunID  `json:"run_id"`
	Sequence uint64 `json:"sequence"`
}

// Genesis reports whether the position is the genesis form: before any
// model-affecting event on the session.
func (p RunPosition) Genesis() bool { return p.RunID == "" && p.Sequence == 0 }

// MarshalJSON spells the genesis run as null rather than as an empty id: the
// wire's opaque ids are non-empty, so "" would be a malformed id where null is
// the stated absence of a run.
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

// DeliveryKey is the capability key one requested delivery mode is gated on.
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

// ToolChoice is the typed tool-selection policy a submission may carry. The
// wire keeps tool_choice permissive, so this shape is enforced by
// ToolChoicePolicy, by the validator's run-controls rules, and by adapters
// rather than by the schema.
//
// Precedence is fixed: Allowed or Disallowed filters the advertised catalog
// first, then Mode applies to the filtered set. Name is present when and only
// when Mode is ToolChoiceNamed, and Allowed and Disallowed are mutually
// exclusive.
type ToolChoice struct {
	Mode       string   `json:"mode,omitempty"`
	Name       string   `json:"name,omitempty"`
	Allowed    []string `json:"allowed,omitempty"`
	Disallowed []string `json:"disallowed,omitempty"`
}

// The tool_choice modes. An endpoint discloses the subset it can enforce in
// run.tool_selection's FeatureSupport.Modes.
const (
	ToolChoiceAuto     = "auto"
	ToolChoiceNone     = "none"
	ToolChoiceRequired = "required"
	ToolChoiceNamed    = "named"
)

// MessageSubmitRequest carries the per-submit run controls. ModelID and
// Instructions are pointers because presence is what the fail-closed gate is
// about: the schema permits an empty string, so a plain string could not tell
// an absent control from `{"model_id": ""}`, and an endpoint testing the field
// with != "" would admit the second as "no selection" instead of refusing it.
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

// Control reports a present-or-absent string control's value. A present-empty
// control is a control: it is judged through the gate like any other, and only
// then read as a value.
func Control(control *string) string {
	if control == nil {
		return ""
	}
	return *control
}

// ControlValue wraps a value as a present control, so a caller can express
// presence without taking the address of a local.
func ControlValue(value string) *string { return &value }

// AllowsDegraded reports whether the submission opted into the degraded
// application of one capability key.
func (r MessageSubmitRequest) AllowsDegraded(key string) bool {
	for _, allowed := range r.AllowDegradedFeatures {
		if allowed == key {
			return true
		}
	}
	return false
}

// ToolChoicePolicy decodes the typed policy a submission carries. It reports
// (nil, nil) when no tool_choice is present, and an error when the member is
// present but is not the typed shape: an unknown member, an unknown mode, a
// name present on a mode other than "named" or absent on "named", a name that
// is null or empty, a null allowed or disallowed, or both of them present. A
// policy that decodes here may still be unsatisfiable against a catalog;
// ToolChoice.Unsatisfiable judges that.
//
// A present `null` is a control, not an absent one. The schema admits any JSON
// value here, and presence is what the gate judges — the same rule that makes
// `{"model_id": ""}` a control the endpoint must refuse rather than read as
// "no selection". A null is present and is not the typed policy, so it is
// refused like any other untyped value; reading it as absence would put the
// wire's meaning at the mercy of a decoder convention.
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
	// Presence is read off the wire rather than off the decoded value.
	// Unmarshalling into value fields loses it: `"name": ""` and `"name":
	// null` both land as the empty string, and `"allowed": []` alongside
	// `"disallowed": []` as two empty slices, so a policy whose typed shape is
	// wrong would read as one whose members were simply absent. The shape is
	// stated in terms of presence — `name` when and only when the mode is
	// `named`, `allowed` and `disallowed` mutually exclusive — so presence is
	// what must be judged.
	var members struct {
		Name       json.RawMessage `json:"name"`
		Allowed    json.RawMessage `json:"allowed"`
		Disallowed json.RawMessage `json:"disallowed"`
	}
	if err := json.Unmarshal(r.ToolChoice, &members); err != nil {
		return nil, fmt.Errorf("tool_choice is not the typed policy: %w", err)
	}
	switch policy.Mode {
	case ToolChoiceAuto, ToolChoiceNone, ToolChoiceRequired, ToolChoiceNamed:
	default:
		return nil, fmt.Errorf("tool_choice mode %q is not one of auto, none, required, named", policy.Mode)
	}
	if (members.Name != nil) != (policy.Mode == ToolChoiceNamed) {
		return nil, fmt.Errorf("tool_choice name is present when and only when mode is %q", ToolChoiceNamed)
	}
	// A null is a present member of the wrong type, and an empty name can
	// name no tool in any catalog, so neither is the typed shape.
	if members.Name != nil && policy.Name == "" {
		return nil, fmt.Errorf("tool_choice name must be a non-empty tool name")
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

// ToolChoiceDefect names the one member that makes a policy unsatisfiable. The
// pointer is relative to the submit payload, so the validator and an adapter
// report the same offending entry; Tool is the name the refusal's
// details.tool carries when the defect names one.
type ToolChoiceDefect struct {
	Pointer string
	Tool    string
	Reason  string
}

// Unsatisfiable judges a decoded policy against the catalog it governs and
// reports the first offending member in JSON Pointer order (object members
// lexicographically, array indices numerically), so two encodings of one
// request owe the same refusal. Pass known=false when the trace or the session
// carries no catalog: the self-contradiction checks still apply, and the
// catalog-dependent ones are held until a catalog is in evidence.
func (c ToolChoice) Unsatisfiable(catalog []string, known bool) *ToolChoiceDefect {
	listed := make(map[string]bool, len(catalog))
	for _, name := range catalog {
		listed[name] = true
	}
	// "allowed" precedes "disallowed" precedes "mode" precedes "name".
	for index, name := range c.Allowed {
		if known && !listed[name] {
			return &ToolChoiceDefect{Pointer: fmt.Sprintf("/payload/tool_choice/allowed/%d", index), Tool: name, Reason: "allowed names a tool outside the catalog"}
		}
	}
	for index, name := range c.Disallowed {
		if known && !listed[name] {
			return &ToolChoiceDefect{Pointer: fmt.Sprintf("/payload/tool_choice/disallowed/%d", index), Tool: name, Reason: "disallowed names a tool outside the catalog"}
		}
	}
	filtered := c.Filter(catalog)
	if c.Mode == ToolChoiceRequired && known && len(filtered) == 0 {
		return &ToolChoiceDefect{Pointer: "/payload/tool_choice/mode", Reason: "required cannot be honoured against an empty filtered set"}
	}
	if c.Mode == ToolChoiceNamed {
		for _, name := range c.Disallowed {
			if name == c.Name {
				return &ToolChoiceDefect{Pointer: "/payload/tool_choice/name", Tool: c.Name, Reason: "named tool is excluded by its own disallowed list"}
			}
		}
		if c.Allowed != nil {
			permitted := false
			for _, name := range c.Allowed {
				if name == c.Name {
					permitted = true
				}
			}
			if !permitted {
				return &ToolChoiceDefect{Pointer: "/payload/tool_choice/name", Tool: c.Name, Reason: "named tool is outside its own allowed list"}
			}
		}
		if known && !slices.Contains(filtered, c.Name) {
			return &ToolChoiceDefect{Pointer: "/payload/tool_choice/name", Tool: c.Name, Reason: "named tool is not in the filtered catalog"}
		}
	}
	return nil
}

// Filter applies the policy's allowed/disallowed filter to a catalog, which is
// the first half of the fixed precedence; Mode then applies to the result.
func (c ToolChoice) Filter(catalog []string) []string {
	filtered := make([]string, 0, len(catalog))
	for _, name := range catalog {
		// Presence, not length: an `allowed` the caller sent empty permits no
		// tool at all. Reading it as absent would turn the most restrictive
		// allowlist expressible into the most permissive one, and hand the
		// whole catalog to a caller that allowed none of it.
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

// Permits reports whether a policy admits a call to one tool: the catalog
// first, then the filter, then the mode.
//
// The policy is defined over the effective catalog — allowed/disallowed filter
// it and the mode applies to what is left — so the permitted set is always a
// subset of the catalog and no mode admits a tool the catalog does not carry.
// Without that first step a plain auto or required policy would admit any name
// at all, which is the one reading that lets an admitted policy govern nothing.
// known says whether catalog is the effective one; a caller that cannot see a
// catalog judges the filter and the mode alone.
func (c ToolChoice) Permits(name string, catalog []string, known bool) bool {
	if c.Mode == ToolChoiceNone {
		return false
	}
	if known && !slices.Contains(catalog, name) {
		return false
	}
	if c.Allowed != nil && !slices.Contains(c.Allowed, name) {
		return false
	}
	if slices.Contains(c.Disallowed, name) {
		return false
	}
	if c.Mode == ToolChoiceNamed && name != c.Name {
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

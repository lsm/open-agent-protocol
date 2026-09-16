// Package adapter defines the in-process boundary between an OAP control layer
// and an agent-control implementation.
package adapter

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/lsm/open-agent-protocol/protocol"
)

// Adapter discovers capabilities and opens isolated sessions.
type Adapter interface {
	Probe(context.Context) (Descriptor, error)
	Open(context.Context, OpenRequest) (Session, error)
}

// Session is safe for concurrent use. Exactly one run may be nonterminal.
type Session interface {
	Submit(context.Context, protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, EventStream, error)
	State(context.Context) (protocol.SessionState, error)
	Resolve(context.Context, InteractionResolution) error
	Cancel(context.Context, protocol.RunID) (protocol.RunCancelResponse, error)
	Resume(context.Context, ResumeRequest) (Recovery, EventStream, error)
	Close(context.Context) error
}

// EventStream carries either an envelope or an error in one ordered channel.
// A closed channel is normal end-of-stream; there is no separately racing Err method.
type EventStream <-chan Result

type Result struct {
	Envelope protocol.Envelope
	Error    error
}

// Descriptor reports effective behavior of this adapter implementation.
type Descriptor struct {
	Capabilities               protocol.CapabilityDescriptor
	CapabilityRevision         string
	Journal                    JournalDescriptor
	MaxActiveRunsPerSession    int
	InteractiveGates           bool
	CancellationTarget         string
	CancellationImplementation string
}

type JournalDescriptor struct {
	Scope       string
	Persistence string
	Replay      protocol.SupportLevel
	Capacity    int
}

type OpenRequest struct {
	SessionID   protocol.SessionID
	Participant protocol.Participant
	Metadata    map[string]any
	// ToolSources are the sources attached for the session's lifetime. An
	// adapter that cannot attach at open refuses the open with the typed
	// unsupported_feature naming action.tool_sources.attach rather than
	// opening a session that silently has none of them.
	ToolSources []protocol.ToolSourceAttachment
	// AllowDegradedFeatures is the caller's consent to a degraded application
	// of the capabilities the open elects. An open electing a degraded key
	// without naming it here is refused with capability_degraded.
	AllowDegradedFeatures []string
}

// AllowsDegraded reports whether the open opted into the degraded application
// of one capability key.
func (r OpenRequest) AllowsDegraded(key string) bool {
	return slices.Contains(r.AllowDegradedFeatures, key)
}

// ToolLister is the optional catalog surface. An adapter that can publish a
// portable catalog implements it and advertises action.tools.list; one that
// cannot does not implement it, and the boundary answers the typed refusal
// that names the capability rather than a generic failure.
//
// The argument is the wire payload struct so the adapter applies the
// degraded opt-in rule to exactly what the caller sent.
type ToolLister interface {
	Tools(context.Context, protocol.ToolsListRequest) (protocol.ToolsListResponse, error)
}

// ErrToolCatalogUnavailable is the sentinel for an endpoint that serves no
// portable catalog. Codecs map it to unsupported_feature with
// details.feature: "action.tools.list" and details.reason: "unadvertised",
// which is the refusal the wire requires and the validator accepts.
var ErrToolCatalogUnavailable = errors.New("adapter: no portable tool catalog is served")

// InteractionResolution is a tagged union: exactly one of Permission or Input
// must be present. RespondedBy must match the pending interaction's responder.
type InteractionResolution struct {
	RunID       protocol.RunID
	RespondedBy protocol.ParticipantID
	Permission  *protocol.PermissionResolveRequest
	Input       *protocol.UserInputResolveRequest
}

type ResumeRequest struct {
	RunID         protocol.RunID
	AfterSequence uint64
}

type Recovery struct {
	State           protocol.SessionState
	RunID           protocol.RunID
	RequestedAfter  uint64
	ReplayedFrom    uint64
	ReplayedThrough uint64
	ReplayGap       *ReplayGap
}

// ReplayGap says the requested cursor is older than this process-memory journal.
type ReplayGap struct {
	RequestedAfter  uint64
	OldestAvailable uint64
	LatestAvailable uint64
}

func (g *ReplayGap) Error() string { return "adapter: requested replay cursor is no longer retained" }

var (
	ErrSessionClosed       = errors.New("adapter: session closed")
	ErrInvalidParticipant  = errors.New("adapter: participant identity is required")
	ErrUnsupportedInput    = errors.New("adapter: unsupported input")
	ErrRunActive           = errors.New("adapter: a run is already active")
	ErrInvalidSubmission   = errors.New("adapter: invalid submission")
	ErrRunNotFound         = errors.New("adapter: run not found")
	ErrReplayCursorFuture  = errors.New("adapter: replay cursor is newer than the run")
	ErrRunAlreadyTerminal  = errors.New("adapter: run already completed or failed")
	ErrInteractionNotFound = errors.New("adapter: interaction not found")
	ErrInteractionResolved = errors.New("adapter: interaction already resolved")
	ErrWrongResponder      = errors.New("adapter: interaction resolved by undeclared participant")
	ErrInvalidResolution   = errors.New("adapter: invalid interaction resolution")
	ErrEventStreamOverflow = errors.New("adapter: event stream consumer fell behind; resume from the last sequence")
)

// ErrModelNotFound reports a model_id outside the endpoint's effective
// catalog. It is the one control refusal that is not an unsupported feature:
// the capability is advertised and the request was understood.
var ErrModelNotFound = errors.New("adapter: model is not in the effective catalog")

// RefuseUnadvertisedControls reports the typed refusal owed for the first
// per-submit control a submission carries that this endpoint has not
// advertised, and nil when it carries none. advertised names the capability
// keys the endpoint offers above `unavailable`.
//
// Every endpoint owes this refusal whether or not it supports a single
// control: refusing an unadvertised control correctly is the discipline, and
// a generic "invalid submission" tells a caller only that something was wrong
// — not which control to stop sending. The controls are judged in the refusal
// precedence order, which within the capability rung is the lower capability
// key, so an endpoint and the validator name the same one.
//
// Call it before ordinary submission validation, so a control is refused
// before any identity is allocated or any native write happens.
func RefuseUnadvertisedControls(request protocol.MessageSubmitRequest, advertised ...string) error {
	offers := func(key string) bool { return slices.Contains(advertised, key) }
	// Sorted by capability key: instructions, model_selection,
	// structured_output, tool_selection.
	for _, control := range []struct {
		key     string
		present bool
	}{
		{protocol.FeatureInstructions, request.Instructions != nil},
		{protocol.FeatureModelSelection, request.ModelID != nil},
		{protocol.FeatureStructuredOutput, len(request.OutputSchema) > 0},
		{protocol.FeatureToolSelection, len(request.ToolChoice) > 0},
	} {
		if control.present && !offers(control.key) {
			return &UnsupportedControlError{Feature: control.key, Reason: ControlUnadvertised}
		}
	}
	return nil
}

// RefuseUnadvertisedToolSources reports the typed refusal owed when an open
// attaches tool sources to an endpoint whose own disclosure does not admit
// them, and nil when the open attaches none. disclosed is the endpoint's
// `action.tool_sources.attach` support — the same value its Probe publishes —
// and passing none says it discloses none.
//
// It exists for the same reason RefuseUnadvertisedControls does, and the
// reason is sharper here: OpenRequest.ToolSources is a field an adapter
// written before this unit never reads, so without an explicit gate such an
// adapter returns a successful session having silently dropped the sources the
// caller asked for. That is the one outcome the fail-closed contract exists to
// prevent — a caller cannot tell an endpoint that attached its sources from
// one that discarded them — and "the adapter ignores the field" is not a
// refusal a caller can act on.
//
// It takes the disclosure rather than a list of key names because the key is
// not usable on its name alone: an attach capability that discloses no
// session_open mode offers nothing an open can elect, and the validator and
// the daemon's open route both refuse such an open. An adapter admitting it
// would make the in-process path weaker than the wire path — the asymmetry
// this helper exists to prevent — so the one gate answers both.
//
// Call it before any native write and before a session identity exists, so a
// refused open leaves nothing behind.
func RefuseUnadvertisedToolSources(request OpenRequest, disclosed ...protocol.FeatureSupport) error {
	if len(request.ToolSources) == 0 {
		return nil
	}
	for _, support := range disclosed {
		if support.Level == "" || support.Level == protocol.SupportUnavailable {
			continue
		}
		if support.DisclosesMode(protocol.ModeSessionOpen) {
			return nil
		}
	}
	return &UnsupportedControlError{Feature: protocol.FeatureToolSourcesAttach, Reason: ControlUnadvertised}
}

// UnsupportedControlError refuses a per-submit control before admission:
// either the endpoint never advertised the capability (Reason
// ControlUnadvertised) or it cannot honour this request's value of the control
// (Reason ControlUnsatisfiable). Codecs map it to the typed
// `unsupported_feature` error with details.feature and details.reason.
type UnsupportedControlError struct {
	Feature string
	Reason  string
	// Tool and Field name the offending member of an unsatisfiable control,
	// so a refusal and a validator name the same entry. Source names the
	// offending tool source of an unsatisfiable attachment.
	Tool, Field, Source string
	Detail              string
}

// The two conditions unsupported_feature covers.
const (
	ControlUnadvertised  = "unadvertised"
	ControlUnsatisfiable = "unsatisfiable"
)

func (e *UnsupportedControlError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s (%s): %s", ErrUnsupportedInput.Error(), e.Feature, e.Reason, e.Detail)
	}
	return fmt.Sprintf("%s: %s (%s)", ErrUnsupportedInput.Error(), e.Feature, e.Reason)
}
func (e *UnsupportedControlError) Unwrap() error { return ErrUnsupportedInput }

// DegradedControlError refuses a control the endpoint advertises `degraded`
// when the caller did not name its key in allow_degraded_features. The opt-in
// it asks for is a request the caller can simply reissue.
type DegradedControlError struct{ Feature string }

func (e *DegradedControlError) Error() string {
	return fmt.Sprintf("%s: %s is degraded and was not opted into", ErrUnsupportedInput.Error(), e.Feature)
}
func (e *DegradedControlError) Unwrap() error { return ErrUnsupportedInput }

// ModelNotFoundError names the id a catalog does not carry. An empty id is
// necessarily outside every catalog, so it takes this refusal too rather than
// a second unsatisfiability of its own.
type ModelNotFoundError struct{ ModelID string }

func (e *ModelNotFoundError) Error() string {
	return fmt.Sprintf("%s: %q", ErrModelNotFound.Error(), e.ModelID)
}
func (e *ModelNotFoundError) Unwrap() error { return ErrModelNotFound }

type RunTerminalError struct {
	RunID  protocol.RunID
	Status protocol.RunStatus
}

func (e *RunTerminalError) Error() string { return ErrRunAlreadyTerminal.Error() }
func (e *RunTerminalError) Unwrap() error { return ErrRunAlreadyTerminal }

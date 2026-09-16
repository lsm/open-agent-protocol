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

// ModelLister is the optional session capability for the session-scoped model
// catalog (unit `models`). It is discovered by type assertion rather than
// added to Session, so every existing implementation compiles unchanged and a
// session whose adapter does not implement it is refused under the ordinary
// gate instead of answering an empty catalog.
//
// The request carries the caller's own allow_degraded_features, so an adapter
// serving a degraded catalog applies the opt-in rule itself: the consent is
// per request, and only the adapter knows what its catalog costs.
type ModelLister interface {
	Models(context.Context, protocol.ModelsRequest) (protocol.ModelsResponse, error)
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
}

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

// UnsupportedControlError refuses a per-submit control before admission:
// either the endpoint never advertised the capability (Reason
// ControlUnadvertised) or it cannot honour this request's value of the control
// (Reason ControlUnsatisfiable). Codecs map it to the typed
// `unsupported_feature` error with details.feature and details.reason.
type UnsupportedControlError struct {
	Feature string
	Reason  string
	// Tool and Field name the offending member of an unsatisfiable control,
	// so a refusal and a validator name the same entry.
	Tool, Field string
	Detail      string
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

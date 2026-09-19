package adapter

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

type Adapter interface {
	Probe(context.Context) (Descriptor, error)
	Open(context.Context, OpenRequest) (Session, error)
}

type Session interface {
	Submit(context.Context, protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, EventStream, error)
	State(context.Context) (protocol.SessionState, error)
	Resolve(context.Context, InteractionResolution) error
	Cancel(context.Context, protocol.RunID) (protocol.RunCancelResponse, error)
	Resume(context.Context, ResumeRequest) (Recovery, EventStream, error)
	Close(context.Context) error
}

type Catalog struct {
	Revision string

	Models protocol.ModelsResponse
}

type ModelLister interface {
	Models(context.Context, protocol.ModelsRequest) (Catalog, error)
}

type EventStream <-chan Result

type Result struct {
	Envelope protocol.Envelope
	Error    error
}

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

	ToolSources []protocol.ToolSourceAttachment

	Tools []protocol.ToolDefinition

	AllowDegradedFeatures []string
}

func (r OpenRequest) AllowsDegraded(key string) bool {
	return slices.Contains(r.AllowDegradedFeatures, key)
}

type ToolCatalog struct {
	Revision string

	Tools protocol.ToolsListResponse
}

type ToolLister interface {
	Tools(context.Context, protocol.ToolsListRequest) (ToolCatalog, error)
}

var ErrToolCatalogUnavailable = errors.New("adapter: no portable tool catalog is served")

type InteractionResolution struct {
	RunID       protocol.RunID
	RespondedBy protocol.ParticipantID
	Permission  *protocol.PermissionResolveRequest
	Input       *protocol.UserInputResolveRequest
}

type CallResolution struct {
	RequestID protocol.EnvelopeID
	Request   protocol.ActionCallResolveRequest
}

type CallResolver interface {
	ResolveCall(context.Context, CallResolution) (protocol.ActionCallResolveResponse, error)
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

var ErrModelNotFound = errors.New("adapter: model is not in the effective catalog")

func RefuseUnadvertisedControls(request protocol.MessageSubmitRequest, advertised ...string) error {
	offers := func(key string) bool { return slices.Contains(advertised, key) }

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

func RefuseUnadvertisedTools(request OpenRequest, disclosed ...protocol.FeatureSupport) error {
	if len(request.Tools) == 0 {
		return nil
	}
	for _, support := range disclosed {
		if support.Level != "" && support.Level != protocol.SupportUnavailable {
			return nil
		}
	}
	return &UnsupportedControlError{Feature: protocol.FeatureToolsProvide, Reason: ControlUnadvertised}
}

func DuplicateEnvironmentName(entries []string) string {
	if len(entries) < 2 {
		return ""
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		name, _, _ := strings.Cut(entry, "=")
		if seen[name] {
			return name
		}
		seen[name] = true
	}
	return ""
}

type UnsupportedControlError struct {
	Feature string
	Reason  string

	Tool, Field, Source string
	Detail              string
}

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

type DegradedControlError struct{ Feature string }

func (e *DegradedControlError) Error() string {
	return fmt.Sprintf("%s: %s is degraded and was not opted into", ErrUnsupportedInput.Error(), e.Feature)
}
func (e *DegradedControlError) Unwrap() error { return ErrUnsupportedInput }

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

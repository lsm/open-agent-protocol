// Package adapter defines the in-process boundary between an OAP control layer
// and an agent-control implementation.
package adapter

import (
	"context"
	"errors"

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

type RunTerminalError struct {
	RunID  protocol.RunID
	Status protocol.RunStatus
}

func (e *RunTerminalError) Error() string { return ErrRunAlreadyTerminal.Error() }
func (e *RunTerminalError) Unwrap() error { return ErrRunAlreadyTerminal }

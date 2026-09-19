package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const DefaultShutdownTimeout = 10 * time.Second

const DefaultParticipant = protocol.ParticipantID("user")

const defaultStreamQueue = 64

type Options struct {
	StreamQueue int

	Logger *log.Logger

	ShutdownTimeout time.Duration
}

type Hub struct {
	registry *Registry
	sessions *sessionRegistry
	queue    int
	shutdown time.Duration
	logger   *log.Logger
}

func New(registry *Registry, options Options) *Hub {
	queue := options.StreamQueue
	if queue <= 0 {
		queue = defaultStreamQueue
	}
	logger := options.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	shutdown := options.ShutdownTimeout
	if shutdown <= 0 {
		shutdown = DefaultShutdownTimeout
	}
	return &Hub{
		registry: registry, sessions: newSessionRegistry(),
		queue: queue, shutdown: shutdown, logger: logger,
	}
}

func (h *Hub) Registry() *Registry { return h.registry }

func (h *Hub) Open(ctx context.Context, adapterName string, request base.OpenRequest) (*Session, protocol.SessionState, error) {
	implementation, ok := h.registry.Lookup(adapterName)
	if !ok {
		return nil, protocol.SessionState{}, &UnknownAdapterError{Name: adapterName}
	}
	if request.Participant.ID == "" {
		request.Participant.ID = DefaultParticipant
	}
	session, err := implementation.Open(ctx, request)
	if err != nil {
		return nil, protocol.SessionState{}, err
	}
	state, err := session.State(ctx)
	if err != nil && !errors.Is(err, base.ErrSessionClosed) {
		_ = session.Close(context.WithoutCancel(ctx))
		return nil, protocol.SessionState{}, err
	}

	entry := newSession(state.SessionID, adapterName, session)
	if err != nil {
		entry.markClosed()
	}
	if err := h.sessions.add(entry); err != nil {
		_ = session.Close(context.WithoutCancel(ctx))
		return nil, protocol.SessionState{}, &SessionExistsError{ID: entry.id}
	}
	return entry, state, nil
}

func (h *Hub) Session(id protocol.SessionID) (*Session, error) {
	entry, ok := h.sessions.get(id)
	if !ok {
		return nil, &UnknownSessionError{ID: id}
	}
	return entry, nil
}

type AdapterStatus struct {
	Name string

	Descriptor base.Descriptor

	Err error
}

func (h *Hub) Adapters(ctx context.Context) []AdapterStatus {
	statuses := make([]AdapterStatus, 0, h.registry.adaptersLen())
	for _, name := range h.registry.Names() {
		status := AdapterStatus{Name: name}
		implementation, _ := h.registry.Lookup(name)
		descriptor, err := implementation.Probe(ctx)
		switch {
		case err != nil:
			status.Err = err
		case descriptor.CapabilityRevision == "":

			status.Err = errors.New("adapter descriptor carries no capability revision")
		default:
			status.Descriptor = descriptor
		}
		statuses = append(statuses, status)
	}
	return statuses
}

func (h *Hub) Probe(ctx context.Context, name string) (base.Descriptor, error) {
	implementation, ok := h.registry.Lookup(name)
	if !ok {
		return base.Descriptor{}, &UnknownAdapterError{Name: name}
	}
	descriptor, err := implementation.Probe(ctx)
	if err != nil {
		return base.Descriptor{}, err
	}
	if descriptor.CapabilityRevision == "" {
		return base.Descriptor{}, errors.New("adapter descriptor carries no capability revision")
	}
	return descriptor, nil
}

type SessionStatus struct {
	SessionID protocol.SessionID

	Adapter string

	Status protocol.SessionStatus

	ActiveRunID protocol.RunID

	ActiveRuns []protocol.ActiveRun

	CreatedAt time.Time
}

func (h *Hub) Sessions(ctx context.Context) []SessionStatus {
	entries := h.sessions.list()
	statuses := make([]SessionStatus, 0, len(entries))
	for _, entry := range entries {
		status := SessionStatus{
			SessionID: entry.id, Adapter: entry.adapterName, CreatedAt: entry.created,
		}
		state, err := entry.session.State(ctx)
		status.Status = state.Status
		status.ActiveRunID = state.ActiveRunID
		status.ActiveRuns = state.ActiveRuns
		if err != nil && !errors.Is(err, base.ErrSessionClosed) {
			status.Status = protocol.SessionError
			status.ActiveRunID = ""
			status.ActiveRuns = nil
		}
		statuses = append(statuses, status)
	}
	return statuses
}

func (h *Hub) CloseSessions(ctx context.Context) {
	entries := h.sessions.list()
	sweep, cancelSweep := context.WithTimeout(ctx, h.shutdown)
	defer cancelSweep()
	for index, entry := range entries {
		if sweep.Err() != nil {
			h.logger.Printf("serve: shutdown budget exhausted before closing session %s", entry.id)
			break
		}
		deadline, _ := sweep.Deadline()
		remaining := time.Until(deadline)

		share := remaining / time.Duration(len(entries)-index)
		perSession, cancelSession := context.WithTimeout(sweep, share)
		if err := entry.closeForShutdown(perSession); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, base.ErrSessionClosed) {
			h.logger.Printf("serve: close session %s: %v", entry.id, err)
		}
		cancelSession()
	}
}

var (
	ErrUnknownAdapter = errors.New("serve: unknown adapter")

	ErrUnknownSession = errors.New("serve: unknown session")

	ErrSessionExists = errors.New("serve: session already exists")

	ErrScopeMismatch = errors.New("serve: request session does not match the addressed session")

	ErrNoRunToResume = errors.New("the session has no run to replay")
)

type UnknownAdapterError struct {
	Name string
}

func (e *UnknownAdapterError) Error() string { return fmt.Sprintf("no adapter %q", e.Name) }
func (e *UnknownAdapterError) Unwrap() error { return ErrUnknownAdapter }

type UnknownSessionError struct {
	ID protocol.SessionID
}

func (e *UnknownSessionError) Error() string { return fmt.Sprintf("no session %q", e.ID) }
func (e *UnknownSessionError) Unwrap() error { return ErrUnknownSession }

type SessionExistsError struct {
	ID protocol.SessionID
}

func (e *SessionExistsError) Error() string { return fmt.Sprintf("session %q already exists", e.ID) }
func (e *SessionExistsError) Unwrap() error { return ErrSessionExists }

type ScopeMismatchError struct {
	Payload   protocol.SessionID
	Addressed protocol.SessionID
}

func (e *ScopeMismatchError) Error() string {
	return fmt.Sprintf("payload session_id %q does not match the addressed session %q", e.Payload, e.Addressed)
}
func (e *ScopeMismatchError) Unwrap() error { return ErrScopeMismatch }

type SessionClosedError struct {
	ID protocol.SessionID
}

func (e *SessionClosedError) Error() string { return "the session is closed" }
func (e *SessionClosedError) Unwrap() error { return base.ErrSessionClosed }

func ControlRefusal(err error) (code, message string, details map[string]any, ok bool) {
	var unsupported *base.UnsupportedControlError
	if errors.As(err, &unsupported) {
		details = map[string]any{"feature": unsupported.Feature, "reason": unsupported.Reason}
		if unsupported.Tool != "" {
			details["tool"] = unsupported.Tool
		}
		if unsupported.Field != "" {
			details["field"] = unsupported.Field
		}
		if unsupported.Source != "" {
			details["source"] = unsupported.Source
		}
		return "unsupported_feature", unsupported.Error(), details, true
	}
	var degraded *base.DegradedControlError
	if errors.As(err, &degraded) {
		return "capability_degraded", degraded.Error(), map[string]any{"feature": degraded.Feature}, true
	}
	var missing *base.ModelNotFoundError
	if errors.As(err, &missing) {
		return "model_not_found", missing.Error(), map[string]any{"model_id": missing.ModelID}, true
	}
	return "", "", nil, false
}

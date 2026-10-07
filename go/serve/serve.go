package serve

import (
	"sync"
	"sync/atomic"

	"context"
	"errors"
	"fmt"
	"github.com/lsm/open-agent-protocol/go/binding"
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

	Bindings binding.Store

	Home string
}

type Hub struct {
	registry  *Registry
	sessions  *sessionRegistry
	queue     int
	shutdown  time.Duration
	logger    *log.Logger
	bindings  binding.Store
	home      string
	bindingMu sync.Mutex
	stopping  atomic.Bool
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
		bindings: options.Bindings, home: options.Home,
	}
}

func (h *Hub) Registry() *Registry { return h.registry }

func (h *Hub) Open(ctx context.Context, adapterName string, request base.OpenRequest) (*Session, protocol.SessionState, error) {
	return h.OpenIn(ctx, adapterName, "", request)
}

func (h *Hub) OpenIn(ctx context.Context, adapterName, directory string, request base.OpenRequest) (*Session, protocol.SessionState, error) {
	if _, ok := h.registry.Lookup(adapterName); !ok {
		return nil, protocol.SessionState{}, &UnknownAdapterError{Name: adapterName}
	}
	if request.Participant.ID == "" {
		request.Participant.ID = DefaultParticipant
	}
	if _, live := h.sessions.get(request.SessionID); request.Reopen && live {
		return nil, protocol.SessionState{}, &SessionExistsError{ID: request.SessionID}
	}
	adopting := request.Reopen && request.Adopted && request.NativeSessionID != ""
	if request.Reopen && h.bindings != nil && !adopting {
		bound, found, err := h.bindings.Latest(ctx, string(request.SessionID))
		if err != nil {
			return nil, protocol.SessionState{}, err
		}
		if !found || bound.Record.Adapter != adapterName {
			return nil, protocol.SessionState{}, &UnknownSessionError{ID: request.SessionID}
		}
		request.NativeSessionID = bound.Record.NativeSessionID
		request.Adopted = bound.Record.Adopted
		directory = bound.Record.Directory
	}
	implementation, placed, err := h.registry.Place(adapterName, directory)
	if err != nil {
		return nil, protocol.SessionState{}, err
	}
	session, err := implementation.Open(ctx, request)
	var gone *base.UnknownSessionError
	if request.Reopen && h.bindings != nil && errors.As(err, &gone) {
		return nil, protocol.SessionState{}, &base.UnsupportedControlError{Feature: protocol.FeatureOpenReopen, Reason: base.ControlUnsatisfiable, Detail: "the binding names a session the adapter can no longer load"}
	}
	if err != nil {
		return nil, protocol.SessionState{}, err
	}
	state, err := session.State(ctx)
	if err != nil && !errors.Is(err, base.ErrSessionClosed) {
		_ = session.Close(context.WithoutCancel(ctx))
		return nil, protocol.SessionState{}, err
	}

	entry := newSession(state.SessionID, adapterName, session, func(released *Session) {
		h.bindingMu.Lock()
		defer h.bindingMu.Unlock()
		h.sessions.remove(released.id, released)
		if !h.stopping.Load() {
			h.recordBinding(context.Background(), released.binding, binding.ActionClosed, h.now())
		}
	})
	entry.runs = h.sessions.runs
	entry.work.directory = placed
	opened := h.openRecord(ctx, adapterName, placed, implementation, state, request)
	if native, ok := session.(base.NativeSession); ok {
		opened.NativeSessionID = native.NativeSessionID()
	}
	entry.binding = opened
	settled := err != nil || state.Status == protocol.SessionClosed
	began := binding.ActionOpened
	if request.Reopen {
		began = binding.ActionReopened
	}
	h.bindingMu.Lock()
	var added error
	if settled {
		entry.binding = binding.Record{}
		h.recordBinding(ctx, opened, began, state.UpdatedAtMS)
		h.recordBinding(ctx, opened, binding.ActionClosed, h.now())
	} else if added = h.sessions.add(entry); added == nil {
		h.recordBinding(ctx, opened, began, state.UpdatedAtMS)
	} else {
		h.recordBinding(ctx, opened, binding.ActionRefused, h.now())
	}
	h.bindingMu.Unlock()
	if settled {
		entry.markClosed()
		return entry, state, base.ErrSessionClosed
	}
	if added != nil {
		_ = session.Close(context.WithoutCancel(ctx))
		return nil, protocol.SessionState{}, &SessionExistsError{ID: entry.id}
	}
	return entry, state, nil
}

func (h *Hub) SetWorkingDirectory(adapter, directory string) {
	h.registry.SetWorkingDirectory(adapter, directory)
}

func (h *Hub) now() int64 {
	return time.Now().UnixMilli()
}

func (h *Hub) openRecord(ctx context.Context, adapterName, directory string, implementation base.Adapter, state protocol.SessionState, request base.OpenRequest) binding.Record {
	if h.bindings == nil {
		return binding.Record{}
	}
	version := ""
	if descriptor, err := implementation.Probe(ctx); err == nil {
		version = descriptor.Capabilities.Endpoint.Version
	}
	sources := make([]string, 0, len(request.ToolSources))
	for _, source := range request.ToolSources {
		sources = append(sources, string(source.ID))
	}
	record := binding.FromOpen(string(state.SessionID), adapterName, version, state.CurrentModelID, h.home, directory, sources)
	record.ReasoningLevel = string(request.ReasoningLevel)
	record.Adopted = request.Adopted
	if request.CompactionPolicy != nil {
		policy := *request.CompactionPolicy
		record.CompactionPolicy = &policy
	}
	return record
}

func (h *Hub) recordBinding(ctx context.Context, record binding.Record, action binding.Action, timeMS int64) {
	if h.bindings == nil || record.SessionID == "" {
		return
	}
	if err := h.bindings.Append(context.WithoutCancel(ctx), binding.Entry{Action: action, TimeMS: timeMS, Record: record}); err != nil {
		h.logger.Printf("binding: %s for %s: %v", action, record.SessionID, err)
	}
}

func (h *Hub) Binding() binding.Store {
	return h.bindings
}

func (h *Hub) Published(id protocol.SessionID, request protocol.EnvelopeID) {
	if entry, err := h.Session(id); err == nil {
		entry.Published(request)
	}
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
		state, err := entry.State(ctx)
		if entry.IsClosed() {

			continue
		}
		status := SessionStatus{
			SessionID: entry.id, Adapter: entry.adapterName, CreatedAt: entry.created,
		}
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
	h.stopping.Store(true)
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

	ErrUnknownSession = base.ErrUnknownSession

	ErrSessionExists = errors.New("serve: session already exists")

	ErrScopeMismatch = errors.New("serve: request session does not match the addressed session")

	ErrNoRunToResume = errors.New("the session has no run to replay")
)

type UnknownAdapterError struct {
	Name string
}

func (e *UnknownAdapterError) Error() string { return fmt.Sprintf("no adapter %q", e.Name) }
func (e *UnknownAdapterError) Unwrap() error { return ErrUnknownAdapter }

type UnknownSessionError = base.UnknownSessionError

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
	var steer *base.InvalidSteerTargetError
	if errors.As(err, &steer) {
		return "invalid_steer_target", steer.Error(), map[string]any{"reason": steer.Reason}, true
	}
	return "", "", nil, false
}

// Package serve hosts OAP adapters in process: a named adapter registry,
// multi-session lifecycle over any registered adapter, and fan-out event
// subscriptions with bounded buffers and cursor replay. It is the
// transport-neutral core of the `oap serve` daemon — the serve/servehttp
// package exposes this same hub over HTTP + SSE — so an embedding host gets
// the daemon's full registry semantics (multi-adapter, multi-session,
// per-session subscriptions with resume, gap and overflow signals, session
// listing) with no HTTP hop.
//
// The hub adds no protocol semantics of its own: OAP values cross its methods
// verbatim and it never validates envelopes. Hosts passing untrusted input
// validate at their own boundary, exactly as servehttp does against the
// bundled OAP schema.
//
// A minimal host embeds the default registry and drives one session:
//
//	hub := serve.New(must(serve.DefaultRegistry()), serve.Options{})
//	session, err := hub.Open(ctx, "memory", adapter.OpenRequest{})
//	subscription, err := hub.Subscribe(ctx, session.ID())
//	// Submit, then consume run envelopes through subscription.Next until
//	// io.EOF, resolving interactive gates as they arrive.
package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
)

// DefaultShutdownTimeout bounds the CloseSessions sweep when Options leaves
// the timeout zero.
const DefaultShutdownTimeout = 10 * time.Second

// DefaultParticipant is the responder identity the hub acts as when it opens
// an adapter session whose request names no participant: the v0.1
// session.open exchange carries none, so interactive gates resolve with this
// identifier.
const DefaultParticipant = protocol.ParticipantID("user")

// defaultStreamQueue bounds the envelopes buffered per subscription when
// Options leaves StreamQueue zero.
const defaultStreamQueue = 64

// Options tunes hub behavior. The zero value is usable.
type Options struct {
	// StreamQueue bounds the envelopes buffered per subscription before the
	// subscription is terminated with an OverflowError.
	StreamQueue int
	// Logger receives lifecycle diagnostics. Envelope payloads and resolved
	// environment values are never written to it.
	Logger *log.Logger
	// ShutdownTimeout bounds the CloseSessions sweep and is split across the
	// open sessions so one lingering child cannot consume the whole window.
	// Zero means DefaultShutdownTimeout.
	ShutdownTimeout time.Duration
}

// Hub hosts the sessions of one adapter registry: it opens sessions on any
// registered adapter, tracks them for their whole lifetime (entries survive
// adapter close, listed with their final state), fans each run's events out
// to any number of subscriptions, and settles every session on demand. A Hub
// is safe for concurrent use; construct it with New.
type Hub struct {
	registry *Registry
	sessions *sessionRegistry
	queue    int
	shutdown time.Duration
	logger   *log.Logger
}

// New returns a hub over the registry, which must be non-nil.
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

// Registry returns the adapter registry the hub was built over.
func (h *Hub) Registry() *Registry { return h.registry }

// Open opens one session on the named adapter and returns it together with
// the adapter-reported state that confirmed the open — callers build their
// response from that state rather than re-reading it, so a session that
// opened successfully can never be reported as failed by a second state
// read (leaving it live in the hub while the caller believes the open
// failed). A request with no participant is opened as DefaultParticipant; a
// request with an explicit session id is honoured, and reopening an id the
// hub already tracks fails with *SessionExistsError (the duplicate adapter
// session is closed again before the error returns). The adapter's own open
// failure surfaces verbatim.
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
		return nil, protocol.SessionState{}, err
	}
	entry := newSession(state.SessionID, adapterName, session)
	if err := h.sessions.add(entry); err != nil {
		_ = session.Close(context.WithoutCancel(ctx))
		return nil, protocol.SessionState{}, &SessionExistsError{ID: entry.id}
	}
	return entry, state, nil
}

// Session returns the session registered under id. Entries survive Close —
// a closed session is still returned, reporting its final state — and are
// dropped only when the Hub itself goes away.
func (h *Hub) Session(id protocol.SessionID) (*Session, error) {
	entry, ok := h.sessions.get(id)
	if !ok {
		return nil, &UnknownSessionError{ID: id}
	}
	return entry, nil
}

// AdapterStatus is one registry entry's probe outcome: a healthy adapter
// reports its descriptor; a failing or defective one reports Err.
type AdapterStatus struct {
	// Name is the registry name of the entry.
	Name string
	// Descriptor is the probed capability descriptor; valid when Err is nil.
	Descriptor base.Descriptor
	// Err is the probe failure, or the defect that keeps the descriptor from
	// being relayable (a descriptor with no capability revision).
	Err error
}

// Adapters probes every registered adapter in name order.
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
			// The envelope contract requires a revision on every capability
			// exchange; a descriptor without one cannot be relayed.
			status.Err = errors.New("adapter descriptor carries no capability revision")
		default:
			status.Descriptor = descriptor
		}
		statuses = append(statuses, status)
	}
	return statuses
}

// Probe returns one adapter's capability descriptor. A descriptor without a
// capability revision is rejected: the capabilities exchange requires one on
// every envelope, so such a descriptor cannot be relayed.
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

// SessionStatus is one hosted session's listing entry. Status reports the
// adapter's authoritative session state; a session whose state read fails is
// listed with the error status and no active run.
type SessionStatus struct {
	// SessionID is the session's identifier.
	SessionID protocol.SessionID
	// Adapter is the registry name of the adapter the session runs on.
	Adapter string
	// Status is the adapter-reported session status.
	Status protocol.SessionStatus
	// ActiveRunID names the session's active run, if any.
	ActiveRunID protocol.RunID
	// CreatedAt is when the session was opened through the hub.
	CreatedAt time.Time
}

// Sessions lists every tracked session in id order, closed ones included.
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
		if err != nil && !errors.Is(err, base.ErrSessionClosed) {
			status.Status = protocol.SessionError
			status.ActiveRunID = ""
		}
		statuses = append(statuses, status)
	}
	return statuses
}

// CloseSessions settles and closes every registered session, bounded by ctx
// and by the configured shutdown timeout: active runs refuse Close by
// contract, so each is cancelled first where the adapter supports
// cancellation. The timeout bounds the whole sweep — each session's share is
// carved from the budget still remaining, so many lingering children cannot
// overrun the window the host set. Within that window the split is per
// session: a child that lingers through its own share cannot starve the
// remaining sessions into skipping Close entirely, which would orphan their
// children. In-process hosts own their shutdown; calling this before
// discarding a Hub settles every child process it opened.
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
		// A session needs a real window to settle, but never more than the
		// budget still has: the floor only applies while the whole sweep
		// stays inside the configured timeout.
		if share < 500*time.Millisecond {
			share = 500 * time.Millisecond
		}
		if share > remaining {
			share = remaining
		}
		perSession, cancelSession := context.WithTimeout(sweep, share)
		if err := entry.closeForShutdown(perSession); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, base.ErrSessionClosed) {
			h.logger.Printf("serve: close session %s: %v", entry.id, err)
		}
		cancelSession()
	}
}

// --- hub errors ---
//
// Each hub error carries its diagnostic verbatim (the daemon relays the same
// text on the wire) and wraps a sentinel for errors.Is dispatch.

var (
	// ErrUnknownAdapter reports a registry lookup that matched no entry.
	ErrUnknownAdapter = errors.New("serve: unknown adapter")
	// ErrUnknownSession reports a session id the hub does not track.
	ErrUnknownSession = errors.New("serve: unknown session")
	// ErrSessionExists reports reopening a session id the hub already tracks.
	ErrSessionExists = errors.New("serve: session already exists")
	// ErrScopeMismatch reports a request whose payload names a session other
	// than the one it was addressed to.
	ErrScopeMismatch = errors.New("serve: request session does not match the addressed session")
	// ErrNoRunToResume reports a cursor subscription on a session that has
	// never run: there is nothing to replay.
	ErrNoRunToResume = errors.New("serve: the session has no run to replay")
)

// UnknownAdapterError reports a hub operation addressed to an unregistered
// adapter.
type UnknownAdapterError struct {
	Name string
}

func (e *UnknownAdapterError) Error() string { return fmt.Sprintf("no adapter %q", e.Name) }
func (e *UnknownAdapterError) Unwrap() error { return ErrUnknownAdapter }

// UnknownSessionError reports a hub operation addressed to an untracked
// session.
type UnknownSessionError struct {
	ID protocol.SessionID
}

func (e *UnknownSessionError) Error() string { return fmt.Sprintf("no session %q", e.ID) }
func (e *UnknownSessionError) Unwrap() error { return ErrUnknownSession }

// SessionExistsError reports an open whose session id is already tracked.
type SessionExistsError struct {
	ID protocol.SessionID
}

func (e *SessionExistsError) Error() string { return fmt.Sprintf("session %q already exists", e.ID) }
func (e *SessionExistsError) Unwrap() error { return ErrSessionExists }

// ScopeMismatchError reports a request payload whose session id differs from
// the session it was addressed to.
type ScopeMismatchError struct {
	Payload   protocol.SessionID
	Addressed protocol.SessionID
}

func (e *ScopeMismatchError) Error() string {
	return fmt.Sprintf("payload session_id %q does not match the addressed session %q", e.Payload, e.Addressed)
}
func (e *ScopeMismatchError) Unwrap() error { return ErrScopeMismatch }

// SessionClosedError reports a subscription against a session that was
// already closed through the hub: nothing will ever be delivered, so parking
// would hang the subscriber. It unwraps to adapter.ErrSessionClosed.
type SessionClosedError struct {
	ID protocol.SessionID
}

func (e *SessionClosedError) Error() string { return "the session is closed" }
func (e *SessionClosedError) Unwrap() error { return base.ErrSessionClosed }

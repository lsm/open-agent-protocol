// Package opencode adapts the pinned OpenCode v1.18.29 server to OAP.
package opencode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/opencode/internal/httpapi"
	"github.com/lsm/open-agent-protocol/adapter/opencode/internal/native"
	"github.com/lsm/open-agent-protocol/protocol"
)

const (
	PinnedTag    = native.PinnedTag
	PinnedCommit = "16747470f976aca3d362ad730bcd3fe82ecc2c9a"
	// A revision identifies exactly one descriptor, so a consumer holding the
	// v1 snapshot must see this one as new rather than read the queued
	// admissions it now carries against a descriptor that called the queue
	// unavailable. v2 advertises session.message.delivery.queue with its
	// bounds.
	CapabilityRevision  = "opencode-v1.18.29-oap-v2"
	defaultJournalCap   = 256
	defaultHistoryLimit = 100
	// Settlement polls session.active until the agent loop's drain releases
	// the session. The backoff keeps a long provider turn from issuing one
	// request per few milliseconds for its whole duration.
	defaultSettlePollMin = 10 * time.Millisecond
	defaultSettlePollMax = 500 * time.Millisecond
)

var (
	ErrNativeProtocol = errors.New("opencode adapter: invalid native protocol observation")
	ErrUnsupported    = errors.New("opencode adapter: unsupported input")
)

// Subscription is the per-session durable event stream the reducer consumes.
type Subscription interface {
	Events() <-chan native.Event
	Done() <-chan struct{}
	Err() error
	Close() error
}

// Client is the reduced server surface the adapter depends on.
//
// The pinned server's POST /api/session/:id/wait route is declared in the
// OpenAPI document but not implemented: its handler resolves the session and
// then always fails with ServiceUnavailableError, which the server returns as
// HTTP 503 (upstream asserts this in its own httpapi-session test). So the
// wait route is deliberately absent from this interface and quiescence is
// corroborated with Active, whose set the run coordinator holds for the whole
// agent-loop drain rather than per step.
type Client interface {
	CreateSession(ctx context.Context, request httpapi.CreateSessionRequest) (native.SessionInfo, error)
	Prompt(ctx context.Context, session native.SessionID, request native.PromptRequest) (native.Admitted, error)
	Interrupt(ctx context.Context, session native.SessionID) error
	Active(ctx context.Context) (map[native.SessionID]bool, error)
	History(ctx context.Context, session native.SessionID, after int64, limit int) (native.HistoryPage, error)
	Subscribe(ctx context.Context, session native.SessionID, after int64) (Subscription, error)
	Close() error
}

// ClientFactory opens one client per OAP session.
type ClientFactory interface {
	Start(ctx context.Context) (Client, error)
}
type ClientFactoryFunc func(context.Context) (Client, error)

func (f ClientFactoryFunc) Start(ctx context.Context) (Client, error) { return f(ctx) }

// Config wires the adapter. Credentials are caller-supplied only; ambient
// environment is never read.
type Config struct {
	Factory         ClientFactory
	Endpoint        string
	Username        string
	Password        string
	HTTP            *http.Client
	Agent           string
	Model           *native.ModelRef
	Clock           base.Clock
	IDs             base.IDGenerator
	JournalCapacity int
	FrameLimit      int
	QueueCapacity   int
	HistoryLimit    int
	// SettlePollMin and SettlePollMax bound the backoff between the
	// session.active polls that corroborate settlement. The first poll is
	// immediate, so a run whose loop has already drained settles without
	// sleeping at all.
	SettlePollMin time.Duration
	SettlePollMax time.Duration
}

type Adapter struct {
	config Config
	clock  base.Clock
	ids    base.IDGenerator
}

func New(config Config) (*Adapter, error) {
	if config.Factory == nil && config.Endpoint == "" {
		return nil, errors.New("opencode adapter: factory or endpoint is required")
	}
	if config.Clock == nil {
		config.Clock = systemClock{}
	}
	if config.IDs == nil {
		config.IDs = &sequenceIDs{}
	}
	if config.JournalCapacity <= 0 {
		config.JournalCapacity = defaultJournalCap
	}
	if config.HistoryLimit <= 0 {
		config.HistoryLimit = defaultHistoryLimit
	}
	if config.SettlePollMin <= 0 {
		config.SettlePollMin = defaultSettlePollMin
	}
	if config.SettlePollMax < config.SettlePollMin {
		config.SettlePollMax = defaultSettlePollMax
	}
	if config.SettlePollMax < config.SettlePollMin {
		config.SettlePollMax = config.SettlePollMin
	}
	if config.Factory == nil {
		options := httpapi.Options{Username: config.Username, Password: config.Password, HTTP: config.HTTP, FrameLimit: config.FrameLimit, QueueCapacity: config.QueueCapacity}
		config.Factory = ClientFactoryFunc(func(ctx context.Context) (Client, error) {
			client, err := httpapi.New(config.Endpoint, options)
			if err != nil {
				return nil, err
			}
			return &clientBridge{Client: client}, nil
		})
	}
	return &Adapter{config: config, clock: config.Clock, ids: config.IDs}, nil
}

func (a *Adapter) Probe(ctx context.Context) (base.Descriptor, error) {
	if err := ctx.Err(); err != nil {
		return base.Descriptor{}, err
	}
	features := map[string]protocol.FeatureSupport{
		"protocol.initialize":            {Level: protocol.SupportEmulated, Reason: "OpenCode has no initialize handshake; OpenAPI and catalogs describe the server"},
		"capabilities":                   {Level: protocol.SupportEmulated, Reason: "descriptor synthesized from the pinned route inventory"},
		"session.open":                   {Level: protocol.SupportNative, Reason: "POST /api/session with server-assigned identity"},
		"session.state":                  {Level: protocol.SupportEmulated, Reason: "active set and adapter-owned projection"},
		"session.message.submit":         {Level: protocol.SupportNative, Reason: "durable admission receipt with typed conflict rejection"},
		"session.message.delivery.auto":  {Level: protocol.SupportEmulated, Reason: "no native auto; maps to steer which starts immediately when idle"},
		"session.message.delivery.queue": {Level: protocol.SupportNative, Reason: "SessionInput.Admitted carries delivery=queue with promotedSeq; a reservation is admitted durably and promoted by session.next.prompted"},
		"session.message.delivery.steer": {Level: protocol.SupportUnavailable, Reason: "an explicit steer request is rejected as outside the v0.1 subset; the server's default delivery is exposed through an auto request"},
		"run.streaming":                  {Level: protocol.SupportDegraded, Reason: "durable stream carries full-value text.ended boundaries, not live deltas"},
		"run.status":                     {Level: protocol.SupportNative, Reason: "session.active and durable step events"},
		"run.cancel":                     {Level: protocol.SupportDegraded, Reason: "interrupt is intent with idle no-op; settlement derived from durable evidence and the active set"},
		"run.resume":                     {Level: protocol.SupportDegraded, Reason: "conversation resume exists natively but is not exercised; OAP resume replays the adapter journal"},
		"run.reconciliation":             {Level: protocol.SupportEmulated, Reason: "adapter-owned projection over active and durable sequence"},
		"run.replay":                     {Level: protocol.SupportDegraded, Reason: "bounded adapter journal; the native durable cursor is exposed as the transcript cursor"},
		"action.tools":                   {Level: protocol.SupportNative, Reason: "tool.called/progress/success/failed lifecycle observed natively"},
		"action.tools.execute":           {Level: protocol.SupportUnavailable, Reason: "tools execute server-side; no client-hosted execution surface"},
		"action.permissions":             {Level: protocol.SupportUnavailable, Reason: "durable stream carries no permission events; the polling surface is unexercised"},
		// The pinned ledger names model.list and provider.list as native
		// catalog routes but pins no response shape for either, and an adapter
		// may not decode a shape no pin covers. What is pinned is what this
		// session has run: the session record's model and the model each
		// durable step names. That is served here, and it is why the key is
		// degraded rather than native — it is this session's effective models,
		// not the server's own list, and it grows as steps are observed.
		protocol.FeatureModelsList: {Level: protocol.SupportDegraded, Reason: "the models this session is observed to run, projected from the native session record and durable step events; the server's own model.list route has no pinned response shape at this revision"},
	}
	endpoint := protocol.EndpointDescriptor{ID: "opencode.server", Name: "OpenCode Server Adapter", Version: PinnedTag, Adapter: "opencode-http-sse"}
	return base.Descriptor{
		Capabilities: protocol.CapabilityDescriptor{
			Endpoint: endpoint, ProtocolVersions: []string{protocol.Version}, Profiles: []string{protocol.Profile}, Features: features,
			// One started run beside one reservation. The server queues more
			// than one natively, but this adapter derives settlement from
			// quiescence over a single execution, so a second reservation is
			// beyond what the pin's evidence supports. The bound is disclosed
			// because a queue nothing could ever reach promises nothing.
			Limits: &protocol.CapabilityLimits{
				MaxActiveRunsPerSession: protocol.Limit(maxActiveRuns),
				MaxQueuedRunsPerSession: protocol.Limit(maxQueuedRuns),
			},
		},
		CapabilityRevision:         CapabilityRevision,
		Journal:                    base.JournalDescriptor{Scope: "session", Persistence: "process_memory", Replay: protocol.SupportDegraded, Capacity: a.config.JournalCapacity},
		MaxActiveRunsPerSession:    maxActiveRuns,
		InteractiveGates:           false,
		CancellationTarget:         "run",
		CancellationImplementation: "session_interrupt_with_derived_settlement",
	}, nil
}

func (a *Adapter) Open(ctx context.Context, req base.OpenRequest) (base.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client, err := a.config.Factory.Start(ctx)
	if err != nil {
		return nil, err
	}
	info, err := client.CreateSession(ctx, httpapi.CreateSessionRequest{Agent: a.config.Agent, Model: a.config.Model})
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("create OpenCode session: %w", err)
	}
	// The durable subscription outlives the Open call. If it reused the
	// caller's ctx, a normal `defer cancel()` would tear down the SSE request
	// as soon as Open returned, leaving the session unusable. Give it a
	// session-owned context that Close cancels. One-shot calls such as
	// CreateSession and Prompt keep the caller's ctx.
	subCtx, subCancel := context.WithCancel(context.Background())
	subscription, err := client.Subscribe(subCtx, info.ID, -1)
	if err != nil {
		subCancel()
		_ = client.Close()
		return nil, fmt.Errorf("subscribe OpenCode session events: %w", err)
	}
	id := req.SessionID
	if id == "" {
		id = protocol.SessionID(a.ids.NewID("session"))
	}
	now := a.clock.Now().UnixMilli()
	// The native session reports the model CreateSession selected; fall back to
	// the configured value when the server echoes none.
	model := normalizeModelRef(info.Model)
	if model == "" {
		model = normalizeModelRef(a.config.Model)
	}
	s := &session{
		client:       client,
		subscription: subscription,
		events:       subscription.Events(),
		clock:        a.clock,
		ids:          a.ids,
		capacity:     a.config.JournalCapacity,
		historyLimit: a.config.HistoryLimit,
		pollMin:      a.config.SettlePollMin,
		pollMax:      a.config.SettlePollMax,
		nativeID:     info.ID,
		participant:  req.Participant.ID,
		state:        protocol.SessionState{SessionID: id, Status: protocol.SessionIdle, CurrentModelID: model, UpdatedAtMS: now},
		runs:         map[protocol.RunID]*runState{},
		pending:      map[native.MessageID]*runState{},
		tools:        map[string]*toolState{},
		reduced:      map[int64]bool{},
		stop:         make(chan struct{}),
		subCancel:    subCancel,
	}
	// The session's own model is the first catalog evidence there is; durable
	// steps add whatever else this session turns out to run.
	s.observeModel(model)
	go s.dispatch()
	return s, nil
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type sequenceIDs struct{ next atomic.Uint64 }

func (g *sequenceIDs) NewID(kind string) string {
	n := g.next.Add(1)
	switch kind {
	case "opencode-message":
		return fmt.Sprintf("msg_oap%016d", n)
	default:
		return fmt.Sprintf("%s-%d", kind, n)
	}
}

func formatSeq(seq int64) string { return strconv.FormatInt(seq, 10) }

// clientBridge adapts the concrete subscription type to the adapter's
// Subscription interface.
type clientBridge struct {
	*httpapi.Client
}

func (b *clientBridge) Subscribe(ctx context.Context, session native.SessionID, after int64) (Subscription, error) {
	subscription, err := b.Client.Subscribe(ctx, session, after)
	if err != nil {
		return nil, err
	}
	return subscription, nil
}

var _ base.Adapter = (*Adapter)(nil)

// The graduating adapter for the models unit: it advertises models.list and
// serves the catalog it advertises, at the fidelity the pinned ledger supports.
var _ base.ModelLister = (*session)(nil)

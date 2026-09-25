package opencode

import (
	"context"
	"errors"
	"fmt"
	"github.com/lsm/open-agent-protocol/harnesses"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/httpapi"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

var (
	pin                = harnesses.Current("opencode")
	PinnedTag          = pin.EndpointVersion
	PinnedCommit       = pin.Source("opencode").Commit
	CapabilityRevision = pin.CapabilityRevision
	CorpusDirectory    = pin.Corpus
)

const (
	endpointID          = "opencode.server"
	defaultJournalCap   = 256
	defaultHistoryLimit = 100

	defaultSettlePollMin = 10 * time.Millisecond
	defaultSettlePollMax = 500 * time.Millisecond
)

var (
	ErrNativeProtocol = errors.New("opencode adapter: invalid native protocol observation")
	ErrUnsupported    = errors.New("opencode adapter: unsupported input")
)

type Subscription interface {
	Events() <-chan native.Event
	Done() <-chan struct{}
	Err() error
	Close() error
}

type Client interface {
	CreateSession(ctx context.Context, request httpapi.CreateSessionRequest) (native.SessionInfo, error)
	Prompt(ctx context.Context, session native.SessionID, request native.PromptRequest) (native.Admitted, error)
	Interrupt(ctx context.Context, session native.SessionID) error
	Active(ctx context.Context) (map[native.SessionID]bool, error)
	History(ctx context.Context, session native.SessionID, after int64, limit int) (native.HistoryPage, error)
	Subscribe(ctx context.Context, session native.SessionID, after int64) (Subscription, error)
	Close() error
}

type ClientFactory interface {
	Start(ctx context.Context) (Client, error)
}
type ClientFactoryFunc func(context.Context) (Client, error)

func (f ClientFactoryFunc) Start(ctx context.Context) (Client, error) { return f(ctx) }

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

		protocol.FeatureModelsList: {Level: protocol.SupportDegraded, Reason: "the models this session is observed to run, projected from the native session record and durable step events; the server's own model.list route has no pinned response shape at this revision"},
	}
	endpoint := protocol.EndpointDescriptor{ID: endpointID, Name: "OpenCode Server Adapter", Version: PinnedTag, Adapter: "opencode-http-sse"}
	return base.Descriptor{
		Capabilities: protocol.CapabilityDescriptor{
			Endpoint: endpoint, ProtocolVersions: []string{protocol.Version}, Profiles: []string{protocol.Profile}, Features: features,

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

	if err := base.RefuseUnadvertisedToolSources(req); err != nil {
		return nil, err
	}

	if err := base.RefuseUnadvertisedTools(req); err != nil {
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

var _ base.ModelLister = (*session)(nil)

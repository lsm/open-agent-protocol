package opencode

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	endpointID            = "opencode.server"
	defaultJournalCap     = 256
	defaultRequestTimeout = 60 * time.Second
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
	Session(ctx context.Context, session native.SessionID) (native.SessionInfo, error)
	SwitchModel(ctx context.Context, session native.SessionID, model native.ModelRef) error
	Prompt(ctx context.Context, session native.SessionID, request native.PromptRequest) (native.Admitted, error)
	Interrupt(ctx context.Context, session native.SessionID) (bool, error)
	CancelInbox(ctx context.Context, session native.SessionID, inbox native.MessageID) error
	Active(ctx context.Context) (map[native.SessionID]bool, error)
	Subscribe(ctx context.Context, session native.SessionID) (Subscription, error)
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
	RequestTimeout  time.Duration
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
		config.IDs = newSequenceIDs()
	}
	if config.JournalCapacity <= 0 {
		config.JournalCapacity = defaultJournalCap
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = defaultRequestTimeout
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

func advertisedFeatures() map[string]protocol.FeatureSupport {
	return map[string]protocol.FeatureSupport{
		"protocol.initialize":            {Level: protocol.SupportEmulated, Reason: "OpenCode has no initialize handshake; OpenAPI and catalogs describe the server"},
		"capabilities":                   {Level: protocol.SupportEmulated, Reason: "descriptor synthesized from the pinned route inventory"},
		"session.open":                   {Level: protocol.SupportNative, Reason: "POST /api/session with server-assigned identity"},
		protocol.FeatureOpenReopen:       {Level: protocol.SupportNative, Reason: reopenSupportReason},
		"session.state":                  {Level: protocol.SupportEmulated, Reason: "active set and adapter-owned projection"},
		"session.message.submit":         {Level: protocol.SupportNative, Reason: "durable admission receipt with typed conflict rejection"},
		"session.message.delivery.auto":  {Level: protocol.SupportEmulated, Reason: "no native auto; steer when the session is idle, queue behind an open run"},
		"session.message.delivery.queue": {Level: protocol.SupportNative, Reason: "a prompt with delivery=queue is admitted to the session inbox and starts its run at session.inbox.delivered"},
		"session.message.delivery.steer": {Level: protocol.SupportUnavailable, Reason: "an explicit steer request is rejected as outside the v0.1 subset; the server's default delivery is exposed through an auto request"},
		"run.streaming":                  {Level: protocol.SupportNative, Reason: "session.text.delta and session.reasoning.delta are forwarded as they arrive, and a part's ended event adds only the text its deltas did not carry"},
		"run.status":                     {Level: protocol.SupportNative, Reason: "session.inbox.delivered starts a run and session.execution.* settles it"},
		"run.cancel":                     {Level: protocol.SupportDegraded, Reason: "interrupt is intent with an idle no-op; a running run settles at session.execution.interrupted and a queued one at session.inbox.cancelled"},
		"run.resume":                     {Level: protocol.SupportDegraded, Reason: "conversation resume exists natively but is not exercised; OAP resume replays the adapter journal"},
		"run.reconciliation":             {Level: protocol.SupportEmulated, Reason: "adapter-owned projection over the session events; when the event stream ends it subscribes again once and reconciles the open runs from the session record"},
		"run.replay":                     {Level: protocol.SupportDegraded, Reason: "bounded adapter journal; the native durable cursor is exposed as the transcript cursor"},
		"action.tools":                   {Level: protocol.SupportNative, Reason: "tool.called/progress/success/failed lifecycle observed natively"},
		"action.tools.execute":           {Level: protocol.SupportUnavailable, Reason: "tools execute server-side; no client-hosted execution surface"},
		"action.permissions":             {Level: protocol.SupportUnavailable, Reason: "permission.asked travels only on the volatile global event stream and is not served"},

		protocol.FeatureSessionReasoning: {Level: protocol.SupportNative, Modes: []string{protocol.ModeSessionOpen, protocol.ModeSessionLive}, Reason: "the session's model carries the level as its variant, which the runner sends on every step: set at create and between runs by switching the session to the same model with the new variant; it needs a model, and a variant the session record does not confirm is refused"},
		protocol.FeatureCompactionPolicy: {Level: protocol.SupportUnavailable, Reason: "compaction is the server's config, fixed when its operator starts it; the adapter attaches to a running server"},

		protocol.FeatureModelsList: {Level: protocol.SupportDegraded, Reason: "the models this session is observed to run, projected from the native session record and durable step events; the server's own model.list route has no pinned response shape at this revision"},
	}
}

func (a *Adapter) Probe(ctx context.Context) (base.Descriptor, error) {
	if err := ctx.Err(); err != nil {
		return base.Descriptor{}, err
	}
	endpoint := protocol.EndpointDescriptor{ID: endpointID, Name: "OpenCode Server Adapter", Version: PinnedTag, Adapter: "opencode-http-sse"}
	return base.Descriptor{
		Capabilities: protocol.CapabilityDescriptor{
			Endpoint: endpoint, ProtocolVersions: []string{protocol.Version}, Profiles: []string{protocol.Profile}, Features: advertisedFeatures(),

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
	descriptor, err := a.Probe(ctx)
	if err != nil {
		return nil, err
	}
	if err := base.RefuseUnadvertisedSettings(req, descriptor.Capabilities); err != nil {
		return nil, err
	}
	createModel := a.config.Model
	if req.ReasoningLevel != "" && !req.Reopen {
		if a.config.Model == nil {
			return nil, &base.UnsupportedControlError{Feature: protocol.FeatureSessionReasoning, Reason: base.ControlUnsatisfiable, Field: "reasoning_level", Detail: "a variant rides on a model, and this adapter has none configured"}
		}
		withVariant := *a.config.Model
		withVariant.Variant = string(req.ReasoningLevel)
		createModel = &withVariant
	}
	client, err := a.config.Factory.Start(ctx)
	if err != nil {
		return nil, err
	}
	var info native.SessionInfo
	if req.Reopen {
		info, err = reloadBinding(ctx, client, req.NativeSessionID)
		if err != nil {
			_ = client.Close()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, reopenRefusal(err)
		}
	} else if info, err = client.CreateSession(ctx, httpapi.CreateSessionRequest{Agent: a.config.Agent, Model: createModel}); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("create OpenCode session: %w", err)
	}
	if !req.Reopen && req.ReasoningLevel != "" && (info.Model == nil || info.Model.Variant != string(req.ReasoningLevel)) {
		_ = client.Close()
		return nil, &base.UnsupportedControlError{Feature: protocol.FeatureSessionReasoning, Reason: base.ControlUnsatisfiable, Field: "reasoning_level", Detail: "OpenCode did not record the variant on the session it created"}
	}

	subCtx, subCancel := context.WithCancel(context.Background())
	waitCtx, waitCancel := context.WithTimeout(ctx, a.config.RequestTimeout)
	stopWaiting := context.AfterFunc(waitCtx, subCancel)
	subscription, err := client.Subscribe(subCtx, info.ID)
	waited := !stopWaiting()
	waitCancel()
	if err != nil || waited {
		if err == nil {
			_ = subscription.Close()
		}
		subCancel()
		_ = client.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if waited {
			return nil, errors.New("subscribe OpenCode session events: no server.connected within the request timeout")
		}
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
		timeout:      a.config.RequestTimeout,
		nativeID:     info.ID,
		model:        info.Model,
		participant:  req.Participant.ID,
		state:        protocol.SessionState{SessionID: id, Status: protocol.SessionIdle, CurrentModelID: model, UpdatedAtMS: now, ReasoningLevel: req.ReasoningLevel},
		runs:         map[protocol.RunID]*runState{},
		pending:      map[native.MessageID]*runState{},
		tools:        map[string]*toolState{},
		toolNames:    map[string]string{},
		reduced:      map[int64]bool{},
		stop:         make(chan struct{}),
		subCancel:    subCancel,
	}

	if req.Reopen {
		s.restoreState()
	}
	s.observeModel(model)
	go s.dispatch()
	if req.Reopen && req.ReasoningLevel != "" {
		if _, _, err := s.UpdateSettings(ctx, protocol.SessionSettingsUpdateRequest{SessionID: id, ReasoningLevel: req.ReasoningLevel}); err != nil {
			_ = s.Close(context.Background())
			return nil, err
		}
	}
	return s, nil
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type sequenceIDs struct {
	next    atomic.Uint64
	message string
}

func newSequenceIDs() *sequenceIDs {
	var nonce [8]byte
	_, _ = rand.Read(nonce[:])
	return &sequenceIDs{message: "msg_oap" + hex.EncodeToString(nonce[:])}
}

func (g *sequenceIDs) NewID(kind string) string {
	n := g.next.Add(1)
	switch kind {
	case "opencode-message":
		return fmt.Sprintf("%s%016d", g.message, n)
	default:
		return fmt.Sprintf("%s-%d", kind, n)
	}
}

func formatSeq(seq int64) string { return strconv.FormatInt(seq, 10) }

type clientBridge struct {
	*httpapi.Client
}

func (b *clientBridge) Subscribe(ctx context.Context, session native.SessionID) (Subscription, error) {
	subscription, err := b.Client.Subscribe(ctx, session)
	if err != nil {
		return nil, err
	}
	return subscription, nil
}

var _ base.Adapter = (*Adapter)(nil)

var _ base.ModelLister = (*session)(nil)

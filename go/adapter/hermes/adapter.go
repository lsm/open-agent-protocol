package hermes

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/hermes/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/hermes/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const (
	endpointID             = "hermes.gateway"
	PinnedVersion          = native.ReleaseTag
	CapabilityRevision     = "hermes-v2026.8.31-oap-v1"
	defaultJournalCapacity = 256
	relayCapacity          = 256
)

var ErrNativeProtocol = errors.New("hermes adapter: invalid native protocol observation")

type Client interface {
	Call(context.Context, string, any, any) error
	Inbound() <-chan rpc.InboundMessage
	Done() <-chan struct{}

	ReadDone() <-chan struct{}
	Err() error
	Close() error
}

type ClientFactory interface {
	Start(context.Context) (Client, string, error)
}
type ClientFactoryFunc func(context.Context) (Client, string, error)

func (f ClientFactoryFunc) Start(ctx context.Context) (Client, string, error) { return f(ctx) }

type ProcessBridge interface {
	ClientHandle() Client
	Done() <-chan struct{}
	WaitError() error
	Close(context.Context) error
}
type ProcessFactory interface {
	Start(context.Context, rpc.ProcessConfig) (ProcessBridge, error)
}
type ProcessFactoryFunc func(context.Context, rpc.ProcessConfig) (ProcessBridge, error)

func (f ProcessFactoryFunc) Start(ctx context.Context, c rpc.ProcessConfig) (ProcessBridge, error) {
	return f(ctx, c)
}

type Config struct {
	Factory            ClientFactory
	ProcessFactory     ProcessFactory
	Executable         string
	Args               []string
	Environment        []string
	WorkingDirectory   string
	Model              string
	Clock              base.Clock
	IDs                base.IDGenerator
	JournalCapacity    int
	FrameLimit         int
	QueueCapacity      int
	WriteQueueCapacity int
	ExitTimeout        time.Duration
}

type Adapter struct {
	config Config
	clock  base.Clock
	ids    base.IDGenerator
}

func New(config Config) (*Adapter, error) {
	if config.Factory == nil && config.ProcessFactory == nil && config.Executable == "" {
		return nil, errors.New("hermes adapter: factory or executable is required")
	}
	if config.Clock == nil {
		config.Clock = systemClock{}
	}
	if config.IDs == nil {
		config.IDs = &sequenceIDs{}
	}
	if config.JournalCapacity <= 0 {
		config.JournalCapacity = defaultJournalCapacity
	}
	if config.ProcessFactory == nil {
		config.ProcessFactory = ProcessFactoryFunc(func(ctx context.Context, c rpc.ProcessConfig) (ProcessBridge, error) {
			p, err := rpc.Start(ctx, c)
			if err != nil {
				return nil, err
			}
			return &rpcProcess{p}, nil
		})
	}
	if config.Factory == nil {

		env := config.Environment
		if env != nil {
			env = append([]string{}, env...)
		}
		pc := rpc.ProcessConfig{Path: config.Executable, Args: append([]string(nil), config.Args...), Dir: config.WorkingDirectory, Env: env, FrameLimit: config.FrameLimit, QueueCapacity: config.QueueCapacity, WriteQueueCapacity: config.WriteQueueCapacity, ExitTimeout: config.ExitTimeout}
		config.Factory = ClientFactoryFunc(func(ctx context.Context) (Client, string, error) {
			p, err := config.ProcessFactory.Start(ctx, pc)
			if err != nil {
				return nil, "", err
			}
			client := p.ClientHandle()

			relay := make(chan rpc.InboundMessage, relayCapacity)
			go relayInbound(client, relay)

			var created native.SessionCreateResult
			if err := client.Call(ctx, native.MethodSessionCreate, native.SessionCreateParams{Model: config.Model}, &created); err != nil {
				_ = p.Close(context.Background())
				return nil, "", err
			}
			if created.SessionID == "" || created.StoredSessionID == "" {
				_ = p.Close(context.Background())
				return nil, "", ErrNativeProtocol
			}
			return &sessionClient{Client: client, bridge: p, session: created, inbound: relay}, created.SessionID, nil
		})
	}
	return &Adapter{config: config, clock: config.Clock, ids: config.IDs}, nil
}

func relayInbound(client Client, relay chan rpc.InboundMessage) {
	defer close(relay)
	inbound := client.Inbound()
	forward := func(message rpc.InboundMessage) {
		if message.Barrier != nil {
			close(message.Barrier)
			return
		}
		relay <- message
	}
	for {
		select {
		case message, ok := <-inbound:
			if !ok {
				return
			}
			forward(message)
		case <-client.Done():
			for {
				select {
				case message, ok := <-inbound:
					if !ok {
						return
					}
					forward(message)
				case <-client.ReadDone():
					for {
						select {
						case message, ok := <-inbound:
							if !ok {
								return
							}
							forward(message)
						default:
							return
						}
					}
				}
			}
		}
	}
}

type rpcProcess struct{ *rpc.Process }

func (p *rpcProcess) ClientHandle() Client { return p.Client }

type sessionClient struct {
	Client
	bridge  ProcessBridge
	session native.SessionCreateResult
	inbound chan rpc.InboundMessage
}

func (p *sessionClient) Inbound() <-chan rpc.InboundMessage { return p.inbound }

func (p *sessionClient) Done() <-chan struct{} { return p.bridge.Done() }
func (p *sessionClient) Err() error {
	if err := p.bridge.WaitError(); err != nil {
		return err
	}
	return p.Client.Err()
}
func (p *sessionClient) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return p.bridge.Close(ctx)
}

func (a *Adapter) Probe(ctx context.Context) (base.Descriptor, error) {
	if err := ctx.Err(); err != nil {
		return base.Descriptor{}, err
	}
	features := map[string]protocol.FeatureSupport{
		"protocol.initialize":            {Level: protocol.SupportNative, Reason: "gateway.ready frame with replay epoch before any input"},
		"capabilities":                   {Level: protocol.SupportEmulated, Reason: "conservative descriptor for the pinned gateway"},
		"session.open":                   {Level: protocol.SupportNative, Reason: "session.create mints the runtime session id"},
		"session.state":                  {Level: protocol.SupportDegraded, Reason: "reducer-owned live projection corroborated by session.info"},
		"session.message.submit":         {Level: protocol.SupportDegraded, Reason: "status-only result; ownership by construction via message.start"},
		"session.message.delivery.auto":  {Level: protocol.SupportDegraded, Reason: "accepted only for known idle sessions"},
		"session.message.delivery.queue": {Level: protocol.SupportUnavailable, Reason: "busy statuses are rejected rather than guessed"},
		"session.message.delivery.steer": {Level: protocol.SupportUnavailable, Reason: "native session.steer not exposed in v1"},
		"run.streaming":                  {Level: protocol.SupportNative, Reason: "immediate frames on stdio; post-scrubber provenance disclosed"},
		"run.status":                     {Level: protocol.SupportEmulated},
		"run.cancel":                     {Level: protocol.SupportDegraded, Reason: "session.interrupt intent; settlement via message.complete interrupted"},
		"run.resume":                     {Level: protocol.SupportUnavailable, Reason: "recovery family not exposed in v1"},
		"run.reconciliation":             {Level: protocol.SupportDegraded, Reason: "session.info running/turn_started_at corroboration"},
		"run.replay":                     {Level: protocol.SupportUnavailable, Reason: "bounded native replay not exposed in v1"},
		"action.tools":                   {Level: protocol.SupportDegraded, Reason: "tool.start/complete only; started synthesized; failures ride in result without a pinned discriminator"},
		"action.tools.execute":           {Level: protocol.SupportUnavailable, Reason: "the gateway executes tools internally"},
		"action.permissions":             {Level: protocol.SupportDegraded, Reason: "approval gates surface as input interactions"},
		"user_input":                     {Level: protocol.SupportNative, Reason: "approval/clarify/sudo/secret gates with expire siblings"},
	}
	return base.Descriptor{Capabilities: protocol.CapabilityDescriptor{Endpoint: protocol.EndpointDescriptor{ID: endpointID, Name: "Hermes Gateway Adapter", Version: PinnedVersion, Adapter: "hermes-tui-gateway"}, ProtocolVersions: []string{protocol.Version}, Profiles: []string{protocol.Profile}, Features: features}, CapabilityRevision: CapabilityRevision, Journal: base.JournalDescriptor{Scope: "session", Persistence: "process_memory", Replay: protocol.SupportUnavailable, Capacity: a.config.JournalCapacity}, MaxActiveRunsPerSession: 1, InteractiveGates: true, CancellationTarget: "session", CancellationImplementation: "session.interrupt"}, nil
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

	client, nativeID, err := a.config.Factory.Start(ctx)
	if err != nil {
		return nil, err
	}
	if nativeID == "" {
		_ = client.Close()
		return nil, ErrNativeProtocol
	}
	id := req.SessionID
	if id == "" {
		id = protocol.SessionID(a.ids.NewID("session"))
	}
	now := a.clock.Now().UnixMilli()
	s := &Session{client: client, inbound: client.Inbound(), clock: a.clock, ids: a.ids, capacity: a.config.JournalCapacity, nativeID: nativeID, participant: participant(req.Participant), state: protocol.SessionState{SessionID: id, Status: protocol.SessionIdle, CurrentModelID: a.config.Model, UpdatedAtMS: now}, runs: map[protocol.RunID]*runState{}, tools: map[string]*toolState{}, interactions: map[protocol.InteractionID]*inputState{}, stop: make(chan struct{})}
	go s.dispatch()
	return s, nil
}

func participant(p protocol.Participant) protocol.ParticipantID {
	if p.ID != "" {
		return p.ID
	}
	return "user"
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type sequenceIDs struct{ next atomic.Uint64 }

func (g *sequenceIDs) NewID(kind string) string { return fmt.Sprintf("%s-%d", kind, g.next.Add(1)) }

var _ base.Adapter = (*Adapter)(nil)

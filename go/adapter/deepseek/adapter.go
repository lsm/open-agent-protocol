package deepseek

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/deepseek/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/deepseek/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/adapter/internal/journal"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const (
	endpointID             = "deepseek.harness"
	PinnedVersion          = native.ServerVersion
	CapabilityRevision     = "deepseek-harness-47f9438-oap-v2"
	CorpusDirectory        = "fixtures/adapters/deepseek-harness-47f9438"
	defaultJournalCapacity = 256
)

var ErrNativeProtocol = errors.New("deepseek adapter: invalid native protocol observation")

type Client interface {
	Call(context.Context, string, any, any) error
	CallStarted(context.Context, string, any, any, chan<- error) error
	Inbound() <-chan rpc.InboundMessage
	Done() <-chan struct{}
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
	Provider           string
	Model              string
	MaxTokens          *int64
	Clock              base.Clock
	IDs                base.IDGenerator
	JournalCapacity    int
	FrameLimit         int
	QueueCapacity      int
	WriteQueueCapacity int
	ShutdownTimeout    time.Duration
}

type Adapter struct {
	config Config
	clock  base.Clock
	ids    base.IDGenerator
}

func New(config Config) (*Adapter, error) {
	if config.Factory == nil && config.ProcessFactory == nil && config.Executable == "" {
		return nil, errors.New("deepseek adapter: factory or executable is required")
	}
	if config.Factory == nil {
		if config.WorkingDirectory == "" || !filepath.IsAbs(config.WorkingDirectory) {
			return nil, errors.New("deepseek adapter: absolute working directory is required")
		}
		if err := native.ValidateInitializeParams(native.InitializeParams{Cwd: config.WorkingDirectory, Provider: config.Provider, Model: config.Model, MaxTokens: config.MaxTokens}); err != nil {
			return nil, err
		}
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
		pc := rpc.ProcessConfig{Path: config.Executable, Args: append([]string(nil), config.Args...), Dir: config.WorkingDirectory, Env: env, FrameLimit: config.FrameLimit, QueueCapacity: config.QueueCapacity, WriteQueueCapacity: config.WriteQueueCapacity, ShutdownTimeout: config.ShutdownTimeout, Initialize: native.InitializeParams{Cwd: config.WorkingDirectory, Provider: config.Provider, Model: config.Model, MaxTokens: config.MaxTokens}}
		config.Factory = ClientFactoryFunc(func(ctx context.Context) (Client, string, error) {
			p, err := config.ProcessFactory.Start(ctx, pc)
			if err != nil {
				return nil, "", err
			}
			return &processClient{Client: p.ClientHandle(), bridge: p}, config.Model, nil
		})
	}
	return &Adapter{config: config, clock: config.Clock, ids: config.IDs}, nil
}

type rpcProcess struct{ *rpc.Process }

func (p *rpcProcess) ClientHandle() Client { return p.Client }

type processClient struct {
	Client
	bridge ProcessBridge
}

func (p *processClient) Done() <-chan struct{} { return p.bridge.Done() }
func (p *processClient) Err() error {
	if err := p.bridge.WaitError(); err != nil {
		return err
	}
	return p.Client.Err()
}
func (p *processClient) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.bridge.Close(ctx)
}

func (a *Adapter) Probe(ctx context.Context) (base.Descriptor, error) {
	if err := ctx.Err(); err != nil {
		return base.Descriptor{}, err
	}
	features := map[string]protocol.FeatureSupport{
		"protocol.initialize":            {Level: protocol.SupportEmulated, Reason: "adapter-owned one-shot initialization freeze"},
		"capabilities":                   {Level: protocol.SupportEmulated, Reason: "conservative descriptor for pinned SDK wire"},
		"session.open":                   {Level: protocol.SupportEmulated, Reason: "one process and native session per OAP session"},
		"session.state":                  {Level: protocol.SupportDegraded, Reason: "reducer-owned live projection; no native query"},
		"session.message.submit":         {Level: protocol.SupportDegraded, Reason: "receipt plus entered direct-user message proves start"},
		"session.message.delivery.auto":  {Level: protocol.SupportDegraded, Reason: "accepted only for known idle sessions and normalized to start"},
		"session.message.delivery.queue": {Level: protocol.SupportUnavailable, Reason: "overlapping native followups are outside the adapter contract"},
		"session.message.delivery.steer": {Level: protocol.SupportUnavailable, Reason: "selected SDK wire has no steer request"},
		"run.streaming":                  {Level: protocol.SupportNative}, "run.status": {Level: protocol.SupportEmulated},
		"run.cancel":           {Level: protocol.SupportUnavailable, Reason: "selected SDK wire has no cancel request"},
		"run.resume":           {Level: protocol.SupportDegraded, Reason: "selected SDK wire has no resume request; OAP resume replays the adapter journal"},
		"run.reconciliation":   {Level: protocol.SupportDegraded, Reason: "live status corroboration only"},
		"run.replay":           {Level: protocol.SupportDegraded, Reason: "bounded adapter journal; gaps are explicit and there is no native replay request"},
		"action.tools":         {Level: protocol.SupportDegraded, Reason: "call/result only; started is synthesized"},
		"action.tools.execute": {Level: protocol.SupportUnavailable, Reason: "Harness executes tools internally"},
		"action.permissions":   {Level: protocol.SupportUnavailable, Reason: "selected SDK wire has no interaction channel"},
	}
	return base.Descriptor{Capabilities: protocol.CapabilityDescriptor{Endpoint: protocol.EndpointDescriptor{ID: endpointID, Name: "DeepSeek Harness SDK Adapter", Version: PinnedVersion, Adapter: "deepseek-harness-jsonrpc"}, ProtocolVersions: []string{protocol.Version}, Profiles: []string{protocol.Profile}, Features: features}, CapabilityRevision: CapabilityRevision, Journal: base.JournalDescriptor{Scope: "session", Persistence: "process_memory", Replay: protocol.SupportDegraded, Capacity: a.config.JournalCapacity}, MaxActiveRunsPerSession: 1, InteractiveGates: false, CancellationTarget: "none", CancellationImplementation: "unavailable"}, nil
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
	client, model, err := a.config.Factory.Start(ctx)
	if err != nil {
		return nil, err
	}
	id := req.SessionID
	if id == "" {
		id = protocol.SessionID(a.ids.NewID("session"))
	}
	now := a.clock.Now().UnixMilli()
	s := &Session{client: client, inbound: client.Inbound(), clock: a.clock, ids: a.ids, journal: journal.New(a.config.JournalCapacity), nativeID: string(id), model: model, state: protocol.SessionState{SessionID: id, Status: protocol.SessionIdle, CurrentModelID: model, UpdatedAtMS: now}, runs: map[protocol.RunID]*runState{}, tools: map[string]*toolState{}, children: map[string]*childState{}, stop: make(chan struct{})}
	go s.dispatch()
	return s, nil
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type sequenceIDs struct{ next atomic.Uint64 }

func (g *sequenceIDs) NewID(kind string) string { return fmt.Sprintf("%s-%d", kind, g.next.Add(1)) }

var _ base.Adapter = (*Adapter)(nil)

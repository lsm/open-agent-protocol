package appserver

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/codex/appserver/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/codex/appserver/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const (
	endpointID             = "codex.app-server"
	CodexCommit            = "8d7cc24a87f4aa66aa434eb4f25f4f4bafc0e0a9"
	CapabilityRevision     = "codex-appserver-8d7cc24-oap-v1"
	defaultJournalCapacity = 256
)

var (
	ErrNativeProtocol   = errors.New("codex app-server adapter: invalid native protocol observation")
	ErrUnsupportedInput = errors.New("codex app-server adapter: unsupported input")
)

type Client interface {
	Call(context.Context, string, any, any) error
	Notify(context.Context, string, any) error

	Inbound() <-chan rpc.InboundMessage
	Done() <-chan struct{}
	Err() error
	Close() error
}

type ClientFactory interface {
	Start(context.Context) (Client, error)
}

type ClientFactoryFunc func(context.Context) (Client, error)

func (factory ClientFactoryFunc) Start(ctx context.Context) (Client, error) { return factory(ctx) }

type Config struct {
	Factory          ClientFactory
	Executable       string
	Args             []string
	Environment      []string
	WorkingDirectory string
	Model            string
	ApprovalPolicy   string
	Sandbox          string
	ResumeThreadID   string
	Clock            adapter.Clock
	IDs              adapter.IDGenerator
	JournalCapacity  int
	FrameLimit       int
	ShutdownTimeout  time.Duration
}

type Adapter struct {
	config Config
	clock  adapter.Clock
	ids    adapter.IDGenerator
}

func New(config Config) (*Adapter, error) {
	if config.Factory == nil && config.Executable == "" {
		return nil, errors.New("codex app-server adapter: factory or executable is required")
	}
	clock := config.Clock
	if clock == nil {
		clock = systemClock{}
	}
	ids := config.IDs
	if ids == nil {
		ids = &sequenceIDs{}
	}
	if config.JournalCapacity <= 0 {
		config.JournalCapacity = defaultJournalCapacity
	}
	if config.Factory == nil {
		processConfig := rpc.ProcessConfig{
			Path: config.Executable, Args: config.Args, Env: config.Environment,
			Dir: config.WorkingDirectory, FrameLimit: config.FrameLimit,
			ShutdownTimeout: config.ShutdownTimeout,
			ClientInfo:      rpc.ClientInfo{Name: "open-agent-protocol", Version: protocol.Version},
		}
		config.Factory = ClientFactoryFunc(func(ctx context.Context) (Client, error) {
			process, err := rpc.Start(ctx, processConfig)
			if err != nil {
				return nil, err
			}
			return &processClient{Process: process}, nil
		})
	}
	return &Adapter{config: config, clock: clock, ids: ids}, nil
}

type processClient struct{ *rpc.Process }

func (client *processClient) Call(ctx context.Context, method string, params any, result any) error {
	return client.Process.Client.Call(ctx, method, params, result)
}

func (client *processClient) Notify(ctx context.Context, method string, params any) error {
	return client.Process.Client.Notify(ctx, method, params)
}

func (client *processClient) Inbound() <-chan rpc.InboundMessage {
	return client.Process.Client.Inbound()
}

func (client *processClient) Done() <-chan struct{} { return client.Process.Client.Done() }
func (client *processClient) Err() error            { return client.Process.Client.Err() }

func (client *processClient) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return client.Process.Close(ctx)
}

func (implementation *Adapter) Probe(context.Context) (adapter.Descriptor, error) {
	features := map[string]protocol.FeatureSupport{
		"protocol.initialize":           {Level: protocol.SupportNative},
		"capabilities":                  {Level: protocol.SupportNative},
		"session.open":                  {Level: protocol.SupportNative},
		"session.state":                 {Level: protocol.SupportNative},
		"session.message.submit":        {Level: protocol.SupportNative},
		"session.message.delivery.auto": {Level: protocol.SupportNative},

		protocol.FeatureModelSelection:   {Level: protocol.SupportNative, Mode: protocol.ModePerRun, Reason: "turn/start carries the model for one turn"},
		protocol.FeatureInstructions:     {Level: protocol.SupportUnavailable, Reason: "this pin exposes no per-turn instruction override"},
		protocol.FeatureToolSelection:    {Level: protocol.SupportUnavailable, Reason: "this pin exposes no per-turn tool policy"},
		protocol.FeatureStructuredOutput: {Level: protocol.SupportUnavailable, Reason: "this pin exposes no per-turn output schema"},
		"run.streaming":                  {Level: protocol.SupportNative},
		"run.status":                     {Level: protocol.SupportNative},
		"run.cancel":                     {Level: protocol.SupportNative, Reason: "turn/interrupt targets an exact native thread and turn; settlement is asynchronous"},
		"run.resume":                     {Level: protocol.SupportDegraded, Reason: "thread/resume restores native attachment; canonical replay is bounded process memory"},
		"run.reconciliation":             {Level: protocol.SupportEmulated, Reason: "state is the adapter's canonical projection of native observations"},
		"run.replay":                     {Level: protocol.SupportDegraded, Reason: "only adapter-emitted events in bounded process memory are replayable"},
		"action.tools":                   {Level: protocol.SupportDegraded, Reason: "only pinned command, file-change, and MCP item families are normalized"},
		"action.tools.execute":           {Level: protocol.SupportDegraded, Reason: "only pinned command, file-change, and MCP item families are normalized"},
		"action.permissions":             {Level: protocol.SupportNative, Reason: "command and file-change reverse approvals are correlated and round-trip once"},
		"user_input":                     {Level: protocol.SupportDegraded, Reason: "Codex option questions normalize to OAP single-choice input"},
	}
	endpoint := protocol.EndpointDescriptor{ID: endpointID, Name: "Codex app-server Adapter", Version: CodexCommit, Adapter: "codex-appserver-stdio"}
	return adapter.Descriptor{
		Capabilities:               protocol.CapabilityDescriptor{Endpoint: endpoint, ProtocolVersions: []string{protocol.Version}, Profiles: []string{protocol.Profile}, Features: features},
		CapabilityRevision:         CapabilityRevision,
		Journal:                    adapter.JournalDescriptor{Scope: "session", Persistence: "process_memory", Replay: protocol.SupportDegraded, Capacity: implementation.config.JournalCapacity},
		MaxActiveRunsPerSession:    1,
		InteractiveGates:           true,
		CancellationTarget:         "run",
		CancellationImplementation: "native_turn_interrupt",
	}, nil
}

func (implementation *Adapter) Open(ctx context.Context, request adapter.OpenRequest) (adapter.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := adapter.RefuseUnadvertisedToolSources(request); err != nil {
		return nil, err
	}

	if err := adapter.RefuseUnadvertisedTools(request); err != nil {
		return nil, err
	}

	if request.Participant.ID == "" {
		return nil, adapter.ErrInvalidParticipant
	}
	client, err := implementation.config.Factory.Start(ctx)
	if err != nil {
		return nil, err
	}
	threadID := implementation.config.ResumeThreadID
	if threadID == "" {
		params := native.ThreadStartParams{Model: implementation.config.Model, Cwd: implementation.config.WorkingDirectory, ApprovalPolicy: implementation.config.ApprovalPolicy, Sandbox: implementation.config.Sandbox}
		var response native.ThreadStartResponse
		if err := client.Call(ctx, native.MethodThreadStart, params, &response); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("start Codex thread: %w", err)
		}
		threadID = response.Thread.ID
		if threadID == "" {
			_ = client.Close()
			return nil, fmt.Errorf("%w: thread/start returned no thread id", ErrNativeProtocol)
		}
	} else {
		var response native.ThreadResumeResponse
		if err := client.Call(ctx, native.MethodThreadResume, native.ThreadResumeParams{ThreadID: threadID}, &response); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("resume Codex thread: %w", err)
		}
		if response.Thread.ID == "" || response.Thread.ID != threadID {
			_ = client.Close()
			return nil, fmt.Errorf("%w: thread/resume returned unexpected thread id %q", ErrNativeProtocol, response.Thread.ID)
		}
	}
	sessionID := request.SessionID
	if sessionID == "" {
		sessionID = protocol.SessionID(implementation.ids.NewID("session"))
	}
	now := implementation.clock.Now().UnixMilli()
	session := &session{
		client: client, clock: implementation.clock, ids: implementation.ids,
		capacity: implementation.config.JournalCapacity, participant: request.Participant.ID,
		threadID: threadID, model: implementation.config.Model,
		state: protocol.SessionState{SessionID: sessionID, Status: protocol.SessionIdle, CurrentModelID: implementation.config.Model, UpdatedAtMS: now},
		runs:  make(map[protocol.RunID]*runState), turns: make(map[string]protocol.RunID),
		items: make(map[string]itemBinding), interactions: make(map[protocol.InteractionID]*interactionBinding), stop: make(chan struct{}),
	}
	go session.dispatch()
	return session, nil
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type sequenceIDs struct{ next atomic.Uint64 }

func (generator *sequenceIDs) NewID(kind string) string {
	return fmt.Sprintf("%s-%d", kind, generator.next.Add(1))
}

var _ adapter.Adapter = (*Adapter)(nil)

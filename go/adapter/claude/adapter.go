package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/claude/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/claude/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const (
	endpointID             = "claude-code.cli"
	PinnedVersion          = native.ReleaseTag
	CapabilityRevision     = "claude-code-2.1.263-oap-v3"
	defaultJournalCapacity = 256
	initializeTimeout      = 60 * time.Second
)

func endpointSources() []protocol.ToolSourceDescriptor {
	return []protocol.ToolSourceDescriptor{
		{ID: nativeToolSource, Kind: protocol.ToolSourceNative, DisplayName: "Claude Code built-in tools"},
	}
}

var ErrNativeProtocol = errors.New("claude adapter: invalid native protocol observation")

type Client interface {
	Call(ctx context.Context, request any, result any) error
	WriteUser(ctx context.Context, frame json.RawMessage) error
	Inbound() <-chan rpc.InboundMessage
	Done() <-chan struct{}
	Err() error
	Close() error
}

type ClientFactory interface {
	Start(context.Context) (Client, error)
}
type ClientFactoryFunc func(context.Context) (Client, error)

func (f ClientFactoryFunc) Start(ctx context.Context) (Client, error) { return f(ctx) }

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
	Factory          ClientFactory
	ProcessFactory   ProcessFactory
	Executable       string
	Args             []string
	Environment      []string
	WorkingDirectory string
	Model            string
	Clock            base.Clock
	IDs              base.IDGenerator
	JournalCapacity  int
	FrameLimit       int
	QueueCapacity    int
	ExitTimeout      time.Duration
}

type Adapter struct {
	config Config
	clock  base.Clock
	ids    base.IDGenerator

	initializeAtOpen bool
}

func New(config Config) (*Adapter, error) {
	if config.Factory == nil && config.ProcessFactory == nil && config.Executable == "" {
		return nil, errors.New("claude adapter: factory or executable is required")
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
	initializeAtOpen := false
	if config.Factory == nil {

		env := config.Environment
		if env == nil {
			env = []string{}
		}
		argv := []string{
			"--output-format", "stream-json",
			"--verbose",
			"--input-format", "stream-json",
			"--system-prompt", "",
			"--include-partial-messages",
			"--permission-prompt-tool", "stdio",
			"--setting-sources=",
		}
		if config.Model != "" {
			argv = append(argv, "--model", config.Model)
		}
		argv = append(argv, config.Args...)
		pc := rpc.ProcessConfig{Path: config.Executable, Args: argv, Dir: config.WorkingDirectory, Env: env, FrameLimit: config.FrameLimit, QueueCapacity: config.QueueCapacity, ExitTimeout: config.ExitTimeout}
		initializeAtOpen = true
		config.Factory = ClientFactoryFunc(func(ctx context.Context) (Client, error) {
			p, err := config.ProcessFactory.Start(ctx, pc)
			if err != nil {
				return nil, err
			}
			return &sessionClient{Client: p.ClientHandle(), bridge: p}, nil
		})
	}
	return &Adapter{config: config, clock: config.Clock, ids: config.IDs, initializeAtOpen: initializeAtOpen}, nil
}

type rpcProcess struct{ *rpc.Process }

func (p *rpcProcess) ClientHandle() Client { return p.Client }

type sessionClient struct {
	Client
	bridge ProcessBridge
}

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
		"protocol.initialize":            {Level: protocol.SupportEmulated, Reason: "initialize control exchange at open; no capability negotiation"},
		"capabilities":                   {Level: protocol.SupportEmulated, Reason: "conservative descriptor; per-turn system/init refresh recorded as evidence"},
		"session.open":                   {Level: protocol.SupportEmulated, Reason: "process spawn + initialize; CLI session UUID observed on frames"},
		"session.state":                  {Level: protocol.SupportDegraded, Reason: "reducer-owned live projection"},
		"session.message.submit":         {Level: protocol.SupportDegraded, Reason: "host-minted turn uuid correlated by the user_message_uuid echo"},
		"session.message.delivery.auto":  {Level: protocol.SupportDegraded, Reason: "accepted only when the CLI session is idle"},
		"session.message.delivery.queue": {Level: protocol.SupportUnavailable, Reason: "queued continuation turns are not exposed in v1"},
		"session.message.delivery.steer": {Level: protocol.SupportUnavailable, Reason: "shouldQuery/priority are unexercised"},
		"run.streaming":                  {Level: protocol.SupportNative, Reason: "stream_event deltas with --include-partial-messages always on"},
		"run.status":                     {Level: protocol.SupportEmulated},
		"run.cancel":                     {Level: protocol.SupportDegraded, Reason: "interrupt intent; settlement only via terminal_reason aborted_*"},
		"run.resume":                     {Level: protocol.SupportUnavailable, Reason: "conversation-level resume inputs are not OAP run replay"},
		"run.replay":                     {Level: protocol.SupportUnavailable, Reason: "transcript persistence is not event replay"},
		"run.reconciliation":             {Level: protocol.SupportDegraded, Reason: "system/init and session state frames corroborate"},
		"action.tools":                   {Level: protocol.SupportDegraded, Reason: "tool_use/tool_result projection; started synthesized; tool_progress observed-only"},
		"action.tools.execute":           {Level: protocol.SupportUnavailable, Reason: "the CLI executes tools internally"},

		protocol.FeatureToolsList: {Level: protocol.SupportDegraded, Reason: "system/init republishes the tool and MCP server lists per turn; there is none before the first"},
		"action.permissions":      {Level: protocol.SupportNative, Reason: "can_use_tool reverse control requests"},
		"user_input":              {Level: protocol.SupportNative, Reason: "permission gates over the control plane"},
	}
	return base.Descriptor{Capabilities: protocol.CapabilityDescriptor{Endpoint: protocol.EndpointDescriptor{ID: endpointID, Name: "Claude Code Adapter", Version: PinnedVersion, Adapter: "claude-code-stream-json"}, ProtocolVersions: []string{protocol.Version}, Profiles: []string{protocol.Profile}, Features: features, Sources: endpointSources()}, CapabilityRevision: CapabilityRevision, Journal: base.JournalDescriptor{Scope: "session", Persistence: "process_memory", Replay: protocol.SupportUnavailable, Capacity: a.config.JournalCapacity}, MaxActiveRunsPerSession: 1, InteractiveGates: true, CancellationTarget: "session", CancellationImplementation: "interrupt control request"}, nil
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
	id := req.SessionID
	if id == "" {
		id = protocol.SessionID(a.ids.NewID("session"))
	}
	now := a.clock.Now().UnixMilli()
	s := &Session{client: client, clock: a.clock, ids: a.ids, capacity: a.config.JournalCapacity, participant: participant(req.Participant), state: protocol.SessionState{SessionID: id, Status: protocol.SessionIdle, CurrentModelID: a.config.Model, UpdatedAtMS: now}, runs: map[protocol.RunID]*runState{}, tools: map[string]*toolState{}, interactions: map[protocol.InteractionID]*gateState{}, children: map[string]*childState{}, stop: make(chan struct{})}
	go s.dispatch()
	if a.initializeAtOpen {

		initCtx, cancel := context.WithTimeout(ctx, initializeTimeout)
		defer cancel()
		if err := client.Call(initCtx, native.InitializeRequest{Subtype: native.ControlInitialize, Hooks: nil}, &struct{}{}); err != nil {
			_ = s.Close(context.Background())
			return nil, fmt.Errorf("claude adapter: initialize exchange failed: %w", err)
		}
	}
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

func (g *sequenceIDs) NewID(kind string) string {
	return fmt.Sprintf("%s-%d", g.kindPrefix(kind), g.next.Add(1))
}

func (g *sequenceIDs) kindPrefix(kind string) string {
	safe := make([]byte, 0, len(kind))
	for _, char := range kind {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', char == '-':
			safe = append(safe, byte(char))
		default:
			safe = append(safe, '-')
		}
	}
	return string(safe)
}

var _ base.Adapter = (*Adapter)(nil)

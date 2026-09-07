// Package acp adapts the stable Agent Client Protocol v1 client surface to OAP.
package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/acp/internal/native"
	"github.com/lsm/open-agent-protocol/adapter/acp/internal/rpc"
	"github.com/lsm/open-agent-protocol/protocol"
)

const (
	ACPVersion             = 1
	SchemaVersion          = "1.21.0"
	CapabilityRevision     = "acp-v1.7.0-schema-v1.21.0-oap-v1"
	defaultJournalCapacity = 256
)

var (
	ErrNativeProtocol   = errors.New("acp adapter: invalid native protocol observation")
	ErrUnsupportedInput = errors.New("acp adapter: unsupported input")
)

type Client interface {
	Call(context.Context, string, any, any) error
	CallStarted(context.Context, string, any, any, chan<- error) error
	Notify(context.Context, string, any) error
	Requests() <-chan *rpc.IncomingRequest
	Notifications() <-chan rpc.NotificationMessage
	Inbound() <-chan rpc.InboundMessage
	Done() <-chan struct{}
	Err() error
	Close() error
}
type ClientFactory interface {
	Start(context.Context) (Client, rpc.InitializeResponse, error)
}
type ClientFactoryFunc func(context.Context) (Client, rpc.InitializeResponse, error)

func (f ClientFactoryFunc) Start(ctx context.Context) (Client, rpc.InitializeResponse, error) {
	return f(ctx)
}

type Config struct {
	Factory          ClientFactory
	Executable       string
	Args             []string
	Environment      []string
	WorkingDirectory string
	MCPServers       []native.MCPServer
	Clock            base.Clock
	IDs              base.IDGenerator
	JournalCapacity  int
	FrameLimit       int
	QueueCapacity    int
	ShutdownTimeout  time.Duration
}
type Adapter struct {
	config Config
	clock  base.Clock
	ids    base.IDGenerator
}

func New(config Config) (*Adapter, error) {
	if config.Factory == nil && config.Executable == "" {
		return nil, errors.New("acp adapter: factory or executable is required")
	}
	if config.WorkingDirectory == "" || !filepath.IsAbs(config.WorkingDirectory) {
		return nil, errors.New("acp adapter: absolute working directory is required")
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
	if config.Factory == nil {
		pc := rpc.ProcessConfig{Path: config.Executable, Args: config.Args, Env: config.Environment, Dir: config.WorkingDirectory, FrameLimit: config.FrameLimit, QueueCapacity: config.QueueCapacity, ShutdownTimeout: config.ShutdownTimeout, ProtocolVersion: ACPVersion, ClientCapabilities: rpc.ClientCapabilities{}, ClientInfo: &rpc.Implementation{Name: "open-agent-protocol", Version: protocol.Version}}
		config.Factory = ClientFactoryFunc(func(ctx context.Context) (Client, rpc.InitializeResponse, error) {
			p, err := rpc.Start(ctx, pc)
			if err != nil {
				return nil, rpc.InitializeResponse{}, err
			}
			return &processClient{p}, p.Initialize, nil
		})
	}
	return &Adapter{config: config, clock: config.Clock, ids: config.IDs}, nil
}

type processClient struct{ *rpc.Process }

func (c *processClient) Call(ctx context.Context, m string, p, r any) error {
	return c.Client.Call(ctx, m, p, r)
}
func (c *processClient) CallStarted(ctx context.Context, m string, p, r any, started chan<- error) error {
	return c.Client.CallStarted(ctx, m, p, r, started)
}
func (c *processClient) Notify(ctx context.Context, m string, p any) error {
	return c.Client.Notify(ctx, m, p)
}
func (c *processClient) Requests() <-chan *rpc.IncomingRequest { return c.Client.Requests() }
func (c *processClient) Notifications() <-chan rpc.NotificationMessage {
	return c.Client.Notifications()
}
func (c *processClient) Inbound() <-chan rpc.InboundMessage { return c.Client.Inbound() }
func (c *processClient) Done() <-chan struct{}              { return c.Client.Done() }
func (c *processClient) Err() error                         { return c.Client.Err() }
func (c *processClient) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.Process.Close(ctx)
}

func (a *Adapter) Probe(ctx context.Context) (base.Descriptor, error) {
	if err := ctx.Err(); err != nil {
		return base.Descriptor{}, err
	}
	features := map[string]protocol.FeatureSupport{
		"protocol.initialize": {Level: protocol.SupportEmulated, Reason: "ACP initialize is normalized into the OAP adapter boundary"},
		"capabilities":        {Level: protocol.SupportEmulated, Reason: "effective support is synthesized conservatively from stable ACP v1 and adapter policy"},
		"session.open":        {Level: protocol.SupportNative}, "session.state": {Level: protocol.SupportEmulated, Reason: "adapter-owned projection"},
		"session.message.submit":        {Level: protocol.SupportEmulated, Reason: "admission is synthesized after the prompt request is written"},
		"session.message.delivery.auto": {Level: protocol.SupportEmulated, Reason: "auto is normalized to start"},
		"run.streaming":                 {Level: protocol.SupportNative}, "run.status": {Level: protocol.SupportEmulated},
		"run.cancel":           {Level: protocol.SupportDegraded, Reason: "ACP cancellation is an unacknowledged session notification; prompt settlement is authoritative"},
		"run.resume":           {Level: protocol.SupportDegraded, Reason: "canonical replay is bounded process memory only"},
		"run.reconciliation":   {Level: protocol.SupportEmulated, Reason: "state is adapter-owned"},
		"run.replay":           {Level: protocol.SupportDegraded, Reason: "bounded process-memory journal; gaps are explicit"},
		"action.tools":         {Level: protocol.SupportDegraded, Reason: "observed ACP presentation tool calls only; no catalog"},
		"action.tools.execute": {Level: protocol.SupportDegraded, Reason: "observed tool lifecycle is normalized"},
		"action.permissions":   {Level: protocol.SupportNative, Reason: "ACP permission choice semantics with synthesized portable identity"},
	}
	return base.Descriptor{Capabilities: protocol.CapabilityDescriptor{Endpoint: protocol.EndpointDescriptor{ID: "acp.v1", Name: "ACP v1 Adapter", Version: "1.7.0", Adapter: "acp-v1-stdio"}, ProtocolVersions: []string{protocol.Version}, Profiles: []string{protocol.Profile}, Features: features}, CapabilityRevision: CapabilityRevision, Journal: base.JournalDescriptor{Scope: "session", Persistence: "process_memory", Replay: protocol.SupportDegraded, Capacity: a.config.JournalCapacity}, MaxActiveRunsPerSession: 1, InteractiveGates: true, CancellationTarget: "run", CancellationImplementation: "native_session_notification"}, nil
}

func (a *Adapter) Open(ctx context.Context, req base.OpenRequest) (base.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client, initialized, err := a.config.Factory.Start(ctx)
	if err != nil {
		return nil, err
	}
	if initialized.ProtocolVersion != ACPVersion || initialized.AgentCapabilities == nil {
		_ = client.Close()
		return nil, fmt.Errorf("%w: initialize selected version %d or omitted capabilities", ErrNativeProtocol, initialized.ProtocolVersion)
	}
	var opened native.SessionNewResult
	if err := client.Call(ctx, native.MethodSessionNew, native.SessionNewParams{Cwd: a.config.WorkingDirectory, MCPServers: append([]native.MCPServer(nil), a.config.MCPServers...)}, &opened); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("create ACP session: %w", err)
	}
	if opened.SessionID == "" {
		_ = client.Close()
		return nil, fmt.Errorf("%w: session/new returned no session id", ErrNativeProtocol)
	}
	id := req.SessionID
	if id == "" {
		id = protocol.SessionID(a.ids.NewID("session"))
	}
	now := a.clock.Now().UnixMilli()
	s := &session{client: client, inbound: client.Inbound(), clock: a.clock, ids: a.ids, capacity: a.config.JournalCapacity, nativeID: opened.SessionID, participant: req.Participant.ID, state: protocol.SessionState{SessionID: id, Status: protocol.SessionIdle, UpdatedAtMS: now}, runs: map[protocol.RunID]*runState{}, tools: map[string]*toolState{}, interactions: map[protocol.InteractionID]*permissionState{}, stop: make(chan struct{})}
	go s.dispatch()
	return s, nil
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type sequenceIDs struct{ next atomic.Uint64 }

func (g *sequenceIDs) NewID(kind string) string  { return fmt.Sprintf("%s-%d", kind, g.next.Add(1)) }
func rawClone(v json.RawMessage) json.RawMessage { return append(json.RawMessage(nil), v...) }

var _ base.Adapter = (*Adapter)(nil)

// Package pi adapts Pi coding agent v0.85.1 RPC mode to OAP.
package pi

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/pi/internal/native"
	"github.com/lsm/open-agent-protocol/adapter/pi/internal/rpc"
	"github.com/lsm/open-agent-protocol/protocol"
)

const (
	PinnedVersion          = native.Version
	PinnedCommit           = native.Commit
	CapabilityRevision     = "pi-v0.85.1-oap-v1"
	defaultJournalCapacity = 256
)

var (
	ErrNativeProtocol   = errors.New("pi adapter: invalid native protocol observation")
	ErrUnsupportedInput = errors.New("pi adapter: unsupported input")
)

// Client is the ordered Pi RPC surface consumed by a Session.
type Client interface {
	Call(context.Context, native.Command, any) error
	Respond(context.Context, native.ExtensionUIResponse) error
	Inbound() <-chan rpc.Inbound
	Done() <-chan struct{}
	Err() error
	Close() error
}

// ClientFactory supplies a ready client and its initial get_state snapshot.
// rpc.Process performs that readiness handshake for the built-in factory. Custom
// factories must disable Pi extensions: every admitted prompt must cross the
// model-producing agent_start boundary rather than be consumed by an extension
// command or input hook.
type ClientFactory interface {
	Start(context.Context) (Client, native.SessionState, error)
}
type ClientFactoryFunc func(context.Context) (Client, native.SessionState, error)

func (f ClientFactoryFunc) Start(ctx context.Context) (Client, native.SessionState, error) {
	return f(ctx)
}

// ProcessBridge allows process ownership to be injected independently of the
// RPC client. Done must close when the child exits; Close owns child teardown.
type ProcessBridge interface {
	ClientHandle() Client
	InitialSessionState() native.SessionState
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
	Clock              base.Clock
	IDs                base.IDGenerator
	JournalCapacity    int
	FrameLimit         int
	QueueCapacity      int
	WriteQueueCapacity int
	ShutdownTimeout    time.Duration
}

// Adapter maps Pi process sessions into canonical OAP sessions.
type Adapter struct {
	config Config
	clock  base.Clock
	ids    base.IDGenerator
}

func New(config Config) (*Adapter, error) {
	if config.Factory == nil {
		for _, arg := range config.Args {
			if arg == "--" {
				return nil, errors.New("pi adapter: standalone -- prevents enforced extension disabling")
			}
			if arg == "--extension" || arg == "-e" || strings.HasPrefix(arg, "--extension=") {
				return nil, errors.New("pi adapter: explicit extensions are incompatible with canonical prompt admission")
			}
		}
	}
	if config.Factory == nil && config.ProcessFactory == nil && config.Executable == "" {
		return nil, errors.New("pi adapter: factory or executable is required")
	}
	if config.Executable != "" && (config.WorkingDirectory == "" || !filepath.IsAbs(config.WorkingDirectory)) {
		return nil, errors.New("pi adapter: absolute working directory is required")
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
			return &rpcProcess{Process: p}, nil
		})
	}
	if config.Factory == nil {
		// Pi's parser treats repeated --no-extensions flags idempotently. Force it
		// after caller arguments so discovered extensions cannot consume a prompt
		// without producing agent_start.
		args := append(append([]string(nil), config.Args...), "--no-extensions")
		// slices.Clone preserves non-nilness: an explicitly empty allowlist must
		// reach rpc.Start as non-nil or it collapses to "inherit the parent".
		pc := rpc.ProcessConfig{Path: config.Executable, Args: args, Dir: config.WorkingDirectory, Env: slices.Clone(config.Environment), FrameLimit: config.FrameLimit, QueueCapacity: config.QueueCapacity, WriteQueueCapacity: config.WriteQueueCapacity, ShutdownTimeout: config.ShutdownTimeout}
		config.Factory = ClientFactoryFunc(func(ctx context.Context) (Client, native.SessionState, error) {
			bridge, err := config.ProcessFactory.Start(ctx, pc)
			if err != nil {
				return nil, native.SessionState{}, err
			}
			return &processClient{Client: bridge.ClientHandle(), bridge: bridge}, bridge.InitialSessionState(), nil
		})
	}
	return &Adapter{config: config, clock: config.Clock, ids: config.IDs}, nil
}

type rpcProcess struct{ *rpc.Process }

func (p *rpcProcess) ClientHandle() Client                     { return p.Client }
func (p *rpcProcess) InitialSessionState() native.SessionState { return p.InitialState }

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
		"protocol.initialize":            {Level: protocol.SupportEmulated, Reason: "Pi has no negotiation; readiness is a get_state handshake"},
		"capabilities":                   {Level: protocol.SupportEmulated, Reason: "conservative descriptor synthesized for the pinned RPC vocabulary"},
		"session.open":                   {Level: protocol.SupportEmulated, Reason: "one ready Pi process is associated with one OAP session"},
		"session.state":                  {Level: protocol.SupportEmulated, Reason: "adapter projection reconciled with get_state"},
		"session.message.submit":         {Level: protocol.SupportEmulated, Reason: "successful prompt response proves admission only"},
		"session.message.delivery.auto":  {Level: protocol.SupportEmulated, Reason: "idle auto is normalized to native prompt/start"},
		"session.message.delivery.queue": {Level: protocol.SupportUnavailable, Reason: "v0.1 admission cannot expose Pi queued prompt semantics safely"},
		"session.message.delivery.steer": {Level: protocol.SupportUnavailable, Reason: "v0.1 admission cannot expose Pi steering semantics safely"},
		"run.streaming":                  {Level: protocol.SupportNative}, "run.status": {Level: protocol.SupportEmulated},
		"run.cancel":           {Level: protocol.SupportDegraded, Reason: "abort intent is local; agent_settled remains terminal authority"},
		"run.resume":           {Level: protocol.SupportDegraded, Reason: "bounded process-memory replay"},
		"run.reconciliation":   {Level: protocol.SupportEmulated, Reason: "get_state reconciles streaming state"},
		"run.replay":           {Level: protocol.SupportDegraded, Reason: "bounded adapter journal; gaps explicit"},
		"action.tools":         {Level: protocol.SupportDegraded, Reason: "observed tool lifecycle only; no portable catalog"},
		"action.tools.execute": {Level: protocol.SupportUnavailable, Reason: "Pi executes tools internally"},
		"action.permissions":   {Level: protocol.SupportUnavailable, Reason: "extension dialogs are generic user input, not permissions"},
	}
	return base.Descriptor{Capabilities: protocol.CapabilityDescriptor{Endpoint: protocol.EndpointDescriptor{ID: "pi.rpc", Name: "Pi RPC Adapter", Version: PinnedVersion, Adapter: "pi-rpc-stdio"}, ProtocolVersions: []string{protocol.Version}, Profiles: []string{protocol.Profile}, Features: features}, CapabilityRevision: CapabilityRevision, Journal: base.JournalDescriptor{Scope: "session", Persistence: "process_memory", Replay: protocol.SupportDegraded, Capacity: a.config.JournalCapacity}, MaxActiveRunsPerSession: 1, InteractiveGates: false, CancellationTarget: "run", CancellationImplementation: "native_abort_with_agent_settled_authority"}, nil
}

func (a *Adapter) Open(ctx context.Context, req base.OpenRequest) (base.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Extension dialogs are emitted with the participant as the responder, so an
	// empty identity would produce schema-invalid events that no valid resolution
	// could satisfy. Reject before starting a process.
	if req.Participant.ID == "" {
		return nil, base.ErrInvalidParticipant
	}
	client, initial, err := a.config.Factory.Start(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateState(initial); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("%w: initial state: %v", ErrNativeProtocol, err)
	}
	if initial.IsStreaming {
		_ = client.Close()
		return nil, fmt.Errorf("%w: initial state is already streaming", ErrNativeProtocol)
	}
	id := req.SessionID
	if id == "" {
		id = protocol.SessionID(a.ids.NewID("session"))
	}
	now := a.clock.Now().UnixMilli()
	s := &Session{client: client, inbound: client.Inbound(), clock: a.clock, ids: a.ids, capacity: a.config.JournalCapacity, nativeID: initial.SessionID, participant: req.Participant.ID, state: protocol.SessionState{SessionID: id, Status: protocol.SessionIdle, UpdatedAtMS: now}, nativeState: initial, runs: map[protocol.RunID]*runState{}, tools: map[string]*toolState{}, interactions: map[protocol.InteractionID]*inputState{}, stop: make(chan struct{})}
	if initial.IsStreaming {
		s.state.Status = protocol.SessionRunning
	}
	go s.dispatch()
	return s, nil
}

func validateState(s native.SessionState) error {
	if s.SessionID == "" || s.MessageCount < 0 || s.PendingMessageCount < 0 {
		return errors.New("missing identity or negative counts")
	}
	validMode := func(v native.QueueMode) bool { return v == native.QueueAll || v == native.QueueOneAtATime }
	if !validMode(s.SteeringMode) || !validMode(s.FollowUpMode) {
		return errors.New("invalid queue mode")
	}
	switch s.ThinkingLevel {
	case native.ThinkingOff, native.ThinkingMinimal, native.ThinkingLow, native.ThinkingMedium, native.ThinkingHigh, native.ThinkingXHigh, native.ThinkingMax:
	default:
		return errors.New("invalid thinking level")
	}
	return nil
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type sequenceIDs struct{ next atomic.Uint64 }

func (g *sequenceIDs) NewID(kind string) string { return fmt.Sprintf("%s-%d", kind, g.next.Add(1)) }

var _ base.Adapter = (*Adapter)(nil)

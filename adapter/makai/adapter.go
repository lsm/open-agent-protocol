// Package makai adapts the pinned Makai agent protocol to OAP.
package makai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/makai/internal/native"
	"github.com/lsm/open-agent-protocol/adapter/makai/internal/stdio"
	"github.com/lsm/open-agent-protocol/protocol"
)

const (
	PinnedCommit           = "67ad51420c3f4d7918218573366fde7db8c35b9c"
	CapabilityRevision     = "makai-agent-67ad514-oap-v1"
	defaultJournalCapacity = 256
)

var (
	ErrNativeProtocol   = errors.New("makai adapter: invalid native protocol observation")
	ErrUnsupportedInput = errors.New("makai adapter: unsupported input")
)

type Client interface {
	Call(context.Context, native.Envelope, ...native.Type) (native.Envelope, error)
	Send(context.Context, native.Envelope) error
	Inbound() <-chan stdio.Inbound
	Done() <-chan struct{}
	Err() error
	Close() error
}
type ClientFactory interface {
	Start(context.Context) (Client, error)
}
type ClientFactoryFunc func(context.Context) (Client, error)

func (f ClientFactoryFunc) Start(ctx context.Context) (Client, error) { return f(ctx) }

type Config struct {
	Factory          ClientFactory
	Executable       string
	Args             []string
	Environment      []string
	WorkingDirectory string
	AgentConfig      json.RawMessage
	SystemPrompt     string
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
		return nil, errors.New("makai adapter: factory or executable is required")
	}
	if config.WorkingDirectory == "" || !filepath.IsAbs(config.WorkingDirectory) {
		return nil, errors.New("makai adapter: absolute working directory is required")
	}
	if len(config.AgentConfig) == 0 || !json.Valid(config.AgentConfig) {
		return nil, errors.New("makai adapter: valid agent config JSON is required")
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
		processConfig := stdio.ProcessConfig{Path: config.Executable, Args: config.Args, Env: config.Environment, Dir: config.WorkingDirectory, FrameLimit: config.FrameLimit, QueueCapacity: config.QueueCapacity, ShutdownTimeout: config.ShutdownTimeout}
		config.Factory = ClientFactoryFunc(func(ctx context.Context) (Client, error) {
			process, err := stdio.Start(ctx, processConfig)
			if err != nil {
				return nil, err
			}
			return &processClient{Process: process}, nil
		})
	}
	return &Adapter{config: config, clock: config.Clock, ids: config.IDs}, nil
}

type processClient struct{ Process *stdio.Process }

func (c *processClient) Call(ctx context.Context, env native.Envelope, accepted ...native.Type) (native.Envelope, error) {
	return c.Process.Client.Call(ctx, env, accepted...)
}
func (c *processClient) Send(ctx context.Context, env native.Envelope) error {
	return c.Process.Client.Send(ctx, env)
}
func (c *processClient) Inbound() <-chan stdio.Inbound { return c.Process.Client.Inbound() }
func (c *processClient) Done() <-chan struct{}         { return c.Process.Client.Done() }
func (c *processClient) Err() error                    { return c.Process.Client.Err() }
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
		"protocol.initialize":           {Level: protocol.SupportEmulated, Reason: "Makai has a ready frame but no capability negotiation"},
		"capabilities":                  {Level: protocol.SupportEmulated, Reason: "effective support is synthesized conservatively from the pinned agent protocol"},
		"session.open":                  {Level: protocol.SupportNative, Reason: "agent_start association normalized to OAP"},
		"session.state":                 {Level: protocol.SupportEmulated, Reason: "adapter-owned projection"},
		"session.message.submit":        {Level: protocol.SupportEmulated, Reason: "admission is synthesized after the complete agent_message frame is written"},
		"session.message.delivery.auto": {Level: protocol.SupportEmulated, Reason: "auto is normalized to start"},
		"run.streaming":                 {Level: protocol.SupportNative, Reason: "pinned agent_event text stream normalized to OAP"},
		"run.status":                    {Level: protocol.SupportEmulated},
		"run.cancel":                    {Level: protocol.SupportDegraded, Reason: "agent_stop destroys the session and is not run-targeted"},
		"run.resume":                    {Level: protocol.SupportDegraded, Reason: "canonical replay is bounded process memory only"},
		"run.reconciliation":            {Level: protocol.SupportEmulated, Reason: "state is adapter-owned"},
		"run.replay":                    {Level: protocol.SupportDegraded, Reason: "bounded process-memory journal; gaps are explicit"},
		"action.tools":                  {Level: protocol.SupportDegraded, Reason: "observed native tool lifecycle; no portable authoritative catalog"},
		"action.tools.execute":          {Level: protocol.SupportUnavailable, Reason: "client-hosted tool execution requires an explicit executor boundary not yet exposed by this adapter"},
		"action.permissions":            {Level: protocol.SupportUnavailable, Reason: "Makai agent protocol exposes no permission interaction"},
	}
	return base.Descriptor{Capabilities: protocol.CapabilityDescriptor{Endpoint: protocol.EndpointDescriptor{ID: "makai.agent", Name: "Makai Agent Adapter", Version: PinnedCommit[:7], Adapter: "makai-agent-stdio"}, ProtocolVersions: []string{protocol.Version}, Profiles: []string{protocol.Profile}, Features: features}, CapabilityRevision: CapabilityRevision, Journal: base.JournalDescriptor{Scope: "session", Persistence: "process_memory", Replay: protocol.SupportDegraded, Capacity: a.config.JournalCapacity}, MaxActiveRunsPerSession: 1, InteractiveGates: false, CancellationTarget: "session", CancellationImplementation: "native_session_teardown"}, nil
}

func (a *Adapter) Open(ctx context.Context, req base.OpenRequest) (base.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client, err := a.config.Factory.Start(ctx)
	if err != nil {
		return nil, err
	}
	nativeAssociation := native.SessionID(a.ids.NewID("makai-session"))
	if !nativeAssociation.Valid() {
		_ = client.Close()
		return nil, errors.New("makai adapter: ID generator must produce a 21-character native session ID for kind makai-session")
	}
	request, err := a.envelope(native.TypeAgentStart, nativeAssociation, 1, native.AgentStart{ConfigJSON: string(a.config.AgentConfig), SystemPrompt: a.config.SystemPrompt})
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	type startResult struct {
		envelope native.Envelope
		err      error
	}
	startDone := make(chan startResult, 1)
	go func() {
		response, callErr := client.Call(ctx, request, native.TypeAgentStarted, native.TypeAgentError)
		startDone <- startResult{envelope: response, err: callErr}
	}()
	var response native.Envelope
	for response.Type == "" {
		select {
		case result := <-startDone:
			response, err = result.envelope, result.err
		case inbound := <-client.Inbound():
			if inbound.Barrier != nil {
				close(inbound.Barrier)
				continue
			}
			err = fmt.Errorf("%w: observation preceded agent_started", ErrNativeProtocol)
		case <-ctx.Done():
			err = ctx.Err()
		case <-client.Done():
			err = client.Err()
		}
		if err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("start Makai agent session: %w", err)
		}
	}
	if response.Type == native.TypeAgentError {
		payload, _ := native.DecodePayload[native.AgentError](response)
		_ = client.Close()
		return nil, fmt.Errorf("%w: agent_start failed: %s: %s", ErrNativeProtocol, payload.Code, payload.Message)
	}
	started, err := native.DecodePayload[native.AgentStarted](response)
	if err != nil || !started.SessionID.Valid() {
		_ = client.Close()
		return nil, fmt.Errorf("%w: invalid agent_started response", ErrNativeProtocol)
	}
	id := req.SessionID
	if id == "" {
		id = protocol.SessionID(a.ids.NewID("session"))
	}
	now := a.clock.Now().UnixMilli()
	s := &session{client: client, inbound: client.Inbound(), clock: a.clock, ids: a.ids, capacity: a.config.JournalCapacity, nativeID: started.SessionID, participant: req.Participant.ID, state: protocol.SessionState{SessionID: id, Status: protocol.SessionIdle, UpdatedAtMS: now}, runs: map[protocol.RunID]*runState{}, tools: map[string]*toolState{}, stop: make(chan struct{}), nativeSequence: 2}
	go s.dispatch()
	return s, nil
}

func (a *Adapter) envelope(typ native.Type, sessionID native.SessionID, sequence uint64, payload any) (native.Envelope, error) {
	id := native.MessageID(a.ids.NewID("makai-frame"))
	if !id.Valid() {
		return native.Envelope{}, errors.New("makai adapter: ID generator must produce a ULID for kind makai-frame")
	}
	return native.NewEnvelope(typ, sessionID, id, sequence, a.clock.Now().UnixMilli(), payload)
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type sequenceIDs struct{ next atomic.Uint64 }

func (g *sequenceIDs) NewID(kind string) string {
	n := g.next.Add(1)
	switch kind {
	case "makai-session":
		return fmt.Sprintf("%021d", n)
	case "makai-frame":
		return fmt.Sprintf("000000000000000000000%05d", n%100000)
	default:
		return fmt.Sprintf("%s-%d", kind, n)
	}
}

var _ base.Adapter = (*Adapter)(nil)

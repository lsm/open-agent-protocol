// Package acp adapts the stable Agent Client Protocol v1 client surface to OAP.
package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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
	CapabilityRevision     = "acp-v1.7.0-schema-v1.21.0-oap-v3"
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
	// A configured server's name is now a published source id, so it is held to
	// what a source id must be: present, and one per source. An empty one would
	// put a descriptor with an empty required `id` on the wire, and a duplicate
	// would make the descriptor's own catalog ambiguous — the same two defects
	// an attachment is refused for, at the other place a name enters.
	seen := make(map[string]bool, len(config.MCPServers))
	for _, configured := range config.MCPServers {
		if configured.Name == "" {
			return nil, errors.New("acp adapter: a configured MCP server needs a name")
		}
		if seen[configured.Name] {
			return nil, fmt.Errorf("acp adapter: MCP server %q is configured twice", configured.Name)
		}
		seen[configured.Name] = true
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

// attachSupport is this adapter's one disclosure for
// action.tool_sources.attach. session/new takes the MCP server array natively,
// so attachment at open is what ACP already does; the transports it accepts
// are disclosed because stdio descriptors are the pinned surface and HTTP/SSE
// MCP is explicitly deferred at this pin. Probe publishes it and
// attachToolSources gates on it, so the descriptor a caller reads and the
// admission its open meets cannot drift apart.
var attachSupport = protocol.FeatureSupport{
	Level: protocol.SupportNative, Modes: []string{protocol.ModeSessionOpen},
	Limits: map[string]json.RawMessage{protocol.LimitTransports: json.RawMessage(`["process"]`)},
	Reason: "session/new carries the MCP server array; stdio descriptors only at this pin",
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
		"run.cancel":                      {Level: protocol.SupportDegraded, Reason: "ACP cancellation is an unacknowledged session notification; prompt settlement is authoritative"},
		"run.resume":                      {Level: protocol.SupportDegraded, Reason: "canonical replay is bounded process memory only"},
		"run.reconciliation":              {Level: protocol.SupportEmulated, Reason: "state is adapter-owned"},
		"run.replay":                      {Level: protocol.SupportDegraded, Reason: "bounded process-memory journal; gaps are explicit"},
		"action.tools":                    {Level: protocol.SupportDegraded, Reason: "observed ACP presentation tool calls only; no catalog"},
		protocol.FeatureToolSourcesAttach: attachSupport,
		"action.tools.execute":            {Level: protocol.SupportDegraded, Reason: "observed tool lifecycle is normalized"},
		"action.permissions":              {Level: protocol.SupportNative, Reason: "ACP permission choice semantics with synthesized portable identity"},
	}
	return base.Descriptor{Capabilities: protocol.CapabilityDescriptor{Endpoint: protocol.EndpointDescriptor{ID: "acp.v1", Name: "ACP v1 Adapter", Version: "1.7.0", Adapter: "acp-v1-stdio"}, ProtocolVersions: []string{protocol.Version}, Profiles: []string{protocol.Profile}, Features: features, Sources: a.configuredSources()}, CapabilityRevision: CapabilityRevision, Journal: base.JournalDescriptor{Scope: "session", Persistence: "process_memory", Replay: protocol.SupportDegraded, Capacity: a.config.JournalCapacity}, MaxActiveRunsPerSession: 1, InteractiveGates: true, CancellationTarget: "run", CancellationImplementation: "native_session_notification"}, nil
}

func (a *Adapter) Open(ctx context.Context, req base.OpenRequest) (base.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The participant is the recorded responder for every permission gate, so an
	// empty identity would emit schema-invalid events and admit no valid
	// resolution. Reject before starting a process.
	if req.Participant.ID == "" {
		return nil, base.ErrInvalidParticipant
	}
	// Attachment is admitted before the child is started: the decision depends
	// only on the request and this adapter's own configuration, so an open
	// that cannot be honoured should not pay a process spawn and an initialize
	// round trip, nor leave the side effects of one behind. Same placement,
	// and the same reason, as the shared unadvertised-attachment gate.
	attached, err := a.attachToolSources(req)
	if err != nil {
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
	// ACP v1 types mcpServers as a required array, not a nullable field. A nil
	// slice would marshal as null and a conforming agent rejects the request
	// with -32602 Invalid params, so always send the empty array.
	mcpServers := append([]native.MCPServer{}, a.config.MCPServers...)
	mcpServers = append(mcpServers, attached...)
	var opened native.SessionNewResult
	if err := client.Call(ctx, native.MethodSessionNew, native.SessionNewParams{Cwd: a.config.WorkingDirectory, MCPServers: mcpServers}, &opened); err != nil {
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
	s := &session{client: client, inbound: client.Inbound(), clock: a.clock, ids: a.ids, capacity: a.config.JournalCapacity, nativeID: opened.SessionID, participant: req.Participant.ID, state: protocol.SessionState{SessionID: id, Status: protocol.SessionIdle, UpdatedAtMS: now, Sources: a.sessionSources(req.ToolSources)}, runs: map[protocol.RunID]*runState{}, tools: map[string]*toolState{}, interactions: map[protocol.InteractionID]*permissionState{}, stop: make(chan struct{})}
	go s.dispatch()
	return s, nil
}

// attachToolSources turns the open's attachments into ACP's own MCP server
// array. The wire's `process` kind is ACP's stdio descriptor; every other kind
// is refused under the transports the descriptor discloses, because HTTP/SSE
// MCP is not part of this pin.
//
// An `environment` entry resolves only against the operator's own allowlist:
// a bare NAME takes the value the adapter's configured environment carries and
// is dropped when it carries none, so a wire caller cannot read an ambient
// credential the operator never exposed, and a NAME=value literal passes
// through as ACP's own env pair.
func (a *Adapter) attachToolSources(request base.OpenRequest) ([]native.MCPServer, error) {
	// The same gate every other adapter runs, with the key this one
	// advertises: uniform so one grep finds every endpoint's admission, and a
	// no-op here only because the advertisement is real.
	if err := base.RefuseUnadvertisedToolSources(request, attachSupport); err != nil {
		return nil, err
	}
	// The same gate for control-layer-provided tools: this adapter advertises
	// no action.tools.provide, so an open supplying its own tool definitions
	// is refused rather than returning a session whose provided catalog was
	// silently discarded.
	if err := base.RefuseUnadvertisedTools(request); err != nil {
		return nil, err
	}
	attachments := request.ToolSources
	if len(attachments) == 0 {
		return nil, nil
	}
	allowlist := make(map[string]string, len(a.config.Environment))
	for _, entry := range a.config.Environment {
		if name, value, ok := strings.Cut(entry, "="); ok {
			allowlist[name] = value
		}
	}
	// One id resolves to one source, so a collision — with another attachment
	// or with a server the operator already configured — is refused rather
	// than appended. ACP names its MCP servers by that id, so two identically
	// named entries would make both the catalog's attribution and the native
	// routing ambiguous, and the schema does not enforce uniqueness.
	//
	// The configured half of this set is declared in the descriptor and in the
	// session's own sources, so the collision is one a caller can see before it
	// sends: a refusal for a name nothing published would be a refusal no
	// disclosure covers, which is the defect this unit's own validator reports
	// as undisclosed_attach_limit.
	seen := make(map[string]bool, len(a.config.MCPServers)+len(attachments))
	for _, configured := range a.config.MCPServers {
		seen[configured.Name] = true
	}
	servers := make([]native.MCPServer, 0, len(attachments))
	for _, attachment := range attachments {
		// The id is read before anything is done with it, because everything
		// after this is done by it: it is the collision key below, the name ACP
		// routes the MCP server by, and the id the session publishes the source
		// under. An empty one passes the collision check on its first use, names
		// a server nothing can address, and reaches a client as a descriptor
		// whose required `id` is empty — a schema rejection produced by the
		// adapter itself. The reference adapter refuses it in the same place, so
		// an embedder meets one rule on both.
		if attachment.ID == "" {
			return nil, &base.UnsupportedControlError{
				Feature: protocol.FeatureToolSourcesAttach, Reason: base.ControlUnsatisfiable,
				Detail: "an attachment needs an id",
			}
		}
		if seen[attachment.ID] {
			return nil, &base.UnsupportedControlError{
				Feature: protocol.FeatureToolSourcesAttach, Reason: base.ControlUnsatisfiable,
				Source: attachment.ID, Detail: "the id already names a configured or attached MCP server",
			}
		}
		seen[attachment.ID] = true
		if attachment.Kind != protocol.ToolSourceProcess {
			return nil, &base.UnsupportedControlError{
				Feature: protocol.FeatureToolSourcesAttach, Reason: base.ControlUnsatisfiable,
				Source: attachment.ID, Detail: "ACP v1 accepts stdio MCP descriptors only at this pin",
			}
		}
		if name := base.DuplicateEnvironmentName(attachment.Environment); name != "" {
			// The daemon's registry refuses this on the operator's own entries,
			// and the route drops a caller entry colliding with one — but this
			// API is reachable without either, and an embedder handing over two
			// entries for one variable would have both forwarded to the child
			// with no defined winner. Judged where the attachment is admitted, so
			// every path into this adapter meets it.
			return nil, &base.UnsupportedControlError{
				Feature: protocol.FeatureToolSourcesAttach, Reason: base.ControlUnsatisfiable,
				Source: attachment.ID, Detail: "environment names " + name + " twice",
			}
		}
		if attachment.Command == "" {
			// Not a capability refusal: over the daemon the command comes
			// from the operator's registry and is never empty, so a
			// commandless attachment is an embedder's invalid argument.
			return nil, fmt.Errorf("%w: tool source %q declares a process source with no command", base.ErrInvalidResolution, attachment.ID)
		}
		server := native.MCPServer{Name: attachment.ID, Command: attachment.Command, Args: attachment.Args}
		for _, entry := range attachment.Environment {
			name, value, literal := strings.Cut(entry, "=")
			if literal {
				server.Env = append(server.Env, native.EnvVariable{Name: name, Value: value})
				continue
			}
			if resolved, ok := allowlist[name]; ok {
				server.Env = append(server.Env, native.EnvVariable{Name: name, Value: resolved})
			}
		}
		servers = append(servers, server)
	}
	return servers, nil
}

// configuredSources publishes the MCP servers the operator configured this
// adapter with, as descriptors.
//
// They are declared because they are reserved. attachToolSources refuses an
// attachment whose id collides with one of them, and ACP names its servers by
// that id, so the collision is real — but until the descriptor said so, the
// reservation was invisible: a caller sending a `process` attachment within
// every disclosed limit got an unsatisfiable refusal naming a source it had no
// way to know was taken, which the validator reports as
// `undisclosed_attach_limit` because no disclosure covered it. Declaring them
// makes the collision checkable in both directions, which is the shape this
// unit's refusals are held to everywhere else.
//
// This is the opposite call to Claude's, and the two are consistent rather than
// in tension, because the facts are different. Claude's MCP servers are learned
// from a session's own `system/init` frame: they are that session's, known only
// once it exists, so publishing them endpoint-wide would present one caller's
// configuration as everyone's. These are the *adapter's* configuration, fixed
// before any session opens and applying to every one of them. That is precisely
// what an endpoint-level descriptor is for. The test is where the fact comes
// from and when it is known, not whether it happens to be an MCP server.
//
// The projection carries no command, args, or environment — the same rule
// attachedSources keeps — and no endpoint, because ACP's configured server
// carries none and an invented one would put a value on the wire the operator
// never wrote.
func (a *Adapter) configuredSources() []protocol.ToolSourceDescriptor {
	if len(a.config.MCPServers) == 0 {
		return nil
	}
	sources := make([]protocol.ToolSourceDescriptor, 0, len(a.config.MCPServers))
	for _, configured := range a.config.MCPServers {
		sources = append(sources, protocol.ToolSourceDescriptor{
			ID: configured.Name, Kind: protocol.ToolSourceProcess, Protocol: protocol.ToolSourceMCP,
		})
	}
	return sources
}

// sessionSources is the union a session publishes: the adapter's configured
// servers and the open's attachments, in that order. Both are sources the
// session resolves, and the descriptor declares the first of them, so a
// snapshot that carried only the second would contradict the descriptor it was
// opened under — which is exactly what the unit's union rule diagnoses.
func (a *Adapter) sessionSources(attachments []protocol.ToolSourceAttachment) []protocol.ToolSourceDescriptor {
	configured := a.configuredSources()
	attached := attachedSources(attachments)
	if len(configured) == 0 {
		return attached
	}
	return append(configured, attached...)
}

// attachedSources is the sanitized projection the session publishes: the
// attachment's descriptor members and none of the attachment-only ones, so a
// command or an environment value can never reach a client through state.
func attachedSources(attachments []protocol.ToolSourceAttachment) []protocol.ToolSourceDescriptor {
	if len(attachments) == 0 {
		return nil
	}
	sources := make([]protocol.ToolSourceDescriptor, 0, len(attachments))
	for _, attachment := range attachments {
		sources = append(sources, attachment.Descriptor())
	}
	return sources
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type sequenceIDs struct{ next atomic.Uint64 }

func (g *sequenceIDs) NewID(kind string) string  { return fmt.Sprintf("%s-%d", kind, g.next.Add(1)) }
func rawClone(v json.RawMessage) json.RawMessage { return append(json.RawMessage(nil), v...) }

var _ base.Adapter = (*Adapter)(nil)

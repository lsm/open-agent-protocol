// Package makai adapts the pinned Makai agent protocol to OAP.
package makai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/makai/internal/native"
	"github.com/lsm/open-agent-protocol/adapter/makai/internal/stdio"
	"github.com/lsm/open-agent-protocol/protocol"
)

const (
	PinnedCommit           = "9f351fe12448f86b94498b4dfc4f6dfdaf5f1df5"
	CapabilityRevision     = "makai-agent-67ad514-oap-v2"
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

// The provisioning limits this adapter discloses. maxProvidedTools is a bound
// on the native tools array rather than a protocol one; the dialect is the
// one makai's parameters_schema_json carries at this pin. Both are declared
// because a constraint must be advertised to be exercised: refusing an array
// that satisfies every disclosed limit is undisclosed_provide_limit.
const (
	maxProvidedTools = 32
	providedDialect  = "https://json-schema.org/draft/2020-12/schema"
)

var provideSupport = protocol.FeatureSupport{
	Level: protocol.SupportEmulated,
	Limits: map[string]json.RawMessage{
		protocol.LimitMaxTools: json.RawMessage(strconv.Itoa(maxProvidedTools)),
		"schema_dialect":       json.RawMessage(`"` + providedDialect + `"`),
	},
	Reason: "provided definitions are written onto every agent_message and executed through the native tool_execute/tool_result bridge",
}

// admitProvidedTools judges one open's control-owned catalog. Provisioning is
// whole or not at all and is judged before a process starts, so a refused open
// leaves no child behind and no session holding a catalog it silently trimmed.
//
// Makai provisions per message rather than per session, which is a wider
// surface than this unit admits. The adapter narrows it rather than widening
// the protocol: the array is fixed at open and repeated verbatim on every
// agent_message, so the session's provided catalog cannot change under a run.
func admitProvidedTools(req base.OpenRequest) ([]protocol.ToolDefinition, error) {
	if err := base.RefuseUnadvertisedTools(req, provideSupport); err != nil {
		return nil, err
	}
	if len(req.Tools) == 0 {
		return nil, nil
	}
	refuse := func(tool, detail string) error {
		return &base.UnsupportedControlError{Feature: protocol.FeatureToolsProvide, Reason: base.ControlUnsatisfiable, Tool: tool, Detail: detail}
	}
	if len(req.Tools) > maxProvidedTools {
		return nil, refuse(req.Tools[maxProvidedTools].Name, fmt.Sprintf("at most %d tools may be provided", maxProvidedTools))
	}
	seen := map[string]bool{}
	for _, tool := range req.Tools {
		switch {
		case tool.Name == "":
			return nil, refuse("", "a provided tool needs a name")
		case tool.ExecutionOwner != req.Participant.ID:
			return nil, refuse(tool.Name, "execution_owner must be the opening participant")
		case seen[tool.Name]:
			// One name resolves to one definition. Makai routes tool_execute
			// by name, so a collision would route a call to whichever entry
			// happened to win.
			return nil, refuse(tool.Name, "the name is provided twice")
		case tool.Source != "":
			// This adapter declares no sources and attaches none, so a
			// provided tool naming one is dangling by construction.
			return nil, refuse(tool.Name, "source "+tool.Source+" resolves to no declared or attached source")
		case !admissibleDialect(tool.InputSchema):
			return nil, refuse(tool.Name, "the input schema declares a dialect outside the disclosed "+providedDialect)
		}
		seen[tool.Name] = true
	}
	return append([]protocol.ToolDefinition(nil), req.Tools...), nil
}

// admissibleDialect reports whether a provided schema elects a dialect this
// adapter accepts. An absent $schema elects the endpoint's.
func admissibleDialect(schema json.RawMessage) bool {
	if len(schema) == 0 {
		return true
	}
	var declared struct {
		Schema string `json:"$schema"`
	}
	if err := json.Unmarshal(schema, &declared); err != nil {
		return false
	}
	return declared.Schema == "" || declared.Schema == providedDialect
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
		// agent_message carries model_ref per message, so a requested model is
		// applied to exactly the run it was requested for and the session
		// default is untouched (decision 0005).
		protocol.FeatureModelSelection:   {Level: protocol.SupportNative, Mode: protocol.ModePerRun, Reason: "agent_message.model_ref selects the model for one message"},
		protocol.FeatureInstructions:     {Level: protocol.SupportUnavailable, Reason: "agent_start.system_prompt is session-level; this pin exposes no per-run instructions"},
		protocol.FeatureToolSelection:    {Level: protocol.SupportUnavailable, Reason: "the pinned agent protocol carries no per-run tool policy"},
		protocol.FeatureStructuredOutput: {Level: protocol.SupportUnavailable, Reason: "the pinned agent protocol carries no per-run output schema"},
		"run.streaming":                  {Level: protocol.SupportNative, Reason: "pinned agent_event text stream normalized to OAP"},
		"run.status":                     {Level: protocol.SupportEmulated},
		"run.cancel":                     {Level: protocol.SupportDegraded, Reason: "agent_stop destroys the session and is not run-targeted"},
		"run.resume":                     {Level: protocol.SupportDegraded, Reason: "canonical replay is bounded process memory only"},
		"run.reconciliation":             {Level: protocol.SupportEmulated, Reason: "state is adapter-owned"},
		"run.replay":                     {Level: protocol.SupportDegraded, Reason: "bounded process-memory journal; gaps are explicit"},
		"action.tools":                   {Level: protocol.SupportDegraded, Reason: "observed native tool lifecycle; no portable authoritative catalog"},
		"action.tools.execute":           {Level: protocol.SupportUnavailable, Reason: "the pinned agent protocol has no harness-side executor this adapter can drive"},
		// The tool_execute/tool_result bridge is exactly the control-layer
		// boundary: the harness asks the client to run a tool and waits for
		// the answer. The limits are disclosed because a refusal is only
		// conforming where it violates one — max_tools is the native
		// tools array this adapter writes onto every agent_message, and the
		// dialect is what makai's parameters_schema_json carries.
		protocol.FeatureToolsProvide: provideSupport,
		"action.permissions":         {Level: protocol.SupportUnavailable, Reason: "Makai agent protocol exposes no permission interaction"},
	}
	return base.Descriptor{Capabilities: protocol.CapabilityDescriptor{Endpoint: protocol.EndpointDescriptor{ID: "makai.agent", Name: "Makai Agent Adapter", Version: PinnedCommit[:7], Adapter: "makai-agent-stdio"}, ProtocolVersions: []string{protocol.Version}, Profiles: []string{protocol.Profile}, Features: features}, CapabilityRevision: CapabilityRevision, Journal: base.JournalDescriptor{Scope: "session", Persistence: "process_memory", Replay: protocol.SupportDegraded, Capacity: a.config.JournalCapacity}, MaxActiveRunsPerSession: 1, InteractiveGates: true, CancellationTarget: "session", CancellationImplementation: "native_session_teardown"}, nil
}

func (a *Adapter) Open(ctx context.Context, req base.OpenRequest) (base.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// An open attaching tool sources to an endpoint that never advertised
	// attachment is refused before a process starts: this adapter reads no
	// ToolSources, so admitting the open would return a session that silently
	// discarded them.
	if err := base.RefuseUnadvertisedToolSources(req); err != nil {
		return nil, err
	}
	provided, err := admitProvidedTools(req)
	if err != nil {
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
	// Dual-key emission per the v0.2.0 #198 transition: the canonical
	// session_id key plus the permanent resume_session_id alias, same value —
	// pre-rename servers keep binding the caller's id, dual-key servers take
	// the canonical one.
	request, err := a.envelope(native.TypeAgentStart, nativeAssociation, 1, native.AgentStart{ConfigJSON: string(a.config.AgentConfig), SystemPrompt: a.config.SystemPrompt, SessionID: &nativeAssociation, ResumeSessionID: &nativeAssociation})
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
	if err != nil || !started.SessionID.Valid() || started.SessionID != nativeAssociation || response.SessionID != nativeAssociation {
		_ = client.Close()
		return nil, fmt.Errorf("%w: invalid or foreign agent_started response: requested=%s envelope=%s payload=%s", ErrNativeProtocol, nativeAssociation, response.SessionID, started.SessionID)
	}
	id := req.SessionID
	if id == "" {
		id = protocol.SessionID(a.ids.NewID("session"))
	}
	now := a.clock.Now().UnixMilli()
	s := &session{client: client, inbound: client.Inbound(), clock: a.clock, ids: a.ids, capacity: a.config.JournalCapacity, nativeID: started.SessionID, participant: req.Participant.ID, state: protocol.SessionState{SessionID: id, Status: protocol.SessionIdle, UpdatedAtMS: now}, runs: map[protocol.RunID]*runState{}, tools: map[string]*toolState{}, provided: provided, stop: make(chan struct{}), nativeSequence: 1}
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

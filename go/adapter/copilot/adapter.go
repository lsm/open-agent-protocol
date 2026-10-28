package copilot

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync/atomic"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/copilot/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type Config struct {
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
	RequestTimeout   time.Duration
}

type Adapter struct{ config Config }

var _ base.Adapter = (*Adapter)(nil)

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type sequenceIDs struct{ counter atomic.Uint64 }

func (s *sequenceIDs) NewID(kind string) string { return fmt.Sprintf("%s-%d", kind, s.counter.Add(1)) }

func New(config Config) (*Adapter, error) {
	if config.Executable == "" || !filepath.IsAbs(config.WorkingDirectory) {
		return nil, errors.New("copilot: executable and absolute working directory are required")
	}
	for _, arg := range config.Args {
		if arg == "--" {
			return nil, errors.New("copilot: standalone -- prevents enforced headless options")
		}
	}
	config.Args = append(slices.Clone(config.Args), "--headless", "--stdio", "--no-auto-update")
	config.Environment = slices.Clone(config.Environment)
	if config.Clock == nil {
		config.Clock = systemClock{}
	}
	if config.IDs == nil {
		config.IDs = &sequenceIDs{}
	}
	if config.JournalCapacity <= 0 {
		config.JournalCapacity = 256
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = time.Minute
	}
	return &Adapter{config: config}, nil
}

func (a *Adapter) Probe(ctx context.Context) (base.Descriptor, error) {
	if err := ctx.Err(); err != nil {
		return base.Descriptor{}, err
	}
	features := map[string]protocol.FeatureSupport{
		"protocol.initialize":            {Level: protocol.SupportEmulated, Reason: "the adapter checks the server's protocol version with ping and creates the native session"},
		"capabilities":                   {Level: protocol.SupportEmulated, Reason: "a conservative descriptor for the pinned headless server"},
		"session.open":                   {Level: protocol.SupportNative, Reason: "session.create in one headless server per OAP session"},
		"session.state":                  {Level: protocol.SupportEmulated, Reason: "adapter-owned projection of the runs it admitted"},
		"session.message.submit":         {Level: protocol.SupportEmulated, Reason: "session.send answers a message id at once; the run starts at its own user.message"},
		"session.message.delivery.auto":  {Level: protocol.SupportEmulated, Reason: "start when no run is open, otherwise queue behind it, as session.send enqueues"},
		"session.message.delivery.queue": {Level: protocol.SupportEmulated, Reason: "session.send enqueues behind an open run; an explicit queue on an idle session is admitted as a reservation that Copilot promotes at once"},
		"run.streaming":                  {Level: protocol.SupportNative},
		"run.status":                     {Level: protocol.SupportEmulated},
		"action.tools":                   {Level: protocol.SupportNative, Reason: "tool.execution_start and tool.execution_complete with progress between"},
		"action.tools.execute":           {Level: protocol.SupportUnavailable, Reason: "Copilot executes its own tools"},
	}
	caps := protocol.CapabilityDescriptor{Endpoint: protocol.EndpointDescriptor{ID: "copilot.headless", Name: "GitHub Copilot CLI Adapter", Version: PinnedVersion, Adapter: "copilot-headless-stdio"}, ProtocolVersions: []string{protocol.Version}, Profiles: []string{protocol.Profile}, Features: features, Limits: &protocol.CapabilityLimits{MaxActiveRunsPerSession: protocol.Limit(maxQueued + 1), MaxQueuedRunsPerSession: protocol.Limit(maxQueued)}}
	return base.Descriptor{Capabilities: caps, CapabilityRevision: CapabilityRevision, Journal: base.JournalDescriptor{Scope: "session", Persistence: "process_memory", Replay: protocol.SupportEmulated, Capacity: a.config.JournalCapacity}, MaxActiveRunsPerSession: maxQueued + 1, CancellationTarget: "none", CancellationImplementation: "unavailable"}, nil
}

func (a *Adapter) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	descriptor, err := a.Probe(ctx)
	if err != nil {
		return nil, err
	}
	if request.Reopen || request.NativeSessionID != "" || request.Adopted {
		return nil, &base.UnsupportedControlError{Feature: protocol.FeatureOpenReopen, Reason: base.ControlUnadvertised}
	}
	if request.Participant.ID == "" {
		return nil, base.ErrInvalidParticipant
	}
	if err := base.RefuseUnadvertisedSettings(request, descriptor.Capabilities); err != nil {
		return nil, err
	}
	if err := base.RefuseUnadvertisedToolSources(request); err != nil {
		return nil, err
	}
	if err := base.RefuseUnadvertisedTools(request); err != nil {
		return nil, err
	}
	config := a.config
	process, err := rpc.Start(rpc.ProcessConfig{Path: config.Executable, Args: config.Args, Dir: config.WorkingDirectory, Env: config.Environment, FrameLimit: config.FrameLimit, QueueCapacity: config.QueueCapacity})
	if err != nil {
		return nil, err
	}
	kept := false
	defer func() {
		if !kept {
			_ = process.Close()
		}
	}()
	call, cancel := context.WithTimeout(ctx, config.RequestTimeout)
	defer cancel()
	var pong struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := process.Client.Call(call, "ping", map[string]any{}, &pong); err != nil {
		return nil, err
	}
	if pong.ProtocolVersion != 3 {
		return nil, errors.New("copilot: server does not speak pinned protocol 3")
	}
	var status struct {
		Version string `json:"version"`
	}
	if err := process.Client.Call(call, "status.get", map[string]any{}, &status); err != nil {
		return nil, err
	}
	if status.Version != PinnedVersion {
		return nil, fmt.Errorf("copilot: server does not report pinned CLI version %s", PinnedVersion)
	}
	params := map[string]any{"clientName": "open-agent-protocol", "workingDirectory": config.WorkingDirectory, "streaming": true, "requestPermission": false, "requestUserInput": false, "envValueMode": "direct"}
	if config.Model != "" {
		params["model"] = config.Model
	}
	var created struct {
		SessionID string `json:"sessionId"`
	}
	if err := process.Client.Call(call, "session.create", params, &created); err != nil {
		return nil, err
	}
	if created.SessionID == "" {
		return nil, errors.New("copilot: session.create returned no sessionId")
	}
	id := request.SessionID
	if id == "" {
		id = protocol.SessionID(config.IDs.NewID("session"))
	}
	session := newSession(newReducer(id, created.SessionID, config.IDs, config.Clock), process, config.JournalCapacity, config.RequestTimeout)
	kept = true
	return session, nil
}

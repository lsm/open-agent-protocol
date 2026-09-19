package deepseek

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/deepseek/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/deepseek/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const (
	dshCorpusRepository = "https://github.com/deepseek-ai/deepseek-harness"
	dshCorpusTag        = "fb2c4b9"
	dshCorpusCommit     = "fb2c4b9e698e30edb738bca4cf0618587db7d203"
	dshCorpusCommitTree = "bd7dd6d90010a35d3d6ff9f12c1f6207d5b6fe38"

	dshBlobSDKProtocolTypes     = "605b97cc6945397563e6351e03ffde7a9436b23f"
	dshBlobSDKProtocolTransport = "36574f46bf3e34738be045408e25bb79932ff609"
	dshBlobSDKServer            = "1cc17059c9254c6bd4f809441bd9e43bc26a7d2d"
	dshBlobCoreSessionTypes     = "139fccd5a5660a8d0c4e1ef95f4e4d64b274230f"
	dshBlobCoreKnownEvents      = "dd6411240b0527ec98d5ff51bcfb3e8b5f47e715"
	dshBlobCoreAgentTypes       = "d0be69ac58747a042ac937a250878705bcbf0d8f"
	dshBlobCoreAgentInbox       = "db89cd3072677ebd6acbd40f7d496bab15c19cef"
	dshBlobCoreAgentRuntime     = "31338e8d8da6ccb2e99fd459abbe2238bf5c1736"
	dshBlobCoreAgentLoop        = "06e1f51b57277ba296698b6c8b810f0e455e3695"
	dshBlobLLMMessage           = "6f920fe0191d17c0a272fbc881eb7e37f8142815"
	dshBlobLLMTypes             = "bfddde7fc4b2a08144e2f76f8ca59e61a2b4e37f"
	dshBlobLLMAssistantStream   = "5d878020e8a2eab1a1a84d1867bf2923a409527a"
	dshBlobCoreSessionInvariant = "6ed0b6b3c5abf84dd4129281ed6029880c7e6ad3"
	dshBlobCoreSessionSurface   = "5d8ce74fe2461cb2f777a7bc7556795f337f0c03"
)

var dshLedgerFixtures = map[string]bool{
	"initialize-minimal": true, "initialize-repeat-rejected": true, "initialize-before-prompt": true,
	"message-enqueued": true, "owned-start": true, "overlap-rejected": true,
	"intercepted-no-run": true, "blocked-no-run": true, "empty-no-run": true,
	"completed-turn": true, "max-tokens-failed": true, "turn-end-reasons": true,
	"streaming-chunks": true, "tool-lifecycle": true, "tool-failed": true, "injected-origin": true,
	"subagent-run": true, "child-after-turn-end": true, "settlement": true,
	"process-exit": true, "shutdown": true,
	"unknown-ignorable": true, "unknown-required": true,
	"native-tolerant-frame": true, "adapter-strict-frame": true,
	"no-native-cancel": true, "no-wire-claim": true, "no-implied-replay": true,
}

type dshCorpusManifest struct {
	Version    int                     `json:"version"`
	Adapter    string                  `json:"adapter"`
	Tag        string                  `json:"tag"`
	Commit     string                  `json:"commit"`
	CommitTree string                  `json:"commit_tree"`
	Sources    dshCorpusSources        `json:"sources"`
	Cases      []dshCorpusManifestCase `json:"cases"`
}
type dshCorpusSources struct {
	SDKProtocolTypes     string `json:"sdk_protocol_types_blob"`
	SDKProtocolTransport string `json:"sdk_protocol_transport_blob"`
	SDKServer            string `json:"sdk_server_blob"`
	CoreSessionTypes     string `json:"core_session_types_blob"`
	CoreKnownEvents      string `json:"core_session_known_event_types_blob"`
	CoreAgentTypes       string `json:"core_agent_types_blob"`
	CoreAgentInbox       string `json:"core_agent_inbox_blob"`
	CoreAgentRuntime     string `json:"core_agent_runtime_types_blob"`
	CoreAgentLoop        string `json:"core_agent_loop_blob"`
	LLMMessage           string `json:"llm_message_blob"`
	LLMTypes             string `json:"llm_types_blob"`
	LLMAssistantStream   string `json:"llm_assistant_stream_blob"`
	CoreSessionInvariant string `json:"core_session_invariant_blob"`
	CoreSessionSurface   string `json:"core_session_surface_blob"`
}
type dshCorpusManifestCase struct {
	ID             string   `json:"id"`
	Path           string   `json:"path"`
	LedgerFixtures []string `json:"ledger_fixtures"`
}
type dshCorpusCase struct {
	Version      int                 `json:"version"`
	ID           string              `json:"id"`
	Native       string              `json:"native"`
	ExpectedOAP  string              `json:"expected_oap"`
	Mapping      string              `json:"mapping"`
	Omissions    string              `json:"omissions"`
	Provenance   dshCorpusProvenance `json:"provenance"`
	Capabilities map[string]string   `json:"advertised_capabilities"`
	IdentityMap  map[string]string   `json:"identity_map"`
	ServerMode   string              `json:"server_mode,omitempty"`
	OpenError    bool                `json:"open_error,omitempty"`
	CodecOnly    bool                `json:"codec_only,omitempty"`
	Noncanonical string              `json:"noncanonical_mismatch,omitempty"`
}
type dshCorpusProvenance struct {
	Repository string           `json:"repository"`
	Tag        string           `json:"tag"`
	Commit     string           `json:"commit"`
	CommitTree string           `json:"commit_tree"`
	Sources    dshCorpusSources `json:"sources"`
}
type dshFrame struct {
	Direction      string          `json:"direction"`
	Classification string          `json:"classification"`
	Fidelity       string          `json:"fidelity"`
	Action         string          `json:"action"`
	Raw            json.RawMessage `json:"raw"`
}
type dshControl struct {
	Type   string `json:"type"`
	Error  string `json:"error,omitempty"`
	Op     string `json:"op,omitempty"`
	Expect string `json:"expect,omitempty"`
	Status string `json:"status,omitempty"`
}
type dshCorpusMapping struct {
	Index          int    `json:"index"`
	Type           string `json:"type"`
	Direction      string `json:"direction"`
	Classification string `json:"classification"`
	Fidelity       string `json:"fidelity"`
	OAP            string `json:"oap,omitempty"`
}
type dshCorpusOmission struct {
	Index  int    `json:"index"`
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

type dshDecodedFrame struct {
	Message       *rpc.Message
	Notification  any
	Event         *native.Event
	Control       *dshControl
	Invalid       error
	RequestMethod string
}

func pinnedDSHSources() dshCorpusSources {
	return dshCorpusSources{
		SDKProtocolTypes:     dshBlobSDKProtocolTypes,
		SDKProtocolTransport: dshBlobSDKProtocolTransport,
		SDKServer:            dshBlobSDKServer,
		CoreSessionTypes:     dshBlobCoreSessionTypes,
		CoreKnownEvents:      dshBlobCoreKnownEvents,
		CoreAgentTypes:       dshBlobCoreAgentTypes,
		CoreAgentInbox:       dshBlobCoreAgentInbox,
		CoreAgentRuntime:     dshBlobCoreAgentRuntime,
		CoreAgentLoop:        dshBlobCoreAgentLoop,
		LLMMessage:           dshBlobLLMMessage,
		LLMTypes:             dshBlobLLMTypes,
		LLMAssistantStream:   dshBlobLLMAssistantStream,
		CoreSessionInvariant: dshBlobCoreSessionInvariant,
		CoreSessionSurface:   dshBlobCoreSessionSurface,
	}
}

func TestDSHEvidenceCorpus(t *testing.T) {
	root := dshCorpusRoot(t)
	manifest := dshLoadJSON[dshCorpusManifest](t, filepath.Join(root, "manifest.json"))
	if manifest.Version != 1 || manifest.Adapter != "deepseek-harness-jsonrpc" || manifest.Tag != dshCorpusTag || manifest.Commit != dshCorpusCommit || manifest.CommitTree != dshCorpusCommitTree || manifest.Sources != pinnedDSHSources() {
		t.Fatalf("corpus provenance pin mismatch: %+v", manifest)
	}
	seenIDs, seenPaths, covered := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, entry := range manifest.Cases {
		entry := entry
		t.Run(entry.ID, func(t *testing.T) {
			if entry.ID == "" || seenIDs[entry.ID] || seenPaths[entry.Path] || !dshSafeRelative(entry.Path) || len(entry.LedgerFixtures) == 0 {
				t.Fatalf("invalid manifest entry: %+v", entry)
			}
			for _, fixture := range entry.LedgerFixtures {
				if !dshLedgerFixtures[fixture] || covered[fixture] {
					t.Fatalf("invalid or duplicate ledger fixture %q", fixture)
				}
				covered[fixture] = true
			}
			seenIDs[entry.ID], seenPaths[entry.Path] = true, true
			runDSHCorpusCase(t, root, entry)
		})
	}
	for fixture := range dshLedgerFixtures {
		if !covered[fixture] {
			t.Errorf("ledger fixture %q has no case", fixture)
		}
	}
	assertDSHCorpusInventory(t, root, manifest)
}

func runDSHCorpusCase(t *testing.T, root string, entry dshCorpusManifestCase) {
	dir := filepath.Join(root, entry.Path)
	definition := dshLoadJSON[dshCorpusCase](t, filepath.Join(dir, "case.json"))
	p := definition.Provenance
	if definition.Version != 1 || definition.ID != entry.ID || p.Repository != dshCorpusRepository || p.Tag != dshCorpusTag || p.Commit != dshCorpusCommit || p.CommitTree != dshCorpusCommitTree || p.Sources != pinnedDSHSources() || len(definition.Capabilities) == 0 || len(definition.IdentityMap) == 0 {
		t.Fatalf("invalid case metadata: %+v", definition)
	}
	frames, decoded := dshLoadFrames(t, filepath.Join(dir, definition.Native))
	mappings := dshLoadJSON[[]dshCorpusMapping](t, filepath.Join(dir, definition.Mapping))
	omissions := dshLoadJSON[[]dshCorpusOmission](t, filepath.Join(dir, definition.Omissions))
	assertDSHClassifications(t, frames, decoded, mappings, omissions)
	assertDSHCaseActions(t, definition, frames)
	execution := dshExecution{}
	if definition.CodecOnly {
		if definition.Noncanonical == "" {
			t.Fatal("codec-only case must state its noncanonical mismatch")
		}
		for i := range frames {
			if decoded[i].Invalid == nil {
				t.Fatalf("frame %d: production codec accepted a framing-violation fixture", i+1)
			}
		}
		assertDSHLedgerEvidence(t, entry.LedgerFixtures, definition, frames, decoded, &execution)
		assertDSHExpected(t, filepath.Join(dir, definition.ExpectedOAP), nil)
		return
	}
	if definition.ServerMode != "" {
		execution = runDSHProcessCase(t, dir, definition, frames, decoded)
	} else {
		execution = runDSHFakeCase(t, definition, frames, decoded)
	}
	assertDSHLedgerEvidence(t, entry.LedgerFixtures, definition, frames, decoded, &execution)
	assertDSHExpected(t, filepath.Join(dir, definition.ExpectedOAP), execution.envelopes)
}

func runDSHFakeCase(t *testing.T, definition dshCorpusCase, frames []dshFrame, decoded []dshDecodedFrame) dshExecution {
	t.Helper()
	execution := dshExecution{}
	client := newCorpusClient()
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, string, error) { return client, "deepseek-chat", nil }), Clock: &testClock{}, IDs: &testIDs{}, JournalCapacity: 64})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := implementation.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.AssertDescriptor(t, descriptor)
	execution.descriptor = descriptor
	session := adaptertest.AssertInitialState(t, implementation, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})

	type submitResult struct {
		admission protocol.MessageSubmitResponse
		stream    base.EventStream
		err       error
	}
	var pending chan submitResult
	var settled []submitResult
	submits := 0

	waitReap := func() {
		if pending == nil {
			return
		}
		select {
		case result := <-pending:
			settled = append(settled, result)
		case <-time.After(5 * time.Second):
			t.Fatal("submit did not settle")
		}
		pending = nil
	}
	for i, frame := range frames {
		switch frame.Action {
		case "submit":
			waitReap()
			submits++
			channel := make(chan submitResult, 1)
			pending = channel
			go func() {
				admission, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
				channel <- submitResult{admission, stream, err}
			}()
			select {
			case <-client.started:
			case <-time.After(5 * time.Second):
				t.Fatal("prompt request was not written")
			}
			var want native.SessionPromptParams
			dshDecodeStrict(t, decoded[i].Message.Params, &want, fmt.Sprintf("frame %d params", i+1))
			got, _ := json.Marshal(client.lastCall(t))
			expected, _ := json.Marshal(want)
			if !bytes.Equal(got, expected) {
				t.Fatalf("frame %d: adapter wrote params %s, want %s", i+1, got, expected)
			}
		case "reply":
			var result native.SessionPromptResult
			dshDecodeStrict(t, decoded[i].Message.Result, &result, fmt.Sprintf("frame %d result", i+1))
			if result.MessageID == "" {
				t.Fatalf("frame %d: prompt response omitted messageId", i+1)
			}
			client.prompts <- corpusReply{id: result.MessageID}
		case "reply-error":
			if decoded[i].Message.Error == nil {
				t.Fatalf("frame %d: expected JSON-RPC error response", i+1)
			}
			client.prompts <- corpusReply{err: fmt.Errorf("fixture prompt rejection: %s", decoded[i].Message.Error.Message)}
		case "observe":
			client.deliver(t, decoded[i])
		case "overlap-submit":
			calls := client.callCount()
			_, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello again")}}})
			if !errors.Is(err, base.ErrRunActive) {
				t.Fatalf("frame %d: overlapping submit error = %v, want %v", i+1, err, base.ErrRunActive)
			}
			if stream != nil {
				t.Fatalf("frame %d: rejected overlap exposed an event stream", i+1)
			}
			if client.callCount() != calls {
				t.Fatalf("frame %d: rejected overlap wrote a native request", i+1)
			}
			execution.overlapRejected = true
		case "oap-control":
			control := decoded[i].Control
			switch control.Op {
			case "cancel":
				if _, err := session.Cancel(context.Background(), "run"); !errors.Is(err, errUnavailable) {
					t.Fatalf("frame %d: cancel error = %v", i+1, err)
				}
			case "resume":
				if _, _, err := session.Resume(context.Background(), base.ResumeRequest{RunID: "run"}); !errors.Is(err, errUnavailable) {
					t.Fatalf("frame %d: resume error = %v", i+1, err)
				}
			case "resolve":
				if err := session.Resolve(context.Background(), base.InteractionResolution{}); !errors.Is(err, errUnavailable) {
					t.Fatalf("frame %d: resolve error = %v", i+1, err)
				}
			case "assert-state":
				state, err := session.State(context.Background())
				if err != nil {
					t.Fatalf("frame %d: state error = %v", i+1, err)
				}
				if string(state.Status) != control.Status {
					t.Fatalf("frame %d: session status = %q, want %q", i+1, state.Status, control.Status)
				}
				execution.assertStates = append(execution.assertStates, control.Status)
			default:
				t.Fatalf("frame %d: unsupported oap control %q", i+1, control.Op)
			}
			if client.callCount() != submits {
				t.Fatalf("frame %d: adapter control produced a native request", i+1)
			}
			execution.controlsUnavailable = true
		case "wait-submit":

			waitReap()
		case "process-exit":
			client.transportClose(errors.New(decoded[i].Control.Error))
		case "observe-invalid":
			if decoded[i].Invalid == nil {
				t.Fatalf("frame %d: production validation accepted an invalid observation", i+1)
			}
			client.transportClose(decoded[i].Invalid)
		default:
			t.Fatalf("frame %d: unsupported action %q for the in-process harness", i+1, frame.Action)
		}
	}
	waitReap()
	for _, result := range settled {
		execution.record(t, result.admission, result.stream, result.err)
	}
	execution.wirePrompts = client.callCount()
	if execution.wirePrompts != submits {
		t.Fatalf("adapter wrote %d native prompts for %d submissions", execution.wirePrompts, submits)
	}
	return execution
}

func runDSHProcessCase(t *testing.T, dir string, definition dshCorpusCase, frames []dshFrame, decoded []dshDecodedFrame) dshExecution {
	t.Helper()
	execution := dshExecution{}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	logPath := filepath.Join(workspace, "wire.log")
	environment := []string{
		"OAP_DSH_FIXTURE_MODE=" + definition.ServerMode,
		"OAP_DSH_FIXTURE_LOG=" + logPath,
		"OAP_DSH_FIXTURE_SCRIPT=" + filepath.Join(dir, definition.Native),
		"OAP_DSH_FIXTURE_PROMPT_ERROR=fixture prompt rejection",
	}
	if receipts := dshResponseReceipts(decoded); len(receipts) > 0 {
		environment = append(environment, "OAP_DSH_FIXTURE_RECEIPT="+receipts[0])
	}
	implementation, err := New(Config{Executable: self, Args: []string{"--dsh-fixture-server"}, Environment: environment, WorkingDirectory: workspace, Provider: "fixture-provider", Model: "fixture-model", Clock: &testClock{}, IDs: &testIDs{}, JournalCapacity: 64})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := implementation.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.AssertDescriptor(t, descriptor)
	execution.descriptor = descriptor

	type submitResult struct {
		admission protocol.MessageSubmitResponse
		stream    base.EventStream
		err       error
	}
	var session base.Session
	var pending chan submitResult
	waitPending := func() {
		if pending == nil {
			return
		}
		select {
		case result := <-pending:
			execution.record(t, result.admission, result.stream, result.err)
		case <-time.After(15 * time.Second):
			t.Fatal("submit did not settle")
		}
		pending = nil
	}
	for i, frame := range frames {
		switch frame.Action {
		case "open":
			if session != nil {
				t.Fatalf("frame %d: session opened twice", i+1)
			}
			session, err = implementation.Open(context.Background(), base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
			if definition.OpenError {
				if err == nil {
					t.Fatalf("frame %d: expected handshake failure", i+1)
				}
				execution.openErr = err
				session = nil
			} else if err != nil {
				t.Fatalf("frame %d: open: %v", i+1, err)
			}
		case "submit":
			if session == nil {
				t.Fatalf("frame %d: submit without an open session", i+1)
			}
			waitPending()
			channel := make(chan submitResult, 1)
			pending = channel
			go func() {
				admission, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
				channel <- submitResult{admission, stream, err}
			}()
		case "shutdown":
			if session == nil {
				t.Fatalf("frame %d: shutdown without an open session", i+1)
			}
			waitPending()
			if err := session.Close(context.Background()); err != nil {
				t.Fatalf("frame %d: close: %v", i+1, err)
			}
			execution.closed = true
			session = nil
		case "auto":

		default:
			t.Fatalf("frame %d: action %q is not valid for the process harness", i+1, frame.Action)
		}
	}
	waitPending()
	if session != nil {
		t.Fatal("process case ended without a shutdown frame")
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	var expected []string
	for i, frame := range frames {
		if frame.Direction != "host-to-dsh" {
			continue
		}
		want := string(dshWireBytes(t, frame.Raw, filepath.Join(dir, definition.Native), i+1))
		expected = append(expected, strings.ReplaceAll(want, "WORKSPACE_PATH", workspace))
	}
	if len(lines) != len(expected) {
		t.Fatalf("host wire log has %d lines, want %d: %q", len(lines), len(expected), lines)
	}
	for i := range expected {
		if lines[i] != expected[i] {
			t.Fatalf("host wire line %d = %s, want %s", i+1, lines[i], expected[i])
		}
	}
	execution.logLines = lines
	for _, line := range lines {
		if strings.Contains(line, `"method":"session/prompt"`) {
			execution.wirePrompts++
		}
	}
	return execution
}

type dshExecution struct {
	descriptor          base.Descriptor
	admissions          []protocol.MessageSubmitResponse
	submitErrors        []error
	envelopes           []protocol.Envelope
	wirePrompts         int
	logLines            []string
	openErr             error
	closed              bool
	overlapRejected     bool
	controlsUnavailable bool
	assertStates        []string
}

func (e *dshExecution) record(t *testing.T, admission protocol.MessageSubmitResponse, stream base.EventStream, err error) {
	t.Helper()
	if err == nil {
		e.admissions = append(e.admissions, admission)
	} else {
		e.submitErrors = append(e.submitErrors, err)
	}
	if stream == nil {
		return
	}
	events := adaptertest.Drain(t, stream, 5*time.Second)
	if len(events) > 0 {
		e.validate(t, admission, events)
	}
	e.envelopes = append(e.envelopes, events...)
}

func (e *dshExecution) validate(t *testing.T, admission protocol.MessageSubmitResponse, events []protocol.Envelope) {
	t.Helper()
	adaptertest.AssertProtocolValidWithDescriptor(t, admission, e.descriptor, events)
}

type corpusClient struct {
	in      chan rpc.InboundMessage
	done    chan struct{}
	prompts chan corpusReply
	started chan struct{}
	mu      sync.Mutex
	closed  bool
	dead    bool
	err     error
	calls   []native.SessionPromptParams
}

type corpusReply struct {
	id  string
	err error
}

func newCorpusClient() *corpusClient {
	return &corpusClient{in: make(chan rpc.InboundMessage, 64), done: make(chan struct{}), prompts: make(chan corpusReply, 8), started: make(chan struct{}, 8)}
}

func (c *corpusClient) Call(ctx context.Context, method string, params, result any) error {
	return c.CallStarted(ctx, method, params, result, nil)
}
func (c *corpusClient) CallStarted(_ context.Context, method string, params any, result any, started chan<- error) error {
	if method != native.MethodSessionPrompt {
		return fmt.Errorf("fixture client: unexpected native call %q", method)
	}
	c.mu.Lock()
	promptParams, _ := params.(native.SessionPromptParams)
	c.calls = append(c.calls, promptParams)
	c.mu.Unlock()
	c.started <- struct{}{}
	if started != nil {
		started <- nil
		close(started)
	}
	reply := <-c.prompts
	if reply.err == nil {
		result.(*native.SessionPromptResult).MessageID = reply.id
	}
	return reply.err
}
func (c *corpusClient) Inbound() <-chan rpc.InboundMessage { return c.in }
func (c *corpusClient) Done() <-chan struct{}              { return c.done }
func (c *corpusClient) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	return errors.New("EOF")
}
func (c *corpusClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		close(c.done)
		c.closed = true
	}
	return nil
}

func (c *corpusClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}
func (c *corpusClient) lastCall(t *testing.T) native.SessionPromptParams {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.calls) == 0 {
		t.Fatal("no native prompt call was recorded")
	}
	return c.calls[len(c.calls)-1]
}
func (c *corpusClient) deliver(t *testing.T, decoded dshDecodedFrame) {
	t.Helper()
	c.mu.Lock()
	dead := c.dead
	c.mu.Unlock()
	if dead {
		t.Fatal("observation delivered after transport closure")
	}
	c.in <- rpc.InboundMessage{Notification: &rpc.NotificationMessage{Method: decoded.Message.Method, Params: decoded.Message.Params, Value: decoded.Notification}}
	c.barrier(t)
}
func (c *corpusClient) barrier(t *testing.T) {
	t.Helper()
	ack := make(chan struct{})
	c.in <- rpc.InboundMessage{Barrier: ack}
	select {
	case <-ack:
	case <-time.After(5 * time.Second):
		t.Fatal("reducer barrier timed out")
	}
}
func (c *corpusClient) transportClose(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead {
		return
	}
	c.dead = true
	c.err = err
	if !c.closed {
		close(c.done)
		c.closed = true
	}
}

func dshLoadFrames(t *testing.T, filename string) ([]dshFrame, []dshDecodedFrame) {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if len(lines) == 0 || len(lines[0]) == 0 {
		t.Fatal("empty native transcript")
	}
	frames := make([]dshFrame, 0, len(lines))
	decoded := make([]dshDecodedFrame, 0, len(lines))
	requestMethods := map[string]string{}
	for i, line := range lines {
		var frame dshFrame
		dshDecodeStrict(t, line, &frame, fmt.Sprintf("%s frame %d", filename, i+1))
		entry := dshDecodedFrame{}
		switch frame.Direction {
		case "dsh-to-host":
			wire := dshWireBytes(t, frame.Raw, filename, i+1)
			reader := bytes.NewReader(append(append([]byte(nil), wire...), '\n'))
			if frame.Action == "decode-error-unterminated" {
				reader = bytes.NewReader(wire)
			}
			message, decodeErr := rpc.NewDecoder(reader, rpc.DefaultFrameLimit).Decode()
			if decodeErr == nil && message.Kind == rpc.MessageNotification {
				value, nativeErr := native.DecodeNotification(message.Method, message.Params)
				if nativeErr != nil {
					decodeErr = nativeErr
				} else {
					entry.Notification = value
					if event, ok := value.(*native.SessionEventNotification); ok {
						eventCopy := event.Event
						entry.Event = &eventCopy
					}
				}
			}
			switch frame.Action {
			case "decode-error", "decode-error-unterminated", "observe-invalid":
				if decodeErr == nil {
					t.Fatalf("frame %d: production codec accepted an invalid frame", i+1)
				}
				entry.Invalid = decodeErr
			default:
				if decodeErr != nil {
					t.Fatalf("frame %d production decode: %v", i+1, decodeErr)
				}
				entry.Message = &message
			}
			if decodeErr == nil {
				entry.Message = &message
			}
		case "host-to-dsh":
			wire := dshWireBytes(t, frame.Raw, filename, i+1)
			message, err := rpc.NewDecoder(bytes.NewReader(append(append([]byte(nil), wire...), '\n')), rpc.DefaultFrameLimit).Decode()
			if err != nil {
				t.Fatalf("frame %d request decode: %v", i+1, err)
			}
			if message.Kind != rpc.MessageRequest {
				t.Fatalf("frame %d: host frame is not a request", i+1)
			}
			encoded, err := message.MarshalJSON()
			if err != nil {
				t.Fatalf("frame %d production encode: %v", i+1, err)
			}
			if !bytes.Equal(encoded, wire) {
				t.Fatalf("frame %d is not canonical outbound JSON", i+1)
			}
			id, _ := message.ID.MarshalJSON()
			requestMethods[string(id)] = message.Method
			entry.Message = &message
		case "harness-control":
			var control dshControl
			dshDecodeStrict(t, frame.Raw, &control, fmt.Sprintf("%s frame %d", filename, i+1))
			switch control.Type {
			case "process_exit":
				if control.Error == "" {
					t.Fatalf("frame %d invalid process-exit control", i+1)
				}
			case "oap_control":
				switch control.Op {
				case "cancel", "resume", "resolve":
					if control.Expect != "unavailable" {
						t.Fatalf("frame %d invalid oap control expectation", i+1)
					}
				case "assert-state":
					if control.Status != "idle" && control.Status != "running" {
						t.Fatalf("frame %d invalid assert-state status", i+1)
					}
				default:
					t.Fatalf("frame %d invalid oap control op %q", i+1, control.Op)
				}
			case "harness_sync":
				if control.Op != "wait-submit" {
					t.Fatalf("frame %d invalid harness sync op %q", i+1, control.Op)
				}
			default:
				t.Fatalf("frame %d invalid harness control type %q", i+1, control.Type)
			}
			entry.Control = &control
		default:
			t.Fatalf("frame %d invalid direction %q", i+1, frame.Direction)
		}
		frames = append(frames, frame)
		decoded = append(decoded, entry)
	}

	for i := range decoded {
		if frames[i].Direction != "dsh-to-host" || decoded[i].Message == nil || decoded[i].Notification != nil {
			continue
		}
		if decoded[i].Message.Kind == rpc.MessageResponse || decoded[i].Message.Kind == rpc.MessageError {
			id, _ := decoded[i].Message.ID.MarshalJSON()
			decoded[i].RequestMethod = requestMethods[string(id)]
		}
	}
	return frames, decoded
}

func dshWireBytes(t *testing.T, raw json.RawMessage, filename string, index int) []byte {
	t.Helper()
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var literal string
		if err := json.Unmarshal(trimmed, &literal); err != nil {
			t.Fatalf("%s frame %d: raw string literal: %v", filename, index, err)
		}
		return []byte(literal)
	}
	return raw
}

func assertDSHCaseActions(t *testing.T, definition dshCorpusCase, frames []dshFrame) {
	t.Helper()
	for i, frame := range frames {
		var valid map[string]bool
		switch frame.Direction {
		case "dsh-to-host":
			valid = map[string]bool{"auto": true, "reply": true, "reply-error": true, "observe": true, "observe-invalid": true, "decode-error": true, "decode-error-unterminated": true}
		case "host-to-dsh":
			valid = map[string]bool{"submit": true, "open": true, "shutdown": true, "overlap-submit": true}
		case "harness-control":
			valid = map[string]bool{"process-exit": true, "oap-control": true, "wait-submit": true}
		}
		if valid == nil || !valid[frame.Action] {
			t.Fatalf("frame %d has unknown action %q for %q", i+1, frame.Action, frame.Direction)
		}
	}
	if definition.CodecOnly {
		for i, frame := range frames {
			if frame.Direction != "dsh-to-host" || (frame.Action != "decode-error" && frame.Action != "decode-error-unterminated") {
				t.Fatalf("codec-only frame %d must be a decode-error observation", i+1)
			}
		}
	}
	if definition.ServerMode != "" {
		for i, frame := range frames {
			switch frame.Action {
			case "open", "submit", "shutdown", "auto":
			default:
				t.Fatalf("frame %d action %q is not valid for the process harness", i+1, frame.Action)
			}
		}
	} else {
		for i, frame := range frames {
			switch frame.Action {
			case "submit", "reply", "reply-error", "observe", "overlap-submit", "oap-control", "process-exit", "observe-invalid", "decode-error", "decode-error-unterminated", "wait-submit":
			default:
				t.Fatalf("frame %d action %q requires the process harness", i+1, frame.Action)
			}
		}
	}
	if definition.OpenError && definition.ServerMode == "" {
		t.Fatal("open_error is only meaningful for a process case")
	}
}

func dshFrameType(frame dshFrame, decoded dshDecodedFrame) string {
	switch frame.Direction {
	case "harness-control":
		if decoded.Control.Type == "process_exit" {
			return "process_exit"
		}
		if decoded.Control.Type == "harness_sync" {
			return "sync:" + decoded.Control.Op
		}
		return "oap_control:" + decoded.Control.Op
	case "host-to-dsh":
		if decoded.Message != nil {
			return "request:" + decoded.Message.Method
		}
	case "dsh-to-host":
		if decoded.Invalid != nil {
			return "codec-error"
		}
		if decoded.Notification != nil {
			if decoded.Event != nil {
				return "session.event/" + decoded.Event.Type
			}
			return decoded.Message.Method
		}
		if decoded.Message != nil && (decoded.Message.Kind == rpc.MessageResponse || decoded.Message.Kind == rpc.MessageError) {
			return "response:" + decoded.RequestMethod
		}
	}
	return "codec-error"
}

func assertDSHClassifications(t *testing.T, frames []dshFrame, decoded []dshDecodedFrame, mappings []dshCorpusMapping, omissions []dshCorpusOmission) {
	t.Helper()
	if len(frames) != len(mappings) {
		t.Fatalf("mapping count %d does not cover %d frames", len(mappings), len(frames))
	}
	omitted := map[int]dshCorpusOmission{}
	for _, o := range omissions {
		if o.Index < 1 || o.Index > len(frames) || o.Reason == "" || omitted[o.Index].Index != 0 {
			t.Fatalf("invalid omission: %+v", o)
		}
		omitted[o.Index] = o
	}
	for i, f := range frames {
		typ := dshFrameType(f, decoded[i])
		m := mappings[i]
		if m.Index != i+1 || m.Type != typ || m.Direction != f.Direction || m.Classification != f.Classification || m.Fidelity != f.Fidelity {
			t.Fatalf("frame %d mapping mismatch: %+v type=%s", i+1, m, typ)
		}
		if o, ok := omitted[i+1]; ok && o.Type != typ {
			t.Fatalf("omission %d type mismatch: %q vs %q", i+1, o.Type, typ)
		}
		switch f.Classification {
		case "mapped":
			if omitted[i+1].Index != 0 || m.OAP == "" {
				t.Fatalf("mapped frame %d must name its OAP projection and cannot be omitted", i+1)
			}
		case "required-unmapped":
			if omitted[i+1].Index != 0 || m.OAP != "" || f.Fidelity != "unsupported" {
				t.Fatalf("required-unmapped frame %d has inconsistent mapping semantics", i+1)
			}
		case "observed-only":
			if omitted[i+1].Index == 0 || m.OAP != "" {
				t.Fatalf("observed-only frame %d has inconsistent omission semantics", i+1)
			}
		default:
			t.Fatalf("invalid classification %q", f.Classification)
		}
		switch f.Fidelity {
		case "native", "normalized", "synthesized", "lossy", "unsupported":
		default:
			t.Fatalf("invalid fidelity %q", f.Fidelity)
		}
	}
}

func hasDSHRequest(frames []dshFrame, decoded []dshDecodedFrame, method string) bool {
	for i := range frames {
		if frames[i].Direction == "host-to-dsh" && decoded[i].Message != nil && decoded[i].Message.Method == method {
			return true
		}
	}
	return false
}

func hasDSHEvent(decoded []dshDecodedFrame, typ string) bool {
	for i := range decoded {
		if decoded[i].Event != nil && decoded[i].Event.Type == typ {
			return true
		}
	}
	return false
}

func dshEventFrameIndex(decoded []dshDecodedFrame, typ string) (int, bool) {
	for i := range decoded {
		if decoded[i].Event != nil && decoded[i].Event.Type == typ {
			return i + 1, true
		}
	}
	return 0, false
}

func hasDSHTurnEndKind(decoded []dshDecodedFrame, kind string) bool {
	for i := range decoded {
		if decoded[i].Event == nil || decoded[i].Event.Type != "turn/end" {
			continue
		}

		var data map[string]json.RawMessage
		if native.DecodeStrict(decoded[i].Event.Data, &data) != nil {
			continue
		}
		var reason map[string]json.RawMessage
		if native.DecodeStrict(data["reason"], &reason) != nil {
			continue
		}
		var value string
		if json.Unmarshal(reason["kind"], &value) == nil && value == kind {
			return true
		}
	}
	return false
}

func lastDSHEnvelope(t *testing.T, execution dshExecution) protocol.Envelope {
	t.Helper()
	if len(execution.envelopes) == 0 {
		t.Fatal("case produced no OAP envelopes")
	}
	return execution.envelopes[len(execution.envelopes)-1]
}

func hasDSHEnvelopeType(execution dshExecution, typ protocol.EnvelopeType) bool {
	for _, envelope := range execution.envelopes {
		if envelope.Type == typ {
			return true
		}
	}
	return false
}

func dshFailedCode(t *testing.T, envelope protocol.Envelope) (string, bool) {
	t.Helper()
	if envelope.Type != protocol.TypeRunFailed {
		return "", false
	}
	var payload protocol.RunFailedPayload
	if err := envelope.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	return payload.Error.Code, true
}

func dshResponseReceipts(decoded []dshDecodedFrame) []string {
	var receipts []string
	for i := range decoded {
		if decoded[i].Message == nil || decoded[i].Notification != nil || decoded[i].RequestMethod != native.MethodSessionPrompt {
			continue
		}
		if decoded[i].Message.Kind != rpc.MessageResponse {
			continue
		}
		var result native.SessionPromptResult
		if native.DecodeStrict(decoded[i].Message.Result, &result) == nil && result.MessageID != "" {
			receipts = append(receipts, result.MessageID)
		}
	}
	return receipts
}

func assertDSHLedgerEvidence(t *testing.T, labels []string, definition dshCorpusCase, frames []dshFrame, decoded []dshDecodedFrame, execution *dshExecution) {
	t.Helper()
	for _, label := range labels {
		ok := true
		switch label {
		case "initialize-minimal":
			ok = hasDSHRequest(frames, decoded, native.MethodInitialize)
			for i := range frames {
				if ok && frames[i].Direction == "host-to-dsh" && decoded[i].Message != nil && decoded[i].Message.Method == native.MethodInitialize {
					var params map[string]json.RawMessage
					if native.DecodeStrict(decoded[i].Message.Params, &params) != nil {
						ok = false
					}
					if _, extra := params["maxTokens"]; extra {
						ok = false
					}
				}
			}

			pinned := native.ValidateInitializeResult(native.InitializeResult{ServerInfo: native.ServerInfo{Name: native.ServerName, Version: native.ServerVersion}}) == nil
			foreign := native.ValidateInitializeResult(native.InitializeResult{ServerInfo: native.ServerInfo{Name: "other-runtime", Version: native.ServerVersion}}) != nil
			ok = ok && pinned && foreign && execution.openErr == nil
		case "initialize-repeat-rejected":
			count, prompts := 0, 0
			for _, line := range execution.logLines {
				switch {
				case strings.Contains(line, `"method":"initialize"`):
					count++
				case strings.Contains(line, `"method":"session/prompt"`):
					prompts++
				}
			}
			ok = count == 1 && prompts >= 2 && execution.closed
		case "initialize-before-prompt":
			prompts := 0
			for _, line := range execution.logLines {
				if strings.Contains(line, `"method":"session/prompt"`) {
					prompts++
				}
			}
			ok = execution.openErr != nil && prompts == 0 && len(execution.envelopes) == 0
		case "message-enqueued":
			receipts := dshResponseReceipts(decoded)
			inserted, admitted := false, false
			for i := range decoded {
				if decoded[i].Event == nil || decoded[i].Event.Type != "agent/inbox/spliced" {
					continue
				}
				var splice native.InboxSpliced
				if native.DecodeStrict(decoded[i].Event.Data, &splice) != nil {
					continue
				}
				for _, message := range splice.Inserted {
					for _, receipt := range receipts {
						if message.ID == receipt {
							inserted = true
						}
					}
				}
			}
			for _, admission := range execution.admissions {
				for _, receipt := range receipts {
					if string(admission.SubmissionID) == receipt {
						admitted = true
					}
				}
			}
			ok = len(receipts) > 0 && inserted && admitted
		case "owned-start":
			ok = len(execution.admissions) > 0 && execution.admissions[0].Admission == protocol.AdmissionStarted && len(execution.envelopes) > 0 && execution.envelopes[0].Type == protocol.TypeRunStarted
		case "overlap-rejected":
			ok = execution.overlapRejected
		case "intercepted-no-run":
			ok = hasDSHTurnEndKind(decoded, "interrupted") && len(execution.envelopes) == 0 && len(execution.submitErrors) > 0
		case "blocked-no-run":
			ok = hasDSHTurnEndKind(decoded, "blocked") && len(execution.envelopes) == 0 && len(execution.submitErrors) > 0
		case "empty-no-run":
			ok = hasDSHTurnEndKind(decoded, "completed") && !hasDSHEvent(decoded, "step/start") && !hasDSHEvent(decoded, "user/message") && len(execution.envelopes) == 0 && len(execution.submitErrors) > 0
		case "completed-turn":
			envelope := lastDSHEnvelope(t, *execution)
			var payload protocol.RunCompletedPayload
			usage := false
			if err := envelope.DecodePayload(&payload); err == nil && envelope.Type == protocol.TypeRunCompleted {
				usage = payload.Usage != nil
			}
			ok = envelope.Type == protocol.TypeRunCompleted && hasDSHTurnEndKind(decoded, "completed") && usage
		case "max-tokens-failed":
			code, failed := dshFailedCode(t, lastDSHEnvelope(t, *execution))
			ok = hasDSHTurnEndKind(decoded, "max-tokens") && failed && code == "deepseek_max-tokens"
		case "turn-end-reasons":
			failedRuns := 0
			for _, envelope := range execution.envelopes {
				if envelope.Type == protocol.TypeRunFailed {
					failedRuns++
				}
			}
			ok = hasDSHTurnEndKind(decoded, "error") && hasDSHTurnEndKind(decoded, "aborted") && failedRuns >= 2
		case "streaming-chunks":

			records, mapped := 0, true
			for i := range decoded {
				if decoded[i].Event == nil || decoded[i].Event.Type != "assistant/message" {
					continue
				}
				var message native.AssistantMessageEvent
				if native.DecodeStrict(decoded[i].Event.Data, &message) != nil || len(message.Stream) == 0 {
					continue
				}
				records += len(message.Stream)
				mapped = mapped && frames[i].Classification == "mapped"
			}
			ok = records > 0 && mapped && hasDSHEnvelopeType(*execution, protocol.TypeContentDelta) && hasDSHEnvelopeType(*execution, protocol.TypeRunCompleted)
		case "tool-lifecycle":
			ok = hasDSHEvent(decoded, "tool/call") && hasDSHEvent(decoded, "tool/result") && hasDSHEnvelopeType(*execution, protocol.TypeActionCallRequested) && hasDSHEnvelopeType(*execution, protocol.TypeActionCallStarted) && hasDSHEnvelopeType(*execution, protocol.TypeActionCallCompleted)
		case "tool-failed":
			ok = hasDSHEnvelopeType(*execution, protocol.TypeActionCallFailed)
		case "injected-origin":
			synthetic, direct := false, false
			for i := range decoded {
				if decoded[i].Event == nil || decoded[i].Event.Type != "user/message" {
					continue
				}
				var message native.UserMessage
				if native.DecodeStrict(decoded[i].Event.Data, &message) == nil {
					if message.Source.Kind != "user" {
						synthetic = true
					} else if message.Source.Plugin == "" {
						direct = true
					}
				}
			}
			ok = synthetic && direct && len(execution.admissions) > 0
		case "subagent-run":
			started, finished := false, false
			for i := range decoded {
				if _, isStarted := decoded[i].Notification.(*native.SubagentStartedNotification); isStarted {
					started = true
				}
				if _, isFinished := decoded[i].Notification.(*native.SubagentFinishedNotification); isFinished {
					finished = true
				}
			}
			ok = started && finished && hasDSHEnvelopeType(*execution, protocol.TypeRunCompleted)
		case "child-after-turn-end":
			turnEnd, hasTurn := dshEventFrameIndex(decoded, "turn/end")
			finish := 0
			for i := range decoded {
				if _, isFinished := decoded[i].Notification.(*native.SubagentFinishedNotification); isFinished {
					finish = i + 1
				}
			}
			ok = hasTurn && finish > turnEnd
		case "settlement":
			terminal := false
			for _, envelope := range execution.envelopes {
				if envelope.Type == protocol.TypeRunCompleted || envelope.Type == protocol.TypeRunFailed {
					terminal = true
				}
			}
			ok = terminal && len(execution.assertStates) > 0 && execution.assertStates[len(execution.assertStates)-1] == "running"
		case "process-exit":
			control := false
			for i := range frames {
				if frames[i].Direction == "harness-control" && frames[i].Action == "process-exit" {
					control = true
				}
			}
			code, failed := dshFailedCode(t, lastDSHEnvelope(t, *execution))
			ok = control && failed && code == "deepseek_process_exit"
		case "unknown-ignorable":
			ignorable := false
			for i := range decoded {
				if decoded[i].Event != nil && decoded[i].Event.Ignorable != nil && *decoded[i].Event.Ignorable {
					ignorable = true
				}
			}
			ok = ignorable && hasDSHEnvelopeType(*execution, protocol.TypeRunCompleted)
		case "unknown-required":
			rejected := false
			for i := range frames {
				if frames[i].Action == "observe-invalid" && decoded[i].Event == nil {
					rejected = true
				}
			}
			ok = rejected && len(execution.envelopes) > 0
		case "native-tolerant-frame", "adapter-strict-frame":
			ok = definition.CodecOnly
			for i := range frames {
				if decoded[i].Invalid == nil {
					ok = false
				}
			}
		case "no-native-cancel", "no-implied-replay":
			ok = execution.controlsUnavailable && execution.wirePrompts == len(execution.admissions)+len(execution.submitErrors)
		case "no-wire-claim":
			_, err := native.DecodeNotification("agent/inbox/claimed", []byte(`{"message":{"id":"m","role":"user","content":[{"type":"text","text":"x"}],"source":{"kind":"user"}},"turn":1}`))
			deletion := false
			for i := range decoded {
				if decoded[i].Event == nil || decoded[i].Event.Type != "agent/inbox/spliced" {
					continue
				}
				var splice native.InboxSpliced
				if native.DecodeStrict(decoded[i].Event.Data, &splice) == nil && splice.RemovedCount != nil && *splice.RemovedCount > 0 && len(splice.Inserted) == 0 {
					deletion = true
				}
			}
			ok = err != nil && deletion
		case "shutdown":
			ok = execution.closed
			for _, line := range execution.logLines {
				if strings.Contains(line, `"method":"shutdown"`) {
					ok = ok && true
				}
			}
			ok = ok && len(execution.logLines) > 0 && containsDSHLog(execution.logLines, "shutdown")
		default:
			t.Fatalf("ledger label %q has no evidence rule", label)
		}
		if !ok {
			var trace []string
			for _, envelope := range execution.envelopes {
				code := ""
				if envelope.Type == protocol.TypeRunFailed {
					var payload protocol.RunFailedPayload
					if envelope.DecodePayload(&payload) == nil {
						code = payload.Error.Code
					}
				}
				trace = append(trace, string(envelope.Type)+":"+code)
			}
			t.Fatalf("ledger label %q lacks executable evidence (envelopes: %v; admissions: %d; submitErrors: %d; states: %v)", label, trace, len(execution.admissions), len(execution.submitErrors), execution.assertStates)
		}
	}
}

func containsDSHLog(lines []string, method string) bool {
	for _, line := range lines {
		if strings.Contains(line, `"method":"`+method+`"`) {
			return true
		}
	}
	return false
}

func assertDSHExpected(t *testing.T, filename string, events []protocol.Envelope) {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var expected []protocol.Envelope
	if len(bytes.TrimSpace(data)) == 0 {
		if os.Getenv("OAP_UPDATE_DSH_CORPUS") != "1" {
			t.Fatalf("%s empty; set OAP_UPDATE_DSH_CORPUS=1", filename)
		}
		encoded, _ := json.MarshalIndent(events, "", "  ")
		if err := os.WriteFile(filename, append(encoded, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		expected = events
	} else {
		dshDecodeStrict(t, data, &expected, filename)
	}
	if !dshEqualEvents(expected, events) {
		want, _ := json.MarshalIndent(expected, "", "  ")
		got, _ := json.MarshalIndent(events, "", "  ")
		t.Fatalf("normalized trace mismatch\nwant: %s\ngot: %s", want, got)
	}
}

func dshEqualEvents(a, b []protocol.Envelope) bool {
	return bytes.Equal(dshNormalizeTrace(a), dshNormalizeTrace(b))
}

func dshNormalizeTrace(events []protocol.Envelope) []byte {
	encoded, err := json.Marshal(events)
	if err != nil {
		return nil
	}
	var frames []map[string]any
	if err := json.Unmarshal(encoded, &frames); err != nil {
		return nil
	}
	for _, frame := range frames {
		delete(frame, "timestamp_ms")
		delete(frame, "id")
		delete(frame, "in_reply_to")
		delete(frame, "run_id")
		delete(frame, "tool_call_id")
		if payload, ok := frame["payload"].(map[string]any); ok {
			delete(payload, "started_at_ms")
			delete(payload, "updated_at_ms")
			delete(payload, "duration_ms")
			delete(payload, "message_id")
			delete(payload, "run_id")
			delete(payload, "tool_call_id")
			if final, ok := payload["final_response"].(map[string]any); ok {
				delete(final, "id")
			}
		}
	}
	normalized, err := json.Marshal(frames)
	if err != nil {
		return nil
	}
	return normalized
}

func dshLoadJSON[T any](t *testing.T, filename string) T {
	t.Helper()
	var value T
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	dshDecodeStrict(t, data, &value, filename)
	return value
}

func dshDecodeStrict(t *testing.T, data []byte, value any, label string) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		t.Fatalf("decode %s: %v", label, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		t.Fatalf("decode %s: trailing JSON", label)
	}
}

func assertDSHCorpusInventory(t *testing.T, root string, manifest dshCorpusManifest) {
	t.Helper()
	listed := map[string]bool{"manifest.json": true}
	for _, e := range manifest.Cases {
		d := dshLoadJSON[dshCorpusCase](t, filepath.Join(root, e.Path, "case.json"))
		for _, name := range []string{"case.json", d.Native, d.ExpectedOAP, d.Mapping, d.Omissions} {
			if !dshSafeRelative(name) || filepath.Base(name) != name {
				t.Fatalf("invalid corpus filename %q", name)
			}
			listed[filepath.ToSlash(filepath.Join(e.Path, name))] = true
		}
	}
	var unlisted []string
	if err := filepath.WalkDir(root, func(path string, item os.DirEntry, err error) error {
		if err != nil || item.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if !listed[filepath.ToSlash(rel)] {
			unlisted = append(unlisted, filepath.ToSlash(rel))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(unlisted)
	if len(unlisted) > 0 {
		t.Fatalf("unlisted corpus files: %v", unlisted)
	}
}

func dshSafeRelative(path string) bool {
	clean := filepath.Clean(path)
	return path != "" && !filepath.IsAbs(path) && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func dshCorpusRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "fixtures", "adapters", "deepseek-harness-47f9438")
}

func TestDSHCorpusPinConstants(t *testing.T) {
	if dshCorpusTag == "" || dshCorpusCommit == "" || dshCorpusCommitTree == "" || CapabilityRevision == "" || PinnedVersion == "" {
		t.Fatal("missing DeepSeek corpus pin")
	}
	if dshBlobSDKProtocolTypes == "" || dshBlobSDKProtocolTransport == "" || dshBlobSDKServer == "" || dshBlobCoreSessionTypes == "" || dshBlobCoreKnownEvents == "" || dshBlobCoreAgentTypes == "" || dshBlobCoreAgentInbox == "" || dshBlobCoreAgentRuntime == "" || dshBlobCoreAgentLoop == "" || dshBlobLLMMessage == "" || dshBlobLLMTypes == "" || dshBlobLLMAssistantStream == "" || dshBlobCoreSessionInvariant == "" || dshBlobCoreSessionSurface == "" {
		t.Fatal("missing DeepSeek corpus source pin")
	}
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--dsh-fixture-server" {
		dshServeFixture()
		return
	}
	os.Exit(m.Run())
}

const (
	dshServerModeLifecycle  = "lifecycle"
	dshServerModePreObserve = "pre-observe"
)

func dshServeFixture() {
	mode := os.Getenv("OAP_DSH_FIXTURE_MODE")
	logPath := os.Getenv("OAP_DSH_FIXTURE_LOG")
	script := os.Getenv("OAP_DSH_FIXTURE_SCRIPT")
	receipt := os.Getenv("OAP_DSH_FIXTURE_RECEIPT")
	promptError := os.Getenv("OAP_DSH_FIXTURE_PROMPT_ERROR")
	if logPath == "" || script == "" {
		os.Exit(2)
	}
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(2)
	}
	notifications := dshServerNotifications(script)
	stdout := bufio.NewWriter(os.Stdout)
	writeLine := func(line string) {
		_, _ = stdout.WriteString(line)
		_, _ = stdout.WriteString("\n")
		_ = stdout.Flush()
	}
	respond := func(id json.RawMessage, body string) {
		writeLine(fmt.Sprintf(`{"id":%s,"jsonrpc":"2.0",%s}`, string(id), body))
	}
	reader := bufio.NewReader(os.Stdin)
	servedPrompt := false
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		frame := bytes.TrimSuffix(line, []byte("\n"))
		if _, err := logFile.Write(append(append([]byte(nil), frame...), '\n')); err != nil {
			os.Exit(2)
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(frame, &request) != nil || len(request.ID) == 0 || request.Method == "" {
			return
		}
		switch request.Method {
		case native.MethodInitialize:
			if mode == dshServerModePreObserve {

				if len(notifications) > 0 {
					writeLine(notifications[0])
				}
				select {}
			}
			respond(request.ID, `"result":{"serverInfo":{"name":"`+native.ServerName+`","version":"`+native.ServerVersion+`"}}`)
		case native.MethodSessionPrompt:
			if mode == dshServerModeLifecycle && !servedPrompt && receipt != "" {
				servedPrompt = true
				for _, notification := range notifications {
					writeLine(notification)
				}
				respond(request.ID, `"result":{"messageId":"`+receipt+`"}`)
				continue
			}
			message := promptError
			if message == "" {
				message = "fixture prompt rejection"
			}
			respond(request.ID, fmt.Sprintf(`"error":{"code":-32603,"message":%q}`, message))
		case native.MethodShutdown:
			respond(request.ID, `"result":{}`)
			return
		default:
			return
		}
	}
}

func dshServerNotifications(script string) []string {
	data, err := os.ReadFile(script)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
		var frame struct {
			Direction string          `json:"direction"`
			Action    string          `json:"action"`
			Raw       json.RawMessage `json:"raw"`
		}
		if json.Unmarshal(line, &frame) != nil || frame.Direction != "dsh-to-host" || frame.Action != "auto" {
			continue
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(frame.Raw, &object) != nil {
			continue
		}
		if _, hasMethod := object["method"]; !hasMethod {
			continue
		}
		if _, hasID := object["id"]; hasID {
			continue
		}
		out = append(out, string(frame.Raw))
	}
	return out
}

package hermes

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

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/adapter/hermes/internal/native"
	"github.com/lsm/open-agent-protocol/adapter/hermes/internal/rpc"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/validation"
)

// The Hermes evidence corpus pins the exact repository, release, commit, tree,
// and source blobs frozen in research/hermes-v2026.8.31-mapping.md.
const (
	hmCorpusRepository = "https://github.com/NousResearch/hermes-agent"
	hmCorpusTag        = "v2026.8.31"
	hmCorpusCommit     = "29112bef099274229cadff79cdff7bf7b99c4b77"
	hmCorpusCommitTree = "daaffc303ae437041b7f76be17c5f61b14f2ce99"

	hmBlobEntry          = "27fd051b8aff7cb6e6ddd9103eb14c06d7b2c0e8"
	hmBlobTransport      = "ce93e518a3d5255f9729de80cadb4377747d0d6d"
	hmBlobServer         = "4e846e36e339172123248fafdac762f204604fd8"
	hmBlobEventReplay    = "0ec4e2a86b4a4fa75c51b25297cba74d6fd9198e"
	hmBlobStdinRecovery  = "80c77aeb0dcae31afa1d5c21d3abb7e2cc976962"
	hmBlobWS             = "988733a9b14e178289b446c11e6eb31f30a4b91b"
	hmBlobMethodsPrompt  = "3525ffcfdd4ed06a27c97092a28cf9a04d63efc8"
	hmBlobMethodsSession = "485456a87f61918c3c53891bb4c05cebddff0a40"
)

// Every label required by the ledger's "Required evidence corpus" section.
var hmLedgerFixtures = map[string]bool{
	// handshake
	"ready-epoch": true, "ready-before-input": true, "malformed-frame": true,
	// admission
	"submit-streaming": true, "busy-steered": true, "busy-queued": true, "submit-error-codes": true,
	// run lifecycle
	"turn-open": true, "completed-turn": true, "interrupted-turn": true,
	"error-turn": true, "error-surface": true, "partial-error": true,
	// streaming
	"text-deltas": true, "reasoning-deltas": true, "interim": true, "scrubbed-provenance": true,
	// tools
	"tool-lifecycle": true, "tool-failure-in-result": true,
	// interactions
	"approval-gate": true, "approval-choices": true, "clarify-gate": true,
	"sudo-gate": true, "secret-gate": true, "expire-sibling": true,
	// side channels
	"btw-delivery": true, "background-prompt": true,
	// steering
	"steer-run": true, "steer-rejected": true, "subagent-steer": true, "subagent-interrupt": true,
	// children
	"subagent-lifecycle": true, "subagent-complete-failed": true, "child-mirror-not-terminal": true,
	// replay
	"replay-in-window": true, "replay-truncated": true, "replay-unknown-session": true, "epoch-restart": true,
	// recovery
	"resume-live": true, "branch": true, "undo": true,
	// reconciliation
	"settled-session-info": true, "usage-ticker": true,
	// teardown/failure
	"stdin-eof-exit": true, "process-exit": true, "pre-ready-observation": true, "no-turn-lifecycle-events": true,
	// hygiene
	"global-events-unsequenced": true, "session-reclaimed": true,
}

type hmCorpusSources struct {
	EntryPy          string `json:"entry_py_blob"`
	TransportPy      string `json:"transport_py_blob"`
	ServerPy         string `json:"server_py_blob"`
	EventReplayPy    string `json:"event_replay_py_blob"`
	StdinRecoveryPy  string `json:"stdin_recovery_py_blob"`
	WsPy             string `json:"ws_py_blob"`
	MethodsPromptPy  string `json:"methods_prompt_py_blob"`
	MethodsSessionPy string `json:"methods_session_py_blob"`
}

type hmCorpusManifest struct {
	Version    int                    `json:"version"`
	Adapter    string                 `json:"adapter"`
	Tag        string                 `json:"tag"`
	Commit     string                 `json:"commit"`
	CommitTree string                 `json:"commit_tree"`
	Sources    hmCorpusSources        `json:"sources"`
	Cases      []hmCorpusManifestCase `json:"cases"`
}
type hmCorpusManifestCase struct {
	ID             string   `json:"id"`
	Path           string   `json:"path"`
	LedgerFixtures []string `json:"ledger_fixtures"`
}
type hmCorpusCase struct {
	Version      int                `json:"version"`
	ID           string             `json:"id"`
	Native       string             `json:"native"`
	ExpectedOAP  string             `json:"expected_oap"`
	Mapping      string             `json:"mapping"`
	Omissions    string             `json:"omissions"`
	Provenance   hmCorpusProvenance `json:"provenance"`
	Capabilities map[string]string  `json:"advertised_capabilities"`
	IdentityMap  map[string]string  `json:"identity_map"`
	ServerMode   string             `json:"server_mode,omitempty"`
	OpenError    bool               `json:"open_error,omitempty"`
}
type hmCorpusProvenance struct {
	Repository string          `json:"repository"`
	Tag        string          `json:"tag"`
	Commit     string          `json:"commit"`
	CommitTree string          `json:"commit_tree"`
	Sources    hmCorpusSources `json:"sources"`
}
type hmFrame struct {
	Direction      string          `json:"direction"`
	Classification string          `json:"classification"`
	Fidelity       string          `json:"fidelity"`
	Action         string          `json:"action"`
	Raw            json.RawMessage `json:"raw"`
}
type hmControl struct {
	Type   string `json:"type"`
	Op     string `json:"op,omitempty"`
	Error  string `json:"error,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Answer string `json:"answer,omitempty"`
	Status string `json:"status,omitempty"`
	Expect string `json:"expect,omitempty"`
}
type hmCorpusMapping struct {
	Index          int    `json:"index"`
	Type           string `json:"type"`
	Direction      string `json:"direction"`
	Classification string `json:"classification"`
	Fidelity       string `json:"fidelity"`
	OAP            string `json:"oap,omitempty"`
}
type hmCorpusOmission struct {
	Index  int    `json:"index"`
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

// hmDecodedFrame carries the production-codec result for one native frame.
type hmDecodedFrame struct {
	Message       *rpc.Message
	Notification  any // typed value from native.DecodeNotification
	Event         *native.Event
	Control       *hmControl
	Invalid       error // production rejection, for decode-error frames
	RequestMethod string
}

func pinnedHMSources() hmCorpusSources {
	return hmCorpusSources{
		EntryPy:          hmBlobEntry,
		TransportPy:      hmBlobTransport,
		ServerPy:         hmBlobServer,
		EventReplayPy:    hmBlobEventReplay,
		StdinRecoveryPy:  hmBlobStdinRecovery,
		WsPy:             hmBlobWS,
		MethodsPromptPy:  hmBlobMethodsPrompt,
		MethodsSessionPy: hmBlobMethodsSession,
	}
}

func TestHermesEvidenceCorpus(t *testing.T) {
	root := hmCorpusRoot(t)
	manifest := hmLoadJSON[hmCorpusManifest](t, filepath.Join(root, "manifest.json"))
	if manifest.Version != 1 || manifest.Adapter != "hermes-tui-gateway" || manifest.Tag != hmCorpusTag || manifest.Commit != hmCorpusCommit || manifest.CommitTree != hmCorpusCommitTree || manifest.Sources != pinnedHMSources() {
		t.Fatalf("corpus provenance pin mismatch: %+v", manifest)
	}
	seenIDs, seenPaths, covered := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, entry := range manifest.Cases {
		entry := entry
		t.Run(entry.ID, func(t *testing.T) {
			if entry.ID == "" || seenIDs[entry.ID] || seenPaths[entry.Path] || !hmSafeRelative(entry.Path) || len(entry.LedgerFixtures) == 0 {
				t.Fatalf("invalid manifest entry: %+v", entry)
			}
			for _, fixture := range entry.LedgerFixtures {
				if !hmLedgerFixtures[fixture] || covered[fixture] {
					t.Fatalf("invalid or duplicate ledger fixture %q", fixture)
				}
				covered[fixture] = true
			}
			seenIDs[entry.ID], seenPaths[entry.Path] = true, true
			runHermesCorpusCase(t, root, entry)
		})
	}
	for fixture := range hmLedgerFixtures {
		if !covered[fixture] {
			t.Errorf("ledger fixture %q has no case", fixture)
		}
	}
	assertHermesCorpusInventory(t, root, manifest)
}

func runHermesCorpusCase(t *testing.T, root string, entry hmCorpusManifestCase) {
	dir := filepath.Join(root, entry.Path)
	definition := hmLoadJSON[hmCorpusCase](t, filepath.Join(dir, "case.json"))
	p := definition.Provenance
	if definition.Version != 1 || definition.ID != entry.ID || p.Repository != hmCorpusRepository || p.Tag != hmCorpusTag || p.Commit != hmCorpusCommit || p.CommitTree != hmCorpusCommitTree || p.Sources != pinnedHMSources() || len(definition.Capabilities) == 0 || len(definition.IdentityMap) == 0 {
		t.Fatalf("invalid case metadata: %+v", definition)
	}
	frames, decoded := hmLoadFrames(t, filepath.Join(dir, definition.Native))
	mappings := hmLoadJSON[[]hmCorpusMapping](t, filepath.Join(dir, definition.Mapping))
	omissions := hmLoadJSON[[]hmCorpusOmission](t, filepath.Join(dir, definition.Omissions))
	assertHermesClassifications(t, frames, decoded, mappings, omissions)
	assertHermesCaseActions(t, definition, frames)
	var execution hmExecution
	if definition.ServerMode != "" {
		execution = runHermesProcessCase(t, dir, definition, frames, decoded)
	} else {
		execution = runHermesFakeCase(t, definition, frames, decoded)
	}
	assertHermesLedgerEvidence(t, entry.LedgerFixtures, frames, decoded, &execution)
	assertHermesExpected(t, filepath.Join(dir, definition.ExpectedOAP), execution.envelopes)
}

// runHermesFakeCase drives the production reducer through the public adapter
// with a deterministic in-process client, replaying fixture frames in wire
// order. Every observation is barrier-acknowledged so the execution is
// deterministic without sleeps.
func runHermesFakeCase(t *testing.T, definition hmCorpusCase, frames []hmFrame, decoded []hmDecodedFrame) hmExecution {
	t.Helper()
	execution := hmExecution{nativeWrites: map[string]int{}}
	client := newHMCorpusClient()
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, string, error) { return client, "sess0001", nil }), Model: "hermes-test", Clock: &testClock{}, IDs: &testIDs{}, JournalCapacity: 64})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := implementation.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.AssertDescriptor(t, descriptor)
	execution.descriptor = descriptor
	hmAssertCapabilities(t, definition, descriptor)
	session := adaptertest.AssertInitialState(t, implementation, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})

	type hmSubmit struct {
		admission protocol.MessageSubmitResponse
		stream    base.EventStream
		err       error
	}
	var pending chan hmSubmit
	var settled []hmSubmit
	var pendingResolve chan error
	// waitReap waits for the in-flight submission to return WITHOUT draining
	// its stream: draining waits for run terminality, which would deadlock a
	// mid-run wait-submit barrier because later frames are yet to be delivered.
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
	joinResolve := func() {
		if pendingResolve == nil {
			return
		}
		select {
		case err := <-pendingResolve:
			if err != nil {
				t.Fatalf("resolve failed: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("resolve did not settle")
		}
		pendingResolve = nil
	}
	for i, frame := range frames {
		switch frame.Action {
		case "submit":
			waitReap()
			channel := make(chan hmSubmit, 1)
			pending = channel
			go func() {
				admission, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
				channel <- hmSubmit{admission, stream, err}
			}()
			client.awaitCall(t, native.MethodPromptSubmit)
			var want native.PromptSubmitParams
			hmDecodeStrict(t, decoded[i].Message.Params, &want, fmt.Sprintf("frame %d params", i+1))
			got, _ := json.Marshal(client.lastCall(t).params)
			expected, _ := json.Marshal(want)
			if !bytes.Equal(got, expected) {
				t.Fatalf("frame %d: adapter wrote params %s, want %s", i+1, got, expected)
			}
		case "reply":
			client.replies <- hmReply{result: decoded[i].Message.Result}
			joinResolve()
		case "reply-error":
			if decoded[i].Message.Error == nil {
				t.Fatalf("frame %d: expected JSON-RPC error response", i+1)
			}
			client.replies <- hmReply{err: fmt.Errorf("fixture rejection: %s", decoded[i].Message.Error.Message)}
		case "observe":
			client.deliver(t, decoded[i])
		case "expect-write":
			message := decoded[i].Message
			call := client.lastCall(t)
			if call.method != message.Method || !hmJSONEqual(call.params, message.Params) {
				t.Fatalf("frame %d: adapter wrote %s %s, want %s %s", i+1, call.method, call.params, message.Method, message.Params)
			}
		case "oap-control":
			control := decoded[i].Control
			switch control.Op {
			case "resolve":
				binding := hmFindInteraction(t, session, control.Kind)
				if binding == nil {
					t.Fatalf("frame %d: no open %s gate", i+1, control.Kind)
				}
				channel := make(chan error, 1)
				pendingResolve = channel
				go func() { channel <- session.Resolve(context.Background(), hmResolutionFor(binding, *control)) }()
				client.awaitCall(t, hmRespondMethod(control.Kind))
			case "assert-state":
				state, err := session.State(context.Background())
				if err != nil {
					t.Fatalf("frame %d: state error = %v", i+1, err)
				}
				if string(state.Status) != control.Status {
					t.Fatalf("frame %d: session status = %q, want %q", i+1, state.Status, control.Status)
				}
				execution.assertStates = append(execution.assertStates, control.Status)
			case "overlap-submit":
				calls := client.totalCalls()
				_, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello again")}}})
				if !errors.Is(err, base.ErrRunActive) {
					t.Fatalf("frame %d: overlapping submit error = %v, want %v", i+1, err, base.ErrRunActive)
				}
				if stream != nil {
					t.Fatalf("frame %d: rejected overlap exposed an event stream", i+1)
				}
				if client.totalCalls() != calls {
					t.Fatalf("frame %d: rejected overlap wrote a native request", i+1)
				}
				execution.overlapRejected = true
			case "submit-closed":
				calls := client.totalCalls()
				if _, _, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello again")}}}); !errors.Is(err, base.ErrSessionClosed) {
					t.Fatalf("frame %d: unusable-session submit error = %v, want %v", i+1, err, base.ErrSessionClosed)
				}
				if client.totalCalls() != calls {
					t.Fatalf("frame %d: unusable-session submit wrote a native request", i+1)
				}
				execution.submitClosed = true
			case "resume":
				calls := client.totalCalls()
				if _, _, err := session.Resume(context.Background(), base.ResumeRequest{RunID: "run"}); !errors.Is(err, errUnavailable) {
					t.Fatalf("frame %d: resume error = %v, want %v", i+1, err, errUnavailable)
				}
				if client.totalCalls() != calls {
					t.Fatalf("frame %d: resume produced a native request", i+1)
				}
				execution.resumeUnavailable++
			case "close":
				waitReap()
				if err := session.Close(context.Background()); err != nil {
					t.Fatalf("frame %d: close: %v", i+1, err)
				}
				execution.closed = true
				execution.closeErr = nil
			default:
				t.Fatalf("frame %d: unsupported oap control %q", i+1, control.Op)
			}
		case "process-exit":
			client.transportClose(errors.New(decoded[i].Control.Error))
			// Deterministic barrier: the dispatch goroutine settles the started
			// run through the failure path before Close can observe idle.
			deadline := time.Now().Add(5 * time.Second)
			for {
				state, stateErr := session.State(context.Background())
				if stateErr != nil || state.Status != protocol.SessionRunning {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("transport failure did not settle the run")
				}
				time.Sleep(time.Millisecond)
			}
		case "wait-submit":
			waitReap()
		default:
			t.Fatalf("frame %d: unsupported action %q for the in-process harness", i+1, frame.Action)
		}
	}
	waitReap()
	joinResolve()
	for _, result := range settled {
		execution.record(t, result.admission, result.stream, result.err)
	}
	for _, method := range client.methods() {
		if !hmAllowedMethod(method) {
			t.Fatalf("adapter wrote unsupported native method %q", method)
		}
		execution.nativeWrites[method]++
	}
	execution.wirePrompts = client.callCount(native.MethodPromptSubmit)
	return execution
}

// runHermesProcessCase exercises the production process transport: the adapter
// spawns a re-exec of the test binary as a pinned-protocol gateway fixture, and
// the case transcript records both wire directions.
func runHermesProcessCase(t *testing.T, dir string, definition hmCorpusCase, frames []hmFrame, decoded []hmDecodedFrame) hmExecution {
	t.Helper()
	execution := hmExecution{nativeWrites: map[string]int{}}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	logPath := filepath.Join(workspace, "wire.log")
	environment := []string{
		"OAP_HM_FIXTURE_MODE=" + definition.ServerMode,
		"OAP_HM_FIXTURE_LOG=" + logPath,
		"OAP_HM_FIXTURE_SCRIPT=" + filepath.Join(dir, definition.Native),
	}
	implementation, err := New(Config{Executable: self, Args: []string{"--hermes-fixture-server"}, Environment: environment, WorkingDirectory: workspace, Model: "hermes-test", Clock: &testClock{}, IDs: &testIDs{}, JournalCapacity: 64})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := implementation.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.AssertDescriptor(t, descriptor)
	execution.descriptor = descriptor
	hmAssertCapabilities(t, definition, descriptor)

	type hmSubmit struct {
		admission protocol.MessageSubmitResponse
		stream    base.EventStream
		err       error
	}
	var session base.Session
	var pending chan hmSubmit
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
			channel := make(chan hmSubmit, 1)
			pending = channel
			go func() {
				admission, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
				channel <- hmSubmit{admission, stream, err}
			}()
		case "oap-control":
			if decoded[i].Control.Op != "close" {
				t.Fatalf("frame %d: control %q is not valid for the process harness", i+1, decoded[i].Control.Op)
			}
			waitPending()
			if session != nil {
				if err := session.Close(context.Background()); err != nil {
					t.Fatalf("frame %d: close: %v", i+1, err)
				}
			}
			execution.closed = true
			session = nil
		case "auto", "decode-error":
			// Emitted by the fixture gateway and already validated through the
			// production codec at load time.
		default:
			t.Fatalf("frame %d: action %q is not valid for the process harness", i+1, frame.Action)
		}
	}
	waitPending()
	if session != nil {
		t.Fatal("process case ended without a close frame")
	}

	// The gateway's wire log must be exactly the host transcript, and every
	// host frame must be one of the adapter's supported methods: teardown is
	// stdin EOF, so a shutdown RPC on the wire would contradict the pin.
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logLines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(logLines) == 1 && logLines[0] == "" {
		logLines = nil
	}
	var expected []string
	for i, frame := range frames {
		if frame.Direction != "host-to-gateway" || len(frame.Raw) == 0 || bytes.Equal(frame.Raw, []byte("null")) {
			continue
		}
		expected = append(expected, string(hmWireBytes(t, frame.Raw, filepath.Join(dir, definition.Native), i+1)))
	}
	if len(logLines) != len(expected) {
		t.Fatalf("host wire log has %d lines, want %d: %q", len(logLines), len(expected), logLines)
	}
	for i := range expected {
		if logLines[i] != expected[i] {
			t.Fatalf("host wire line %d = %s, want %s", i+1, logLines[i], expected[i])
		}
	}
	execution.logLines = logLines
	for _, line := range logLines {
		var wire struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal([]byte(line), &wire); err != nil {
			t.Fatalf("wire line is not JSON: %q", line)
		}
		if !hmAllowedMethod(wire.Method) {
			t.Fatalf("adapter wrote unsupported native method %q", wire.Method)
		}
		execution.nativeWrites[wire.Method]++
		if wire.Method == native.MethodPromptSubmit {
			execution.wirePrompts++
		}
	}
	return execution
}

// hmExecution records what the behavioral run actually proved.
type hmExecution struct {
	descriptor        base.Descriptor
	admissions        []protocol.MessageSubmitResponse
	submitErrors      []error
	envelopes         []protocol.Envelope
	runs              [][]protocol.Envelope
	wirePrompts       int
	logLines          []string
	nativeWrites      map[string]int
	openErr           error
	closed            bool
	closeErr          error
	overlapRejected   bool
	submitClosed      bool
	resumeUnavailable int
	assertStates      []string
}

func (e *hmExecution) record(t *testing.T, admission protocol.MessageSubmitResponse, stream base.EventStream, err error) {
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
		adaptertest.AssertRunEvents(t, admission, CapabilityRevision, events)
		e.validate(t, admission, events)
		e.runs = append(e.runs, events)
	} else {
		e.runs = append(e.runs, nil)
	}
	e.envelopes = append(e.envelopes, events...)
}

// validate runs the executable OAP schema and state machine over one admitted
// run. A capability descriptor pair from the probed adapter precedes the submit
// exchange so optional features (tools, interactions) carry current
// capabilities.
func (e *hmExecution) validate(t *testing.T, admission protocol.MessageSubmitResponse, events []protocol.Envelope) {
	t.Helper()
	capReq, _ := protocol.NewEnvelope(protocol.TypeCapabilitiesRequest, "capabilities-request", protocol.CapabilitiesRequest{})
	capRes, _ := protocol.NewEnvelope(protocol.TypeCapabilitiesResponse, "capabilities-response", e.descriptor.Capabilities)
	capRes.InReplyTo, capRes.CapabilityRevision = capReq.ID, e.descriptor.CapabilityRevision
	submitReq, _ := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitRequest, "submit-request", protocol.MessageSubmitRequest{SessionID: admission.SessionID, Delivery: admission.RequestedDelivery, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
	submitReq.SessionID = admission.SessionID
	submitRes, _ := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitResponse, "submit-response", admission)
	submitRes.SessionID, submitRes.InReplyTo = admission.SessionID, submitReq.ID
	trace := append([]protocol.Envelope{capReq, capRes, submitReq, submitRes}, events...)
	data, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	if result := validation.MustNew().ValidateBytes(data, "hermes-corpus"); !result.Valid() {
		t.Fatalf("adapter trace failed OAP validation: %v\ntrace: %s", result.Diagnostics, data)
	}
}

// hmCorpusClient is the deterministic in-process Hermes client used by the
// corpus. It records every native call, answers through an explicit reply
// queue in call order, and rejects any method outside the adapter's supported
// surface — so a steer, replay, or recovery write fails the case outright.
type hmCorpusClient struct {
	in      chan rpc.InboundMessage
	done    chan struct{}
	replies chan hmReply
	started chan string
	mu      sync.Mutex
	closed  bool
	dead    bool
	err     error
	calls   []hmRecordedCall
}

type hmReply struct {
	result json.RawMessage
	err    error
}

type hmRecordedCall struct {
	method string
	params json.RawMessage
}

func newHMCorpusClient() *hmCorpusClient {
	return &hmCorpusClient{in: make(chan rpc.InboundMessage, 64), done: make(chan struct{}), replies: make(chan hmReply, 8), started: make(chan string, 8)}
}

func hmAllowedMethod(method string) bool {
	switch method {
	case native.MethodSessionCreate, native.MethodPromptSubmit, native.MethodApprovalRespond, native.MethodClarifyRespond,
		native.MethodSudoRespond, native.MethodSecretRespond, native.MethodSessionInterrupt:
		return true
	}
	return false
}

func hmRespondMethod(kind string) string {
	switch kind {
	case "approval":
		return native.MethodApprovalRespond
	case "clarify":
		return native.MethodClarifyRespond
	case "sudo":
		return native.MethodSudoRespond
	case "secret":
		return native.MethodSecretRespond
	}
	return ""
}

func (c *hmCorpusClient) Call(_ context.Context, method string, params any, result any) error {
	if !hmAllowedMethod(method) {
		return fmt.Errorf("fixture client: unexpected native call %q", method)
	}
	data, _ := json.Marshal(params)
	c.mu.Lock()
	c.calls = append(c.calls, hmRecordedCall{method, data})
	c.mu.Unlock()
	c.started <- method
	reply := <-c.replies
	if reply.err != nil {
		return reply.err
	}
	if result != nil && len(reply.result) > 0 {
		return json.Unmarshal(reply.result, result)
	}
	return nil
}
func (c *hmCorpusClient) Inbound() <-chan rpc.InboundMessage { return c.in }
func (c *hmCorpusClient) Done() <-chan struct{}              { return c.done }
func (c *hmCorpusClient) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	return errors.New("EOF")
}
func (c *hmCorpusClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		close(c.done)
		c.closed = true
	}
	return nil
}

func (c *hmCorpusClient) totalCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}
func (c *hmCorpusClient) callCount(method string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, call := range c.calls {
		if call.method == method {
			count++
		}
	}
	return count
}
func (c *hmCorpusClient) methods() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, call := range c.calls {
		out = append(out, call.method)
	}
	return out
}
func (c *hmCorpusClient) lastCall(t *testing.T) hmRecordedCall {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.calls) == 0 {
		t.Fatal("no native call was recorded")
	}
	return c.calls[len(c.calls)-1]
}

// awaitCall blocks until the adapter issued the given native call.
func (c *hmCorpusClient) awaitCall(t *testing.T, method string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case got := <-c.started:
			if got == method {
				return
			}
		case <-deadline:
			t.Fatalf("native call %q was not issued", method)
		}
	}
}

func (c *hmCorpusClient) deliver(t *testing.T, decoded hmDecodedFrame) {
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
func (c *hmCorpusClient) barrier(t *testing.T) {
	t.Helper()
	ack := make(chan struct{})
	c.in <- rpc.InboundMessage{Barrier: ack}
	select {
	case <-ack:
	case <-time.After(5 * time.Second):
		t.Fatal("reducer barrier timed out")
	}
}
func (c *hmCorpusClient) transportClose(err error) {
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
	// The reducer's dispatch drains the inbound stream to this close before
	// settling the failure, mirroring the production relay's close.
	close(c.in)
}

// hmFindInteraction locates the open gate of the given kind on the reducer's
// interaction table.
func hmFindInteraction(t *testing.T, session base.Session, kind string) *inputState {
	t.Helper()
	impl, ok := session.(*Session)
	if !ok {
		t.Fatal("session is not the Hermes reducer")
	}
	impl.mu.Lock()
	defer impl.mu.Unlock()
	var found *inputState
	for _, binding := range impl.interactions {
		if binding.kind == kind && !binding.resolved {
			found = binding
		}
	}
	return found
}

func hmResolutionFor(binding *inputState, control hmControl) base.InteractionResolution {
	question := binding.questions[0]
	answer := protocol.InputAnswer{QuestionID: question.ID}
	if binding.kind == "approval" || binding.kind == "clarify" {
		answer.SelectedOptionIDs = []string{control.Answer}
	} else {
		answer.Text = control.Answer
	}
	return base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: binding.id, SessionID: "session", Answers: []protocol.InputAnswer{answer}}}
}

func hmAssertCapabilities(t *testing.T, definition hmCorpusCase, descriptor base.Descriptor) {
	t.Helper()
	for key, level := range definition.Capabilities {
		feature, ok := descriptor.Capabilities.Features[key]
		if !ok {
			t.Fatalf("case advertises unknown capability %q", key)
		}
		if string(feature.Level) != level {
			t.Fatalf("capability %q = %q, case claims %q", key, feature.Level, level)
		}
	}
}

func hmLoadFrames(t *testing.T, filename string) ([]hmFrame, []hmDecodedFrame) {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if len(lines) == 0 || len(lines[0]) == 0 {
		t.Fatal("empty native transcript")
	}
	frames := make([]hmFrame, 0, len(lines))
	decoded := make([]hmDecodedFrame, 0, len(lines))
	methodByID := map[string]string{}
	for i, line := range lines {
		var frame hmFrame
		hmDecodeStrict(t, line, &frame, fmt.Sprintf("%s frame %d", filename, i+1))
		entry := hmDecodedFrame{}
		switch frame.Direction {
		case "gateway-to-host":
			wire := hmWireBytes(t, frame.Raw, filename, i+1)
			message, decodeErr := rpc.NewDecoder(bytes.NewReader(append(append([]byte(nil), wire...), '\n')), rpc.DefaultFrameLimit).Decode()
			if decodeErr == nil && message.Kind == rpc.MessageNotification {
				value, nativeErr := native.DecodeNotification(message.Method, message.Params)
				if nativeErr != nil {
					decodeErr = nativeErr
				} else {
					entry.Notification = value
					if event, ok := value.(*native.Event); ok {
						entry.Event = event
					}
				}
			}
			switch frame.Action {
			case "decode-error":
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
		case "host-to-gateway":
			// A null raw marks an "open" frame whose request is never written
			// (the handshake fails first): trigger only, no wire expectation.
			if len(frame.Raw) == 0 || bytes.Equal(frame.Raw, []byte("null")) {
				if frame.Action != "open" {
					t.Fatalf("frame %d: host frame without raw bytes", i+1)
				}
				break
			}
			wire := hmWireBytes(t, frame.Raw, filename, i+1)
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
			methodByID[string(id)] = message.Method
			entry.Message = &message
		case "harness-control":
			var control hmControl
			hmDecodeStrict(t, frame.Raw, &control, fmt.Sprintf("%s frame %d", filename, i+1))
			switch control.Type {
			case "process_exit":
				if control.Error == "" {
					t.Fatalf("frame %d invalid process-exit control", i+1)
				}
			case "oap_control":
				switch control.Op {
				case "resolve":
					if hmRespondMethod(control.Kind) == "" || control.Answer == "" {
						t.Fatalf("frame %d invalid resolve control", i+1)
					}
				case "assert-state":
					if control.Status != "idle" && control.Status != "running" {
						t.Fatalf("frame %d invalid assert-state status", i+1)
					}
				case "overlap-submit":
					if control.Expect != "run-active" {
						t.Fatalf("frame %d invalid overlap-submit expectation", i+1)
					}
				case "submit-closed":
					if control.Expect != "session-closed" {
						t.Fatalf("frame %d invalid submit-closed expectation", i+1)
					}
				case "resume":
					if control.Expect != "unavailable" {
						t.Fatalf("frame %d invalid resume expectation", i+1)
					}
				case "close":
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
	// Response frames are typed by the method of the request they answer.
	for i := range decoded {
		if frames[i].Direction != "gateway-to-host" || decoded[i].Message == nil || decoded[i].Notification != nil {
			continue
		}
		if decoded[i].Message.Kind == rpc.MessageResponse || decoded[i].Message.Kind == rpc.MessageError {
			id, _ := decoded[i].Message.ID.MarshalJSON()
			method, ok := methodByID[string(id)]
			if !ok {
				t.Fatalf("frame %d: response answers unknown request id %s", i+1, id)
			}
			decoded[i].RequestMethod = method
		}
	}
	return frames, decoded
}

// hmWireBytes returns the exact native wire bytes for a fixture raw value.
// String raws carry byte-exact frames (leading whitespace, corrupt tails);
// object raws are used verbatim.
func hmWireBytes(t *testing.T, raw json.RawMessage, filename string, index int) []byte {
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

func assertHermesCaseActions(t *testing.T, definition hmCorpusCase, frames []hmFrame) {
	t.Helper()
	for i, frame := range frames {
		var valid map[string]bool
		switch frame.Direction {
		case "gateway-to-host":
			valid = map[string]bool{"auto": true, "reply": true, "reply-error": true, "observe": true, "decode-error": true}
		case "host-to-gateway":
			valid = map[string]bool{"submit": true, "open": true, "expect-write": true}
		case "harness-control":
			valid = map[string]bool{"process-exit": true, "oap-control": true, "wait-submit": true}
		}
		if valid == nil || !valid[frame.Action] {
			t.Fatalf("frame %d has unknown action %q for %q", i+1, frame.Action, frame.Direction)
		}
	}
	if definition.ServerMode != "" {
		for i, frame := range frames {
			switch frame.Action {
			case "open", "submit", "auto", "decode-error", "oap-control":
			default:
				t.Fatalf("frame %d action %q is not valid for the process harness", i+1, frame.Action)
			}
		}
	} else {
		for i, frame := range frames {
			switch frame.Action {
			case "submit", "reply", "reply-error", "observe", "expect-write", "oap-control", "process-exit", "wait-submit":
			default:
				t.Fatalf("frame %d action %q requires the process harness", i+1, frame.Action)
			}
		}
	}
	if definition.OpenError && definition.ServerMode == "" {
		t.Fatal("open_error is only meaningful for a process case")
	}
}

func hmFrameType(frame hmFrame, decoded hmDecodedFrame) string {
	switch frame.Direction {
	case "harness-control":
		if decoded.Control.Type == "process_exit" {
			return "process_exit"
		}
		if decoded.Control.Type == "harness_sync" {
			return "sync:" + decoded.Control.Op
		}
		return "oap_control:" + decoded.Control.Op
	case "host-to-gateway":
		if decoded.Message != nil {
			return "request:" + decoded.Message.Method
		}
		return "open"
	case "gateway-to-host":
		if decoded.Invalid != nil {
			return "codec-error"
		}
		if decoded.Event != nil {
			return "event/" + decoded.Event.Type
		}
		if decoded.Message != nil && (decoded.Message.Kind == rpc.MessageResponse || decoded.Message.Kind == rpc.MessageError) {
			return "response:" + decoded.RequestMethod
		}
	}
	return "codec-error"
}

func assertHermesClassifications(t *testing.T, frames []hmFrame, decoded []hmDecodedFrame, mappings []hmCorpusMapping, omissions []hmCorpusOmission) {
	t.Helper()
	if len(frames) != len(mappings) {
		t.Fatalf("mapping count %d does not cover %d frames", len(mappings), len(frames))
	}
	omitted := map[int]hmCorpusOmission{}
	for _, o := range omissions {
		if o.Index < 1 || o.Index > len(frames) || o.Reason == "" || omitted[o.Index].Index != 0 {
			t.Fatalf("invalid omission: %+v", o)
		}
		omitted[o.Index] = o
	}
	for i, f := range frames {
		typ := hmFrameType(f, decoded[i])
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

// ---- ledger evidence rules -------------------------------------------------

func hmEventIndexes(decoded []hmDecodedFrame, typ string) []int {
	var out []int
	for i := range decoded {
		if decoded[i].Event != nil && decoded[i].Event.Type == typ {
			out = append(out, i)
		}
	}
	return out
}

func hmHasEvent(decoded []hmDecodedFrame, typ string) bool {
	return len(hmEventIndexes(decoded, typ)) > 0
}

func hmEnvelopeCount(execution hmExecution, typ protocol.EnvelopeType) int {
	count := 0
	for _, envelope := range execution.envelopes {
		if envelope.Type == typ {
			count++
		}
	}
	return count
}

func hmLastEnvelope(t *testing.T, execution hmExecution) protocol.Envelope {
	t.Helper()
	if len(execution.envelopes) == 0 {
		t.Fatal("case produced no OAP envelopes")
	}
	return execution.envelopes[len(execution.envelopes)-1]
}

func hmFailedCode(t *testing.T, envelope protocol.Envelope) (string, bool) {
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

func hmRunTerminal(events []protocol.Envelope) (protocol.EnvelopeType, string) {
	for _, envelope := range events {
		switch envelope.Type {
		case protocol.TypeRunCompleted, protocol.TypeRunFailed:
			code := ""
			if envelope.Type == protocol.TypeRunFailed {
				var payload protocol.RunFailedPayload
				if envelope.DecodePayload(&payload) == nil {
					code = payload.Error.Code
				}
			}
			return envelope.Type, code
		}
	}
	return "", ""
}

// hmSettlements decodes every message.complete payload in the transcript.
func hmSettlements(t *testing.T, decoded []hmDecodedFrame) []native.MessageCompletePayload {
	t.Helper()
	var out []native.MessageCompletePayload
	for _, index := range hmEventIndexes(decoded, native.EventMessageComplete) {
		var payload native.MessageCompletePayload
		if err := native.DecodeStrict(decoded[index].Event.Payload, &payload); err != nil {
			t.Fatalf("settlement decode: %v", err)
		}
		out = append(out, payload)
	}
	return out
}

func hmHasSettlement(settlements []native.MessageCompletePayload, status string) bool {
	for _, s := range settlements {
		if s.Status == status {
			return true
		}
	}
	return false
}

func hmDescriptorLevel(execution hmExecution, feature string) (protocol.SupportLevel, bool) {
	support, ok := execution.descriptor.Capabilities.Features[feature]
	if !ok {
		return "", false
	}
	return support.Level, true
}

func hmWrote(execution hmExecution, method string) bool { return execution.nativeWrites[method] > 0 }

// hmReadyPayload extracts the epoch-bearing handshake payload from a frame.
func hmReadyPayload(t *testing.T, decoded []hmDecodedFrame) (native.ReadyPayload, bool) {
	t.Helper()
	for i := range decoded {
		if decoded[i].Event == nil || decoded[i].Event.Type != native.EventGatewayReady {
			continue
		}
		var payload native.ReadyPayload
		if err := native.DecodeStrict(decoded[i].Event.Payload, &payload); err != nil {
			t.Fatalf("gateway.ready decode: %v", err)
		}
		return payload, true
	}
	return native.ReadyPayload{}, false
}

// assertHermesLedgerEvidence requires each ledger label to have executable
// evidence in the fixture transcript and the behavioral execution record.
func assertHermesLedgerEvidence(t *testing.T, labels []string, frames []hmFrame, decoded []hmDecodedFrame, execution *hmExecution) {
	t.Helper()
	settlements := hmSettlements(t, decoded)
	for _, label := range labels {
		ok := true
		switch label {
		case "ready-epoch":
			ready, hasReady := hmReadyPayload(t, decoded)
			ok = hasReady && native.ValidateReady(&ready) == nil && len(ready.ReplayEpoch) == 32 &&
				execution.openErr == nil && execution.descriptor.Capabilities.ProtocolVersions[0] == protocol.Version
		case "ready-before-input":
			readyIndex, hostIndex := -1, -1
			for i := range decoded {
				if readyIndex < 0 && decoded[i].Event != nil && decoded[i].Event.Type == native.EventGatewayReady {
					readyIndex = i
				}
				if hostIndex < 0 && frames[i].Direction == "host-to-gateway" && len(frames[i].Raw) > 0 {
					hostIndex = i
				}
			}
			ok = readyIndex >= 0 && readyIndex < hostIndex && execution.openErr == nil
		case "stdin-eof-exit":
			teardownRPC := false
			for _, line := range execution.logLines {
				if strings.Contains(line, `"method":"session.close"`) || strings.Contains(line, "shutdown") {
					teardownRPC = true
				}
			}
			ok = execution.closed && execution.closeErr == nil && !teardownRPC && len(execution.logLines) > 0
		case "malformed-frame":
			rejected := false
			for i := range decoded {
				if frames[i].Action == "decode-error" && decoded[i].Invalid != nil {
					rejected = true
				}
			}
			terminal := hmLastEnvelope(t, *execution)
			code, failed := hmFailedCode(t, terminal)
			var payload protocol.RunFailedPayload
			live := failed && terminal.DecodePayload(&payload) == nil &&
				strings.Contains(payload.Error.Message, "invalid JSON-RPC message")
			// The corrupt bytes must have traversed the production decoder on
			// the live transport: the surfaced cause is the reader's codec
			// error, not a coincidental process exit.
			ok = rejected && failed && code == "hermes_process_exit" && live
		case "submit-streaming":
			streaming := 0
			for i := range decoded {
				if decoded[i].Message == nil || decoded[i].RequestMethod != native.MethodPromptSubmit || decoded[i].Message.Kind != rpc.MessageResponse {
					continue
				}
				var result native.PromptSubmitResult
				if native.DecodeStrict(decoded[i].Message.Result, &result) == nil && result.Status == native.SubmitStreaming {
					streaming++
				}
			}
			ok = streaming > 0 && len(execution.admissions) == streaming
		case "turn-open":
			responseFirst, openFirst := false, false
			for i := 0; i+1 < len(frames); i++ {
				isStart := decoded[i].Event != nil && decoded[i].Event.Type == native.EventMessageStart
				isReply := frames[i+1].Action == "reply"
				prevReply := frames[i].Action == "reply"
				nextStart := i+1 < len(decoded) && decoded[i+1].Event != nil && decoded[i+1].Event.Type == native.EventMessageStart
				if isStart && isReply {
					openFirst = true
				}
				if prevReply && nextStart {
					responseFirst = true
				}
			}
			started := len(execution.admissions) > 0
			for _, events := range execution.runs {
				started = started && len(events) > 0 && events[0].Type == protocol.TypeRunStarted
			}
			ok = responseFirst && openFirst && started
		case "busy-steered", "busy-queued":
			want := native.SubmitSteered
			if label == "busy-queued" {
				want = native.SubmitQueued
			}
			saw := false
			for i := range decoded {
				if decoded[i].Message == nil || decoded[i].RequestMethod != native.MethodPromptSubmit {
					continue
				}
				var result native.PromptSubmitResult
				if native.DecodeStrict(decoded[i].Message.Result, &result) == nil && result.Status == want {
					saw = true
				}
			}
			ok = saw && len(execution.admissions) == 0 && len(execution.submitErrors) > 0 && !hmWrote(*execution, native.MethodSessionSteer)
		case "submit-error-codes":
			codeSeen := false
			for i := range decoded {
				if frames[i].Action == "reply-error" && decoded[i].Message != nil && decoded[i].Message.Error != nil && decoded[i].Message.Error.Code != 0 {
					codeSeen = true
				}
			}
			ok = codeSeen && len(execution.admissions) == 0 && len(execution.submitErrors) > 0
		case "completed-turn":
			ok = false
			for _, events := range execution.runs {
				if len(events) == 0 {
					continue
				}
				last := events[len(events)-1]
				if last.Type != protocol.TypeRunCompleted {
					continue
				}
				var payload protocol.RunCompletedPayload
				if last.DecodePayload(&payload) == nil && payload.Usage != nil && payload.Usage.InputTokens == 3 {
					ok = true
				}
			}
			ok = ok && hmHasSettlement(settlements, "complete")
		case "interrupted-turn":
			code := false
			for _, events := range execution.runs {
				if _, c := hmRunTerminal(events); c == "hermes_interrupted" {
					code = true
				}
			}
			ok = hmHasSettlement(settlements, "interrupted") && code
		case "error-turn":
			seen := false
			for _, s := range settlements {
				if s.Status == "error" && s.Error != "" {
					seen = true
				}
			}
			code := false
			for _, events := range execution.runs {
				if _, c := hmRunTerminal(events); c == "hermes_error" {
					code = true
				}
			}
			ok = seen && code
		case "error-surface":
			surface := false
			for _, s := range settlements {
				if s.ErrorSurface != nil && s.ErrorSurface.Code == "rate_limited" {
					surface = true
				}
			}
			code := false
			for _, events := range execution.runs {
				if _, c := hmRunTerminal(events); c == "hermes_rate_limited" {
					code = true
				}
			}
			ok = surface && code
		case "partial-error":
			partial := false
			for _, s := range settlements {
				if s.Partial && s.Status == "error" {
					partial = true
				}
			}
			var lastTyp protocol.EnvelopeType
			for _, events := range execution.runs {
				if len(events) == 0 {
					continue
				}
				if typ, _ := hmRunTerminal(events); typ != "" {
					lastTyp = typ
				}
			}
			ok = partial && lastTyp == protocol.TypeRunFailed
		case "text-deltas":
			textDeltas := len(hmEventIndexes(decoded, native.EventMessageDelta))
			projected := 0
			for _, envelope := range execution.envelopes {
				if envelope.Type != protocol.TypeContentDelta {
					continue
				}
				var payload protocol.ContentDeltaPayload
				if envelope.DecodePayload(&payload) == nil && payload.Part.Type == protocol.ContentText {
					projected++
				}
			}
			normalized := true
			for _, i := range hmEventIndexes(decoded, native.EventMessageDelta) {
				normalized = normalized && frames[i].Fidelity == "normalized"
			}
			ok = textDeltas >= 2 && projected == textDeltas && normalized
		case "reasoning-deltas":
			reasoning := len(hmEventIndexes(decoded, native.EventReasoningDelta)) + len(hmEventIndexes(decoded, native.EventThinkingDelta))
			projected := 0
			for _, envelope := range execution.envelopes {
				if envelope.Type != protocol.TypeContentDelta {
					continue
				}
				var payload protocol.ContentDeltaPayload
				if envelope.DecodePayload(&payload) == nil && payload.Part.Type == protocol.ContentReasoning {
					projected++
				}
			}
			ok = reasoning >= 2 && projected == reasoning
		case "interim":
			interims := hmEventIndexes(decoded, native.EventMessageInterim)
			leaked := false
			for _, envelope := range execution.envelopes {
				data, _ := json.Marshal(envelope.Payload)
				if strings.Contains(string(data), "note") {
					leaked = true
				}
			}
			ok = len(interims) > 0 && !leaked
		case "scrubbed-provenance":
			streamed := false
			for _, i := range hmEventIndexes(decoded, native.EventMessageInterim) {
				var payload native.InterimPayload
				if native.DecodeStrict(decoded[i].Event.Payload, &payload) == nil && payload.AlreadyStreamed {
					streamed = true
				}
			}
			ok = streamed && len(hmEventIndexes(decoded, native.EventMessageDelta)) > 0 && len(hmEventIndexes(decoded, native.EventMessageInterim)) > 0
		case "tool-lifecycle":
			synthesized := true
			for _, i := range hmEventIndexes(decoded, native.EventToolStart) {
				synthesized = synthesized && frames[i].Fidelity == "synthesized"
			}
			ok = hmHasEvent(decoded, native.EventToolStart) && hmHasEvent(decoded, native.EventToolComplete) &&
				hmEnvelopeCount(*execution, protocol.TypeActionCallRequested) > 0 &&
				hmEnvelopeCount(*execution, protocol.TypeActionCallStarted) > 0 &&
				hmEnvelopeCount(*execution, protocol.TypeActionCallCompleted) > 0 && synthesized
		case "tool-failure-in-result":
			failureRiding := false
			for _, i := range hmEventIndexes(decoded, native.EventToolComplete) {
				var payload native.ToolCompletePayload
				if native.DecodeStrict(decoded[i].Event.Payload, &payload) == nil && strings.Contains(string(payload.Result), "error") {
					failureRiding = failureRiding || frames[i].Fidelity == "lossy"
				}
			}
			ok = failureRiding && hmEnvelopeCount(*execution, protocol.TypeActionCallCompleted) >= 2 &&
				hmEnvelopeCount(*execution, protocol.TypeActionCallFailed) == 0
		case "approval-gate", "clarify-gate", "sudo-gate", "secret-gate":
			var event string
			switch label {
			case "approval-gate":
				event = native.EventApprovalRequest
			case "clarify-gate":
				event = native.EventClarifyRequest
			case "sudo-gate":
				event = native.EventSudoRequest
			case "secret-gate":
				event = native.EventSecretRequest
			}
			method := hmRespondMethod(strings.TrimSuffix(label, "-gate"))
			if label == "approval-gate" {
				method = native.MethodApprovalRespond
			}
			resolved := hmEnvelopeCount(*execution, protocol.TypeUserInputResolved)
			ok = hmHasEvent(decoded, event) && hmWrote(*execution, method) &&
				hmEnvelopeCount(*execution, protocol.TypeUserInputRequested) > 0 && resolved > 0
		case "approval-choices":
			options := false
			for _, envelope := range execution.envelopes {
				if envelope.Type != protocol.TypeUserInputRequested {
					continue
				}
				var payload protocol.UserInputRequestedPayload
				if envelope.DecodePayload(&payload) != nil || len(payload.Questions) != 1 {
					continue
				}
				question := payload.Questions[0]
				if question.ID != "choice" || question.Kind != protocol.InputSingleChoice || !question.Required {
					continue
				}
				ids := make([]string, len(question.Options))
				for j, option := range question.Options {
					ids[j] = string(option.ID)
				}
				options = strings.Join(ids, ",") == "once,session,always,deny"
			}
			ok = options && hmHasEvent(decoded, native.EventApprovalRequest)
		case "expire-sibling":
			ok = hmHasEvent(decoded, native.EventClarifyExpire) && !hmWrote(*execution, native.MethodClarifyRespond) &&
				hmEnvelopeCount(*execution, protocol.TypeUserInputResolved) > 0 &&
				hmEnvelopeCount(*execution, protocol.TypeRunCompleted) > 0
			cancelled := false
			for _, envelope := range execution.envelopes {
				if envelope.Type != protocol.TypeUserInputResolved {
					continue
				}
				var payload protocol.UserInputResolvedPayload
				if envelope.DecodePayload(&payload) == nil && payload.Status == protocol.InputCancelled {
					cancelled = true
				}
			}
			ok = ok && cancelled
		case "steer-run":
			level, advertised := hmDescriptorLevel(*execution, "session.message.delivery.steer")
			ok = execution.overlapRejected && !hmWrote(*execution, native.MethodSessionSteer) &&
				advertised && level == protocol.SupportUnavailable &&
				execution.wirePrompts == len(execution.admissions)+len(execution.submitErrors)
		case "steer-rejected":
			// The pinned native surface (steer result statuses queued|rejected)
			// is typed and tested in the native package; this rule pins the
			// behavioral side only: v1 never steers, and overlap is rejected
			// locally before any native write.
			ok = execution.overlapRejected && !hmWrote(*execution, native.MethodSessionSteer) &&
				execution.wirePrompts == len(execution.admissions)+len(execution.submitErrors)
		case "subagent-steer":
			ok = !hmWrote(*execution, native.MethodSubagentSteer) &&
				execution.wirePrompts == len(execution.admissions)+len(execution.submitErrors)
		case "subagent-interrupt":
			ok = !hmWrote(*execution, native.MethodSubagentInterrupt) &&
				execution.wirePrompts == len(execution.admissions)+len(execution.submitErrors)
		case "btw-delivery":
			var task native.TaskRequestResult
			ok = hmHasEvent(decoded, native.EventBTWComplete) && !hmWrote(*execution, native.MethodPromptBTW) &&
				native.DecodeStrict([]byte(`{"task_id":"btw_ab12cd"}`), &task) == nil &&
				hmEnvelopeCount(*execution, protocol.TypeRunCompleted) > 0
		case "background-prompt":
			var task native.TaskRequestResult
			ok = hmHasEvent(decoded, native.EventBackgroundComplete) && !hmWrote(*execution, native.MethodPromptBackground) &&
				native.DecodeStrict([]byte(`{"task_id":"bg_ab12cd"}`), &task) == nil &&
				hmEnvelopeCount(*execution, protocol.TypeRunCompleted) > 0
		case "subagent-lifecycle":
			spawn := hmHasEvent(decoded, "subagent.spawn_requested")
			progress := hmHasEvent(decoded, "subagent.progress")
			tool := hmHasEvent(decoded, "subagent.tool")
			observed := true
			for _, typ := range []string{"subagent.spawn_requested", "subagent.progress", "subagent.tool"} {
				for _, i := range hmEventIndexes(decoded, typ) {
					observed = observed && frames[i].Classification == "observed-only"
				}
			}
			leaked := false
			for _, envelope := range execution.envelopes {
				data, _ := json.Marshal(envelope)
				if strings.Contains(string(data), "sa1") {
					leaked = true
				}
			}
			ok = spawn && progress && tool && observed && !leaked
		case "subagent-complete-failed":
			failed := false
			for _, i := range hmEventIndexes(decoded, native.EventSubagentComplete) {
				var payload native.SubagentPayload
				if native.DecodeStrict(decoded[i].Event.Payload, &payload) == nil && payload.Status == "failed" {
					failed = frames[i].Classification == "observed-only"
				}
			}
			ok = failed && hmEnvelopeCount(*execution, protocol.TypeRunCompleted) == 0
		case "child-mirror-not-terminal":
			mirror := false
			for _, s := range settlements {
				if s.Status == "" {
					mirror = true
				}
			}
			var typ protocol.EnvelopeType
			code := ""
			for _, events := range execution.runs {
				if len(events) == 0 {
					continue
				}
				typ, code = hmRunTerminal(events)
			}
			ok = mirror && typ == protocol.TypeRunFailed && code == "hermes_invalid_settlement"
		case "settled-session-info":
			settleIdx, infoIdx := -1, -1
			for i := range decoded {
				if decoded[i].Event == nil {
					continue
				}
				if decoded[i].Event.Type == native.EventMessageComplete && settleIdx < 0 {
					settleIdx = i
				}
				if decoded[i].Event.Type == native.EventSessionInfo {
					infoIdx = i
				}
			}
			terminals := 0
			for _, events := range execution.runs {
				if typ, _ := hmRunTerminal(events); typ != "" {
					terminals++
				}
			}
			ok = settleIdx >= 0 && infoIdx > settleIdx && terminals == 1 && len(execution.assertStates) > 0 && execution.assertStates[len(execution.assertStates)-1] == "idle"
		case "usage-ticker":
			hasTick := false
			for _, i := range hmEventIndexes(decoded, native.EventSessionUsage) {
				hasTick = hasTick || frames[i].Classification == "observed-only"
			}
			usage := false
			for _, events := range execution.runs {
				if len(events) == 0 {
					continue
				}
				var payload protocol.RunCompletedPayload
				if events[len(events)-1].DecodePayload(&payload) == nil && payload.Usage != nil {
					usage = true
				}
			}
			ok = hasTick && usage && len(hmEventIndexes(decoded, native.EventSessionUsage)) == 1
		case "replay-in-window", "replay-truncated", "replay-unknown-session":
			// The pinned replay shapes (window, explicit truncation,
			// unknown-session empty result) are typed and tested in the
			// native package; these rules pin the behavioral side only: v1
			// never issues session.events.since and advertises replay
			// unavailable, so no gap contract is ever implied.
			level, advertised := hmDescriptorLevel(*execution, "run.replay")
			ok = !hmWrote(*execution, native.MethodSessionEventsSinc) && !hmWrote(*execution, native.MethodSessionEventsStat) &&
				advertised && level == protocol.SupportUnavailable &&
				execution.wirePrompts == len(execution.admissions)+len(execution.submitErrors)
		case "epoch-restart":
			// A silent seq reset (restart) breaks the contiguous fencing and
			// fails the session closed; epoch identity itself is validated by
			// the handshake pin and typed in the native package.
			restart := false
			maxSeq := int64(0)
			for i := range decoded {
				if decoded[i].Event == nil || decoded[i].Event.SessionID == "" {
					continue
				}
				if decoded[i].Event.Seq <= maxSeq && frames[i].Classification == "required-unmapped" {
					restart = true
				}
				if decoded[i].Event.Seq > maxSeq {
					maxSeq = decoded[i].Event.Seq
				}
			}
			ok = restart && execution.submitClosed
		case "resume-live":
			level, advertised := hmDescriptorLevel(*execution, "run.resume")
			ok = execution.resumeUnavailable >= 2 && !hmWrote(*execution, "session.resume") &&
				advertised && level == protocol.SupportUnavailable
		case "branch", "undo":
			ok = !hmWrote(*execution, "session."+label) && !hmWrote(*execution, native.MethodSessionEventsSinc) &&
				execution.resumeUnavailable > 0 && len(execution.admissions) > 0
		case "global-events-unsequenced":
			globals := 0
			for i := range decoded {
				if decoded[i].Event != nil && decoded[i].Event.SessionID == "" {
					globals++
				}
			}
			contiguous := false
			for i := range decoded {
				if decoded[i].Event == nil || decoded[i].Event.Type != native.EventMessageDelta || decoded[i].Event.Seq != 2 {
					continue
				}
				contiguous = hmHasEvent(decoded, native.EventMessageStart)
			}
			ok = globals >= 3 && contiguous && hmEnvelopeCount(*execution, protocol.TypeRunCompleted) > 0
		case "session-reclaimed":
			reclaimed := false
			for _, i := range hmEventIndexes(decoded, "session.reclaimed") {
				var payload struct {
					SessionID string `json:"session_id"`
					Reason    string `json:"reason"`
				}
				if json.Unmarshal(decoded[i].Event.Payload, &payload) == nil && payload.Reason == "idle_timeout" && payload.SessionID != "sess0001" {
					reclaimed = frames[i].Classification == "observed-only"
				}
			}
			ok = reclaimed && hmEnvelopeCount(*execution, protocol.TypeRunCompleted) > 0
		case "no-turn-lifecycle-events":
			_, startErr := native.DecodeNotification(native.NotifyEvent, []byte(`{"type":"turn.start","session_id":"sess0001","seq":1}`))
			_, endErr := native.DecodeNotification(native.NotifyEvent, []byte(`{"type":"turn.end","session_id":"sess0001","seq":2,"payload":{"status":"complete"}}`))
			ok = startErr != nil && endErr != nil
		case "process-exit":
			control := false
			for i := range frames {
				if frames[i].Direction == "harness-control" && frames[i].Action == "process-exit" {
					control = true
				}
			}
			code, failed := hmFailedCode(t, hmLastEnvelope(t, *execution))
			ok = control && failed && code == "hermes_process_exit" && execution.closed
		case "pre-ready-observation":
			ok = execution.openErr != nil && errors.Is(execution.openErr, rpc.ErrHandshake) &&
				len(execution.envelopes) == 0 && len(execution.logLines) == 0
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
			t.Fatalf("ledger label %q lacks executable evidence (envelopes: %v; admissions: %d; submitErrors: %v; states: %v; writes: %v)", label, trace, len(execution.admissions), execution.submitErrors, execution.assertStates, execution.nativeWrites)
		}
	}
}

// ---- expected-trace comparison ----------------------------------------------

func assertHermesExpected(t *testing.T, filename string, events []protocol.Envelope) {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var expected []protocol.Envelope
	if len(bytes.TrimSpace(data)) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("[]")) {
		if os.Getenv("OAP_UPDATE_HERMES_CORPUS") != "1" {
			t.Fatalf("%s empty; set OAP_UPDATE_HERMES_CORPUS=1", filename)
		}
		encoded, _ := json.MarshalIndent(events, "", "  ")
		if err := os.WriteFile(filename, append(encoded, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		expected = events
	} else {
		hmDecodeStrict(t, data, &expected, filename)
	}
	if !hmEqualEvents(expected, events) {
		want, _ := json.MarshalIndent(expected, "", "  ")
		got, _ := json.MarshalIndent(events, "", "  ")
		t.Fatalf("normalized trace mismatch\nwant: %s\ngot: %s", want, got)
	}
}

// hmEqualEvents compares two traces after dropping wall-clock stamps: the
// corpus harness is barrier-deterministic, so every other member — ids,
// sequences, correlations — must match exactly.
func hmEqualEvents(a, b []protocol.Envelope) bool {
	return bytes.Equal(hmNormalizeTrace(a), hmNormalizeTrace(b))
}

func hmNormalizeTrace(events []protocol.Envelope) []byte {
	if len(events) == 0 {
		return []byte("[]")
	}
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
	}
	normalized, err := json.Marshal(frames)
	if err != nil {
		return nil
	}
	return normalized
}

func hmJSONEqual(a, b json.RawMessage) bool {
	var va, vb any
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		return false
	}
	na, _ := json.Marshal(va)
	nb, _ := json.Marshal(vb)
	return bytes.Equal(na, nb)
}

// ---- corpus plumbing ---------------------------------------------------------

func hmLoadJSON[T any](t *testing.T, filename string) T {
	t.Helper()
	var value T
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	hmDecodeStrict(t, data, &value, filename)
	return value
}

func hmDecodeStrict(t *testing.T, data []byte, value any, label string) {
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

func assertHermesCorpusInventory(t *testing.T, root string, manifest hmCorpusManifest) {
	t.Helper()
	listed := map[string]bool{"manifest.json": true}
	for _, e := range manifest.Cases {
		d := hmLoadJSON[hmCorpusCase](t, filepath.Join(root, e.Path, "case.json"))
		for _, name := range []string{"case.json", d.Native, d.ExpectedOAP, d.Mapping, d.Omissions} {
			if !hmSafeRelative(name) || filepath.Base(name) != name {
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

func hmSafeRelative(path string) bool {
	clean := filepath.Clean(path)
	return path != "" && !filepath.IsAbs(path) && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func hmCorpusRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "fixtures", "adapters", "hermes-v2026.8.31")
}

func TestHermesCorpusPinConstants(t *testing.T) {
	if hmCorpusTag == "" || hmCorpusCommit == "" || hmCorpusCommitTree == "" || CapabilityRevision == "" || PinnedVersion == "" {
		t.Fatal("missing Hermes corpus pin")
	}
	if hmBlobEntry == "" || hmBlobTransport == "" || hmBlobServer == "" || hmBlobEventReplay == "" || hmBlobStdinRecovery == "" || hmBlobWS == "" || hmBlobMethodsPrompt == "" || hmBlobMethodsSession == "" {
		t.Fatal("missing Hermes corpus source pin")
	}
}

// TestMain lets the corpus re-exec the test binary as a pinned-protocol Hermes
// gateway fixture over stdio, so the ready handshake and stdin-EOF teardown
// drive the production process transport without any download.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--hermes-fixture-server" {
		hmServeFixture()
		return
	}
	os.Exit(m.Run())
}

const (
	hmServerModeLifecycle  = "lifecycle"
	hmServerModePreObserve = "pre-observe"
	hmServerModeMalformed  = "malformed"
)

// hmServerParts is the replay program extracted from a case transcript.
type hmServerParts struct {
	ready     string
	responses map[string]string // raw id bytes → full response frame
	script    []string          // verbatim lines after the first prompt response
}

// hmServeFixture speaks the pinned tui_gateway surface for corpus process
// cases. It logs every received host frame verbatim, writes gateway.ready
// before reading any input, answers session.create from the transcript, and
// replays the scripted event frames around the first prompt response. In
// pre-observe mode it emits a session observation before the handshake and
// never becomes ready; in malformed mode it finishes with a corrupt line.
func hmServeFixture() {
	mode := os.Getenv("OAP_HM_FIXTURE_MODE")
	logPath := os.Getenv("OAP_HM_FIXTURE_LOG")
	script := os.Getenv("OAP_HM_FIXTURE_SCRIPT")
	if logPath == "" || script == "" {
		os.Exit(2)
	}
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(2)
	}
	parts := hmServerScript(script)
	stdout := bufio.NewWriter(os.Stdout)
	writeLine := func(line string) {
		_, _ = stdout.WriteString(line)
		_, _ = stdout.WriteString("\n")
		_ = stdout.Flush()
	}
	if mode == hmServerModePreObserve {
		// Foreign activity before the handshake completes: the gateway owns no
		// sessions before gateway.ready.
		if len(parts.script) > 0 {
			writeLine(parts.script[0])
		}
		select {}
	}
	writeLine(parts.ready)
	reader := bufio.NewReader(os.Stdin)
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
		if response, ok := parts.responses[string(request.ID)]; ok {
			writeLine(response)
		}
		if request.Method == native.MethodPromptSubmit {
			for _, scripted := range parts.script {
				writeLine(scripted)
			}
			if mode == hmServerModeMalformed {
				return
			}
		}
	}
}

// hmServerScript extracts the byte-exact program the fixture gateway must run
// from a case transcript: the ready frame, id-keyed responses, and the event
// script replayed after the first prompt response. decode-error frames carry
// corrupt wire bytes as string raws and are replayed verbatim, so malformed
// input actually traverses the production decoder.
func hmServerScript(script string) hmServerParts {
	data, err := os.ReadFile(script)
	if err != nil {
		return hmServerParts{}
	}
	parts := hmServerParts{responses: map[string]string{}}
	for _, line := range bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
		var frame struct {
			Direction string          `json:"direction"`
			Action    string          `json:"action"`
			Raw       json.RawMessage `json:"raw"`
		}
		if json.Unmarshal(line, &frame) != nil || frame.Direction != "gateway-to-host" {
			continue
		}
		if frame.Action != "auto" && frame.Action != "decode-error" {
			continue
		}
		trimmed := bytes.TrimSpace(frame.Raw)
		if len(trimmed) > 0 && trimmed[0] == '"' {
			// A string raw is a corrupt wire line replayed verbatim.
			var literal string
			if json.Unmarshal(trimmed, &literal) == nil {
				parts.script = append(parts.script, literal)
			}
			continue
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(frame.Raw, &object) != nil {
			continue
		}
		if _, hasID := object["id"]; hasID {
			parts.responses[string(object["id"])] = string(frame.Raw)
			continue
		}
		if _, hasMethod := object["method"]; !hasMethod {
			continue
		}
		var params struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(object["params"], &params)
		if params.Type == native.EventGatewayReady {
			parts.ready = string(frame.Raw)
			continue
		}
		parts.script = append(parts.script, string(frame.Raw))
	}
	return parts
}

package makai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/adapter/makai/internal/native"
	"github.com/lsm/open-agent-protocol/adapter/makai/internal/stdio"
	"github.com/lsm/open-agent-protocol/protocol"
)

const (
	makaiCommitTree = "41b5793e363647314d93f929b014fda23d4a4aa6"
	makaiAgentTree  = "3d3f7a767fe24b1363f2f1f51531426a1eebebb8"
	makaiToolBlob   = "b2cb0c58abd20a1691c19f97386d88bf58123cc8"
	makaiClientBlob = "a23cf6c230b14044f1f6a4ca4cd730db4e68ab5b"
)

var makaiLedgerFixtures = map[string]bool{
	"initialize-minimal": true, "session-start": true, "message-admitted": true,
	"completed-text": true, "result-before-agent-end": true, "max-turns": true,
	"second-message-same-session": true, "multi-turn-tools": true,
	"tool-completed": true, "tool-progress": true, "tool-failed": true,
	"unfinished-child": true, "error-plus-agent-end": true, "cancel-confirmed": true,
	"provider-error-result": true, "completion-wins-race": true,
	"process-exit": true, "malformed-nested-event": true,
	"no-implied-native-replay":   true,
	"idle-eviction-session-gone": true, "post-stop-stale-publication": true,
	"tool-bridge-roundtrip": true,
}

type makaiCorpusManifest struct {
	Version    int                       `json:"version"`
	Adapter    string                    `json:"adapter"`
	Commit     string                    `json:"commit"`
	CommitTree string                    `json:"commit_tree"`
	AgentTree  string                    `json:"agent_protocol_tree"`
	ToolBlob   string                    `json:"makai_tool_blob"`
	ClientBlob string                    `json:"execution_client_blob"`
	Cases      []makaiCorpusManifestCase `json:"cases"`
}
type makaiCorpusManifestCase struct {
	ID             string   `json:"id"`
	Path           string   `json:"path"`
	LedgerFixtures []string `json:"ledger_fixtures"`
}
type makaiCorpusCase struct {
	Version      int               `json:"version"`
	ID           string            `json:"id"`
	Native       string            `json:"native"`
	ExpectedOAP  string            `json:"expected_oap"`
	Mapping      string            `json:"mapping"`
	Omissions    string            `json:"omissions"`
	Provenance   makaiProvenance   `json:"provenance"`
	Capabilities map[string]string `json:"advertised_capabilities"`
	IdentityMap  map[string]string `json:"identity_map"`
	Journal      int               `json:"journal_capacity,omitempty"`
	ReplayAfter  *uint64           `json:"replay_after,omitempty"`
	// RetireAfterTerminal asserts the terminal above retired the mapped
	// session: a further submission and a state read must both answer
	// ErrSessionClosed instead of admitting doomed native work.
	RetireAfterTerminal bool `json:"retire_after_terminal,omitempty"`
	// ProvidedTools is the control-owned catalog the case opens with. A case
	// that supplies one reaches the native tool_execute/tool_result bridge,
	// which is unreachable without it: a tool name the session never provided
	// has no owner to route to and keeps the adapter's long-standing refusal.
	ProvidedTools []protocol.ToolDefinition `json:"provided_tools,omitempty"`
}
type makaiProvenance struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
	CommitTree string `json:"commit_tree"`
	AgentTree  string `json:"agent_protocol_tree"`
	ToolBlob   string `json:"makai_tool_blob"`
	ClientBlob string `json:"execution_client_blob"`
}
type makaiCorpusFrame struct {
	Direction      string          `json:"direction"`
	Classification string          `json:"classification"`
	Fidelity       string          `json:"fidelity"`
	Action         string          `json:"action,omitempty"`
	Raw            json.RawMessage `json:"raw"`
	// Resolve is the control participant's answer to a pending control-owned
	// call. It appears on a client-to-agent frame whose raw is the native
	// tool_result the adapter must write back, so the corpus pins both halves
	// of what the adapter owes: the ranked response the resolver reads, and
	// the frame the harness is waiting for. Pinning only the OAP side would
	// pass an adapter that emitted a conforming terminal and told makai
	// nothing.
	Resolve *makaiCorpusResolve `json:"resolve,omitempty"`
}

// makaiCorpusResolve is one scripted resolution and the answer it must get.
// Accepted is stated rather than assumed, so a refusal can be pinned as
// deliberately as an acceptance.
type makaiCorpusResolve struct {
	Arm      string                  `json:"arm"`
	Result   json.RawMessage         `json:"result,omitempty"`
	Error    *protocol.ProtocolError `json:"error,omitempty"`
	Accepted bool                    `json:"accepted"`
	Reason   protocol.ResolveReason  `json:"reason,omitempty"`
}
type makaiCorpusMapping struct {
	Index          int    `json:"index"`
	Type           string `json:"type"`
	Classification string `json:"classification"`
	Fidelity       string `json:"fidelity"`
	OAP            string `json:"oap,omitempty"`
}
type makaiCorpusOmission struct {
	Index  int    `json:"index"`
	Type   string `json:"type"`
	Reason string `json:"reason"`
}
type makaiCasePaths struct{ native, expected, mapping, omissions string }

func TestMakaiEvidenceCorpus(t *testing.T) {
	root := makaiCorpusRoot(t)
	manifest := makaiLoadJSON[makaiCorpusManifest](t, filepath.Join(root, "manifest.json"))
	if manifest.Version != 1 || manifest.Adapter != "makai-agent-stdio" || manifest.Commit != PinnedCommit || manifest.CommitTree != makaiCommitTree || manifest.AgentTree != makaiAgentTree || manifest.ToolBlob != makaiToolBlob || manifest.ClientBlob != makaiClientBlob {
		t.Fatalf("corpus provenance pin mismatch: %+v", manifest)
	}
	seenIDs, seenPaths, covered := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, entry := range manifest.Cases {
		entry := entry
		t.Run(entry.ID, func(t *testing.T) {
			if entry.ID == "" || seenIDs[entry.ID] || seenPaths[entry.Path] || !makaiSafeRelative(entry.Path) || len(entry.LedgerFixtures) == 0 {
				t.Fatalf("invalid manifest entry: %+v", entry)
			}
			for _, fixture := range entry.LedgerFixtures {
				if !makaiLedgerFixtures[fixture] || covered[fixture] {
					t.Fatalf("invalid or duplicate ledger fixture %q", fixture)
				}
				covered[fixture] = true
			}
			seenIDs[entry.ID], seenPaths[entry.Path] = true, true
			runMakaiCorpusCase(t, root, entry)
		})
	}
	for fixture := range makaiLedgerFixtures {
		if !covered[fixture] {
			t.Errorf("ledger fixture %q has no case", fixture)
		}
	}
	assertMakaiCorpusInventory(t, root, manifest)
}

func runMakaiCorpusCase(t *testing.T, root string, entry makaiCorpusManifestCase) {
	dir := filepath.Join(root, entry.Path)
	definition := makaiLoadJSON[makaiCorpusCase](t, filepath.Join(dir, "case.json"))
	p := definition.Provenance
	if definition.Version != 1 || definition.ID != entry.ID || p.Repository != "https://github.com/lsm/makai" || p.Commit != PinnedCommit || p.CommitTree != makaiCommitTree || p.AgentTree != makaiAgentTree || p.ToolBlob != makaiToolBlob || p.ClientBlob != makaiClientBlob || len(definition.Capabilities) == 0 || len(definition.IdentityMap) == 0 {
		t.Fatalf("invalid case metadata: %+v", definition)
	}
	paths := makaiPaths(t, dir, definition)
	frames, decoded := makaiLoadFrames(t, paths.native)
	mappings := makaiLoadJSON[[]makaiCorpusMapping](t, paths.mapping)
	omissions := makaiLoadJSON[[]makaiCorpusOmission](t, paths.omissions)
	assertMakaiClassifications(t, frames, decoded, mappings, omissions)

	capacity := definition.Journal
	if capacity == 0 {
		capacity = 64
	}
	client := newFakeClient()
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil }), WorkingDirectory: "/workspace", AgentConfig: json.RawMessage(`{"model":"fixture"}`), Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: capacity})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := implementation.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.AssertDescriptor(t, descriptor)
	session := adaptertest.AssertInitialState(t, implementation, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}, Tools: definition.ProvidedTools})
	admission, stream := submitTest(t, session)
	client.mu.Lock()
	if len(client.sends) != 1 {
		client.mu.Unlock()
		t.Fatalf("got %d native submissions", len(client.sends))
	}
	nativeMessageID := client.sends[0].MessageID
	client.mu.Unlock()
	for i, frame := range frames {
		switch frame.Action {
		case "", "observe":
			if decoded[i].Type == native.TypeAgentError && decoded[i].Sequence == 0 {
				decoded[i].InReplyTo = &nativeMessageID
			}
			client.inbound <- stdio.Inbound{Envelope: decoded[i]}
		case "process-exit":
			client.mu.Lock()
			if !client.closed {
				close(client.done)
				client.closed = true
			}
			client.mu.Unlock()
		case "cancel":
			client.callHook = func(_ context.Context, request native.Envelope, _ ...native.Type) (native.Envelope, error) {
				response := *decoded[i]
				response.InReplyTo = &request.MessageID
				return response, nil
			}
			if _, err := session.Cancel(context.Background(), admission.RunID); err != nil {
				t.Fatal(err)
			}
		case "resolve-call":
			resolveMakaiCorpusCall(t, session, client, admission.RunID, i+1, frame, decoded[i])
		case "decode-error":
			if decoded[i] != nil {
				t.Fatal("invalid frame unexpectedly decoded")
			}
			client.mu.Lock()
			if !client.closed {
				close(client.done)
				client.closed = true
			}
			client.mu.Unlock()
		default:
			t.Fatalf("unsupported action %q", frame.Action)
		}
	}
	events := adaptertest.Drain(t, stream, time.Second)
	if containsMakaiAction(frames, "cancel") {
		adaptertest.AssertProtocolValidWithCancellation(t, admission, descriptor, events)
	} else {
		adaptertest.AssertProtocolValidWithDescriptor(t, admission, descriptor, events)
	}
	if definition.RetireAfterTerminal {
		if _, _, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, ModelID: protocol.ControlValue("test"), Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("again")}}}); !errors.Is(err, base.ErrSessionClosed) {
			t.Fatalf("submission after session-retiring terminal accepted: %v", err)
		}
		if _, err := session.State(context.Background()); !errors.Is(err, base.ErrSessionClosed) {
			t.Fatalf("state after session-retiring terminal: %v", err)
		}
	}
	if definition.ReplayAfter != nil {
		assertMakaiReplay(t, session, admission.RunID, *definition.ReplayAfter, events)
	}
	expected := makaiLoadJSON[[]protocol.Envelope](t, paths.expected)
	if len(expected) == 0 {
		makaiWriteExpected(t, paths.expected, events)
		expected = events
	}
	want, _ := json.Marshal(expected)
	got, _ := json.Marshal(events)
	if !bytes.Equal(want, got) {
		prettyWant, _ := json.MarshalIndent(expected, "", "  ")
		prettyGot, _ := json.MarshalIndent(events, "", "  ")
		t.Fatalf("normalized trace mismatch\nwant: %s\ngot: %s", prettyWant, prettyGot)
	}
}

// resolveMakaiCorpusCall answers the run's pending control-owned call and
// asserts both halves of what the adapter owes: the ranked response the
// resolver reads, and the native tool_result the harness is waiting for,
// compared against the frame the corpus pins.
func resolveMakaiCorpusCall(t *testing.T, open base.Session, client *fakeClient, runID protocol.RunID, index int, frame makaiCorpusFrame, want *native.Envelope) {
	t.Helper()
	if frame.Resolve == nil || want == nil || want.Type != native.TypeToolResult {
		t.Fatalf("frame %d: a resolve-call frame needs a resolve block and a native tool_result", index)
	}
	// The corpus reads the pending call off the reducer rather than off the
	// event stream, which the case drains only at the end. This test is in the
	// adapter's own package precisely so a fixture can name an interaction the
	// adapter minted without the protocol growing a surface for it.
	inner, ok := open.(*session)
	if !ok {
		t.Fatalf("frame %d: unexpected session type", index)
	}
	// Frames reach the reducer through a channel, so the call this resolution
	// answers may not have been published yet when the script reaches here.
	// Waiting for it is the scripted step's own synchronization; the
	// alternative is a flake that depends on goroutine scheduling.
	var call *callState
	deadline := time.Now().Add(time.Second)
	for {
		inner.mu.Lock()
		if run := inner.runs[runID]; run != nil {
			call = run.call
		}
		inner.mu.Unlock()
		if call != nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if call == nil {
		t.Fatalf("frame %d: no control-owned call is pending", index)
	}
	request := protocol.ActionCallResolveRequest{
		InteractionID: call.interaction, SessionID: "session", RunID: runID,
		ToolCallID: call.toolCallID, RequestedBy: "agent", RespondedBy: "user",
	}
	switch frame.Resolve.Arm {
	case protocol.ResolveArmAcknowledge:
		request.Started = &protocol.ResolveArmStarted{}
	case protocol.ResolveArmResult:
		request.Result = frame.Resolve.Result
	case protocol.ResolveArmError:
		request.Error = frame.Resolve.Error
	default:
		t.Fatalf("frame %d: unsupported resolve arm %q", index, frame.Resolve.Arm)
	}
	before := len(client.sends)
	answer, err := open.(base.CallResolver).ResolveCall(context.Background(), base.CallResolution{
		RequestID: protocol.EnvelopeID(fmt.Sprintf("corpus-resolve-%d", index)), Request: request,
	})
	if err != nil {
		t.Fatalf("frame %d resolve: %v", index, err)
	}
	if answer.Accepted != frame.Resolve.Accepted || answer.Reason != frame.Resolve.Reason {
		t.Fatalf("frame %d: got accepted=%v reason=%q, want accepted=%v reason=%q",
			index, answer.Accepted, answer.Reason, frame.Resolve.Accepted, frame.Resolve.Reason)
	}
	client.mu.Lock()
	sent := append([]native.Envelope(nil), client.sends...)
	client.mu.Unlock()
	if len(sent) != before+1 {
		t.Fatalf("frame %d: want exactly one native frame written back, got %d", index, len(sent)-before)
	}
	written := sent[len(sent)-1]
	if written.Type != native.TypeToolResult {
		t.Fatalf("frame %d: wrote %q, want tool_result", index, written.Type)
	}
	got, err := native.DecodePayload[native.ToolResult](written)
	if err != nil {
		t.Fatalf("frame %d decode written tool_result: %v", index, err)
	}
	expected, err := native.DecodePayload[native.ToolResult](*want)
	if err != nil {
		t.Fatalf("frame %d decode pinned tool_result: %v", index, err)
	}
	if got.ToolCallID != expected.ToolCallID || got.IsError != expected.IsError || !makaiSameJSON(t, got.ResultJSON, expected.ResultJSON) {
		t.Fatalf("frame %d: wrote %+v, want %+v", index, got, expected)
	}
}

// makaiSameJSON compares two encodings by value, so a corpus expectation is
// not pinned to the adapter's key order.
func makaiSameJSON(t *testing.T, got, want string) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal([]byte(got), &a); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatalf("invalid pinned tool_result %q: %v", want, err)
	}
	return reflect.DeepEqual(a, b)
}

func makaiLoadFrames(t *testing.T, filename string) ([]makaiCorpusFrame, []*native.Envelope) {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if len(lines) == 0 || len(lines[0]) == 0 {
		t.Fatal("empty native transcript")
	}
	frames := make([]makaiCorpusFrame, 0, len(lines))
	decoded := make([]*native.Envelope, 0, len(lines))
	for index, line := range lines {
		var frame makaiCorpusFrame
		decodeMakaiStrict(t, line, &frame, fmt.Sprintf("%s frame %d", filename, index+1))
		wire := append(append([]byte(nil), frame.Raw...), '\n')
		got, decodeErr := stdio.NewDecoder(bytes.NewReader(wire), stdio.DefaultFrameLimit).Decode()
		if frame.Action == "decode-error" {
			if decodeErr == nil || !errors.Is(decodeErr, stdio.ErrInvalidFrame) {
				t.Fatalf("frame %d: want production codec invalid-frame error, got %v", index+1, decodeErr)
			}
			decoded = append(decoded, nil)
		} else {
			if decodeErr != nil || got.Envelope == nil {
				t.Fatalf("frame %d production decode: %v", index+1, decodeErr)
			}
			decoded = append(decoded, got.Envelope)
		}
		frames = append(frames, frame)
	}
	return frames, decoded
}

func assertMakaiClassifications(t *testing.T, frames []makaiCorpusFrame, decoded []*native.Envelope, mappings []makaiCorpusMapping, omissions []makaiCorpusOmission) {
	t.Helper()
	if len(frames) != len(mappings) {
		t.Fatalf("mapping count %d does not cover %d native frames", len(mappings), len(frames))
	}
	omitted := map[int]makaiCorpusOmission{}
	for _, omission := range omissions {
		if omission.Index < 1 || omission.Index > len(frames) || omission.Reason == "" || omitted[omission.Index].Index != 0 {
			t.Fatalf("invalid omission: %+v", omission)
		}
		omitted[omission.Index] = omission
	}
	for i, frame := range frames {
		typ := "codec-error"
		if decoded[i] != nil {
			typ = string(decoded[i].Type)
		}
		mapping := mappings[i]
		// A resolution is the one frame that travels the other way: the
		// adapter writes it, so the corpus records it as client-to-agent and
		// the case asserts the adapter produced it rather than feeding it in.
		direction := "agent-to-client"
		if frame.Action == "resolve-call" {
			direction = "client-to-agent"
		}
		if frame.Direction != direction || mapping.Index != i+1 || mapping.Type != typ || mapping.Classification != frame.Classification || mapping.Fidelity != frame.Fidelity {
			t.Fatalf("frame %d mapping mismatch", i+1)
		}
		switch frame.Classification {
		case "mapped", "required-unmapped":
			if omitted[i+1].Index != 0 {
				t.Fatalf("mapped frame %d is omitted", i+1)
			}
		case "observed-only":
			if omitted[i+1].Index == 0 || omitted[i+1].Type != typ {
				t.Fatalf("omitted frame %d lacks matching reason", i+1)
			}
		default:
			t.Fatalf("invalid classification %q", frame.Classification)
		}
		switch frame.Fidelity {
		case "native", "normalized", "synthesized", "lossy", "unsupported":
		default:
			t.Fatalf("invalid fidelity %q", frame.Fidelity)
		}
	}
}

func assertMakaiReplay(t *testing.T, session base.Session, runID protocol.RunID, after uint64, events []protocol.Envelope) {
	t.Helper()
	recovery, replay, err := session.Resume(context.Background(), base.ResumeRequest{RunID: runID, AfterSequence: after})
	if err != nil {
		t.Fatal(err)
	}
	replayed := adaptertest.Drain(t, replay, time.Second)
	if recovery.ReplayedFrom != after+1 || recovery.ReplayedThrough != uint64(len(events)) || !reflect.DeepEqual(replayed, events[after:]) {
		t.Fatalf("degraded replay mismatch: %+v", recovery)
	}
}

func TestMakaiCorpusSequentialReuseAndCompletionOrdering(t *testing.T) {
	session, client := openTest(t, 64)
	first, firstStream := submitTest(t, session)
	client.event(t, 2, map[string]any{"type": "agent_end", "stop_reason": "max_turns"})
	firstEvents := adaptertest.Drain(t, firstStream, time.Second)
	adaptertest.AssertRunTrace(t, first, CapabilityRevision, firstEvents)
	second, secondStream := submitTest(t, session)
	if first.RunID == second.RunID || first.SubmissionID == second.SubmissionID {
		t.Fatal("sequential submission reused typed identity")
	}
	client.event(t, 3, map[string]any{"type": "agent_end", "stop_reason": "stop"})
	secondEvents := adaptertest.Drain(t, secondStream, time.Second)
	adaptertest.AssertRunTrace(t, second, CapabilityRevision, secondEvents)
	if _, err := session.Cancel(context.Background(), second.RunID); err == nil {
		t.Fatal("completed run accepted late cancellation")
	} else {
		var terminal *base.RunTerminalError
		if !errors.As(err, &terminal) || terminal.Status != protocol.RunCompleted {
			t.Fatalf("completion did not win cancellation ordering: %v", err)
		}
	}
}

func makaiPaths(t *testing.T, dir string, definition makaiCorpusCase) makaiCasePaths {
	t.Helper()
	resolve := func(label, name string) string {
		if !makaiSafeRelative(name) || filepath.Base(name) != name {
			t.Fatalf("invalid %s filename %q", label, name)
		}
		return filepath.Join(dir, name)
	}
	return makaiCasePaths{resolve("native", definition.Native), resolve("expected", definition.ExpectedOAP), resolve("mapping", definition.Mapping), resolve("omissions", definition.Omissions)}
}
func makaiLoadJSON[T any](t *testing.T, filename string) T {
	t.Helper()
	var value T
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	decodeMakaiStrict(t, data, &value, filename)
	return value
}
func decodeMakaiStrict(t *testing.T, data []byte, value any, label string) {
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
func makaiWriteExpected(t *testing.T, filename string, events []protocol.Envelope) {
	t.Helper()
	if os.Getenv("OAP_UPDATE_MAKAI_CORPUS") != "1" {
		t.Fatalf("%s empty; set OAP_UPDATE_MAKAI_CORPUS=1", filename)
	}
	data, _ := json.MarshalIndent(events, "", "  ")
	if err := os.WriteFile(filename, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
func assertMakaiCorpusInventory(t *testing.T, root string, manifest makaiCorpusManifest) {
	t.Helper()
	listed := map[string]bool{"manifest.json": true}
	for _, entry := range manifest.Cases {
		definition := makaiLoadJSON[makaiCorpusCase](t, filepath.Join(root, entry.Path, "case.json"))
		paths := makaiPaths(t, filepath.Join(root, entry.Path), definition)
		for _, name := range []string{"case.json", filepath.Base(paths.native), filepath.Base(paths.expected), filepath.Base(paths.mapping), filepath.Base(paths.omissions)} {
			listed[filepath.ToSlash(filepath.Join(entry.Path, name))] = true
		}
	}
	var unlisted []string
	if err := filepath.WalkDir(root, func(path string, item os.DirEntry, err error) error {
		if err != nil || item.IsDir() {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if !listed[relative] {
			unlisted = append(unlisted, relative)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(unlisted)
	if len(unlisted) != 0 {
		t.Fatalf("unlisted corpus files: %v", unlisted)
	}
}
func containsMakaiAction(frames []makaiCorpusFrame, action string) bool {
	for _, frame := range frames {
		if frame.Action == action {
			return true
		}
	}
	return false
}
func makaiSafeRelative(path string) bool {
	clean := filepath.Clean(path)
	return path != "" && !filepath.IsAbs(path) && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}
func makaiCorpusRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "fixtures", "adapters", "makai-agent-67ad514")
}

func TestMakaiCorpusPinConstants(t *testing.T) {
	if PinnedCommit == "" || makaiCommitTree == "" || makaiAgentTree == "" || makaiToolBlob == "" || makaiClientBlob == "" || CapabilityRevision == "" {
		t.Fatal("missing Makai corpus pin")
	}
}

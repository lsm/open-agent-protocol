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
	"github.com/lsm/open-agent-protocol/validation"
)

const (
	makaiCommitTree = "27d32e64ff5efed88c1302de0e25c1acdb9373b2"
	makaiAgentTree  = "0a21997a9bc6d4358a8ca549bb2f31c623f4583b"
	makaiToolBlob   = "feccd54dde57fa2a5eafec97dd880bf8c63121c0"
	makaiClientBlob = "d3a1e9d6c28372f271c33501d33a9290c55a3a91"
)

var makaiLedgerFixtures = map[string]bool{
	"initialize-minimal": true, "session-start": true, "message-admitted": true,
	"completed-text": true, "result-before-agent-end": true, "max-turns": true,
	"second-message-same-session": true, "multi-turn-tools": true,
	"tool-completed": true, "tool-progress": true, "tool-failed": true,
	"unfinished-child": true, "error-plus-agent-end": true, "cancel-confirmed": true,
	"completion-wins-race": true, "process-exit": true, "malformed-nested-event": true,
	"no-implied-native-replay": true,
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
	session := adaptertest.AssertInitialState(t, implementation, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
	admission, stream := submitTest(t, session)
	for i, frame := range frames {
		switch frame.Action {
		case "", "observe":
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
	adaptertest.AssertRunEvents(t, admission, descriptor.CapabilityRevision, events)
	validateMakaiTrace(t, admission, descriptor, events, containsMakaiAction(frames, "cancel"))
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
		if frame.Direction != "agent-to-client" || mapping.Index != i+1 || mapping.Type != typ || mapping.Classification != frame.Classification || mapping.Fidelity != frame.Fidelity {
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

func validateMakaiTrace(t *testing.T, admission protocol.MessageSubmitResponse, descriptor base.Descriptor, events []protocol.Envelope, cancelled bool) {
	t.Helper()
	capRequest, _ := protocol.NewEnvelope(protocol.TypeCapabilitiesRequest, "capabilities-request", protocol.CapabilitiesRequest{})
	capResponse, _ := protocol.NewEnvelope(protocol.TypeCapabilitiesResponse, "capabilities-response", descriptor.Capabilities)
	capResponse.InReplyTo, capResponse.CapabilityRevision = capRequest.ID, descriptor.CapabilityRevision
	submitRequest, _ := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitRequest, "submit-request", protocol.MessageSubmitRequest{SessionID: admission.SessionID, Delivery: admission.RequestedDelivery, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
	submitRequest.SessionID = admission.SessionID
	submitResponse, _ := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitResponse, "submit-response", admission)
	submitResponse.SessionID, submitResponse.InReplyTo = admission.SessionID, submitRequest.ID
	trace := []protocol.Envelope{capRequest, capResponse, submitRequest, submitResponse}
	if cancelled {
		statusIndex := -1
		for i := range events {
			if events[i].Type == protocol.TypeRunStatusUpdated {
				statusIndex = i
				break
			}
		}
		if statusIndex < 0 {
			t.Fatal("cancel case emitted no cancelling status")
		}
		cancelRequest, _ := protocol.NewEnvelope(protocol.TypeRunCancelRequest, "cancel-request", protocol.RunCancelRequest{SessionID: admission.SessionID, RunID: admission.RunID})
		cancelRequest.SessionID, cancelRequest.RunID = admission.SessionID, admission.RunID
		cancelResponse, _ := protocol.NewEnvelope(protocol.TypeRunCancelResponse, "cancel-response", protocol.RunCancelResponse{SessionID: admission.SessionID, RunID: admission.RunID, Accepted: true, Status: protocol.RunCancelled})
		cancelResponse.SessionID, cancelResponse.RunID, cancelResponse.InReplyTo = admission.SessionID, admission.RunID, cancelRequest.ID
		trace = append(trace, events[:statusIndex]...)
		trace = append(trace, cancelRequest, cancelResponse)
		trace = append(trace, events[statusIndex:]...)
	} else {
		trace = append(trace, events...)
	}
	encoded, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	if result := validation.MustNew().ValidateBytes(encoded, "makai-corpus"); !result.Valid() {
		t.Fatalf("corpus trace failed OAP validation: %v\ntrace: %s", result.Diagnostics, encoded)
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

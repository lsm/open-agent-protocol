package opencode

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
	"github.com/lsm/open-agent-protocol/adapter/opencode/internal/httpapi"
	"github.com/lsm/open-agent-protocol/adapter/opencode/internal/native"
	"github.com/lsm/open-agent-protocol/protocol"
)

const (
	opencodeCommitTree    = "6d8cc725d9c0945d7259b78e2f60cdec6c493a26"
	opencodeSessionEvent  = "3a559c3e38a401218ac36e3f79051172df4dbe3d"
	opencodeSessionInput  = "40babac105f66671baeb59679e275f6536a5ae26"
	opencodeDeliveryBlob  = "9b678dabf9f910b173f2b2cfddebacbd11922264"
	opencodeSessionGroup  = "8ce85ef79686dd5f448c9b31a9416c22608e7665"
	opencodeServerHandler = "5b7d354b04fc32567e41582e6a8e74537be6e57d"
	opencodeCoreSession   = "2dabfb2d6fba2eeff6306abcae0f5fb8c99b6f13"
)

var opencodeLedgerFixtures = map[string]bool{
	"initialize-minimal": true, "session-create": true, "message-admitted": true,
	"message-conflict": true, "completed-text": true, "streaming-deltas": true,
	"reasoning-deltas": true, "tool-lifecycle": true, "tool-failed": true,
	"multi-step": true, "step-failure": true, "interrupt-idle": true,
	"interrupt-active": true, "settlement": true, "history-page": true,
	"foreign-session": true, "process-exit": true, "observed-only": true,
	"queued-admission": true, "history-fence": true, "no-implied-run-id": true,
}

type opencodeCorpusManifest struct {
	Version    int                       `json:"version"`
	Adapter    string                    `json:"adapter"`
	Tag        string                    `json:"tag"`
	Commit     string                    `json:"commit"`
	CommitTree string                    `json:"commit_tree"`
	Cases      []opencodeCorpusCaseEntry `json:"cases"`
}
type opencodeCorpusCaseEntry struct {
	ID             string   `json:"id"`
	Path           string   `json:"path"`
	LedgerFixtures []string `json:"ledger_fixtures"`
}
type opencodeCorpusCase struct {
	Version           int                `json:"version"`
	ID                string             `json:"id"`
	Native            string             `json:"native"`
	ExpectedOAP       string             `json:"expected_oap"`
	Mapping           string             `json:"mapping"`
	Omissions         string             `json:"omissions"`
	Provenance        opencodeProvenance `json:"provenance"`
	Capabilities      map[string]string  `json:"advertised_capabilities"`
	IdentityMap       map[string]string  `json:"identity_map"`
	Journal           int                `json:"journal_capacity,omitempty"`
	ReplayAfter       *uint64            `json:"replay_after,omitempty"`
	Cancel            bool               `json:"cancel,omitempty"`
	AdmissionRejected bool               `json:"admission_rejected,omitempty"`
}
type opencodeProvenance struct {
	Repository    string `json:"repository"`
	Tag           string `json:"tag"`
	Commit        string `json:"commit"`
	CommitTree    string `json:"commit_tree"`
	SessionEvent  string `json:"session_event_blob"`
	SessionInput  string `json:"session_input_blob"`
	Delivery      string `json:"session_delivery_blob"`
	SessionGroup  string `json:"session_group_blob"`
	ServerHandler string `json:"server_handler_blob"`
	CoreSession   string `json:"core_session_blob"`
}
type opencodeCorpusFrame struct {
	Source         string          `json:"source"`
	Classification string          `json:"classification"`
	Fidelity       string          `json:"fidelity"`
	Action         string          `json:"action,omitempty"`
	Raw            json.RawMessage `json:"raw"`
}
type opencodeCorpusMapping struct {
	Index          int    `json:"index"`
	Type           string `json:"type"`
	Source         string `json:"source"`
	Classification string `json:"classification"`
	Fidelity       string `json:"fidelity"`
	OAP            string `json:"oap,omitempty"`
}
type opencodeCorpusOmission struct {
	Index  int    `json:"index"`
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

func TestOpenCodeEvidenceCorpus(t *testing.T) {
	root := opencodeCorpusRoot(t)
	manifest := opencodeLoadJSON[opencodeCorpusManifest](t, filepath.Join(root, "manifest.json"))
	if manifest.Version != 1 || manifest.Adapter != "opencode-http-sse" || manifest.Tag != PinnedTag || manifest.Commit != PinnedCommit || manifest.CommitTree != opencodeCommitTree {
		t.Fatalf("corpus provenance pin mismatch: %+v", manifest)
	}
	seenIDs, seenPaths, covered := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, entry := range manifest.Cases {
		entry := entry
		t.Run(entry.ID, func(t *testing.T) {
			if entry.ID == "" || seenIDs[entry.ID] || seenPaths[entry.Path] || !opencodeSafeRelative(entry.Path) || len(entry.LedgerFixtures) == 0 {
				t.Fatalf("invalid manifest entry: %+v", entry)
			}
			for _, fixture := range entry.LedgerFixtures {
				if !opencodeLedgerFixtures[fixture] || covered[fixture] {
					t.Fatalf("invalid or duplicate ledger fixture %q", fixture)
				}
				covered[fixture] = true
			}
			seenIDs[entry.ID], seenPaths[entry.Path] = true, true
			runOpenCodeCorpusCase(t, root, entry)
		})
	}
	for fixture := range opencodeLedgerFixtures {
		if !covered[fixture] {
			t.Errorf("ledger fixture %q has no case", fixture)
		}
	}
	assertOpenCodeCorpusInventory(t, root, manifest)
}

func runOpenCodeCorpusCase(t *testing.T, root string, entry opencodeCorpusCaseEntry) {
	dir := filepath.Join(root, entry.Path)
	definition := opencodeLoadJSON[opencodeCorpusCase](t, filepath.Join(dir, "case.json"))
	p := definition.Provenance
	if definition.Version != 1 || definition.ID != entry.ID || p.Repository != "https://github.com/anomalyco/opencode" || p.Tag != PinnedTag || p.Commit != PinnedCommit ||
		p.CommitTree != opencodeCommitTree || p.SessionEvent != opencodeSessionEvent || p.SessionInput != opencodeSessionInput ||
		p.Delivery != opencodeDeliveryBlob || p.SessionGroup != opencodeSessionGroup || p.ServerHandler != opencodeServerHandler || p.CoreSession != opencodeCoreSession ||
		len(definition.Capabilities) == 0 || len(definition.IdentityMap) == 0 {
		t.Fatalf("invalid case metadata: %+v", definition)
	}
	frames, decoded := opencodeLoadFrames(t, filepath.Join(dir, definition.Native))
	mappings := opencodeLoadJSON[[]opencodeCorpusMapping](t, filepath.Join(dir, definition.Mapping))
	omissions := opencodeLoadJSON[[]opencodeCorpusOmission](t, filepath.Join(dir, definition.Omissions))
	assertOpenCodeClassifications(t, frames, decoded, mappings, omissions)

	// Preset the history page before any frame is fed: settlement can fire
	// on the dispatcher goroutine as soon as a terminal candidate arrives.
	var presetHistory []native.Event
	for index, frame := range frames {
		if frame.Source != "history" || frame.Action != "" {
			continue
		}
		presetHistory = append(presetHistory, decoded[index])
	}

	capacity := definition.Journal
	if capacity == 0 {
		capacity = 64
	}
	client := newFakeClient()
	// Report the session as natively active until every frame has been
	// delivered, so settlement at an intermediate step boundary cannot race the
	// producer and complete the run with only the steps seen so far. The gate is
	// released after the feed loop.
	fed := make(chan struct{})
	client.mu.Lock()
	client.historyPage = native.HistoryPage{Events: presetHistory}
	client.idleGate = fed
	client.mu.Unlock()
	if definition.AdmissionRejected {
		client.promptErr = errors.New("HTTP 409 ConflictError")
		client.promoted = true
	} else {
		client.promoted = !strings.Contains(entry.ID, "queued")
	}
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil }), Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: capacity, SettlePollMin: time.Millisecond, SettlePollMax: 2 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := implementation.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.AssertDescriptor(t, descriptor)
	session := adaptertest.AssertInitialState(t, implementation, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
	response, stream, submitErr := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
	if submitErr != nil {
		t.Fatal(submitErr)
	}
	if definition.AdmissionRejected {
		// Decision 0002: the harness rejected the prompt, the reserved run
		// settles pre-start on the stream, and the response reports the
		// accepted queued reservation.
		if !response.Accepted || response.Admission != protocol.AdmissionQueued || response.EffectiveDelivery != protocol.EffectiveDeliveryQueue || response.RunID == "" {
			t.Fatalf("conflict reservation = %+v", response)
		}
	}
	admission := response
	client.mu.Lock()
	var admittedMessage native.MessageID
	if len(client.prompts) == 1 {
		admittedMessage = client.prompts[0].ID
	}
	client.mu.Unlock()
	var collected []protocol.Envelope
	for i, frame := range frames {
		switch frame.Action {
		case "", "observe":
			event := decoded[i]
			if event.Type == native.TypePrompted {
				// Bind the fixture's prompted turn to the identity the
				// adapter actually admitted, mirroring the live contract.
				var data native.PromptedData
				if err := native.DecodeData(event, &data); err != nil {
					t.Fatal(err)
				}
				data.MessageID = admittedMessage
				reencoded, err := json.Marshal(data)
				if err != nil {
					t.Fatal(err)
				}
				event.Data = reencoded
			}
			if frame.Source == "history" {
				continue
			}
			client.events <- event
		case "cancel":
			// Ensure run.started has been emitted so the cancelling status
			// ordering is deterministic.
			started := false
			for _, event := range collected {
				if event.Type == protocol.TypeRunStarted {
					started = true
					break
				}
			}
			if !started && !definition.AdmissionRejected {
				collected = append(collected, adaptertest.Next(t, stream, time.Second))
				if collected[len(collected)-1].Type != protocol.TypeRunStarted {
					t.Fatalf("cancel frame %d: expected run.started first, got %s", i+1, collected[len(collected)-1].Type)
				}
			}
			if _, err := session.Cancel(context.Background(), admission.RunID); err != nil {
				t.Fatal(err)
			}
		case "cancel-idle":
			// Cancel before the prompted turn: intent only, no status event.
			if _, err := session.Cancel(context.Background(), admission.RunID); err != nil {
				t.Fatal(err)
			}
		case "stream-failure":
			client.subscription.fail(errors.New("corpus stream failure"))
		default:
			t.Fatalf("unsupported action %q", frame.Action)
		}
	}
	// Every frame is delivered; report the loop idle so settlement can drain
	// the ordered prefix and derive the terminal from the full run.
	close(fed)
	events := append(collected, adaptertest.Drain(t, stream, time.Second)...)
	// Decision 0002 made both former mismatch shapes canonical: a queued
	// reservation that promotes via run.started, and an accepted run whose
	// first and only event is a pre-start terminal. Non-terminal events
	// before run.started remain invalid.
	canonical := len(events) > 0 && (events[0].Type == protocol.TypeRunStarted || events[0].Type == protocol.TypeRunFailed || events[0].Type == protocol.TypeRunCancelled)
	if !canonical {
		t.Fatalf("case %s: trace has no canonical first event", entry.ID)
	}
	if definition.Cancel {
		adaptertest.AssertProtocolValidWithCancellation(t, admission, descriptor, events)
	} else {
		adaptertest.AssertProtocolValidWithDescriptor(t, admission, descriptor, events)
	}
	if definition.ReplayAfter != nil {
		assertOpenCodeReplay(t, session, admission.RunID, *definition.ReplayAfter, events)
	}
	expectedPath := filepath.Join(dir, definition.ExpectedOAP)
	stored, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatal(err)
	}
	var expected []protocol.Envelope
	if len(bytes.TrimSpace(stored)) == 0 {
		opencodeWriteExpected(t, expectedPath, events)
		expected = events
	} else {
		opencodeDecodeStrict(t, stored, &expected, expectedPath)
	}
	want, _ := json.Marshal(expected)
	got, _ := json.Marshal(events)
	if !bytes.Equal(want, got) {
		prettyWant, _ := json.MarshalIndent(expected, "", "  ")
		prettyGot, _ := json.MarshalIndent(events, "", "  ")
		t.Fatalf("normalized trace mismatch\nwant: %s\ngot: %s", prettyWant, prettyGot)
	}
}

// opencodeLoadFrames returns each fixture wrapper alongside the native event the
// production path yields for it, so the corpus exercises transport framing and
// event decoding rather than only the reducer. Frames that carry a harness
// action instead of a payload decode to the zero event.
func opencodeLoadFrames(t *testing.T, filename string) ([]opencodeCorpusFrame, []native.Event) {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if len(lines) == 0 || len(lines[0]) == 0 {
		t.Fatal("empty native transcript")
	}
	frames := make([]opencodeCorpusFrame, 0, len(lines))
	decoded := make([]native.Event, 0, len(lines))
	for index, line := range lines {
		var frame opencodeCorpusFrame
		opencodeDecodeStrict(t, line, &frame, fmt.Sprintf("%s frame %d", filename, index+1))
		if frame.Source != "stream" && frame.Source != "history" {
			t.Fatalf("frame %d: invalid source %q", index+1, frame.Source)
		}
		frames = append(frames, frame)
		decoded = append(decoded, opencodeDecodeFrame(t, filename, index+1, frame))
	}
	return frames, decoded
}

// opencodeDecodeFrame turns one fixture payload into the native event the
// adapter would see on the live path. A stream frame is rewritten as the SSE
// wire text the pinned server writes and read back with the production SSE
// decoder at the production frame limit, so a change to field parsing, the
// event terminator, or the frame bound regresses the corpus and not only the
// httpapi tests. A history frame is an element of the HTTP history endpoint's
// JSON array, never an SSE event, so it stays on the payload path the
// production client uses for that endpoint.
func opencodeDecodeFrame(t *testing.T, filename string, index int, frame opencodeCorpusFrame) native.Event {
	t.Helper()
	if frame.Action != "" && frame.Action != "observe" {
		return native.Event{}
	}
	payload := frame.Raw
	if frame.Source == "stream" {
		var wire bytes.Buffer
		wire.WriteString("event: message\ndata: ")
		wire.Write(frame.Raw)
		wire.WriteString("\n\n")
		sse, err := httpapi.NewSSEDecoder(&wire, httpapi.DefaultFrameLimit).Decode()
		if err != nil {
			t.Fatalf("%s frame %d production SSE decode: %v", filename, index, err)
		}
		if sse.Name != "message" {
			t.Fatalf("%s frame %d decoded SSE event name %q, want \"message\"", filename, index, sse.Name)
		}
		payload = sse.Data
	}
	event, err := native.DecodeEvent(payload)
	if err != nil {
		t.Fatalf("%s frame %d production decode: %v", filename, index, err)
	}
	return event
}

func assertOpenCodeClassifications(t *testing.T, frames []opencodeCorpusFrame, decoded []native.Event, mappings []opencodeCorpusMapping, omissions []opencodeCorpusOmission) {
	t.Helper()
	if len(frames) != len(mappings) {
		t.Fatalf("mapping count %d does not cover %d native frames", len(mappings), len(frames))
	}
	omitted := map[int]opencodeCorpusOmission{}
	for _, omission := range omissions {
		if omission.Index < 1 || omission.Index > len(frames) || omission.Reason == "" || omitted[omission.Index].Index != 0 {
			t.Fatalf("invalid omission: %+v", omission)
		}
		omitted[omission.Index] = omission
	}
	for i, frame := range frames {
		mapping := mappings[i]
		typ := "action"
		if frame.Action == "" {
			typ = string(decoded[i].Type)
		}
		if mapping.Index != i+1 || mapping.Type != typ || mapping.Source != frame.Source || mapping.Classification != frame.Classification || mapping.Fidelity != frame.Fidelity {
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

func assertOpenCodeReplay(t *testing.T, session base.Session, runID protocol.RunID, after uint64, events []protocol.Envelope) {
	t.Helper()
	recovery, replay, err := session.Resume(context.Background(), base.ResumeRequest{RunID: runID, AfterSequence: after})
	if err != nil {
		t.Fatal(err)
	}
	replayed := adaptertest.Drain(t, replay, time.Second)
	if recovery.ReplayedFrom != after+1 || recovery.ReplayedThrough != uint64(len(events)) || !reflect.DeepEqual(replayed, events[after:]) {
		t.Fatalf("replay mismatch: %+v", recovery)
	}
}

func assertOpenCodeCorpusInventory(t *testing.T, root string, manifest opencodeCorpusManifest) {
	t.Helper()
	listed := map[string]bool{"manifest.json": true}
	for _, entry := range manifest.Cases {
		definition := opencodeLoadJSON[opencodeCorpusCase](t, filepath.Join(root, entry.Path, "case.json"))
		for _, name := range []string{"case.json", definition.Native, definition.ExpectedOAP, definition.Mapping, definition.Omissions} {
			if !opencodeSafeRelative(name) || filepath.Base(name) != name {
				t.Fatalf("invalid corpus filename %q", name)
			}
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

func opencodeLoadJSON[T any](t *testing.T, filename string) T {
	t.Helper()
	var value T
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	opencodeDecodeStrict(t, data, &value, filename)
	return value
}
func opencodeDecodeStrict(t *testing.T, data []byte, value any, label string) {
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
func opencodeWriteExpected(t *testing.T, filename string, events []protocol.Envelope) {
	t.Helper()
	if os.Getenv("OAP_UPDATE_OPENCODE_CORPUS") != "1" {
		t.Fatalf("%s empty; set OAP_UPDATE_OPENCODE_CORPUS=1", filename)
	}
	data, _ := json.MarshalIndent(events, "", "  ")
	if err := os.WriteFile(filename, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
func opencodeSafeRelative(path string) bool {
	clean := filepath.Clean(path)
	return path != "" && !filepath.IsAbs(path) && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}
func opencodeCorpusRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "fixtures", "adapters", "opencode-v1.18.29")
}

func TestOpenCodeCorpusPinConstants(t *testing.T) {
	if PinnedCommit == "" || opencodeCommitTree == "" || opencodeSessionEvent == "" || opencodeSessionInput == "" ||
		opencodeDeliveryBlob == "" || opencodeSessionGroup == "" || opencodeServerHandler == "" || opencodeCoreSession == "" || CapabilityRevision == "" {
		t.Fatal("missing OpenCode corpus pin")
	}
}

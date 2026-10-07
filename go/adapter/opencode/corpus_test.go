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

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/httpapi"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

var opencodeCommitTree = pin.Source("opencode").Tree

const (
	opencodeSessionEvent  = "0cffae27ad631740c4b7b59c09c01440b4b603a6"
	opencodeSessionInput  = "361c0c492ce81aaea18fa31d36d3b00dc8d13c7b"
	opencodeDeliveryBlob  = "eeed241ae14e30645d4e0904c69d3ebfe919ef38"
	opencodeSessionGroup  = "d5bf2af1db1281785aa996ed0d63c5f61821d830"
	opencodeServerHandler = "73a0763352095b093a2bc959ebe6295a5d9515f0"
	opencodeCoreSession   = "39f5f36d3a56e017a8b474a52f7d3863ee0effaf"
)

var opencodeLedgerFixtures = map[string]bool{
	"initialize-minimal": true, "session-create": true, "message-admitted": true,
	"message-conflict": true, "completed-text": true, "streaming-deltas": true,
	"reasoning-deltas": true, "tool-lifecycle": true, "tool-failed": true,
	"multi-step": true, "step-failure": true, "interrupt-idle": true,
	"interrupt-active": true, "settlement": true,
	"foreign-session": true, "process-exit": true, "observed-only": true,
	"queued-admission": true, "no-implied-run-id": true,
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
	Delivery          string             `json:"delivery,omitempty"`

	Catalog string `json:"catalog,omitempty"`
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

	capacity := definition.Journal
	if capacity == 0 {
		capacity = 64
	}
	client := newFakeClient()
	client.silentCancels = true
	if definition.AdmissionRejected {
		client.promptErr = &native.APIError{Status: 409, Tag: "ConflictError", Fields: map[string]json.RawMessage{}}
	}
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil }), Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: capacity})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := implementation.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.AssertDescriptor(t, descriptor)
	session := adaptertest.AssertInitialState(t, implementation, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
	delivery := protocol.DeliveryAuto
	if definition.Delivery != "" {
		delivery = protocol.RequestedDeliveryMode(definition.Delivery)
	}
	response, stream, submitErr := session.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Delivery: delivery, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}}})
	if submitErr != nil {
		t.Fatal(submitErr)
	}
	if definition.AdmissionRejected {

		if !response.Accepted || response.Admission != protocol.AdmissionQueued || response.EffectiveDelivery != protocol.EffectiveDeliveryQueue || response.RunID == "" {
			t.Fatalf("conflict reservation = %+v", response)
		}
	}
	if delivery == protocol.DeliveryQueue && (response.Admission != protocol.AdmissionQueued || response.EffectiveDelivery != protocol.EffectiveDeliveryQueue) {
		t.Fatalf("queue submission was admitted %+v", response)
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
			if event.Type == native.TypeInboxEnqueued || event.Type == native.TypeInboxDelivered || event.Type == native.TypeInboxCancelled {
				event.Data = rebindInbox(t, event.Data, admittedMessage)
			}
			client.events <- event
		case "cancel":

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

			if _, err := session.Cancel(context.Background(), admission.RunID); err != nil {
				t.Fatal(err)
			}
		case "stream-failure":
			client.subscription.fail(errors.New("corpus stream failure"))
		default:
			t.Fatalf("unsupported action %q", frame.Action)
		}
	}

	events := append(collected, adaptertest.Drain(t, stream, time.Second)...)

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
	if definition.Catalog != "" {
		assertOpenCodeCatalog(t, session, filepath.Join(dir, definition.Catalog))
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

func rebindInbox(t *testing.T, data json.RawMessage, admitted native.MessageID) json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["inboxID"]) != `"msg_rebind"` {
		return data
	}
	encoded, err := json.Marshal(admitted)
	if err != nil {
		t.Fatal(err)
	}
	fields["inboxID"] = encoded
	rebound, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return rebound
}

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
		if frame.Source != "stream" {
			t.Fatalf("frame %d: invalid source %q", index+1, frame.Source)
		}
		frames = append(frames, frame)
		decoded = append(decoded, opencodeDecodeFrame(t, filename, index+1, frame))
	}
	return frames, decoded
}

func opencodeDecodeFrame(t *testing.T, filename string, index int, frame opencodeCorpusFrame) native.Event {
	t.Helper()
	if frame.Action != "" && frame.Action != "observe" {
		return native.Event{}
	}
	payload := frame.Raw
	{
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

func assertOpenCodeCatalog(t *testing.T, session base.Session, filename string) {
	t.Helper()
	lister, ok := session.(base.ModelLister)
	if !ok {
		t.Fatal("the OpenCode session serves no catalog")
	}

	catalog, err := lister.Models(context.Background(), protocol.ModelsRequest{
		SessionID: "session", AllowDegradedFeatures: []string{protocol.FeatureModelsList},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var expected protocol.ModelsResponse
	opencodeDecodeStrict(t, stored, &expected, filename)
	want, _ := json.Marshal(expected)
	got, _ := json.Marshal(catalog.Models)
	if !bytes.Equal(want, got) {
		t.Fatalf("catalog mismatch\nwant: %s\n got: %s", want, got)
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
		names := []string{"case.json", definition.Native, definition.ExpectedOAP, definition.Mapping, definition.Omissions}
		if definition.Catalog != "" {
			names = append(names, definition.Catalog)
		}
		for _, name := range names {
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
	return filepath.Join(filepath.Dir(file), "..", "..", "..", filepath.FromSlash(CorpusDirectory))
}

func TestOpenCodeCorpusPinConstants(t *testing.T) {
	if PinnedCommit == "" || opencodeCommitTree == "" || opencodeSessionEvent == "" || opencodeSessionInput == "" ||
		opencodeDeliveryBlob == "" || opencodeSessionGroup == "" || opencodeServerHandler == "" || opencodeCoreSession == "" || CapabilityRevision == "" {
		t.Fatal("missing OpenCode corpus pin")
	}
}

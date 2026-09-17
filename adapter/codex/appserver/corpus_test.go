package appserver

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
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/adapter/codex/appserver/internal/rpc"
	"github.com/lsm/open-agent-protocol/protocol"
)

const codexSchemaTreeSHA256 = "d31125f254f93a9c6300e50c86ffbd3cc6ad388ef5b8833ecbd0a47371a344b6"

var codexReducerFixtures = map[string]bool{
	"completed-text":        true,
	"command-completed":     true,
	"file-change-completed": true,
	"mcp-completed":         true,
	"failed-turn":           true,
	"interrupted-turn":      true,
	"command-approval":      true,
	"file-approval":         true,
	"permissions-approval":  true,
	"user-input":            true,
	"duplicate-terminal":    true,
	"process-exit":          true,
	"model-per-turn":        true,
}

type corpusManifest struct {
	Version          int                  `json:"version"`
	Adapter          string               `json:"adapter"`
	CodexCommit      string               `json:"codex_commit"`
	SchemaTreeSHA256 string               `json:"schema_tree_sha256"`
	Cases            []corpusManifestCase `json:"cases"`
}

type corpusManifestCase struct {
	ID             string   `json:"id"`
	Path           string   `json:"path"`
	LedgerFixtures []string `json:"ledger_fixtures"`
}

type corpusCase struct {
	Version     int    `json:"version"`
	ID          string `json:"id"`
	Native      string `json:"native"`
	ExpectedOAP string `json:"expected_oap"`
	Mapping     string `json:"mapping"`
	Omissions   string `json:"omissions"`

	ModelID string `json:"model_id,omitempty"`
}

type corpusFrame struct {
	Direction      string                  `json:"direction"`
	Kind           string                  `json:"kind"`
	Method         string                  `json:"method"`
	Classification string                  `json:"classification"`
	Fidelity       string                  `json:"fidelity"`
	Params         json.RawMessage         `json:"params,omitempty"`
	Error          string                  `json:"error,omitempty"`
	ID             int64                   `json:"id,omitempty"`
	AwaitEvents    int                     `json:"await_events,omitempty"`
	Resolve        *corpusFrameResolution  `json:"resolve,omitempty"`
	ExpectedResult json.RawMessage         `json:"expected_result,omitempty"`
	ExpectedError  *corpusExpectedRPCError `json:"expected_error,omitempty"`
}

type corpusFrameResolution struct {
	Kind     string                 `json:"kind"`
	ChoiceID string                 `json:"choice_id,omitempty"`
	Granted  bool                   `json:"granted,omitempty"`
	Answers  []protocol.InputAnswer `json:"answers,omitempty"`
}

type corpusExpectedRPCError struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

type corpusMapping struct {
	Index          int    `json:"index"`
	Method         string `json:"method"`
	Classification string `json:"classification"`
	Fidelity       string `json:"fidelity"`
}

type corpusOmission struct {
	Index  int    `json:"index"`
	Method string `json:"method"`
	Reason string `json:"reason"`
}

type corpusCasePaths struct {
	native      string
	expectedOAP string
	mapping     string
	omissions   string
}

func TestCodexEvidenceCorpus(t *testing.T) {
	root := corpusRoot(t)
	manifest := loadJSON[corpusManifest](t, filepath.Join(root, "manifest.json"))
	if manifest.Version != 1 || manifest.Adapter != "codex-appserver-stdio" || manifest.CodexCommit != CodexCommit || manifest.SchemaTreeSHA256 != codexSchemaTreeSHA256 {
		t.Fatalf("corpus pin mismatch: %+v", manifest)
	}
	seenIDs := map[string]bool{}
	seenPaths := map[string]bool{}
	coveredFixtures := map[string]bool{}
	for _, entry := range manifest.Cases {
		entry := entry
		t.Run(entry.ID, func(t *testing.T) {
			if entry.ID == "" || seenIDs[entry.ID] || seenPaths[entry.Path] || !safeRelative(entry.Path) || len(entry.LedgerFixtures) == 0 {
				t.Fatalf("invalid corpus manifest entry: %+v", entry)
			}
			seenLedgerFixtures := map[string]bool{}
			for _, fixture := range entry.LedgerFixtures {
				if !codexReducerFixtures[fixture] || seenLedgerFixtures[fixture] || coveredFixtures[fixture] {
					t.Fatalf("invalid ledger fixture %q in manifest entry %q", fixture, entry.ID)
				}
				seenLedgerFixtures[fixture] = true
				coveredFixtures[fixture] = true
			}
			seenIDs[entry.ID], seenPaths[entry.Path] = true, true
			runCorpusCase(t, root, entry)
		})
	}
	for fixture := range codexReducerFixtures {
		if !coveredFixtures[fixture] {
			t.Errorf("ledger fixture %q has no reducer corpus case", fixture)
		}
	}
	assertCorpusInventory(t, root, manifest)
}

func runCorpusCase(t *testing.T, root string, entry corpusManifestCase) {
	dir := filepath.Join(root, entry.Path)
	definition := loadJSON[corpusCase](t, filepath.Join(dir, "case.json"))
	if definition.Version != 1 || definition.ID != entry.ID {
		t.Fatalf("case metadata: %+v", definition)
	}
	paths := casePaths(t, dir, definition)
	frames, decoded := loadFrames(t, paths.native)
	mappings := loadJSON[[]corpusMapping](t, paths.mapping)
	omissions := loadJSON[[]corpusOmission](t, paths.omissions)
	assertClassifications(t, frames, mappings, omissions)

	client, session, descriptor := openFake(t)
	request := protocol.MessageSubmitRequest{
		SessionID: "session-1", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}},
	}
	if definition.ModelID != "" {
		request.ModelID = protocol.ControlValue(definition.ModelID)
	}
	admission, stream, err := session.Submit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if definition.ModelID != "" {

		client.mu.Lock()
		sent := client.turnStart
		client.mu.Unlock()
		if sent.Model != definition.ModelID || admission.ModelID != definition.ModelID {
			t.Fatalf("turn/start model %q and admission model %q, want %q", sent.Model, admission.ModelID, definition.ModelID)
		}
		state, err := session.State(context.Background())
		if err != nil || state.CurrentModelID != "glm-test" {
			t.Fatalf("per_run selection moved the session default: %+v err=%v", state, err)
		}
	}
	var prefix []protocol.Envelope
	if entry.ID == "process-exit" {
		client.send(t, "turn/started", map[string]any{"threadId": client.threadID, "turn": map[string]any{"id": client.turnID, "status": "inProgress"}})
		prefix = append(prefix, adaptertest.Next(t, stream, time.Second))
	}
	if entry.ID == "interrupted-turn" {
		client.send(t, "turn/started", map[string]any{"threadId": client.threadID, "turn": map[string]any{"id": client.turnID, "status": "inProgress"}})
		prefix = append(prefix, adaptertest.Next(t, stream, time.Second))
		if _, err := session.Cancel(context.Background(), admission.RunID); err != nil {
			t.Fatal(err)
		}
		prefix = append(prefix, adaptertest.Next(t, stream, time.Second))
		frames, decoded = frames[1:], decoded[1:]
	}
	for index, frame := range frames {
		switch {
		case frame.Direction == "server_to_client" && frame.Kind == "notification":
			client.notify(decoded[index].Method, decoded[index].Params)
			for range frame.AwaitEvents {
				prefix = append(prefix, adaptertest.Next(t, stream, time.Second))
			}
		case frame.Direction == "server_to_client" && frame.Kind == "request":
			prefix = append(prefix, runCorpusRequest(t, client, session, admission, stream, frame, decoded[index])...)
		case frame.Direction == "process" && frame.Kind == "transport":
			client.err = errors.New(frame.Error)
			_ = client.Close()
		default:
			t.Fatalf("unsupported fixture direction/kind %q/%q", frame.Direction, frame.Kind)
		}
	}
	events := append(prefix, adaptertest.Drain(t, stream, time.Second)...)
	if entry.ID == "interrupted-turn" {
		adaptertest.AssertProtocolValidWithCancellation(t, admission, descriptor, events)
	} else {

		adaptertest.AssertProtocolValidWithSubmit(t, request, admission, descriptor, events)
	}
	expected := loadJSON[[]protocol.Envelope](t, paths.expectedOAP)
	if len(expected) == 0 {
		writeExpected(t, paths.expectedOAP, events)
		expected = events
	}
	want, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, got) {
		prettyWant, _ := json.MarshalIndent(expected, "", "  ")
		prettyGot, _ := json.MarshalIndent(events, "", "  ")
		t.Fatalf("normalized trace mismatch\nwant: %s\ngot:  %s", prettyWant, prettyGot)
	}
}

func runCorpusRequest(t *testing.T, client *fakeClient, session adapter.Session, admission protocol.MessageSubmitResponse, stream adapter.EventStream, frame corpusFrame, message rpc.Message) []protocol.Envelope {
	t.Helper()
	if frame.ID == 0 || frame.Method == "" {
		t.Fatalf("invalid reverse request fixture: %+v", frame)
	}

	id, ok := message.ID.IntegerValue()
	if !ok {
		t.Fatalf("decoded reverse request id %s is not an integer", message.ID)
	}
	_, response := client.request(t, id, message.Method, message.Params)
	count := frame.AwaitEvents
	if count <= 0 {
		count = 1
	}
	events := make([]protocol.Envelope, 0, count+2)
	for range count {
		events = append(events, adaptertest.Next(t, stream, time.Second))
	}
	if frame.Resolve != nil {
		requested := protocol.Envelope{}
		for _, event := range events {
			if event.Type == protocol.TypeActionPermissionRequested || event.Type == protocol.TypeUserInputRequested {
				requested = event
			}
		}
		if requested.Type == "" {
			t.Fatalf("reverse request %s emitted no portable interaction", frame.Method)
		}
		switch frame.Resolve.Kind {
		case "permission":
			var payload protocol.PermissionRequestedPayload
			if err := requested.DecodePayload(&payload); err != nil {
				t.Fatal(err)
			}
			err := session.Resolve(context.Background(), adapter.InteractionResolution{
				RunID: admission.RunID, RespondedBy: "user",
				Permission: &protocol.PermissionResolveRequest{
					InteractionID: payload.InteractionID, RequestedBy: endpointID, RespondedBy: "user",
					SessionID: admission.SessionID, RunID: admission.RunID,
					ChoiceID: frame.Resolve.ChoiceID, Granted: frame.Resolve.Granted,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			events = append(events, adaptertest.Next(t, stream, time.Second))
		case "input":
			var payload protocol.UserInputRequestedPayload
			if err := requested.DecodePayload(&payload); err != nil {
				t.Fatal(err)
			}
			err := session.Resolve(context.Background(), adapter.InteractionResolution{
				RunID: admission.RunID, RespondedBy: "user",
				Input: &protocol.UserInputResolveRequest{
					InteractionID: payload.InteractionID, RequestedBy: endpointID, RespondedBy: "user",
					SessionID: admission.SessionID, RunID: admission.RunID, Answers: frame.Resolve.Answers,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			events = append(events, adaptertest.Next(t, stream, time.Second), adaptertest.Next(t, stream, time.Second))
		default:
			t.Fatalf("unsupported fixture resolution kind %q", frame.Resolve.Kind)
		}
	}
	nativeResponse := <-response
	switch {
	case len(frame.ExpectedResult) != 0:
		if nativeResponse.Kind != rpc.MessageResponse || !jsonEqual(frame.ExpectedResult, nativeResponse.Result) {
			t.Fatalf("native response mismatch: %+v", nativeResponse)
		}
	case frame.ExpectedError != nil:
		if nativeResponse.Kind != rpc.MessageError || nativeResponse.Error == nil || nativeResponse.Error.Code != frame.ExpectedError.Code || nativeResponse.Error.Message != frame.ExpectedError.Message {
			t.Fatalf("native error mismatch: %+v", nativeResponse)
		}
	default:
		t.Fatal("reverse request fixture has no expected result or error")
	}
	return events
}

func jsonEqual(left, right []byte) bool {
	var leftValue, rightValue any
	return json.Unmarshal(left, &leftValue) == nil && json.Unmarshal(right, &rightValue) == nil && reflect.DeepEqual(leftValue, rightValue)
}

func casePaths(t *testing.T, dir string, definition corpusCase) corpusCasePaths {
	t.Helper()
	resolve := func(name, value string) string {
		t.Helper()
		if !safeRelative(value) || filepath.Base(value) != value {
			t.Fatalf("case %s has invalid %s filename %q", definition.ID, name, value)
		}
		path := filepath.Join(dir, value)
		relative, err := filepath.Rel(dir, path)
		if err != nil || relative != value {
			t.Fatalf("case %s %s filename escapes its directory: %q", definition.ID, name, value)
		}
		return path
	}
	return corpusCasePaths{
		native:      resolve("native", definition.Native),
		expectedOAP: resolve("expected_oap", definition.ExpectedOAP),
		mapping:     resolve("mapping", definition.Mapping),
		omissions:   resolve("omissions", definition.Omissions),
	}
}

func assertClassifications(t *testing.T, frames []corpusFrame, mappings []corpusMapping, omissions []corpusOmission) {
	t.Helper()
	if len(mappings) != len(frames) {
		t.Fatalf("mapping count %d does not cover %d frames", len(mappings), len(frames))
	}
	omitted := map[int]corpusOmission{}
	for _, omission := range omissions {
		if omission.Index < 1 || omission.Index > len(frames) || omission.Reason == "" || omission.Method != frames[omission.Index-1].Method {
			t.Fatalf("invalid omission: %+v", omission)
		}
		if _, exists := omitted[omission.Index]; exists {
			t.Fatalf("duplicate omission index %d", omission.Index)
		}
		omitted[omission.Index] = omission
	}
	for index, frame := range frames {
		mapping := mappings[index]
		if mapping.Index != index+1 || mapping.Method != frame.Method || mapping.Classification != frame.Classification || mapping.Fidelity != frame.Fidelity {
			t.Fatalf("frame %d mapping mismatch: frame=%+v mapping=%+v", index+1, frame, mapping)
		}
		switch frame.Classification {
		case "mapped", "required-unmapped", "unsupported-request":
			if _, exists := omitted[index+1]; exists {
				t.Fatalf("mapped frame %d appears in omissions", index+1)
			}
		case "observed-only":
			if _, exists := omitted[index+1]; !exists {
				t.Fatalf("observed-only frame %d lacks an omission reason", index+1)
			}
		default:
			t.Fatalf("frame %d has invalid classification %q", index+1, frame.Classification)
		}
		switch frame.Fidelity {
		case "native", "normalized", "synthesized", "lossy", "unsupported":
		default:
			t.Fatalf("frame %d has invalid fidelity %q", index+1, frame.Fidelity)
		}
	}
}

func loadFrames(t *testing.T, filename string) ([]corpusFrame, []rpc.Message) {
	t.Helper()
	file, err := os.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), rpc.DefaultFrameLimit)
	var frames []corpusFrame
	var decoded []rpc.Message
	for scanner.Scan() {
		var frame corpusFrame
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			t.Fatalf("decode %s frame %d: %v", filename, len(frames)+1, err)
		}
		frames = append(frames, frame)
		decoded = append(decoded, decodeCorpusFrame(t, filename, len(frames), frame))
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(frames) == 0 {
		t.Fatalf("%s contains no native frames", filename)
	}
	return frames, decoded
}

func decodeCorpusFrame(t *testing.T, filename string, index int, frame corpusFrame) rpc.Message {
	t.Helper()
	var outbound rpc.Message
	switch {
	case frame.Direction == "server_to_client" && frame.Kind == "notification":
		outbound = rpc.Notification(frame.Method, frame.Params)
	case frame.Direction == "server_to_client" && frame.Kind == "request":
		if frame.ID == 0 {
			t.Fatalf("%s frame %d: reverse request without an id", filename, index)
		}
		outbound = rpc.Request(rpc.IntegerID(frame.ID), frame.Method, frame.Params)
	default:
		return rpc.Message{}
	}
	var wire bytes.Buffer
	if err := rpc.NewEncoder(&wire).Encode(outbound); err != nil {
		t.Fatalf("%s frame %d production encode: %v", filename, index, err)
	}
	message, err := rpc.NewDecoder(&wire, rpc.DefaultFrameLimit).Decode()
	if err != nil {
		t.Fatalf("%s frame %d production decode: %v", filename, index, err)
	}
	if message.Kind != outbound.Kind {
		t.Fatalf("%s frame %d decoded as kind %d, want %d", filename, index, message.Kind, outbound.Kind)
	}
	if message.Method != frame.Method {
		t.Fatalf("%s frame %d decoded method %q, want %q", filename, index, message.Method, frame.Method)
	}
	if outbound.Kind == rpc.MessageRequest {
		if id, ok := message.ID.IntegerValue(); !ok || id != frame.ID {
			t.Fatalf("%s frame %d decoded id %s, want %d", filename, index, message.ID, frame.ID)
		}
	}
	return message
}

func loadJSON[T any](t *testing.T, filename string) T {
	t.Helper()
	var result T
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode %s: %v", filename, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		t.Fatalf("decode %s: trailing JSON value", filename)
	}
	return result
}

func writeExpected(t *testing.T, filename string, events []protocol.Envelope) {
	t.Helper()
	if os.Getenv("OAP_UPDATE_CODEX_CORPUS") != "1" {
		t.Fatalf("%s is empty; run OAP_UPDATE_CODEX_CORPUS=1 go test ./adapter/codex/appserver -run TestCodexEvidenceCorpus", filename)
	}
	data, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filename, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertCorpusInventory(t *testing.T, root string, manifest corpusManifest) {
	t.Helper()
	listed := map[string]bool{"manifest.json": true}
	for _, entry := range manifest.Cases {
		dir := filepath.Join(root, entry.Path)
		definition := loadJSON[corpusCase](t, filepath.Join(dir, "case.json"))
		paths := casePaths(t, dir, definition)
		for _, name := range []string{"case.json", filepath.Base(paths.native), filepath.Base(paths.expectedOAP), filepath.Base(paths.mapping), filepath.Base(paths.omissions)} {
			listed[filepath.ToSlash(filepath.Join(entry.Path, name))] = true
		}
	}
	var unlisted []string
	if err := filepath.WalkDir(root, func(path string, item os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if item.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if !listed[filepath.ToSlash(relative)] {
			unlisted = append(unlisted, filepath.ToSlash(relative))
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

func safeRelative(path string) bool {
	clean := filepath.Clean(path)
	return path != "" && !filepath.IsAbs(path) && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func corpusRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "fixtures", "adapters", "codex-appserver")
}

func TestCodexNonReducerEvidenceRegistry(t *testing.T) {
	evidence := map[string]string{
		"handshake":         "internal/rpc.TestStartPerformsHandshakeAndCalls",
		"thread-start":      "TestCompletedLifecycle",
		"thread-resume":     "TestOpenResumesExplicitNativeThread",
		"turn-admitted":     "TestTurnStartResponseIsAdmissionOnly",
		"cancellation-race": "TestNaturalTerminalWinsCancellationRace",
	}
	for fixture, testName := range evidence {
		if fixture == "" || testName == "" {
			t.Fatalf("invalid non-reducer evidence entry %q: %q", fixture, testName)
		}
	}
}

func TestCorpusPinConstants(t *testing.T) {
	if CodexCommit == "" || CapabilityRevision == "" || codexSchemaTreeSHA256 == "" {
		t.Fatal(fmt.Errorf("missing corpus pin constants"))
	}
}

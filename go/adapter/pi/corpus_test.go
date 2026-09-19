package pi

import (
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
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/pi/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/pi/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const (
	piCommitTree       = "346294a615d2d0ad4f6e5fbccb4cee4ccd7b2d6c"
	piRPCTypesBlob     = "1cbd49a898382f0fbb409a7d241ad694b2f59e0d"
	piRPCModeBlob      = "fc8083bedc67824dd7ff1a5a154f1a08b28c4098"
	piAgentSessionBlob = "ac2bd4b18dbe4d888e48309ad7bb63f1166179b5"
	piSessionMgrBlob   = "d25b196a5c661d33bc0b12693eb30a0591837562"
	piAgentTypesBlob   = "eebb5dc052205ddcbc0188320ce1c2f013c4fdae"
	piRPCEntryBlob     = "11059a8d47f6d4f22469802f8dca0a6af6c8db88"
	piCLIArgsBlob      = "8ad5da63e5cce1ee17476d061b3798359818fc97"
)

var piLedgerFixtures = map[string]bool{
	"initialize-minimal": true, "message-admitted": true, "message-rejected": true,
	"completed-text": true, "streaming-deltas": true, "multi-turn-tools": true,
	"tool-completed": true, "tool-failed": true, "tool-progress": true,
	"tool-parallel-order": true, "steer-queued": true, "steer-injected": true,
	"follow-up-run": true, "cancel-settled": true, "error-retry": true,
	"compaction": true, "extension-dialog": true, "reconcile-state": true,
	"entries-since": true, "switch-session": true, "process-exit": true,
	"malformed-command": true, "fork-tree": true, "no-implied-replay": true,
}

type piCorpusManifest struct {
	Version    int                    `json:"version"`
	Adapter    string                 `json:"adapter"`
	Tag        string                 `json:"tag"`
	Commit     string                 `json:"commit"`
	CommitTree string                 `json:"commit_tree"`
	Sources    piCorpusSources        `json:"sources"`
	Cases      []piCorpusManifestCase `json:"cases"`
}
type piCorpusSources struct {
	RPCTypes       string `json:"rpc_types_blob"`
	RPCMode        string `json:"rpc_mode_blob"`
	AgentSession   string `json:"agent_session_blob"`
	SessionManager string `json:"session_manager_blob"`
	AgentTypes     string `json:"agent_types_blob"`
	RPCEntry       string `json:"rpc_entry_blob"`
	CLIArgs        string `json:"cli_args_blob"`
}
type piCorpusManifestCase struct {
	ID             string   `json:"id"`
	Path           string   `json:"path"`
	LedgerFixtures []string `json:"ledger_fixtures"`
}
type piCorpusCase struct {
	Version          int                `json:"version"`
	ID               string             `json:"id"`
	Native           string             `json:"native"`
	ExpectedOAP      string             `json:"expected_oap"`
	Mapping          string             `json:"mapping"`
	Omissions        string             `json:"omissions"`
	Provenance       piCorpusProvenance `json:"provenance"`
	Capabilities     map[string]string  `json:"advertised_capabilities"`
	IdentityMap      map[string]string  `json:"identity_map"`
	Journal          int                `json:"journal_capacity,omitempty"`
	ReplayAfter      *uint64            `json:"replay_after,omitempty"`
	Cancel           bool               `json:"cancel,omitempty"`
	ResolveExtension bool               `json:"resolve_extension,omitempty"`
	PromptFailure    bool               `json:"prompt_failure,omitempty"`
	Noncanonical     string             `json:"noncanonical_mismatch,omitempty"`
	CodecOnly        bool               `json:"codec_only,omitempty"`
}
type piCorpusProvenance struct {
	Repository string          `json:"repository"`
	Tag        string          `json:"tag"`
	Commit     string          `json:"commit"`
	CommitTree string          `json:"commit_tree"`
	Sources    piCorpusSources `json:"sources"`
}
type piCorpusFrame struct {
	Direction      string          `json:"direction"`
	Classification string          `json:"classification"`
	Fidelity       string          `json:"fidelity"`
	Action         string          `json:"action,omitempty"`
	Raw            json.RawMessage `json:"raw"`
}
type piCorpusMapping struct {
	Index          int    `json:"index"`
	Type           string `json:"type"`
	Direction      string `json:"direction"`
	Classification string `json:"classification"`
	Fidelity       string `json:"fidelity"`
	OAP            string `json:"oap,omitempty"`
}
type piCorpusOmission struct {
	Index  int    `json:"index"`
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

func pinnedPiSources() piCorpusSources {
	return piCorpusSources{piRPCTypesBlob, piRPCModeBlob, piAgentSessionBlob, piSessionMgrBlob, piAgentTypesBlob, piRPCEntryBlob, piCLIArgsBlob}
}
func samePiSources(a, b piCorpusSources) bool { return a == b }

func TestPiEvidenceCorpus(t *testing.T) {
	root := piCorpusRoot(t)
	manifest := piLoadJSON[piCorpusManifest](t, filepath.Join(root, "manifest.json"))
	if manifest.Version != 1 || manifest.Adapter != "pi-rpc-stdio" || manifest.Tag != PinnedVersion || manifest.Commit != PinnedCommit || manifest.CommitTree != piCommitTree || !samePiSources(manifest.Sources, pinnedPiSources()) {
		t.Fatalf("corpus provenance pin mismatch: %+v", manifest)
	}
	seenIDs, seenPaths, covered := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, entry := range manifest.Cases {
		entry := entry
		t.Run(entry.ID, func(t *testing.T) {
			if entry.ID == "" || seenIDs[entry.ID] || seenPaths[entry.Path] || !piSafeRelative(entry.Path) || len(entry.LedgerFixtures) == 0 {
				t.Fatalf("invalid manifest entry: %+v", entry)
			}
			for _, fixture := range entry.LedgerFixtures {
				if !piLedgerFixtures[fixture] || covered[fixture] {
					t.Fatalf("invalid or duplicate ledger fixture %q", fixture)
				}
				covered[fixture] = true
			}
			seenIDs[entry.ID], seenPaths[entry.Path] = true, true
			runPiCorpusCase(t, root, entry)
		})
	}
	for fixture := range piLedgerFixtures {
		if !covered[fixture] {
			t.Errorf("ledger fixture %q has no case", fixture)
		}
	}
	assertPiCorpusInventory(t, root, manifest)
}

func runPiCorpusCase(t *testing.T, root string, entry piCorpusManifestCase) {
	dir := filepath.Join(root, entry.Path)
	definition := piLoadJSON[piCorpusCase](t, filepath.Join(dir, "case.json"))
	p := definition.Provenance
	if definition.Version != 1 || definition.ID != entry.ID || p.Repository != "https://github.com/earendil-works/pi" || p.Tag != PinnedVersion || p.Commit != PinnedCommit || p.CommitTree != piCommitTree || !samePiSources(p.Sources, pinnedPiSources()) || len(definition.Capabilities) == 0 || len(definition.IdentityMap) == 0 {
		t.Fatalf("invalid case metadata: %+v", definition)
	}
	frames, decoded := piLoadFrames(t, filepath.Join(dir, definition.Native))
	mappings := piLoadJSON[[]piCorpusMapping](t, filepath.Join(dir, definition.Mapping))
	omissions := piLoadJSON[[]piCorpusOmission](t, filepath.Join(dir, definition.Omissions))
	assertPiClassifications(t, frames, decoded, mappings, omissions)
	assertPiCaseActions(t, definition, frames)
	assertPiLedgerEvidence(t, entry.LedgerFixtures, frames, decoded)
	if definition.CodecOnly {
		assertPiExpected(t, filepath.Join(dir, definition.ExpectedOAP), nil)
		if definition.Noncanonical == "" {
			t.Fatal("codec-only case must state its noncanonical mismatch")
		}
		return
	}

	client := newFakeClient()
	promptErr := errors.New("fixture prompt rejected")
	client.onCall = func(c native.Command) {
		if c.Type == native.CommandPrompt && !definition.PromptFailure {
			client.emit(t, map[string]any{"type": "agent_start"})
		}
	}
	capacity := definition.Journal
	if capacity == 0 {
		capacity = 64
	}
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, native.SessionState, error) { return client, client.state, nil }), Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: capacity})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := implementation.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.AssertDescriptor(t, descriptor)
	session := adaptertest.AssertInitialState(t, implementation, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
	var admission protocol.MessageSubmitResponse
	var stream base.EventStream
	if definition.PromptFailure {
		client.mu.Lock()
		client.err = promptErr
		client.mu.Unlock()
		var err error
		admission, stream, err = session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
		if !errors.Is(err, promptErr) {
			t.Fatalf("prompt rejection error = %v, want %v", err, promptErr)
		}
		if admission.Accepted || len(adaptertest.Drain(t, stream, time.Second)) != 0 {
			t.Fatal("rejected prompt exposed admission or run events")
		}
		if _, err := session.State(context.Background()); err == nil {
			t.Fatal("rejected prompt did not leave session fail-closed")
		}
		assertPiExpected(t, filepath.Join(dir, definition.ExpectedOAP), nil)
		return
	}
	admission, stream = submitTest(t, session.(*Session))
	var collected []protocol.Envelope
	var resolutionTrace []protocol.Envelope
	for i, frame := range frames {
		switch frame.Action {
		case "", "observe", "observe-extension":
			if decoded[i].Event != nil {
				client.inbound <- rpc.Inbound{Event: decoded[i].Event}
			} else if decoded[i].ExtensionRequest != nil {
				client.inbound <- rpc.Inbound{ExtensionRequest: decoded[i].ExtensionRequest}
			}
		case "cancel":
			if _, err := session.Cancel(context.Background(), admission.RunID); err != nil {
				t.Fatal(err)
			}
		case "resolve-extension":
			for len(collected) < 3 {
				collected = append(collected, adaptertest.Next(t, stream, time.Second))
			}
			var requested protocol.UserInputRequestedPayload
			if err := collected[len(collected)-2].DecodePayload(&requested); err != nil {
				t.Fatal(err)
			}
			request := protocol.UserInputResolveRequest{InteractionID: requested.InteractionID, RequestedBy: requested.RequestedBy, RespondedBy: requested.RespondedBy, SessionID: admission.SessionID, RunID: admission.RunID, Answers: []protocol.InputAnswer{{QuestionID: "value", SelectedOptionIDs: []string{"yes"}}}}
			requestEnvelope, err := protocol.NewEnvelope(protocol.TypeUserInputResolveRequest, "resolve-request", request)
			if err != nil {
				t.Fatal(err)
			}
			requestEnvelope.SessionID, requestEnvelope.RunID = admission.SessionID, admission.RunID
			if err := session.Resolve(context.Background(), base.InteractionResolution{RunID: admission.RunID, RespondedBy: request.RespondedBy, Input: &request}); err != nil {
				t.Fatal(err)
			}
			client.mu.Lock()
			responses := append([]native.ExtensionUIResponse(nil), client.responses...)
			client.mu.Unlock()
			if len(responses) != 1 || responses[0].ID != "ui-1" || responses[0].Confirmed == nil || !*responses[0].Confirmed {
				t.Fatalf("native extension response mismatch: %+v", responses)
			}
			responseEnvelope, err := protocol.NewEnvelope(protocol.TypeUserInputResolveResponse, "resolve-response", protocol.UserInputResolveResponse{InteractionID: requested.InteractionID, SessionID: admission.SessionID, RunID: admission.RunID, Accepted: true})
			if err != nil {
				t.Fatal(err)
			}
			responseEnvelope.SessionID, responseEnvelope.RunID, responseEnvelope.InReplyTo = admission.SessionID, admission.RunID, requestEnvelope.ID
			resolutionTrace = []protocol.Envelope{requestEnvelope, responseEnvelope}
		case "process-exit":
			client.mu.Lock()
			client.err = errors.New("fixture process exit")
			if !client.closed {
				close(client.done)
				client.closed = true
			}
			client.mu.Unlock()
		case "state":
			if _, err := session.State(context.Background()); err != nil {
				t.Fatal(err)
			}
		case "outbound-only":
		default:
			t.Fatalf("unsupported frame action %q", frame.Action)
		}
	}
	events := append(collected, adaptertest.Drain(t, stream, time.Second)...)
	validatePiTrace(t, admission, descriptor, events, definition.Cancel, resolutionTrace)
	if definition.Noncanonical != "" {
		t.Logf("case %s: explicit production-boundary mismatch: %s", entry.ID, definition.Noncanonical)
	}
	if definition.ReplayAfter != nil {
		assertPiReplay(t, session, admission.RunID, *definition.ReplayAfter, events)
	}
	assertPiExpected(t, filepath.Join(dir, definition.ExpectedOAP), events)
}

func piLoadFrames(t *testing.T, filename string) ([]piCorpusFrame, []rpc.Frame) {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if len(lines) == 0 || len(lines[0]) == 0 {
		t.Fatal("empty native transcript")
	}
	frames := make([]piCorpusFrame, 0, len(lines))
	decoded := make([]rpc.Frame, 0, len(lines))
	for i, line := range lines {
		var frame piCorpusFrame
		piDecodeStrict(t, line, &frame, fmt.Sprintf("%s frame %d", filename, i+1))
		var got rpc.Frame
		switch frame.Direction {
		case "pi-to-host":
			value, err := rpc.NewDecoder(bytes.NewReader(append(append([]byte(nil), frame.Raw...), '\n')), rpc.DefaultFrameLimit).Decode()
			if frame.Action == "decode-error" {
				if err == nil || !errors.Is(err, rpc.ErrInvalidFrame) {
					t.Fatalf("frame %d: want invalid frame, got %v", i+1, err)
				}
			} else if err != nil {
				t.Fatalf("frame %d production decode: %v", i+1, err)
			} else {
				got = value
			}
		case "harness-control":
			if frame.Action != "process-exit" {
				t.Fatalf("frame %d invalid harness control action %q", i+1, frame.Action)
			}
			var control struct {
				Type  string `json:"type"`
				Error string `json:"error"`
			}
			piDecodeStrict(t, frame.Raw, &control, fmt.Sprintf("%s frame %d", filename, i+1))
			if control.Type != "process_exit" || control.Error == "" {
				t.Fatalf("frame %d invalid process-exit control", i+1)
			}
		case "host-to-pi":
			var command native.Command
			err := native.DecodeStrict(frame.Raw, &command)
			if frame.Action == "decode-error" {
				if err == nil {
					err = command.Validate()
				}
				if err == nil {
					t.Fatalf("frame %d unexpectedly valid", i+1)
				}
			} else {
				if err != nil {
					t.Fatalf("frame %d command decode: %v", i+1, err)
				}
				var wire bytes.Buffer
				if err := rpc.NewEncoder(&wire).Encode(command); err != nil {
					t.Fatalf("frame %d production encode: %v", i+1, err)
				}
				if !bytes.Equal(bytes.TrimSuffix(wire.Bytes(), []byte("\n")), frame.Raw) {
					t.Fatalf("frame %d is not canonical outbound JSON", i+1)
				}
			}
		default:
			t.Fatalf("frame %d invalid direction %q", i+1, frame.Direction)
		}
		frames = append(frames, frame)
		decoded = append(decoded, got)
	}
	return frames, decoded
}

func assertPiCaseActions(t *testing.T, definition piCorpusCase, frames []piCorpusFrame) {
	t.Helper()
	cancel, resolve, promptFailure := 0, 0, 0
	for i, frame := range frames {
		switch frame.Action {
		case "", "observe", "observe-extension", "outbound-only", "decode-error", "process-exit", "cancel", "resolve-extension", "prompt-failure", "state":
		default:
			t.Fatalf("frame %d has unknown action %q", i+1, frame.Action)
		}
		if frame.Action == "cancel" {
			cancel++
		}
		if frame.Action == "resolve-extension" {
			resolve++
		}
		if frame.Action == "prompt-failure" {
			promptFailure++
		}
	}
	if definition.Cancel != (cancel == 1) || cancel > 1 {
		t.Fatalf("cancel metadata/action mismatch: metadata=%t actions=%d", definition.Cancel, cancel)
	}
	if definition.ResolveExtension != (resolve == 1) || resolve > 1 {
		t.Fatalf("resolve_extension metadata/action mismatch: metadata=%t actions=%d", definition.ResolveExtension, resolve)
	}
	if definition.PromptFailure != (promptFailure == 1) || promptFailure > 1 {
		t.Fatalf("prompt_failure metadata/action mismatch: metadata=%t actions=%d", definition.PromptFailure, promptFailure)
	}
}

func hasPiCommand(frames []piCorpusFrame, typ native.CommandType) bool {
	for _, frame := range frames {
		if frame.Direction != "host-to-pi" {
			continue
		}
		var command native.Command
		if json.Unmarshal(frame.Raw, &command) == nil && command.Type == typ {
			return true
		}
	}
	return false
}
func hasPiEvent(decoded []rpc.Frame, typ native.EventType) bool {
	for _, frame := range decoded {
		if frame.Event != nil && frame.Event.Type == typ {
			return true
		}
	}
	return false
}
func hasPiResponse(decoded []rpc.Frame, command native.CommandType) bool {
	for _, frame := range decoded {
		if frame.Response != nil && frame.Response.Command == command && frame.Response.Success {
			return true
		}
	}
	return false
}
func assertPiLedgerEvidence(t *testing.T, labels []string, frames []piCorpusFrame, decoded []rpc.Frame) {
	t.Helper()
	for _, label := range labels {
		ok := true
		switch label {
		case "message-rejected":
			ok = len(frames) == 1 && frames[0].Action == "prompt-failure"
		case "malformed-command":
			ok = len(frames) == 1 && frames[0].Action == "decode-error"
		case "steer-queued":
			ok = hasPiEvent(decoded, native.EventQueueUpdate)
		case "steer-injected":
			ok = hasPiCommand(frames, native.CommandSteer) && hasPiResponse(decoded, native.CommandSteer)
		case "follow-up-run":
			ok = hasPiCommand(frames, native.CommandFollowUp) && hasPiResponse(decoded, native.CommandFollowUp)
		case "reconcile-state":
			ok = hasPiCommand(frames, native.CommandGetState) && hasPiResponse(decoded, native.CommandGetState)
		case "entries-since":
			ok = hasPiCommand(frames, native.CommandGetEntries) && hasPiResponse(decoded, native.CommandGetEntries)
		case "switch-session":
			ok = hasPiCommand(frames, native.CommandSwitchSession) && hasPiResponse(decoded, native.CommandSwitchSession)
		case "fork-tree":
			ok = hasPiCommand(frames, native.CommandFork) && hasPiResponse(decoded, native.CommandFork)
		case "no-implied-replay":
			ok = hasPiCommand(frames, native.CommandGetEntries) && hasPiResponse(decoded, native.CommandGetEntries)
		case "process-exit":
			ok = false
			for _, frame := range frames {
				if frame.Action == "process-exit" && frame.Direction == "harness-control" {
					ok = true
				}
			}
		case "extension-dialog":
			request, response := false, false
			for _, frame := range frames {
				request = request || frame.Action == "observe-extension"
				response = response || frame.Action == "resolve-extension"
			}
			ok = request && response
		}
		if !ok {
			t.Fatalf("ledger label %q lacks executable evidence", label)
		}
	}
}

func assertPiClassifications(t *testing.T, frames []piCorpusFrame, decoded []rpc.Frame, mappings []piCorpusMapping, omissions []piCorpusOmission) {
	t.Helper()
	if len(frames) != len(mappings) {
		t.Fatalf("mapping count %d does not cover %d frames", len(mappings), len(frames))
	}
	omitted := map[int]piCorpusOmission{}
	for _, o := range omissions {
		if o.Index < 1 || o.Index > len(frames) || o.Reason == "" || omitted[o.Index].Index != 0 {
			t.Fatalf("invalid omission: %+v", o)
		}
		omitted[o.Index] = o
	}
	for i, f := range frames {
		typ := "codec-error"
		if f.Direction == "harness-control" {
			typ = "process_exit"
		} else if f.Direction == "host-to-pi" && f.Action != "decode-error" {
			var c native.Command
			_ = json.Unmarshal(f.Raw, &c)
			typ = string(c.Type)
		} else if decoded[i].Event != nil {
			typ = string(decoded[i].Event.Type)
		} else if decoded[i].ExtensionRequest != nil {
			typ = "extension_ui_request"
		} else if decoded[i].Response != nil {
			typ = "response:" + string(decoded[i].Response.Command)
		}
		m := mappings[i]
		if m.Index != i+1 || m.Type != typ || m.Direction != f.Direction || m.Classification != f.Classification || m.Fidelity != f.Fidelity {
			t.Fatalf("frame %d mapping mismatch: %+v type=%s", i+1, m, typ)
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
			if omitted[i+1].Index == 0 || omitted[i+1].Type != typ || m.OAP != "" {
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

func assertPiReplay(t *testing.T, session base.Session, runID protocol.RunID, after uint64, events []protocol.Envelope) {
	t.Helper()
	recovery, replay, err := session.Resume(context.Background(), base.ResumeRequest{RunID: runID, AfterSequence: after})
	if err != nil {
		t.Fatal(err)
	}
	got := adaptertest.Drain(t, replay, time.Second)
	if recovery.ReplayedFrom != after+1 || recovery.ReplayedThrough != uint64(len(events)) || !equalPiEvents(got, events[after:]) {
		t.Fatalf("replay mismatch: %+v", recovery)
	}
}
func equalPiEvents(a, b []protocol.Envelope) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
func assertPiExpected(t *testing.T, filename string, events []protocol.Envelope) {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var expected []protocol.Envelope
	if len(bytes.TrimSpace(data)) == 0 {
		if os.Getenv("OAP_UPDATE_PI_CORPUS") != "1" {
			t.Fatalf("%s empty; set OAP_UPDATE_PI_CORPUS=1", filename)
		}
		encoded, _ := json.MarshalIndent(events, "", "  ")
		if err := os.WriteFile(filename, append(encoded, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		expected = events
	} else {
		piDecodeStrict(t, data, &expected, filename)
	}
	if !equalPiEvents(expected, events) {
		want, _ := json.MarshalIndent(expected, "", "  ")
		got, _ := json.MarshalIndent(events, "", "  ")
		t.Fatalf("normalized trace mismatch\nwant: %s\ngot: %s", want, got)
	}
}
func piLoadJSON[T any](t *testing.T, filename string) T {
	t.Helper()
	var value T
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	piDecodeStrict(t, data, &value, filename)
	return value
}
func piDecodeStrict(t *testing.T, data []byte, value any, label string) {
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
func assertPiCorpusInventory(t *testing.T, root string, manifest piCorpusManifest) {
	t.Helper()
	listed := map[string]bool{"manifest.json": true}
	for _, e := range manifest.Cases {
		d := piLoadJSON[piCorpusCase](t, filepath.Join(root, e.Path, "case.json"))
		for _, name := range []string{"case.json", d.Native, d.ExpectedOAP, d.Mapping, d.Omissions} {
			if !piSafeRelative(name) || filepath.Base(name) != name {
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
func piSafeRelative(path string) bool {
	clean := filepath.Clean(path)
	return path != "" && !filepath.IsAbs(path) && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}
func piCorpusRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "fixtures", "adapters", "pi-v0.85.1")
}
func TestPiCorpusPinConstants(t *testing.T) {
	if PinnedVersion == "" || PinnedCommit == "" || piCommitTree == "" || piRPCTypesBlob == "" || piRPCModeBlob == "" || piAgentSessionBlob == "" || piSessionMgrBlob == "" || piAgentTypesBlob == "" || piRPCEntryBlob == "" || piCLIArgsBlob == "" || CapabilityRevision == "" {
		t.Fatal("missing Pi corpus pin")
	}
}

func validatePiTrace(t *testing.T, admission protocol.MessageSubmitResponse, descriptor base.Descriptor, events []protocol.Envelope, cancelled bool, resolutionTrace []protocol.Envelope) {
	t.Helper()
	if len(resolutionTrace) == 0 {
		if cancelled {
			adaptertest.AssertProtocolValidWithCancellation(t, admission, descriptor, events)
		} else {
			adaptertest.AssertProtocolValidWithDescriptor(t, admission, descriptor, events)
		}
		return
	}
	adaptertest.AssertRunEvents(t, admission, descriptor.CapabilityRevision, events)
	var requested protocol.UserInputRequestedPayload
	found := false
	for _, event := range events {
		if event.Type != protocol.TypeUserInputRequested {
			continue
		}
		if err := event.DecodePayload(&requested); err != nil {
			t.Fatal(err)
		}
		found = true
		break
	}
	if !found || len(resolutionTrace) != 2 || resolutionTrace[0].Type != protocol.TypeUserInputResolveRequest || resolutionTrace[1].Type != protocol.TypeUserInputResolveResponse || resolutionTrace[1].InReplyTo != resolutionTrace[0].ID {
		t.Fatal("invalid injected interaction request/response trace")
	}
	var request protocol.UserInputResolveRequest
	var response protocol.UserInputResolveResponse
	if err := resolutionTrace[0].DecodePayload(&request); err != nil {
		t.Fatal(err)
	}
	if err := resolutionTrace[1].DecodePayload(&response); err != nil {
		t.Fatal(err)
	}
	if request.InteractionID != requested.InteractionID || response.InteractionID != requested.InteractionID || request.SessionID != admission.SessionID || response.SessionID != admission.SessionID || request.RunID != admission.RunID || response.RunID != admission.RunID || !response.Accepted {
		t.Fatal("injected interaction identity or acceptance mismatch")
	}
}

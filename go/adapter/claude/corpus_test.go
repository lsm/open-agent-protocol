package claude

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
	"github.com/lsm/open-agent-protocol/go/adapter/claude/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/claude/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const (
	ccTSRepository = "https://github.com/anthropics/claude-agent-sdk-typescript"
	ccPyRepository = "https://github.com/anthropics/claude-agent-sdk-python"
)

type ccCorpusPin struct {
	dir     string
	sources ccCorpusSources
}

var ccCurrentCorpus = ccCorpusPin{dir: "claude-code-2.1.280", sources: ccCorpusSources{
	CLIVersion:        "2.1.280",
	NPMTarballSHA256:  "1326e6b8cf00404fc3f9bd101d806b3fdec9588264e5a1aa8d98d1f7afe50170",
	LauncherCjsSHA256: "61ad63033d9c8155d5e60a29f45dc4665afa07631c0b108e62cc83bf45ba490e",
	LinuxTarballSHA:   "3d95573100e302f79d536ef3eb64f5da0d537433af6ae8de571dfeda6d52bfd3",
	LinuxBinarySHA:    "1e08503dbdf3c2cb0d706d32f3408277388d1c76ef108673e8fe42c1b322925b",
	LinuxBinaryBytes:  233709640,
	DarwinTarballSHA:  "76170ceef79015e118fdea65e3b11663342153d3559f301ab8e6a7dfecc7f4a3",
	DarwinBinarySHA:   "387a5c5dcdbb815085edf0baf79591f9d8894efe922bceaf3d75b1b08055229d",
	DarwinBinaryBytes: 217254576,
	BuildCommit:       "80abbfe7d7232280011ff01a21ae3338f4c6e372",
	BuildDate:         "2026-09-21T20:55:27Z",
	TSSDKVersion:      "0.3.280",
	TSSDKTarballSHA:   "5d3f5706261215c352d8b41606fb320f92a63cf252f020b47e7eed598bcb7ba8",
	TSSDKDtsSHA:       "b7ac9c0ed0db5c1792a5394e72c75d69d85f4ce9edc0279487ec55d32eabfa76",
	TSRepository:      ccTSRepository,
	PySDKVersion:      "0.2.158",
	PySDKCommit:       "2c24c8248d0b52d44ff352854d7b679ac37b0db7",
	PySDKTree:         "698715c4e378cd259f3513eb44c3932fa56d502c",
	PyPyprojectBlob:   "916afbc6977b9d1572fd0d970d362c7f31da39d2",
	PyRepository:      ccPyRepository,
	PyClientPy:        "f3155011c17fb5ca5d44ff21a43ecd66dca51282",
	PyTypesPy:         "861c316936565edc413896d27e280028c4660125",
	PyQueryPy:         "63bac7d43eedab56e4d7adbb68e1e6d92d21eb6d",
	PyParserPy:        "931cc2a632f296aab43f3f98209020138431ce7d",
	PyTransportPy:     "7e53b8131c7e003543ebf005dc4dd5c28f9986d9",
	PyResumePy:        "a50e578fdaea7b10de83697fe355145b7351cecc",
	PyStorePy:         "bb6a2155b08ad546227eba9f2349d95bffd910fa",
	PyStoreValPy:      "16addd216281eecaadaedbe7ed361ad8205d0433",
}}

var ccFloorCorpus = ccCorpusPin{dir: "claude-code-2.1.263", sources: ccCorpusSources{
	CLIVersion:        "2.1.263",
	NPMTarballSHA256:  "b325aaaf748065ebce116c50893120384ce6ec56c1133f42f45177f8d1030c66",
	LauncherCjsSHA256: "61ad63033d9c8155d5e60a29f45dc4665afa07631c0b108e62cc83bf45ba490e",
	LinuxTarballSHA:   "8b6207348ad56fdcde085a0ad1f7cff0dfe06ce2c6c1bf97f69f1a1a7b6d0945",
	LinuxBinarySHA:    "26d020351e8112f4006790f3cfce43b4c9df0c1bb1d0e542364d64151b81d5ba",
	LinuxBinaryBytes:  215662064,
	BuildCommit:       "37ae3f38d765199d54a6913cd61c6c9ad8576cc6",
	BuildDate:         "2026-09-06T01:17:56Z",
	TSSDKVersion:      "0.3.263",
	TSSDKTarballSHA:   "e1d6b68b557fc3c57430cafa8cc65eea9d40ff7348b238fe2c431284d727901d",
	TSSDKDtsSHA:       "59560e31f91e47ed93e7cbcaa846e3fc3c96d8dcc41ea36cda64de96c0c4edf4",
	TSRepository:      ccTSRepository,
	PySDKVersion:      "0.2.152",
	PySDKCommit:       "efd4d865ef1795daffee3cd24cce45307aed8a51",
	PySDKTree:         "d617ca6d630c7bab54f3c0cd1376dcbb938103a2",
	PyPyprojectBlob:   "ebece50404bb77b0d02aa47be14375fde76eea60",
	PyRepository:      ccPyRepository,
	PyClientPy:        "bba76b10e4c2ecb6b0d526ad302122b7549c3ac4",
	PyTypesPy:         "308b76cb7fd928d124666c255b253c92c343f15d",
	PyQueryPy:         "4d5f0070e0568778255a39cc6351aaf40429da7c",
	PyParserPy:        "931cc2a632f296aab43f3f98209020138431ce7d",
	PyTransportPy:     "58abc438ddadc7406330a32d90f743ae60d10c69",
	PyResumePy:        "a50e578fdaea7b10de83697fe355145b7351cecc",
	PyStorePy:         "bb6a2155b08ad546227eba9f2349d95bffd910fa",
	PyStoreValPy:      "16addd216281eecaadaedbe7ed361ad8205d0433",
}}

var ccLedgerFixtures = map[string]bool{

	"initialize-minimal": true, "per-turn-init": true, "second-turn": true,

	"message-admitted": true, "command-lifecycle": true, "injected-turn-origin": true,
	"queued-turn-count": true,

	"completed-text": true, "max-turns": true, "api-error-result": true,
	"error-result": true, "interrupt-cancel": true, "process-exit": true,
	"malformed-stdout-line": true,

	"streaming-deltas": true, "interleaved-blocks": true,

	"tool-roundtrip": true, "tool-failed": true, "tool-progress": true,
	"auto-approved-tool": true,

	"permission-gate": true, "permission-deny": true,

	"subagent-task": true, "task-updated-terminal": true, "stop-task": true,

	"keep-alive-ignored": true, "unknown-frame-ignored": true,
	"no-implied-replay": true, "resume-fork": true,

	"tools-catalog-sources": true,
}

type ccCorpusSources struct {
	CLIVersion        string `json:"cli_version"`
	NPMTarballSHA256  string `json:"npm_tarball_sha256"`
	LauncherCjsSHA256 string `json:"cli_wrapper_cjs_sha256"`
	LinuxTarballSHA   string `json:"linux_x64_tarball_sha256"`
	LinuxBinarySHA    string `json:"linux_x64_binary_sha256"`
	LinuxBinaryBytes  int    `json:"linux_x64_binary_bytes"`
	DarwinTarballSHA  string `json:"darwin_arm64_tarball_sha256"`
	DarwinBinarySHA   string `json:"darwin_arm64_binary_sha256"`
	DarwinBinaryBytes int    `json:"darwin_arm64_binary_bytes"`
	BuildCommit       string `json:"build_commit"`
	BuildDate         string `json:"build_date"`
	TSSDKVersion      string `json:"ts_sdk_version"`
	TSSDKTarballSHA   string `json:"ts_sdk_tarball_sha256"`
	TSSDKDtsSHA       string `json:"ts_sdk_dts_sha256"`
	TSRepository      string `json:"ts_sdk_repository"`
	PySDKVersion      string `json:"py_sdk_version"`
	PySDKCommit       string `json:"py_sdk_commit"`
	PySDKTree         string `json:"py_sdk_tree"`
	PyPyprojectBlob   string `json:"py_sdk_pyproject_blob"`
	PyRepository      string `json:"py_sdk_repository"`
	PyClientPy        string `json:"py_client_py_blob"`
	PyTypesPy         string `json:"py_types_py_blob"`
	PyQueryPy         string `json:"py_query_py_blob"`
	PyParserPy        string `json:"py_parser_py_blob"`
	PyTransportPy     string `json:"py_transport_py_blob"`
	PyResumePy        string `json:"py_resume_py_blob"`
	PyStorePy         string `json:"py_store_py_blob"`
	PyStoreValPy      string `json:"py_store_validation_py_blob"`
}

type ccCorpusManifest struct {
	Version int                    `json:"version"`
	Adapter string                 `json:"adapter"`
	Tag     string                 `json:"tag"`
	Sources ccCorpusSources        `json:"sources"`
	Cases   []ccCorpusManifestCase `json:"cases"`
}
type ccCorpusManifestCase struct {
	ID             string   `json:"id"`
	Path           string   `json:"path"`
	LedgerFixtures []string `json:"ledger_fixtures"`
}
type ccCorpusCase struct {
	Version      int                `json:"version"`
	ID           string             `json:"id"`
	Native       string             `json:"native"`
	ExpectedOAP  string             `json:"expected_oap"`
	Mapping      string             `json:"mapping"`
	Omissions    string             `json:"omissions"`
	Provenance   ccCorpusProvenance `json:"provenance"`
	Capabilities map[string]string  `json:"advertised_capabilities"`
	IdentityMap  map[string]string  `json:"identity_map"`
}
type ccCorpusProvenance struct {
	Repository string          `json:"repository"`
	Tag        string          `json:"tag"`
	Sources    ccCorpusSources `json:"sources"`
}
type ccFrame struct {
	Direction      string          `json:"direction"`
	Action         string          `json:"action"`
	Classification string          `json:"classification"`
	Fidelity       string          `json:"fidelity"`
	OAP            string          `json:"oap,omitempty"`
	Raw            json.RawMessage `json:"raw"`
}
type ccControl struct {
	Type     string `json:"type"`
	Op       string `json:"op,omitempty"`
	Decision string `json:"decision,omitempty"`
	Status   string `json:"status,omitempty"`
	Expect   string `json:"expect,omitempty"`
	RunID    string `json:"run_id,omitempty"`

	Catalog *protocol.ToolsListResponse `json:"catalog,omitempty"`
}
type ccCorpusMapping struct {
	Index          int    `json:"index"`
	Type           string `json:"type"`
	Direction      string `json:"direction"`
	Classification string `json:"classification"`
	Fidelity       string `json:"fidelity"`
	OAP            string `json:"oap,omitempty"`
}
type ccCorpusOmission struct {
	Index  int    `json:"index"`
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

type ccDecodedFrame struct {
	Message      *rpc.Message
	Observation  any
	Control      *ccControl
	Invalid      error
	UserUUID     string
	ResponseType string
}

func TestClaudeEvidenceCorpus(t *testing.T) {
	for _, pin := range []ccCorpusPin{ccCurrentCorpus, ccFloorCorpus} {
		t.Run(pin.dir, func(t *testing.T) {
			runClaudeEvidenceCorpus(t, pin)
		})
	}
}

func runClaudeEvidenceCorpus(t *testing.T, pin ccCorpusPin) {
	root := ccCorpusRoot(t, pin.dir)
	manifest := ccLoadJSON[ccCorpusManifest](t, filepath.Join(root, "manifest.json"))
	if manifest.Version != 1 || manifest.Adapter != "claude-code-stream-json" || manifest.Tag != pin.sources.CLIVersion || manifest.Sources != pin.sources {
		t.Fatalf("corpus provenance pin mismatch: %+v", manifest)
	}
	seenIDs, seenPaths, covered := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, entry := range manifest.Cases {
		entry := entry
		t.Run(entry.ID, func(t *testing.T) {
			if entry.ID == "" || seenIDs[entry.ID] || seenPaths[entry.Path] || !ccSafeRelative(entry.Path) || len(entry.LedgerFixtures) == 0 {
				t.Fatalf("invalid manifest entry: %+v", entry)
			}
			for _, fixture := range entry.LedgerFixtures {
				if !ccLedgerFixtures[fixture] || covered[fixture] {
					t.Fatalf("invalid or duplicate ledger fixture %q", fixture)
				}
				covered[fixture] = true
			}
			seenIDs[entry.ID], seenPaths[entry.Path] = true, true
			runClaudeCorpusCase(t, root, entry, pin)
		})
	}
	for fixture := range ccLedgerFixtures {
		if !covered[fixture] {
			t.Errorf("ledger fixture %q has no case", fixture)
		}
	}
	assertClaudeCorpusInventory(t, root, manifest)
}

func runClaudeCorpusCase(t *testing.T, root string, entry ccCorpusManifestCase, pin ccCorpusPin) {
	dir := filepath.Join(root, entry.Path)
	definition := ccLoadJSON[ccCorpusCase](t, filepath.Join(dir, "case.json"))
	p := definition.Provenance
	if definition.Version != 1 || definition.ID != entry.ID || p.Repository != ccTSRepository || p.Tag != pin.sources.CLIVersion || p.Sources != pin.sources || len(definition.Capabilities) == 0 || len(definition.IdentityMap) == 0 {
		t.Fatalf("invalid case metadata: %+v", definition)
	}
	frames, decoded := ccLoadFrames(t, filepath.Join(dir, definition.Native))
	mappings := ccLoadOptional[[]ccCorpusMapping](t, filepath.Join(dir, definition.Mapping))
	if len(mappings) == 0 {
		if os.Getenv("OAP_UPDATE_CLAUDE_CORPUS") != "1" {
			t.Fatalf("%s empty; set OAP_UPDATE_CLAUDE_CORPUS=1", filepath.Join(dir, definition.Mapping))
		}
		mappings = make([]ccCorpusMapping, len(frames))
		for i, f := range frames {
			mappings[i] = ccCorpusMapping{Index: i + 1, Type: ccFrameType(f, decoded[i]), Direction: f.Direction, Classification: f.Classification, Fidelity: f.Fidelity, OAP: f.OAP}
		}
		encoded, _ := json.MarshalIndent(mappings, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, definition.Mapping), append(encoded, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	omissions := ccLoadJSON[[]ccCorpusOmission](t, filepath.Join(dir, definition.Omissions))
	assertClaudeClassifications(t, frames, decoded, mappings, omissions)
	assertClaudeCaseActions(t, frames)
	execution := runClaudeScriptedCase(t, definition, frames, decoded)
	assertClaudeLedgerEvidence(t, entry.LedgerFixtures, frames, decoded, &execution)
	assertClaudeExpected(t, filepath.Join(dir, definition.ExpectedOAP), execution.envelopes)
}

func runClaudeScriptedCase(t *testing.T, definition ccCorpusCase, frames []ccFrame, decoded []ccDecodedFrame) ccExecution {
	t.Helper()
	peer := newWirePeer(t)
	var initializeErr chan error
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) {
		initializeErr = make(chan error, 1)
		go func() {
			initializeErr <- peer.client.Call(context.Background(), native.InitializeRequest{Subtype: native.ControlInitialize, Hooks: nil}, &struct{}{})
		}()
		return peer.client, nil
	}), Model: "claude-test", Clock: &testClock{}, IDs: &testIDs{}, JournalCapacity: 64})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := implementation.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.AssertDescriptor(t, descriptor)
	execution := ccExecution{descriptor: descriptor, controlWrites: map[string]int{}}
	ccAssertCapabilities(t, definition, descriptor)
	session := adaptertest.AssertInitialState(t, implementation, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
	impl := session.(*Session)

	type ccSubmit struct {
		admission protocol.MessageSubmitResponse
		stream    base.EventStream
		err       error
	}
	var pending chan ccSubmit
	var submitted *ccSubmit
	initializePending := true
	var cancelDone chan error
	var pendingRequests []string

	reap := func() *ccSubmit {
		if pending == nil {
			return submitted
		}
		select {
		case result := <-pending:
			submitted = &result
		case <-time.After(5 * time.Second):
			t.Fatal("submit did not settle")
		}
		pending = nil
		return submitted
	}

	recordPending := func() {
		if submitted == nil {
			return
		}
		execution.record(t, submitted.admission, submitted.stream, submitted.err)
		submitted = nil
	}

	readRequest := func(label string) rpc.Message {
		t.Helper()
		select {
		case message, ok := <-peer.frames:
			if !ok {
				t.Fatalf("%s: adapter write stream ended", label)
			}
			if message.Kind != rpc.KindControlRequest {
				t.Fatalf("%s: expected a control request, got %+v", label, message)
			}
			execution.controlWrites[message.Subtype]++
			pendingRequests = append(pendingRequests, message.RequestID)
			return message
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: adapter wrote no control request", label)
			return rpc.Message{}
		}
	}
	for i, frame := range frames {
		switch frame.Action {
		case "submit":
			recordPending()
			channel := make(chan ccSubmit, 1)
			pending = channel
			go func() {
				admission, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
				channel <- ccSubmit{admission, stream, err}
			}()
			written, raw := peer.written()
			if written.Kind != rpc.KindObservation || written.Type != rpc.TypeUser {
				t.Fatalf("frame %d: expected a user turn write, got %+v", i+1, written)
			}
			execution.userWrites++
			execution.lastUserUUID = turnUUIDOf(t, ccDecodeFrameMap(t, raw))
			ccAssertWritten(t, raw, frame.Raw, "user turn")
			decoded[i].UserUUID = execution.lastUserUUID
		case "expect-write":
			written, raw := peer.written()
			if written.Kind == rpc.KindControlRequest {
				execution.controlWrites[written.Subtype]++
				if written.Subtype == native.ControlInitialize {
					var envelope struct {
						Request native.InitializeRequest `json:"request"`
					}
					if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Request.Subtype != native.ControlInitialize || envelope.Request.Hooks != nil {
						t.Fatalf("frame %d: initialize shape = %s", i+1, raw)
					}
					execution.initializeShape = true
				}
				pendingRequests = append(pendingRequests, written.RequestID)
			}
			ccAssertWritten(t, raw, frame.Raw, "adapter write")
		case "reply":
			if len(pendingRequests) == 0 {
				t.Fatalf("frame %d: reply without a pending adapter request", i+1)
			}
			id := pendingRequests[0]
			pendingRequests = pendingRequests[1:]
			peer.answerControl(id, ccReplyPayload(t, frame.Raw))
			if initializePending {
				select {
				case err := <-initializeErr:
					if err != nil {
						t.Fatalf("frame %d: initialize exchange failed: %v", i+1, err)
					}
					execution.initializeReply = true
				case <-time.After(5 * time.Second):
					t.Fatal("initialize exchange did not settle")
				}
				initializePending = false
			}
			if cancelDone != nil {
				select {
				case err := <-cancelDone:
					if err != nil {
						t.Fatalf("frame %d: cancel failed: %v", i+1, err)
					}
					execution.cancelAccepted = true
				case <-time.After(5 * time.Second):
					t.Fatal("cancel did not settle")
				}
				cancelDone = nil
			}
		case "observe", "decode-error":
			peer.send(string(ccWireBytes(t, frame.Raw)))
		case "oap-control":
			control := decoded[i].Control
			switch control.Op {
			case "resolve":
				gate := ccOpenGate(t, impl)
				decision := control.Decision
				if decision != "allow" && decision != "deny" {
					t.Fatalf("frame %d: invalid resolve decision %q", i+1, decision)
				}
				if err := session.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: gate.id, SessionID: "session", Answers: []protocol.InputAnswer{{QuestionID: "decision", SelectedOptionIDs: []string{decision}}}}}); err != nil {
					t.Fatalf("frame %d: resolve: %v", i+1, err)
				}
			case "cancel":
				current := reap()
				if current == nil {
					t.Fatalf("frame %d: cancel without an admitted run", i+1)
				}
				channel := make(chan error, 1)
				cancelDone = channel
				execution.cancelsIssued++
				runID := current.admission.RunID
				go func() {
					_, err := session.Cancel(context.Background(), runID)
					channel <- err
				}()
				message := readRequest("cancel")
				if message.Subtype != native.ControlInterrupt {
					t.Fatalf("frame %d: cancel wrote %q, want interrupt", i+1, message.Subtype)
				}
			case "assert-catalog":

				lister, ok := session.(base.ToolLister)
				if !ok {
					t.Fatalf("frame %d: the session serves no catalog", i+1)
				}

				var degraded *base.DegradedControlError
				if _, err := lister.Tools(context.Background(), protocol.ToolsListRequest{SessionID: "session"}); !errors.As(err, &degraded) || degraded.Feature != protocol.FeatureToolsList {
					t.Fatalf("frame %d: a catalog was served without the degraded opt-in (err = %v)", i+1, err)
				}
				request := protocol.ToolsListRequest{SessionID: "session", AllowDegradedFeatures: []string{protocol.FeatureToolsList}}
				catalog, err := lister.Tools(context.Background(), request)
				if err != nil {
					t.Fatalf("frame %d: tools: %v", i+1, err)
				}
				if control.Catalog == nil {
					t.Fatalf("frame %d: assert-catalog declares no expected catalog", i+1)
				}
				got, err := json.Marshal(catalog.Tools)
				if err != nil {
					t.Fatal(err)
				}
				want, err := json.Marshal(*control.Catalog)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("frame %d: projected catalog\n got: %s\nwant: %s", i+1, got, want)
				}
				adaptertest.AssertToolCatalog(t, execution.descriptor, protocol.SessionOpenRequest{}, request, catalog)
				execution.catalogs = append(execution.catalogs, catalog.Tools)

				if catalog.Tools.SessionID != "" {
					served := catalog
					execution.served, execution.servedRequest = &served, request
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
				execution.modelIDs = append(execution.modelIDs, state.CurrentModelID)
			case "resume":
				if control.Expect == "run-not-found" {
					if _, stream, err := session.Resume(context.Background(), base.ResumeRequest{RunID: protocol.RunID(control.RunID)}); !errors.Is(err, base.ErrRunNotFound) || stream != nil {
						t.Fatalf("frame %d: resume error = %v", i+1, err)
					}
					execution.resumeRefused++
					break
				}
				if len(execution.runs) == 0 || len(execution.runs[len(execution.runs)-1]) == 0 {
					t.Fatalf("frame %d: no delivered run to replay", i+1)
				}
				delivered := execution.runs[len(execution.runs)-1]
				recovery, stream, err := session.Resume(context.Background(), base.ResumeRequest{RunID: delivered[0].RunID})
				if err != nil || recovery.ReplayGap != nil {
					t.Fatalf("frame %d: resume error = %v", i+1, err)
				}
				want, err := json.Marshal(delivered)
				if err != nil {
					t.Fatal(err)
				}
				got, err := json.Marshal(adaptertest.Drain(t, stream, 5*time.Second))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("frame %d: replay %s differs from delivery %s", i+1, got, want)
				}
				execution.resumeReplayed++
			case "close":
				recordPending()
				if err := session.Close(context.Background()); err != nil {
					t.Fatalf("frame %d: close: %v", i+1, err)
				}
				execution.closed = true
			default:
				t.Fatalf("frame %d: unsupported oap control %q", i+1, control.Op)
			}
		case "process-exit":
			_ = peer.client.Close()
		case "drain":
			peer.awaitDrain()
		case "wait-run":
			reap()
			recordPending()
		default:
			t.Fatalf("frame %d: unsupported action %q", i+1, frame.Action)
		}
	}
	reap()
	recordPending()
	if pending != nil || cancelDone != nil || initializePending || len(pendingRequests) != 0 {
		t.Fatal("transcript ended with an unsettled exchange")
	}
	for subtype := range execution.controlWrites {
		switch subtype {
		case native.ControlInitialize, native.ControlInterrupt:
		default:
			t.Fatalf("adapter wrote unsupported control subtype %q", subtype)
		}
	}
	return execution
}

type ccExecution struct {
	descriptor      base.Descriptor
	admissions      []protocol.MessageSubmitResponse
	submitErrors    []error
	envelopes       []protocol.Envelope
	runs            [][]protocol.Envelope
	userWrites      int
	lastUserUUID    string
	controlWrites   map[string]int
	initializeShape bool
	initializeReply bool
	cancelAccepted  bool
	cancelsIssued   int
	resumeReplayed  int
	resumeRefused   int
	assertStates    []string
	modelIDs        []string
	catalogs        []protocol.ToolsListResponse

	servedRequest protocol.ToolsListRequest
	served        *base.ToolCatalog
	closed        bool
}

func (e *ccExecution) record(t *testing.T, admission protocol.MessageSubmitResponse, stream base.EventStream, err error) {
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
		switch {
		case e.cancelAccepted:
			assertCancelledTrace(t, admission, events)
		case e.served != nil:
			adaptertest.AssertProtocolValidWithCatalog(t, admission, testDescriptor(t), e.servedRequest, *e.served, events)
		default:
			assertValidTrace(t, admission, events)
		}
		e.runs = append(e.runs, events)
	} else {
		e.runs = append(e.runs, nil)
	}
	e.envelopes = append(e.envelopes, events...)
}

func ccLoadFrames(t *testing.T, filename string) ([]ccFrame, []ccDecodedFrame) {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if len(lines) == 0 || len(lines[0]) == 0 {
		t.Fatal("empty native transcript")
	}
	frames := make([]ccFrame, 0, len(lines))
	decoded := make([]ccDecodedFrame, 0, len(lines))
	lastRequestSubtype := ""
	for i, line := range lines {
		var frame ccFrame
		ccDecodeStrict(t, line, &frame, fmt.Sprintf("%s frame %d", filename, i+1))
		entry := ccDecodedFrame{}
		switch frame.Direction {
		case "cli-to-host":
			wire := ccWireBytes(t, frame.Raw)
			message, decodeErr := rpc.ParseMessage(wire)
			if decodeErr == nil {
				switch message.Kind {
				case rpc.KindObservation:
					value, nativeErr := native.DecodeObservation(message.Type, message.Subtype, message.Raw)
					if nativeErr != nil {
						decodeErr = nativeErr
					} else {
						entry.Observation = value
					}
				case rpc.KindControlRequest:
					value, nativeErr := native.DecodeControlRequest(message.Subtype, message.Raw)
					if nativeErr != nil {
						decodeErr = nativeErr
					} else {
						entry.Observation = value
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
				if message.Kind == rpc.KindControlResponse {
					entry.ResponseType = "response:" + lastRequestSubtype
				}
			}
		case "host-to-cli":
			wire := ccWireBytes(t, frame.Raw)
			message, err := rpc.ParseMessage(wire)
			if err != nil {
				t.Fatalf("frame %d host write decode: %v", i+1, err)
			}
			if message.Kind == rpc.KindControlRequest {
				lastRequestSubtype = message.Subtype
			}
			entry.Message = &message
			var holder struct {
				UUID string `json:"uuid"`
			}
			_ = json.Unmarshal(message.Raw, &holder)
			entry.UserUUID = holder.UUID
		case "harness-control":
			var control ccControl
			ccDecodeStrict(t, frame.Raw, &control, fmt.Sprintf("%s frame %d", filename, i+1))
			switch control.Type {
			case "process_exit":
			case "oap_control":
				switch control.Op {
				case "resolve":
					if control.Decision != "allow" && control.Decision != "deny" {
						t.Fatalf("frame %d invalid resolve decision", i+1)
					}
				case "assert-state":
					if control.Status != "idle" && control.Status != "running" {
						t.Fatalf("frame %d invalid assert-state status", i+1)
					}
				case "cancel":

					lastRequestSubtype = native.ControlInterrupt
				case "assert-catalog":
					if control.Catalog == nil {
						t.Fatalf("frame %d assert-catalog declares no catalog", i+1)
					}
				case "resume":
					if (control.Expect != "replay" || control.RunID != "") && (control.Expect != "run-not-found" || control.RunID == "") {
						t.Fatalf("frame %d invalid resume expectation", i+1)
					}
				case "close":
				default:
					t.Fatalf("frame %d invalid oap control op %q", i+1, control.Op)
				}
			case "harness_sync":
				if control.Op != "drain" && control.Op != "wait-run" {
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
	return frames, decoded
}

func ccWireBytes(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var literal string
		if err := json.Unmarshal(trimmed, &literal); err != nil {
			t.Fatal(err)
		}
		return []byte(literal)
	}
	return raw
}

func ccReplyPayload(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var envelope struct {
		Response struct {
			RequestID string          `json:"request_id"`
			Response  json.RawMessage `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Response.RequestID != "@request" {
		t.Fatalf("reply raw is not a placeholder-addressed control_response: %s", raw)
	}
	payload := []byte("{}")
	if len(envelope.Response.Response) > 0 {
		payload = envelope.Response.Response
	}
	return string(payload)
}

func ccAssertWritten(t *testing.T, actual, expected json.RawMessage, label string) {
	t.Helper()
	if bytes.Contains(expected, []byte(`"@request"`)) {
		actual = ccNormalizeRequestIDs(t, actual)
	}
	if !ccJSONEqual(actual, expected) {
		t.Fatalf("%s: adapter wrote %s, want %s", label, actual, expected)
	}
}

func ccNormalizeRequestIDs(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	var rewrite func(node any) any
	rewrite = func(node any) any {
		object, ok := node.(map[string]any)
		if !ok {
			return node
		}
		if _, isString := object["request_id"].(string); isString {
			object["request_id"] = "@request"
		}
		for key, child := range object {
			object[key] = rewrite(child)
		}
		return object
	}
	encoded, err := json.Marshal(rewrite(value))
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func ccJSONEqual(a, b json.RawMessage) bool {
	var va, vb any
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		return false
	}
	na, _ := json.Marshal(va)
	nb, _ := json.Marshal(vb)
	return bytes.Equal(na, nb)
}

func ccDecodeFrameMap(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatal(err)
	}
	return frame
}

func ccOpenGate(t *testing.T, impl *Session) *gateState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		impl.reduceMu.Lock()
		var found *gateState
		for _, candidate := range impl.interactions {
			if !candidate.resolved {
				found = candidate
			}
		}
		impl.reduceMu.Unlock()
		if found != nil {
			return found
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("permission gate never opened")
	return nil
}

func ccAssertCapabilities(t *testing.T, definition ccCorpusCase, descriptor base.Descriptor) {
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

func assertClaudeCaseActions(t *testing.T, frames []ccFrame) {
	t.Helper()
	for i, frame := range frames {
		var valid map[string]bool
		switch frame.Direction {
		case "cli-to-host":
			valid = map[string]bool{"observe": true, "reply": true, "decode-error": true}
		case "host-to-cli":
			valid = map[string]bool{"submit": true, "expect-write": true}
		case "harness-control":
			valid = map[string]bool{"oap-control": true, "process-exit": true, "drain": true, "wait-run": true}
		}
		if valid == nil || !valid[frame.Action] {
			t.Fatalf("frame %d has unknown action %q for %q", i+1, frame.Action, frame.Direction)
		}
	}
	if len(frames) == 0 || frames[0].Action != "expect-write" {
		t.Fatal("transcript does not open with the initialize exchange")
	}
}

func ccFrameType(frame ccFrame, decoded ccDecodedFrame) string {
	switch frame.Direction {
	case "harness-control":
		if decoded.Control.Type == "process_exit" {
			return "process_exit"
		}
		if decoded.Control.Type == "harness_sync" {
			return "sync:" + decoded.Control.Op
		}
		return "oap_control:" + decoded.Control.Op
	case "host-to-cli":
		if decoded.Message == nil {
			return "write:unknown"
		}
		switch decoded.Message.Kind {
		case rpc.KindControlRequest:
			return "write:control_request/" + decoded.Message.Subtype
		case rpc.KindControlResponse:
			return "write:control_response"
		default:
			return "write:" + decoded.Message.Type
		}
	case "cli-to-host":
		if decoded.Invalid != nil {
			return "codec-error"
		}
		if decoded.Message == nil {
			return "codec-error"
		}
		switch decoded.Message.Kind {
		case rpc.KindControlResponse:
			return decoded.ResponseType
		case rpc.KindControlRequest:
			return "observe:control_request/" + decoded.Message.Subtype
		default:
			if decoded.Message.Subtype != "" {
				return "observe:" + decoded.Message.Type + "/" + decoded.Message.Subtype
			}
			return "observe:" + decoded.Message.Type
		}
	}
	return "codec-error"
}

func assertClaudeClassifications(t *testing.T, frames []ccFrame, decoded []ccDecodedFrame, mappings []ccCorpusMapping, omissions []ccCorpusOmission) {
	t.Helper()
	if len(frames) != len(mappings) {
		t.Fatalf("mapping count %d does not cover %d frames", len(mappings), len(frames))
	}
	omitted := map[int]ccCorpusOmission{}
	for _, o := range omissions {
		if o.Index < 1 || o.Index > len(frames) || o.Reason == "" || omitted[o.Index].Index != 0 {
			t.Fatalf("invalid omission: %+v", o)
		}
		omitted[o.Index] = o
	}
	for i, f := range frames {
		typ := ccFrameType(f, decoded[i])
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

func ccObserveIndexes(decoded []ccDecodedFrame, frameType, subtype string) []int {
	var out []int
	for i := range decoded {
		if decoded[i].Message == nil || decoded[i].Message.Kind != rpc.KindObservation {
			continue
		}
		if decoded[i].Message.Type == frameType && (subtype == "" || decoded[i].Message.Subtype == subtype) {
			out = append(out, i)
		}
	}
	return out
}

func ccHasObservation(decoded []ccDecodedFrame, frameType, subtype string) bool {
	return len(ccObserveIndexes(decoded, frameType, subtype)) > 0
}

func ccEnvelopeCount(execution ccExecution, typ protocol.EnvelopeType) int {
	count := 0
	for _, envelope := range execution.envelopes {
		if envelope.Type == typ {
			count++
		}
	}
	return count
}

func ccRunTerminal(events []protocol.Envelope) (protocol.EnvelopeType, string, string) {
	for _, envelope := range events {
		switch envelope.Type {
		case protocol.TypeRunCompleted:
			var payload protocol.RunCompletedPayload
			text, stop := "", ""
			if envelope.DecodePayload(&payload) == nil {
				text, _ = payload.FinalResponse.Content.Text()
				stop = payload.StopReason
			}
			return envelope.Type, text, stop
		case protocol.TypeRunFailed:
			var payload protocol.RunFailedPayload
			code := ""
			if envelope.DecodePayload(&payload) == nil {
				code = payload.Error.Code
			}
			return envelope.Type, code, payload.Error.Message
		case protocol.TypeRunCancelled:
			return envelope.Type, "", ""
		}
	}
	return "", "", ""
}

func ccResultEchoMatches(frame ccFrame, uuid string) bool {
	if uuid == "" {
		return false
	}
	var holder struct {
		UserMessageUUID string   `json:"user_message_uuid"`
		UUIDs           []string `json:"user_message_uuids"`
	}
	if json.Unmarshal(frame.Raw, &holder) != nil {
		return false
	}
	if holder.UserMessageUUID == uuid {
		return true
	}
	for _, candidate := range holder.UUIDs {
		if candidate == uuid {
			return true
		}
	}
	return false
}

func ccAnyRun(execution ccExecution, probe func(typ protocol.EnvelopeType, a, b string) bool) bool {
	for _, events := range execution.runs {
		if len(events) == 0 {
			continue
		}
		typ, a, b := ccRunTerminal(events)
		if typ != "" && probe(typ, a, b) {
			return true
		}
	}
	return false
}

func ccCallSources(runs [][]protocol.Envelope, tool string) []string {
	var sources []string
	for _, events := range runs {
		for _, envelope := range events {
			if envelope.Type != protocol.TypeActionCallRequested {
				continue
			}
			var payload protocol.ActionCallPayload
			if err := envelope.DecodePayload(&payload); err != nil || payload.Name != tool {
				continue
			}
			sources = append(sources, payload.Source)
		}
	}
	return sources
}

func assertClaudeLedgerEvidence(t *testing.T, labels []string, frames []ccFrame, decoded []ccDecodedFrame, execution *ccExecution) {
	t.Helper()
	for _, label := range labels {
		ok := true
		switch label {
		case "initialize-minimal":
			feature, advertised := execution.descriptor.Capabilities.Features["protocol.initialize"]
			ok = execution.initializeShape && execution.initializeReply && execution.userWrites > 0 &&
				advertised && feature.Level == protocol.SupportEmulated
		case "tools-catalog-sources":

			inits := ccObserveIndexes(decoded, native.TypeSystem, native.SystemInit)
			ok = len(inits) == 2 && len(execution.catalogs) == 2
			if ok {
				before := execution.catalogs[0]
				ok = len(before.Tools) == 0 && len(before.Sources) == 1 && before.Sources[0].ID == nativeToolSource
			}
			if ok {
				catalog := execution.catalogs[1]
				declared, attributed := map[string]bool{}, map[string]string{}
				for _, source := range catalog.Sources {
					declared[source.ID] = true
				}
				for _, tool := range catalog.Tools {
					attributed[tool.Name] = tool.Source
				}
				ok = len(catalog.Sources) == 3 && declared[nativeToolSource] &&
					declared[mcpSourcePrefix+"files"] && declared[mcpSourcePrefix+"files__nested"] &&
					attributed["mcp__files__read_file"] == mcpSourcePrefix+"files" &&

					attributed["mcp__files__nested__read"] == mcpSourcePrefix+"files__nested" &&

					attributed["mcp__absent__ghost"] == nativeToolSource &&
					attributed["Bash"] == nativeToolSource
				for _, tool := range catalog.Tools {
					if !declared[tool.Source] || tool.ExecutionOwner != harnessOwner {
						ok = false
					}
				}
			}
			if ok {

				sources := ccCallSources(execution.runs, "mcp__files__read_file")
				ok = len(sources) == 2 && sources[0] == "" && sources[1] == mcpSourcePrefix+"files"
			}
		case "per-turn-init":
			inits := ccObserveIndexes(decoded, native.TypeSystem, native.SystemInit)
			submits := 0
			for i := range frames {
				if frames[i].Action == "submit" {
					submits++
				}
			}
			ok = len(inits) == submits && submits >= 2 && len(execution.modelIDs) > 0 &&
				execution.modelIDs[len(execution.modelIDs)-1] == "claude-sonnet-4-5"
		case "second-turn":
			var uuids []string
			for i := range frames {
				if frames[i].Action == "submit" {
					uuids = append(uuids, decoded[i].UserUUID)
				}
			}
			distinct := len(uuids) == 2 && uuids[0] != uuids[1] && uuids[0] != "" && uuids[1] != ""
			ok = distinct && len(execution.admissions) == 2 &&
				execution.admissions[0].RunID != execution.admissions[1].RunID &&
				execution.admissions[0].SubmissionID != execution.admissions[1].SubmissionID &&
				execution.userWrites == 2
			for _, events := range execution.runs {
				ok = ok && len(events) > 0
			}
		case "message-admitted":
			echoed := false
			for i := range decoded {
				if decoded[i].Message == nil || decoded[i].Message.Kind != rpc.KindObservation || decoded[i].Message.Type == native.TypeResult {
					continue
				}
				if ccResultEchoMatches(frames[i], execution.lastUserUUID) {
					echoed = true
				}
			}
			ok = echoed && len(execution.admissions) > 0 &&
				execution.admissions[0].Admission == protocol.AdmissionStarted &&
				execution.admissions[0].RunID != "" && len(execution.submitErrors) == 0
		case "command-lifecycle":
			states := map[string]bool{}
			corroborated := true
			for _, i := range ccObserveIndexes(decoded, native.TypeCommandLifecycle, "") {
				lifecycle, is := decoded[i].Observation.(*native.CommandLifecycleFrame)
				if !is {
					continue
				}
				states[lifecycle.State] = true
				corroborated = corroborated && frames[i].Classification == "observed-only"
			}
			ok = states["queued"] && states["started"] && corroborated && len(execution.admissions) > 0
		case "injected-turn-origin":
			var injected []string
			for _, i := range ccObserveIndexes(decoded, native.TypeUser, "") {
				if user, is := decoded[i].Observation.(*native.UserFrame); is && user.Origin != nil && user.Origin.Kind != "human" {
					injected = append(injected, ccUserText(user))
				}
			}
			for _, i := range ccObserveIndexes(decoded, native.TypeResult, "") {
				if result, is := decoded[i].Observation.(*native.ResultFrame); is && result.Origin != nil && result.Origin.Kind != "human" {
					injected = append(injected, result.Result)
				}
			}
			leaked, blank := false, false
			for _, text := range injected {
				blank = blank || text == ""
				for _, envelope := range execution.envelopes {
					data, _ := json.Marshal(envelope.Payload)
					leaked = leaked || strings.Contains(string(data), text)
				}
			}
			ok = len(injected) > 0 && !blank && !leaked && len(execution.admissions) == 1 &&
				len(execution.runs) == 1 && len(execution.assertStates) > 0 &&
				execution.assertStates[len(execution.assertStates)-1] == "idle"
		case "queued-turn-count":
			deferred := false
			for _, i := range ccObserveIndexes(decoded, native.TypeResult, "") {
				result, is := decoded[i].Observation.(*native.ResultFrame)
				if is && result.QueuedTurnCount != nil && *result.QueuedTurnCount > 0 && ccResultEchoMatches(frames[i], execution.lastUserUUID) {
					deferred = true
				}
			}
			_, text, _ := ccRunTerminal(execution.runs[len(execution.runs)-1])
			ok = deferred && len(execution.admissions) == 1 && text == "closing"
		case "completed-text":
			ok = ccAnyRun(*execution, func(typ protocol.EnvelopeType, text, stop string) bool {
				return typ == protocol.TypeRunCompleted && text == "fixture response" && stop != "" && stop != "max_turns"
			}) && ccHasObservation(decoded, native.TypeResult, native.ResultSuccess)
		case "max-turns":
			ok = ccAnyRun(*execution, func(typ protocol.EnvelopeType, _, stop string) bool {
				return typ == protocol.TypeRunCompleted && stop == "max_turns"
			}) && ccHasObservation(decoded, native.TypeResult, native.ResultErrorMaxTurns)
		case "api-error-result":
			reported := ""
			for _, i := range ccObserveIndexes(decoded, native.TypeResult, "") {
				if result, is := decoded[i].Observation.(*native.ResultFrame); is && result.APIErrorStatus != nil && *result.APIErrorStatus == 429 {
					reported = strings.TrimSpace(result.Result)
				}
			}
			ok = reported != "" && ccAnyRun(*execution, func(typ protocol.EnvelopeType, code, message string) bool {
				return typ == protocol.TypeRunFailed && code == "claude_api_429" && message == reported
			})
		case "error-result":
			ok = ccAnyRun(*execution, func(typ protocol.EnvelopeType, code, _ string) bool {
				return typ == protocol.TypeRunFailed && code == "claude_error_during_execution"
			})
		case "interrupt-cancel":
			cancelled := ccAnyRun(*execution, func(typ protocol.EnvelopeType, _, _ string) bool {
				return typ == protocol.TypeRunCancelled
			})
			aborted, synthetic := false, false
			for _, i := range ccObserveIndexes(decoded, native.TypeResult, "") {
				if result, is := decoded[i].Observation.(*native.ResultFrame); is && result.Cancelled() {
					aborted = true
				}
			}
			for _, i := range ccObserveIndexes(decoded, native.TypeUser, "") {
				if user, is := decoded[i].Observation.(*native.UserFrame); is && strings.Contains(ccUserText(user), "interrupted by user") {
					synthetic = true
				}
			}
			ok = cancelled && aborted && synthetic &&
				execution.cancelsIssued == 1 && execution.cancelAccepted &&
				execution.controlWrites[native.ControlInterrupt] == 1
		case "process-exit":
			control := false
			for i := range frames {
				if frames[i].Action == "process-exit" {
					control = true
				}
			}
			ok = control && ccAnyRun(*execution, func(typ protocol.EnvelopeType, code, _ string) bool {
				return typ == protocol.TypeRunFailed && code == "claude_process_exit"
			}) && execution.closed
		case "malformed-stdout-line":
			rejected := false
			for i := range decoded {
				if frames[i].Action == "decode-error" && decoded[i].Invalid != nil {
					rejected = true
				}
			}
			ok = rejected && ccAnyRun(*execution, func(typ protocol.EnvelopeType, code, message string) bool {
				return typ == protocol.TypeRunFailed && code == "claude_process_exit" && strings.Contains(message, "invalid")
			})
		case "streaming-deltas":
			texts, thinking := 0, 0
			for _, i := range ccObserveIndexes(decoded, native.TypeStreamEvent, "") {
				event, is := decoded[i].Observation.(*native.StreamEventFrame)
				if !is {
					continue
				}
				if kind, _, isDelta := event.StreamDelta(); isDelta {
					if kind == "thinking" {
						thinking++
					} else {
						texts++
					}
				}
			}
			projectedText, projectedThinking := 0, 0
			for _, envelope := range execution.envelopes {
				if envelope.Type != protocol.TypeContentDelta {
					continue
				}
				var payload protocol.ContentDeltaPayload
				if envelope.DecodePayload(&payload) == nil {
					if payload.Part.Type == protocol.ContentText {
						projectedText++
					}
					if payload.Part.Type == protocol.ContentReasoning {
						projectedThinking++
					}
				}
			}
			ok = texts >= 2 && thinking >= 1 && projectedText == texts && projectedThinking == thinking
		case "interleaved-blocks":
			complete := 0
			for _, i := range ccObserveIndexes(decoded, native.TypeAssistant, "") {
				assistant, is := decoded[i].Observation.(*native.AssistantFrame)
				if is && assistant.ParentToolUseID == nil {
					for _, block := range assistant.Message.Content {
						if block.Type == "text" {
							complete++
						}
					}
				}
			}
			var projected []string
			for _, envelope := range execution.envelopes {
				if envelope.Type != protocol.TypeContentDelta {
					continue
				}
				var payload protocol.ContentDeltaPayload
				if envelope.DecodePayload(&payload) == nil && payload.Part.Type == protocol.ContentText {
					projected = append(projected, payload.Part.Text)
				}
			}
			ok = complete >= 1 && strings.Join(projected, "") == "fixture response"
		case "tool-roundtrip":
			ok = ccEnvelopeCount(*execution, protocol.TypeActionCallRequested) > 0 &&
				ccEnvelopeCount(*execution, protocol.TypeActionCallStarted) > 0 &&
				ccEnvelopeCount(*execution, protocol.TypeActionCallCompleted) > 0
		case "tool-failed":
			failed := ccEnvelopeCount(*execution, protocol.TypeActionCallFailed)
			errored := false
			for _, i := range ccObserveIndexes(decoded, native.TypeUser, "") {
				user, is := decoded[i].Observation.(*native.UserFrame)
				if !is {
					continue
				}
				blocks, isBlocks := user.Blocks()
				if !isBlocks {
					continue
				}
				for _, block := range blocks {
					if block.Type == "tool_result" && block.IsError != nil && *block.IsError {
						errored = true
					}
				}
			}
			ok = errored && failed == 1
		case "tool-progress":
			progress := false
			for _, i := range ccObserveIndexes(decoded, native.TypeToolProgress, "") {
				progress = frames[i].Classification == "observed-only"
			}
			ok = progress && ccHasObservation(decoded, native.TypeToolProgress, "") &&
				ccEnvelopeCount(*execution, protocol.TypeRunCompleted) > 0
		case "auto-approved-tool":

			span := false
			for i := range decoded {
				if decoded[i].Message == nil || decoded[i].Message.Kind != rpc.KindObservation || decoded[i].Message.Type != native.TypeAssistant {
					continue
				}
				assistant, is := decoded[i].Observation.(*native.AssistantFrame)
				if !is {
					continue
				}
				for _, block := range assistant.Message.Content {
					if block.Type != "tool_use" {
						continue
					}
					closed, askBetween := false, false
					for j := i + 1; j < len(decoded); j++ {
						if decoded[j].Message == nil {
							continue
						}
						if decoded[j].Message.Kind == rpc.KindControlRequest && decoded[j].Message.Subtype == native.ControlCanUseTool {
							askBetween = true
						}
						if decoded[j].Message.Kind == rpc.KindObservation && decoded[j].Message.Type == native.TypeUser {
							if user, is := decoded[j].Observation.(*native.UserFrame); is {
								if blocks, isBlocks := user.Blocks(); isBlocks {
									for _, done := range blocks {
										if done.Type == "tool_result" && done.ToolUseID == block.ID {
											closed = true
										}
									}
								}
							}
						}
						if closed {
							span = span || !askBetween
							break
						}
					}
				}
			}
			ok = span && ccEnvelopeCount(*execution, protocol.TypeUserInputRequested) == 0 &&
				ccEnvelopeCount(*execution, protocol.TypeActionCallCompleted) > 0
		case "permission-gate":
			ask := ccHasControlRequest(decoded, native.ControlCanUseTool)
			allow := false
			for _, envelope := range execution.envelopes {
				if envelope.Type != protocol.TypeUserInputResolved {
					continue
				}
				var payload protocol.UserInputResolvedPayload
				if envelope.DecodePayload(&payload) == nil && payload.Status == protocol.InputSubmitted {
					allow = true
				}
			}
			ok = ask && allow && ccEnvelopeCount(*execution, protocol.TypeUserInputRequested) > 0 &&
				ccEnvelopeCount(*execution, protocol.TypeActionCallCompleted) > 0
		case "permission-deny":
			denied := false
			for i := range frames {
				if frames[i].Action == "expect-write" && bytes.Contains(frames[i].Raw, []byte(`"deny"`)) {
					denied = true
				}
			}
			ok = denied && ccEnvelopeCount(*execution, protocol.TypeActionCallFailed) > 0 &&
				ccEnvelopeCount(*execution, protocol.TypeRunCompleted) > 0
		case "subagent-task":
			started := ccHasObservation(decoded, native.TypeSystem, native.SystemTaskStarted)
			notified := ccHasObservation(decoded, native.TypeSystem, native.SystemTaskNotification)
			completed := ccAnyRun(*execution, func(typ protocol.EnvelopeType, _, _ string) bool {
				return typ == protocol.TypeRunCompleted
			})
			ok = started && notified && completed
		case "task-updated-terminal":
			updated := false
			for _, i := range ccObserveIndexes(decoded, native.TypeSystem, native.SystemTaskUpdated) {
				if patch, is := decoded[i].Observation.(*native.TaskUpdatedFrame); is && patch.Terminal() {
					updated = true
				}
			}
			completed := ccAnyRun(*execution, func(typ protocol.EnvelopeType, _, _ string) bool {
				return typ == protocol.TypeRunCompleted
			})
			ok = updated && completed
		case "stop-task":

			onlyPinned := execution.controlWrites[native.ControlInitialize] >= 1
			for subtype := range execution.controlWrites {
				switch subtype {
				case native.ControlInitialize, native.ControlInterrupt:
				default:
					onlyPinned = false
				}
			}
			ok = onlyPinned && execution.descriptor.CancellationImplementation == "interrupt control request"
		case "keep-alive-ignored":
			leaked := false
			for _, envelope := range execution.envelopes {
				data, _ := json.Marshal(envelope.Payload)
				if bytes.Contains(data, []byte("keep_alive")) {
					leaked = true
				}
			}
			ok = ccHasObservation(decoded, native.TypeKeepAlive, "") && !leaked &&
				ccEnvelopeCount(*execution, protocol.TypeRunCompleted) > 0
		case "unknown-frame-ignored":
			tolerated := false
			for i := range decoded {
				if decoded[i].Message != nil && decoded[i].Message.Type == "prompt_suggestion" {
					tolerated = frames[i].Classification == "observed-only"
				}
			}
			ok = tolerated && ccEnvelopeCount(*execution, protocol.TypeRunFailed) == 0 &&
				ccEnvelopeCount(*execution, protocol.TypeRunCompleted) > 0
		case "no-implied-replay":
			feature, advertised := execution.descriptor.Capabilities.Features["run.replay"]
			journal := execution.descriptor.Journal
			ok = execution.resumeReplayed >= 1 && advertised && feature.Level == protocol.SupportDegraded &&
				journal.Replay == protocol.SupportDegraded && journal.Persistence == "process_memory"
		case "resume-fork":
			feature, advertised := execution.descriptor.Capabilities.Features["run.resume"]
			ok = execution.resumeRefused >= 1 && advertised && feature.Level == protocol.SupportDegraded
		default:
			t.Fatalf("ledger label %q has no evidence rule", label)
		}
		if !ok {
			var trace []string
			for _, envelope := range execution.envelopes {
				trace = append(trace, string(envelope.Type))
			}
			t.Fatalf("ledger label %q lacks executable evidence (envelopes: %v; admissions: %d; errors: %v; states: %v; writes: %v; models: %v)", label, trace, len(execution.admissions), execution.submitErrors, execution.assertStates, execution.controlWrites, execution.modelIDs)
		}
	}
}

func ccUserText(user *native.UserFrame) string {
	if text, ok := user.TextContent(); ok {
		return text
	}
	blocks, _ := user.Blocks()
	var texts []string
	for _, block := range blocks {
		if block.Type == "text" {
			texts = append(texts, block.Text)
		}
	}
	return strings.Join(texts, "")
}

func ccHasControlRequest(decoded []ccDecodedFrame, subtype string) bool {
	for i := range decoded {
		if decoded[i].Message != nil && decoded[i].Message.Kind == rpc.KindControlRequest && decoded[i].Message.Subtype == subtype {
			return true
		}
	}
	return false
}

func assertClaudeExpected(t *testing.T, filename string, events []protocol.Envelope) {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var expected []protocol.Envelope
	if len(bytes.TrimSpace(data)) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("[]")) {
		if os.Getenv("OAP_UPDATE_CLAUDE_CORPUS") != "1" {
			t.Fatalf("%s empty; set OAP_UPDATE_CLAUDE_CORPUS=1", filename)
		}
		encoded, _ := json.MarshalIndent(events, "", "  ")
		if err := os.WriteFile(filename, append(encoded, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		expected = events
	} else {
		ccDecodeStrict(t, data, &expected, filename)
	}
	if !ccEqualEvents(expected, events) {
		want, _ := json.MarshalIndent(expected, "", "  ")
		got, _ := json.MarshalIndent(events, "", "  ")
		t.Fatalf("normalized trace mismatch\nwant: %s\ngot: %s", want, got)
	}
}

func ccEqualEvents(a, b []protocol.Envelope) bool {
	return bytes.Equal(ccNormalizeTrace(a), ccNormalizeTrace(b))
}

func ccNormalizeTrace(events []protocol.Envelope) []byte {
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

func ccLoadJSON[T any](t *testing.T, filename string) T {
	t.Helper()
	var value T
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	ccDecodeStrict(t, data, &value, filename)
	return value
}

func ccLoadOptional[T any](t *testing.T, filename string) T {
	t.Helper()
	var value T
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return value
	}
	ccDecodeStrict(t, data, &value, filename)
	return value
}

func ccDecodeStrict(t *testing.T, data []byte, value any, label string) {
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

func assertClaudeCorpusInventory(t *testing.T, root string, manifest ccCorpusManifest) {
	t.Helper()
	listed := map[string]bool{"manifest.json": true}
	for _, e := range manifest.Cases {
		d := ccLoadJSON[ccCorpusCase](t, filepath.Join(root, e.Path, "case.json"))
		for _, name := range []string{"case.json", d.Native, d.ExpectedOAP, d.Mapping, d.Omissions} {
			if !ccSafeRelative(name) || filepath.Base(name) != name {
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

func ccSafeRelative(path string) bool {
	clean := filepath.Clean(path)
	return path != "" && !filepath.IsAbs(path) && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func ccCorpusRoot(t *testing.T, dir string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "fixtures", "adapters", dir)
}

func TestClaudeCorpusPinConstants(t *testing.T) {
	if CapabilityRevision == "" || PinnedVersion == "" {
		t.Fatal("missing Claude Code adapter pin")
	}
	for _, pin := range []ccCorpusPin{ccCurrentCorpus, ccFloorCorpus} {
		s := pin.sources
		if pin.dir != "claude-code-"+s.CLIVersion || s.NPMTarballSHA256 == "" || s.LinuxTarballSHA == "" || s.LinuxBinarySHA == "" || s.LinuxBinaryBytes == 0 || s.BuildCommit == "" || s.BuildDate == "" {
			t.Fatalf("missing Claude Code corpus pin: %+v", pin)
		}
		if s.TSSDKTarballSHA == "" || s.TSSDKDtsSHA == "" || s.PySDKCommit == "" || s.PySDKTree == "" || s.PyPyprojectBlob == "" {
			t.Fatalf("missing Claude Code SDK corpus pin: %+v", pin)
		}
		if s.PyClientPy == "" || s.PyTypesPy == "" || s.PyQueryPy == "" || s.PyParserPy == "" || s.PyTransportPy == "" || s.PyResumePy == "" || s.PyStorePy == "" || s.PyStoreValPy == "" {
			t.Fatalf("missing Claude Code Python source pin: %+v", pin)
		}
	}
	if s := ccCurrentCorpus.sources; s.DarwinTarballSHA == "" || s.DarwinBinarySHA == "" || s.DarwinBinaryBytes == 0 {
		t.Fatal("the current corpus does not name the binary it was recorded against")
	}
}

func TestClaudeCurrentCorpusRecordsThePinnedVersion(t *testing.T) {
	pinned := strings.TrimPrefix(PinnedVersion, "v")
	root := ccCorpusRoot(t, ccCurrentCorpus.dir)
	manifest := ccLoadJSON[ccCorpusManifest](t, filepath.Join(root, "manifest.json"))
	if ccCurrentCorpus.sources.CLIVersion != pinned || manifest.Tag != pinned || manifest.Sources.CLIVersion != pinned {
		t.Fatalf("the current corpus records %q (sources %q), the adapter pins %q", manifest.Tag, manifest.Sources.CLIVersion, PinnedVersion)
	}
	inits := 0
	for _, entry := range manifest.Cases {
		dir := filepath.Join(root, entry.Path)
		definition := ccLoadJSON[ccCorpusCase](t, filepath.Join(dir, "case.json"))
		if definition.Provenance.Tag != pinned || definition.Provenance.Sources.CLIVersion != pinned {
			t.Fatalf("%s records %q, the adapter pins %q", entry.ID, definition.Provenance.Tag, PinnedVersion)
		}
		_, decoded := ccLoadFrames(t, filepath.Join(dir, definition.Native))
		for _, i := range ccObserveIndexes(decoded, native.TypeSystem, native.SystemInit) {
			init, ok := decoded[i].Observation.(*native.InitFrame)
			if !ok || init.ClaudeCodeVersion != pinned {
				t.Fatalf("%s frame %d reports claude_code_version %+v, the adapter pins %q", entry.ID, i+1, decoded[i].Observation, PinnedVersion)
			}
			inits++
		}
	}
	if inits == 0 {
		t.Fatal("the current corpus carries no system/init frame")
	}
}

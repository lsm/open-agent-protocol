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

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/adapter/claude/internal/native"
	"github.com/lsm/open-agent-protocol/adapter/claude/internal/rpc"
	"github.com/lsm/open-agent-protocol/protocol"
)

// The Claude Code evidence corpus pins the artifacts frozen in
// research/claude-code-agent-sdk-2.1.263-mapping.md: the wrapper and native
// CLI tarballs, the CLI binary, and both reference SDK trees.
const (
	ccCorpusCLIVersion   = "2.1.263"
	ccCorpusWrapperSHA   = "b325aaaf748065ebce116c50893120384ce6ec56c1133f42f45177f8d1030c66"
	ccCorpusLauncherSHA  = "61ad63033d9c8155d5e60a29f45dc4665afa07631c0b108e62cc83bf45ba490e"
	ccCorpusLinuxSHA     = "8b6207348ad56fdcde085a0ad1f7cff0dfe06ce2c6c1bf97f69f1a1a7b6d0945"
	ccCorpusBinarySHA    = "26d020351e8112f4006790f3cfce43b4c9df0c1bb1d0e542364d64151b81d5ba"
	ccCorpusBinaryBytes  = 215662064
	ccCorpusBuildCommit  = "37ae3f38d765199d54a6913cd61c6c9ad8576cc6"
	ccCorpusBuildDate    = "2026-09-06T01:17:56Z"
	ccCorpusTSSDKVersion = "0.3.263"
	ccCorpusTSSDKSHA     = "e1d6b68b557fc3c57430cafa8cc65eea9d40ff7348b238fe2c431284d727901d"
	ccCorpusTSSDKDtsSHA  = "59560e31f91e47ed93e7cbcaa846e3fc3c96d8dcc41ea36cda64de96c0c4edf4"
	ccCorpusTSRepository = "https://github.com/anthropics/claude-agent-sdk-typescript"
	ccCorpusPySDKVersion = "0.2.152"
	ccCorpusPySDKCommit  = "efd4d865ef1795daffee3cd24cce45307aed8a51"
	ccCorpusPySDKTree    = "d617ca6d630c7bab54f3c0cd1376dcbb938103a2"
	ccCorpusPyPyproject  = "ebece50404bb77b0d02aa47be14375fde76eea60"
	ccCorpusPyRepository = "https://github.com/anthropics/claude-agent-sdk-python"
	ccCorpusPyClientPy   = "bba76b10e4c2ecb6b0d526ad302122b7549c3ac4"
	ccCorpusPyTypesPy    = "308b76cb7fd928d124666c255b253c92c343f15d"
	ccCorpusPyQueryPy    = "4d5f0070e0568778255a39cc6351aaf40429da7c"
	ccCorpusPyParserPy   = "931cc2a632f296aab43f3f98209020138431ce7d"
	ccCorpusPyTransport  = "58abc438ddadc7406330a32d90f743ae60d10c69"
	ccCorpusPyResumePy   = "a50e578fdaea7b10de83697fe355145b7351cecc"
	ccCorpusPyStorePy    = "bb6a2155b08ad546227eba9f2349d95bffd910fa"
	ccCorpusPyStoreVal   = "16addd216281eecaadaedbe7ed361ad8205d0433"
)

// Every label required by the ledger's frozen evidence corpus plan.
var ccLedgerFixtures = map[string]bool{
	// handshake/session
	"initialize-minimal": true, "per-turn-init": true, "second-turn": true,
	// admission
	"message-admitted": true, "command-lifecycle": true, "injected-turn-origin": true,
	"queued-turn-count": true,
	// run lifecycle
	"completed-text": true, "max-turns": true, "api-error-result": true,
	"error-result": true, "interrupt-cancel": true, "process-exit": true,
	"malformed-stdout-line": true,
	// streaming
	"streaming-deltas": true, "interleaved-blocks": true,
	// tools
	"tool-roundtrip": true, "tool-failed": true, "tool-progress": true,
	"auto-approved-tool": true,
	// interactions
	"permission-gate": true, "permission-deny": true,
	// children
	"subagent-task": true, "task-updated-terminal": true, "stop-task": true,
	// hygiene
	"keep-alive-ignored": true, "unknown-frame-ignored": true,
	"no-implied-replay": true, "resume-fork": true,
	// catalog
	"tools-catalog-sources": true,
}

type ccCorpusSources struct {
	CLIVersion        string `json:"cli_version"`
	NPMTarballSHA256  string `json:"npm_tarball_sha256"`
	LauncherCjsSHA256 string `json:"cli_wrapper_cjs_sha256"`
	LinuxTarballSHA   string `json:"linux_x64_tarball_sha256"`
	LinuxBinarySHA    string `json:"linux_x64_binary_sha256"`
	LinuxBinaryBytes  int    `json:"linux_x64_binary_bytes"`
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
	// Catalog is the assert-catalog op's expected projection. A catalog is
	// not an event, so the case declares it here rather than in the
	// expected-oap trace.
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

// ccDecodedFrame carries the production-codec result for one native frame.
type ccDecodedFrame struct {
	Message      *rpc.Message
	Observation  any // typed value from the production decoder
	Control      *ccControl
	Invalid      error // production rejection, for decode-error frames
	UserUUID     string
	ResponseType string
}

func pinnedCCSources() ccCorpusSources {
	return ccCorpusSources{
		CLIVersion:        ccCorpusCLIVersion,
		NPMTarballSHA256:  ccCorpusWrapperSHA,
		LauncherCjsSHA256: ccCorpusLauncherSHA,
		LinuxTarballSHA:   ccCorpusLinuxSHA,
		LinuxBinarySHA:    ccCorpusBinarySHA,
		LinuxBinaryBytes:  ccCorpusBinaryBytes,
		BuildCommit:       ccCorpusBuildCommit,
		BuildDate:         ccCorpusBuildDate,
		TSSDKVersion:      ccCorpusTSSDKVersion,
		TSSDKTarballSHA:   ccCorpusTSSDKSHA,
		TSSDKDtsSHA:       ccCorpusTSSDKDtsSHA,
		TSRepository:      ccCorpusTSRepository,
		PySDKVersion:      ccCorpusPySDKVersion,
		PySDKCommit:       ccCorpusPySDKCommit,
		PySDKTree:         ccCorpusPySDKTree,
		PyPyprojectBlob:   ccCorpusPyPyproject,
		PyRepository:      ccCorpusPyRepository,
		PyClientPy:        ccCorpusPyClientPy,
		PyTypesPy:         ccCorpusPyTypesPy,
		PyQueryPy:         ccCorpusPyQueryPy,
		PyParserPy:        ccCorpusPyParserPy,
		PyTransportPy:     ccCorpusPyTransport,
		PyResumePy:        ccCorpusPyResumePy,
		PyStorePy:         ccCorpusPyStorePy,
		PyStoreValPy:      ccCorpusPyStoreVal,
	}
}

func TestClaudeEvidenceCorpus(t *testing.T) {
	root := ccCorpusRoot(t)
	manifest := ccLoadJSON[ccCorpusManifest](t, filepath.Join(root, "manifest.json"))
	if manifest.Version != 1 || manifest.Adapter != "claude-code-stream-json" || manifest.Tag != ccCorpusCLIVersion || manifest.Sources != pinnedCCSources() {
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
			runClaudeCorpusCase(t, root, entry)
		})
	}
	for fixture := range ccLedgerFixtures {
		if !covered[fixture] {
			t.Errorf("ledger fixture %q has no case", fixture)
		}
	}
	assertClaudeCorpusInventory(t, root, manifest)
}

func runClaudeCorpusCase(t *testing.T, root string, entry ccCorpusManifestCase) {
	dir := filepath.Join(root, entry.Path)
	definition := ccLoadJSON[ccCorpusCase](t, filepath.Join(dir, "case.json"))
	p := definition.Provenance
	if definition.Version != 1 || definition.ID != entry.ID || p.Repository != ccCorpusTSRepository || p.Tag != ccCorpusCLIVersion || p.Sources != pinnedCCSources() || len(definition.Capabilities) == 0 || len(definition.IdentityMap) == 0 {
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

// runClaudeScriptedCase drives the production codec, transport, and reducer
// through the public adapter over real pipes: the scripted CLI counterpart
// writes fixture lines the reader must decode, and every adapter write is
// compared against the transcript. The initialize exchange at open is issued
// exactly like the production process factory, so its wire shape is evidence.
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
	// reap collects the in-flight submission's outcome WITHOUT draining its
	// stream: draining waits for run terminality, which mid-run controls
	// (cancel) must not require.
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
	// recordPending drains and records the reaped submission's event stream.
	recordPending := func() {
		if submitted == nil {
			return
		}
		execution.record(t, submitted.admission, submitted.stream, submitted.err)
		submitted = nil
	}
	// readRequest consumes the next adapter-issued control request, tracking
	// its id so a later transcript reply can address it.
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
				// The catalog is not an event, so it cannot ride the
				// expected-oap trace: the case declares it inline and the
				// projection is compared against it and then run through the
				// real validator, which is what proves it resolves.
				lister, ok := session.(base.ToolLister)
				if !ok {
					t.Fatalf("frame %d: the session serves no catalog", i+1)
				}
				// A degraded catalog is not served without consent: the
				// refusal names the key to opt into, and only then is the
				// catalog projected.
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
				got, err := json.Marshal(catalog)
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
				adaptertest.AssertToolCatalog(t, execution.descriptor, nil, request, catalog)
				execution.catalogs = append(execution.catalogs, catalog)
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
				if _, stream, err := session.Resume(context.Background(), base.ResumeRequest{RunID: "run"}); !errors.Is(err, errUnavailable) || stream != nil {
					t.Fatalf("frame %d: resume error = %v", i+1, err)
				}
				execution.resumeUnavailable++
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

// ccExecution records what the behavioral run actually proved.
type ccExecution struct {
	descriptor        base.Descriptor
	admissions        []protocol.MessageSubmitResponse
	submitErrors      []error
	envelopes         []protocol.Envelope
	runs              [][]protocol.Envelope
	userWrites        int
	lastUserUUID      string
	controlWrites     map[string]int
	initializeShape   bool
	initializeReply   bool
	cancelAccepted    bool
	cancelsIssued     int
	resumeUnavailable int
	assertStates      []string
	modelIDs          []string
	catalogs          []protocol.ToolsListResponse
	closed            bool
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
		if e.cancelAccepted {
			assertCancelledTrace(t, admission, events)
		} else {
			assertValidTrace(t, admission, events)
		}
		e.runs = append(e.runs, events)
	} else {
		e.runs = append(e.runs, nil)
	}
	e.envelopes = append(e.envelopes, events...)
}

// ---- transcript loading ------------------------------------------------------

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
					// The cancel control makes the adapter issue interrupt, so
					// the next reply answers an interrupt, not initialize.
					lastRequestSubtype = native.ControlInterrupt
				case "assert-catalog":
					if control.Catalog == nil {
						t.Fatalf("frame %d assert-catalog declares no catalog", i+1)
					}
				case "resume", "close":
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

// ccWireBytes returns the exact native wire bytes for a fixture raw value.
// String raws carry byte-exact frames (including corrupt lines); object raws
// are used verbatim.
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

// ccReplyPayload renders a fixture control_response raw with the addressed
// request id answered by the harness (the minted id is opaque).
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

// ccAssertWritten compares an adapter write against the fixture expectation:
// canonical JSON equality, with the minted request id collapsed to the
// "@request" placeholder when the fixture uses one.
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

// ccOpenGate waits for the reducer to surface an unresolved permission gate.
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

// ---- ledger evidence rules ---------------------------------------------------

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

// ccResultEchoMatches reports whether a frame echoes the submitted turn uuid.
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

// ccAnyRun reports whether any recorded run satisfies the probe.
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

// assertClaudeLedgerEvidence requires each ledger label to have executable
// evidence in the fixture transcript and the behavioral execution record.
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
			// The catalog is the system/init frame's two lists, joined: every
			// listed tool becomes a catalog entry, every listed MCP server a
			// declared source, and a tool is attributed to a server only when
			// its namespaced name matches one the same frame listed.
			inits := ccObserveIndexes(decoded, native.TypeSystem, native.SystemInit)
			ok = len(inits) == 1 && len(execution.catalogs) == 1
			if ok {
				catalog := execution.catalogs[0]
				declared, attributed := map[string]bool{}, map[string]int{}
				for _, source := range catalog.Sources {
					declared[source.ID] = true
				}
				for _, tool := range catalog.Tools {
					attributed[tool.Source]++
				}
				ok = len(catalog.Sources) == 2 && declared[nativeToolSource] && declared[mcpSourcePrefix+"files"] &&
					attributed[mcpSourcePrefix+"files"] == 1 && attributed[nativeToolSource] > 0
				for _, tool := range catalog.Tools {
					if !declared[tool.Source] || tool.ExecutionOwner != harnessOwner {
						ok = false
					}
				}
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
			injected := false
			for _, i := range ccObserveIndexes(decoded, native.TypeUser, "") {
				user, is := decoded[i].Observation.(*native.UserFrame)
				if is && user.Origin != nil && user.Origin.Kind != "human" {
					injected = true
				}
			}
			leaked := false
			for _, envelope := range execution.envelopes {
				data, _ := json.Marshal(envelope.Payload)
				if strings.Contains(string(data), "task complete") {
					leaked = true
				}
			}
			ok = injected && !leaked && len(execution.admissions) == 1 &&
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
			ok = ccAnyRun(*execution, func(typ protocol.EnvelopeType, code, message string) bool {
				return typ == protocol.TypeRunFailed && code == "claude_api_429" && message == "API Error: rate limited"
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
				if user, is := decoded[i].Observation.(*native.UserFrame); is {
					if text, isText := user.TextContent(); isText && strings.Contains(text, "interrupted by user") {
						synthetic = true
					}
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
			// A tool_use → tool_result span with no permission ask between it.
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
			// v1 never issues a stop_task-shaped control request: interrupt is
			// the only cancellation write, and children settle via frames.
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
			ok = execution.resumeUnavailable >= 1 && advertised && feature.Level == protocol.SupportUnavailable &&
				execution.descriptor.Journal.Replay == protocol.SupportUnavailable
		case "resume-fork":
			feature, advertised := execution.descriptor.Capabilities.Features["run.resume"]
			ok = execution.resumeUnavailable >= 2 && advertised && feature.Level == protocol.SupportUnavailable
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

func ccHasControlRequest(decoded []ccDecodedFrame, subtype string) bool {
	for i := range decoded {
		if decoded[i].Message != nil && decoded[i].Message.Kind == rpc.KindControlRequest && decoded[i].Message.Subtype == subtype {
			return true
		}
	}
	return false
}

// ---- expected-trace comparison -------------------------------------------------

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

// ccEqualEvents compares two traces after dropping wall-clock stamps: the
// corpus harness is deterministic, so every other member — ids, sequences,
// correlations — must match exactly.
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

// ---- corpus plumbing ------------------------------------------------------------

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

// ccLoadOptional decodes a corpus file that may legitimately be empty in
// update mode (the generated mapping file starts empty).
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

func ccCorpusRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "fixtures", "adapters", "claude-code-2.1.263")
}

func TestClaudeCorpusPinConstants(t *testing.T) {
	if ccCorpusCLIVersion == "" || ccCorpusWrapperSHA == "" || ccCorpusLinuxSHA == "" || ccCorpusBinarySHA == "" || ccCorpusBinaryBytes == 0 || ccCorpusBuildCommit == "" || CapabilityRevision == "" || PinnedVersion == "" {
		t.Fatal("missing Claude Code corpus pin")
	}
	if ccCorpusTSSDKSHA == "" || ccCorpusTSSDKDtsSHA == "" || ccCorpusPySDKCommit == "" || ccCorpusPySDKTree == "" || ccCorpusPyPyproject == "" {
		t.Fatal("missing Claude Code SDK corpus pin")
	}
	if ccCorpusPyClientPy == "" || ccCorpusPyTypesPy == "" || ccCorpusPyQueryPy == "" || ccCorpusPyParserPy == "" || ccCorpusPyTransport == "" || ccCorpusPyResumePy == "" || ccCorpusPyStorePy == "" || ccCorpusPyStoreVal == "" {
		t.Fatal("missing Claude Code Python source pin")
	}
}

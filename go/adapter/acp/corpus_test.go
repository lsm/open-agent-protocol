package acp

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

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const (
	acpCommitSHA256     = "272bf799f35a258c6a4107a0410ed361e83683d3"
	acpSpecTreeSHA256   = "a23fba5f3ec62d4aeddec67f183a781e68acc89bc87dac8ffb98a440d20e1995"
	acpSchemaTreeSHA256 = "135854daf9d2b934c9498771d8084d3bf3174c6cf1db29b9d1a3ad1ec8a8dff3"
)

var acpEvidenceFixtures = map[string]bool{
	"initialize-minimal": true, "session-new-minimal": true, "new-prompt-completed": true,
	"cancel-confirmed": true, "completion-wins-race": true, "tool-lifecycle-permission": true,
	"refusal": true, "prompt-error": true, "process-exit": true,
	"malformed-update": true, "update-after-terminal": true, "replay-degradation": true,
	"session-new-tool-sources": true,
}

type acpCorpusManifest struct {
	Version          int                     `json:"version"`
	Adapter          string                  `json:"adapter"`
	ACPVersion       int                     `json:"acp_version"`
	ACPRelease       string                  `json:"acp_release"`
	SchemaRelease    string                  `json:"schema_release"`
	Commit           string                  `json:"commit"`
	SpecTreeSHA256   string                  `json:"spec_tree_sha256"`
	SchemaTreeSHA256 string                  `json:"schema_tree_sha256"`
	Cases            []acpCorpusManifestCase `json:"cases"`
}
type acpCorpusManifestCase struct {
	ID             string   `json:"id"`
	Path           string   `json:"path"`
	LedgerFixtures []string `json:"ledger_fixtures"`
}
type acpCorpusCase struct {
	Version      int               `json:"version"`
	ID           string            `json:"id"`
	Native       string            `json:"native"`
	ExpectedOAP  string            `json:"expected_oap"`
	Mapping      string            `json:"mapping"`
	Omissions    string            `json:"omissions"`
	Provenance   acpProvenance     `json:"provenance"`
	Capabilities map[string]string `json:"advertised_capabilities"`
	IdentityMap  map[string]string `json:"identity_map"`
	Journal      int               `json:"journal_capacity,omitempty"`
	ReplayAfter  *uint64           `json:"replay_after,omitempty"`

	ToolSources     []protocol.ToolSourceAttachment `json:"tool_sources,omitempty"`
	Environment     []string                        `json:"environment,omitempty"`
	ExpectedServers []native.MCPServer              `json:"expected_mcp_servers,omitempty"`
}
type acpProvenance struct {
	Repository    string `json:"repository"`
	Release       string `json:"release"`
	Commit        string `json:"commit"`
	SchemaRelease string `json:"schema_release"`
	SchemaSHA256  string `json:"schema_tree_sha256"`
}
type acpCorpusFrame struct {
	Direction      string          `json:"direction"`
	Classification string          `json:"classification"`
	Fidelity       string          `json:"fidelity"`
	Raw            json.RawMessage `json:"raw"`
	Action         string          `json:"action,omitempty"`
	AwaitEvents    int             `json:"await_events,omitempty"`
	ChoiceID       string          `json:"choice_id,omitempty"`
	Granted        *bool           `json:"granted,omitempty"`
}
type acpCorpusMapping struct {
	Index          int    `json:"index"`
	Method         string `json:"method"`
	Classification string `json:"classification"`
	Fidelity       string `json:"fidelity"`
	OAP            string `json:"oap,omitempty"`
}
type acpCorpusOmission struct {
	Index  int    `json:"index"`
	Method string `json:"method"`
	Reason string `json:"reason"`
}
type acpCasePaths struct{ native, expected, mapping, omissions string }

type corpusClient struct {
	notifications chan rpc.NotificationMessage
	requests      chan *rpc.IncomingRequest
	inbound       chan rpc.InboundMessage
	done          chan struct{}
	promptStarted chan struct{}
	prompt        chan promptOutcome
	sessionNew    native.SessionNewParams
	closed        bool
	err           error
}

func newCorpusClient() *corpusClient {
	return &corpusClient{notifications: make(chan rpc.NotificationMessage, 32), requests: make(chan *rpc.IncomingRequest, 8), inbound: make(chan rpc.InboundMessage, 32), done: make(chan struct{}), promptStarted: make(chan struct{}, 1), prompt: make(chan promptOutcome, 2)}
}
func (c *corpusClient) Call(_ context.Context, method string, params any, result any) error {
	switch method {
	case native.MethodSessionNew:

		if typed, ok := params.(native.SessionNewParams); ok {
			c.sessionNew = typed
		}
		*result.(*native.SessionNewResult) = native.SessionNewResult{SessionID: "native-session"}
		return nil
	case native.MethodSessionPrompt:
		c.promptStarted <- struct{}{}
		outcome := <-c.prompt
		if outcome.err == nil {
			*result.(*native.PromptResult) = outcome.result
		}
		return outcome.err
	default:
		return fmt.Errorf("unexpected call %s", method)
	}
}
func (c *corpusClient) CallStarted(ctx context.Context, method string, params, result any, started chan<- error) error {
	started <- nil
	close(started)
	return c.Call(ctx, method, params, result)
}
func (c *corpusClient) Notify(context.Context, string, any) error     { return nil }
func (c *corpusClient) Requests() <-chan *rpc.IncomingRequest         { return c.requests }
func (c *corpusClient) Notifications() <-chan rpc.NotificationMessage { return c.notifications }
func (c *corpusClient) Inbound() <-chan rpc.InboundMessage            { return c.inbound }
func (c *corpusClient) Done() <-chan struct{}                         { return c.done }
func (c *corpusClient) Err() error {
	if c.err != nil {
		return c.err
	}
	return io.EOF
}
func (c *corpusClient) Close() error {
	if !c.closed {
		close(c.done)
		c.closed = true
	}
	return nil
}

func TestACPEvidenceCorpus(t *testing.T) {
	root := acpCorpusRoot(t)
	manifest := acpLoadJSON[acpCorpusManifest](t, filepath.Join(root, "manifest.json"))
	if manifest.Version != 1 || manifest.Adapter != "acp-v1-stdio" || manifest.ACPVersion != ACPVersion || manifest.ACPRelease != "v1.7.0" || manifest.SchemaRelease != SchemaVersion || manifest.Commit != acpCommitSHA256 || manifest.SpecTreeSHA256 != acpSpecTreeSHA256 || manifest.SchemaTreeSHA256 != acpSchemaTreeSHA256 {
		t.Fatalf("corpus provenance pin mismatch: %+v", manifest)
	}
	seenIDs, seenPaths, covered := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, entry := range manifest.Cases {
		entry := entry
		t.Run(entry.ID, func(t *testing.T) {
			if entry.ID == "" || seenIDs[entry.ID] || seenPaths[entry.Path] || !acpSafeRelative(entry.Path) || len(entry.LedgerFixtures) == 0 {
				t.Fatalf("invalid manifest entry: %+v", entry)
			}
			for _, fixture := range entry.LedgerFixtures {
				if !acpEvidenceFixtures[fixture] || covered[fixture] {
					t.Fatalf("invalid or duplicate ledger fixture %q", fixture)
				}
				covered[fixture] = true
			}
			seenIDs[entry.ID], seenPaths[entry.Path] = true, true
			runACPCorpusCase(t, root, entry)
		})
	}
	for fixture := range acpEvidenceFixtures {
		if !covered[fixture] {
			t.Errorf("ledger fixture %q has no case", fixture)
		}
	}
	assertACPCorpusInventory(t, root, manifest)
}

func runACPCorpusCase(t *testing.T, root string, entry acpCorpusManifestCase) {
	dir := filepath.Join(root, entry.Path)
	definition := acpLoadJSON[acpCorpusCase](t, filepath.Join(dir, "case.json"))
	if definition.Version != 1 || definition.ID != entry.ID || definition.Provenance.Repository != "https://github.com/agentclientprotocol/agent-client-protocol" || definition.Provenance.Release != "v1.7.0" || definition.Provenance.Commit != acpCommitSHA256 || definition.Provenance.SchemaRelease != SchemaVersion || definition.Provenance.SchemaSHA256 != acpSchemaTreeSHA256 || len(definition.Capabilities) == 0 || len(definition.IdentityMap) == 0 {
		t.Fatalf("invalid case metadata: %+v", definition)
	}
	paths := acpPaths(t, dir, definition)
	frames, messages := acpLoadFrames(t, paths.native)
	mappings := acpLoadJSON[[]acpCorpusMapping](t, paths.mapping)
	omissions := acpLoadJSON[[]acpCorpusOmission](t, paths.omissions)
	assertACPClassifications(t, frames, messages, mappings, omissions)

	capacity := definition.Journal
	if capacity == 0 {
		capacity = 64
	}
	client := newCorpusClient()
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, rpc.InitializeResponse, error) {
		return client, rpc.InitializeResponse{ProtocolVersion: 1, AgentCapabilities: rpc.AgentCapabilities{}}, nil
	}), WorkingDirectory: "/workspace", Environment: definition.Environment, Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: capacity})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := implementation.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.AssertDescriptor(t, descriptor)
	session := adaptertest.AssertInitialState(t, implementation, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}, ToolSources: definition.ToolSources})
	if len(definition.ToolSources) > 0 {
		assertACPAttachment(t, session, client, definition)
	}
	admission, stream := submit(t, session)
	<-client.promptStarted
	prefix := []protocol.Envelope{adaptertest.Next(t, stream, time.Second)}
	for index, frame := range frames {
		message := messages[index]
		switch frame.Action {
		case "", "observe":
		case "update":
			notification := rpc.NotificationMessage{Method: message.Method, Params: message.Params}
			client.inbound <- rpc.InboundMessage{Notification: &notification}
			for range frame.AwaitEvents {
				prefix = append(prefix, adaptertest.Next(t, stream, time.Second))
			}
		case "permission":
			incoming := corpusIncomingRequest(t, message)
			client.inbound <- rpc.InboundMessage{Request: incoming}
			var requested protocol.Envelope
			for requested.Type != protocol.TypeActionPermissionRequested {
				requested = adaptertest.Next(t, stream, time.Second)
				prefix = append(prefix, requested)
			}
			var payload protocol.PermissionRequestedPayload
			if err := requested.DecodePayload(&payload); err != nil {
				t.Fatal(err)
			}
			if frame.Granted == nil {
				t.Fatal("permission fixture missing granted")
			}
			err := session.Resolve(context.Background(), base.InteractionResolution{RunID: admission.RunID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: payload.InteractionID, RequestedBy: endpointID, RespondedBy: "user", SessionID: admission.SessionID, RunID: admission.RunID, ChoiceID: frame.ChoiceID, Granted: *frame.Granted}})
			if err != nil {
				t.Fatal(err)
			}
			prefix = append(prefix, adaptertest.Next(t, stream, time.Second))
		case "cancel":
			if _, err := session.Cancel(context.Background(), admission.RunID); err != nil {
				t.Fatal(err)
			}
			prefix = append(prefix, adaptertest.Next(t, stream, time.Second))
		case "complete", "cancelled", "refusal", "complete-await":
			stop := map[string]string{"complete": "end_turn", "complete-await": "end_turn", "cancelled": "cancelled", "refusal": "refusal"}[frame.Action]
			client.prompt <- promptOutcome{result: native.PromptResult{StopReason: stop}}
			if frame.Action == "complete-await" {
				prefix = append(prefix, adaptertest.Drain(t, stream, time.Second)...)
			}
		case "prompt-error":

			if message.Error == nil {
				t.Fatal("prompt-error frame carries no JSON-RPC error object")
			}
			client.prompt <- promptOutcome{err: &rpc.RemoteError{ID: message.ID, Object: *message.Error}}
		case "process-exit":
			client.err = errors.New("fixture process exited")
			close(client.done)
			client.closed = true
		default:
			t.Fatalf("unsupported action %q", frame.Action)
		}
	}
	events := append(prefix, adaptertest.Drain(t, stream, time.Second)...)
	validateACPTrace(t, admission, descriptor, events, containsAction(frames, "cancel"), containsAction(frames, "permission"))
	if definition.ReplayAfter != nil {
		assertReplayEvidence(t, session, admission.RunID, *definition.ReplayAfter, events)
	}
	expected := acpLoadJSON[[]protocol.Envelope](t, paths.expected)
	if len(expected) == 0 {
		acpWriteExpected(t, paths.expected, events)
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
		t.Fatalf("normalized trace mismatch\nwant: %s\ngot: %s", prettyWant, prettyGot)
	}
}

func assertACPAttachment(t *testing.T, session base.Session, client *corpusClient, definition acpCorpusCase) {
	t.Helper()
	got, err := json.Marshal(client.sessionNew.MCPServers)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(definition.ExpectedServers)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("session/new mcpServers\n got: %s\nwant: %s", got, want)
	}
	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	published := make(map[string]protocol.ToolSourceDescriptor, len(state.Sources))
	for _, source := range state.Sources {
		published[source.ID] = source
	}
	if len(published) != len(definition.ToolSources) {
		t.Fatalf("session state publishes %d sources, want %d", len(published), len(definition.ToolSources))
	}
	for _, attachment := range definition.ToolSources {
		if published[attachment.ID] != attachment.Descriptor() {
			t.Fatalf("session state publishes %+v for %q, want the descriptor projection", published[attachment.ID], attachment.ID)
		}
	}

	encoded, err := json.Marshal(state.Sources)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{"command", "args", "environment"} {
		if bytes.Contains(encoded, []byte(`"`+member+`"`)) {
			t.Fatalf("session state leaked the attachment-only member %q: %s", member, encoded)
		}
	}
}

func corpusIncomingRequest(t *testing.T, message rpc.Message) *rpc.IncomingRequest {
	t.Helper()

	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	client := rpc.NewClient(serverReader, serverWriter, rpc.ClientOptions{CloseReadWriter: pipePair{serverReader, serverWriter}, StrictResponseIDs: true})
	go func() { _, _ = io.Copy(io.Discard, clientReader) }()
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = clientWriter.Write(append(raw, '\n')) }()
	select {
	case request := <-client.Requests():
		t.Cleanup(func() { _ = client.Close(); _ = clientWriter.Close(); _ = clientReader.Close() })
		return request
	case <-time.After(time.Second):
		t.Fatal("timed out decoding reverse request")
	}
	return nil
}

type pipePair struct {
	io.Closer
	second io.Closer
}

func (p pipePair) Close() error { _ = p.Closer.Close(); return p.second.Close() }

func assertReplayEvidence(t *testing.T, session base.Session, runID protocol.RunID, after uint64, events []protocol.Envelope) {
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
func containsAction(frames []acpCorpusFrame, action string) bool {
	for _, frame := range frames {
		if frame.Action == action {
			return true
		}
	}
	return false
}
func acpLoadFrames(t *testing.T, filename string) ([]acpCorpusFrame, []rpc.Message) {
	t.Helper()
	file, err := os.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), rpc.DefaultFrameLimit)
	var frames []acpCorpusFrame
	var messages []rpc.Message
	for scanner.Scan() {
		var frame acpCorpusFrame
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&frame); err != nil {
			t.Fatalf("decode %s frame %d: %v", filename, len(frames)+1, err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			t.Fatalf("decode %s frame %d: trailing JSON", filename, len(frames)+1)
		}
		message, err := rpc.ParseMessage(frame.Raw)
		if err != nil {
			t.Fatalf("decode native wire frame %d: %v", len(frames)+1, err)
		}
		frames, messages = append(frames, frame), append(messages, message)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(frames) == 0 {
		t.Fatal("empty native transcript")
	}
	return frames, messages
}
func acpMethod(message rpc.Message) string {
	if message.Method != "" {
		return message.Method
	}
	if message.Error != nil {
		return "response:error"
	}
	return "response:result"
}
func assertACPClassifications(t *testing.T, frames []acpCorpusFrame, messages []rpc.Message, mappings []acpCorpusMapping, omissions []acpCorpusOmission) {
	t.Helper()
	if len(frames) != len(mappings) {
		t.Fatalf("mapping count %d does not cover %d native frames", len(mappings), len(frames))
	}
	omitted := map[int]acpCorpusOmission{}
	for _, omission := range omissions {
		if omission.Index < 1 || omission.Index > len(frames) || omission.Reason == "" || omission.Method != acpMethod(messages[omission.Index-1]) || omitted[omission.Index].Index != 0 {
			t.Fatalf("invalid omission: %+v", omission)
		}
		omitted[omission.Index] = omission
	}
	for i, frame := range frames {
		mapping := mappings[i]
		method := acpMethod(messages[i])
		if mapping.Index != i+1 || mapping.Method != method || mapping.Classification != frame.Classification || mapping.Fidelity != frame.Fidelity {
			t.Fatalf("frame %d mapping mismatch", i+1)
		}
		switch frame.Classification {
		case "mapped", "required-unmapped", "unsupported-request":
			if omitted[i+1].Index != 0 {
				t.Fatalf("mapped frame %d is omitted", i+1)
			}
		case "observed-only", "ignorable-custom":
			if omitted[i+1].Index == 0 {
				t.Fatalf("omitted frame %d lacks reason", i+1)
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
func acpPaths(t *testing.T, dir string, definition acpCorpusCase) acpCasePaths {
	t.Helper()
	resolve := func(label, name string) string {
		if !acpSafeRelative(name) || filepath.Base(name) != name {
			t.Fatalf("invalid %s filename %q", label, name)
		}
		return filepath.Join(dir, name)
	}
	return acpCasePaths{resolve("native", definition.Native), resolve("expected", definition.ExpectedOAP), resolve("mapping", definition.Mapping), resolve("omissions", definition.Omissions)}
}
func acpLoadJSON[T any](t *testing.T, filename string) T {
	t.Helper()
	var value T
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode %s: %v", filename, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		t.Fatalf("decode %s: trailing JSON", filename)
	}
	return value
}
func acpWriteExpected(t *testing.T, filename string, events []protocol.Envelope) {
	t.Helper()
	if os.Getenv("OAP_UPDATE_ACP_CORPUS") != "1" {
		t.Fatalf("%s empty; set OAP_UPDATE_ACP_CORPUS=1", filename)
	}
	data, _ := json.MarshalIndent(events, "", "  ")
	if err := os.WriteFile(filename, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
func assertACPCorpusInventory(t *testing.T, root string, manifest acpCorpusManifest) {
	t.Helper()
	listed := map[string]bool{"manifest.json": true}
	for _, entry := range manifest.Cases {
		definition := acpLoadJSON[acpCorpusCase](t, filepath.Join(root, entry.Path, "case.json"))
		paths := acpPaths(t, filepath.Join(root, entry.Path), definition)
		for _, name := range []string{"case.json", filepath.Base(paths.native), filepath.Base(paths.expected), filepath.Base(paths.mapping), filepath.Base(paths.omissions)} {
			listed[filepath.ToSlash(filepath.Join(entry.Path, name))] = true
		}
	}
	var unlisted []string
	err := filepath.WalkDir(root, func(path string, item os.DirEntry, err error) error {
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
		relative = filepath.ToSlash(relative)
		if !listed[relative] {
			unlisted = append(unlisted, relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(unlisted)
	if len(unlisted) != 0 {
		t.Fatalf("unlisted corpus files: %v", unlisted)
	}
}
func acpSafeRelative(path string) bool {
	clean := filepath.Clean(path)
	return path != "" && !filepath.IsAbs(path) && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}
func acpCorpusRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", filepath.FromSlash(CorpusDirectory))
}

func TestACPCorpusPinConstants(t *testing.T) {
	if acpCommitSHA256 == "" || acpSpecTreeSHA256 == "" || acpSchemaTreeSHA256 == "" || CapabilityRevision == "" {
		t.Fatal("missing ACP corpus pin")
	}
}

func validateACPTrace(t *testing.T, admission protocol.MessageSubmitResponse, descriptor base.Descriptor, events []protocol.Envelope, cancelled, permission bool) {
	t.Helper()
	if !permission {
		if cancelled {
			adaptertest.AssertProtocolValidWithCancellation(t, admission, descriptor, events)
		} else {
			adaptertest.AssertProtocolValidWithDescriptor(t, admission, descriptor, events)
		}
		return
	}
	var requested protocol.PermissionRequestedPayload
	cut := -1
	for index, event := range events {
		if event.Type != protocol.TypeActionPermissionRequested {
			continue
		}
		if err := event.DecodePayload(&requested); err != nil {
			t.Fatal(err)
		}
		cut = index + 1
		break
	}
	if cut < 0 {
		t.Fatal("permission case emitted no permission gate")
	}
	resolveRequest, err := protocol.NewEnvelope(protocol.TypeActionPermissionResolveRequest, "permission-resolve-request", protocol.PermissionResolveRequest{InteractionID: requested.InteractionID, RequestedBy: endpointID, RespondedBy: "user", SessionID: admission.SessionID, RunID: admission.RunID, ChoiceID: "allow", Granted: true})
	if err != nil {
		t.Fatal(err)
	}
	resolveRequest.SessionID, resolveRequest.RunID = admission.SessionID, admission.RunID
	resolveRequest.CapabilityRevision = descriptor.CapabilityRevision
	resolveResponse, err := protocol.NewEnvelope(protocol.TypeActionPermissionResolveResponse, "permission-resolve-response", protocol.PermissionResolveResponse{InteractionID: requested.InteractionID, SessionID: admission.SessionID, RunID: admission.RunID, Accepted: true})
	if err != nil {
		t.Fatal(err)
	}
	resolveResponse.SessionID, resolveResponse.RunID, resolveResponse.InReplyTo = admission.SessionID, admission.RunID, resolveRequest.ID
	resolveResponse.CapabilityRevision = descriptor.CapabilityRevision
	spliced := make([]protocol.Envelope, 0, len(events)+2)
	spliced = append(spliced, events[:cut]...)
	spliced = append(spliced, resolveRequest, resolveResponse)
	spliced = append(spliced, events[cut:]...)
	if cancelled {
		adaptertest.AssertProtocolValidWithCancellation(t, admission, descriptor, spliced)
	} else {
		adaptertest.AssertProtocolValidWithDescriptor(t, admission, descriptor, spliced)
	}
}

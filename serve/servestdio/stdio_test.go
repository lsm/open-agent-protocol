package servestdio

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
)

// --- deterministic hub and frontend harness ---

// testClock and testIDs make the memory adapter's envelopes deterministic, so
// whole transcripts can be compared byte for byte; both are mutex-guarded
// because concurrent requests share one adapter.
type testClock struct {
	mu sync.Mutex
	n  int64
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return time.UnixMilli(c.n)
}

type testIDs struct {
	mu sync.Mutex
	n  int
}

func (g *testIDs) NewID(kind string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return fmt.Sprintf("%s-%02d", kind, g.n)
}

func newTestHub(t *testing.T, journalCapacity, streamQueue int) *serve.Hub {
	t.Helper()
	registry := serve.NewRegistry()
	clock, ids := &testClock{}, &testIDs{}
	if err := registry.Register("memory", base.NewMemory(base.Config{Clock: clock, IDs: ids, JournalCapacity: journalCapacity})); err != nil {
		t.Fatal(err)
	}
	return serve.New(registry, serve.Options{StreamQueue: streamQueue})
}

// frontend drives one Server through real pipes, so line framing — not just
// the values — is what the tests observe.
type frontend struct {
	t      *testing.T
	stdin  io.WriteCloser
	reader *bufio.Reader
	stdout io.ReadCloser
	done   chan error
}

func startFrontend(t *testing.T, hub *serve.Hub, options Options) *frontend {
	t.Helper()
	server, err := New(hub, options)
	if err != nil {
		t.Fatal(err)
	}
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), stdinReader, stdoutWriter) }()
	return &frontend{t: t, stdin: stdinWriter, reader: bufio.NewReader(stdoutReader), stdout: stdoutReader, done: done}
}

func (f *frontend) send(line string) {
	f.t.Helper()
	if _, err := f.stdin.Write([]byte(line + "\n")); err != nil {
		f.t.Fatalf("write request %q: %v", line, err)
	}
}

// line reads one daemon line within a deadline; a frontend that produces
// nothing is stuck, not slow.
func (f *frontend) line() string {
	f.t.Helper()
	type read struct {
		text string
		err  error
	}
	readDone := make(chan read, 1)
	go func() {
		text, err := f.reader.ReadString('\n')
		readDone <- read{text: text, err: err}
	}()
	select {
	case result := <-readDone:
		if result.err != nil {
			f.t.Fatalf("read line: %v (got %q)", result.err, result.text)
		}
		return strings.TrimSuffix(result.text, "\n")
	case <-time.After(10 * time.Second):
		f.t.Fatal("frontend produced no line within the deadline")
		return ""
	}
}

func (f *frontend) lines(count int) []string {
	f.t.Helper()
	lines := make([]string, 0, count)
	for len(lines) < count {
		lines = append(lines, f.line())
	}
	return lines
}

// group reads count lines and partitions them into responses and event
// lines. The single writer makes every line atomic and preserves per-session
// event order, but responses and events interleave freely; tests therefore
// assert each class exactly and never the interleaving across classes.
func (f *frontend) group(count int) (responses []responseLine, events []string) {
	f.t.Helper()
	for _, line := range f.lines(count) {
		if strings.HasPrefix(line, `{"id":`) {
			var response responseLine
			if err := json.Unmarshal([]byte(line), &response); err != nil {
				f.t.Fatalf("response line %q: %v", line, err)
			}
			responses = append(responses, response)
			continue
		}
		var kind struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal([]byte(line), &kind); err != nil || kind.Event == "" {
			f.t.Fatalf("line %q is neither a response nor an event line", line)
		}
		events = append(events, line)
	}
	return responses, events
}

func (f *frontend) finish() error {
	f.t.Helper()
	if err := f.stdin.Close(); err != nil {
		f.t.Fatalf("close stdin: %v", err)
	}
	select {
	case err := <-f.done:
		return err
	case <-time.After(10 * time.Second):
		f.t.Fatal("frontend did not stop after stdin closed")
		return nil
	}
}

// expectResponse reads the next line and requires it to be the response for
// id; use where nothing else can be in flight.
func (f *frontend) expectResponse(id int64) responseLine {
	f.t.Helper()
	response := f.decodeResponse(f.line())
	if response.ID != id {
		f.t.Fatalf("response id %d, want %d", response.ID, id)
	}
	return response
}

func (f *frontend) decodeResponse(line string) responseLine {
	f.t.Helper()
	if !strings.HasPrefix(line, `{"id":`) {
		f.t.Fatalf("line %q is not a response", line)
	}
	var response responseLine
	if err := json.Unmarshal([]byte(line), &response); err != nil {
		f.t.Fatalf("response line %q: %v", line, err)
	}
	return response
}

func requireOK(t *testing.T, response responseLine) {
	t.Helper()
	if !response.OK || response.Error != nil {
		t.Fatalf("response %+v is not ok", response)
	}
}

func requireCode(t *testing.T, response responseLine, code string) {
	t.Helper()
	if response.OK || response.Error == nil {
		t.Fatalf("response %+v succeeded, want error %s", response, code)
	}
	if response.Error.Code != code {
		t.Fatalf("response error %s (%s), want %s", response.Error.Code, response.Error.Message, code)
	}
}

// requestEnvelope builds one schema-valid request envelope the way the Go
// client does, for driving the op surface from tests.
func requestEnvelope(t *testing.T, id string, typ protocol.EnvelopeType, payload any, sessionID string, runID string) json.RawMessage {
	t.Helper()
	envelope, err := protocol.NewEnvelope(typ, protocol.EnvelopeID(id), payload)
	if err != nil {
		t.Fatal(err)
	}
	envelope.SessionID = protocol.SessionID(sessionID)
	envelope.RunID = protocol.RunID(runID)
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// runToCompletion drives one scripted memory run — submit, resolve the
// permission gate, resolve the input gate — against an already-subscribed
// session, reading the admitted envelopes in phases so every expected line is
// consumed. The scripted shapes are fixed: the submit admits four envelopes
// and parks; the permission resolution continues through five (the park's
// run.status.update included) and parks at the input gate; the input
// resolution settles the final three. It returns the run id.
func runToCompletion(t *testing.T, f *frontend, sessionID string, submitID, resolveA, resolveB int64) protocol.RunID {
	t.Helper()
	f.send(fmt.Sprintf(`{"id":%d,"op":"submit","session_id":%q,"request":%s}`, submitID, sessionID, requestEnvelope(t, "submit-1", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: protocol.SessionID(sessionID), Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
	}, sessionID, "")))
	responses, events := f.group(5)
	if len(responses) != 1 || len(events) != 4 {
		t.Fatalf("submit phase: %d responses, %d events", len(responses), len(events))
	}
	requireOK(t, responses[0])
	var admission protocol.MessageSubmitResponse
	if err := json.Unmarshal(responses[0].Result, &admission); err != nil {
		t.Fatal(err)
	}
	requireSequences(t, events, 1, 4)

	f.send(fmt.Sprintf(`{"id":%d,"op":"resolve","session_id":%q,"request":%s}`, resolveA, sessionID, resolveEnvelope(t, "resolve-p", events[3], sessionID)))
	responses, events = f.group(6)
	if len(responses) != 1 || len(events) != 5 {
		t.Fatalf("permission phase: %d responses, %d events", len(responses), len(events))
	}
	requireOK(t, responses[0])
	requireSequences(t, events, 5, 9)

	f.send(fmt.Sprintf(`{"id":%d,"op":"resolve","session_id":%q,"request":%s}`, resolveB, sessionID, resolveEnvelope(t, "resolve-i", events[3], sessionID)))
	responses, events = f.group(4)
	if len(responses) != 1 || len(events) != 3 {
		t.Fatalf("input phase: %d responses, %d events", len(responses), len(events))
	}
	requireOK(t, responses[0])
	requireSequences(t, events, 10, 12)
	if last := events[2]; !strings.Contains(last, `"run.completed"`) {
		t.Fatalf("run did not end on run.completed: %s", last)
	}
	return admission.RunID
}

// resolveEnvelope echoes the interaction a gate envelope requested, the way
// the Go client resolves it: approve the permission, answer the input.
func resolveEnvelope(t *testing.T, id string, gateLine string, sessionID string) json.RawMessage {
	t.Helper()
	var event envelopeLine
	if err := json.Unmarshal([]byte(gateLine), &event); err != nil {
		t.Fatal(err)
	}
	envelope, err := protocol.ParseEnvelope(event.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	switch envelope.Type {
	case protocol.TypeActionPermissionRequested:
		var gate protocol.PermissionRequestedPayload
		if err := envelope.DecodePayload(&gate); err != nil {
			t.Fatal(err)
		}
		return requestEnvelope(t, id, protocol.TypeActionPermissionResolveRequest, protocol.PermissionResolveRequest{
			InteractionID: gate.InteractionID, SessionID: protocol.SessionID(sessionID), RunID: gate.RunID,
			RequestedBy: gate.RequestedBy, RespondedBy: gate.RespondedBy, ChoiceID: "approve", Granted: true,
		}, sessionID, string(gate.RunID))
	case protocol.TypeUserInputRequested:
		var gate protocol.UserInputRequestedPayload
		if err := envelope.DecodePayload(&gate); err != nil {
			t.Fatal(err)
		}
		return requestEnvelope(t, id, protocol.TypeUserInputResolveRequest, protocol.UserInputResolveRequest{
			InteractionID: gate.InteractionID, SessionID: protocol.SessionID(sessionID), RunID: gate.RunID,
			RequestedBy: gate.RequestedBy, RespondedBy: gate.RespondedBy,
			Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}},
		}, sessionID, string(gate.RunID))
	default:
		t.Fatalf("cannot resolve %s", envelope.Type)
		return nil
	}
}

// requireSequences asserts one phase's event lines carry the expected run
// sequences in order.
func requireSequences(t *testing.T, events []string, from, to uint64) {
	t.Helper()
	if uint64(len(events)) != to-from+1 {
		t.Fatalf("%d event lines, want %d", len(events), to-from+1)
	}
	for index, line := range events {
		var event envelopeLine
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("event line %q: %v", line, err)
		}
		if event.Event != signalEnvelope || event.Sequence == nil || *event.Sequence != from+uint64(index) {
			t.Fatalf("event line %q is not envelope sequence %d", line, from+uint64(index))
		}
	}
}

// --- transcripts ---

// TestGoldenSessionTranscript drives one whole session across every op and
// compares each phase's lines byte for byte. The interleaving of responses
// and events is free (asserted per class), so the transcript is fully
// deterministic: the fake clock and id generator fix the envelope bytes, and
// sequential requests fix the daemon-minted ids.
func TestGoldenSessionTranscript(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})
	var transcript []string
	keep := func(lines ...string) { transcript = append(transcript, lines...) }

	f.send(`{"id":1,"op":"adapters"}`)
	keep(f.line())

	f.send(`{"id":2,"op":"capabilities","adapter":"memory"}`)
	keep(f.line())

	f.send(`{"id":3,"op":"open","adapter":"memory","request":` + string(requestEnvelope(t, "open-1", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{SessionID: "golden"}, "", "")) + `}`)
	keep(f.line())

	f.send(`{"id":4,"op":"events","session_id":"golden"}`)
	keep(f.line())

	// Submit and the two gate resolutions each produce one response and four
	// event lines; the classes interleave freely.
	f.send(`{"id":5,"op":"submit","session_id":"golden","request":` + string(requestEnvelope(t, "submit-1", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "golden", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
	}, "golden", "")) + `}`)
	responses, events := f.group(5)
	keep(append(eventLinesOf(t, responses), events...)...)
	var admission protocol.MessageSubmitResponse
	if err := json.Unmarshal(responses[0].Result, &admission); err != nil {
		t.Fatal(err)
	}

	f.send(`{"id":6,"op":"resolve","session_id":"golden","request":` + string(resolveEnvelope(t, "resolve-p", events[3], "golden")) + `}`)
	responses, events = f.group(6)
	keep(append(eventLinesOf(t, responses), events...)...)

	f.send(`{"id":7,"op":"resolve","session_id":"golden","request":` + string(resolveEnvelope(t, "resolve-i", events[3], "golden")) + `}`)
	responses, events = f.group(4)
	keep(append(eventLinesOf(t, responses), events...)...)

	// The subscription ended silently at run.completed — no line — so the
	// remaining ops are race-free.
	f.send(`{"id":8,"op":"state","session_id":"golden"}`)
	keep(f.line())

	f.send(`{"id":9,"op":"close","session_id":"golden"}`)
	keep(f.line())

	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
	for _, line := range transcript {
		t.Logf("LINE %s", line)
	}
	requireEqualLines(t, goldenTranscript, transcript)
}

// eventLinesOf re-encodes racy-phase responses in arrival order so a phase's
// golden slice records what the wire carried.
func eventLinesOf(t *testing.T, responses []responseLine) []string {
	t.Helper()
	lines := make([]string, 0, len(responses))
	for _, response := range responses {
		data, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(data))
	}
	return lines
}

func requireEqualLines(t *testing.T, want, got []string) {
	t.Helper()
	for index, line := range got {
		if index >= len(want) {
			t.Fatalf("line %d unexpected:\n got %s\n", index+1, line)
		}
		if line != want[index] {
			t.Fatalf("line %d mismatch:\n got %s\nwant %s", index+1, line, want[index])
		}
	}
	if len(got) != len(want) {
		t.Fatalf("%d lines, want %d", len(got), len(want))
	}
}

// goldenTranscript is the byte-exact transcript of
// TestGoldenSessionTranscript: minted frontend ids run in request order and
// the deterministic memory adapter fixes every envelope byte.
var goldenTranscript = []string{
	`{"id":1,"ok":true,"result":{"adapters":[{"name":"memory","capability_revision":"reference-memory-v1","capabilities":{"endpoint":{"id":"reference.memory","name":"Deterministic In-Memory Reference Adapter","version":"0.1","adapter":"process-memory-script"},"protocol_versions":["0.1"],"profiles":["open-agent-protocol.agent-control-core"],"features":{"action.permissions":{"level":"emulated","reason":"the reference adapter exposes an interactive scripted gate"},"action.tools":{"level":"emulated","reason":"the reference adapter projects the scripted tool lifecycle"},"action.tools.execute":{"level":"emulated","reason":"the reference adapter executes a fixed deterministic script"},"capabilities":{"level":"native"},"protocol.initialize":{"level":"native"},"run.cancel":{"level":"emulated","reason":"run-target API is implemented over a one-active-run session"},"run.reconciliation":{"level":"native"},"run.replay":{"level":"degraded","reason":"older cursors can expire and no cross-process replay is claimed"},"run.resume":{"level":"degraded","reason":"reattachment and replay use a bounded process-memory journal"},"run.status":{"level":"native"},"run.streaming":{"level":"native"},"session.message.delivery.auto":{"level":"native"},"session.message.submit":{"level":"native"},"session.open":{"level":"native"},"session.state":{"level":"native"},"user_input":{"level":"emulated","reason":"the reference adapter exposes an interactive scripted gate"}}}}]}}`,
	`{"id":2,"ok":true,"result":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"oap-response-2","payload":{"endpoint":{"id":"reference.memory","name":"Deterministic In-Memory Reference Adapter","version":"0.1","adapter":"process-memory-script"},"protocol_versions":["0.1"],"profiles":["open-agent-protocol.agent-control-core"],"features":{"action.permissions":{"level":"emulated","reason":"the reference adapter exposes an interactive scripted gate"},"action.tools":{"level":"emulated","reason":"the reference adapter projects the scripted tool lifecycle"},"action.tools.execute":{"level":"emulated","reason":"the reference adapter executes a fixed deterministic script"},"capabilities":{"level":"native"},"protocol.initialize":{"level":"native"},"run.cancel":{"level":"emulated","reason":"run-target API is implemented over a one-active-run session"},"run.reconciliation":{"level":"native"},"run.replay":{"level":"degraded","reason":"older cursors can expire and no cross-process replay is claimed"},"run.resume":{"level":"degraded","reason":"reattachment and replay use a bounded process-memory journal"},"run.status":{"level":"native"},"run.streaming":{"level":"native"},"session.message.delivery.auto":{"level":"native"},"session.message.submit":{"level":"native"},"session.open":{"level":"native"},"session.state":{"level":"native"},"user_input":{"level":"emulated","reason":"the reference adapter exposes an interactive scripted gate"}}},"in_reply_to":"oap-request-1","capability_revision":"reference-memory-v1"}}`,
	`{"id":3,"ok":true,"result":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.open.response","id":"oap-response-3","payload":{"session_id":"golden","status":"idle"},"in_reply_to":"open-1","session_id":"golden"}}`,
	`{"id":4,"ok":true,"result":null}`,
	`{"id":5,"ok":true,"result":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.response","id":"oap-response-4","payload":{"session_id":"golden","accepted":true,"submission_id":"submission-06","requested_delivery":"auto","effective_delivery":"start","delivery_resolution":"session_idle","admission":"started","run_id":"run-01","status":"running","message_ids":["message-05"]},"in_reply_to":"submit-1","session_id":"golden","run_id":"run-01"}}`,
	`{"event":"envelope","session_id":"golden","sequence":1,"envelope":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-07","payload":{"session_id":"golden","run_id":"run-01","status":"running","started_at_ms":3},"sequence":1,"timestamp_ms":4,"session_id":"golden","run_id":"run-01","capability_revision":"reference-memory-v1"}}`,
	`{"event":"envelope","session_id":"golden","sequence":2,"envelope":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"content.delta","id":"event-09","payload":{"session_id":"golden","run_id":"run-01","message_id":"message-08","part":{"type":"text","text":"I will use the scripted tool."}},"sequence":2,"timestamp_ms":5,"session_id":"golden","run_id":"run-01","capability_revision":"reference-memory-v1"}}`,
	`{"event":"envelope","session_id":"golden","sequence":3,"envelope":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.requested","id":"event-10","payload":{"session_id":"golden","run_id":"run-01","tool_call_id":"tool-call-04","requested_by":"agent","execution_owner":"reference-adapter","name":"scripted_tool","arguments_json":{"operation":"golden"}},"sequence":3,"timestamp_ms":6,"session_id":"golden","run_id":"run-01","tool_call_id":"tool-call-04","capability_revision":"reference-memory-v1"}}`,
	`{"event":"envelope","session_id":"golden","sequence":4,"envelope":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.permission.requested","id":"event-11","payload":{"interaction_id":"permission-02","requested_by":"agent","responded_by":"user","session_id":"golden","run_id":"run-01","tool_call_id":"tool-call-04","title":"Allow scripted tool","description":"The golden script requires approval.","choices":[{"id":"approve","label":"Approve"},{"id":"deny","label":"Deny"}],"arguments_json":{"operation":"golden"}},"sequence":4,"timestamp_ms":7,"session_id":"golden","run_id":"run-01","tool_call_id":"tool-call-04","capability_revision":"reference-memory-v1"}}`,
	`{"id":6,"ok":true,"result":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.permission.resolve.response","id":"oap-response-5","payload":{"interaction_id":"permission-02","session_id":"golden","run_id":"run-01","accepted":true},"in_reply_to":"resolve-p","session_id":"golden","run_id":"run-01"}}`,
	`{"event":"envelope","session_id":"golden","sequence":5,"envelope":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.permission.resolved","id":"event-12","payload":{"interaction_id":"permission-02","requested_by":"agent","responded_by":"user","session_id":"golden","run_id":"run-01","tool_call_id":"tool-call-04","outcome":"resolved","choice_id":"approve","granted":true},"sequence":5,"timestamp_ms":8,"session_id":"golden","run_id":"run-01","capability_revision":"reference-memory-v1"}}`,
	`{"event":"envelope","session_id":"golden","sequence":6,"envelope":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.started","id":"event-13","payload":{"session_id":"golden","run_id":"run-01","tool_call_id":"tool-call-04","requested_by":"agent","execution_owner":"reference-adapter","name":"scripted_tool","arguments_json":{"operation":"golden"}},"sequence":6,"timestamp_ms":9,"session_id":"golden","run_id":"run-01","tool_call_id":"tool-call-04","capability_revision":"reference-memory-v1"}}`,
	`{"event":"envelope","session_id":"golden","sequence":7,"envelope":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.completed","id":"event-14","payload":{"session_id":"golden","run_id":"run-01","tool_call_id":"tool-call-04","requested_by":"agent","execution_owner":"reference-adapter","name":"scripted_tool","result":{"ok":true}},"sequence":7,"timestamp_ms":10,"session_id":"golden","run_id":"run-01","tool_call_id":"tool-call-04","capability_revision":"reference-memory-v1"}}`,
	`{"event":"envelope","session_id":"golden","sequence":8,"envelope":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-15","payload":{"interaction_id":"input-03","requested_by":"agent","responded_by":"user","session_id":"golden","run_id":"run-01","tool_call_id":"tool-call-04","title":"Golden input","description":"Choose the deterministic answer.","questions":[{"id":"choice","prompt":"Continue?","kind":"single_choice","required":true,"options":[{"id":"yes","label":"Yes"}]}]},"sequence":8,"timestamp_ms":11,"session_id":"golden","run_id":"run-01","tool_call_id":"tool-call-04","capability_revision":"reference-memory-v1"}}`,
	`{"event":"envelope","session_id":"golden","sequence":9,"envelope":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-16","payload":{"session_id":"golden","run_id":"run-01","status":"waiting_for_input","pending_user_input_id":"input-03","updated_at_ms":12},"sequence":9,"timestamp_ms":13,"session_id":"golden","run_id":"run-01","capability_revision":"reference-memory-v1"}}`,
	`{"id":7,"ok":true,"result":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolve.response","id":"oap-response-6","payload":{"interaction_id":"input-03","session_id":"golden","run_id":"run-01","accepted":true},"in_reply_to":"resolve-i","session_id":"golden","run_id":"run-01"}}`,
	`{"event":"envelope","session_id":"golden","sequence":10,"envelope":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-17","payload":{"interaction_id":"input-03","requested_by":"agent","responded_by":"user","session_id":"golden","run_id":"run-01","status":"submitted","answers":[{"question_id":"choice","selected_option_ids":["yes"]}]},"sequence":10,"timestamp_ms":14,"session_id":"golden","run_id":"run-01","capability_revision":"reference-memory-v1"}}`,
	`{"event":"envelope","session_id":"golden","sequence":11,"envelope":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"content.delta","id":"event-19","payload":{"session_id":"golden","run_id":"run-01","message_id":"message-18","part":{"type":"text","text":"The golden script completed."}},"sequence":11,"timestamp_ms":15,"session_id":"golden","run_id":"run-01","capability_revision":"reference-memory-v1"}}`,
	`{"event":"envelope","session_id":"golden","sequence":12,"envelope":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-20","payload":{"session_id":"golden","run_id":"run-01","final_response":{"id":"message-18","role":"assistant","content":"The golden script completed."},"stop_reason":"end_turn"},"sequence":12,"timestamp_ms":16,"session_id":"golden","run_id":"run-01","capability_revision":"reference-memory-v1"}}`,
	`{"id":8,"ok":true,"result":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.state.response","id":"oap-response-7","payload":{"session_id":"golden","status":"idle","transcript_cursor":"12","updated_at_ms":16},"in_reply_to":"oap-request-8","session_id":"golden"}}`,
	`{"id":9,"ok":true,"result":null}`,
}

// --- cursor replay, gaps, and signals ---

// TestEventsCursorReplay subscribes after a settled run and receives exactly
// the retained suffix — the stdio form of the SSE ?after= reconnect.
func TestEventsCursorReplay(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})
	f.send(`{"id":1,"op":"open","adapter":"memory","request":` + string(requestEnvelope(t, "open-1", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{SessionID: "replay"}, "", "")) + `}`)
	requireOK(t, f.expectResponse(1))
	f.send(`{"id":2,"op":"events","session_id":"replay"}`)
	requireOK(t, f.expectResponse(2))
	runToCompletion(t, f, "replay", 3, 4, 5)

	f.send(`{"id":6,"op":"events","session_id":"replay","after":4}`)
	requireOK(t, f.expectResponse(6))
	responses, events := f.group(8)
	if len(responses) != 0 || len(events) != 8 {
		t.Fatalf("replay: %d responses, %d events", len(responses), len(events))
	}
	requireSequences(t, events, 5, 12)

	// The replayed subscription ended at the run's terminal event with no
	// further line; a state op proves the frontend is still live.
	f.send(`{"id":7,"op":"state","session_id":"replay"}`)
	requireOK(t, f.expectResponse(7))
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestEventsGapAndResume drives a journal too small to retain a run's start,
// observes the oap-replay-gap signal, and resumes at the documented cursor —
// the acceptance path for recovering after a gap via after.
func TestEventsGapAndResume(t *testing.T) {
	hub := newTestHub(t, 2, 64)
	f := startFrontend(t, hub, Options{})
	f.send(`{"id":1,"op":"open","adapter":"memory","request":` + string(requestEnvelope(t, "open-1", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{SessionID: "gap"}, "", "")) + `}`)
	requireOK(t, f.expectResponse(1))
	f.send(`{"id":2,"op":"events","session_id":"gap"}`)
	requireOK(t, f.expectResponse(2))
	runToCompletion(t, f, "gap", 3, 4, 5)

	// A cursor at zero asks for the whole run; the journal retains only its
	// tail, so the gap signal reports what is still available.
	f.send(`{"id":6,"op":"events","session_id":"gap","after":0}`)
	requireOK(t, f.expectResponse(6))
	signal := f.line()
	var gap gapLine
	if err := json.Unmarshal([]byte(signal), &gap); err != nil {
		t.Fatalf("gap line %q: %v", signal, err)
	}
	if gap.Event != signalReplayGap || gap.SessionID != "gap" {
		t.Fatalf("gap line %q is not a replay gap for the session", signal)
	}
	if gap.RequestedAfter != 0 || gap.OldestAvailable != 11 || gap.LatestAvailable != 12 {
		t.Fatalf("gap cursor bounds: %+v", gap)
	}

	// Resuming at oldest_available - 1 replays exactly the retained suffix.
	f.send(`{"id":7,"op":"events","session_id":"gap","after":10}`)
	requireOK(t, f.expectResponse(7))
	responses, events := f.group(2)
	if len(responses) != 0 || len(events) != 2 {
		t.Fatalf("resume: %d responses, %d events", len(responses), len(events))
	}
	requireSequences(t, events, 11, 12)
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestSessionClosedSignal parks one subscription with no run and closes the
// session: the close response and the oap-session-closed line may interleave
// freely, and both must arrive.
func TestSessionClosedSignal(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})
	f.send(`{"id":1,"op":"open","adapter":"memory","request":` + string(requestEnvelope(t, "open-1", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{SessionID: "quiet"}, "", "")) + `}`)
	requireOK(t, f.expectResponse(1))
	f.send(`{"id":2,"op":"events","session_id":"quiet"}`)
	requireOK(t, f.expectResponse(2))

	f.send(`{"id":3,"op":"close","session_id":"quiet"}`)
	responses, events := f.group(2)
	if len(responses) != 1 || len(events) != 1 {
		t.Fatalf("close phase: %d responses, %d signals", len(responses), len(events))
	}
	requireOK(t, responses[0])
	if responses[0].ID != 3 {
		t.Fatalf("close response id %d", responses[0].ID)
	}
	var closed sessionClosedLine
	if err := json.Unmarshal([]byte(events[0]), &closed); err != nil {
		t.Fatalf("signal line %q: %v", events[0], err)
	}
	if closed.Event != signalSessionClosed || closed.SessionID != "quiet" {
		t.Fatalf("signal line %q is not a session-closed signal", events[0])
	}

	// A fresh subscription to the closed session is refused, mirroring the
	// HTTP 409 session_closed.
	f.send(`{"id":4,"op":"events","session_id":"quiet"}`)
	requireCode(t, f.expectResponse(4), "session_closed")
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// --- pipelining and concurrency ---

// TestPipelinedEventsBeforeSubmit sends the events op and the submit back to
// back without waiting for the events ack: the subscription is registered
// synchronously in the read loop, so the run's first envelope cannot be
// missed — the stdio counterpart of the client's subscribe-before-submit.
func TestPipelinedEventsBeforeSubmit(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})
	f.send(`{"id":1,"op":"open","adapter":"memory","request":` + string(requestEnvelope(t, "open-1", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{SessionID: "pipe"}, "", "")) + `}`)
	requireOK(t, f.expectResponse(1))
	f.send(`{"id":2,"op":"events","session_id":"pipe"}`)
	f.send(`{"id":3,"op":"submit","session_id":"pipe","request":` + string(requestEnvelope(t, "submit-1", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "pipe", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
	}, "pipe", "")) + `}`)
	responses, events := f.group(6)
	if len(responses) != 2 || len(events) != 4 {
		t.Fatalf("pipelined phase: %d responses, %d events", len(responses), len(events))
	}
	for _, response := range responses {
		requireOK(t, response)
	}
	requireSequences(t, events, 1, 4)
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestConcurrentOpsInterleaving pipelines three whole sessions with no
// per-line waiting: handlers run concurrently and three subscription pumps
// interleave through the single writer. The deterministic facts are the line
// atomicity, the id correlation, and each session's envelope order.
func TestConcurrentOpsInterleaving(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})

	sessions := []string{"c1", "c2", "c3"}
	nextID := int64(0)
	idOf := make(map[string]int64)
	send := func(op, session, extra string) {
		nextID++
		idOf[op+"/"+session] = nextID
		f.send(fmt.Sprintf(`{"id":%d,"op":%q,"session_id":%q%s}`, nextID, op, session, extra))
	}
	// Requests run concurrently, so a host acks the open before addressing
	// the session — the same discipline the HTTP client follows — and then
	// pipelines freely: the events registration happens in the read loop, so
	// a submit pipelined behind its events op cannot miss the first
	// envelope, and the three sessions' ops and pumps all interleave.
	for _, session := range sessions {
		nextID++
		f.send(fmt.Sprintf(`{"id":%d,"op":"open","adapter":"memory","request":%s}`, nextID, requestEnvelope(t, "open-"+session, protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{SessionID: protocol.SessionID(session)}, "", "")))
	}
	for range sessions {
		requireOK(t, f.decodeResponse(f.line()))
	}
	for _, session := range sessions {
		send("events", session, "")
		send("submit", session, `,"request":`+string(requestEnvelope(t, "submit-"+session, protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
			SessionID: protocol.SessionID(session), Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
		}, session, "")))
	}
	responses, events := f.group(18)
	if len(responses) != 6 || len(events) != 12 {
		t.Fatalf("admission phase: %d responses, %d events", len(responses), len(events))
	}
	for _, response := range responses {
		requireOK(t, response)
	}
	bySession := splitEvents(t, events)
	var gates = make(map[string]string)
	for _, session := range sessions {
		requireSequences(t, bySession[session], 1, 4)
		gates[session] = bySession[session][3]
	}
	for _, session := range sessions {
		send("resolve", session, `,"request":`+string(resolveEnvelope(t, "resolve-p-"+session, gates[session], session)))
	}
	responses, events = f.group(18)
	if len(responses) != 3 || len(events) != 15 {
		t.Fatalf("permission phase: %d responses, %d events", len(responses), len(events))
	}
	bySession = splitEvents(t, events)
	for _, session := range sessions {
		requireSequences(t, bySession[session], 5, 9)
		gates[session] = bySession[session][3]
	}
	for _, session := range sessions {
		send("resolve", session, `,"request":`+string(resolveEnvelope(t, "resolve-i-"+session, gates[session], session)))
	}
	responses, events = f.group(12)
	if len(responses) != 3 || len(events) != 9 {
		t.Fatalf("input phase: %d responses, %d events", len(responses), len(events))
	}
	bySession = splitEvents(t, events)
	for _, session := range sessions {
		requireSequences(t, bySession[session], 10, 12)
		if last := bySession[session][2]; !strings.Contains(last, `"run.completed"`) {
			t.Fatalf("session %s did not end on run.completed: %s", session, last)
		}
	}
	for _, session := range sessions {
		send("state", session, "")
		send("close", session, "")
	}
	final := f.lines(6)
	sawClose := 0
	for _, line := range final {
		response := f.decodeResponse(line)
		requireOK(t, response)
		if strings.Contains(line, `"result":null`) {
			sawClose++
		}
	}
	if sawClose != 3 {
		t.Fatalf("%d close responses, want 3", sawClose)
	}
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// splitEvents groups envelope lines by their session_id, preserving arrival
// order within one session.
func splitEvents(t *testing.T, events []string) map[string][]string {
	t.Helper()
	bySession := make(map[string][]string)
	for _, line := range events {
		var event envelopeLine
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("event line %q: %v", line, err)
		}
		bySession[event.SessionID] = append(bySession[event.SessionID], line)
	}
	return bySession
}

// --- error surface ---

// TestOpErrorCodesMirrorHTTP drives the refusal codes the HTTP routes emit
// and requires the stdio error responses to carry the same codes.
func TestOpErrorCodesMirrorHTTP(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})
	nextID := int64(0)
	op := func(line string, code string) {
		t.Helper()
		nextID++
		f.send(strings.Replace(line, "%ID%", fmt.Sprint(nextID), 1))
		requireCode(t, f.expectResponse(nextID), code)
	}

	op(`{"id":%ID%,"op":"capabilities","adapter":"nope"}`, "unknown_adapter")
	op(`{"id":%ID%,"op":"open","adapter":"nope","request":`+string(requestEnvelope(t, "o", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{}, "", ""))+`}`, "unknown_adapter")
	op(`{"id":%ID%,"op":"state","session_id":"nope"}`, "unknown_session")
	op(`{"id":%ID%,"op":"close","session_id":"nope"}`, "unknown_session")
	op(`{"id":%ID%,"op":"events","session_id":"nope"}`, "unknown_session")
	op(`{"id":%ID%,"op":"submit","session_id":"nope","request":`+string(requestEnvelope(t, "s", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{SessionID: "nope", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}}}, "nope", ""))+`}`, "unknown_session")
	op(`{"id":%ID%,"op":"bogus"}`, "unknown_op")
	op(`{"id":%ID%,"op":"adapters","adapter":"memory"}`, "invalid_request")
	op(`{"id":%ID%,"op":"state","session_id":"s","after":3}`, "invalid_request")
	op(`{"id":%ID%,"op":"open","adapter":"memory"}`, "invalid_request")
	op(`{"id":%ID%,"op":"submit","session_id":"s"}`, "invalid_request")
	op(`{"id":%ID%,"op":"adapters","request":{}}`, "invalid_request")
	op(`{"id":%ID%,"op":"open","adapter":"memory","request":"not an envelope"}`, "malformed_json")
	op(`{"id":%ID%,"op":"open","adapter":"memory","request":{"type":"session.open.request","payload":{}}}`, "schema_invalid")
	op(`{"id":%ID%,"op":"open","adapter":"memory","request":`+string(requestEnvelope(t, "w", protocol.TypeRunCancelRequest, protocol.RunCancelRequest{SessionID: "err", RunID: "run-9"}, "err", "run-9"))+`}`, "type_mismatch")

	// A real session makes the session-scoped refusals reachable.
	f.send(`{"id":100,"op":"open","adapter":"memory","request":` + string(requestEnvelope(t, "open-1", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{SessionID: "err"}, "", "")) + `}`)
	requireOK(t, f.expectResponse(100))
	f.send(`{"id":101,"op":"open","adapter":"memory","request":` + string(requestEnvelope(t, "open-2", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{SessionID: "err"}, "", "")) + `}`)
	requireCode(t, f.expectResponse(101), "session_exists")
	op(`{"id":%ID%,"op":"submit","session_id":"err","request":`+string(requestEnvelope(t, "s2", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "other", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}},
	}, "other", ""))+`}`, "scope_mismatch")
	op(`{"id":%ID%,"op":"cancel","session_id":"err","request":`+string(requestEnvelope(t, "c", protocol.TypeRunCancelRequest, protocol.RunCancelRequest{SessionID: "err", RunID: "run-99"}, "err", "run-99"))+`}`, "run_not_found")
	op(`{"id":%ID%,"op":"events","session_id":"err","after":"x"}`, "invalid_cursor")
	op(`{"id":%ID%,"op":"events","session_id":"err","after":7}`, "no_run_to_resume")

	// An active run refuses close; a completed run refuses a second cancel.
	f.send(`{"id":110,"op":"events","session_id":"err"}`)
	requireOK(t, f.expectResponse(110))
	f.send(`{"id":111,"op":"submit","session_id":"err","request":` + string(requestEnvelope(t, "s3", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "err", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
	}, "err", "")) + `}`)
	responses, events := f.group(5)
	if len(responses) != 1 || len(events) != 4 {
		t.Fatalf("submit phase: %d responses, %d events", len(responses), len(events))
	}
	requireOK(t, responses[0])
	var admission protocol.MessageSubmitResponse
	if err := json.Unmarshal(responses[0].Result, &admission); err != nil {
		t.Fatal(err)
	}
	op(`{"id":%ID%,"op":"close","session_id":"err"}`, "run_active")

	f.send(`{"id":120,"op":"cancel","session_id":"err","request":` + string(requestEnvelope(t, "c2", protocol.TypeRunCancelRequest, protocol.RunCancelRequest{SessionID: "err", RunID: admission.RunID}, "err", string(admission.RunID))) + `}`)
	responses, events = f.group(5)
	if len(responses) != 1 || len(events) != 4 {
		t.Fatalf("cancel phase: %d responses, %d events", len(responses), len(events))
	}
	requireOK(t, responses[0])
	if responses[0].ID != 120 {
		t.Fatalf("cancel response id %d", responses[0].ID)
	}
	requireSequences(t, events, 5, 8)
	op(`{"id":%ID%,"op":"events","session_id":"err","after":99}`, "replay_cursor_future")

	f.send(`{"id":130,"op":"close","session_id":"err"}`)
	requireOK(t, f.expectResponse(130))
	// The subscription ended at the cancelled run's terminal event, so no
	// closed-session signal follows; the close-while-parked signal is
	// TestSessionClosedSignal's subject.
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// --- fail-closed framing ---

// TestMalformedLinesFailClosed drives one valid request followed by each
// framing defect: the response for the valid request is still flushed, Run
// fails closed with the offending line number, and nothing else is emitted.
func TestMalformedLinesFailClosed(t *testing.T) {
	cases := []struct {
		name  string
		line  string
		raw   bool // written without the LF terminator
		fresh bool // needs its own small frame limit
	}{
		{name: "not json", line: "not json"},
		{name: "array", line: `[1,2]`},
		{name: "string", line: `"adapters"`},
		{name: "number", line: `7`},
		{name: "missing op", line: `{"id":2}`},
		{name: "missing id", line: `{"op":"adapters"}`},
		{name: "null id", line: `{"id":null,"op":"adapters"}`},
		{name: "string id", line: `{"id":"2","op":"adapters"}`},
		{name: "fractional id", line: `{"id":2.5,"op":"adapters"}`},
		{name: "unknown field", line: `{"id":2,"op":"adapters","extra":1}`},
		{name: "trailing object", line: `{"id":2,"op":"adapters"} {"id":3}`},
		{name: "empty line", line: ""},
		{name: "carriage return", line: "{\"id\":2,\"op\":\"adapters\"}\r"},
		{name: "invalid utf8", line: "{\"id\":2,\"op\":\"\xff\"}"},
		{name: "unterminated", line: `{"id":2,"op":"adapters"`, raw: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			options := Options{}
			if testCase.fresh {
				options.FrameLimit = 64
			}
			hub := newTestHub(t, 64, 64)
			f := startFrontend(t, hub, options)
			f.send(`{"id":1,"op":"adapters"}`)
			if testCase.raw {
				if _, err := f.stdin.Write([]byte(testCase.line)); err != nil {
					t.Fatal(err)
				}
			} else {
				f.send(testCase.line)
			}
			// The admitted request's response is flushed before the exit.
			response := f.decodeResponse(f.line())
			if response.ID != 1 || !response.OK {
				t.Fatalf("prior response not flushed: %+v", response)
			}
			err := f.finish()
			var malformed *MalformedLineError
			if !errors.As(err, &malformed) || malformed.Line != 2 {
				t.Fatalf("finish returned %v, want MalformedLineError on line 2", err)
			}
			if malformed.Detail == "" {
				t.Fatal("malformed error carries no detail")
			}
		})
	}
}

// TestOversizedLineFailsClosed bounds one line with a small frame limit.
func TestOversizedLineFailsClosed(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{FrameLimit: 128})
	f.send(`{"id":1,"op":"adapters"}`)
	f.send(`{"id":2,"op":"adapters","pad":"` + strings.Repeat("x", 200) + `"}`)
	response := f.decodeResponse(f.line())
	if response.ID != 1 || !response.OK {
		t.Fatalf("prior response not flushed: %+v", response)
	}
	err := f.finish()
	var malformed *MalformedLineError
	if !errors.As(err, &malformed) || malformed.Line != 2 {
		t.Fatalf("finish returned %v, want MalformedLineError on line 2", err)
	}
}

// --- slow-consumer backpressure ---

// creditOut is a consumer whose reading the test meters write by write: the
// frontend's writer may emit one line per granted credit, so a host that
// stops reading stdout — no further credits — stalls the pipeline exactly.
type creditOut struct {
	credits chan struct{}
	inner   io.Writer
}

func newCreditOut(inner io.Writer) *creditOut {
	return &creditOut{credits: make(chan struct{}, 64), inner: inner}
}

func (c *creditOut) grant(count int) {
	for index := 0; index < count; index++ {
		c.credits <- struct{}{}
	}
}

func (c *creditOut) Write(p []byte) (int, error) {
	<-c.credits
	return c.inner.Write(p)
}

// stagedAdapter is a test adapter whose run emits a first envelope burst,
// parks until the test releases it, then emits a second burst and reports
// the adapter-side event-stream overflow: the deterministic trigger for the
// daemon's oap-overflow signal while the consumer is stalled. The hub-side
// mailbox-drop path that produces the same signal is covered by the serve
// package's own tests; the frontend converges both onto one line.
type stagedAdapter struct {
	release chan struct{}
}

func (a *stagedAdapter) Probe(context.Context) (base.Descriptor, error) {
	return base.Descriptor{
		Capabilities: protocol.CapabilityDescriptor{
			Endpoint:         protocol.EndpointDescriptor{ID: "staged.test", Name: "Staged test adapter", Version: "0.1", Adapter: "process-memory-script"},
			ProtocolVersions: []string{protocol.Version},
			Profiles:         []string{protocol.Profile},
		},
		CapabilityRevision: "staged-test-v1",
	}, nil
}

func (a *stagedAdapter) Open(_ context.Context, request base.OpenRequest) (base.Session, error) {
	id := request.SessionID
	if id == "" {
		id = "staged-1"
	}
	return &stagedSession{
		adapter: a, id: id,
		state: protocol.SessionState{SessionID: id, Status: protocol.SessionIdle},
	}, nil
}

type stagedSession struct {
	adapter *stagedAdapter
	mu      sync.Mutex
	id      protocol.SessionID
	state   protocol.SessionState
	journal []protocol.Envelope
	settled bool
	closed  bool
}

func (s *stagedSession) Submit(_ context.Context, request protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, base.ErrSessionClosed
	}
	s.state.Status = protocol.SessionRunning
	s.state.ActiveRunID = "run-staged"
	s.mu.Unlock()
	stream := make(chan base.Result, 32)
	go func() {
		for sequence := uint64(1); sequence <= 4; sequence++ {
			stream <- base.Result{Envelope: s.emit(sequence)}
		}
		// Park: the test holds the second burst until the consumer stalls.
		<-s.adapter.release
		for sequence := uint64(5); sequence <= 8; sequence++ {
			stream <- base.Result{Envelope: s.emit(sequence)}
		}
		s.mu.Lock()
		s.settled = true
		s.state.Status = protocol.SessionIdle
		s.state.ActiveRunID = ""
		s.mu.Unlock()
		stream <- base.Result{Error: base.ErrEventStreamOverflow}
		close(stream)
	}()
	return protocol.MessageSubmitResponse{
		SessionID: s.id, Accepted: true, RunID: "run-staged", Status: protocol.RunRunning,
	}, stream, nil
}

func (s *stagedSession) emit(sequence uint64) protocol.Envelope {
	envelope, err := protocol.NewEnvelope(protocol.TypeContentDelta, protocol.EnvelopeID(fmt.Sprintf("staged-%02d", sequence)), protocol.ContentDeltaPayload{
		SessionID: s.id, RunID: "run-staged", MessageID: "staged-message",
		Part: protocol.ContentPart{Type: protocol.ContentText, Text: fmt.Sprintf("staged %d", sequence)},
	})
	if err != nil {
		panic(err)
	}
	envelope.Sequence = &sequence
	envelope.SessionID = s.id
	envelope.RunID = "run-staged"
	s.mu.Lock()
	s.journal = append(s.journal, envelope)
	s.mu.Unlock()
	return envelope
}

func (s *stagedSession) State(context.Context) (protocol.SessionState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, nil
}

func (s *stagedSession) Resolve(context.Context, base.InteractionResolution) error { return nil }

func (s *stagedSession) Cancel(context.Context, protocol.RunID) (protocol.RunCancelResponse, error) {
	return protocol.RunCancelResponse{SessionID: s.id, RunID: "run-staged", Accepted: true, Status: protocol.RunCancelled}, nil
}

func (s *stagedSession) Resume(_ context.Context, request base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stream := make(chan base.Result, 32)
	recovery := base.Recovery{State: s.state, RunID: "run-staged", RequestedAfter: request.AfterSequence}
	for _, envelope := range s.journal {
		if envelope.Sequence != nil && *envelope.Sequence > request.AfterSequence {
			replayed := envelope
			stream <- base.Result{Envelope: replayed}
		}
	}
	if s.settled {
		close(stream)
	}
	return recovery, stream, nil
}

func (s *stagedSession) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// TestSlowConsumerBackpressure stalls the consumer across a run's second
// envelope burst: the writer blocks on the ungranted credit, the line queue,
// the pump, and the bounded mailbox absorb only a bounded amount, and the
// adapter-reported overflow surfaces as the oap-overflow signal with the
// resume cursor once the consumer returns; an events op resuming after the
// cursor replays cleanly.
func TestSlowConsumerBackpressure(t *testing.T) {
	registry := serve.NewRegistry()
	staged := &stagedAdapter{release: make(chan struct{})}
	if err := registry.Register("staged", staged); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{StreamQueue: 8})
	server, err := New(hub, Options{WriteQueue: 1})
	if err != nil {
		t.Fatal(err)
	}
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	out := newCreditOut(stdoutWriter)
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), stdinReader, out) }()
	f := &frontend{t: t, stdin: stdinWriter, reader: bufio.NewReader(stdoutReader), stdout: stdoutReader, done: done}

	out.grant(2)
	f.send(`{"id":1,"op":"open","adapter":"staged","request":` + string(requestEnvelope(t, "open-1", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{SessionID: "slow"}, "", "")) + `}`)
	requireOK(t, f.expectResponse(1))
	f.send(`{"id":2,"op":"events","session_id":"slow"}`)
	requireOK(t, f.expectResponse(2))

	// The consumer keeps up through the first burst: five lines, granted.
	out.grant(5)
	f.send(`{"id":3,"op":"submit","session_id":"slow","request":` + string(requestEnvelope(t, "submit-1", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "slow", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
	}, "slow", "")) + `}`)
	responses, events := f.group(5)
	if len(responses) != 1 || len(events) != 4 {
		t.Fatalf("submit phase: %d responses, %d events", len(responses), len(events))
	}
	requireOK(t, responses[0])
	requireSequences(t, events, 1, 4)

	// The consumer stops reading; the second burst and the adapter's
	// overflow report flow into the bounded pipeline and stop there.
	close(staged.release)
	time.Sleep(100 * time.Millisecond)
	out.grant(16)

	lastSequence := uint64(4)
	cursor := uint64(0)
	deadline := time.After(10 * time.Second)
	for cursor == 0 {
		select {
		case <-deadline:
			t.Fatal("frontend did not drain after the consumer returned")
		default:
		}
		line := f.line()
		if strings.HasPrefix(line, `{"id":`) {
			continue
		}
		if strings.Contains(line, signalOverflow) {
			var signal overflowLine
			if err := json.Unmarshal([]byte(line), &signal); err != nil {
				t.Fatalf("overflow line %q: %v", line, err)
			}
			if signal.RunID != "run-staged" || signal.SessionID != "slow" {
				t.Fatalf("overflow signal %+v does not name the run", signal)
			}
			if signal.LastSequence != lastSequence {
				t.Fatalf("overflow cursor %d, want the last delivered sequence %d", signal.LastSequence, lastSequence)
			}
			cursor = signal.LastSequence
			continue
		}
		var event envelopeLine
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("event line %q: %v", line, err)
		}
		if *event.Sequence != lastSequence+1 {
			t.Fatalf("delivered sequence %d after %d: delivery is not contiguous", *event.Sequence, lastSequence)
		}
		lastSequence = *event.Sequence
	}
	if lastSequence != 8 {
		t.Fatalf("second burst delivered through sequence %d, want 8", lastSequence)
	}

	// The cursor at the burst's end replays nothing further; an earlier
	// cursor replays the retained suffix exactly.
	f.send(`{"id":4,"op":"events","session_id":"slow","after":6}`)
	requireOK(t, f.expectResponse(4))
	_, replayed := f.group(2)
	requireSequences(t, replayed, 7, 8)
	f.send(`{"id":5,"op":"state","session_id":"slow"}`)
	requireOK(t, f.expectResponse(5))
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

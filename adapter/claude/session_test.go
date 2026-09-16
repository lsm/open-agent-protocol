package claude

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
	"github.com/lsm/open-agent-protocol/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/adapter/claude/internal/rpc"
	"github.com/lsm/open-agent-protocol/protocol"
)

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

func (i *testIDs) NewID(k string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.n++
	return fmt.Sprintf("%s-%d", k, i.n)
}

// wirePeer is a scripted CLI counterpart driving the production rpc client
// over real pipes. io.Pipe writes block until consumed, so a drain goroutine
// buffers the adapter's writes into a channel the test receives from.
type wirePeer struct {
	t         *testing.T
	client    *rpc.Client
	writeIn   io.Writer
	frames    chan rpc.Message
	userTurns int
}

// pipePair retires both directions of the scripted transport, so client Close
// unblocks a parked reader the way a dying process closes its pipes.
type pipePair struct {
	read, write io.Closer
	once        sync.Once
}

func (p *pipePair) Close() error {
	p.once.Do(func() {
		_ = p.write.Close()
		_ = p.read.Close()
	})
	return nil
}

func newWirePeer(t *testing.T) *wirePeer {
	t.Helper()
	upstreamRead, upstreamWrite := io.Pipe()
	downstreamRead, downstreamWrite := io.Pipe()
	client := rpc.NewClient(upstreamRead, downstreamWrite, rpc.ClientOptions{CloseReadWriter: &pipePair{read: upstreamRead, write: downstreamWrite}})
	t.Cleanup(func() { _ = client.Close() })
	frames := make(chan rpc.Message, 64)
	go func() {
		reader := bufio.NewReader(downstreamRead)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				close(frames)
				return
			}
			message, err := rpc.ParseMessage([]byte(strings.TrimSuffix(line, "\n")))
			if err != nil {
				t.Errorf("adapter wrote an invalid frame %q: %v", line, err)
				close(frames)
				return
			}
			frames <- message
		}
	}()
	return &wirePeer{t: t, client: client, writeIn: upstreamWrite, frames: frames}
}

func (w *wirePeer) send(line string) {
	w.t.Helper()
	if _, err := w.writeIn.Write([]byte(line + "\n")); err != nil {
		w.t.Fatalf("peer write: %v", err)
	}
}

// written returns the next frame the adapter wrote (user turn or control).
func (w *wirePeer) written() (rpc.Message, json.RawMessage) {
	w.t.Helper()
	select {
	case message, ok := <-w.frames:
		if !ok {
			w.t.Fatal("adapter write stream ended")
		}
		if message.Kind == rpc.KindObservation && message.Type == rpc.TypeUser {
			w.userTurns++
		}
		return message, message.Raw
	case <-time.After(5 * time.Second):
		w.t.Fatal("adapter wrote nothing")
		return rpc.Message{}, nil
	}
}

func (w *wirePeer) writtenUser() map[string]any {
	w.t.Helper()
	message, raw := w.written()
	if message.Kind != rpc.KindObservation || message.Type != rpc.TypeUser {
		w.t.Fatalf("expected a user turn, got %+v", message)
	}
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		w.t.Fatal(err)
	}
	return frame
}

// answerControl answers a control_request the adapter issued.
func (w *wirePeer) answerControl(id, payload string) {
	w.t.Helper()
	w.send(`{"type":"control_response","response":{"subtype":"success","request_id":"` + id + `","response":` + payload + `}}`)
}

// awaitDrain blocks until the reducer has consumed every frame sent so far:
// a control call's response barriers behind all wire-earlier observations,
// so its return proves the scripted frames reduced.
func (w *wirePeer) awaitDrain() {
	w.t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- w.client.Call(context.Background(), json.RawMessage(`{"subtype":"interrupt"}`), nil)
	}()
	message, _ := w.written()
	w.answerControl(message.RequestID, `{}`)
	select {
	case err := <-done:
		if err != nil {
			w.t.Fatalf("drain barrier call failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		w.t.Fatal("reducer did not drain the scripted frames")
	}
}

func openWire(t *testing.T) (base.Adapter, base.Session, *wirePeer) {
	t.Helper()
	peer := newWirePeer(t)
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return peer.client, nil }), Model: "claude-test", Clock: &testClock{}, IDs: &testIDs{}, JournalCapacity: 64})
	if err != nil {
		t.Fatal(err)
	}
	session := adaptertest.AssertInitialState(t, implementation, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
	return implementation, session, peer
}

// gateWriter blocks the adapter's next write once armed until unblocked, so a
// test can hold the native answer mid-write while it injects a racing frame.
type gateWriter struct {
	inner   io.Writer
	mu      sync.Mutex
	blocked bool
	entered chan struct{}
	release chan struct{}
}

func (g *gateWriter) arm() {
	g.mu.Lock()
	g.blocked = true
	g.mu.Unlock()
}
func (g *gateWriter) unblock() {
	g.mu.Lock()
	g.blocked = false
	g.mu.Unlock()
	close(g.release)
}
func (g *gateWriter) Write(p []byte) (int, error) {
	g.mu.Lock()
	blocked := g.blocked
	g.mu.Unlock()
	if blocked {
		select {
		case g.entered <- struct{}{}:
		default:
		}
		<-g.release
	}
	return g.inner.Write(p)
}

func openWireBlocking(t *testing.T) (base.Session, *wirePeer, *gateWriter) {
	t.Helper()
	upstreamRead, upstreamWrite := io.Pipe()
	downstreamRead, downstreamWrite := io.Pipe()
	gated := &gateWriter{inner: downstreamWrite, entered: make(chan struct{}, 1), release: make(chan struct{})}
	client := rpc.NewClient(upstreamRead, gated, rpc.ClientOptions{CloseReadWriter: &pipePair{read: upstreamRead, write: downstreamWrite}})
	t.Cleanup(func() { _ = client.Close() })
	frames := make(chan rpc.Message, 64)
	go func() {
		reader := bufio.NewReader(downstreamRead)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				close(frames)
				return
			}
			message, err := rpc.ParseMessage([]byte(strings.TrimSuffix(line, "\n")))
			if err != nil {
				close(frames)
				return
			}
			frames <- message
		}
	}()
	peer := &wirePeer{t: t, client: client, writeIn: upstreamWrite, frames: frames}
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil }), Model: "claude-test", Clock: &testClock{}, IDs: &testIDs{}, JournalCapacity: 64})
	if err != nil {
		t.Fatal(err)
	}
	session := adaptertest.AssertInitialState(t, implementation, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
	return session, peer, gated
}

// The native user frame carries no model override, so a per-submit model cannot
// be applied; it must be refused rather than silently running the session model.
func TestSubmitRejectsUnappliedModelID(t *testing.T) {
	_, session, _ := openWire(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, err := session.Submit(ctx, protocol.MessageSubmitRequest{
		SessionID: "session", Delivery: protocol.DeliveryAuto, ModelID: protocol.ControlValue("claude-other"),
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}},
	})
	if !errors.Is(err, base.ErrUnsupportedInput) {
		t.Fatalf("got %v, want ErrUnsupportedInput", err)
	}
}

// submit starts a submit in the background; the caller scripts the wire and
// then awaits the outcome.
func submit(session base.Session) chan submitOutcome {
	channel := make(chan submitOutcome, 1)
	go func() {
		admission, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
		channel <- submitOutcome{admission, stream, err}
	}()
	return channel
}

type submitOutcome struct {
	admission protocol.MessageSubmitResponse
	stream    base.EventStream
	err       error
}

func awaitSubmit(t *testing.T, channel chan submitOutcome) submitOutcome {
	t.Helper()
	select {
	case outcome := <-channel:
		return outcome
	case <-time.After(5 * time.Second):
		t.Fatal("submit did not return")
		return submitOutcome{}
	}
}

const peerSession = "3b926aac-d113-4b86-9dc1-0c36b2013f93"

const initFrame = `{"type":"system","subtype":"init","session_id":"` + peerSession + `","tools":["Task","Bash"],"mcp_servers":[],"model":"claude-test","permissionMode":"default","slash_commands":[],"apiKeySource":"none","claude_code_version":"2.1.263","capabilities":["interrupt_receipt_v1","msg_lifecycle_v1"],"uuid":"i1"}`

// turnUUIDOf extracts the submitted uuid from a written user turn.
func turnUUIDOf(t *testing.T, frame map[string]any) string {
	t.Helper()
	uuid, _ := frame["uuid"].(string)
	if uuid == "" {
		t.Fatalf("user turn carries no uuid: %v", frame)
	}
	return uuid
}

func resultFrame(uuid, subtype string, isError bool, terminal, text string, queued int) string {
	isErrorJSON := "false"
	if isError {
		isErrorJSON = "true"
	}
	return `{"type":"result","subtype":"` + subtype + `","duration_ms":130,"duration_api_ms":75,"is_error":` + isErrorJSON + `,"num_turns":1,"session_id":"` + peerSession + `","stop_reason":"end_turn","total_cost_usd":0.0001,"usage":{"input_tokens":7,"output_tokens":5},"modelUsage":{},"permission_denials":[],"terminal_reason":"` + terminal + `","result":"` + text + `","user_message_uuid":"` + uuid + `","user_message_uuids":["` + uuid + `"],"queued_turn_count":` + jsonInt(int64(queued)) + `,"uuid":"r1"}`
}

func jsonInt(v int64) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func streamEcho(uuid string) string {
	return `{"type":"stream_event","event":{"type":"message_start"},"session_id":"` + peerSession + `","parent_tool_use_id":null,"uuid":"e1","user_message_uuid":"` + uuid + `","user_message_uuids":["` + uuid + `"]}`
}

func textDelta(uuid2, text string) string {
	return `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + text + `"}},"session_id":"` + peerSession + `","parent_tool_use_id":null,"uuid":"e2"}`
}

func terminalOf(events []protocol.Envelope) protocol.Envelope {
	if len(events) == 0 {
		return protocol.Envelope{}
	}
	return events[len(events)-1]
}

func eventTypes(events []protocol.Envelope) []string {
	var out []string
	for _, event := range events {
		out = append(out, string(event.Type))
	}
	return out
}

// assertValidTrace runs the shared protocol assertion with the adapter's live
// descriptor. assertCancelledTrace is the variant for runs the test itself
// cancelled: it splices the harness-side exchange the cancellation implies.
func assertValidTrace(t *testing.T, admission protocol.MessageSubmitResponse, events []protocol.Envelope) {
	t.Helper()
	adaptertest.AssertProtocolValidWithDescriptor(t, admission, testDescriptor(t), events)
}

func assertCancelledTrace(t *testing.T, admission protocol.MessageSubmitResponse, events []protocol.Envelope) {
	t.Helper()
	adaptertest.AssertProtocolValidWithCancellation(t, admission, testDescriptor(t), events)
}

func testDescriptor(t *testing.T) base.Descriptor {
	t.Helper()
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return nil, errors.New("unused") })})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := implementation.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

func TestSubmitAdmissionViaStreamEventEcho(t *testing.T) {
	_, session, peer := openWire(t)
	outcome := submit(session)
	written := peer.writtenUser()
	if written["session_id"] != "default" {
		t.Fatalf("logical stream label = %v", written["session_id"])
	}
	if origin, ok := written["origin"].(map[string]any); !ok || origin["kind"] != "human" {
		t.Fatalf("origin = %v", written["origin"])
	}
	uuid := turnUUIDOf(t, written)
	peer.send(initFrame)
	peer.send(streamEcho(uuid))
	result := awaitSubmit(t, outcome)
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.admission.Admission != protocol.AdmissionStarted || result.stream == nil {
		t.Fatalf("admission = %+v", result.admission)
	}
	if result.admission.SubmissionID == "" || result.admission.RunID == "" {
		t.Fatalf("identities = %+v", result.admission)
	}
	peer.send(textDelta(uuid, "fixture "))
	peer.send(textDelta(uuid, "response"))
	peer.send(resultFrame(uuid, "success", false, "completed", "fixture response", 0))
	events := adaptertest.Drain(t, result.stream, 5*time.Second)
	assertValidTrace(t, result.admission, events)
	last := terminalOf(events)
	if last.Type != protocol.TypeRunCompleted {
		t.Fatalf("terminal = %s (%v)", last.Type, eventTypes(events))
	}
	var payload protocol.RunCompletedPayload
	if err := last.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if text, _ := payload.FinalResponse.Content.Text(); text != "fixture response" || payload.StopReason != "completed" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Usage == nil || payload.Usage.InputTokens != 7 || payload.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", payload.Usage)
	}
	deltas := 0
	for _, event := range events {
		if event.Type == protocol.TypeContentDelta {
			deltas++
		}
	}
	if deltas != 2 {
		t.Fatalf("deltas = %d", deltas)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEchoOnAssistantFrameConverges(t *testing.T) {
	_, session, peer := openWire(t)
	outcome := submit(session)
	uuid := turnUUIDOf(t, peer.writtenUser())
	// Without stream events the first complete assistant frame carries the
	// echo.
	peer.send(`{"type":"assistant","message":{"id":"msg_1","model":"claude-test","content":[{"type":"text","text":"hi"}],"stop_reason":null,"usage":{"input_tokens":7}},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"a1","user_message_uuid":"` + uuid + `"}`)
	result := awaitSubmit(t, outcome)
	if result.err != nil {
		t.Fatal(result.err)
	}
	peer.send(resultFrame(uuid, "success", false, "completed", "hi", 0))
	events := adaptertest.Drain(t, result.stream, 5*time.Second)
	if len(events) == 0 || events[0].Type != protocol.TypeRunStarted {
		t.Fatalf("events = %v", eventTypes(events))
	}
	assertValidTrace(t, result.admission, events)
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBufferedObservationsReplayInWireOrder(t *testing.T) {
	_, session, peer := openWire(t)
	outcome := submit(session)
	uuid := turnUUIDOf(t, peer.writtenUser())
	// Wire-earlier frames arrive before the echo and buffer; the echo rides
	// the first complete assistant frame, whose tool_use content must reduce
	// AFTER run.started.
	peer.send(`{"type":"command_lifecycle","command_uuid":"` + uuid + `","state":"queued","session_id":"` + peerSession + `","uuid":"c1"}`)
	peer.send(`{"type":"system","subtype":"status","status":"requesting","session_id":"` + peerSession + `","uuid":"s1"}`)
	peer.send(`{"type":"assistant","message":{"id":"msg_1","model":"claude-test","content":[{"type":"tool_use","id":"toolu_00","name":"Read","input":{"file_path":"/tmp/x"}}],"stop_reason":null,"usage":{"input_tokens":7}},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"a0","user_message_uuid":"` + uuid + `"}`)
	result := awaitSubmit(t, outcome)
	if result.err != nil {
		t.Fatal(result.err)
	}
	peer.send(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_00","type":"tool_result","content":"data","is_error":false}]},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"u0"}`)
	peer.send(resultFrame(uuid, "success", false, "completed", "done", 0))
	events := adaptertest.Drain(t, result.stream, 5*time.Second)
	assertValidTrace(t, result.admission, events)
	if len(events) < 4 || events[0].Type != protocol.TypeRunStarted || events[1].Type != protocol.TypeActionCallRequested || events[2].Type != protocol.TypeActionCallStarted || events[3].Type != protocol.TypeActionCallCompleted {
		t.Fatalf("order = %v", eventTypes(events))
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// admit submits, converges admission on the stream echo, and returns the
// submitted uuid plus the submit outcome.
func admit(t *testing.T, session base.Session, peer *wirePeer) (string, submitOutcome) {
	t.Helper()
	outcome := submit(session)
	uuid := turnUUIDOf(t, peer.writtenUser())
	peer.send(initFrame)
	peer.send(streamEcho(uuid))
	return uuid, awaitSubmit(t, outcome)
}

func openGate(t *testing.T, session base.Session, uuid string) *gateState {
	t.Helper()
	impl := session.(*Session)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		impl.reduceMu.Lock()
		var found *gateState
		for _, candidate := range impl.interactions {
			found = candidate
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

func TestPermissionGateAllowAndDeny(t *testing.T) {
	_, session, peer := openWire(t)
	uuid, outcome := admit(t, session, peer)
	peer.send(`{"type":"assistant","message":{"id":"msg_1","model":"claude-test","content":[{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"command":"touch /tmp/x"}}],"stop_reason":null,"usage":{"input_tokens":7}},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"a1"}`)
	peer.send(`{"type":"control_request","request_id":"ask-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"touch /tmp/x"},"blocked_path":"/tmp/x","tool_use_id":"toolu_01"}}`)
	gate := openGate(t, session, uuid)
	if err := session.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: gate.id, SessionID: "session", Answers: []protocol.InputAnswer{{QuestionID: "decision", SelectedOptionIDs: []string{"allow"}}}}}); err != nil {
		t.Fatal(err)
	}
	message, _ := peer.written()
	if message.Kind != rpc.KindControlResponse {
		t.Fatalf("expected control_response, got %+v", message)
	}
	var decision struct {
		Behavior     string          `json:"behavior"`
		UpdatedInput json.RawMessage `json:"updatedInput"`
	}
	if err := json.Unmarshal(message.Response.Response, &decision); err != nil {
		t.Fatal(err)
	}
	if decision.Behavior != "allow" || string(decision.UpdatedInput) != `{"command":"touch /tmp/x"}` {
		t.Fatalf("decision = %s", message.Response.Response)
	}

	peer.send(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_01","type":"tool_result","content":"done","is_error":false}]},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"u1"}`)
	peer.send(resultFrame(uuid, "success", false, "completed", "done", 0))
	events := adaptertest.Drain(t, outcome.stream, 5*time.Second)
	assertValidTrace(t, outcome.admission, events)
	var kinds []string
	for _, event := range events {
		if strings.HasPrefix(string(event.Type), "action.call.") {
			kinds = append(kinds, string(event.Type))
		}
	}
	if strings.Join(kinds, ",") != "action.call.requested,action.call.started,action.call.completed" {
		t.Fatalf("tool lifecycle = %v", kinds)
	}
	hasGate := false
	for _, event := range events {
		if event.Type == protocol.TypeUserInputRequested || event.Type == protocol.TypeUserInputResolved {
			hasGate = true
		}
	}
	if !hasGate {
		t.Fatal("permission interaction not surfaced")
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPermissionGateDenyFailsTool(t *testing.T) {
	_, session, peer := openWire(t)
	uuid, outcome := admit(t, session, peer)
	peer.send(`{"type":"assistant","message":{"id":"msg_1","model":"claude-test","content":[{"type":"tool_use","id":"toolu_02","name":"Bash","input":{"command":"rm -rf /tmp/y"}}],"stop_reason":null,"usage":{"input_tokens":7}},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"a2"}`)
	peer.send(`{"type":"control_request","request_id":"ask-2","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"rm -rf /tmp/y"},"tool_use_id":"toolu_02"}}`)
	gate := openGate(t, session, uuid)
	if err := session.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: gate.id, SessionID: "session", Answers: []protocol.InputAnswer{{QuestionID: "decision", SelectedOptionIDs: []string{"deny"}}}}}); err != nil {
		t.Fatal(err)
	}
	if _, raw := peer.written(); !strings.Contains(string(raw), `"deny"`) {
		t.Fatalf("deny decision = %s", raw)
	}
	// A denial returns an error tool_result and the turn still settles.
	peer.send(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_02","type":"tool_result","content":"User denied the operation","is_error":true}]},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"u2"}`)
	peer.send(resultFrame(uuid, "success", false, "completed", "denied", 0))
	events := adaptertest.Drain(t, outcome.stream, 5*time.Second)
	assertValidTrace(t, outcome.admission, events)
	failed := 0
	for _, event := range events {
		if event.Type == protocol.TypeActionCallFailed {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("failed tool calls = %d", failed)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestResolveValidatesAnswerShapes(t *testing.T) {
	_, session, peer := openWire(t)
	uuid, outcome := admit(t, session, peer)
	peer.send(`{"type":"assistant","message":{"id":"m","model":"claude-test","content":[{"type":"tool_use","id":"toolu_03","name":"Bash","input":{"command":"ls"}}],"stop_reason":null,"usage":{"input_tokens":7}},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"a3"}`)
	peer.send(`{"type":"control_request","request_id":"ask-3","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"},"tool_use_id":"toolu_03"}}`)
	gate := openGate(t, session, uuid)
	for name, answers := range map[string][]protocol.InputAnswer{
		"no answers":       {},
		"unknown option":   {{QuestionID: "decision", SelectedOptionIDs: []string{"maybe"}}},
		"option list":      {{QuestionID: "decision", SelectedOptionIDs: []string{"allow", "deny"}}},
		"text answer":      {{QuestionID: "decision", Text: "allow"}},
		"mixed form":       {{QuestionID: "decision", SelectedOptionIDs: []string{"allow"}, Text: "allow"}},
		"foreign question": {{QuestionID: "other", SelectedOptionIDs: []string{"allow"}}},
	} {
		if err := session.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: gate.id, SessionID: "session", Answers: answers}}); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	// The gate stays resolvable after rejections.
	if err := session.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: gate.id, SessionID: "session", Answers: []protocol.InputAnswer{{QuestionID: "decision", SelectedOptionIDs: []string{"deny"}}}}}); err != nil {
		t.Fatalf("valid answer rejected after invalid ones: %v", err)
	}
	if _, raw := peer.written(); !strings.Contains(string(raw), `"deny"`) {
		t.Fatalf("final decision = %s", raw)
	}
	// The denial returns an error tool_result and the turn settles before the
	// session may close.
	peer.send(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_03","type":"tool_result","content":"User denied the operation","is_error":true}]},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"u4"}`)
	peer.send(resultFrame(uuid, "success", false, "completed", "denied", 0))
	events := adaptertest.Drain(t, outcome.stream, 5*time.Second)
	assertValidTrace(t, outcome.admission, events)
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestResolveRejectsForeignOwnership(t *testing.T) {
	_, session, peer := openWire(t)
	impl := session.(*Session)
	uuid, outcome := admit(t, session, peer)
	peer.send(`{"type":"assistant","message":{"id":"m","model":"claude-test","content":[{"type":"tool_use","id":"toolu_04","name":"Bash","input":{"command":"ls"}}],"stop_reason":null,"usage":{"input_tokens":7}},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"a4"}`)
	peer.send(`{"type":"control_request","request_id":"ask-4","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"},"tool_use_id":"toolu_04"}}`)
	gate := openGate(t, session, uuid)
	allow := func() []protocol.InputAnswer {
		return []protocol.InputAnswer{{QuestionID: "decision", SelectedOptionIDs: []string{"allow"}}}
	}
	// A caller who knows the gate id must not be able to approve as another
	// participant or against a foreign scope.
	for name, resolution := range map[string]base.InteractionResolution{
		"foreign top-level run":   {RunID: "other-run", Input: &protocol.UserInputResolveRequest{InteractionID: gate.id, SessionID: impl.state.SessionID, Answers: allow()}},
		"foreign top responder":   {RespondedBy: "intruder", Input: &protocol.UserInputResolveRequest{InteractionID: gate.id, SessionID: impl.state.SessionID, Answers: allow()}},
		"foreign session":         {Input: &protocol.UserInputResolveRequest{InteractionID: gate.id, SessionID: "other-session", Answers: allow()}},
		"foreign input run":       {Input: &protocol.UserInputResolveRequest{InteractionID: gate.id, SessionID: impl.state.SessionID, RunID: "other-run", Answers: allow()}},
		"foreign input responder": {Input: &protocol.UserInputResolveRequest{InteractionID: gate.id, SessionID: impl.state.SessionID, RespondedBy: "intruder", Answers: allow()}},
		"foreign requester":       {Input: &protocol.UserInputResolveRequest{InteractionID: gate.id, SessionID: impl.state.SessionID, RequestedBy: "intruder", Answers: allow()}},
	} {
		if err := session.Resolve(context.Background(), resolution); !errors.Is(err, base.ErrInvalidResolution) {
			t.Fatalf("%s: got %v, want ErrInvalidResolution", name, err)
		}
	}
	// The gate stays resolvable for the declared participant and scope.
	if err := session.Resolve(context.Background(), base.InteractionResolution{RunID: gate.run.id, RespondedBy: "user", Input: &protocol.UserInputResolveRequest{InteractionID: gate.id, SessionID: impl.state.SessionID, RunID: gate.run.id, RespondedBy: "user", Answers: allow()}}); err != nil {
		t.Fatalf("valid resolution rejected after foreign ones: %v", err)
	}
	if _, raw := peer.written(); !strings.Contains(string(raw), `"allow"`) {
		t.Fatalf("final decision = %s", raw)
	}
	peer.send(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_04","type":"tool_result","content":"done","is_error":false}]},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"u5"}`)
	peer.send(resultFrame(uuid, "success", false, "completed", "done", 0))
	events := adaptertest.Drain(t, outcome.stream, 5*time.Second)
	assertValidTrace(t, outcome.admission, events)
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCancelSettlesOnlyOnAbortedTerminalReason(t *testing.T) {
	_, session, peer := openWire(t)
	uuid, outcome := admit(t, session, peer)
	peer.send(textDelta(uuid, "partial"))
	go func() {
		if _, err := session.Cancel(context.Background(), outcome.admission.RunID); err != nil {
			t.Errorf("cancel: %v", err)
		}
	}()
	message, _ := peer.written()
	if message.Kind != rpc.KindControlRequest || message.Subtype != "interrupt" {
		t.Fatalf("expected an interrupt request, got %+v", message)
	}
	// The receipt acknowledges intent and never settles.
	peer.answerControl(message.RequestID, `{"still_queued":[]}`)
	peer.send(`{"type":"user","message":{"role":"user","content":"[Request interrupted by user]"},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"u3"}`)
	peer.send(`{"type":"result","subtype":"error_during_execution","duration_ms":66,"duration_api_ms":0,"is_error":true,"num_turns":2,"session_id":"` + peerSession + `","stop_reason":null,"usage":{"input_tokens":7,"output_tokens":5},"modelUsage":{},"permission_denials":[],"terminal_reason":"aborted_streaming","errors":["[ede_diagnostic] result_type=user"],"user_message_uuid":"` + uuid + `","user_message_uuids":["` + uuid + `"],"queued_turn_count":0,"uuid":"r2"}`)
	events := adaptertest.Drain(t, outcome.stream, 5*time.Second)
	assertCancelledTrace(t, outcome.admission, events)
	last := terminalOf(events)
	if last.Type != protocol.TypeRunCancelled {
		t.Fatalf("terminal = %s (%v)", last.Type, eventTypes(events))
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAPIErrorResultFailsRun(t *testing.T) {
	_, session, peer := openWire(t)
	uuid, outcome := admit(t, session, peer)
	peer.send(`{"type":"result","subtype":"success","duration_ms":80,"duration_api_ms":0,"is_error":true,"num_turns":1,"session_id":"` + peerSession + `","stop_reason":"stop_sequence","total_cost_usd":0,"usage":{"input_tokens":0,"output_tokens":0},"modelUsage":{},"permission_denials":[],"terminal_reason":"api_error","api_error_status":429,"result":"API Error: rate limited","user_message_uuid":"` + uuid + `","user_message_uuids":["` + uuid + `"],"queued_turn_count":0,"uuid":"r3"}`)
	events := adaptertest.Drain(t, outcome.stream, 5*time.Second)
	assertValidTrace(t, outcome.admission, events)
	last := terminalOf(events)
	if last.Type != protocol.TypeRunFailed {
		t.Fatalf("terminal = %s", last.Type)
	}
	var payload protocol.RunFailedPayload
	if err := last.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != "claude_api_429" || payload.Error.Message != "API Error: rate limited" {
		t.Fatalf("error = %+v", payload.Error)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMaxTurnsCompletesWithLimitReason(t *testing.T) {
	_, session, peer := openWire(t)
	uuid, outcome := admit(t, session, peer)
	peer.send(`{"type":"result","subtype":"error_max_turns","duration_ms":500,"duration_api_ms":400,"is_error":true,"num_turns":3,"session_id":"` + peerSession + `","stop_reason":null,"usage":{"input_tokens":9,"output_tokens":9},"modelUsage":{},"permission_denials":[],"errors":["Max turns reached"],"user_message_uuid":"` + uuid + `","user_message_uuids":["` + uuid + `"],"queued_turn_count":0,"uuid":"r4"}`)
	events := adaptertest.Drain(t, outcome.stream, 5*time.Second)
	assertValidTrace(t, outcome.admission, events)
	last := terminalOf(events)
	if last.Type != protocol.TypeRunCompleted {
		t.Fatalf("terminal = %s", last.Type)
	}
	var payload protocol.RunCompletedPayload
	if err := last.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.StopReason != "max_turns" {
		t.Fatalf("stop reason = %q", payload.StopReason)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestInjectedTurnCreatesNoPhantomRun(t *testing.T) {
	_, session, peer := openWire(t)
	peer.send(`{"type":"user","message":{"role":"user","content":"task notification"},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"inj1","origin":{"kind":"task-notification"}}`)
	peer.send(`{"type":"assistant","message":{"id":"m","model":"claude-test","content":[{"type":"text","text":"noted"}],"stop_reason":null,"usage":{"input_tokens":1}},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"inj2"}`)
	peer.send(`{"type":"result","subtype":"success","duration_ms":10,"duration_api_ms":1,"is_error":false,"num_turns":1,"session_id":"` + peerSession + `","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1},"modelUsage":{},"permission_denials":[],"terminal_reason":"completed","result":"noted","origin":{"kind":"task-notification"},"uuid":"r5"}`)
	peer.awaitDrain()
	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != protocol.SessionIdle {
		t.Fatalf("injected turn changed session status: %s", state.Status)
	}
	impl := session.(*Session)
	impl.mu.Lock()
	runs := len(impl.runs)
	impl.mu.Unlock()
	if runs != 0 {
		t.Fatalf("injected turn created %d runs", runs)
	}
	// The session stays usable for a real submission afterwards.
	uuid, outcome := admit(t, session, peer)
	peer.send(resultFrame(uuid, "success", false, "completed", "ok", 0))
	events := adaptertest.Drain(t, outcome.stream, 5*time.Second)
	assertValidTrace(t, outcome.admission, events)
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSecondTurnSequentialDistinctRuns(t *testing.T) {
	_, session, peer := openWire(t)
	uuid1, outcome1 := admit(t, session, peer)
	peer.send(resultFrame(uuid1, "success", false, "completed", "one", 0))
	events1 := adaptertest.Drain(t, outcome1.stream, 5*time.Second)
	assertValidTrace(t, outcome1.admission, events1)

	uuid2, outcome2 := admit(t, session, peer)
	if uuid2 == uuid1 {
		t.Fatal("second submit reused the turn uuid")
	}
	peer.send(resultFrame(uuid2, "success", false, "completed", "two", 0))
	events2 := adaptertest.Drain(t, outcome2.stream, 5*time.Second)
	assertValidTrace(t, outcome2.admission, events2)
	if outcome1.admission.RunID == outcome2.admission.RunID {
		t.Fatal("second submit reused the run id")
	}
	if outcome1.admission.SubmissionID == outcome2.admission.SubmissionID {
		t.Fatal("second submit reused the submission id")
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestQueuedTurnCountDefersTerminal(t *testing.T) {
	_, session, peer := openWire(t)
	uuid, outcome := admit(t, session, peer)
	peer.send(textDelta(uuid, "first"))
	peer.send(resultFrame(uuid, "success", false, "completed", "first", 1))
	peer.awaitDrain()
	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != protocol.SessionRunning {
		t.Fatalf("queued continuation dropped the run: %s", state.Status)
	}
	peer.send(resultFrame(uuid, "success", false, "completed", "closing", 0))
	events := adaptertest.Drain(t, outcome.stream, 5*time.Second)
	assertValidTrace(t, outcome.admission, events)
	var payload protocol.RunCompletedPayload
	terminal := terminalOf(events)
	if err := terminal.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if text, _ := payload.FinalResponse.Content.Text(); text != "closing" {
		t.Fatalf("closing result = %q", text)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDeferringChildHoldsTerminalUntilSettled(t *testing.T) {
	_, session, peer := openWire(t)
	uuid, outcome := admit(t, session, peer)
	peer.send(`{"type":"system","subtype":"task_started","task_id":"task-1","description":"research","uuid":"t1","session_id":"` + peerSession + `","tool_use_id":"toolu_04","task_type":"local_agent"}`)
	peer.send(resultFrame(uuid, "success", false, "completed", "spawned", 0))
	peer.awaitDrain()
	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != protocol.SessionRunning {
		t.Fatalf("child held no terminal: %s", state.Status)
	}
	peer.send(`{"type":"system","subtype":"task_notification","task_id":"task-1","status":"completed","output_file":"/out","summary":"done","uuid":"t2","session_id":"` + peerSession + `","tool_use_id":"toolu_04"}`)
	events := adaptertest.Drain(t, outcome.stream, 5*time.Second)
	assertValidTrace(t, outcome.admission, events)
	if terminalOf(events).Type != protocol.TypeRunCompleted {
		t.Fatalf("terminal = %s", terminalOf(events).Type)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestTaskUpdatedPatchIsALegalChildTerminal(t *testing.T) {
	_, session, peer := openWire(t)
	uuid, outcome := admit(t, session, peer)
	peer.send(`{"type":"system","subtype":"task_started","task_id":"task-2","description":"workflow","uuid":"t3","session_id":"` + peerSession + `","task_type":"local_workflow"}`)
	peer.send(resultFrame(uuid, "success", false, "completed", "spawned", 0))
	peer.send(`{"type":"system","subtype":"task_updated","task_id":"task-2","session_id":"` + peerSession + `","patch":{"status":"killed","end_time":123}}`)
	events := adaptertest.Drain(t, outcome.stream, 5*time.Second)
	assertValidTrace(t, outcome.admission, events)
	terminal := terminalOf(events)
	if terminal.Type != protocol.TypeRunCompleted {
		t.Fatalf("terminal = %s", terminal.Type)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestUnmatchedToolResultFailsRun(t *testing.T) {
	_, session, peer := openWire(t)
	uuid, outcome := admit(t, session, peer)
	peer.send(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_missing","type":"tool_result","content":"x","is_error":false}]},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"u9"}`)
	events := adaptertest.Drain(t, outcome.stream, 5*time.Second)
	last := terminalOf(events)
	if last.Type != protocol.TypeRunFailed {
		t.Fatalf("terminal = %s", last.Type)
	}
	var payload protocol.RunFailedPayload
	if err := last.DecodePayload(&payload); err != nil || payload.Error.Code != "claude_tool_lifecycle" {
		t.Fatalf("payload = %+v err=%v", payload, err)
	}
	_ = uuid
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestTransportDeathFailsActiveRun(t *testing.T) {
	_, session, peer := openWire(t)
	uuid, outcome := admit(t, session, peer)
	_ = uuid
	peer.send(textDelta(uuid, "partial"))
	// The transport retires: the client closes and the inbound stream ends.
	peer.client.Close()
	events := adaptertest.Drain(t, outcome.stream, 5*time.Second)
	last := terminalOf(events)
	if last.Type != protocol.TypeRunFailed {
		t.Fatalf("terminal = %s", last.Type)
	}
	var payload protocol.RunFailedPayload
	if err := last.DecodePayload(&payload); err != nil || payload.Error.Code != "claude_process_exit" {
		t.Fatalf("payload = %+v err=%v", payload, err)
	}
}

func TestOverlapSubmitRejectedBeforeWrite(t *testing.T) {
	_, session, peer := openWire(t)
	uuid, outcome := admit(t, session, peer)
	peer.send(textDelta(uuid, "streaming"))
	_, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("again")}}})
	if err == nil {
		t.Fatal("overlap accepted")
	}
	if stream != nil {
		t.Fatal("rejected overlap exposed an event stream")
	}
	// No second user turn reached the wire: the rejection happened before
	// any write, and the drain barrier proves nothing else is in flight.
	peer.send(resultFrame(uuid, "success", false, "completed", "done", 0))
	adaptertest.Drain(t, outcome.stream, 5*time.Second)
	peer.awaitDrain()
	if peer.userTurns != 1 {
		t.Fatalf("rejected overlap wrote %d user turns", peer.userTurns)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCancelUnknownRunRejected(t *testing.T) {
	_, session, _ := openWire(t)
	if _, err := session.Cancel(context.Background(), protocol.RunID("run-nope")); err == nil {
		t.Fatal("unknown run cancelled")
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestUnavailableOperations(t *testing.T) {
	_, session, peer := openWire(t)
	if _, stream, err := session.Resume(context.Background(), base.ResumeRequest{RunID: "run"}); err == nil || stream != nil {
		t.Fatal("resume available")
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = peer
}

func TestSubmitRejectsInvalidSurfaces(t *testing.T) {
	_, session, peer := openWire(t)
	cases := map[string]protocol.MessageSubmitRequest{
		"no messages":     {SessionID: "session"},
		"two messages":    {SessionID: "session", Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("a")}, {Role: protocol.RoleUser, Content: protocol.TextContent("b")}}},
		"assistant role":  {SessionID: "session", Messages: []protocol.Message{{Role: protocol.RoleAssistant, Content: protocol.TextContent("a")}}},
		"steer delivery":  {SessionID: "session", Delivery: protocol.DeliverySteer, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("a")}}},
		"foreign session": {SessionID: "other", Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("a")}}},
	}
	for name, request := range cases {
		_, stream, err := session.Submit(context.Background(), request)
		if err == nil {
			t.Fatalf("%s: accepted", name)
		}
		if stream != nil {
			t.Fatalf("%s: returned a stream", name)
		}
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = peer
}

// ---- review regression tests (fail-before evidence) -------------------------

func TestCancelWithOpenToolAndGateSettlesBeforeTerminal(t *testing.T) {
	_, session, peer := openWire(t)
	uuid, outcome := admit(t, session, peer)
	peer.send(`{"type":"assistant","message":{"id":"m","model":"claude-test","content":[{"type":"tool_use","id":"toolu_10","name":"Bash","input":{"command":"ls"}}],"stop_reason":null,"usage":{"input_tokens":7}},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"a10"}`)
	peer.send(`{"type":"control_request","request_id":"ask-10","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"},"tool_use_id":"toolu_10"}}`)
	gate := openGate(t, session, uuid)
	_ = gate
	go func() {
		_, _ = session.Cancel(context.Background(), outcome.admission.RunID)
	}()
	message, _ := peer.written()
	if message.Subtype != "interrupt" {
		t.Fatalf("expected interrupt, got %+v", message)
	}
	peer.answerControl(message.RequestID, `{"still_queued":[]}`)
	peer.send(`{"type":"user","message":{"role":"user","content":"[Request interrupted by user]"},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"u10"}`)
	peer.send(`{"type":"result","subtype":"error_during_execution","duration_ms":66,"duration_api_ms":0,"is_error":true,"num_turns":2,"session_id":"` + peerSession + `","stop_reason":null,"usage":{"input_tokens":7,"output_tokens":5},"modelUsage":{},"permission_denials":[],"terminal_reason":"aborted_streaming","errors":["[ede_diagnostic] result_type=user"],"user_message_uuid":"` + uuid + `","user_message_uuids":["` + uuid + `"],"queued_turn_count":0,"uuid":"r10"}`)
	events := adaptertest.Drain(t, outcome.stream, 5*time.Second)
	assertCancelledTrace(t, outcome.admission, events)
	if terminalOf(events).Type != protocol.TypeRunCancelled {
		t.Fatalf("terminal = %s", terminalOf(events).Type)
	}
	// The abandoned ask is answered so the CLI is never left blocked.
	if _, raw := peer.written(); !strings.Contains(string(raw), `"error"`) {
		t.Fatalf("gate answer = %s", raw)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestResolveSerializesGateBeforeTerminal(t *testing.T) {
	// The CLI can emit its terminal result immediately after reading the control
	// response. If Resolve releases the reducer between marking the gate resolved
	// and emitting user.input.resolved, the terminal settles the run and the
	// resolved event is dropped, leaving an unresolved interaction at
	// terminality. Hold the native answer mid-write across a terminal to prove
	// the resolution is serialized first.
	session, peer, writer := openWireBlocking(t)
	uuid, outcome := admit(t, session, peer)
	peer.send(`{"type":"assistant","message":{"id":"m","model":"claude-test","content":[{"type":"tool_use","id":"toolu_g","name":"Bash","input":{"command":"ls"}}],"stop_reason":null,"usage":{"input_tokens":7}},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"ag"}`)
	peer.send(`{"type":"control_request","request_id":"ask-g","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"},"tool_use_id":"toolu_g"}}`)
	gate := openGate(t, session, uuid)

	writer.arm()
	resolved := make(chan error, 1)
	go func() {
		resolved <- session.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: gate.id, SessionID: "session", Answers: []protocol.InputAnswer{{QuestionID: "decision", SelectedOptionIDs: []string{"allow"}}}}})
	}()
	select {
	case <-writer.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("native answer was never written")
	}
	// The answer is mid-write; race the terminal against it. Delivery happens
	// on a separate goroutine and the pause gives the reader time to route the
	// terminal into the reducer's queue before the answer is released, so the
	// terminal is genuinely contending for the reducer when the bug would let
	// it settle first.
	sent := make(chan struct{})
	go func() {
		peer.send(resultFrame(uuid, "success", false, "completed", "done", 0))
		close(sent)
	}()
	time.Sleep(200 * time.Millisecond)
	writer.unblock()
	<-sent
	if err := <-resolved; err != nil {
		t.Fatalf("resolve: %v", err)
	}
	events := adaptertest.Drain(t, outcome.stream, 5*time.Second)
	assertValidTrace(t, outcome.admission, events)
	resolvedIdx, terminalIdx := -1, -1
	for index, event := range events {
		switch event.Type {
		case protocol.TypeUserInputResolved:
			if resolvedIdx == -1 {
				resolvedIdx = index
			}
		case protocol.TypeRunCompleted, protocol.TypeRunFailed, protocol.TypeRunCancelled:
			if terminalIdx == -1 {
				terminalIdx = index
			}
		}
	}
	if resolvedIdx == -1 {
		t.Fatalf("gate resolution was dropped at terminality: %v", eventTypes(events))
	}
	if terminalIdx != -1 && resolvedIdx > terminalIdx {
		t.Fatalf("resolution index %d after terminal index %d", resolvedIdx, terminalIdx)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSubmitCancellationAfterWriteRetiresSession(t *testing.T) {
	_, session, peer := openWire(t)
	ctx, cancel := context.WithCancel(context.Background())
	channel := make(chan submitOutcome, 1)
	go func() {
		admission, stream, err := session.Submit(ctx, protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
		channel <- submitOutcome{admission, stream, err}
	}()
	peer.writtenUser()
	cancel()
	select {
	case outcome := <-channel:
		if outcome.err == nil {
			t.Fatal("cancelled submit accepted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("submit did not return")
	}
	// The user turn is already on the wire and its outcome is ambiguous: the
	// session must refuse further submissions rather than overlap them.
	if _, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("again")}}}); !errors.Is(err, base.ErrSessionClosed) || stream != nil {
		t.Fatalf("post-cancellation submit err = %v", err)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestForeignTurnDeltaNotAttributed(t *testing.T) {
	_, session, peer := openWire(t)
	uuid, outcome := admit(t, session, peer)
	peer.send(`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"foreign"}},"session_id":"` + peerSession + `","parent_tool_use_id":null,"uuid":"e9","user_message_uuid":"other-turn","user_message_uuids":["other-turn"]}`)
	peer.send(resultFrame(uuid, "success", false, "completed", "done", 0))
	events := adaptertest.Drain(t, outcome.stream, 5*time.Second)
	assertValidTrace(t, outcome.admission, events)
	for _, event := range events {
		if event.Type != protocol.TypeContentDelta {
			continue
		}
		var payload protocol.ContentDeltaPayload
		if err := event.DecodePayload(&payload); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(payload.Part.Text, "foreign") {
			t.Fatal("another turn's delta was attributed to this run")
		}
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestIdleSignalPublishesDeferredTerminal(t *testing.T) {
	_, session, peer := openWire(t)
	uuid, outcome := admit(t, session, peer)
	peer.send(`{"type":"system","subtype":"task_started","task_id":"task-9","description":"research","uuid":"t9","session_id":"` + peerSession + `","task_type":"local_agent"}`)
	peer.send(resultFrame(uuid, "success", false, "completed", "spawned", 0))
	peer.send(`{"type":"system","subtype":"session_state_changed","state":"idle","session_id":"` + peerSession + `","uuid":"ss1"}`)
	events := adaptertest.Drain(t, outcome.stream, 5*time.Second)
	assertValidTrace(t, outcome.admission, events)
	if terminalOf(events).Type != protocol.TypeRunCompleted {
		t.Fatalf("terminal = %s", terminalOf(events).Type)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStaleChildDoesNotDeferLaterRuns(t *testing.T) {
	_, session, peer := openWire(t)
	uuid1, outcome1 := admit(t, session, peer)
	peer.send(`{"type":"system","subtype":"task_started","task_id":"task-stale","description":"research","uuid":"ts","session_id":"` + peerSession + `","task_type":"local_agent"}`)
	peer.send(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_unknown","type":"tool_result","content":"x","is_error":false}]},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"us"}`)
	events1 := adaptertest.Drain(t, outcome1.stream, 5*time.Second)
	if terminalOf(events1).Type != protocol.TypeRunFailed {
		t.Fatalf("run 1 terminal = %s", terminalOf(events1).Type)
	}
	_ = uuid1
	// Run 1's unsettled child must not hold run 2's terminal.
	uuid2, outcome2 := admit(t, session, peer)
	peer.send(resultFrame(uuid2, "success", false, "completed", "second", 0))
	events2 := adaptertest.Drain(t, outcome2.stream, 5*time.Second)
	assertValidTrace(t, outcome2.admission, events2)
	if terminalOf(events2).Type != protocol.TypeRunCompleted {
		t.Fatalf("run 2 terminal = %s", terminalOf(events2).Type)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLateToolResultForPriorRunIgnored(t *testing.T) {
	_, session, peer := openWire(t)
	uuid1, outcome1 := admit(t, session, peer)
	peer.send(`{"type":"assistant","message":{"id":"m","model":"claude-test","content":[{"type":"tool_use","id":"toolu_30","name":"Read","input":{"file_path":"/tmp/x"}}],"stop_reason":null,"usage":{"input_tokens":7}},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"a30"}`)
	peer.send(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_30","type":"tool_result","content":"data","is_error":false}]},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"u30"}`)
	peer.send(resultFrame(uuid1, "success", false, "completed", "one", 0))
	adaptertest.Drain(t, outcome1.stream, 5*time.Second)
	// A late tool_result for run 1's tool must not fail run 2.
	uuid2, outcome2 := admit(t, session, peer)
	peer.send(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_30","type":"tool_result","content":"late","is_error":false}]},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"u31"}`)
	peer.send(resultFrame(uuid2, "success", false, "completed", "two", 0))
	events := adaptertest.Drain(t, outcome2.stream, 5*time.Second)
	assertValidTrace(t, outcome2.admission, events)
	if terminalOf(events).Type != protocol.TypeRunCompleted {
		t.Fatalf("run 2 terminal = %s (%v)", terminalOf(events).Type, eventTypes(events))
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownResultSubtypeFailsRunNotTransport(t *testing.T) {
	_, session, peer := openWire(t)
	uuid, outcome := admit(t, session, peer)
	peer.send(`{"type":"result","subtype":"error_new_future","duration_ms":50,"duration_api_ms":10,"is_error":true,"num_turns":1,"session_id":"` + peerSession + `","stop_reason":null,"usage":{"input_tokens":2,"output_tokens":1},"modelUsage":{},"permission_denials":[],"terminal_reason":"completed","errors":["future failure"],"user_message_uuid":"` + uuid + `","user_message_uuids":["` + uuid + `"],"queued_turn_count":0,"uuid":"rf"}`)
	events := adaptertest.Drain(t, outcome.stream, 5*time.Second)
	terminal := terminalOf(events)
	if terminal.Type != protocol.TypeRunFailed {
		t.Fatalf("terminal = %s", terminal.Type)
	}
	var payload protocol.RunFailedPayload
	if err := terminal.DecodePayload(&payload); err != nil || payload.Error.Code != "claude_error_new_future" {
		t.Fatalf("payload = %+v err=%v", payload, err)
	}
	// The transport survived: the session stays usable for another turn.
	uuid2, outcome2 := admit(t, session, peer)
	peer.send(resultFrame(uuid2, "success", false, "completed", "next", 0))
	events2 := adaptertest.Drain(t, outcome2.stream, 5*time.Second)
	if terminalOf(events2).Type != protocol.TypeRunCompleted {
		t.Fatalf("second terminal = %s", terminalOf(events2).Type)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

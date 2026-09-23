package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/claude/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/claude/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
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

type wirePeer struct {
	t         *testing.T
	client    *rpc.Client
	writeIn   io.Writer
	frames    chan rpc.Message
	userTurns int
}

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

func (w *wirePeer) answerControl(id, payload string) {
	w.t.Helper()
	w.send(`{"type":"control_response","response":{"subtype":"success","request_id":"` + id + `","response":` + payload + `}}`)
}

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

func TestSubmittedTurnIsClientComposedUnlessPromptsExpand(t *testing.T) {
	for _, expand := range []bool{false, true} {
		peer := newWirePeer(t)
		implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return peer.client, nil }), Model: "claude-test", Clock: &testClock{}, IDs: &testIDs{}, JournalCapacity: 64, ExpandPrompts: expand})
		if err != nil {
			t.Fatal(err)
		}
		session := adaptertest.AssertInitialState(t, implementation, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
		outcome := submit(session)
		frame := peer.writtenUser()
		composed, present := frame["client_composed"]
		if expand == present || (!expand && composed != true) {
			t.Fatalf("ExpandPrompts=%v wrote client_composed=%v, present=%v", expand, composed, present)
		}
		uuid := turnUUIDOf(t, frame)
		peer.send(streamEcho(uuid))
		result := awaitSubmit(t, outcome)
		if result.err != nil {
			t.Fatal(result.err)
		}
		peer.send(resultFrame(uuid, "success", false, "completed", "done", 0))
		assertValidTrace(t, result.admission, adaptertest.Drain(t, result.stream, 5*time.Second))
		if err := session.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

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

	if err := session.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: gate.id, SessionID: "session", Answers: []protocol.InputAnswer{{QuestionID: "decision", SelectedOptionIDs: []string{"deny"}}}}}); err != nil {
		t.Fatalf("valid answer rejected after invalid ones: %v", err)
	}
	if _, raw := peer.written(); !strings.Contains(string(raw), `"deny"`) {
		t.Fatalf("final decision = %s", raw)
	}

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

func initFrameNaming(model string) string {
	return `{"type":"system","subtype":"init","session_id":"` + peerSession + `","tools":["Task","Bash"],"mcp_servers":[],"model":"` + model + `","permissionMode":"default","slash_commands":[],"apiKeySource":"none","claude_code_version":"2.1.263","capabilities":["interrupt_receipt_v1","msg_lifecycle_v1"],"uuid":"i1"}`
}

func awaitBuffered(t *testing.T, session base.Session) {
	t.Helper()
	impl := session.(*Session)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		impl.reduceMu.Lock()
		impl.mu.Lock()
		pending := impl.pending
		buffered := 0
		if pending != nil {
			buffered = len(pending.buffered)
		}
		impl.mu.Unlock()
		impl.reduceMu.Unlock()
		if buffered > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the pending run never buffered the observation")
}

func awaitModel(t *testing.T, session base.Session, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		state, err := session.State(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		last = state.CurrentModelID
		if last == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("state reported %q, want %q", last, want)
}

func TestStateAdoptsAnInitWithNoRunPending(t *testing.T) {
	_, session, peer := openWire(t)
	peer.send(initFrameNaming("model-idle"))
	awaitModel(t, session, "model-idle")
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStateDoesNotAdoptAPendingRunsInit(t *testing.T) {
	_, session, peer := openWire(t)

	outcome := submit(session)
	uuid := turnUUIDOf(t, peer.writtenUser())
	peer.send(initFrameNaming("model-published"))
	awaitBuffered(t, session)

	before, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before.CurrentModelID != "claude-test" {
		t.Fatalf("state reported %q while the run was still pending, want the configured model: a buffered init is not adopted until the run starts", before.CurrentModelID)
	}

	peer.send(streamEcho(uuid))
	admitted := awaitSubmit(t, outcome)
	peer.send(resultFrame(uuid, "success", false, "completed", "one", 0))
	adaptertest.Drain(t, admitted.stream, 5*time.Second)

	after, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.CurrentModelID != "model-published" {
		t.Fatalf("state reported %q after the run started; the replayed init should have been adopted", after.CurrentModelID)
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

func openWireWithJournal(t *testing.T, capacity int) (base.Session, *wirePeer) {
	t.Helper()
	peer := newWirePeer(t)
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return peer.client, nil }), Model: "claude-test", Clock: &testClock{}, IDs: &testIDs{}, JournalCapacity: capacity})
	if err != nil {
		t.Fatal(err)
	}
	return adaptertest.AssertInitialState(t, implementation, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}}), peer
}

func startTextRun(t *testing.T, session base.Session, peer *wirePeer, deltas int) (submitOutcome, string) {
	t.Helper()
	outcome := submit(session)
	uuid := turnUUIDOf(t, peer.writtenUser())
	peer.send(streamEcho(uuid))
	result := awaitSubmit(t, outcome)
	if result.err != nil {
		t.Fatal(result.err)
	}
	for index := range deltas {
		peer.send(textDelta(uuid, strconv.Itoa(index)))
	}
	return result, uuid
}

func readUntilClosed(t *testing.T, stream base.EventStream) ([]protocol.Envelope, error) {
	t.Helper()
	var envelopes []protocol.Envelope
	var streamErr error
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case result, ok := <-stream:
			if !ok {
				return envelopes, streamErr
			}
			if result.Error != nil {
				streamErr = result.Error
				continue
			}
			envelopes = append(envelopes, result.Envelope)
		case <-timer.C:
			t.Fatal("event stream did not close")
			return nil, nil
		}
	}
}

func TestResumeAfterOverflowDeliversTheRestOfTheRun(t *testing.T) {
	session, peer := openWireWithJournal(t, 256)
	result, uuid := startTextRun(t, session, peer, 2*streamCapacity)
	peer.awaitDrain()
	prefix, streamErr := readUntilClosed(t, result.stream)
	if !errors.Is(streamErr, base.ErrEventStreamOverflow) || len(prefix) != streamCapacity {
		t.Fatalf("stream closed with %v after %d envelopes", streamErr, len(prefix))
	}
	cursor := *prefix[len(prefix)-1].Sequence
	recovery, resumed, err := session.Resume(context.Background(), base.ResumeRequest{RunID: result.admission.RunID, AfterSequence: cursor})
	if err != nil || recovery.ReplayGap != nil || recovery.RequestedAfter != cursor || recovery.ReplayedFrom != cursor+1 {
		t.Fatalf("resume = %+v, %v", recovery, err)
	}
	peer.send(resultFrame(uuid, "success", false, "completed", "done", 0))
	events := append(prefix, adaptertest.Drain(t, resumed, 5*time.Second)...)
	deltas := 0
	for index, event := range events {
		if event.Sequence == nil || *event.Sequence != uint64(index+1) {
			t.Fatalf("event %d carries sequence %v", index, event.Sequence)
		}
		if event.Type == protocol.TypeContentDelta {
			deltas++
		}
	}
	if deltas != 2*streamCapacity || terminalOf(events).Type != protocol.TypeRunCompleted {
		t.Fatalf("resumed run delivered %d deltas and ended with %s", deltas, terminalOf(events).Type)
	}
	assertValidTrace(t, result.admission, events)
}

func TestResumeReplaysAnEndedRunFromADetachedJournal(t *testing.T) {
	session, peer := openWireWithJournal(t, 256)
	result, uuid := startTextRun(t, session, peer, 3)
	peer.send(resultFrame(uuid, "success", false, "completed", "done", 0))
	events := adaptertest.Drain(t, result.stream, 5*time.Second)
	if len(terminalOf(events).Extensions) == 0 {
		t.Fatal("the terminal carries no extensions to detach")
	}
	last := *terminalOf(events).Sequence
	want, err := json.Marshal(events[1:])
	if err != nil {
		t.Fatal(err)
	}
	vandalize := func(envelopes []protocol.Envelope) {
		for _, envelope := range envelopes {
			envelope.Payload[0] = 'X'
			*envelope.Sequence += 1000
			*envelope.TimestampMS += 1000
			for _, value := range envelope.Extensions {
				value[0] = 'X'
			}
		}
	}
	vandalize(events)
	for range 2 {
		recovery, replay, err := session.Resume(context.Background(), base.ResumeRequest{RunID: result.admission.RunID, AfterSequence: 1})
		if err != nil || recovery.ReplayedFrom != 2 || recovery.ReplayedThrough != last {
			t.Fatalf("resume = %+v, %v", recovery, err)
		}
		replayed := adaptertest.Drain(t, replay, 5*time.Second)
		got, err := json.Marshal(replayed)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("replay = %s, want %s", got, want)
		}
		vandalize(replayed)
	}
}

func TestResumeReportsAGapOnceTheJournalEvictsTheCursor(t *testing.T) {
	session, peer := openWireWithJournal(t, 4)
	result, uuid := startTextRun(t, session, peer, 6)
	peer.send(resultFrame(uuid, "success", false, "completed", "done", 0))
	latest := *terminalOf(adaptertest.Drain(t, result.stream, 5*time.Second)).Sequence
	oldest := latest - 3
	recovery, stream, err := session.Resume(context.Background(), base.ResumeRequest{RunID: result.admission.RunID, AfterSequence: oldest - 2})
	var gap *base.ReplayGap
	if !errors.As(err, &gap) || *gap != (base.ReplayGap{RequestedAfter: oldest - 2, OldestAvailable: oldest, LatestAvailable: latest}) || recovery.ReplayGap != gap || recovery.ReplayedFrom != 0 || recovery.State.Status != protocol.SessionIdle {
		t.Fatalf("resume = %+v, %v", recovery, err)
	}
	if replayed, _ := readUntilClosed(t, stream); len(replayed) != 0 {
		t.Fatalf("a gap replayed %d envelopes", len(replayed))
	}
	_, stream, err = session.Resume(context.Background(), base.ResumeRequest{RunID: result.admission.RunID, AfterSequence: oldest - 1})
	if err != nil {
		t.Fatal(err)
	}
	if replayed := adaptertest.Drain(t, stream, 5*time.Second); len(replayed) != 4 || *replayed[0].Sequence != oldest {
		t.Fatalf("boundary replay = %v", eventTypes(replayed))
	}
}

func TestResumeOfALiveRunRefusesAFutureCursorAndFollowsItToTheTerminal(t *testing.T) {
	session, peer := openWireWithJournal(t, 256)
	result, uuid := startTextRun(t, session, peer, 1)
	var latest uint64
	for latest == 0 {
		if event := adaptertest.Next(t, result.stream, 5*time.Second); event.Type == protocol.TypeContentDelta {
			latest = *event.Sequence
		}
	}
	if _, stream, err := session.Resume(context.Background(), base.ResumeRequest{RunID: result.admission.RunID, AfterSequence: latest + 1}); !errors.Is(err, base.ErrReplayCursorFuture) || stream != nil {
		t.Fatalf("future cursor on a live run = %v", err)
	}
	recovery, resumed, err := session.Resume(context.Background(), base.ResumeRequest{RunID: result.admission.RunID, AfterSequence: latest})
	if err != nil || recovery.ReplayedFrom != latest || recovery.ReplayedThrough != latest {
		t.Fatalf("resume at the head = %+v, %v", recovery, err)
	}
	peer.send(resultFrame(uuid, "success", false, "completed", "done", 0))
	followed := adaptertest.Drain(t, resumed, 5*time.Second)
	if len(followed) == 0 || *followed[0].Sequence != latest+1 || terminalOf(followed).Type != protocol.TypeRunCompleted {
		t.Fatalf("resumed live run delivered %v", eventTypes(followed))
	}
}

func TestResumeRefusesAFutureCursorAnUnknownRunAndAClosedSession(t *testing.T) {
	session, peer := openWireWithJournal(t, 256)
	result, uuid := startTextRun(t, session, peer, 1)
	peer.send(resultFrame(uuid, "success", false, "completed", "done", 0))
	latest := *terminalOf(adaptertest.Drain(t, result.stream, 5*time.Second)).Sequence
	if _, stream, err := session.Resume(context.Background(), base.ResumeRequest{RunID: result.admission.RunID, AfterSequence: latest + 1}); !errors.Is(err, base.ErrReplayCursorFuture) || stream != nil {
		t.Fatalf("future cursor = %v", err)
	}
	if _, stream, err := session.Resume(context.Background(), base.ResumeRequest{RunID: "run-unknown"}); !errors.Is(err, base.ErrRunNotFound) || stream != nil {
		t.Fatalf("unknown run = %v", err)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, stream, err := session.Resume(context.Background(), base.ResumeRequest{RunID: result.admission.RunID}); !errors.Is(err, base.ErrSessionClosed) || stream != nil {
		t.Fatalf("closed session = %v", err)
	}
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

	if _, raw := peer.written(); !strings.Contains(string(raw), `"error"`) {
		t.Fatalf("gate answer = %s", raw)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestResolveSerializesGateBeforeTerminal(t *testing.T) {

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

func TestToollessInitFrameServesAnEmptyCatalogArray(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		frame native.InitFrame
	}{
		{"no tools at all", native.InitFrame{}},
		{"an empty tool list", native.InitFrame{Tools: []string{}}},
		{"only unusable names", native.InitFrame{Tools: []string{"", ""}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			session := &Session{state: protocol.SessionState{SessionID: "session", Status: protocol.SessionIdle}}
			frame := testCase.frame
			session.projectCatalogLocked(&frame)
			request := protocol.ToolsListRequest{SessionID: "session", AllowDegradedFeatures: []string{protocol.FeatureToolsList}}
			catalog, err := session.Tools(context.Background(), request)
			if err != nil {
				t.Fatalf("tools: %v", err)
			}
			if len(catalog.Tools.Tools) != 0 {
				t.Fatalf("a toolless frame projected %d tools", len(catalog.Tools.Tools))
			}
			encoded, err := json.Marshal(catalog)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), `"tools":null`) {
				t.Fatalf("the served catalog encodes tools as null: %s", encoded)
			}

			implementation, err := New(Config{Executable: "/bin/claude", WorkingDirectory: "/tmp", Tools: UnrestrictedTools()})
			if err != nil {
				t.Fatal(err)
			}
			descriptor, err := implementation.Probe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			adaptertest.AssertToolCatalog(t, descriptor, protocol.SessionOpenRequest{}, request, catalog)
		})
	}
}

const mcpInitFrame = `{"type":"system","subtype":"init","session_id":"` + peerSession + `","tools":["Bash","mcp__files__read_file"],"mcp_servers":[{"name":"files","status":"connected"}],"model":"claude-test","permissionMode":"default","slash_commands":[],"apiKeySource":"none","claude_code_version":"2.1.263","capabilities":["interrupt_receipt_v1","msg_lifecycle_v1"],"uuid":"i1"}`

func TestServingACatalogRacesNoToolCall(t *testing.T) {
	_, session, peer := openWire(t)
	outcome := submit(session)
	uuid := turnUUIDOf(t, peer.writtenUser())

	peer.send(mcpInitFrame)
	peer.send(streamEcho(uuid))
	admitted := awaitSubmit(t, outcome)

	lister, ok := session.(base.ToolLister)
	if !ok {
		t.Fatal("the session serves no catalog")
	}
	const rounds = 64

	type drainResult struct {
		events []protocol.Envelope
		err    error
	}
	reduced := make(chan struct{}, 1)
	drained := make(chan drainResult, 1)
	go func() {
		var result drainResult
		for delivery := range admitted.stream {
			if delivery.Error != nil {
				result.err = delivery.Error
				break
			}
			result.events = append(result.events, delivery.Envelope)
			if delivery.Envelope.Type == protocol.TypeActionCallRequested {
				reduced <- struct{}{}
			}
		}

		close(reduced)
		drained <- result
	}()

	feeding := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-feeding:
				return
			default:
			}

			runtime.Gosched()

			if _, err := lister.Tools(context.Background(), protocol.ToolsListRequest{
				SessionID: "session", AllowDegradedFeatures: []string{protocol.FeatureToolsList},
			}); err != nil {
				t.Errorf("tools: %v", err)
				return
			}
		}
	}()
	for i := 0; i < rounds; i++ {
		id := fmt.Sprintf("toolu_%02d", i)
		peer.send(`{"type":"assistant","message":{"id":"msg_1","model":"claude-test","content":[{"type":"tool_use","id":"` + id + `","name":"mcp__files__read_file","input":{"path":"/tmp/x"}}],"stop_reason":null,"usage":{"input_tokens":1}},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"a` + id + `"}`)
		select {
		case _, ok := <-reduced:
			if !ok {
				close(feeding)
				wg.Wait()
				t.Fatalf("the event stream ended during round %d: %v", i, (<-drained).err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d was never reduced", i)
		}
		peer.send(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"` + id + `","type":"tool_result","content":"ok","is_error":false}]},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"u` + id + `"}`)
	}
	close(feeding)
	wg.Wait()
	peer.send(resultFrame(uuid, "success", false, "completed", "done", 0))
	var result drainResult
	select {
	case result = <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("the event stream never closed")
	}
	if result.err != nil {
		t.Fatalf("the event stream failed: %v", result.err)
	}
	if last := terminalOf(result.events); last.Type != protocol.TypeRunCompleted {
		t.Fatalf("terminal = %s", last.Type)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func catalogFrame(t *testing.T) *native.InitFrame {
	t.Helper()
	var frame native.InitFrame
	if err := json.Unmarshal([]byte(`{"tools":["Bash","mcp__files__read_file"],"mcp_servers":[{"name":"files","status":"connected"}]}`), &frame); err != nil {
		t.Fatal(err)
	}
	return &frame
}

func TestUnscopedCatalogPublishesNoSessionMCPServers(t *testing.T) {
	session := &Session{state: protocol.SessionState{SessionID: "session", Status: protocol.SessionIdle}}
	session.projectCatalogLocked(catalogFrame(t))
	request := protocol.ToolsListRequest{AllowDegradedFeatures: []string{protocol.FeatureToolsList}}
	catalog, err := session.Tools(context.Background(), request)
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	if catalog.Tools.SessionID != "" {
		t.Fatalf("an unscoped request was answered under session %q", catalog.Tools.SessionID)
	}
	for _, source := range catalog.Tools.Sources {
		if strings.HasPrefix(source.ID, mcpSourcePrefix) {
			t.Fatalf("the endpoint catalog publishes a session's MCP server: %+v", catalog.Tools.Sources)
		}
	}
	if len(catalog.Tools.Sources) != 1 || catalog.Tools.Sources[0].ID != nativeToolSource {
		t.Fatalf("endpoint catalog sources %+v", catalog.Tools.Sources)
	}
	if len(catalog.Tools.Tools) != 0 {
		t.Fatalf("the endpoint catalog publishes %d of a session's tools", len(catalog.Tools.Tools))
	}

	scoped, err := session.Tools(context.Background(), protocol.ToolsListRequest{SessionID: "session", AllowDegradedFeatures: []string{protocol.FeatureToolsList}})
	if err != nil {
		t.Fatalf("scoped tools: %v", err)
	}
	attributed := false
	for _, tool := range scoped.Tools.Tools {
		if tool.Name == "mcp__files__read_file" && tool.Source == mcpSourcePrefix+"files" {
			attributed = true
		}
	}
	if !attributed {
		t.Fatalf("the session catalog lost its MCP attribution: %+v", scoped.Tools.Tools)
	}
}

func TestCallCarriesTheCatalogSource(t *testing.T) {
	session := &Session{state: protocol.SessionState{SessionID: "session", Status: protocol.SessionIdle}}
	session.projectCatalogLocked(catalogFrame(t))
	for _, testCase := range []struct{ tool, source string }{

		{"Bash", nativeToolSource},

		{"mcp__files__read_file", ""},

		{"NotInTheCatalog", ""},
	} {
		t.Run(testCase.tool, func(t *testing.T) {
			if got := session.attributionFor(testCase.tool); got != testCase.source {
				t.Fatalf("catalog source for %q = %q, want %q", testCase.tool, got, testCase.source)
			}
		})
	}

	fresh := &Session{state: protocol.SessionState{SessionID: "session"}}
	if got := fresh.attributionFor("Bash"); got != "" {
		t.Fatalf("a session with no catalog attributed a call to %q", got)
	}

	catalog, err := session.Tools(context.Background(), protocol.ToolsListRequest{
		SessionID: "session", AllowDegradedFeatures: []string{protocol.FeatureToolsList},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range catalog.Tools.Tools {
		if tool.Name == "mcp__files__read_file" && tool.Source != mcpSourcePrefix+"files" {
			t.Fatalf("the catalog lost the attribution the call gave up: %+v", tool)
		}
	}
}

func spawnArgv(t *testing.T, config Config) []string {
	t.Helper()
	var captured []string
	config.Executable = "/bin/claude"
	config.ProcessFactory = ProcessFactoryFunc(func(_ context.Context, c rpc.ProcessConfig) (ProcessBridge, error) {
		captured = append([]string(nil), c.Args...)
		return nil, errors.New("not spawning in this test")
	})
	implementation, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = implementation.Open(context.Background(), base.OpenRequest{SessionID: "s", Participant: protocol.Participant{ID: "user"}})
	return captured
}

func TestToolPostureMustBeStated(t *testing.T) {
	_, err := New(Config{Executable: "/bin/claude"})
	if !errors.Is(err, ErrToolPostureUnstated) {
		t.Fatalf("constructing without a posture returned %v, want ErrToolPostureUnstated", err)
	}

	if _, err := New(Config{Executable: "/bin/claude", Tools: AllowTools()}); err == nil {
		t.Fatal("an allowlist naming no tool was accepted")
	}
	if _, err := New(Config{Executable: "/bin/claude", Tools: AllowTools("Read", "")}); err == nil {
		t.Fatal("an allowlist naming an empty tool was accepted")
	}
	if _, err := New(Config{Executable: "/bin/claude", Tools: AllowTools("Read", "(git *)")}); err == nil {
		t.Fatal("an allowlist rule naming no tool was accepted")
	}
}

func TestToolPostureIsNotRequiredOfACallerSuppliedFactory(t *testing.T) {
	peer := newWirePeer(t)
	if _, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return peer.client, nil })}); err != nil {
		t.Fatalf("a caller-supplied factory spawns its own process and was refused: %v", err)
	}
}

func TestToolPostureReachesTheSpawn(t *testing.T) {
	restricted := spawnArgv(t, Config{Tools: AllowTools("Read", "Grep", "Glob")})
	joined := strings.Join(restricted, " ")
	if !strings.Contains(joined, "--tools Read,Grep,Glob --allowedTools Read Grep Glob") {
		t.Fatalf("the allowlist did not reach the spawn as both the surface and the pre-approval: %v", restricted)
	}

	rules := spawnArgv(t, Config{Tools: AllowTools("Bash(git diff:*)", "Read", "Bash(git log:*)")})
	if !strings.Contains(strings.Join(rules, " "), "--tools Bash,Read --allowedTools Bash(git diff:*) Read Bash(git log:*)") {
		t.Fatalf("permission rules must reach --allowedTools whole and --tools as the tool each names, once: %v", rules)
	}

	unrestricted := spawnArgv(t, Config{Tools: UnrestrictedTools()})
	if slices.Contains(unrestricted, "--allowedTools") || slices.Contains(unrestricted, "--tools") {
		t.Fatalf("an unrestricted posture still restricted the spawn: %v", unrestricted)
	}

	trailing := spawnArgv(t, Config{Tools: AllowTools("Read"), Args: []string{"--append-system-prompt", "x"}})
	allowAt := slices.Index(trailing, "--allowedTools")
	argsAt := slices.Index(trailing, "--append-system-prompt")
	if allowAt < 0 || argsAt < 0 || allowAt > argsAt {
		t.Fatalf("Config.Args must stay last so a caller can still override: %v", trailing)
	}
}

func TestToolSelectionIsUnadvertisedBecauseItsProjectionFailsValidation(t *testing.T) {
	descriptor := testDescriptor(t)
	if support, ok := descriptor.Capabilities.Features[protocol.FeatureToolSelection]; ok && support.Level != protocol.SupportUnavailable {
		t.Fatalf("%s is advertised as %s", protocol.FeatureToolSelection, support.Level)
	}

	_, session, peer := openWire(t)
	uuid, outcome := admit(t, session, peer)
	peer.send(`{"type":"assistant","message":{"id":"msg_1","model":"claude-test","content":[{"type":"tool_use","id":"toolu_09","name":"Bash","input":{"command":"ls"}}],"stop_reason":null,"usage":{"input_tokens":7}},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"a9"}`)
	peer.send(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_09","type":"tool_result","content":"ok","is_error":false}]},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"u9"}`)
	peer.send(resultFrame(uuid, "success", false, "completed", "done", 0))
	events := adaptertest.Drain(t, outcome.stream, 5*time.Second)

	assertValidTrace(t, outcome.admission, events)

	excluded := protocol.MessageSubmitRequest{
		SessionID:  "session",
		Messages:   []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
		Delivery:   protocol.DeliveryAuto,
		ToolChoice: json.RawMessage(`{"disallowed":["Bash"]}`),
	}
	adaptertest.AssertProtocolInvalidWithSubmit(t, excluded, outcome.admission, descriptor, events, "unavailable_capability")

	advertising := testDescriptor(t)
	advertising.Capabilities.Features[protocol.FeatureToolSelection] = protocol.FeatureSupport{Level: protocol.SupportEmulated}
	adaptertest.AssertProtocolInvalidWithSubmit(t, excluded, outcome.admission, advertising, events, "unapplied_control")

	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNamelessToolUseFailsRunInsteadOfEmittingAnInvalidTrace(t *testing.T) {
	_, session, peer := openWire(t)
	uuid, outcome := admit(t, session, peer)
	peer.send(`{"type":"assistant","message":{"id":"m","model":"claude-test","content":[{"type":"tool_use","id":"toolu_nameless","input":{"command":"ls"}}],"stop_reason":null,"usage":{"input_tokens":7}},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"a-nameless","user_message_uuid":"` + uuid + `"}`)
	events := adaptertest.Drain(t, outcome.stream, 5*time.Second)
	assertValidTrace(t, outcome.admission, events)

	terminal := terminalOf(events)
	if terminal.Type != protocol.TypeRunFailed {
		t.Fatalf("terminal = %s, want run.failed", terminal.Type)
	}
	var payload protocol.RunFailedPayload
	if err := terminal.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != "claude_tool_lifecycle" || payload.Error.Message != "tool call without a name" {
		t.Fatalf("error = %+v", payload.Error)
	}
	for _, e := range events {
		if e.Type == protocol.TypeActionCallRequested || e.Type == protocol.TypeActionCallStarted {
			t.Fatalf("a nameless tool was announced as %s: %s", e.Type, e.Payload)
		}
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNegativeDurationIsNotReported(t *testing.T) {
	for _, elapsed := range []int64{-5, 0, 130} {
		_, session, peer := openWire(t)
		uuid, outcome := admit(t, session, peer)
		frame := strings.Replace(resultFrame(uuid, "success", false, "completed", "done", 0), `"duration_ms":130`, `"duration_ms":`+jsonInt(elapsed), 1)
		peer.send(frame)
		events := adaptertest.Drain(t, outcome.stream, 5*time.Second)
		assertValidTrace(t, outcome.admission, events)

		var payload protocol.RunCompletedPayload
		terminal := terminalOf(events)
		if err := terminal.DecodePayload(&payload); err != nil {
			t.Fatal(err)
		}
		want := elapsed
		if want < 0 {
			want = 0
		}
		if payload.DurationMS != want {
			t.Fatalf("duration_ms = %d for a frame reporting %d, want %d", payload.DurationMS, elapsed, want)
		}
		if err := session.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAFailedRunSweepsToolsStartedBeforeTheFailure(t *testing.T) {
	_, session, peer := openWire(t)
	uuid, outcome := admit(t, session, peer)
	peer.send(`{"type":"assistant","message":{"id":"m","model":"claude-test","content":[{"type":"tool_use","id":"toolu_ok","name":"Bash","input":{"command":"ls"}},{"type":"tool_use","id":"toolu_nameless","input":{"command":"ls"}}],"stop_reason":null,"usage":{"input_tokens":7}},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"a-mixed","user_message_uuid":"` + uuid + `"}`)
	events := adaptertest.Drain(t, outcome.stream, 5*time.Second)
	assertValidTrace(t, outcome.admission, events)

	var kinds []protocol.EnvelopeType
	for _, e := range events {
		kinds = append(kinds, e.Type)
	}
	if len(kinds) != 5 || kinds[3] != protocol.TypeActionCallCancelled || kinds[4] != protocol.TypeRunFailed {
		t.Fatalf("trace = %v, want the started call cancelled before the terminal", kinds)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestABlockAfterATerminalizingOneIsNotStarted(t *testing.T) {
	_, session, peer := openWire(t)
	uuid, outcome := admit(t, session, peer)
	peer.send(`{"type":"assistant","message":{"id":"m","model":"claude-test","content":[{"type":"tool_use","id":"toolu_nameless","input":{}},{"type":"tool_use","id":"toolu_ok","name":"Bash","input":{"command":"ls"}}],"stop_reason":null,"usage":{"input_tokens":7}},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"a-rev","user_message_uuid":"` + uuid + `"}`)
	assertValidTrace(t, outcome.admission, adaptertest.Drain(t, outcome.stream, 5*time.Second))

	impl := session.(*Session)
	impl.mu.Lock()
	leftover := len(impl.tools)
	impl.mu.Unlock()
	if leftover != 0 {
		t.Fatalf("tools left behind by a terminalized frame = %d, want 0", leftover)
	}

	uuid2, outcome2 := admit(t, session, peer)
	peer.send(`{"type":"assistant","message":{"id":"m2","model":"claude-test","content":[{"type":"tool_use","id":"toolu_ok","name":"Bash","input":{"command":"ls"}}],"stop_reason":null,"usage":{"input_tokens":7}},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"a-rev2","user_message_uuid":"` + uuid2 + `"}`)
	peer.send(resultFrame(uuid2, "success", false, "completed", "done", 0))
	second := adaptertest.Drain(t, outcome2.stream, 5*time.Second)
	assertValidTrace(t, outcome2.admission, second)
	var kinds []protocol.EnvelopeType
	for _, e := range second {
		kinds = append(kinds, e.Type)
	}
	if len(kinds) < 2 || kinds[1] != protocol.TypeActionCallRequested {
		t.Fatalf("the next run reusing that tool id produced %v", kinds)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func settledWithFrame(t *testing.T, frame string) (protocol.MessageSubmitResponse, []protocol.Envelope) {
	t.Helper()
	_, session, peer := openWire(t)
	outcome := submit(session)
	uuid := turnUUIDOf(t, peer.writtenUser())
	peer.send(initFrame)
	peer.send(streamEcho(uuid))
	result := awaitSubmit(t, outcome)
	if result.err != nil {
		t.Fatal(result.err)
	}
	peer.send(strings.ReplaceAll(frame, "REPLACE_UUID", uuid))
	return result.admission, adaptertest.Drain(t, result.stream, 5*time.Second)
}

func TestATerminalCarriesTheCostTheHarnessReported(t *testing.T) {
	admission, events := settledWithFrame(t, resultFrame("REPLACE_UUID", "success", false, "completed", "done", 0))
	assertValidTrace(t, admission, events)
	carried := terminalOf(events).Extensions[costExtension]
	if carried == nil {
		t.Fatalf("terminal carried no cost: %v", eventTypes(events))
	}
	var reported struct {
		TotalCostUSD float64 `json:"total_cost_usd"`
	}
	if err := json.Unmarshal(carried, &reported); err != nil {
		t.Fatal(err)
	}
	if reported.TotalCostUSD != 0.0001 {
		t.Fatalf("total_cost_usd = %v, want the 0.0001 the frame reported", reported.TotalCostUSD)
	}
}

func TestATerminalWithoutAReportedCostCarriesNoExtension(t *testing.T) {
	frame := strings.Replace(resultFrame("REPLACE_UUID", "success", false, "completed", "done", 0), `"total_cost_usd":0.0001,`, "", 1)
	if strings.Contains(frame, "total_cost_usd") {
		t.Fatal("the probe frame still names a cost")
	}
	admission, events := settledWithFrame(t, frame)
	assertValidTrace(t, admission, events)
	if len(terminalOf(events).Extensions) != 0 {
		t.Fatalf("extensions = %v, want none when the harness reported no cost", terminalOf(events).Extensions)
	}
}

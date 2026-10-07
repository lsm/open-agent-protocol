package opencode

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/httpapi"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func recordPage(records ...string) httpapi.MessagePage {
	var page httpapi.MessagePage
	for _, record := range records {
		page.Data = append(page.Data, json.RawMessage(record))
	}
	return page
}

func userRecord(id string) string {
	return `{"id":"` + id + `","type":"user","text":"ping","time":{"created":1}}`
}

func (f *fakeClient) loseStream(t *testing.T, pages ...httpapi.MessagePage) {
	t.Helper()
	f.mu.Lock()
	f.pages, f.readAsked = pages, nil
	f.resubscription = &fakeSubscription{events: f.events, done: make(chan struct{})}
	old := f.subscription
	f.mu.Unlock()
	old.fail(errors.New("connection reset"))
}

func (f *fakeClient) resubscribed(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		done := f.subscribes == 1
		f.mu.Unlock()
		if done {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the adapter did not subscribe again")
}

func streamedTexts(t *testing.T, events []protocol.Envelope) []string {
	t.Helper()
	var texts []string
	for _, event := range events {
		if event.Type != protocol.TypeContentDelta {
			continue
		}
		var delta protocol.ContentDeltaPayload
		if err := event.DecodePayload(&delta); err != nil {
			t.Fatal(err)
		}
		texts = append(texts, delta.Part.Text+delta.Part.Reasoning)
	}
	return texts
}

func finalTexts(t *testing.T, event protocol.Envelope) ([]string, protocol.RunCompletedPayload) {
	t.Helper()
	var completed protocol.RunCompletedPayload
	if event.Type != protocol.TypeRunCompleted || event.DecodePayload(&completed) != nil {
		t.Fatalf("ended with %s", event.Type)
	}
	parts, _ := completed.FinalResponse.Content.Parts()
	var texts []string
	for _, part := range parts {
		texts = append(texts, part.Text+part.Reasoning)
	}
	return texts, completed
}

func TestALostStreamSettlesARunTheRecordShowsFinished(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	input := response.MessageIDs[0]
	client.deliver(t, 1, native.MessageID(input))
	client.emit(t, 2, native.TypeTextDelta, native.PartDeltaData{SessionID: client.session, AssistantMessage: "msg_a1", Delta: "po"})
	client.loseStream(t, recordPage(
		`{"id":"msg_idle","type":"idle","outcome":"succeeded","time":{"created":4}}`,
		`{"id":"msg_a1","type":"assistant","content":[{"type":"text","text":"pong"}],"finish":"stop","cost":0.5,"tokens":{"input":3,"output":2,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":2,"completed":3}}`,
		userRecord(string(input)),
	))
	events := adaptertest.Drain(t, stream, time.Second)
	adaptertest.AssertRunTrace(t, response, CapabilityRevision, events)
	if got := streamedTexts(t, events); !slices.Equal(got, []string{"po", "ng"}) {
		t.Fatalf("streamed %q", got)
	}
	final, completed := finalTexts(t, events[len(events)-1])
	if !slices.Equal(final, []string{"pong"}) || completed.StopReason != "stop" || completed.Usage == nil || completed.Usage.TotalTokens != 5 {
		t.Fatalf("final %q, completed %+v", final, completed)
	}
	if !strings.Contains(string(events[len(events)-1].Extensions["io.github.anomalyco.opencode.cost"]), "0.5") {
		t.Fatalf("cost %s", events[len(events)-1].Extensions)
	}
	client.mu.Lock()
	asked := slices.Clone(client.readAsked)
	client.mu.Unlock()
	if len(asked) != 1 || !strings.HasSuffix(asked[0], "|desc") {
		t.Fatalf("read the record %v, want one newest-first page", asked)
	}
}

func TestALostStreamKeepsFollowingARunStillRunning(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	input := response.MessageIDs[0]
	client.deliver(t, 1, native.MessageID(input))
	client.mu.Lock()
	client.activeFor = 1
	client.mu.Unlock()
	client.loseStream(t, recordPage(
		`{"id":"msg_a1","type":"assistant","content":[{"type":"reasoning","text":"hmm","time":{"created":2,"completed":2}},{"type":"text","text":"first"},{"type":"reasoning","text":"deep","time":{"created":2,"completed":2}},{"type":"text","text":""}],"time":{"created":2}}`,
		userRecord(string(input)),
	))
	client.resubscribed(t)
	client.emit(t, 3, native.TypeReasoningEnded, native.ReasoningEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Text: "hmm"})
	client.emit(t, 4, native.TypeTextEnded, native.TextEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Ordinal: 1, Text: "second"})
	client.emit(t, 5, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop", Tokens: native.TokenAccounting{Input: 1, Output: 1}})
	client.succeed(t, 6)
	events := adaptertest.Drain(t, stream, time.Second)
	adaptertest.AssertRunTrace(t, response, CapabilityRevision, events)
	final, completed := finalTexts(t, events[len(events)-1])
	if !slices.Equal(final, []string{"hmm", "first", "deep", "second"}) || completed.Usage.TotalTokens != 2 {
		t.Fatalf("final %q usage %+v", final, completed.Usage)
	}
	if got := streamedTexts(t, events); !slices.Equal(got, []string{"hmm", "first", "deep", "second"}) {
		t.Fatalf("streamed %q", got)
	}
}

func TestALostStreamStartsAQueuedRunTheRecordShowsDelivered(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	first, firstStream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(first.MessageIDs[0]))
	second, secondStream := submitTest(t, session)
	client.mu.Lock()
	client.activeFor = 1
	client.mu.Unlock()
	client.loseStream(t, recordPage(
		userRecord(string(second.MessageIDs[0])),
		`{"id":"msg_idle1","type":"idle","outcome":"succeeded","time":{"created":5}}`,
		`{"id":"msg_a1","type":"assistant","content":[{"type":"text","text":"one"}],"finish":"stop","cost":0,"time":{"created":2,"completed":3}}`,
		userRecord(string(first.MessageIDs[0])),
	))
	client.resubscribed(t)
	client.deliver(t, 10, native.MessageID(second.MessageIDs[0]))
	client.emit(t, 11, native.TypeTextEnded, native.TextEndedData{SessionID: client.session, AssistantMessage: "msg_a2", Text: "two"})
	client.emit(t, 12, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a2", Finish: "stop"})
	client.succeed(t, 13)
	firstEvents := adaptertest.Drain(t, firstStream, time.Second)
	adaptertest.AssertRunTrace(t, first, CapabilityRevision, firstEvents)
	if final, _ := finalTexts(t, firstEvents[len(firstEvents)-1]); !slices.Equal(final, []string{"one"}) {
		t.Fatalf("first run final %q", final)
	}
	secondEvents := adaptertest.Drain(t, secondStream, time.Second)
	adaptertest.AssertRunTrace(t, second, CapabilityRevision, secondEvents)
	if final, _ := finalTexts(t, secondEvents[len(secondEvents)-1]); !slices.Equal(final, []string{"two"}) {
		t.Fatalf("second run final %q, want the text that followed the live repeat of its delivery", final)
	}
}

func TestALostStreamFailsARunTheRecordShowsFailed(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(response.MessageIDs[0]))
	client.loseStream(t, recordPage(
		`{"id":"msg_idle","type":"idle","outcome":"failed","time":{"created":9}}`,
		`{"id":"msg_a1","type":"assistant","content":[],"finish":"error","error":{"type":"provider","message":"bad request"},"cost":0,"time":{"created":7,"completed":8}}`,
		userRecord(string(response.MessageIDs[0])),
	))
	events := adaptertest.Drain(t, stream, time.Second)
	adaptertest.AssertRunTrace(t, response, CapabilityRevision, events)
	var failed protocol.RunFailedPayload
	last := events[len(events)-1]
	if last.Type != protocol.TypeRunFailed || last.DecodePayload(&failed) != nil || failed.Error.Code != "opencode_step_failed" || failed.Error.Message != "bad request" {
		t.Fatalf("events %v %+v", types(events), failed)
	}
}

func TestALostStreamFailsARunTheServerStoppedWithoutARecord(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(response.MessageIDs[0]))
	client.loseStream(t, recordPage(userRecord(string(response.MessageIDs[0]))))
	events := adaptertest.Drain(t, stream, time.Second)
	var failed protocol.RunFailedPayload
	last := events[len(events)-1]
	if last.Type != protocol.TypeRunFailed || last.DecodePayload(&failed) != nil || failed.Error.Code != "opencode_execution_interrupted" {
		t.Fatalf("events %v %+v", types(events), failed)
	}
}

func TestALostStreamFailsARunTheRecordDoesNotHold(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(response.MessageIDs[0]))
	client.mu.Lock()
	client.renew = true
	client.mu.Unlock()
	client.loseStream(t, recordPage(userRecord("msg_someone_else")))
	events := adaptertest.Drain(t, stream, time.Second)
	client.mu.Lock()
	attempts := client.attempts
	client.mu.Unlock()
	if attempts != 1 {
		t.Fatalf("subscribed %d times for a record that cannot hold the run", attempts)
	}
	var failed protocol.RunFailedPayload
	last := events[len(events)-1]
	if last.Type != protocol.TypeRunFailed || last.DecodePayload(&failed) != nil || failed.Error.Code != "opencode_stream_failed" {
		t.Fatalf("events %v %+v", types(events), failed)
	}
	if _, _, err := session.Submit(t.Context(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("again")}}}}); !errors.Is(err, base.ErrSessionClosed) {
		t.Fatalf("submit after an unreconciled loss: %v", err)
	}
}

func TestASecondLostStreamDoesNotApplyTheFirstReplayAgain(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	input := response.MessageIDs[0]
	client.deliver(t, 1, native.MessageID(input))
	running := recordPage(
		`{"id":"msg_a1","type":"assistant","content":[{"type":"text","text":"first"}],"finish":"tool-calls","cost":1,"tokens":{"input":2,"output":2},"time":{"created":2,"completed":3}}`,
		userRecord(string(input)),
	)
	client.mu.Lock()
	client.activeFor = 2
	client.mu.Unlock()
	client.loseStream(t, running)
	client.resubscribed(t)
	client.mu.Lock()
	client.subscribes = 0
	client.mu.Unlock()
	client.loseStream(t, running)
	client.resubscribed(t)
	client.emit(t, 4, native.TypeTextEnded, native.TextEndedData{SessionID: client.session, AssistantMessage: "msg_a2", Text: "second"})
	client.emit(t, 5, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a2", Finish: "stop", Tokens: native.TokenAccounting{Input: 1, Output: 1}})
	client.succeed(t, 6)
	events := adaptertest.Drain(t, stream, time.Second)
	adaptertest.AssertRunTrace(t, response, CapabilityRevision, events)
	final, completed := finalTexts(t, events[len(events)-1])
	if !slices.Equal(final, []string{"first", "second"}) || completed.Usage.TotalTokens != 6 {
		t.Fatalf("final %q usage %+v after two losses", final, completed.Usage)
	}
	if got := streamedTexts(t, events); !slices.Equal(got, []string{"first", "second"}) {
		t.Fatalf("streamed %q after two losses", got)
	}
}

func TestALostStreamRetriesTheServerUntilItAnswersAgain(t *testing.T) {
	for _, down := range []struct {
		name               string
		refusals, failures int
	}{{"refused subscribes", 2, 0}, {"failed record reads", 0, 2}} {
		t.Run(down.name, func(t *testing.T) {
			client := newFakeClient()
			session, _ := openTestWithin(t, client, 32, 5*time.Second)
			response, stream := submitTest(t, session)
			input := response.MessageIDs[0]
			client.deliver(t, 1, native.MessageID(input))
			client.mu.Lock()
			client.refusals, client.readFailures, client.renew = down.refusals, down.failures, true
			client.mu.Unlock()
			client.loseStream(t, recordPage(
				`{"id":"msg_idle","type":"idle","outcome":"succeeded","time":{"created":4}}`,
				`{"id":"msg_a1","type":"assistant","content":[{"type":"text","text":"pong"}],"finish":"stop","tokens":{"input":1,"output":1},"time":{"created":2,"completed":3}}`,
				userRecord(string(input)),
			))
			events := adaptertest.Drain(t, stream, 5*time.Second)
			adaptertest.AssertRunTrace(t, response, CapabilityRevision, events)
			if final, _ := finalTexts(t, events[len(events)-1]); !slices.Equal(final, []string{"pong"}) {
				t.Fatalf("final %q", final)
			}
			client.mu.Lock()
			attempts := client.attempts
			client.mu.Unlock()
			if attempts != 3 {
				t.Fatalf("subscribed %d times, want 3", attempts)
			}
		})
	}
}

func TestALostStreamGivesUpOnTheServerAtTheRequestTimeout(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(response.MessageIDs[0]))
	client.mu.Lock()
	client.refusals = 1000
	client.mu.Unlock()
	began := time.Now()
	client.subscription.fail(errors.New("connection reset"))
	events := adaptertest.Drain(t, stream, time.Second)
	waited := time.Since(began)
	var failed protocol.RunFailedPayload
	last := events[len(events)-1]
	if last.Type != protocol.TypeRunFailed || last.DecodePayload(&failed) != nil || failed.Error.Code != "opencode_stream_failed" {
		t.Fatalf("events %v %+v", types(events), failed)
	}
	client.mu.Lock()
	attempts := client.attempts
	client.mu.Unlock()
	if waited < 200*time.Millisecond || attempts < 2 {
		t.Fatalf("gave up after %v and %d attempts, want retries until the 250ms request timeout", waited, attempts)
	}
}

func toolCalls(t *testing.T, events []protocol.Envelope) []string {
	t.Helper()
	var calls []string
	for _, event := range events {
		switch event.Type {
		case protocol.TypeActionCallRequested, protocol.TypeActionCallStarted, protocol.TypeActionCallCompleted, protocol.TypeActionCallFailed:
		default:
			continue
		}
		var call protocol.ActionCallPayload
		if err := event.DecodePayload(&call); err != nil {
			t.Fatal(err)
		}
		entry := string(event.Type) + " " + call.Name + " " + string(call.ArgumentsJSON) + string(call.Result)
		if call.Error != nil {
			entry += call.Error.Message
		}
		calls = append(calls, entry)
	}
	return calls
}

func assertToolTrace(t *testing.T, response protocol.MessageSubmitResponse, events []protocol.Envelope) {
	t.Helper()
	implementation, err := New(Config{Endpoint: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := implementation.Probe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.AssertProtocolValidWithDescriptor(t, response, descriptor, events)
}

func TestALostStreamReplaysAToolCallTheRecordShowsRunAndFinished(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 64)
	response, stream := submitTest(t, session)
	input := response.MessageIDs[0]
	client.deliver(t, 1, native.MessageID(input))
	client.loseStream(t, recordPage(
		`{"id":"msg_idle","type":"idle","outcome":"succeeded","time":{"created":6}}`,
		`{"id":"msg_a2","type":"assistant","content":[{"type":"text","text":"done"}],"finish":"stop","tokens":{"input":1,"output":1},"time":{"created":4,"completed":5}}`,
		`{"id":"msg_a1","type":"assistant","content":[{"type":"tool","id":"call_1","name":"shell","executed":false,"state":{"status":"completed","input":{"command":"echo hi"},"content":[{"type":"text","text":"hi\n"}],"metadata":{"exit":0}},"time":{"created":2,"completed":3}},{"type":"tool","id":"call_2","name":"read","executed":false,"state":{"status":"pending","input":{}}}],"finish":"tool-calls","tokens":{"input":1,"output":1},"time":{"created":2,"completed":3}}`,
		userRecord(string(input)),
	))
	events := adaptertest.Drain(t, stream, time.Second)
	assertToolTrace(t, response, events)
	want := []string{
		`action.call.requested shell {"command":"echo hi"}`,
		`action.call.started shell `,
		`action.call.completed shell [{"type":"text","text":"hi\n"}]`,
	}
	if got := toolCalls(t, events); !slices.Equal(got, want) {
		t.Fatalf("tool calls\n%q\nwant\n%q", got, want)
	}
	if final, _ := finalTexts(t, events[len(events)-1]); !slices.Equal(final, []string{"done"}) {
		t.Fatalf("final %q", final)
	}
}

func TestALostStreamTakesToolCallsFromTheRecordAndDropsTheirLiveRepeats(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 64)
	response, stream := submitTest(t, session)
	input := response.MessageIDs[0]
	client.deliver(t, 1, native.MessageID(input))
	client.toolCalled(t, 2, 3, "call_1", "shell", map[string]any{"command": "rm -rf /"})
	client.mu.Lock()
	client.activeFor = 1
	client.mu.Unlock()
	client.loseStream(t, recordPage(
		`{"id":"msg_a1","type":"assistant","content":[{"type":"tool","id":"call_1","name":"shell","executed":false,"state":{"status":"error","input":{"command":"rm -rf /"},"error":{"type":"aborted","message":"The user declined this tool call"}}},{"type":"tool","id":"call_2","name":"read","executed":false,"state":{"status":"running","input":{"path":"a"}}}],"time":{"created":2}}`,
		userRecord(string(input)),
	))
	client.resubscribed(t)
	client.emit(t, 5, native.TypeToolFailed, native.ToolFailedData{SessionID: client.session, AssistantMessage: "msg_a1", ID: "call_1", Error: native.SessionError{Type: "aborted", Message: "The user declined this tool call"}})
	client.emit(t, 6, native.TypeToolProgress, native.ToolProgressData{SessionID: client.session, AssistantMessage: "msg_a1", ID: "call_1", Metadata: json.RawMessage(`{"late":true}`)})
	client.emit(t, 8, native.TypeToolProgress, native.ToolProgressData{SessionID: client.session, AssistantMessage: "msg_a1", ID: "call_2", Metadata: json.RawMessage(`{"line":1}`)})
	client.emit(t, 9, native.TypeToolSuccess, native.ToolSuccessData{SessionID: client.session, AssistantMessage: "msg_a1", ID: "call_2", Content: []native.ToolContent{{Type: "text", Text: "a"}}})
	client.emit(t, 10, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Finish: "tool-calls", Tokens: native.TokenAccounting{Input: 1, Output: 1}})
	client.succeed(t, 11)
	events := adaptertest.Drain(t, stream, time.Second)
	assertToolTrace(t, response, events)
	want := []string{
		`action.call.requested shell {"command":"rm -rf /"}`,
		`action.call.started shell `,
		`action.call.failed shell The user declined this tool call`,
		`action.call.requested read {"path":"a"}`,
		`action.call.started read `,
		`action.call.completed read [{"type":"text","text":"a"}]`,
	}
	if got := toolCalls(t, events); !slices.Equal(got, want) {
		t.Fatalf("tool calls\n%q\nwant\n%q", got, want)
	}
	if events[len(events)-1].Type != protocol.TypeRunCompleted {
		t.Fatalf("ended with %s", events[len(events)-1].Type)
	}
	progress := 0
	for _, event := range events {
		if event.Type == protocol.TypeActionCallProgress {
			progress++
		}
	}
	if progress != 1 {
		t.Fatalf("%d progress events, want only the running call's", progress)
	}
}

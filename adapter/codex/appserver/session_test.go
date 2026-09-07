package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/adapter/codex/appserver/internal/native"
	"github.com/lsm/open-agent-protocol/adapter/codex/appserver/internal/rpc"
	"github.com/lsm/open-agent-protocol/protocol"
)

type fakeClock struct {
	mu sync.Mutex
	n  int64
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.n++
	return time.UnixMilli(clock.n)
}

type fakeIDs struct {
	mu sync.Mutex
	n  int
}

func (ids *fakeIDs) NewID(kind string) string {
	ids.mu.Lock()
	defer ids.mu.Unlock()
	ids.n++
	return fmt.Sprintf("%s-%02d", kind, ids.n)
}

type fakeClient struct {
	mu            sync.Mutex
	threadID      string
	turnID        string
	calls         []string
	interruptGate chan struct{}
	requests      chan *rpc.IncomingRequest
	notifications chan rpc.NotificationMessage
	done          chan struct{}
	closeOnce     sync.Once
	err           error
}

func newFakeClient() *fakeClient {
	return &fakeClient{threadID: "native-thread", turnID: "native-turn", requests: make(chan *rpc.IncomingRequest, 8), notifications: make(chan rpc.NotificationMessage, 32), done: make(chan struct{})}
}

func (client *fakeClient) Call(ctx context.Context, method string, params, result any) error {
	client.mu.Lock()
	client.calls = append(client.calls, method)
	client.mu.Unlock()
	switch method {
	case native.MethodThreadStart:
		response := result.(*native.ThreadStartResponse)
		response.Thread.ID = client.threadID
	case native.MethodThreadResume:
		response := result.(*native.ThreadResumeResponse)
		response.Thread.ID = client.threadID
	case native.MethodTurnStart:
		response := result.(*native.TurnStartResponse)
		response.Turn.ID = client.turnID
		response.Turn.Status = native.TurnInProgress
	case native.MethodTurnInterrupt:
		if client.interruptGate != nil {
			select {
			case <-client.interruptGate:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	default:
		return fmt.Errorf("unexpected method %s", method)
	}
	return nil
}

func (client *fakeClient) Notify(context.Context, string, any) error     { return nil }
func (client *fakeClient) Requests() <-chan *rpc.IncomingRequest         { return client.requests }
func (client *fakeClient) Notifications() <-chan rpc.NotificationMessage { return client.notifications }
func (client *fakeClient) Done() <-chan struct{}                         { return client.done }
func (client *fakeClient) Err() error                                    { return client.err }
func (client *fakeClient) Close() error {
	client.closeOnce.Do(func() { close(client.done) })
	return nil
}

func (client *fakeClient) send(t *testing.T, method string, payload any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	client.notifications <- rpc.NotificationMessage{Method: method, Params: data}
}

func (client *fakeClient) request(t *testing.T, id int64, method string, payload any) (*rpc.IncomingRequest, <-chan rpc.Message) {
	t.Helper()
	serverToClientReader, serverToClientWriter := io.Pipe()
	clientToServerReader, clientToServerWriter := io.Pipe()
	rpcClient := rpc.NewClient(serverToClientReader, clientToServerWriter, rpc.ClientOptions{QueueCapacity: 8})
	t.Cleanup(func() {
		_ = rpcClient.Close()
		_ = serverToClientWriter.Close()
		_ = clientToServerReader.Close()
	})
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = rpc.NewEncoder(serverToClientWriter).Encode(rpc.Request(rpc.IntegerID(id), method, data))
	}()
	request := <-rpcClient.Requests()
	response := make(chan rpc.Message, 1)
	go func() {
		decoder := rpc.NewDecoder(clientToServerReader, 0)
		message, _ := decoder.Decode()
		response <- message
	}()
	client.requests <- request
	return request, response
}

func openFake(t *testing.T) (*fakeClient, adapter.Session, adapter.Descriptor) {
	t.Helper()
	client := newFakeClient()
	implementation, err := New(Config{
		Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil }),
		Clock:   &fakeClock{}, IDs: &fakeIDs{}, Model: "glm-test", JournalCapacity: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := implementation.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	session, err := implementation.Open(context.Background(), adapter.OpenRequest{SessionID: "session-1", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	return client, session, descriptor
}

func submitFake(t *testing.T, session adapter.Session) (protocol.MessageSubmitResponse, adapter.EventStream) {
	t.Helper()
	response, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{
		SessionID: "session-1", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return response, stream
}

func nextEvent(t *testing.T, stream adapter.EventStream) protocol.Envelope {
	t.Helper()
	return adaptertest.Next(t, stream, time.Second)
}

func drainClosed(t *testing.T, stream adapter.EventStream) []protocol.Envelope {
	t.Helper()
	return adaptertest.Drain(t, stream, time.Second)
}

func TestCompletedLifecycle(t *testing.T) {
	client, session, descriptor := openFake(t)
	admission, stream := submitFake(t, session)
	if admission.Status != protocol.RunQueued || admission.EffectiveDelivery != protocol.DeliveryStart {
		t.Fatalf("admission: %+v", admission)
	}
	client.send(t, native.MethodTurnStarted, native.TurnStartedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInProgress}})
	client.send(t, native.MethodAgentDelta, native.AgentMessageDeltaNotification{ThreadID: client.threadID, TurnID: client.turnID, ItemID: "message-native", Delta: "fixture-ok"})
	client.send(t, native.MethodTurnCompleted, native.TurnCompletedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnCompleted}})
	events := drainClosed(t, stream)
	adaptertest.AssertTypes(t, events, protocol.TypeRunStarted, protocol.TypeContentDelta, protocol.TypeRunCompleted)
	adaptertest.AssertRunTrace(t, admission, descriptor.CapabilityRevision, events)
	for index, event := range events {
		if event.RunID != admission.RunID || event.Sequence == nil || *event.Sequence != uint64(index+1) || event.CapabilityRevision != descriptor.CapabilityRevision {
			t.Fatalf("event %d: %+v", index, event)
		}
	}
	var completed protocol.RunCompletedPayload
	if err := events[2].DecodePayload(&completed); err != nil {
		t.Fatal(err)
	}
	if text, ok := completed.FinalResponse.Content.Text(); !ok || text != "fixture-ok" {
		t.Fatalf("final response: %+v", completed.FinalResponse)
	}
	state, err := session.State(context.Background())
	if err != nil || state.Status != protocol.SessionIdle || state.ActiveRunID != "" || state.TranscriptCursor != "3" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestOpenResumesExplicitNativeThread(t *testing.T) {
	client := newFakeClient()
	implementation, err := New(Config{
		Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil }),
		Clock:   &fakeClock{}, IDs: &fakeIDs{}, Model: "glm-test", JournalCapacity: 32,
		ResumeThreadID: client.threadID,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := implementation.Open(context.Background(), adapter.OpenRequest{SessionID: "session-1", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	client.mu.Lock()
	calls := append([]string(nil), client.calls...)
	client.mu.Unlock()
	if len(calls) != 1 || calls[0] != native.MethodThreadResume {
		t.Fatalf("native calls: %v", calls)
	}
}

func TestTurnStartResponseIsAdmissionOnly(t *testing.T) {
	_, session, _ := openFake(t)
	_, stream := submitFake(t, session)
	select {
	case event := <-stream:
		t.Fatalf("event before native turn/started: %+v", event)
	default:
	}
	if _, _, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session-1", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("again")}}}); !errors.Is(err, adapter.ErrRunActive) {
		t.Fatalf("second submit: %v", err)
	}
}

func TestFailedAndInterruptedTerminals(t *testing.T) {
	for _, test := range []struct {
		name   string
		status native.TurnStatus
		want   protocol.EnvelopeType
	}{
		{"failed", native.TurnFailed, protocol.TypeRunFailed},
		{"interrupted", native.TurnInterrupted, protocol.TypeRunCancelled},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, session, _ := openFake(t)
			_, stream := submitFake(t, session)
			client.send(t, native.MethodTurnStarted, native.TurnStartedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInProgress}})
			client.send(t, native.MethodTurnCompleted, native.TurnCompletedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: test.status, Error: &native.TurnError{Message: "failed"}}})
			events := drainClosed(t, stream)
			if got := events[len(events)-1].Type; got != test.want {
				t.Fatalf("got %s want %s", got, test.want)
			}
		})
	}
}

func TestCancellationAcknowledgementWaitsForTerminal(t *testing.T) {
	client, session, _ := openFake(t)
	admission, stream := submitFake(t, session)
	client.send(t, native.MethodTurnStarted, native.TurnStartedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInProgress}})
	for result := range stream {
		if result.Envelope.Type == protocol.TypeRunStarted {
			break
		}
	}
	ack, err := session.Cancel(context.Background(), admission.RunID)
	if err != nil || !ack.Accepted || ack.Status != protocol.RunCancelling {
		t.Fatalf("ack=%+v err=%v", ack, err)
	}
	select {
	case result := <-stream:
		if result.Envelope.Type != protocol.TypeRunStatusUpdated {
			t.Fatalf("unexpected post-ack event: %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("missing cancelling status")
	}
	client.send(t, native.MethodTurnCompleted, native.TurnCompletedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInterrupted}})
	events := drainClosed(t, stream)
	if len(events) != 1 || events[0].Type != protocol.TypeRunCancelled {
		t.Fatalf("settlement: %+v", events)
	}
	duplicate, err := session.Cancel(context.Background(), admission.RunID)
	if err != nil || duplicate.Status != protocol.RunCancelled {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}
}

func TestNaturalTerminalWinsCancellationRace(t *testing.T) {
	for _, test := range []struct {
		name   string
		status native.TurnStatus
		want   protocol.EnvelopeType
	}{
		{name: "completed", status: native.TurnCompleted, want: protocol.TypeRunCompleted},
		{name: "failed", status: native.TurnFailed, want: protocol.TypeRunFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, session, _ := openFake(t)
			admission, stream := submitFake(t, session)
			client.send(t, native.MethodTurnStarted, native.TurnStartedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInProgress}})
			_ = nextEvent(t, stream)
			client.interruptGate = make(chan struct{})
			cancelled := make(chan error, 1)
			go func() {
				_, err := session.Cancel(context.Background(), admission.RunID)
				cancelled <- err
			}()
			deadline := time.Now().Add(time.Second)
			for {
				client.mu.Lock()
				calls := append([]string(nil), client.calls...)
				client.mu.Unlock()
				if calls[len(calls)-1] == native.MethodTurnInterrupt {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("interrupt call did not start")
				}
				time.Sleep(time.Millisecond)
			}
			turn := native.Turn{ID: client.turnID, Status: test.status}
			if test.status == native.TurnFailed {
				turn.Error = &native.TurnError{Message: "race failure"}
			}
			client.send(t, native.MethodTurnCompleted, native.TurnCompletedNotification{ThreadID: client.threadID, Turn: turn})
			events := drainClosed(t, stream)
			close(client.interruptGate)
			if err := <-cancelled; !errors.Is(err, adapter.ErrRunAlreadyTerminal) {
				t.Fatalf("cancel result: %v", err)
			}
			if len(events) != 1 || events[0].Type != test.want {
				t.Fatalf("race events: %+v", events)
			}
		})
	}
}

func TestProcessExitFailsActiveRun(t *testing.T) {
	client, session, _ := openFake(t)
	_, stream := submitFake(t, session)
	client.err = errors.New("boom")
	_ = client.Close()
	events := drainClosed(t, stream)
	if len(events) != 1 || events[0].Type != protocol.TypeRunFailed {
		t.Fatalf("events: %+v", events)
	}
}

func TestActionLifecycleAndReplay(t *testing.T) {
	client, session, _ := openFake(t)
	admission, stream := submitFake(t, session)
	client.send(t, native.MethodTurnStarted, native.TurnStartedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInProgress}})
	item := native.Item{Type: "commandExecution", ID: "native-item", Command: "true", Status: "inProgress", Arguments: json.RawMessage(`{"command":"true"}`)}
	client.send(t, native.MethodItemStarted, native.ItemNotification{ThreadID: client.threadID, TurnID: client.turnID, Item: item})
	item.Status = "completed"
	output := "ok"
	item.Output = &output
	item.Arguments = nil
	client.send(t, native.MethodItemCompleted, native.ItemNotification{ThreadID: client.threadID, TurnID: client.turnID, Item: item})
	client.send(t, native.MethodTurnCompleted, native.TurnCompletedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnCompleted}})
	events := drainClosed(t, stream)
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeActionCallRequested, protocol.TypeActionCallStarted, protocol.TypeActionCallCompleted, protocol.TypeRunCompleted}
	for index := range want {
		if events[index].Type != want[index] {
			t.Fatalf("event %d: got %s want %s", index, events[index].Type, want[index])
		}
	}
	recovery, replay, err := session.Resume(context.Background(), adapter.ResumeRequest{RunID: admission.RunID, AfterSequence: 2})
	if err != nil || recovery.ReplayedFrom != 3 || recovery.ReplayedThrough != 5 {
		t.Fatalf("recovery=%+v err=%v", recovery, err)
	}
	if got := drainClosed(t, replay); len(got) != 3 {
		t.Fatalf("replayed %d", len(got))
	}
}

func TestCommandApprovalRoundTrip(t *testing.T) {
	client, session, _ := openFake(t)
	admission, stream := submitFake(t, session)
	client.send(t, native.MethodTurnStarted, native.TurnStartedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInProgress}})
	item := native.Item{Type: "commandExecution", ID: "native-item", Command: "true", Status: "inProgress"}
	client.send(t, native.MethodItemStarted, native.ItemNotification{ThreadID: client.threadID, TurnID: client.turnID, Item: item})
	for range 3 {
		_ = nextEvent(t, stream)
	}
	reason := "needs approval"
	_, nativeResponse := client.request(t, 7, native.MethodCommandApproval, native.CommandApprovalParams{
		ThreadID: client.threadID, TurnID: client.turnID, ItemID: item.ID,
		Kind: "command", StartedAtMS: 10, Reason: &reason,
	})
	requested := nextEvent(t, stream)
	if requested.Type != protocol.TypeActionPermissionRequested {
		t.Fatalf("event: %+v", requested)
	}
	var payload protocol.PermissionRequestedPayload
	if err := requested.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	resolution := adapter.InteractionResolution{
		RunID: admission.RunID, RespondedBy: "user",
		Permission: &protocol.PermissionResolveRequest{
			InteractionID: payload.InteractionID, RequestedBy: "agent", RespondedBy: "user",
			SessionID: "session-1", RunID: admission.RunID, ChoiceID: "accept", Granted: true,
		},
	}
	if err := session.Resolve(context.Background(), resolution); err != nil {
		t.Fatal(err)
	}
	response := <-nativeResponse
	if response.Kind != rpc.MessageResponse {
		t.Fatalf("native response: %+v", response)
	}
	var approval native.ApprovalResponse
	if err := json.Unmarshal(response.Result, &approval); err != nil || approval.Decision != native.ApprovalAccept {
		t.Fatalf("approval=%+v err=%v", approval, err)
	}
	resolved := nextEvent(t, stream)
	if resolved.Type != protocol.TypeActionPermissionResolved {
		t.Fatalf("event: %+v", resolved)
	}
	if err := session.Resolve(context.Background(), resolution); !errors.Is(err, adapter.ErrInteractionResolved) {
		t.Fatalf("duplicate resolution: %v", err)
	}
	client.send(t, native.MethodTurnCompleted, native.TurnCompletedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInterrupted}})
	_ = drainClosed(t, stream)
}

func TestApprovalDecisionsAndAvailability(t *testing.T) {
	for _, test := range []struct {
		name     string
		choiceID string
		granted  bool
		outcome  protocol.InteractionOutcome
	}{
		{name: "session", choiceID: "acceptForSession", granted: true, outcome: protocol.InteractionResolved},
		{name: "cancel", choiceID: "cancel", granted: false, outcome: protocol.InteractionCancelled},
		{name: "decline", choiceID: "decline", granted: false, outcome: protocol.InteractionRejected},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, session, _ := openFake(t)
			admission, stream := submitFake(t, session)
			client.send(t, native.MethodTurnStarted, native.TurnStartedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInProgress}})
			item := native.Item{Type: "commandExecution", ID: "native-item", Status: "inProgress"}
			client.send(t, native.MethodItemStarted, native.ItemNotification{ThreadID: client.threadID, TurnID: client.turnID, Item: item})
			for range 3 {
				_ = nextEvent(t, stream)
			}
			available, err := json.Marshal([]native.ApprovalDecision{native.ApprovalAcceptForSession, native.ApprovalDecline, native.ApprovalCancel})
			if err != nil {
				t.Fatal(err)
			}
			_, nativeResponse := client.request(t, 11, native.MethodCommandApproval, native.CommandApprovalParams{ThreadID: client.threadID, TurnID: client.turnID, ItemID: item.ID, Kind: "command", StartedAtMS: 10, AvailableDecisions: available})
			requested := nextEvent(t, stream)
			var payload protocol.PermissionRequestedPayload
			if err := requested.DecodePayload(&payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.Choices) != 3 {
				t.Fatalf("choices: %+v", payload.Choices)
			}
			resolution := adapter.InteractionResolution{RunID: admission.RunID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: payload.InteractionID, RequestedBy: "agent", RespondedBy: "user", SessionID: "session-1", RunID: admission.RunID, ChoiceID: test.choiceID, Granted: test.granted}}
			if err := session.Resolve(context.Background(), resolution); err != nil {
				t.Fatal(err)
			}
			response := <-nativeResponse
			var result native.ApprovalResponse
			if err := json.Unmarshal(response.Result, &result); err != nil || string(result.Decision) != test.choiceID {
				t.Fatalf("response=%+v err=%v", result, err)
			}
			resolved := nextEvent(t, stream)
			var resolvedPayload protocol.PermissionResolvedPayload
			if err := resolved.DecodePayload(&resolvedPayload); err != nil || resolvedPayload.Outcome != test.outcome {
				t.Fatalf("payload=%+v err=%v", resolvedPayload, err)
			}
			client.send(t, native.MethodTurnCompleted, native.TurnCompletedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInterrupted}})
			_ = drainClosed(t, stream)
		})
	}
}

func TestUserInputRoundTrip(t *testing.T) {
	client, session, _ := openFake(t)
	admission, stream := submitFake(t, session)
	client.send(t, native.MethodTurnStarted, native.TurnStartedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInProgress}})
	_ = nextEvent(t, stream)
	options := []native.UserInputOption{{Label: "Fast", Description: "Lower latency"}, {Label: "Safe", Description: "More checks"}}
	_, nativeResponse := client.request(t, 8, native.MethodUserInput, native.UserInputRequestParams{
		ThreadID: client.threadID, TurnID: client.turnID, ItemID: "tool-item", IsBlocking: true,
		Questions: []native.UserInputQuestion{{ID: "mode", Header: "Mode", Question: "Choose mode", Options: &options}, {ID: "note", Header: "Note", Question: "Add note", Options: nil}},
	})
	requested := nextEvent(t, stream)
	status := nextEvent(t, stream)
	if requested.Type != protocol.TypeUserInputRequested || status.Type != protocol.TypeRunStatusUpdated {
		t.Fatalf("events: %+v %+v", requested, status)
	}
	var payload protocol.UserInputRequestedPayload
	if err := requested.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Questions) != 2 || payload.Questions[0].Kind != protocol.InputSingleChoice || payload.Questions[1].Kind != protocol.InputText {
		t.Fatalf("questions: %+v", payload.Questions)
	}
	resolution := adapter.InteractionResolution{
		RunID: admission.RunID, RespondedBy: "user",
		Input: &protocol.UserInputResolveRequest{
			InteractionID: payload.InteractionID, RequestedBy: "agent", RespondedBy: "user",
			SessionID: "session-1", RunID: admission.RunID,
			Answers: []protocol.InputAnswer{{QuestionID: "mode", SelectedOptionIDs: []string{"option-2"}}, {QuestionID: "note", Text: "ship it"}},
		},
	}
	if err := session.Resolve(context.Background(), resolution); err != nil {
		t.Fatal(err)
	}
	response := <-nativeResponse
	var nativeResult native.UserInputResponse
	if err := json.Unmarshal(response.Result, &nativeResult); err != nil {
		t.Fatal(err)
	}
	if got := nativeResult.Answers["mode"].Answers; len(got) != 1 || got[0] != "Safe" {
		t.Fatalf("selected answer: %+v", nativeResult.Answers)
	}
	if got := nativeResult.Answers["note"].Answers; len(got) != 1 || got[0] != "ship it" {
		t.Fatalf("text answer: %+v", nativeResult.Answers)
	}
	if nextEvent(t, stream).Type != protocol.TypeUserInputResolved || nextEvent(t, stream).Type != protocol.TypeRunStatusUpdated {
		t.Fatal("missing input resolution lifecycle")
	}
	state, err := session.State(context.Background())
	if err != nil || state.Status != protocol.SessionRunning {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	client.send(t, native.MethodTurnCompleted, native.TurnCompletedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnCompleted}})
	_ = drainClosed(t, stream)
}

func TestUserInputOtherAndRequiredAnswers(t *testing.T) {
	client, session, _ := openFake(t)
	admission, stream := submitFake(t, session)
	client.send(t, native.MethodTurnStarted, native.TurnStartedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInProgress}})
	_ = nextEvent(t, stream)
	options := []native.UserInputOption{{Label: "Known", Description: "known value"}}
	_, nativeResponse := client.request(t, 12, native.MethodUserInput, native.UserInputRequestParams{
		ThreadID: client.threadID, TurnID: client.turnID, ItemID: "tool-item", IsBlocking: true,
		Questions: []native.UserInputQuestion{{ID: "choice", Header: "Choice", Question: "Choose", IsOther: true, Options: &options}, {ID: "note", Header: "Note", Question: "Add note"}},
	})
	requested := nextEvent(t, stream)
	_ = nextEvent(t, stream)
	var payload protocol.UserInputRequestedPayload
	if err := requested.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if got := payload.Questions[0].Options; len(got) != 2 || got[1].ID != "other" {
		t.Fatalf("options: %+v", got)
	}
	partial := protocol.UserInputResolveRequest{InteractionID: payload.InteractionID, RequestedBy: "agent", RespondedBy: "user", SessionID: "session-1", RunID: admission.RunID, Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"other"}, Text: "custom"}}}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: admission.RunID, RespondedBy: "user", Input: &partial}); !errors.Is(err, adapter.ErrInvalidResolution) {
		t.Fatalf("partial resolution: %v", err)
	}
	partial.Answers = append(partial.Answers, protocol.InputAnswer{QuestionID: "note", Text: "complete"})
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: admission.RunID, RespondedBy: "user", Input: &partial}); err != nil {
		t.Fatal(err)
	}
	response := <-nativeResponse
	var result native.UserInputResponse
	if err := json.Unmarshal(response.Result, &result); err != nil || len(result.Answers["choice"].Answers) != 1 || result.Answers["choice"].Answers[0] != "custom" {
		t.Fatalf("response=%+v err=%v", result, err)
	}
	_ = nextEvent(t, stream)
	_ = nextEvent(t, stream)
	client.send(t, native.MethodTurnCompleted, native.TurnCompletedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnCompleted}})
	_ = drainClosed(t, stream)
}

func TestPendingInteractionClosesBeforeRunTerminal(t *testing.T) {
	client, session, _ := openFake(t)
	_, stream := submitFake(t, session)
	client.send(t, native.MethodTurnStarted, native.TurnStartedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInProgress}})
	_ = nextEvent(t, stream)
	_, nativeResponse := client.request(t, 9, native.MethodUserInput, native.UserInputRequestParams{
		ThreadID: client.threadID, TurnID: client.turnID, ItemID: "tool-item", IsBlocking: true,
		Questions: []native.UserInputQuestion{{ID: "note", Header: "Note", Question: "Add note"}},
	})
	_ = nextEvent(t, stream)
	_ = nextEvent(t, stream)
	client.send(t, native.MethodTurnCompleted, native.TurnCompletedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInterrupted}})
	events := drainClosed(t, stream)
	if len(events) != 2 || events[0].Type != protocol.TypeUserInputResolved || events[1].Type != protocol.TypeRunCancelled {
		t.Fatalf("terminal ordering: %+v", events)
	}
	if response := <-nativeResponse; response.Kind != rpc.MessageError {
		t.Fatalf("native response: %+v", response)
	}
}

func TestInteractionResolutionValidation(t *testing.T) {
	client, session, _ := openFake(t)
	admission, stream := submitFake(t, session)
	client.send(t, native.MethodTurnStarted, native.TurnStartedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInProgress}})
	_ = nextEvent(t, stream)
	options := []native.UserInputOption{{Label: "One", Description: "first"}}
	_, _ = client.request(t, 10, native.MethodUserInput, native.UserInputRequestParams{
		ThreadID: client.threadID, TurnID: client.turnID, ItemID: "tool-item", IsBlocking: true,
		Questions: []native.UserInputQuestion{{ID: "choice", Header: "Choice", Question: "Choose", Options: &options}},
	})
	requested := nextEvent(t, stream)
	_ = nextEvent(t, stream)
	var payload protocol.UserInputRequestedPayload
	if err := requested.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	base := protocol.UserInputResolveRequest{InteractionID: payload.InteractionID, RequestedBy: "agent", RespondedBy: "user", SessionID: "session-1", RunID: admission.RunID, Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"missing"}}}}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: admission.RunID, RespondedBy: "intruder", Input: &base}); !errors.Is(err, adapter.ErrWrongResponder) {
		t.Fatalf("wrong responder: %v", err)
	}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: admission.RunID, RespondedBy: "user", Input: &base}); !errors.Is(err, adapter.ErrInvalidResolution) {
		t.Fatalf("invalid answer: %v", err)
	}
	client.send(t, native.MethodTurnCompleted, native.TurnCompletedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInterrupted}})
	_ = drainClosed(t, stream)
}

func TestDescriptorClaimsTestedInteractions(t *testing.T) {
	_, _, descriptor := openFake(t)
	adaptertest.AssertDescriptor(t, descriptor)
	if descriptor.Capabilities.Features["action.permissions"].Level != protocol.SupportNative || descriptor.Capabilities.Features["user_input"].Level != protocol.SupportDegraded || !descriptor.InteractiveGates {
		t.Fatalf("descriptor: %+v", descriptor.Capabilities.Features)
	}
	if descriptor.MaxActiveRunsPerSession != 1 || descriptor.Journal.Replay != protocol.SupportDegraded {
		t.Fatalf("descriptor: %+v", descriptor)
	}
}

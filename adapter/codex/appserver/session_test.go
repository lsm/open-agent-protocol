package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/adapter"
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

func drainClosed(t *testing.T, stream adapter.EventStream) []protocol.Envelope {
	t.Helper()
	var events []protocol.Envelope
	for result := range stream {
		if result.Error != nil {
			t.Fatal(result.Error)
		}
		events = append(events, result.Envelope)
	}
	return events
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
	if len(events) != 3 || events[0].Type != protocol.TypeRunStarted || events[1].Type != protocol.TypeContentDelta || events[2].Type != protocol.TypeRunCompleted {
		t.Fatalf("events: %+v", events)
	}
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

func TestDescriptorDoesNotOverclaimInteractions(t *testing.T) {
	_, _, descriptor := openFake(t)
	if descriptor.Capabilities.Features["action.permissions"].Level != protocol.SupportUnavailable || descriptor.Capabilities.Features["user_input"].Level != protocol.SupportUnavailable {
		t.Fatalf("descriptor: %+v", descriptor.Capabilities.Features)
	}
	if descriptor.MaxActiveRunsPerSession != 1 || descriptor.Journal.Replay != protocol.SupportDegraded {
		t.Fatalf("descriptor: %+v", descriptor)
	}
}

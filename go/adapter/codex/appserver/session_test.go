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

	"github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/codex/appserver/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/codex/appserver/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
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
	inbound       chan rpc.InboundMessage
	done          chan struct{}
	closeOnce     sync.Once
	err           error
	turnStartErr  error

	turnStart native.TurnStartParams
}

func newFakeClient() *fakeClient {
	return &fakeClient{threadID: "native-thread", turnID: "native-turn", inbound: make(chan rpc.InboundMessage, 40), done: make(chan struct{})}
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
		if client.turnStartErr != nil {
			return client.turnStartErr
		}
		if sent, ok := params.(native.TurnStartParams); ok {
			client.mu.Lock()
			client.turnStart = sent
			client.mu.Unlock()
		}
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

func (client *fakeClient) Notify(context.Context, string, any) error { return nil }
func (client *fakeClient) Inbound() <-chan rpc.InboundMessage        { return client.inbound }
func (client *fakeClient) Done() <-chan struct{}                     { return client.done }
func (client *fakeClient) Err() error                                { return client.err }
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
	client.notify(method, data)
}

func (client *fakeClient) notify(method string, params json.RawMessage) {
	client.inbound <- rpc.InboundMessage{Notification: &rpc.NotificationMessage{Method: method, Params: params}}
}

func (client *fakeClient) enqueue(request *rpc.IncomingRequest) {
	client.inbound <- rpc.InboundMessage{Request: request}
}

func (client *fakeClient) request(t *testing.T, id int64, method string, payload any) (*rpc.IncomingRequest, <-chan rpc.Message) {
	t.Helper()
	request, response := client.newRequest(t, id, method, payload)
	client.enqueue(request)
	return request, response
}

func (client *fakeClient) newRequest(t *testing.T, id int64, method string, payload any) (*rpc.IncomingRequest, <-chan rpc.Message) {
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
	inbound := <-rpcClient.Inbound()
	if inbound.Request == nil {
		t.Fatalf("decoded frame is not a reverse request: %+v", inbound)
	}
	response := make(chan rpc.Message, 1)
	go func() {
		decoder := rpc.NewDecoder(clientToServerReader, 0)
		message, _ := decoder.Decode()
		response <- message
	}()
	return inbound.Request, response
}

func TestOpenRejectsEmptyParticipant(t *testing.T) {
	started := 0
	implementation, err := New(Config{
		Factory: ClientFactoryFunc(func(context.Context) (Client, error) {
			started++
			return newFakeClient(), nil
		}),
		Clock: &fakeClock{}, IDs: &fakeIDs{}, Model: "glm-test", JournalCapacity: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := implementation.Open(context.Background(), adapter.OpenRequest{SessionID: "session"}); !errors.Is(err, adapter.ErrInvalidParticipant) {
		t.Fatalf("got %v, want ErrInvalidParticipant", err)
	}
	if started != 0 {
		t.Fatalf("factory started for an invalid request: %d", started)
	}
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

func TestAmbiguousTurnStartRetiresSession(t *testing.T) {
	client, session, _ := openFake(t)
	ambiguous := errors.New("start Codex turn: context deadline exceeded")
	client.mu.Lock()
	client.turnStartErr = ambiguous
	client.err = ambiguous
	client.mu.Unlock()
	if _, _, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{
		SessionID: "session-1", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}},
	}); err == nil {
		t.Fatal("submit unexpectedly succeeded")
	}
	if _, err := session.State(context.Background()); !errors.Is(err, adapter.ErrSessionClosed) {
		t.Fatalf("ambiguous turn start left the session usable: %v", err)
	}
}

func TestDefiniteTurnStartRejectionLeavesSessionUsable(t *testing.T) {
	client, session, _ := openFake(t)
	client.mu.Lock()
	client.turnStartErr = errors.New("turn/start rejected")
	client.mu.Unlock()
	if _, _, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{
		SessionID: "session-1", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}},
	}); err == nil {
		t.Fatal("submit unexpectedly succeeded")
	}
	if _, err := session.State(context.Background()); err != nil {
		t.Fatalf("definite rejection retired the session: %v", err)
	}
}

func TestCompletedLifecycle(t *testing.T) {
	client, session, descriptor := openFake(t)
	admission, stream := submitFake(t, session)
	if admission.Status != protocol.RunRunning || admission.EffectiveDelivery != protocol.DeliveryStart {
		t.Fatalf("admission: %+v", admission)
	}
	client.send(t, native.MethodTurnStarted, native.TurnStartedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInProgress}})
	client.send(t, native.MethodAgentDelta, native.AgentMessageDeltaNotification{ThreadID: client.threadID, TurnID: client.turnID, ItemID: "message-native", Delta: "fixture-ok"})
	client.send(t, native.MethodTurnCompleted, native.TurnCompletedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnCompleted}})
	events := drainClosed(t, stream)
	adaptertest.AssertTypes(t, events, protocol.TypeRunStarted, protocol.TypeContentDelta, protocol.TypeRunCompleted)
	adaptertest.AssertProtocolValidWithDescriptor(t, admission, descriptor, events)
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

func TestPublishedAndReplayedEnvelopesAreDetached(t *testing.T) {
	client, session, _ := openFake(t)
	admission, stream := submitFake(t, session)
	client.send(t, native.MethodTurnStarted, native.TurnStartedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInProgress}})
	client.send(t, native.MethodAgentDelta, native.AgentMessageDeltaNotification{ThreadID: client.threadID, TurnID: client.turnID, ItemID: "message-native", Delta: "hi"})
	client.send(t, native.MethodTurnCompleted, native.TurnCompletedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnCompleted}})
	prefix := drainClosed(t, stream)
	if len(prefix) < 3 {
		t.Fatalf("prefix=%v", prefix)
	}
	target := prefix[len(prefix)-2]
	originalPayload := append([]byte(nil), target.Payload...)
	originalSequence := *target.Sequence
	copy(target.Payload, []byte(`{"tampered":true}`))
	*target.Sequence = originalSequence + 100
	_, replay, err := session.Resume(context.Background(), adapter.ResumeRequest{RunID: admission.RunID, AfterSequence: originalSequence - 1})
	if err != nil {
		t.Fatal(err)
	}
	matched := false
	for _, envelope := range drainClosed(t, replay) {
		if envelope.Sequence != nil && *envelope.Sequence == originalSequence {
			matched = true
			if string(envelope.Payload) != string(originalPayload) {
				t.Fatalf("replayed payload aliased: got %s want %s", envelope.Payload, originalPayload)
			}
		}
	}
	if !matched {
		t.Fatalf("sequence %d not replayed", originalSequence)
	}
}

func TestLiveStreamOverflowReportsErrorAndResumes(t *testing.T) {
	client, session, _ := openFake(t)
	admission, stream := submitFake(t, session)
	client.send(t, native.MethodTurnStarted, native.TurnStartedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInProgress}})
	for index := range 40 {
		client.send(t, native.MethodAgentDelta, native.AgentMessageDeltaNotification{ThreadID: client.threadID, TurnID: client.turnID, ItemID: "message-native", Delta: fmt.Sprintf("%02d", index)})
	}
	client.send(t, native.MethodTurnCompleted, native.TurnCompletedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnCompleted}})
	deadline := time.Now().Add(time.Second)
	for {
		state, err := session.State(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if state.ActiveRunID == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native events did not settle")
		}
		time.Sleep(time.Millisecond)
	}

	var prefix []protocol.Envelope
	var streamErr error
	for result := range stream {
		if result.Error != nil {
			streamErr = result.Error
			continue
		}
		prefix = append(prefix, result.Envelope)
	}
	if !errors.Is(streamErr, adapter.ErrEventStreamOverflow) {
		t.Fatalf("stream error=%v", streamErr)
	}
	if len(prefix) == 0 || prefix[len(prefix)-1].Type == protocol.TypeRunCompleted {
		t.Fatalf("overflowed prefix unexpectedly contains terminal: %d events", len(prefix))
	}
	for index, event := range prefix {
		if event.Sequence == nil || *event.Sequence != uint64(index+1) {
			t.Fatalf("event %d sequence=%v", index, event.Sequence)
		}
	}
	last := *prefix[len(prefix)-1].Sequence
	recovery, replay, err := session.Resume(context.Background(), adapter.ResumeRequest{RunID: admission.RunID, AfterSequence: last})
	if err != nil {
		t.Fatal(err)
	}
	replayed := drainClosed(t, replay)
	if recovery.ReplayedFrom != last+1 || len(replayed) == 0 || replayed[len(replayed)-1].Type != protocol.TypeRunCompleted {
		t.Fatalf("recovery=%+v replayed=%d", recovery, len(replayed))
	}
	for index, event := range replayed {
		if event.Sequence == nil || *event.Sequence != last+uint64(index)+1 {
			t.Fatalf("replayed event %d sequence=%v", index, event.Sequence)
		}
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

func TestTurnStartWithoutTurnIDRetiresSession(t *testing.T) {
	client, session, _ := openFake(t)
	client.turnID = ""
	request := func() protocol.MessageSubmitRequest {
		return protocol.MessageSubmitRequest{SessionID: "session-1", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}}
	}
	if _, _, err := session.Submit(context.Background(), request()); !errors.Is(err, ErrNativeProtocol) {
		t.Fatalf("got %v, want ErrNativeProtocol", err)
	}
	if _, _, err := session.Submit(context.Background(), request()); !errors.Is(err, adapter.ErrSessionClosed) {
		t.Fatalf("retry: got %v, want ErrSessionClosed", err)
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
			InteractionID: payload.InteractionID, RequestedBy: endpointID, RespondedBy: "user",
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
			resolution := adapter.InteractionResolution{RunID: admission.RunID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: payload.InteractionID, RequestedBy: endpointID, RespondedBy: "user", SessionID: "session-1", RunID: admission.RunID, ChoiceID: test.choiceID, Granted: test.granted}}
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
			InteractionID: payload.InteractionID, RequestedBy: endpointID, RespondedBy: "user",
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

func TestDispatchReducesRequestAfterPrecedingNotification(t *testing.T) {
	client, sess, descriptor := openFake(t)
	admission, stream := submitFake(t, sess)
	options := []native.UserInputOption{{Label: "Fast", Description: "Lower latency"}, {Label: "Safe", Description: "More checks"}}
	request, nativeResponse := client.newRequest(t, 9, native.MethodUserInput, native.UserInputRequestParams{
		ThreadID: client.threadID, TurnID: client.turnID, ItemID: "tool-item", IsBlocking: true,
		Questions: []native.UserInputQuestion{{ID: "mode", Header: "Mode", Question: "Choose mode", Options: &options}},
	})

	reducer := sess.(*session)
	reducer.opMu.Lock()
	client.send(t, "thread/status/changed", map[string]any{"threadId": client.threadID})
	client.send(t, native.MethodTurnStarted, native.TurnStartedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInProgress}})
	client.enqueue(request)
	reducer.opMu.Unlock()

	events := []protocol.Envelope{nextEvent(t, stream), nextEvent(t, stream), nextEvent(t, stream)}
	adaptertest.AssertTypes(t, events, protocol.TypeRunStarted, protocol.TypeUserInputRequested, protocol.TypeRunStatusUpdated)
	var payload protocol.UserInputRequestedPayload
	if err := events[1].DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	err := sess.Resolve(context.Background(), adapter.InteractionResolution{
		RunID: admission.RunID, RespondedBy: "user",
		Input: &protocol.UserInputResolveRequest{
			InteractionID: payload.InteractionID, RequestedBy: endpointID, RespondedBy: "user",
			SessionID: "session-1", RunID: admission.RunID,
			Answers: []protocol.InputAnswer{{QuestionID: "mode", SelectedOptionIDs: []string{"option-2"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response := <-nativeResponse; response.Kind != rpc.MessageResponse {
		t.Fatalf("native response: %+v", response)
	}
	client.send(t, native.MethodTurnCompleted, native.TurnCompletedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnCompleted}})
	events = append(events, drainClosed(t, stream)...)
	adaptertest.AssertProtocolValidWithDescriptor(t, admission, descriptor, events)
}

func TestUserInputEmptyOptionsSurfacesAsText(t *testing.T) {

	client, session, _ := openFake(t)
	_, stream := submitFake(t, session)
	client.send(t, native.MethodTurnStarted, native.TurnStartedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInProgress}})
	_ = nextEvent(t, stream)
	empty := []native.UserInputOption{}
	_, _ = client.request(t, 13, native.MethodUserInput, native.UserInputRequestParams{
		ThreadID: client.threadID, TurnID: client.turnID, ItemID: "tool-item", IsBlocking: true,
		Questions: []native.UserInputQuestion{{ID: "choice", Header: "Choice", Question: "Choose", Options: &empty}},
	})
	requested := nextEvent(t, stream)
	_ = nextEvent(t, stream)
	var payload protocol.UserInputRequestedPayload
	if err := requested.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Questions[0].Kind != protocol.InputText || len(payload.Questions[0].Options) != 0 {
		t.Fatalf("question = %+v", payload.Questions[0])
	}
	client.send(t, native.MethodTurnCompleted, native.TurnCompletedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnCompleted}})
	_ = drainClosed(t, stream)
}

func TestUserInputIsOtherDoesNotAdvertiseUnsatisfiableOption(t *testing.T) {

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
	if got := payload.Questions[0].Options; len(got) != 1 || got[0].ID != "option-1" {
		t.Fatalf("options: %+v", got)
	}
	for name, answer := range map[string]protocol.InputAnswer{
		"option with text": {QuestionID: "choice", SelectedOptionIDs: []string{"option-1"}, Text: "custom"},
		"other with text":  {QuestionID: "choice", SelectedOptionIDs: []string{"other"}, Text: "custom"},
		"bare other":       {QuestionID: "choice", SelectedOptionIDs: []string{"other"}},
	} {
		resolution := protocol.UserInputResolveRequest{InteractionID: payload.InteractionID, RequestedBy: endpointID, RespondedBy: "user", SessionID: "session-1", RunID: admission.RunID, Answers: []protocol.InputAnswer{answer, {QuestionID: "note", Text: "n"}}}
		if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: admission.RunID, RespondedBy: "user", Input: &resolution}); !errors.Is(err, adapter.ErrInvalidResolution) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}

	missing := protocol.UserInputResolveRequest{InteractionID: payload.InteractionID, RequestedBy: endpointID, RespondedBy: "user", SessionID: "session-1", RunID: admission.RunID, Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"option-1"}}}}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: admission.RunID, RespondedBy: "user", Input: &missing}); !errors.Is(err, adapter.ErrInvalidResolution) {
		t.Fatalf("missing required answer: %v", err)
	}

	valid := protocol.UserInputResolveRequest{InteractionID: payload.InteractionID, RequestedBy: endpointID, RespondedBy: "user", SessionID: "session-1", RunID: admission.RunID, Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"option-1"}}, {QuestionID: "note", Text: "complete"}}}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: admission.RunID, RespondedBy: "user", Input: &valid}); err != nil {
		t.Fatal(err)
	}
	response := <-nativeResponse
	var result native.UserInputResponse
	if err := json.Unmarshal(response.Result, &result); err != nil || len(result.Answers["choice"].Answers) != 1 || result.Answers["choice"].Answers[0] != "Known" {
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
	base := protocol.UserInputResolveRequest{InteractionID: payload.InteractionID, RequestedBy: endpointID, RespondedBy: "user", SessionID: "session-1", RunID: admission.RunID, Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"missing"}}}}
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

func TestSubmitAppliesModelPerTurn(t *testing.T) {
	client, session, descriptor := openFake(t)
	request := protocol.MessageSubmitRequest{
		SessionID: "session-1", Delivery: protocol.DeliveryAuto,
		ModelID:  protocol.ControlValue("glm-per-turn"),
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}},
	}
	admission, stream, err := session.Submit(context.Background(), request)
	if err != nil {
		t.Fatalf("admitted model refused: %v", err)
	}
	client.mu.Lock()
	sent := client.turnStart
	client.mu.Unlock()
	if sent.Model != "glm-per-turn" {
		t.Fatalf("turn/start model = %q, want the requested model", sent.Model)
	}
	if admission.ModelID != "glm-per-turn" {
		t.Fatalf("admission model = %q, want the requested model", admission.ModelID)
	}
	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentModelID != "glm-test" {
		t.Fatalf("per_run selection moved the session default to %q", state.CurrentModelID)
	}
	client.send(t, native.MethodTurnStarted, native.TurnStartedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnInProgress}})
	client.send(t, native.MethodTurnCompleted, native.TurnCompletedNotification{ThreadID: client.threadID, Turn: native.Turn{ID: client.turnID, Status: native.TurnCompleted}})
	events := drainClosed(t, stream)
	var started protocol.RunStartedPayload
	if err := events[0].DecodePayload(&started); err != nil {
		t.Fatal(err)
	}
	if started.ModelID != "glm-per-turn" {
		t.Fatalf("run.started model = %q, want the admitted model", started.ModelID)
	}
	adaptertest.AssertProtocolValidWithSubmit(t, request, admission, descriptor, events)
	if state, err := session.State(context.Background()); err != nil || state.CurrentModelID != "glm-test" {
		t.Fatalf("the session default moved at the terminal: %+v err=%v", state, err)
	}
}

func TestSubmitRefusesUnadvertisedControls(t *testing.T) {
	for name, testCase := range map[string]struct {
		request protocol.MessageSubmitRequest
		feature string
	}{
		"instructions":  {request: protocol.MessageSubmitRequest{Instructions: protocol.ControlValue("be terse")}, feature: protocol.FeatureInstructions},
		"tool choice":   {request: protocol.MessageSubmitRequest{ToolChoice: json.RawMessage(`{"mode":"none"}`)}, feature: protocol.FeatureToolSelection},
		"output schema": {request: protocol.MessageSubmitRequest{OutputSchema: json.RawMessage(`{"type":"object"}`)}, feature: protocol.FeatureStructuredOutput},
	} {
		client, session, _ := openFake(t)
		request := testCase.request
		request.SessionID, request.Delivery = "session-1", protocol.DeliveryAuto
		request.Messages = []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}
		_, _, err := session.Submit(context.Background(), request)
		var refusal *adapter.UnsupportedControlError
		if !errors.As(err, &refusal) || refusal.Feature != testCase.feature || refusal.Reason != adapter.ControlUnadvertised {
			t.Fatalf("%s: got %v, want an unadvertised refusal naming %s", name, err, testCase.feature)
		}
		client.mu.Lock()
		calls := append([]string(nil), client.calls...)
		client.mu.Unlock()
		for _, method := range calls {
			if method == native.MethodTurnStart {
				t.Fatalf("%s: a refused control reached the native codec", name)
			}
		}
		state, err := session.State(context.Background())
		if err != nil || state.ActiveRunID != "" || state.Status != protocol.SessionIdle {
			t.Fatalf("%s: a refused control allocated identity: %+v err=%v", name, state, err)
		}
	}
}

func TestSubmitRefusesEmptyModelID(t *testing.T) {
	_, session, _ := openFake(t)
	_, _, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{
		SessionID: "session-1", Delivery: protocol.DeliveryAuto,
		ModelID:  protocol.ControlValue(""),
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}},
	})
	var missing *adapter.ModelNotFoundError
	if !errors.As(err, &missing) || missing.ModelID != "" {
		t.Fatalf("got %v, want model_not_found naming the empty id", err)
	}
}

func TestControlRefusalOutranksOrdinaryValidation(t *testing.T) {
	message := []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}
	for name, request := range map[string]protocol.MessageSubmitRequest{
		"no messages":      {SessionID: "session-1", Delivery: protocol.DeliveryAuto, Instructions: protocol.ControlValue("be terse")},
		"wrong session":    {SessionID: "other", Delivery: protocol.DeliveryAuto, Instructions: protocol.ControlValue("be terse"), Messages: message},
		"unsupported mode": {SessionID: "session-1", Delivery: protocol.DeliveryQueue, Instructions: protocol.ControlValue("be terse"), Messages: message},
		"degraded consent": {SessionID: "session-1", Delivery: protocol.DeliveryAuto, Instructions: protocol.ControlValue("be terse"), AllowDegradedFeatures: []string{protocol.FeatureInstructions}, Messages: message},
	} {
		_, session, _ := openFake(t)
		_, _, err := session.Submit(context.Background(), request)
		var refusal *adapter.UnsupportedControlError
		if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureInstructions || refusal.Reason != adapter.ControlUnadvertised {
			t.Fatalf("%s: got %v, want the unadvertised control named ahead of the ordinary refusal", name, err)
		}
	}

	_, session, _ := openFake(t)
	_, _, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{
		SessionID: "session-1", Delivery: protocol.DeliveryAuto, ModelID: protocol.ControlValue("glm-per-turn"),
	})
	if !errors.Is(err, adapter.ErrInvalidSubmission) {
		t.Fatalf("got %v, want the ordinary refusal when no control is at fault", err)
	}
}

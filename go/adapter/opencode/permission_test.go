package opencode

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type gatedRun struct {
	client     *fakeClient
	session    base.Session
	descriptor base.Descriptor
	response   protocol.MessageSubmitResponse
	stream     base.EventStream
	seen       []protocol.Envelope
	request    protocol.PermissionRequestedPayload
}

func openGatedRun(t *testing.T) *gatedRun {
	t.Helper()
	client := newFakeClient()
	adapter, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil }), Clock: &fakeClock{}, IDs: &fakeIDs{}})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	session, err := adapter.Open(context.Background(), base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	response, stream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(response.MessageIDs[0]))
	client.toolCalled(t, 2, 3, "call_1", "shell", map[string]any{"command": "echo hi"})
	client.emit(t, 0, native.TypePermissionAsked, native.PermissionAskedData{ID: "per_1", SessionID: client.session, Action: "shell", Resources: []string{"echo hi"}, Save: []string{"echo *"}, Source: &native.PermissionSource{Type: "tool", MessageID: "msg_a1", ID: "call_1"}})
	run := &gatedRun{client: client, session: session, descriptor: descriptor, response: response, stream: stream}
	for {
		event := adaptertest.Next(t, stream, time.Second)
		run.seen = append(run.seen, event)
		if event.Type == protocol.TypeActionPermissionRequested {
			if err := event.DecodePayload(&run.request); err != nil {
				t.Fatal(err)
			}
			return run
		}
	}
}

func (g *gatedRun) answer(choice, reason string) error {
	return g.session.Resolve(context.Background(), base.InteractionResolution{RunID: g.response.RunID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{
		InteractionID: g.request.InteractionID, RequestedBy: g.request.RequestedBy, RespondedBy: "user", SessionID: "session", RunID: g.response.RunID,
		ChoiceID: choice, Granted: choice != "reject", Reason: reason,
	}})
}

func (g *gatedRun) finish(t *testing.T) []protocol.Envelope {
	t.Helper()
	events := append(g.seen, adaptertest.Drain(t, g.stream, time.Second)...)
	adaptertest.AssertProtocolValidWithDescriptor(t, g.response, g.descriptor, events)
	return events
}

func resolvedOf(t *testing.T, events []protocol.Envelope) []protocol.PermissionResolvedPayload {
	t.Helper()
	var resolved []protocol.PermissionResolvedPayload
	for _, event := range events {
		if event.Type != protocol.TypeActionPermissionResolved {
			continue
		}
		var payload protocol.PermissionResolvedPayload
		if err := event.DecodePayload(&payload); err != nil {
			t.Fatal(err)
		}
		resolved = append(resolved, payload)
	}
	return resolved
}

func TestAPermissionTheServerAsksBecomesARequestForItsToolCallAndAnAllowedOneLetsTheRunFinish(t *testing.T) {
	g := openGatedRun(t)
	if g.request.ToolCallID == "" || g.request.Title != "shell: echo hi" || len(g.request.Choices) != 3 || g.request.RespondedBy != "user" || string(g.request.ArgumentsJSON) != `{"command":"echo hi"}` {
		t.Fatalf("requested %+v", g.request)
	}
	if err := g.answer("once", ""); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(g.client.permissionReplies, []string{"per_1|once|"}) {
		t.Fatalf("replied %v", g.client.permissionReplies)
	}
	if err := g.answer("once", ""); !errors.Is(err, base.ErrInteractionResolved) {
		t.Fatalf("a second answer: %v", err)
	}
	g.client.emit(t, 0, native.TypePermissionReplied, native.PermissionRepliedData{SessionID: g.client.session, RequestID: "per_1", Reply: "once"})
	g.client.emit(t, 4, native.TypeToolSuccess, native.ToolSuccessData{SessionID: g.client.session, AssistantMessage: "msg_a1", ID: "call_1", Content: []native.ToolContent{{Type: "text", Text: "hi"}}})
	g.client.emit(t, 5, native.TypeStepEnded, native.StepEndedData{SessionID: g.client.session, AssistantMessage: "msg_a1", Finish: "stop"})
	g.client.succeed(t, 6)
	events := g.finish(t)
	resolved := resolvedOf(t, events)
	if len(resolved) != 1 || resolved[0].Outcome != protocol.InteractionResolved || resolved[0].ChoiceID != "once" || resolved[0].Granted == nil || !*resolved[0].Granted || resolved[0].ToolCallID != g.request.ToolCallID {
		t.Fatalf("resolved %+v", resolved)
	}
	if events[len(events)-1].Type != protocol.TypeRunCompleted {
		t.Fatalf("ended %s", events[len(events)-1].Type)
	}
}

func TestARejectionWithoutAReasonEndsTheRunAsDeclinedAndOneWithAReasonCarriesIt(t *testing.T) {
	g := openGatedRun(t)
	if err := g.answer("reject", ""); err != nil {
		t.Fatal(err)
	}
	g.client.emit(t, 4, native.TypeToolFailed, native.ToolFailedData{SessionID: g.client.session, AssistantMessage: "msg_a1", ID: "call_1", Error: native.SessionError{Type: "aborted", Message: "The user declined this tool call"}})
	g.client.emit(t, 5, native.TypeExecutionInterrupted, native.ExecutionInterruptedData{SessionID: g.client.session, Reason: "shutdown"})
	events := g.finish(t)
	if resolved := resolvedOf(t, events); len(resolved) != 1 || resolved[0].Outcome != protocol.InteractionRejected || resolved[0].Granted == nil || *resolved[0].Granted {
		t.Fatalf("resolved %+v", resolved)
	}
	var failed protocol.RunFailedPayload
	last := events[len(events)-1]
	if last.Type != protocol.TypeRunFailed || last.DecodePayload(&failed) != nil || failed.Error.Code != "opencode_permission_declined" {
		t.Fatalf("ended %s %+v", last.Type, failed)
	}

	reasoned := openGatedRun(t)
	if err := reasoned.answer("reject", "use ls instead"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(reasoned.client.permissionReplies, []string{"per_1|reject|use ls instead"}) {
		t.Fatalf("replied %v", reasoned.client.permissionReplies)
	}
	reasoned.client.emit(t, 4, native.TypeExecutionInterrupted, native.ExecutionInterruptedData{SessionID: reasoned.client.session, Reason: "shutdown"})
	events = reasoned.finish(t)
	if last := events[len(events)-1]; last.Type != protocol.TypeRunFailed || last.DecodePayload(&failed) != nil || failed.Error.Code != "opencode_execution_interrupted" {
		t.Fatalf("a rejection that gave a reason was taken as one that ended the run: %+v", failed)
	}
}

func TestAPermissionAnsweredElsewhereOrLeftOpenAtTheRunsEndIsCancelled(t *testing.T) {
	g := openGatedRun(t)
	g.client.emit(t, 0, native.TypePermissionReplied, native.PermissionRepliedData{SessionID: g.client.session, RequestID: "per_1", Reply: "always"})
	g.client.toolCalled(t, 4, 5, "call_2", "shell", map[string]any{"command": "rm x"})
	g.client.emit(t, 0, native.TypePermissionAsked, native.PermissionAskedData{ID: "per_2", SessionID: g.client.session, Action: "shell", Resources: []string{"rm x"}, Source: &native.PermissionSource{Type: "tool", MessageID: "msg_a1", ID: "call_2"}})
	g.client.emit(t, 6, native.TypeExecutionFailed, native.ExecutionFailedData{SessionID: g.client.session, Error: native.SessionError{Type: "unknown", Message: "boom"}})
	events := g.finish(t)
	resolved := resolvedOf(t, events)
	if len(resolved) != 2 || resolved[0].Outcome != protocol.InteractionCancelled || resolved[0].Reason == nil || resolved[0].Reason.Code != "opencode_permission_replied_elsewhere" || resolved[1].Outcome != protocol.InteractionCancelled || resolved[1].Reason == nil || resolved[1].Reason.Code != "run_settled" {
		t.Fatalf("resolved %+v", resolved)
	}
	if err := g.answer("once", ""); !errors.Is(err, base.ErrInteractionNotFound) {
		t.Fatalf("an answer after the run ended: %v", err)
	}
}

func TestAnAnswerTheServerCannotTakeOrThatContradictsItselfLeavesThePermissionOpen(t *testing.T) {
	g := openGatedRun(t)
	if err := g.answer("maybe", ""); !errors.Is(err, base.ErrInvalidResolution) {
		t.Fatalf("an unknown choice: %v", err)
	}
	contradictory := *g
	if err := contradictory.session.Resolve(context.Background(), base.InteractionResolution{RunID: g.response.RunID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: g.request.InteractionID, RespondedBy: "user", SessionID: "session", RunID: g.response.RunID, ChoiceID: "reject", Granted: true}}); !errors.Is(err, base.ErrInvalidResolution) {
		t.Fatalf("a rejection claiming a grant: %v", err)
	}
	if err := g.session.Resolve(context.Background(), base.InteractionResolution{RunID: g.response.RunID, RespondedBy: "someone", Permission: &protocol.PermissionResolveRequest{InteractionID: g.request.InteractionID, RespondedBy: "someone", SessionID: "session", RunID: g.response.RunID, ChoiceID: "once", Granted: true}}); !errors.Is(err, base.ErrWrongResponder) {
		t.Fatalf("another responder: %v", err)
	}
	g.client.mu.Lock()
	g.client.replyErr = errors.New("connection refused")
	g.client.mu.Unlock()
	if err := g.answer("once", ""); err == nil {
		t.Fatal("a reply the server never took was reported as resolved")
	}
	g.client.mu.Lock()
	g.client.replyErr = nil
	g.client.mu.Unlock()
	if err := g.answer("always", ""); err != nil {
		t.Fatalf("the permission did not stay open after a failed reply: %v", err)
	}
	if !slices.Equal(g.client.permissionReplies, []string{"per_1|once|", "per_1|always|"}) {
		t.Fatalf("replied %v", g.client.permissionReplies)
	}
}

func TestAPermissionOutsideAToolCallTheRunStartedFailsTheRun(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(response.MessageIDs[0]))
	client.emit(t, 0, native.TypePermissionAsked, native.PermissionAskedData{ID: "per_1", SessionID: client.session, Action: "external_directory", Resources: []string{"/etc"}})
	events := adaptertest.Drain(t, stream, time.Second)
	var failed protocol.RunFailedPayload
	last := events[len(events)-1]
	if last.Type != protocol.TypeRunFailed || last.DecodePayload(&failed) != nil || failed.Error.Code != "opencode_permission_without_tool" {
		t.Fatalf("ended %s %+v", last.Type, failed)
	}
}

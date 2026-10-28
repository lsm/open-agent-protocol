package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/copilot/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func emptySession(t *testing.T, mode string) *session {
	t.Helper()
	p, err := rpc.Start(syntheticProcessConfig(t, mode))
	if err != nil {
		t.Fatal(err)
	}
	s := newSession(newReducer("s1", "ses", &corpusIDs{}, &corpusClock{}), p, 256, time.Second)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

func helloRequest() base.SubmitRequest {
	return base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "s1", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}}}
}

func TestSubmitBindsAnEventReceivedBeforeTheReply(t *testing.T) {
	s := emptySession(t, "early")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request := helloRequest()
	ack, stream, err := s.Submit(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, stream, time.Second)
	adaptertest.AssertRunTrace(t, ack, CapabilityRevision, events)
	if len(events) != 3 || events[0].Type != protocol.TypeRunStarted || events[1].Type != protocol.TypeContentDelta || events[2].Type != protocol.TypeRunCompleted {
		t.Fatalf("lost early event: %+v", events)
	}
	if string(ack.SubmissionID) == "m1" || string(ack.RunID) == "m1" {
		t.Fatal("native message identity escaped its private binding")
	}
}

func TestSubmitRefusesUnavailableControlsAndNonTextInput(t *testing.T) {
	s := emptySession(t, "")
	ctx := context.Background()
	for _, request := range []protocol.MessageSubmitRequest{
		{ModelID: protocol.ControlValue("")}, {Instructions: protocol.ControlValue("")}, {ToolChoice: json.RawMessage(`null`)}, {OutputSchema: json.RawMessage(`{}`)}, {Delivery: protocol.DeliverySteer}, {Delivery: protocol.DeliveryBTW},
	} {
		good := helloRequest()
		request.SessionID = good.Request.SessionID
		request.Messages = good.Request.Messages
		if request.Delivery == "" {
			request.Delivery = protocol.DeliveryAuto
		}
		_, _, err := s.Submit(ctx, base.SubmitRequest{Request: request})
		var unsupported *base.UnsupportedControlError
		if !errors.As(err, &unsupported) {
			t.Fatalf("control refusal: %v", err)
		}
	}
	for _, message := range []protocol.Message{{Role: protocol.RoleAssistant, Content: protocol.TextContent("hello")}, {Role: protocol.RoleUser, Content: protocol.PartsContent([]protocol.ContentPart{{Type: protocol.ContentImage}})}, {Role: protocol.RoleUser, Content: protocol.TextContent("")}} {
		request := helloRequest()
		request.Request.Messages = []protocol.Message{message}
		if _, _, err := s.Submit(ctx, request); !errors.Is(err, base.ErrInvalidSubmission) {
			t.Fatalf("input refusal: %v", err)
		}
	}
	state, err := s.State(ctx)
	if err != nil || len(state.ActiveRuns) != 0 {
		t.Fatalf("refusal created runs: %+v %v", state, err)
	}
}

func TestQueueAdmissionAndBound(t *testing.T) {
	s := emptySession(t, "queued")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for i := 0; i < 9; i++ {
		ack, _, err := s.Submit(ctx, helloRequest())
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && (ack.Admission != protocol.AdmissionQueued || ack.EffectiveDelivery != protocol.EffectiveDeliveryQueue || ack.DeliveryResolution != "session_busy") {
			t.Fatalf("queue admission %+v", ack)
		}
	}
	if _, _, err := s.Submit(ctx, helloRequest()); !errors.Is(err, base.ErrRunActive) {
		t.Fatalf("queue bound: %v", err)
	}
	state, err := s.State(ctx)
	if err != nil || len(state.ActiveRuns) != 9 {
		t.Fatalf("queue state %+v %v", state, err)
	}
	for i, active := range state.ActiveRuns {
		if i > 0 && (active.QueuePosition == nil || *active.QueuePosition != i) {
			t.Fatalf("queue position %+v", active)
		}
	}
}

func TestNativeRefusalDoesNotAdmitAnOAPRun(t *testing.T) {
	s := emptySession(t, "refuse")
	_, _, err := s.Submit(context.Background(), helloRequest())
	var refusal *rpc.RemoteError
	if !errors.As(err, &refusal) || refusal.Code != -32602 {
		t.Fatalf("native refusal lost: %v", err)
	}
	state, err := s.State(context.Background())
	if err != nil || len(state.ActiveRuns) != 0 {
		t.Fatalf("refused run leaked: %+v %v", state, err)
	}
}

func TestSubmitPreservesUnicodeTextAcrossMessagesAndParts(t *testing.T) {
	s := emptySession(t, "")
	request := helloRequest()
	request.Request.Messages = []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("\u4f60")}, {Role: protocol.RoleUser, Content: protocol.PartsContent([]protocol.ContentPart{{Type: protocol.ContentText, Text: "\u597d"}})}}
	ack, stream, err := s.Submit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, stream, time.Second)
	adaptertest.AssertRunTrace(t, ack, CapabilityRevision, events)
	var terminal protocol.RunCompletedPayload
	if err := events[len(events)-1].DecodePayload(&terminal); err != nil {
		t.Fatal(err)
	}
	parts, ok := terminal.FinalResponse.Content.Parts()
	if !ok || len(parts) != 1 || parts[0].Text != "\u4f60\u597d" {
		t.Fatalf("native round trip changed text: %s", terminal.FinalResponse.Content)
	}
}

func TestMissingNativeBindingRefusesAndStopsTheSession(t *testing.T) {
	s := emptySession(t, "missing-id")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ack, stream, err := s.Submit(ctx, helloRequest())
	if err == nil || ack.Accepted || stream != nil {
		t.Fatalf("missing binding admitted %+v %v", ack, err)
	}
	select {
	case <-s.process.Done():
	case <-ctx.Done():
		t.Fatal("invalid admission left child running")
	}
	if _, err := s.State(ctx); !errors.Is(err, base.ErrSessionClosed) {
		t.Fatalf("invalid session remained open: %v", err)
	}
}

func TestQueuedSubmissionPromotesAtItsNativeUserMessage(t *testing.T) {
	s := emptySession(t, "hold")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	first, firstStream, err := s.Submit(ctx, helloRequest())
	if err != nil {
		t.Fatal(err)
	}
	second, secondStream, err := s.Submit(ctx, helloRequest())
	if err != nil {
		t.Fatal(err)
	}
	if second.Admission != protocol.AdmissionQueued || first.RunID == second.RunID {
		t.Fatalf("queued admission %+v", second)
	}
	finished := adaptertest.Drain(t, firstStream, time.Second)
	if len(finished) != 2 || finished[1].Type != protocol.TypeRunCompleted {
		t.Fatalf("prior run did not settle: %+v", finished)
	}
	started := adaptertest.Next(t, secondStream, time.Second)
	if started.Type != protocol.TypeRunStarted || started.RunID != second.RunID {
		t.Fatalf("queued run did not start: %+v", started)
	}
	state, err := s.State(ctx)
	if err != nil || state.ActiveRunID != second.RunID || len(state.ActiveRuns) != 1 || state.ActiveRuns[0].Status != protocol.RunRunning {
		t.Fatalf("promoted state %+v %v", state, err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	terminal := adaptertest.Drain(t, secondStream, time.Second)
	if len(terminal) != 1 || terminal[0].Type != protocol.TypeRunFailed {
		t.Fatalf("close did not settle promoted run: %+v", terminal)
	}
}

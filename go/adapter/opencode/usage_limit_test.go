package opencode

import (
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func usageLimitError(body string) native.SessionError {
	status := 429
	failure := native.SessionError{Type: "provider.rate-limit", Message: "Go usage limit exceeded", Status: &status}
	failure.Response = &struct {
		Body string `json:"body"`
	}{Body: body}
	return failure
}

func TestAGatewayUsageLimitNamesItsWindowAndWhenItResets(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"monthly", `{"type":"error","error":{"type":"GoUsageLimitError","message":"Monthly usage limit reached. Resets in 6 days. To continue using this model now, enable usage from your available balance: https://opencode.ai/go"},"metadata":{"workspace":"wrk_1","limitName":"monthly"}}`, "Go usage limit exceeded; monthly limit, resets in 6 days"},
		{"rolling", `{"type":"error","error":{"type":"GoUsageLimitError","message":"5-hour usage limit reached. Resets in 3hr 1min. To continue using this model now, enable usage from your available balance: https://opencode.ai/go"},"metadata":{"workspace":"wrk_1","limitName":"5 hour"}}`, "Go usage limit exceeded; 5 hour limit, resets in 3 hours 1 minute"},
		{"subscription", `{"type":"error","error":{"type":"BlackUsageLimitError","message":"Subscription quota exceeded. Retry in 1hr 20min."},"metadata":{}}`, "Go usage limit exceeded; resets in 1 hour 20 minutes"},
		{"another error", `{"type":"error","error":{"type":"RateLimitError","message":"Resets in 2min."}}`, "Go usage limit exceeded"},
		{"not json", `upstream exploded`, "Go usage limit exceeded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := failureText(usageLimitError(tc.body)); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	if got := failureText(native.SessionError{Type: "unknown", Message: "provider exploded"}); got != "provider exploded" {
		t.Fatalf("without a body: %q", got)
	}
}

func TestAStepFailedOnAUsageLimitFailsTheRunNamingTheLimit(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(response.MessageIDs[0]))
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a1"})
	client.emit(t, 3, native.TypeStepFailed, native.StepFailedData{SessionID: client.session, AssistantMessage: "msg_a1", Error: usageLimitError(`{"type":"error","error":{"type":"GoUsageLimitError","message":"Weekly usage limit reached. Resets in 2 days."},"metadata":{"limitName":"weekly"}}`)})
	client.emit(t, 4, native.TypeExecutionFailed, native.ExecutionFailedData{SessionID: client.session, Error: native.SessionError{Type: "unknown", Message: "step failed"}})
	events := adaptertest.Drain(t, stream, time.Second)
	adaptertest.AssertRunTrace(t, response, CapabilityRevision, events)
	var failed protocol.RunFailedPayload
	if err := events[len(events)-1].DecodePayload(&failed); err != nil {
		t.Fatal(err)
	}
	if failed.Error.Code != "opencode_step_failed" || failed.Error.Message != "Go usage limit exceeded; weekly limit, resets in 2 days" {
		t.Fatalf("failed=%+v", failed)
	}
}

func TestAnExecutionFailedOnAUsageLimitFailsTheRunNamingTheLimit(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(response.MessageIDs[0]))
	client.emit(t, 2, native.TypeExecutionFailed, native.ExecutionFailedData{SessionID: client.session, Error: usageLimitError(`{"type":"error","error":{"type":"GoUsageLimitError","message":"Monthly usage limit reached. Resets in 6 days."},"metadata":{"limitName":"monthly"}}`)})
	events := adaptertest.Drain(t, stream, time.Second)
	adaptertest.AssertRunTrace(t, response, CapabilityRevision, events)
	var failed protocol.RunFailedPayload
	if err := events[len(events)-1].DecodePayload(&failed); err != nil {
		t.Fatal(err)
	}
	if failed.Error.Code != "opencode_execution_failed" || failed.Error.Message != "Go usage limit exceeded; monthly limit, resets in 6 days" {
		t.Fatalf("failed=%+v", failed)
	}
}

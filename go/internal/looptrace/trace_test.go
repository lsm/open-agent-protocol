package looptrace

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/agent"
	"github.com/lsm/open-agent-protocol/go/internal/provider"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type sequenceIDs struct {
	n int
}

func (s *sequenceIDs) NewID(kind string) string {
	s.n++
	return kind + "-" + string(rune('a'+s.n%26)) + string(rune('0'+s.n/26))
}

func trace(t *testing.T, ids Ids) *Trace {
	t.Helper()
	if ids == nil {
		ids = &sequenceIDs{}
	}
	built, err := NewTrace(Options{
		SessionID: "s1", RunID: "run-1", ModelID: "local/model",
		Revision: "reference-loop-v1", Ids: ids, NowMS: 1700000000000,
	})
	if err != nil {
		t.Fatalf("NewTrace: %v", err)
	}
	return built
}

func joinTypes(envelopes []protocol.Envelope) string {
	parts := make([]string, 0, len(envelopes))
	for _, envelope := range envelopes {
		parts = append(parts, string(envelope.Type))
	}
	return strings.Join(parts, " ")
}

func TestATraceNeedsAnIdGeneratorAndTwoIds(t *testing.T) {
	if _, err := NewTrace(Options{SessionID: "s1", RunID: "run-1"}); err == nil {
		t.Error("a trace with no id generator would number envelopes from nothing, so it must be refused")
	}
	if _, err := NewTrace(Options{Ids: &sequenceIDs{}}); err == nil {
		t.Error("a trace with no session or run would stamp envelopes with neither, so it must be refused")
	}
}

func TestEveryEnvelopeCarriesTheRunScopeAndAContiguousSequence(t *testing.T) {
	tr := trace(t, nil)
	envelopes := tr.Envelopes(agent.Event{Kind: agent.TextDelta, Delta: "a"})
	envelopes = append(envelopes, tr.Envelopes(agent.Event{Kind: agent.TextDelta, Delta: "b"})...)
	for index, envelope := range envelopes {
		if envelope.SessionID != "s1" || envelope.RunID != "run-1" {
			t.Errorf("envelope %d carries %s/%s, want the run's own session and run", index, envelope.SessionID, envelope.RunID)
		}
		if envelope.Sequence == nil || *envelope.Sequence != uint64(index+1) {
			t.Errorf("envelope %d has sequence %v, want %d: a run's sequence is positive and contiguous", index, envelope.Sequence, index+1)
		}
		if envelope.CapabilityRevision != "reference-loop-v1" {
			t.Errorf("envelope %d cites revision %q, want the trace's own", index, envelope.CapabilityRevision)
		}
		if envelope.TimestampMS == nil {
			t.Errorf("envelope %d carries no timestamp", index)
		}
	}
}

func TestAnAnswerBecomesTheCallCompletedWithTheCallersOwnResult(t *testing.T) {
	tr := trace(t, nil)
	call := provider.ToolCall{ID: "call_1", Name: "read", Arguments: `{"path":"a"}`}
	requested := tr.Envelopes(agent.Event{Kind: agent.ToolCallRequested, Call: &call})
	if got := joinTypes(requested); got != "action.call.requested" {
		t.Fatalf("asking for a call emits %s, want one action.call.requested", got)
	}
	var asked protocol.ActionCallPayload
	if err := json.Unmarshal(mustJSON(t, requested[0]), &asked); err != nil {
		t.Fatal(err)
	}
	if string(asked.ArgumentsJSON) != `{"path":"a"}` {
		t.Errorf("the ask carries %s, want the arguments the model wrote", asked.ArgumentsJSON)
	}
	if asked.InteractionID == "" {
		t.Error("an ask with no interaction_id is a call a control layer cannot answer")
	}
	if requested[0].ToolCallID != protocol.ToolCallID("call_1") {
		t.Errorf("the ask is scoped to tool call %q, want call_1", requested[0].ToolCallID)
	}

	result := provider.ToolResult{ToolCallID: "call_1", ToolName: "read", Parts: []provider.ContentPart{{Text: &provider.TextPart{Text: "a"}}}}
	resolved := tr.Envelopes(agent.Event{Kind: agent.ToolCallResolved, Call: &call, ToolResult: &result})
	if got := joinTypes(resolved); got != "action.call.completed" {
		t.Fatalf("an answered call emits %s, want one action.call.completed", got)
	}
	var completed protocol.ActionCallPayload
	if err := json.Unmarshal(mustJSON(t, resolved[0]), &completed); err != nil {
		t.Fatal(err)
	}
	if !contains(completed.Result, "a") {
		t.Errorf("the completion carries %s, want the caller's own result", completed.Result)
	}
}

func TestAnErroredCallFailsRatherThanCompleting(t *testing.T) {
	tr := trace(t, nil)
	call := provider.ToolCall{ID: "call_1", Name: "read", Arguments: "{}"}
	result := provider.ToolResult{ToolCallID: "call_1", Parts: []provider.ContentPart{{Text: &provider.TextPart{Text: "it was refused"}}}, IsError: true}
	envelopes := tr.Envelopes(agent.Event{Kind: agent.ToolCallResolved, Call: &call, ToolResult: &result})
	if got := joinTypes(envelopes); got != "action.call.failed" {
		t.Fatalf("an errored call emits %s, want action.call.failed", got)
	}
	var failed protocol.ActionCallPayload
	if err := json.Unmarshal(mustJSON(t, envelopes[0]), &failed); err != nil {
		t.Fatal(err)
	}
	if failed.Error == nil || failed.Error.Message != "it was refused" {
		t.Errorf("the failure carries %+v, want the tool's own reason", failed.Error)
	}
}

func TestACutOffOrUnansweredCallStillFailsTheCallOnTheWire(t *testing.T) {
	tr := trace(t, nil)
	call := provider.ToolCall{ID: "call_1", Name: "write", Arguments: `{"text":"cut`}
	for _, body := range []string{
		"Tool call \"write\" was not run: the reply hit the output token limit, so its arguments may be cut off. Call the tool again with complete arguments.",
		"Tool call \"write\" was not run: the run was cancelled while it waited for a result.",
	} {
		result := provider.ToolResult{ToolCallID: "call_1", Parts: []provider.ContentPart{{Text: &provider.TextPart{Text: body}}}, IsError: true}
		envelopes := tr.Envelopes(agent.Event{Kind: agent.ToolCallResolved, Call: &call, ToolResult: &result})
		if got := joinTypes(envelopes); got != "action.call.failed" {
			t.Fatalf("a call nothing ran emits %s, want action.call.failed", got)
		}
		var failed protocol.ActionCallPayload
		if err := json.Unmarshal(mustJSON(t, envelopes[0]), &failed); err != nil {
			t.Fatal(err)
		}
		if failed.Error == nil || failed.Error.Message != body {
			t.Errorf("the failure says %+v, want the loop's own reason: a call that silently did not run is what a model cannot recover from", failed.Error)
		}
	}
}

func TestArgumentsThatAreNotJSONAreOmittedRatherThanCarriedBroken(t *testing.T) {
	tr := trace(t, nil)
	call := provider.ToolCall{ID: "call_1", Name: "write", Arguments: `{"text":"cut`}
	envelopes := tr.Envelopes(agent.Event{Kind: agent.ToolCallRequested, Call: &call})
	var asked protocol.ActionCallPayload
	if err := json.Unmarshal(mustJSON(t, envelopes[0]), &asked); err != nil {
		t.Fatal(err)
	}
	if len(asked.ArgumentsJSON) != 0 {
		t.Errorf("a truncated argument string reaches the wire as %s, want it omitted: a control layer cannot be asked to run invalid JSON", asked.ArgumentsJSON)
	}
}

func TestAnAnsweredRunCompletesWithItsTextAndStopReason(t *testing.T) {
	tr := trace(t, nil)
	event := agent.Event{Kind: agent.AgentEnd, Termination: agent.TerminationClean, Result: agent.Result{
		FinalMessage: provider.AssistantContent{
			Parts:      []provider.ContentPart{{Text: &provider.TextPart{Text: "I read it."}}},
			StopReason: provider.StopStop,
			API:        "openai-completions", Provider: "local", Model: "local/model",
		},
		Termination: agent.TerminationClean,
	}}
	envelopes := tr.Envelopes(event)
	if got := joinTypes(envelopes); got != "run.completed" {
		t.Fatalf("an answered run emits %s, want one run.completed", got)
	}
	var completed protocol.RunCompletedPayload
	if err := json.Unmarshal(mustJSON(t, envelopes[0]), &completed); err != nil {
		t.Fatal(err)
	}
	if completed.StopReason != "stop" {
		t.Errorf("the completion carries stop reason %q, want the model's own", completed.StopReason)
	}
	text, ok := completed.FinalResponse.Content.Text()
	if !ok || text != "I read it." {
		t.Errorf("the completion carries %s, want the run's final text", completed.FinalResponse.Content)
	}
	if completed.ModelID != "local/model" {
		t.Errorf("the completion names model %q, want the model's own", completed.ModelID)
	}
}

func TestAProviderRefusalSettlesAsAFailedRunWithTheProvidersCode(t *testing.T) {
	tr := trace(t, nil)
	envelopes := tr.Envelopes(agent.Event{Kind: agent.AgentEnd, Result: agent.Result{
		FinalMessage: provider.AssistantContent{
			Parts:      []provider.ContentPart{{Text: &provider.TextPart{Text: "the provider refused the request"}}},
			StopReason: provider.StopError,
		},
	}})
	if got := joinTypes(envelopes); got != "run.failed" {
		t.Fatalf("a provider refusal emits %s, want run.failed", got)
	}
	var failed protocol.RunFailedPayload
	if err := json.Unmarshal(mustJSON(t, envelopes[0]), &failed); err != nil {
		t.Fatal(err)
	}
	if failed.Error.Code != "provider_error" {
		t.Errorf("the failure carries code %q, want provider_error", failed.Error.Code)
	}
	if failed.Error.Message != "the provider refused the request" {
		t.Errorf("the failure carries %q, want the provider's own reason", failed.Error.Message)
	}
}

func TestACancelledRunCancelsRatherThanFailing(t *testing.T) {
	tr := trace(t, nil)
	envelopes := tr.Envelopes(agent.Event{Kind: agent.AgentEnd, Termination: agent.TerminationCanceled, Result: agent.Result{
		Termination: agent.TerminationCanceled,
	}})
	if got := joinTypes(envelopes); got != "run.cancelled" {
		t.Fatalf("a cancelled run emits %s, want run.cancelled", got)
	}
}

func TestARunThatHitTheTurnLimitCompletes(t *testing.T) {
	tr := trace(t, nil)
	envelopes := tr.Envelopes(agent.Event{Kind: agent.AgentEnd, Result: agent.Result{
		FinalMessage: provider.AssistantContent{Parts: []provider.ContentPart{{Text: &provider.TextPart{Text: "partial"}}}, StopReason: provider.StopToolUse},
		Termination:  agent.TerminationMaxTurns,
	}})
	if got := joinTypes(envelopes); got != "run.completed" {
		t.Errorf("a run that hit the turn limit emits %s, want run.completed: a limit is not a failure", got)
	}
}

func TestALoopFailureIsAFailedRunCarryingItsReason(t *testing.T) {
	tr := trace(t, nil)
	envelopes := tr.Envelopes(agent.Event{Kind: agent.RunFailed, Reason: "a run needs a streamer to reach a model"})
	if got := joinTypes(envelopes); got != "run.failed" {
		t.Fatalf("a loop failure emits %s, want run.failed", got)
	}
	var failed protocol.RunFailedPayload
	if err := json.Unmarshal(mustJSON(t, envelopes[0]), &failed); err != nil {
		t.Fatal(err)
	}
	if failed.Error.Message != "a run needs a streamer to reach a model" {
		t.Errorf("the failure carries %q, want the loop's own reason", failed.Error.Message)
	}
}

func TestTheLoopOwnEventsCarryNothingOntoTheWire(t *testing.T) {
	tr := trace(t, nil)
	assistant := provider.AssistantContent{StopReason: provider.StopStop}
	for _, kind := range []agent.EventKind{agent.AgentStart, agent.TurnStart, agent.MessageStart, agent.MessageEnd, agent.TurnEnd} {
		if envelopes := tr.Envelopes(agent.Event{Kind: kind, Assistant: &assistant}); len(envelopes) != 0 {
			t.Errorf("%q emits %s, want nothing: those are the loop's own bookkeeping, not protocol events", kind, joinTypes(envelopes))
		}
	}
	if tr.sequence != 1 {
		t.Errorf("the sequence reached %d without an envelope, want 1: a gap is a lost event", tr.sequence)
	}
}

func TestReasoningReachesTheWireAsAReasoningPart(t *testing.T) {
	tr := trace(t, nil)
	envelopes := tr.Envelopes(agent.Event{Kind: agent.ReasoningDelta, Delta: "thinking"})
	var delta protocol.ContentDeltaPayload
	if err := json.Unmarshal(mustJSON(t, envelopes[0]), &delta); err != nil {
		t.Fatal(err)
	}
	if delta.Part.Type != protocol.ContentReasoning {
		t.Errorf("a reasoning delta arrives as %q, want a reasoning part: outbound reasoning is preserved even where it is not accepted back", delta.Part.Type)
	}
	if delta.Part.Reasoning != "thinking" {
		t.Errorf("the reasoning part carries %q, want the delta's text", delta.Part.Reasoning)
	}
}

func contains(raw json.RawMessage, needle string) bool {
	return strings.Contains(string(raw), needle)
}

func mustJSON(t *testing.T, envelope protocol.Envelope) []byte {
	t.Helper()
	encoded, err := json.Marshal(envelope.Payload)
	if err != nil {
		t.Fatalf("marshalling %s: %v", envelope.Type, err)
	}
	return encoded
}

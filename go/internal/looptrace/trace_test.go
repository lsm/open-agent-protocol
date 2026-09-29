package looptrace

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/agent"
	"github.com/lsm/open-agent-protocol/go/internal/provider"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/validation"
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
		Revision:       "reference-loop-v1",
		Requester:      "goap.agent",
		Responder:      "user",
		ExecutionOwner: "user",
		Ids:            ids,
		Now:            func() int64 { return 1700000000000 },
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

func TestATraceRefusesWhatTheSchemaWouldReject(t *testing.T) {
	full := Options{SessionID: "s1", RunID: "run-1", Ids: &sequenceIDs{}, Requester: "agent", Responder: "user", ExecutionOwner: "user"}
	if _, err := NewTrace(full); err != nil {
		t.Fatalf("a fully specified trace is refused: %v", err)
	}
	if _, err := NewTrace(Options{SessionID: "s1", RunID: "run-1", Requester: "a", Responder: "b", ExecutionOwner: "c"}); err == nil {
		t.Error("a trace with no id generator would number envelopes from nothing, so it must be refused")
	}
	if _, err := NewTrace(Options{Ids: &sequenceIDs{}, Requester: "a", Responder: "b", ExecutionOwner: "c"}); err == nil {
		t.Error("a trace with no session or run would stamp envelopes with neither, so it must be refused")
	}
	for _, missing := range []string{"requester", "responder", "owner"} {
		options := full
		switch missing {
		case "requester":
			options.Requester = ""
		case "responder":
			options.Responder = ""
		case "owner":
			options.ExecutionOwner = ""
		}
		if _, err := NewTrace(options); err == nil {
			t.Errorf("a trace with no %s would emit an action.call naming \"\", which the schema's opaqueID rejects", missing)
		}
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
	if asked.RequestedBy != "goap.agent" || asked.RespondedBy != "user" || asked.ExecutionOwner != "user" {
		t.Errorf("the ask names %q/%q/%q, want the three participants the trace was built with: a client-executed tool's owner is the control layer, not the endpoint", asked.RequestedBy, asked.RespondedBy, asked.ExecutionOwner)
	}
	if asked.InteractionID == "" {
		t.Error("an ask with no interaction_id is a call a control layer cannot answer")
	}
	if requested[0].ToolCallID != protocol.ToolCallID("call_1") {
		t.Errorf("the ask is scoped to tool call %q, want call_1", requested[0].ToolCallID)
	}

	result := provider.ToolResult{ToolCallID: "call_1", ToolName: "read", Parts: []provider.ContentPart{{Text: &provider.TextPart{Text: "a"}}}}
	resolved := tr.Envelopes(agent.Event{Kind: agent.ToolCallResolved, Call: &call, ToolResult: &result})
	if got := joinTypes(resolved); got != "action.call.started action.call.completed" {
		t.Fatalf("an answered call emits %s, want it started immediately before the terminal it derives", got)
	}
	var completed protocol.ActionCallPayload
	if err := json.Unmarshal(mustJSON(t, last(resolved)), &completed); err != nil {
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
	if got := joinTypes(envelopes); got != "action.call.started action.call.failed" {
		t.Fatalf("an errored call emits %s, want it started immediately before the terminal it derives", got)
	}
	var failed protocol.ActionCallPayload
	if err := json.Unmarshal(mustJSON(t, last(envelopes)), &failed); err != nil {
		t.Fatal(err)
	}
	if failed.Error == nil || failed.Error.Message != "it was refused" {
		t.Errorf("the failure carries %+v, want the tool's own reason", failed.Error)
	}
}

func TestACallTheRunWasCancelledWaitingForIsCancelledRatherThanFailed(t *testing.T) {
	tr := trace(t, nil)
	call := provider.ToolCall{ID: "call_1", Name: "read", Arguments: "{}"}
	cancelled := provider.ToolResult{ToolCallID: "call_1", Parts: []provider.ContentPart{{Text: &provider.TextPart{Text: "Tool call \"read\" was not run: the run was cancelled while it waited for a result."}}}, IsError: true}
	envelopes := tr.Envelopes(agent.Event{Kind: agent.ToolCallCancelled, Call: &call, ToolResult: &cancelled})
	if got := joinTypes(envelopes); got != "action.call.cancelled" {
		t.Fatalf("a call the run was cancelled waiting for emits %s, want action.call.cancelled: a failure reaches the validator only from started or progress, and a cancel closes a pending call", got)
	}
	var closed protocol.ActionCallPayload
	if err := json.Unmarshal(mustJSON(t, last(envelopes)), &closed); err != nil {
		t.Fatal(err)
	}
	if closed.Error != nil || closed.Result != nil {
		t.Errorf("the cancellation carries %+v, want neither a result nor an error: nothing ran the call, so there is nothing to report about how it went", closed)
	}
	if closed.ExecutionOwner != "user" {
		t.Errorf("the cancellation names owner %q, want the trace's own", closed.ExecutionOwner)
	}
}

func TestACutOffCallIsFailedWithTheLoopsOwnReason(t *testing.T) {
	tr := trace(t, nil)
	call := provider.ToolCall{ID: "call_1", Name: "write", Arguments: `{"text":"cut`}
	body := "Tool call \"write\" was not run: the reply hit the output token limit, so its arguments may be cut off. Call the tool again with complete arguments."
	result := provider.ToolResult{ToolCallID: "call_1", Parts: []provider.ContentPart{{Text: &provider.TextPart{Text: body}}}, IsError: true}
	envelopes := tr.Envelopes(agent.Event{Kind: agent.ToolCallResolved, Call: &call, ToolResult: &result})
	if got := joinTypes(envelopes); got != "action.call.started action.call.failed" {
		t.Fatalf("a cut-off call emits %s, want it started immediately before the terminal it derives", got)
	}
	var failed protocol.ActionCallPayload
	if err := json.Unmarshal(mustJSON(t, last(envelopes)), &failed); err != nil {
		t.Fatal(err)
	}
	if failed.Error == nil || failed.Error.Message != body {
		t.Errorf("the failure says %+v, want the loop's own reason: a tool that silently did not run is what a model cannot recover from", failed.Error)
	}
}

func TestArgumentsThatAreNotJSONAreCarriedAsNullRatherThanOmitted(t *testing.T) {
	tr := trace(t, nil)
	call := provider.ToolCall{ID: "call_1", Name: "write", Arguments: `{"text":"cut`}
	envelopes := tr.Envelopes(agent.Event{Kind: agent.ToolCallRequested, Call: &call})
	var asked protocol.ActionCallPayload
	if err := json.Unmarshal(mustJSON(t, envelopes[0]), &asked); err != nil {
		t.Fatal(err)
	}
	if string(asked.ArgumentsJSON) != "null" {
		t.Errorf("a truncated argument string reaches the wire as %s, want null: the member is required, and a control layer cannot be asked to run invalid JSON", asked.ArgumentsJSON)
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

func TestEveryPartOfACallersResultReachesTheWire(t *testing.T) {
	tr := trace(t, nil)
	call := provider.ToolCall{ID: "call_1", Name: "read", Arguments: "{}"}
	result := provider.ToolResult{ToolCallID: "call_1", Parts: []provider.ContentPart{
		{Text: &provider.TextPart{Text: "first"}},
		{Image: &provider.ImagePart{Data: "AAAA", MediaType: "image/png"}},
		{Text: &provider.TextPart{Text: "last"}},
	}}
	envelopes := tr.Envelopes(agent.Event{Kind: agent.ToolCallResolved, Call: &call, ToolResult: &result})
	var completed protocol.ActionCallPayload
	if err := json.Unmarshal(mustJSON(t, last(envelopes)), &completed); err != nil {
		t.Fatal(err)
	}
	var parts []protocol.ContentPart
	if err := json.Unmarshal(completed.Result, &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 3 {
		t.Fatalf("the completion carries %d parts, want all three: a caller's own result is what the answer has to match", len(parts))
	}
	if parts[0].Text != "first" || parts[2].Text != "last" {
		t.Errorf("the completion carries %q and %q, want first and last in the caller's order", parts[0].Text, parts[2].Text)
	}
	if parts[1].Type != protocol.ContentImage || parts[1].Image == nil {
		t.Fatalf("the middle part is %+v, want the caller's image: dropping it would misreport the result", parts[1])
	}
	if !strings.HasPrefix(parts[1].Image.URL, "data:image/png;base64,") {
		t.Errorf("the image part carries url %q, want the caller's data URL: the image vocabulary admits a url or a data and media_type pair, never both", parts[1].Image.URL)
	}
	if parts[1].Image.MediaType != "" {
		t.Errorf("the image part also carries media_type %q, which the schema's image oneOf does not admit alongside a url", parts[1].Image.MediaType)
	}
}

func TestARefusalWithNoTextStillCarriesAMessage(t *testing.T) {
	tr := trace(t, nil)
	envelopes := tr.Envelopes(agent.Event{Kind: agent.AgentEnd, Result: agent.Result{
		FinalMessage: provider.AssistantContent{StopReason: provider.StopError},
	}})
	var failed protocol.RunFailedPayload
	if err := json.Unmarshal(mustJSON(t, envelopes[0]), &failed); err != nil {
		t.Fatal(err)
	}
	if failed.Error.Message == "" {
		t.Error("a failure with an empty message is refused by the schema, and a filtered or empty reply is exactly when a provider reports an error with no text")
	}
	if !strings.Contains(failed.Error.Message, string(provider.StopError)) {
		t.Errorf("the failure says %q, want it to name the stop reason it had nothing else to report", failed.Error.Message)
	}
}

func TestEachEnvelopeCarriesTheMomentItWasWritten(t *testing.T) {
	tick := int64(1700000000000)
	tr, err := NewTrace(Options{
		SessionID: "s1", RunID: "run-1", Requester: "agent", Responder: "user",
		ExecutionOwner: "user", Ids: &sequenceIDs{}, Now: func() int64 { tick++; return tick },
	})
	if err != nil {
		t.Fatal(err)
	}
	first := tr.Envelopes(agent.Event{Kind: agent.TextDelta, Delta: "a"})
	second := tr.Envelopes(agent.Event{Kind: agent.TextDelta, Delta: "b"})
	if first[0].TimestampMS == nil || second[0].TimestampMS == nil {
		t.Fatal("an envelope with no timestamp cannot say when its event happened")
	}
	if *first[0].TimestampMS == *second[0].TimestampMS {
		t.Errorf("both envelopes carry %d, want each to carry the moment it was written: a run.completed minutes later is not the same instant as its first delta", *first[0].TimestampMS)
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

func last(envelopes []protocol.Envelope) protocol.Envelope {
	return envelopes[len(envelopes)-1]
}

func mustJSON(t *testing.T, envelope protocol.Envelope) []byte {
	t.Helper()
	encoded, err := json.Marshal(envelope.Payload)
	if err != nil {
		t.Fatalf("marshalling %s: %v", envelope.Type, err)
	}
	return encoded
}

func TestEveryPayloadMatchesItsOwnTypeSchemaAndNotTheTraceRulesAroundIt(t *testing.T) {
	validator, err := validation.New()
	if err != nil {
		t.Fatalf("building the validator: %v", err)
	}
	tr := trace(t, &sequenceIDs{})
	call := provider.ToolCall{ID: "call_1", Name: "read", Arguments: `{"path":"a"}`}
	cut := provider.ToolCall{ID: "call_2", Name: "write", Arguments: `{"text":"cut`}
	tr.Accepted("call_1", "resolve-1")
	tr.Accepted("call_2", "resolve-2")
	answered := provider.ToolResult{ToolCallID: "call_1", Parts: []provider.ContentPart{{Text: &provider.TextPart{Text: "a"}}}}
	failed := provider.ToolResult{ToolCallID: "call_2", Parts: []provider.ContentPart{{Text: &provider.TextPart{Text: "not run"}}}, IsError: true}
	answeredText := provider.AssistantContent{
		Parts:      []provider.ContentPart{{Text: &provider.TextPart{Text: "done"}}},
		StopReason: provider.StopStop, Model: "local/model",
	}
	events := []agent.Event{
		{Kind: agent.TextDelta, Delta: "reading"},
		{Kind: agent.ReasoningDelta, Delta: "thinking"},
		{Kind: agent.ToolCallRequested, Call: &call},
		{Kind: agent.ToolCallResolved, Call: &call, ToolResult: &answered},
		{Kind: agent.ToolCallRequested, Call: &cut},
		{Kind: agent.ToolCallResolved, Call: &cut, ToolResult: &failed},
		{Kind: agent.ToolCallCancelled, Call: &call, ToolResult: &failed},
		{Kind: agent.AgentEnd, Result: agent.Result{FinalMessage: answeredText}},
		{Kind: agent.AgentEnd, Result: agent.Result{
			FinalMessage: provider.AssistantContent{StopReason: provider.StopError},
		}},
		{Kind: agent.RunFailed, Reason: "a run needs a streamer"},
		{Kind: agent.AgentEnd, Result: agent.Result{Termination: agent.TerminationCanceled}},
		{Kind: agent.AgentEnd, Result: agent.Result{
			FinalMessage: provider.AssistantContent{Parts: []provider.ContentPart{{Text: &provider.TextPart{Text: "partial"}}}, StopReason: provider.StopToolUse},
			Termination:  agent.TerminationMaxTurns,
		}},
	}
	var envelopes []protocol.Envelope
	for _, event := range events {
		envelopes = append(envelopes, tr.Envelopes(event)...)
	}
	seen := map[protocol.EnvelopeType]bool{}
	for _, envelope := range envelopes {
		seen[envelope.Type] = true
	}
	for _, want := range []protocol.EnvelopeType{
		protocol.TypeContentDelta, protocol.TypeActionCallRequested,
		protocol.TypeActionCallStarted, protocol.TypeActionCallCompleted, protocol.TypeActionCallFailed,
		protocol.TypeActionCallCancelled,
		protocol.TypeRunCompleted, protocol.TypeRunFailed, protocol.TypeRunCancelled,
	} {
		if !seen[want] {
			t.Errorf("no %s was emitted, so this is not exercising every mapping: %s", want, joinTypes(envelopes))
		}
	}
	for _, envelope := range envelopes {
		encoded, err := json.Marshal(envelope)
		if err != nil {
			t.Fatalf("marshalling %s: %v", envelope.Type, err)
		}
		one := &bytes.Buffer{}
		one.Write(encoded)
		one.WriteByte('\n')
		result := validator.Validate(one, "looptrace.jsonl")
		for _, diagnostic := range result.Diagnostics {
			if diagnostic.Code != "schema_invalid" {
				continue
			}
			t.Errorf("%s does not match the schema for its own type: %s\n%s", envelope.Type, diagnostic.Message, encoded)
		}
	}
}

func TestAToolErrorWithNoMessageStillCarriesOne(t *testing.T) {
	tr := trace(t, nil)
	call := provider.ToolCall{ID: "call_1", Name: "read", Arguments: "{}"}
	result := provider.ToolResult{ToolCallID: "call_1", ToolName: "read", IsError: true}
	envelopes := tr.Envelopes(agent.Event{Kind: agent.ToolCallResolved, Call: &call, ToolResult: &result})
	if got := joinTypes(envelopes); got != "action.call.started action.call.failed" {
		t.Fatalf("a tool error with no message emits %s, want it started immediately before the terminal it derives", got)
	}
	var failed protocol.ActionCallPayload
	if err := json.Unmarshal(mustJSON(t, last(envelopes)), &failed); err != nil {
		t.Fatal(err)
	}
	if failed.Error == nil || failed.Error.Message == "" {
		t.Fatalf("the failure carries %+v, want a message: the schema admits no empty one, and a tool that reports an error with no text is exactly the case that leaves nothing to say", failed.Error)
	}
	if !strings.Contains(failed.Error.Message, `"read"`) {
		t.Errorf("the failure says %q, want it to name the tool: nothing else identifies which call went wrong", failed.Error.Message)
	}
}

func TestAResolveDerivedTerminalNamesTheRecordedRequestAndInventsNoneWithoutOne(t *testing.T) {
	tr := trace(t, nil)
	call := provider.ToolCall{ID: "call_1", Name: "read", Arguments: "{}"}
	result := provider.ToolResult{ToolCallID: "call_1", Parts: []provider.ContentPart{{Text: &provider.TextPart{Text: "a"}}}}
	tr.Accepted("call_1", "resolve-7")
	envelopes := tr.Envelopes(agent.Event{Kind: agent.ToolCallResolved, Call: &call, ToolResult: &result})
	for index, want := range []protocol.EnvelopeType{protocol.TypeActionCallStarted, protocol.TypeActionCallCompleted} {
		var payload protocol.ActionCallPayload
		if err := json.Unmarshal(mustJSON(t, envelopes[index]), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.RequestID != "resolve-7" {
			t.Errorf("%s names request %q, want resolve-7: only the control layer knows which of its own requests an answer answers", want, payload.RequestID)
		}
	}
	unrecorded := trace(t, nil)
	envelopes = unrecorded.Envelopes(agent.Event{Kind: agent.ToolCallResolved, Call: &call, ToolResult: &result})
	var payload protocol.ActionCallPayload
	if err := json.Unmarshal(mustJSON(t, last(envelopes)), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.RequestID != "" {
		t.Errorf("a terminal cites request %q with none recorded, want none: a trace that invented a resolve request would settle a call against an answer nobody gave", payload.RequestID)
	}
}

func TestTheWholeToolSequenceSatisfiesTheLifecycleTheValidatorEnforces(t *testing.T) {
	validator, err := validation.New()
	if err != nil {
		t.Fatalf("building the validator: %v", err)
	}
	tr := trace(t, &sequenceIDs{})
	call := provider.ToolCall{ID: "call_1", Name: "read", Arguments: `{"path":"a"}`}
	dropped := provider.ToolCall{ID: "call_2", Name: "write", Arguments: "{}"}
	answered := provider.ToolResult{ToolCallID: "call_1", Parts: []provider.ContentPart{{Text: &provider.TextPart{Text: "a"}}}}
	tr.Accepted("call_1", "resolve-1")
	var envelopes []protocol.Envelope
	for _, event := range []agent.Event{
		{Kind: agent.ToolCallRequested, Call: &call},
		{Kind: agent.ToolCallResolved, Call: &call, ToolResult: &answered},
		{Kind: agent.ToolCallRequested, Call: &dropped},
		{Kind: agent.ToolCallCancelled, Call: &dropped},
	} {
		envelopes = append(envelopes, tr.Envelopes(event)...)
	}
	lines := &bytes.Buffer{}
	for _, envelope := range envelopes {
		encoded, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		lines.Write(encoded)
		lines.WriteByte('\n')
	}
	for _, code := range []string{"illegal_tool_transition", "unmatched_tool", "schema_invalid", "unmatched_interaction"} {
		result := validator.Validate(lines, "looptrace.jsonl")
		for _, diagnostic := range result.Diagnostics {
			if diagnostic.Code == code {
				t.Errorf("%s: %s\n%s", code, diagnostic.Message, lines.String())
			}
		}
	}
}

func TestACallNamingNoToolReachesNoEnvelope(t *testing.T) {
	tr := trace(t, nil)
	nameless := provider.ToolCall{ID: "call_1", Arguments: "{}"}
	result := provider.ToolResult{ToolCallID: "call_1", Parts: []provider.ContentPart{{Text: &provider.TextPart{Text: "a"}}}}
	for _, event := range []agent.Event{
		{Kind: agent.ToolCallRequested, Call: &nameless},
		{Kind: agent.ToolCallResolved, Call: &nameless, ToolResult: &result},
		{Kind: agent.ToolCallCancelled, Call: &nameless},
	} {
		if envelopes := tr.Envelopes(event); len(envelopes) != 0 {
			t.Errorf("%q for a call naming no tool emits %s, want nothing: name is a required non-empty member and nothing could answer such a call", event.Kind, joinTypes(envelopes))
		}
	}
	if tr.sequence != 1 {
		t.Errorf("the sequence reached %d, want 1: a refused call must not consume a sequence number a later envelope would then skip", tr.sequence)
	}
}

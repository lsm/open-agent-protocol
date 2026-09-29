package provider

import (
	"encoding/json"
	"testing"
)

func TestAnEmptyToolCallsKeySuppressesTheTextAndThinkingBranches(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[],"content":"text"}}]}`),
	)
	if countKind(events, EventTextDelta) != 0 {
		t.Error("zig branches on the presence of the tool_calls key, so an empty array suppresses the content branch too")
	}
	nulled := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"tool_calls":null,"reasoning_content":"thought"}}]}`),
	)
	if countKind(nulled, EventThinkingDelta) != 0 {
		t.Error("a null tool_calls key still suppresses the reasoning branch")
	}
	without := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"content":"text"}}]}`),
	)
	if countKind(without, EventTextDelta) != 1 {
		t.Error("with no tool_calls key at all the content branch runs")
	}
}

func TestANullUsageLeavesTheAccumulatedTotalsAlone(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"usage":{"prompt_tokens":10,"completion_tokens":4}}`),
		sseFrame(`{"choices":[{"delta":{"content":"x"}}],"usage":null}`),
	)
	done := findEvent(t, events, EventDone)
	if done.Message.Usage.InputTokens != 10 || done.Message.Usage.OutputTokens != 4 {
		t.Errorf("usage = %+v, want the reported totals kept: zig reads usage only when it is an object", done.Message.Usage)
	}
	nonObject := runStream(t, streamModel(),
		sseFrame(`{"usage":{"prompt_tokens":10,"completion_tokens":4}}`),
		sseFrame(`{"usage":42}`),
	)
	got := findEvent(t, nonObject, EventDone)
	if got.Message.Usage.InputTokens != 10 {
		t.Errorf("usage = %+v, want a non-object left alone too", got.Message.Usage)
	}
	empty := runStream(t, streamModel(),
		sseFrame(`{"usage":{"prompt_tokens":10,"completion_tokens":4}}`),
		sseFrame(`{"usage":{}}`),
	)
	gotEmpty := findEvent(t, empty, EventDone)
	if gotEmpty.Message.Usage.InputTokens != 10 {
		t.Errorf("usage = %+v, want prompt_tokens kept: zig reads a member only when it is present", gotEmpty.Message.Usage)
	}
	if gotEmpty.Message.Usage.OutputTokens != 0 {
		t.Errorf("usage = %+v, want output zeroed: zig assigns output = completion + reasoning unconditionally inside the usage branch", gotEmpty.Message.Usage)
	}
}

func TestAnAbsentBaseURLGetsZigsEmptyCaps(t *testing.T) {
	caps := DetectCapabilities("", false)
	if caps.Vision {
		t.Error("an unknown provider type is an empty caps struct, not a compatible one")
	}
	if caps.ProviderType != ProviderUnknown {
		t.Errorf("provider type = %q, want unknown", caps.ProviderType)
	}
	if caps.ExtendedThinking || caps.PromptCaching {
		t.Errorf("caps = %+v, want every non-default field left alone", caps)
	}
}

func TestEveryEventIsStampedWithTheClock(t *testing.T) {
	ticks := int64(1000)
	sink := &EventSink{}
	Stream(sink, streamModel(), Context{}, StreamOptions{Now: func() int64 {
		ticks += 7
		return ticks
	}}, chunkReader([]string{sseFrame(`{"choices":[{"delta":{"content":"x"}}]}`)}), nil)
	events := sink.take()
	if len(events) == 0 {
		t.Fatal("no events")
	}
	for _, e := range events {
		if e.Partial.Timestamp == 0 && e.Message == nil {
			t.Errorf("a %q event carries no timestamp", e.Kind)
		}
		if e.Message != nil && e.Message.Timestamp == 0 {
			t.Error("the terminal message carries no timestamp")
		}
	}
	if events[0].Partial.Timestamp == 0 {
		t.Error("the start event is stamped before the first byte is read")
	}
}

func TestAKeepaliveIsEmittedOnThePingInterval(t *testing.T) {
	now := int64(0)
	reads := 0
	sink := &EventSink{}
	Stream(sink, streamModel(), Context{}, StreamOptions{
		Now:        func() int64 { return now },
		PingMillis: 50,
	}, func() ([]byte, error) {
		now += 60
		reads++
		if reads > 3 {
			return nil, nil
		}
		return []byte(sseFrame(`{"choices":[{"delta":{"content":"x"}}]}`)), nil
	}, nil)
	events := sink.take()
	if countKind(events, EventKeepalive) == 0 {
		t.Fatalf("got %v, want a keepalive once the interval elapsed", kindsOf(events))
	}
}

func TestTheFirstPingFiresBecauseTheLastPingStartsAtZero(t *testing.T) {
	sink := &EventSink{}
	Stream(sink, streamModel(), Context{}, StreamOptions{
		Now:        func() int64 { return 1_700_000_000_000 },
		PingMillis: 5000,
	}, chunkReader([]string{sseFrame(`{"choices":[{"delta":{"content":"x"}}]}`)}), nil)
	events := sink.take()
	if events[1].Kind != EventKeepalive {
		t.Errorf("events = %v, want a keepalive on the first loop: zig's last_ping_time starts at 0", kindsOf(events))
	}
}

func TestNoKeepaliveWithoutAPingInterval(t *testing.T) {
	events := runStream(t, streamModel(), sseFrame(`{"choices":[{"delta":{"content":"x"}}]}`))
	if countKind(events, EventKeepalive) != 0 {
		t.Errorf("got %v, want no keepalive when pinging is off", kindsOf(events))
	}
}

func TestTheMessageWriterSkipsAnAbortedAssistantEvenIfOneReachesIt(t *testing.T) {
	aborted := Message{Assistant: &AssistantContent{StopReason: StopAborted, Parts: []ContentPart{{Text: &TextPart{Text: "partial"}}}}}
	if !shouldSkipAssistant(aborted) {
		t.Error("the writer's own guard must drop an aborted assistant")
	}
	failed := Message{Assistant: &AssistantContent{StopReason: StopError, Parts: []ContentPart{{Text: &TextPart{Text: "partial"}}}}}
	if !shouldSkipAssistant(failed) {
		t.Error("the writer's own guard must drop an errored assistant")
	}
	kept := Message{Assistant: &AssistantContent{StopReason: "end_turn", Parts: []ContentPart{{Text: &TextPart{Text: "fine"}}}}}
	if shouldSkipAssistant(kept) {
		t.Error("a normal stop reason is kept")
	}
	if shouldSkipAssistant(Message{User: &UserContent{Text: "hi", HasText: true}}) {
		t.Error("a user message is never skipped")
	}
}

func TestTheBodyTruncatesALongToolIDForAnOpenAIHost(t *testing.T) {
	long := "abcdefghijklmnopqrstuvwxyz1234567890ABCDEFGHIJ"
	messages := []Message{
		{Assistant: &AssistantContent{API: "openai-completions", Provider: "openai", Model: "gpt-4o", StopReason: "tool_use", Parts: []ContentPart{
			{ToolCall: &ToolCall{ID: long, Name: "bash", Arguments: "{}"}},
		}}},
		{ToolResult: &ToolResult{ToolCallID: long, Parts: []ContentPart{{Text: &TextPart{Text: "ok"}}}}},
	}
	body := BuildRequestBody(openAIModel(), Context{Messages: messages}, StreamOptions{})
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("the body is not json: %v", err)
	}
	want := NormalizeToolID(long, 40)
	for _, m := range decoded["messages"].([]any) {
		parsed := m.(map[string]any)
		if parsed["role"] == "tool" && parsed["tool_call_id"] != want {
			t.Errorf("the result's id = %v, want the truncated %q", parsed["tool_call_id"], want)
		}
	}
}

func TestAnAnsweredCallStillGrowsASyntheticResultOnceItsIDIsNormalized(t *testing.T) {
	model := loopbackModel()
	model.BaseURL = "https://api.mistral.ai"
	messages := []Message{
		{Assistant: &AssistantContent{API: "openai-completions", Provider: "local", Model: "local-model", StopReason: "tool_use", Parts: []ContentPart{
			{ToolCall: &ToolCall{ID: "call_original", Name: "bash", Arguments: "{}"}},
		}}},
		{ToolResult: &ToolResult{ToolCallID: "call_original", Parts: []ContentPart{{Text: &TextPart{Text: "ok"}}}}},
		{User: &UserContent{Text: "next", HasText: true}},
	}
	out := PreTransform(messages, TransformConfig{
		TargetAPI: "openai-completions", TargetProvider: "local", TargetModelID: "local-model",
		MistralToolIDs: true, InsertSyntheticResult: true,
	})
	results := 0
	for _, m := range out {
		if m.ToolResult != nil {
			results++
		}
	}
	if results != 2 {
		t.Errorf("got %d tool results, want 2: zig keys the answered set by the original id and the pending set by the normalized one, so an answered call still grows a synthetic result", results)
	}
}

func TestAnAnsweredCallWithAnUnchangedIDGrowsNoSyntheticResult(t *testing.T) {
	messages := []Message{
		{Assistant: &AssistantContent{API: "openai-completions", Provider: "local", Model: "local-model", StopReason: "tool_use", Parts: []ContentPart{
			{ToolCall: &ToolCall{ID: "call_1", Name: "bash", Arguments: "{}"}},
		}}},
		{ToolResult: &ToolResult{ToolCallID: "call_1", Parts: []ContentPart{{Text: &TextPart{Text: "ok"}}}}},
		{User: &UserContent{Text: "next", HasText: true}},
	}
	out := PreTransform(messages, TransformConfig{
		TargetAPI: "openai-completions", TargetProvider: "local", TargetModelID: "local-model",
		InsertSyntheticResult: true,
	})
	results := 0
	for _, m := range out {
		if m.ToolResult != nil {
			results++
		}
	}
	if results != 1 {
		t.Errorf("got %d tool results, want 1: an id that normalization leaves alone lines the two sets up", results)
	}
}

func TestTheCostIsCalculatedFromTheModelsOwnRates(t *testing.T) {
	model := streamModel()
	model.Cost = Cost{Input: 2, Output: 8, CacheRead: 0.5, CacheWrite: 1}
	events := runStream(t, model,
		sseFrame(`{"usage":{"prompt_tokens":1000000,"completion_tokens":500000}}`),
	)
	done := findEvent(t, events, EventDone)
	cost := done.Message.Usage.Cost
	if cost.Input != 2 || cost.Output != 4 {
		t.Errorf("cost = %+v, want 2 in and 4 out at a million and a half million tokens", cost)
	}
	if cost.Total != 6 {
		t.Errorf("total = %v, want the four parts summed", cost.Total)
	}
}

func TestTheUnknownCapsKeepZigsFunctionCallingDefault(t *testing.T) {
	caps := DetectCapabilities("", false)
	if !caps.FunctionCalling {
		t.Error("zig's ProviderCapabilities defaults function_calling to true, so even the empty caps struct reports it")
	}
	if caps.Vision {
		t.Error("vision is not a default, so the empty caps struct leaves it false")
	}
}

func TestTheOAuthBranchCanonicalizesAToolNameAgainstTheTools(t *testing.T) {
	messages := []Message{
		{Assistant: &AssistantContent{API: "openai-completions", Provider: "local", Model: "local-model", StopReason: "tool_use", Parts: []ContentPart{
			{ToolCall: &ToolCall{ID: "c1", Name: "bash", Arguments: "{}"}},
		}}},
		{ToolResult: &ToolResult{ToolCallID: "c1", ToolName: "bash", Parts: []ContentPart{{Text: &TextPart{Text: "ok"}}}}},
	}
	tools := []Tool{{Name: "Bash"}}
	oauth := PreTransform(messages, TransformConfig{
		TargetAPI: "openai-completions", TargetProvider: "local", TargetModelID: "local-model",
		IsOAuth: true, Tools: tools,
	})
	if got := oauth[0].Assistant.Parts[0].ToolCall.Name; got != "Bash" {
		t.Errorf("with oauth the name = %q, want the tool's own spelling", got)
	}
	plain := PreTransform(messages, TransformConfig{
		TargetAPI: "openai-completions", TargetProvider: "local", TargetModelID: "local-model",
		Tools: tools,
	})
	if got := plain[0].Assistant.Parts[0].ToolCall.Name; got != "bash" {
		t.Errorf("without oauth the name = %q, want the caller's own spelling: the rewrite is behind the flag", got)
	}
}

func TestTheReasoningDetailIsEscapedRatherThanSpliced(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.encrypted","id":"c1","data":"a\"b\\c"}]}}]}`),
	)
	end := findEvent(t, events, EventToolCallEnd)
	var detail map[string]any
	if err := json.Unmarshal([]byte(end.ToolCall.ThoughtSig), &detail); err != nil {
		t.Fatalf("the detail is not json: %v\n%s", err, end.ToolCall.ThoughtSig)
	}
	if detail["data"] != `a"b\c` {
		t.Errorf("the data = %v, want it round-tripped", detail["data"])
	}
}

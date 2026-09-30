package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("the body is not json: %v\n%s", err, body)
	}
	return out
}

func messages(t *testing.T, body []byte) []any {
	t.Helper()
	list, ok := decode(t, body)["messages"].([]any)
	if !ok {
		t.Fatalf("the body carries no messages array\n%s", body)
	}
	return list
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func openAIModel() Model {
	return Model{ID: "gpt-4o", API: "openai-completions", Provider: "openai", BaseURL: "https://api.openai.com", HasBaseURL: true, MaxTokens: 100, HasCompat: true}
}

func loopbackModel() Model {
	return Model{ID: "local-model", API: "openai-completions", Provider: "local", BaseURL: "http://127.0.0.1:8080/v1", HasBaseURL: true, MaxTokens: 100, HasCompat: true}
}

func TestTheBodyWritesItsMembersInZigsOrder(t *testing.T) {
	body := BuildRequestBody(openAIModel(), Context{Tools: []Tool{{Name: "t", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}}}, StreamOptions{HasToolChoice: true, ToolChoice: ToolChoice{Mode: "auto"}})
	want := []string{"model", "messages", "stream", "stream_options", "max_completion_tokens", "tools", "tool_choice", "store"}
	parsed := decode(t, body)
	for _, key := range want {
		if _, ok := parsed[key]; !ok {
			t.Errorf("the body is missing %q, want the members %v", key, want)
		}
	}
	at := func(name string) int {
		return strings.Index(string(body), `"`+name+`":`)
	}
	for i := 1; i < len(want); i++ {
		if at(want[i-1]) < 0 || at(want[i]) < 0 {
			t.Fatalf("the body is missing %q or %q\n%s", want[i-1], want[i], body)
		}
		if at(want[i-1]) > at(want[i]) {
			t.Errorf("%q appears after %q, want the zig order", want[i-1], want[i])
		}
	}
}

func TestStreamIsAlwaysTrueAndUsageDependsOnTheMerge(t *testing.T) {
	withUsage := decode(t, BuildRequestBody(openAIModel(), Context{}, StreamOptions{}))
	if withUsage["stream"] != true {
		t.Error("stream is always true")
	}
	if _, ok := withUsage["stream_options"]; !ok {
		t.Error("an openai host supports usage in streaming, so stream_options is written")
	}
	off := openAIModel()
	off.Compat = CompatOptions{SupportsUsageInStreaming: boolPtr(false)}
	if _, ok := decode(t, BuildRequestBody(off, Context{}, StreamOptions{}))["stream_options"]; ok {
		t.Error("the model asked for no usage in streaming, so stream_options is absent")
	}
}

func TestTheMaxTokensMemberIsNamedByTheMerge(t *testing.T) {
	openai := decode(t, BuildRequestBody(openAIModel(), Context{}, StreamOptions{}))
	if openai["max_completion_tokens"] != float64(100) {
		t.Errorf("an openai host = %v, want max_completion_tokens 100 from the model", openai)
	}
	if _, ok := openai["max_tokens"]; ok {
		t.Error("the completion spelling is the only one written")
	}
	loopback := decode(t, BuildRequestBody(loopbackModel(), Context{}, StreamOptions{}))
	if loopback["max_tokens"] != float64(100) {
		t.Errorf("a loopback = %v, want max_tokens 100", loopback)
	}
	override := decode(t, BuildRequestBody(loopbackModel(), Context{}, StreamOptions{MaxTokens: 7, HasMaxTokens: true}))
	if override["max_tokens"] != float64(7) {
		t.Errorf("the caller's own max tokens = %v, want 7 to win over the model's 100", override["max_tokens"])
	}
}

func TestTemperatureIsWrittenUnlessKimiRefusesIt(t *testing.T) {
	got := decode(t, BuildRequestBody(loopbackModel(), Context{}, StreamOptions{Temperature: 0.2, HasTemperature: true}))
	if got["temperature"] != 0.2 {
		t.Errorf("a loopback = %v, want the temperature written", got)
	}
	kimi := loopbackModel()
	kimi.Provider = "kimi"
	got = decode(t, BuildRequestBody(kimi, Context{}, StreamOptions{Temperature: 0.2, HasTemperature: true}))
	if _, ok := got["temperature"]; ok {
		t.Error("kimi refuses a temperature that is not exactly 1.0")
	}
	got = decode(t, BuildRequestBody(kimi, Context{}, StreamOptions{Temperature: 1.0, HasTemperature: true}))
	if got["temperature"] != 1.0 {
		t.Errorf("kimi at exactly 1.0 = %v, want it written", got)
	}
}

func TestReasoningEffortNeedsBothTheModelAndTheMerge(t *testing.T) {
	reasoning := openAIModel()
	reasoning.Reasoning = true
	got := decode(t, BuildRequestBody(reasoning, Context{}, StreamOptions{ReasoningEffort: "high"}))
	if got["reasoning_effort"] != "high" {
		t.Errorf("a reasoning openai model = %v, want the effort written", got)
	}
	plain := openAIModel()
	got = decode(t, BuildRequestBody(plain, Context{}, StreamOptions{ReasoningEffort: "high"}))
	if _, ok := got["reasoning_effort"]; ok {
		t.Error("a model that is not a reasoning model writes no effort")
	}
	loopReasoning := loopbackModel()
	loopReasoning.Reasoning = true
	got = decode(t, BuildRequestBody(loopReasoning, Context{}, StreamOptions{ReasoningEffort: "high"}))
	if _, ok := got["reasoning_effort"]; ok {
		t.Error("the merge must support the effort, and a loopback does not")
	}
}

func TestStoreIsWrittenOnlyWhenTheMergeSupportsIt(t *testing.T) {
	got := decode(t, BuildRequestBody(openAIModel(), Context{}, StreamOptions{}))
	if got["store"] != false {
		t.Errorf("an openai host = %v, want store false", got)
	}
	got = decode(t, BuildRequestBody(loopbackModel(), Context{}, StreamOptions{}))
	if _, ok := got["store"]; ok {
		t.Errorf("a loopback = %v, want no store member", got)
	}
}

func TestEachToolCarriesItsSchemaAndStrictOnlyWhenTheMergeAsks(t *testing.T) {
	tools := []Tool{{Name: "read", Description: "read a file", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}}
	openai := decode(t, BuildRequestBody(openAIModel(), Context{Tools: tools}, StreamOptions{}))
	first := openai["tools"].([]any)[0].(map[string]any)
	if first["type"] != "function" {
		t.Errorf("a tool = %v, want type function", first)
	}
	fn := first["function"].(map[string]any)
	if fn["name"] != "read" || fn["description"] != "read a file" {
		t.Errorf("the tool function = %v", fn)
	}
	if fn["strict"] != true {
		t.Errorf("an openai host supports strict mode, so the tool = %v, want strict true", fn)
	}
	if _, ok := fn["parameters"].(map[string]any); !ok {
		t.Errorf("the schema is written raw as an object, got %v", fn["parameters"])
	}
	loopback := decode(t, BuildRequestBody(loopbackModel(), Context{Tools: tools}, StreamOptions{}))
	loopFn := loopback["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if _, ok := loopFn["strict"]; ok {
		t.Errorf("a loopback does not support strict mode, got %v", loopFn)
	}
}

func TestToolChoiceIsSpelledThreeWaysAndOnlyWhenThereAreTools(t *testing.T) {
	tools := []Tool{{Name: "read"}}
	for _, mode := range []string{ToolChoiceAuto, ToolChoiceNone, ToolChoiceRequired} {
		body := BuildRequestBody(openAIModel(), Context{Tools: tools}, StreamOptions{HasToolChoice: true, ToolChoice: ToolChoice{Mode: mode}})
		if got := decode(t, body)["tool_choice"]; got != mode {
			t.Errorf("tool choice = %v, want %q as a bare string", got, mode)
		}
	}
	named := decode(t, BuildRequestBody(openAIModel(), Context{Tools: tools}, StreamOptions{HasToolChoice: true, ToolChoice: ToolChoice{Mode: "function", Function: "read"}}))
	choice := named["tool_choice"].(map[string]any)
	if choice["type"] != "function" {
		t.Errorf("a named tool choice = %v, want type function", choice)
	}
	if choice["function"].(map[string]any)["name"] != "read" {
		t.Errorf("the named function = %v, want the name", choice["function"])
	}
	none := decode(t, BuildRequestBody(openAIModel(), Context{}, StreamOptions{HasToolChoice: true, ToolChoice: ToolChoice{Mode: "auto"}}))
	if _, ok := none["tool_choice"]; ok {
		t.Error("with no tools the tool choice is not written at all: it is nested inside the tools branch")
	}
}

func TestTheSystemPromptBecomesADeveloperRoleOnlyForAReasoningModel(t *testing.T) {
	ctx := Context{HasSystem: true, SystemPrompt: "be terse"}
	plain := decode(t, BuildRequestBody(openAIModel(), ctx, StreamOptions{}))
	if first := plain["messages"].([]any)[0].(map[string]any); first["role"] != "system" {
		t.Errorf("a plain openai model = %v, want the system role", first)
	}
	reasoning := openAIModel()
	reasoning.Reasoning = true
	dev := decode(t, BuildRequestBody(reasoning, ctx, StreamOptions{}))
	if first := dev["messages"].([]any)[0].(map[string]any); first["role"] != "developer" {
		t.Errorf("a reasoning openai model = %v, want the developer role", first)
	}
	loopReasoning := loopbackModel()
	loopReasoning.Reasoning = true
	loop := decode(t, BuildRequestBody(loopReasoning, ctx, StreamOptions{}))
	if first := loop["messages"].([]any)[0].(map[string]any); first["role"] != "system" {
		t.Errorf("a reasoning loopback model = %v, want the system role: the merge does not support the developer role", first)
	}
}

func TestASingleTextUserMessageIsAPlainStringUnlessItIsTheLastOne(t *testing.T) {
	ctx := Context{Messages: []Message{{User: &UserContent{Text: "hello", HasText: true}}}}
	got := messages(t, BuildRequestBody(loopbackModel(), ctx, StreamOptions{}))
	if content := got[0].(map[string]any)["content"]; content != "hello" {
		t.Errorf("content = %v, want the plain string", content)
	}
}

func TestTheThinkingMemberIsNamedReasoningContentOrTheSignatureThatCameBack(t *testing.T) {
	thinking := func(sig string) []ContentPart {
		return []ContentPart{{Text: &TextPart{Text: "the answer"}}, {Thinking: &ThinkingPart{Thinking: "step one", Signature: sig}}}
	}
	ctx := func(sig string) Context {
		return Context{Messages: []Message{{Assistant: &AssistantContent{Parts: thinking(sig), API: "openai-completions", Provider: "local", Model: "local-model"}}}}
	}
	plain := messages(t, BuildRequestBody(loopbackModel(), ctx(""), StreamOptions{}))[0].(map[string]any)
	if plain["reasoning_content"] != "step one" {
		t.Errorf("without a signature = %v, want the thinking under reasoning_content", plain)
	}
	signed := messages(t, BuildRequestBody(loopbackModel(), ctx("sig-abc"), StreamOptions{}))[0].(map[string]any)
	if signed["sig-abc"] != "step one" {
		t.Errorf("with a signature = %v, want the member named for the signature", signed)
	}
	if _, ok := signed["reasoning_content"]; ok {
		t.Error("the signature replaces the name, it does not accompany it")
	}
}

func TestThinkingBlocksAreJoinedWithANewlineAndBlankOnesSkipped(t *testing.T) {
	parts := []ContentPart{
		{Text: &TextPart{Text: "answer"}},
		{Thinking: &ThinkingPart{Thinking: "first"}},
		{Thinking: &ThinkingPart{Thinking: "   "}},
		{Thinking: &ThinkingPart{Thinking: "second"}},
	}
	ctx := Context{Messages: []Message{{Assistant: &AssistantContent{Parts: parts, API: "openai-completions", Provider: "local", Model: "local-model"}}}}
	got := messages(t, BuildRequestBody(loopbackModel(), ctx, StreamOptions{}))[0].(map[string]any)
	if got["reasoning_content"] != "first\nsecond" {
		t.Errorf("thinking = %q, want the two visible blocks joined and the blank one skipped", got["reasoning_content"])
	}
}

func TestAnAssistantWithNothingInItIsDropped(t *testing.T) {
	cases := map[string]AssistantContent{
		"no parts at all":     {Parts: nil},
		"whitespace text":     {Parts: []ContentPart{{Text: &TextPart{Text: "  \t "}}}},
		"whitespace thinking": {Parts: []ContentPart{{Thinking: &ThinkingPart{Thinking: "\n"}}}},
	}
	for name, a := range cases {
		ctx := Context{Messages: []Message{{Assistant: &a}}}
		if got := messages(t, BuildRequestBody(loopbackModel(), ctx, StreamOptions{})); len(got) != 0 {
			t.Errorf("%s: got %d messages, want none", name, len(got))
		}
	}
}

func TestAnAbortedOrErroredAssistantIsSkipped(t *testing.T) {
	parts := []ContentPart{{Text: &TextPart{Text: "partial"}}}
	for _, reason := range []StopReason{StopAborted, StopError} {
		ctx := Context{Messages: []Message{{Assistant: &AssistantContent{Parts: parts, StopReason: reason}}}}
		if got := messages(t, BuildRequestBody(loopbackModel(), ctx, StreamOptions{})); len(got) != 0 {
			t.Errorf("stop reason %q: got %d messages, want the assistant skipped", reason, len(got))
		}
	}
	kept := Context{Messages: []Message{{Assistant: &AssistantContent{Parts: parts, StopReason: "end_turn"}}}}
	if got := messages(t, BuildRequestBody(loopbackModel(), kept, StreamOptions{})); len(got) != 1 {
		t.Errorf("a normal stop reason: got %d messages, want the assistant kept", len(got))
	}
}

func toolResultIDs(t *testing.T, ctx Context) []string {
	t.Helper()
	var ids []string
	for _, m := range messages(t, BuildRequestBody(loopbackModel(), ctx, StreamOptions{})) {
		if m.(map[string]any)["role"] == "tool" {
			ids = append(ids, m.(map[string]any)["tool_call_id"].(string))
		}
	}
	return ids
}

func TestAnOrphanedToolResultIsDroppedWhereverItFallsInARun(t *testing.T) {
	caller := Context{Messages: []Message{
		{Assistant: &AssistantContent{Parts: []ContentPart{
			{ToolCall: &ToolCall{ID: "call-1", Name: "read", Arguments: "{}"}},
			{ToolCall: &ToolCall{ID: "call-2", Name: "read", Arguments: "{}"}},
		}}},
		{ToolResult: &ToolResult{ToolCallID: "call-1", Parts: []ContentPart{{Text: &TextPart{Text: "contents"}}}}},
		{ToolResult: &ToolResult{ToolCallID: "call-9", Parts: []ContentPart{{Text: &TextPart{Text: "orphan"}}}}},
		{ToolResult: &ToolResult{ToolCallID: "call-2", Parts: []ContentPart{{Text: &TextPart{Text: "more"}}}}},
	}}
	got := toolResultIDs(t, caller)
	if len(got) != 2 || got[0] != "call-1" || got[1] != "call-2" {
		t.Errorf("got %v, want [call-1 call-2]: the run loop writes one tool message per result, so an orphan in the middle is just a message that is not written", got)
	}
}

func TestARunOfOnlyOrphansWritesNoToolMessage(t *testing.T) {
	caller := Context{Messages: []Message{
		{Assistant: &AssistantContent{Parts: []ContentPart{
			{ToolCall: &ToolCall{ID: "call-1", Name: "read", Arguments: "{}"}},
		}}},
		{ToolResult: &ToolResult{ToolCallID: "call-1", Parts: []ContentPart{{Text: &TextPart{Text: "answered"}}}}},
		{User: &UserContent{Text: "carry on", HasText: true}},
		{ToolResult: &ToolResult{ToolCallID: "call-8", Parts: []ContentPart{{Text: &TextPart{Text: "orphan"}}}}},
		{ToolResult: &ToolResult{ToolCallID: "call-9", Parts: []ContentPart{{Text: &TextPart{Text: "orphan"}}}}},
	}}
	got := toolResultIDs(t, caller)
	if len(got) != 1 || got[0] != "call-1" {
		t.Errorf("got %v, want only the answered one: the second run is entirely orphans, and the call it could have been standing in for is already answered so no synthetic result is grown for it", got)
	}
}

func TestARunWithNoToolCallAnywhereKeepsEveryResult(t *testing.T) {
	none := Context{Messages: []Message{
		{ToolResult: &ToolResult{ToolCallID: "call-1", Parts: []ContentPart{{Text: &TextPart{Text: "x"}}}}},
	}}
	if got := toolResultIDs(t, none); len(got) != 1 {
		t.Errorf("got %v, want the one result kept: with no tool call to match against the guard stands down", got)
	}
}

func TestALeadingOrphanedToolResultIsDropped(t *testing.T) {
	caller := Context{Messages: []Message{
		{Assistant: &AssistantContent{Parts: []ContentPart{{ToolCall: &ToolCall{ID: "call-1", Name: "read", Arguments: "{}"}}}}},
		{ToolResult: &ToolResult{ToolCallID: "call-9", Parts: []ContentPart{{Text: &TextPart{Text: "orphan"}}}}},
	}}
	got := messages(t, BuildRequestBody(loopbackModel(), caller, StreamOptions{}))
	for _, m := range got {
		if m.(map[string]any)["tool_call_id"] == "call-9" {
			t.Error("a tool result that opens a run and whose call is gone is dropped")
		}
	}
}

func TestAToolResultCarriesItsNameOnlyWhenTheMergeAsks(t *testing.T) {
	ctx := Context{Messages: []Message{
		{Assistant: &AssistantContent{Parts: []ContentPart{{ToolCall: &ToolCall{ID: "c1", Name: "read", Arguments: "{}"}}}}},
		{ToolResult: &ToolResult{ToolCallID: "c1", ToolName: "read", Parts: []ContentPart{{Text: &TextPart{Text: "out"}}}}},
	}}
	plain := messages(t, BuildRequestBody(loopbackModel(), ctx, StreamOptions{}))
	for _, m := range plain {
		if m.(map[string]any)["role"] == "tool" {
			if _, ok := m.(map[string]any)["name"]; ok {
				t.Errorf("a loopback does not require the name, got %v", m)
			}
		}
	}
	model := loopbackModel()
	model.Compat = CompatOptions{RequiresToolResultName: boolPtr(true)}
	named := messages(t, BuildRequestBody(model, ctx, StreamOptions{}))
	for _, m := range named {
		if m.(map[string]any)["role"] == "tool" {
			if m.(map[string]any)["name"] != "read" {
				t.Errorf("the model asked for the name, got %v", m)
			}
		}
	}
}

func TestAToolResultWithOnlyImagesSaysSo(t *testing.T) {
	ctx := Context{Messages: []Message{
		{Assistant: &AssistantContent{Parts: []ContentPart{{ToolCall: &ToolCall{ID: "c1", Name: "shot", Arguments: "{}"}}}}},
		{ToolResult: &ToolResult{ToolCallID: "c1", Parts: []ContentPart{{Image: &ImagePart{Data: "aGk=", MediaType: "image/png"}}}}},
	}}
	got := messages(t, BuildRequestBody(loopbackModel(), ctx, StreamOptions{}))
	sawPlaceholder, sawUser := false, false
	for _, m := range got {
		parsed := m.(map[string]any)
		if parsed["role"] == "tool" && parsed["content"] == "(see attached image)" {
			sawPlaceholder = true
		}
		if parsed["role"] == "user" {
			sawUser = true
		}
	}
	if !sawPlaceholder {
		t.Error("a result with no text but an image carries the placeholder")
	}
	if !sawUser {
		t.Error("the images follow in a user message")
	}
}

func TestAnEmptyAssistantIsWrittenAroundToolResultsOnlyWhenAsked(t *testing.T) {
	ctx := Context{Messages: []Message{
		{Assistant: &AssistantContent{Parts: []ContentPart{
			{ToolCall: &ToolCall{ID: "c1", Name: "read", Arguments: "{}"}},
			{ToolCall: &ToolCall{ID: "c2", Name: "read", Arguments: "{}"}},
		}}},
		{ToolResult: &ToolResult{ToolCallID: "c1", Parts: []ContentPart{{Text: &TextPart{Text: "a"}}}}},
		{ToolResult: &ToolResult{ToolCallID: "c2", Parts: []ContentPart{{Text: &TextPart{Text: "b"}}}}},
	}}
	plain := messages(t, BuildRequestBody(loopbackModel(), ctx, StreamOptions{}))
	for _, m := range plain {
		if m.(map[string]any)["role"] == "assistant" && m.(map[string]any)["content"] == "" {
			t.Error("a loopback does not ask for the empty assistant, got one")
		}
	}
	model := loopbackModel()
	model.Compat = CompatOptions{RequiresAssistantAfterToolResult: boolPtr(true)}
	padded := messages(t, BuildRequestBody(model, ctx, StreamOptions{}))
	empties := 0
	for _, m := range padded {
		if m.(map[string]any)["role"] == "assistant" && m.(map[string]any)["content"] == "" {
			empties++
		}
	}
	if empties == 0 {
		t.Error("the model asked for an empty assistant between tool results, got none")
	}
}

func TestARequestToolCallCarriesItsIdTypeAndFunction(t *testing.T) {
	ctx := Context{Messages: []Message{{Assistant: &AssistantContent{Parts: []ContentPart{
		{Text: &TextPart{Text: "looking"}},
		{ToolCall: &ToolCall{ID: "call-7", Name: "read", Arguments: `{"path":"a"}`}},
	}}}}}
	got := messages(t, BuildRequestBody(loopbackModel(), ctx, StreamOptions{}))[0].(map[string]any)
	calls := got["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(calls))
	}
	call := calls[0].(map[string]any)
	if call["id"] != "call-7" || call["type"] != "function" {
		t.Errorf("a tool call = %v, want the id and the function type", call)
	}
	fn := call["function"].(map[string]any)
	if fn["name"] != "read" || fn["arguments"] != `{"path":"a"}` {
		t.Errorf("the function = %v, want the name and the arguments verbatim", fn)
	}
}

func TestReasoningDetailsRideAlongWhenAToolCallCarriesOne(t *testing.T) {
	ctx := Context{Messages: []Message{{Assistant: &AssistantContent{API: "openai-completions", Provider: "local", Model: "local-model", Parts: []ContentPart{
		{ToolCall: &ToolCall{ID: "c1", Name: "read", Arguments: "{}", ThoughtSig: `{"k":1}`, HasThought: true}},
	}}}}}
	withIt := messages(t, BuildRequestBody(loopbackModel(), ctx, StreamOptions{}))[0].(map[string]any)
	if _, ok := withIt["reasoning_details"]; !ok {
		t.Errorf("a tool call with a thought signature = %v, want reasoning_details", withIt)
	}
	plain := Context{Messages: []Message{{Assistant: &AssistantContent{Parts: []ContentPart{
		{ToolCall: &ToolCall{ID: "c1", Name: "read", Arguments: "{}"}},
	}}}}}
	if _, ok := messages(t, BuildRequestBody(loopbackModel(), plain, StreamOptions{}))[0].(map[string]any)["reasoning_details"]; ok {
		t.Error("without a thought signature there are no reasoning details")
	}
}

func TestTheLastUserMessageCarriesCacheControlUnderOpenRouterAnthropic(t *testing.T) {
	model := loopbackModel()
	model.Provider = "openrouter"
	model.BaseURL = "https://openrouter.ai/api"
	model.ID = "anthropic/claude"
	ctx := Context{Messages: []Message{
		{User: &UserContent{Text: "first", HasText: true}},
		{User: &UserContent{Text: "second", HasText: true}},
	}}
	got := messages(t, BuildRequestBody(model, ctx, StreamOptions{}))
	first := got[0].(map[string]any)
	if _, isString := first["content"].(string); !isString {
		t.Error("only the last user message is an array")
	}
	second := got[1].(map[string]any)
	parts := second["content"].([]any)
	last := parts[len(parts)-1].(map[string]any)
	if _, ok := last["cache_control"]; !ok {
		t.Errorf("the last user message's last part = %v, want cache_control", last)
	}

	notAnthropic := model
	notAnthropic.ID = "openai/gpt"
	if _, isString := messages(t, BuildRequestBody(notAnthropic, ctx, StreamOptions{}))[1].(map[string]any)["content"].(string); !isString {
		t.Error("a non anthropic openrouter model writes plain content")
	}
	notOpenRouter := loopbackModel()
	if _, isString := messages(t, BuildRequestBody(notOpenRouter, ctx, StreamOptions{}))[1].(map[string]any)["content"].(string); !isString {
		t.Error("cache control is the openrouter anthropic rule alone")
	}
}

func TestAUserMessageWithImagesIsAlwaysAnArray(t *testing.T) {
	ctx := Context{Messages: []Message{{User: &UserContent{UseParts: true, Parts: []ContentPart{
		{Text: &TextPart{Text: "look"}},
		{Image: &ImagePart{Data: "aGk=", MediaType: "image/png"}},
	}}}}}
	got := messages(t, BuildRequestBody(loopbackModel(), ctx, StreamOptions{}))[0].(map[string]any)
	parts := got["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want the text and the image", len(parts))
	}
	image := parts[1].(map[string]any)
	if image["type"] != "image_url" {
		t.Errorf("an image part = %v, want image_url", image)
	}
	if image["image_url"].(map[string]any)["url"] != "data:image/png;base64,aGk=" {
		t.Errorf("the image url = %v", image["image_url"])
	}
}

func TestTextOnlyPartsAreJoinedWithANewlineWhenThereAreNoImages(t *testing.T) {
	ctx := Context{Messages: []Message{{User: &UserContent{UseParts: true, Parts: []ContentPart{
		{Text: &TextPart{Text: "one"}},
		{Text: &TextPart{Text: "two"}},
	}}}}}
	got := messages(t, BuildRequestBody(loopbackModel(), ctx, StreamOptions{}))[0].(map[string]any)
	if got["content"] != "one\ntwo" {
		t.Errorf("content = %v, want the parts joined into one string", got["content"])
	}
}

func TestADeepSeekAssistantPassesItsReasoningBackAsReasoningContent(t *testing.T) {
	model := loopbackModel()
	model.BaseURL = "https://api.deepseek.com"
	ctx := Context{Messages: []Message{{Assistant: &AssistantContent{API: "openai-completions", Provider: "local", Model: "local-model", Parts: []ContentPart{
		{Thinking: &ThinkingPart{Thinking: "step"}},
		{Text: &TextPart{Text: "answer"}},
	}}}}}
	got := messages(t, BuildRequestBody(model, ctx, StreamOptions{}))[0].(map[string]any)
	if got["reasoning_content"] != "step" {
		t.Errorf("reasoning_content = %v, want the thinking passed back as reasoning", got["reasoning_content"])
	}
	parts := got["content"].([]any)
	if len(parts) != 1 || parts[0].(map[string]any)["text"] != "answer" {
		t.Errorf("content = %v, want only the answer", parts)
	}
}

func TestADeepSeekRequestSendsOneOfItsThreeEfforts(t *testing.T) {
	model := loopbackModel()
	model.BaseURL = "https://api.deepseek.com"
	model.Reasoning = true
	for level, sent := range map[string]string{"minimal": "low", "low": "low", "medium": "high", "high": "high", "xhigh": "max"} {
		got := decode(t, BuildRequestBody(model, Context{}, StreamOptions{ReasoningEffort: level}))
		if got["reasoning_effort"] != sent {
			t.Errorf("level %q sent %v, want %q", level, got["reasoning_effort"], sent)
		}
	}
}

func TestAnAssistantWithThinkingAsTextWritesItInsideTheContentArray(t *testing.T) {
	model := loopbackModel()
	model.Compat = CompatOptions{RequiresThinkingAsText: boolPtr(true)}
	ctx := Context{Messages: []Message{{Assistant: &AssistantContent{API: "openai-completions", Provider: "local", Model: "local-model", Parts: []ContentPart{
		{Thinking: &ThinkingPart{Thinking: "step"}},
		{Text: &TextPart{Text: "answer"}},
	}}}}}
	got := messages(t, BuildRequestBody(model, ctx, StreamOptions{}))[0].(map[string]any)
	if _, ok := got["reasoning_content"]; ok {
		t.Errorf("the model asks for its thinking as text, so there is no separate member: %v", got)
	}
	parts := got["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("got %d content parts, want the thinking then the text", len(parts))
	}
	if parts[0].(map[string]any)["text"] != "step" {
		t.Errorf("the thinking is written first, got %v", parts[0])
	}
}

func TestAnAssistantWithNothingButToolCallsHasANullContent(t *testing.T) {
	ctx := Context{Messages: []Message{{Assistant: &AssistantContent{Parts: []ContentPart{
		{ToolCall: &ToolCall{ID: "c1", Name: "read", Arguments: "{}"}},
	}}}}}
	got := messages(t, BuildRequestBody(loopbackModel(), ctx, StreamOptions{}))[0].(map[string]any)
	if got["content"] != nil {
		t.Errorf("content = %v, want null when there is neither text nor thinking", got["content"])
	}
}

func TestCopilotJoinsAssistantTextWithNothingBetween(t *testing.T) {
	model := loopbackModel()
	model.Provider = "github-copilot"
	ctx := Context{Messages: []Message{{Assistant: &AssistantContent{Parts: []ContentPart{
		{Text: &TextPart{Text: "one"}},
		{Text: &TextPart{Text: "two"}},
	}}}}}
	got := messages(t, BuildRequestBody(model, ctx, StreamOptions{}))[0].(map[string]any)
	if got["content"] != "onetwo" {
		t.Errorf("content = %v, want copilot's bare concatenation", got["content"])
	}
}

func TestTheBodyIsAlwaysValidJSONEvenWithNoContext(t *testing.T) {
	decode(t, BuildRequestBody(loopbackModel(), Context{}, StreamOptions{}))
	decode(t, BuildRequestBody(openAIModel(), Context{}, StreamOptions{}))
}

func TestAnEmptyToolSchemaBecomesAnEmptyObject(t *testing.T) {
	body := BuildRequestBody(loopbackModel(), Context{Tools: []Tool{{Name: "t"}}}, StreamOptions{})
	parsed := decode(t, body)["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if _, ok := parsed["parameters"].(map[string]any); !ok {
		t.Errorf("parameters = %v, want an empty object rather than nothing", parsed["parameters"])
	}
}

func TestDeepSeekEffortMappingFollowsIdentityBehindANonVendorProxy(t *testing.T) {
	proxied := openAIModel()
	proxied.Provider = "deepseek"
	proxied.BaseURL = "https://gateway.corp/v1"
	proxied.HasBaseURL = true
	proxied.Reasoning = true
	body := decode(t, BuildRequestBody(proxied, Context{}, StreamOptions{ReasoningEffort: "minimal"}))
	if got := body["reasoning_effort"]; got != "low" {
		t.Fatalf("reasoning_effort = %v, want \"low\": a deepseek model behind a proxy must still get the deepseek mapping", got)
	}

}

func TestDeepSeekEffortMappingMatchesTheDocumentedTable(t *testing.T) {
	for _, want := range []struct {
		requested string
		actual    string
	}{
		{"minimal", "low"},
		{"low", "low"},
		{"medium", "high"},
		{"high", "high"},
		{"xhigh", "high"},
		{"max", "max"},
		{"ultra", "max"},
	} {
		if got := deepSeekEffort(want.requested); got != want.actual {
			t.Errorf("deepSeekEffort(%q) = %q, want %q", want.requested, got, want.actual)
		}
	}
}

func TestDeepSeekPublicEffortOptionsMapToTheWireValue(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		requested string
		want      any
	}{
		{"documented low stays low", "low", "low"},
		{"documented medium becomes high", "medium", "high"},
		{"documented high stays high", "high", "high"},
		{"documented xhigh becomes high not max", "xhigh", "high"},
		{"documented max stays max", "max", "max"},
		{"documented ultra becomes max", "ultra", "max"},
		{"an absent effort writes no field", "", nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			model := openAIModel()
			model.Provider = "deepseek"
			model.BaseURL = "https://gateway.corp/v1"
			model.HasBaseURL = true
			model.Reasoning = true
			body := decode(t, BuildRequestBody(model, Context{}, StreamOptions{ReasoningEffort: testCase.requested}))
			got, present := body["reasoning_effort"]
			if testCase.want == nil {
				if present {
					t.Fatalf("an absent effort wrote reasoning_effort=%v, want the field omitted", got)
				}
				return
			}
			if !present || got != testCase.want {
				t.Fatalf("reasoning_effort = %v (present=%v), want %v", got, present, testCase.want)
			}
		})
	}
}

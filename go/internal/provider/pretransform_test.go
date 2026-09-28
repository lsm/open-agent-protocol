package provider

import (
	"encoding/json"
	"testing"
)

func TestAToolIDIsStrippedAtThePipeAndTruncatedAndSanitized(t *testing.T) {
	if got := NormalizeToolID("call_123|very_long_id_suffix", 40); got != "call_123" {
		t.Errorf("got %q, want the pipe suffix dropped", got)
	}
	if got := NormalizeToolID("call+123/abc=def", 0); got != "call_123_abc_def" {
		t.Errorf("got %q, want every non-alphanumeric byte replaced", got)
	}
	if got := NormalizeToolID("call_abc123", 40); got != "call_abc123" {
		t.Errorf("got %q, want a clean short id untouched", got)
	}
	long := NormalizeToolID("abcdefghijklmnopqrstuvwxyz1234567890ABCDEFGHIJ", 40)
	if len(long) != 40 {
		t.Errorf("got %d chars, want the 40 byte limit", len(long))
	}
}

func TestAMistralToolIDIsNineDeterministicCharacters(t *testing.T) {
	first := MistralToolID("call_123456")
	if len(first) != 9 {
		t.Errorf("got %d chars, want 9", len(first))
	}
	if second := MistralToolID("call_123456"); second != first {
		t.Errorf("got %q then %q, want the same id twice", first, second)
	}
	if other := MistralToolID("call_999999"); other == first {
		t.Error("two different ids must not hash to the same nine characters")
	}
}

func TestTheClaudeCodeNameMatchIsCaseInsensitive(t *testing.T) {
	tools := []Tool{{Name: "Bash"}, {Name: "Read"}}
	if got := fromClaudeCodeName("bash", tools); got != "Bash" {
		t.Errorf("got %q, want the tool's own spelling", got)
	}
	if got := fromClaudeCodeName("READ", tools); got != "Read" {
		t.Errorf("got %q, want the tool's own spelling", got)
	}
	if got := fromClaudeCodeName("unknown", tools); got != "unknown" {
		t.Errorf("got %q, want the name untouched when no tool matches", got)
	}
	if got := fromClaudeCodeName("bash", nil); got != "bash" {
		t.Errorf("got %q, want the name untouched with no tools", got)
	}
}

func transformFixture() []Message {
	return []Message{
		{Assistant: &AssistantContent{API: "openai-completions", Provider: "local", Model: "local-model", StopReason: "tool_use", Parts: []ContentPart{
			{ToolCall: &ToolCall{ID: "tc_1", Name: "bash", Arguments: "{}"}},
			{ToolCall: &ToolCall{ID: "tc_2", Name: "read", Arguments: "{}"}},
		}}},
		{ToolResult: &ToolResult{ToolCallID: "tc_1", ToolName: "bash", Parts: []ContentPart{{Text: &TextPart{Text: "ok"}}}}},
		{User: &UserContent{Text: "next", HasText: true}},
	}
}

func TestAnUnansweredToolCallGrowsASyntheticResult(t *testing.T) {
	out := PreTransform(transformFixture(), TransformConfig{TargetAPI: "openai-completions", TargetProvider: "local", TargetModelID: "local-model", InsertSyntheticResult: true})
	if len(out) != 4 {
		t.Fatalf("got %d messages, want the assistant, the answered result, the synthetic one and the user", len(out))
	}
	synthetic := out[2].ToolResult
	if synthetic == nil {
		t.Fatalf("message 2 = %+v, want a tool result", out[2])
	}
	if synthetic.ToolCallID != "tc_2" {
		t.Errorf("the synthetic result answers %q, want the unanswered tc_2", synthetic.ToolCallID)
	}
	if synthetic.Parts[0].Text.Text != "No result provided" {
		t.Errorf("the synthetic content = %q", synthetic.Parts[0].Text.Text)
	}
	if !synthetic.IsError {
		t.Error("a synthetic result is marked as an error")
	}
}

func TestASyntheticResultIsNotInsertedWhenTheCallerSaysNotTo(t *testing.T) {
	out := PreTransform(transformFixture(), TransformConfig{TargetAPI: "openai-completions", TargetProvider: "local", TargetModelID: "local-model"})
	if len(out) != 3 {
		t.Fatalf("got %d messages, want no synthetic one", len(out))
	}
}

func TestTheSyntheticResultIsFlushedBeforeTheNextTurn(t *testing.T) {
	messages := []Message{
		{Assistant: &AssistantContent{API: "openai-completions", Provider: "local", Model: "local-model", StopReason: "tool_use", Parts: []ContentPart{
			{ToolCall: &ToolCall{ID: "tc_1", Name: "bash", Arguments: "{}"}},
		}}},
		{Assistant: &AssistantContent{API: "openai-completions", Provider: "local", Model: "local-model", Parts: []ContentPart{
			{Text: &TextPart{Text: "done"}},
		}}},
	}
	out := PreTransform(messages, TransformConfig{TargetAPI: "openai-completions", TargetProvider: "local", TargetModelID: "local-model", InsertSyntheticResult: true})
	if len(out) != 3 || out[1].ToolResult == nil {
		t.Fatalf("got %+v, want the synthetic result flushed before the next assistant turn", out)
	}
}

func TestAnAbortedAssistantIsDroppedByTheTransformToo(t *testing.T) {
	messages := []Message{
		{User: &UserContent{Text: "hello", HasText: true}},
		{Assistant: &AssistantContent{StopReason: StopAborted, Parts: []ContentPart{{Text: &TextPart{Text: "partial"}}}}},
		{User: &UserContent{Text: "retry", HasText: true}},
	}
	out := PreTransform(messages, TransformConfig{TargetAPI: "openai-completions", TargetProvider: "local", TargetModelID: "local-model"})
	if len(out) != 2 {
		t.Fatalf("got %d messages, want the aborted assistant gone", len(out))
	}
}

func TestCrossModelThinkingBecomesTextAndASignatureIsDropped(t *testing.T) {
	messages := []Message{
		{Assistant: &AssistantContent{API: "anthropic", Provider: "anthropic", Model: "claude-3", Parts: []ContentPart{
			{Thinking: &ThinkingPart{Thinking: "internal reasoning", Signature: "sig123"}},
			{Text: &TextPart{Text: "response"}},
		}}},
	}
	out := PreTransform(messages, TransformConfig{TargetAPI: "openai-completions", TargetProvider: "openai", TargetModelID: "gpt-4"})
	parts := out[0].Assistant.Parts
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want the thinking kept as a text part", len(parts))
	}
	if parts[0].Text == nil || parts[0].Text.Text != "internal reasoning" {
		t.Errorf("the first part = %+v, want the thinking as text", parts[0])
	}
	if parts[1].Text.Text != "response" {
		t.Errorf("the second part = %+v", parts[1])
	}
}

func TestSameModelThinkingKeepsItsSignature(t *testing.T) {
	messages := []Message{
		{Assistant: &AssistantContent{API: "openai-completions", Provider: "local", Model: "local-model", Parts: []ContentPart{
			{Thinking: &ThinkingPart{Thinking: "reasoning", Signature: "sig"}},
		}}},
	}
	out := PreTransform(messages, TransformConfig{TargetAPI: "openai-completions", TargetProvider: "local", TargetModelID: "local-model"})
	part := out[0].Assistant.Parts[0]
	if part.Thinking == nil || part.Thinking.Signature != "sig" {
		t.Errorf("the part = %+v, want the thinking kept with its signature", part)
	}
}

func TestAToolIDIsRemappedOnBothTheCallAndItsResult(t *testing.T) {
	messages := []Message{
		{Assistant: &AssistantContent{API: "x", Provider: "p", Model: "m", Parts: []ContentPart{
			{ToolCall: &ToolCall{ID: "call_123|very_long_suffix_that_should_be_stripped", Name: "bash", Arguments: "{}"}},
		}}},
		{ToolResult: &ToolResult{ToolCallID: "call_123|very_long_suffix_that_should_be_stripped", Parts: []ContentPart{{Text: &TextPart{Text: "ok"}}}}},
	}
	out := PreTransform(messages, TransformConfig{TargetAPI: "x", TargetProvider: "p", TargetModelID: "m", MaxToolIDLen: 40, InsertSyntheticResult: true})
	if got := out[0].Assistant.Parts[0].ToolCall.ID; got != "call_123" {
		t.Errorf("the call's id = %q, want the normalized one", got)
	}
	if got := out[1].ToolResult.ToolCallID; got != "call_123" {
		t.Errorf("the result's id = %q, want it remapped to match", got)
	}
}

func TestAPipeIsStrippedEvenWhenNoLimitIsSet(t *testing.T) {
	messages := []Message{
		{Assistant: &AssistantContent{API: "x", Provider: "p", Model: "m", Parts: []ContentPart{
			{ToolCall: &ToolCall{ID: "call_9|worker", Name: "bash", Arguments: "{}"}},
		}}},
	}
	out := PreTransform(messages, TransformConfig{TargetAPI: "x", TargetProvider: "p", TargetModelID: "m"})
	if got := out[0].Assistant.Parts[0].ToolCall.ID; got != "call_9" {
		t.Errorf("the call's id = %q, want the pipe stripped even with a zero limit", got)
	}
}

func TestAnOpenAIHostGetsTheFortyByteToolIDLimit(t *testing.T) {
	if got := maxToolIDLen(openAIModel()); got != 40 {
		t.Errorf("an openai host = %d, want 40", got)
	}
	if got := maxToolIDLen(loopbackModel()); got != 0 {
		t.Errorf("a loopback = %d, want 0", got)
	}
	proxy := loopbackModel()
	proxy.Provider = "openai"
	proxy.Compat = CompatOptions{SupportsStore: boolPtr(true), SupportsDeveloperRole: boolPtr(true), SupportsReasoningEffort: boolPtr(true)}
	if got := maxToolIDLen(proxy); got != 40 {
		t.Errorf("a transparent proxy = %d, want 40", got)
	}
}

func TestTheBodyCarriesTheSyntheticResultZigWouldWrite(t *testing.T) {
	body := BuildRequestBody(loopbackModel(), Context{Messages: transformFixture()}, StreamOptions{})
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("the body is not json: %v", err)
	}
	list := decoded["messages"].([]any)
	sawSynthetic := false
	for _, m := range list {
		parsed := m.(map[string]any)
		if parsed["role"] == "tool" && parsed["content"] == "No result provided" {
			sawSynthetic = true
		}
	}
	if !sawSynthetic {
		t.Errorf("the body = %s\nwant the synthetic result for the unanswered call", body)
	}
}

func TestMistralHostsGetTheHashedToolIDsInTheBody(t *testing.T) {
	model := loopbackModel()
	model.BaseURL = "https://api.mistral.ai"
	messages := []Message{
		{Assistant: &AssistantContent{API: "openai-completions", Provider: "local", Model: "local-model", StopReason: "tool_use", Parts: []ContentPart{
			{ToolCall: &ToolCall{ID: "call_original", Name: "bash", Arguments: "{}"}},
		}}},
		{ToolResult: &ToolResult{ToolCallID: "call_original", Parts: []ContentPart{{Text: &TextPart{Text: "ok"}}}}},
	}
	body := BuildRequestBody(model, Context{Messages: messages}, StreamOptions{})
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("the body is not json: %v", err)
	}
	hashed := MistralToolID("call_original")
	for _, m := range decoded["messages"].([]any) {
		parsed := m.(map[string]any)
		if parsed["role"] == "tool" && parsed["tool_call_id"] != hashed {
			t.Errorf("the result's id = %v, want the hashed %q", parsed["tool_call_id"], hashed)
		}
	}
}

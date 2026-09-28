package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func anthropicModel() Model {
	return Model{
		ID: "claude-sonnet-4-5", API: AnthropicWire, Provider: "anthropic",
		BaseURL: "https://api.anthropic.com", HasBaseURL: true, MaxTokens: 30000, HasCompat: true,
	}
}

func headerValue(headers []Header, name string) (string, bool) {
	for _, header := range headers {
		if header.Name == name {
			return header.Value, true
		}
	}
	return "", false
}

func names(headers []Header) []string {
	out := make([]string, 0, len(headers))
	for _, header := range headers {
		out = append(out, header.Name)
	}
	return out
}

func TestTheKeyGoesOutAsXAPIKeyAndNotAsABearer(t *testing.T) {
	headers := BuildAnthropicHeaders("sk-ant-ordinary", nil)
	value, ok := headerValue(headers, "x-api-key")
	if !ok || value != "sk-ant-ordinary" {
		t.Errorf("headers = %v, want the key on x-api-key", names(headers))
	}
	if _, present := headerValue(headers, "authorization"); present {
		t.Error("the ordinary path must not send an authorization header: bearer is the oauth branch only")
	}
}

func TestAnOAuthKeySwitchesToBearerAndAddsItsOwnHeaders(t *testing.T) {
	headers := BuildAnthropicHeaders("sk-ant-oat01-abc", nil)
	auth, ok := headerValue(headers, "authorization")
	if !ok || auth != "Bearer sk-ant-oat01-abc" {
		t.Errorf("authorization = %q, want the bearer value", auth)
	}
	if _, present := headerValue(headers, "x-api-key"); present {
		t.Error("an oauth key must not also go out on x-api-key")
	}
	if value, _ := headerValue(headers, "anthropic-beta"); !strings.Contains(value, "oauth-2025-04-20") {
		t.Errorf("anthropic-beta = %q, want the oauth flags", value)
	}
	if value, _ := headerValue(headers, "user-agent"); value != oauthUserAgent {
		t.Errorf("user-agent = %q, want the fixed oauth agent", value)
	}
	if value, _ := headerValue(headers, "x-app"); value != "cli" {
		t.Errorf("x-app = %q, want cli", value)
	}
	if value, _ := headerValue(headers, "anthropic-dangerous-direct-browser-access"); value != "true" {
		t.Errorf("anthropic-dangerous-direct-browser-access = %q, want true", value)
	}
}

func TestAnEmptyKeyStillCarriesTheBetaHeader(t *testing.T) {
	headers := BuildAnthropicHeaders("", nil)
	if value, ok := headerValue(headers, "anthropic-beta"); !ok || value != betaFlagsPlain {
		t.Errorf("headers = %v, want the plain beta flags: an empty key is not an absent header", names(headers))
	}
	if _, present := headerValue(headers, "x-api-key"); present {
		t.Error("an empty key sends no key header at all")
	}
}

func TestTheVersionAndContentTypeAreAlwaysPresent(t *testing.T) {
	for _, key := range []string{"", "sk-ant-ordinary", "sk-ant-oat01"} {
		headers := BuildAnthropicHeaders(key, nil)
		if value, _ := headerValue(headers, "anthropic-version"); value != "2023-06-01" {
			t.Errorf("key %q: anthropic-version = %q", key, value)
		}
		if value, _ := headerValue(headers, "content-type"); value != "application/json" {
			t.Errorf("key %q: content-type = %q", key, value)
		}
	}
}

func TestAModelHeaderCannotDisplaceOneTheClientAlreadySet(t *testing.T) {
	headers := BuildAnthropicHeaders("sk-ant-oat01", []Header{
		{"User-Agent", "mine/1.0"},
		{"anthropic-beta", "only-mine"},
		{"x-custom", "kept"},
	})
	values := func(name string) []string {
		out := []string{}
		for _, header := range headers {
			if strings.EqualFold(header.Name, name) {
				out = append(out, header.Value)
			}
		}
		return out
	}
	if got := values("user-agent"); len(got) != 1 || got[0] != oauthUserAgent {
		t.Errorf("user-agent = %v, want exactly the client's own: the model's is not appended at all", got)
	}
	if got := values("anthropic-beta"); len(got) != 1 || strings.Contains(got[0], "only-mine") {
		t.Errorf("anthropic-beta = %v, want exactly the client's own list", got)
	}
	if got := values("x-custom"); len(got) != 1 || got[0] != "kept" {
		t.Errorf("x-custom = %v, want a name the client does not set to be appended", got)
	}
}

func TestTheAnthropicHostTestIsExactAndHasNoSuffixBranch(t *testing.T) {
	if !IsAnthropicHost("https://api.anthropic.com", true) {
		t.Error("the exact host matches")
	}
	if !IsAnthropicHost("https://API.Anthropic.com", true) {
		t.Error("the host comparison ignores case")
	}
	if IsAnthropicHost("https://eu.anthropic.com", true) {
		t.Error("no suffix branch here: this is exact equality, unlike the openai host test")
	}
	if IsAnthropicHost("", false) {
		t.Error("an absent base url is not a host")
	}
}

func TestAdaptiveThinkingIsASubstringTestOnTheModelID(t *testing.T) {
	if !supportsAdaptiveThinking("claude-opus-4-6") || !supportsAdaptiveThinking("claude-opus-4.6") {
		t.Error("both spellings of opus 4.6 are adaptive")
	}
	if supportsAdaptiveThinking("claude-sonnet-4-5") {
		t.Error("a non-adaptive id is not")
	}
}

func TestThreeThinkingLevelsCollapseOntoLow(t *testing.T) {
	cases := map[string]string{
		"off": "low", "minimal": "low", "low": "low",
		"medium": "medium", "high": "high", "xhigh": "max",
	}
	for level, want := range cases {
		if got := mapThinkingLevelToEffort(level); got != want {
			t.Errorf("%q = %q, want %q", level, got, want)
		}
	}
}

func TestTheDefaultThinkingBudgetsAndTheirOverrides(t *testing.T) {
	cases := map[string]int{"off": 0, "minimal": 256, "low": 512, "medium": 1024, "high": 2048, "xhigh": 4096}
	for level, want := range cases {
		if got := defaultThinkingBudget(level, nil); got != want {
			t.Errorf("%q = %d, want %d", level, got, want)
		}
	}
	if got := defaultThinkingBudget("high", map[string]int{"high": 99}); got != 99 {
		t.Errorf("a configured budget = %d, want it to win over 2048", got)
	}
	if got := defaultThinkingBudget("off", map[string]int{"off": 99}); got != 0 {
		t.Errorf("off = %d, want 0 whatever a budget says: only off is fixed", got)
	}
}

func TestCacheControlNeedsALongRetentionAndAnAnthropicHost(t *testing.T) {
	if getCacheControl("https://api.anthropic.com", true, CacheNone, true, false) != nil {
		t.Error("a retention of none returns nothing at all")
	}
	short := getCacheControl("https://api.anthropic.com", true, CacheShort, true, false)
	if short == nil || short.hasTTL {
		t.Errorf("short = %+v, want a control with no ttl", short)
	}
	long := getCacheControl("https://api.anthropic.com", true, CacheLong, true, false)
	if long == nil || !long.hasTTL {
		t.Errorf("long on an anthropic host = %+v, want the ttl", long)
	}
	elsewhere := getCacheControl("https://proxy.test", true, CacheLong, true, false)
	if elsewhere == nil || elsewhere.hasTTL {
		t.Errorf("long off-host without the flag = %+v, want no ttl", elsewhere)
	}
	flagged := getCacheControl("https://proxy.test", true, CacheLong, true, true)
	if flagged == nil || !flagged.hasTTL {
		t.Errorf("long with the compat flag = %+v, want the ttl", flagged)
	}
	unset := getCacheControl("https://api.anthropic.com", true, "", false, false)
	if unset == nil || unset.retention != CacheShort {
		t.Errorf("no retention = %+v, want short", unset)
	}
}

func anthropicBody(t *testing.T, model Model, ctx Context, options AnthropicOptions) map[string]any {
	t.Helper()
	raw, _ := BuildAnthropicRequestBody(model, ctx, options)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("the body is not json: %v\n%s", err, raw)
	}
	return out
}

func TestTheBodyOrderIsModelThenMaxTokensThenStream(t *testing.T) {
	raw, _ := BuildAnthropicRequestBody(anthropicModel(), Context{}, AnthropicOptions{})
	order := []string{"\"model\":", "\"max_tokens\":", "\"stream\":"}
	at := func(name string) int { return strings.Index(string(raw), name) }
	for i := 1; i < len(order); i++ {
		if at(order[i-1]) > at(order[i]) {
			t.Errorf("%s appears after %s, want model, max_tokens, stream\n%s", order[i-1], order[i], raw)
		}
	}
	if _, ok := anthropicBody(t, anthropicModel(), Context{}, AnthropicOptions{})["stream_options"]; ok {
		t.Error("there is no stream_options: usage arrives in message_start")
	}
}

func TestTheDefaultMaxTokensIsAMinusThreeCappedAtThirtyTwoThousand(t *testing.T) {
	if got := anthropicBody(t, anthropicModel(), Context{}, AnthropicOptions{})["max_tokens"]; got != float64(10000) {
		t.Errorf("max_tokens = %v, want 30000/3", got)
	}
	huge := anthropicModel()
	huge.MaxTokens = 300000
	if got := anthropicBody(t, huge, Context{}, AnthropicOptions{})["max_tokens"]; got != float64(32000) {
		t.Errorf("max_tokens = %v, want the 32000 cap", got)
	}
	tiny := anthropicModel()
	tiny.MaxTokens = 900
	if got := anthropicBody(t, tiny, Context{}, AnthropicOptions{})["max_tokens"]; got != float64(300) {
		t.Errorf("max_tokens = %v, want 900/3", got)
	}
	over := anthropicBody(t, anthropicModel(), Context{}, AnthropicOptions{MaxTokens: 7, HasMaxTokens: true})
	if over["max_tokens"] != float64(7) {
		t.Errorf("max_tokens = %v, want the caller's own", over)
	}
}

func TestTheSystemPromptIsAnArrayOfTextBlocks(t *testing.T) {
	body := anthropicBody(t, anthropicModel(), Context{HasSystem: true, SystemPrompt: "be terse"}, AnthropicOptions{})
	system, ok := body["system"].([]any)
	if !ok || len(system) != 1 {
		t.Fatalf("system = %v, want a one block array", body["system"])
	}
	block := system[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "be terse" {
		t.Errorf("the block = %v, want a text block holding the prompt", block)
	}
	if _, present := body["role"]; present {
		t.Error("the system prompt is not a role message here")
	}
}

func TestTemperatureIsDroppedWhenThinkingAndNotOne(t *testing.T) {
	model := anthropicModel()
	model.Reasoning = true
	thinking := AnthropicOptions{Temperature: 0.3, HasTemperature: true, ThinkingEnabled: true, HasThinkingBudget: true, ThinkingBudgetTokens: 2048}
	body := anthropicBody(t, model, Context{}, thinking)
	if _, ok := body["temperature"]; ok {
		t.Error("thinking at a temperature that is not 1 drops the temperature")
	}
	one := thinking
	one.Temperature = 1
	if got := anthropicBody(t, model, Context{}, one)["temperature"]; got != 1.0 {
		t.Errorf("temperature = %v, want it kept at exactly 1", got)
	}
	plain := anthropicBody(t, model, Context{}, AnthropicOptions{Temperature: 0.3, HasTemperature: true})
	if plain["temperature"] != 0.3 {
		t.Errorf("temperature = %v, want it kept when no thinking is emitted", plain["temperature"])
	}
}

func TestAToolCallIsABlockInsideTheContentArray(t *testing.T) {
	ctx := Context{Messages: []Message{
		{Assistant: &AssistantContent{API: AnthropicWire, Provider: "anthropic", Model: "claude-sonnet-4-5", StopReason: "tool_use", Parts: []ContentPart{
			{Text: &TextPart{Text: "reading"}},
			{ToolCall: &ToolCall{ID: "toolu_1", Name: "Read", Arguments: `{"path":"a"}`}},
		}}},
		{ToolResult: &ToolResult{ToolCallID: "toolu_1", Parts: []ContentPart{{Text: &TextPart{Text: "contents"}}}}},
	}}
	body := anthropicBody(t, anthropicModel(), ctx, AnthropicOptions{})
	messages := body["messages"].([]any)
	assistant := messages[0].(map[string]any)
	if assistant["role"] != "assistant" {
		t.Errorf("role = %v, want assistant", assistant["role"])
	}
	blocks := assistant["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want the text and the tool use", len(blocks))
	}
	call := blocks[1].(map[string]any)
	if call["type"] != "tool_use" || call["id"] != "toolu_1" || call["name"] != "Read" {
		t.Errorf("the call block = %v", call)
	}
	if _, ok := call["input"].(map[string]any); !ok {
		t.Errorf("input = %v, want the arguments spliced as an object", call["input"])
	}
	if _, present := assistant["tool_calls"]; present {
		t.Error("there is no sibling tool_calls member here: the call is a block in the array")
	}
}

func TestAToolResultIsAUserMessageWithAThreeWayContent(t *testing.T) {
	base := func(parts []ContentPart) Context {
		return Context{Messages: []Message{
			{Assistant: &AssistantContent{API: AnthropicWire, Provider: "anthropic", Model: "claude-sonnet-4-5", StopReason: "tool_use", Parts: []ContentPart{
				{ToolCall: &ToolCall{ID: "toolu_1", Name: "Read", Arguments: "{}"}},
			}}},
			{ToolResult: &ToolResult{ToolCallID: "toolu_1", Parts: parts}},
		}}
	}
	single := anthropicBody(t, anthropicModel(), base([]ContentPart{{Text: &TextPart{Text: "ok"}}}), AnthropicOptions{})["messages"].([]any)[1].(map[string]any)
	if single["role"] != "user" {
		t.Errorf("role = %v, want user: a result is not a tool message here", single["role"])
	}
	entry := single["content"].([]any)[0].(map[string]any)
	if entry["content"] != "ok" {
		t.Errorf("one text part = %v, want a plain string", entry["content"])
	}
	if entry["is_error"] != false {
		t.Errorf("is_error = %v, want it always written", entry["is_error"])
	}
	if _, ok := entry["tool_call_id"]; ok {
		t.Error("the member is tool_use_id here, not tool_call_id")
	}
	if entry["tool_use_id"] != "toolu_1" {
		t.Errorf("tool_use_id = %v", entry["tool_use_id"])
	}

	empty := anthropicBody(t, anthropicModel(), base(nil), AnthropicOptions{})["messages"].([]any)[1].(map[string]any)
	emptyEntry := empty["content"].([]any)[0].(map[string]any)
	if emptyEntry["content"] != "" {
		t.Errorf("zero parts = %v, want an empty string, not an array", emptyEntry["content"])
	}

	multi := anthropicBody(t, anthropicModel(), base([]ContentPart{
		{Text: &TextPart{Text: "one"}},
		{Text: &TextPart{Text: "two"}},
	}), AnthropicOptions{})["messages"].([]any)[1].(map[string]any)
	multiEntry := multi["content"].([]any)[0].(map[string]any)
	if _, ok := multiEntry["content"].([]any); !ok {
		t.Errorf("two parts = %v, want an array of blocks", multiEntry["content"])
	}
}

func TestAConsecutiveRunOfResultsIsOneUserMessage(t *testing.T) {
	ctx := Context{Messages: []Message{
		{Assistant: &AssistantContent{API: AnthropicWire, Provider: "anthropic", Model: "claude-sonnet-4-5", StopReason: "tool_use", Parts: []ContentPart{
			{ToolCall: &ToolCall{ID: "a", Name: "f", Arguments: "{}"}},
			{ToolCall: &ToolCall{ID: "b", Name: "g", Arguments: "{}"}},
		}}},
		{ToolResult: &ToolResult{ToolCallID: "a", Parts: []ContentPart{{Text: &TextPart{Text: "one"}}}}},
		{ToolResult: &ToolResult{ToolCallID: "b", Parts: []ContentPart{{Text: &TextPart{Text: "two"}}}}},
	}}
	messages := anthropicBody(t, anthropicModel(), ctx, AnthropicOptions{})["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("got %d messages, want the assistant and one batched user message", len(messages))
	}
	if entries := messages[1].(map[string]any)["content"].([]any); len(entries) != 2 {
		t.Errorf("got %d results in the batch, want 2", len(entries))
	}
}

func TestASignedThinkingBlockIsTheOnlyOneThatIsNotText(t *testing.T) {
	ctx := func(sig string) Context {
		return Context{Messages: []Message{{Assistant: &AssistantContent{API: AnthropicWire, Provider: "anthropic", Model: "claude-sonnet-4-5", Parts: []ContentPart{
			{Thinking: &ThinkingPart{Thinking: "step", Signature: sig}},
		}}}}}
	}
	unsigned := anthropicBody(t, anthropicModel(), ctx(""), AnthropicOptions{})["messages"].([]any)[0].(map[string]any)
	block := unsigned["content"].([]any)[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "step" {
		t.Errorf("an unsigned thinking block = %v, want text", block)
	}
	signed := anthropicBody(t, anthropicModel(), ctx("sig-1"), AnthropicOptions{})["messages"].([]any)[0].(map[string]any)
	signedBlock := signed["content"].([]any)[0].(map[string]any)
	if signedBlock["type"] != "thinking" || signedBlock["thinking"] != "step" || signedBlock["signature"] != "sig-1" {
		t.Errorf("a signed thinking block = %v, want thinking with its signature", signedBlock)
	}
}

func TestAToolCarriesItsSchemaUnderInputSchema(t *testing.T) {
	ctx := Context{Tools: []Tool{{Name: "Read", Description: "read a file", Parameters: json.RawMessage(`{"type":"object"}`)}}}
	tools := anthropicBody(t, anthropicModel(), ctx, AnthropicOptions{})["tools"].([]any)
	tool := tools[0].(map[string]any)
	if tool["name"] != "Read" || tool["description"] != "read a file" {
		t.Errorf("the tool = %v", tool)
	}
	if _, ok := tool["input_schema"].(map[string]any); !ok {
		t.Errorf("input_schema = %v, want the schema under its own name, not parameters", tool["input_schema"])
	}
	if _, present := tool["strict"]; present {
		t.Error("there is no strict member on this wire")
	}
}

func TestTheAnonymityRuleExcludesOneVendorNotFour(t *testing.T) {
	allowed := Model{Provider: "openai", AllowsAnonymous: true, HasBaseURL: true, BaseURL: "https://api.openai.com"}
	if !AllowsAnonymousWith(allowed, AnthropicAnonymousBlocked) {
		t.Error("openai is not on this client's blocked list")
	}
	blocked := Model{Provider: "anthropic", AllowsAnonymous: true, HasBaseURL: true, BaseURL: "https://api.anthropic.com"}
	if AllowsAnonymousWith(blocked, AnthropicAnonymousBlocked) {
		t.Error("anthropic is the one vendor this client blocks")
	}
	if !AllowsAnonymousWith(blocked, OpenAIAnonymousBlocked) {
		t.Error("the openai list does not name anthropic, so reusing it here would let an anthropic model through anonymously: the two lists are separate on purpose")
	}
}

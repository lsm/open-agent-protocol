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
	raw, _ := BuildAnthropicRequestBody(model, ctx, options, "")
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("the body is not json: %v\n%s", err, raw)
	}
	return out
}

func anthropicToolResult(id string) Message {
	return Message{ToolResult: &ToolResult{ToolCallID: id, ToolName: "bash", Parts: []ContentPart{{Text: &TextPart{Text: "output"}}}}}
}

func anthropicToolUsesInBody(t *testing.T, ctx Context) []string {
	t.Helper()
	raw, _ := BuildAnthropicRequestBody(anthropicModel(), ctx, AnthropicOptions{}, "")
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("the body is not json: %v\n%s", err, raw)
	}
	var ids []string
	for _, rawMsg := range out["messages"].([]any) {
		msg := rawMsg.(map[string]any)
		content, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		if len(content) == 0 {
			t.Errorf("a message carries an empty content array: an all-orphan run writes no message at all\n%s", raw)
		}
		for _, rawBlock := range content {
			block := rawBlock.(map[string]any)
			if block["type"] != "tool_result" {
				continue
			}
			ids = append(ids, block["tool_use_id"].(string))
		}
	}
	return ids
}

func TestARunOfOnlyOrphanedResultsWritesNoMessageAtAll(t *testing.T) {
	ctx := Context{Messages: []Message{
		{User: &UserContent{Text: "go", HasText: true}},
		{Assistant: &AssistantContent{API: AnthropicWire, Provider: "anthropic", Model: "claude-sonnet-4-5", StopReason: "tool_use", Parts: []ContentPart{
			{ToolCall: &ToolCall{ID: "toolu_1", Name: "bash", Arguments: "{}"}},
		}}},
		anthropicToolResult("toolu_1"),
		{User: &UserContent{Text: "carry on", HasText: true}},
		anthropicToolResult("toolu_8"),
		anthropicToolResult("toolu_9"),
	}}
	got := anthropicToolUsesInBody(t, ctx)
	if len(got) != 1 || got[0] != "toolu_1" {
		t.Errorf("got %v, want only the answered one: the second run is entirely orphans, and the call it could have stood in for is already answered so no synthetic result is grown", got)
	}
}

func TestAMixedRunKeepsOnlyTheResultsWhoseCallIsInTheConversation(t *testing.T) {
	ctx := Context{Messages: []Message{
		{User: &UserContent{Text: "go", HasText: true}},
		{Assistant: &AssistantContent{API: AnthropicWire, Provider: "anthropic", Model: "claude-sonnet-4-5", StopReason: "tool_use", Parts: []ContentPart{
			{ToolCall: &ToolCall{ID: "toolu_1", Name: "bash", Arguments: "{}"}},
			{ToolCall: &ToolCall{ID: "toolu_2", Name: "bash", Arguments: "{}"}},
		}}},
		anthropicToolResult("toolu_1"),
		anthropicToolResult("toolu_9"),
		anthropicToolResult("toolu_2"),
	}}
	got := anthropicToolUsesInBody(t, ctx)
	if len(got) != 2 || got[0] != "toolu_1" || got[1] != "toolu_2" {
		t.Errorf("got %v, want [toolu_1 toolu_2]", got)
	}
}

func TestTheBodyOrderIsModelThenMaxTokensThenStream(t *testing.T) {
	raw, _ := BuildAnthropicRequestBody(anthropicModel(), Context{}, AnthropicOptions{}, "")
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

func TestTheAnonymityRuleExcludesTheVendorsThisWireServes(t *testing.T) {
	allowed := Model{Provider: "openai", AllowsAnonymous: true, HasBaseURL: true, BaseURL: "https://api.openai.com"}
	if !AllowsAnonymousWith(allowed, AnthropicAnonymousBlocked) {
		t.Error("openai is not on this client's blocked list")
	}
	blocked := Model{Provider: "anthropic", AllowsAnonymous: true, HasBaseURL: true, BaseURL: "https://api.anthropic.com"}
	if AllowsAnonymousWith(blocked, AnthropicAnonymousBlocked) {
		t.Error("anthropic is a vendor this client serves, so it is blocked")
	}
	deepseek := Model{Provider: "deepseek", AllowsAnonymous: true, HasBaseURL: true, BaseURL: "https://api.deepseek.com/anthropic"}
	if AllowsAnonymousWith(deepseek, AnthropicAnonymousBlocked) {
		t.Error("deepseek is served on this wire too, so it is blocked")
	}
	if !AllowsAnonymousWith(blocked, OpenAIAnonymousBlocked) {
		t.Error("the openai list does not name anthropic, so reusing it here would let an anthropic model through anonymously: the two lists are separate on purpose")
	}
}

func systemTextOf(t *testing.T, body []byte) string {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("not json: %v", err)
	}
	system, ok := parsed["system"].([]any)
	if !ok || len(system) == 0 {
		t.Fatalf("no system array in %s", body)
	}
	return system[0].(map[string]any)["text"].(string)
}

func TestAnOAuthKeyChangesTheBodyAsWellAsTheHeaders(t *testing.T) {
	ctx := Context{HasSystem: true, SystemPrompt: "be terse"}
	plain, isOAuth := BuildAnthropicRequestBody(anthropicModel(), ctx, AnthropicOptions{}, "sk-ant-ordinary")
	if isOAuth {
		t.Error("an ordinary key is not oauth")
	}
	if strings.Contains(string(plain), "be terse") == false {
		t.Errorf("the caller's prompt should survive on the ordinary path:\n%s", plain)
	}
	oauth, isOAuth := BuildAnthropicRequestBody(anthropicModel(), ctx, AnthropicOptions{}, "sk-ant-oat01-x")
	if !isOAuth {
		t.Fatal("a key containing sk-ant-oat is oauth")
	}
	if !strings.Contains(string(oauth), "be terse") {
		t.Errorf("the oauth path prepends the Claude Code text to the caller's prompt, so the prompt must survive:\n%s", oauth)
	}
	if !strings.HasPrefix(systemTextOf(t, oauth), oauthSystemText+"\n\n") {
		t.Errorf("the oauth system text = %q, want the Claude Code sentence then a blank line then the caller's prompt", systemTextOf(t, oauth))
	}
	if !strings.Contains(string(oauth), "Claude Code") {
		t.Errorf("the oauth system text is missing:\n%s", oauth)
	}
	noPrompt, _ := BuildAnthropicRequestBody(anthropicModel(), Context{}, AnthropicOptions{}, "sk-ant-oat01-x")
	if !strings.Contains(string(noPrompt), "Claude Code") {
		t.Errorf("with no system prompt the oauth text is written anyway:\n%s", noPrompt)
	}
}

func TestTheOAuthPathCanonicalizesTheToolNameInTheBody(t *testing.T) {
	ctx := Context{Tools: []Tool{{Name: "Bash"}}, Messages: []Message{
		{Assistant: &AssistantContent{API: AnthropicWire, Provider: "anthropic", Model: "claude-sonnet-4-5", StopReason: "tool_use", Parts: []ContentPart{
			{ToolCall: &ToolCall{ID: "t1", Name: "bash", Arguments: "{}"}},
		}}},
		{ToolResult: &ToolResult{ToolCallID: "t1", Parts: []ContentPart{{Text: &TextPart{Text: "ok"}}}}},
	}}
	oauth, _ := BuildAnthropicRequestBody(anthropicModel(), ctx, AnthropicOptions{}, "sk-ant-oat01")
	if !strings.Contains(string(oauth), `"name":"Bash"`) {
		t.Errorf("the oauth path should canonicalise the name to the tool's own spelling:\n%s", oauth)
	}
	plain, _ := BuildAnthropicRequestBody(anthropicModel(), ctx, AnthropicOptions{}, "sk-ant-ordinary")
	if !strings.Contains(string(plain), `"name":"bash"`) {
		t.Errorf("without oauth the caller's spelling stands:\n%s", plain)
	}
}

func TestTheLongCacheTTLRidesTheModelsCompatFlagOffHost(t *testing.T) {
	model := anthropicModel()
	model.BaseURL = "https://gateway.test"
	model.Compat = CompatOptions{SupportsAnthropicCacheTTL: boolPtr(true)}
	raw, _ := BuildAnthropicRequestBody(model, Context{HasSystem: true, SystemPrompt: "s"},
		AnthropicOptions{CacheRetention: CacheLong, HasCacheRetention: true}, "")
	if !strings.Contains(string(raw), `"ttl":"1h"`) {
		t.Errorf("a gateway with the compat flag and a long retention = %s\nwant the ttl", raw)
	}
	off := anthropicModel()
	off.BaseURL = "https://gateway.test"
	off.Compat = CompatOptions{}
	plain, _ := BuildAnthropicRequestBody(off, Context{HasSystem: true, SystemPrompt: "s"},
		AnthropicOptions{CacheRetention: CacheLong, HasCacheRetention: true}, "")
	if strings.Contains(string(plain), `"ttl":"1h"`) {
		t.Errorf("without the flag off-host = %s\nwant no ttl", plain)
	}
}

func TestAnAdaptiveModelDeclaresAdaptiveThinking(t *testing.T) {
	model := anthropicModel()
	model.Reasoning = true
	model.ID = "claude-opus-4-6"
	raw, _ := BuildAnthropicRequestBody(model, Context{}, AnthropicOptions{
		ThinkingEnabled: true, ThinkingEffort: "high",
	}, "")
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("not json: %v", err)
	}
	thinking, ok := body["thinking"].(map[string]any)
	if !ok || thinking["type"] != "adaptive" {
		t.Errorf("thinking = %v, want the adaptive member: the effort rides a body that must declare it", body["thinking"])
	}
	if effort := body["output_config"].(map[string]any)["effort"]; effort != "high" {
		t.Errorf("effort = %v, want high", effort)
	}
}

func TestTheBudgetIsGuardedDefaultedAndClamped(t *testing.T) {
	model := anthropicModel()
	model.Reasoning = true
	budget := func(max int, tokens int, has bool) (map[string]any, bool) {
		raw, _ := BuildAnthropicRequestBody(model, Context{}, AnthropicOptions{
			ThinkingEnabled: true, ThinkingBudgetTokens: tokens, HasThinkingBudget: has,
			MaxTokens: max, HasMaxTokens: true,
		}, "")
		var body map[string]any
		json.Unmarshal(raw, &body)
		thinking, ok := body["thinking"].(map[string]any)
		return thinking, ok
	}
	if _, ok := budget(1000, 2048, true); ok {
		t.Error("at a max of 1000 the branch is guarded off, so no thinking member at all")
	}
	thinking, ok := budget(8000, 2048, true)
	if !ok || thinking["budget_tokens"] != float64(2048) {
		t.Errorf("thinking = %v, want the explicit budget inside the clamp", thinking)
	}
	thinking, ok = budget(8000, 0, false)
	if !ok || thinking["budget_tokens"] != float64(1024) {
		t.Errorf("thinking = %v, want the 1024 default when no budget is given", thinking)
	}
	thinking, ok = budget(8000, 10, true)
	if !ok || thinking["budget_tokens"] != float64(1024) {
		t.Errorf("thinking = %v, want a small budget lifted to the 1024 floor", thinking)
	}
	thinking, ok = budget(1200, 4096, true)
	if !ok || thinking["budget_tokens"] != float64(1199) {
		t.Errorf("thinking = %v, want the budget capped at one under the max", thinking)
	}
}

func TestToolChoiceTakesTheSharedVocabulary(t *testing.T) {
	cases := map[string]string{
		ToolChoiceAuto:     "auto",
		ToolChoiceNone:     "none",
		ToolChoiceRequired: "any",
	}
	for mode, wire := range cases {
		raw, _ := BuildAnthropicRequestBody(anthropicModel(), Context{}, AnthropicOptions{
			ToolChoiceType: mode, HasToolChoice: true,
		}, "")
		if !strings.Contains(string(raw), `"tool_choice":{"type":"`+wire+`"}`) {
			t.Errorf("%q = %s\nwant the wire spelling %q", mode, raw, wire)
		}
	}
	none := anthropicBodyWithChoice(t, ToolChoiceNone)
	if _, ok := none["tool_choice"]; !ok {
		t.Error("none must be written, not dropped: never calling tools is a choice the caller made")
	}
}

func anthropicBodyWithChoice(t *testing.T, mode string) map[string]any {
	t.Helper()
	raw, _ := BuildAnthropicRequestBody(anthropicModel(), Context{}, AnthropicOptions{
		ToolChoiceType: mode, HasToolChoice: true,
	}, "")
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("not json: %v", err)
	}
	return body
}

func TestToolChoiceIsWrittenEvenWithNoTools(t *testing.T) {
	body := anthropicBodyWithChoice(t, ToolChoiceAuto)
	if _, ok := body["tool_choice"]; !ok {
		t.Error("here it is written outside the tools branch, the opposite of the openai path")
	}
}

func TestAMessageWithNoKindAtAllIsSkipped(t *testing.T) {
	ctx := Context{Messages: []Message{
		{},
		{User: &UserContent{Text: "after", HasText: true}},
	}}
	raw, _ := BuildAnthropicRequestBody(anthropicModel(), ctx, AnthropicOptions{}, "")
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("not json: %v", err)
	}
	if len(body["messages"].([]any)) != 1 {
		t.Errorf("got %v, want the empty message skipped", body["messages"])
	}
}

func TestAnAssistantImageIsDroppedWhereTheOracleDropsIt(t *testing.T) {
	ctx := func() Context {
		return Context{Messages: []Message{{Assistant: &AssistantContent{
			API: AnthropicWire, Provider: "anthropic", Model: "claude-sonnet-4-5",
			Parts: []ContentPart{
				{Text: &TextPart{Text: "hi"}},
				{Image: &ImagePart{Data: "aGk=", MediaType: "image/png"}},
			},
		}}}}
	}
	plain := anthropicBody(t, anthropicModel(), ctx(), AnthropicOptions{})["messages"].([]any)[0].(map[string]any)
	for _, block := range plain["content"].([]any) {
		if block.(map[string]any)["type"] == "image" {
			t.Errorf("the plain assistant branch drops images, got %v", plain["content"])
		}
	}
	withTools := Context{Messages: []Message{{Assistant: &AssistantContent{
		API: AnthropicWire, Provider: "anthropic", Model: "claude-sonnet-4-5", StopReason: "tool_use",
		Parts: []ContentPart{
			{ToolCall: &ToolCall{ID: "t", Name: "f", Arguments: "{}"}},
			{Image: &ImagePart{Data: "aGk=", MediaType: "image/png"}},
		},
	}}}}
	kept := anthropicBody(t, anthropicModel(), withTools, AnthropicOptions{})["messages"].([]any)[0].(map[string]any)
	sawImage := false
	for _, block := range kept["content"].([]any) {
		if block.(map[string]any)["type"] == "image" {
			sawImage = true
		}
	}
	if !sawImage {
		t.Errorf("the branch carrying a tool use keeps its images, got %v", kept["content"])
	}
}

func TestAPartsUserMessageIsAlwaysABlockArray(t *testing.T) {
	ctx := Context{Messages: []Message{
		{User: &UserContent{Text: "first", HasText: true}},
		{User: &UserContent{UseParts: true, Parts: []ContentPart{
			{Text: &TextPart{Text: "one"}},
			{Text: &TextPart{Text: "two"}},
		}}},
	}}
	messages := anthropicBody(t, anthropicModel(), ctx, AnthropicOptions{})["messages"].([]any)
	parts := messages[1].(map[string]any)["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("got %d blocks, want the two text parts written as blocks", len(parts))
	}
	if parts[0].(map[string]any)["text"] != "one" || parts[1].(map[string]any)["text"] != "two" {
		t.Errorf("the blocks = %v", parts)
	}
	plain := messages[0].(map[string]any)["content"]
	if plain != "first" {
		t.Errorf("a message with no parts is still a plain string, got %v", plain)
	}
}

func TestAPartsUserMessageOfOnlyTextIsStillAnArray(t *testing.T) {
	ctx := Context{Messages: []Message{{User: &UserContent{UseParts: true, Parts: []ContentPart{
		{Text: &TextPart{Text: "only"}},
	}}}}}
	messages := anthropicBody(t, anthropicModel(), ctx, AnthropicOptions{})["messages"].([]any)
	content := messages[0].(map[string]any)["content"]
	if _, ok := content.([]any); !ok {
		t.Errorf("content = %v, want an array: the openai writer flattens these and this one does not", content)
	}
}

func TestAnEmptySystemPromptCountsAsAbsent(t *testing.T) {
	body := anthropicBody(t, anthropicModel(), Context{HasSystem: true, SystemPrompt: ""}, AnthropicOptions{})
	if _, present := body["system"]; present {
		t.Errorf("an empty prompt writes no system at all, got %v", body["system"])
	}
	oauth, _ := BuildAnthropicRequestBody(anthropicModel(), Context{HasSystem: true, SystemPrompt: ""}, AnthropicOptions{}, "sk-ant-oat01")
	text := systemTextOf(t, oauth)
	if text != oauthSystemText {
		t.Errorf("under oauth with no prompt the text = %q, want the bare sentence with no trailing blank line", text)
	}
}

func TestOneImagePartServesBothWriters(t *testing.T) {
	img := ImagePart{Data: "aGVsbG8=", MediaType: "image/png"}
	if got := img.DataURL(); got != "data:image/png;base64,aGVsbG8=" {
		t.Errorf("the data url = %q, want the mime and the base64 spliced in", got)
	}
	ctx := func() Context {
		return Context{Messages: []Message{{User: &UserContent{UseParts: true, Parts: []ContentPart{{Image: &img}}}}}}
	}
	openaiRaw := BuildRequestBody(openAIModel(), ctx(), StreamOptions{})
	var openaiParsed map[string]any
	if err := json.Unmarshal(openaiRaw, &openaiParsed); err != nil {
		t.Fatalf("the openai body is not json: %v", err)
	}
	openai := openaiParsed["messages"].([]any)[0].(map[string]any)
	openaiURL := openai["content"].([]any)[0].(map[string]any)["image_url"].(map[string]any)["url"]
	if openaiURL != "data:image/png;base64,aGVsbG8=" {
		t.Errorf("the openai writer = %v, want the data url built from the part", openaiURL)
	}
	anthropic := anthropicBody(t, anthropicModel(), ctx(), AnthropicOptions{})["messages"].([]any)[0].(map[string]any)
	source := anthropic["content"].([]any)[0].(map[string]any)["source"].(map[string]any)
	if source["media_type"] != "image/png" || source["data"] != "aGVsbG8=" {
		t.Errorf("the anthropic writer = %v, want the part's own two fields", source)
	}
}

func TestAReasoningLevelBecomesTheEffortAndBudgetThatWireTakes(t *testing.T) {
	cases := map[string]struct {
		effort  string
		budget  int
		enabled bool
	}{
		"":        {"", 0, false},
		"off":     {"", 0, false},
		"minimal": {"low", 256, true},
		"low":     {"low", 512, true},
		"medium":  {"medium", 1024, true},
		"high":    {"high", 2048, true},
		"xhigh":   {"max", 4096, true},
	}
	for level, want := range cases {
		got := AnthropicThinkingForLevel(level, nil)
		if got.ThinkingEnabled != want.enabled || got.ThinkingEffort != want.effort || got.ThinkingBudgetTokens != want.budget {
			t.Errorf("the level %q gives %+v, want enabled %v at effort %q and budget %d", level, got, want.enabled, want.effort, want.budget)
		}
	}
}

func TestABudgetForTheLevelWinsOverTheFallback(t *testing.T) {
	got := AnthropicThinkingForLevel("high", map[string]int{"high": 9000})
	if got.ThinkingBudgetTokens != 9000 {
		t.Errorf("a budget for the level gives %+v, want the caller's 9000 rather than the 2048 fallback", got)
	}
}

package provider

import "testing"

func boolPtr(v bool) *bool    { return &v }
func strPtr(v string) *string { return &v }

func hostModel(baseURL string) Model {
	return Model{Provider: "openai", BaseURL: baseURL, HasBaseURL: true, HasCompat: true}
}

func TestAQwenHostIsDashscopeAliyuncsDotComOrASubdomainOfIt(t *testing.T) {
	hosts := []string{
		"https://dashscope.aliyuncs.com",
		"https://coding-intl.dashscope.aliyuncs.com",
		"https://coding-intl.dashscope.aliyuncs.com/v1",
		"https://DASHSCOPE.ALIYUNCS.COM",
	}
	for _, url := range hosts {
		if !isQwenURL(url, true) {
			t.Errorf("isQwenURL(%q) = false, want true", url)
		}
	}

	notHosts := []string{
		"https://mydashscope.aliyuncs.com.attacker.example",
		"https://aliyuncs.com",
		"https://www.aliyuncs.com",
		"https://notdashscope.aliyuncs.com",
		"https://evil.example/?next=dashscope",
		"https://evil.example/v1/qwen",
		"https://gateway.example/proxy/dashscope.aliyuncs.com",
		"not a url at all",
	}
	for _, url := range notHosts {
		if isQwenURL(url, true) {
			t.Errorf("isQwenURL(%q) = true, want false: the domain is dashscope.aliyuncs.com, not aliyuncs.com, and the old rule matched the bare words dashscope and qwen anywhere", url)
		}
	}

	if isQwenURL("https://dashscope.aliyuncs.com", false) {
		t.Error("no base url is not a qwen host")
	}
}

func TestAnAnthropicHostIsAnthropicDotComOrASubdomainOfIt(t *testing.T) {
	hosts := []string{
		"https://api.anthropic.com",
		"https://api.anthropic.com/v1",
		"https://anthropic.com",
		"https://API.ANTHROPIC.COM",
	}
	for _, url := range hosts {
		if !isAnthropicURL(url, true) {
			t.Errorf("isAnthropicURL(%q) = false, want true", url)
		}
	}

	notHosts := []string{
		"https://myanthropic.com",
		"https://notanthropic.com",
		"https://anthropic.com.evil.example",
		"https://evil.example/?next=api.anthropic.com",
		"https://evil.example/v1/api.anthropic.com",
		"https://gateway.example/proxy/api.anthropic.com",
		"not a url at all",
	}
	for _, url := range notHosts {
		if isAnthropicURL(url, true) {
			t.Errorf("isAnthropicURL(%q) = true, want false: the name is in a host suffix, a path or a query", url)
		}
	}

	if isAnthropicURL("https://api.anthropic.com", false) {
		t.Error("no base url is not an anthropic host")
	}
}

func TestTheCataloguedAnthropicBaseStillGetsTheAnthropicCaps(t *testing.T) {
	const url = "https://api.anthropic.com"
	if got := DetectProviderType(url, true); got != ProviderAnthropic {
		t.Errorf("DetectProviderType(%q) = %q, want anthropic", url, got)
	}
	caps := DetectCapabilities(url, true)
	if caps.ProviderType != ProviderAnthropic {
		t.Errorf("caps provider type = %q, want anthropic", caps.ProviderType)
	}
	if !caps.ExtendedThinking || !caps.PromptCaching || !caps.Vision {
		t.Errorf("caps = %+v, want the anthropic set", caps)
	}
}

func TestAnOpenRouterHostIsOpenrouterDotAIOrASubdomainOfIt(t *testing.T) {
	hosts := []string{
		"https://openrouter.ai",
		"https://openrouter.ai/api/v1",
		"https://OPENROUTER.AI",
	}
	for _, url := range hosts {
		if !isOpenRouterURL(url, true) {
			t.Errorf("isOpenRouterURL(%q) = false, want true", url)
		}
	}

	notHosts := []string{
		"https://myopenrouter.ai",
		"https://notopenrouter.ai",
		"https://openrouter.ai.evil.example",
		"https://evil.example/?next=openrouter.ai",
		"https://evil.example/v1/openrouter.ai",
		"https://gateway.example/proxy/openrouter.ai",
		"not a url at all",
	}
	for _, url := range notHosts {
		if isOpenRouterURL(url, true) {
			t.Errorf("isOpenRouterURL(%q) = true, want false: the name is in a host suffix, a path or a query", url)
		}
	}

	if isOpenRouterURL("https://openrouter.ai", false) {
		t.Error("no base url is not an openrouter host")
	}
}

func TestADeepSeekHostIsDeepseekDotComOrASubdomainOfIt(t *testing.T) {
	hosts := []string{
		"https://api.deepseek.com",
		"https://api.deepseek.com/v1",
		"https://deepseek.com",
		"https://API.DEEPSEEK.COM",
	}
	for _, url := range hosts {
		if !isDeepSeekURL(url, true) {
			t.Errorf("isDeepSeekURL(%q) = false, want true", url)
		}
	}

	notHosts := []string{
		"https://mydeepseek.com",
		"https://notdeepseek.com",
		"https://deepseek.com.evil.example",
		"https://evil.example/?next=api.deepseek.com",
		"https://evil.example/v1/api.deepseek.com",
		"https://gateway.example/proxy/api.deepseek.com",
		"not a url at all",
	}
	for _, url := range notHosts {
		if isDeepSeekURL(url, true) {
			t.Errorf("isDeepSeekURL(%q) = true, want false: the name is in a host suffix, a path or a query", url)
		}
	}

	if isDeepSeekURL("https://api.deepseek.com", false) {
		t.Error("no base url is not a deepseek host")
	}
}

func TestAGitHubCopilotHostIsGithubcopilotDotComOrASubdomainOfIt(t *testing.T) {
	hosts := []string{
		"https://api.githubcopilot.com",
		"https://api.individual.githubcopilot.com",
		"https://api.acme.githubcopilot.com",
		"https://githubcopilot.com",
		"https://API.GITHUBCOPILOT.COM",
	}
	for _, url := range hosts {
		if !isGitHubCopilotURL(url, true) {
			t.Errorf("isGitHubCopilotURL(%q) = false, want true", url)
		}
	}

	notHosts := []string{
		"https://notgithubcopilot.com",
		"https://mygithubcopilot.com",
		"https://githubcopilot.com.attacker.test",
		"https://api.githubcopilot.com@attacker.test",
		"https://evil.example/?next=api.githubcopilot.com",
		"https://evil.example/v1/api.githubcopilot.com",
		"https://gateway.example/proxy/api.githubcopilot.com",
		"not a url at all",
	}
	for _, url := range notHosts {
		if isGitHubCopilotURL(url, true) {
			t.Errorf("isGitHubCopilotURL(%q) = true, want false: the name is in a host suffix, a path, a query or a userinfo section", url)
		}
	}

	if isGitHubCopilotURL("https://api.githubcopilot.com", false) {
		t.Error("no base url is not a github copilot host")
	}
}

func TestAChutesHostIsChutesDotAIOrASubdomainOfIt(t *testing.T) {
	hosts := []string{
		"https://api.chutes.ai",
		"https://api.chutes.ai/v1",
		"https://chutes.ai",
		"https://API.CHUTES.AI",
	}
	for _, url := range hosts {
		if !isChutesURL(url, true) {
			t.Errorf("isChutesURL(%q) = false, want true", url)
		}
	}

	notHosts := []string{
		"https://mychutes.ai",
		"https://notchutes.ai",
		"https://chutes.ai.evil.example",
		"https://evil.example/?next=chutes.ai",
		"https://evil.example/v1/chutes.ai",
		"https://gateway.example/proxy/chutes.ai",
		"not a url at all",
	}
	for _, url := range notHosts {
		if isChutesURL(url, true) {
			t.Errorf("isChutesURL(%q) = true, want false: the name is in a host suffix, a path or a query", url)
		}
	}

	if isChutesURL("https://api.chutes.ai", false) {
		t.Error("no base url is not a chutes host")
	}
}

func TestACerebrasHostIsCerebrasDotAIOrASubdomainOfIt(t *testing.T) {
	hosts := []string{
		"https://api.cerebras.ai",
		"https://api.cerebras.ai/v1",
		"https://cerebras.ai",
		"https://API.CEREBRAS.AI",
	}
	for _, url := range hosts {
		if !isCerebrasURL(url, true) {
			t.Errorf("isCerebrasURL(%q) = false, want true", url)
		}
	}

	notHosts := []string{
		"https://mycerebras.ai",
		"https://notcerebras.ai",
		"https://cerebras.ai.evil.example",
		"https://evil.example/?next=api.cerebras.ai",
		"https://evil.example/v1/api.cerebras.ai",
		"https://gateway.example/proxy/api.cerebras.ai",
		"not a url at all",
	}
	for _, url := range notHosts {
		if isCerebrasURL(url, true) {
			t.Errorf("isCerebrasURL(%q) = true, want false: the name is in a host suffix, a path or a query", url)
		}
	}

	if isCerebrasURL("https://api.cerebras.ai", false) {
		t.Error("no base url is not a cerebras host")
	}
}

func TestAGroqHostIsGroqDotComOrASubdomainOfIt(t *testing.T) {
	hosts := []string{
		"https://api.groq.com",
		"https://api.groq.com/openai/v1",
		"https://groq.com",
		"https://API.GROQ.COM",
	}
	for _, url := range hosts {
		if !isGroqURL(url, true) {
			t.Errorf("isGroqURL(%q) = false, want true", url)
		}
	}

	notHosts := []string{
		"https://mygroq.com",
		"https://notgroq.com",
		"https://groq.com.evil.example",
		"https://evil.example/?next=api.groq.com",
		"https://evil.example/v1/api.groq.com",
		"https://gateway.example/proxy/api.groq.com",
		"not a url at all",
	}
	for _, url := range notHosts {
		if isGroqURL(url, true) {
			t.Errorf("isGroqURL(%q) = true, want false: the name is in a host suffix, a path or a query", url)
		}
	}

	if isGroqURL("https://api.groq.com", false) {
		t.Error("no base url is not a groq host")
	}
}

func TestAMistralHostIsMistralDotAIOrASubdomainOfIt(t *testing.T) {
	hosts := []string{
		"https://api.mistral.ai",
		"https://api.mistral.ai/v1",
		"https://mistral.ai",
		"https://API.MISTRAL.AI",
	}
	for _, url := range hosts {
		if !isMistralURL(url, true) {
			t.Errorf("isMistralURL(%q) = false, want true", url)
		}
	}

	notHosts := []string{
		"https://mymistral.ai",
		"https://notmistral.ai",
		"https://mistral.ai.evil.example",
		"https://evil.example/?next=api.mistral.ai",
		"https://evil.example/v1/api.mistral.ai",
		"https://gateway.example/proxy/api.mistral.ai",
		"not a url at all",
	}
	for _, url := range notHosts {
		if isMistralURL(url, true) {
			t.Errorf("isMistralURL(%q) = true, want false: the name is in a host suffix, a path or a query", url)
		}
	}

	if isMistralURL("https://api.mistral.ai", false) {
		t.Error("no base url is not a mistral host")
	}
}

func TestAnOpenAIHostIsAHostEndingInOpenAIDotComOnALabelBoundary(t *testing.T) {
	hosts := []string{
		"https://api.openai.com",
		"https://api.openai.com/v1",
		"https://openai.com",
		"https://eu.openai.com",
		"https://OpenAI.com",
		"https://API.OPENAI.COM",
		"https://user:pass@api.openai.com/v1",
	}
	for _, url := range hosts {
		if !IsOpenAIHost(url, true) {
			t.Errorf("IsOpenAIHost(%q) = false, want true", url)
		}
		if got := DetectProviderType(url, true); got != ProviderOpenAINative {
			t.Errorf("DetectProviderType(%q) = %q, want openai_native", url, got)
		}
	}

	notHosts := []string{
		"https://myopenai.com",
		"https://notopenai.com",
		"https://openai.com.evil.example",
		"https://api.openai.com.evil.example",
		"https://evil.example/?next=api.openai.com",
		"https://evil.example/openai/api.openai.com/v1",
		"https://gateway.example/proxy/api.openai.com/v1/chat/completions",
		"not a url at all",
	}
	for _, url := range notHosts {
		if IsOpenAIHost(url, true) {
			t.Errorf("IsOpenAIHost(%q) = true, want false: the name is in a host suffix, a path or a query, not on a label boundary of the host", url)
		}
	}

	if IsOpenAIHost("https://api.openai.com", false) {
		t.Error("no base url is not an openai host")
	}
}

func TestIsOpenAIHostNeedsALabelBoundaryBeforeOpenAIDotCom(t *testing.T) {
	if IsOpenAIHost("https://notopenai.com", true) {
		t.Error("notopenai.com ends with openai.com but has no dot before it: it is not an openai host")
	}
	if !IsOpenAIHost("https://eu.openai.com", true) {
		t.Error("eu.openai.com has the dot boundary: it is an openai host")
	}
}

func TestAnAbsentBaseURLIsUnknownAndAUrlMatchingNothingIsOpenAICompatible(t *testing.T) {
	if got := DetectProviderType("", false); got != ProviderUnknown {
		t.Errorf("no base url = %q, want unknown", got)
	}
	if got := DetectProviderType("https://example.test/v1", true); got != ProviderOpenAICompat {
		t.Errorf("an unmatched url = %q, want openai_compatible: that is the default, not a miss", got)
	}
}

func TestDetectProviderTypeOrdersItsSubstringChain(t *testing.T) {
	cases := map[string]ProviderType{
		"https://api.anthropic.com":                        ProviderAnthropic,
		"https://api.openai.com":                           ProviderOpenAINative,
		"https://api.githubcopilot.com":                    ProviderOpenAICompat,
		"https://api.mistral.ai":                           ProviderOpenAICompat,
		"https://api.groq.com":                             ProviderOpenAICompat,
		"https://api.cerebras.ai":                          ProviderOpenAICompat,
		"https://api.zukijourney.com":                      ProviderOpenAICompat,
		"https://openrouter.ai/api":                        ProviderOpenAICompat,
		"https://generativelanguage.googleapis.com/v1beta": ProviderGoogle,
		"https://aiplatform.googleapis.com":                ProviderGoogle,
		"https://bedrock-runtime.us-east-1.amazonaws.com":  ProviderBedrock,
		"https://foo.openai.azure.com":                     ProviderAzure,
		"https://cognitiveservices.azure.com":              ProviderAzure,
		"http://localhost:11434":                           ProviderOllama,
		"http://127.0.0.1:11434":                           ProviderOllama,
		"http://myollama.example":                          ProviderOllama,
	}
	for baseURL, want := range cases {
		if got := DetectProviderType(baseURL, true); got != want {
			t.Errorf("DetectProviderType(%q) = %q, want %q", baseURL, got, want)
		}
	}
}

func TestOnlyTheOpenAINativeTypeCarriesTheNativeFlags(t *testing.T) {
	caps := DetectCapabilities("https://api.openai.com", true)
	if !caps.SupportsDeveloperRole || !caps.SupportsReasoningEffort || caps.MaxTokensField != MaxTokensCompletion {
		t.Errorf("the openai native caps = %+v, want the developer role, the effort and the completion token field", caps)
	}
	compat := DetectCapabilities("https://example.test/v1", true)
	if compat.SupportsDeveloperRole || compat.SupportsReasoningEffort {
		t.Errorf("a compatible host's caps = %+v, want neither native flag", compat)
	}
	if compat.MaxTokensField != MaxTokensPlain {
		t.Errorf("max tokens field = %q, want %q by default", compat.MaxTokensField, MaxTokensPlain)
	}
}

func TestPerHostRefinementsInsideTheCompatibleBranch(t *testing.T) {
	mistral := DetectCapabilities("https://api.mistral.ai", true)
	if !mistral.RequiresMistralToolIDs {
		t.Error("mistral wants mistral tool ids")
	}
	deep := DetectCapabilities("https://api.deepseek.com", true)
	if !deep.RequiresThinkingAsText {
		t.Error("deepseek wants thinking as text")
	}
	zai := DetectCapabilities("https://api.zukijourney.com", true)
	if zai.ThinkingFormat != ThinkingZai {
		t.Errorf("zai thinking format = %q, want zai", zai.ThinkingFormat)
	}
	qwen := DetectCapabilities("https://dashscope.aliyuncs.com", true)
	if qwen.ThinkingFormat != ThinkingQwen {
		t.Errorf("qwen thinking format = %q, want qwen", qwen.ThinkingFormat)
	}
	chutes := DetectCapabilities("https://api.chutes.ai", true)
	if chutes.MaxTokensField != MaxTokensPlain {
		t.Errorf("chutes max tokens field = %q, want the plain spelling", chutes.MaxTokensField)
	}
}

func TestTheDetectionGateDecidesWhetherTheNativeFlagsAreUsed(t *testing.T) {
	openai := MergeCompat(hostModel("https://api.openai.com"))
	if !openai.SupportsDeveloperRole || !openai.SupportsReasoningEffort || openai.MaxTokensField != MaxTokensCompletion {
		t.Errorf("an openai host merges to %+v, want the native values", openai)
	}
	loopback := MergeCompat(hostModel("http://127.0.0.1:8080/v1"))
	if loopback.SupportsDeveloperRole || loopback.SupportsReasoningEffort {
		t.Errorf("a loopback host merges to %+v, want the native values gated off", loopback)
	}
	if loopback.MaxTokensField != MaxTokensPlain {
		t.Errorf("a gated max tokens field = %q, want the plain spelling", loopback.MaxTokensField)
	}
}

func TestASpoofedHostIsDetectedCompatibleSoThereIsNoGateLeftToDiscard(t *testing.T) {
	const spoofed = "https://api.openai.com.evil.example"
	if got := DetectProviderType(spoofed, true); got != ProviderOpenAICompat {
		t.Fatalf("the spoofed url detects as %q, want openai_compatible: a name in a host suffix is not on a label boundary", got)
	}
	merged := MergeCompat(hostModel(spoofed))
	if merged.SupportsDeveloperRole || merged.SupportsReasoningEffort {
		t.Errorf("merged = %+v, want the native flags off", merged)
	}
	if merged.MaxTokensField != MaxTokensPlain {
		t.Errorf("max tokens field = %q, want the plain spelling", merged.MaxTokensField)
	}
	if merged.SupportsStore || merged.SupportsStrictMode {
		t.Errorf("merged = %+v, want store and strict off for a host that is not openai.com", merged)
	}
}

func TestAnOpenAIHostGetsTheNativeCapsFromDetectionAndFromTheMerge(t *testing.T) {
	const suffixHost = "https://proxy.openai.com"
	if !IsOpenAIHost(suffixHost, true) {
		t.Fatalf("%q is an openai host", suffixHost)
	}
	if got := DetectProviderType(suffixHost, true); got != ProviderOpenAINative {
		t.Fatalf("the suffix host detects as %q, want openai_native: detection and use now read the same predicate", got)
	}
	merged := MergeCompat(hostModel(suffixHost))
	if !merged.SupportsDeveloperRole || !merged.SupportsReasoningEffort {
		t.Errorf("merged = %+v, want the native flags: an openai.com subdomain is a host", merged)
	}
	if merged.MaxTokensField != MaxTokensCompletion {
		t.Errorf("max tokens field = %q, want the completion spelling", merged.MaxTokensField)
	}
	if !merged.SupportsStore || !merged.SupportsStrictMode {
		t.Errorf("merged = %+v, want store and strict on", merged)
	}
}

func TestANonGatedFlagReachesTheMergeWhateverTheHost(t *testing.T) {
	deep := MergeCompat(hostModel("https://api.deepseek.com"))
	if !deep.RequiresThinkingAsText {
		t.Error("requires_thinking_as_text is not gated: deepseek keeps it")
	}
	mistral := MergeCompat(hostModel("https://api.mistral.ai"))
	if !mistral.RequiresMistralToolIDs {
		t.Error("requires_mistral_tool_ids is not gated: mistral keeps it")
	}
}

func TestTheStoreAndStrictDefaultsRideTheHostRatherThanTheGate(t *testing.T) {
	openai := MergeCompat(hostModel("https://api.openai.com"))
	if !openai.SupportsStore || !openai.SupportsStrictMode {
		t.Errorf("an openai host = %+v, want store and strict on", openai)
	}
	loopback := MergeCompat(hostModel("http://127.0.0.1:8080/v1"))
	if loopback.SupportsStore {
		t.Error("store defaults to the host being openai, so a loopback is off")
	}
	if loopback.SupportsStrictMode {
		t.Error("strict mode defaults to the same gate, so a loopback is off")
	}
}

func TestAModelsOwnCompatWinsOverEveryDefault(t *testing.T) {
	model := hostModel("http://127.0.0.1:8080/v1")
	model.Compat = CompatOptions{
		SupportsStore:            boolPtr(true),
		SupportsDeveloperRole:    boolPtr(true),
		SupportsReasoningEffort:  boolPtr(false),
		SupportsUsageInStreaming: boolPtr(false),
		MaxTokensField:           MaxTokensCompletion,
		RequiresThinkingAsText:   boolPtr(true),
		ThinkingFormat:           ThinkingQwen,
		SupportsStrictMode:       boolPtr(true),
	}
	merged := MergeCompat(model)
	if !merged.SupportsStore || !merged.SupportsDeveloperRole {
		t.Errorf("the model's own store and developer role = %v, %v, want both true", merged.SupportsStore, merged.SupportsDeveloperRole)
	}
	if merged.SupportsReasoningEffort {
		t.Error("the model asked for no reasoning effort, so the merge must not grant it")
	}
	if merged.SupportsUsageInStreaming {
		t.Error("the model asked for no usage in streaming, so the merge must not send it")
	}
	if merged.MaxTokensField != MaxTokensCompletion {
		t.Errorf("max tokens field = %q, want the model's own spelling", merged.MaxTokensField)
	}
	if !merged.RequiresThinkingAsText || merged.ThinkingFormat != ThinkingQwen {
		t.Errorf("thinking = %v, %q, want the model's own", merged.RequiresThinkingAsText, merged.ThinkingFormat)
	}
	if !merged.SupportsStrictMode {
		t.Error("the model asked for strict mode, so the merge must grant it")
	}
}

func TestAProxyIsTransparentOnlyWhenAllThreeFlagsAreSet(t *testing.T) {
	full := hostModel("http://proxy.test")
	full.Compat = CompatOptions{SupportsStore: boolPtr(true), SupportsDeveloperRole: boolPtr(true), SupportsReasoningEffort: boolPtr(true)}
	if !IsTransparentOpenAIProxy(full) {
		t.Fatal("all three set on an openai model is a transparent proxy")
	}
	if merged := MergeCompat(full); !merged.SupportsDeveloperRole || !merged.SupportsStrictMode {
		t.Errorf("a transparent proxy merges to %+v, want the gate open", merged)
	}
	partial := hostModel("http://proxy.test")
	partial.Compat = CompatOptions{SupportsStore: boolPtr(true), SupportsDeveloperRole: boolPtr(true)}
	if IsTransparentOpenAIProxy(partial) {
		t.Error("two of the three is not a transparent proxy: all three or nothing")
	}
	other := full
	other.Provider = "azure"
	if IsTransparentOpenAIProxy(other) {
		t.Error("the provider must be openai for the proxy to be transparent")
	}
}

func TestAModelWithNoCompatAtAllIsNotATransparentProxy(t *testing.T) {
	model := Model{Provider: "openai", BaseURL: "http://proxy.test", HasBaseURL: true}
	if IsTransparentOpenAIProxy(model) {
		t.Error("no compat block means no flags, so not a transparent proxy")
	}
}

func TestAnonymityNeedsBothTheModelFlagAndAVendorOutsideTheFour(t *testing.T) {
	yes := Model{Provider: "ollama", AllowsAnonymous: true}
	if !AllowsAnonymous(yes) {
		t.Error("ollama is outside the four named vendors, so an anonymous model is allowed")
	}
	no := Model{Provider: "ollama"}
	if AllowsAnonymous(no) {
		t.Error("without the model's own flag, nothing is anonymous")
	}
	for _, vendor := range []string{"openai", "deepseek", "kimi", "github-copilot"} {
		if AllowsAnonymous(Model{Provider: vendor, AllowsAnonymous: true}) {
			t.Errorf("%s is named as a vendor that must not be anonymous", vendor)
		}
	}
}

func TestUsageInStreamingDefaultsOnAndMergesEverythingElse(t *testing.T) {
	merged := MergeCompat(hostModel("http://127.0.0.1:8080/v1"))
	if !merged.SupportsUsageInStreaming {
		t.Error("usage in streaming defaults on")
	}
	if merged.ThinkingFormat != ThinkingOpenAI {
		t.Errorf("thinking format = %q, want the openai default", merged.ThinkingFormat)
	}
}

package provider

import "testing"

func boolPtr(v bool) *bool    { return &v }
func strPtr(v string) *string { return &v }

func hostModel(baseURL string) Model {
	return Model{Provider: "openai", BaseURL: baseURL, HasBaseURL: true, HasCompat: true}
}

func TestTheTwoOpenAIPredicatesAreNotTheSameFunction(t *testing.T) {
	cases := []struct {
		url    string
		native bool
		isHost bool
	}{
		{"https://api.openai.com", true, true},
		{"https://myopenai.com", false, false},
		{"https://api.openai.com.evil.example", true, false},
		{"https://evil.example/?next=api.openai.com", true, false},
		{"https://OpenAI.com", false, true},
		{"https://API.OPENAI.COM", false, true},
		{"https://proxy.openai.com", false, true},
		{"not a url at all", false, false},
	}
	for _, c := range cases {
		if got := IsOpenAINativeURL(c.url, true); got != c.native {
			t.Errorf("IsOpenAINativeURL(%q) = %v, want %v", c.url, got, c.native)
		}
		if got := IsOpenAIHost(c.url, true); got != c.isHost {
			t.Errorf("IsOpenAIHost(%q) = %v, want %v", c.url, got, c.isHost)
		}
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

func TestTheGateIsWhatDiscardsDetectedCapsForASpoofedHost(t *testing.T) {
	const spoofed = "https://api.openai.com.evil.example"
	if DetectProviderType(spoofed, true) != ProviderOpenAINative {
		t.Fatalf("the spoofed url detects as %q, want openai_native: detection reads the substring", DetectProviderType(spoofed, true))
	}
	merged := MergeCompat(hostModel(spoofed))
	if merged.SupportsDeveloperRole || merged.SupportsReasoningEffort {
		t.Errorf("merged = %+v, want the native flags discarded: the host is not openai.com", merged)
	}
	if merged.MaxTokensField != MaxTokensPlain {
		t.Errorf("max tokens field = %q, want the detection's completion field discarded for the plain one", merged.MaxTokensField)
	}
	if merged.SupportsStore || merged.SupportsStrictMode {
		t.Errorf("merged = %+v, want store and strict off for a host that is not openai.com", merged)
	}
}

func TestAHostThatIsOpenAIKeepsTheCapsDetectionFoundByTheSubstring(t *testing.T) {
	const suffixHost = "https://proxy.openai.com"
	if !IsOpenAIHost(suffixHost, true) {
		t.Fatalf("%q is an openai host", suffixHost)
	}
	if DetectProviderType(suffixHost, true) != ProviderOpenAICompat {
		t.Fatalf("the suffix host detects as %q, want openai_compatible: no api.openai.com substring", DetectProviderType(suffixHost, true))
	}
	merged := MergeCompat(hostModel(suffixHost))
	if merged.MaxTokensField != MaxTokensPlain {
		t.Errorf("max tokens field = %q, want the plain spelling: the gate is open but detection found no native caps", merged.MaxTokensField)
	}
	if !merged.SupportsStore || !merged.SupportsStrictMode {
		t.Errorf("merged = %+v, want store and strict on: both read the host directly, not the gate", merged)
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

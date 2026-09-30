package provider

import (
	"net/url"
	"strings"
)

type ProviderType string

const (
	ProviderUnknown      ProviderType = "unknown"
	ProviderAnthropic    ProviderType = "anthropic"
	ProviderOpenAINative ProviderType = "openai_native"
	ProviderOpenAICompat ProviderType = "openai_compatible"
	ProviderGoogle       ProviderType = "google"
	ProviderBedrock      ProviderType = "bedrock"
	ProviderAzure        ProviderType = "azure"
	ProviderOllama       ProviderType = "ollama"
)

type Capabilities struct {
	ExtendedThinking            bool
	PromptCaching               bool
	Vision                      bool
	FunctionCalling             bool
	RequiresMistralToolIDs      bool
	SupportsReasoningEffort     bool
	SupportsDeveloperRole       bool
	RequiresThinkingAsText      bool
	RequiresToolResultName      bool
	RequiresToolResultAssistant bool
	MaxTokensField              string
	ThinkingFormat              ThinkingFormat
	ProviderType                ProviderType
}

type ThinkingFormat string

const (
	ThinkingOpenAI ThinkingFormat = "openai"
	ThinkingZai    ThinkingFormat = "zai"
	ThinkingQwen   ThinkingFormat = "qwen"
)

const (
	MaxTokensCompletion = "max_completion_tokens"
	MaxTokensPlain      = "max_tokens"
)

type CompatOptions struct {
	SupportsStore                    *bool
	SupportsDeveloperRole            *bool
	SupportsReasoningEffort          *bool
	SupportsUsageInStreaming         *bool
	MaxTokensField                   string
	RequiresToolResultName           *bool
	RequiresAssistantAfterToolResult *bool
	RequiresThinkingAsText           *bool
	RequiresMistralToolIDs           *bool
	ThinkingFormat                   ThinkingFormat
	SupportsStrictMode               *bool
	SupportsAnthropicCacheTTL        *bool
}

type MergedCompat struct {
	SupportsStore                    bool
	SupportsDeveloperRole            bool
	SupportsReasoningEffort          bool
	SupportsUsageInStreaming         bool
	MaxTokensField                   string
	RequiresToolResultName           bool
	RequiresAssistantAfterToolResult bool
	RequiresThinkingAsText           bool
	RequiresMistralToolIDs           bool
	ThinkingFormat                   ThinkingFormat
	SupportsStrictMode               bool
}

type Model struct {
	ID              string
	API             string
	Provider        string
	BaseURL         string
	HasBaseURL      bool
	Compat          CompatOptions
	HasCompat       bool
	AllowsAnonymous bool
	Reasoning       bool
	MaxTokens       int
	Cost            Cost
}

func isGitHubCopilotURL(baseURL string, hasBaseURL bool) bool {
	return isHostOrSubdomain(baseURL, hasBaseURL, "githubcopilot.com")
}

func hostOf(baseURL string, hasBaseURL bool) string {
	if !hasBaseURL {
		return ""
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

func hostIsOrSubdomainOf(host, domain string) bool {
	return host == domain || (len(host) > len(domain) && strings.HasSuffix(host, domain) && host[len(host)-len(domain)-1] == '.')
}

func isHostOrSubdomain(baseURL string, hasBaseURL bool, domain string) bool {
	host := hostOf(baseURL, hasBaseURL)
	return hostIsOrSubdomainOf(host, strings.ToLower(domain))
}

var bedrockFirstLabels = []string{"bedrock", "bedrock-runtime", "bedrock-fips", "bedrock-runtime-fips"}
var awsParents = []string{"amazonaws.com", "amazonaws.com.cn"}

func isBedrockURL(baseURL string, hasBaseURL bool) bool {
	host := hostOf(baseURL, hasBaseURL)
	dot := strings.IndexByte(host, '.')
	if dot < 0 {
		return false
	}
	label := host[:dot]
	for _, candidate := range bedrockFirstLabels {
		if label != candidate {
			continue
		}
		for _, parent := range awsParents {
			if hostIsOrSubdomainOf(host, parent) {
				return true
			}
		}
	}
	return false
}

func isMistralURL(baseURL string, hasBaseURL bool) bool {
	return isHostOrSubdomain(baseURL, hasBaseURL, "mistral.ai")
}

func isGroqURL(baseURL string, hasBaseURL bool) bool {
	return isHostOrSubdomain(baseURL, hasBaseURL, "groq.com")
}

func isCerebrasURL(baseURL string, hasBaseURL bool) bool {
	return isHostOrSubdomain(baseURL, hasBaseURL, "cerebras.ai")
}

func isOllamaURL(baseURL string, hasBaseURL bool) bool {
	if !hasBaseURL {
		return false
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	if parsed.Port() != "11434" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	for _, candidate := range []string{"localhost", "127.0.0.1", "::1"} {
		if host == candidate {
			return true
		}
	}
	return false
}

var azureLabels = []string{"openai.azure.com", "cognitiveservices.azure.com"}

func isAzureURL(baseURL string, hasBaseURL bool) bool {
	for _, label := range azureLabels {
		if isHostOrSubdomain(baseURL, hasBaseURL, label) {
			return true
		}
	}
	return false
}

func isGoogleURL(baseURL string, hasBaseURL bool) bool {
	host := hostOf(baseURL, hasBaseURL)
	if host == "generativelanguage.googleapis.com" || host == "aiplatform.googleapis.com" {
		return true
	}
	const suffix = "-aiplatform.googleapis.com"
	if len(host) > len(suffix) && strings.HasSuffix(host, suffix) {
		return !strings.Contains(host[:len(host)-len(suffix)], ".")
	}
	return false
}

func isZaiURL(baseURL string, hasBaseURL bool) bool {
	return isHostOrSubdomain(baseURL, hasBaseURL, "zukijourney.com")
}

func isOpenRouterURL(baseURL string, hasBaseURL bool) bool {
	return isHostOrSubdomain(baseURL, hasBaseURL, "openrouter.ai")
}

func isChutesURL(baseURL string, hasBaseURL bool) bool {
	return isHostOrSubdomain(baseURL, hasBaseURL, "chutes.ai")
}

func isQwenURL(baseURL string, hasBaseURL bool) bool {
	return isHostOrSubdomain(baseURL, hasBaseURL, "dashscope.aliyuncs.com") ||
		isHostOrSubdomain(baseURL, hasBaseURL, "dashscope-intl.aliyuncs.com")
}

func isDeepSeekURL(baseURL string, hasBaseURL bool) bool {
	return isHostOrSubdomain(baseURL, hasBaseURL, "deepseek.com")
}

func isAnthropicURL(baseURL string, hasBaseURL bool) bool {
	return isHostOrSubdomain(baseURL, hasBaseURL, "anthropic.com")
}

func IsOpenAIHost(baseURL string, hasBaseURL bool) bool {
	if !hasBaseURL {
		return false
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	return strings.EqualFold(host, "openai.com") ||
		(len(host) > len("openai.com") &&
			strings.EqualFold(host[len(host)-len("openai.com"):], "openai.com") &&
			host[len(host)-len("openai.com")-1] == '.')
}

func DetectProviderType(baseURL string, hasBaseURL bool) ProviderType {
	if !hasBaseURL {
		return ProviderUnknown
	}
	switch {
	case isAnthropicURL(baseURL, hasBaseURL):
		return ProviderAnthropic
	case IsOpenAIHost(baseURL, hasBaseURL):
		return ProviderOpenAINative
	case isGitHubCopilotURL(baseURL, hasBaseURL),
		isMistralURL(baseURL, hasBaseURL),
		isGroqURL(baseURL, hasBaseURL),
		isCerebrasURL(baseURL, hasBaseURL),
		isZaiURL(baseURL, hasBaseURL),
		isOpenRouterURL(baseURL, hasBaseURL):
		return ProviderOpenAICompat
	case isGoogleURL(baseURL, hasBaseURL):
		return ProviderGoogle
	case isBedrockURL(baseURL, hasBaseURL):
		return ProviderBedrock
	case isAzureURL(baseURL, hasBaseURL):
		return ProviderAzure
	case isOllamaURL(baseURL, hasBaseURL):
		return ProviderOllama
	}
	if len(baseURL) > 0 {
		return ProviderOpenAICompat
	}
	return ProviderUnknown
}

func DetectCapabilities(baseURL string, hasBaseURL bool) Capabilities {
	switch DetectProviderType(baseURL, hasBaseURL) {
	case ProviderUnknown:
		return Capabilities{FunctionCalling: true, MaxTokensField: MaxTokensPlain, ThinkingFormat: ThinkingOpenAI, ProviderType: ProviderUnknown}
	case ProviderAnthropic, ProviderGoogle, ProviderBedrock, ProviderAzure:
		return Capabilities{
			ExtendedThinking: true, PromptCaching: true, Vision: true, FunctionCalling: true,
			MaxTokensField: MaxTokensPlain, ThinkingFormat: ThinkingOpenAI,
			ProviderType: DetectProviderType(baseURL, hasBaseURL),
		}
	case ProviderOpenAINative:
		caps := Capabilities{
			ExtendedThinking: true, PromptCaching: true, Vision: true, FunctionCalling: true,
			SupportsReasoningEffort: true, SupportsDeveloperRole: true,
			MaxTokensField: MaxTokensCompletion, ThinkingFormat: ThinkingOpenAI,
			ProviderType: ProviderOpenAINative,
		}
		if isZaiURL(baseURL, hasBaseURL) {
			caps.ThinkingFormat = ThinkingZai
		}
		return caps
	case ProviderOllama:
		return Capabilities{
			Vision: true, FunctionCalling: true,
			MaxTokensField: MaxTokensPlain, ThinkingFormat: ThinkingOpenAI,
			ProviderType: ProviderOllama,
		}
	}
	caps := Capabilities{
		Vision: true, FunctionCalling: true,
		MaxTokensField: MaxTokensPlain, ThinkingFormat: ThinkingOpenAI,
		ProviderType: ProviderOpenAICompat,
	}
	if isMistralURL(baseURL, hasBaseURL) {
		caps.RequiresMistralToolIDs = true
	}
	if isMistralURL(baseURL, hasBaseURL) || isChutesURL(baseURL, hasBaseURL) {
		caps.MaxTokensField = MaxTokensPlain
	}
	if isZaiURL(baseURL, hasBaseURL) {
		caps.ThinkingFormat = ThinkingZai
	}
	if isQwenURL(baseURL, hasBaseURL) {
		caps.ThinkingFormat = ThinkingQwen
	}
	if isDeepSeekURL(baseURL, hasBaseURL) {
		caps.SupportsReasoningEffort = true
	}
	return caps
}

func IsTransparentOpenAIProxy(model Model) bool {
	if model.Provider != "openai" {
		return false
	}
	if !model.HasCompat {
		return false
	}
	compat := model.Compat
	return compat.SupportsStore != nil && *compat.SupportsStore &&
		compat.SupportsDeveloperRole != nil && *compat.SupportsDeveloperRole &&
		compat.SupportsReasoningEffort != nil && *compat.SupportsReasoningEffort
}

var OpenAIAnonymousBlocked = []string{"openai", "deepseek", "kimi", "github-copilot"}

var AnthropicAnonymousBlocked = []string{"anthropic", "deepseek"}

func AllowsAnonymousWith(model Model, blocked []string) bool {
	if !model.AllowsAnonymous {
		return false
	}
	for _, vendor := range blocked {
		if model.Provider == vendor {
			return false
		}
	}
	return true
}

func AllowsAnonymous(model Model) bool {
	return AllowsAnonymousWith(model, OpenAIAnonymousBlocked)
}

func MergeCompat(model Model) MergedCompat {
	caps := DetectCapabilities(model.BaseURL, model.HasBaseURL)
	compat := model.Compat
	isOpenAINative := IsOpenAIHost(model.BaseURL, model.HasBaseURL)
	honorsNativeCaps := isOpenAINative || IsTransparentOpenAIProxy(model)

	detectedDeveloperRole := false
	detectedReasoningEffort := false
	detectedMaxTokensField := MaxTokensPlain
	if honorsNativeCaps {
		detectedDeveloperRole = caps.SupportsDeveloperRole
		detectedReasoningEffort = caps.SupportsReasoningEffort
		detectedMaxTokensField = caps.MaxTokensField
	}
	if isDeepSeekURL(model.BaseURL, model.HasBaseURL) {
		detectedReasoningEffort = caps.SupportsReasoningEffort
	}

	merged := MergedCompat{
		SupportsStore:                    isOpenAINative,
		SupportsDeveloperRole:            detectedDeveloperRole,
		SupportsReasoningEffort:          detectedReasoningEffort,
		SupportsUsageInStreaming:         true,
		MaxTokensField:                   detectedMaxTokensField,
		RequiresToolResultName:           caps.RequiresToolResultName,
		RequiresAssistantAfterToolResult: caps.RequiresToolResultAssistant,
		RequiresThinkingAsText:           caps.RequiresThinkingAsText,
		RequiresMistralToolIDs:           caps.RequiresMistralToolIDs,
		ThinkingFormat:                   caps.ThinkingFormat,
		SupportsStrictMode:               honorsNativeCaps,
	}
	if model.HasCompat {
		if compat.SupportsStore != nil {
			merged.SupportsStore = *compat.SupportsStore
		}
		if compat.SupportsDeveloperRole != nil {
			merged.SupportsDeveloperRole = *compat.SupportsDeveloperRole
		}
		if compat.SupportsReasoningEffort != nil {
			merged.SupportsReasoningEffort = *compat.SupportsReasoningEffort
		}
		if compat.SupportsUsageInStreaming != nil {
			merged.SupportsUsageInStreaming = *compat.SupportsUsageInStreaming
		}
		if compat.MaxTokensField != "" {
			merged.MaxTokensField = compat.MaxTokensField
		}
		if compat.RequiresToolResultName != nil {
			merged.RequiresToolResultName = *compat.RequiresToolResultName
		}
		if compat.RequiresAssistantAfterToolResult != nil {
			merged.RequiresAssistantAfterToolResult = *compat.RequiresAssistantAfterToolResult
		}
		if compat.RequiresThinkingAsText != nil {
			merged.RequiresThinkingAsText = *compat.RequiresThinkingAsText
		}
		if compat.RequiresMistralToolIDs != nil {
			merged.RequiresMistralToolIDs = *compat.RequiresMistralToolIDs
		}
		if compat.ThinkingFormat != "" {
			merged.ThinkingFormat = compat.ThinkingFormat
		}
		if compat.SupportsStrictMode != nil {
			merged.SupportsStrictMode = *compat.SupportsStrictMode
		}
	}
	return merged
}

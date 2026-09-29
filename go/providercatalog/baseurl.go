package providercatalog

import "strings"

const GlobalBaseURLEnv = "OAPX_BASE_URL"

var VersionedWires = []string{"anthropic-messages", "openai-completions", "openai-responses"}

type BaseOverrides struct {
	Global     string
	Anthropic  string
	OpenAI     string
	DeepSeek   string
	KimiRegion string
}

func BaseOverridesFromEnv(env []EnvironmentValue) BaseOverrides {
	region := ""
	if value := firstEnvValue(env, "KIMI_REGION"); value != "" {
		region = NormalizeKimiRegion(value)
	}
	return BaseOverrides{
		Global:     firstEnvValue(env, GlobalBaseURLEnv),
		Anthropic:  firstEnvValue(env, "ANTHROPIC_BASE_URL"),
		OpenAI:     firstEnvValue(env, "OPENAI_BASE_URL"),
		DeepSeek:   firstEnvValue(env, "DEEPSEEK_BASE_URL"),
		KimiRegion: region,
	}
}

func firstEnvValue(env []EnvironmentValue, name string) string {
	for _, held := range env {
		if held.Name == name {
			return held.Value
		}
	}
	return ""
}

func NormalizeKimiRegion(value string) string {
	trimmed := strings.Trim(value, " \t\r\n")
	if trimmed == "global" {
		return "global"
	}
	if trimmed == "china" {
		return "china"
	}
	return ""
}

func NormalizeVersionedBaseURL(url string) string {
	trimmed := strings.TrimRight(url, "/")
	if strings.HasSuffix(trimmed, "/v1") {
		return trimmed[:len(trimmed)-len("/v1")]
	}
	return trimmed
}

func UsesVersionedRoute(providerID, wire string) bool {
	if providerID == "github-copilot" {
		return false
	}
	for _, candidate := range VersionedWires {
		if wire == candidate {
			return true
		}
	}
	return false
}

func BaseURLWithOverrides(overrides BaseOverrides, providerID, wire string) string {
	if overrides.Global != "" {
		if UsesVersionedRoute(providerID, wire) {
			return NormalizeVersionedBaseURL(overrides.Global)
		}
		return overrides.Global
	}
	switch {
	case providerID == "anthropic" && wire == "anthropic-messages":
		if overrides.Anthropic != "" {
			return NormalizeVersionedBaseURL(overrides.Anthropic)
		}
		return ""
	case providerID == "openai" && wire == "openai-completions":
		if overrides.OpenAI != "" {
			return NormalizeVersionedBaseURL(overrides.OpenAI)
		}
		return ""
	case providerID == "deepseek" && wire == "openai-completions":
		if overrides.DeepSeek != "" {
			return NormalizeVersionedBaseURL(overrides.DeepSeek)
		}
		return ""
	}
	return ""
}

func ResolveBaseURL(catalog Catalog, env []EnvironmentValue, id, wire, region string) (string, bool) {
	overrides := BaseOverridesFromEnv(env)
	if override := BaseURLWithOverrides(overrides, id, wire); override != "" {
		return override, true
	}
	if id == "kimi" && wire == "openai-completions" {
		if overrides.KimiRegion != "" {
			region = overrides.KimiRegion
		}
	}
	return BaseURL(catalog, id, wire, region)
}

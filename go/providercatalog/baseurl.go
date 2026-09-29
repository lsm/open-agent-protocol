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

func BaseOverridesFromEnv(catalog Catalog, env []EnvironmentValue) BaseOverrides {
	return BaseOverrides{
		Global:     firstEnvValue(env, GlobalBaseURLEnv),
		Anthropic:  firstEnvValue(env, FirstBaseURLEnv(catalog, "anthropic")),
		OpenAI:     firstEnvValue(env, FirstBaseURLEnv(catalog, "openai")),
		DeepSeek:   firstEnvValue(env, FirstBaseURLEnv(catalog, "deepseek")),
		KimiRegion: NormalizeKimiRegion(firstEnvValue(env, RegionEnv(catalog, "kimi"))),
	}
}

func FirstBaseURLEnv(catalog Catalog, id string) string {
	names := BaseURLEnv(catalog, id)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func firstEnvValue(env []EnvironmentValue, name string) string {
	if name == "" {
		return ""
	}
	for _, held := range env {
		if held.Name == name {
			return held.Value
		}
	}
	return ""
}

func EqualFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		left, right := a[i], b[i]
		if left == right {
			continue
		}
		if left >= 'A' && left <= 'Z' {
			left += 'a' - 'A'
		}
		if right >= 'A' && right <= 'Z' {
			right += 'a' - 'A'
		}
		if left != right {
			return false
		}
	}
	return true
}

func NormalizeKimiRegion(value string) string {
	trimmed := strings.Trim(value, " \t\r\n")
	switch {
	case EqualFoldASCII(trimmed, "global"), EqualFoldASCII(trimmed, "moonshot"):
		return "global"
	case EqualFoldASCII(trimmed, "china"), EqualFoldASCII(trimmed, "cn"), EqualFoldASCII(trimmed, "coding"):
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
		return strings.TrimRight(overrides.Global, "/")
	}
	switch ProviderArm(providerID, wire) {
	case "anthropic":
		if overrides.Anthropic != "" {
			return NormalizeVersionedBaseURL(overrides.Anthropic)
		}
		return ""
	case "openai":
		if overrides.OpenAI != "" {
			return NormalizeVersionedBaseURL(overrides.OpenAI)
		}
		return ""
	case "deepseek":
		if overrides.DeepSeek != "" {
			return NormalizeVersionedBaseURL(overrides.DeepSeek)
		}
		return ""
	}
	return ""
}

func ProviderArm(providerID, wire string) string {
	switch {
	case providerID == "anthropic" && wire == "anthropic-messages":
		return "anthropic"
	case providerID == "openai" && (wire == "openai-completions" || wire == "openai-responses"):
		return "openai"
	case providerID == "deepseek" && wire == "openai-completions":
		return "deepseek"
	case providerID == "openai-codex" && wire == "openai-codex-responses":
		return "openai-codex"
	case providerID == "kimi" && wire == "openai-completions":
		return "kimi"
	}
	return ""
}

func KimiRegion(overrides BaseOverrides, stored string) string {
	if overrides.KimiRegion != "" {
		return overrides.KimiRegion
	}
	if normalized := NormalizeKimiRegion(stored); normalized != "" {
		return normalized
	}
	return "china"
}

func ResolveBaseURL(catalog Catalog, env []EnvironmentValue, id, wire, region string) (string, bool) {
	overrides := BaseOverridesFromEnv(catalog, env)
	if override := BaseURLWithOverrides(overrides, id, wire); override != "" {
		return override, true
	}
	if ProviderArm(id, wire) == "kimi" {
		return BaseURL(catalog, id, wire, KimiRegion(overrides, region))
	}
	return BaseURL(catalog, id, wire, "")
}

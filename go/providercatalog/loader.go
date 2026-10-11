package providercatalog

import "strings"

var CatalogLoaderRows = []string{
	"deepseek",
	"openrouter",
	"opencode-zen",
	"opencode-go",
	"vercel",
	"zenmux",
	"deepinfra",
	"zai-coding-plan",
	"alibaba-coding-plan",
	"minimax-coding-plan",
	"tencent-coding-plan",
	"volcengine-coding-plan",
	"openai",
	"kimi",
	"xiaomi-token-plan-cn",
	"xiaomi-token-plan-sgp",
	"xiaomi-token-plan-ams",
	"xiaomi",
}

var LoaderWires = []string{"anthropic-messages", "openai-completions", "openai-responses", "ollama"}

func ServedByCatalogLoader(id string) bool {
	for _, row := range CatalogLoaderRows {
		if row == id {
			return true
		}
	}
	return false
}

func LoaderOrder(catalog Catalog) []string {
	ordered := make([]string, 0, len(CatalogLoaderRows))
	for _, row := range catalog.Providers {
		if row.Offering == "coding_plan" && ServedByCatalogLoader(row.ID) {
			ordered = append(ordered, row.ID)
		}
	}
	for _, row := range catalog.Providers {
		if row.Offering != "coding_plan" && ServedByCatalogLoader(row.ID) {
			ordered = append(ordered, row.ID)
		}
	}
	return ordered
}

func OverriddenWire(id string) string {
	if id == "deepseek" {
		return "openai-completions"
	}
	return ""
}

func RegionFromValue(catalog Catalog, id, value string) string {
	trimmed := strings.Trim(value, " \t\r\n")
	if trimmed == "" {
		return ""
	}
	if id == "kimi" {
		if normalized := NormalizeKimiRegion(trimmed); normalized != "" {
			return normalized
		}
	}
	for _, endpoint := range Endpoints(catalog, id) {
		if endpoint.Region != "" && EqualFoldASCII(endpoint.Region, trimmed) {
			return endpoint.Region
		}
	}
	return ""
}

func ResolveRegion(catalog Catalog, env []EnvironmentValue, id string) string {
	if name := RegionEnv(catalog, id); name != "" {
		if region := RegionFromValue(catalog, id, firstEnvValue(env, name)); region != "" {
			return region
		}
	}
	return DefaultRegion(catalog, id)
}

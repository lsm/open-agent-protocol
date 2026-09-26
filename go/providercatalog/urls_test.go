package providercatalog

import (
	"testing"

	"github.com/lsm/open-agent-protocol/providers"
)

func TestResolveMatchesTheURLsTheTablePins(t *testing.T) {
	pinned := []Resolved{
		{"openai", "openai-completions", "", "https://api.openai.com", "https://api.openai.com/v1/models", "https://api.openai.com/v1/chat/completions"},
		{"openai", "openai-responses", "", "https://api.openai.com", "https://api.openai.com/v1/models", "https://api.openai.com/v1/responses"},
		{"anthropic", "anthropic-messages", "", "https://api.anthropic.com", "https://api.anthropic.com/v1/models", "https://api.anthropic.com/v1/messages"},
		{"opencode", "openai-completions", "", "https://opencode.ai/zen/v1", "https://opencode.ai/zen/v1/models", "https://opencode.ai/zen/v1/chat/completions"},
		{"openrouter", "openai-completions", "", "https://openrouter.ai/api/v1", "https://openrouter.ai/api/v1/models", "https://openrouter.ai/api/v1/chat/completions"},
		{"deepseek", "openai-completions", "", "https://api.deepseek.com", "https://api.deepseek.com/v1/models", "https://api.deepseek.com/v1/chat/completions"},
		{"zai-coding-plan", "openai-completions", "", "https://api.z.ai/api/coding/paas/v4", "https://api.z.ai/api/coding/paas/v4/models", "https://api.z.ai/api/coding/paas/v4/chat/completions"},
		{"kimi", "openai-completions", "china", "https://api.kimi.com/coding", "https://api.kimi.com/coding/v1/models", "https://api.kimi.com/coding/v1/chat/completions"},
		{"kimi", "openai-completions", "global", "https://api.moonshot.ai", "https://api.moonshot.ai/v1/models", "https://api.moonshot.ai/v1/chat/completions"},
		{"alibaba-coding-plan", "openai-completions", "", "https://coding-intl.dashscope.aliyuncs.com/v1", "https://coding-intl.dashscope.aliyuncs.com/v1/models", "https://coding-intl.dashscope.aliyuncs.com/v1/chat/completions"},
		{"minimax-coding-plan", "anthropic-messages", "", "https://api.minimax.io/anthropic/v1", "https://api.minimax.io/anthropic/v1/models", "https://api.minimax.io/anthropic/v1/messages"},
		{"tencent-coding-plan", "openai-completions", "", "https://api.lkeap.cloud.tencent.com/coding/v3", "https://api.lkeap.cloud.tencent.com/coding/v3/models", "https://api.lkeap.cloud.tencent.com/coding/v3/chat/completions"},
		{"volcengine-coding-plan", "openai-completions", "", "https://ark.cn-beijing.volces.com/api/coding/v3", "https://ark.cn-beijing.volces.com/api/coding/v3/models", "https://ark.cn-beijing.volces.com/api/coding/v3/chat/completions"},
		{"openai-codex", "openai-codex-responses", "", "https://chatgpt.com/backend-api/codex", "", "https://chatgpt.com/backend-api/codex/responses"},
		{"xiaomi-token-plan-cn", "openai-completions", "", "https://token-plan-cn.xiaomimimo.com/v1", "https://token-plan-cn.xiaomimimo.com/v1/models", "https://token-plan-cn.xiaomimimo.com/v1/chat/completions"},
		{"xiaomi-token-plan-sgp", "openai-completions", "", "https://token-plan-sgp.xiaomimimo.com/v1", "https://token-plan-sgp.xiaomimimo.com/v1/models", "https://token-plan-sgp.xiaomimimo.com/v1/chat/completions"},
		{"xiaomi-token-plan-ams", "openai-completions", "", "https://token-plan-ams.xiaomimimo.com/v1", "https://token-plan-ams.xiaomimimo.com/v1/models", "https://token-plan-ams.xiaomimimo.com/v1/chat/completions"},
		{"deepinfra", "openai-completions", "", "https://api.deepinfra.com/v1/openai", "https://api.deepinfra.com/v1/openai/models", "https://api.deepinfra.com/v1/openai/chat/completions"},
		{"xiaomi", "openai-completions", "", "https://api.xiaomimimo.com/v1", "https://api.xiaomimimo.com/v1/models", "https://api.xiaomimimo.com/v1/chat/completions"},
		{"vercel", "openai-completions", "", "https://ai-gateway.vercel.sh/v1", "https://ai-gateway.vercel.sh/v1/models", "https://ai-gateway.vercel.sh/v1/chat/completions"},
		{"zenmux", "openai-completions", "", "https://zenmux.ai/api/v1", "https://zenmux.ai/api/v1/models", "https://zenmux.ai/api/v1/chat/completions"},
		{"google", "google-generative-ai", "", "https://generativelanguage.googleapis.com", "", ""},
	}
	catalog, err := Load(providers.Files)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	resolved := Resolve(catalog)
	if len(resolved) != len(pinned) {
		t.Fatalf("resolved %d endpoints, want %d", len(resolved), len(pinned))
	}
	for index, want := range pinned {
		if resolved[index] != want {
			t.Fatalf("endpoint %d = %+v, want %+v", index, resolved[index], want)
		}
	}
}

func TestModelsURLAndRequestURLResolveNothingTheCatalogDoesNotHold(t *testing.T) {
	catalog, err := Load(providers.Files)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, absent := range []struct{ id, wire, region string }{
		{id: "ollama", wire: "ollama"},
		{id: "github-copilot", wire: "openai-completions"},
		{id: "azure", wire: "openai-responses"},
		{id: "google", wire: "google-generative-ai"},
		{id: "kimi", wire: "openai-completions"},
		{id: "kimi", wire: "openai-completions", region: "mars"},
		{id: "openai", wire: "no-such-wire"},
		{id: "no-such-provider", wire: "openai-completions"},
	} {
		if url := RequestURL(catalog, absent.id, absent.wire, absent.region); url != "" {
			t.Fatalf("request url for %s on %s = %q, want none", absent.id, absent.wire, url)
		}
	}
	for _, absent := range []struct{ id, region string }{
		{id: "ollama"},
		{id: "github-copilot"},
		{id: "azure"},
		{id: "google"},
		{id: "kimi"},
		{id: "kimi", region: "mars"},
		{id: "no-such-provider"},
	} {
		if url := ModelsURL(catalog, absent.id, absent.region); url != "" {
			t.Fatalf("models url for %s in %q = %q, want none", absent.id, absent.region, url)
		}
	}
	if url := RequestURL(catalog, "kimi", "openai-completions", "china"); url != "https://api.kimi.com/coding/v1/chat/completions" {
		t.Fatalf("kimi china request url = %q", url)
	}
	if url := ModelsURL(catalog, "kimi", "global"); url != "https://api.moonshot.ai/v1/models" {
		t.Fatalf("kimi global models url = %q", url)
	}
}

func TestARequestURLIsTheBaseAndItsWirePath(t *testing.T) {
	catalog := Catalog{Providers: []Provider{{
		ID:         "row",
		ModelsPath: "/models",
		Endpoints: []Endpoint{
			{Wire: "openai-completions", BaseURL: "https://api.example.com/coding/v4", Region: "versioned"},
			{Wire: "anthropic-messages", BaseURL: "https://api.example.com/anthropic/v1", Region: "messages"},
			{Wire: "openai-completions", BaseURL: "https://api.example.com", Region: "bare"},
			{Wire: "openai-completions", BaseURL: "https://api.example.com/v1/", Region: "trailing"},
			{Wire: "openai-completions", BaseURL: "https://api.example.com/v1/openai", Region: "rooted"},
			{Wire: "ollama", BaseURL: "http://localhost:11434", Region: "local"},
			{Wire: "openai-responses", BaseURL: "https://api.example.com", Region: "responses"},
		},
	}}}
	for _, want := range []struct{ region, request, models string }{
		{region: "versioned", request: "https://api.example.com/coding/v4/chat/completions", models: "https://api.example.com/coding/v4/models"},
		{region: "messages", request: "https://api.example.com/anthropic/v1/messages", models: "https://api.example.com/anthropic/v1/models"},
		{region: "bare", request: "https://api.example.com/v1/chat/completions", models: "https://api.example.com/models"},
		{region: "trailing", request: "https://api.example.com/v1/chat/completions", models: "https://api.example.com/v1//models"},
		{region: "rooted", request: "https://api.example.com/v1/openai/chat/completions", models: "https://api.example.com/v1/openai/models"},
		{region: "local", request: "http://localhost:11434/api/chat", models: "http://localhost:11434/models"},
	} {
		if got := RequestURL(catalog, "row", wireForRegion(want.region), want.region); got != want.request {
			t.Fatalf("%s request = %q, want %q", want.region, got, want.request)
		}
		if got := ModelsURL(catalog, "row", want.region); got != want.models {
			t.Fatalf("%s models = %q, want %q", want.region, got, want.models)
		}
	}
	if got := RequestURL(catalog, "row", "openai-responses", "responses"); got != "https://api.example.com/v1/responses" {
		t.Fatalf("responses request = %q", got)
	}
}

func wireForRegion(region string) string {
	switch region {
	case "messages":
		return "anthropic-messages"
	case "local":
		return "ollama"
	default:
		return "openai-completions"
	}
}

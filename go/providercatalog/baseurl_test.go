package providercatalog

import "testing"

func envOf(pairs ...string) []EnvironmentValue {
	held := make([]EnvironmentValue, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		held = append(held, EnvironmentValue{Name: pairs[i], Value: pairs[i+1]})
	}
	return held
}

func TestTheGlobalOverrideRedirectsAnyProviderToTheLoopback(t *testing.T) {
	overrides := BaseOverridesFromEnv(envOf(GlobalBaseURLEnv, "http://127.0.0.1:9/v1"))
	for _, id := range []string{"openai", "anthropic", "deepseek", "kimi"} {
		wire := "openai-completions"
		if id == "anthropic" {
			wire = "anthropic-messages"
		}
		got := BaseURLWithOverrides(overrides, id, wire)
		if got != "http://127.0.0.1:9" {
			t.Errorf("%s = %q, want the loopback with the version suffix stripped", id, got)
		}
	}
}

func TestTheGlobalOverrideIsUntouchedForAnUnversionedRoute(t *testing.T) {
	overrides := BaseOverridesFromEnv(envOf(GlobalBaseURLEnv, "http://127.0.0.1:9/v1"))
	if got := BaseURLWithOverrides(overrides, "github-copilot", "openai-completions"); got != "http://127.0.0.1:9/v1" {
		t.Errorf("got %q, want the raw value: github-copilot is excluded from the versioned route", got)
	}
}

func TestTheGlobalOverrideWinsOverThePerProviderOne(t *testing.T) {
	overrides := BaseOverridesFromEnv(envOf(
		GlobalBaseURLEnv, "http://global.test",
		"OPENAI_BASE_URL", "http://openai.test",
	))
	if got := BaseURLWithOverrides(overrides, "openai", "openai-completions"); got != "http://global.test" {
		t.Errorf("got %q, want the global to win", got)
	}
}

func TestAPerProviderOverrideOnlyReachesItsOwnProviderAndWire(t *testing.T) {
	overrides := BaseOverridesFromEnv(envOf("ANTHROPIC_BASE_URL", "http://anthropic.test"))
	if got := BaseURLWithOverrides(overrides, "anthropic", "anthropic-messages"); got != "http://anthropic.test" {
		t.Errorf("got %q, want the anthropic override", got)
	}
	if got := BaseURLWithOverrides(overrides, "openai", "openai-completions"); got != "" {
		t.Errorf("got %q, want nothing: the anthropic variable does not reach openai", got)
	}
	if got := BaseURLWithOverrides(overrides, "anthropic", "openai-completions"); got != "" {
		t.Errorf("got %q, want nothing: the wire has to match too", got)
	}
	if got := BaseURLWithOverrides(overrides, "openai", "anthropic-messages"); got != "" {
		t.Errorf("got %q, want nothing: a matching wire on the wrong provider is still no override", got)
	}
	if got := BaseURLWithOverrides(overrides, "kimi", "anthropic-messages"); got != "" {
		t.Errorf("got %q, want nothing", got)
	}
}

func TestTheVersionSuffixIsStrippedAndTrailingSlashesTrimmed(t *testing.T) {
	cases := map[string]string{
		"http://a.test/v1":    "http://a.test",
		"http://a.test/":      "http://a.test",
		"http://a.test///":    "http://a.test",
		"http://a.test":       "http://a.test",
		"http://a.test/v1///": "http://a.test",
		"":                    "",
	}
	for in, want := range cases {
		if got := NormalizeVersionedBaseURL(in); got != want {
			t.Errorf("NormalizeVersionedBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnEmptyOverrideIsNoOverride(t *testing.T) {
	overrides := BaseOverridesFromEnv(envOf(GlobalBaseURLEnv, "", "OPENAI_BASE_URL", ""))
	if got := BaseURLWithOverrides(overrides, "openai", "openai-completions"); got != "" {
		t.Errorf("got %q, want nothing: a variable that is set but empty is not an override", got)
	}
}

func TestTheKimiRegionComesFromTheEnvironmentAndAnUnknownOneIsIgnored(t *testing.T) {
	if got := NormalizeKimiRegion(" global "); got != "global" {
		t.Errorf("got %q, want global", got)
	}
	if got := NormalizeKimiRegion("elsewhere"); got != "" {
		t.Errorf("got %q, want nothing: only two regions are recognised", got)
	}
	overrides := BaseOverridesFromEnv(envOf("KIMI_REGION", "elsewhere"))
	if overrides.KimiRegion != "" {
		t.Errorf("got %q, want an unusable region to be dropped", overrides.KimiRegion)
	}
}

func TestTheKimiRegionPicksTheCatalogEndpoint(t *testing.T) {
	catalog := heldCatalog(t)
	url, ok := ResolveBaseURL(catalog, envOf("KIMI_REGION", "global"), "kimi", "openai-completions", "china")
	if !ok || url != "https://api.moonshot.ai" {
		t.Errorf("kimi with a global region = %q, want the global endpoint", url)
	}
	fallback, ok := ResolveBaseURL(catalog, nil, "kimi", "openai-completions", "china")
	if !ok || fallback != "https://api.kimi.com/coding" {
		t.Errorf("kimi with no region = %q, want the china endpoint", fallback)
	}
}

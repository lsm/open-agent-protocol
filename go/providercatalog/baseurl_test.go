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
	overrides := BaseOverridesFromEnv(heldCatalog(t), envOf(GlobalBaseURLEnv, "http://127.0.0.1:9/v1"))
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

func TestTheGlobalOverrideKeepsItsPathButLosesItsSlashesForAnUnversionedRoute(t *testing.T) {
	overrides := BaseOverridesFromEnv(heldCatalog(t), envOf(GlobalBaseURLEnv, "http://127.0.0.1:9/proxy/"))
	if got := BaseURLWithOverrides(overrides, "github-copilot", "openai-completions"); got != "http://127.0.0.1:9/proxy" {
		t.Errorf("got %q, want the path kept and the trailing slash trimmed: the unversioned branch trims, it does not normalise the version away", got)
	}
	overrides = BaseOverridesFromEnv(heldCatalog(t), envOf(GlobalBaseURLEnv, "http://127.0.0.1:9///"))
	if got := BaseURLWithOverrides(overrides, "github-copilot", "openai-completions"); got != "http://127.0.0.1:9" {
		t.Errorf("got %q, want every trailing slash gone", got)
	}
}

func TestTheGlobalOverrideWinsOverThePerProviderOne(t *testing.T) {
	overrides := BaseOverridesFromEnv(heldCatalog(t), envOf(
		GlobalBaseURLEnv, "http://global.test",
		"OPENAI_BASE_URL", "http://openai.test",
	))
	if got := BaseURLWithOverrides(overrides, "openai", "openai-completions"); got != "http://global.test" {
		t.Errorf("got %q, want the global to win", got)
	}
}

func TestAPerProviderOverrideOnlyReachesItsOwnProviderAndWire(t *testing.T) {
	overrides := BaseOverridesFromEnv(heldCatalog(t), envOf("ANTHROPIC_BASE_URL", "http://anthropic.test"))
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
	overrides := BaseOverridesFromEnv(heldCatalog(t), envOf(GlobalBaseURLEnv, "", "OPENAI_BASE_URL", ""))
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
	overrides := BaseOverridesFromEnv(heldCatalog(t), envOf("KIMI_REGION", "elsewhere"))
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

func TestTheOpenAIOverrideReachesTheResponsesWireAsWellAsCompletions(t *testing.T) {
	overrides := BaseOverridesFromEnv(heldCatalog(t), envOf("OPENAI_BASE_URL", "http://both.test"))
	for _, wire := range []string{"openai-completions", "openai-responses"} {
		if got := BaseURLWithOverrides(overrides, "openai", wire); got != "http://both.test" {
			t.Errorf("openai on %s = %q, want the override: a responses model is reachable through WireForModel", wire, got)
		}
	}
	if got := BaseURLWithOverrides(overrides, "openai", "openai-codex-responses"); got != "" {
		t.Errorf("got %q, want nothing: the codex wire is a different provider", got)
	}
}

func TestTheKimiRegionTakesItsAliasesAndIgnoresCase(t *testing.T) {
	cases := map[string]string{
		"global": "global", "GLOBAL": "global", " moonshot ": "global", "Moonshot": "global",
		"china": "china", "CN": "china", "coding": "china", " Coding\n": "china",
		"elsewhere": "", "": "", "globalish": "", "moonſhot": "",
	}
	for in, want := range cases {
		if got := NormalizeKimiRegion(in); got != want {
			t.Errorf("NormalizeKimiRegion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnAliasInTheEnvironmentReachesTheOtherEndpoint(t *testing.T) {
	catalog := heldCatalog(t)
	for _, value := range []string{"moonshot", "GLOBAL", "global"} {
		url, ok := ResolveBaseURL(catalog, envOf("KIMI_REGION", value), "kimi", "openai-completions", "china")
		if !ok || url != "https://api.moonshot.ai" {
			t.Errorf("KIMI_REGION=%q resolved to %q, want the global endpoint", value, url)
		}
	}
}

func TestKimiFallsBackToChinaWhenNeitherTheEnvironmentNorTheCallerIsUsable(t *testing.T) {
	catalog := heldCatalog(t)
	for _, stored := range []string{"", "elsewhere", "   "} {
		url, ok := ResolveBaseURL(catalog, nil, "kimi", "openai-completions", stored)
		if !ok || url != "https://api.kimi.com/coding" {
			t.Errorf("kimi with a stored region of %q = %q, want the china endpoint: an unusable region is not a refusal", stored, url)
		}
	}
}

func TestAStoredKimiRegionIsNormalisedOnItsWayToTheCatalog(t *testing.T) {
	catalog := heldCatalog(t)
	url, ok := ResolveBaseURL(catalog, nil, "kimi", "openai-completions", "moonshot")
	if !ok || url != "https://api.moonshot.ai" {
		t.Errorf("kimi with a stored region of moonshot = %q, want the global endpoint", url)
	}
}

func TestTheCatalogFallbackForAKnownPairIgnoresTheRegion(t *testing.T) {
	catalog := heldCatalog(t)
	cases := map[string]string{
		"openai":       "openai-completions",
		"anthropic":    "anthropic-messages",
		"deepseek":     "openai-completions",
		"openai-codex": "openai-codex-responses",
	}
	for id, wire := range cases {
		if _, ok := ResolveBaseURL(catalog, nil, id, wire, ""); !ok {
			t.Fatalf("%s on %s did not resolve with no region", id, wire)
		}
		if _, ok := ResolveBaseURL(catalog, nil, id, wire, "a-region-nobody-declares"); !ok {
			t.Errorf("%s on %s stopped resolving once a region was passed, want the catalog's own endpoint: only kimi is region-selected", id, wire)
		}
	}
}

func TestAnUnknownPairResolvesWithoutARegionToo(t *testing.T) {
	catalog := heldCatalog(t)
	for _, region := range []string{"", "a-region-nobody-declares"} {
		if _, ok := ResolveBaseURL(catalog, nil, "openrouter", "openai-completions", region); !ok {
			t.Errorf("openrouter with a region of %q did not resolve, want the row's own endpoint: the region is kimi's alone", region)
		}
	}
}

func TestTheOverrideNamesComeFromTheCatalogRowAndNotFromASpellinGo(t *testing.T) {
	catalog := heldCatalog(t)
	renamed := catalog
	renamed.Providers = append([]Provider(nil), catalog.Providers...)
	for i, row := range renamed.Providers {
		switch row.ID {
		case "openai":
			renamed.Providers[i].BaseURLEnv = []string{"A_DIFFERENT_NAME"}
		case "kimi":
			renamed.Providers[i].RegionEnv = "A_DIFFERENT_REGION_NAME"
		}
	}
	overrides := BaseOverridesFromEnv(renamed, envOf("A_DIFFERENT_NAME", "http://renamed.test", "OPENAI_BASE_URL", "http://stale.test"))
	if got := BaseURLWithOverrides(overrides, "openai", "openai-completions"); got != "http://renamed.test" {
		t.Errorf("got %q, want the name the row records: a catalog rename must not strand the Go tree", got)
	}
	regionOverrides := BaseOverridesFromEnv(renamed, envOf("A_DIFFERENT_REGION_NAME", "moonshot", "KIMI_REGION", "china"))
	if regionOverrides.KimiRegion != "global" {
		t.Errorf("the region = %q, want global: the region variable's name comes from the row too", regionOverrides.KimiRegion)
	}
	if got := FirstBaseURLEnv(catalog, "openai"); got != "OPENAI_BASE_URL" {
		t.Errorf("got %q, want the row's own name", got)
	}
	if got := FirstBaseURLEnv(catalog, "a-row-that-has-none"); got != "" {
		t.Errorf("got %q, want nothing for a row recording no variable", got)
	}
	if got := RegionEnv(catalog, "kimi"); got != "KIMI_REGION" {
		t.Errorf("got %q, want the row's own region variable", got)
	}
}

func TestTheRegionFoldsAsciiOnlyAndNotUnicode(t *testing.T) {
	if got := NormalizeKimiRegion("MOONSHOT"); got != "global" {
		t.Errorf("MOONSHOT = %q, want global: ascii folding is what both trees do", got)
	}
	if got := NormalizeKimiRegion("Coding"); got != "china" {
		t.Errorf("Coding = %q, want china", got)
	}
	for _, value := range []string{"moonſhot", "globalſ", "CHİNA", "glob\u0131al"} {
		if got := NormalizeKimiRegion(value); got != "" {
			t.Errorf("%q = %q, want nothing: std.ascii.eqlIgnoreCase folds ASCII only, and a value one tree takes and the other rejects is a credential sent to a different endpoint", value, got)
		}
	}
}

func TestEqualFoldASCIIIsNotUnicodeFolding(t *testing.T) {
	if !EqualFoldASCII("CN", "cn") || !EqualFoldASCII("cn", "Cn") {
		t.Error("ascii letters must fold")
	}
	if EqualFoldASCII("cn", "china") {
		t.Error("a prefix is not a fold")
	}
	if EqualFoldASCII("moonſhot", "moonshot") {
		t.Error("the long s must not fold: it is two bytes and outside ASCII")
	}
}

func TestTheKimiFallbackIsTheRowsOwnDefaultRegionAndNotASpellingInGo(t *testing.T) {
	catalog := heldCatalog(t)
	if got := DefaultRegion(catalog, "kimi"); got != "china" {
		t.Errorf("the real catalog's kimi default_region = %q, want china", got)
	}
	if got := DefaultKimiRegion(catalog); got != "china" {
		t.Errorf("got %q, want the row's own value", got)
	}
	renamed := catalog
	renamed.Providers = append([]Provider(nil), catalog.Providers...)
	for i, row := range renamed.Providers {
		if row.ID == "kimi" {
			renamed.Providers[i].DefaultRegion = "global"
		}
	}
	if got := DefaultKimiRegion(renamed); got != "global" {
		t.Errorf("got %q, want global: the fallback is the row's, so editing the catalog moves it", got)
	}
	url, ok := ResolveBaseURL(renamed, nil, "kimi", "openai-completions", "")
	if !ok || url != "https://api.moonshot.ai" {
		t.Errorf("kimi with a default_region of global = %q, want the global endpoint", url)
	}
}

func TestARowNamingNoDefaultRegionFallsBackToChina(t *testing.T) {
	empty := Catalog{Providers: []Provider{{
		ID: "kimi",
		Endpoints: []Endpoint{
			{Wire: "openai-completions", Region: "china", BaseURL: "http://china.test"},
			{Wire: "openai-completions", Region: "global", BaseURL: "http://global.test"},
		},
	}}}
	if got := DefaultRegion(empty, "kimi"); got != "" {
		t.Errorf("got %q, want nothing for a row recording none", got)
	}
	if got := DefaultKimiRegion(empty); got != "china" {
		t.Errorf("got %q, want china: a row that names no default is answered as the source's own literal does", got)
	}
	url, ok := ResolveBaseURL(empty, nil, "kimi", "openai-completions", "")
	if !ok || url != "http://china.test" {
		t.Errorf("got %q, ok=%v, want the china endpoint", url, ok)
	}
}

func TestAKnownRegionStillOutranksTheDefault(t *testing.T) {
	catalog := heldCatalog(t)
	for _, value := range []string{"moonshot", "GLOBAL", "coding", "cn"} {
		url, ok := ResolveBaseURL(catalog, nil, "kimi", "openai-completions", value)
		want := "https://api.kimi.com/coding"
		if value == "moonshot" || value == "GLOBAL" {
			want = "https://api.moonshot.ai"
		}
		if !ok || url != want {
			t.Errorf("a stored region of %q = %q, want %q: the default is a fallback, not an override", value, url, want)
		}
	}
}

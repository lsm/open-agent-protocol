package providercatalog

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lsm/open-agent-protocol/providers"
)

func TestABaseTheCatalogDoesNotHoldReadsTheTrailingV1AClientSupplies(t *testing.T) {
	catalog, err := Load(providers.Files)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, base := range []string{"http://host:8000/v1", "https://proxy.example/v1/"} {
		if !BaseCarriesTrailingVersion(base) {
			t.Errorf("%q carries a trailing v1", base)
		}
		if !CarriesVersionFor(catalog, "gateway", base, nil) {
			t.Errorf("%q resolves to no version without being stated", base)
		}
	}
	for _, base := range []string{"https://proxy.example", "https://gw.test/api/coding/paas/v4", "https://api.deepinfra.com/v1/openai", ""} {
		if BaseCarriesTrailingVersion(base) {
			t.Errorf("%q carries a trailing v1", base)
		}
	}
	denied := false
	if CarriesVersionFor(catalog, "gateway", "http://host:8000/v1", &denied) {
		t.Error("a stated false must win over the trailing v1")
	}
	if !CarriesVersionFor(catalog, "openrouter", "https://openrouter.ai/api/v1", nil) {
		t.Error("a catalogued base resolves from its own endpoint")
	}
}

func TestAListingAndARequestAgreeOnAVersionedBaseWithNoFact(t *testing.T) {
	catalog, err := Load(providers.Files)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	path, ok := wirePaths["openai-completions"]
	if !ok {
		t.Fatal("the catalog holds no openai-completions wire")
	}
	const base = "http://host:8000/v1"
	listing := ModelsURLForBase(base, "/models", false, true)
	request := RequestURLForStatedBase(catalog, "gateway", base, nil, path)
	if want := "http://host:8000/v1/chat/completions"; request != want {
		t.Errorf("request = %q, want %q", request, want)
	}
	if !strings.HasPrefix(listing, "http://host:8000/v1/") {
		t.Errorf("listing = %q, want it under the same version", listing)
	}

	stated := false
	doubled := RequestURLForStatedBase(catalog, "gateway", base, &stated, path)
	if want := "http://host:8000/v1/v1/chat/completions"; doubled != want {
		t.Errorf("stated false request = %q, want %q", doubled, want)
	}
}

func TestCarriesVersionReadsTheRowsOwnEndpointAndNothingElse(t *testing.T) {
	catalog, err := Load(providers.Files)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !CarriesVersion(catalog, "openrouter", "https://openrouter.ai/api/v1") {
		t.Error("openrouter records carries_version on its endpoint")
	}
	if !CarriesVersion(catalog, "openrouter", "https://openrouter.ai/api/v1/") {
		t.Error("a trailing slash must not change which endpoint is matched")
	}
	if !CarriesVersion(catalog, "deepinfra", "https://api.deepinfra.com/v1/openai") {
		t.Error("deepinfra's version is not the last segment but the row records it")
	}
	for _, id := range []string{"deepseek", "kimi", "ollama", "no-such-provider"} {
		if CarriesVersion(catalog, id, "https://api.openai.com/v1") {
			t.Errorf("%s records carries_version for a base it does not hold", id)
		}
	}
	if CarriesVersion(catalog, "openrouter", "https://proxy.example/api/v1") {
		t.Error("an override must never match the catalogued base and inherit the fact")
	}

	stated := true
	if !CarriesVersionFor(catalog, "openrouter", "https://proxy.example/api/v1", &stated) {
		t.Error("a stated fact must win over the catalog")
	}
	denied := false
	if CarriesVersionFor(catalog, "openrouter", "https://openrouter.ai/api/v1", &denied) {
		t.Error("a stated false must win over the catalog")
	}
	if !CarriesVersionFor(catalog, "openrouter", "https://openrouter.ai/api/v1", nil) {
		t.Error("an unstated fact must resolve from the catalog")
	}
}

func TestAListingAndItsRequestAgreeUnderAnOverride(t *testing.T) {
	catalog, err := Load(providers.Files)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, id := range []string{"openrouter", "vercel", "zenmux", "opencode-zen", "deepinfra", "deepseek", "anthropic"} {
		provider, known := findProvider(catalog, id)
		if !known {
			t.Fatalf("no row %s", id)
		}
		endpoint := provider.Endpoints[0]
		path, joined := wirePaths[endpoint.Wire]
		if !joined || path.modelScoped {
			continue
		}
		overridden := "https://proxy.example"
		listing := ModelsURLForBase(overridden, provider.ModelsPath, endpoint.CarriesVersion, true)
		request := RequestURLForBase(overridden, path, false)
		if !strings.HasPrefix(listing, overridden+"/v1/") {
			t.Errorf("%s listing under an override = %s, want it under the version", id, listing)
		}
		if !strings.HasPrefix(request, overridden+"/v1/") {
			t.Errorf("%s request under an override = %s, want it under the version", id, request)
		}
		if strings.Count(strings.TrimPrefix(listing, overridden), "v1") != strings.Count(strings.TrimPrefix(request, overridden), "v1") {
			t.Errorf("%s listing %s and request %s disagree about the version", id, listing, request)
		}
	}
}

func TestResolveMatchesTheURLsTheSharedFilePins(t *testing.T) {
	catalog, err := Load(providers.Files)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	pinned, err := LoadPinned(providers.Files)
	if err != nil {
		t.Fatalf("load pinned: %v", err)
	}
	if findings := CheckPinned(catalog, pinned); len(findings) != 0 {
		t.Fatalf("findings = %v, want none", findings)
	}
	resolved := Resolve(catalog)
	if len(resolved) != len(pinned.Endpoints) {
		t.Fatalf("resolved %d endpoints, want %d", len(resolved), len(pinned.Endpoints))
	}
	for index, row := range resolved {
		want := pinned.Endpoints[index]
		if row.ID != want.ID || row.Wire != want.Wire || row.Region != want.Region ||
			row.BaseURL != want.BaseURL || row.ModelsURL != want.ModelsURL || row.RequestURL != want.RequestURL {
			t.Fatalf("endpoint %d = %+v, want %+v", index, row, want)
		}
	}
}

func TestCheckPinnedNamesAStaleRow(t *testing.T) {
	catalog, err := Load(providers.Files)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	pinned, err := LoadPinned(providers.Files)
	if err != nil {
		t.Fatalf("load pinned: %v", err)
	}
	moved := providercatalogPinnedCopy(pinned)
	moved.Endpoints[0].RequestURL = "https://moved.example.com/v1/chat/completions"
	findings := CheckPinned(catalog, moved)
	if len(findings) != 1 || findings[0].Code != CodeStaleURLs {
		t.Fatalf("findings = %v, want one stale row", findings)
	}
	if !strings.Contains(findings[0].Detail, "catalog-urls") {
		t.Fatalf("finding does not name the command that regenerates: %s", findings[0].Detail)
	}
	shortened := providercatalogPinnedCopy(pinned)
	shortened.Endpoints = shortened.Endpoints[:len(shortened.Endpoints)-1]
	findings = CheckPinned(catalog, shortened)
	if len(findings) != 1 || findings[0].Code != CodeStaleURLs {
		t.Fatalf("findings = %v, want one stale length", findings)
	}
}

func TestLoadPinnedRefusesAnUnknownMemberAndAnEmptyFile(t *testing.T) {
	files := func(body string) fstest.MapFS {
		return fstest.MapFS{PinnedFile: &fstest.MapFile{Data: []byte(body)}}
	}
	if _, err := LoadPinned(files(`{"endpoints":[{"id":"kimi","wire":"openai-completions","base_url":"https://api.kimi.com/coding","surprise":1}]}`)); err == nil {
		t.Fatal("a member the file does not declare was accepted")
	}
	if _, err := LoadPinned(files(`{"endpoints":[]}`)); err == nil {
		t.Fatal("a file pinning no endpoint was accepted")
	}
}

func providercatalogPinnedCopy(pinned PinnedCatalog) PinnedCatalog {
	out := PinnedCatalog{Endpoints: make([]Pinned, len(pinned.Endpoints))}
	copy(out.Endpoints, pinned.Endpoints)
	return out
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
		if url, ok := RequestURL(catalog, absent.id, absent.wire, absent.region); ok || url != "" {
			t.Fatalf("request url for %s on %s = %q, found = %t, want none", absent.id, absent.wire, url, ok)
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
		if url, ok := ModelsURL(catalog, absent.id, absent.region); ok || url != "" {
			t.Fatalf("models url for %s in %q = %q, found = %t, want none", absent.id, absent.region, url, ok)
		}
	}
	if url, ok := RequestURL(catalog, "kimi", "openai-completions", "china"); !ok || url != "https://api.kimi.com/coding/v1/chat/completions" {
		t.Fatalf("kimi china request url = %q, found = %t", url, ok)
	}
	if url, ok := ModelsURL(catalog, "kimi", "global"); !ok || url != "https://api.moonshot.ai/v1/models" {
		t.Fatalf("kimi global models url = %q", url)
	}
}

func TestARequestURLIsTheBaseAndItsWirePath(t *testing.T) {
	catalog := Catalog{Providers: []Provider{{
		ID:         "row",
		ModelsPath: "/models",
		Endpoints: []Endpoint{
			{Wire: "openai-completions", BaseURL: "https://api.example.com/coding/v4", Region: "versioned", CarriesVersion: true},
			{Wire: "anthropic-messages", BaseURL: "https://api.example.com/anthropic/v1", Region: "messages", CarriesVersion: true},
			{Wire: "openai-completions", BaseURL: "https://api.example.com", Region: "bare"},
			{Wire: "openai-completions", BaseURL: "https://api.example.com/v1/", Region: "trailing", CarriesVersion: true},
			{Wire: "openai-completions", BaseURL: "https://api.example.com/v1/openai", Region: "rooted", CarriesVersion: true},
			{Wire: "ollama", BaseURL: "http://localhost:11434", Region: "local"},
			{Wire: "openai-responses", BaseURL: "https://api.example.com", Region: "responses"},
		},
	}}}
	for _, want := range []struct{ region, request, models string }{
		{region: "versioned", request: "https://api.example.com/coding/v4/chat/completions", models: "https://api.example.com/coding/v4/models"},
		{region: "messages", request: "https://api.example.com/anthropic/v1/messages", models: "https://api.example.com/anthropic/v1/models"},
		{region: "bare", request: "https://api.example.com/v1/chat/completions", models: "https://api.example.com/models"},
		{region: "trailing", request: "https://api.example.com/v1/chat/completions", models: "https://api.example.com/v1/models"},
		{region: "rooted", request: "https://api.example.com/v1/openai/chat/completions", models: "https://api.example.com/v1/openai/models"},
		{region: "local", request: "http://localhost:11434/api/chat", models: "http://localhost:11434/models"},
	} {
		if got, ok := RequestURL(catalog, "row", wireForRegion(want.region), want.region); !ok || got != want.request {
			t.Fatalf("%s request = %q, found = %t, want %q", want.region, got, ok, want.request)
		}
		if got, ok := ModelsURL(catalog, "row", want.region); !ok || got != want.models {
			t.Fatalf("%s models = %q, found = %t, want %q", want.region, got, ok, want.models)
		}
	}
	if got, ok := RequestURL(catalog, "row", "openai-responses", "responses"); !ok || got != "https://api.example.com/v1/responses" {
		t.Fatalf("responses request = %q, found = %t", got, ok)
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

func TestResolveJoinsEachEndpointsOwnBase(t *testing.T) {
	catalog := Catalog{Providers: []Provider{{
		ID:         "row",
		ModelsPath: "/models",
		Endpoints: []Endpoint{
			{Wire: "openai-completions", BaseURL: "https://first.example.com/v1", CarriesVersion: true},
			{Wire: "openai-responses", BaseURL: "https://second.example.com/api/v1", CarriesVersion: true},
		},
	}}}
	resolved := Resolve(catalog)
	if len(resolved) != 2 {
		t.Fatalf("resolved %d endpoints, want 2", len(resolved))
	}
	for _, want := range []Resolved{
		{ID: "row", Wire: "openai-completions", BaseURL: "https://first.example.com/v1", ModelsURL: "https://first.example.com/v1/models", RequestURL: "https://first.example.com/v1/chat/completions"},
		{ID: "row", Wire: "openai-responses", BaseURL: "https://second.example.com/api/v1", ModelsURL: "https://second.example.com/api/v1/models", RequestURL: "https://second.example.com/api/v1/responses"},
	} {
		if resolved[0] == want {
			continue
		}
		if resolved[1] == want {
			continue
		}
		t.Fatalf("no resolved endpoint = %+v, want %+v", resolved, want)
	}
	if url, ok := ModelsURL(catalog, "row", ""); !ok || url != "https://first.example.com/v1/models" {
		t.Fatalf("models url for the region = %q, found = %t, want the first endpoint's", url, ok)
	}
}

func TestJoinRequestDropsTrailingSlash(t *testing.T) {
	for _, tc := range []struct {
		base  string
		wire  string
		fact  bool
		want  string
		model string
	}{
		{base: "https://api.openai.com/", wire: "openai-completions", want: "https://api.openai.com/v1/chat/completions"},
		{base: "https://api.openai.com///", wire: "openai-completions", want: "https://api.openai.com/v1/chat/completions"},
		{base: "https://api.openai.com/", wire: "openai-responses", want: "https://api.openai.com/v1/responses"},
		{base: "https://chatgpt.com/backend-api/codex/", wire: "openai-codex-responses", want: "https://chatgpt.com/backend-api/codex/responses"},
		{base: "https://api.anthropic.com/", wire: "anthropic-messages", want: "https://api.anthropic.com/v1/messages"},
		{base: "https://api.minimax.io/anthropic/v1/", wire: "anthropic-messages", fact: true, want: "https://api.minimax.io/anthropic/v1/messages"},
		{base: "https://api.minimax.io/anthropic/v1/", wire: "anthropic-messages", want: "https://api.minimax.io/anthropic/v1/v1/messages"},
		{base: "http://localhost:11434/", wire: "ollama", fact: true, want: "http://localhost:11434/api/chat"},
	} {
		path, ok := wirePaths[tc.wire]
		if !ok {
			t.Fatalf("the catalog holds no wire %q", tc.wire)
		}
		if got := joinRequest(tc.base, path, tc.fact); got != tc.want {
			t.Fatalf("joinRequest(%q, %q, %t) = %q, want %q", tc.base, tc.wire, tc.fact, got, tc.want)
		}
	}
}

func TestJoinRequestKeepsACompleteBase(t *testing.T) {
	for _, tc := range []struct {
		base string
		wire string
		want string
	}{
		{base: "https://api.openai.com/v1/chat/completions", wire: "openai-completions", want: "https://api.openai.com/v1/chat/completions"},
		{base: "https://api.openai.com/v1/chat/completions/", wire: "openai-completions", want: "https://api.openai.com/v1/chat/completions"},
		{base: "https://api.openai.com/v1/responses", wire: "openai-responses", want: "https://api.openai.com/v1/responses"},
		{base: "https://chatgpt.com/backend-api/codex/responses", wire: "openai-codex-responses", want: "https://chatgpt.com/backend-api/codex/responses"},
		{base: "https://api.anthropic.com/v1/messages", wire: "anthropic-messages", want: "https://api.anthropic.com/v1/messages"},
		{base: "http://localhost:11434/api/chat", wire: "ollama", want: "http://localhost:11434/api/chat"},
	} {
		path, ok := wirePaths[tc.wire]
		if !ok {
			t.Fatalf("the catalog holds no wire %q", tc.wire)
		}
		if got := joinRequest(tc.base, path, false); got != tc.want {
			t.Fatalf("joinRequest(%q, %q, false) = %q, want %q", tc.base, tc.wire, got, tc.want)
		}
	}
}

func TestJoinModelsDropsTrailingSlash(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{base: "https://api.openai.com", want: "https://api.openai.com/v1/models"},
		{base: "https://api.openai.com/", want: "https://api.openai.com/v1/models"},
		{base: "https://api.openai.com///", want: "https://api.openai.com/v1/models"},
		{base: "https://api.openai.com/v1/models", want: "https://api.openai.com/v1/models"},
		{base: "https://api.openai.com/v1/models/", want: "https://api.openai.com/v1/models"},
	} {
		if got := joinModels(tc.base, "/v1/models", false); got != tc.want {
			t.Fatalf("joinModels(%q, false) = %q, want %q", tc.base, got, tc.want)
		}
	}
}

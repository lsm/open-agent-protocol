package providercatalog

import (
	"testing"

	"github.com/lsm/open-agent-protocol/providers"
)

func heldCatalog(t *testing.T) Catalog {
	t.Helper()
	catalog, err := Load(providers.Files)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return catalog
}

func TestStatusAnswersSupportedForARowThatRecordsNoneAndForAnUnknownId(t *testing.T) {
	catalog := heldCatalog(t)
	for _, provider := range catalog.Providers {
		if got := Status(catalog, provider.ID); got == "" {
			t.Errorf("%s has no status: status never answers nothing", provider.ID)
		}
	}
	if got, want := Status(catalog, "no-such-provider"), "supported"; got != want {
		t.Errorf("an id the catalog does not hold is supported: got %q, want %q", got, want)
	}
}

func TestOfferingAnswersNothingForAnUnknownIdWhereStatusAnswersSupported(t *testing.T) {
	catalog := heldCatalog(t)
	if got := Offering(catalog, "no-such-provider"); got != "" {
		t.Errorf("offering of an unknown id = %q, want nothing", got)
	}
	if got := Status(catalog, "no-such-provider"); got == "" {
		t.Error("status of an unknown id answers nothing, want supported")
	}
	if AcceptsAuth(catalog, "no-such-provider", "api_key") {
		t.Error("an unknown id accepts a credential kind: the row is not there to record one")
	}
}

func TestABaseIsTheEndpointsOwnForItsWireAndItsRegion(t *testing.T) {
	catalog := heldCatalog(t)
	kimi, known := findProvider(catalog, "kimi")
	if !known {
		t.Fatal("the catalog holds no kimi row")
	}
	regions := map[string]bool{}
	for _, endpoint := range kimi.Endpoints {
		regions[endpoint.Region] = true
	}
	if len(regions) != 2 {
		t.Fatalf("kimi's endpoints declare %d regions, want 2: the region rule is untested without two", len(regions))
	}
	first, ok := BaseURL(catalog, "kimi", "openai-completions", "china")
	if !ok {
		t.Fatal("kimi's china endpoint resolves no base")
	}
	second, ok := BaseURL(catalog, "kimi", "openai-completions", "global")
	if !ok {
		t.Fatal("kimi's global endpoint resolves no base")
	}
	if first == second {
		t.Errorf("a region picks that region's endpoint: both %q", first)
	}
	if _, ok := BaseURL(catalog, "kimi", "openai-completions", "mars"); ok {
		t.Error("a region the row declares no endpoint for resolves no base")
	}
	if _, ok := BaseURL(catalog, "kimi", "openai-completions", ""); ok {
		t.Error("no region answers only an endpoint that declares none, and kimi declares none of its")
	}
}

func TestNoRegionAnswersOnlyAnEndpointThatDeclaresNone(t *testing.T) {
	catalog := heldCatalog(t)
	for _, provider := range catalog.Providers {
		for _, endpoint := range provider.Endpoints {
			if endpoint.Region != "" {
				continue
			}
			got, ok := BaseURL(catalog, provider.ID, endpoint.Wire, "")
			if !ok {
				t.Fatalf("%s %s has an endpoint with no region and resolves no base", provider.ID, endpoint.Wire)
			}
			if got != endpoint.BaseURL {
				t.Errorf("%s %s base = %q, want %q", provider.ID, endpoint.Wire, got, endpoint.BaseURL)
			}
		}
	}
}

func TestTheDefaultBaseSkipsTheWire(t *testing.T) {
	catalog := heldCatalog(t)
	openai, known := findProvider(catalog, "openai")
	if !known {
		t.Fatal("the catalog holds no openai row")
	}
	if len(openai.Endpoints) < 2 {
		t.Fatalf("openai has %d endpoints, want at least 2", len(openai.Endpoints))
	}
	first, ok := DefaultBaseURL(catalog, "openai", "")
	if !ok {
		t.Fatal("openai resolves no default base")
	}
	if first != openai.Endpoints[0].BaseURL {
		t.Errorf("default base = %q, want the row's first %q", first, openai.Endpoints[0].BaseURL)
	}
}

func TestARowWithNoEndpointsResolvesNoBase(t *testing.T) {
	catalog := heldCatalog(t)
	var checked int
	for _, provider := range catalog.Providers {
		if len(provider.Endpoints) != 0 {
			continue
		}
		checked++
		if _, ok := BaseURL(catalog, provider.ID, "openai-completions", ""); ok {
			t.Errorf("%s holds no endpoint and resolves a base", provider.ID)
		}
		if _, ok := DefaultBaseURL(catalog, provider.ID, ""); ok {
			t.Errorf("%s holds no endpoint and resolves a default base", provider.ID)
		}
	}
	if checked == 0 {
		t.Error("every row holds an endpoint: the no-endpoint case is untested")
	}
}

func TestAModelOnlyTheResponsesWireServesPicksThatWire(t *testing.T) {
	catalog := heldCatalog(t)
	openai, known := findProvider(catalog, "openai")
	if !known {
		t.Fatal("the catalog holds no openai row")
	}
	first := FirstImplementedWire(openai)
	if first != "openai-completions" {
		t.Fatalf("openai's first implemented wire = %q, want openai-completions", first)
	}
	for _, model := range []string{"o1-pro", "o3-pro", "gpt-5-pro", "gpt-5-codex", "gpt-5.1-codex-max", "computer-use-preview", "gpt-5-deep-research"} {
		if !IsResponsesOnlyModel(model) {
			t.Errorf("%s is served only by the responses wire", model)
		}
		if got := WireForModel(catalog, "openai", model); got != "openai-responses" {
			t.Errorf("openai serves %s over %q, want openai-responses", model, got)
		}
	}
	if got := WireForModel(catalog, "openai", "gpt-4o"); got != first {
		t.Errorf("openai serves gpt-4o over %q, want %q", got, first)
	}
}

func TestEveryWireTheRowsNameIsOneTheCatalogJoins(t *testing.T) {
	catalog := heldCatalog(t)
	for _, provider := range catalog.Providers {
		for _, wire := range provider.Wires {
			if _, known := wirePaths[wire]; !known {
				t.Errorf("%s names the wire %q, which no rule joins", provider.ID, wire)
			}
		}
		if got := FirstImplementedWire(provider); got == "" {
			t.Errorf("%s names the wires %v, none of which a rule joins", provider.ID, provider.Wires)
		}
		if got := WireForModel(catalog, provider.ID, "some-model"); got != FirstImplementedWire(provider) {
			t.Errorf("%s serves an ordinary model over %q, want %q", provider.ID, got, FirstImplementedWire(provider))
		}
	}
}

func TestAnUnknownIdResolvesNoWireAndNoAccessors(t *testing.T) {
	catalog := heldCatalog(t)
	if got := WireForModel(catalog, "no-such-provider", "gpt-5-pro"); got != "" {
		t.Errorf("wire for an unknown id = %q, want nothing", got)
	}
	if got := CredentialEnv(catalog, "no-such-provider"); got != nil {
		t.Errorf("credential env for an unknown id = %v, want none", got)
	}
	if got := BaseURLEnv(catalog, "no-such-provider"); got != nil {
		t.Errorf("base url env for an unknown id = %v, want none", got)
	}
	if got := RegionEnv(catalog, "no-such-provider"); got != "" {
		t.Errorf("region env for an unknown id = %q, want none", got)
	}
	if got := ModelsEndpoint(catalog, "no-such-provider"); got != "" {
		t.Errorf("models endpoint for an unknown id = %q, want none", got)
	}
	if _, ok := OAuthOriginFor(catalog, "no-such-provider"); ok {
		t.Error("oauth origin for an unknown id resolves one")
	}
}

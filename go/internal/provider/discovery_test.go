package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/providercatalog"
)

type listingAnswer struct {
	status int
	body   string
}

type listingServer struct {
	t       *testing.T
	server  *httptest.Server
	mu      sync.Mutex
	answers map[string]listingAnswer
	hits    map[string]int
	headers map[string]http.Header
	paths   map[string]string
}

func newListingServer(t *testing.T, answers map[string]listingAnswer) *listingServer {
	t.Helper()
	s := &listingServer{t: t, answers: answers, hits: map[string]int{}, headers: map[string]http.Header{}, paths: map[string]string{}}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)[0]
		s.mu.Lock()
		s.hits[id]++
		s.headers[id] = r.Header.Clone()
		s.paths[id] = r.URL.Path
		answer, ok := s.answers[id]
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(answer.status)
		w.Write([]byte(answer.body))
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *listingServer) answer(id string, answer listingAnswer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answers[id] = answer
}

func (s *listingServer) hitsFor(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[id]
}

func (s *listingServer) headerFor(id string) http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.headers[id]
}

func (s *listingServer) pathFor(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paths[id]
}

func (s *listingServer) catalog(t *testing.T, ids ...string) providercatalog.Catalog {
	t.Helper()
	held := heldCatalogForTest(t)
	var kept providercatalog.Catalog
	for _, row := range held.Providers {
		if !slices.Contains(ids, row.ID) {
			continue
		}
		endpoints := make([]providercatalog.Endpoint, len(row.Endpoints))
		for i, endpoint := range row.Endpoints {
			endpoint.BaseURL = s.server.URL + "/" + row.ID
			if endpoint.Region != "" {
				endpoint.BaseURL += "-" + endpoint.Region
			}
			if endpoint.CarriesVersion {
				endpoint.BaseURL += "/v1"
			}
			endpoints[i] = endpoint
		}
		row.Endpoints = endpoints
		kept.Providers = append(kept.Providers, row)
	}
	return kept
}

func listingOf(ids ...string) listingAnswer {
	entries := make([]string, len(ids))
	for i, id := range ids {
		entries[i] = `{"id":"` + id + `","object":"model"}`
	}
	return listingAnswer{status: http.StatusOK, body: `{"object":"list","data":[` + strings.Join(entries, ",") + `]}`}
}

func modelKeys(found Discovery) []string {
	keys := make([]string, len(found.Models))
	for i, model := range found.Models {
		keys[i] = model.Model.Provider + "/" + model.Model.ID
	}
	return keys
}

func xiaomiKey() []providercatalog.EnvironmentValue {
	return []providercatalog.EnvironmentValue{{Name: "XIAOMI_API_KEY", Value: "sk-oap-shared"}}
}

var xiaomiRows = []string{"xiaomi-token-plan-cn", "xiaomi-token-plan-sgp", "xiaomi-token-plan-ams", "xiaomi"}

func TestParseModelListingReadsTheMembersZigReads(t *testing.T) {
	models, err := ParseModelListing([]byte(`{"data":[
		{"id":"a","display_name":"A","context_length":200000,"max_output_tokens":32000,"supports_reasoning":true,"supports_image_in":false},
		{"id":"b","context_length":null,"context_window":64000.9,"max_tokens":0,"reasoning":null,"effort":{"supported_levels":["low"]},"input_modalities":["text","IMAGE"]},
		{"id":"c","modalities":["text"],"context_length":5000000000,"context_window":100},
		{"id":""},
		{"name":"no id"},
		"not an object"
	]}`))
	if err != nil {
		t.Fatalf("the listing was refused: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("models = %+v, want a, b and c only", models)
	}
	a, b, c := models[0], models[1], models[2]
	if a.ID != "a" || a.Name != "A" || a.ContextWindow != 200000 || a.MaxTokens != 32000 || a.Reasoning == nil || !*a.Reasoning || a.ImageInput == nil || *a.ImageInput {
		t.Fatalf("a = %+v", a)
	}
	if b.ID != "b" || b.Name != "" || b.ContextWindow != 64000 || b.MaxTokens != 0 || b.Reasoning == nil || !*b.Reasoning || b.ImageInput == nil || !*b.ImageInput {
		t.Fatalf("b = %+v", b)
	}
	if c.ID != "c" || c.ContextWindow != 100 || c.Reasoning != nil || c.ImageInput == nil || *c.ImageInput {
		t.Fatalf("c = %+v", c)
	}
}

func TestParseModelListingRefusesABodyWithoutADataArray(t *testing.T) {
	for _, body := range []string{`[]`, `null`, `{}`, `{"data":null}`, `{"data":{}}`, `not json`} {
		if _, err := ParseModelListing([]byte(body)); err == nil {
			t.Fatalf("%s was accepted", body)
		}
	}
}

func TestAKeyWithoutAPlanLeavesOnlyThePayAsYouGoRow(t *testing.T) {
	server := newListingServer(t, map[string]listingAnswer{
		"xiaomi-token-plan-cn":  {status: http.StatusUnauthorized},
		"xiaomi-token-plan-sgp": {status: http.StatusForbidden},
		"xiaomi-token-plan-ams": {status: http.StatusUnauthorized},
		"xiaomi":                listingOf("mimo-v2"),
	})
	found := Discoverer{Catalog: server.catalog(t, xiaomiRows...), Environment: xiaomiKey()}.Discover(context.Background(), DiscoverAllowCache)
	if got := modelKeys(found); !slices.Equal(got, []string{"xiaomi/mimo-v2"}) {
		t.Fatalf("models = %v, want only the pay-as-you-go row", got)
	}
	if !slices.Equal(found.KeyRefused, xiaomiRows[:3]) {
		t.Fatalf("key refused = %v, want the three plans", found.KeyRefused)
	}
}

func TestAKeyWithAPlanKeepsBothRowsPlanFirst(t *testing.T) {
	server := newListingServer(t, map[string]listingAnswer{
		"xiaomi-token-plan-cn":  listingOf("mimo-plan"),
		"xiaomi-token-plan-sgp": {status: http.StatusUnauthorized},
		"xiaomi-token-plan-ams": {status: http.StatusUnauthorized},
		"xiaomi":                listingOf("mimo-v2"),
	})
	rows := []string{"xiaomi", "xiaomi-token-plan-cn", "xiaomi-token-plan-sgp", "xiaomi-token-plan-ams"}
	found := Discoverer{Catalog: server.catalog(t, rows...), Environment: xiaomiKey()}.Discover(context.Background(), DiscoverAllowCache)
	if got := modelKeys(found); !slices.Equal(got, []string{"xiaomi-token-plan-cn/mimo-plan", "xiaomi/mimo-v2"}) {
		t.Fatalf("models = %v, want the plan before the pay-as-you-go row", got)
	}
}

func TestA402DropsAPlanWithoutCallingTheKeyRefused(t *testing.T) {
	server := newListingServer(t, map[string]listingAnswer{"kimi-china": {status: http.StatusPaymentRequired}})
	env := []providercatalog.EnvironmentValue{{Name: "KIMI_API_KEY", Value: "sk-oap-kimi"}}
	found := Discoverer{Catalog: server.catalog(t, "kimi"), Environment: env}.Discover(context.Background(), DiscoverAllowCache)
	if len(found.Models) != 0 {
		t.Fatalf("models = %v, want the unpaid plan dropped, declared models included", modelKeys(found))
	}
	if len(found.KeyRefused) != 0 {
		t.Fatalf("key refused = %v, want none for a payment refusal", found.KeyRefused)
	}
}

func TestAnOutageKeepsThePlanOnItsCachedList(t *testing.T) {
	server := newListingServer(t, map[string]listingAnswer{
		"xiaomi-token-plan-cn":  {status: http.StatusBadGateway},
		"xiaomi-token-plan-sgp": {status: http.StatusUnauthorized},
		"xiaomi-token-plan-ams": {status: http.StatusUnauthorized},
		"xiaomi":                listingOf("mimo-v2"),
	})
	cache := ListingCache{Dir: t.TempDir()}
	stale := filepath.Join(cache.Dir, "catalog-xiaomi-token-plan-cn.json")
	if err := os.WriteFile(stale, []byte(listingOf("mimo-cached").body), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	found := Discoverer{Catalog: server.catalog(t, xiaomiRows...), Environment: xiaomiKey(), Cache: cache}.Discover(context.Background(), DiscoverAllowCache)
	if got := modelKeys(found); !slices.Equal(got, []string{"xiaomi-token-plan-cn/mimo-cached", "xiaomi/mimo-v2"}) {
		t.Fatalf("models = %v, want the plan kept on its stale cached list", got)
	}
	if server.hitsFor("xiaomi-token-plan-cn") != 1 {
		t.Fatalf("the stale listing was not re-fetched first")
	}
}

func TestAnOutageWithNoCacheFallsBackToTheDeclaredModels(t *testing.T) {
	server := newListingServer(t, map[string]listingAnswer{"kimi-china": {status: http.StatusInternalServerError}})
	env := []providercatalog.EnvironmentValue{{Name: "KIMI_API_KEY", Value: "sk-oap-kimi"}}
	found := Discoverer{Catalog: server.catalog(t, "kimi"), Environment: env}.Discover(context.Background(), DiscoverAllowCache)
	if len(found.Models) != 1 || found.Models[0].Model.ID != "kimi-k2.7-code" || found.Models[0].Discovered {
		t.Fatalf("models = %+v, want the catalog's declared kimi model", found.Models)
	}
	if found.Models[0].ContextWindow != 262144 || found.Models[0].Model.MaxTokens != 16384 {
		t.Fatalf("figures = %d/%d, want the row's 262144/16384", found.Models[0].ContextWindow, found.Models[0].Model.MaxTokens)
	}
}

func TestARefusedPayAsYouGoRowFallsBackToItsCachedList(t *testing.T) {
	server := newListingServer(t, map[string]listingAnswer{"deepseek": {status: http.StatusUnauthorized}})
	cache := ListingCache{Dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(cache.Dir, "catalog-deepseek.json"), []byte(listingOf("deepseek-v4-pro").body), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []providercatalog.EnvironmentValue{{Name: "DEEPSEEK_API_KEY", Value: "sk-oap-deepseek"}}
	found := Discoverer{Catalog: server.catalog(t, "deepseek"), Environment: env, Cache: cache}.Discover(context.Background(), DiscoverForceFetch)
	if got := modelKeys(found); !slices.Equal(got, []string{"deepseek/deepseek-v4-pro"}) {
		t.Fatalf("models = %v, want the cached deepseek list", got)
	}
	if !slices.Equal(found.KeyRefused, []string{"deepseek"}) {
		t.Fatalf("key refused = %v, want deepseek", found.KeyRefused)
	}
}

func TestAFreshListingIsServedWithoutAFetchUntilAForcedRefresh(t *testing.T) {
	server := newListingServer(t, map[string]listingAnswer{"deepseek": listingOf("deepseek-v4-flash")})
	cache := ListingCache{Dir: t.TempDir()}
	discoverer := Discoverer{Catalog: server.catalog(t, "deepseek"), Environment: []providercatalog.EnvironmentValue{{Name: "DEEPSEEK_API_KEY", Value: "sk-oap-deepseek"}}, Cache: cache}
	first := discoverer.Discover(context.Background(), DiscoverAllowCache)
	server.answer("deepseek", listingOf("deepseek-v4-pro"))
	second := discoverer.Discover(context.Background(), DiscoverAllowCache)
	if server.hitsFor("deepseek") != 1 || !slices.Equal(modelKeys(first), modelKeys(second)) {
		t.Fatalf("hits = %d, second = %v, want the fresh cache served without a fetch", server.hitsFor("deepseek"), modelKeys(second))
	}
	forced := discoverer.Discover(context.Background(), DiscoverForceFetch)
	if server.hitsFor("deepseek") != 2 || !slices.Equal(modelKeys(forced), []string{"deepseek/deepseek-v4-pro"}) {
		t.Fatalf("hits = %d, forced = %v, want a forced refresh to fetch", server.hitsFor("deepseek"), modelKeys(forced))
	}
}

func TestOneStoredOpenCodeCredentialListsBothOfferingsAndRemovingItListsNeither(t *testing.T) {
	server := newListingServer(t, map[string]listingAnswer{
		"opencode-go":  listingOf("glm-5.3"),
		"opencode-zen": listingOf("deepseek-v4.1-flash"),
	})
	key := "sk-oap-opencode"
	stored := map[string]*providercatalog.StoredCredential{"opencode": {APIKey: &key}}
	discoverer := Discoverer{
		Catalog: server.catalog(t, "opencode-go", "opencode-zen"),
		Stored:  func(id string) *providercatalog.StoredCredential { return stored[id] },
		Cache:   ListingCache{Dir: t.TempDir()},
	}
	listed := modelKeys(discoverer.Discover(context.Background(), DiscoverForceFetch))
	slices.Sort(listed)
	if !slices.Equal(listed, []string{"opencode-go/glm-5.3", "opencode-zen/deepseek-v4.1-flash"}) {
		t.Fatalf("listed = %v, want both offerings from the one credential", listed)
	}
	delete(stored, "opencode")
	if left := modelKeys(discoverer.Discover(context.Background(), DiscoverForceFetch)); len(left) != 0 {
		t.Fatalf("left = %v, want neither offering once the shared credential is gone", left)
	}
}

func TestAStoredKeyRefusalIsRememberedUntilAForcedRefreshSucceeds(t *testing.T) {
	server := newListingServer(t, map[string]listingAnswer{"minimax-coding-plan": {status: http.StatusUnauthorized}})
	cache := ListingCache{Dir: t.TempDir()}
	key := "sk-oap-stored"
	discoverer := Discoverer{
		Catalog: server.catalog(t, "minimax-coding-plan"),
		Stored:  func(string) *providercatalog.StoredCredential { return &providercatalog.StoredCredential{APIKey: &key} },
		Cache:   cache,
	}
	first := discoverer.Discover(context.Background(), DiscoverAllowCache)
	marker, err := os.ReadFile(filepath.Join(cache.Dir, "catalog-minimax-coding-plan.refused"))
	if err != nil || string(marker) != "key" {
		t.Fatalf("marker = %q, %v, want a key refusal remembered", marker, err)
	}
	server.answer("minimax-coding-plan", listingOf("MiniMax-M3"))
	second := discoverer.Discover(context.Background(), DiscoverAllowCache)
	if server.hitsFor("minimax-coding-plan") != 1 || len(second.Models) != 0 || !slices.Equal(second.KeyRefused, []string{"minimax-coding-plan"}) {
		t.Fatalf("hits = %d, second = %+v, want the remembered refusal honoured without a fetch", server.hitsFor("minimax-coding-plan"), second)
	}
	if len(first.Models) != 0 || !slices.Equal(first.KeyRefused, []string{"minimax-coding-plan"}) {
		t.Fatalf("first = %+v, want the plan dropped and its key refused", first)
	}
	forced := discoverer.Discover(context.Background(), DiscoverForceFetch)
	if got := modelKeys(forced); !slices.Equal(got, []string{"minimax-coding-plan/MiniMax-M3"}) {
		t.Fatalf("forced = %v, want a re-probe to list the plan", got)
	}
	if _, err := os.Stat(filepath.Join(cache.Dir, "catalog-minimax-coding-plan.refused")); !os.IsNotExist(err) {
		t.Fatalf("the marker survived a successful listing: %v", err)
	}
}

func TestAnEnvironmentKeyRefusalIsNotRemembered(t *testing.T) {
	server := newListingServer(t, map[string]listingAnswer{"minimax-coding-plan": {status: http.StatusUnauthorized}})
	cache := ListingCache{Dir: t.TempDir()}
	discoverer := Discoverer{
		Catalog:     server.catalog(t, "minimax-coding-plan"),
		Environment: []providercatalog.EnvironmentValue{{Name: "MINIMAX_API_KEY", Value: "sk-oap-env"}},
		Cache:       cache,
	}
	discoverer.Discover(context.Background(), DiscoverAllowCache)
	discoverer.Discover(context.Background(), DiscoverAllowCache)
	if server.hitsFor("minimax-coding-plan") != 2 {
		t.Fatalf("hits = %d, want every discovery to probe an environment key", server.hitsFor("minimax-coding-plan"))
	}
	if _, err := os.Stat(filepath.Join(cache.Dir, "catalog-minimax-coding-plan.refused")); !os.IsNotExist(err) {
		t.Fatalf("an environment key's refusal left a marker: %v", err)
	}
}

func TestTheListingRequestCarriesTheWiresCredentialHeader(t *testing.T) {
	server := newListingServer(t, map[string]listingAnswer{
		"minimax-coding-plan": listingOf("MiniMax-M3"),
		"openrouter":          listingOf("openrouter/auto"),
	})
	env := []providercatalog.EnvironmentValue{{Name: "MINIMAX_API_KEY", Value: "sk-oap-minimax"}, {Name: "OPENROUTER_API_KEY", Value: "sk-oap-router"}}
	found := Discoverer{Catalog: server.catalog(t, "minimax-coding-plan", "openrouter"), Environment: env}.Discover(context.Background(), DiscoverAllowCache)
	anthropic := server.headerFor("minimax-coding-plan")
	if anthropic.Get("x-api-key") != "sk-oap-minimax" || anthropic.Get("anthropic-version") != "2023-06-01" || anthropic.Get("authorization") != "" {
		t.Fatalf("anthropic-messages headers = %v", anthropic)
	}
	openai := server.headerFor("openrouter")
	if openai.Get("authorization") != "Bearer sk-oap-router" || openai.Get("x-api-key") != "" || openai.Get("accept") != "application/json" {
		t.Fatalf("openai-completions headers = %v", openai)
	}
	if found.Models[0].Model.API != "anthropic-messages" || found.Models[1].Model.API != "openai-completions" {
		t.Fatalf("wires = %s, %s", found.Models[0].Model.API, found.Models[1].Model.API)
	}
}

func TestARowWithoutACredentialIsNotProbed(t *testing.T) {
	server := newListingServer(t, map[string]listingAnswer{"openrouter": listingOf("openrouter/auto")})
	found := Discoverer{Catalog: server.catalog(t, "openrouter")}.Discover(context.Background(), DiscoverAllowCache)
	if server.hitsFor("openrouter") != 0 || len(found.Models) != 0 {
		t.Fatalf("hits = %d, models = %v, want nothing without a key", server.hitsFor("openrouter"), modelKeys(found))
	}
}

func TestADeepSeekBaseOverrideMovesTheListingToTheCompletionsWire(t *testing.T) {
	server := newListingServer(t, map[string]listingAnswer{"relay": listingOf("deepseek-v4-pro")})
	env := []providercatalog.EnvironmentValue{
		{Name: "DEEPSEEK_API_KEY", Value: "sk-oap-deepseek"},
		{Name: "DEEPSEEK_BASE_URL", Value: server.server.URL + "/relay/v1/"},
	}
	found := Discoverer{Catalog: server.catalog(t, "deepseek"), Environment: env}.Discover(context.Background(), DiscoverAllowCache)
	if server.pathFor("relay") != "/relay/v1/models" {
		t.Fatalf("listing path = %q, want the override's /v1/models", server.pathFor("relay"))
	}
	if len(found.Models) != 1 || found.Models[0].Model.API != "openai-completions" || found.Models[0].Model.BaseURL != server.server.URL+"/relay" {
		t.Fatalf("models = %+v, want one completions model on the override base", found.Models)
	}
}

func TestTheKimiListingFollowsItsRegion(t *testing.T) {
	server := newListingServer(t, map[string]listingAnswer{"kimi-global": listingOf("kimi-k2.7")})
	cache := ListingCache{Dir: t.TempDir()}
	env := []providercatalog.EnvironmentValue{{Name: "KIMI_API_KEY", Value: "sk-oap-kimi"}, {Name: "KIMI_REGION", Value: "moonshot"}}
	found := Discoverer{Catalog: server.catalog(t, "kimi"), Environment: env, Cache: cache}.Discover(context.Background(), DiscoverAllowCache)
	if got := modelKeys(found); !slices.Equal(got, []string{"kimi/kimi-k2.7"}) {
		t.Fatalf("models = %v, want the global endpoint's listing", got)
	}
	if _, err := os.Stat(filepath.Join(cache.Dir, "catalog-kimi-global.json")); err != nil {
		t.Fatalf("the listing was not cached under its region: %v", err)
	}
}

func TestAnEnvironmentKeyProbesPastAStoredKeysRefusal(t *testing.T) {
	server := newListingServer(t, map[string]listingAnswer{"minimax-coding-plan": listingOf("MiniMax-M3")})
	cache := ListingCache{Dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(cache.Dir, "catalog-minimax-coding-plan.refused"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	discoverer := Discoverer{
		Catalog:     server.catalog(t, "minimax-coding-plan"),
		Environment: []providercatalog.EnvironmentValue{{Name: "MINIMAX_API_KEY", Value: "sk-oap-env"}},
		Cache:       cache,
	}
	found := discoverer.Discover(context.Background(), DiscoverAllowCache)
	if got := modelKeys(found); !slices.Equal(got, []string{"minimax-coding-plan/MiniMax-M3"}) || len(found.KeyRefused) != 0 {
		t.Fatalf("models = %v, refused = %v, want the environment key probed and listed", got, found.KeyRefused)
	}
}

func TestAStaleStoredRefusalStaysDroppedWhenTheReprobeFails(t *testing.T) {
	server := newListingServer(t, map[string]listingAnswer{"kimi-china": {status: http.StatusInternalServerError}})
	cache := ListingCache{Dir: t.TempDir()}
	marker := filepath.Join(cache.Dir, "catalog-kimi-china.refused")
	if err := os.WriteFile(marker, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatal(err)
	}
	key := "sk-oap-stored"
	found := Discoverer{
		Catalog: server.catalog(t, "kimi"),
		Stored:  func(string) *providercatalog.StoredCredential { return &providercatalog.StoredCredential{APIKey: &key} },
		Cache:   cache,
	}.Discover(context.Background(), DiscoverAllowCache)
	if server.hitsFor("kimi-china") != 1 {
		t.Fatalf("hits = %d, want a stale marker re-probed", server.hitsFor("kimi-china"))
	}
	if len(found.Models) != 0 {
		t.Fatalf("models = %v, want the plan kept dropped, declared models included", modelKeys(found))
	}
}

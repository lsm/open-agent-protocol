package providercatalog

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lsm/open-agent-protocol/providers"
)

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

func TestResolveJoinsEachEndpointsOwnBase(t *testing.T) {
	catalog := Catalog{Providers: []Provider{{
		ID:         "row",
		ModelsPath: "/models",
		Endpoints: []Endpoint{
			{Wire: "openai-completions", BaseURL: "https://first.example.com/v1"},
			{Wire: "openai-responses", BaseURL: "https://second.example.com/api/v1"},
		},
	}}}
	resolved := Resolve(catalog)
	if len(resolved) != 2 {
		t.Fatalf("resolved %d endpoints, want 2", len(resolved))
	}
	for _, want := range []Resolved{
		{ID: "row", Wire: "openai-completions", BaseURL: "https://first.example.com/v1", ModelsURL: "https://first.example.com/v1/models", RequestURL: "https://first.example.com/v1/chat/completions"},
		{ID: "row", Wire: "openai-responses", BaseURL: "https://second.example.com/api/v1", ModelsURL: "https://second.example.com/api/v1/models", RequestURL: "https://second.example.com/api/v1/v1/responses"},
	} {
		if resolved[0] == want {
			continue
		}
		if resolved[1] == want {
			continue
		}
		t.Fatalf("no resolved endpoint = %+v, want %+v", resolved, want)
	}
	if url := ModelsURL(catalog, "row", ""); url != "https://first.example.com/v1/models" {
		t.Fatalf("models url for the region = %q, want the first endpoint's", url)
	}
}

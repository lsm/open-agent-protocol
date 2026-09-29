package providercatalog

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lsm/open-agent-protocol/providers"
)

func TestLoadReadsARowAndItsEndpoints(t *testing.T) {
	catalog, err := Load(fstest.MapFS{
		SchemaFile:  {Data: []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"catalog.schema.json","type":"object","required":["providers"],"properties":{"providers":{"type":"array","minItems":1,"items":{"$ref":"#/$defs/provider"}}},"additionalProperties":false,"$defs":{"provider":{"type":"object","required":["id"],"properties":{"id":{"type":"string","minLength":1},"endpoints":{"type":"array","items":{"type":"object","required":["wire","base_url"],"properties":{"wire":{"type":"string","minLength":1},"base_url":{"type":"string","minLength":1},"region":{"type":"string","minLength":1}},"additionalProperties":false}}},"additionalProperties":false}}}`)},
		CatalogFile: {Data: []byte(`{"providers":[{"id":"kimi","endpoints":[{"wire":"openai-completions","base_url":"https://api.kimi.com/coding","region":"china"}]}]}`)},
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(catalog.Providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(catalog.Providers))
	}
	if catalog.Providers[0].Endpoints[0].BaseURL != "https://api.kimi.com/coding" {
		t.Fatalf("base url = %q", catalog.Providers[0].Endpoints[0].BaseURL)
	}
}

func TestLoadReadsTheCheckedInCatalog(t *testing.T) {
	catalog, err := Load(providers.Files)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(catalog.Providers) == 0 {
		t.Fatal("the checked-in catalog holds no provider")
	}
	if findings := Check(catalog); len(findings) != 0 {
		t.Fatalf("findings = %v, want none", findings)
	}
}

func TestLoadRefusesAnUnknownMemberAndAnEmptyCatalog(t *testing.T) {
	schema := []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"catalog.schema.json","type":"object","required":["providers"],"properties":{"providers":{"type":"array","minItems":1,"items":{"$ref":"#/$defs/provider"}}},"additionalProperties":false,"$defs":{"provider":{"type":"object","required":["id"],"properties":{"id":{"type":"string","minLength":1}},"additionalProperties":false}}}`)
	files := func(catalog string) fstest.MapFS {
		out := fstest.MapFS{SchemaFile: &fstest.MapFile{Data: schema}, CatalogFile: &fstest.MapFile{Data: []byte(catalog)}}
		return out
	}
	if _, err := Load(files(`{"providers":[{"id":"kimi","surprise":1}]}`)); err == nil {
		t.Fatal("a member the schema does not declare was accepted")
	}
	if _, err := Load(files(`{"providers":[]}`)); err == nil {
		t.Fatal("an empty catalog was accepted")
	}
	if _, err := Load(files(`{"providers":[{"id":"a"},{"id":"a"}]}`)); err != nil {
		t.Fatalf("two rows for one id decode: %v", err)
	}
}

func TestLoadReadsTheContextWindowCeilingAndOnlyTheRowsThatStateOneDo(t *testing.T) {
	catalog, err := Load(providers.Files)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ceiling := func(id string) int {
		t.Helper()
		provider, known := findProvider(catalog, id)
		if !known {
			t.Fatalf("no row for %s", id)
		}
		return provider.MaxContextWindow
	}
	if got := ceiling("openai"); got != 1000000 {
		t.Errorf("openai max_context_window = %d, want 1000000", got)
	}
	if got := ceiling("openai-codex"); got != 1000000 {
		t.Errorf("openai-codex max_context_window = %d, want 1000000", got)
	}
	if got := ceiling("anthropic"); got != 0 {
		t.Errorf("anthropic states a ceiling of %d, want none", got)
	}
	for _, provider := range catalog.Providers {
		switch provider.ID {
		case "openai", "openai-codex":
			if provider.MaxContextWindow == 0 {
				t.Errorf("%s records no ceiling, want the one the owner's statement gives it", provider.ID)
			}
		default:
			if provider.MaxContextWindow != 0 {
				t.Errorf("%s states a ceiling of %d, and only the OpenAI rows record one", provider.ID, provider.MaxContextWindow)
			}
		}
		if provider.ContextWindow == 0 || provider.MaxContextWindow == 0 {
			continue
		}
		if provider.ContextWindow > provider.MaxContextWindow {
			t.Errorf("%s records a window of %d above its own ceiling of %d", provider.ID, provider.ContextWindow, provider.MaxContextWindow)
		}
		for _, model := range provider.Models {
			limit := model.MaxContextWindow
			if limit == 0 {
				limit = provider.MaxContextWindow
			}
			if model.ContextWindow == 0 || limit == 0 {
				continue
			}
			if model.ContextWindow > limit {
				t.Errorf("%s model %s records a window of %d above its own ceiling of %d", provider.ID, model.ID, model.ContextWindow, limit)
			}
		}
	}
}

func TestLoadRefusesARepeatedMember(t *testing.T) {
	if _, err := DecodeStrict([]byte(`{"providers":[],"providers":[]}`)); err == nil {
		t.Fatal("a member spelled twice decoded")
	}
}

func TestCheckNamesACredentialSourceItDoesNotKnowOrRepeats(t *testing.T) {
	findings := Check(Catalog{Providers: []Provider{{ID: "kimi", CredentialOrder: []string{"stored", "keychain"}}}})
	if len(findings) != 1 || findings[0].Code != CodeCredentialOrder {
		t.Fatalf("findings = %v, want one unknown credential source", findings)
	}
	findings = Check(Catalog{Providers: []Provider{{ID: "kimi", CredentialOrder: []string{"stored", "stored"}}}})
	if len(findings) != 1 || findings[0].Code != CodeCredentialOrder {
		t.Fatalf("findings = %v, want one repeated credential source", findings)
	}
	findings = Check(Catalog{Providers: []Provider{{ID: "kimi", CredentialOrder: []string{"stored", "environment"}}}})
	if len(findings) != 0 {
		t.Fatalf("findings = %v, want none", findings)
	}
}

func TestCheckNamesADuplicateRow(t *testing.T) {
	duplicated := []Provider{{ID: "kimi"}, {ID: "kimi"}}
	findings := Check(Catalog{Providers: duplicated})
	if len(findings) != 1 || findings[0].Code != CodeDuplicateID {
		t.Fatalf("findings = %v, want one duplicate", findings)
	}
	wireless := []Provider{{ID: "kimi", Endpoints: []Endpoint{{BaseURL: "https://api.kimi.com/coding"}}}}
	findings = Check(Catalog{Providers: wireless})
	if len(findings) != 1 || findings[0].Code != CodeEndpointLone {
		t.Fatalf("findings = %v, want one endpoint naming no wire", findings)
	}
	sound := []Provider{{ID: "kimi"}}
	if len(Check(Catalog{Providers: sound})) != 0 {
		t.Fatal("a sound catalog produced a finding")
	}
}

func TestCheckNamesAnOfferingOrStatusItDoesNotKnow(t *testing.T) {
	findings := Check(Catalog{Providers: []Provider{{ID: "kimi", Offering: "seat"}}})
	if len(findings) != 1 || findings[0].Code != CodeOffering {
		t.Fatalf("findings = %v, want one unknown offering", findings)
	}
	findings = Check(Catalog{Providers: []Provider{{ID: "kimi", Status: "retired"}}})
	if len(findings) != 1 || findings[0].Code != CodeStatus {
		t.Fatalf("findings = %v, want one unknown status", findings)
	}
	findings = Check(Catalog{Providers: []Provider{{ID: "kimi", Offering: "subscription", Status: "withheld"}}})
	if len(findings) != 0 {
		t.Fatalf("findings = %v, want none", findings)
	}
}

func TestCheckNamesARowWithNoID(t *testing.T) {
	findings := Check(Catalog{Providers: []Provider{{}}})
	if len(findings) != 1 || findings[0].Code != CodeMissingID {
		t.Fatalf("findings = %v, want one missing id", findings)
	}
}

func TestCheckNamesCarriesVersionOnAWireWithNoVersionedPath(t *testing.T) {
	for _, wire := range []string{"openai-codex-responses", "ollama", "google-generative-ai", "no-such-wire"} {
		findings := Check(Catalog{Providers: []Provider{{ID: "kimi", Endpoints: []Endpoint{
			{Wire: wire, BaseURL: "https://kimi.example.com", CarriesVersion: true},
		}}}})
		if len(findings) != 1 || findings[0].Code != CodeCarriesOn {
			t.Fatalf("%s: findings = %v, want one %s", wire, findings, CodeCarriesOn)
		}
		if !strings.Contains(findings[0].Detail, "/v1/") {
			t.Fatalf("%s: finding does not explain the rule: %s", wire, findings[0].Detail)
		}
	}
}

func TestCheckAcceptsCarriesVersionOnEveryVersionedWire(t *testing.T) {
	for _, wire := range []string{"openai-completions", "openai-responses", "anthropic-messages"} {
		findings := Check(Catalog{Providers: []Provider{{ID: "kimi", Endpoints: []Endpoint{
			{Wire: wire, BaseURL: "https://kimi.example.com/v1", CarriesVersion: true},
		}}}})
		if len(findings) != 0 {
			t.Fatalf("%s: findings = %v, want none", wire, findings)
		}
	}
}

func TestCheckAcceptsAnEndpointThatRecordsNoVersionFact(t *testing.T) {
	for _, wire := range []string{"openai-codex-responses", "ollama", "google-generative-ai"} {
		findings := Check(Catalog{Providers: []Provider{{ID: "kimi", Endpoints: []Endpoint{
			{Wire: wire, BaseURL: "https://kimi.example.com"},
		}}}})
		if len(findings) != 0 {
			t.Fatalf("%s: findings = %v, want none", wire, findings)
		}
	}
}

func TestCheckLiteralsNamesACataloguedBaseURLOutsideTestCode(t *testing.T) {
	catalog := Catalog{Providers: []Provider{{
		ID:        "kimi",
		Endpoints: []Endpoint{{Wire: "openai-completions", BaseURL: "https://api.kimi.com/coding", Region: "china"}},
	}}}
	tree := fstest.MapFS{
		"zig/src/model_catalog.zig": {Data: []byte("const std = @import(\"std\");\nconst base = \"https://api.kimi.com/coding\";\ntest \"the default resolves\" {\n    try std.testing.expectEqualStrings(\"https://api.kimi.com/coding\", base);\n}\n")},
		"go/provider/zai.go":        {Data: []byte("package provider\n\nconst endpoint = \"https://api.kimi.com/coding\"\n\nfunc TestEndpoint(t *testing.T) {\n\t_ = \"https://api.kimi.com/coding\"\n}\n")},
		"go/provider/zai_test.go":   {Data: []byte("package provider\n\nconst endpoint = \"https://api.kimi.com/coding\"\n")},
	}
	findings := CheckLiterals(tree, catalog)
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2: %v", len(findings), findings)
	}
	for _, finding := range findings {
		if !strings.Contains(finding.Detail, "providers/catalog.json") {
			t.Fatalf("finding does not name the catalog: %s", finding.Detail)
		}
		if !strings.Contains(finding.Detail, "china") {
			t.Fatalf("finding does not name the region: %s", finding.Detail)
		}
	}
}

func TestCheckLiteralsIgnoresAValueTheCatalogDoesNotCarry(t *testing.T) {
	catalog := Catalog{Providers: []Provider{{ID: "kimi", Endpoints: []Endpoint{{Wire: "openai-completions", BaseURL: "https://api.kimi.com/coding"}}}}}
	tree := fstest.MapFS{
		"zig/src/a.zig": {Data: []byte("const base = \"https://api.moonshot.ai\";\n")},
	}
	if findings := CheckLiterals(tree, catalog); len(findings) != 0 {
		t.Fatalf("findings = %v, want none", findings)
	}
}

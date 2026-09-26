package providercatalog

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoadReadsTheCheckedInCatalog(t *testing.T) {
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

func TestLoadRefusesARepeatedMember(t *testing.T) {
	if _, err := DecodeStrict([]byte(`{"providers":[],"providers":[]}`)); err == nil {
		t.Fatal("a member spelled twice decoded")
	}
}

func TestCheckNamesADuplicateRow(t *testing.T) {
	duplicates := Check(Catalog{Providers: []Provider{{ID: "kimi"}, {ID: "kimi"}}})
	if len(duplicates) != 1 || duplicates[0].Code != CodeDuplicateID {
		t.Fatalf("findings = %v, want one duplicate", duplicates)
	}
	wireless := Check(Catalog{Providers: []Provider{{ID: "kimi", Endpoints: []Endpoint{{BaseURL: "https://api.kimi.com/coding"}}}}})
	if len(wireless) != 1 || wireless[0].Code != CodeEndpointLone {
		t.Fatalf("findings = %v, want one endpoint naming no wire", wireless)
	}
	if len(Check(Catalog{Providers: []Provider{{ID: "kimi"}})) != 0 {
		t.Fatal("a sound catalog produced a finding")
	}
}

func TestCheckNamesARowWithNoID(t *testing.T) {
	findings := Check(Catalog{Providers: []Provider{{}}})
	if len(findings) != 1 || findings[0].Code != CodeMissingID {
		t.Fatalf("findings = %v, want one missing id", findings)
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

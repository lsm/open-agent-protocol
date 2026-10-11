package providercatalog

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestTheCatalogLoaderRowsAreZigs(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "zig", "src", "provider_catalog.zig"))
	if err != nil {
		t.Fatalf("the Zig catalog module is not readable: %v", err)
	}
	block := regexp.MustCompile(`(?s)pub const catalog_loader_rows = \[_\]\[\]const u8\{(.*?)\};`).FindSubmatch(source)
	if block == nil {
		t.Fatal("zig/src/provider_catalog.zig declares no catalog_loader_rows")
	}
	var zig []string
	for _, quoted := range regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(block[1], -1) {
		zig = append(zig, string(quoted[1]))
	}
	if !slices.Equal(CatalogLoaderRows, zig) {
		t.Fatalf("Go loader rows = %v, Zig = %v", CatalogLoaderRows, zig)
	}
}

func TestLoaderOrderPutsEveryCodingPlanFirstInCatalogOrder(t *testing.T) {
	catalog := heldCatalog(t)
	ordered := LoaderOrder(catalog)
	if len(ordered) != len(CatalogLoaderRows) {
		t.Fatalf("ordered = %v, want every loader row once", ordered)
	}
	var plans, rest []string
	for _, row := range catalog.Providers {
		if !ServedByCatalogLoader(row.ID) {
			continue
		}
		if row.Offering == "coding_plan" {
			plans = append(plans, row.ID)
		} else {
			rest = append(rest, row.ID)
		}
	}
	if len(plans) == 0 || len(rest) == 0 {
		t.Fatalf("plans = %v, rest = %v, want both kinds in the catalog", plans, rest)
	}
	if want := append(plans, rest...); !slices.Equal(ordered, want) {
		t.Fatalf("ordered = %v, want %v", ordered, want)
	}
}

func TestResolveRegionReadsTheRowsRegionVariable(t *testing.T) {
	catalog := heldCatalog(t)
	cases := []struct {
		value string
		want  string
	}{
		{"", "china"},
		{"moonshot", "global"},
		{" GLOBAL ", "global"},
		{"cn", "china"},
		{"mars", "china"},
	}
	for _, c := range cases {
		env := []EnvironmentValue{{Name: RegionEnv(catalog, "kimi"), Value: c.value}}
		if got := ResolveRegion(catalog, env, "kimi"); got != c.want {
			t.Fatalf("KIMI_REGION=%q resolves %q, want %q", c.value, got, c.want)
		}
	}
	if got := ResolveRegion(catalog, nil, "deepseek"); got != "" {
		t.Fatalf("deepseek resolves region %q, want none", got)
	}
}

func TestOnlyDeepSeekMovesWireOnABaseOverride(t *testing.T) {
	for _, row := range heldCatalog(t).Providers {
		got := OverriddenWire(row.ID)
		if row.ID == "deepseek" {
			if got != "openai-completions" || !strings.Contains(strings.Join(row.Wires, ","), got) {
				t.Fatalf("deepseek moves to %q", got)
			}
			continue
		}
		if got != "" {
			t.Fatalf("%s moves to %q", row.ID, got)
		}
	}
}

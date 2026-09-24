package harness

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/harnesses"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func catalogFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := fs.ReadFile(harnesses.Files, name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mutated(t *testing.T, data []byte, mutate func(document map[string]any, version map[string]any)) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	mutate(document, document["versions"].([]any)[0].(map[string]any))
	out, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func compiledSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	schema, err := CompileSchema(harnesses.Files)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func TestLoadReadsEveryHarnessFileAndNotTheSchema(t *testing.T) {
	catalog := loadCatalog(t)
	entries, err := fs.ReadDir(harnesses.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") && entry.Name() != SchemaFile {
			want[strings.TrimSuffix(entry.Name(), ".json")] = true
		}
	}
	if len(catalog.Harnesses) != len(want) {
		t.Fatalf("loaded %d harnesses from %d files", len(catalog.Harnesses), len(want))
	}
	for _, entry := range catalog.Harnesses {
		if !want[entry.ID] {
			t.Fatalf("loaded %q, which no file names", entry.ID)
		}
	}
}

func TestDecodeStrictRefusesAnUnknownMember(t *testing.T) {
	original := catalogFile(t, "pi.json")
	if _, err := DecodeStrict(original); err != nil {
		t.Fatalf("the unmodified file does not decode: %v", err)
	}
	for name, mutate := range map[string]func(map[string]any, map[string]any){
		"harness": func(document, _ map[string]any) { document["homepage"] = "https://example.invalid" },
		"version": func(_, version map[string]any) { version["digest"] = strings.Repeat("a", 64) },
		"artifact": func(_, version map[string]any) {
			version["artifacts"].([]any)[0].(map[string]any)["sha512"] = "x"
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeStrict(mutated(t, original, mutate))
			if err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("an unknown %s member decoded: %v", name, err)
			}
		})
	}
}

func TestDecodeStrictRefusesDataAfterTheObject(t *testing.T) {
	_, err := DecodeStrict(append(catalogFile(t, "pi.json"), []byte(`{}`)...))
	if err == nil || !strings.Contains(err.Error(), "data after the harness object") {
		t.Fatalf("trailing data decoded: %v", err)
	}
}

func TestTheSchemaRefusesWhatTheStrictDecoderAccepts(t *testing.T) {
	schema := compiledSchema(t)
	original := catalogFile(t, "pi.json")
	if _, err := Parse(schema, "pi.json", original); err != nil {
		t.Fatalf("the unmodified file fails: %v", err)
	}
	for name, mutate := range map[string]func(map[string]any, map[string]any){
		"a status outside the four":          func(_, version map[string]any) { version["status"] = "beta" },
		"a current version with no revision": func(_, version map[string]any) { delete(version, "capability_revision") },
		"a floor carrying a revision":        func(_, version map[string]any) { version["status"] = "floor" },
		"a retired version with a corpus": func(_, version map[string]any) {
			version["status"] = "retired"
			delete(version, "capability_revision")
			delete(version, "endpoint_version")
			delete(version, "admits")
		},
		"a digest that is not sha256": func(_, version map[string]any) {
			version["artifacts"].([]any)[0].(map[string]any)["sha256"] = "494e498f"
		},
		"a ledger path climbing out of the tree": func(_, version map[string]any) { version["ledgers"] = []any{"../pi.md"} },
	} {
		t.Run(name, func(t *testing.T) {
			data := mutated(t, original, mutate)
			if _, err := DecodeStrict(data); err != nil {
				t.Fatalf("the strict decoder refuses it too, so the schema is not what is tested: %v", err)
			}
			if _, err := Parse(schema, "pi.json", data); err == nil {
				t.Fatalf("the schema accepted %s", name)
			}
		})
	}
}

func TestParseRefusesAFileNamedForAnotherHarness(t *testing.T) {
	_, err := Parse(compiledSchema(t), "hermes.json", catalogFile(t, "pi.json"))
	if err == nil || !strings.Contains(err.Error(), `harness "pi" belongs in pi.json`) {
		t.Fatalf("pi's entry loaded as hermes.json: %v", err)
	}
}

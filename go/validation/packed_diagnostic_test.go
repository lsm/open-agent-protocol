package validation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackedPrimaryDiagnosticSelectsDeclaredPayload(t *testing.T) {
	packs, err := LoadPacks(packDir(t, "storage"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), "fixtures/schema-invalid/ext-packed-type-malformed.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []Mode{ModeStrict, ModeTolerant} {
		t.Run(string(mode), func(t *testing.T) {
			validator, err := NewWith(Options{Mode: mode, Packs: packs})
			if err != nil {
				t.Fatal(err)
			}
			result := validator.ValidateBytes(data, "packed-request")
			if len(result.Diagnostics) != 1 {
				t.Fatalf("diagnostics: %v", result.Diagnostics)
			}
			d := result.Diagnostics[0]
			if d.Code != CodeSchemaInvalid || d.Phase != PhaseSchema || d.Index != 0 || d.Pointer != "/payload" {
				t.Fatalf("primary diagnostic: %+v", d)
			}
		})
	}
}

func TestPackedPrimaryDiagnosticReferencesAndGlobalConstraints(t *testing.T) {
	for _, name := range []string{"whole", "root-fragment", "escaped-fragment", "escaped-pipe-id", "escaped-space-id", "nested-core"} {
		t.Run(name, func(t *testing.T) {
			kind := "com.example.primary.event"
			payloadSchema := map[string]any{"type": "object", "required": []string{"ok"}, "properties": map[string]any{"ok": map[string]any{"const": true}}}
			branch := map[string]any{"type": "object", "properties": map[string]any{"type": map[string]any{"const": kind}, "payload": payloadSchema}}
			document := map[string]any{"$defs": map[string]any{"event": branch, "value|": payloadSchema}}
			citation := "types.json#/$defs/event"
			payload := map[string]any{"ok": false}
			pointer := "/payload/ok"
			switch name {
			case "whole", "root-fragment":
				document = branch
				citation = "types.json"
				if name == "root-fragment" {
					citation += "#"
				}
			case "escaped-fragment":
				branch["properties"].(map[string]any)["payload"] = map[string]any{"$ref": "#/$defs/value%7C"}
			case "escaped-pipe-id", "escaped-space-id":
				document["$id"] = "alias%7C.json"
				if name == "escaped-space-id" {
					document["$id"] = "alias%20space.json"
				}
			case "nested-core":
				branch["properties"].(map[string]any)["payload"] = map[string]any{"$ref": "https://open-agent-protocol.local/v0.1/envelope.schema.json#/$defs/runCompleted"}
				payload = map[string]any{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "run.completed", "id": "inner", "session_id": "s", "run_id": "r", "sequence": 1, "payload": map[string]any{"session_id": "s", "run_id": "r", "final_response": map[string]any{"role": "assistant", "content": "ok"}}}
				pointer = "/payload/payload"
			}
			directory := writeTempPack(t, map[string]any{
				"types.json": document,
				"pack.json": map[string]any{
					"id": "com.example.primary", "version": "1", "schemas": []string{"types.json"},
					"envelope_types": []any{map[string]any{"type": kind, "role": "event", "schema": citation}},
					"gates":          []any{map[string]any{"type": kind, "ungated": true}},
				},
			})
			packs, err := LoadPacks([]string{directory})
			if err != nil {
				t.Fatal(err)
			}
			for _, mode := range []Mode{ModeStrict, ModeTolerant} {
				t.Run(string(mode), func(t *testing.T) {
					validator, err := NewWith(Options{Mode: mode, Packs: packs})
					if err != nil {
						t.Fatal(err)
					}
					envelope := map[string]any{"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": kind, "id": "outer", "payload": payload}
					data, _ := json.Marshal(envelope)
					result := validator.ValidateBytes(data, name)
					if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != CodeSchemaInvalid || result.Diagnostics[0].Pointer != pointer {
						t.Fatalf("%s: %+v", pointer, result.Diagnostics)
					}
					var valid map[string]any
					if err := json.Unmarshal(data, &valid); err != nil {
						t.Fatal(err)
					}
					p := valid["payload"].(map[string]any)
					if name == "nested-core" {
						p["payload"].(map[string]any)["stop_reason"] = "end_turn"
					} else {
						p["ok"] = true
					}
					data, _ = json.Marshal(valid)
					if result := validator.ValidateBytes(data, "valid-control"); result.HasCode(CodeSchemaInvalid) {
						t.Fatalf("valid payload: %+v", result.Diagnostics)
					}
					delete(valid, "id")
					data, _ = json.Marshal(valid)
					result = validator.ValidateBytes(data, "global-required")
					if len(result.Diagnostics) != 1 || result.Diagnostics[0].Pointer != "" || !strings.Contains(result.Diagnostics[0].Message, "'id'") {
						t.Fatalf("global constraint: %+v", result.Diagnostics)
					}
				})
			}
		})
	}
}

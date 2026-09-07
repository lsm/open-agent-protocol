package validation

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"

	bundled "github.com/lsm/open-agent-protocol/schema"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const schemaBase = "https://open-agent-protocol.local/v0.1/"

func CompileSchemas() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	entries, err := fs.ReadDir(bundled.V01, "v0.1")
	if err != nil {
		return nil, fmt.Errorf("read embedded schemas: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := fs.ReadFile(bundled.V01, "v0.1/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read embedded schema %s: %w", entry.Name(), err)
		}
		var document any
		if err := json.Unmarshal(data, &document); err != nil {
			return nil, fmt.Errorf("decode embedded schema %s: %w", entry.Name(), err)
		}
		if err := compiler.AddResource(schemaBase+entry.Name(), document); err != nil {
			return nil, fmt.Errorf("register embedded schema %s: %w", entry.Name(), err)
		}
	}
	var root *jsonschema.Schema
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".json" {
			continue
		}
		compiled, err := compiler.Compile(schemaBase + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("compile embedded schema %s: %w", entry.Name(), err)
		}
		if entry.Name() == "envelope.schema.json" {
			root = compiled
		}
	}
	if root == nil {
		return nil, fmt.Errorf("compile schema bundle: envelope schema not found")
	}
	return root, nil
}

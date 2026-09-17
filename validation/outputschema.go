package validation

import (
	"bytes"
	"encoding/json"
	"fmt"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

type OutputSchema struct{ compiled *jsonschema.Schema }

const outputSchemaBase = "https://open-agent-protocol.local/output-schema"

func CompileOutputSchema(raw json.RawMessage) (*OutputSchema, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("output_schema is empty")
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("output_schema is not valid JSON: %w", err)
	}
	object, ok := document.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("output_schema must be a JSON Schema object")
	}

	if err := objectRooted(object); err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(&refusingLoader{})
	if err := compiler.AddResource(outputSchemaBase, document); err != nil {
		return nil, fmt.Errorf("output_schema is not a schema this engine accepts: %w", err)
	}
	compiled, err := compiler.Compile(outputSchemaBase)
	if err != nil {
		return nil, fmt.Errorf("output_schema does not compile: %w", err)
	}
	return &OutputSchema{compiled: compiled}, nil
}

func objectRooted(document map[string]any) error {
	declared, ok := document["type"]
	if !ok {
		return nil
	}
	switch typed := declared.(type) {
	case string:
		if typed != "object" {
			return fmt.Errorf("output_schema root type is %q; a structured result is an object", typed)
		}
	case []any:
		if len(typed) == 0 {
			return fmt.Errorf("output_schema root type list is empty")
		}
		for _, entry := range typed {
			if entry != "object" {
				return fmt.Errorf("output_schema root type list names %v; a structured result is an object", entry)
			}
		}
	default:
		return fmt.Errorf("output_schema root type is not a string or a list of strings")
	}
	return nil
}

func (s *OutputSchema) Validate(document json.RawMessage) error {
	if s == nil || s.compiled == nil {
		return fmt.Errorf("output_schema was not compiled")
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(document))
	if err != nil {
		return fmt.Errorf("result is not valid JSON: %w", err)
	}
	return s.compiled.Validate(value)
}

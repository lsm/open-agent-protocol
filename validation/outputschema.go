package validation

import (
	"bytes"
	"encoding/json"
	"fmt"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// OutputSchema is a compiled `output_schema` control: the JSON Schema a
// submission asks the run's final response to conform to.
//
// The same function compiles it for the validator and for an adapter, so the
// two cannot disagree about which schemas are satisfiable. A schema the
// reference adapter refuses is one the validator calls unsatisfiable, and a
// schema that compiles here is one whose refusal the validator diagnoses.
type OutputSchema struct{ compiled *jsonschema.Schema }

const outputSchemaBase = "https://open-agent-protocol.local/output-schema"

// CompileOutputSchema compiles one submitted output_schema. It fails when the
// document is not a schema the engine will take (any metaschema or compilation
// failure), when its root type is not "object", or when it references anything
// outside itself. The first two named cases are instances of the general rule,
// not an enumeration of it: `{"type":"object","required":"x"}` is wire-valid,
// object-rooted, and self-contained, and no compiler will take it either.
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
	// run.completed.result is an object in the schema bundle and a JSON object
	// in every adapter, so a root-array or scalar schema could never be met by
	// a conforming success.
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

// objectRooted enforces the root `type`: absent is permitted (the schema
// constrains nothing about the root kind and an object satisfies it), a string
// must be "object", and a list may name only "object".
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

// Validate reports whether one result document conforms to the admitted
// schema.
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

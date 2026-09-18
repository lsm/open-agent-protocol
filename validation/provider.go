package validation

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	CodeCredentialInTrace   = "credential_in_trace"
	CodeTerminalNotAssembly = "terminal_not_assembly"
)

type ProviderValidator struct {
	schema *jsonschema.Schema
	mode   Mode
}

func NewProviderValidator() (*ProviderValidator, error) {
	return NewProviderValidatorWith(ModeStrict)
}

func NewProviderValidatorWith(mode Mode) (*ProviderValidator, error) {
	if mode == "" {
		mode = ModeStrict
	}
	bundle, err := compileBundle(CompileOptions{Mode: mode})
	if err != nil {
		return nil, err
	}
	if bundle.provider == nil {
		return nil, fmt.Errorf("compile schema bundle: provider envelope schema not found")
	}
	return &ProviderValidator{schema: bundle.provider, mode: mode}, nil
}

type providerEnvelope struct {
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	InferenceID string          `json:"inference_id"`
	Payload     json.RawMessage `json:"payload"`
}

type providerPartEnded struct {
	PartIndex int    `json:"part_index"`
	PartKind  string `json:"part_kind"`
	Text      string `json:"text"`
	ToolCall  *struct {
		ToolCallID    string          `json:"tool_call_id"`
		Name          string          `json:"name"`
		ArgumentsJSON json.RawMessage `json:"arguments_json"`
	} `json:"tool_call"`
}

type providerCompleted struct {
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func (v *ProviderValidator) Validate(r io.Reader, fixture string) Result {
	raw, parseDiag := parseTrace(r, fixture)
	if parseDiag != nil {
		return Result{Diagnostics: []Diagnostic{*parseDiag}}
	}
	result := Result{}
	parts := map[string][]providerPartEnded{}
	settled := map[string]string{}
	var terminals []providerTerminal
	for i, entry := range raw {
		var document any
		if err := json.Unmarshal(entry.raw, &document); err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Fixture: fixture, Phase: PhaseDecode, Code: CodeMalformedJSON, Index: i, Line: entry.line, Message: err.Error()})
			continue
		}
		if key, duplicate := duplicateKey(entry.raw); duplicate {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Fixture: fixture, Phase: PhaseDecode, Code: CodeDuplicateKey, Index: i, Line: entry.line, Message: fmt.Sprintf("duplicate object key %q", key)})
			continue
		}
		var e providerEnvelope
		if err := json.Unmarshal(entry.raw, &e); err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Fixture: fixture, Phase: PhaseDecode, Code: CodePayloadDecode, Index: i, Line: entry.line, Message: err.Error()})
			continue
		}
		if e.Type == "provider.credential.grant.request" || e.Type == "provider.credential.grant.response" || e.Type == "provider.credential.grant.channel" {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{
				Fixture: fixture, Phase: PhaseSemantic, Code: CodeCredentialInTrace, Index: i, Line: entry.line,
				EnvelopeID: e.ID, Type: e.Type, Pointer: "/type",
				Expected: "no credential exchange in an assembled trace", Actual: e.Type,
				Message: "a credential grant exchange is not journalled, traced or replayed, so a trace containing one was assembled from a stream that recorded a secret",
			})
			continue
		}
		if err := v.schema.Validate(document); err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Fixture: fixture, Phase: PhaseSchema, Code: CodeSchemaInvalid, Index: i, Line: entry.line, EnvelopeID: e.ID, Type: e.Type, Message: err.Error()})
			continue
		}
		if e.InferenceID != "" && settled[e.InferenceID] != "" && scopedEvent(e.Type) {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{
				Fixture: fixture, Phase: PhaseSemantic, Code: CodeEventAfterTerminal, Index: i, Line: entry.line,
				EnvelopeID: e.ID, Type: e.Type, Pointer: "/type", RelatedIDs: []string{settled[e.InferenceID]},
				Expected: "no scoped event after a terminal", Actual: e.Type,
				Message: "an inference emitted a scoped event after it had already settled",
			})
		}
		switch e.Type {
		case "inference.part.ended":
			var p providerPartEnded
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{Fixture: fixture, Phase: PhaseDecode, Code: CodePayloadDecode, Index: i, Line: entry.line, EnvelopeID: e.ID, Type: e.Type, Message: err.Error()})
				continue
			}
			parts[e.InferenceID] = append(parts[e.InferenceID], p)
		case "inference.completed":
			var p providerCompleted
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{Fixture: fixture, Phase: PhaseDecode, Code: CodePayloadDecode, Index: i, Line: entry.line, EnvelopeID: e.ID, Type: e.Type, Message: err.Error()})
				continue
			}
			terminals = append(terminals, providerTerminal{envelope: e, index: i, line: entry.line, payload: p})
			settled[e.InferenceID] = e.ID
		case "inference.failed":
			settled[e.InferenceID] = e.ID
		}
	}
	for _, t := range terminals {
		if diag := assemblyDefect(fixture, t.index, t.line, t.envelope, parts[t.envelope.InferenceID], t.payload); diag != nil {
			result.Diagnostics = append(result.Diagnostics, *diag)
		}
	}
	sortDiagnostics(result.Diagnostics)
	return result
}

type providerTerminal struct {
	envelope providerEnvelope
	index    int
	line     int
	payload  providerCompleted
}

func scopedEvent(kind string) bool {
	switch kind {
	case "inference.started", "inference.part.started", "inference.part.delta", "inference.part.ended",
		"inference.completed", "inference.failed":
		return true
	}
	return false
}

func assemblyDefect(fixture string, index, line int, e providerEnvelope, ended []providerPartEnded, p providerCompleted) *Diagnostic {
	if len(ended) == 0 {
		return nil
	}
	ordered := append([]providerPartEnded(nil), ended...)
	sort.SliceStable(ordered, func(a, b int) bool { return ordered[a].PartIndex < ordered[b].PartIndex })
	ended = ordered
	var terminal []map[string]json.RawMessage
	if err := json.Unmarshal(p.Message.Content, &terminal); err != nil {
		return &Diagnostic{
			Fixture: fixture, Phase: PhaseSemantic, Code: CodeTerminalNotAssembly, Index: index, Line: line,
			EnvelopeID: e.ID, Type: e.Type, Pointer: "/payload/message/content",
			Expected: fmt.Sprintf("%d content parts assembled from the ended parts", len(ended)), Actual: "content that is not a part array",
			Message: "an inference that streamed parts settled with a terminal whose content is not their assembly",
		}
	}
	if len(terminal) != len(ended) {
		return &Diagnostic{
			Fixture: fixture, Phase: PhaseSemantic, Code: CodeTerminalNotAssembly, Index: index, Line: line,
			EnvelopeID: e.ID, Type: e.Type, Pointer: "/payload/message/content",
			Expected: fmt.Sprintf("%d content parts", len(ended)), Actual: fmt.Sprintf("%d", len(terminal)),
			Message: "the terminal message does not carry one content part per ended part",
		}
	}
	for i, part := range ended {
		if diag := partMismatch(fixture, index, line, e, i, part, terminal[i]); diag != nil {
			return diag
		}
	}
	return nil
}

func partMismatch(fixture string, index, line int, e providerEnvelope, at int, ended providerPartEnded, terminal map[string]json.RawMessage) *Diagnostic {
	pointer := fmt.Sprintf("/payload/message/content/%d", at)
	defect := func(expected, actual, message string) *Diagnostic {
		return &Diagnostic{
			Fixture: fixture, Phase: PhaseSemantic, Code: CodeTerminalNotAssembly, Index: index, Line: line,
			EnvelopeID: e.ID, Type: e.Type, Pointer: pointer, Expected: expected, Actual: actual, Message: message,
		}
	}
	var kind string
	if raw, ok := terminal["type"]; ok {
		_ = json.Unmarshal(raw, &kind)
	}
	if kind != ended.PartKind {
		return defect(ended.PartKind, kind, "the terminal's content part is not the kind the part that ended declared")
	}
	switch ended.PartKind {
	case "text", "reasoning":
		var text string
		if raw, ok := terminal[ended.PartKind]; ok {
			_ = json.Unmarshal(raw, &text)
		}
		if text != ended.Text {
			return defect(ended.Text, text, "the terminal's text is not the accumulated string the part ended with")
		}
	case "tool_call":
		if ended.ToolCall == nil {
			return nil
		}
		var id, name string
		if raw, ok := terminal["tool_call_id"]; ok {
			_ = json.Unmarshal(raw, &id)
		}
		if raw, ok := terminal["name"]; ok {
			_ = json.Unmarshal(raw, &name)
		}
		if id != ended.ToolCall.ToolCallID || name != ended.ToolCall.Name {
			return defect(ended.ToolCall.ToolCallID+" "+ended.ToolCall.Name, id+" "+name, "the terminal's tool call is not the one the part ended with")
		}
		if !sameJSON(terminal["arguments_json"], ended.ToolCall.ArgumentsJSON) {
			return defect(string(ended.ToolCall.ArgumentsJSON), string(terminal["arguments_json"]), "the terminal's tool call arguments are not the ones the part ended with")
		}
	}
	return nil
}

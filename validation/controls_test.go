package validation

import (
	"encoding/json"
	"strings"
	"testing"
)

// The run-controls rules the fixture corpus cannot state on its own: the
// refusal precedence, the pointer ordering within one rule, and the boundary
// between a refusal the validator judges and one it leaves alone.

const controlsCore = `"profile":"open-agent-protocol.agent-control-core","protocol":"open-agent-protocol","version":"0.1"`

// controlsTrace assembles a capability exchange, one submit carrying the given
// controls, and one response, so a test states only what it is about.
func controlsTrace(features, controls, response string) []byte {
	catalog := `,"tools":[{"name":"scripted_tool","input_schema":{"type":"object"},"execution_owner":"agent"}]`
	descriptor := `{` + controlsCore + `,"type":"capabilities.response","id":"caps-resp","in_reply_to":"caps-req","capability_revision":"rev-1","payload":{"endpoint":{"id":"fixture"},"features":` + features + catalog + `}}`
	submit := `{` + controlsCore + `,"type":"session.message.submit.request","id":"submit-req","session_id":"s1","capability_revision":"rev-1","payload":{"session_id":"s1","messages":[{"role":"user","content":"go"}],"delivery":"auto"` + controls + `}}`
	return []byte(`[{` + controlsCore + `,"type":"capabilities.request","id":"caps-req","payload":{}},` + descriptor + `,` + submit + `,` + response + `]`)
}

const controlsAdmission = `{` + controlsCore + `,"type":"session.message.submit.response","id":"submit-resp","in_reply_to":"submit-req","session_id":"s1","run_id":"r1","capability_revision":"rev-1","payload":{"session_id":"s1","accepted":true,"submission_id":"sub1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"r1","status":"running"}},
	{` + controlsCore + `,"type":"run.started","id":"ev-start","session_id":"s1","run_id":"r1","sequence":1,"payload":{"session_id":"s1","run_id":"r1","status":"running"}},
	{` + controlsCore + `,"type":"run.completed","id":"ev-done","session_id":"s1","run_id":"r1","sequence":2,"payload":{"session_id":"s1","run_id":"r1","final_response":{"role":"assistant","content":"ok"},"stop_reason":"end_turn"}}`

// refusalEnvelope renders one correlated error.response.
func refusalEnvelope(code string, details map[string]any) string {
	encoded, _ := json.Marshal(details)
	payload := `{"error":{"code":"` + code + `","message":"refused"`
	if details != nil {
		payload += `,"details":` + string(encoded)
	}
	payload += `}}`
	return `{` + controlsCore + `,"type":"error.response","id":"err","in_reply_to":"submit-req","session_id":"s1","payload":` + payload + `}`
}

// A request can fail several fail-closed tests at once and one error.response
// carries one code, so the expectations are ranked rather than conjoined: the
// capability rung outranks the degradation rung whichever field carries it,
// and a caller told to stop sending something learns more than one handed an
// opt-in it could have supplied.
func TestRefusalPrecedencePrefersTheCapabilityRung(t *testing.T) {
	v := MustNew()
	features := `{"run.model_selection":{"level":"degraded","mode":"per_run","reason":"attribution is unconfirmed"},"run.instructions":{"level":"unavailable","reason":"no per-run surface"}}`
	controls := `,"model_id":"m1","instructions":"be terse"`
	// The conforming refusal names the unadvertised control, not the
	// degraded one.
	conforming := controlsTrace(features, controls, refusalEnvelope("unsupported_feature", map[string]any{"feature": "run.instructions", "reason": "unadvertised"}))
	if got := v.ValidateBytes(conforming, "precedence-conforming"); !got.Valid() {
		t.Fatalf("a refusal on the capability rung was rejected: %+v", got.Diagnostics)
	}
	// Refusing under the lower rung leaves the caller sending something the
	// endpoint will never accept.
	wrongRung := controlsTrace(features, controls, refusalEnvelope("capability_degraded", map[string]any{"feature": "run.model_selection"}))
	got := v.ValidateBytes(wrongRung, "precedence-wrong-rung")
	if !got.HasCode(CodeUnavailableCapability) {
		t.Fatalf("want %s when the capability rung is answered with the degradation rung: %+v", CodeUnavailableCapability, got.Diagnostics)
	}
}

// Within one rung the expectation whose capability key sorts first wins, so a
// request failing two capability gates owes one determinate refusal.
func TestRefusalPrecedenceOrdersPeersByKey(t *testing.T) {
	v := MustNew()
	features := `{"run.instructions":{"level":"unavailable","reason":"none"},"run.structured_output":{"level":"unavailable","reason":"none"}}`
	controls := `,"instructions":"be terse","output_schema":{"type":"object"}`
	// run.instructions sorts before run.structured_output.
	if got := v.ValidateBytes(controlsTrace(features, controls, refusalEnvelope("unsupported_feature", map[string]any{"feature": "run.instructions", "reason": "unadvertised"})), "peers-first"); !got.Valid() {
		t.Fatalf("the lower key's refusal was rejected: %+v", got.Diagnostics)
	}
	if got := v.ValidateBytes(controlsTrace(features, controls, refusalEnvelope("unsupported_feature", map[string]any{"feature": "run.structured_output", "reason": "unadvertised"})), "peers-second"); !got.HasCode(CodeUnavailableCapability) {
		t.Fatalf("want %s when the refusal names the later key: %+v", CodeUnavailableCapability, got.Diagnostics)
	}
}

// One rule can fail twice over, so the ordering is carried down to the
// offending value: the winner is the entry with the lowest JSON Pointer, which
// inside an array is the caller's own order.
func TestUnsatisfiableToolChoiceNamesTheFirstOffendingEntry(t *testing.T) {
	v := MustNew()
	features := `{"run.tool_selection":{"level":"emulated","modes":["auto"]}}`
	controls := `,"tool_choice":{"mode":"auto","allowed":["absent_a","absent_b"]}`
	trace := func(tool string) []byte {
		return controlsTrace(features, controls, refusalEnvelope("unsupported_feature", map[string]any{"feature": "run.tool_selection", "reason": "unsatisfiable", "tool": tool}))
	}
	if got := v.ValidateBytes(trace("absent_a"), "first-entry"); !got.Valid() {
		t.Fatalf("a refusal naming the first offending entry was rejected: %+v", got.Diagnostics)
	}
	if got := v.ValidateBytes(trace("absent_b"), "second-entry"); !got.HasCode(CodeUnsatisfiableControl) {
		t.Fatalf("want %s when the refusal names the later entry: %+v", CodeUnsatisfiableControl, got.Diagnostics)
	}
}

// The gate judges the response, never the request, because the wire requires
// the endpoint to refuse and diagnosing the request would fail the behaviour
// it mandates. A refusal that is not about a control at all is left alone.
func TestControlGateLeavesUnrelatedRefusalsAlone(t *testing.T) {
	v := MustNew()
	features := `{"run.instructions":{"level":"emulated"}}`
	trace := controlsTrace(features, `,"instructions":"be terse"`, refusalEnvelope("run_active", map[string]any{"run_id": "r0"}))
	if got := v.ValidateBytes(trace, "unrelated-refusal"); !got.Valid() {
		t.Fatalf("a state refusal was judged as a control refusal: %+v", got.Diagnostics)
	}
}

// A tool_choice mode the endpoint never said it enforces is one it may refuse.
// Without that, run.tool_selection would promise nothing: an endpoint could
// advertise it, refuse every required and named policy, and pass.
func TestUndisclosedToolChoiceModeMayBeRefused(t *testing.T) {
	v := MustNew()
	features := `{"run.tool_selection":{"level":"emulated","modes":["auto"]}}`
	refused := refusalEnvelope("unsupported_feature", map[string]any{"feature": "run.tool_selection", "reason": "unsatisfiable"})
	if got := v.ValidateBytes(controlsTrace(features, `,"tool_choice":{"mode":"required"}`, refused), "undisclosed-mode"); !got.Valid() {
		t.Fatalf("refusing an undisclosed mode was diagnosed: %+v", got.Diagnostics)
	}
	if got := v.ValidateBytes(controlsTrace(features, `,"tool_choice":{"mode":"auto"}`, refused), "disclosed-mode"); !got.HasCode(CodeUnsatisfiableControl) {
		t.Fatalf("want %s when a disclosed mode is refused: %+v", CodeUnsatisfiableControl, got.Diagnostics)
	}
}

// An output_schema is compiled with a loader that refuses every reference
// outside the document, so an untrusted schema can never make the validator
// read a local path or fetch a URL.
func TestOutputSchemaCompilationIsSelfContained(t *testing.T) {
	for name, document := range map[string]string{
		"absolute reference": `{"type":"object","properties":{"a":{"$ref":"https://example.test/s.json"}}}`,
		"file reference":     `{"type":"object","properties":{"a":{"$ref":"file:///etc/hosts"}}}`,
		"relative reference": `{"type":"object","properties":{"a":{"$ref":"other.json"}}}`,
		"root array":         `{"type":"array"}`,
		"root scalar":        `{"type":"string"}`,
		"type list":          `{"type":["object","null"]}`,
		"uncompilable":       `{"type":"object","required":"answer"}`,
		"not an object":      `[]`,
	} {
		if _, err := CompileOutputSchema(json.RawMessage(document)); err == nil {
			t.Fatalf("%s: compiled a schema that should be unsatisfiable", name)
		}
	}
	for name, document := range map[string]string{
		"object root":      `{"type":"object","required":["answer"]}`,
		"untyped root":     `{"properties":{"answer":{"type":"string"}}}`,
		"internal pointer": `{"type":"object","properties":{"a":{"$ref":"#/$defs/x"}},"$defs":{"x":{"type":"string"}}}`,
	} {
		compiled, err := CompileOutputSchema(json.RawMessage(document))
		if err != nil {
			t.Fatalf("%s: rejected a satisfiable schema: %v", name, err)
		}
		if compiled == nil {
			t.Fatalf("%s: compiled to nothing", name)
		}
	}
}

// A compiled schema judges the result a run completed with.
func TestOutputSchemaValidatesResults(t *testing.T) {
	compiled, err := CompileOutputSchema(json.RawMessage(`{"type":"object","required":["answer"],"properties":{"answer":{"type":"string"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled.Validate(json.RawMessage(`{"answer":"42"}`)); err != nil {
		t.Fatalf("conforming result rejected: %v", err)
	}
	if err := compiled.Validate(json.RawMessage(`{"answer":42}`)); err == nil {
		t.Fatal("nonconforming result accepted")
	}
	if err := compiled.Validate(json.RawMessage(`{}`)); err == nil {
		t.Fatal("result missing a required member accepted")
	}
}

// Pointer order compares segment by segment, numerically inside an array, so
// two encodings of one request owe the same refusal.
func TestPointerOrderIsSegmentWise(t *testing.T) {
	ordered := []string{
		"/payload/tool_choice/allowed/0",
		"/payload/tool_choice/allowed/2",
		"/payload/tool_choice/allowed/10",
		"/payload/tool_choice/disallowed/0",
		"/payload/tool_choice/mode",
		"/payload/tool_choice/name",
	}
	for i := 0; i+1 < len(ordered); i++ {
		if !pointerLess(ordered[i], ordered[i+1]) {
			t.Fatalf("%s should precede %s", ordered[i], ordered[i+1])
		}
		if pointerLess(ordered[i+1], ordered[i]) {
			t.Fatalf("%s should not precede %s", ordered[i+1], ordered[i])
		}
	}
}

// Every diagnostic the unit introduces is registered, or a fixture asserting
// it could not be declared.
func TestRunControlDiagnosticsAreRegistered(t *testing.T) {
	known := diagnosticCodes()
	for _, code := range []string{
		CodeUnappliedControl, CodeUnsatisfiableControl, CodeDegradedWithoutOptin,
		CodeDuplicateToolName, CodeUndisclosedSelectionModes,
	} {
		if !known[code] {
			t.Fatalf("diagnostic %q is not registered", code)
		}
	}
}

// The unit's capability keys are registered, so the corpus-completeness check
// holds this unit to both aspects of each.
func TestRunControlCapabilitiesAreRegistered(t *testing.T) {
	keys := strings.Join(unitCapabilities["run-controls"], " ")
	for _, key := range []string{"run.model_selection", "run.instructions", "run.tool_selection", "run.structured_output"} {
		if !strings.Contains(keys, key) {
			t.Fatalf("capability %q is not registered under run-controls: %q", key, keys)
		}
	}
}

package validation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/protocol"
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
// outside the registered resources, so an untrusted schema can never make the validator
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

// The schema admits any JSON value for tool_choice, so the validator must
// classify every one of them rather than assume a decoded policy. A present
// null is a control that is not the typed policy: presence is what the gate
// judges, the same rule that makes an empty model_id a control.
func TestNullToolChoiceIsUnsatisfiable(t *testing.T) {
	v := MustNew()
	features := `{"run.tool_selection":{"level":"emulated","modes":["auto"]}}`
	for name, control := range map[string]string{
		"null":    `,"tool_choice":null`,
		"string":  `,"tool_choice":"none"`,
		"number":  `,"tool_choice":3`,
		"boolean": `,"tool_choice":true`,
		"array":   `,"tool_choice":["scripted_tool"]`,
	} {
		admitted := v.ValidateBytes(controlsTrace(features, control, controlsAdmission), "untyped-"+name)
		if !admitted.HasCode(CodeUnsatisfiableControl) {
			t.Fatalf("%s: want %s when an untyped tool_choice is admitted: %+v", name, CodeUnsatisfiableControl, admitted.Diagnostics)
		}
		refused := v.ValidateBytes(
			controlsTrace(features, control, refusalEnvelope("unsupported_feature", map[string]any{"feature": "run.tool_selection", "reason": "unsatisfiable"})),
			"untyped-refused-"+name,
		)
		if !refused.Valid() {
			t.Fatalf("%s: the typed refusal was rejected: %+v", name, refused.Diagnostics)
		}
	}
}

// An absent tool_choice is not a control at all, so nothing is judged.
func TestAbsentToolChoiceIsNotAControl(t *testing.T) {
	policy, err := protocol.MessageSubmitRequest{}.ToolChoicePolicy()
	if policy != nil || err != nil {
		t.Fatalf("absent tool_choice decoded as %+v, %v", policy, err)
	}
	if _, err := (protocol.MessageSubmitRequest{ToolChoice: json.RawMessage(`null`)}).ToolChoicePolicy(); err == nil {
		t.Fatal("a present null decoded as an absent control")
	}
}

// A declared fixed_result is an exact promise, so the comparison must not pass
// numbers through float64: the two integers below differ by one and are the
// same float64.
func TestFixedResultComparisonKeepsIntegerPrecision(t *testing.T) {
	for name, pair := range map[string][2]string{
		"integers past 2^53": {`{"n":9007199254740992}`, `{"n":9007199254740993}`},
		"nested":             {`{"a":{"n":9007199254740992}}`, `{"a":{"n":9007199254740993}}`},
		"in an array":        {`{"a":[9007199254740992]}`, `{"a":[9007199254740993]}`},
	} {
		if sameJSON(json.RawMessage(pair[0]), json.RawMessage(pair[1])) {
			t.Fatalf("%s: %s and %s compared equal", name, pair[0], pair[1])
		}
	}
	// Member order and whitespace still do not decide it.
	for name, pair := range map[string][2]string{
		"member order": {`{"a":1,"b":2}`, `{"b":2, "a":1}`},
		"whitespace":   {`{"ok":true}`, "{\n  \"ok\": true\n}"},
		"same integer": {`{"n":9007199254740993}`, `{"n":9007199254740993}`},
	} {
		if !sameJSON(json.RawMessage(pair[0]), json.RawMessage(pair[1])) {
			t.Fatalf("%s: %s and %s compared unequal", name, pair[0], pair[1])
		}
	}
}

// A tool_choice is a policy over the effective catalog: allowed and disallowed
// filter the catalog, then the mode applies to what is left, so the permitted
// set never reaches past the catalog. Without that first step a plain auto or
// required policy admits any name at all and an admitted policy governs
// nothing. The gate already binds the catalog in both directions; the honour
// side must bind it the same way.
func TestPolicyPermitsOnlyCataloguedTools(t *testing.T) {
	catalog := []string{"scripted_tool", "other_tool"}
	for name, policy := range map[string]protocol.ToolChoice{
		"auto":                 {Mode: protocol.ToolChoiceAuto},
		"required":             {Mode: protocol.ToolChoiceRequired},
		"auto with allowed":    {Mode: protocol.ToolChoiceAuto, Allowed: []string{"scripted_tool"}},
		"auto with disallowed": {Mode: protocol.ToolChoiceAuto, Disallowed: []string{"other_tool"}},
	} {
		if policy.Permits("unlisted_tool", catalog, true) {
			t.Fatalf("%s: a tool the catalog does not carry was permitted", name)
		}
		if !policy.Permits("scripted_tool", catalog, true) {
			t.Fatalf("%s: a catalogued tool the policy admits was refused", name)
		}
		// An empty catalog is a catalog and permits nothing, the same reading
		// that makes `required` against an empty filtered set unsatisfiable.
		if policy.Permits("scripted_tool", nil, true) {
			t.Fatalf("%s: an empty catalog permitted a tool", name)
		}
		// A trace carrying no catalog cannot decide membership, so the filter
		// and the mode judge alone rather than refusing everything.
		if !policy.Permits("scripted_tool", nil, false) {
			t.Fatalf("%s: an unknown catalog was read as an empty one", name)
		}
	}
	// Within the catalog the filter and the mode still decide.
	for name, policy := range map[string]protocol.ToolChoice{
		"disallowed":      {Mode: protocol.ToolChoiceAuto, Disallowed: []string{"other_tool"}},
		"outside allowed": {Mode: protocol.ToolChoiceAuto, Allowed: []string{"scripted_tool"}},
		"named elsewhere": {Mode: protocol.ToolChoiceNamed, Name: "scripted_tool"},
		"none":            {Mode: protocol.ToolChoiceNone},
	} {
		if policy.Permits("other_tool", catalog, true) {
			t.Fatalf("%s: the policy permitted a tool it excludes", name)
		}
	}
}

// modelAdmission renders an admission reporting one model, a run.started that
// names the given model or omits it, and a completion.
func modelAdmission(admitted, started string) string {
	start := `"session_id":"s1","run_id":"r1","status":"running"`
	if started != "" {
		start += `,"model_id":"` + started + `"`
	}
	return `{` + controlsCore + `,"type":"session.message.submit.response","id":"submit-resp","in_reply_to":"submit-req","session_id":"s1","run_id":"r1","capability_revision":"rev-1","payload":{"session_id":"s1","accepted":true,"submission_id":"sub1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"r1","status":"running","model_id":"` + admitted + `"}},
	{` + controlsCore + `,"type":"run.started","id":"ev-start","session_id":"s1","run_id":"r1","sequence":1,"payload":{` + start + `}},
	{` + controlsCore + `,"type":"run.completed","id":"ev-done","session_id":"s1","run_id":"r1","sequence":2,"payload":{"session_id":"s1","run_id":"r1","final_response":{"role":"assistant","content":"ok"},"stop_reason":"end_turn"}}`
}

// An admitted model_id is authoritative for the run: the submit response
// repeats it and run.started repeats it. Omitting it there is not silence
// about a model nobody chose — the caller chose one, and a consumer reading
// the start boundary cannot see the control was applied, which is what the
// repeat exists for. A run whose submission carried no model_id keeps the
// present-only comparison, since there the id is attribution the endpoint
// volunteers rather than a control it owes.
func TestStartedRepeatsTheAdmittedModel(t *testing.T) {
	v := MustNew()
	features := `{"run.model_selection":{"level":"emulated","mode":"per_run"}}`
	omitted := v.ValidateBytes(controlsTrace(features, `,"model_id":"m1"`, modelAdmission("m1", "")), "started-omits-model")
	if !omitted.HasCode(CodeUnappliedControl) {
		t.Fatalf("want %s when run.started omits the admitted model: %+v", CodeUnappliedControl, omitted.Diagnostics)
	}
	repeated := v.ValidateBytes(controlsTrace(features, `,"model_id":"m1"`, modelAdmission("m1", "m1")), "started-repeats-model")
	if !repeated.Valid() {
		t.Fatalf("a run.started repeating the admitted model was rejected: %+v", repeated.Diagnostics)
	}
	// No control, so no control to apply: the reported model is attribution.
	attribution := v.ValidateBytes(controlsTrace(features, "", modelAdmission("m1", "")), "started-omits-attribution")
	if !attribution.Valid() {
		t.Fatalf("an uncontrolled run was judged against a volunteered model: %+v", attribution.Diagnostics)
	}
}

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

// unsupported_feature answers about one capability, so the key is the
// refusal's subject: a refusal that omits it, or names another key, tells the
// caller no more than that something was unsupported — and the caller's next
// move is to stop sending a control it now cannot identify. The
// unsatisfiability rung carries the offending member in details.tool or
// details.field, so the feature there is checked by nothing else.
func TestRefusalMustNameTheFeatureItAnswersFor(t *testing.T) {
	v := MustNew()
	features := `{"run.tool_selection":{"level":"emulated","modes":["auto","none","required","named"]}}`
	control := `,"tool_choice":{"mode":"named","name":"absent_tool"}`
	for name, details := range map[string]map[string]any{
		"feature omitted":      {"reason": "unsatisfiable", "tool": "absent_tool"},
		"wrong feature":        {"feature": "run.structured_output", "reason": "unsatisfiable", "tool": "absent_tool"},
		"feature not a string": {"feature": 7, "reason": "unsatisfiable", "tool": "absent_tool"},
	} {
		result := v.ValidateBytes(controlsTrace(features, control, refusalEnvelope("unsupported_feature", details)), "refusal-"+name)
		if !result.HasCode(CodeUnsatisfiableControl) {
			t.Fatalf("%s: want %s: %+v", name, CodeUnsatisfiableControl, result.Diagnostics)
		}
	}
	conforming := refusalEnvelope("unsupported_feature", map[string]any{"feature": "run.tool_selection", "reason": "unsatisfiable", "tool": "absent_tool"})
	if result := v.ValidateBytes(controlsTrace(features, control, conforming), "refusal-conforming"); !result.Valid() {
		t.Fatalf("a refusal naming its own feature was rejected: %+v", result.Diagnostics)
	}
}

// The typed policy is stated in terms of member presence: `name` when and only
// when the mode is `named`, `allowed` and `disallowed` mutually exclusive. A
// decode into value fields cannot see presence — `"name": ""` and `"name":
// null` both land as the empty string, two empty lists as two empty slices —
// so a policy whose shape is wrong would read as one whose members were simply
// absent, and the endpoint would run under a policy nobody wrote.
func TestToolChoiceShapeIsJudgedByMemberPresence(t *testing.T) {
	for name, encoded := range map[string]string{
		"empty name off named mode": `{"mode":"auto","name":""}`,
		"null name off named mode":  `{"mode":"auto","name":null}`,
		"null name on named mode":   `{"mode":"named","name":null}`,
		"empty name on named mode":  `{"mode":"named","name":""}`,
		"both filters empty":        `{"mode":"auto","allowed":[],"disallowed":[]}`,
		"both filters present":      `{"mode":"auto","allowed":["scripted_tool"],"disallowed":[]}`,
		"null allowed":              `{"mode":"auto","allowed":null}`,
		"null disallowed":           `{"mode":"auto","disallowed":null}`,
	} {
		policy, err := (protocol.MessageSubmitRequest{ToolChoice: json.RawMessage(encoded)}).ToolChoicePolicy()
		if err == nil {
			t.Fatalf("%s: %s decoded as the typed policy %+v", name, encoded, policy)
		}
	}
	// One filter, empty or not, is still the typed shape: an empty allowed
	// list filters every tool out, which is empty rather than contradictory.
	for name, encoded := range map[string]string{
		"empty allowed":    `{"mode":"auto","allowed":[]}`,
		"empty disallowed": `{"mode":"auto","disallowed":[]}`,
		"named with name":  `{"mode":"named","name":"scripted_tool"}`,
		"bare auto":        `{"mode":"auto"}`,
	} {
		if _, err := (protocol.MessageSubmitRequest{ToolChoice: json.RawMessage(encoded)}).ToolChoicePolicy(); err != nil {
			t.Fatalf("%s: %s was refused as untyped: %v", name, encoded, err)
		}
	}
	// Every shape above is a control the endpoint must refuse as
	// unsatisfiable, not one it may read past.
	v := MustNew()
	features := `{"run.tool_selection":{"level":"emulated","modes":["auto"]}}`
	admitted := v.ValidateBytes(controlsTrace(features, `,"tool_choice":{"mode":"auto","allowed":[],"disallowed":[]}`, controlsAdmission), "empty-filters")
	if !admitted.HasCode(CodeUnsatisfiableControl) {
		t.Fatalf("want %s when both filters are present: %+v", CodeUnsatisfiableControl, admitted.Diagnostics)
	}
}

// An `allowed` the caller sent empty permits no tool at all. Length cannot say
// that — an empty list and an absent one are both zero-length — so reading the
// filter by length turns the most restrictive allowlist expressible into the
// most permissive one: `required` would be admitted against the whole catalog
// and `auto` would permit every tool in it, which is the opposite of what the
// caller wrote. Presence is what the filter is defined by, here as in the
// shape rules.
func TestEmptyAllowlistPermitsNothing(t *testing.T) {
	catalog := []string{"scripted_tool", "other_tool"}
	empty := protocol.ToolChoice{Mode: protocol.ToolChoiceAuto, Allowed: []string{}}
	if filtered := empty.Filter(catalog); len(filtered) != 0 {
		t.Fatalf("an empty allowlist filtered to %v, want nothing", filtered)
	}
	for _, name := range catalog {
		if empty.Permits(name, catalog, true) {
			t.Fatalf("an empty allowlist permitted %q", name)
		}
	}
	// An absent allowlist is not an empty one: it filters nothing.
	absent := protocol.ToolChoice{Mode: protocol.ToolChoiceAuto}
	if filtered := absent.Filter(catalog); len(filtered) != len(catalog) {
		t.Fatalf("an absent allowlist filtered to %v, want the catalog", filtered)
	}
	if !absent.Permits("scripted_tool", catalog, true) {
		t.Fatalf("an absent allowlist refused a catalogued tool")
	}
	// `required` over an empty filtered set can never be honoured, so it is
	// unsatisfiable rather than admitted and run against the whole catalog.
	required := protocol.ToolChoice{Mode: protocol.ToolChoiceRequired, Allowed: []string{}}
	defect := required.Unsatisfiable(catalog, true)
	if defect == nil || defect.Pointer != "/payload/tool_choice/mode" {
		t.Fatalf("required over an empty allowlist: defect %+v, want the empty filtered set", defect)
	}
	// `named` against an empty allowlist names a tool its own list excludes.
	named := protocol.ToolChoice{Mode: protocol.ToolChoiceNamed, Name: "scripted_tool", Allowed: []string{}}
	if defect := named.Unsatisfiable(catalog, true); defect == nil {
		t.Fatal("named over an empty allowlist was satisfiable")
	}
	// `auto` over an empty allowlist is empty, not unsatisfiable: the run is
	// admitted and simply calls nothing.
	if defect := empty.Unsatisfiable(catalog, true); defect != nil {
		t.Fatalf("auto over an empty allowlist was refused: %+v", defect)
	}
}

// A descriptor advertising run.model_selection without saying how a selection
// is applied leaves the key promising nothing a validator can check: the
// per_run rule and the session_mutation rule both key on the mode, so with
// neither in force a session default could move under a per-run selection, or
// stay put under a mutation, with nothing to diagnose. Disclosure is
// machine-readable for this key as it is for the tool_choice modes beside it.
func TestModelSelectionMustDiscloseItsApplicationMode(t *testing.T) {
	v := MustNew()
	for name, support := range map[string]string{
		"missing":       `{"level":"emulated"}`,
		"empty":         `{"level":"emulated","mode":""}`,
		"unknown":       `{"level":"emulated","mode":"whenever"}`,
		"not yet ruled": `{"level":"emulated","mode":"restart"}`,
		"degraded":      `{"level":"degraded","mode":"","reason":"attribution is unconfirmed"}`,
	} {
		features := `{"run.model_selection":` + support + `}`
		result := v.ValidateBytes(controlsTrace(features, `,"model_id":"m1"`, modelAdmission("m1", "m1")), "mode-"+name)
		if !result.HasCode(CodeUndisclosedSelectionModes) {
			t.Fatalf("%s: want %s: %+v", name, CodeUndisclosedSelectionModes, result.Diagnostics)
		}
	}
	// Both modes this phase defines are disclosure enough, and a key the
	// endpoint does not advertise owes no mode at all.
	for name, features := range map[string]string{
		"per_run":          `{"run.model_selection":{"level":"emulated","mode":"per_run"}}`,
		"session_mutation": `{"run.model_selection":{"level":"native","mode":"session_mutation"}}`,
	} {
		result := v.ValidateBytes(controlsTrace(features, `,"model_id":"m1"`, modelAdmission("m1", "m1")), "mode-"+name)
		if !result.Valid() {
			t.Fatalf("%s: a disclosed mode was rejected: %+v", name, result.Diagnostics)
		}
	}
	unadvertised := `{"run.model_selection":{"level":"unavailable","reason":"no per-run surface"}}`
	refusal := refusalEnvelope("unsupported_feature", map[string]any{"feature": "run.model_selection", "reason": "unadvertised"})
	if result := v.ValidateBytes(controlsTrace(unadvertised, `,"model_id":"m1"`, refusal), "mode-unavailable"); !result.Valid() {
		t.Fatalf("an unavailable key was held to a mode: %+v", result.Diagnostics)
	}
}

// A `modes` list naming nothing a caller can send is the empty list in a
// costume: every policy the typed shape admits carries one of the four modes,
// so a descriptor listing none of them refuses every policy as unsatisfiable
// and passes — the empty advertisement the disclosure exists to prevent.
// Unknown names beside a recognised one are additive vocabulary: the
// descriptor still enforces the one it names here, and a mode a later unit
// defines must not make today's disclosure a defect.
func TestToolSelectionMustEnforceAModeCallersCanSend(t *testing.T) {
	v := MustNew()
	policy := `,"tool_choice":{"mode":"auto"}`
	for name, modes := range map[string]string{
		"none at all":     `[]`,
		"nothing known":   `["whenever"]`,
		"several unknown": `["whenever","someday"]`,
	} {
		features := `{"run.tool_selection":{"level":"emulated","modes":` + modes + `}}`
		result := v.ValidateBytes(controlsTrace(features, policy, controlsAdmission), "modes-"+name)
		if !result.HasCode(CodeUndisclosedSelectionModes) {
			t.Fatalf("%s: want %s: %+v", name, CodeUndisclosedSelectionModes, result.Diagnostics)
		}
	}
	for name, modes := range map[string]string{
		"one known":            `["auto"]`,
		"known and unknown":    `["auto","later_mode"]`,
		"every mode this unit": `["auto","none","required","named"]`,
	} {
		features := `{"run.tool_selection":{"level":"emulated","modes":` + modes + `}}`
		result := v.ValidateBytes(controlsTrace(features, policy, controlsAdmission), "modes-"+name)
		if !result.Valid() {
			t.Fatalf("%s: a disclosed mode was rejected: %+v", name, result.Diagnostics)
		}
	}
}

// Only an object is a structured result, so only an object is a fixed_result.
// The schema requires one, so a trace carrying anything else never reaches the
// semantic phase; if it ever did, the constraint is ignored rather than
// enforced, because a null or a scalar satisfies no object-rooted schema and
// reading it as a promise would make every structured-output request
// unsatisfiable at an endpoint whose descriptor looked conformant.
func TestFixedResultIsAnObjectOrNoConstraintAtAll(t *testing.T) {
	v := MustNew()
	schema := `,"output_schema":{"type":"object","properties":{"ok":{"type":"boolean"}}}`
	for name, fixed := range map[string]string{
		"null":   `null`,
		"scalar": `"ok"`,
		"number": `7`,
		"array":  `[{"ok":true}]`,
	} {
		features := `{"run.structured_output":{"level":"emulated","constraints":{"fixed_result":` + fixed + `}}}`
		result := v.ValidateBytes(controlsTrace(features, schema, controlsAdmission), "fixed-"+name)
		// The admission is not refused for a constraint the descriptor did not
		// state in the one shape a fixed result has.
		if result.HasCode(CodeUnsatisfiableControl) {
			t.Fatalf("%s: a fixed_result that is not an object refused a satisfiable schema: %+v", name, result.Diagnostics)
		}
	}
	// A declared object still binds, in both directions.
	features := `{"run.structured_output":{"level":"emulated","constraints":{"fixed_result":{"ok":true}}}}`
	unsatisfiable := v.ValidateBytes(controlsTrace(features, `,"output_schema":{"type":"object","required":["answer"]}`, controlsAdmission), "fixed-unsatisfiable")
	if !unsatisfiable.HasCode(CodeUnsatisfiableControl) {
		t.Fatalf("a schema the declared fixed result cannot satisfy was admitted: %+v", unsatisfiable.Diagnostics)
	}
}

// A declared fixed_result is an exact promise about a value, not about the
// token that spelled it: 1, 1.0, and 1e0 are one number, and an endpoint that
// emitted any of them kept a promise made with any other. Precision is the
// other half — the comparison must stay exact past 2^53, where float64 stops
// telling consecutive integers apart.
func TestFixedResultComparesNumbersByValue(t *testing.T) {
	for name, pair := range map[string][2]string{
		"integer and decimal":    {`{"n":1}`, `{"n":1.0}`},
		"integer and exponent":   {`{"n":1}`, `{"n":1e0}`},
		"decimal and exponent":   {`{"n":1.0}`, `{"n":1e0}`},
		"trailing zero":          {`{"n":1.5}`, `{"n":1.50}`},
		"negative exponent":      {`{"n":0.001}`, `{"n":1e-3}`},
		"large integer exponent": {`{"n":9007199254740992}`, `{"n":9.007199254740992e15}`},
		"nested":                 {`{"a":{"n":1}}`, `{"a":{"n":1.0}}`},
		"in an array":            {`{"a":[1]}`, `{"a":[1.0]}`},
	} {
		if !sameJSON(json.RawMessage(pair[0]), json.RawMessage(pair[1])) {
			t.Fatalf("%s: %s and %s compared unequal", name, pair[0], pair[1])
		}
	}
	for name, pair := range map[string][2]string{
		"consecutive past 2^53": {`{"n":9007199254740992}`, `{"n":9007199254740993}`},
		"different values":      {`{"n":1}`, `{"n":2}`},
		"sign":                  {`{"n":1}`, `{"n":-1}`},
		"number and string":     {`{"n":1}`, `{"n":"1"}`},
	} {
		if sameJSON(json.RawMessage(pair[0]), json.RawMessage(pair[1])) {
			t.Fatalf("%s: %s and %s compared equal", name, pair[0], pair[1])
		}
	}
}

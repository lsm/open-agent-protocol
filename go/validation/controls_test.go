package validation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

const controlsCore = `"profile":"open-agent-protocol.agent-control-core","protocol":"open-agent-protocol","version":"0.1"`

func controlsTrace(features, controls, response string) []byte {
	catalog := `,"tools":[{"name":"scripted_tool","input_schema":{"type":"object"},"execution_owner":"agent"}]`
	descriptor := `{` + controlsCore + `,"type":"capabilities.response","id":"caps-resp","in_reply_to":"caps-req","capability_revision":"rev-1","payload":{"endpoint":{"id":"fixture"},"features":` + features + catalog + `}}`
	submit := `{` + controlsCore + `,"type":"session.message.submit.request","id":"submit-req","session_id":"s1","capability_revision":"rev-1","payload":{"session_id":"s1","messages":[{"role":"user","content":"go"}],"delivery":"auto"` + controls + `}}`
	return []byte(`[{` + controlsCore + `,"type":"capabilities.request","id":"caps-req","payload":{}},` + descriptor + `,` + submit + `,` + response + `]`)
}

const controlsAdmission = `{` + controlsCore + `,"type":"session.message.submit.response","id":"submit-resp","in_reply_to":"submit-req","session_id":"s1","run_id":"r1","capability_revision":"rev-1","payload":{"session_id":"s1","accepted":true,"submission_id":"sub1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"r1","status":"running"}},
	{` + controlsCore + `,"type":"run.started","id":"ev-start","session_id":"s1","run_id":"r1","sequence":1,"payload":{"session_id":"s1","run_id":"r1","status":"running"}},
	{` + controlsCore + `,"type":"run.completed","id":"ev-done","session_id":"s1","run_id":"r1","sequence":2,"payload":{"session_id":"s1","run_id":"r1","final_response":{"role":"assistant","content":"ok"},"stop_reason":"end_turn"}}`

func refusalEnvelope(code string, details map[string]any) string {
	encoded, _ := json.Marshal(details)
	payload := `{"error":{"code":"` + code + `","message":"refused"`
	if details != nil {
		payload += `,"details":` + string(encoded)
	}
	payload += `}}`
	return `{` + controlsCore + `,"type":"error.response","id":"err","in_reply_to":"submit-req","session_id":"s1","payload":` + payload + `}`
}

func TestRefusalPrecedencePrefersTheCapabilityRung(t *testing.T) {
	v := MustNew()
	features := `{"run.model_selection":{"level":"degraded","scope":"run","reason":"attribution is unconfirmed"},"run.instructions":{"level":"unavailable","reason":"no per-run surface"}}`
	controls := `,"model_id":"m1","instructions":"be terse"`

	conforming := controlsTrace(features, controls, refusalEnvelope("unsupported_feature", map[string]any{"feature": "run.instructions", "reason": "unadvertised"}))
	if got := v.ValidateBytes(conforming, "precedence-conforming"); !got.Valid() {
		t.Fatalf("a refusal on the capability rung was rejected: %+v", got.Diagnostics)
	}

	wrongRung := controlsTrace(features, controls, refusalEnvelope("capability_degraded", map[string]any{"feature": "run.model_selection"}))
	got := v.ValidateBytes(wrongRung, "precedence-wrong-rung")
	if !got.HasCode(CodeUnavailableCapability) {
		t.Fatalf("want %s when the capability rung is answered with the degradation rung: %+v", CodeUnavailableCapability, got.Diagnostics)
	}
}

func TestRefusalPrecedenceOrdersPeersByKey(t *testing.T) {
	v := MustNew()
	features := `{"run.instructions":{"level":"unavailable","reason":"none"},"run.structured_output":{"level":"unavailable","reason":"none"}}`
	controls := `,"instructions":"be terse","output_schema":{"type":"object"}`

	if got := v.ValidateBytes(controlsTrace(features, controls, refusalEnvelope("unsupported_feature", map[string]any{"feature": "run.instructions", "reason": "unadvertised"})), "peers-first"); !got.Valid() {
		t.Fatalf("the lower key's refusal was rejected: %+v", got.Diagnostics)
	}
	if got := v.ValidateBytes(controlsTrace(features, controls, refusalEnvelope("unsupported_feature", map[string]any{"feature": "run.structured_output", "reason": "unadvertised"})), "peers-second"); !got.HasCode(CodeUnavailableCapability) {
		t.Fatalf("want %s when the refusal names the later key: %+v", CodeUnavailableCapability, got.Diagnostics)
	}
}

func TestUnsatisfiableToolChoiceNamesTheFirstOffendingEntry(t *testing.T) {
	v := MustNew()
	features := `{"run.tool_selection":{"level":"emulated"}}`
	controls := `,"tool_choice":{"allowed":["absent_a","absent_b"]}`
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

func TestControlGateLeavesUnrelatedRefusalsAlone(t *testing.T) {
	v := MustNew()
	features := `{"run.instructions":{"level":"emulated"}}`
	trace := controlsTrace(features, `,"instructions":"be terse"`, refusalEnvelope("run_active", map[string]any{"run_id": "r0"}))
	if got := v.ValidateBytes(trace, "unrelated-refusal"); !got.Valid() {
		t.Fatalf("a state refusal was judged as a control refusal: %+v", got.Diagnostics)
	}
}

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

func TestRunControlDiagnosticsAreRegistered(t *testing.T) {
	known := diagnosticCodes()
	for _, code := range []string{
		CodeUnappliedControl, CodeUnsatisfiableControl, CodeDegradedWithoutOptin,
		CodeDuplicateToolName, CodeUndisclosedSelectionScope,
	} {
		if !known[code] {
			t.Fatalf("diagnostic %q is not registered", code)
		}
	}
}

func TestRunControlCapabilitiesAreRegistered(t *testing.T) {
	keys := strings.Join(unitCapabilities["run-controls"], " ")
	for _, key := range []string{"run.model_selection", "run.instructions", "run.tool_selection", "run.structured_output"} {
		if !strings.Contains(keys, key) {
			t.Fatalf("capability %q is not registered under run-controls: %q", key, keys)
		}
	}
}

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

func TestAbsentToolChoiceIsNotAControl(t *testing.T) {
	policy, err := protocol.MessageSubmitRequest{}.ToolChoicePolicy()
	if policy != nil || err != nil {
		t.Fatalf("absent tool_choice decoded as %+v, %v", policy, err)
	}
	if _, err := (protocol.MessageSubmitRequest{ToolChoice: json.RawMessage(`null`)}).ToolChoicePolicy(); err == nil {
		t.Fatal("a present null decoded as an absent control")
	}
}

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

func TestPolicyPermitsOnlyCataloguedTools(t *testing.T) {
	catalog := []string{"scripted_tool", "other_tool"}
	for name, policy := range map[string]protocol.ToolChoice{
		"allowed":    {Allowed: []string{"scripted_tool"}},
		"disallowed": {Disallowed: []string{"other_tool"}},
	} {
		if policy.Permits("unlisted_tool", catalog, true) {
			t.Fatalf("%s: a tool the catalog does not carry was permitted", name)
		}
		if !policy.Permits("scripted_tool", catalog, true) {
			t.Fatalf("%s: a catalogued tool the policy admits was refused", name)
		}

		if policy.Permits("scripted_tool", nil, true) {
			t.Fatalf("%s: an empty catalog permitted a tool", name)
		}

		if !policy.Permits("scripted_tool", nil, false) {
			t.Fatalf("%s: an unknown catalog was read as an empty one", name)
		}
	}

	for name, policy := range map[string]protocol.ToolChoice{
		"disallowed":      {Disallowed: []string{"other_tool"}},
		"outside allowed": {Allowed: []string{"scripted_tool"}},
		"empty allowlist": {Allowed: []string{}},
	} {
		if policy.Permits("other_tool", catalog, true) {
			t.Fatalf("%s: the policy permitted a tool it excludes", name)
		}
	}
}

func modelAdmission(admitted, started string) string {
	start := `"session_id":"s1","run_id":"r1","status":"running"`
	if started != "" {
		start += `,"model_id":"` + started + `"`
	}
	return `{` + controlsCore + `,"type":"session.message.submit.response","id":"submit-resp","in_reply_to":"submit-req","session_id":"s1","run_id":"r1","capability_revision":"rev-1","payload":{"session_id":"s1","accepted":true,"submission_id":"sub1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"r1","status":"running","model_id":"` + admitted + `"}},
	{` + controlsCore + `,"type":"run.started","id":"ev-start","session_id":"s1","run_id":"r1","sequence":1,"payload":{` + start + `}},
	{` + controlsCore + `,"type":"run.completed","id":"ev-done","session_id":"s1","run_id":"r1","sequence":2,"payload":{"session_id":"s1","run_id":"r1","final_response":{"role":"assistant","content":"ok"},"stop_reason":"end_turn"}}`
}

func TestStartedRepeatsTheAdmittedModel(t *testing.T) {
	v := MustNew()
	features := `{"run.model_selection":{"level":"emulated","scope":"run"}}`
	omitted := v.ValidateBytes(controlsTrace(features, `,"model_id":"m1"`, modelAdmission("m1", "")), "started-omits-model")
	if !omitted.HasCode(CodeUnappliedControl) {
		t.Fatalf("want %s when run.started omits the admitted model: %+v", CodeUnappliedControl, omitted.Diagnostics)
	}
	repeated := v.ValidateBytes(controlsTrace(features, `,"model_id":"m1"`, modelAdmission("m1", "m1")), "started-repeats-model")
	if !repeated.Valid() {
		t.Fatalf("a run.started repeating the admitted model was rejected: %+v", repeated.Diagnostics)
	}

	attribution := v.ValidateBytes(controlsTrace(features, "", modelAdmission("m1", "")), "started-omits-attribution")
	if !attribution.Valid() {
		t.Fatalf("an uncontrolled run was judged against a volunteered model: %+v", attribution.Diagnostics)
	}
}

func TestRefusalMustNameTheFeatureItAnswersFor(t *testing.T) {
	v := MustNew()
	features := `{"run.tool_selection":{"level":"emulated","modes":["auto","none","required","named"]}}`
	control := `,"tool_choice":{"allowed":["absent_tool"]}`
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

func TestToolChoiceShapeIsJudgedByMemberPresence(t *testing.T) {
	for name, encoded := range map[string]string{
		"mode is no longer a member": `{"mode":"auto","allowed":["scripted_tool"]}`,
		"name is no longer a member": `{"name":"scripted_tool","allowed":["scripted_tool"]}`,
		"neither filter":             `{}`,
		"both filters empty":         `{"allowed":[],"disallowed":[]}`,
		"both filters present":       `{"allowed":["scripted_tool"],"disallowed":[]}`,
		"null allowed":               `{"allowed":null}`,
		"null disallowed":            `{"disallowed":null}`,
	} {
		policy, err := (protocol.MessageSubmitRequest{ToolChoice: json.RawMessage(encoded)}).ToolChoicePolicy()
		if err == nil {
			t.Fatalf("%s: %s decoded as the typed policy %+v", name, encoded, policy)
		}
	}

	for name, encoded := range map[string]string{
		"empty allowed":     `{"allowed":[]}`,
		"empty disallowed":  `{"disallowed":[]}`,
		"populated allowed": `{"allowed":["scripted_tool"]}`,
	} {
		if _, err := (protocol.MessageSubmitRequest{ToolChoice: json.RawMessage(encoded)}).ToolChoicePolicy(); err != nil {
			t.Fatalf("%s: %s was refused as untyped: %v", name, encoded, err)
		}
	}

	v := MustNew()
	features := `{"run.tool_selection":{"level":"emulated"}}`
	admitted := v.ValidateBytes(controlsTrace(features, `,"tool_choice":{"allowed":[],"disallowed":[]}`, controlsAdmission), "empty-filters")
	if !admitted.HasCode(CodeUnsatisfiableControl) {
		t.Fatalf("want %s when both filters are present: %+v", CodeUnsatisfiableControl, admitted.Diagnostics)
	}
}

func TestEmptyAllowlistPermitsNothing(t *testing.T) {
	catalog := []string{"scripted_tool", "other_tool"}
	empty := protocol.ToolChoice{Allowed: []string{}}
	if filtered := empty.Filter(catalog); len(filtered) != 0 {
		t.Fatalf("an empty allowlist filtered to %v, want nothing", filtered)
	}
	for _, name := range catalog {
		if empty.Permits(name, catalog, true) {
			t.Fatalf("an empty allowlist permitted %q", name)
		}
	}

	absent := protocol.ToolChoice{}
	if filtered := absent.Filter(catalog); len(filtered) != len(catalog) {
		t.Fatalf("an absent allowlist filtered to %v, want the catalog", filtered)
	}
	if !absent.Permits("scripted_tool", catalog, true) {
		t.Fatalf("an absent allowlist refused a catalogued tool")
	}

	if defect := empty.Unsatisfiable(catalog, true); defect != nil {
		t.Fatalf("an empty allowlist was refused: %+v", defect)
	}
}

func TestModelSelectionMustDiscloseHowLongASelectionLives(t *testing.T) {
	v := MustNew()
	for name, support := range map[string]string{
		"missing":       `{"level":"emulated"}`,
		"empty":         `{"level":"emulated","scope":""}`,
		"unknown":       `{"level":"emulated","scope":"whenever"}`,
		"not yet ruled": `{"level":"emulated","scope":"restart"}`,
		"degraded":      `{"level":"degraded","scope":"","reason":"attribution is unconfirmed"}`,
	} {
		features := `{"run.model_selection":` + support + `}`
		result := v.ValidateBytes(controlsTrace(features, `,"model_id":"m1"`, modelAdmission("m1", "m1")), "scope-"+name)
		if !result.HasCode(CodeUndisclosedSelectionScope) {
			t.Fatalf("%s: want %s: %+v", name, CodeUndisclosedSelectionScope, result.Diagnostics)
		}
	}

	for name, features := range map[string]string{
		"run":     `{"run.model_selection":{"level":"emulated","scope":"run"}}`,
		"session": `{"run.model_selection":{"level":"native","scope":"session"}}`,
	} {
		result := v.ValidateBytes(controlsTrace(features, `,"model_id":"m1"`, modelAdmission("m1", "m1")), "scope-"+name)
		if !result.Valid() {
			t.Fatalf("%s: a disclosed scope was rejected: %+v", name, result.Diagnostics)
		}
	}
	unadvertised := `{"run.model_selection":{"level":"unavailable","reason":"no per-run surface"}}`
	refusal := refusalEnvelope("unsupported_feature", map[string]any{"feature": "run.model_selection", "reason": "unadvertised"})
	if result := v.ValidateBytes(controlsTrace(unadvertised, `,"model_id":"m1"`, refusal), "mode-unavailable"); !result.Valid() {
		t.Fatalf("an unavailable key was held to a mode: %+v", result.Diagnostics)
	}
}

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

		if result.HasCode(CodeUnsatisfiableControl) {
			t.Fatalf("%s: a fixed_result that is not an object refused a satisfiable schema: %+v", name, result.Diagnostics)
		}
	}

	features := `{"run.structured_output":{"level":"emulated","constraints":{"fixed_result":{"ok":true}}}}`
	unsatisfiable := v.ValidateBytes(controlsTrace(features, `,"output_schema":{"type":"object","required":["answer"]}`, controlsAdmission), "fixed-unsatisfiable")
	if !unsatisfiable.HasCode(CodeUnsatisfiableControl) {
		t.Fatalf("a schema the declared fixed result cannot satisfy was admitted: %+v", unsatisfiable.Diagnostics)
	}
}

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

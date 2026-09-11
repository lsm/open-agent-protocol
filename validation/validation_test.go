package validation

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"

	"github.com/lsm/open-agent-protocol/protocol"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Dir(filepath.Dir(file))
}

func TestSchemasCompileOffline(t *testing.T) {
	if _, err := CompileSchemas(); err != nil {
		t.Fatal(err)
	}
}

func TestManifest(t *testing.T) {
	v, err := New()
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := v.ValidateManifest(filepath.Join(repositoryRoot(t), "fixtures", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) < 25 {
		t.Fatalf("only %d fixtures", len(outcomes))
	}
	kinds := map[string]int{}
	for _, o := range outcomes {
		kinds[o.Entry.Kind]++
	}
	for _, kind := range []string{"positive", "schema-invalid", "semantic-invalid"} {
		if kinds[kind] == 0 {
			t.Errorf("no %s fixtures", kind)
		}
	}
}

func TestInputFormats(t *testing.T) {
	v := MustNew()
	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), "fixtures", "valid", "core-completed.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := v.ValidateBytes(data, "array"); !got.Valid() {
		t.Fatal(got.Diagnostics)
	}
	var objects []json.RawMessage
	if err := json.Unmarshal(data, &objects); err != nil {
		t.Fatal(err)
	}
	if got := v.ValidateBytes(objects[0], "single"); got.HasCode(CodeMalformedJSON) || got.HasCode(CodeSchemaInvalid) {
		t.Fatal(got.Diagnostics)
	}
	lines := make([][]byte, len(objects))
	for i := range objects {
		var compact bytes.Buffer
		if err := json.Compact(&compact, objects[i]); err != nil {
			t.Fatal(err)
		}
		lines[i] = compact.Bytes()
	}
	jsonl := bytes.Join(lines, []byte("\n"))
	if got := v.ValidateBytes(jsonl, "jsonl"); !got.Valid() {
		t.Fatal(got.Diagnostics)
	}
}

func TestDiagnosticsStable(t *testing.T) {
	v := MustNew()
	data, _ := os.ReadFile(filepath.Join(repositoryRoot(t), "fixtures", "semantic-invalid", "duplicate-response.json"))
	a := v.ValidateBytes(data, "x")
	b := v.ValidateBytes(data, "x")
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("diagnostics differ:\n%#v\n%#v", a, b)
	}
	if !sort.SliceIsSorted(a.Diagnostics, func(i, j int) bool {
		if a.Diagnostics[i].Index != a.Diagnostics[j].Index {
			return a.Diagnostics[i].Index < a.Diagnostics[j].Index
		}
		return a.Diagnostics[i].Code < a.Diagnostics[j].Code
	}) {
		t.Fatal("diagnostics not sorted")
	}
}

func TestTransitionCodes(t *testing.T) {
	v := MustNew()
	root := filepath.Join(repositoryRoot(t), "fixtures", "semantic-invalid")
	cases := map[string]string{"sequence-gap.json": CodeSequenceGap, "duplicate-terminal.json": CodeDuplicateRunTerminal, "event-after-terminal.json": CodeEventAfterTerminal, "cancel-not-settled.json": CodeCancelNotSettled, "pending-tool.json": CodePendingToolAtTerminal, "pending-interaction.json": CodePendingInteractionAtTerminal, "wrong-responder.json": CodeWrongInteractionResponder, "unavailable-feature.json": CodeUnavailableCapability, "stale-capability.json": CodeStaleCapabilityRevision, "undeclared-replay-gap.json": CodeUndeclaredReplayGap}
	for file, code := range cases {
		t.Run(file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, file))
			if err != nil {
				t.Fatal(err)
			}
			if got := v.ValidateBytes(data, file); !got.HasCode(code) {
				t.Fatalf("want %s: %+v", code, got.Diagnostics)
			}
		})
	}
}

func TestResponseCorrelationCorrections(t *testing.T) {
	v := MustNew()
	const core = `"profile":"open-agent-protocol.agent-control-core","protocol":"open-agent-protocol","version":"0.1"`
	t.Run("submit requested_delivery must repeat the request", func(t *testing.T) {
		stream := `[
			{` + core + `,"type":"session.message.submit.request","id":"submit","session_id":"s1","payload":{"session_id":"s1","messages":[{"role":"user","content":"go"}],"delivery":"auto"}},
			{` + core + `,"type":"session.message.submit.response","id":"admit","in_reply_to":"submit","session_id":"s1","payload":{"session_id":"s1","accepted":true,"submission_id":"sub1","requested_delivery":"queue","effective_delivery":"start","admission":"started","run_id":"r1","status":"running"}},
			{` + core + `,"type":"run.started","id":"start","session_id":"s1","run_id":"r1","sequence":1,"payload":{"session_id":"s1","run_id":"r1","status":"running"}},
			{` + core + `,"type":"run.completed","id":"done","session_id":"s1","run_id":"r1","sequence":2,"payload":{"session_id":"s1","run_id":"r1","final_response":{"role":"assistant","content":"ok"},"stop_reason":"end_turn"}}
		]`
		if got := v.ValidateBytes([]byte(stream), "submit-delivery-mismatch"); !got.HasCode(CodeScopeMismatch) {
			t.Fatalf("want %s: %+v", CodeScopeMismatch, got.Diagnostics)
		}
	})
	t.Run("resolution response must name the requested interaction", func(t *testing.T) {
		stream := `[
			{` + core + `,"type":"capabilities.request","id":"capq","payload":{}},
			{` + core + `,"type":"capabilities.response","id":"capr","in_reply_to":"capq","capability_revision":"v1","payload":{"endpoint":{"id":"agent"},"features":{"user_input":{"level":"native"}}}},
			{` + core + `,"type":"session.message.submit.request","id":"req1","session_id":"s1","payload":{"session_id":"s1","messages":[{"role":"user","content":"go"}],"delivery":"auto"}},
			{` + core + `,"type":"session.message.submit.response","id":"resp1","in_reply_to":"req1","session_id":"s1","payload":{"session_id":"s1","accepted":true,"submission_id":"sub1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"r1","status":"running"}},
			{` + core + `,"type":"run.started","id":"ev-start","session_id":"s1","run_id":"r1","sequence":1,"payload":{"session_id":"s1","run_id":"r1","status":"running"}},
			{` + core + `,"type":"user.input.requested","id":"input1","session_id":"s1","run_id":"r1","sequence":2,"capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i1","requested_by":"agent","responded_by":"user","title":"Q","questions":[{"id":"q1","kind":"text","prompt":"Continue?"}]}},
			{` + core + `,"type":"user.input.resolve.request","id":"rr1","session_id":"s1","run_id":"r1","capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i1","requested_by":"agent","responded_by":"user","answers":[{"question_id":"q1","text":"yes"}]}},
			{` + core + `,"type":"user.input.resolve.response","id":"rr2","in_reply_to":"rr1","session_id":"s1","run_id":"r1","capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i2","accepted":true}},
			{` + core + `,"type":"user.input.resolved","id":"input2","session_id":"s1","run_id":"r1","sequence":3,"capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i1","requested_by":"agent","responded_by":"user","status":"submitted","answers":[{"question_id":"q1","text":"yes"}]}},
			{` + core + `,"type":"run.completed","id":"ev-done","session_id":"s1","run_id":"r1","sequence":4,"payload":{"session_id":"s1","run_id":"r1","final_response":{"role":"assistant","content":"ok"},"stop_reason":"end_turn"}}
		]`
		if got := v.ValidateBytes([]byte(stream), "resolution-interaction-mismatch"); !got.HasCode(CodeScopeMismatch) {
			t.Fatalf("want %s: %+v", CodeScopeMismatch, got.Diagnostics)
		}
	})
	t.Run("obsolete revision on a discovery request is not pinned to its response", func(t *testing.T) {
		stream := `[
			{` + core + `,"type":"capabilities.request","id":"capq","payload":{}},
			{` + core + `,"type":"capabilities.response","id":"capr","in_reply_to":"capq","capability_revision":"v1","payload":{"endpoint":{"id":"agent"},"features":{}}},
			{` + core + `,"type":"capabilities.request","id":"capq2","capability_revision":"v0","payload":{}},
			{` + core + `,"type":"capabilities.response","id":"capr2","in_reply_to":"capq2","capability_revision":"v1","payload":{"endpoint":{"id":"agent"},"features":{}}}
		]`
		if got := v.ValidateBytes([]byte(stream), "stale-discovery-revision"); len(got.Diagnostics) != 0 {
			t.Fatalf("discovery response was pinned to a stale request revision: %+v", got.Diagnostics)
		}
	})
	t.Run("canonical delivery feature key is recognized", func(t *testing.T) {
		stream := `[
			{` + core + `,"type":"capabilities.request","id":"capq","payload":{}},
			{` + core + `,"type":"capabilities.response","id":"capr","in_reply_to":"capq","capability_revision":"v1","payload":{"endpoint":{"id":"agent"},"features":{"session.message.delivery.queue":{"level":"native"}}}},
			{` + core + `,"type":"session.message.submit.request","id":"submit","session_id":"s1","capability_revision":"v1","payload":{"session_id":"s1","messages":[{"role":"user","content":"go"}],"delivery":"queue"}},
			{` + core + `,"type":"session.message.submit.response","id":"admit","in_reply_to":"submit","session_id":"s1","capability_revision":"v1","payload":{"session_id":"s1","accepted":true,"submission_id":"sub1","requested_delivery":"queue","effective_delivery":"queue","admission":"queued","run_id":"r1","status":"queued"}},
			{` + core + `,"type":"run.failed","id":"fail","session_id":"s1","run_id":"r1","sequence":1,"payload":{"session_id":"s1","run_id":"r1","error":{"code":"harness_conflict","message":"conflict"}}}
		]`
		if got := v.ValidateBytes([]byte(stream), "canonical-delivery-feature"); len(got.Diagnostics) != 0 {
			t.Fatalf("canonical delivery feature was not recognized: %+v", got.Diagnostics)
		}
	})
	t.Run("initialize response must select an offered version and profile", func(t *testing.T) {
		// The response payload is schema-fixed to 0.1 / the core profile, so an
		// unoffered selection is only observable when the request offers neither.
		stream := `[
			{` + core + `,"type":"protocol.initialize.request","id":"init","payload":{"protocol_versions":["9.9"],"profiles":["open-agent-protocol.other"]}},
			{` + core + `,"type":"protocol.initialize.response","id":"initr","in_reply_to":"init","payload":{"protocol_version":"0.1","profile":"open-agent-protocol.agent-control-core","endpoint":{"id":"agent"}}}
		]`
		if got := v.ValidateBytes([]byte(stream), "initialize-unoffered-selection"); !got.HasCode(CodeScopeMismatch) {
			t.Fatalf("want %s: %+v", CodeScopeMismatch, got.Diagnostics)
		}
	})
	t.Run("initialize response selecting an offered pair is accepted", func(t *testing.T) {
		stream := `[
			{` + core + `,"type":"protocol.initialize.request","id":"init","payload":{"protocol_versions":["0.1"],"profiles":["open-agent-protocol.agent-control-core"]}},
			{` + core + `,"type":"protocol.initialize.response","id":"initr","in_reply_to":"init","payload":{"protocol_version":"0.1","profile":"open-agent-protocol.agent-control-core","endpoint":{"id":"agent"}}}
		]`
		if got := v.ValidateBytes([]byte(stream), "initialize-offered-selection"); len(got.Diagnostics) != 0 {
			t.Fatalf("offered selection was rejected: %+v", got.Diagnostics)
		}
	})
	t.Run("state request envelope and payload scope must agree", func(t *testing.T) {
		stream := `[
			{` + core + `,"type":"session.state.request","id":"state","session_id":"s1","payload":{"session_id":"s2"}},
			{` + core + `,"type":"session.state.response","id":"stater","in_reply_to":"state","session_id":"s2","payload":{"session_id":"s2","status":"idle"}}
		]`
		if got := v.ValidateBytes([]byte(stream), "state-request-scope"); !got.HasCode(CodeScopeMismatch) {
			t.Fatalf("want %s: %+v", CodeScopeMismatch, got.Diagnostics)
		}
	})
	t.Run("error response must answer within the request scope", func(t *testing.T) {
		stream := `[
			{` + core + `,"type":"session.open.request","id":"open","session_id":"s1","payload":{"session_id":"s1"}},
			{` + core + `,"type":"error.response","id":"err","in_reply_to":"open","session_id":"s2","payload":{"error":{"code":"rejected","message":"no"}}}
		]`
		if got := v.ValidateBytes([]byte(stream), "error-response-scope"); !got.HasCode(CodeScopeMismatch) {
			t.Fatalf("want %s: %+v", CodeScopeMismatch, got.Diagnostics)
		}
	})
	t.Run("permission envelope and payload tool_call_id must agree", func(t *testing.T) {
		stream := `[
			{` + core + `,"type":"capabilities.request","id":"capq","payload":{}},
			{` + core + `,"type":"capabilities.response","id":"capr","in_reply_to":"capq","capability_revision":"v1","payload":{"endpoint":{"id":"agent"},"features":{}}},
			{` + core + `,"type":"session.message.submit.request","id":"req1","session_id":"s1","capability_revision":"v1","payload":{"session_id":"s1","messages":[{"role":"user","content":"go"}],"delivery":"auto"}},
			{` + core + `,"type":"session.message.submit.response","id":"resp1","in_reply_to":"req1","session_id":"s1","capability_revision":"v1","payload":{"session_id":"s1","accepted":true,"submission_id":"sub1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"r1","status":"running"}},
			{` + core + `,"type":"run.started","id":"ev-start","session_id":"s1","run_id":"r1","sequence":1,"payload":{"session_id":"s1","run_id":"r1","status":"running"}},
			{` + core + `,"type":"action.call.requested","id":"call1","session_id":"s1","run_id":"r1","sequence":2,"tool_call_id":"t1","capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","tool_call_id":"t1","requested_by":"agent","execution_owner":"test","name":"run","arguments_json":{}}},
			{` + core + `,"type":"action.permission.requested","id":"perm1","session_id":"s1","run_id":"r1","sequence":3,"tool_call_id":"t1","capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i1","requested_by":"agent","responded_by":"user","tool_call_id":"t2","title":"T","choices":[{"id":"a","label":"A"}]}},
			{` + core + `,"type":"run.failed","id":"fail","session_id":"s1","run_id":"r1","sequence":4,"capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","error":{"code":"x","message":"y"}}}
		]`
		if got := v.ValidateBytes([]byte(stream), "permission-tool-scope"); !got.HasCode(CodeScopeMismatch) {
			t.Fatalf("want %s: %+v", CodeScopeMismatch, got.Diagnostics)
		}
	})
}

// Two recovered sessions may be opened before either reconciliation completes.
// Each open declares its own replay-gap expectation, so providing authoritative
// state for only one of them must still diagnose the other.
func TestRecoveryExpectationsArePerSession(t *testing.T) {
	v := MustNew()
	const core = `"profile":"open-agent-protocol.agent-control-core","protocol":"open-agent-protocol","version":"0.1"`
	stream := `[
		{` + core + `,"type":"session.open.request","id":"o1","payload":{"session_id":"s1","recovery":{"recovered":true,"previous_run_id":"r1","resume_cursor":"5","reason":"replay_gap"}}},
		{` + core + `,"type":"session.open.response","id":"o1r","in_reply_to":"o1","session_id":"s1","payload":{"session_id":"s1","status":"idle","recovery":{"recovered":true,"previous_run_id":"r1","resume_cursor":"5","reason":"replay_gap"}}},
		{` + core + `,"type":"session.open.request","id":"o2","payload":{"session_id":"s2","recovery":{"recovered":true,"previous_run_id":"r2","resume_cursor":"9","reason":"replay_gap"}}},
		{` + core + `,"type":"session.open.response","id":"o2r","in_reply_to":"o2","session_id":"s2","payload":{"session_id":"s2","status":"idle","recovery":{"recovered":true,"previous_run_id":"r2","resume_cursor":"9","reason":"replay_gap"}}},
		{` + core + `,"type":"session.state.request","id":"st2","session_id":"s2","payload":{"session_id":"s2"}},
		{` + core + `,"type":"session.state.response","id":"st2r","in_reply_to":"st2","session_id":"s2","payload":{"session_id":"s2","status":"idle","transcript_cursor":"9","recovery":{"recovered":true,"previous_run_id":"r2","resume_cursor":"9","reason":"replay_gap"}}}
	]`
	if got := v.ValidateBytes([]byte(stream), "recovery-per-session"); !got.HasCode(CodeUndeclaredReplayGap) {
		t.Fatalf("want %s for the state-less recovered session: %+v", CodeUndeclaredReplayGap, got.Diagnostics)
	}
}

// A recovery expectation is satisfied by the first authoritative snapshot; an
// ordinary later state update must not be re-checked against the old recovery.
func TestRecoveryExpectationConsumedAfterSnapshot(t *testing.T) {
	v := MustNew()
	const core = `"profile":"open-agent-protocol.agent-control-core","protocol":"open-agent-protocol","version":"0.1"`
	stream := `[
		{` + core + `,"type":"session.open.request","id":"o1","payload":{"session_id":"s1","recovery":{"recovered":true,"previous_run_id":"r1","resume_cursor":"5","reason":"replay_gap"}}},
		{` + core + `,"type":"session.open.response","id":"o1r","in_reply_to":"o1","session_id":"s1","payload":{"session_id":"s1","status":"idle","recovery":{"recovered":true,"previous_run_id":"r1","resume_cursor":"5","reason":"replay_gap"}}},
		{` + core + `,"type":"session.state.request","id":"st1","session_id":"s1","payload":{"session_id":"s1"}},
		{` + core + `,"type":"session.state.response","id":"st1r","in_reply_to":"st1","session_id":"s1","payload":{"session_id":"s1","status":"idle","transcript_cursor":"5","recovery":{"recovered":true,"previous_run_id":"r1","resume_cursor":"5","reason":"replay_gap"}}},
		{` + core + `,"type":"session.state.request","id":"st2","session_id":"s1","payload":{"session_id":"s1"}},
		{` + core + `,"type":"session.state.response","id":"st2r","in_reply_to":"st2","session_id":"s1","payload":{"session_id":"s1","status":"idle"}}
	]`
	if got := v.ValidateBytes([]byte(stream), "recovery-consumed"); got.HasCode(CodeUndeclaredReplayGap) {
		t.Fatalf("a later ordinary state update was re-checked against the recovery: %+v", got.Diagnostics)
	}
}

// A prompt that declared allow_cancel:false may not be withdrawn.
func TestCancelRejectedWhenNotAllowed(t *testing.T) {
	v := MustNew()
	const core = `"profile":"open-agent-protocol.agent-control-core","protocol":"open-agent-protocol","version":"0.1"`
	stream := `[
		{` + core + `,"type":"capabilities.request","id":"capq","payload":{}},
		{` + core + `,"type":"capabilities.response","id":"capr","in_reply_to":"capq","capability_revision":"v1","payload":{"endpoint":{"id":"agent"},"features":{"user_input":{"level":"native"}}}},
		{` + core + `,"type":"session.message.submit.request","id":"req1","session_id":"s1","capability_revision":"v1","payload":{"session_id":"s1","messages":[{"role":"user","content":"go"}],"delivery":"auto"}},
		{` + core + `,"type":"session.message.submit.response","id":"resp1","in_reply_to":"req1","session_id":"s1","capability_revision":"v1","payload":{"session_id":"s1","accepted":true,"submission_id":"sub1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"r1","status":"running"}},
		{` + core + `,"type":"run.started","id":"ev-start","session_id":"s1","run_id":"r1","sequence":1,"payload":{"session_id":"s1","run_id":"r1","status":"running"}},
		{` + core + `,"type":"user.input.requested","id":"input1","session_id":"s1","run_id":"r1","sequence":2,"capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i1","requested_by":"agent","responded_by":"user","title":"Q","allow_cancel":false,"questions":[{"id":"q1","kind":"text","prompt":"Continue?"}]}},
		{` + core + `,"type":"user.input.cancel.request","id":"cancel1","session_id":"s1","run_id":"r1","capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i1","requested_by":"agent","responded_by":"user","reason":"withdrawn"}}
	]`
	if got := v.ValidateBytes([]byte(stream), "cancel-not-allowed"); !got.HasCode(CodeUnmatchedInteraction) {
		t.Fatalf("want %s for a non-cancellable prompt: %+v", CodeUnmatchedInteraction, got.Diagnostics)
	}
}

// A permission resolution must select a choice the gate offered.
func TestPermissionResolutionChoiceMustBeOffered(t *testing.T) {
	v := MustNew()
	const core = `"profile":"open-agent-protocol.agent-control-core","protocol":"open-agent-protocol","version":"0.1"`
	stream := `[
		{` + core + `,"type":"capabilities.request","id":"capq","payload":{}},
		{` + core + `,"type":"capabilities.response","id":"capr","in_reply_to":"capq","capability_revision":"v1","payload":{"endpoint":{"id":"agent"},"features":{"permissions":{"level":"native"}}}},
		{` + core + `,"type":"session.message.submit.request","id":"req1","session_id":"s1","capability_revision":"v1","payload":{"session_id":"s1","messages":[{"role":"user","content":"go"}],"delivery":"auto"}},
		{` + core + `,"type":"session.message.submit.response","id":"resp1","in_reply_to":"req1","session_id":"s1","capability_revision":"v1","payload":{"session_id":"s1","accepted":true,"submission_id":"sub1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"r1","status":"running"}},
		{` + core + `,"type":"run.started","id":"ev-start","session_id":"s1","run_id":"r1","sequence":1,"payload":{"session_id":"s1","run_id":"r1","status":"running"}},
		{` + core + `,"type":"action.permission.requested","id":"perm1","session_id":"s1","run_id":"r1","sequence":2,"tool_call_id":"t1","capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i1","requested_by":"agent","responded_by":"user","tool_call_id":"t1","title":"T","choices":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}},
		{` + core + `,"type":"action.permission.resolve.request","id":"pr1","session_id":"s1","run_id":"r1","capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i1","requested_by":"agent","responded_by":"user","choice_id":"bogus","granted":true}}
	]`
	if got := v.ValidateBytes([]byte(stream), "permission-choice-unoffered"); !got.HasCode(CodeUnmatchedInteraction) {
		t.Fatalf("want %s for an unoffered choice: %+v", CodeUnmatchedInteraction, got.Diagnostics)
	}
}

// The authoritative resolved event, not just the resolve request, must select
// an offered choice; otherwise a valid request can be paired with a bogus
// resolution and still certify.
func TestPermissionResolutionEventChoiceMustBeOffered(t *testing.T) {
	v := MustNew()
	const core = `"profile":"open-agent-protocol.agent-control-core","protocol":"open-agent-protocol","version":"0.1"`
	stream := `[
		{` + core + `,"type":"capabilities.request","id":"capq","payload":{}},
		{` + core + `,"type":"capabilities.response","id":"capr","in_reply_to":"capq","capability_revision":"v1","payload":{"endpoint":{"id":"agent"},"features":{"permissions":{"level":"native"}}}},
		{` + core + `,"type":"session.message.submit.request","id":"req1","session_id":"s1","capability_revision":"v1","payload":{"session_id":"s1","messages":[{"role":"user","content":"go"}],"delivery":"auto"}},
		{` + core + `,"type":"session.message.submit.response","id":"resp1","in_reply_to":"req1","session_id":"s1","capability_revision":"v1","payload":{"session_id":"s1","accepted":true,"submission_id":"sub1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"r1","status":"running"}},
		{` + core + `,"type":"run.started","id":"ev-start","session_id":"s1","run_id":"r1","sequence":1,"payload":{"session_id":"s1","run_id":"r1","status":"running"}},
		{` + core + `,"type":"action.permission.requested","id":"perm1","session_id":"s1","run_id":"r1","sequence":2,"tool_call_id":"t1","capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i1","requested_by":"agent","responded_by":"user","tool_call_id":"t1","title":"T","choices":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}},
		{` + core + `,"type":"action.permission.resolve.request","id":"pr1","session_id":"s1","run_id":"r1","capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i1","requested_by":"agent","responded_by":"user","choice_id":"a","granted":true}},
		{` + core + `,"type":"action.permission.resolved","id":"pe1","session_id":"s1","run_id":"r1","sequence":3,"tool_call_id":"t1","capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i1","requested_by":"agent","responded_by":"user","tool_call_id":"t1","outcome":"resolved","choice_id":"bogus","granted":true}}
	]`
	if got := v.ValidateBytes([]byte(stream), "permission-event-choice-unoffered"); !got.HasCode(CodeUnmatchedInteraction) {
		t.Fatalf("want %s for an unoffered resolved-event choice: %+v", CodeUnmatchedInteraction, got.Diagnostics)
	}
}

// A permission request bound to one tool call must not be resolved by an event
// that consistently names a different tool; both resolution-event tool-call
// fields are compared against the request's binding.
func TestPermissionResolutionMustKeepToolBinding(t *testing.T) {
	v := MustNew()
	const core = `"profile":"open-agent-protocol.agent-control-core","protocol":"open-agent-protocol","version":"0.1"`
	stream := `[
		{` + core + `,"type":"capabilities.request","id":"capq","payload":{}},
		{` + core + `,"type":"capabilities.response","id":"capr","in_reply_to":"capq","capability_revision":"v1","payload":{"endpoint":{"id":"agent"},"features":{"permissions":{"level":"native"}}}},
		{` + core + `,"type":"session.message.submit.request","id":"req1","session_id":"s1","capability_revision":"v1","payload":{"session_id":"s1","messages":[{"role":"user","content":"go"}],"delivery":"auto"}},
		{` + core + `,"type":"session.message.submit.response","id":"resp1","in_reply_to":"req1","session_id":"s1","capability_revision":"v1","payload":{"session_id":"s1","accepted":true,"submission_id":"sub1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"r1","status":"running"}},
		{` + core + `,"type":"run.started","id":"ev-start","session_id":"s1","run_id":"r1","sequence":1,"payload":{"session_id":"s1","run_id":"r1","status":"running"}},
		{` + core + `,"type":"action.permission.requested","id":"perm1","session_id":"s1","run_id":"r1","sequence":2,"tool_call_id":"t1","capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i1","requested_by":"agent","responded_by":"user","tool_call_id":"t1","title":"T","choices":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}},
		{` + core + `,"type":"action.permission.resolve.request","id":"pr1","session_id":"s1","run_id":"r1","capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i1","requested_by":"agent","responded_by":"user","choice_id":"a","granted":true}},
		{` + core + `,"type":"action.permission.resolved","id":"pe1","session_id":"s1","run_id":"r1","sequence":3,"tool_call_id":"t2","capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i1","requested_by":"agent","responded_by":"user","tool_call_id":"t2","outcome":"resolved","choice_id":"a","granted":true}}
	]`
	if got := v.ValidateBytes([]byte(stream), "permission-tool-rebind"); !got.HasCode(CodeScopeMismatch) {
		t.Fatalf("want %s for a reassigned tool binding: %+v", CodeScopeMismatch, got.Diagnostics)
	}
}

// A state snapshot may not erase a run the trace admitted and has not
// terminated, which would let a second overlapping admission through.
func TestStateSnapshotCannotEraseLiveRun(t *testing.T) {
	v := MustNew()
	const core = `"profile":"open-agent-protocol.agent-control-core","protocol":"open-agent-protocol","version":"0.1"`
	stream := `[
		{` + core + `,"type":"session.message.submit.request","id":"req1","session_id":"s1","payload":{"session_id":"s1","messages":[{"role":"user","content":"go"}],"delivery":"auto"}},
		{` + core + `,"type":"session.message.submit.response","id":"resp1","in_reply_to":"req1","session_id":"s1","payload":{"session_id":"s1","accepted":true,"submission_id":"sub1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"r1","status":"running"}},
		{` + core + `,"type":"session.state.request","id":"st1","session_id":"s1","payload":{"session_id":"s1"}},
		{` + core + `,"type":"session.state.response","id":"st1r","in_reply_to":"st1","session_id":"s1","payload":{"session_id":"s1","status":"idle"}}
	]`
	if got := v.ValidateBytes([]byte(stream), "snapshot-erases-run"); !got.HasCode(CodeSessionStateMismatch) {
		t.Fatalf("want %s for an idle snapshot over a live run: %+v", CodeSessionStateMismatch, got.Diagnostics)
	}
}

// run.started must not attribute the run to a model other than the admitted one.
func TestRunStartedModelMustMatchAdmission(t *testing.T) {
	v := MustNew()
	const core = `"profile":"open-agent-protocol.agent-control-core","protocol":"open-agent-protocol","version":"0.1"`
	stream := `[
		{` + core + `,"type":"session.message.submit.request","id":"req1","session_id":"s1","payload":{"session_id":"s1","messages":[{"role":"user","content":"go"}],"delivery":"auto"}},
		{` + core + `,"type":"session.message.submit.response","id":"resp1","in_reply_to":"req1","session_id":"s1","payload":{"session_id":"s1","accepted":true,"submission_id":"sub1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"r1","status":"running","model_id":"m1"}},
		{` + core + `,"type":"run.started","id":"ev-start","session_id":"s1","run_id":"r1","sequence":1,"payload":{"session_id":"s1","run_id":"r1","status":"running","model_id":"m2"}}
	]`
	if got := v.ValidateBytes([]byte(stream), "run-model-mismatch"); !got.HasCode(CodeIllegalRunTransition) {
		t.Fatalf("want %s for a conflicting started model: %+v", CodeIllegalRunTransition, got.Diagnostics)
	}
}

// A resolution must answer the prompt's offered questions.
func TestInputResolutionMustAnswerOfferedQuestions(t *testing.T) {
	v := MustNew()
	const core = `"profile":"open-agent-protocol.agent-control-core","protocol":"open-agent-protocol","version":"0.1"`
	stream := `[
		{` + core + `,"type":"capabilities.request","id":"capq","payload":{}},
		{` + core + `,"type":"capabilities.response","id":"capr","in_reply_to":"capq","capability_revision":"v1","payload":{"endpoint":{"id":"agent"},"features":{"user_input":{"level":"native"}}}},
		{` + core + `,"type":"session.message.submit.request","id":"req1","session_id":"s1","capability_revision":"v1","payload":{"session_id":"s1","messages":[{"role":"user","content":"go"}],"delivery":"auto"}},
		{` + core + `,"type":"session.message.submit.response","id":"resp1","in_reply_to":"req1","session_id":"s1","capability_revision":"v1","payload":{"session_id":"s1","accepted":true,"submission_id":"sub1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"r1","status":"running"}},
		{` + core + `,"type":"run.started","id":"ev-start","session_id":"s1","run_id":"r1","sequence":1,"payload":{"session_id":"s1","run_id":"r1","status":"running"}},
		{` + core + `,"type":"user.input.requested","id":"input1","session_id":"s1","run_id":"r1","sequence":2,"capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i1","requested_by":"agent","responded_by":"user","title":"Q","questions":[{"id":"q1","kind":"single_choice","prompt":"Pick","required":true,"options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}]}},
		{` + core + `,"type":"user.input.resolve.request","id":"ir1","session_id":"s1","run_id":"r1","capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i1","requested_by":"agent","responded_by":"user","answers":[{"question_id":"nope","selected_option_ids":["z"]}]}}
	]`
	if got := v.ValidateBytes([]byte(stream), "input-answers-unoffered"); !got.HasCode(CodeUnmatchedInteraction) {
		t.Fatalf("want %s for answers the prompt never offered: %+v", CodeUnmatchedInteraction, got.Diagnostics)
	}
}

// A tool-bound user-input prompt must not contradict its own tool binding.
func TestUserInputRequestToolBindingMustAgree(t *testing.T) {
	v := MustNew()
	const core = `"profile":"open-agent-protocol.agent-control-core","protocol":"open-agent-protocol","version":"0.1"`
	stream := `[
		{` + core + `,"type":"capabilities.request","id":"capq","payload":{}},
		{` + core + `,"type":"capabilities.response","id":"capr","in_reply_to":"capq","capability_revision":"v1","payload":{"endpoint":{"id":"agent"},"features":{"user_input":{"level":"native"}}}},
		{` + core + `,"type":"session.message.submit.request","id":"req1","session_id":"s1","capability_revision":"v1","payload":{"session_id":"s1","messages":[{"role":"user","content":"go"}],"delivery":"auto"}},
		{` + core + `,"type":"session.message.submit.response","id":"resp1","in_reply_to":"req1","session_id":"s1","capability_revision":"v1","payload":{"session_id":"s1","accepted":true,"submission_id":"sub1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"r1","status":"running"}},
		{` + core + `,"type":"run.started","id":"ev-start","session_id":"s1","run_id":"r1","sequence":1,"payload":{"session_id":"s1","run_id":"r1","status":"running"}},
		{` + core + `,"type":"user.input.requested","id":"input1","session_id":"s1","run_id":"r1","sequence":2,"tool_call_id":"t1","capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i1","requested_by":"agent","responded_by":"user","tool_call_id":"t2","title":"Q","questions":[{"id":"q1","kind":"text","prompt":"Continue?"}]}}
	]`
	if got := v.ValidateBytes([]byte(stream), "input-tool-blind"); !got.HasCode(CodeScopeMismatch) {
		t.Fatalf("want %s for a contradictory input tool binding: %+v", CodeScopeMismatch, got.Diagnostics)
	}
}

// The authoritative resolution event must answer the offered questions too.
func TestInputResolutionEventAnswersMustBeOffered(t *testing.T) {
	v := MustNew()
	const core = `"profile":"open-agent-protocol.agent-control-core","protocol":"open-agent-protocol","version":"0.1"`
	stream := `[
		{` + core + `,"type":"capabilities.request","id":"capq","payload":{}},
		{` + core + `,"type":"capabilities.response","id":"capr","in_reply_to":"capq","capability_revision":"v1","payload":{"endpoint":{"id":"agent"},"features":{"user_input":{"level":"native"}}}},
		{` + core + `,"type":"session.message.submit.request","id":"req1","session_id":"s1","capability_revision":"v1","payload":{"session_id":"s1","messages":[{"role":"user","content":"go"}],"delivery":"auto"}},
		{` + core + `,"type":"session.message.submit.response","id":"resp1","in_reply_to":"req1","session_id":"s1","capability_revision":"v1","payload":{"session_id":"s1","accepted":true,"submission_id":"sub1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"r1","status":"running"}},
		{` + core + `,"type":"run.started","id":"ev-start","session_id":"s1","run_id":"r1","sequence":1,"payload":{"session_id":"s1","run_id":"r1","status":"running"}},
		{` + core + `,"type":"user.input.requested","id":"input1","session_id":"s1","run_id":"r1","sequence":2,"capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i1","requested_by":"agent","responded_by":"user","title":"Q","questions":[{"id":"q1","kind":"single_choice","prompt":"Pick","required":true,"options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}]}},
		{` + core + `,"type":"user.input.resolved","id":"input2","session_id":"s1","run_id":"r1","sequence":3,"capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i1","requested_by":"agent","responded_by":"user","status":"submitted","answers":[{"question_id":"nope","selected_option_ids":["z"]}]}}
	]`
	if got := v.ValidateBytes([]byte(stream), "input-resolved-answers"); !got.HasCode(CodeUnmatchedInteraction) {
		t.Fatalf("want %s for resolution answers the prompt never offered: %+v", CodeUnmatchedInteraction, got.Diagnostics)
	}
}

// A resolution event must match the pending interaction's kind: a permission
// event cannot resolve a pending input (or the converse).
func TestCrossKindResolutionEventRejected(t *testing.T) {
	v := MustNew()
	const core = `"profile":"open-agent-protocol.agent-control-core","protocol":"open-agent-protocol","version":"0.1"`
	stream := `[
		{` + core + `,"type":"capabilities.request","id":"capq","payload":{}},
		{` + core + `,"type":"capabilities.response","id":"capr","in_reply_to":"capq","capability_revision":"v1","payload":{"endpoint":{"id":"agent"},"features":{"permissions":{"level":"native"},"user_input":{"level":"native"}}}},
		{` + core + `,"type":"session.message.submit.request","id":"req1","session_id":"s1","capability_revision":"v1","payload":{"session_id":"s1","messages":[{"role":"user","content":"go"}],"delivery":"auto"}},
		{` + core + `,"type":"session.message.submit.response","id":"resp1","in_reply_to":"req1","session_id":"s1","capability_revision":"v1","payload":{"session_id":"s1","accepted":true,"submission_id":"sub1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"r1","status":"running"}},
		{` + core + `,"type":"run.started","id":"ev-start","session_id":"s1","run_id":"r1","sequence":1,"payload":{"session_id":"s1","run_id":"r1","status":"running"}},
		{` + core + `,"type":"user.input.requested","id":"input1","session_id":"s1","run_id":"r1","sequence":2,"capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i1","requested_by":"agent","responded_by":"user","title":"Q","questions":[{"id":"q1","kind":"text","prompt":"Continue?"}]}},
		{` + core + `,"type":"action.permission.resolved","id":"perm1","session_id":"s1","run_id":"r1","sequence":3,"capability_revision":"v1","payload":{"session_id":"s1","run_id":"r1","interaction_id":"i1","requested_by":"agent","responded_by":"user","outcome":"rejected"}}
	]`
	if got := v.ValidateBytes([]byte(stream), "cross-kind-resolution"); !got.HasCode(CodeUnmatchedInteraction) {
		t.Fatalf("want %s for a permission event resolving a pending input: %+v", CodeUnmatchedInteraction, got.Diagnostics)
	}
}

func TestRunStatusTransitions(t *testing.T) {
	valid := map[protocol.RunStatus][]protocol.RunStatus{
		protocol.RunQueued:          {protocol.RunRunning, protocol.RunCancelling},
		protocol.RunRunning:         {protocol.RunRunning, protocol.RunWaitingForInput, protocol.RunCancelling},
		protocol.RunWaitingForInput: {protocol.RunWaitingForInput, protocol.RunRunning, protocol.RunCancelling},
		protocol.RunCancelling:      {protocol.RunCancelling},
	}
	all := []protocol.RunStatus{protocol.RunQueued, protocol.RunRunning, protocol.RunWaitingForInput, protocol.RunCancelling, protocol.RunCompleted, protocol.RunFailed, protocol.RunCancelled}
	for _, from := range all {
		allowed := map[protocol.RunStatus]bool{}
		for _, to := range valid[from] {
			allowed[to] = true
		}
		for _, to := range all {
			if got := legalRunStatusTransition(from, to); got != allowed[to] {
				t.Errorf("%s -> %s: got %v want %v", from, to, got, allowed[to])
			}
		}
	}
}

func TestCoverageMatrix(t *testing.T) {
	m, err := LoadManifest(filepath.Join(repositoryRoot(t), "fixtures", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	required := map[string]bool{"core": false, "tools": false, "permissions": false, "user-input": false, "recovery": false, "capabilities": false}
	for _, entry := range m.Fixtures {
		for _, unit := range entry.Units {
			if _, ok := required[unit]; ok {
				required[unit] = true
			}
		}
	}
	for unit, covered := range required {
		if !covered {
			t.Errorf("coverage matrix has no fixture for %s", unit)
		}
	}
}

func TestStructuralCorrections(t *testing.T) {
	v := MustNew()
	tests := map[string]string{
		"unknown core event":                 `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.future","id":"e","payload":{}}`,
		"empty envelope id":                  `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.request","id":"","payload":{}}`,
		"response correlation":               `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"e","capability_revision":"v1","payload":{"endpoint":{"id":"a"}}}`,
		"sequence starts at one":             `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"e","session_id":"s","run_id":"r","sequence":0,"payload":{"session_id":"s","run_id":"r","status":"running"}}`,
		"empty structured content":           `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.request","id":"e","session_id":"s","payload":{"session_id":"s","messages":[{"role":"user","content":[]}],"delivery":"auto"}}`,
		"content discriminator":              `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"content.delta","id":"e","session_id":"s","run_id":"r","sequence":1,"payload":{"session_id":"s","run_id":"r","part":{"type":"text","text":"x","reasoning":"y"}}}`,
		"exclusive image source":             `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"content.delta","id":"e","session_id":"s","run_id":"r","sequence":1,"payload":{"session_id":"s","run_id":"r","part":{"type":"image","image":{"url":"https://example.test/x","data":"AA==","media_type":"image/png"}}}}`,
		"error details object":               `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"e","in_reply_to":"q","payload":{"error":{"code":"bad","message":"bad","details":[]}}}`,
		"nonnegative duration":               `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.cancelled","id":"e","session_id":"s","run_id":"r","sequence":1,"payload":{"session_id":"s","run_id":"r","duration_ms":-1}}`,
		"run scope":                          `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.cancel.request","id":"e","session_id":"s","payload":{"session_id":"s"}}`,
		"permission resolve full scope":      `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.permission.resolve.request","id":"e","session_id":"s","run_id":"r","payload":{"interaction_id":"i","requested_by":"a","responded_by":"u","granted":true}}`,
		"legacy permission id":               `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.permission.resolve.request","id":"e","session_id":"s","run_id":"r","payload":{"permission_id":"i","requested_by":"a","responded_by":"u","session_id":"s","run_id":"r","granted":true}}`,
		"completed tool result":              `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.completed","id":"e","session_id":"s","run_id":"r","tool_call_id":"t","sequence":1,"payload":{"session_id":"s","run_id":"r","tool_call_id":"t","execution_owner":"a"}}`,
		"failed tool typed error":            `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.failed","id":"e","session_id":"s","run_id":"r","tool_call_id":"t","sequence":1,"payload":{"session_id":"s","run_id":"r","tool_call_id":"t","execution_owner":"a"}}`,
		"requested effective delivery split": `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"e","in_reply_to":"q","capability_revision":"v1","payload":{"endpoint":{"id":"a"},"layers":{"agent_control":{"delivery_modes":["auto"]}}}}`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if got := v.ValidateBytes([]byte(input), name); !got.HasCode(CodeSchemaInvalid) {
				t.Fatalf("expected structural rejection: %+v", got.Diagnostics)
			}
		})
	}
}

func FuzzValidateNeverPanics(f *testing.F) {
	f.Add([]byte(`{"protocol":"open-agent-protocol"}`))
	f.Add([]byte(`[]`))
	f.Add([]byte("{\n"))
	v := MustNew()
	f.Fuzz(func(t *testing.T, data []byte) { _ = v.ValidateBytes(data, "fuzz") })
}

func FuzzApplyEnvelopeNeverPanics(f *testing.F) {
	f.Add([]byte(`{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"x","session_id":"s","run_id":"r","sequence":1,"payload":{"session_id":"s","run_id":"r","status":"running"}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var e protocol.Envelope
		if json.Unmarshal(data, &e) != nil {
			return
		}
		s := newState("fuzz")
		s.apply(0, 1, e)
		s.close(1)
	})
}

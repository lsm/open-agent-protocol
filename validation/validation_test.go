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

package validation

import (
	"encoding/json"
	"testing"
)

func TestSessionTaskSchema(t *testing.T) {
	schema, err := CompileSchemas()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, typ, payload, extra string
		valid                     bool
	}{
		{"started", "session.task.started", `{"task":{"task_id":"t","kind":"command","status":"running","cancellable":true,"run_id":"r","tool_call_id":"c"}}`, `,"sequence":1`, true},
		{"ended", "session.task.ended", `{"task_id":"t","status":"cancelled","cause":"cancelled_by_model"}`, `,"sequence":2`, true},
		{"missing sequence", "session.task.started", `{"task":{"task_id":"t","kind":"command","status":"running","cancellable":true}}`, ``, false},
		{"invalid end", "session.task.ended", `{"task_id":"t","status":"running","cause":"finished"}`, `,"sequence":2`, false},
		{"output", "session.task.output.response", `{"output":"é","next":2,"truncated":false,"ended":true}`, `,"in_reply_to":"q"`, true},
		{"negative offset", "session.task.output.request", `{"session_id":"s","task_id":"t","from":-1}`, ``, false},
		{"state", "session.state.response", `{"session_id":"s","status":"idle","tasks":[{"task_id":"t","kind":"command","status":"completed","cancellable":true,"cause":"finished"}],"tasks_truncated":true}`, `,"in_reply_to":"q"`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"` + tc.typ + `","id":"e","session_id":"s","payload":` + tc.payload + tc.extra + `}`
			var doc any
			if err := json.Unmarshal([]byte(raw), &doc); err != nil {
				t.Fatal(err)
			}
			err := schema.Validate(doc)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}

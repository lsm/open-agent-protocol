package validation

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func compileMode(t *testing.T, mode Mode) *jsonschema.Schema {
	t.Helper()
	s, err := CompileSchemasWith(CompileOptions{Mode: mode})
	if err != nil {
		t.Fatalf("compile %s: %v", mode, err)
	}
	return s
}

func loadTrace(t *testing.T, name string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), "fixtures", "valid", name))
	if err != nil {
		t.Fatal(err)
	}
	var trace []map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&trace); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return trace
}

func firstOfType(t *testing.T, trace []map[string]any, typ string) map[string]any {
	t.Helper()
	for _, e := range trace {
		if e["type"] == typ {
			return e
		}
	}
	t.Fatalf("no %s envelope in trace", typ)
	return nil
}

func roundTrip(t *testing.T, e map[string]any) any {
	t.Helper()
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

type modeExpectation struct {
	name     string
	envelope func(t *testing.T) map[string]any
	strict   bool
	tolerant bool
}

func TestTolerantCompileSamples(t *testing.T) {
	strict := compileMode(t, ModeStrict)
	tolerant := compileMode(t, ModeTolerant)
	samples := []modeExpectation{
		{

			name: "new leaf enum value on submit response",
			envelope: func(t *testing.T) map[string]any {
				e := firstOfType(t, loadTrace(t, "core-completed.json"), "session.message.submit.response")
				e["payload"].(map[string]any)["admission"] = "parked"
				return e
			},
			strict: false, tolerant: true,
		},
		{

			name: "unknown payload member",
			envelope: func(t *testing.T) map[string]any {
				e := firstOfType(t, loadTrace(t, "core-completed.json"), "run.started")
				e["payload"].(map[string]any)["com.example.trace_id"] = "abc"
				return e
			},
			strict: false, tolerant: true,
		},
		{

			name: "audio content part",
			envelope: func(t *testing.T) map[string]any {
				e := firstOfType(t, loadTrace(t, "core-completed.json"), "session.message.submit.request")
				messages := e["payload"].(map[string]any)["messages"].([]any)
				messages[0].(map[string]any)["content"] = []any{map[string]any{"type": "audio", "audio": map[string]any{"url": "https://example.test/a.wav"}}}
				return e
			},
			strict: false, tolerant: true,
		},
		{

			name: "text content part with an additive member",
			envelope: func(t *testing.T) map[string]any {
				e := firstOfType(t, loadTrace(t, "core-completed.json"), "session.message.submit.request")
				messages := e["payload"].(map[string]any)["messages"].([]any)
				messages[0].(map[string]any)["content"] = []any{map[string]any{"type": "text", "text": "hi", "annotations": []any{map[string]any{"kind": "cite"}}}}
				return e
			},
			strict: false, tolerant: true,
		},
		{

			name: "text content part missing text",
			envelope: func(t *testing.T) map[string]any {
				e := firstOfType(t, loadTrace(t, "core-completed.json"), "session.message.submit.request")
				messages := e["payload"].(map[string]any)["messages"].([]any)
				messages[0].(map[string]any)["content"] = []any{map[string]any{"type": "text"}}
				return e
			},
			strict: false, tolerant: false,
		},
		{

			name: "run.started with status failed",
			envelope: func(t *testing.T) map[string]any {
				e := firstOfType(t, loadTrace(t, "core-completed.json"), "run.started")
				e["payload"].(map[string]any)["status"] = "failed"
				return e
			},
			strict: false, tolerant: false,
		},
		{

			name: "user.input.requested with unknown kind",
			envelope: func(t *testing.T) map[string]any {
				e := firstOfType(t, loadTrace(t, "user-input-completed.json"), "user.input.requested")
				questions := e["payload"].(map[string]any)["questions"].([]any)
				q := questions[0].(map[string]any)
				q["kind"] = "date"
				delete(q, "options")
				return e
			},
			strict: false, tolerant: true,
		},
		{

			name: "single_choice question without options",
			envelope: func(t *testing.T) map[string]any {
				e := firstOfType(t, loadTrace(t, "user-input-completed.json"), "user.input.requested")
				questions := e["payload"].(map[string]any)["questions"].([]any)
				q := questions[0].(map[string]any)
				q["kind"] = "single_choice"
				delete(q, "options")
				return e
			},
			strict: false, tolerant: false,
		},
		{

			name: "unknown envelope type",
			envelope: func(t *testing.T) map[string]any {
				e := firstOfType(t, loadTrace(t, "core-completed.json"), "run.started")
				e["type"] = "com.example.trace.mark"
				e["id"] = "ext-1"
				e["payload"] = map[string]any{"note": "hello"}
				return e
			},
			strict: false, tolerant: true,
		},
		{

			name: "known envelope type with wrong payload",
			envelope: func(t *testing.T) map[string]any {
				e := firstOfType(t, loadTrace(t, "core-completed.json"), "run.started")
				e["payload"] = map[string]any{"note": "not a run.started payload"}
				return e
			},
			strict: false, tolerant: false,
		},
		{

			name: "wrong protocol version",
			envelope: func(t *testing.T) map[string]any {
				e := firstOfType(t, loadTrace(t, "core-completed.json"), "run.started")
				e["version"] = "0.2"
				return e
			},
			strict: false, tolerant: false,
		},
		{

			name: "unmodified run.started",
			envelope: func(t *testing.T) map[string]any {
				return firstOfType(t, loadTrace(t, "core-completed.json"), "run.started")
			},
			strict: true, tolerant: true,
		},
	}
	for _, sample := range samples {
		t.Run(sample.name, func(t *testing.T) {
			value := roundTrip(t, sample.envelope(t))
			if got := strict.Validate(value) == nil; got != sample.strict {
				t.Errorf("strict: accepted=%v want %v", got, sample.strict)
			}
			if got := tolerant.Validate(value) == nil; got != sample.tolerant {
				t.Errorf("tolerant: accepted=%v want %v", got, sample.tolerant)
			}
		})
	}
}

func TestTolerantStateClassifiesUnknownRunEvent(t *testing.T) {
	trace := loadTrace(t, "core-completed.json")

	var out []map[string]any
	inserted := false
	for _, e := range trace {
		out = append(out, e)
		if e["type"] == "run.started" && !inserted {
			seq, _ := e["sequence"].(json.Number).Int64()
			out = append(out, map[string]any{
				"protocol": e["protocol"], "version": e["version"], "profile": e["profile"],
				"type": "com.example.trace.mark", "id": "ext-mark",
				"session_id": e["session_id"], "run_id": e["run_id"],
				"sequence": json.Number(itoa(seq + 1)),
				"payload":  map[string]any{"note": "additive"},
			})
			inserted = true
			continue
		}
		if inserted && e["run_id"] != nil && e["sequence"] != nil {
			seq, _ := e["sequence"].(json.Number).Int64()
			e["sequence"] = json.Number(itoa(seq + 1))
		}
	}
	if !inserted {
		t.Fatal("trace has no run.started")
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	strict, err := NewWith(Options{Mode: ModeStrict})
	if err != nil {
		t.Fatal(err)
	}
	if result := strict.ValidateBytes(data, "tolerant-state"); result.Valid() {
		t.Fatal("strict accepted an unknown envelope type")
	}
	tolerant, err := NewWith(Options{Mode: ModeTolerant})
	if err != nil {
		t.Fatal(err)
	}
	result := tolerant.ValidateBytes(data, "tolerant-state")
	if !result.Valid() {
		t.Fatalf("tolerant rejected a trace with an unknown run-scoped event: %v", result.Diagnostics)
	}
}

func validateBoth(t *testing.T, trace []map[string]any, name string) (strict, tolerant Result) {
	t.Helper()
	data, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewWith(Options{Mode: ModeStrict})
	if err != nil {
		t.Fatal(err)
	}
	tl, err := NewWith(Options{Mode: ModeTolerant})
	if err != nil {
		t.Fatal(err)
	}
	return s.ValidateBytes(data, name), tl.ValidateBytes(data, name)
}

func hasCode(r Result, code string) bool {
	for _, d := range r.Diagnostics {
		if d.Code == code {
			return true
		}
	}
	return false
}

func TestTolerantStateTreatsUnknownEnumValuesAsOpaque(t *testing.T) {
	t.Run("run.status.updated with an unknown status", func(t *testing.T) {
		trace := afterRunStarted(t, loadTrace(t, "core-completed.json"), statusUpdates("paused"))
		strict, tolerant := validateBoth(t, trace, "paused")
		if strict.Valid() {
			t.Fatal("strict accepted an unknown run status")
		}
		if !tolerant.Valid() {
			t.Fatalf("tolerant rejected an unknown run status: %v", tolerant.Diagnostics)
		}
	})
	t.Run("the first step out of an unknown status is not judged by the table", func(t *testing.T) {

		trace := afterRunStarted(t, loadTrace(t, "core-completed.json"), statusUpdates("paused", "queued"))
		if _, tolerant := validateBoth(t, trace, "paused-queued"); !tolerant.Valid() {
			t.Fatalf("tolerant judged the exit from an unknown status: %v", tolerant.Diagnostics)
		}
		trace = afterRunStarted(t, loadTrace(t, "core-completed.json"), statusUpdates("paused", "queued", "waiting_for_input"))
		if _, tolerant := validateBoth(t, trace, "paused-queued-waiting"); !hasCode(tolerant, CodeIllegalRunTransition) {
			t.Fatalf("table did not re-engage after a known status: %v", tolerant.Diagnostics)
		}
	})
	t.Run("submit response with an unknown admission still registers the run", func(t *testing.T) {
		trace := loadTrace(t, "core-completed.json")
		resp := firstOfType(t, trace, "session.message.submit.response")
		resp["payload"].(map[string]any)["admission"] = "parked"
		strict, tolerant := validateBoth(t, trace, "parked")
		if strict.Valid() {
			t.Fatal("strict accepted an unknown admission")
		}
		if !tolerant.Valid() {
			t.Fatalf("tolerant rejected an unknown admission: %v", tolerant.Diagnostics)
		}

		if hasCode(tolerant, CodeIllegalRunTransition) {
			t.Fatalf("run was not registered under an unknown admission: %v", tolerant.Diagnostics)
		}
	})
	t.Run("known members are still held to each other", func(t *testing.T) {

		trace := loadTrace(t, "core-completed.json")
		resp := firstOfType(t, trace, "session.message.submit.response")
		payload := resp["payload"].(map[string]any)
		payload["effective_delivery"], payload["status"] = "later", "queued"
		if _, tolerant := validateBoth(t, trace, "later-queued"); !hasCode(tolerant, CodeIllegalRunTransition) {
			t.Fatalf("contradictory known members passed under a foreign delivery: %v", tolerant.Diagnostics)
		}

		trace = loadTrace(t, "core-completed.json")
		payload = firstOfType(t, trace, "session.message.submit.response")["payload"].(map[string]any)
		payload["admission"], payload["effective_delivery"] = "parked", "queue"
		if _, tolerant := validateBoth(t, trace, "parked-queue-running"); !hasCode(tolerant, CodeIllegalRunTransition) {
			t.Fatalf("contradictory known members passed under a foreign admission: %v", tolerant.Diagnostics)
		}
	})
	t.Run("a foreign admission naming a tracked run reserves nothing", func(t *testing.T) {

		secondSubmit := func(session any, admission string) func(started map[string]any, seq int64) []map[string]any {
			return func(started map[string]any, seq int64) []map[string]any {
				common := map[string]any{"protocol": started["protocol"], "version": started["version"], "profile": started["profile"], "session_id": session}
				req := map[string]any{"type": "session.message.submit.request", "id": "req-again", "payload": map[string]any{"session_id": session, "delivery": "auto", "messages": []any{map[string]any{"role": "user", "content": "more"}}}}
				resp := map[string]any{"type": "session.message.submit.response", "id": "resp-again", "in_reply_to": "req-again", "payload": map[string]any{
					"session_id": session, "accepted": true, "submission_id": "sub-again", "requested_delivery": "auto",
					"admission": admission, "effective_delivery": "merge", "run_id": started["run_id"], "status": "running",
				}}
				for k, v := range common {
					req[k], resp[k] = v, v
				}
				return []map[string]any{req, resp}
			}
		}
		base := loadTrace(t, "core-completed.json")
		session := firstOfType(t, base, "run.started")["session_id"]
		trace := afterRunStarted(t, base, secondSubmit(session, "merged"))
		strict, tolerant := validateBoth(t, trace, "merged")
		if strict.Valid() {
			t.Fatal("strict accepted an unknown admission")
		}
		if !tolerant.Valid() {
			t.Fatalf("tolerant reported a foreign admission on a tracked run: %v", tolerant.Diagnostics)
		}
		trace = afterRunStarted(t, loadTrace(t, "core-completed.json"), secondSubmit("s-other", "merged"))
		if _, tolerant := validateBoth(t, trace, "merged-other-session"); !hasCode(tolerant, CodeScopeMismatch) {
			t.Fatalf("foreign admission naming another session's run passed: %v", tolerant.Diagnostics)
		}
	})
	t.Run("a foreign requested delivery is not held to a capability key", func(t *testing.T) {
		trace := loadTrace(t, "core-completed.json")
		firstOfType(t, trace, "session.message.submit.request")["payload"].(map[string]any)["delivery"] = "later"
		firstOfType(t, trace, "session.message.submit.response")["payload"].(map[string]any)["requested_delivery"] = "later"
		strict, tolerant := validateBoth(t, trace, "delivery-later")
		if strict.Valid() {
			t.Fatal("strict accepted an unknown requested delivery")
		}
		if !tolerant.Valid() || hasCode(tolerant, CodeUnavailableCapability) {
			t.Fatalf("tolerant held a foreign delivery to a capability key: %v", tolerant.Diagnostics)
		}
	})
	t.Run("a run admitted under a foreign admission is not held to the started lifecycle", func(t *testing.T) {

		foreignAdmitted := func(before ...string) []map[string]any {
			trace := loadTrace(t, "core-completed.json")
			firstOfType(t, trace, "session.message.submit.response")["payload"].(map[string]any)["admission"] = "merged"
			return beforeRunStarted(t, trace, statusUpdates(before...))
		}
		if _, tolerant := validateBoth(t, foreignAdmitted("waiting_for_input"), "merged-prestart"); !tolerant.Valid() {
			t.Fatalf("pre-start rule applied under a foreign admission: %v", tolerant.Diagnostics)
		}
		_, tolerant := validateBoth(t, foreignAdmitted("queued"), "merged-prestart-queued")
		if !hasCode(tolerant, CodeIllegalRunTransition) || hasCode(tolerant, CodeMissingRunStarted) {
			t.Fatalf("declared status was not the one judged from: %v", tolerant.Diagnostics)
		}
	})
	t.Run("an unknown support level does not satisfy a capability gate", func(t *testing.T) {

		trace := loadTrace(t, "tools-permission-completed.json")
		features := firstOfType(t, trace, "capabilities.response")["payload"].(map[string]any)["features"].(map[string]any)
		features["tools"].(map[string]any)["level"] = "partial"
		strict, tolerant := validateBoth(t, trace, "tools-partial")
		if strict.Valid() {
			t.Fatal("strict accepted an unknown support level")
		}
		if !hasCode(tolerant, CodeUnavailableCapability) {
			t.Fatalf("unknown support level satisfied the tools gate: %v", tolerant.Diagnostics)
		}
	})
	t.Run("a foreign status on an accepted cancel response is opaque", func(t *testing.T) {

		cancelled := func(responseStatus, nextStatus string) []map[string]any {
			trace := loadTrace(t, "core-cancelled.json")
			firstOfType(t, trace, "run.cancel.response")["payload"].(map[string]any)["status"] = responseStatus
			firstOfType(t, trace, "run.status.updated")["payload"].(map[string]any)["status"] = nextStatus
			return trace
		}
		strict, tolerant := validateBoth(t, cancelled("draining", "running"), "cancel-draining")
		if strict.Valid() {
			t.Fatal("strict accepted an unknown cancel response status")
		}
		if !tolerant.Valid() {
			t.Fatalf("step out of a foreign cancel status was judged: %v", tolerant.Diagnostics)
		}
		if _, tolerant := validateBoth(t, cancelled("cancelling", "running"), "cancel-cancelling"); !hasCode(tolerant, CodeIllegalRunTransition) {
			t.Fatalf("cancelling -> running passed under a known cancel status: %v", tolerant.Diagnostics)
		}
	})
	t.Run("a missing status is a shape defect, not an extension", func(t *testing.T) {

		trace := loadTrace(t, "core-completed.json")
		resp := firstOfType(t, trace, "session.message.submit.response")
		delete(resp["payload"].(map[string]any), "status")
		strict, tolerant := validateBoth(t, trace, "nostatus")
		if !hasCode(strict, CodeIllegalRunTransition) {
			t.Fatalf("strict accepted a started admission without status: %v", strict.Diagnostics)
		}
		if !hasCode(tolerant, CodeIllegalRunTransition) {
			t.Fatalf("tolerant suspended the shape rule for a missing status: %v", tolerant.Diagnostics)
		}
	})
}

func TestTolerantStateChecksScopeOfUnknownEnvelopes(t *testing.T) {

	unknown := func(typ string, envelope, payload map[string]any) func(started map[string]any, seq int64) []map[string]any {
		return func(started map[string]any, seq int64) []map[string]any {
			e := map[string]any{"protocol": started["protocol"], "version": started["version"], "profile": started["profile"], "type": typ, "id": "ext-" + typ, "payload": payload}
			for k, v := range envelope {
				e[k] = v
			}
			return []map[string]any{e}
		}
	}
	cases := map[string]struct {
		typ      string
		envelope map[string]any
		payload  map[string]any
		mismatch bool
	}{
		"session notification agreeing":        {"com.example.session.note", map[string]any{"session_id": "s1"}, map[string]any{"session_id": "s1", "note": "x"}, false},
		"session notification disagreeing":     {"com.example.session.note", map[string]any{"session_id": "s1"}, map[string]any{"session_id": "s2", "note": "x"}, true},
		"unsequenced run envelope agreeing":    {"com.example.run.note", map[string]any{"session_id": "s1", "run_id": "r1"}, map[string]any{"run_id": "r1"}, false},
		"unsequenced run envelope disagreeing": {"com.example.run.note", map[string]any{"session_id": "s1", "run_id": "r1"}, map[string]any{"run_id": "rB"}, true},
		"request disagreeing":                  {"com.example.run.pause.request", map[string]any{"session_id": "s1", "run_id": "r1"}, map[string]any{"session_id": "s1", "run_id": "rB"}, true},
		"payload without scope members":        {"com.example.session.note", map[string]any{"session_id": "s1"}, map[string]any{"note": "x"}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			trace := afterRunStarted(t, loadTrace(t, "core-completed.json"), unknown(tc.typ, tc.envelope, tc.payload))
			_, tolerant := validateBoth(t, trace, "scope-"+tc.typ)
			if got := hasCode(tolerant, CodeScopeMismatch); got != tc.mismatch {
				t.Fatalf("scope_mismatch=%v, want %v: %v", got, tc.mismatch, tolerant.Diagnostics)
			}

			if !tc.mismatch && !strings.HasSuffix(tc.typ, ".request") && !tolerant.Valid() {
				t.Fatalf("agreeing unknown envelope rejected: %v", tolerant.Diagnostics)
			}
		})
	}
}

func TestTolerantStateUnknownEnvelopeScopeDiagnosticsAreExact(t *testing.T) {
	count := func(r Result, code string) int {
		n := 0
		for _, d := range r.Diagnostics {
			if d.Code == code {
				n++
			}
		}
		return n
	}
	t.Run("a sequenced unknown run event reports one scope_mismatch", func(t *testing.T) {

		trace := afterRunStarted(t, loadTrace(t, "core-completed.json"), func(started map[string]any, seq int64) []map[string]any {
			return []map[string]any{{
				"protocol": started["protocol"], "version": started["version"], "profile": started["profile"],
				"type": "com.example.run.mark", "id": "ext-mark",
				"session_id": started["session_id"], "run_id": started["run_id"], "sequence": json.Number(itoa(seq)),
				"payload": map[string]any{"run_id": "rB"},
			}}
		})
		_, tolerant := validateBoth(t, trace, "mark-rB")
		if n := count(tolerant, CodeScopeMismatch); n != 1 {
			t.Fatalf("scope_mismatch reported %d times, want 1: %v", n, tolerant.Diagnostics)
		}
	})
	t.Run("an unknown response is held to its own envelope", func(t *testing.T) {

		trace := afterRunStarted(t, loadTrace(t, "core-completed.json"), func(started map[string]any, seq int64) []map[string]any {
			common := map[string]any{"protocol": started["protocol"], "version": started["version"], "profile": started["profile"], "session_id": started["session_id"]}
			scope := map[string]any{"session_id": started["session_id"], "run_id": started["run_id"]}
			req := map[string]any{"type": "com.example.run.pause.request", "id": "req-pause", "run_id": started["run_id"], "payload": scope}
			resp := map[string]any{"type": "com.example.run.pause.response", "id": "resp-pause", "in_reply_to": "req-pause", "run_id": "rA", "payload": scope}
			for k, v := range common {
				req[k], resp[k] = v, v
			}
			return []map[string]any{req, resp}
		})
		_, tolerant := validateBoth(t, trace, "pause-rA")
		if n := count(tolerant, CodeScopeMismatch); n != 1 {
			t.Fatalf("scope_mismatch reported %d times, want 1: %v", n, tolerant.Diagnostics)
		}
	})
	t.Run("a sequenced unknown request reports one scope_mismatch", func(t *testing.T) {

		trace := afterRunStarted(t, loadTrace(t, "core-completed.json"), func(started map[string]any, seq int64) []map[string]any {
			return []map[string]any{{
				"protocol": started["protocol"], "version": started["version"], "profile": started["profile"],
				"type": "com.example.run.pause.request", "id": "req-seq",
				"session_id": started["session_id"], "run_id": started["run_id"], "sequence": json.Number(itoa(seq)),
				"payload": map[string]any{"session_id": started["session_id"], "run_id": "rB"},
			}}
		})
		_, tolerant := validateBoth(t, trace, "req-seq-rB")
		if n := count(tolerant, CodeScopeMismatch); n != 1 {
			t.Fatalf("scope_mismatch reported %d times, want 1: %v", n, tolerant.Diagnostics)
		}
	})
	t.Run("an unknown response naming no scope cannot answer a scoped request", func(t *testing.T) {
		trace := afterRunStarted(t, loadTrace(t, "core-completed.json"), func(started map[string]any, seq int64) []map[string]any {
			common := map[string]any{"protocol": started["protocol"], "version": started["version"], "profile": started["profile"]}
			req := map[string]any{"type": "com.example.run.pause.request", "id": "req-pause", "session_id": started["session_id"], "run_id": started["run_id"], "payload": map[string]any{"session_id": started["session_id"], "run_id": started["run_id"]}}
			resp := map[string]any{"type": "com.example.run.pause.response", "id": "resp-pause", "in_reply_to": "req-pause", "payload": map[string]any{"ok": true}}
			for k, v := range common {
				req[k], resp[k] = v, v
			}
			return []map[string]any{req, resp}
		})
		_, tolerant := validateBoth(t, trace, "pause-unscoped")
		if n := count(tolerant, CodeScopeMismatch); n != 2 {
			t.Fatalf("scope_mismatch reported %d times, want 2 (session and run): %v", n, tolerant.Diagnostics)
		}
	})
	t.Run("an uncorrelated unknown response is still held to its own envelope", func(t *testing.T) {

		trace := afterRunStarted(t, loadTrace(t, "core-completed.json"), func(started map[string]any, seq int64) []map[string]any {
			return []map[string]any{{
				"protocol": started["protocol"], "version": started["version"], "profile": started["profile"],
				"type": "com.example.run.pause.response", "id": "resp-orphan", "in_reply_to": "req-nope",
				"session_id": started["session_id"], "run_id": "rA",
				"payload": map[string]any{"session_id": started["session_id"], "run_id": started["run_id"]},
			}}
		})
		_, tolerant := validateBoth(t, trace, "orphan-rA")
		if !hasCode(tolerant, CodeUnmatchedResponse) {
			t.Fatalf("orphan response not reported as unmatched: %v", tolerant.Diagnostics)
		}
		if n := count(tolerant, CodeScopeMismatch); n != 1 {
			t.Fatalf("scope_mismatch reported %d times, want 1: %v", n, tolerant.Diagnostics)
		}
	})
}

func TestTolerantStateOpaqueAdmissionAtClose(t *testing.T) {

	trace := loadTrace(t, "core-completed.json")
	firstOfType(t, trace, "session.message.submit.response")["payload"].(map[string]any)["admission"] = "merged"
	trace = trace[:2]
	_, tolerant := validateBoth(t, trace, "merged-open")
	if !hasCode(tolerant, CodeMissingRunTerminal) {
		t.Fatalf("open foreign-admitted run not reported as missing its terminal: %v", tolerant.Diagnostics)
	}
	if hasCode(tolerant, CodeMissingRunStarted) {
		t.Fatalf("close-time start check applied under a foreign admission: %v", tolerant.Diagnostics)
	}
}

func TestTolerantStateSkipsPreStartRuleForUnknownEvents(t *testing.T) {
	trace := loadTrace(t, "core-completed.json")
	var out []map[string]any
	inserted := false
	for _, e := range trace {
		if e["type"] == "run.started" && !inserted {

			out = append(out, map[string]any{
				"protocol": e["protocol"], "version": e["version"], "profile": e["profile"],
				"type": "com.example.run.prelude", "id": "ext-prelude",
				"session_id": e["session_id"], "run_id": e["run_id"],
				"sequence": json.Number("1"),
				"payload":  map[string]any{},
			})
			inserted = true
		}
		if inserted && e["run_id"] != nil && e["sequence"] != nil {
			seq, _ := e["sequence"].(json.Number).Int64()
			e["sequence"] = json.Number(itoa(seq + 1))
		}
		out = append(out, e)
	}
	_, tolerant := validateBoth(t, out, "prelude")
	if hasCode(tolerant, CodeMissingRunStarted) {
		t.Fatalf("unknown pre-start event was judged by the known-type rule: %v", tolerant.Diagnostics)
	}
	if !tolerant.Valid() {
		t.Fatalf("tolerant rejected an unknown pre-start event: %v", tolerant.Diagnostics)
	}
}

func TestTolerantStateScopesUnknownRequestResponse(t *testing.T) {
	base := firstOfType(t, loadTrace(t, "core-completed.json"), "run.started")
	common := func(typ, id string, extra map[string]any) map[string]any {
		e := map[string]any{
			"protocol": base["protocol"], "version": base["version"], "profile": base["profile"],
			"type": typ, "id": id, "session_id": base["session_id"], "payload": map[string]any{},
		}
		for k, v := range extra {
			e[k] = v
		}
		return e
	}
	trace := []map[string]any{
		common("com.example.run.pause.request", "pause-req", map[string]any{"run_id": "rA"}),
		common("com.example.run.pause.response", "pause-resp", map[string]any{"run_id": "rB", "in_reply_to": "pause-req"}),
	}
	_, tolerant := validateBoth(t, trace, "pause")
	if !hasCode(tolerant, CodeScopeMismatch) {
		t.Fatalf("response on another run was not diagnosed: %v", tolerant.Diagnostics)
	}

	trace[1]["run_id"] = "rA"
	_, tolerant = validateBoth(t, trace, "pause-ok")
	if hasCode(tolerant, CodeScopeMismatch) {
		t.Fatalf("matching scope was diagnosed: %v", tolerant.Diagnostics)
	}
}

func TestTolerantAcceptsEveryPositiveFixture(t *testing.T) {
	tolerant, err := NewWith(Options{Mode: ModeTolerant})
	if err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest(filepath.Join(repositoryRoot(t), "fixtures", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(repositoryRoot(t), "fixtures")
	checked := 0
	for _, entry := range m.Fixtures {
		if !entry.Valid || entry.Profile == ProfileModelProvider {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Path))
		if err != nil {
			t.Fatal(err)
		}
		validator := tolerant
		if len(entry.Packs) > 0 {

			dirs := make([]string, 0, len(entry.Packs))
			for _, pack := range entry.Packs {
				dirs = append(dirs, filepath.Join(root, pack))
			}
			packs, err := LoadPacks(dirs)
			if err != nil {
				t.Fatal(err)
			}
			validator, err = NewWith(Options{Mode: ModeTolerant, Packs: packs})
			if err != nil {
				t.Fatal(err)
			}
		}
		if result := validator.ValidateBytes(data, entry.Path); !result.Valid() {
			t.Errorf("tolerant rejected positive fixture %s: %v", entry.ID, result.Diagnostics)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no positive fixtures checked")
	}
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"": ModeStrict, "strict": ModeStrict, "Tolerant": ModeTolerant, " tolerant ": ModeTolerant} {
		got, err := ParseMode(in)
		if err != nil || got != want {
			t.Errorf("ParseMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseMode("lenient"); err == nil {
		t.Error("ParseMode accepted an unknown mode")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func insertRunEvents(t *testing.T, trace []map[string]any, anchor string, before bool, build func(anchor map[string]any, seq int64) []map[string]any) []map[string]any {
	t.Helper()
	seqOf := func(e map[string]any) int64 {
		seq, _ := e["sequence"].(json.Number).Int64()
		return seq
	}
	var out []map[string]any
	shift, seen := int64(0), false
	insert := func(built []map[string]any) {
		for _, inserted := range built {
			if inserted["sequence"] != nil {
				shift++
			}
			out = append(out, inserted)
		}
	}
	for _, e := range trace {
		isAnchor := !seen && e["type"] == anchor
		if isAnchor && before {
			seen = true
			insert(build(e, seqOf(e)))
		}
		if seen && e["run_id"] != nil && e["sequence"] != nil {
			e["sequence"] = json.Number(itoa(seqOf(e) + shift))
		}
		out = append(out, e)
		if isAnchor && !before {
			seen = true
			insert(build(e, seqOf(e)+1))
		}
	}
	if !seen {
		t.Fatalf("trace has no %s", anchor)
	}
	return out
}

func afterRunStarted(t *testing.T, trace []map[string]any, build func(started map[string]any, seq int64) []map[string]any) []map[string]any {
	t.Helper()
	return insertRunEvents(t, trace, "run.started", false, build)
}

func beforeRunStarted(t *testing.T, trace []map[string]any, build func(started map[string]any, seq int64) []map[string]any) []map[string]any {
	t.Helper()
	return insertRunEvents(t, trace, "run.started", true, build)
}

func statusUpdates(statuses ...string) func(started map[string]any, seq int64) []map[string]any {
	return func(started map[string]any, seq int64) []map[string]any {
		var out []map[string]any
		for n, status := range statuses {
			out = append(out, map[string]any{
				"protocol": started["protocol"], "version": started["version"], "profile": started["profile"],
				"type": "run.status.updated", "id": "ext-status-" + itoa(int64(n)),
				"session_id": started["session_id"], "run_id": started["run_id"],
				"sequence": json.Number(itoa(seq + int64(n))),
				"payload":  map[string]any{"session_id": started["session_id"], "run_id": started["run_id"], "status": status},
			})
		}
		return out
	}
}

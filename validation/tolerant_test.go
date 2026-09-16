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

// The tolerance step is judged against the bundle *as it stands*: every sample
// here is an envelope a later revision, or an extension pack, could legally
// emit, validated against this revision's schemas in both modes. Tolerant must
// accept the additive ones and both modes must reject the malformed ones —
// the second half is what keeps the transform from being merely permissive.

func compileMode(t *testing.T, mode Mode) *jsonschema.Schema {
	t.Helper()
	s, err := CompileSchemasWith(CompileOptions{Mode: mode})
	if err != nil {
		t.Fatalf("compile %s: %v", mode, err)
	}
	return s
}

// loadTrace reads a fixture as the list of decoded envelopes it contains.
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

// firstOfType returns the first envelope of the type, or fails.
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

// roundTrip re-encodes an envelope so the schema sees plain decoded JSON.
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
	strict   bool // accepted by the strict compile
	tolerant bool // accepted by the tolerant compile
}

func TestTolerantCompileSamples(t *testing.T) {
	strict := compileMode(t, ModeStrict)
	tolerant := compileMode(t, ModeTolerant)
	samples := []modeExpectation{
		{
			// A new leaf enum value surfaces as a string rather than failing
			// the message.
			name: "new leaf enum value on submit response",
			envelope: func(t *testing.T) map[string]any {
				e := firstOfType(t, loadTrace(t, "core-completed.json"), "session.message.submit.response")
				e["payload"].(map[string]any)["admission"] = "parked"
				return e
			},
			strict: false, tolerant: true,
		},
		{
			// An unknown member of a closed payload is ignored, not rejected.
			name: "unknown payload member",
			envelope: func(t *testing.T) map[string]any {
				e := firstOfType(t, loadTrace(t, "core-completed.json"), "run.started")
				e["payload"].(map[string]any)["com.example.trace_id"] = "abc"
				return e
			},
			strict: false, tolerant: true,
		},
		{
			// An unknown content kind takes the discriminated-union fallback:
			// the layered draft already names audio as a content kind.
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
			// A later revision's optional member on a known content kind is
			// ignored like any other unknown member: the branch's required
			// members and types stay exact, its closedness does not.
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
			// A known kind with a malformed body is rejected in both modes:
			// the branch's required members and member types stay exact, so
			// only an unknown kind takes the fallback.
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
			// A const is a fixed semantic value, never a vocabulary: run.started
			// carrying status "failed" is malformed under any revision.
			name: "run.started with status failed",
			envelope: func(t *testing.T) map[string]any {
				e := firstOfType(t, loadTrace(t, "core-completed.json"), "run.started")
				e["payload"].(map[string]any)["status"] = "failed"
				return e
			},
			strict: false, tolerant: false,
		},
		{
			// An unknown question kind matches neither if-guard, takes neither
			// then, and surfaces as a string. The guards themselves stay exact;
			// only the outer enum they compare against is lifted.
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
			// A known question kind still takes its guard: single_choice
			// without options is rejected in both modes, which is what proves
			// the if-guards were not lifted along with the outer enum.
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
			// An unknown envelope type satisfies the common fields only.
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
			// A known envelope type that fails its own branch is not rescued
			// by the fallback: the fallback excludes every known type.
			name: "known envelope type with wrong payload",
			envelope: func(t *testing.T) map[string]any {
				e := firstOfType(t, loadTrace(t, "core-completed.json"), "run.started")
				e["payload"] = map[string]any{"note": "not a run.started payload"}
				return e
			},
			strict: false, tolerant: false,
		},
		{
			// The protocol constants are never lifted.
			name: "wrong protocol version",
			envelope: func(t *testing.T) map[string]any {
				e := firstOfType(t, loadTrace(t, "core-completed.json"), "run.started")
				e["version"] = "0.2"
				return e
			},
			strict: false, tolerant: false,
		},
		{
			// The unmodified envelope is the control: valid in both.
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

// A schema that admits an unknown envelope is not enough on its own: the
// stateful validator advances a run's sequence cursor only for the types it
// enumerates, so a tolerated unknown run event at N would be skipped and the
// next known event at N+1 diagnosed as sequence_gap. Tolerant mode classifies
// the unknown type by wire scope instead.
func TestTolerantStateClassifiesUnknownRunEvent(t *testing.T) {
	trace := loadTrace(t, "core-completed.json")
	// Insert an unknown run-scoped event right after run.started, at the next
	// sequence, and shift every later run event of that run by one.
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

// validateBoth runs a trace through both validators and returns the results.
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

// A known envelope carrying an unknown enum value is the other half of
// tolerance: the schema accepts it, and the state must treat the value as
// opaque — suspending the rule that reads it, keeping every rule that does
// not — rather than judging it against a table it is not in.
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
		// running -> paused -> queued: running -> queued is illegal in this
		// revision's table, but the validator cannot know what may follow
		// paused. Once queued is reached the table applies again, so
		// queued -> waiting_for_input is illegal.
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
		// The run must have been registered: without that every later run
		// event would carry "run event has no accepted admission".
		if hasCode(tolerant, CodeIllegalRunTransition) {
			t.Fatalf("run was not registered under an unknown admission: %v", tolerant.Diagnostics)
		}
	})
	t.Run("known members are still held to each other", func(t *testing.T) {
		// A foreign effective delivery suspends only the predicates that read
		// it; admission started with status queued contradicts itself.
		trace := loadTrace(t, "core-completed.json")
		resp := firstOfType(t, trace, "session.message.submit.response")
		payload := resp["payload"].(map[string]any)
		payload["effective_delivery"], payload["status"] = "later", "queued"
		if _, tolerant := validateBoth(t, trace, "later-queued"); !hasCode(tolerant, CodeIllegalRunTransition) {
			t.Fatalf("contradictory known members passed under a foreign delivery: %v", tolerant.Diagnostics)
		}
		// Likewise a foreign admission leaves effective delivery and status
		// bound to each other.
		trace = loadTrace(t, "core-completed.json")
		payload = firstOfType(t, trace, "session.message.submit.response")["payload"].(map[string]any)
		payload["admission"], payload["effective_delivery"] = "parked", "queue"
		if _, tolerant := validateBoth(t, trace, "parked-queue-running"); !hasCode(tolerant, CodeIllegalRunTransition) {
			t.Fatalf("contradictory known members passed under a foreign admission: %v", tolerant.Diagnostics)
		}
	})
	t.Run("a foreign admission naming a tracked run reserves nothing", func(t *testing.T) {
		// A later revision's admission may describe an operation on the
		// existing run; the validator cannot judge it, so it neither reports
		// a second admission nor reserves anything. The run's session is
		// still checked.
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
		// Whether run.started is owed under a later revision's admission is
		// unknown, so a known run event before it is not missing_run_started;
		// the run's status is the one the response declared, so the first
		// transition is judged from running, not from a fabricated queued.
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
		// agent-control-profile.md: unknown or absent support levels are
		// treated as unavailable. The descriptor keeps the opaque value, but
		// a tools event gated on it is refused.
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
		// core-cancelled: run.started, cancel accepted, status.updated,
		// run.cancelled. With the response declaring a foreign status the
		// next known update is the first step out of it and is not judged as
		// cancelling -> running; with the response declaring cancelling, it is.
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
		// The schema leaves status optional on submit.response; the state rule
		// requires it for a started admission. An absent member is not a
		// foreign value, so tolerant mode must keep that rule.
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

// An unknown run-scoped event before run.started takes only the
// type-independent bookkeeping: the pre-start rule is a known-type rule, since
// only a known terminal may settle a run early and an unknown type cannot say
// whether it is one.
func TestTolerantStateChecksScopeOfUnknownEnvelopes(t *testing.T) {
	// Every unknown scoped envelope is held to scope agreement between its
	// generic payload members and its envelope, whatever it is called and
	// whether or not it carries a sequence.
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
		envelope map[string]any // merged over the common members; "started" values resolved below
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
			// The unmatched request diagnostic is expected for the request
			// case; nothing else may leak from an agreeing envelope.
			if !tc.mismatch && !strings.HasSuffix(tc.typ, ".request") && !tolerant.Valid() {
				t.Fatalf("agreeing unknown envelope rejected: %v", tolerant.Diagnostics)
			}
		})
	}
}

func TestTolerantStateOpaqueAdmissionAtClose(t *testing.T) {
	// A trace that ends while a foreign-admitted run is open owes a terminal
	// (type-independent) but not run.started (a lifecycle rule the validator
	// cannot judge under that admission).
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
			// Before run.started, at sequence 1; run.started and everything
			// after shift by one.
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

// An unknown request/response pair is scoped by its envelope, so the generic
// correlation check still binds the response to the request's run: a request
// on run A answered on run B is a scope_mismatch whatever the operation is
// called.
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
	// The same pair on one run correlates cleanly.
	trace[1]["run_id"] = "rA"
	_, tolerant = validateBoth(t, trace, "pause-ok")
	if hasCode(tolerant, CodeScopeMismatch) {
		t.Fatalf("matching scope was diagnosed: %v", tolerant.Diagnostics)
	}
}

// The regression guard the plan requires: every fixture the strict compile
// accepts, the tolerant compile accepts too.
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
		if !entry.Valid {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Path))
		if err != nil {
			t.Fatal(err)
		}
		if result := tolerant.ValidateBytes(data, entry.Path); !result.Valid() {
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

// insertRunEvents returns the trace with the envelopes build produces
// inserted before or after the first envelope of the anchor type. build
// receives that envelope and the first sequence number the inserted run
// events must take, in order; every run event from there on is renumbered
// past them.
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

// statusUpdates builds one run.status.updated per status, in order.
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

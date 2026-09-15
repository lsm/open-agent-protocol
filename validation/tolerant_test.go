package validation

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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
			// A known kind with a malformed body is rejected in both modes:
			// known branches stay strict, only an unknown kind is tolerated.
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

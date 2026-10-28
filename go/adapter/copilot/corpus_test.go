package copilot

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/adapter/copilot/internal/native"
)

type corpusIDs struct{ n int }

func (s *corpusIDs) NewID(kind string) string { s.n++; return fmt.Sprintf("%s-%d", kind, s.n) }

type corpusClock struct{ n int64 }

func (c *corpusClock) Now() time.Time { c.n++; return time.UnixMilli(c.n) }

func structural(t *testing.T, data []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var out any
	if err := decoder.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func replay(t *testing.T, name string) {
	t.Helper()
	dir := filepath.Join("..", "..", "..", filepath.FromSlash(CorpusDirectory), name)
	r := newReducer("s1", "ses", &corpusIDs{}, &corpusClock{})
	file, err := os.Open(filepath.Join(dir, "native.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scan := bufio.NewScanner(file)
	var admitted *run
	for scan.Scan() {
		var step corpusStep
		if err := json.Unmarshal(scan.Bytes(), &step); err != nil {
			t.Fatal(err)
		}
		admitted, err = applyStep(r, admitted, step)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(dir, "expected-oap.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(r.events)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(structural(t, want), structural(t, got)) {
		t.Fatalf("wanted %s\ngot %s", want, got)
	}
}

func TestLifecycleCorpus(t *testing.T) {
	for _, name := range []string{"completed-text", "queued-busy-period", "unknown-event", "session-error", "foreign-session"} {
		t.Run(name, func(t *testing.T) { replay(t, name) })
	}
}

type corpusStep struct {
	Action    string          `json:"action"`
	Direction string          `json:"direction"`
	Raw       json.RawMessage `json:"raw"`
}

func applyStep(r *reducer, admitted *run, step corpusStep) (*run, error) {
	direction := "copilot-to-host"
	if step.Action == "admit" {
		direction = "host-to-copilot"
	}
	if step.Direction != direction {
		return admitted, fmt.Errorf("invalid corpus direction")
	}
	m, err := native.Parse(step.Raw)
	if err != nil {
		return admitted, err
	}
	switch step.Action {
	case "admit":
		if m.Kind != native.Request || m.Method != "session.send" {
			return admitted, fmt.Errorf("invalid admission")
		}
		return r.admit(false)
	case "bind":
		var result map[string]json.RawMessage
		if err := json.Unmarshal(m.Result, &result); err != nil {
			return admitted, err
		}
		if admitted == nil || m.Kind != native.Response || result == nil || native.Text(result, "messageId") == "" {
			return admitted, fmt.Errorf("unbound reply")
		}
		admitted.nativeMessage = native.Text(result, "messageId")
	case "observe":
		event, err := native.DecodeEvent(m)
		if err != nil {
			return admitted, err
		}
		if event == nil {
			return admitted, fmt.Errorf("missing event")
		}
		return admitted, r.observe(*event)
	default:
		return admitted, fmt.Errorf("unhandled action %s", step.Action)
	}
	return admitted, nil
}

func TestReplayRejectsDirectionAndBindingCorruption(t *testing.T) {
	for _, step := range []corpusStep{
		{Action: "bind", Direction: "host-to-copilot", Raw: json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"messageId":"m1"}}`)},
		{Action: "observe", Direction: "host-to-copilot", Raw: json.RawMessage(`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"ses","event":{"type":"session.idle","data":{}}}}`)},
		{Action: "bind", Direction: "copilot-to-host", Raw: json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{}}`)},
		{Action: "bind", Direction: "copilot-to-host", Raw: json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":null}`)},
	} {
		r := newReducer("s1", "ses", &corpusIDs{}, &corpusClock{})
		if _, err := applyStep(r, &run{}, step); err == nil {
			t.Errorf("accepted corrupted %s step: %s", step.Action, step.Raw)
		}
	}
}

func TestUsageCorpus(t *testing.T) {
	for _, name := range []string{"reasoning-usage", "usage-sparse", "usage-precision", "usage-wide", "usage-partial-call", "usage-overflow"} {
		t.Run(name, func(t *testing.T) { replay(t, name) })
	}
}

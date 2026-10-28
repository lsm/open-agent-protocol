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
		var step struct {
			Action    string          `json:"action"`
			Direction string          `json:"direction"`
			Raw       json.RawMessage `json:"raw"`
		}
		if err := json.Unmarshal(scan.Bytes(), &step); err != nil {
			t.Fatal(err)
		}
		m, err := native.Parse(step.Raw)
		if err != nil {
			t.Fatal(err)
		}
		switch step.Action {
		case "admit":
			if step.Direction != "host-to-copilot" || m.Kind != native.Request || m.Method != "session.send" {
				t.Fatal("invalid admission")
			}
			admitted, err = r.admit(false)
			if err != nil {
				t.Fatal(err)
			}
		case "bind":
			var result map[string]json.RawMessage
			if err := json.Unmarshal(m.Result, &result); err != nil {
				t.Fatal(err)
			}
			if admitted == nil || m.Kind != native.Response {
				t.Fatal("unbound reply")
			}
			admitted.nativeMessage = native.Text(result, "messageId")
		case "observe":
			event, err := native.DecodeEvent(m)
			if err != nil || event == nil {
				t.Fatalf("event %v %v", event, err)
			}
			if err := r.observe(*event); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("unhandled action %s", step.Action)
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

package native

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lsm/open-agent-protocol/harnesses"
)

func TestPinnedCorpusMessages(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "..", filepath.FromSlash(harnesses.Current("copilot-cli").Corpus))
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Cases []struct {
			Path string `json:"path"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil || len(manifest.Cases) == 0 {
		t.Fatalf("manifest: %v", err)
	}
	var entries []string
	for _, item := range manifest.Cases {
		entries = append(entries, filepath.Join(root, item.Path, "native.jsonl"))
	}
	for _, path := range entries {
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			scan := bufio.NewScanner(file)
			for scan.Scan() {
				var item struct {
					Action string          `json:"action"`
					Raw    json.RawMessage `json:"raw"`
				}
				if err := json.Unmarshal(scan.Bytes(), &item); err != nil {
					t.Fatal(err)
				}
				m, err := Parse(item.Raw)
				if item.Action == "reject-wire" {
					if err == nil {
						t.Fatal("accepted invalid wire")
					}
					continue
				}
				if err != nil {
					t.Fatalf("%s: %v", item.Raw, err)
				}
				want := Notification
				if item.Action == "admit" {
					want = Request
				} else if item.Action == "bind" {
					want = Response
				}
				if m.Kind != want {
					t.Fatalf("kind %s, want %s", m.Kind, want)
				}
				event, err := DecodeEvent(m)
				if item.Action == "reject-event" {
					if err == nil {
						t.Fatal("accepted malformed event")
					}
				} else if err != nil {
					t.Fatal(err)
				} else if item.Action == "observe" && (event == nil || event.Type == "" || event.SessionID == "") {
					t.Fatalf("lost event: %+v", event)
				}
			}
			if err := scan.Err(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEventKeepsNativeNamesAndExactNumbers(t *testing.T) {
	m, err := Parse([]byte(`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"native-session","event":{"type":"session.future","ephemeral":true,"data":{"count":18446744073709551615}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	event, err := DecodeEvent(m)
	if err != nil || event == nil || event.Type != "session.future" || !event.Ephemeral || string(event.Data["count"]) != "18446744073709551615" {
		t.Fatalf("event %+v, error %v", event, err)
	}
	for _, frame := range []string{`{"jsonrpc":"2.0","method":"session.lifecycle","params":{}}`, `{"jsonrpc":"2.0","id":1,"result":{}}`} {
		m, err := Parse([]byte(frame))
		if err != nil {
			t.Fatal(err)
		}
		if e, err := DecodeEvent(m); e != nil || err != nil {
			t.Fatalf("non-event decoded: %+v %v", e, err)
		}
	}
}

func TestMessageShapes(t *testing.T) {
	for _, frame := range []string{
		`[]`, `null`, `{"jsonrpc":"1.0","id":1,"result":{}}`,
		`{"jsonrpc":"2.0","result":{}}`, `{"jsonrpc":"2.0","id":null,"result":{}}`,
		`{"jsonrpc":"2.0","id":1.5,"result":{}}`, `{"jsonrpc":"2.0","id":true,"result":{}}`,
		`{"jsonrpc":"2.0","method":""}`, `{"jsonrpc":"2.0","method":"m","result":{}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":1}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":1,"message":""}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":"1","message":"no"}}`,
		`{"jsonrpc":"2.0","id":1,"id":2,"result":{}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"nested":1,"nested":2}}`,
		`{"jsonrpc":"2.0","id":1,"result":{}} {}`,
	} {
		if _, err := Parse([]byte(frame)); err == nil {
			t.Errorf("accepted %s", frame)
		}
	}
	for _, frame := range []string{`{"jsonrpc":"2.0","id":1,"result":null}`, `{"jsonrpc":"2.0","id":"q1","method":"gitHubToken.getToken"}`, `{"jsonrpc":"2.0","id":-1,"error":{"code":-32601,"message":"no such method"}}`} {
		if _, err := Parse([]byte(frame)); err != nil {
			t.Errorf("refused %s: %v", frame, err)
		}
	}
}

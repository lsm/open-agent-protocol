package native

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPinnedCorpusMessages(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "..", "fixtures", "adapters", "copilot-cli-v1.0.98")
	entries, err := filepath.Glob(filepath.Join(root, "*", "native.jsonl"))
	if err != nil || len(entries) != 10 {
		t.Fatalf("corpus inventory: %v, cases %d", err, len(entries))
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
			}
			if err := scan.Err(); err != nil {
				t.Fatal(err)
			}
		})
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

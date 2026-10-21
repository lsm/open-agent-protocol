package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHubStdioBudgetsTheRawRequestMember(t *testing.T) {
	oapx := os.Getenv("OAP_OAPX_BIN")
	if oapx == "" {
		t.Skip("set OAP_OAPX_BIN to drive the real stdio envelope boundary")
	}
	if !filepath.IsAbs(oapx) {
		t.Fatalf("OAP_OAPX_BIN must be absolute, got %q", oapx)
	}
	const cap = 16 << 20
	for _, encoding := range []string{"compact", "whitespace", "escaped"} {
		for _, size := range []int{cap - 1, cap, cap + 1} {
			t.Run(fmt.Sprintf("%s/%d", encoding, size), func(t *testing.T) {
				prefix := `{` + openEnvelopeFields + `"id":"open","payload":{"session_id":"boundary"},"extensions":{"test.padding":"`
				suffix := `"}}`
				padding := size - len(prefix) - len(suffix)
				var raw string
				switch encoding {
				case "compact":
					raw = prefix + strings.Repeat("a", padding) + suffix
				case "whitespace":
					raw = prefix + suffix[:len(suffix)-1] + strings.Repeat(" ", padding) + "}"
				case "escaped":
					raw = prefix + strings.Repeat(`\u0061`, padding/6) + strings.Repeat("a", padding%6) + suffix
				}
				var parsed any
				if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
					t.Fatal(err)
				}
				compact, err := json.Marshal(parsed)
				if err != nil || len(raw) != size || (encoding != "compact" && len(compact) >= cap) {
					t.Fatalf("bad boundary input: raw=%d compact=%d err=%v", len(raw), len(compact), err)
				}
				ops := []string{"open"}
				if encoding == "whitespace" && size > cap {
					ops = append(ops, "submit", "resolve", "cancel", "settings")
				}
				for _, op := range ops {
					line := fmt.Sprintf(`{"id":1,"op":%q, "reque\u0073t" : `, op) + raw + ` }`
					if op == "open" {
						line = strings.Replace(line, `"op":"open"`, `"op":"open","adapter":"memory"`, 1)
					}
					for _, binary := range []string{oapBinary(t), oapx} {
						c := spawnBinary(t, binary, "--session-history=")
						c.send("%s", line)
						received, ok := c.next()
						if !ok {
							t.Fatal("stdio ended before the response")
						}
						answer := c.decode(received)
						if answer.ID != 1 {
							t.Fatalf("unexpected response: %s", received)
						}
						if size > cap {
							if answer.OK || answer.Error.Code != "request_too_large" {
								t.Errorf("%s %s raw=%d compact=%d: want request_too_large, got %s", binary, op, size, len(compact), received)
							}
						} else if !answer.OK {
							t.Errorf("%s raw=%d: want successful open, got %s", binary, size, received)
						}
					}
				}
			})
		}
	}
}

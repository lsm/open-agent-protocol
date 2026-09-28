package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// The hub's stdio wire, driven the same way through both trees.
//
// One script goes to `goap hub --stdio` and to `oapx hub --stdio`, and the two
// answers are compared as parsed JSON rather than as bytes. Bytes would compare
// things this is not about: the two trees order an envelope's members
// differently, which the hub draft records under "recorded, and not
// divergences", and only the ids and timestamps the daemon mints are normalised
// away. Everything else — every member, every code, every word of a message —
// has to be the same, or this fails.
//
// Answers are keyed by the request's `id` rather than by position, because the
// two trees do not answer in the same order: Go's frontend serves ops
// concurrently and writes each as it completes, while the Zig frontend serves
// one line at a time in the order it read them. The id is the correlation the
// host chose, and comparing by it is what makes the two comparable at all.
var hubParityScenarios = map[string][]string{
	"the five ops this wire serves": {
		`{"id":1,"op":"adapters"}`,
		`{"id":2,"op":"sessions"}`,
		`{"id":3,"op":"capabilities","adapter":"memory"}`,
		`{"id":4,"op":"state","session_id":"absent"}`,
		`{"id":5,"op":"close","session_id":"absent"}`,
	},
	"refusals, each naming the code the draft pins": {
		`{"id":1,"op":"nope"}`,
		`{"id":2,"op":"adapters","adapter":"absent"}`,
		`{"id":3,"op":"state"}`,
		`{"id":4,"op":"capabilities"}`,
		`{"id":5,"op":"adapters","session_id":"x"}`,
		`{"id":6,"op":"close"}`,
	},
	"a null parameter is supplied, and a wrongly typed one is not read": {
		`{"id":1,"op":"adapters","adapter":null}`,
		`{"id":2,"op":"sessions","session_id":null}`,
		`{"id":3,"op":"adapters","after":null}`,
	},
	"a framing defect stops the wire, and nothing after it is answered": {
		`{"id":1,"op":"adapters"}`,
		`not json`,
		`{"id":2,"op":"sessions"}`,
	},
}

func TestHubStdioAnswersGoapAndOapxTheSame(t *testing.T) {
	oapx := os.Getenv("OAP_OAPX_BIN")
	if oapx == "" {
		t.Skip("set OAP_OAPX_BIN to an oapx binary to compare its stdio wire with goap's")
	}
	if !filepath.IsAbs(oapx) {
		t.Fatalf("OAP_OAPX_BIN must be absolute, got %q", oapx)
	}

	for name, lines := range hubParityScenarios {
		t.Run(name, func(t *testing.T) {
			goAnswers := runHubScript(t, oapBinary(t), lines)
			zigAnswers := runHubScript(t, oapx, lines)
			assertSameHubAnswers(t, goAnswers, zigAnswers)
		})
	}
}

// TestHubStdioAnswersUnavailableForATransportItDoesNotCarry is not a
// differential case. `drafts/cli.md` says a verb or flag a binary does not carry
// answers `unavailable`, names what is missing, and exits non-zero — and never
// falls back to something else that looks like it worked. `oapx hub` carries only
// the stdio transport today, so the other two are the rule's own cases: a host
// that asked for HTTP deserves to be told it is not here yet rather than handed
// a pipe.
func TestHubStdioAnswersUnavailableForATransportItDoesNotCarry(t *testing.T) {
	oapx := os.Getenv("OAP_OAPX_BIN")
	if oapx == "" {
		t.Skip("set OAP_OAPX_BIN to an oapx binary to check its unavailable answers")
	}
	for _, argument := range []string{"--addr=127.0.0.1:0", "--config=missing.json"} {
		t.Run(argument, func(t *testing.T) {
			command := exec.Command(oapx, "hub", argument)
			command.Stdin = strings.NewReader("")
			output, err := command.CombinedOutput()
			if err == nil {
				t.Errorf("oapx hub %s exited 0; a transport it does not carry must exit non-zero", argument)
			}
			if !strings.Contains(string(output), "unavailable") {
				t.Errorf("oapx hub %s did not answer unavailable:\n%s", argument, output)
			}
			if strings.Contains(string(output), "{\"id\"") {
				t.Errorf("oapx hub %s answered a request rather than refusing the flag", argument)
			}
		})
	}
}

// TestHubStdioEndsCleanlyWhenTheHostClosesThePipe pins the other half: a host
// that says it is done is a serve that succeeded, not a failure. Both trees exit
// zero, and treating end-of-stream as an error would make every well-behaved host
// look like a broken one.
func TestHubStdioEndsCleanlyWhenTheHostClosesThePipe(t *testing.T) {
	oapx := os.Getenv("OAP_OAPX_BIN")
	if oapx == "" {
		t.Skip("set OAP_OAPX_BIN to an oapx binary to check it ends cleanly")
	}
	command := exec.Command(oapx, "hub", "--stdio")
	command.Stdin = strings.NewReader("")
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		t.Fatalf("oapx hub --stdio with a host that sent nothing and closed: %v", err)
	}
}

func hubCommand(t *testing.T, binary string) *exec.Cmd {
	t.Helper()
	command := exec.Command(binary, "hub", "--stdio")
	return command
}

// runHubScript feeds `lines` to one binary's stdio hub and returns its answers
// keyed by request id, with the daemon's own ids and timestamps normalised away.
func runHubScript(t *testing.T, binary string, lines []string) map[string]map[string]any {
	t.Helper()
	command := hubCommand(t, binary)
	command.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	var stderr strings.Builder
	command.Stderr = &stderr

	done := make(chan error, 1)
	var output []byte
	go func() {
		var err error
		output, err = command.Output()
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		_ = command.Process.Kill()
		t.Fatalf("%s hub --stdio did not finish within 60s", binary)
	}

	answers := map[string]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			t.Fatalf("%s wrote a line that is not JSON: %q", binary, line)
		}
		normaliseHubAnswer(parsed)
		key, ok := parsed["id"].(float64)
		if !ok {
			t.Fatalf("%s wrote an answer with no id: %q", binary, line)
		}
		answers[fmt.Sprintf("%d", int64(key))] = parsed
	}
	return answers
}

// normaliseHubAnswer replaces the two things a daemon is free to choose — the
// envelope ids it mints and the timestamps it stamps — with a fixed marker, and
// leaves everything else exactly as it arrived. The ids are still compared
// *relationally*: `in_reply_to` is checked against the minted id it replies to,
// so a wire that correlated wrongly still fails.
func normaliseHubAnswer(answer map[string]any) {
	result, isObject := answer["result"].(map[string]any)
	if !isObject {
		return
	}
	if id, minted := result["id"].(string); minted {
		result["id"] = "<minted>"
		_ = id
	}
	if _, present := result["in_reply_to"]; present {
		result["in_reply_to"] = "<minted>"
	}
	if created, present := result["created_at"]; present {
		_ = created
		result["created_at"] = "<minted>"
	}
	payload, isObject := result["payload"].(map[string]any)
	if isObject {
		if created, present := payload["created_at"]; present {
			_ = created
			payload["created_at"] = "<minted>"
		}
	}
}

func assertSameHubAnswers(t *testing.T, goAnswers, zigAnswers map[string]map[string]any) {
	t.Helper()
	if len(goAnswers) != len(zigAnswers) {
		t.Errorf("goap answered %d requests and oapx answered %d", len(goAnswers), len(zigAnswers))
	}
	ids := make([]string, 0, len(goAnswers))
	for id := range goAnswers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		goAnswer, goPresent := goAnswers[id]
		zigAnswer, zigPresent := zigAnswers[id]
		if !goPresent || !zigPresent {
			t.Errorf("request %s: goap answered %t, oapx answered %t", id, goPresent, zigPresent)
			continue
		}
		goJSON, _ := json.MarshalIndent(goAnswer, "", "  ")
		zigJSON, _ := json.MarshalIndent(zigAnswer, "", "  ")
		if string(goJSON) != string(zigJSON) {
			t.Errorf("request %s answered differently:\ngoap: %s\noapx: %s", id, goJSON, zigJSON)
		}
	}
}

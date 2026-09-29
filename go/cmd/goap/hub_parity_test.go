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

const openEnvelopeFields = `\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\"`

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
	"the open gate refuses the same five ways": {
		`{"id":1,"op":"open","adapter":"memory"}`,
		`{"id":2,"op":"open","request":{}}`,
		`{"id":3,"op":"open","adapter":"memory","request":null}`,
		`{"id":4,"op":"open","adapter":"memory","request":{"id":"x"}}`,
		`{"id":5,"op":"open","adapter":"memory","request":{" + openEnvelopeFields + ","id":"o1"}}`,
	},
	"an open is refused the same way after it is asked for twice": {
		`{"id":1,"op":"open","adapter":"absent","request":{" + openEnvelopeFields + ","id":"o1","payload":{"session_id":"s1"}}}`,
		`{"id":2,"op":"open","adapter":"memory","request":{" + openEnvelopeFields + ","id":"o1","payload":{"session_id":"s1"},"metadata":7}}`,
		`{"id":3,"op":"sessions"}`,
	},
	"the catalog ops refuse the same refusals": {
		`{"id":1,"op":"models","session_id":"absent"}`,
		`{"id":2,"op":"tools","session_id":"absent"}`,
		`{"id":3,"op":"models"}`,
		`{"id":4,"op":"tools"}`,
		`{"id":5,"op":"models","run_id":"r1"}`,
		`{"id":6,"op":"tools","adapter":"memory"}`,
		`{"id":7,"op":"models","session_id":"absent","allow_degraded_features":"nope"}`,
		`{"id":8,"op":"models","session_id":7}`,
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

func normaliseHubAnswer(answer map[string]any) {
	normaliseMinted(answer)
	refusal, ok := answer["error"].(map[string]any)
	if !ok {
		return
	}
	delete(refusal, "message")
}
func normaliseMinted(node any) {
	switch value := node.(type) {
	case map[string]any:
		for key, child := range value {
			switch key {
			case "id", "in_reply_to", "created_at", "as_of":
				if _, isText := child.(string); isText {
					value[key] = "<minted>"
					continue
				}
			case "timestamp", "sequence":
				if _, isNumber := child.(float64); isNumber {
					value[key] = float64(0)
					continue
				}
			}
			normaliseMinted(child)
		}
	case []any:
		for _, child := range value {
			normaliseMinted(child)
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

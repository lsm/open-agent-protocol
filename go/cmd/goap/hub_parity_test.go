package main

import (
	"bufio"
	"context"
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

const openEnvelopeFields = `"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.open.request",`

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
	"the open gate refuses the same two ways, and the same line is a success": {
		`{"id":1,"op":"open","adapter":"memory"}`,
		`{"id":2,"op":"open","request":{}}`,
		`{"id":3,"op":"open","adapter":"memory","request":null}`,
		`{"id":4,"op":"open","adapter":"memory","request":{"id":"x"}}`,
		`{"id":5,"op":"open","adapter":"memory","request":{` + openEnvelopeFields + `"id":"o5","payload":{"session_id":"s5"}}}`,
	},
	"a reopen of a session the hub never closed is unknown to both": {
		`{"id":1,"op":"open","adapter":"memory","request":{` + openEnvelopeFields + `"id":"o1","payload":{"session_id":"ghost","reopen":true}}}`,
	},
	"an attachment that names something to run is refused by both": {
		`{"id":1,"op":"open","adapter":"memory","request":{` + openEnvelopeFields + `"id":"o1","payload":{"session_id":"s1","tool_sources":[{"id":"l1","kind":"local","command":"/bin/sh"}]}}}`,
		`{"id":2,"op":"open","adapter":"memory","request":{` + openEnvelopeFields + `"id":"o2","payload":{"session_id":"s2","tool_sources":[{"id":"l2","kind":"local","args":["-c"]}]}}}`,
		`{"id":3,"op":"open","adapter":"memory","request":{` + openEnvelopeFields + `"id":"o3","payload":{"session_id":"s3","tool_sources":[{"id":"l3","kind":"local","environment":["PATH=/tmp"]}]}}}`,
		`{"id":4,"op":"open","adapter":"memory","request":{` + openEnvelopeFields + `"id":"o4","payload":{"session_id":"s4","tool_sources":[{"id":"p1","kind":"process"}]}}}`,
		`{"id":5,"op":"sessions"}`,
	},
	"a refused open leaves no session behind": {
		`{"id":1,"op":"open","adapter":"absent","request":{` + openEnvelopeFields + `"id":"o1","payload":{"session_id":"s1"}}}`,
		`{"id":2,"op":"open","adapter":"memory","request":{` + openEnvelopeFields + `"id":"o2","payload":{"session_id":"s1","metadata":7}}}`,
		`{"id":3,"op":"sessions"}`,
	},
	"the catalog ops refuse the same refusals": {
		`{"id":1,"op":"models","session_id":"absent"}`,
		`{"id":2,"op":"tools","session_id":"absent"}`,
		`{"id":3,"op":"models"}`,
		`{"id":4,"op":"tools"}`,
		`{"id":5,"op":"models","run_id":"r1"}`,
		`{"id":6,"op":"tools","adapter":"memory"}`,
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
		`{"id":3,"op":"models","session_id":7}`,
		`{"id":4,"op":"sessions"}`,
	},
	"a parameter of the wrong type is a framing defect, not a refusal": {
		`{"id":1,"op":"adapters"}`,
		`{"id":2,"op":"models","session_id":"absent","allow_degraded_features":"nope"}`,
		`{"id":3,"op":"sessions"}`,
	},
}

func TestHubStdioComparesWhatTheScenariosSend(t *testing.T) {
	for name, lines := range hubParityScenarios {
		stopAt := len(lines) - 1
		if at, stops := hubWireStops[name]; stops {
			stopAt = at
		}
		ids := expectedHubIDs(lines, stopAt)
		if len(ids) == 0 {
			t.Errorf("scenario %q would compare nothing", name)
		}
	}
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
			assertSameHubAnswers(t, name, lines, goAnswers, zigAnswers)
		})
	}
}

func TestHubRefusesAConfigItCannotReadAndNamesTheFile(t *testing.T) {
	oapx := os.Getenv("OAP_OAPX_BIN")
	if oapx == "" {
		t.Skip("set OAP_OAPX_BIN to an oapx binary to check the flags it refuses")
	}
	cases := []struct {
		argument string
		wants    string
	}{
		{argument: "--config=missing.json", wants: "missing.json"},
	}
	for _, each := range cases {
		t.Run(each.argument, func(t *testing.T) {
			command := exec.Command(oapx, "serve", each.argument, "--session-history=")
			command.Stdin = strings.NewReader("")
			output, err := command.CombinedOutput()
			if err == nil {
				t.Errorf("oapx serve %s exited 0; a flag it cannot honour must exit non-zero", each.argument)
			}
			if !strings.Contains(string(output), each.wants) {
				t.Errorf("oapx serve %s did not name %q in its refusal:\n%s", each.argument, each.wants, output)
			}
			if strings.Contains(string(output), "{\"id\"") {
				t.Errorf("oapx serve %s answered a request rather than refusing the flag", each.argument)
			}
		})
	}
}

func TestHubStdioEndsCleanlyWhenTheHostClosesThePipe(t *testing.T) {
	oapx := os.Getenv("OAP_OAPX_BIN")
	if oapx == "" {
		t.Skip("set OAP_OAPX_BIN to an oapx binary to check it ends cleanly")
	}
	command := exec.Command(oapx, "serve", "--stdio", "--session-history=")
	command.Stdin = strings.NewReader("")
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		t.Fatalf("oapx serve --stdio with a host that sent nothing and closed: %v", err)
	}
}

func TestHubAddrBindsLoopbackAndEndsOnAnInterrupt(t *testing.T) {
	oapx := os.Getenv("OAP_OAPX_BIN")
	if oapx == "" {
		t.Skip("set OAP_OAPX_BIN to an oapx binary to check its HTTP daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, oapx, "serve", "--addr=127.0.0.1:0", "--session-history=")
	command.Stdin = strings.NewReader("")
	var stderr strings.Builder
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	bound := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "listening on http://") {
				bound <- strings.TrimPrefix(line, "listening on ")
				return
			}
		}
		bound <- ""
	}()
	var address string
	select {
	case address = <-bound:
	case <-ctx.Done():
		_ = command.Process.Kill()
		t.Fatalf("oapx serve --addr never reported a bound address:\n%s", stderr.String())
	}
	if address == "" {
		_ = command.Process.Kill()
		t.Fatalf("oapx serve --addr bound nothing:\n%s", stderr.String())
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		_ = command.Process.Kill()
		t.Fatalf("oapx serve --addr could not be interrupted: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("oapx serve --addr ended on an interrupt with %v:\n%s", err, stderr.String())
		}
	case <-time.After(30 * time.Second):
		_ = command.Process.Kill()
		t.Fatalf("oapx serve --addr ignored an interrupt:\n%s", stderr.String())
	}
}

func hubCommand(t *testing.T, binary string) *exec.Cmd {
	t.Helper()
	command := exec.Command(binary, "serve", "--stdio", "--session-history=")
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
			case "timestamp", "sequence", "updated_at_ms":
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

var hubWireStops = map[string]int{
	"a framing defect stops the wire, and nothing after it is answered": 0,
	"a parameter of the wrong type is a framing defect, not a refusal":  0,
}

func expectedHubIDs(lines []string, stopAt int) []string {
	seen := map[string]bool{}
	ids := make([]string, 0, len(lines))
	for position, line := range lines {
		if position > stopAt {
			break
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
			break
		}
		key, ok := parsed["id"].(float64)
		if !ok {
			continue
		}
		id := fmt.Sprintf("%d", int64(key))
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

func assertSameHubAnswers(t *testing.T, name string, lines []string, goAnswers, zigAnswers map[string]map[string]any) {
	t.Helper()
	if len(goAnswers) != len(zigAnswers) {
		t.Errorf("goap answered %d requests and oapx answered %d", len(goAnswers), len(zigAnswers))
	}
	stopAt := len(lines) - 1
	if at, stops := hubWireStops[name]; stops {
		stopAt = at
	}
	expected := expectedHubIDs(lines, stopAt)
	ids := make([]string, 0, len(expected))
	for _, id := range expected {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		goAnswer, goPresent := goAnswers[id]
		zigAnswer, zigPresent := zigAnswers[id]
		if !goPresent && !zigPresent {
			t.Errorf("request %s was answered by neither tree, so nothing was compared", id)
			continue
		}
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

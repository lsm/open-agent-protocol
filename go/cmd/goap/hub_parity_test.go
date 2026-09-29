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
			command := exec.Command(oapx, "hub", each.argument)
			command.Stdin = strings.NewReader("")
			output, err := command.CombinedOutput()
			if err == nil {
				t.Errorf("oapx hub %s exited 0; a flag it cannot honour must exit non-zero", each.argument)
			}
			if !strings.Contains(string(output), each.wants) {
				t.Errorf("oapx hub %s did not name %q in its refusal:\n%s", each.argument, each.wants, output)
			}
			if strings.Contains(string(output), "{\"id\"") {
				t.Errorf("oapx hub %s answered a request rather than refusing the flag", each.argument)
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

func TestHubAddrBindsLoopbackAndEndsOnAnInterrupt(t *testing.T) {
	oapx := os.Getenv("OAP_OAPX_BIN")
	if oapx == "" {
		t.Skip("set OAP_OAPX_BIN to an oapx binary to check its HTTP daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, oapx, "hub", "--addr=127.0.0.1:0")
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
		t.Fatalf("oapx hub --addr never reported a bound address:\n%s", stderr.String())
	}
	if address == "" {
		_ = command.Process.Kill()
		t.Fatalf("oapx hub --addr bound nothing:\n%s", stderr.String())
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		_ = command.Process.Kill()
		t.Fatalf("oapx hub --addr could not be interrupted: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("oapx hub --addr ended on an interrupt with %v:\n%s", err, stderr.String())
		}
	case <-time.After(30 * time.Second):
		_ = command.Process.Kill()
		t.Fatalf("oapx hub --addr ignored an interrupt:\n%s", stderr.String())
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

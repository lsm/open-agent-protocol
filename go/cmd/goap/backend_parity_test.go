package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestBackendsMatchOapx(t *testing.T) {
	oapx := os.Getenv("OAP_OAPX_BIN")
	if oapx == "" {
		t.Skip("set OAP_OAPX_BIN to an oapx binary to compare its served backends with goap's")
	}
	if !filepath.IsAbs(oapx) {
		t.Fatalf("OAP_OAPX_BIN must be absolute, got %q", oapx)
	}
	goap := oapBinary(t)
	cases, err := filepath.Glob(filepath.Join("testdata", "parity", "*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range cases {
		backend := filepath.Base(dir)
		t.Run(backend, func(t *testing.T) {
			scenario := readLines(t, filepath.Join(dir, "scenario.jsonl"))
			wantOut, wantChild := exchangeWithChild(t, dir, backend, scenario, goap, "serve", "agent")
			gotOut, gotChild := exchangeWithChild(t, dir, backend, scenario, oapx, "serve", "agent")
			if missing, extra := lineDifference(wantOut, gotOut); len(missing)+len(extra) > 0 {
				t.Errorf("oapx answers differently\n--- only goap\n%s\n--- only oapx\n%s", strings.Join(missing, "\n"), strings.Join(extra, "\n"))
			}
			if wantChild != gotChild {
				t.Errorf("oapx writes the child differently\n--- goap\n%s\n--- oapx\n%s", wantChild, gotChild)
			}
		})
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func exchangeWithChild(t *testing.T, fixture, backend string, scenario []string, binary string, args ...string) ([]string, string) {
	t.Helper()
	work := t.TempDir()
	child, err := os.ReadFile(filepath.Join(fixture, "child.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "child.sh"), child, 0o755); err != nil {
		t.Fatal(err)
	}
	registry, err := os.ReadFile(filepath.Join(fixture, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "registry.json"), []byte(strings.ReplaceAll(string(registry), "@DIR@", work)), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, append(args, "--backend", backend, "--config", filepath.Join(work, "registry.json"))...)
	cmd.Dir = work
	out := settledExchange(t, cmd, scenario)
	written, err := os.ReadFile(filepath.Join(work, "stdin.log"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return out, strings.ReplaceAll(string(written), work, "@DIR@")
}

func settledExchange(t *testing.T, cmd *exec.Cmd, lines []string) []string {
	t.Helper()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	received := make(chan string, 1024)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		for scanner.Scan() {
			received <- scanner.Text()
		}
		close(received)
	}()
	var out []string
	closed := false
	collect := func(until func() bool, idle time.Duration, limit time.Duration) {
		deadline := time.After(limit)
		quiet := time.NewTimer(idle)
		defer quiet.Stop()
		for {
			select {
			case line, ok := <-received:
				if !ok {
					closed = true
					return
				}
				out = append(out, line)
				quiet.Reset(idle)
			case <-quiet.C:
				if until == nil || until() {
					return
				}
				quiet.Reset(idle)
			case <-deadline:
				return
			}
		}
	}
	for _, line := range lines {
		var request struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(line), &request)
		if _, err := io.WriteString(stdin, line+"\n"); err != nil {
			t.Fatal(err)
		}
		answered := func() bool {
			for _, seen := range out {
				if strings.Contains(seen, `"in_reply_to":"`+request.ID+`"`) || strings.Contains(seen, `"control"`) && strings.Contains(seen, `"id":"`+request.ID+`"`) {
					return true
				}
			}
			return false
		}
		collect(answered, 300*time.Millisecond, 10*time.Second)
		if closed {
			break
		}
	}
	stdin.Close()
	if !closed {
		collect(nil, 2*time.Second, 5*time.Second)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	normalized := make([]string, 0, len(out))
	for _, line := range out {
		normalized = append(normalized, normalizedLine(t, line))
	}
	sort.Strings(normalized)
	return normalized
}

func normalizedLine(t *testing.T, line string) string {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(line), &value); err != nil {
		t.Fatalf("not JSON: %q", line)
	}
	encoded, err := json.Marshal(scrubbed(value))
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func scrubbed(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		kept := make(map[string]any, len(typed))
		for key, member := range typed {
			if key == "id" || strings.HasSuffix(key, "_ms") {
				continue
			}
			kept[key] = scrubbed(member)
		}
		return kept
	case []any:
		for index := range typed {
			typed[index] = scrubbed(typed[index])
		}
		return typed
	}
	return value
}

func lineDifference(want, got []string) ([]string, []string) {
	counts := map[string]int{}
	for _, line := range got {
		counts[line]++
	}
	var missing []string
	for _, line := range want {
		if counts[line] > 0 {
			counts[line]--
			continue
		}
		missing = append(missing, line)
	}
	var extra []string
	for _, line := range got {
		if counts[line] > 0 {
			counts[line]--
			extra = append(extra, line)
		}
	}
	return missing, extra
}

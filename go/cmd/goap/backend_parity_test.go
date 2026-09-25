package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
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
	var server *fakeOpenCode
	if child, err := os.ReadFile(filepath.Join(fixture, "child.sh")); err == nil {
		if err := os.WriteFile(filepath.Join(work, "child.sh"), child, 0o755); err != nil {
			t.Fatal(err)
		}
	} else {
		server = startFakeOpenCode(t)
	}
	registry, err := os.ReadFile(filepath.Join(fixture, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	resolved := strings.ReplaceAll(string(registry), "@DIR@", work)
	if server != nil {
		resolved = strings.ReplaceAll(resolved, "@URL@", server.url)
	}
	if err := os.WriteFile(filepath.Join(work, "registry.json"), []byte(resolved), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, append(args, "--backend", backend, "--config", filepath.Join(work, "registry.json"))...)
	cmd.Dir = work
	out := settledExchange(t, cmd, scenario)
	if server != nil {
		return out, server.transcript()
	}
	written, err := os.ReadFile(filepath.Join(work, "stdin.log"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return out, requestEntropy.ReplaceAllString(strings.ReplaceAll(string(written), work, "@DIR@"), "${1}_@ENTROPY@\"")
}

var requestEntropy = regexp.MustCompile(`("request_id":"req_[0-9]+)_[0-9a-f]{8}"`)

const fakeOpenCodeSession = "ses_fake00000000000000"

type fakeOpenCode struct {
	url    string
	mu     sync.Mutex
	posts  []string
	gets   map[string]bool
	seq    int
	stream chan string
}

func startFakeOpenCode(t *testing.T) *fakeOpenCode {
	t.Helper()
	fake := &fakeOpenCode{gets: map[string]bool{}, stream: make(chan string, 64)}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)
	fake.url = server.URL
	return fake
}

func (f *fakeOpenCode) transcript() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	gets := make([]string, 0, len(f.gets))
	for path := range f.gets {
		gets = append(gets, path)
	}
	sort.Strings(gets)
	return strings.Join(f.posts, "\n") + "\n--- GET\n" + strings.Join(gets, "\n")
}

func (f *fakeOpenCode) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	path := r.URL.RequestURI()
	f.mu.Lock()
	if r.Method == http.MethodGet {
		f.gets[path] = true
	} else {
		f.posts = append(f.posts, r.Method+" "+path+" "+string(body))
	}
	f.mu.Unlock()
	switch {
	case strings.HasPrefix(path, "/api/session/"+fakeOpenCodeSession+"/event"):
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		flusher.Flush()
		for {
			select {
			case line := <-f.stream:
				fmt.Fprintf(w, "data: %s\n\n", line)
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	case r.Method == http.MethodPost && path == "/api/session":
		writeJSON(w, `{"data":{"id":"`+fakeOpenCodeSession+`","projectID":"prj_fake","model":{"id":"fixture","providerID":"fixture"},"time":{"created":1,"updated":1}}}`)
	case strings.HasSuffix(path, "/prompt"):
		var prompt struct {
			ID       string `json:"id"`
			Delivery string `json:"delivery"`
			Prompt   struct {
				Text string `json:"text"`
			} `json:"prompt"`
		}
		_ = json.Unmarshal(body, &prompt)
		f.mu.Lock()
		turn := f.seq
		f.mu.Unlock()
		writeJSON(w, fmt.Sprintf(`{"data":{"admittedSeq":1,"id":%q,"sessionID":"%s","prompt":{"text":%q},"delivery":%q,"timeCreated":1,"promotedSeq":%d}}`, prompt.ID, fakeOpenCodeSession, prompt.Prompt.Text, prompt.Delivery, turn+1))
		for _, event := range []string{
			`"prompted",` + `"data":{"timestamp":%SEQ%,"sessionID":"` + fakeOpenCodeSession + `","messageID":"%MSG%","prompt":{"text":"hello"},"delivery":"steer"}`,
			`"step.started",` + `"data":{"timestamp":%SEQ%,"sessionID":"` + fakeOpenCodeSession + `","assistantMessageID":"msg_a1","agent":"build","model":{"id":"fixture","providerID":"fixture"}}`,
			`"text.ended",` + `"data":{"timestamp":%SEQ%,"sessionID":"` + fakeOpenCodeSession + `","assistantMessageID":"msg_a1","textID":"t1","text":"done"}`,
			`"step.ended",` + `"data":{"timestamp":%SEQ%,"sessionID":"` + fakeOpenCodeSession + `","assistantMessageID":"msg_a1","finish":"stop","cost":0,"tokens":{"input":2,"output":5,"reasoning":0,"cache":{"read":0,"write":0}}}`,
		} {
			f.mu.Lock()
			f.seq++
			seq := fmt.Sprint(f.seq)
			f.mu.Unlock()
			kind, data, _ := strings.Cut(event, ",")
			line := `{"id":"evt_` + seq + `","type":"session.next.` + strings.Trim(kind, `"`) + `","durable":{"aggregateID":"` + fakeOpenCodeSession + `","seq":` + seq + `,"version":1},` + data + `}`
			line = strings.ReplaceAll(strings.ReplaceAll(line, "%SEQ%", seq), "%MSG%", prompt.ID)
			f.stream <- line
		}
	case strings.HasSuffix(path, "/interrupt"):
		w.WriteHeader(http.StatusNoContent)
	case path == "/api/session/active":
		writeJSON(w, `{"data":{}}`)
	case strings.Contains(path, "/history"):
		writeJSON(w, `{"data":[],"hasMore":false}`)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "{}")
	}
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
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

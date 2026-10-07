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
	"strconv"
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
		backend := fixtureBackend(t, dir)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			scenario := readLines(t, filepath.Join(dir, "scenario.jsonl"))
			wantOut, wantChild := exchangeWithChild(t, dir, backend, scenario, goap, "serve", "agent")
			gotOut, gotChild := exchangeWithChild(t, dir, backend, scenario, oapx, "serve", "agent")
			if len(wantOut) < len(scenario) {
				t.Fatalf("goap wrote %d lines for %d requests; the fixture does not start", len(wantOut), len(scenario))
			}
			if missing, extra := lineDifference(wantOut, gotOut); len(missing)+len(extra) > 0 {
				t.Errorf("oapx answers differently\n--- only goap\n%s\n--- only oapx\n%s", strings.Join(missing, "\n"), strings.Join(extra, "\n"))
			}
			if where := runOrderDifference(t, wantOut, gotOut); where != "" {
				t.Errorf("oapx orders a run differently: %s", where)
			}
			if missing, extra := childLineDifference(t, wantChild, gotChild); len(missing)+len(extra) > 0 {
				t.Errorf("oapx writes different data to its child\n--- only goap\n%s\n--- only oapx\n%s", strings.Join(missing, "\n"), strings.Join(extra, "\n"))
			}
		})
	}
}

func fixtureBackend(t *testing.T, dir string) string {
	t.Helper()
	held, err := os.ReadFile(filepath.Join(dir, "backend"))
	if err != nil {
		if os.IsNotExist(err) {
			return filepath.Base(dir)
		}
		t.Fatalf("read the backend of %s: %v", dir, err)
	}
	backend := strings.TrimSpace(string(held))
	if backend == "" {
		t.Fatalf("%s/backend is empty: it names the backend the fixture serves", dir)
	}
	return backend
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
	child, childErr := os.ReadFile(filepath.Join(fixture, "child.sh"))
	registry, err := os.ReadFile(filepath.Join(fixture, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case childErr == nil:
		if err := os.WriteFile(filepath.Join(work, "child.sh"), child, 0o755); err != nil {
			t.Fatal(err)
		}
	case strings.Contains(string(registry), "@URL@"):
		server = startFakeOpenCode(t)
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
	return out, turnEntropy.ReplaceAllString(requestEntropy.ReplaceAllString(strings.ReplaceAll(string(written), work, "@DIR@"), "${1}_@ENTROPY@"), "turn-@ENTROPY@-${1}")
}

var requestEntropy = regexp.MustCompile(`\b(req_[0-9]+)_[0-9a-f]{8}\b`)

var turnEntropy = regexp.MustCompile(`\bturn-[0-9a-f]{16}-([0-9]+)\b`)

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
	case path == "/api/event":
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"id\":\"evt_connected\",\"type\":\"server.connected\",\"data\":{}}\n\n")
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
			Text     string `json:"text"`
		}
		_ = json.Unmarshal(body, &prompt)
		writeJSON(w, fmt.Sprintf(`{"data":{"id":%q,"sessionID":"%s","time":{"created":1},"type":"user","payload":{"text":%q},"delivery":%q}}`, prompt.ID, fakeOpenCodeSession, prompt.Text, prompt.Delivery))
		for _, event := range []string{
			`inbox.delivered,"inboxID":"%MSG%"`,
			`step.started,"assistantMessageID":"msg_a1","agent":"build","model":{"id":"fixture","providerID":"fixture"},"started":1`,
			`text.ended,"assistantMessageID":"msg_a1","ordinal":0,"text":"done"`,
			`step.ended,"assistantMessageID":"msg_a1","finish":"stop","cost":0,"tokens":{"input":2,"output":5,"reasoning":0,"cache":{"read":0,"write":0}}`,
			`execution.succeeded,`,
		} {
			f.mu.Lock()
			f.seq++
			seq := fmt.Sprint(f.seq)
			f.mu.Unlock()
			kind, data, _ := strings.Cut(event, ",")
			if data != "" {
				data = "," + data
			}
			line := `{"id":"evt_` + seq + `","created":` + seq + `,"type":"session.` + kind + `","durable":{"aggregateID":"` + fakeOpenCodeSession + `","seq":` + seq + `,"version":1},"data":{"sessionID":"` + fakeOpenCodeSession + `"` + data + `}}`
			f.stream <- strings.ReplaceAll(line, "%MSG%", prompt.ID)
		}
	case strings.HasSuffix(path, "/interrupt"):
		writeJSON(w, `{"interrupted":false}`)
	case path == "/api/session/active":
		writeJSON(w, `{"data":{}}`)
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
	for _, line := range normalizeSelectedNativeBindings(t, out) {
		normalized = append(normalized, normalizedLine(t, requestEntropy.ReplaceAllString(line, "${1}_@ENTROPY@")))
	}
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

func TestAFixtureMayNameTheBackendItServes(t *testing.T) {
	dir := t.TempDir()
	if got, want := fixtureBackend(t, dir), filepath.Base(dir); got != want {
		t.Errorf("a fixture with no backend file is served by its directory: got %q, want %q", got, want)
	}
	if err := os.WriteFile(filepath.Join(dir, "backend"), []byte("pi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := fixtureBackend(t, dir); got != "pi" {
		t.Errorf("a fixture may name a backend other than its directory: got %q, want %q", got, "pi")
	}
}

func TestTwoFixtureDirectoryNamesOneBackend(t *testing.T) {
	dir := filepath.Join("testdata", "parity", "pi-two-open-calls")
	held, err := os.ReadFile(filepath.Join(dir, "backend"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(held)), "pi"; got != want {
		t.Fatalf("the fixture names the backend it serves: got %q, want %q", got, want)
	}
	if got := fixtureBackend(t, dir); got == filepath.Base(dir) {
		t.Fatalf("the fixture's directory and the backend it serves differ, so the name comes from the file: both are %q", got)
	}
	registry, err := os.ReadFile(filepath.Join(dir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(registry), `"pi"`) {
		t.Fatalf("the fixture's registry holds the backend it names: %s", registry)
	}
}

func TestTheTwoOpenCallsFixtureLeavesBothOpenAtSettlement(t *testing.T) {
	child, err := os.ReadFile(filepath.Join("testdata", "parity", "pi-two-open-calls", "child.sh"))
	if err != nil {
		t.Fatal(err)
	}
	started := strings.Count(string(child), `"type":"tool_execution_start"`)
	ended := strings.Count(string(child), `"type":"tool_execution_end"`)
	if started != 2 || ended != 0 {
		t.Errorf("the fixture starts %d calls and ends %d; the contested settlement needs two calls open when the run settles", started, ended)
	}
}

func TestChildLinesCompareDataNotBytes(t *testing.T) {
	escaped := "{\"id\":\"req_1\",\"params\":{\"text\":\"a<b>c&d\u2028e\"}}"
	plain := "{\"params\":{\"text\":\"a\\u003cb\\u003ec\\u0026d\u2028e\"},\"id\":\"req_1\"}"
	if escaped == plain {
		t.Fatal("the two inputs are the same bytes, so the comparison is not exercised")
	}
	if len(escaped) == len(plain) {
		t.Fatalf("the two inputs are the same length (%d), so no escaping is being compared", len(escaped))
	}
	missing, extra := childLineDifference(t, escaped+"\n", plain+"\n")
	if len(missing)+len(extra) != 0 {
		t.Fatalf("the same data in different bytes compared unequal: only goap %v, only oapx %v\ngoap %q\noapx %q", missing, extra, escaped, plain)
	}
	changed := "{\"id\":\"req_1\",\"params\":{\"text\":\"a<b>c&d\u2028f\"}}"
	missing, extra = childLineDifference(t, escaped+"\n", changed+"\n")
	if len(missing) != 1 || len(extra) != 1 {
		t.Fatalf("different data compared equal: only goap %v, only oapx %v", missing, extra)
	}
	if _, extra := childLineDifference(t, "--- GET\n", "--- GET\n/api/session/ses_1/event\n"); len(extra) != 1 {
		t.Fatalf("a request the other tree never made compared equal: %v", extra)
	}
	withID := "{\"id\":\"msg_oap0000000000000001\",\"params\":{\"text\":\"a<b>c&d\"}}"
	otherID := "{\"id\":\"msg_oap0000000000000002\",\"params\":{\"text\":\"a<b>c&d\"}}"
	if missing, extra := childLineDifference(t, withID+"\n", otherID+"\n"); len(missing) != 1 || len(extra) != 1 {
		t.Fatalf("two trees minting different ids compared equal: only goap %v, only oapx %v", missing, extra)
	}
}

func childLineDifference(t *testing.T, want, got string) ([]string, []string) {
	t.Helper()
	return lineDifference(childLines(t, want), childLines(t, got))
}

func childLines(t *testing.T, text string) []string {
	t.Helper()
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		lines = append(lines, parsedChildLine(t, line))
	}
	return lines
}

func parsedChildLine(t *testing.T, line string) string {
	t.Helper()
	at := strings.IndexByte(line, '{')
	if at < 0 {
		return line
	}
	prefix, body := line[:at], line[at:]
	if !json.Valid([]byte(body)) {
		return line
	}
	return prefix + canonicalJSON(t, body)
}

func canonicalJSON(t *testing.T, line string) string {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(line), &value); err != nil {
		t.Fatalf("not JSON: %q", line)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)

}

func TestRunOrderDifferenceSeesOrderWithinARun(t *testing.T) {
	lines := func(specs ...string) []string {
		var out []string
		for i, spec := range specs {
			out = append(out, `{"protocol":"open-agent-protocol","run_id":"run-1","sequence":`+strconv.Itoa(i+1)+`,"payload":{"run_id":"run-1","status":"`+spec+`"}}`)
		}
		return out
	}
	unsequenced := []string{`{"protocol":"open-agent-protocol","run_id":"run-1","type":"run.cancel.response","payload":{"run_id":"run-1","accepted":true}}`}
	if where := runOrderDifference(t, lines("queued", "running", "completed"), lines("queued", "running", "completed")); where != "" {
		t.Fatalf("the same order compared unequal: %s", where)
	}
	reordered := lines("queued", "running", "completed")
	reordered[1], reordered[2] = reordered[2], reordered[1]
	where := runOrderDifference(t, lines("queued", "running", "completed"), reordered)
	if where == "" {
		t.Fatal("a run whose envelopes arrived in a different order compared equal")
	}
	if !strings.Contains(where, "run run-1 at line 2") {
		t.Fatalf("the disagreement is not located: %s", where)
	}
	if where := runOrderDifference(t, lines("queued", "running", "completed"), lines("queued", "running")); where == "" {
		t.Fatal("a run that stopped early compared equal")
	}
	shortened := lines("queued", "running")
	if where := runOrderDifference(t, shortened, lines("queued", "running", "completed")); where == "" {
		t.Fatal("a run that answered more compared equal")
	}
	unrun := []string{`{"protocol":"open-agent-protocol","payload":{"session_id":"session"}}`}
	if where := runOrderDifference(t, unrun, unrun); where != "" {
		t.Fatalf("output with no run compared unequal: %s", where)
	}
	withResponse := append(append([]string{}, unsequenced...), lines("queued", "running")...)
	if where := runOrderDifference(t, withResponse, lines("queued", "running")); where != "" {
		t.Fatalf("a response with no sequence compared unequal against a stream without one: %s", where)
	}
}

func runOrderDifference(t *testing.T, want, got []string) string {
	t.Helper()
	wantRuns, gotRuns := runGroups(t, want), runGroups(t, got)
	ids := make([]string, 0, len(wantRuns)+len(gotRuns))
	for id := range wantRuns {
		ids = append(ids, id)
	}
	for id := range gotRuns {
		if _, seen := wantRuns[id]; !seen {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		where, onlyGoap, onlyOapx := orderedDifference(wantRuns[id], gotRuns[id])
		if where != "" {
			return fmt.Sprintf("run %s at line %s: goap %s, oapx %s", id, where, onlyGoap, onlyOapx)
		}
	}
	return ""
}

func runGroups(t *testing.T, lines []string) map[string][]string {
	t.Helper()
	groups := map[string][]string{}
	for _, line := range lines {
		var envelope struct {
			RunID    string  `json:"run_id"`
			Sequence *uint64 `json:"sequence"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("not JSON: %q", line)
		}
		if envelope.RunID == "" || envelope.Sequence == nil {
			continue
		}
		groups[envelope.RunID] = append(groups[envelope.RunID], normalizedLine(t, line))
	}
	return groups
}

func orderedDifference(want, got []string) (string, string, string) {
	for i := 0; i < len(want) || i < len(got); i++ {
		where := strconv.Itoa(i + 1)
		switch {
		case i >= len(got):
			return where, want[i], "<nothing>"
		case i >= len(want):
			return where, "<nothing>", got[i]
		case want[i] != got[i]:
			return where, want[i], got[i]
		}
	}
	return "", "", ""
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

var selectedNativeUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func normalizeSelectedNativeBindings(t *testing.T, lines []string) []string {
	t.Helper()
	frames := make([]map[string]any, 0, len(lines))
	aliases := map[string]string{}
	for _, line := range lines {
		var frame map[string]any
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, frame)
		if frame["type"] != "session.open.response" {
			continue
		}
		payload, _ := frame["payload"].(map[string]any)
		if payload["status"] != "idle" {
			continue
		}
		recovery, _ := payload["recovery"].(map[string]any)
		if recovery["recovered"] == true {
			continue
		}
		metadata, _ := payload["metadata"].(map[string]any)
		id, _ := metadata["claude_native_session_id"].(string)
		if selectedNativeUUID.MatchString(id) && aliases[id] == "" {
			aliases[id] = fmt.Sprintf("@selected-claude-session-%d@", len(aliases)+1)
		}
	}
	normalized := make([]string, 0, len(frames))
	for _, frame := range frames {
		payload, _ := frame["payload"].(map[string]any)
		metadata, _ := payload["metadata"].(map[string]any)
		id, _ := metadata["claude_native_session_id"].(string)
		if alias := aliases[id]; alias != "" {
			metadata["claude_native_session_id"] = alias
		}
		encoded, err := json.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		normalized = append(normalized, string(encoded))
	}
	return normalized
}

func TestNativeBindingEntropyNormalizationStillDetectsAChangedSession(t *testing.T) {
	trace := func(opened, later string) []string {
		return []string{
			`{"type":"session.open.response","payload":{"status":"idle","metadata":{"claude_native_session_id":"` + opened + `"}}}`,
			`{"type":"session.state.response","payload":{"status":"idle","metadata":{"claude_native_session_id":"` + later + `"}}}`,
		}
	}
	a := "9d992266-63b1-4a69-8000-3aaf8b854e5c"
	b := "24c7cf93-b337-48d5-8c34-d428857d43cc"
	c := "f312402f-b05e-4cbf-95e4-09fd6a950e6a"
	first := normalizeSelectedNativeBindings(t, trace(a, a))
	second := normalizeSelectedNativeBindings(t, trace(b, b))
	if missing, extra := lineDifference(first, second); len(missing)+len(extra) != 0 {
		t.Fatal("independent native UUID entropy must compare equally")
	}
	changed := normalizeSelectedNativeBindings(t, trace(b, c))
	if missing, extra := lineDifference(first, changed); len(missing)+len(extra) == 0 {
		t.Fatal("a native session changed after open without a parity difference")
	}
}

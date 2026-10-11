package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const benchModel = "claude-sonnet-4-5"

var benchTools = []string{"Read", "Grep", "Glob"}

type benchHit struct {
	main bool
	at   time.Time
	done time.Time
}

type benchMock struct {
	server     *httptest.Server
	firstByte  time.Duration
	chunkDelay time.Duration
	chunks     int
	toolTurns  int

	mu        sync.Mutex
	workspace string
	hits      []benchHit
}

func newBenchMock(t *testing.T, firstByte, chunkDelay time.Duration, chunks, toolTurns int) *benchMock {
	t.Helper()
	mock := &benchMock{firstByte: firstByte, chunkDelay: chunkDelay, chunks: max(chunks, 1), toolTurns: toolTurns}
	mock.server = httptest.NewServer(http.HandlerFunc(mock.serve))
	t.Cleanup(mock.server.Close)
	return mock
}

func (m *benchMock) reset(workspace string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workspace = workspace
	m.hits = nil
}

func (m *benchMock) taken() []benchHit {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]benchHit(nil), m.hits...)
}

type benchRequest struct {
	Model    string            `json:"model"`
	Stream   bool              `json:"stream"`
	Tools    []json.RawMessage `json:"tools"`
	Messages []struct {
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
}

func (m *benchMock) serve(writer http.ResponseWriter, request *http.Request) {
	at := time.Now()
	body, _ := io.ReadAll(request.Body)
	if strings.HasSuffix(request.URL.Path, "/count_tokens") {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"input_tokens":1}`)
		return
	}
	if !strings.HasSuffix(request.URL.Path, "/v1/messages") {
		http.NotFound(writer, request)
		return
	}
	var decoded benchRequest
	_ = json.Unmarshal(body, &decoded)
	main := benchOffersRead(decoded.Tools)
	turn := benchToolResults(decoded)
	m.mu.Lock()
	workspace := m.workspace
	m.mu.Unlock()

	var blocks []benchBlock
	stop := "end_turn"
	switch {
	case !main:
		blocks = []benchBlock{{text: "ok"}}
	case turn < m.toolTurns:
		blocks = []benchBlock{{text: benchNarration(turn)}, benchToolUse(turn, workspace)}
		stop = "tool_use"
	default:
		blocks = []benchBlock{{text: `{"verdict":"clean","summary":"scripted review complete","findings":[]}`}}
	}
	time.Sleep(m.firstByte)
	if decoded.Stream {
		m.stream(writer, decoded.Model, blocks, stop)
	} else {
		m.whole(writer, decoded.Model, blocks, stop)
	}
	m.mu.Lock()
	m.hits = append(m.hits, benchHit{main: main, at: at, done: time.Now()})
	m.mu.Unlock()
}

type benchBlock struct {
	text  string
	id    string
	name  string
	input string
}

func benchToolUse(turn int, workspace string) benchBlock {
	id := "toolu_bench_" + strconv.Itoa(turn)
	switch turn % 3 {
	case 0:
		return benchBlock{id: id, name: "Glob", input: `{"pattern":"**/*.go"}`}
	case 1:
		return benchBlock{id: id, name: "Grep", input: `{"pattern":"func","path":` + strconv.Quote(workspace) + `}`}
	default:
		return benchBlock{id: id, name: "Read", input: `{"file_path":` + strconv.Quote(filepath.Join(workspace, "main.go")) + `}`}
	}
}

func benchOffersRead(tools []json.RawMessage) bool {
	for _, raw := range tools {
		var tool struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &tool) == nil && tool.Name == "Read" {
			return true
		}
	}
	return false
}

func benchToolResults(request benchRequest) int {
	count := 0
	for _, message := range request.Messages {
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(message.Content, &parts) != nil {
			continue
		}
		for _, part := range parts {
			if part.Type == "tool_result" {
				count++
			}
		}
	}
	return count
}

func (m *benchMock) stream(writer http.ResponseWriter, model string, blocks []benchBlock, stop string) {
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	flusher, _ := writer.(http.Flusher)
	send := func(event, data string) {
		_, _ = io.WriteString(writer, "event: "+event+"\ndata: "+data+"\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}
	send("message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_bench","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":100,"output_tokens":1}}}`, model))
	for index, block := range blocks {
		if block.name != "" {
			send("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":%q,"name":%q,"input":{}}}`, index, block.id, block.name))
			send("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":%s}}`, index, strconv.Quote(block.input)))
		} else {
			send("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, index))
			for _, chunk := range benchChunks(block.text, m.chunks) {
				time.Sleep(m.chunkDelay)
				send("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":%s}}`, index, strconv.Quote(chunk)))
			}
		}
		send("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, index))
	}
	send("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q,"stop_sequence":null},"usage":{"output_tokens":10}}`, stop))
	send("message_stop", `{"type":"message_stop"}`)
}

func (m *benchMock) whole(writer http.ResponseWriter, model string, blocks []benchBlock, stop string) {
	content := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.name != "" {
			content = append(content, fmt.Sprintf(`{"type":"tool_use","id":%q,"name":%q,"input":%s}`, block.id, block.name, block.input))
		} else {
			content = append(content, fmt.Sprintf(`{"type":"text","text":%s}`, strconv.Quote(block.text)))
		}
	}
	writer.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(writer, `{"id":"msg_bench","type":"message","role":"assistant","model":%q,"content":[%s],"stop_reason":%q,"stop_sequence":null,"usage":{"input_tokens":100,"output_tokens":10}}`, model, strings.Join(content, ","), stop)
}

func benchNarration(turn int) string {
	return strings.Repeat(fmt.Sprintf("Turn %d: checking the next part of the change before calling a tool. ", turn), 4)
}

func benchChunks(text string, parts int) []string {
	size := (len(text) + parts - 1) / parts
	var chunks []string
	for start := 0; start < len(text); start += size {
		chunks = append(chunks, text[start:min(start+size, len(text))])
	}
	return chunks
}

type benchSample struct {
	Total     time.Duration
	Startup   time.Duration
	Open      time.Duration
	FirstEnv  time.Duration
	Model     time.Duration
	ToolGaps  time.Duration
	Tail      time.Duration
	Close     time.Duration
	MainTurns int
	SideCalls int
}

func benchPhases(sample *benchSample, start, end time.Time, hits []benchHit) {
	var main []benchHit
	for _, hit := range hits {
		if hit.main {
			main = append(main, hit)
		} else {
			sample.SideCalls++
		}
	}
	sample.MainTurns = len(main)
	sample.Total = end.Sub(start)
	if len(main) == 0 {
		return
	}
	sample.Startup = main[0].at.Sub(start)
	for index, hit := range main {
		sample.Model += hit.done.Sub(hit.at)
		if index > 0 {
			sample.ToolGaps += hit.at.Sub(main[index-1].done)
		}
	}
	sample.Tail = end.Sub(main[len(main)-1].done)
}

func benchWorkspace(t *testing.T, root string, iteration int, path string) string {
	t.Helper()
	workspace := filepath.Join(root, fmt.Sprintf("%s-%d", path, iteration))
	if err := os.MkdirAll(filepath.Join(workspace, "pkg"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"main.go":       "package main\n\nfunc main() {\n\trun()\n}\n",
		"run.go":        "package main\n\nfunc run() {}\n",
		"pkg/helper.go": "package pkg\n\nfunc Helper() int { return 1 }\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return workspace
}

func benchFlags() []string {
	return []string{"--system-prompt", "", "--setting-sources=", "--model", benchModel, "--tools", strings.Join(benchTools, ","), "--allowedTools", "Read", "Grep", "Glob", "--no-session-persistence"}
}

const benchPrompt = "Review the repository and answer with the JSON verdict."

func benchNative(t *testing.T, binary string, mock *benchMock, environment []string, workspace string) benchSample {
	t.Helper()
	mock.reset(workspace)
	command := exec.Command(binary, append([]string{"-p", "--output-format", "json"}, benchFlags()...)...)
	command.Dir = workspace
	command.Env = environment
	command.Stdin = strings.NewReader(benchPrompt)
	start := time.Now()
	output, err := command.Output()
	end := time.Now()
	if err != nil {
		t.Fatalf("native claude: %v: %s", err, output)
	}
	if !strings.Contains(string(output), `scripted review complete`) {
		t.Fatalf("native claude ended without the scripted verdict: %s", output)
	}
	var sample benchSample
	benchPhases(&sample, start, end, mock.taken())
	return sample
}

func benchOAP(t *testing.T, binary string, mock *benchMock, environment []string, workspace string) benchSample {
	t.Helper()
	mock.reset(workspace)
	start := time.Now()
	implementation, err := New(Config{
		Executable: binary, Environment: environment, WorkingDirectory: workspace,
		Model: benchModel, Tools: AllowTools(benchTools...), Args: []string{"--no-session-persistence"},
		ExitTimeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	session, err := implementation.Open(ctx, base.OpenRequest{SessionID: "bench", Participant: protocol.Participant{ID: "bench-host"}})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	opened := time.Now()
	admission, stream, err := session.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{
		SessionID: "bench", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent(benchPrompt)}},
	}})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	submitted := time.Now()
	var first, end time.Time
	var terminal protocol.Envelope
	for result := range stream {
		if result.Error != nil {
			t.Fatalf("stream: %v", result.Error)
		}
		if first.IsZero() {
			first = time.Now()
		}
		switch result.Envelope.Type {
		case protocol.TypeRunCompleted, protocol.TypeRunFailed, protocol.TypeRunCancelled:
			end = time.Now()
			terminal = result.Envelope
		}
	}
	if terminal.Type != protocol.TypeRunCompleted || admission.RunID == "" {
		t.Fatalf("oap run ended %s: %s", terminal.Type, terminal.Payload)
	}
	closeStart := time.Now()
	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	var sample benchSample
	benchPhases(&sample, start, end, mock.taken())
	sample.Open = opened.Sub(start)
	sample.FirstEnv = first.Sub(submitted)
	sample.Close = time.Since(closeStart)
	return sample
}

func benchEnvInt(name string, fallback int) int {
	if value, err := strconv.Atoi(os.Getenv(name)); err == nil && value >= 0 {
		return value
	}
	return fallback
}

func benchPercentile(values []time.Duration, quantile float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := int(quantile*float64(len(sorted)-1) + 0.5)
	return sorted[index]
}

type benchRow struct {
	Phase  string   `json:"phase"`
	Native [2]int64 `json:"native_p50_p95_us"`
	OAP    [2]int64 `json:"oap_p50_p95_us"`
}

func TestClaudeOAPOverheadAgainstScriptedMessages(t *testing.T) {
	if os.Getenv("OAP_CLAUDE_BENCH") != "1" {
		t.Skip("set OAP_CLAUDE_BENCH=1 with absolute OAP_CLAUDE_BIN to measure OAP's overhead over native claude -p against a scripted Messages mock; OAP_CLAUDE_BENCH_N, _TOOL_TURNS, _FIRST_BYTE_MS, _CHUNKS, _CHUNK_MS and _OUT tune and record it")
	}
	binary := verifiedClaudeCLI(t)
	iterations := benchEnvInt("OAP_CLAUDE_BENCH_N", 10)
	toolTurns := benchEnvInt("OAP_CLAUDE_BENCH_TOOL_TURNS", 6)
	firstByte := time.Duration(benchEnvInt("OAP_CLAUDE_BENCH_FIRST_BYTE_MS", 100)) * time.Millisecond
	chunk := time.Duration(benchEnvInt("OAP_CLAUDE_BENCH_CHUNK_MS", 5)) * time.Millisecond
	chunks := benchEnvInt("OAP_CLAUDE_BENCH_CHUNKS", 40)
	mock := newBenchMock(t, firstByte, chunk, chunks, toolTurns)
	root := t.TempDir()
	environment := claudeEnvironment(t, root, mock.server.URL)

	benchNative(t, binary, mock, environment, benchWorkspace(t, root, -1, "native"))
	benchOAP(t, binary, mock, environment, benchWorkspace(t, root, -1, "oap"))

	var native, oap []benchSample
	for iteration := 0; iteration < iterations; iteration++ {
		if iteration%2 == 0 {
			native = append(native, benchNative(t, binary, mock, environment, benchWorkspace(t, root, iteration, "native")))
			oap = append(oap, benchOAP(t, binary, mock, environment, benchWorkspace(t, root, iteration, "oap")))
		} else {
			oap = append(oap, benchOAP(t, binary, mock, environment, benchWorkspace(t, root, iteration, "oap")))
			native = append(native, benchNative(t, binary, mock, environment, benchWorkspace(t, root, iteration, "native")))
		}
	}
	for index := range native {
		if native[index].MainTurns != toolTurns+1 || oap[index].MainTurns != toolTurns+1 {
			t.Fatalf("iteration %d made %d native and %d oap model turns, want %d each", index, native[index].MainTurns, oap[index].MainTurns, toolTurns+1)
		}
	}

	phases := []struct {
		name string
		of   func(benchSample) time.Duration
	}{
		{"total (start to result)", func(s benchSample) time.Duration { return s.Total }},
		{"startup (start to first model request)", func(s benchSample) time.Duration { return s.Startup }},
		{"  oap open (spawn + initialize)", func(s benchSample) time.Duration { return s.Open }},
		{"  oap submit to first envelope", func(s benchSample) time.Duration { return s.FirstEnv }},
		{"model time (mock, summed)", func(s benchSample) time.Duration { return s.Model }},
		{"tool gaps (response end to next request, summed)", func(s benchSample) time.Duration { return s.ToolGaps }},
		{"tail (last response to result)", func(s benchSample) time.Duration { return s.Tail }},
		{"  oap close", func(s benchSample) time.Duration { return s.Close }},
	}
	var rows []benchRow
	var report strings.Builder
	fmt.Fprintf(&report, "\nclaude %s, %d iterations, %d tool turns, first byte %s, %d text deltas per block every %s\n", benchModel, iterations, toolTurns, firstByte, chunks, chunk)
	fmt.Fprintf(&report, "%-52s %21s %21s %10s\n", "phase", "native p50 / p95", "oap p50 / p95", "Δp50")
	for _, phase := range phases {
		var nv, ov []time.Duration
		for index := range native {
			nv = append(nv, phase.of(native[index]))
			ov = append(ov, phase.of(oap[index]))
		}
		row := benchRow{Phase: strings.TrimSpace(phase.name),
			Native: [2]int64{benchPercentile(nv, 0.5).Microseconds(), benchPercentile(nv, 0.95).Microseconds()},
			OAP:    [2]int64{benchPercentile(ov, 0.5).Microseconds(), benchPercentile(ov, 0.95).Microseconds()}}
		rows = append(rows, row)
		fmt.Fprintf(&report, "%-52s %9s / %9s %9s / %9s %10s\n", phase.name,
			benchMS(row.Native[0]), benchMS(row.Native[1]), benchMS(row.OAP[0]), benchMS(row.OAP[1]), benchMS(row.OAP[0]-row.Native[0]))
	}
	fmt.Fprintf(&report, "side calls per run (requests without the Read tool): native %d, oap %d\n", native[0].SideCalls, oap[0].SideCalls)
	t.Log(report.String())
	if out := os.Getenv("OAP_CLAUDE_BENCH_OUT"); out != "" {
		encoded, _ := json.MarshalIndent(map[string]any{"iterations": iterations, "tool_turns": toolTurns, "first_byte_ms": firstByte.Milliseconds(), "chunk_ms": chunk.Milliseconds(), "chunks": chunks, "phases": rows}, "", "  ")
		if err := os.WriteFile(out, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func benchMS(us int64) string {
	return strconv.FormatFloat(float64(us)/1000, 'f', 1, 64) + "ms"
}

package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/claude/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/internal/providertest"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type captureLine struct {
	Direction string          `json:"direction"`
	AtMS      int64           `json:"at_ms"`
	Raw       json.RawMessage `json:"raw,omitempty"`
	Text      string          `json:"text,omitempty"`
}

type captureTranscript struct {
	mu      sync.Mutex
	start   time.Time
	last    time.Time
	lines   []captureLine
	pending map[string][]byte
}

func newCaptureTranscript() *captureTranscript {
	now := time.Now()
	return &captureTranscript{start: now, last: now, pending: map[string][]byte{}}
}

type captureWriter struct {
	transcript *captureTranscript
	direction  string
}

func (w captureWriter) Write(data []byte) (int, error) {
	w.transcript.append(w.direction, data)
	return len(data), nil
}

func (c *captureTranscript) append(direction string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	buffer := append(c.pending[direction], data...)
	for {
		index := bytes.IndexByte(buffer, '\n')
		if index < 0 {
			break
		}
		c.recordLocked(direction, buffer[:index])
		buffer = buffer[index+1:]
	}
	c.pending[direction] = append([]byte(nil), buffer...)
	c.last = time.Now()
}

func (c *captureTranscript) recordLocked(direction string, line []byte) {
	entry := captureLine{Direction: direction, AtMS: time.Since(c.start).Milliseconds()}
	if json.Valid(line) {
		entry.Raw = append(json.RawMessage(nil), line...)
	} else {
		entry.Text = string(line)
	}
	c.lines = append(c.lines, entry)
}

func (c *captureTranscript) note(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, captureLine{Direction: "note", AtMS: time.Since(c.start).Milliseconds(), Text: text})
}

func (c *captureTranscript) quietFor() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Since(c.last)
}

func (c *captureTranscript) results() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for _, line := range c.lines {
		var frame struct {
			Type string `json:"type"`
		}
		if line.Direction == "cli-to-host" && json.Unmarshal(line.Raw, &frame) == nil && frame.Type == "result" {
			total++
		}
	}
	return total
}

func (c *captureTranscript) count(direction string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for _, line := range c.lines {
		if line.Direction == direction {
			total++
		}
	}
	return total
}

func (c *captureTranscript) save(t *testing.T, filename string) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var buffer bytes.Buffer
	for _, line := range c.lines {
		encoded, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		buffer.Write(encoded)
		buffer.WriteByte('\n')
	}
	if err := os.WriteFile(filename, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

type recordingProcess struct {
	client  *rpc.Client
	command *exec.Cmd
	stdin   io.Closer
	done    chan struct{}
	mu      sync.Mutex
	err     error
}

func recordingFactory(transcript *captureTranscript, stderr io.Writer) ProcessFactory {
	return ProcessFactoryFunc(func(ctx context.Context, config rpc.ProcessConfig) (ProcessBridge, error) {
		command := exec.Command(config.Path, config.Args...)
		command.Dir = config.Dir
		command.Env = append([]string{}, config.Env...)
		command.Stderr = stderr
		stdin, err := command.StdinPipe()
		if err != nil {
			return nil, err
		}
		stdout, err := command.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := command.Start(); err != nil {
			return nil, err
		}
		process := &recordingProcess{command: command, stdin: stdin, done: make(chan struct{})}
		forward, feed := io.Pipe()
		writer := io.MultiWriter(stdin, captureWriter{transcript: transcript, direction: "host-to-cli"})
		process.client = rpc.NewClient(forward, writer, rpc.ClientOptions{FrameLimit: config.FrameLimit, QueueCapacity: config.QueueCapacity, CloseReadWriter: stdin})
		pumped := make(chan struct{})
		go func() {
			defer close(pumped)
			tap := captureWriter{transcript: transcript, direction: "cli-to-host"}
			buffer := make([]byte, 64<<10)
			forwarding := true
			for {
				n, err := stdout.Read(buffer)
				if n > 0 {
					_, _ = tap.Write(buffer[:n])
					if forwarding {
						if _, writeErr := feed.Write(buffer[:n]); writeErr != nil {
							forwarding = false
						}
					}
				}
				if err != nil {
					_ = feed.CloseWithError(err)
					return
				}
			}
		}()
		go func() {
			<-process.client.Done()
			_ = forward.CloseWithError(io.ErrClosedPipe)
		}()
		go func() {
			<-pumped
			err := command.Wait()
			process.mu.Lock()
			process.err = err
			process.mu.Unlock()
			close(process.done)
		}()
		return process, nil
	})
}

func (p *recordingProcess) ClientHandle() Client  { return p.client }
func (p *recordingProcess) Done() <-chan struct{} { return p.done }
func (p *recordingProcess) WaitError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *recordingProcess) Close(ctx context.Context) error {
	_ = p.stdin.Close()
	select {
	case <-p.done:
	case <-ctx.Done():
		_ = p.command.Process.Kill()
		<-p.done
	}
	return p.client.Close()
}

type proxySink struct {
	listener net.Listener
	mu       sync.Mutex
	seen     []string
}

func newProxySink(t *testing.T) *proxySink {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sink := &proxySink{listener: listener}
	go sink.serve()
	t.Cleanup(func() { _ = listener.Close() })
	return sink
}

func (s *proxySink) serve() {
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer connection.Close()
			_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
			line, _ := bufio.NewReader(connection).ReadString('\n')
			s.mu.Lock()
			s.seen = append(s.seen, strings.TrimSpace(line))
			s.mu.Unlock()
			_, _ = io.WriteString(connection, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		}()
	}
}

func (s *proxySink) url() string { return "http://" + s.listener.Addr().String() }

func (s *proxySink) connections() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

func captureEnvironment(t *testing.T, root, loopbackBaseURL string, sink *proxySink) []string {
	t.Helper()
	environment := claudeEnvironment(t, root, loopbackBaseURL)
	for i, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY":
			environment[i] = name + "=" + sink.url()
		}
	}
	return environment
}

type captureProbe struct {
	name        string
	tools       ToolPosture
	args        []string
	credentials bool
	enqueue     func(mock *providertest.Server, work string)
	turns       int
	decision    string
	cancel      bool

	releaseAfterResult bool
	environment        []string
	linger             time.Duration
	wait               time.Duration
	terminal           protocol.EnvelopeType
}

func bashCall(id, command string) providertest.ToolCall {
	arguments, _ := json.Marshal(map[string]string{"command": command})
	return providertest.ToolCall{ID: id, Name: "Bash", Arguments: string(arguments)}
}

func corpusSurface() ToolPosture { return AllowTools("Task", "Bash(git status)") }

func claudeCaptureProbes() []captureProbe {
	success := func(count int) func(*providertest.Server, string) {
		return func(mock *providertest.Server, _ string) {
			for range count {
				mock.Enqueue(providertest.AnthropicMessages, providertest.Success)
			}
		}
	}
	tool := func(call func(work string) providertest.ToolCall, after int) func(*providertest.Server, string) {
		return func(mock *providertest.Server, work string) {
			mock.EnqueueToolCall(providertest.AnthropicMessages, call(work))
			for range after {
				mock.Enqueue(providertest.AnthropicMessages, providertest.Success)
			}
		}
	}
	bash := func(id, command string) func(*providertest.Server, string) {
		return tool(func(string) providertest.ToolCall { return bashCall(id, command) }, 1)
	}
	return []captureProbe{
		{name: "text", tools: corpusSurface(), credentials: true, enqueue: success(2), turns: 2, terminal: protocol.TypeRunCompleted},
		{name: "text-unrestricted", tools: UnrestrictedTools(), credentials: true, enqueue: success(1), turns: 1, terminal: protocol.TypeRunCompleted},
		{name: "tool-ls", tools: corpusSurface(), credentials: true, enqueue: bash("toolu_capture_ls", "ls"), turns: 1, decision: "allow", terminal: protocol.TypeRunCompleted},
		{name: "tool-sleep", tools: corpusSurface(), credentials: true, enqueue: bash("toolu_capture_sleep", "sleep 6; ls"), turns: 1, decision: "allow", terminal: protocol.TypeRunCompleted},
		{name: "tool-stream", tools: corpusSurface(), credentials: true, enqueue: bash("toolu_capture_stream", "for i in 1 2 3 4 5 6; do echo tick $i; sleep 1; done"), turns: 1, decision: "allow", terminal: protocol.TypeRunCompleted},
		{name: "tool-stream-container", tools: corpusSurface(), credentials: true, environment: []string{"CLAUDE_CODE_CONTAINER_ID=capture"}, enqueue: bash("toolu_capture_stream", "for i in 1 2 3 4 5 6; do echo tick $i; sleep 1; done"), turns: 1, decision: "allow", terminal: protocol.TypeRunCompleted},
		{name: "tool-false", tools: corpusSurface(), credentials: true, enqueue: bash("toolu_capture_false", "false"), turns: 1, decision: "allow", terminal: protocol.TypeRunCompleted},
		{name: "tool-read", tools: UnrestrictedTools(), credentials: true, enqueue: tool(func(work string) providertest.ToolCall {
			arguments, _ := json.Marshal(map[string]string{"file_path": filepath.Join(work, "notes.txt")})
			return providertest.ToolCall{ID: "toolu_capture_read", Name: "Read", Arguments: string(arguments)}
		}, 1), turns: 1, decision: "allow", terminal: protocol.TypeRunCompleted},
		{name: "gate-allow", tools: corpusSurface(), credentials: true, enqueue: tool(func(work string) providertest.ToolCall {
			return bashCall("toolu_capture_touch", "touch "+filepath.Join(work, "x"))
		}, 1), turns: 1, decision: "allow", terminal: protocol.TypeRunCompleted},
		{name: "gate-deny", tools: corpusSurface(), credentials: true, enqueue: tool(func(work string) providertest.ToolCall {
			return bashCall("toolu_capture_rm", "rm -rf "+filepath.Join(work, "y"))
		}, 1), turns: 1, decision: "deny", terminal: protocol.TypeRunCompleted},
		{name: "interrupt", tools: corpusSurface(), credentials: true, enqueue: func(mock *providertest.Server, _ string) {
			mock.Enqueue(providertest.AnthropicMessages, providertest.Slow)
		}, turns: 1, cancel: true, terminal: protocol.TypeRunCancelled},
		{name: "max-turns", tools: corpusSurface(), args: []string{"--max-turns", "1"}, credentials: true, enqueue: bash("toolu_capture_turns", "ls"), turns: 1, decision: "allow", terminal: protocol.TypeRunCompleted},
		{name: "api-error", tools: corpusSurface(), credentials: true, enqueue: func(mock *providertest.Server, _ string) {
			for range 16 {
				mock.Enqueue(providertest.AnthropicMessages, providertest.Error)
			}
		}, turns: 1, wait: 300 * time.Second, terminal: protocol.TypeRunFailed},
		{name: "auth-failure", tools: corpusSurface(), turns: 1, terminal: protocol.TypeRunFailed},
		{name: "subagent", tools: corpusSurface(), credentials: true, enqueue: func(mock *providertest.Server, _ string) {
			arguments, _ := json.Marshal(map[string]string{"description": "research", "prompt": "Reply with the fixture response.", "subagent_type": "general-purpose"})
			mock.EnqueueToolCall(providertest.AnthropicMessages, providertest.ToolCall{ID: "toolu_capture_agent", Name: "Agent", Arguments: string(arguments)})
			success(5)(mock, "")
		}, turns: 1, decision: "allow", linger: 8 * time.Second, terminal: protocol.TypeRunCompleted},
		{name: "subagent-outlives-turn", tools: corpusSurface(), credentials: true, enqueue: func(mock *providertest.Server, _ string) {
			arguments, _ := json.Marshal(map[string]string{"description": "research", "prompt": "Reply with the fixture response.", "subagent_type": "general-purpose"})
			mock.EnqueueToolCall(providertest.AnthropicMessages, providertest.ToolCall{ID: "toolu_capture_agent", Name: "Agent", Arguments: string(arguments)})
			mock.Enqueue(providertest.AnthropicMessages, providertest.Slow)
			success(4)(mock, "")
		}, turns: 1, decision: "allow", releaseAfterResult: true, linger: 8 * time.Second, terminal: protocol.TypeRunCompleted},
		{name: "background-bash", tools: corpusSurface(), credentials: true, enqueue: func(mock *providertest.Server, _ string) {
			arguments, _ := json.Marshal(map[string]any{"command": "sleep 2; echo done", "description": "Wait, then print done", "run_in_background": true})
			mock.EnqueueToolCall(providertest.AnthropicMessages, providertest.ToolCall{ID: "toolu_capture_background", Name: "Bash", Arguments: string(arguments)})
			success(4)(mock, "")
		}, turns: 1, decision: "allow", linger: 8 * time.Second, terminal: protocol.TypeRunCompleted},
	}
}

func claudeRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func TestClaudeProcessRecordsCorpusProbes(t *testing.T) {
	directory := os.Getenv("OAP_CLAUDE_CAPTURE_DIR")
	if directory == "" {
		t.Skip("set OAP_CLAUDE_CAPTURE_DIR to an absolute directory outside the repository, with absolute OAP_CLAUDE_BIN (pinned claude 2.1.280 binary), to record the corpus probes; optionally set OAP_CLAUDE_SHA256 (64 hex characters) for exact-artifact evidence")
	}
	if testing.Short() {
		t.Skip("skipping opt-in Claude Code corpus capture in short mode")
	}
	repository := claudeRepositoryRoot(t)
	relative, err := filepath.Rel(repository, directory)
	if !filepath.IsAbs(directory) || err != nil || relative == "." || !strings.HasPrefix(relative, "..") {
		t.Fatalf("OAP_CLAUDE_CAPTURE_DIR must be an absolute directory outside %s", repository)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := verifiedClaudeCLI(t)
	sink := newProxySink(t)
	versionCtx, cancelVersion := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelVersion()
	root := t.TempDir()
	command := exec.CommandContext(versionCtx, binary, "--version")
	command.Env = captureEnvironment(t, root, "", sink)
	command.Dir = root
	version, err := command.Output()
	if err != nil {
		t.Fatalf("--version: %v", err)
	}
	if !strings.HasPrefix(string(version), strings.TrimPrefix(PinnedVersion, "v")+" ") {
		t.Fatalf("the binary self-reports %q, want %s", version, PinnedVersion)
	}
	if err := os.WriteFile(filepath.Join(directory, "version.txt"), version, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, probe := range claudeCaptureProbes() {
		t.Run(probe.name, func(t *testing.T) {
			recordClaudeProbe(t, directory, sink, probe)
		})
	}
	if seen := sink.connections(); len(seen) != 0 {
		t.Fatalf("the child reached for a proxy: %v", seen)
	}
}

func recordClaudeProbe(t *testing.T, directory string, sink *proxySink, probe captureProbe) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "notes.txt"), []byte("probe notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mock := providertest.New(t, providertest.Config{AnthropicKey: claudeMockSecret})
	if probe.enqueue != nil {
		probe.enqueue(mock, work)
	}
	baseURL := ""
	if probe.credentials {
		baseURL = mock.AnthropicBaseURL()
	}
	transcript := newCaptureTranscript()
	var stderr bytes.Buffer
	implementation := newPinnedClaude(t, Config{
		Environment: append(captureEnvironment(t, root, baseURL, sink), probe.environment...), WorkingDirectory: work,
		Tools: probe.tools, Args: probe.args, ProcessFactory: recordingFactory(transcript, &stderr),
	})
	wait := probe.wait
	if wait == 0 {
		wait = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait+60*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, base.OpenRequest{SessionID: "capture-session", Participant: protocol.Participant{ID: "capture-user"}})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if probe.releaseAfterResult {
		go func() {
			for deadline := time.Now().Add(20 * time.Second); transcript.results() == 0 && time.Now().Before(deadline); {
				time.Sleep(50 * time.Millisecond)
			}
			transcript.note("release")
			mock.Release(providertest.AnthropicMessages)
		}()
	}
	var envelopes []protocol.Envelope
	for turn := range probe.turns {
		transcript.note("submit")
		admission, stream, err := session.Submit(ctx, protocol.MessageSubmitRequest{SessionID: "capture-session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
		if err != nil {
			t.Fatalf("turn %d submit: %v", turn+1, err)
		}
		for settled := false; !settled; {
			event := adaptertest.Next(t, stream, wait)
			envelopes = append(envelopes, event)
			switch event.Type {
			case protocol.TypeRunStarted:
				if probe.cancel {
					transcript.note("cancel")
					if _, err := session.Cancel(ctx, admission.RunID); err != nil {
						t.Fatalf("cancel: %v", err)
					}
					mock.Release(providertest.AnthropicMessages)
				}
			case protocol.TypeUserInputRequested:
				var payload protocol.UserInputRequestedPayload
				if err := event.DecodePayload(&payload); err != nil {
					t.Fatal(err)
				}
				if probe.decision == "" {
					t.Fatalf("a gate opened: %s", event.Payload)
				}
				transcript.note("resolve " + probe.decision)
				if err := session.Resolve(ctx, base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: payload.InteractionID, SessionID: "capture-session", Answers: []protocol.InputAnswer{{QuestionID: "decision", SelectedOptionIDs: []string{probe.decision}}}}}); err != nil {
					t.Fatalf("resolve: %v", err)
				}
			case protocol.TypeRunCompleted, protocol.TypeRunFailed, protocol.TypeRunCancelled:
				settled = true
				if event.Type != probe.terminal {
					t.Errorf("turn %d settled %s, want %s: %s", turn+1, event.Type, probe.terminal, event.Payload)
				}
			}
		}
		envelopes = append(envelopes, adaptertest.Drain(t, stream, 10*time.Second)...)
	}
	linger := probe.linger
	if linger == 0 {
		linger = time.Second
	}
	for deadline := time.Now().Add(30 * time.Second); transcript.quietFor() < linger && time.Now().Before(deadline); {
		time.Sleep(100 * time.Millisecond)
	}
	transcript.note("close")
	if err := session.Close(context.Background()); err != nil && !errors.Is(err, base.ErrSessionClosed) {
		t.Logf("close: %v", err)
	}
	transcript.save(t, filepath.Join(directory, probe.name+".jsonl"))
	oap, err := json.MarshalIndent(envelopes, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, probe.name+".oap.json"), append(oap, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	requests, err := json.MarshalIndent(mock.RequestsFor(providertest.AnthropicMessages), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, probe.name+".provider.json"), append(requests, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, probe.name+".stderr"), stderr.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if transcript.count("cli-to-host") == 0 || transcript.count("host-to-cli") == 0 {
		t.Fatalf("the transcript is missing a direction")
	}
}

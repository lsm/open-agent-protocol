package servestdio

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/protocol"
)

// buildOAP compiles the oap binary once for the spawned-process tests, the
// same re-exec discipline the corpus process harnesses use; go test runs
// with the module available, so the build needs no network.
func buildOAP(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	binary := filepath.Join(t.TempDir(), "oap")
	build := exec.Command("go", "build", "-o", binary, "./cmd/oap")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build oap: %v: %s", err, output)
	}
	return binary
}

// child is one spawned `oap serve --stdio` process driven like a host would:
// requests on stdin, protocol lines on stdout, diagnostics on stderr.
type child struct {
	t      *testing.T
	stdin  io.WriteCloser
	reader *bufio.Reader
	stderr *syncBuffer
	done   chan error
}

// startChild spawns the binary over the default memory registry.
func startChild(t *testing.T, binary string) *child {
	t.Helper()
	command := exec.Command(binary, "serve", "--stdio")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	c := &child{t: t, stdin: stdin, reader: bufio.NewReader(stdout), stderr: &syncBuffer{}}
	c.done = make(chan error, 1)
	go func() {
		_, _ = io.Copy(c.stderr, stderr)
	}()
	go func() { c.done <- command.Wait() }()
	return c
}

// syncBuffer is a mutex-guarded buffer for the child's stderr.
type syncBuffer struct {
	mu     sync.Mutex
	buffer strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buffer.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buffer.String()
}

func (c *child) send(line string) {
	c.t.Helper()
	if _, err := c.stdin.Write([]byte(line + "\n")); err != nil {
		c.t.Fatalf("write request %q: %v", line, err)
	}
}

func (c *child) line() string {
	c.t.Helper()
	type read struct {
		text string
		err  error
	}
	readDone := make(chan read, 1)
	go func() {
		text, err := c.reader.ReadString('\n')
		readDone <- read{text: text, err: err}
	}()
	select {
	case result := <-readDone:
		if result.err != nil {
			c.t.Fatalf("read line: %v (got %q)", result.err, result.text)
		}
		return strings.TrimSuffix(result.text, "\n")
	case <-time.After(15 * time.Second):
		c.t.Fatal("child produced no line within the deadline")
		return ""
	}
}

// expect reads the next line and requires it to be the ok response for id.
func (c *child) expect(id int64) json.RawMessage {
	c.t.Helper()
	line := c.line()
	var response responseLine
	if err := json.Unmarshal([]byte(line), &response); err != nil {
		c.t.Fatalf("response line %q: %v", line, err)
	}
	if response.ID != id || !response.OK {
		c.t.Fatalf("line %q is not the ok response for %d", line, id)
	}
	return response.Result
}

// envelope builds one request envelope the way the Go client does; the
// run-scoped ops need the envelope-level run_id the schema requires.
func envelope(t *testing.T, id string, typ protocol.EnvelopeType, payload any, sessionID, runID string) json.RawMessage {
	t.Helper()
	return requestEnvelope(t, id, typ, payload, sessionID, runID)
}

// TestStdioEndToEnd drives one whole scripted session against the spawned
// binary: list adapters, probe capabilities, open, subscribe, submit, resolve
// both interactive gates from the envelopes the stream itself delivers,
// settle the run, read state, close, and exit zero on stdin EOF.
func TestStdioEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the oap binary")
	}
	binary := buildOAP(t)
	c := startChild(t, binary)
	nextID := int64(0)
	next := func() int64 { nextID++; return nextID }

	// Adapter listing names the memory adapter.
	c.send(fmt.Sprintf(`{"id":%d,"op":"adapters"}`, next()))
	if result := c.expect(1); !strings.Contains(string(result), `"name":"memory"`) {
		t.Fatalf("adapters result lacks memory: %s", result)
	}

	c.send(fmt.Sprintf(`{"id":%d,"op":"capabilities","adapter":"memory"}`, next()))
	if capabilities := c.expect(2); !strings.Contains(string(capabilities), `"capabilities.response"`) {
		t.Fatalf("capabilities result is not a response envelope: %s", capabilities)
	}

	c.send(fmt.Sprintf(`{"id":%d,"op":"open","adapter":"memory","request":%s}`, next(), envelope(t, "e2e-open", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{SessionID: "e2e"}, "", "")))
	if opened := c.expect(3); !strings.Contains(string(opened), `"session_id":"e2e"`) {
		t.Fatalf("open result: %s", opened)
	}

	// The session listing mirrors GET /sessions.
	c.send(fmt.Sprintf(`{"id":%d,"op":"sessions"}`, next()))
	if listed := c.expect(4); !strings.Contains(string(listed), `"session_id":"e2e"`) || !strings.Contains(string(listed), `"adapter":"memory"`) {
		t.Fatalf("sessions listing lacks the open session: %s", listed)
	}

	// Subscribe before submitting, waiting for the subscription ack.
	c.send(fmt.Sprintf(`{"id":%d,"op":"events","session_id":"e2e"}`, next()))
	c.expect(5)

	c.send(fmt.Sprintf(`{"id":%d,"op":"submit","session_id":"e2e","request":%s}`, next(), envelope(t, "e2e-submit", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "e2e", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run the scripted session")}},
	}, "e2e", "")))

	// The submit response and the run's first four envelopes interleave
	// freely; the permission gate envelope ends the burst.
	var permission string
	responsesLeft, envelopesLeft := 1, 4
	for responsesLeft > 0 || envelopesLeft > 0 {
		line := c.line()
		if strings.HasPrefix(line, `{"id":`) {
			var response responseLine
			if err := json.Unmarshal([]byte(line), &response); err != nil || response.ID != 6 || !response.OK {
				t.Fatalf("submit response %q: %v", line, err)
			}
			responsesLeft--
			continue
		}
		var event envelopeLine
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("event line %q: %v", line, err)
		}
		if event.Sequence != nil && *event.Sequence == 4 {
			permission = line
		}
		envelopesLeft--
	}
	if permission == "" {
		t.Fatal("permission gate envelope not observed")
	}

	// Resolve the permission gate using the interaction it requested; the
	// permission resolve's response may interleave with the input gate.
	permissionResolve, runID := resolveFromGate(t, permission, "e2e", "e2e-permission")
	c.send(fmt.Sprintf(`{"id":%d,"op":"resolve","session_id":"e2e","request":%s}`, next(), permissionResolve))
	inputGate := readGate(t, c, 7)
	inputResolve, inputRun := resolveFromGate(t, inputGate, "e2e", "e2e-input")
	if inputRun != runID {
		t.Fatalf("input gate names run %s, permission gate named %s", inputRun, runID)
	}
	c.send(fmt.Sprintf(`{"id":%d,"op":"resolve","session_id":"e2e","request":%s}`, next(), inputResolve))

	// The run settles: consume lines until run.completed is delivered,
	// remembering whether the trailing resolve response landed first.
	resolveSeen, completed := false, false
	for !completed {
		line := c.line()
		if strings.HasPrefix(line, `{"id":`) {
			var response responseLine
			if err := json.Unmarshal([]byte(line), &response); err != nil {
				t.Fatalf("resolve response %q: %v", line, err)
			}
			if response.ID == 8 {
				if !response.OK {
					t.Fatalf("input resolve refused: %s", line)
				}
				resolveSeen = true
			}
			continue
		}
		if strings.Contains(line, `"run.completed"`) {
			completed = true
		}
	}
	if !resolveSeen {
		c.expect(8)
	}

	c.send(fmt.Sprintf(`{"id":%d,"op":"state","session_id":"e2e"}`, next()))
	if state := c.expect(9); !strings.Contains(string(state), `"status":"idle"`) {
		t.Fatalf("state result: %s", state)
	}

	// A cursor subscription replays the settled run's retained suffix.
	c.send(fmt.Sprintf(`{"id":%d,"op":"events","session_id":"e2e","after":9}`, next()))
	c.expect(10)
	for sequence := uint64(10); sequence <= 12; sequence++ {
		line := c.line()
		if !strings.Contains(line, fmt.Sprintf(`"sequence":%d`, sequence)) {
			t.Fatalf("replay line %q is not sequence %d", line, sequence)
		}
	}

	c.send(fmt.Sprintf(`{"id":%d,"op":"close","session_id":"e2e"}`, next()))
	c.expect(11)

	// Closing stdin is the host's shutdown: the process settles and exits 0.
	if err := c.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-c.done:
		if err != nil {
			t.Fatalf("child exited with error: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("child did not exit after stdin closed")
	}
	if diagnostics := c.stderr.String(); !strings.Contains(diagnostics, "stopped") {
		t.Fatalf("child stderr lacks the shutdown note: %q", diagnostics)
	}
}

// readGate consumes lines until the user-input gate envelope arrives,
// skipping the resolve response that may interleave with it.
func readGate(t *testing.T, c *child, responseID int64) string {
	t.Helper()
	for {
		line := c.line()
		if strings.HasPrefix(line, `{"id":`) {
			var response responseLine
			if err := json.Unmarshal([]byte(line), &response); err != nil || response.ID != responseID || !response.OK {
				t.Fatalf("resolve response %q: %v", line, err)
			}
			continue
		}
		if strings.Contains(line, `"user.input.requested"`) {
			return line
		}
	}
}

// resolveFromGate echoes the interaction one gate envelope requested, the
// way the Go client resolves it, and reports the run id.
func resolveFromGate(t *testing.T, gateLine string, sessionID string, id string) (json.RawMessage, protocol.RunID) {
	t.Helper()
	var event envelopeLine
	if err := json.Unmarshal([]byte(gateLine), &event); err != nil {
		t.Fatal(err)
	}
	gate, err := protocol.ParseEnvelope(event.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	switch gate.Type {
	case protocol.TypeActionPermissionRequested:
		var requested protocol.PermissionRequestedPayload
		if err := gate.DecodePayload(&requested); err != nil {
			t.Fatal(err)
		}
		resolve := envelope(t, id, protocol.TypeActionPermissionResolveRequest, protocol.PermissionResolveRequest{
			InteractionID: requested.InteractionID, SessionID: protocol.SessionID(sessionID), RunID: requested.RunID,
			RequestedBy: requested.RequestedBy, RespondedBy: requested.RespondedBy, ChoiceID: "approve", Granted: true,
		}, sessionID, string(requested.RunID))
		return resolve, requested.RunID
	case protocol.TypeUserInputRequested:
		var requested protocol.UserInputRequestedPayload
		if err := gate.DecodePayload(&requested); err != nil {
			t.Fatal(err)
		}
		resolve := envelope(t, id, protocol.TypeUserInputResolveRequest, protocol.UserInputResolveRequest{
			InteractionID: requested.InteractionID, SessionID: protocol.SessionID(sessionID), RunID: requested.RunID,
			RequestedBy: requested.RequestedBy, RespondedBy: requested.RespondedBy,
			Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}},
		}, sessionID, string(requested.RunID))
		return resolve, requested.RunID
	default:
		t.Fatalf("cannot resolve %s", gate.Type)
		return nil, ""
	}
}

// TestStdioFailClosedOnMalformedLine drives one framing defect at the
// spawned binary: the process still flushes the already-admitted response,
// emits exactly one bounded diagnostic on stderr, and exits non-zero.
func TestStdioFailClosedOnMalformedLine(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and spawns the oap binary")
	}
	binary := buildOAP(t)
	c := startChild(t, binary)

	c.send(`{"id":1,"op":"adapters"}`)
	c.send("not json at all")
	if err := c.stdin.Close(); err != nil {
		t.Fatal(err)
	}

	// The admitted request's response is flushed before the exit.
	line := c.line()
	var response responseLine
	if err := json.Unmarshal([]byte(line), &response); err != nil || response.ID != 1 || !response.OK {
		t.Fatalf("prior response not flushed: %q (%v)", line, err)
	}

	select {
	case err := <-c.done:
		if err == nil {
			t.Fatal("child exited zero on a malformed line")
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("child wait error is not an exit: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("child did not exit after the malformed line")
	}
	diagnostics := strings.TrimSpace(c.stderr.String())
	lines := 0
	for _, diagnostic := range strings.Split(diagnostics, "\n") {
		switch {
		case strings.Contains(diagnostic, "serving adapters over stdio"):
		case strings.Contains(diagnostic, "line 2 is not a valid request"):
			lines++
		case diagnostic == "":
		default:
			t.Fatalf("unexpected stderr line %q", diagnostic)
		}
	}
	if lines != 1 {
		t.Fatalf("expected exactly one malformed-line diagnostic, stderr: %q", diagnostics)
	}
}

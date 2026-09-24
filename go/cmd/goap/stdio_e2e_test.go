package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

var (
	buildOnce   sync.Once
	builtBinary string
	buildErr    error
	buildDir    string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if buildDir != "" {
		os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

func oapBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		goTool := os.Getenv("OAP_GO")
		if goTool == "" {
			goTool = "go"
		}
		resolved, err := exec.LookPath(goTool)
		if err != nil {
			buildErr = fmt.Errorf("no go toolchain on PATH: %w", err)
			return
		}
		buildDir, buildErr = os.MkdirTemp("", "oap-e2e")
		if buildErr != nil {
			return
		}
		binary := filepath.Join(buildDir, "goap")
		build := exec.Command(resolved, "build", "-o", binary, "./go/cmd/goap")
		build.Dir = repositoryRoot()
		if output, err := build.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("build goap: %v: %s", err, output)
			return
		}
		builtBinary = binary
	})
	if buildErr != nil {
		if strings.Contains(buildErr.Error(), "no go toolchain") {
			t.Skip(buildErr.Error())
		}
		t.Fatal(buildErr)
	}
	return builtBinary
}

type child struct {
	t       *testing.T
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	lines   chan string
	readErr chan error
	stderr  *strings.Builder

	held        map[int64]responseShape
	heldSignals []string
}

func spawn(t *testing.T, args ...string) *child {
	t.Helper()
	cmd := exec.Command(oapBinary(t), append([]string{"serve", "--stdio"}, args...)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &strings.Builder{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &child{t: t, cmd: cmd, stdin: stdin, lines: make(chan string, 64), readErr: make(chan error, 1), stderr: stderr, held: map[int64]responseShape{}}

	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for scanner.Scan() {
			c.lines <- scanner.Text()
		}
		c.readErr <- scanner.Err()
		close(c.lines)
	}()
	t.Cleanup(func() {
		stdin.Close()
		cmd.Wait()
	})
	return c
}

func (c *child) send(format string, args ...any) {
	c.t.Helper()
	line := fmt.Sprintf(format, args...)
	if _, err := fmt.Fprintln(c.stdin, line); err != nil {
		c.t.Fatalf("write %q: %v", line, err)
	}
}

func (c *child) next() (string, bool) {
	c.t.Helper()
	select {
	case line, ok := <-c.lines:
		return line, ok
	case <-time.After(20 * time.Second):
		c.t.Fatalf("daemon produced no line within the deadline; stderr:\n%s", c.stderr)
		return "", false
	}
}

func (c *child) response(id int64) responseShape {
	c.t.Helper()
	if shape, ok := c.held[id]; ok {
		delete(c.held, id)
		return c.require(id, shape)
	}
	for {
		line, ok := c.next()
		if !ok {
			c.t.Fatalf("stdout ended before the response to %d; stderr:\n%s", id, c.stderr)
		}
		shape := c.decode(line)
		if shape.Event != "" {
			c.heldSignals = append(c.heldSignals, line)
			continue
		}
		if shape.ID != id {
			c.held[shape.ID] = shape
			continue
		}
		return c.require(id, shape)
	}
}

func (c *child) signal() string {
	c.t.Helper()
	if len(c.heldSignals) > 0 {
		line := c.heldSignals[0]
		c.heldSignals = c.heldSignals[1:]
		return line
	}
	for {
		line, ok := c.next()
		if !ok {
			c.t.Fatalf("stdout ended before the next subscription line; stderr:\n%s", c.stderr)
		}
		shape := c.decode(line)
		if shape.Event == "" {
			c.held[shape.ID] = shape
			continue
		}
		return line
	}
}

func (c *child) wait() error {
	c.t.Helper()
	for range c.lines {

	}
	if err := <-c.readErr; err != nil {
		c.t.Fatalf("stdout scan: %v", err)
	}
	return c.cmd.Wait()
}

func (c *child) decode(line string) responseShape {
	c.t.Helper()
	var shape responseShape
	if err := json.Unmarshal([]byte(line), &shape); err != nil {
		c.t.Fatalf("line %q: %v", line, err)
	}
	return shape
}

func (c *child) require(id int64, shape responseShape) responseShape {
	c.t.Helper()
	if !shape.OK {
		c.t.Fatalf("op %d failed: %s: %s", id, shape.Error.Code, shape.Error.Message)
	}
	return shape
}

type responseShape struct {
	ID     int64           `json:"id"`
	OK     bool            `json:"ok"`
	Event  string          `json:"event"`
	Result json.RawMessage `json:"result"`
	Error  struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type signalShape struct {
	Event    string          `json:"event"`
	ID       int64           `json:"id"`
	Sequence *uint64         `json:"sequence"`
	Envelope json.RawMessage `json:"envelope"`
}

func TestStdioBinaryDrivesAFullSession(t *testing.T) {
	c := spawn(t)

	c.send(`{"id":1,"op":"adapters"}`)
	c.send(`{"id":2,"op":"capabilities","adapter":"memory"}`)
	c.response(1)
	c.response(2)

	c.send(`{"id":3,"op":"open","adapter":"memory","request":%s}`,
		envelopeJSON(t, "e2e-open", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{SessionID: "e2e"}, "", ""))
	opened := c.response(3)
	if session := envelopeSessionID(t, opened.Result); session != "e2e" {
		t.Fatalf("open published session %q, want e2e", session)
	}

	c.send(`{"id":4,"op":"sessions"}`)
	if listing := c.response(4); !strings.Contains(string(listing.Result), `"e2e"`) {
		t.Fatalf("sessions listing omits the opened session: %s", listing.Result)
	}

	c.send(`{"id":5,"op":"events","session_id":"e2e"}`)
	if ack := c.response(5); string(ack.Result) != "null" {
		t.Fatalf("events acknowledgement %s, want null", ack.Result)
	}

	c.send(`{"id":6,"op":"submit","session_id":"e2e","request":%s}`,
		envelopeJSON(t, "e2e-submit", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
			SessionID: "e2e", Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
		}, "e2e", ""))
	c.response(6)

	var lastSequence uint64
	nextID := int64(7)
	terminal := protocol.EnvelopeType("")
	for terminal == "" {
		line := c.signal()
		var signal signalShape
		if err := json.Unmarshal([]byte(line), &signal); err != nil {
			t.Fatal(err)
		}
		if signal.Event != "envelope" {
			t.Fatalf("unexpected signal on a healthy stream: %s", line)
		}
		if signal.ID != 5 {
			t.Fatalf("envelope correlated to %d, want the events request 5", signal.ID)
		}
		if signal.Sequence == nil || *signal.Sequence <= lastSequence {
			t.Fatalf("sequence %v does not advance past %d", signal.Sequence, lastSequence)
		}
		lastSequence = *signal.Sequence
		var envelope protocol.Envelope
		if err := json.Unmarshal(signal.Envelope, &envelope); err != nil {
			t.Fatal(err)
		}
		switch envelope.Type {
		case protocol.TypeActionPermissionRequested, protocol.TypeUserInputRequested:
			c.send(`{"id":%d,"op":"resolve","session_id":"e2e","request":%s}`, nextID, resolveJSON(t, envelope))
			nextID++
		case protocol.TypeRunCompleted, protocol.TypeRunFailed, protocol.TypeRunCancelled:
			terminal = envelope.Type
		}
	}
	if terminal != protocol.TypeRunCompleted {
		t.Fatalf("run settled %s, want %s", terminal, protocol.TypeRunCompleted)
	}

	c.send(`{"id":20,"op":"state","session_id":"e2e"}`)
	c.response(20)

	replayed := 0
	c.send(`{"id":21,"op":"events","session_id":"e2e","after":%d}`, lastSequence-2)
	c.response(21)
	for replayed < 2 {
		line := c.signal()
		var signal signalShape
		if err := json.Unmarshal([]byte(line), &signal); err != nil {
			t.Fatal(err)
		}
		if signal.Event != "envelope" || signal.ID != 21 {
			t.Fatalf("unexpected line during replay: %s", line)
		}
		if signal.Sequence == nil || *signal.Sequence <= lastSequence-2 {
			t.Fatalf("replay delivered sequence %v, which is not after the cursor", signal.Sequence)
		}
		replayed++
	}

	c.send(`{"id":22,"op":"close","session_id":"e2e"}`)
	c.response(22)

	if err := c.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.wait(); err != nil {
		t.Fatalf("clean exit expected, got %v; stderr:\n%s", err, c.stderr)
	}
	if banner := c.stderr.String(); !strings.Contains(banner, "serving adapters over stdio: memory") {
		t.Fatalf("stderr carries no banner:\n%s", banner)
	}
}

func TestStdioBinaryFailsClosedOnAMalformedLine(t *testing.T) {
	c := spawn(t)
	c.send(`{"id":1,"op":"adapters"}`)
	c.response(1)
	c.send(`{"id":2,"op":`)

	var emitted []string
	for line := range c.lines {
		emitted = append(emitted, line)
	}
	if scanErr := <-c.readErr; scanErr != nil {
		t.Fatalf("stdout scan: %v", scanErr)
	}
	err := c.cmd.Wait()
	if err == nil {
		t.Fatalf("a malformed line exited zero; stderr:\n%s", c.stderr)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() == 0 {
		t.Fatalf("want a non-zero exit, got %v", err)
	}
	diagnostic := c.stderr.String()
	if !strings.Contains(diagnostic, "line 2") {
		t.Fatalf("stderr does not name the offending line:\n%s", diagnostic)
	}
	if len(diagnostic) > 4096 {
		t.Fatalf("stderr diagnostic is unbounded (%d bytes)", len(diagnostic))
	}
	for _, line := range emitted {
		if strings.Contains(line, "invalid JSON") {
			t.Fatalf("the framing diagnostic reached stdout: %s", line)
		}
	}
}

func TestStdioRefusesAListenAddress(t *testing.T) {
	binary := oapBinary(t)
	output, err := exec.Command(binary, "serve", "--stdio", "--addr", "127.0.0.1:0").CombinedOutput()
	if err == nil {
		t.Fatalf("serve --stdio --addr succeeded: %s", output)
	}
	if !strings.Contains(string(output), "mutually exclusive") {
		t.Fatalf("refusal does not explain itself: %s", output)
	}
}

func envelopeJSON(t *testing.T, id string, typ protocol.EnvelopeType, payload any, sessionID, runID string) []byte {
	t.Helper()
	envelope, err := protocol.NewEnvelope(typ, protocol.EnvelopeID(id), payload)
	if err != nil {
		t.Fatal(err)
	}
	envelope.SessionID = protocol.SessionID(sessionID)
	envelope.RunID = protocol.RunID(runID)
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func envelopeSessionID(t *testing.T, result json.RawMessage) string {
	t.Helper()
	var envelope protocol.Envelope
	if err := json.Unmarshal(result, &envelope); err != nil {
		t.Fatal(err)
	}
	return string(envelope.SessionID)
}

func resolveJSON(t *testing.T, gate protocol.Envelope) []byte {
	t.Helper()
	switch gate.Type {
	case protocol.TypeActionPermissionRequested:
		var request protocol.PermissionRequestedPayload
		if err := gate.DecodePayload(&request); err != nil {
			t.Fatal(err)
		}
		return envelopeJSON(t, "e2e-permission", protocol.TypeActionPermissionResolveRequest, protocol.PermissionResolveRequest{
			InteractionID: request.InteractionID, SessionID: "e2e", RunID: request.RunID,
			RequestedBy: request.RequestedBy, RespondedBy: request.RespondedBy, ChoiceID: "approve", Granted: true,
		}, "e2e", string(request.RunID))
	case protocol.TypeUserInputRequested:
		var request protocol.UserInputRequestedPayload
		if err := gate.DecodePayload(&request); err != nil {
			t.Fatal(err)
		}
		return envelopeJSON(t, "e2e-input", protocol.TypeUserInputResolveRequest, protocol.UserInputResolveRequest{
			InteractionID: request.InteractionID, SessionID: "e2e", RunID: request.RunID,
			RequestedBy: request.RequestedBy, RespondedBy: request.RespondedBy,
			Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}},
		}, "e2e", string(request.RunID))
	default:
		t.Fatalf("cannot resolve %s", gate.Type)
		return nil
	}
}

func TestStdioExampleSessionRuns(t *testing.T) {
	script, err := os.ReadFile(filepath.Join(repositoryRoot(), "examples", "oap-stdio-session.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	c := spawn(t)
	gates := map[string]bool{}
	completed := false

	note := func(line string) {
		var signal signalShape
		if err := json.Unmarshal([]byte(line), &signal); err != nil {
			t.Fatal(err)
		}
		if signal.Event != "envelope" {
			return
		}
		var envelope protocol.Envelope
		if err := json.Unmarshal(signal.Envelope, &envelope); err != nil {
			t.Fatal(err)
		}
		var payload struct {
			InteractionID string `json:"interaction_id"`
		}
		json.Unmarshal(envelope.Payload, &payload)
		if payload.InteractionID != "" {
			gates[payload.InteractionID] = true
		}
		if envelope.Type == protocol.TypeRunCompleted {
			completed = true
		}
	}

	for _, raw := range strings.Split(strings.TrimSpace(string(script)), "\n") {
		var op struct {
			ID      int64           `json:"id"`
			Op      string          `json:"op"`
			Request json.RawMessage `json:"request"`
		}
		if err := json.Unmarshal([]byte(raw), &op); err != nil {
			t.Fatalf("example line %q: %v", raw, err)
		}
		if op.Op == "resolve" {

			var envelope struct {
				Payload struct {
					InteractionID string `json:"interaction_id"`
				} `json:"payload"`
			}
			if err := json.Unmarshal(op.Request, &envelope); err != nil {
				t.Fatal(err)
			}
			for !gates[envelope.Payload.InteractionID] {
				note(c.signal())
			}
		}
		c.send("%s", raw)
		c.response(op.ID)
	}

	for !completed && len(c.heldSignals) > 0 {
		note(c.signal())
	}
	for !completed {
		note(c.signal())
	}
	if !completed {
		t.Fatalf("the example session never completed its run; stderr:\n%s", c.stderr)
	}
	if err := c.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.wait(); err != nil {
		t.Fatalf("the example session exited %v; stderr:\n%s", err, c.stderr)
	}
}

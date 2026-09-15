package servestdio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
)

// --- deterministic hub and frontend harness ---

// testClock and testIDs make the memory adapter's envelopes deterministic;
// both are mutex-guarded because concurrent requests share one adapter.
type testClock struct {
	mu sync.Mutex
	n  int64
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return time.UnixMilli(c.n)
}

type testIDs struct {
	mu sync.Mutex
	n  int
}

func (g *testIDs) NewID(kind string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return fmt.Sprintf("%s-%02d", kind, g.n)
}

func newTestHub(t *testing.T, journalCapacity, streamQueue int) *serve.Hub {
	t.Helper()
	registry := serve.NewRegistry()
	clock, ids := &testClock{}, &testIDs{}
	if err := registry.Register("memory", base.NewMemory(base.Config{Clock: clock, IDs: ids, JournalCapacity: journalCapacity})); err != nil {
		t.Fatal(err)
	}
	return serve.New(registry, serve.Options{StreamQueue: streamQueue})
}

// frontend drives one Server through real pipes, so line framing — not just
// the values — is what the tests observe.
type frontend struct {
	t      *testing.T
	stdin  io.WriteCloser
	reader *bufio.Reader
	done   chan error
}

func startFrontend(t *testing.T, hub *serve.Hub, options Options) *frontend {
	t.Helper()
	server, err := New(hub, options)
	if err != nil {
		t.Fatal(err)
	}
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), stdinReader, stdoutWriter) }()
	return &frontend{t: t, stdin: stdinWriter, reader: bufio.NewReader(stdoutReader), done: done}
}

func (f *frontend) send(line string) {
	f.t.Helper()
	if _, err := f.stdin.Write([]byte(line + "\n")); err != nil {
		f.t.Fatalf("write request %q: %v", line, err)
	}
}

// line reads one daemon line within a deadline; a frontend that produces
// nothing is stuck, not slow.
func (f *frontend) line() string {
	f.t.Helper()
	type read struct {
		text string
		err  error
	}
	readDone := make(chan read, 1)
	go func() {
		text, err := f.reader.ReadString('\n')
		readDone <- read{text: text, err: err}
	}()
	select {
	case result := <-readDone:
		if result.err != nil {
			f.t.Fatalf("read line: %v (got %q)", result.err, result.text)
		}
		return strings.TrimSuffix(result.text, "\n")
	case <-time.After(10 * time.Second):
		f.t.Fatal("frontend produced no line within the deadline")
		return ""
	}
}

func (f *frontend) finish() error {
	f.t.Helper()
	if err := f.stdin.Close(); err != nil {
		f.t.Fatalf("close stdin: %v", err)
	}
	select {
	case err := <-f.done:
		return err
	case <-time.After(10 * time.Second):
		f.t.Fatal("frontend did not stop after stdin closed")
		return nil
	}
}

// expectResponse reads the next line and requires it to be the response for
// id; use where nothing else can be in flight.
func (f *frontend) expectResponse(id int64) responseLine {
	f.t.Helper()
	response := f.decodeResponse(f.line())
	if response.ID != id {
		f.t.Fatalf("response id %d, want %d", response.ID, id)
	}
	return response
}

func (f *frontend) decodeResponse(line string) responseLine {
	f.t.Helper()
	if !strings.HasPrefix(line, `{"id":`) {
		f.t.Fatalf("line %q is not a response", line)
	}
	var response responseLine
	if err := json.Unmarshal([]byte(line), &response); err != nil {
		f.t.Fatalf("response line %q: %v", line, err)
	}
	return response
}

func requireOK(t *testing.T, response responseLine) {
	t.Helper()
	if !response.OK || response.Error != nil {
		t.Fatalf("response %+v is not ok", response)
	}
}

func requireCode(t *testing.T, response responseLine, code string) {
	t.Helper()
	if response.OK || response.Error == nil {
		t.Fatalf("response %+v succeeded, want error %s", response, code)
	}
	if response.Error.Code != code {
		t.Fatalf("response error %s (%s), want %s", response.Error.Code, response.Error.Message, code)
	}
}

// --- the adapters op over the codec ---

// TestAdaptersOpListsRegistry proves the one op end to end over the codec:
// the registry listing arrives as a correlated ok response, one LF-terminated
// line, carrying the adapter's descriptor.
func TestAdaptersOpListsRegistry(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})
	f.send(`{"id":7,"op":"adapters"}`)
	response := f.expectResponse(7)
	requireOK(t, response)
	var listing struct {
		Adapters []adapterInfo `json:"adapters"`
	}
	if err := json.Unmarshal(response.Result, &listing); err != nil {
		t.Fatalf("adapters result %s: %v", response.Result, err)
	}
	if len(listing.Adapters) != 1 || listing.Adapters[0].Name != "memory" {
		t.Fatalf("adapters listing %+v", listing.Adapters)
	}
	if listing.Adapters[0].CapabilityRevision != "reference-memory-v1" {
		t.Fatalf("capability revision %q", listing.Adapters[0].CapabilityRevision)
	}
	if caps := listing.Adapters[0].Capabilities; caps == nil || caps.Endpoint.ID != "reference.memory" {
		t.Fatalf("capabilities not relayed: %+v", caps)
	}
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestAdaptersOpRefusesParams pins the closed shape: a well-formed line
// carrying a param the op does not define is a request error, not a framing
// defect — the frontend stays up. Presence is the rule: a supplied-but-empty
// or null param is still supplied.
func TestAdaptersOpRefusesParams(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})
	cases := []struct{ name, line string }{
		{"value param", `{"id":1,"op":"adapters","session_id":"s"}`},
		{"empty param", `{"id":1,"op":"adapters","adapter":""}`},
		{"null param", `{"id":1,"op":"adapters","after":null}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f.send(testCase.line)
			requireCode(t, f.expectResponse(1), "invalid_request")
		})
	}
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestUnknownOpIsARequestError names the boundary of the fail-closed rule:
// an op this frontend does not serve is a response the host can correct, and
// serving continues — only framing defects end Run.
func TestUnknownOpIsARequestError(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})
	// Re-cut A used the then-unimplemented state op here; the operations
	// slice implements it, so the fixture is an op no slice ever defines.
	f.send(`{"id":1,"op":"bogus"}`)
	requireCode(t, f.expectResponse(1), "unknown_op")
	f.send(`{"id":2,"op":"adapters"}`)
	requireOK(t, f.expectResponse(2))
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// --- fail-closed framing ---

// TestMalformedLinesFailClosed drives one valid request followed by each
// framing defect: the response for the valid request is still flushed, Run
// fails closed with the offending line number, and nothing else is emitted.
func TestMalformedLinesFailClosed(t *testing.T) {
	cases := []struct {
		name string
		line string
		raw  bool // written without the LF terminator
	}{
		{name: "not json", line: "not json"},
		{name: "array", line: `[1,2]`},
		{name: "string", line: `"adapters"`},
		{name: "number", line: `7`},
		{name: "missing op", line: `{"id":2}`},
		{name: "missing id", line: `{"op":"adapters"}`},
		{name: "null id", line: `{"id":null,"op":"adapters"}`},
		{name: "string id", line: `{"id":"2","op":"adapters"}`},
		{name: "fractional id", line: `{"id":2.5,"op":"adapters"}`},
		{name: "unknown field", line: `{"id":2,"op":"adapters","extra":1}`},
		{name: "trailing object", line: `{"id":2,"op":"adapters"} {"id":3}`},
		{name: "trailing closer", line: `{"id":2,"op":"adapters"}}`},
		{name: "duplicate key", line: `{"id":1,"id":2,"op":"adapters"}`},
		{name: "case-aliased id", line: `{"id":1,"ID":2,"op":"adapters"}`},
		{name: "case-aliased param", line: `{"id":1,"op":"adapters","Adapter":"ignored"}`},
		{name: "empty line", line: ""},
		{name: "carriage return", line: "{\"id\":2,\"op\":\"adapters\"}\r"},
		{name: "invalid utf8", line: "{\"id\":2,\"op\":\"\xff\"}"},
		{name: "unterminated", line: `{"id":2,"op":"adapters"`, raw: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			hub := newTestHub(t, 64, 64)
			f := startFrontend(t, hub, Options{})
			f.send(`{"id":1,"op":"adapters"}`)
			if testCase.raw {
				if _, err := f.stdin.Write([]byte(testCase.line)); err != nil {
					t.Fatal(err)
				}
			} else {
				f.send(testCase.line)
			}
			// The admitted request's response is flushed before the exit.
			response := f.decodeResponse(f.line())
			if response.ID != 1 || !response.OK {
				t.Fatalf("prior response not flushed: %+v", response)
			}
			err := f.finish()
			var malformed *MalformedLineError
			if !errors.As(err, &malformed) || malformed.Line != 2 {
				t.Fatalf("finish returned %v, want MalformedLineError on line 2", err)
			}
			if malformed.Detail == "" {
				t.Fatal("malformed error carries no detail")
			}
		})
	}
}

// TestOversizedLineFailsClosed bounds one request line with a small frame
// limit; the prior response still fits and is flushed.
func TestOversizedLineFailsClosed(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{FrameLimit: 1800})
	f.send(`{"id":1,"op":"adapters"}`)
	f.send(`{"id":2,"op":"adapters","pad":"` + strings.Repeat("x", 2000) + `"}`)
	response := f.decodeResponse(f.line())
	if response.ID != 1 || !response.OK {
		t.Fatalf("prior response not flushed: %+v", response)
	}
	err := f.finish()
	var malformed *MalformedLineError
	if !errors.As(err, &malformed) || malformed.Line != 2 {
		t.Fatalf("finish returned %v, want MalformedLineError on line 2", err)
	}
}

// TestContextEndInterruptsIdleInput pins the cancellation contract: a host
// that cancels the context without also closing stdin still gets Run back —
// the end is observed while waiting for the next line, not only between
// lines.
func TestContextEndInterruptsIdleInput(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	server, err := New(hub, Options{})
	if err != nil {
		t.Fatal(err)
	}
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	t.Cleanup(func() { stdinWriter.Close(); stdoutWriter.Close(); stdoutReader.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, stdinReader, stdoutWriter) }()
	f := &frontend{t: t, stdin: stdinWriter, reader: bufio.NewReader(stdoutReader), done: done}
	f.send(`{"id":1,"op":"adapters"}`)
	requireOK(t, f.expectResponse(1)) // serving, and now idle with stdin open
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil after cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not interrupt the idle input read")
	}
}

// blockedWriter parks every write until released, standing in for a host
// that stopped reading stdout.
type blockedWriter struct{ release chan struct{} }

func (b blockedWriter) Write([]byte) (int, error) {
	<-b.release
	return 0, io.ErrClosedPipe
}

// TestCancellationReturnsDespiteStoppedOutput drives the stalled-output
// scenario end to end: with a one-slot queue and a writer parked inside
// out.Write, the third response's send would park the serving loop forever;
// cancellation must still return Run inside the bounded drain window, with
// the abandoned writer reported as ErrShutdownStalled.
func TestCancellationReturnsDespiteStoppedOutput(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	server, err := New(hub, Options{WriteQueue: 1, ShutdownTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) }) // let the abandoned writer finish after the assertion
	stdinReader, stdinWriter := io.Pipe()
	t.Cleanup(func() { stdinWriter.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, stdinReader, blockedWriter{release: release}) }()
	for id := 1; id <= 3; id++ {
		if _, err := stdinWriter.Write([]byte(fmt.Sprintf(`{"id":%d,"op":"adapters"}`+"\n", id))); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrShutdownStalled) {
			t.Fatalf("Run returned %v, want ErrShutdownStalled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not return Run against the stopped output")
	}
}

// TestWriterFailureEndsServing drives the half-closed host: stdout fails
// while stdin stays open, and the frontend must stop admitting work and
// return the write failure instead of serving on with every response
// silently discarded.
func TestWriterFailureEndsServing(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	server, err := New(hub, Options{})
	if err != nil {
		t.Fatal(err)
	}
	stdinReader, stdinWriter := io.Pipe()
	t.Cleanup(func() { stdinWriter.Close() }) // stdin stays open through the assertion
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), stdinReader, failWriter{}) }()
	if _, err := stdinWriter.Write([]byte("{\"id\":1,\"op\":\"adapters\"}\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("Run returned %v, want the writer failure", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writer failure did not end serving while stdin stayed open")
	}
}

// failingReader fails every read with its own error, standing in for an OS
// or transport-level stdin failure.
type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

// TestReadFailureIsNotAMalformedLine pins the error boundary: a transport
// read failure fails the frontend closed but surfaces as itself — not as a
// MalformedLineError — so a host can tell its broken pipe from hostile
// input.
func TestReadFailureIsNotAMalformedLine(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	server, err := New(hub, Options{})
	if err != nil {
		t.Fatal(err)
	}
	readErr := errors.New("device gone")
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), failingReader{err: readErr}, io.Discard) }()
	select {
	case err := <-done:
		if !errors.Is(err, readErr) {
			t.Fatalf("Run returned %v, want the input's read failure", err)
		}
		var malformed *MalformedLineError
		if errors.As(err, &malformed) {
			t.Fatalf("read failure surfaced as a malformed line: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read failure did not end Run")
	}
}

// partialFailReader hands back one over-limit partial read together with
// its failure — the combined return io.Reader permits — so the codec must
// pass the failure through instead of judging the oversized fragment.
type partialFailReader struct {
	data []byte
	err  error
	done bool
}

func (r *partialFailReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	r.done = true
	return copy(p, r.data), r.err
}

// TestPartialReadFailurePassesThrough pins the ordering: a read failure
// carrying over-limit partial bytes surfaces as itself, never as the
// frame-limit defect that would blame the host.
func TestPartialReadFailurePassesThrough(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	server, err := New(hub, Options{FrameLimit: 256})
	if err != nil {
		t.Fatal(err)
	}
	readErr := errors.New("device gone")
	done := make(chan error, 1)
	go func() {
		done <- server.Run(context.Background(), &partialFailReader{data: bytes.Repeat([]byte("x"), 300), err: readErr}, io.Discard)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, readErr) {
			t.Fatalf("Run returned %v, want the input's read failure", err)
		}
		var malformed *MalformedLineError
		if errors.As(err, &malformed) {
			t.Fatalf("read failure surfaced as a malformed line: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read failure did not end Run")
	}
}

// TestCancelledRespondEmitsNoSizeRefusal pins the fallback guard: after
// cancellation a failed send — whether it aborted or raced the queue slot —
// is never answered with the bounded response_too_large refusal, which
// would blame the response's size for the context's end.
func TestCancelledRespondEmitsNoSizeRefusal(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	logs := &bytes.Buffer{}
	server, err := New(hub, Options{Logger: log.New(logs, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	lines := make(chan []byte, 4)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	id := int64(1)
	server.respond(ctx, lines, requestLine{ID: &id}, nil, nil)
	for {
		select {
		case line := <-lines:
			if bytes.Contains(line, []byte("response_too_large")) {
				t.Fatalf("cancelled respond emitted a size refusal: %s", line)
			}
			continue
		default:
		}
		break
	}
	if strings.Contains(logs.String(), "response_too_large") {
		t.Fatalf("cancelled respond attempted a size refusal: %s", logs.String())
	}
}

// TestFrameLimitFloorRejectsUnusableLimits guards the floor: a limit no
// correlated refusal could fit is rejected at construction, not discovered
// mid-session.
func TestFrameLimitFloorRejectsUnusableLimits(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	if _, err := New(hub, Options{FrameLimit: 64}); err == nil {
		t.Fatal("New accepted a frame limit no correlated refusal could fit")
	}
	if _, err := New(hub, Options{FrameLimit: 256}); err != nil {
		t.Fatalf("New rejected the floor: %v", err)
	}
}

// --- the outbound bound ---

// TestOversizedOutputRefused drives the outbound side of the frame limit: a
// response whose encoding exceeds it is replaced by the bounded
// response_too_large refusal, and small lines still flow after it.
func TestOversizedOutputRefused(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{FrameLimit: 1024})
	f.send(`{"id":1,"op":"adapters"}`) // the memory adapter's listing exceeds a small limit
	requireCode(t, f.expectResponse(1), "response_too_large")
	f.send(`{"id":2,"op":"adapters","session_id":"none"}`)
	requireCode(t, f.expectResponse(2), "invalid_request") // small lines still flow
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestErrorMessagesAreBounded guards the every-message-trimmed rule: a
// host-supplied identifier of any length comes back only inside the bounded
// message every refusal path applies.
func TestErrorMessagesAreBounded(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})
	f.send(fmt.Sprintf(`{"id":1,"op":%q}`, strings.Repeat("x", 5000)))
	response := f.expectResponse(1)
	requireCode(t, response, "unknown_op")
	// The construction bounds the host-supplied identifier at 300 runes plus
	// the ellipsis; the message adds only the fixed `no op "…"` wrapping.
	if runes := len([]rune(response.Error.Message)); runes > 310 {
		t.Fatalf("error message carries %d runes, over the trimmed bound", runes)
	}
	if !strings.Contains(response.Error.Message, "…") {
		t.Fatalf("error message %q is not marked as trimmed", response.Error.Message)
	}
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// --- the single writer ---

// TestWriterDrainsQueuedLinesOnStop pins the writer's contract at a clean
// end: lines already handed off are each written whole with their LF
// terminator, in order, before the writer returns.
func TestWriterDrainsQueuedLinesOnStop(t *testing.T) {
	var buf bytes.Buffer
	lines := make(chan []byte, 4)
	stop := make(chan struct{})
	failed := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- writeLines(&buf, lines, stop, failed) }()
	lines <- []byte(`{"id":1,"ok":true}`)
	lines <- []byte(`{"id":2,"ok":true}`)
	close(stop)
	if err := <-done; err != nil {
		t.Fatalf("writeLines returned %v", err)
	}
	if want := "{\"id\":1,\"ok\":true}\n{\"id\":2,\"ok\":true}\n"; buf.String() != want {
		t.Fatalf("writer drained %q, want %q", buf.String(), want)
	}
}

// failWriter refuses every write, standing in for a host that closed stdout.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

// TestWriterRemembersFailureAndKeepsDraining pins the failure contract: the
// first write failure is remembered and returned, while the drain keeps
// consuming queued lines so a producer blocked on the channel hands off
// instead of deadlocking.
func TestWriterRemembersFailureAndKeepsDraining(t *testing.T) {
	lines := make(chan []byte, 4)
	stop := make(chan struct{})
	failed := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- writeLines(failWriter{}, lines, stop, failed) }()
	lines <- []byte(`{"id":1,"ok":true}`) // the write fails; the failure is remembered
	lines <- []byte(`{"id":2,"ok":true}`) // dropped, but still consumed
	close(stop)
	if err := <-done; !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("writeLines returned %v, want the remembered write failure", err)
	}
	select {
	case <-failed:
	default:
		t.Fatal("the write failure was not signalled")
	}
	select {
	case line := <-lines:
		t.Fatalf("writer left %q unconsumed", line)
	default:
	}
}

// TestWriterNeverClosesTheLineChannel pins the never-closed rule: after the
// writer returns, an abandoned producer still parks on its send — neither
// completing nor panicking — because the channel was never closed.
func TestWriterNeverClosesTheLineChannel(t *testing.T) {
	var buf bytes.Buffer
	lines := make(chan []byte)
	stop := make(chan struct{})
	failed := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- writeLines(&buf, lines, stop, failed) }()
	close(stop)
	if err := <-done; err != nil {
		t.Fatalf("writeLines returned %v", err)
	}
	sent := make(chan struct{})
	go func() { lines <- []byte(`{"id":1}`); close(sent) }()
	select {
	case <-sent:
		t.Fatal("late send completed — the channel was closed or drained")
	case <-time.After(100 * time.Millisecond):
	}
}

// --- the operations surface ---

// openSession opens one tracked session on the hub directly — the stand-in
// for the open op until the registration slice lands it: the ops under test
// address sessions by id, and only the registration path is deferred.
func openSession(t *testing.T, hub *serve.Hub, id string) {
	t.Helper()
	_, _, err := hub.Open(context.Background(), "memory", base.OpenRequest{
		SessionID: protocol.SessionID(id), Participant: protocol.Participant{ID: serve.DefaultParticipant},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// requestEnvelope builds one schema-valid request envelope the way the Go
// client does, for driving the op surface from tests.
func requestEnvelope(t *testing.T, id string, typ protocol.EnvelopeType, payload any, sessionID string, runID string) json.RawMessage {
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

// readGate reads envelopes from one hub-side subscription until a gate of
// the wanted type arrives — the test counterpart of a host reading the run
// stream to answer an interaction, until the events op lands its slice.
func readGate(t *testing.T, subscription *serve.Subscription, want protocol.EnvelopeType) protocol.Envelope {
	t.Helper()
	type read struct {
		envelope protocol.Envelope
		err      error
	}
	for {
		done := make(chan read, 1)
		go func() {
			envelope, err := subscription.Next()
			done <- read{envelope: envelope, err: err}
		}()
		var result read
		select {
		case result = <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("subscription produced no %s within the deadline", want)
			return protocol.Envelope{}
		}
		if result.err != nil {
			t.Fatalf("read %s: %v", want, result.err)
		}
		if result.envelope.Type == want {
			return result.envelope
		}
	}
}

// resolveEnvelope echoes the interaction a gate envelope requested, the way
// the Go client resolves it: approve the permission, answer the input.
func resolveEnvelope(t *testing.T, id string, gate protocol.Envelope, sessionID string) json.RawMessage {
	t.Helper()
	switch gate.Type {
	case protocol.TypeActionPermissionRequested:
		var request protocol.PermissionRequestedPayload
		if err := gate.DecodePayload(&request); err != nil {
			t.Fatal(err)
		}
		return requestEnvelope(t, id, protocol.TypeActionPermissionResolveRequest, protocol.PermissionResolveRequest{
			InteractionID: request.InteractionID, SessionID: protocol.SessionID(sessionID), RunID: request.RunID,
			RequestedBy: request.RequestedBy, RespondedBy: request.RespondedBy, ChoiceID: "approve", Granted: true,
		}, sessionID, string(request.RunID))
	case protocol.TypeUserInputRequested:
		var request protocol.UserInputRequestedPayload
		if err := gate.DecodePayload(&request); err != nil {
			t.Fatal(err)
		}
		return requestEnvelope(t, id, protocol.TypeUserInputResolveRequest, protocol.UserInputResolveRequest{
			InteractionID: request.InteractionID, SessionID: protocol.SessionID(sessionID), RunID: request.RunID,
			RequestedBy: request.RequestedBy, RespondedBy: request.RespondedBy,
			Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}},
		}, sessionID, string(request.RunID))
	default:
		t.Fatalf("cannot resolve %s", gate.Type)
		return nil
	}
}

func requireEqualLines(t *testing.T, want, got []string) {
	t.Helper()
	for index, line := range got {
		if index >= len(want) {
			t.Fatalf("line %d unexpected:\n got %s\n", index+1, line)
		}
		if line != want[index] {
			t.Fatalf("line %d mismatch:\n got %s\nwant %s", index+1, line, want[index])
		}
	}
	if len(got) != len(want) {
		t.Fatalf("%d lines, want %d", len(got), len(want))
	}
}

// TestGoldenSessionTranscript drives one whole session across the op surface
// and compares each response line byte for byte. With dispatch still serial
// every request yields exactly one line in order; the run envelopes are read
// through a hub-side subscription, so the transcript is fully deterministic:
// the fake clock and id generator fix the envelope bytes, and sequential
// requests fix the daemon-minted ids.
func TestGoldenSessionTranscript(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})
	openSession(t, hub, "golden")
	subscription, err := hub.Subscribe(context.Background(), "golden")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	var transcript []string
	keep := func(lines ...string) { transcript = append(transcript, lines...) }

	f.send(`{"id":1,"op":"adapters"}`)
	keep(f.line())

	f.send(`{"id":2,"op":"capabilities","adapter":"memory"}`)
	keep(f.line())

	f.send(`{"id":3,"op":"submit","session_id":"golden","request":` + string(requestEnvelope(t, "submit-1", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "golden", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
	}, "golden", "")) + `}`)
	keep(f.line())

	f.send(`{"id":4,"op":"resolve","session_id":"golden","request":` + string(resolveEnvelope(t, "resolve-p", readGate(t, subscription, protocol.TypeActionPermissionRequested), "golden")) + `}`)
	keep(f.line())

	f.send(`{"id":5,"op":"resolve","session_id":"golden","request":` + string(resolveEnvelope(t, "resolve-i", readGate(t, subscription, protocol.TypeUserInputRequested), "golden")) + `}`)
	keep(f.line())

	f.send(`{"id":6,"op":"state","session_id":"golden"}`)
	keep(f.line())

	f.send(`{"id":7,"op":"close","session_id":"golden"}`)
	keep(f.line())

	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
	for _, line := range transcript {
		t.Logf("LINE %s", line)
	}
	requireEqualLines(t, goldenTranscript, transcript)
}

// goldenTranscript is the byte-exact transcript of
// TestGoldenSessionTranscript: minted frontend ids run in request order and
// the deterministic memory adapter fixes every envelope byte.
var goldenTranscript = []string{
	`{"id":1,"ok":true,"result":{"adapters":[{"name":"memory","capability_revision":"reference-memory-v1","capabilities":{"endpoint":{"id":"reference.memory","name":"Deterministic In-Memory Reference Adapter","version":"0.1","adapter":"process-memory-script"},"protocol_versions":["0.1"],"profiles":["open-agent-protocol.agent-control-core"],"features":{"action.permissions":{"level":"emulated","reason":"the reference adapter exposes an interactive scripted gate"},"action.tools":{"level":"emulated","reason":"the reference adapter projects the scripted tool lifecycle"},"action.tools.execute":{"level":"emulated","reason":"the reference adapter executes a fixed deterministic script"},"capabilities":{"level":"native"},"protocol.initialize":{"level":"native"},"run.cancel":{"level":"emulated","reason":"run-target API is implemented over a one-active-run session"},"run.reconciliation":{"level":"native"},"run.replay":{"level":"degraded","reason":"older cursors can expire and no cross-process replay is claimed"},"run.resume":{"level":"degraded","reason":"reattachment and replay use a bounded process-memory journal"},"run.status":{"level":"native"},"run.streaming":{"level":"native"},"session.message.delivery.auto":{"level":"native"},"session.message.submit":{"level":"native"},"session.open":{"level":"native"},"session.state":{"level":"native"},"user_input":{"level":"emulated","reason":"the reference adapter exposes an interactive scripted gate"}}}}]}}`,
	`{"id":2,"ok":true,"result":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"oap-response-2","payload":{"endpoint":{"id":"reference.memory","name":"Deterministic In-Memory Reference Adapter","version":"0.1","adapter":"process-memory-script"},"protocol_versions":["0.1"],"profiles":["open-agent-protocol.agent-control-core"],"features":{"action.permissions":{"level":"emulated","reason":"the reference adapter exposes an interactive scripted gate"},"action.tools":{"level":"emulated","reason":"the reference adapter projects the scripted tool lifecycle"},"action.tools.execute":{"level":"emulated","reason":"the reference adapter executes a fixed deterministic script"},"capabilities":{"level":"native"},"protocol.initialize":{"level":"native"},"run.cancel":{"level":"emulated","reason":"run-target API is implemented over a one-active-run session"},"run.reconciliation":{"level":"native"},"run.replay":{"level":"degraded","reason":"older cursors can expire and no cross-process replay is claimed"},"run.resume":{"level":"degraded","reason":"reattachment and replay use a bounded process-memory journal"},"run.status":{"level":"native"},"run.streaming":{"level":"native"},"session.message.delivery.auto":{"level":"native"},"session.message.submit":{"level":"native"},"session.open":{"level":"native"},"session.state":{"level":"native"},"user_input":{"level":"emulated","reason":"the reference adapter exposes an interactive scripted gate"}}},"in_reply_to":"oap-request-1","capability_revision":"reference-memory-v1"}}`,
	`{"id":3,"ok":true,"result":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.response","id":"oap-response-3","payload":{"session_id":"golden","accepted":true,"submission_id":"submission-06","requested_delivery":"auto","effective_delivery":"start","delivery_resolution":"session_idle","admission":"started","run_id":"run-01","status":"running","message_ids":["message-05"]},"in_reply_to":"submit-1","session_id":"golden","run_id":"run-01"}}`,
	`{"id":4,"ok":true,"result":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.permission.resolve.response","id":"oap-response-4","payload":{"interaction_id":"permission-02","session_id":"golden","run_id":"run-01","accepted":true},"in_reply_to":"resolve-p","session_id":"golden","run_id":"run-01"}}`,
	`{"id":5,"ok":true,"result":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolve.response","id":"oap-response-5","payload":{"interaction_id":"input-03","session_id":"golden","run_id":"run-01","accepted":true},"in_reply_to":"resolve-i","session_id":"golden","run_id":"run-01"}}`,
	`{"id":6,"ok":true,"result":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.state.response","id":"oap-response-6","payload":{"session_id":"golden","status":"idle","transcript_cursor":"12","updated_at_ms":16},"in_reply_to":"oap-request-7","session_id":"golden"}}`,
	`{"id":7,"ok":true,"result":null}`,
}

// TestSessionsOpListsTrackedSessions pins the listing op that completes the
// one-to-one servehttp mirror: entries in id order, closed sessions still
// listed, and the op as closed to params as every other.
func TestSessionsOpListsTrackedSessions(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})
	f.send(`{"id":1,"op":"sessions"}`)
	if result := f.expectResponse(1).Result; string(result) != `{"sessions":[]}` {
		t.Fatalf("empty listing: %s", result)
	}

	openSession(t, hub, "list-a")
	openSession(t, hub, "list-b")
	f.send(`{"id":2,"op":"close","session_id":"list-b"}`)
	requireOK(t, f.expectResponse(2))

	f.send(`{"id":3,"op":"sessions"}`)
	var listing struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
			Adapter   string `json:"adapter"`
			Status    string `json:"status"`
			CreatedAt string `json:"created_at"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(f.expectResponse(3).Result, &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Sessions) != 2 {
		t.Fatalf("%d listed sessions, want 2: %+v", len(listing.Sessions), listing.Sessions)
	}
	want := map[string]string{"list-a": "idle", "list-b": "closed"}
	for index, entry := range listing.Sessions {
		if entry.Adapter != "memory" || entry.CreatedAt == "" {
			t.Fatalf("entry %+v lacks adapter or creation time", entry)
		}
		if index == 0 && entry.SessionID != "list-a" || index == 1 && entry.SessionID != "list-b" {
			t.Fatalf("listing out of id order: %+v", listing.Sessions)
		}
		if entry.Status != want[entry.SessionID] {
			t.Fatalf("session %s listed as %s, want %s", entry.SessionID, entry.Status, want[entry.SessionID])
		}
	}

	f.send(`{"id":4,"op":"sessions","session_id":"list-a"}`)
	requireCode(t, f.expectResponse(4), "invalid_request")
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestOpErrorCodesMirrorHTTP drives every reachable refusal class and checks
// the code against the HTTP route's, the table the one-to-one claim rests
// on; the invalid_request and unknown_op rows are this framing's own line
// shape, with no HTTP counterpart to mirror.
func TestOpErrorCodesMirrorHTTP(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})
	nextID := int64(0)
	op := func(line string, code string) {
		t.Helper()
		nextID++
		f.send(strings.Replace(line, "%ID%", fmt.Sprint(nextID), 1))
		requireCode(t, f.expectResponse(nextID), code)
	}

	op(`{"id":%ID%,"op":"capabilities","adapter":"nope"}`, "unknown_adapter")
	op(`{"id":%ID%,"op":"state","session_id":"nope"}`, "unknown_session")
	op(`{"id":%ID%,"op":"close","session_id":"nope"}`, "unknown_session")
	op(`{"id":%ID%,"op":"submit","session_id":"nope","request":`+string(requestEnvelope(t, "s", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{SessionID: "nope", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}}}, "nope", ""))+`}`, "unknown_session")
	op(`{"id":%ID%,"op":"bogus"}`, "unknown_op")
	op(`{"id":%ID%,"op":"adapters","adapter":"memory"}`, "invalid_request")
	op(`{"id":%ID%,"op":"state","session_id":"s","after":3}`, "invalid_request")
	op(`{"id":%ID%,"op":"submit","session_id":"s"}`, "invalid_request")
	op(`{"id":%ID%,"op":"adapters","request":{}}`, "invalid_request")
	op(`{"id":%ID%,"op":"submit","session_id":"err","request":"not an envelope"}`, "malformed_json")
	op(`{"id":%ID%,"op":"submit","session_id":"err","request":{"type":"session.message.submit.request","payload":{}}}`, "schema_invalid")
	op(`{"id":%ID%,"op":"submit","session_id":"err","request":`+string(requestEnvelope(t, "w", protocol.TypeRunCancelRequest, protocol.RunCancelRequest{SessionID: "err", RunID: "run-9"}, "err", "run-9"))+`}`, "type_mismatch")

	// A real session makes the session-scoped refusals reachable.
	openSession(t, hub, "err")
	op(`{"id":%ID%,"op":"submit","session_id":"err","request":`+string(requestEnvelope(t, "s2", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "other", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}},
	}, "other", ""))+`}`, "scope_mismatch")
	op(`{"id":%ID%,"op":"cancel","session_id":"err","request":`+string(requestEnvelope(t, "c", protocol.TypeRunCancelRequest, protocol.RunCancelRequest{SessionID: "err", RunID: "run-99"}, "err", "run-99"))+`}`, "run_not_found")

	// An active run refuses close; cancelling it settles the session again.
	f.send(`{"id":110,"op":"submit","session_id":"err","request":` + string(requestEnvelope(t, "s3", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "err", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
	}, "err", "")) + `}`)
	response := f.expectResponse(110)
	requireOK(t, response)
	var admission protocol.MessageSubmitResponse
	if err := json.Unmarshal(response.Result, &admission); err != nil {
		t.Fatal(err)
	}
	op(`{"id":%ID%,"op":"close","session_id":"err"}`, "run_active")

	f.send(`{"id":120,"op":"cancel","session_id":"err","request":` + string(requestEnvelope(t, "c2", protocol.TypeRunCancelRequest, protocol.RunCancelRequest{SessionID: "err", RunID: admission.RunID}, "err", string(admission.RunID))) + `}`)
	requireOK(t, f.expectResponse(120))
	f.send(`{"id":130,"op":"close","session_id":"err"}`)
	requireOK(t, f.expectResponse(130))
	// A second close is idempotent success, mirroring the HTTP route's
	// bodyless 204 on a session the adapter already reports closed — only
	// work against a closed session reports it.
	f.send(`{"id":131,"op":"close","session_id":"err"}`)
	requireOK(t, f.expectResponse(131))
	op(`{"id":%ID%,"op":"submit","session_id":"err","request":`+string(requestEnvelope(t, "s4", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "err", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}},
	}, "err", ""))+`}`, "session_closed")
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestSubmitRollsBackUnframableAcknowledgement pins the side-effect custody
// rule: a submit whose acknowledgement cannot be framed must not leave the
// run live behind a generic refusal — it is cancelled, the refusal names the
// rollback, and the session accepts a fresh run afterwards.
func TestSubmitRollsBackUnframableAcknowledgement(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	openSession(t, hub, "rollback")
	// The correlation id is host-minted and echoed back as in_reply_to, so a
	// long one swells the acknowledgement past a limit the request itself
	// fits — the frame limit sits between the two sizes.
	longID := strings.Repeat("s", 400)
	line := `{"id":1,"op":"submit","session_id":"rollback","request":` + string(requestEnvelope(t, longID, protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "rollback", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
	}, "rollback", "")) + `}`
	f := startFrontend(t, hub, Options{FrameLimit: len(line) + 8})
	f.send(line)
	response := f.expectResponse(1)
	requireCode(t, response, "response_too_large")
	if !strings.Contains(response.Error.Message, "the run was cancelled") {
		t.Fatalf("refusal does not name the rollback: %s", response.Error.Message)
	}

	// The rollback really settled the run: a live run behind the refusal
	// would answer run_active here instead of admitting a second one.
	f.send(`{"id":2,"op":"submit","session_id":"rollback","request":` + string(requestEnvelope(t, "submit-2", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "rollback", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
	}, "rollback", "")) + `}`)
	second := f.expectResponse(2)
	requireOK(t, second)
	var admission protocol.MessageSubmitResponse
	if err := json.Unmarshal(second.Result, &admission); err != nil {
		t.Fatal(err)
	}
	f.send(`{"id":3,"op":"cancel","session_id":"rollback","request":` + string(requestEnvelope(t, "cancel-1", protocol.TypeRunCancelRequest, protocol.RunCancelRequest{SessionID: "rollback", RunID: admission.RunID}, "rollback", string(admission.RunID))) + `}`)
	requireOK(t, f.expectResponse(3))
	f.send(`{"id":4,"op":"close","session_id":"rollback"}`)
	requireOK(t, f.expectResponse(4))
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// neverSettlingAdapter wraps the memory adapter so Cancel acknowledges with
// run.cancelling but cancels nothing: the run stays parked at its gate, the
// asynchronous worst case the rollback must survive without claiming a
// settled cancel.
type neverSettlingAdapter struct{ inner base.Adapter }

func (a neverSettlingAdapter) Probe(ctx context.Context) (base.Descriptor, error) {
	return a.inner.Probe(ctx)
}

func (a neverSettlingAdapter) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	entry, err := a.inner.Open(ctx, request)
	if err != nil {
		return nil, err
	}
	return neverSettlingSession{entry}, nil
}

type neverSettlingSession struct{ base.Session }

func (s neverSettlingSession) Cancel(ctx context.Context, runID protocol.RunID) (protocol.RunCancelResponse, error) {
	state, err := s.Session.State(ctx)
	if err != nil {
		return protocol.RunCancelResponse{}, err
	}
	return protocol.RunCancelResponse{SessionID: state.SessionID, RunID: runID, Accepted: true, Status: protocol.RunCancelling}, nil
}

// TestSubmitRollbackWaitsForSettlement pins the asynchronous half of the
// rollback custody rule: a cancel that is only acknowledged — never settled
// — must be reported as unsettled, and the report must be honest about the
// run still being live to a host that cannot know its id.
func TestSubmitRollbackWaitsForSettlement(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("memory", neverSettlingAdapter{base.NewMemory(base.Config{Clock: &testClock{}, IDs: &testIDs{}, JournalCapacity: 64})}); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{StreamQueue: 64})
	openSession(t, hub, "unsettled")
	longID := strings.Repeat("s", 400)
	line := `{"id":1,"op":"submit","session_id":"unsettled","request":` + string(requestEnvelope(t, longID, protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "unsettled", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
	}, "unsettled", "")) + `}`
	f := startFrontend(t, hub, Options{FrameLimit: len(line) + 8, ShutdownTimeout: 100 * time.Millisecond})
	f.send(line)
	response := f.expectResponse(1)
	requireCode(t, response, "response_too_large")
	if !strings.Contains(response.Error.Message, "did not settle within the rollback window") {
		t.Fatalf("refusal does not report the unsettled rollback: %s", response.Error.Message)
	}

	// The report is honest: the run really is still live behind the refusal
	// the host cannot see the id of, exactly as the message warns.
	f.send(`{"id":2,"op":"submit","session_id":"unsettled","request":` + string(requestEnvelope(t, "submit-2", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "unsettled", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
	}, "unsettled", "")) + `}`)
	requireCode(t, f.expectResponse(2), "run_active")
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// --- the open and events registration ops ---

// lines reads count daemon lines in order.
func (f *frontend) lines(count int) []string {
	f.t.Helper()
	lines := make([]string, 0, count)
	for len(lines) < count {
		lines = append(lines, f.line())
	}
	return lines
}

// group reads count lines and partitions them into responses and event
// lines. The single writer makes every line atomic and preserves per-session
// event order, but responses and events interleave freely; tests therefore
// assert each class exactly and never the interleaving across classes.
func (f *frontend) group(count int) (responses []responseLine, events []string) {
	f.t.Helper()
	for _, line := range f.lines(count) {
		if strings.HasPrefix(line, `{"id":`) {
			var response responseLine
			if err := json.Unmarshal([]byte(line), &response); err != nil {
				f.t.Fatalf("response line %q: %v", line, err)
			}
			responses = append(responses, response)
			continue
		}
		var kind struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal([]byte(line), &kind); err != nil || kind.Event == "" {
			f.t.Fatalf("line %q is neither a response nor an event line", line)
		}
		events = append(events, line)
	}
	return responses, events
}

// resolveFromEvent builds the resolve request envelope for the interaction
// gate one event line delivered, the way a host reading the stream resolves
// it: approve the permission, answer the input.
func resolveFromEvent(t *testing.T, id string, gateLine string, sessionID string) json.RawMessage {
	t.Helper()
	var event envelopeLine
	if err := json.Unmarshal([]byte(gateLine), &event); err != nil {
		t.Fatalf("event line %q: %v", gateLine, err)
	}
	envelope, err := protocol.ParseEnvelope(event.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	return resolveEnvelope(t, id, envelope, sessionID)
}

// requireSequences asserts one phase's event lines carry the expected run
// sequences in order.
func requireSequences(t *testing.T, events []string, from, to uint64) {
	t.Helper()
	if uint64(len(events)) != to-from+1 {
		t.Fatalf("%d event lines, want %d", len(events), to-from+1)
	}
	for index, line := range events {
		var event envelopeLine
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("event line %q: %v", line, err)
		}
		if event.Event != signalEnvelope || event.Sequence == nil || *event.Sequence != from+uint64(index) {
			t.Fatalf("event line %q is not envelope sequence %d", line, from+uint64(index))
		}
	}
}

// runToCompletion drives one scripted memory run — submit, resolve the
// permission gate, resolve the input gate — against an already-subscribed
// session, reading the admitted envelopes in phases so every expected line is
// consumed. The scripted shapes are fixed: the submit admits four envelopes
// and parks; the permission resolution continues through five (the park's
// run.status.update included) and parks at the input gate; the input
// resolution settles the final three. It returns the run id.
func runToCompletion(t *testing.T, f *frontend, sessionID string, submitID, resolveA, resolveB int64) protocol.RunID {
	t.Helper()
	f.send(fmt.Sprintf(`{"id":%d,"op":"submit","session_id":%q,"request":%s}`, submitID, sessionID, requestEnvelope(t, "submit-1", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: protocol.SessionID(sessionID), Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
	}, sessionID, "")))
	responses, events := f.group(5)
	if len(responses) != 1 || len(events) != 4 {
		t.Fatalf("submit phase: %d responses, %d events", len(responses), len(events))
	}
	requireOK(t, responses[0])
	var admission protocol.MessageSubmitResponse
	if err := json.Unmarshal(responses[0].Result, &admission); err != nil {
		t.Fatal(err)
	}
	requireSequences(t, events, 1, 4)

	f.send(fmt.Sprintf(`{"id":%d,"op":"resolve","session_id":%q,"request":%s}`, resolveA, sessionID, resolveFromEvent(t, "resolve-p", events[3], sessionID)))
	responses, events = f.group(6)
	if len(responses) != 1 || len(events) != 5 {
		t.Fatalf("permission phase: %d responses, %d events", len(responses), len(events))
	}
	requireOK(t, responses[0])
	requireSequences(t, events, 5, 9)

	f.send(fmt.Sprintf(`{"id":%d,"op":"resolve","session_id":%q,"request":%s}`, resolveB, sessionID, resolveFromEvent(t, "resolve-i", events[3], sessionID)))
	responses, events = f.group(4)
	if len(responses) != 1 || len(events) != 3 {
		t.Fatalf("input phase: %d responses, %d events", len(responses), len(events))
	}
	requireOK(t, responses[0])
	requireSequences(t, events, 10, 12)
	if last := events[2]; !strings.Contains(last, `"run.completed"`) {
		t.Fatalf("run did not end on run.completed: %s", last)
	}
	return admission.RunID
}

// settleMemoryRun drives one scripted memory run to completion entirely on
// the hub side — submit and both gate resolutions through the session entry,
// the gates read through a hub-side subscription — for tests that need a
// settled, journaled run without crossing the frontend (a frame limit too
// small to carry the run's own envelopes, say).
func settleMemoryRun(t *testing.T, hub *serve.Hub, id string) {
	t.Helper()
	entry, err := hub.Session(protocol.SessionID(id))
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := hub.Subscribe(context.Background(), protocol.SessionID(id))
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	if _, err := entry.Submit(context.Background(), protocol.MessageSubmitRequest{
		SessionID: protocol.SessionID(id), Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []protocol.EnvelopeType{protocol.TypeActionPermissionRequested, protocol.TypeUserInputRequested} {
		gate := readGate(t, subscription, want)
		envelope, err := protocol.ParseEnvelope(resolveEnvelope(t, "resolve-"+string(want), gate, id))
		if err != nil {
			t.Fatal(err)
		}
		resolution := base.InteractionResolution{RunID: gate.RunID}
		switch gate.Type {
		case protocol.TypeActionPermissionRequested:
			var payload protocol.PermissionResolveRequest
			if err := envelope.DecodePayload(&payload); err != nil {
				t.Fatal(err)
			}
			resolution.RespondedBy = payload.RespondedBy
			resolution.Permission = &payload
		default:
			var payload protocol.UserInputResolveRequest
			if err := envelope.DecodePayload(&payload); err != nil {
				t.Fatal(err)
			}
			resolution.RespondedBy = payload.RespondedBy
			resolution.Input = &payload
		}
		if err := entry.Resolve(context.Background(), resolution); err != nil {
			t.Fatal(err)
		}
	}
}

// openViaOp opens one session through the open op — the registration path
// under test — and requires its acknowledgement.
func openViaOp(t *testing.T, f *frontend, id, adapter string, requestID int64) {
	t.Helper()
	f.send(fmt.Sprintf(`{"id":%d,"op":"open","adapter":%q,"request":%s}`, requestID, adapter, requestEnvelope(t, "open-1", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{SessionID: protocol.SessionID(id)}, "", "")))
	requireOK(t, f.expectResponse(requestID))
}

// TestPipelinedEventsBeforeSubmit sends the events op and the submit back to
// back without waiting for the events ack: the subscription is registered
// synchronously in the read loop, so the run's first envelope cannot be
// missed — the stdio counterpart of the client's subscribe-before-submit.
func TestPipelinedEventsBeforeSubmit(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})
	openViaOp(t, f, "pipe", "memory", 1)
	f.send(`{"id":2,"op":"events","session_id":"pipe"}`)
	f.send(`{"id":3,"op":"submit","session_id":"pipe","request":` + string(requestEnvelope(t, "submit-1", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "pipe", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
	}, "pipe", "")) + `}`)
	responses, events := f.group(6)
	if len(responses) != 2 || len(events) != 4 {
		t.Fatalf("pipelined phase: %d responses, %d events", len(responses), len(events))
	}
	for _, response := range responses {
		requireOK(t, response)
	}
	requireSequences(t, events, 1, 4)
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestEventsCursorReplay replays a settled run from a mid-run cursor: the
// after subscription delivers the retained suffix and ends at the terminal
// event with no further line, exactly as the SSE replay stream would.
func TestEventsCursorReplay(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})
	openViaOp(t, f, "replay", "memory", 1)
	f.send(`{"id":2,"op":"events","session_id":"replay"}`)
	requireOK(t, f.expectResponse(2))
	runToCompletion(t, f, "replay", 3, 4, 5)

	f.send(`{"id":6,"op":"events","session_id":"replay","after":4}`)
	requireOK(t, f.expectResponse(6))
	responses, events := f.group(8)
	if len(responses) != 0 || len(events) != 8 {
		t.Fatalf("replay: %d responses, %d events", len(responses), len(events))
	}
	requireSequences(t, events, 5, 12)

	// The replayed subscription ended at the run's terminal event with no
	// further line; a state op proves the frontend is still live.
	f.send(`{"id":7,"op":"state","session_id":"replay"}`)
	requireOK(t, f.expectResponse(7))
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestEventsGapAndResume drives a journal too small to retain a run's start,
// observes the oap-replay-gap signal, and resumes at the documented cursor —
// the acceptance path for recovering after a gap via after.
func TestEventsGapAndResume(t *testing.T) {
	hub := newTestHub(t, 2, 64)
	f := startFrontend(t, hub, Options{})
	openViaOp(t, f, "gap", "memory", 1)
	f.send(`{"id":2,"op":"events","session_id":"gap"}`)
	requireOK(t, f.expectResponse(2))
	runToCompletion(t, f, "gap", 3, 4, 5)

	// A cursor at zero asks for the whole run; the journal retains only its
	// tail, so the gap signal reports what is still available.
	f.send(`{"id":6,"op":"events","session_id":"gap","after":0}`)
	requireOK(t, f.expectResponse(6))
	signal := f.line()
	var gap gapLine
	if err := json.Unmarshal([]byte(signal), &gap); err != nil {
		t.Fatalf("gap line %q: %v", signal, err)
	}
	if gap.Event != signalReplayGap || gap.SessionID != "gap" || gap.ID != 6 {
		t.Fatalf("gap line %q is not a replay gap for the session", signal)
	}
	if gap.RequestedAfter != 0 || gap.OldestAvailable != 11 || gap.LatestAvailable != 12 {
		t.Fatalf("gap cursor bounds: %+v", gap)
	}

	// Resuming at oldest_available - 1 replays exactly the retained suffix.
	f.send(`{"id":7,"op":"events","session_id":"gap","after":10}`)
	requireOK(t, f.expectResponse(7))
	responses, events := f.group(2)
	if len(responses) != 0 || len(events) != 2 {
		t.Fatalf("resume: %d responses, %d events", len(responses), len(events))
	}
	requireSequences(t, events, 11, 12)
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestSessionClosedSignal parks one subscription with no run and closes the
// session: the close response and the oap-session-closed line may interleave
// freely, and both must arrive.
func TestSessionClosedSignal(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})
	openViaOp(t, f, "quiet", "memory", 1)
	f.send(`{"id":2,"op":"events","session_id":"quiet"}`)
	requireOK(t, f.expectResponse(2))

	f.send(`{"id":3,"op":"close","session_id":"quiet"}`)
	responses, events := f.group(2)
	if len(responses) != 1 || len(events) != 1 {
		t.Fatalf("close phase: %d responses, %d signals", len(responses), len(events))
	}
	requireOK(t, responses[0])
	if responses[0].ID != 3 {
		t.Fatalf("close response id %d", responses[0].ID)
	}
	var closed sessionClosedLine
	if err := json.Unmarshal([]byte(events[0]), &closed); err != nil {
		t.Fatalf("signal line %q: %v", events[0], err)
	}
	if closed.Event != signalSessionClosed || closed.SessionID != "quiet" || closed.ID != 2 {
		t.Fatalf("signal line %q is not a session-closed signal", events[0])
	}

	// A fresh subscription to the closed session is refused, mirroring the
	// HTTP 409 session_closed.
	f.send(`{"id":4,"op":"events","session_id":"quiet"}`)
	requireCode(t, f.expectResponse(4), "session_closed")
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestEventLinesCorrelateSubscriptions overlaps subscriptions on one session
// and requires every event and signal line to name the events request whose
// subscription produced it — the correlation stdout needs because, unlike
// separate SSE connections, the lines share one stream.
func TestEventLinesCorrelateSubscriptions(t *testing.T) {
	hub := newTestHub(t, 2, 64)
	f := startFrontend(t, hub, Options{})
	openViaOp(t, f, "multi", "memory", 1)

	// Two live subscriptions admitted before the run.
	f.send(`{"id":2,"op":"events","session_id":"multi"}`)
	requireOK(t, f.expectResponse(2))
	f.send(`{"id":3,"op":"events","session_id":"multi"}`)
	requireOK(t, f.expectResponse(3))
	f.send(`{"id":4,"op":"submit","session_id":"multi","request":` + string(requestEnvelope(t, "submit-1", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "multi", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
	}, "multi", "")) + `}`)
	responses, events := f.group(9)
	if len(responses) != 1 || len(events) != 8 {
		t.Fatalf("admission phase: %d responses, %d events", len(responses), len(events))
	}
	requireOK(t, responses[0])
	bySubscription := make(map[int64][]uint64)
	for _, line := range events {
		var event envelopeLine
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("event line %q: %v", line, err)
		}
		if event.SessionID != "multi" || event.Sequence == nil {
			t.Fatalf("event line %q lacks scope", line)
		}
		bySubscription[event.ID] = append(bySubscription[event.ID], *event.Sequence)
	}
	if len(bySubscription) != 2 {
		t.Fatalf("%d subscriptions attributed, want 2", len(bySubscription))
	}
	for id, sequences := range bySubscription {
		if len(sequences) != 4 {
			t.Fatalf("subscription %d delivered %d envelopes, want 4", id, len(sequences))
		}
		for index, sequence := range sequences {
			if sequence != uint64(index+1) {
				t.Fatalf("subscription %d delivered %v, want 1..4", id, sequences)
			}
		}
	}

	// Settle the run through both gates; both subscriptions deliver every
	// burst, interleaved but each attributed to its own id.
	var permission, input string
	for _, line := range events {
		if strings.Contains(line, `"sequence":4`) {
			permission = line
		}
	}
	f.send(`{"id":7,"op":"resolve","session_id":"multi","request":` + string(resolveFromEvent(t, "resolve-p", permission, "multi")) + `}`)
	responses, events = f.group(11)
	if len(responses) != 1 || len(events) != 10 {
		t.Fatalf("permission phase: %d responses, %d events", len(responses), len(events))
	}
	requireOK(t, responses[0])
	for _, line := range events {
		if strings.Contains(line, `"sequence":8`) {
			input = line
		}
	}
	f.send(`{"id":8,"op":"resolve","session_id":"multi","request":` + string(resolveFromEvent(t, "resolve-i", input, "multi")) + `}`)
	responses, events = f.group(7)
	if len(responses) != 1 || len(events) != 6 {
		t.Fatalf("input phase: %d responses, %d events", len(responses), len(events))
	}
	requireOK(t, responses[0])

	// Cursor subscriptions after the settled run: the gap and the replayed
	// suffix each name their own request.
	f.send(`{"id":5,"op":"events","session_id":"multi","after":0}`)
	requireOK(t, f.expectResponse(5))
	signal := f.line()
	var gap gapLine
	if err := json.Unmarshal([]byte(signal), &gap); err != nil {
		t.Fatalf("gap line %q: %v", signal, err)
	}
	if gap.Event != signalReplayGap || gap.ID != 5 {
		t.Fatalf("gap line %q does not name subscription 5", signal)
	}
	f.send(`{"id":6,"op":"events","session_id":"multi","after":10}`)
	requireOK(t, f.expectResponse(6))
	_, replayed := f.group(2)
	for index, line := range replayed {
		var event envelopeLine
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("replay line %q: %v", line, err)
		}
		if event.ID != 6 || event.Sequence == nil || *event.Sequence != uint64(11+index) {
			t.Fatalf("replay line %q does not name subscription 6 at sequence %d", line, 11+index)
		}
	}
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestOpenOpRefusesUnknownAdapterAndExistingSession pins the open op's
// error parity with the HTTP route it mirrors.
func TestOpenOpRefusesUnknownAdapterAndExistingSession(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})
	openViaOp(t, f, "dup", "memory", 1)
	f.send(`{"id":2,"op":"open","adapter":"none","request":` + string(requestEnvelope(t, "open-2", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{SessionID: "x"}, "", "")) + `}`)
	requireCode(t, f.expectResponse(2), "unknown_adapter")
	f.send(`{"id":3,"op":"open","adapter":"memory","request":` + string(requestEnvelope(t, "open-3", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{SessionID: "dup"}, "", "")) + `}`)
	requireCode(t, f.expectResponse(3), "session_exists")
	f.send(`{"id":4,"op":"open","request":` + string(requestEnvelope(t, "open-4", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{SessionID: "x"}, "", "")) + `}`)
	requireCode(t, f.expectResponse(4), "invalid_request")
	f.send(`{"id":5,"op":"events","session_id":"dup","after":"4"}`)
	requireCode(t, f.expectResponse(5), "invalid_cursor")
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// --- the staged adapter: overflow, oversized envelopes, rollback shapes ---

// creditOut is a consumer whose reading the test meters write by write: the
// frontend's writer may emit one line per granted credit, so a host that
// stops reading stdout — no further credits — stalls the pipeline exactly.
type creditOut struct {
	credits chan struct{}
	inner   io.Writer
}

func newCreditOut(inner io.Writer) *creditOut {
	return &creditOut{credits: make(chan struct{}, 64), inner: inner}
}

func (c *creditOut) grant(count int) {
	for index := 0; index < count; index++ {
		c.credits <- struct{}{}
	}
}

func (c *creditOut) Write(p []byte) (int, error) {
	<-c.credits
	return c.inner.Write(p)
}

// stagedAdapter is a test adapter whose run emits a first envelope burst,
// parks until the test releases it, then emits a second burst and reports
// the adapter-side event-stream overflow: the deterministic trigger for the
// daemon's oap-overflow signal while the consumer is stalled. The hub-side
// mailbox-drop path that produces the same signal is covered by the serve
// package's own tests; the frontend converges both onto one line.
type stagedAdapter struct {
	release chan struct{}
	// hugeAt, when nonzero, emits that sequence's envelope with a payload
	// far larger than the frame limit under test.
	hugeAt uint64
	// generatedID, when set, is minted for opens that name no session id —
	// an adapter whose generated identifiers can outrun a small frame
	// limit.
	generatedID string
	// closeFails makes Close fail, for the rollback-honesty shapes.
	closeFails bool
	// alreadyClosed makes Close report the session already closed — the
	// shape of an adapter whose child died between the open's confirmed
	// state and the rollback's close.
	alreadyClosed bool
}

func (a *stagedAdapter) Probe(context.Context) (base.Descriptor, error) {
	return base.Descriptor{
		Capabilities: protocol.CapabilityDescriptor{
			Endpoint:         protocol.EndpointDescriptor{ID: "staged.test", Name: "Staged test adapter", Version: "0.1", Adapter: "process-memory-script"},
			ProtocolVersions: []string{protocol.Version},
			Profiles:         []string{protocol.Profile},
		},
		CapabilityRevision: "staged-test-v1",
	}, nil
}

func (a *stagedAdapter) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	id := request.SessionID
	if id == "" {
		id = "staged-1"
		if a.generatedID != "" {
			id = protocol.SessionID(a.generatedID)
		}
	}
	return &stagedSession{
		adapter: a, id: id,
		state: protocol.SessionState{SessionID: id, Status: protocol.SessionIdle},
	}, nil
}

type stagedSession struct {
	adapter *stagedAdapter
	mu      sync.Mutex
	id      protocol.SessionID
	state   protocol.SessionState
	journal []protocol.Envelope
	settled bool
	closed  bool
}

func (s *stagedSession) Submit(_ context.Context, request protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, base.ErrSessionClosed
	}
	s.state.Status = protocol.SessionRunning
	s.state.ActiveRunID = "run-staged"
	s.mu.Unlock()
	stream := make(chan base.Result, 32)
	go func() {
		for sequence := uint64(1); sequence <= 4; sequence++ {
			stream <- base.Result{Envelope: s.emit(sequence)}
		}
		// Park: the test holds the second burst until the consumer stalls.
		<-s.adapter.release
		for sequence := uint64(5); sequence <= 8; sequence++ {
			stream <- base.Result{Envelope: s.emit(sequence)}
		}
		s.mu.Lock()
		s.settled = true
		s.state.Status = protocol.SessionIdle
		s.state.ActiveRunID = ""
		s.mu.Unlock()
		stream <- base.Result{Error: base.ErrEventStreamOverflow}
		close(stream)
	}()
	return protocol.MessageSubmitResponse{
		SessionID: s.id, Accepted: true, RunID: "run-staged", Status: protocol.RunRunning,
	}, stream, nil
}

func (s *stagedSession) emit(sequence uint64) protocol.Envelope {
	text := fmt.Sprintf("staged %d", sequence)
	if s.adapter.hugeAt == sequence {
		text = strings.Repeat("x", 8192)
	}
	envelope, err := protocol.NewEnvelope(protocol.TypeContentDelta, protocol.EnvelopeID(fmt.Sprintf("staged-%02d", sequence)), protocol.ContentDeltaPayload{
		MessageID: "staged-message",
		Part:      protocol.ContentPart{Type: protocol.ContentText, Text: text},
	})
	if err != nil {
		panic(err)
	}
	envelope.Sequence = &sequence
	envelope.SessionID = s.id
	envelope.RunID = "run-staged"
	s.mu.Lock()
	s.journal = append(s.journal, envelope)
	s.mu.Unlock()
	return envelope
}

func (s *stagedSession) State(context.Context) (protocol.SessionState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, nil
}

func (s *stagedSession) Resolve(context.Context, base.InteractionResolution) error { return nil }

func (s *stagedSession) Cancel(context.Context, protocol.RunID) (protocol.RunCancelResponse, error) {
	return protocol.RunCancelResponse{SessionID: s.id, RunID: "run-staged", Accepted: true, Status: protocol.RunCancelled}, nil
}

func (s *stagedSession) Resume(_ context.Context, request base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stream := make(chan base.Result, 32)
	recovery := base.Recovery{State: s.state, RunID: "run-staged", RequestedAfter: request.AfterSequence}
	for _, envelope := range s.journal {
		if envelope.Sequence != nil && *envelope.Sequence > request.AfterSequence {
			replayed := envelope
			stream <- base.Result{Envelope: replayed}
		}
	}
	if s.settled {
		close(stream)
	}
	return recovery, stream, nil
}

func (s *stagedSession) Close(context.Context) error {
	if s.adapter.closeFails {
		return errors.New("staged: close failed")
	}
	if s.adapter.alreadyClosed {
		return base.ErrSessionClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.state.Status = protocol.SessionClosed
	s.state.ActiveRunID = ""
	return nil
}

// startStagedFrontend serves one staged adapter through real pipes with the
// given options, for tests that meter or stall the consumer.
func startStagedFrontend(t *testing.T, staged *stagedAdapter, streamQueue int, options Options) *frontend {
	t.Helper()
	registry := serve.NewRegistry()
	if err := registry.Register("staged", staged); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{StreamQueue: streamQueue})
	server, err := New(hub, options)
	if err != nil {
		t.Fatal(err)
	}
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), stdinReader, stdoutWriter) }()
	return &frontend{t: t, stdin: stdinWriter, reader: bufio.NewReader(stdoutReader), done: done}
}

// TestSlowConsumerBackpressure stalls the consumer across a run's second
// envelope burst: the writer blocks on the ungranted credit, the line queue,
// the pump, and the bounded mailbox absorb only a bounded amount, and the
// adapter-reported overflow surfaces as the correlated oap-overflow signal
// with the resume cursor once the consumer returns; an events op resuming
// after the cursor replays cleanly.
func TestSlowConsumerBackpressure(t *testing.T) {
	staged := &stagedAdapter{release: make(chan struct{})}
	registry := serve.NewRegistry()
	if err := registry.Register("staged", staged); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{StreamQueue: 8})
	server, err := New(hub, Options{WriteQueue: 1})
	if err != nil {
		t.Fatal(err)
	}
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	out := newCreditOut(stdoutWriter)
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), stdinReader, out) }()
	f := &frontend{t: t, stdin: stdinWriter, reader: bufio.NewReader(stdoutReader), done: done}

	out.grant(2)
	f.send(`{"id":1,"op":"open","adapter":"staged","request":` + string(requestEnvelope(t, "open-1", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{SessionID: "slow"}, "", "")) + `}`)
	requireOK(t, f.expectResponse(1))
	f.send(`{"id":2,"op":"events","session_id":"slow"}`)
	requireOK(t, f.expectResponse(2))

	// The consumer keeps up through the first burst: five lines, granted.
	out.grant(5)
	f.send(`{"id":3,"op":"submit","session_id":"slow","request":` + string(requestEnvelope(t, "submit-1", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "slow", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
	}, "slow", "")) + `}`)
	responses, events := f.group(5)
	if len(responses) != 1 || len(events) != 4 {
		t.Fatalf("submit phase: %d responses, %d events", len(responses), len(events))
	}
	requireOK(t, responses[0])
	requireSequences(t, events, 1, 4)

	// The consumer stops reading; the second burst and the adapter's
	// overflow report flow into the bounded pipeline and stop there.
	close(staged.release)
	time.Sleep(100 * time.Millisecond)
	out.grant(16)

	lastSequence := uint64(4)
	cursor := uint64(0)
	deadline := time.After(10 * time.Second)
	for cursor == 0 {
		select {
		case <-deadline:
			t.Fatal("frontend did not drain after the consumer returned")
		default:
		}
		line := f.line()
		if strings.HasPrefix(line, `{"id":`) {
			continue
		}
		if strings.Contains(line, signalOverflow) {
			var signal overflowLine
			if err := json.Unmarshal([]byte(line), &signal); err != nil {
				t.Fatalf("overflow line %q: %v", line, err)
			}
			if signal.RunID != "run-staged" || signal.SessionID != "slow" || signal.ID != 2 {
				t.Fatalf("overflow signal %+v does not name the run", signal)
			}
			if signal.LastSequence != lastSequence {
				t.Fatalf("overflow cursor %d, want the last delivered sequence %d", signal.LastSequence, lastSequence)
			}
			cursor = signal.LastSequence
			continue
		}
		var event envelopeLine
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("event line %q: %v", line, err)
		}
		if *event.Sequence != lastSequence+1 {
			t.Fatalf("delivered sequence %d after %d: delivery is not contiguous", *event.Sequence, lastSequence)
		}
		lastSequence = *event.Sequence
	}
	if lastSequence != 8 {
		t.Fatalf("second burst delivered through sequence %d, want 8", lastSequence)
	}

	// The cursor at the burst's end replays nothing further; an earlier
	// cursor replays the retained suffix exactly.
	f.send(`{"id":4,"op":"events","session_id":"slow","after":6}`)
	requireOK(t, f.expectResponse(4))
	_, replayed := f.group(2)
	requireSequences(t, replayed, 7, 8)
	f.send(`{"id":5,"op":"state","session_id":"slow"}`)
	requireOK(t, f.expectResponse(5))
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestOversizedEnvelopeEndsWithFrameLimitTerminal drives a staged run whose
// second burst opens with an envelope no line can carry: the subscription
// ends with the correlated oap-frame-limit terminal naming the position a
// cursor resumes after, and nothing further arrives for it — the regression
// for the pump that once ended oversized envelopes silently.
func TestOversizedEnvelopeEndsWithFrameLimitTerminal(t *testing.T) {
	staged := &stagedAdapter{release: make(chan struct{}), hugeAt: 5}
	f := startStagedFrontend(t, staged, 8, Options{FrameLimit: 4096})

	f.send(`{"id":1,"op":"open","adapter":"staged","request":` + string(requestEnvelope(t, "open-1", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{SessionID: "big"}, "", "")) + `}`)
	requireOK(t, f.expectResponse(1))
	f.send(`{"id":2,"op":"events","session_id":"big"}`)
	requireOK(t, f.expectResponse(2))
	f.send(`{"id":3,"op":"submit","session_id":"big","request":` + string(requestEnvelope(t, "submit-1", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "big", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
	}, "big", "")) + `}`)
	responses, events := f.group(5)
	if len(responses) != 1 || len(events) != 4 {
		t.Fatalf("first burst: %d responses, %d events", len(responses), len(events))
	}
	requireOK(t, responses[0])
	requireSequences(t, events, 1, 4)

	close(staged.release)
	// The oversized envelope ends the subscription with the correlated
	// terminal; nothing further arrives for it.
	terminal := f.line()
	var limited frameLimitLine
	if err := json.Unmarshal([]byte(terminal), &limited); err != nil {
		t.Fatalf("terminal line %q: %v", terminal, err)
	}
	if limited.Event != signalFrameLimit || limited.ID != 2 || limited.SessionID != "big" || limited.RunID != "run-staged" || limited.Sequence != 5 {
		t.Fatalf("terminal line %q does not name the oversized position", terminal)
	}
	f.send(`{"id":4,"op":"state","session_id":"big"}`)
	requireOK(t, f.expectResponse(4))
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestOversizedOpenRollsBack drives an adapter whose generated session id
// outruns the frame limit: the open's acknowledgement cannot frame, so the
// session is closed again before the refusal is sent — the host never
// believes a session failed while it stays live behind an unknowable id.
func TestOversizedOpenRollsBack(t *testing.T) {
	staged := &stagedAdapter{generatedID: strings.Repeat("s", 400)}
	f := startStagedFrontend(t, staged, 8, Options{FrameLimit: 640})
	f.send(`{"id":1,"op":"open","adapter":"staged","request":` + string(requestEnvelope(t, "open-big", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{}, "", "")) + `}`)
	response := f.expectResponse(1)
	requireCode(t, response, "response_too_large")
	if !strings.Contains(response.Error.Message, "the session was rolled back") {
		t.Fatalf("refusal does not report the rollback: %s", response.Error.Message)
	}

	// The rolled-back session is listed with its final state, not live.
	f.send(`{"id":2,"op":"sessions"}`)
	var listing struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
			Status    string `json:"status"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(f.expectResponse(2).Result, &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Sessions) != 1 || listing.Sessions[0].Status != "closed" {
		t.Fatalf("rolled-back open left %+v, want one closed entry", listing.Sessions)
	}
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestOpenRollbackFailureIsReportedHonestly guards the rollback's custody:
// a close that fails must not be reported as a rollback that happened — the
// refusal names the possibly-live session instead.
func TestOpenRollbackFailureIsReportedHonestly(t *testing.T) {
	staged := &stagedAdapter{generatedID: strings.Repeat("s", 400), closeFails: true}
	f := startStagedFrontend(t, staged, 8, Options{FrameLimit: 640})
	f.send(`{"id":1,"op":"open","adapter":"staged","request":` + string(requestEnvelope(t, "open-big", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{}, "", "")) + `}`)
	response := f.expectResponse(1)
	requireCode(t, response, "response_too_large")
	if !strings.Contains(response.Error.Message, "rolling the session back failed and it may still be live") {
		t.Fatalf("refusal claims a rollback the adapter refused: %s", response.Error.Message)
	}

	// The report is honest: the session really is still live behind the
	// refusal, exactly as the message warns.
	f.send(`{"id":2,"op":"sessions"}`)
	var listing struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
			Status    string `json:"status"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(f.expectResponse(2).Result, &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Sessions) != 1 || listing.Sessions[0].Status != "idle" {
		t.Fatalf("refused open left %+v, want one live idle entry", listing.Sessions)
	}
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestOversizedGapSignalFallsBackToMinimal pins the gap signal's terminal
// fallback: a session address long enough that the full oap-replay-gap line
// would not frame — while the request line carrying it still does — gets
// the correlated minimal form with the cursor bounds the host cannot know
// on its own, at the frame-limit floor itself.
func TestOversizedGapSignalFallsBackToMinimal(t *testing.T) {
	hub := newTestHub(t, 2, 64)
	f := startFrontend(t, hub, Options{FrameLimit: minFrameLimit})
	longID := strings.Repeat("g", 190)
	openSession(t, hub, longID)
	settleMemoryRun(t, hub, longID)

	f.send(fmt.Sprintf(`{"id":5,"op":"events","session_id":%q,"after":0}`, longID))
	requireOK(t, f.expectResponse(5))
	signal := f.line()
	var gap gapLine
	if err := json.Unmarshal([]byte(signal), &gap); err != nil {
		t.Fatalf("gap line %q: %v", signal, err)
	}
	if gap.Event != signalReplayGap || gap.ID != 5 {
		t.Fatalf("minimal gap line %q does not name its subscription", signal)
	}
	if gap.SessionID != "" {
		t.Fatalf("minimal gap line %q still carries the oversized session address", signal)
	}
	if gap.OldestAvailable != 11 || gap.LatestAvailable != 12 || gap.RequestedAfter != 0 {
		t.Fatalf("minimal gap line %q lost its cursor bounds", signal)
	}
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestOversizedSessionClosedSignalFallsBackToMinimal pins the session-closed
// signal's terminal fallback: the minimal form carries the correlation the
// host cannot do without when the full line's session address would not
// frame.
func TestOversizedSessionClosedSignalFallsBackToMinimal(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{FrameLimit: minFrameLimit})
	longID := strings.Repeat("c", 190)
	openSession(t, hub, longID)

	f.send(fmt.Sprintf(`{"id":2,"op":"events","session_id":%q}`, longID))
	requireOK(t, f.expectResponse(2))
	f.send(fmt.Sprintf(`{"id":3,"op":"close","session_id":%q}`, longID))
	requireOK(t, f.expectResponse(3))
	signal := f.line()
	var closed sessionClosedLine
	if err := json.Unmarshal([]byte(signal), &closed); err != nil {
		t.Fatalf("signal line %q: %v", signal, err)
	}
	if closed.Event != signalSessionClosed || closed.ID != 2 {
		t.Fatalf("minimal signal line %q does not name its subscription", signal)
	}
	if closed.SessionID != "" {
		t.Fatalf("minimal signal line %q still carries the oversized session address", signal)
	}
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestOpenRollbackOfAlreadyClosedSession guards the rollback's
// already-closed branch: an adapter whose session died between the open's
// confirmation and the rollback reports ErrSessionClosed from Close — the
// outcome the rollback sought — so the refusal still reports the session as
// rolled back, and the hub-side entry is closed: a fresh events op is
// refused rather than parking forever on a session that can never run
// again.
func TestOpenRollbackOfAlreadyClosedSession(t *testing.T) {
	staged := &stagedAdapter{generatedID: strings.Repeat("s", 400), alreadyClosed: true}
	f := startStagedFrontend(t, staged, 8, Options{FrameLimit: 640})
	f.send(`{"id":1,"op":"open","adapter":"staged","request":` + string(requestEnvelope(t, "open-big", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{}, "", "")) + `}`)
	response := f.expectResponse(1)
	requireCode(t, response, "response_too_large")
	if !strings.Contains(response.Error.Message, "the session was rolled back") {
		t.Fatalf("refusal does not report the rollback: %s", response.Error.Message)
	}

	// The entry is closed hub-side — the adapter's state read still races
	// the death it discovered at close, so the listing is not the marker; a
	// fresh subscription on the entry is refused instead of parking forever
	// on a session that can never run again.
	f.send(fmt.Sprintf(`{"id":2,"op":"events","session_id":%q}`, staged.generatedID))
	requireCode(t, f.expectResponse(2), "session_closed")
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestMinimalSignalFormsAlwaysFit pins the guarantee the frame-limit floor
// buys for every signal's minimal fallback form: at the widest numeric ids
// and cursors, each minimal line encodes under the floor, so a subscription
// never ends uncorrelated. (The overflow form's full line is unreachable
// oversized with a coherent adapter — a subscription positioned to receive
// a run's overflow has been delivering that run's envelopes, whose lines
// are strictly larger than the signal — so it is pinned here rather than
// end to end; the gap, session-closed, and frame-limit fallbacks are driven
// end to end above.)
func TestMinimalSignalFormsAlwaysFit(t *testing.T) {
	widest := []any{
		frameLimitLine{Event: signalFrameLimit, ID: int64(1) << 62, Sequence: uint64(1) << 63},
		overflowLine{Event: signalOverflow, ID: int64(1) << 62, LastSequence: uint64(1) << 63},
		gapLine{Event: signalReplayGap, ID: int64(1) << 62, RequestedAfter: uint64(1) << 63, OldestAvailable: uint64(1) << 63, LatestAvailable: uint64(1) << 63},
		sessionClosedLine{Event: signalSessionClosed, ID: int64(1) << 62},
	}
	for _, minimal := range widest {
		line, err := json.Marshal(minimal)
		if err != nil {
			t.Fatal(err)
		}
		if len(line) > minFrameLimit {
			t.Fatalf("minimal signal %s encodes to %d bytes, over the %d-byte floor", line, len(line), minFrameLimit)
		}
	}
}

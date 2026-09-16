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
	"runtime"
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
	if listing.Adapters[0].CapabilityRevision != "reference-memory-v6" {
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
	f := startFrontend(t, hub, Options{FrameLimit: 3600})
	f.send(`{"id":1,"op":"adapters"}`)
	f.send(`{"id":2,"op":"adapters","pad":"` + strings.Repeat("x", 4000) + `"}`)
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
// that stopped reading stdout. entered, when supplied, is closed as the
// first write parks, so a test can wait for the writer to be genuinely
// stuck instead of assuming it won a race with its own next statement.
type blockedWriter struct {
	release chan struct{}
	entered chan struct{}
	once    *sync.Once
}

func (b blockedWriter) Write([]byte) (int, error) {
	if b.once != nil {
		b.once.Do(func() { close(b.entered) })
	}
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
	server, err := New(hub, Options{MaxConcurrentOps: 1, ShutdownTimeout: 100 * time.Millisecond})
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
	entered := make(chan struct{})
	writer := blockedWriter{release: release, entered: entered, once: &sync.Once{}}
	go func() { done <- server.Run(ctx, stdinReader, writer) }()
	for id := 1; id <= 3; id++ {
		if _, err := stdinWriter.Write([]byte(fmt.Sprintf(`{"id":%d,"op":"adapters"}`+"\n", id))); err != nil {
			t.Fatal(err)
		}
	}
	<-entered // the writer is genuinely parked inside out.Write, not merely about to be
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
	lines := make(chan outLine, 4)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	id := int64(1)
	server.respond(ctx, lines, requestLine{ID: &id}, nil, nil)
	for {
		select {
		case line := <-lines:
			if bytes.Contains(line.data, []byte("response_too_large")) {
				t.Fatalf("cancelled respond emitted a size refusal: %s", line.data)
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
	lines := make(chan outLine, 4)
	stop := make(chan struct{})
	abandon := make(chan struct{})
	failed := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- writeLines(&buf, lines, stop, abandon, failed, &outputFailure{}, &refusalTally{}) }()
	lines <- outLine{data: []byte(`{"id":1,"ok":true}`)}
	lines <- outLine{data: []byte(`{"id":2,"ok":true}`)}
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
	lines := make(chan outLine, 4)
	stop := make(chan struct{})
	abandon := make(chan struct{})
	failed := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- writeLines(failWriter{}, lines, stop, abandon, failed, &outputFailure{}, &refusalTally{})
	}()
	lines <- outLine{data: []byte(`{"id":1,"ok":true}`)} // the write fails; the failure is remembered
	lines <- outLine{data: []byte(`{"id":2,"ok":true}`)} // dropped, but still consumed
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
		t.Fatalf("writer left %q unconsumed", line.data)
	default:
	}
}

// TestWriterNeverClosesTheLineChannel pins the never-closed rule: after the
// writer returns, an abandoned producer still parks on its send — neither
// completing nor panicking — because the channel was never closed.
func TestWriterNeverClosesTheLineChannel(t *testing.T) {
	var buf bytes.Buffer
	lines := make(chan outLine)
	stop := make(chan struct{})
	abandon := make(chan struct{})
	failed := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- writeLines(&buf, lines, stop, abandon, failed, &outputFailure{}, &refusalTally{}) }()
	close(stop)
	if err := <-done; err != nil {
		t.Fatalf("writeLines returned %v", err)
	}
	sent := make(chan struct{})
	go func() { lines <- outLine{data: []byte(`{"id":1}`)}; close(sent) }()
	select {
	case <-sent:
		t.Fatal("late send completed — the channel was closed or drained")
	case <-time.After(100 * time.Millisecond):
	}
}

// --- the bounded shutdown lifecycle ---

// probeAdapter stands in for a process adapter whose handshake is slow or
// stuck: Probe waits for delay, or for hang when no delay is set, and
// ignores the context exactly as a child that stopped answering does. The
// capabilities op reaches it through the hub, so one request parks one
// admitted worker for as long as the test needs.
type probeAdapter struct {
	delay time.Duration
	hang  chan struct{}
}

func (a *probeAdapter) Probe(context.Context) (base.Descriptor, error) {
	if a.delay > 0 {
		time.Sleep(a.delay)
	} else {
		<-a.hang
	}
	return base.Descriptor{
		Capabilities: protocol.CapabilityDescriptor{
			Endpoint:         protocol.EndpointDescriptor{ID: "probe.test", Name: "Probing test adapter", Version: "0.1", Adapter: "process-memory-script"},
			ProtocolVersions: []string{protocol.Version},
			Profiles:         []string{protocol.Profile},
		},
		CapabilityRevision: "probe-test-v1",
	}, nil
}

func (a *probeAdapter) Open(context.Context, base.OpenRequest) (base.Session, error) {
	return nil, errors.New("probeAdapter opens no session")
}

func newProbeHub(t *testing.T, name string, adapter *probeAdapter) *serve.Hub {
	t.Helper()
	registry := serve.NewRegistry()
	if err := registry.Register(name, adapter); err != nil {
		t.Fatal(err)
	}
	return serve.New(registry, serve.Options{StreamQueue: 8})
}

// TestShutdownDoesNotWaitOnStalledOutput drives the host that closes stdin
// while it has stopped reading stdout: the writer is parked inside
// out.Write, so the final drain can never finish. Shutdown must abandon it
// inside the bounded window — a daemon that waits on the host's pipe is a
// daemon the host can pin open forever.
func TestShutdownDoesNotWaitOnStalledOutput(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	server, err := New(hub, Options{ShutdownTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) }) // let the abandoned writer finish after the assertion
	stdinReader, stdinWriter := io.Pipe()
	done := make(chan error, 1)
	entered := make(chan struct{})
	writer := blockedWriter{release: release, entered: entered, once: &sync.Once{}}
	go func() { done <- server.Run(context.Background(), stdinReader, writer) }()
	if _, err := stdinWriter.Write([]byte(`{"id":1,"op":"adapters"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	<-entered // the response has parked the writer inside out.Write
	if err := stdinWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrShutdownStalled) {
			t.Fatalf("Run returned %v, want ErrShutdownStalled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown hung on the stalled writer")
	}
}

// TestShutdownBoundedWhileWorkerStuck bounds the other stalled stage: an op
// whose adapter never answers holds its worker past the host's disconnect,
// and the wait for admitted workers must abandon it rather than outlive the
// session. The caller's own session sweep and the process exit depend on
// Run returning.
func TestShutdownBoundedWhileWorkerStuck(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) }) // release the abandoned probe after the assertion
	hub := newProbeHub(t, "hang", &probeAdapter{hang: hang})
	server, err := New(hub, Options{ShutdownTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stdinReader, stdinWriter := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), stdinReader, io.Discard) }()
	if _, err := stdinWriter.Write([]byte(`{"id":1,"op":"capabilities","adapter":"hang"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // let the worker reach the hung probe
	if err := stdinWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrShutdownStalled) {
			t.Fatalf("Run returned %v, want ErrShutdownStalled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown hung on the stuck worker")
	}
}

// TestShutdownWindowOpensAtDisconnect guards the window's anchor: it opens
// at the host's end of the session, never at Run's start, so a daemon that
// served longer than the window still grants a full one. The op here runs
// longer than the window itself — a window started eagerly at Run would
// already have expired and cut the answer off — and completes inside the
// window the disconnect opens.
func TestShutdownWindowOpensAtDisconnect(t *testing.T) {
	hub := newProbeHub(t, "slow", &probeAdapter{delay: 300 * time.Millisecond})
	server, err := New(hub, Options{ShutdownTimeout: 250 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), stdinReader, stdoutWriter) }()
	f := &frontend{t: t, stdin: stdinWriter, reader: bufio.NewReader(stdoutReader), done: done}

	f.send(`{"id":1,"op":"capabilities","adapter":"slow"}`)
	time.Sleep(150 * time.Millisecond) // the disconnect lands while the probe is still in flight
	if err := f.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	requireOK(t, f.expectResponse(1))
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want the op to finish inside the window its disconnect opened", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the op completed")
	}
}

// TestTeardownSettlesAdmittedWork pins what the fail-closed paths promise:
// work admitted before the host's end is waited for and flushed, not
// cancelled along with it. The disconnect follows the burst immediately, so
// every response here is one teardown had to wait for.
func TestTeardownSettlesAdmittedWork(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	server, err := New(hub, Options{})
	if err != nil {
		t.Fatal(err)
	}
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), stdinReader, stdoutWriter) }()

	collected := make(chan map[int64]bool, 1)
	go func() {
		reader := bufio.NewReader(stdoutReader)
		seen := map[int64]bool{}
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				var response responseLine
				if json.Unmarshal([]byte(strings.TrimSuffix(line, "\n")), &response) == nil {
					seen[response.ID] = true
				}
			}
			if err != nil {
				collected <- seen
				return
			}
		}
	}()

	const burst = 16
	for id := 1; id <= burst; id++ {
		if _, err := stdinWriter.Write([]byte(fmt.Sprintf(`{"id":%d,"op":"state","session_id":"none"}`+"\n", id))); err != nil {
			t.Fatal(err)
		}
	}
	if err := stdinWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want a clean end", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the disconnect")
	}
	if err := stdoutWriter.Close(); err != nil {
		t.Fatal(err)
	}
	seen := <-collected
	for id := int64(1); id <= burst; id++ {
		if !seen[id] {
			t.Fatalf("response %d was dropped by the teardown; %d of %d arrived", id, len(seen), burst)
		}
	}
}

// TestDisconnectIsObservedWhileAdmissionIsFull guards the bound against the
// contract it could have broken: with every in-flight slot held by an
// adapter that never answers, the serving loop is parked and takes no more
// frames, so the reader must still reach the stdin EOF behind them. It runs
// a frame ahead for exactly this, and without that the disconnect would
// never be observed and closing stdin would leave the process hung until an
// unrelated cancellation.
func TestDisconnectIsObservedWhileAdmissionIsFull(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) }) // release the abandoned probes after the assertion
	hub := newProbeHub(t, "hang", &probeAdapter{hang: hang})
	server, err := New(hub, Options{MaxConcurrentOps: 1, ShutdownTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stdinReader, stdinWriter := io.Pipe()
	done := make(chan error, 1)
	// No cancellation anywhere: the disconnect alone has to end this.
	go func() { done <- server.Run(context.Background(), stdinReader, io.Discard) }()
	// One request to hold the only slot, two more to park the loop behind
	// the bound with a frame still unread.
	for id := 1; id <= 3; id++ {
		if _, err := stdinWriter.Write([]byte(fmt.Sprintf(`{"id":%d,"op":"capabilities","adapter":"hang"}`+"\n", id))); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(50 * time.Millisecond) // let the loop reach the bound
	if err := stdinWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrShutdownStalled) {
			t.Fatalf("Run returned %v, want ErrShutdownStalled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the disconnect was never observed behind the saturated bound")
	}
}

// TestMalformedLineSurvivesASaturatedBound pins the fail-closed contract
// against the stuck path: a line this framing cannot carry yields
// *MalformedLineError with its line number whether or not the serving loop
// was parked behind the in-flight bound when it arrived. The same input
// must return the same error from Run, or a host debugging its own framing
// would be told only that shutdown stalled. The reader counts the lines it
// read, which is the count the loop keeps, so the number names the same
// line from either side.
func TestMalformedLineSurvivesASaturatedBound(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) }) // release the abandoned probes after the assertion
	hub := newProbeHub(t, "hang", &probeAdapter{hang: hang})
	server, err := New(hub, Options{MaxConcurrentOps: 1, ShutdownTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stdinReader, stdinWriter := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), stdinReader, io.Discard) }()
	for id := 1; id <= 3; id++ {
		if _, err := stdinWriter.Write([]byte(fmt.Sprintf(`{"id":%d,"op":"capabilities","adapter":"hang"}`+"\n", id))); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(50 * time.Millisecond) // let the loop park behind the bound
	// A defect the reader itself raises, since the loop that would raise a
	// decode failure is exactly what is parked: a carriage return is not
	// valid framing, and the reader is the one that sees it.
	if _, err := stdinWriter.Write([]byte("{\"id\":4,\"op\":\"adapters\"}\r\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		var malformed *MalformedLineError
		if !errors.As(err, &malformed) {
			t.Fatalf("Run returned %v, want the malformed line to survive the stall", err)
		}
		if malformed.Line != 4 {
			t.Fatalf("malformed line %d, want 4", malformed.Line)
		}
		if malformed.Detail == "" {
			t.Fatal("malformed error carries no detail")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return behind the saturated bound")
	}
}

// TestAdmissionBudgetsRequestBytes pins the half a count cannot express: a
// ceiling on ops is not a memory bound, because one admitted request can
// carry megabytes at the default frame limit, so a modest-looking ceiling
// still multiplies into gigabytes. Admission holds the product down, and a
// release is what lets the next one in.
func TestAdmissionBudgetsRequestBytes(t *testing.T) {
	run := newRunState(64, 1024)
	if got, _ := run.offer(900); got != admitted {
		t.Fatalf("the first offer got %v, want admitted", got)
	}
	if got, _ := run.offer(900); got != refused {
		t.Fatalf("offer past the byte budget got %v, want refused with the count ceiling nowhere near", got)
	}
	run.release(900)
	if got, _ := run.offer(900); got != admitted {
		t.Fatalf("offer after room appeared got %v, want admitted", got)
	}
	run.release(900)
}

// TestAdmissionAdmitsOneOversizeRequest pins the other edge: a request
// larger than the whole budget must still be served rather than refused
// against a bound it can never fit, so an idle registry admits anything.
func TestAdmissionAdmitsOneOversizeRequest(t *testing.T) {
	run := newRunState(64, 1024)
	if got, _ := run.offer(4096); got != admitted {
		t.Fatalf("an idle registry got %v for a request larger than its budget, want admitted", got)
	}
	run.release(4096)
}

// TestAbandonedWorkersLeaveNoWriterBehind pins what an abandoned stage owes
// the caller: a stuck worker whose window expires must not leave a writer
// holding the caller's output. An orphaned writer can emit a stale response
// after Run has returned — or into a later invocation that reuses the same
// stream — which is output corruption rather than a slow shutdown.
func TestAbandonedWorkersLeaveNoWriterBehind(t *testing.T) {
	hang := make(chan struct{})
	release := make(chan struct{})
	hub := newProbeHub(t, "hang", &probeAdapter{hang: hang})
	server, err := New(hub, Options{MaxConcurrentOps: 1, ShutdownTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stdinReader, stdinWriter := io.Pipe()
	var out lockedBuffer
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), stdinReader, &out) }()
	if _, err := stdinWriter.Write([]byte(`{"id":1,"op":"capabilities","adapter":"hang"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // let the worker reach the hung probe
	if err := stdinWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrShutdownStalled) {
			t.Fatalf("Run returned %v, want ErrShutdownStalled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown hung on the stuck worker")
	}
	settled := out.String()
	// Now let the abandoned worker finish. Nothing it produces may reach
	// the output Run has already handed back.
	close(hang)
	close(release)
	time.Sleep(200 * time.Millisecond)
	if after := out.String(); after != settled {
		t.Fatalf("the abandoned worker wrote %d bytes after Run returned", len(after)-len(settled))
	}
}

// lockedBuffer is an io.Writer a test can read while the writer goroutine
// may still hold it, which is the whole question above.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestWriterAbandonedCarriesNothingMore pins the difference between the two
// ways the writer can end. Stopped, it drains what is queued, which settles
// the work a clean teardown waited for. Abandoned, it carries nothing more
// — including a line already queued, because a teardown that gave up on its
// workers has handed the caller's output back and a late response written
// into it is corruption rather than lateness.
func TestWriterAbandonedCarriesNothingMore(t *testing.T) {
	var buf bytes.Buffer
	lines := make(chan outLine, 4)
	stop := make(chan struct{})
	abandon := make(chan struct{})
	failed := make(chan struct{}, 1)
	lines <- outLine{data: []byte(`{"id":1,"ok":true}`)} // queued, not yet taken
	close(abandon)
	done := make(chan error, 1)
	go func() { done <- writeLines(&buf, lines, stop, abandon, failed, &outputFailure{}, &refusalTally{}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("writeLines returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an abandoned writer did not end")
	}
	if buf.String() != "" {
		t.Fatalf("an abandoned writer wrote %q", buf.String())
	}
}

// gatedWriter parks its first write until released and then writes for
// real, standing in for a host that stopped reading stdout and started
// again later — after the daemon had given up on it.
type gatedWriter struct {
	release chan struct{}
	once    sync.Once
	entered chan struct{}

	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *gatedWriter) Write(data []byte) (int, error) {
	w.once.Do(func() {
		close(w.entered)
		<-w.release
	})
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(data)
}

func (w *gatedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestStalledDrainCarriesNothingLater closes the other half of the
// abandonment rule. The workers finished, so the teardown told the writer to
// drain — but the drain outlived its window with the writer parked inside
// out.Write. If that write later unblocks, the queue it was told to drain
// must not follow Run out of the door and into an output the caller has
// taken back.
func TestStalledDrainCarriesNothingLater(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	server, err := New(hub, Options{WriteQueue: 8, ShutdownTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	writer := &gatedWriter{release: make(chan struct{}), entered: make(chan struct{})}
	stdinReader, stdinWriter := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), stdinReader, writer) }()
	for id := 1; id <= 3; id++ {
		if _, err := stdinWriter.Write([]byte(fmt.Sprintf(`{"id":%d,"op":"adapters"}`+"\n", id))); err != nil {
			t.Fatal(err)
		}
	}
	<-writer.entered // the first response has parked the writer
	if err := stdinWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrShutdownStalled) {
			t.Fatalf("Run returned %v, want ErrShutdownStalled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown hung on the stalled drain")
	}
	close(writer.release) // the host starts reading again, too late
	time.Sleep(200 * time.Millisecond)
	// The line already inside out.Write completes: those bytes were in
	// flight before the abandonment and no daemon can recall them. What the
	// rule forbids is the rest of the queue following them out, so exactly
	// one line may appear where a continued drain would write three.
	lines := strings.Count(writer.String(), "\n")
	if lines > 1 {
		t.Fatalf("the abandoned drain wrote %d lines after Run returned, want at most the one in flight", lines)
	}
}

// TestStallAndMalformedLineBothSurvive pins that these are not
// alternatives. A line this framing cannot carry, arriving while a worker is
// wedged behind the in-flight bound, produces two separate conditions and a
// caller needs both: the line number it reports and exits on, and the stall
// that tells it this output must not be reused. Each is a different question
// errors.Is and errors.As are asked, so neither may stand in for the other.
func TestStallAndMalformedLineBothSurvive(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	hub := newProbeHub(t, "hang", &probeAdapter{hang: hang})
	server, err := New(hub, Options{MaxConcurrentOps: 1, ShutdownTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stdinReader, stdinWriter := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), stdinReader, io.Discard) }()
	for id := 1; id <= 3; id++ {
		if _, err := stdinWriter.Write([]byte(fmt.Sprintf(`{"id":%d,"op":"capabilities","adapter":"hang"}`+"\n", id))); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(50 * time.Millisecond) // let the loop park behind the bound
	if _, err := stdinWriter.Write([]byte("{\"id\":4,\"op\":\"adapters\"}\r\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		var malformed *MalformedLineError
		if !errors.As(err, &malformed) {
			t.Fatalf("Run returned %v, want the malformed line to survive", err)
		}
		if malformed.Line != 4 {
			t.Fatalf("malformed line %d, want 4", malformed.Line)
		}
		if !errors.Is(err, ErrShutdownStalled) {
			t.Fatalf("Run returned %v, want the stall to survive alongside it", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
}

// gatedFailWriter parks inside its first Write until it is released, then
// fails that write and every one after it. When the output dies is the whole
// question in these tests: a writer that fails on contact kills the stream
// before the in-flight bound is ever full, which is a different teardown
// entirely from one where the bound is saturated and the output is already
// broken.
type gatedFailWriter struct {
	release chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (w *gatedFailWriter) Write(data []byte) (int, error) {
	w.once.Do(func() {
		close(w.entered)
		<-w.release
	})
	return 0, io.ErrClosedPipe
}

// TestNoteKeepsEveryFactFindable pins the rule the teardown's error
// reporting rests on, independently of which interleavings happen to reach
// it. Every error Run returns names something that independently happened to
// the session, and errors.Is asked about any one of them is a separate
// question, so none may shadow another. Which pairs are reachable has
// already changed once as the admission model changed; that this holds for
// any pair must not.
func TestNoteKeepsEveryFactFindable(t *testing.T) {
	if got := note(nil, nil); got != nil {
		t.Fatalf("note(nil, nil) = %v, want nil", got)
	}
	if got := note(nil, ErrShutdownStalled); !errors.Is(got, ErrShutdownStalled) {
		t.Fatalf("note filled an empty account with %v", got)
	}
	if got := note(ErrRequestsDropped, nil); !errors.Is(got, ErrRequestsDropped) {
		t.Fatalf("note lost the account it was holding: %v", got)
	}
	// A fact already carried is stated once, not twice.
	doubled := note(ErrShutdownStalled, ErrShutdownStalled)
	if doubled != ErrShutdownStalled {
		t.Fatalf("note repeated a fact it already carried: %v", doubled)
	}
	// Two independent facts: both findable, and a typed one still unwraps.
	malformed := &MalformedLineError{Line: 4, Detail: "carriage return is not valid framing"}
	joined := note(note(error(malformed), io.ErrClosedPipe), ErrShutdownStalled)
	var found *MalformedLineError
	if !errors.As(joined, &found) || found.Line != 4 {
		t.Fatalf("errors.As lost the malformed line in %v", joined)
	}
	if !errors.Is(joined, io.ErrClosedPipe) {
		t.Fatalf("errors.Is lost the write failure in %v", joined)
	}
	if !errors.Is(joined, ErrShutdownStalled) {
		t.Fatalf("errors.Is lost the stall in %v", joined)
	}
}

// TestAbandonedTeardownStillReportsTheWriteFailure pins the account a
// caller is owed when both go wrong at once: one response has already
// failed to write while another worker is stuck, so the teardown abandons
// the stuck worker and never collects the writer's return. The write
// failure is why nothing reached the host, and losing it would leave the
// caller with only "shutdown stalled" for an output that was already dead.
func TestAbandonedTeardownStillReportsTheWriteFailure(t *testing.T) {
	registry := serve.NewRegistry()
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	if err := registry.Register("hang", &probeAdapter{hang: hang}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("memory", base.NewMemory(base.Config{Clock: &testClock{}, IDs: &testIDs{}, JournalCapacity: 64})); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{StreamQueue: 8})
	server, err := New(hub, Options{MaxConcurrentOps: 2, ShutdownTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stdinReader, stdinWriter := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), stdinReader, failWriter{}) }()
	// One op that answers, so its response fails the write; one that never
	// answers, so the worker wait is what times out.
	if _, err := stdinWriter.Write([]byte(`{"id":1,"op":"capabilities","adapter":"memory"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := stdinWriter.Write([]byte(`{"id":2,"op":"capabilities","adapter":"hang"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("Run returned %v, want the write failure", err)
		}
		if !errors.Is(err, ErrShutdownStalled) {
			t.Fatalf("Run returned %v, want the stall reported with it", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
}

// slowProbe answers after a delay, standing in for an adapter that is busy
// rather than broken.
type slowProbe struct{ delay time.Duration }

func (a *slowProbe) Probe(context.Context) (base.Descriptor, error) {
	time.Sleep(a.delay)
	return base.Descriptor{
		Capabilities: protocol.CapabilityDescriptor{
			Endpoint:         protocol.EndpointDescriptor{ID: "slow.test", Name: "Slow test adapter", Version: "0.1", Adapter: "process-memory-script"},
			ProtocolVersions: []string{protocol.Version},
			Profiles:         []string{protocol.Profile},
		},
		CapabilityRevision: "slow-test-v1",
	}, nil
}

func (a *slowProbe) Open(context.Context, base.OpenRequest) (base.Session, error) {
	return nil, errors.New("slowProbe opens no session")
}

// TestSlowWorkersBehindTheBoundAreAnswered pins what a saturated bound owes
// the host. The loop does not wait for room: a request it cannot admit is
// answered as busy and the loop reads on, so every request the host sent
// gets exactly one response, nothing is silently let go, and the session
// ends cleanly once the admitted work settles. Waiting instead would buy
// the host nothing it can act on and would stop the loop taking frames,
// which is how the host's own end becomes invisible.
func TestSlowWorkersBehindTheBoundAreAnswered(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("slow", &slowProbe{delay: 100 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{StreamQueue: 8})
	// The window comfortably outlasts the admitted op: this test is about
	// what the refused requests are told, not about a worker that overruns.
	server, err := New(hub, Options{MaxConcurrentOps: 1, ShutdownTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), stdinReader, stdoutWriter) }()
	answers := make(chan []responseLine, 1)
	go func() {
		decoder := json.NewDecoder(stdoutReader)
		var lines []responseLine
		for {
			var line responseLine
			if err := decoder.Decode(&line); err != nil {
				answers <- lines
				return
			}
			lines = append(lines, line)
		}
	}()
	// Three ops against a bound of one: the first is admitted and the other
	// two arrive while it is still running.
	for id := 1; id <= 3; id++ {
		if _, err := stdinWriter.Write([]byte(fmt.Sprintf(`{"id":%d,"op":"capabilities","adapter":"slow"}`+"\n", id))); err != nil {
			t.Fatal(err)
		}
	}
	if err := stdinWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want a clean end: every request was answered and the admitted work drained", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	if err := stdoutWriter.Close(); err != nil {
		t.Fatal(err)
	}
	lines := <-answers
	if len(lines) != 3 {
		t.Fatalf("%d responses reached the host, want one per request", len(lines))
	}
	busy := 0
	for _, line := range lines {
		if line.OK {
			continue
		}
		if line.Error == nil || line.Error.Code != "busy" {
			t.Fatalf("response %d failed with %+v, want the busy refusal", line.ID, line.Error)
		}
		busy++
	}
	if busy != 2 {
		t.Fatalf("%d requests were refused, want the two that did not fit", busy)
	}
}

// TestAdmissionClosesAtTeardown pins the gate itself, which no trace can
// show: once an invocation has closed admission, it refuses, so a serving
// loop abandoned mid-op that then dispatches one more frame cannot Add to a
// WaitGroup whose Wait has already returned — the misuse the race detector
// would report as a panic rather than a failed assertion.
//
// It also pins that a closed gate is not a busy one. The loop answers a
// refusal and reads on; it must not answer a teardown, because there is no
// longer an output to answer on and the session is being given up, not
// queued behind.
func TestAdmissionClosesAtTeardown(t *testing.T) {
	run := newRunState(1, 0)
	if got, _ := run.offer(1); got != admitted {
		t.Fatalf("admission got %v before the session began, want admitted", got)
	}
	if got, _ := run.offer(1); got != refused {
		t.Fatalf("a full gate got %v, want refused", got)
	}
	run.release(1)
	run.closeAdmission()
	if got, _ := run.offer(1); got != closedToWork {
		t.Fatalf("admission got %v after teardown, want closedToWork", got)
	}
}

// TestServerServesAgainAfterTeardown pins that the gate is per invocation:
// the state one session ends in is not a state the next session inherits,
// so a Server whose host disconnected still serves the host after it. A
// flag left set on the Server would leave the second host reading its own
// requests back as silence.
func TestServerServesAgainAfterTeardown(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	server, err := New(hub, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for session := 1; session <= 2; session++ {
		var out bytes.Buffer
		if err := server.Run(context.Background(), strings.NewReader(`{"id":1,"op":"adapters"}`+"\n"), &out); err != nil {
			t.Fatalf("session %d: Run returned %v", session, err)
		}
		if !strings.HasPrefix(out.String(), `{"id":1,"ok":true`) {
			t.Fatalf("session %d answered %q", session, out.String())
		}
	}
}

// TestInFlightOpsAreBounded guards the backpressure the concurrent dispatch
// could have thrown away: with the writer parked and the queue tiny, a host
// that keeps sending must not make the daemon hold its whole stream as
// goroutines, request frames and half-built responses. The serving loop
// stops reading at the bound instead, exactly as the serial dispatch it
// replaced did.
func TestInFlightOpsAreBounded(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	server, err := New(hub, Options{MaxConcurrentOps: 2, ShutdownTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) }) // let the abandoned writer finish after the assertion
	stdinReader, stdinWriter := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	baseline := runtime.NumGoroutine()
	go func() { done <- server.Run(ctx, stdinReader, blockedWriter{release: release}) }()
	fed := make(chan struct{})
	go func() {
		defer close(fed)
		for id := 1; id <= 500; id++ {
			if _, err := stdinWriter.Write([]byte(fmt.Sprintf(`{"id":%d,"op":"adapters"}`+"\n", id))); err != nil {
				return
			}
		}
	}()
	time.Sleep(250 * time.Millisecond) // long enough for an unbounded loop to admit the lot
	if grew := runtime.NumGoroutine() - baseline; grew > 64 {
		t.Fatalf("%d goroutines added against a bound of 2 in flight", grew)
	}
	// A loop parked at the bound has stopped reading, so the disconnect it
	// would read is not what ends this session: with the writer parked too,
	// nothing can make progress and cancellation is the escape — the same
	// bargain the serial dispatch made.
	cancel()
	if err := stdinWriter.Close(); err != nil {
		t.Fatal(err)
	}
	<-fed
	select {
	case err := <-done:
		if !errors.Is(err, ErrShutdownStalled) {
			t.Fatalf("Run returned %v, want ErrShutdownStalled against the parked writer", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not return Run from behind the bound")
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
// and compares each response line byte for byte. Dispatch is concurrent, so
// the transcript is deterministic because the host drives it that way — each
// response is read before the next request is sent, the way a host that
// depends on ordering must — and the run envelopes are read through a
// hub-side subscription: the fake clock and id generator fix the envelope
// bytes, and one request in flight at a time fixes the daemon-minted ids.
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

	f.send(`{"id":7,"op":"tools","session_id":"golden"}`)
	keep(f.line())

	f.send(`{"id":8,"op":"close","session_id":"golden"}`)
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
	`{"id":1,"ok":true,"result":{"adapters":[{"name":"memory","capability_revision":"reference-memory-v6","capabilities":{"endpoint":{"id":"reference.memory","name":"Deterministic In-Memory Reference Adapter","version":"0.1","adapter":"process-memory-script"},"protocol_versions":["0.1"],"profiles":["open-agent-protocol.agent-control-core"],"features":{"action.permissions":{"level":"emulated","reason":"the reference adapter exposes an interactive scripted gate"},"action.tool_sources.attach":{"level":"emulated","reason":"sources are described and published back; the reference adapter runs no client for them","modes":["session_open"],"limits":{"max_sources":2,"transports":["process","local"]}},"action.tools":{"level":"emulated","reason":"the reference adapter projects the scripted tool lifecycle"},"action.tools.execute":{"level":"emulated","reason":"the reference adapter executes a fixed deterministic script"},"action.tools.list":{"level":"emulated","reason":"the reference catalog is the scripted tool plus the session's attached sources"},"capabilities":{"level":"native"},"models.list":{"level":"native","reason":"the reference adapter serves its fixed catalog, which is exactly the set its model gate admits"},"protocol.initialize":{"level":"native"},"run.cancel":{"level":"emulated","reason":"run-target API is implemented over a one-active-run session"},"run.instructions":{"level":"emulated","reason":"instructions are prepended to the scripted text so their effect is observable"},"run.model_selection":{"level":"emulated","reason":"the reference adapter runs no model; it echoes a selection from a fixed catalog for one run","mode":"per_run"},"run.reconciliation":{"level":"native"},"run.replay":{"level":"degraded","reason":"older cursors can expire and no cross-process replay is claimed"},"run.resume":{"level":"degraded","reason":"reattachment and replay use a bounded process-memory journal"},"run.status":{"level":"native"},"run.streaming":{"level":"native"},"run.structured_output":{"level":"emulated","reason":"the scripted result is fixed, so only a schema that object satisfies is admitted","constraints":{"fixed_result":{"ok":true}}},"run.tool_selection":{"level":"emulated","reason":"the policy selects whether the scripted tool is called","modes":["auto","none","required","named"]},"session.message.delivery.auto":{"level":"native"},"session.message.delivery.queue":{"level":"emulated","reason":"a busy session reserves one second run and promotes it when the started run settles"},"session.message.submit":{"level":"native"},"session.open":{"level":"native"},"session.state":{"level":"native"},"user_input":{"level":"emulated","reason":"the reference adapter exposes an interactive scripted gate"}},"tools":[{"name":"scripted_tool","description":"The deterministic scripted tool the reference adapter calls.","input_schema":{"type":"object","properties":{"operation":{"type":"string"}}},"execution_owner":"reference-adapter","source":"reference-native","features":{"action.permissions":{"level":"emulated","reason":"the scripted call is gated"},"action.tools.execute":{"level":"emulated","reason":"the reference adapter executes a fixed deterministic script"}}}],"sources":[{"id":"reference-native","kind":"native","display_name":"Reference Adapter Script"},{"id":"reference-mcp","kind":"process","display_name":"Reference Synthetic MCP Source","protocol":"mcp","endpoint":"stdio:reference-tool-source"}],"limits":{"max_active_runs_per_session":2,"max_queued_runs_per_session":1}}}]}}`,
	`{"id":2,"ok":true,"result":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"oap-response-2","payload":{"endpoint":{"id":"reference.memory","name":"Deterministic In-Memory Reference Adapter","version":"0.1","adapter":"process-memory-script"},"protocol_versions":["0.1"],"profiles":["open-agent-protocol.agent-control-core"],"features":{"action.permissions":{"level":"emulated","reason":"the reference adapter exposes an interactive scripted gate"},"action.tool_sources.attach":{"level":"emulated","reason":"sources are described and published back; the reference adapter runs no client for them","modes":["session_open"],"limits":{"max_sources":2,"transports":["process","local"]}},"action.tools":{"level":"emulated","reason":"the reference adapter projects the scripted tool lifecycle"},"action.tools.execute":{"level":"emulated","reason":"the reference adapter executes a fixed deterministic script"},"action.tools.list":{"level":"emulated","reason":"the reference catalog is the scripted tool plus the session's attached sources"},"capabilities":{"level":"native"},"models.list":{"level":"native","reason":"the reference adapter serves its fixed catalog, which is exactly the set its model gate admits"},"protocol.initialize":{"level":"native"},"run.cancel":{"level":"emulated","reason":"run-target API is implemented over a one-active-run session"},"run.instructions":{"level":"emulated","reason":"instructions are prepended to the scripted text so their effect is observable"},"run.model_selection":{"level":"emulated","reason":"the reference adapter runs no model; it echoes a selection from a fixed catalog for one run","mode":"per_run"},"run.reconciliation":{"level":"native"},"run.replay":{"level":"degraded","reason":"older cursors can expire and no cross-process replay is claimed"},"run.resume":{"level":"degraded","reason":"reattachment and replay use a bounded process-memory journal"},"run.status":{"level":"native"},"run.streaming":{"level":"native"},"run.structured_output":{"level":"emulated","reason":"the scripted result is fixed, so only a schema that object satisfies is admitted","constraints":{"fixed_result":{"ok":true}}},"run.tool_selection":{"level":"emulated","reason":"the policy selects whether the scripted tool is called","modes":["auto","none","required","named"]},"session.message.delivery.auto":{"level":"native"},"session.message.delivery.queue":{"level":"emulated","reason":"a busy session reserves one second run and promotes it when the started run settles"},"session.message.submit":{"level":"native"},"session.open":{"level":"native"},"session.state":{"level":"native"},"user_input":{"level":"emulated","reason":"the reference adapter exposes an interactive scripted gate"}},"tools":[{"name":"scripted_tool","description":"The deterministic scripted tool the reference adapter calls.","input_schema":{"type":"object","properties":{"operation":{"type":"string"}}},"execution_owner":"reference-adapter","source":"reference-native","features":{"action.permissions":{"level":"emulated","reason":"the scripted call is gated"},"action.tools.execute":{"level":"emulated","reason":"the reference adapter executes a fixed deterministic script"}}}],"sources":[{"id":"reference-native","kind":"native","display_name":"Reference Adapter Script"},{"id":"reference-mcp","kind":"process","display_name":"Reference Synthetic MCP Source","protocol":"mcp","endpoint":"stdio:reference-tool-source"}],"limits":{"max_active_runs_per_session":2,"max_queued_runs_per_session":1}},"in_reply_to":"oap-request-1","capability_revision":"reference-memory-v6"}}`,
	`{"id":3,"ok":true,"result":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.response","id":"oap-response-3","payload":{"session_id":"golden","accepted":true,"submission_id":"submission-06","requested_delivery":"auto","effective_delivery":"start","delivery_resolution":"session_idle","admission":"started","run_id":"run-01","status":"running","message_ids":["message-05"]},"in_reply_to":"submit-1","session_id":"golden","run_id":"run-01"}}`,
	`{"id":4,"ok":true,"result":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.permission.resolve.response","id":"oap-response-4","payload":{"interaction_id":"permission-02","session_id":"golden","run_id":"run-01","accepted":true},"in_reply_to":"resolve-p","session_id":"golden","run_id":"run-01"}}`,
	`{"id":5,"ok":true,"result":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolve.response","id":"oap-response-5","payload":{"interaction_id":"input-03","session_id":"golden","run_id":"run-01","accepted":true},"in_reply_to":"resolve-i","session_id":"golden","run_id":"run-01"}}`,
	`{"id":6,"ok":true,"result":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.state.response","id":"oap-response-6","payload":{"session_id":"golden","status":"idle","transcript_cursor":"12","updated_at_ms":16,"sources":[{"id":"reference-native","kind":"native","display_name":"Reference Adapter Script"},{"id":"reference-mcp","kind":"process","display_name":"Reference Synthetic MCP Source","protocol":"mcp","endpoint":"stdio:reference-tool-source"}]},"in_reply_to":"oap-request-7","session_id":"golden"}}`,
	`{"id":7,"ok":true,"result":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.tools.list.response","id":"oap-response-8","payload":{"session_id":"golden","sources":[{"id":"reference-native","kind":"native","display_name":"Reference Adapter Script"},{"id":"reference-mcp","kind":"process","display_name":"Reference Synthetic MCP Source","protocol":"mcp","endpoint":"stdio:reference-tool-source"}],"tools":[{"name":"scripted_tool","description":"The deterministic scripted tool the reference adapter calls.","input_schema":{"type":"object","properties":{"operation":{"type":"string"}}},"execution_owner":"reference-adapter","source":"reference-native","features":{"action.permissions":{"level":"emulated","reason":"the scripted call is gated"},"action.tools.execute":{"level":"emulated","reason":"the reference adapter executes a fixed deterministic script"}}}]},"in_reply_to":"oap-request-9","session_id":"golden","capability_revision":"reference-memory-v6"}}`,
	`{"id":8,"ok":true,"result":null}`,
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

// noControlsAdapter wraps the memory adapter as an endpoint that advertises no
// per-submit run control, the position most harness adapters hold: every
// control is refused before admission under its own capability key.
type noControlsAdapter struct{ inner base.Adapter }

func (a noControlsAdapter) Probe(ctx context.Context) (base.Descriptor, error) {
	return a.inner.Probe(ctx)
}

func (a noControlsAdapter) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	entry, err := a.inner.Open(ctx, request)
	if err != nil {
		return nil, err
	}
	return noControlsSession{entry}, nil
}

type noControlsSession struct{ base.Session }

func (s noControlsSession) Submit(ctx context.Context, request protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	if err := base.RefuseUnadvertisedControls(request); err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}
	return s.Session.Submit(ctx, request)
}

// TestUnadvertisedControlRefusalKeepsItsWireShape pins what a caller reads off
// the wire when an endpoint refuses a control it never advertised: the typed
// unsupported_feature code with the capability key and the reason, not a
// generic invalid_submission that names neither. The codec must relay the
// adapter's typed refusal rather than flattening it, and the run must not be
// admitted behind it.
func TestUnadvertisedControlRefusalKeepsItsWireShape(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("memory", noControlsAdapter{base.NewMemory(base.Config{Clock: &testClock{}, IDs: &testIDs{}, JournalCapacity: 64})}); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{StreamQueue: 64})
	openSession(t, hub, "controls")
	f := startFrontend(t, hub, Options{})
	id := int64(0)
	for _, control := range []struct {
		feature string
		request protocol.MessageSubmitRequest
	}{
		{protocol.FeatureInstructions, protocol.MessageSubmitRequest{Instructions: protocol.ControlValue("be terse")}},
		{protocol.FeatureModelSelection, protocol.MessageSubmitRequest{ModelID: protocol.ControlValue("another-model")}},
		{protocol.FeatureStructuredOutput, protocol.MessageSubmitRequest{OutputSchema: json.RawMessage(`{"type":"object"}`)}},
		{protocol.FeatureToolSelection, protocol.MessageSubmitRequest{ToolChoice: json.RawMessage(`"none"`)}},
	} {
		id++
		request := control.request
		request.SessionID = "controls"
		request.Delivery = protocol.DeliveryAuto
		request.Messages = []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}}
		f.send(fmt.Sprintf(`{"id":%d,"op":"submit","session_id":"controls","request":`, id) + string(requestEnvelope(t, fmt.Sprintf("submit-%d", id), protocol.TypeSessionMessageSubmitRequest, request, "controls", "")) + `}`)
		response := f.expectResponse(id)
		requireCode(t, response, "unsupported_feature")
		if response.Error.Details["feature"] != control.feature {
			t.Fatalf("%s: details = %+v, want the capability key", control.feature, response.Error.Details)
		}
		if response.Error.Details["reason"] != base.ControlUnadvertised {
			t.Fatalf("%s: reason = %v, want %q", control.feature, response.Error.Details["reason"], base.ControlUnadvertised)
		}
	}

	// Nothing was admitted behind the refusals: the session is still idle, so
	// a submission carrying no control still starts a run.
	id++
	f.send(fmt.Sprintf(`{"id":%d,"op":"submit","session_id":"controls","request":`, id) + string(requestEnvelope(t, "submit-clean", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "controls", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}},
	}, "controls", "")) + `}`)
	requireOK(t, f.expectResponse(id))
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
	// the host cannot see the id of, exactly as the message warns. The next
	// submit takes the one queued slot the reference adapter discloses — an
	// auto submission only resolves to a reservation on a busy session — and
	// the one after that exceeds the bound and is refused run_active.
	f.send(`{"id":2,"op":"submit","session_id":"unsettled","request":` + string(requestEnvelope(t, "submit-2", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "unsettled", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
	}, "unsettled", "")) + `}`)
	reservation := f.expectResponse(2)
	if reservation.Error != nil || !strings.Contains(string(reservation.Result), `"admission":"queued"`) {
		t.Fatalf("second submit did not reserve a queued run: %+v", reservation)
	}
	f.send(`{"id":3,"op":"submit","session_id":"unsettled","request":` + string(requestEnvelope(t, "submit-3", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "unsettled", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
	}, "unsettled", "")) + `}`)
	requireCode(t, f.expectResponse(3), "run_active")
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// Wire and schema validity are the floor beneath the refusal ladder, not a
// rung of it: an envelope the protocol cannot read carries no controls to
// judge, because the bytes in the control positions are not a policy or a
// selection until the message is one at all. So the frontend validates before
// it decodes, and a submit that is schema-invalid is answered schema_invalid
// whatever sits in those positions — the endpoint is never reached, and could
// not honestly answer about a control it never received. The ordering is
// deliberate, mirrors the HTTP route one for one, and is the same one the
// validator keeps: its semantic phase, where every control rule lives, runs
// only on a trace whose decode and schema phases were clean (decision 0005).
func TestSchemaValidityPrecedesTheControlGate(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("memory", noControlsAdapter{base.NewMemory(base.Config{Clock: &testClock{}, IDs: &testIDs{}, JournalCapacity: 64})}); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{StreamQueue: 64})
	openSession(t, hub, "floor")
	f := startFrontend(t, hub, Options{})

	// Schema-invalid (delivery is required) and carrying a control this
	// endpoint advertises nowhere.
	f.send(`{"id":1,"op":"submit","session_id":"floor","request":{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.request","id":"submit-floor","session_id":"floor","payload":{"session_id":"floor","messages":[{"role":"user","content":"x"}],"instructions":"be terse"}}}`)
	requireCode(t, f.expectResponse(1), "schema_invalid")

	// Repair the envelope and the same control is refused under its own key,
	// so the first answer was about the message and not about the control.
	f.send(`{"id":2,"op":"submit","session_id":"floor","request":` + string(requestEnvelope(t, "submit-floor-2", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "floor", Delivery: protocol.DeliveryAuto,
		Instructions: protocol.ControlValue("be terse"),
		Messages:     []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}},
	}, "floor", "")) + `}`)
	response := f.expectResponse(2)
	requireCode(t, response, "unsupported_feature")
	if response.Error.Details["feature"] != protocol.FeatureInstructions {
		t.Fatalf("control refusal details = %+v", response.Error.Details)
	}
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// The stdio frontend mirrors the HTTP daemon one for one, so a queued
// submission and the state that describes it must survive this codec too: the
// reservation is relayed, the state op lists both nonterminal runs in
// admission order, and the sessions listing reports the same set.
func TestQueuedSubmissionOverStdio(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	openSession(t, hub, "queue-stdio")
	f := startFrontend(t, hub, Options{})

	f.send(`{"id":1,"op":"submit","session_id":"queue-stdio","request":` + string(requestEnvelope(t, "submit-1", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "queue-stdio", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("first")}},
	}, "queue-stdio", "")) + `}`)
	first := f.expectResponse(1)
	requireOK(t, first)

	f.send(`{"id":2,"op":"submit","session_id":"queue-stdio","request":` + string(requestEnvelope(t, "submit-2", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "queue-stdio", Delivery: protocol.DeliveryQueue,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("after you")}},
	}, "queue-stdio", "")) + `}`)
	reservation := f.expectResponse(2)
	requireOK(t, reservation)
	var envelope protocol.Envelope
	if err := json.Unmarshal(reservation.Result, &envelope); err != nil {
		t.Fatal(err)
	}
	var reserved protocol.MessageSubmitResponse
	if err := envelope.DecodePayload(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved.Admission != protocol.AdmissionQueued || reserved.RequestedDelivery != protocol.DeliveryQueue ||
		reserved.EffectiveDelivery != protocol.EffectiveDeliveryQueue {
		t.Fatalf("reservation = %+v", reserved)
	}

	f.send(`{"id":3,"op":"state","session_id":"queue-stdio"}`)
	stateResponse := f.expectResponse(3)
	requireOK(t, stateResponse)
	if err := json.Unmarshal(stateResponse.Result, &envelope); err != nil {
		t.Fatal(err)
	}
	var state protocol.SessionState
	if err := envelope.DecodePayload(&state); err != nil {
		t.Fatal(err)
	}
	if len(state.ActiveRuns) != 2 || state.ActiveRuns[1].RunID != reserved.RunID {
		t.Fatalf("active_runs = %+v", state.ActiveRuns)
	}
	if state.ActiveRuns[1].QueuePosition == nil || *state.ActiveRuns[1].QueuePosition != 1 {
		t.Fatalf("queue position = %+v", state.ActiveRuns[1])
	}

	f.send(`{"id":4,"op":"sessions"}`)
	listing := f.expectResponse(4)
	requireOK(t, listing)
	var sessions struct {
		Sessions []sessionInfo `json:"sessions"`
	}
	if err := json.Unmarshal(listing.Result, &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions.Sessions) != 1 || len(sessions.Sessions[0].ActiveRuns) != 2 {
		t.Fatalf("sessions listing = %+v", sessions.Sessions)
	}
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// TestCollectLoopWaitsOutTheScheduler pins the distinction the teardown's
// classification rests on: a loop the teardown has released returns on its
// own schedule, and a teardown that merely asked would record it as never
// having returned. The interleaving that exposes this — the admitted worker
// settling and the drain finishing before the freed loop is scheduled — is
// a race no test can stage on demand, so what is pinned here is the
// decision itself.
func TestCollectLoopWaitsOutTheScheduler(t *testing.T) {
	serveDone := make(chan error, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		serveDone <- ErrRequestsDropped
	}()
	window := time.NewTimer(5 * time.Second)
	defer window.Stop()
	loopErr, released := collectLoop(serveDone, window.C)
	if !released {
		t.Fatal("a loop that published late was recorded as never returning")
	}
	if !errors.Is(loopErr, ErrRequestsDropped) {
		t.Fatalf("collectLoop returned %v, want the loop's own account", loopErr)
	}
}

// TestCollectLoopBoundsTheWait pins that the wait is a bound and not an
// expectation: a loop parked somewhere this teardown did not release must
// cost one window, not the process.
func TestCollectLoopBoundsTheWait(t *testing.T) {
	window := time.NewTimer(10 * time.Millisecond)
	defer window.Stop()
	loopErr, released := collectLoop(make(chan error, 1), window.C)
	if released {
		t.Fatal("collectLoop claimed a loop that never returned")
	}
	if loopErr != nil {
		t.Fatalf("collectLoop returned %v, want nothing from a loop that said nothing", loopErr)
	}
}

// TestCollectLoopOnlyAsksWhenNothingReleasedIt pins the abandoned path,
// where waiting would buy a verdict that is already settled.
func TestCollectLoopOnlyAsksWhenNothingReleasedIt(t *testing.T) {
	serveDone := make(chan error, 1)
	if _, released := collectLoop(serveDone, nil); released {
		t.Fatal("collectLoop claimed a loop that had published nothing")
	}
	serveDone <- ErrRequestsDropped
	loopErr, released := collectLoop(serveDone, nil)
	if !released {
		t.Fatal("collectLoop missed an account already published")
	}
	if !errors.Is(loopErr, ErrRequestsDropped) {
		t.Fatalf("collectLoop returned %v, want the published account", loopErr)
	}
}

// TestHostsEndIsObservedAtAnyPipelineDepth pins the property that decides
// the admission model. A wedged adapter holds the only slot; the host
// pipelines far more requests than any buffer between the reader and the
// loop can hold, and then closes stdin.
//
// While the loop waited for room it stopped taking frames, so the reader
// blocked handing the next one on and never read the end behind it — and
// with no end observed, no shutdown window ever opened and Run hung. No
// fixed read-ahead fixes that; it only moves the depth at which it happens,
// which made the trigger "how much the host wrote". A loop that answers
// what it cannot admit never stops taking frames, so the end is always
// reached, at any depth.
func TestHostsEndIsObservedAtAnyPipelineDepth(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	hub := newProbeHub(t, "hang", &probeAdapter{hang: hang})
	server, err := New(hub, Options{MaxConcurrentOps: 1, WriteQueue: 1, ShutdownTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	var input strings.Builder
	for id := 1; id <= 64; id++ {
		fmt.Fprintf(&input, `{"id":%d,"op":"capabilities","adapter":"hang"}`+"\n", id)
	}
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), strings.NewReader(input.String()), io.Discard) }()
	select {
	case err := <-done:
		// The wedged worker is abandoned, which is the stall this reports;
		// what matters here is that Run returned at all.
		if err != nil && !errors.Is(err, ErrShutdownStalled) {
			t.Fatalf("Run returned %v, want a bounded end", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run never observed the host's end behind a saturated bound")
	}
}

// TestRefusedRequestNamesItself pins what the refusal is worth. A host that
// is told only that something was dropped cannot act; one told which id was
// refused, with a code it can branch on, can send that request again.
func TestRefusedRequestNamesItself(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	hub := newProbeHub(t, "hang", &probeAdapter{hang: hang})
	server, err := New(hub, Options{MaxConcurrentOps: 1, ShutdownTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	var out lockedBuffer
	input := `{"id":1,"op":"capabilities","adapter":"hang"}` + "\n" +
		`{"id":7,"op":"adapters"}` + "\n"
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), strings.NewReader(input), &out) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	var line responseLine
	if err := json.NewDecoder(strings.NewReader(out.String())).Decode(&line); err != nil {
		t.Fatalf("no response reached the host: %v (output %q)", err, out.String())
	}
	if line.ID != 7 {
		t.Fatalf("response names request %d, want the refused one", line.ID)
	}
	if line.OK {
		t.Fatalf("request %d was answered ok, want a refusal", line.ID)
	}
	if line.Error == nil || line.Error.Code != "busy" {
		t.Fatalf("refusal carried %+v, want the busy code", line.Error)
	}
}

// TestBufferedDefectIsJudgedBehindASaturatedBound pins that a saturated
// bound no longer hides a framing defect sitting behind it. A wedged worker
// holds the only slot, a valid request arrives behind it, and an
// unparseable line arrives behind that. While the loop waited for room it
// never reached the bad line, so the reader's account of a clean EOF was all
// Run had and the defect was never reported. Answering what cannot be
// admitted means the loop reaches every line the host sent, so the line
// number and the stall are both reported.
func TestBufferedDefectIsJudgedBehindASaturatedBound(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	hub := newProbeHub(t, "hang", &probeAdapter{hang: hang})
	server, err := New(hub, Options{MaxConcurrentOps: 1, ShutdownTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	input := `{"id":1,"op":"capabilities","adapter":"hang"}` + "\n" +
		`{"id":2,"op":"adapters"}` + "\n" +
		`{"id":3,"op":` + "\n"
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), strings.NewReader(input), io.Discard) }()
	select {
	case err := <-done:
		var malformed *MalformedLineError
		if !errors.As(err, &malformed) {
			t.Fatalf("Run returned %v, want the defect behind the bound reported", err)
		}
		if malformed.Line != 3 {
			t.Fatalf("malformed line %d, want 3", malformed.Line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
}

// TestHostsEndIsObservedBehindABlockedOutput pins the other half of the
// liveness property. Answering what the bound cannot admit removed the wait
// for room, but the refusal itself is a send, and the serving loop is what
// gates the reader — so a host that stops draining its own stdout while
// pipelining would park the loop inside that send and hide its end again,
// at whatever depth the output queue happened to hold. The loop offers the
// refusal instead of pushing it, so the end is reachable whether or not
// anything is reading the answers.
func TestHostsEndIsObservedBehindABlockedOutput(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	hub := newProbeHub(t, "hang", &probeAdapter{hang: hang})
	server, err := New(hub, Options{MaxConcurrentOps: 1, WriteQueue: 1, ShutdownTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	// The output never accepts a byte, so the writer parks on its first
	// write and the queue behind it stays full for the whole session.
	writer := &blockedWriter{release: make(chan struct{}), entered: make(chan struct{}), once: &sync.Once{}}
	t.Cleanup(func() { close(writer.release) })
	var input strings.Builder
	fmt.Fprint(&input, `{"id":1,"op":"capabilities","adapter":"hang"}`+"\n")
	for id := 2; id <= 64; id++ {
		fmt.Fprintf(&input, `{"id":%d,"op":"adapters"}`+"\n", id)
	}
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), strings.NewReader(input.String()), writer) }()
	select {
	case err := <-done:
		// Refusals that could not be queued are requests read and never
		// answered, and the caller is owed that count even though no line
		// could carry it.
		if !errors.Is(err, ErrRequestsDropped) {
			t.Fatalf("Run returned %v, want the unanswered requests reported", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run never observed the host's end behind a blocked output")
	}
}

// TestQueuedRefusalWithdrawnUnwrittenIsReported pins that entering the
// writer's queue is not delivery. A refusal can be queued while the writer
// is parked in an earlier write; if the teardown then abandons that writer,
// the queued line is withdrawn and never goes out, leaving the host with no
// account of that request at all — not served, because it never fit, and not
// answered, because the line never left. The stall says the output is
// unreliable; only this says a request went unanswered.
func TestQueuedRefusalWithdrawnUnwrittenIsReported(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	hub := newProbeHub(t, "hang", &probeAdapter{hang: hang})
	// A queue deep enough that the refusal is accepted, behind a writer that
	// never returns from its first write, so nothing queued is ever emitted.
	server, err := New(hub, Options{MaxConcurrentOps: 1, WriteQueue: 8, ShutdownTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	writer := &blockedWriter{release: make(chan struct{}), entered: make(chan struct{}), once: &sync.Once{}}
	t.Cleanup(func() { close(writer.release) })
	input := `{"id":1,"op":"adapters"}` + "\n" + // answered, and parks the writer
		`{"id":2,"op":"capabilities","adapter":"hang"}` + "\n" + // wedges the only slot
		`{"id":3,"op":"adapters"}` + "\n" // refused, queued, never written
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), strings.NewReader(input), writer) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrRequestsDropped) {
			t.Fatalf("Run returned %v, want the refusal that never went out reported", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
}

// TestRefusalNamesTheBoundThatRefused pins that the two bounds are not
// interchangeable to a host. A refusal citing an op ceiling when the byte
// budget is what filled sends the host looking for requests it does not
// have: two large requests can exhaust the budget while the op ceiling is
// nowhere near, and the count in that message would simply be false.
func TestRefusalNamesTheBoundThatRefused(t *testing.T) {
	ops := newRunState(64, 1024)
	if got, _ := ops.offer(900); got != admitted {
		t.Fatalf("first offer got %v, want admitted", got)
	}
	got, why := ops.offer(900)
	if got != refused {
		t.Fatalf("offer past the byte budget got %v, want refused", got)
	}
	if !strings.Contains(why, "budget") {
		t.Fatalf("byte-budget refusal said %q, want it to name the budget", why)
	}
	if strings.Contains(why, "64 operations") {
		t.Fatalf("byte-budget refusal cited the op ceiling: %q", why)
	}

	counted := newRunState(1, 1<<20)
	if got, _ := counted.offer(1); got != admitted {
		t.Fatalf("first offer got %v, want admitted", got)
	}
	got, why = counted.offer(1)
	if got != refused {
		t.Fatalf("offer past the op ceiling got %v, want refused", got)
	}
	if !strings.Contains(why, "1 operations") {
		t.Fatalf("op-ceiling refusal said %q, want it to name the ceiling", why)
	}
}

// TestRefusalHeldThroughAFullQueueStillArrives pins the difference between
// an output that is briefly behind and one nobody is draining. The writer
// parks on its first write while the queue fills, so a refusal cannot be
// handed over at the moment it is decided; then the writer is released and
// drains normally. A host that was told nothing about that request would
// wait forever for a response it had every right to expect — and, still
// holding stdin open, would keep the session from ending at all, so the
// caller would never learn either. The refusal is held and goes out as soon
// as there is room.
func TestRefusalHeldThroughAFullQueueStillArrives(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	hub := newProbeHub(t, "hang", &probeAdapter{hang: hang})
	// One slot in the queue, so one blocked write plus one queued line is
	// enough to leave the next refusal nowhere to go.
	server, err := New(hub, Options{MaxConcurrentOps: 1, WriteQueue: 1, ShutdownTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	writer := &gatedWriter{release: make(chan struct{}), entered: make(chan struct{})}
	stdinReader, stdinWriter := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), stdinReader, writer) }()
	// The wedging op takes the only slot; everything after it is refused.
	if _, err := stdinWriter.Write([]byte(`{"id":1,"op":"capabilities","adapter":"hang"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	for id := 2; id <= 4; id++ {
		if _, err := stdinWriter.Write([]byte(fmt.Sprintf(`{"id":%d,"op":"adapters"}`+"\n", id))); err != nil {
			t.Fatal(err)
		}
	}
	<-writer.entered                   // the writer is parked on the first refusal
	time.Sleep(100 * time.Millisecond) // the queue fills behind it
	close(writer.release)              // and then the output recovers
	if err := stdinWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	answered := map[int64]bool{}
	decoder := json.NewDecoder(strings.NewReader(writer.String()))
	for {
		var line responseLine
		if err := decoder.Decode(&line); err != nil {
			break
		}
		answered[line.ID] = true
	}
	for id := int64(2); id <= 4; id++ {
		if !answered[id] {
			t.Fatalf("request %d was never answered; the host would wait forever (output %q)", id, writer.String())
		}
	}
}

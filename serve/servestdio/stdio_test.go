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
	f.send(`{"id":1,"op":"state","session_id":"none"}`)
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
// out.Write, the third response's send would park its worker forever;
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

// --- the Run lifecycle: bounded, disconnect-anchored shutdown ---

// TestShutdownDoesNotWaitOnStalledOutput drives the r1 regression: the host
// stops reading stdout, so the single writer parks inside out.Write with the
// response line in hand, and the host then closes stdin. The final drain is
// bounded by its window — Run returns ErrShutdownStalled instead of waiting
// on the host's pipe forever — and the caller's sweep and exit still happen.
func TestShutdownDoesNotWaitOnStalledOutput(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	server, err := New(hub, Options{ShutdownTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) }) // let the abandoned writer finish after the assertion
	stdinReader, stdinWriter := io.Pipe()
	t.Cleanup(func() { stdinWriter.Close() })
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), stdinReader, blockedWriter{release: release}) }()

	// One request whose response parks the writer inside out.Write.
	if _, err := stdinWriter.Write([]byte("{\"id\":1,\"op\":\"adapters\"}\n")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
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

// TestShutdownBoundedWhileWorkStuck drives the r2 shape on this slice's op
// surface: with the one-slot queue full and the writer parked, an admitted
// worker's response send is stuck when the host closes stdin, and shutdown
// starts from that host-end signal — the reader's side-channel report — not
// from the stuck work finishing. Each teardown stage gets its window, Run
// abandons what outlives it, and the process is never wedged behind the
// stuck send. (The synchronous hung-adapter-open variant of this regression
// rides the subscription slice, whose registration ops make the decode loop
// itself stickable.)
func TestShutdownBoundedWhileWorkStuck(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	server, err := New(hub, Options{WriteQueue: 1, ShutdownTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) }) // let the abandoned writer finish after the assertion
	stdinReader, stdinWriter := io.Pipe()
	t.Cleanup(func() { stdinWriter.Close() })
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), stdinReader, blockedWriter{release: release}) }()
	for id := 1; id <= 3; id++ {
		if _, err := stdinWriter.Write([]byte(fmt.Sprintf(`{"id":%d,"op":"adapters"}`+"\n", id))); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(50 * time.Millisecond) // let the third worker park on its send
	if err := stdinWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrShutdownStalled) {
			t.Fatalf("Run returned %v, want ErrShutdownStalled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown hung on the stuck in-flight work")
	}
}

// TestShutdownWindowStartsAtDisconnect drives the codex-r3 anchor: the disconnect
// arrives a full window after Run started, with the writer parked inside the
// synchronous pipe write holding the response. The teardown window must be
// created at that disconnect — not at Run's start, or a daemon that served
// longer than the window would abandon the drain before it began — so the
// parked write completes and is acknowledged inside one fresh window. (The
// grace-stage end-to-end variant — a hung registration op completing inside
// its fresh grace — rides the subscription slice with the ops that hang it.)
func TestShutdownWindowStartsAtDisconnect(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	server, err := New(hub, Options{ShutdownTimeout: 250 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	t.Cleanup(func() { stdinWriter.Close(); stdoutWriter.Close(); stdoutReader.Close() })
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), stdinReader, stdoutWriter) }()

	// io.Pipe is synchronous: the writer parks inside out.Write holding the
	// response until the host reads it, standing in for a slow consumer
	// whose drain is held at the disconnect, without failing the write.
	if _, err := stdinWriter.Write([]byte("{\"id\":1,\"op\":\"adapters\"}\n")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond) // idle past one whole window since Run started
	disconnect := time.Now()
	if err := stdinWriter.Close(); err != nil {
		t.Fatal(err) // the disconnect, after the window has already elapsed once
	}
	time.Sleep(100 * time.Millisecond) // the write is still parked; the window must still be live
	line, err := bufio.NewReader(stdoutReader).ReadString('\n')
	if err != nil {
		t.Fatalf("read the parked line: %v", err)
	}
	if !strings.HasPrefix(line, `{"id":1,`) {
		t.Fatalf("drained line %q is not the response for id 1", line)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want the drain to finish inside its fresh window", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the parked write completed")
	}
	if elapsed := time.Since(disconnect); elapsed > 240*time.Millisecond {
		t.Fatalf("drain finished %v after the disconnect, outside the fresh window", elapsed)
	}
}

// TestAdmitRefusesOnceShutdownBegins pins the admission gate's contract
// directly: workers are registered only while serving lasts, and once
// teardown begins the gate refuses, so no Add can race the teardown Wait.
func TestAdmitRefusesOnceShutdownBegins(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	server, err := New(hub, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !server.admit() {
		t.Fatal("admit refused a worker before shutdown began")
	}
	server.mu.Lock()
	server.shuttingDown = true
	server.mu.Unlock()
	if server.admit() {
		t.Fatal("admit accepted a worker after shutdown began")
	}
	server.work.Done() // release the admitted worker
}

// TestAdmissionGateClosesAtTeardown drives the hyperneo-r3 WaitGroup race: requests
// stream in while the context is cancelled mid-stream, so the decode loop
// can hold a frame at the exact moment teardown begins. The gate must
// refuse that late dispatch rather than Add a worker the teardown Wait may
// already have seen return — an ungated Add is a WaitGroup misuse the race
// detector (this suite runs under -race in CI) reports or the runtime
// panics on. The test drives many interleavings and requires only a clean,
// bounded return from every round.
func TestAdmissionGateClosesAtTeardown(t *testing.T) {
	for round := 0; round < 8; round++ {
		hub := newTestHub(t, 64, 64)
		server, err := New(hub, Options{WriteQueue: 1, ShutdownTimeout: 50 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		stdinReader, stdinWriter := io.Pipe()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		done := make(chan error, 1)
		go func() { done <- server.Run(ctx, stdinReader, io.Discard) }()
		streamed := make(chan struct{})
		go func() {
			defer close(streamed)
			for id := 1; id <= 200; id++ {
				if _, err := stdinWriter.Write([]byte(fmt.Sprintf(`{"id":%d,"op":"adapters"}`+"\n", id))); err != nil {
					return
				}
			}
		}()
		time.Sleep(time.Duration(round) * time.Millisecond) // cancel at varied points in the stream
		cancel()
		stdinWriter.Close() // unblock the stream writer's parked pipe send
		<-streamed
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, ErrShutdownStalled) {
				t.Fatalf("round %d: Run returned %v", round, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d: shutdown did not return against the mid-stream cancellation", round)
		}
	}
}

// TestAdmissionBoundedWhenOutputStalls drives this gate's codex admission
// finding: the host keeps writing stdin while draining nothing — the writer
// parks inside out.Write, the one-slot queue fills, and the third worker
// parks on its send holding the only in-flight slot. Admission must then
// park the decode loop, which parks the reader, so the host's own writes
// block — the write-queue bound is the backpressure onto the single
// consumer of stdout — instead of the frontend admitting an unbounded
// number of workers, each holding its request frame and marshaled
// response. Releasing the output drains the bound and the parked writes
// complete.
func TestAdmissionBoundedWhenOutputStalls(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	server, err := New(hub, Options{WriteQueue: 1, ShutdownTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) }) // let the stalled writer finish after the assertion
	stdinReader, stdinWriter := io.Pipe()
	t.Cleanup(func() { stdinWriter.Close() })
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), stdinReader, blockedWriter{release: release}) }()
	for id := 1; id <= 3; id++ {
		if _, err := stdinWriter.Write([]byte(fmt.Sprintf(`{"id":%d,"op":"adapters"}`+"\n", id))); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(50 * time.Millisecond) // the bound is now held by the parked third worker

	// Keep writing past the bound. The frontend has stopped reading, so
	// after the reader's buffered span is absorbed the host's own writes
	// park — 200 requests well past that span — and the stream cannot
	// complete while the output stays stalled.
	writes := make(chan error, 1)
	go func() {
		for id := 4; id <= 200; id++ {
			if _, err := stdinWriter.Write([]byte(fmt.Sprintf(`{"id":%d,"op":"adapters"}`+"\n", id))); err != nil {
				writes <- err
				return
			}
		}
		writes <- nil
	}()
	select {
	case err := <-writes:
		t.Fatalf("the host streamed every request through a full admission bound: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	// The host resumes draining: the writer consumes the queue, the parked
	// send delivers, the bound frees, and the parked writes complete. The
	// released writer fails its held write, so the stream ends with that
	// failure once everything has been handed off.
	close(release)
	select {
	case err := <-writes:
		if err != nil {
			t.Fatalf("releasing the output did not resume the stream: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the admission bound never freed after the output resumed")
	}
	if err := stdinWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("Run returned %v, want the released writer's failure", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the stream completed")
	}
}

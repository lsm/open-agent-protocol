package servestdio

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// releaser closes its channel exactly once however many times release is
// called — the test body may release mid-test and t.Cleanup again after.
type releaser struct {
	once sync.Once
	ch   chan struct{}
}

func newReleaser() *releaser              { return &releaser{ch: make(chan struct{})} }
func (r *releaser) release()              { r.once.Do(func() { close(r.ch) }) }
func (r *releaser) done() <-chan struct{} { return r.ch }

// The round-4 tombstones and the park-point stress corpus of the B′ design
// (GH #17). The two tombstones pin the exact firing evidence of the round
// that shelved the family; the stress table holds each goroutine at a park
// seam while a terminal condition lands, and asserts the outcome class the
// model harness derives — the schedule-exploration half of direction 4
// (Go's runtime offers no scheduler control, so the exhaustive guarantee
// lives in the model harness over the decision core, and this corpus widens
// the real interleaving space the tests reach).

// startParkedFrontend runs one Server over pipes with park hooks attached,
// for tests that drive the seams directly. With drain set, a background
// reader consumes stdout — an io.Pipe write parks until read, so rows that
// expect the writer to make progress need it; tests that read the lines
// themselves pass drain false and use the returned reader.
func startParkedFrontend(t *testing.T, ctx context.Context, options Options, hooks *parkHooks, drain bool) (stdin io.WriteCloser, stdout *bufio.Reader, done <-chan error) {
	t.Helper()
	server, err := New(newTestHub(t, 64, 64), options)
	if err != nil {
		t.Fatal(err)
	}
	server.setParkHooks(hooks)
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	t.Cleanup(func() { stdinWriter.Close(); stdoutWriter.Close(); stdoutReader.Close() })
	doneCh := make(chan error, 1)
	go func() { doneCh <- server.Run(ctx, stdinReader, stdoutWriter) }()
	var reader *bufio.Reader
	if drain {
		go io.Copy(io.Discard, stdoutReader)
	} else {
		reader = bufio.NewReader(stdoutReader)
	}
	return stdinWriter, reader, doneCh
}

// readLine reads one stdout line within a deadline.
func readLine(t *testing.T, stdout *bufio.Reader) string {
	t.Helper()
	type read struct {
		text string
		err  error
	}
	readDone := make(chan read, 1)
	go func() {
		text, err := stdout.ReadString('\n')
		readDone <- read{text: text, err: err}
	}()
	select {
	case result := <-readDone:
		if result.err != nil {
			t.Fatalf("read line: %v (got %q)", result.err, result.text)
		}
		return result.text
	case <-time.After(10 * time.Second):
		t.Fatal("frontend produced no line within the deadline")
		return ""
	}
}

// TestBusyWorkIsBackpressureNotATimeout is the R4a tombstone: the round-4
// finding fired with WriteQueue 1 and adapter ops slower than
// ShutdownTimeout — the delivery-stall window armed on consumer idleness,
// which busy workers cause exactly like a stalled writer, so a
// normally-draining pipelined session was torn down mid-flight and
// ShutdownTimeout silently became a request-execution timeout. Under the
// supervision owner no timer exists in serving: an admitted worker parked
// on its op is backpressure, the bound saturates, the reader parks
// delivering — and past three windows the session is still serving. The
// host that then closes stdin gets its remaining answers and a clean end.
func TestBusyWorkIsBackpressureNotATimeout(t *testing.T) {
	work := make(chan struct{})
	stdin, stdout, done := startParkedFrontend(t, context.Background(),
		Options{WriteQueue: 1, ShutdownTimeout: 100 * time.Millisecond},
		&parkHooks{workerStart: func() { <-work }}, false)
	for id := 1; id <= 3; id++ {
		if _, err := stdin.Write([]byte(fmt.Sprintf(`{"id":%d,"op":"adapters"}`+"\n", id))); err != nil {
			t.Fatal(err)
		}
	}
	// The false-kill point: the sole worker holds the bound parked on its
	// op, the loop parks in admission, the reader parks delivering frame
	// three. Three windows pass with stdin still open.
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("Run returned %v mid-flight on busy work — work-bound saturation killed the session (R4a)", err)
	default:
	}
	close(work)
	for id := 1; id <= 3; id++ {
		if line := readLine(t, stdout); !strings.HasPrefix(line, fmt.Sprintf(`{"id":%d,"ok":true`, id)) {
			t.Fatalf("response %d after the work released: %q", id, line)
		}
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v after the stream completed, want a clean end", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the stream completed")
	}
}

// blockingFailWriter parks its first write until released, then fails it
// and every later write: a host that stopped reading stdout while the
// frontend held responses, then closed it.
type blockingFailWriter struct {
	release <-chan struct{}
}

func (w blockingFailWriter) Write([]byte) (int, error) {
	<-w.release
	return 0, io.ErrClosedPipe
}

// TestWriterFailureOutranksStallThroughTheZombieLoop is the R4b tombstone:
// the round-4 finding fired when the zombie decode loop — released by
// teardown's cancellation while parked in admission — consumed the sole
// buffered writerFailed value into a channel the owner had already
// abandoned, and the write error was lost to ErrShutdownStalled. Under
// write-once custody the loop reads the writer's cell non-destructively
// and returns nil into its own cell, so the owner selects the write error
// whatever the interleaving. (The abandoned-grace half of the same
// interleaving — a loop stuck past its grace when the writer fails — is
// pinned cell-by-cell in the model harness's custody-conservation
// invariant; its end-to-end driver rides the subscriptions slice, whose
// registration ops are the first that can park the loop mid-op.)
func TestWriterFailureOutranksStallThroughTheZombieLoop(t *testing.T) {
	release := newReleaser()
	t.Cleanup(release.release)
	hub := newTestHub(t, 64, 64)
	server, err := New(hub, Options{WriteQueue: 1, ShutdownTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stdinReader, stdinWriter := io.Pipe()
	t.Cleanup(func() { stdinWriter.Close() })
	done := make(chan error, 1)
	go func() {
		done <- server.Run(context.Background(), stdinReader, blockingFailWriter{release: release.done()})
	}()
	// Four requests through a one-slot bound: one response parked inside
	// the blocked write, one queued, one worker parked on its send holding
	// the slot, and one frame that leaves the loop parked in admission —
	// the zombie the teardown releases. The writer then fails mid-flight.
	for id := 1; id <= 4; id++ {
		if _, err := stdinWriter.Write([]byte(fmt.Sprintf(`{"id":%d,"op":"adapters"}`+"\n", id))); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(50 * time.Millisecond) // let the chain park: write, queue, send, admission
	release.release()                 // the host's stdout breaks with work behind it
	select {
	case err := <-done:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("Run returned %v, want the write failure — the zombie loop's observation consumed it (R4b)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run hung on the failed output")
	}
}

// TestDefectSurfacesNumberedWhenTheLoopNeverTranslates pins the codex
// round-1 finding: the reader reports a framing defect to its cell before
// delivering the frame, so teardown can end the decode loop before it ever
// numbers the line — and the outcome must still be the documented
// line-numbered *MalformedLineError, the same translation the loop would
// have produced, never the raw private frame defect an errors.As caller
// cannot match. The park holds the defective frame's delivery so the cell
// is the only translation that ever happens.
func TestDefectSurfacesNumberedWhenTheLoopNeverTranslates(t *testing.T) {
	release := newReleaser()
	t.Cleanup(release.release)
	stdin, _, done := startParkedFrontend(t, context.Background(),
		Options{ShutdownTimeout: 100 * time.Millisecond},
		&parkHooks{readerDeliver: func() { <-release.done() }}, true)
	if _, err := stdin.Write([]byte("bogus\r\n")); err != nil { // CR is a framing defect
		t.Fatal(err)
	}
	select {
	case err := <-done:
		var malformed *MalformedLineError
		if !errors.As(err, &malformed) {
			t.Fatalf("Run returned %v (%T), want the numbered *MalformedLineError", err, err)
		}
		if malformed.Line != 1 {
			t.Fatalf("defect numbered line %d, want 1", malformed.Line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run hung on the undelivered defect")
	}
}

// TestTeardownRacesAgainstParkedSeams is the stress table: each row parks
// one seam of the lifecycle, lands a terminal condition, and Run must
// return inside its windows with the outcome class the model harness
// derives — a clean close with everything settled is nil, a caller
// cancellation that abandons settled-less stages is ErrShutdownStalled,
// and no row may hang. The parks put the goroutines at the exact
// interleaving points the four review rounds found one at a time; under
// the race detector they widen the schedule space the corpus reaches.
func TestTeardownRacesAgainstParkedSeams(t *testing.T) {
	for _, row := range []struct {
		name      string
		requests  int
		hook      func(release <-chan struct{}) *parkHooks
		terminal  func(t *testing.T, stdin io.WriteCloser, cancel context.CancelFunc)
		holdCheck time.Duration // assert Run is still up this long after the terminal
		releaseAt time.Duration // release the park this long after the terminal (0 = never)
		wantStall bool          // true: ErrShutdownStalled; false: nil
	}{
		{
			// The host closes stdin while the reader is parked delivering
			// into the backlog the host itself created: the closure rides
			// behind it, and no timer exists mid-flight to kill the
			// session (INV-A). Run is still up past three windows; the
			// release frees the chain and the end is clean.
			name:     "reader delivery parked across a clean close",
			requests: 2,
			hook: func(release <-chan struct{}) *parkHooks {
				return &parkHooks{readerDeliver: func() { <-release }}
			},
			terminal:  func(t *testing.T, stdin io.WriteCloser, _ context.CancelFunc) { stdin.Close() },
			holdCheck: 300 * time.Millisecond,
			releaseAt: 350 * time.Millisecond,
			wantStall: false,
		},
		{
			// The loop has not yet reached the admission select when the
			// host closes stdin: the reader's custody cell reports the
			// closure independently of the delivery, teardown begins, and
			// when the loop then arrives at admission the closed latch
			// refuses it — the request is dropped, the loop ends inside
			// its grace, and the end is bounded and clean.
			name:     "admission parked across a clean close",
			requests: 1,
			hook: func(release <-chan struct{}) *parkHooks {
				return &parkHooks{loopAdmit: func() { <-release }}
			},
			terminal:  func(t *testing.T, stdin io.WriteCloser, _ context.CancelFunc) { stdin.Close() },
			holdCheck: 0,
			releaseAt: 50 * time.Millisecond,
			wantStall: false,
		},
		{
			// A busy worker — parked before its op, holding the sole
			// in-flight slot — ignores the caller's cancellation: the work
			// window expires, the stage is abandoned, and the bounded
			// report is the stall summary, never a hang.
			name:     "busy worker across a caller cancellation",
			requests: 2,
			hook: func(release <-chan struct{}) *parkHooks {
				return &parkHooks{workerStart: func() { <-release }}
			},
			terminal:  func(t *testing.T, _ io.WriteCloser, cancel context.CancelFunc) { cancel() },
			holdCheck: 0,
			releaseAt: 0,
			wantStall: true,
		},
		{
			// The writer is parked entering out.Write when the caller
			// cancels: the drain window expires against it and the bounded
			// report is the stall summary.
			name:     "writer write parked across a caller cancellation",
			requests: 2,
			hook: func(release <-chan struct{}) *parkHooks {
				return &parkHooks{writerWrite: func() { <-release }}
			},
			terminal:  func(t *testing.T, _ io.WriteCloser, cancel context.CancelFunc) { cancel() },
			holdCheck: 0,
			releaseAt: 0,
			wantStall: true,
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			release := newReleaser()
			t.Cleanup(release.release)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			stdin, _, done := startParkedFrontend(t, ctx,
				Options{WriteQueue: 1, ShutdownTimeout: 100 * time.Millisecond}, row.hook(release.done()), true)
			for id := 1; id <= row.requests; id++ {
				if _, err := stdin.Write([]byte(fmt.Sprintf(`{"id":%d,"op":"adapters"}`+"\n", id))); err != nil {
					t.Fatal(err)
				}
			}
			time.Sleep(50 * time.Millisecond) // let the parked seam hold
			row.terminal(t, stdin, cancel)
			if row.holdCheck > 0 {
				select {
				case err := <-done:
					t.Fatalf("Run returned %v while its own park was the only thing held — a timer killed the session mid-flight", err)
				case <-time.After(row.holdCheck):
				}
			}
			if row.releaseAt > 0 {
				time.AfterFunc(row.releaseAt, release.release)
			}
			select {
			case err := <-done:
				if row.wantStall && !errors.Is(err, ErrShutdownStalled) {
					t.Fatalf("Run returned %v, want ErrShutdownStalled", err)
				}
				if !row.wantStall && err != nil {
					t.Fatalf("Run returned %v, want a clean end", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Run hung against a parked seam")
			}
		})
	}
}

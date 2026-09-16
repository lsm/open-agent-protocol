// Package servestdio exposes a serve.Hub over newline-delimited JSON on a
// subprocess's stdin and stdout: the transport for hosts that spawn the oap
// binary as a child process — the spawn-a-binary embedding model — with no
// port, TLS, or authentication story, since spawning the process is the
// authorization. The env allowlist of the registry config still governs
// adapter credentials exactly as over HTTP.
//
// Host → daemon lines are one JSON object each, {"id":N,"op":...}, and ops
// may be sent repeatedly, correlated by id; daemon → host lines are
// responses {"id":N,"ok":true,"result":...} / {"id":N,"ok":false,"error":...},
// interleaved by exactly one ordered writer goroutine, so every line is
// atomic and no response is ever broken by interleaving. Each op runs on its
// own worker, as the HTTP server runs one handler per connection, so
// responses to overlapping requests may arrive in any order and the id — not
// the position — is what correlates them; a host that needs one op to
// precede another waits for its response, exactly as it would over HTTP.
// stderr carries bounded diagnostics only.
//
// The operations mirror serve/servehttp one to one with identical semantics
// — the same verbatim schema/v0.1 envelopes, the same request gate, the same
// error codes — with the session-registration ops (open, events) joining the
// surface with their ordering slice. The line-shape refusals
// (invalid_request, unknown_op) and the frame-limit refusal are this
// framing's own layer and have no HTTP counterpart.
//
// Framing is strict in both directions, the same discipline the adapters'
// internal rpc codecs apply to their own child stdio: one LF-terminated
// JSON object per line with a bounded line length. An inbound line that
// violates the framing or is not a valid request frame fails closed — Run
// returns *MalformedLineError after flushing already-admitted work, the
// host reads one bounded diagnostic, and the process exits non-zero — and
// an outbound line whose encoding would exceed the limit is refused in
// favor of a bounded, correlated error response, so a host enforcing the
// same limit never receives a line it cannot carry.
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
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/lsm/open-agent-protocol/serve"
	"github.com/lsm/open-agent-protocol/validation"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// maxEnvelopeBytes is the per-request envelope budget both transports of the
// same daemon enforce on the same unit: servehttp reads it as the request
// body limit, the op gate applies it to the request param. The frame limit
// adds the wrapper allowance on top so a line carrying a maximal envelope —
// plus its id, op, and session addressing — still frames.
const maxEnvelopeBytes = 16 << 20

// wrapperAllowance is the headroom the frame limit gives the line around
// the envelope: the id, op, and session_id params plus the JSON wrapper.
// opaqueID is unbounded, so no allowance could cover every schema-valid
// address — the anchor is the other transport's own address bound, through
// both transports' encodings: HTTP carries addressing percent-encoded in
// the URL path (at worst 3 bytes per raw byte, %XX of a one-byte control)
// and admits request lines only within its 1 MiB header limit, while the
// same raw byte costs at worst 6 in a JSON string (\uXXXX) — a 2x
// expansion ratio. Twice the server limit therefore reserves framing space
// for every address whose HTTP encoding the server accepts, so the
// acceptance sets agree wherever the daemon answers at all; beyond them
// HTTP answers the server's over-limit refusal and stdio fails the line
// closed as an ordinary frame-limit defect.
const wrapperAllowance = 2 << 20

// DefaultFrameLimit bounds one NDJSON line in both directions: the envelope
// budget plus the wrapper allowance, with the adapters' rpc-codec discipline
// — a bounded line, refused rather than split — applied to the framing.
const DefaultFrameLimit = maxEnvelopeBytes + wrapperAllowance

// minFrameLimit is the smallest usable FrameLimit: the correlated refusal —
// respond's fixed-size response_too_large fallback, which encodes well under
// this bound — must always be framable, so an oversized result still gets a
// correlated answer even at the tightest limit. Control lines themselves are
// NOT all under the bound (hostile content can swell an error message past
// it); the fallback, not control-line size, is what the floor buys. New
// rejects smaller limits rather than accepting a configuration whose own
// refusals could not be delivered.
const minFrameLimit = 256

// defaultWriteQueue bounds the lines buffered for the writer goroutine;
// beyond it producers block, which is the backpressure onto the single
// consumer of stdout.
const defaultWriteQueue = 256

// DefaultShutdownTimeout bounds the final output drain once serving ends; a
// host that stops draining stdout cannot stretch shutdown past it.
const DefaultShutdownTimeout = 5 * time.Second

// Options tunes the frontend. The zero value is usable.
type Options struct {
	// FrameLimit bounds one line in bytes in both directions; zero means
	// DefaultFrameLimit.
	FrameLimit int
	// WriteQueue bounds the lines buffered for the writer goroutine before
	// producers block; zero means defaultWriteQueue.
	WriteQueue int
	// MaxConcurrentOps bounds the ops in flight, which is what stops the
	// serving loop reading a stream faster than its adapters retire it;
	// zero means maxConcurrentOps. The request bytes those ops hold are
	// budgeted separately and are not configurable, because that budget is
	// a memory bound rather than a tuning knob.
	MaxConcurrentOps int
	// ShutdownTimeout bounds the final output drain once serving ends — the
	// writer may be parked inside out.Write on a pipe the host stopped
	// reading, and shutdown never depends on the host's pipe; zero means
	// DefaultShutdownTimeout.
	ShutdownTimeout time.Duration
	// Logger receives lifecycle diagnostics. Envelope payloads and resolved
	// environment values are never written to it.
	Logger *log.Logger
}

// Server serves one hub over stdio NDJSON. OAP operations exchange verbatim
// schema/v0.1 envelopes, exactly as servehttp does; the framing carries the
// id correlation HTTP gets from its request/response pairing.
type Server struct {
	hub         *serve.Hub
	schema      *jsonschema.Schema
	frameLimit  int
	writeQueue  int
	maxOps      int
	shutdown    time.Duration
	logger      *log.Logger
	nextIDValue atomic.Uint64
}

// maxConcurrentOps is the default ceiling on ops in flight. The serial
// dispatch this replaced ran one; anything above that is already more
// concurrency than a single-user local daemon had, and the ceiling is what
// keeps a host from turning a pipelined stream into goroutines faster than
// its adapters retire them.
const maxConcurrentOps = 16

// admissionBytes budgets the request bytes those ops may hold at once. A
// count alone is not a memory bound: at the default frame limit one
// admitted request can carry megabytes, so a ceiling that looks modest
// still multiplies into gigabytes. The budget bounds the product instead,
// and one op is always admitted however large it is, so a maximal request
// is served rather than deadlocked against a budget it cannot fit.
const admissionBytes = maxEnvelopeBytes

// runState is one Run's worker registry: the ops that invocation admitted
// and the bounds on how many of them run at once. It belongs to the
// invocation and never to the Server, so a Server that served one host can
// serve the next — the state a session ends in is not a state the next
// session inherits.
type runState struct {
	maxOps   int
	maxBytes int

	// mu guards the admission gate. work counts the op workers teardown
	// waits for; shuttingDown closes admission so no worker joins after
	// that wait began. Nothing waits on this gate: a request that does not
	// fit is answered rather than held, so the loop that offered it keeps
	// taking frames.
	mu           sync.Mutex
	work         sync.WaitGroup
	shuttingDown bool
	ops          int
	bytes        int
}

func newRunState(maxOps, maxBytes int) *runState {
	if maxOps <= 0 {
		maxOps = maxConcurrentOps
	}
	if maxBytes <= 0 {
		maxBytes = admissionBytes
	}
	return &runState{maxOps: maxOps, maxBytes: maxBytes}
}

// admission is what the loop learns when it offers a request to the bounds.
// Offering never waits, and the reason is a liveness one rather than a
// throughput one: waiting for room is what made the serving loop stop
// taking frames, and a reader blocked handing the next frame on cannot read
// the host's end behind it. Any fixed amount of slack between them only
// moves the depth at which that happens, so backpressure here bought a hang
// whose trigger was how much the host chose to pipeline. Answering costs
// the host a retry; waiting cost it the session.
type admission int

const (
	// admitted: the request has a slot and its bytes are charged.
	admitted admission = iota
	// refused: the bounds are full right now. The request is answered and
	// not held, because a loop that waits here stops taking frames — and a
	// reader blocked handing the next frame on cannot read the host's end
	// behind it. Waiting would make the session's own end invisible at
	// whatever depth the host happened to pipeline to, which is a liveness
	// bug whose trigger is how much the host wrote.
	refused
	// closedToWork: the teardown has shut admission. Nothing more will be
	// served, and this is the one outcome that is not answered: the output
	// is being given up, not offered.
	closedToWork
)

// offer charges a request against both bounds without ever waiting. An idle
// registry admits anything, so a request larger than the whole byte budget
// still runs; otherwise both bounds must hold.
func (r *runState) offer(size int) admission {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.shuttingDown {
		return closedToWork
	}
	if r.ops == 0 || (r.ops < r.maxOps && r.bytes+size <= r.maxBytes) {
		r.ops++
		r.bytes += size
		r.work.Add(1)
		return admitted
	}
	return refused
}

func (r *runState) release(size int) {
	r.mu.Lock()
	r.ops--
	r.bytes -= size
	r.mu.Unlock()
	r.work.Done()
}

// closeAdmission ends admission for this invocation, before its teardown
// waits for the workers already counted.
func (r *runState) closeAdmission() {
	r.mu.Lock()
	r.shuttingDown = true
	r.mu.Unlock()
}

// New compiles the request gate and returns a frontend over the hub.
func New(hub *serve.Hub, options Options) (*Server, error) {
	if hub == nil {
		return nil, errors.New("servestdio: hub is required")
	}
	schema, err := validation.CompileSchemas()
	if err != nil {
		return nil, fmt.Errorf("servestdio: compile request schema: %w", err)
	}
	frameLimit := options.FrameLimit
	if frameLimit <= 0 {
		frameLimit = DefaultFrameLimit
	}
	if frameLimit < minFrameLimit {
		return nil, fmt.Errorf("servestdio: frame limit %d is below the %d-byte minimum a correlated refusal needs", frameLimit, minFrameLimit)
	}
	writeQueue := options.WriteQueue
	if writeQueue <= 0 {
		writeQueue = defaultWriteQueue
	}
	maxOps := options.MaxConcurrentOps
	if maxOps <= 0 {
		maxOps = maxConcurrentOps
	}
	shutdown := options.ShutdownTimeout
	if shutdown <= 0 {
		shutdown = DefaultShutdownTimeout
	}
	logger := options.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Server{hub: hub, schema: schema, frameLimit: frameLimit, writeQueue: writeQueue, maxOps: maxOps, shutdown: shutdown, logger: logger}, nil
}

// Hub returns the hub the frontend serves.
func (s *Server) Hub() *serve.Hub { return s.hub }

// MalformedLineError reports one line that violates the NDJSON framing or the
// request shape. The daemon fails closed on it: Run stops reading, already
// admitted work is settled and flushed, and the caller exits non-zero after
// reporting this one bounded diagnostic.
type MalformedLineError struct {
	Line   int
	Detail string
}

func (e *MalformedLineError) Error() string {
	return fmt.Sprintf("line %d is not a valid request: %s", e.Line, e.Detail)
}

// ErrLineTooLarge reports an encoded output line that exceeds the frame
// limit: the host's own framing could not carry it, so it is refused rather
// than emitted.
var ErrLineTooLarge = errors.New("servestdio: encoded line exceeds the frame limit")

// ErrRequestsDropped reports that requests the host sent before ending the
// session were read and never served. It happens when the serving loop is
// still holding work at the teardown: admission closes under it, and the
// frames it had taken — the one it holds and any the reader had handed on —
// are let go rather than served behind a session the host has ended.
//
// This is the narrow case that remains once a saturated bound is answered
// rather than waited on. A request that does not fit is refused on the wire
// and the host can send it again; this error is for the frames that were
// past that point when the teardown closed admission under them, which no
// response can reach because the output is being given up. What the caller
// is owed is to be told, rather than a nil return that reads as "all
// served".
var ErrRequestsDropped = errors.New("servestdio: requests read before the host's end were dropped unserved")

// ErrShutdownStalled reports that a shutdown stage outlived its bounded
// window and was abandoned rather than waited on. The caller's session
// sweep and the process exit still happen.
//
// It also marks the one condition under which the output stream must not be
// reused. Run abandons a stalled writer rather than waiting for it, because
// waiting is the stall it is escaping — so on this path, and only this path,
// a write may already be underway or already chosen, and those bytes land
// when the host reads again whatever Run has returned. The queue behind them
// is withdrawn, but a single line is beyond recall. A caller that runs
// another session over the same stream after ErrShutdownStalled may see it
// arrive there; `oap serve` exits the process instead, which is why the
// condition does not arise for it.
var ErrShutdownStalled = errors.New("servestdio: shutdown outlived its bounded window; the stalled stage was abandoned")

// Run serves requests from in until the host closes it (clean end), the
// context ends, the output fails, or a line violates the framing (fail
// closed). Responses are written to out by exactly one writer goroutine, one
// JSON object per LF-terminated line; a write failure ends serving — a host
// that stopped reading stdout receives nothing further, so no work is
// admitted behind its back — and the failure is returned once the writer
// drains the lines already admitted. The frontend is a codec and owns no
// session lifetime: the caller sweeps the hub after Run returns, on every
// exit path. Run itself never writes to stderr; it returns the failure for
// the caller to report.
//
// Shutdown is bounded from the moment the input's end is read — stdin
// closed, the read failed, or the context cancelled — not from when the
// serving loop notices it, because the loop can be stuck: the reader
// reports its own end on a side channel so that end is observed either
// way. Options.ShutdownTimeout then bounds each stage independently, and
// every window opens at that end rather than at Run's start, so a daemon
// that served for hours still grants a full one. A stage that outlives its
// window is abandoned and Run returns ErrShutdownStalled, so the caller's
// bounded session sweep and the process exit still happen; nothing waits
// indefinitely on the host's pipe or a stuck adapter.
//
// "Read" is the load-bearing word, and the limit it marks is worth stating
// rather than discovering. The end of the input sits behind whatever the
// host sent before it, and those frames are read in order: the reader runs
// one frame ahead of the loop, so an end that the loop's own backlog does
// not exceed is observed while the loop is parked, but a host that sent
// more frames than that before closing has an end nobody can see until
// those frames are taken. With every worker stuck, they are not, and the
// context is what ends such a session — the same escape the serial
// dispatch offered, since reading further would mean buffering a stream
// the host controls. Making the daemon answer rather than wait there needs
// a refusal it does not have yet, which is a protocol question and not
// this frontend's to settle.
func (s *Server) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	lines := make(chan []byte, s.writeQueue)
	stop := make(chan struct{})
	abandon := make(chan struct{})
	writerFailed := make(chan struct{}, 1)
	outputFailed := &outputFailure{}
	writerDone := make(chan error, 1)
	go func() { writerDone <- writeLines(out, lines, stop, abandon, writerFailed, outputFailed) }()

	// The reader reports its terminal outcome on a buffered side channel as
	// well as in the frame stream, so that the end is a fact the teardown
	// holds rather than one more thing queued behind the loop. The loop no
	// longer stops at the in-flight bound — it answers what it cannot admit
	// — but it can still be inside a send to an output the host has stopped
	// reading, and the host's end of the session must bound shutdown even
	// then.
	readerDone := make(chan readEnd, 1)
	// One frame of slack, so a reader and a loop that are both running do
	// not hand every frame over in lockstep. It is slack and not a remedy:
	// the loop taking frames without waiting is what keeps the host's end
	// reachable, and no amount of buffering here would substitute for that.
	// One frame is the whole of the read-ahead, so the memory this costs is
	// bounded by the same argument the in-flight bound makes.
	frames := make(chan frameResult, 1)
	go readFrames(in, s.frameLimit, frames, readerDone)

	run := newRunState(s.maxOps, 0)
	serveDone := make(chan error, 1)
	go func() { serveDone <- s.serveLoop(ctx, run, frames, lines, writerFailed) }()

	// The serving loop returning is the session's normal end. When the host
	// has ended it while the loop is stuck, one fresh window — opened at
	// that end, never at Run's start — bounds how much longer the stuck op
	// gets before it is abandoned.
	serveGrace := func() (error, bool) {
		window := time.NewTimer(s.shutdown)
		defer window.Stop()
		select {
		case result := <-serveDone:
			return result, true
		case <-window.C:
			return nil, false
		}
	}
	var err error
	var ended readEnd
	stuck := false
	select {
	case err = <-serveDone:
	case ended = <-readerDone:
		if graceErr, finished := serveGrace(); finished {
			err = graceErr
		} else {
			stuck = true
		}
	case <-ctx.Done():
		if graceErr, finished := serveGrace(); finished {
			err = graceErr
		} else {
			stuck = true
		}
	}

	// Teardown: admission closes so a loop abandoned mid-op cannot add a
	// late worker racing this wait — a positive Add after Wait has seen the
	// counter reach zero is what the WaitGroup contract forbids. The
	// workers already admitted are then waited for rather than cancelled,
	// which is what settles and flushes the work the host was answered for;
	// the second window bounds that wait and the writer's final drain
	// together, and cancellation is what remains when it expires, so a
	// stuck adapter costs one window and not the process. The writer may be
	// parked inside out.Write on a pipe the host stopped reading, and
	// shutdown never depends on the host's pipe.
	run.closeAdmission()
	workDone := make(chan struct{})
	go func() { run.work.Wait(); close(workDone) }()
	workWindow := time.NewTimer(s.shutdown)
	defer workWindow.Stop()
	drained := false
	select {
	case <-workDone:
		close(stop)
		// The drain opens its own window here rather than inheriting what
		// the worker wait left of a shared one: workers that finish near
		// the end of their window would otherwise leave almost no budget to
		// flush responses they had already settled, so a session could be
		// reported stalled for want of the milliseconds its own workers
		// spent.
		drainWindow := time.NewTimer(s.shutdown)
		defer drainWindow.Stop()
		select {
		case writeErr := <-writerDone:
			drained = true
			// The writer's return joins whatever the loop already
			// reported rather than only filling a gap. A write that
			// failed while the loop was holding a frame produces both:
			// the loop refuses the frame and reports the drop, and the
			// output the drop would have been written to is dead. A
			// caller told only that requests were dropped would go
			// looking for a slow adapter and never find the broken pipe.
			err = note(err, writeErr)
		case <-drainWindow.C:
			// The drain outlived its window, so the writer is parked inside
			// out.Write on an output the host stopped reading. Stopping it
			// was the instruction to finish; abandoning it withdraws the
			// queue, because a write that unblocks after Run has returned
			// would otherwise carry every line still waiting into an output
			// the caller has taken back.
			close(abandon)
		}
	case <-workWindow.C:
		// The workers being abandoned here may still finish, and the writer
		// must not outlive Run waiting to carry what they produce: an
		// orphaned writer holds the caller's output and could emit a stale
		// response into it long after this returns, or across a later
		// invocation that reuses it.
		//
		// Cancelling is not enough on its own to stop them. A send whose
		// context is already done selects between a ready cancellation and
		// a ready channel, and a select chooses uniformly among ready
		// cases, so the line can still be queued. Abandoning the writer is
		// what settles it: unlike stop, which drains what is queued before
		// ending, abandon ends the writer where it stands, so a line that
		// wins that race is never carried. A writer parked inside out.Write
		// on a pipe the host stopped reading stays parked, which is the
		// stall this window exists to abandon in the first place.
		cancel()
		close(abandon)
		// The writer's own failure is not waited for here — waiting is what
		// this window just gave up on — but it is still the truest account
		// of why nothing reached the host, so it is read from where the
		// writer records it rather than from a return this path never
		// collects. It joins whatever the input already produced rather
		// than only filling a gap: a malformed line and a dead output are
		// two facts, and the host that sent the line is not the reason its
		// answer never arrived.
		err = note(err, outputFailed.get())
	}
	// A loop abandoned mid-op never reported the input's own end, so the
	// reader's account of it stands in: the same host input returns the same
	// error from Run whether or not a worker was stuck, so a malformed line
	// still fails the frontend closed with its line number and a transport
	// read failure still surfaces as itself. Only a clean end leaves the
	// stall as the whole story.
	if stuck {
		// The reader reports on a buffered channel, so its account is taken
		// here whatever ended this session, not only when the reader's
		// branch is what began the teardown. On this tree the two amount to
		// the same thing — every point the loop can block at observes the
		// context, so a cancelled session's loop returns and is never
		// stuck — but that is an argument about send and about a select in
		// another function. Reading the channel makes the line number
		// survive without it, so a blocking point added later cannot
		// quietly cost a host its fail-closed report.
		select {
		case ended = <-readerDone:
		default:
		}
		err = note(err, ended.terminal())
	}
	// The grace window expiring is not itself proof that anything was left
	// behind. Closing admission ends a loop that offers one more frame, so
	// a session whose loop was mid-frame finishes here after all — and if
	// its work then settled and its responses drained, nothing was
	// abandoned and the stall is not this session's story. The loop is
	// asked rather than the timer. Its return value says nothing about the
	// input, though: such a loop returns because admission closed, not
	// because the host ended anything, which is why the reader's account
	// above is what stands in either way.
	//
	// When this teardown is the only thing that can still free the loop and
	// the writer was collected normally, the loop is waited for and not
	// merely polled, because being runnable is not the same as having run.
	// A loop already collected above published once and will not publish
	// again, so waiting on it there would buy nothing and cost a window. Admission is closed and the queue is being consumed, so every
	// point the loop can be parked at is one this teardown has already
	// released; but the worker can settle and the drain can finish in a
	// handful of channel operations, and a loop that has not yet been
	// scheduled would be recorded as never returning. That would report a
	// stall for a session that settled and drained everything it admitted,
	// and lose its account of the frames it let go. The window is a bound
	// on the reasoning, not an expectation.
	var wait <-chan time.Time
	if stuck && drained {
		loopWindow := time.NewTimer(s.shutdown)
		defer loopWindow.Stop()
		wait = loopWindow.C
	}
	// What the loop says it did with the frames it was holding is its own
	// fact, not a fallback for a quiet teardown. A malformed line at the
	// host's end already fills err from the reader's account above, and the
	// valid requests the loop was holding when admission closed under it
	// were still read and never served — which is the whole of what
	// ErrRequestsDropped promises to tell a caller.
	loopErr, released := collectLoop(serveDone, wait)
	err = note(err, loopErr)
	// The stall and the input's own fault are not alternatives. A malformed
	// line that arrives while a response is stuck in out.Write produces
	// both, and a caller needs both: the line number to report and exit on,
	// and the stall to know this output must not be reused. So the two are
	// joined rather than one shadowing the other, and errors.Is and
	// errors.As each still find what they are looking for.
	if (stuck && !released) || !drained {
		err = note(err, ErrShutdownStalled)
	}
	return err
}

// note adds fact to err rather than letting either shadow the other. Every
// error Run returns names something that independently happened to this
// session — the input's own fault, a dead output, requests read but never
// served, a stage that outlived its window — and none of them is evidence
// about any of the others. A caller asking errors.Is or errors.As about any
// one of them is asking a separate question, so each must still get a true
// answer when several went wrong at once. A fact already carried is not
// repeated.
func note(err, fact error) error {
	switch {
	case fact == nil:
		return err
	case err == nil:
		return fact
	case errors.Is(err, fact):
		return err
	default:
		return fmt.Errorf("%w (%w)", err, fact)
	}
}

// collectLoop takes the serving loop's own account of what it did with the
// frames it was still holding. A nil wait asks without waiting, which is
// what an abandoned teardown can afford: nothing there has released a loop
// parked on a send to a writer that carries nothing more, and its answer
// could not change the verdict anyway, since an undrained teardown reports
// the stall either way. A non-nil wait is for the teardown that did release
// it, where the only thing still in question is whether the scheduler has
// got to it yet.
func collectLoop(serveDone <-chan error, wait <-chan time.Time) (error, bool) {
	if wait == nil {
		select {
		case loopErr := <-serveDone:
			return loopErr, true
		default:
			return nil, false
		}
	}
	select {
	case loopErr := <-serveDone:
		return loopErr, true
	case <-wait:
		return nil, false
	}
}

// readEnd is the reader's account of how the input ended: the line it was
// reading and what stopped it. The line number counts the reader's own
// lines, which is the same count the serving loop keeps, so a defect
// reported from either side names the same line.
type readEnd struct {
	line int
	err  error
}

// terminal reports the error Run owes its caller for this end, or nil when
// the host simply closed the input.
func (e readEnd) terminal() error {
	switch {
	case e.err == nil, errors.Is(e.err, io.EOF):
		return nil
	case errors.As(e.err, new(*frameDefect)):
		return &MalformedLineError{Line: e.line, Detail: e.err.Error()}
	default:
		// A read failure is not the host's protocol fault: pass the input's
		// own error through, exactly as the serving loop would have.
		return e.err
	}
}

// frameResult is one line read from the host: frame carries the line without
// its terminator, err the condition that ended the read (io.EOF for the clean
// host close, a *frameDefect for a framing violation, or the input's own
// read failure, passed through).
type frameResult struct {
	frame []byte
	err   error
}

// readFrames reads bounded NDJSON lines from in and forwards each with its
// outcome. The frames channel is never closed: a reader abandoned by a
// context end parks on its send and is reclaimed by process exit, the same
// discipline as the writer's channel. The terminal outcome is also reported
// on done — buffered, so the report never blocks, and sent before the final
// frame — because the consumer may be inside a send to a stopped output and
// never take that frame, and the host's end of the session must still bound
// shutdown and still be reported as what it was.
func readFrames(in io.Reader, limit int, frames chan<- frameResult, done chan<- readEnd) {
	reader := bufio.NewReader(in)
	line := 0
	for {
		frame, err := readFrame(reader, limit)
		line++
		if err != nil {
			done <- readEnd{line: line, err: err}
			frames <- frameResult{frame: frame, err: err}
			return
		}
		frames <- frameResult{frame: frame}
	}
}

// serveLoop dispatches request frames until a clean end, an output failure,
// or a framing defect. Reading runs on its own goroutine
// so a context end is observed while waiting for the next line, not only
// between lines — an embedding host that cancels without also closing stdin
// still gets Run back — and writerFailed carries the writer's failure the
// same way, so a host that stopped reading stdout ends serving instead of
// admitting work whose responses are silently discarded. The loop-top
// pre-check narrows, but cannot eliminate — a select chooses uniformly among
// ready cases, so a frame arriving with the cancellation can still be taken —
// the dispatch of frames under an already-dead context; the bounded teardown
// owns what remains. Each op runs on its own admitted worker, exactly as the
// HTTP server runs one handler per connection: one slow adapter call neither
// delays the next line nor outlives the teardown that waits for it. The ops
// in flight are bounded, and a request that does not fit that bound is
// answered as busy rather than held, so a host that pipelines faster than
// the daemon can answer learns which requests to send again and the loop
// never stops reading. A framing defect — anything readFrame or decodeRequest refuses — fails the
// frontend closed as *MalformedLineError; op-level refusals are responses
// the host can correct, and the loop reads on past them.
func (s *Server) serveLoop(ctx context.Context, run *runState, frames <-chan frameResult, lines chan<- []byte, writerFailed <-chan struct{}) error {
	number := 0
	// Requests read and never answered, counted so the caller is told. A
	// refusal the output could not take is exactly that: neither served nor
	// answered, and the host cannot know to send it again.
	unanswered := 0
	ended := func(err error) error {
		if unanswered > 0 {
			return note(err, ErrRequestsDropped)
		}
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ended(nil)
		case <-writerFailed:
			return ended(nil)
		default:
		}
		select {
		case <-ctx.Done():
			return ended(nil)
		case <-writerFailed:
			return ended(nil)
		case result := <-frames:
			number++
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {
					return ended(nil)
				}
				var defect *frameDefect
				if errors.As(result.err, &defect) {
					return ended(&MalformedLineError{Line: number, Detail: defect.Error()})
				}
				// A read failure is not the host's protocol fault: fail
				// closed, but let the caller see the input's own error.
				return ended(result.err)
			}
			request, err := decodeRequest(result.frame)
			if err != nil {
				return ended(&MalformedLineError{Line: number, Detail: err.Error()})
			}
			size := len(result.frame)
			switch run.offer(size) {
			case closedToWork:
				// Admission closed under us: the teardown is past waiting
				// and this frame, decoded and never served, is being let
				// go. Say so rather than returning as though the input had
				// simply ended.
				return ended(ErrRequestsDropped)
			case refused:
				// Answered here rather than by a worker, because refusing
				// is precisely the case where no worker was started. The
				// host learns which request went unserved and may send it
				// again, which is what waiting could never tell it.
				//
				// Offered to the queue rather than pushed into it: this is
				// the one send the serving loop makes, and the loop is what
				// gates the reader, so blocking here would hide the host's
				// end behind a full output exactly as waiting for room once
				// hid it behind a full bound. An output too backed up to
				// take a one-line refusal is an output nobody is draining,
				// and telling a host that is not reading outranks nothing.
				// What is owed then is the count, not the line.
				if !s.offerLine(lines, request, &wireError{
					Code:    "busy",
					Message: fmt.Sprintf("the frontend is already running %d operations; send this request again", s.maxOps),
				}) {
					unanswered++
				}
				continue
			}
			go func(request requestLine, size int) {
				defer run.release(size)
				s.serveRequest(ctx, request, lines)
			}(request, size)
		}
	}
}

// writeLines is the single ordered writer: it appends the LF terminator to
// every marshaled line and writes it whole, so lines never interleave. It
// ends one of two ways. Stopped, it drains the lines already queued, which
// is what settles the work a clean teardown waited for; abandoned, it ends
// where it stands and carries nothing more, which is what keeps a teardown
// that gave up on its workers from letting their late responses reach an
// output the caller has already taken back. A write failure (the host
// closed stdout) is remembered while a drain continues, so producers
// blocked on the channel still hand off instead of deadlocking, and is
// signalled on failed exactly once so serving stops admitting work behind a
// dead output. The channel is never closed: an abandoned producer parks on
// its send and is reclaimed by process exit rather than panicking on a
// closed channel.
func writeLines(out io.Writer, lines <-chan []byte, stop, abandon <-chan struct{}, failed chan<- struct{}, record *outputFailure) error {
	var failure error
	write := func(line []byte) {
		if failure != nil {
			return
		}
		// Checked per line, not once per loop: a select chooses uniformly
		// among ready cases, so a line queued as the teardown abandons this
		// writer can still be taken. Refusing it here is what makes
		// abandonment mean "carries nothing more" rather than "usually
		// carries nothing more".
		select {
		case <-abandon:
			return
		default:
		}
		if _, err := out.Write(append(line, '\n')); err != nil {
			failure = err
			record.set(err)      // readable by a teardown that cannot wait for this goroutine
			failed <- struct{}{} // buffered one-slot signal, sent only on the first failure
		}
	}
	for {
		select {
		case line := <-lines:
			write(line)
		case <-abandon:
			return failure
		case <-stop:
			for {
				select {
				case line := <-lines:
					write(line)
				default:
					return failure
				}
			}
		}
	}
}

// outputFailure records the writer's first failure where a teardown can read
// it without waiting for the writer to return. The abandonment path needs
// exactly that: it has just decided not to wait, and the return value it
// therefore never collects is where the failure would otherwise live.
type outputFailure struct {
	mu  sync.Mutex
	err error
}

func (f *outputFailure) set(err error) {
	f.mu.Lock()
	if f.err == nil {
		f.err = err
	}
	f.mu.Unlock()
}

func (f *outputFailure) get() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

// frameDefect marks a framing error the codec itself raised — the line
// violates the NDJSON rules — distinguishing it from a read failure on the
// input (EIO, a closed descriptor), which is not the host's protocol fault.
// Both fail the frontend closed; only the defect becomes a MalformedLineError.
type frameDefect struct {
	detail string
}

func (e *frameDefect) Error() string { return e.detail }

// readFrame reads one NDJSON line from the reader, without its terminator.
// The framing rules mirror the adapter rpc codecs: a line is LF-terminated,
// carries no CR, is non-empty, valid UTF-8, and within the limit; an
// unterminated final line is a defect, while EOF at a line boundary is the
// clean host close. Rule violations return *frameDefect; anything else is
// the reader's own failure, passed through untouched.
func readFrame(reader *bufio.Reader, limit int) ([]byte, error) {
	frame := make([]byte, 0, min(limit, 4096))
	for {
		fragment, err := reader.ReadSlice('\n')
		// A read failure is not the host's protocol fault, whatever
		// partial bytes the reader handed back with it — an io.Reader may
		// return data and an error together — so it passes through before
		// any framing rule judges the line.
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if len(frame)+len(fragment) > limit+1 {
			return nil, &frameDefect{detail: fmt.Sprintf("line exceeds the %d-byte frame limit", limit)}
		}
		frame = append(frame, fragment...)
		switch {
		case err == nil:
			frame = frame[:len(frame)-1]
			switch {
			case bytes.IndexByte(frame, '\r') >= 0:
				return nil, &frameDefect{detail: "carriage return is not valid framing"}
			case len(frame) == 0:
				return nil, &frameDefect{detail: "empty line"}
			case !utf8.Valid(frame):
				return nil, &frameDefect{detail: "line is not UTF-8"}
			}
			return frame, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(frame) > 0:
			return nil, &frameDefect{detail: "unterminated final line"}
		default:
			return nil, err
		}
	}
}

// requestLine is one host → daemon line. The id correlates the response; the
// params each op accepts are enforced per op by presence, so a well-formed
// line carrying params its op does not define is refused as a response, not
// a framing defect.
type requestLine struct {
	ID        *int64          `json:"id"`
	Op        string          `json:"op"`
	Adapter   string          `json:"adapter,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	After     json.RawMessage `json:"after,omitempty"`
	Request   json.RawMessage `json:"request,omitempty"`
	// AllowDegradedFeatures is the models op's per-request degraded opt-in.
	AllowDegradedFeatures []string `json:"allow_degraded_features,omitempty"`

	// present records which keys the raw object actually carried, set by
	// decodeRequest: the per-op shape check refuses on presence, not value,
	// so a supplied-but-empty or null param is still supplied.
	present map[string]bool
}

// decodeRequest parses one frame into a request line. Anything that is not a
// JSON object carrying exactly the protocol's fields, each key once and
// exactly spelled, with a numeric id — invalid JSON, a non-object, an
// unknown field, a case-aliased or repeated key, a mistyped or missing id —
// is a framing defect the daemon fails closed on: the host speaks a protocol
// this frontend cannot correlate.
func decodeRequest(frame []byte) (requestLine, error) {
	var request requestLine
	decoder := json.NewDecoder(bytes.NewReader(frame))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return requestLine{}, fmt.Errorf("invalid JSON request: %v", trimMessage(err.Error()))
	}
	// More() is not an end-of-input check — it also reports false for a
	// stray closing token, which would let frames like {...}} through — so
	// the only complete frame is one whose next decode reaches end of input.
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return requestLine{}, errors.New("invalid JSON request: trailing data after the request object")
	}
	present, err := scanKeys(frame)
	if err != nil {
		return requestLine{}, fmt.Errorf("invalid JSON request: %v", trimMessage(err.Error()))
	}
	request.present = present
	if request.ID == nil {
		return requestLine{}, errors.New("invalid JSON request: id is required")
	}
	if request.Op == "" {
		return requestLine{}, errors.New("invalid JSON request: op is required")
	}
	return request, nil
}

// canonicalKeys is the exact spelling of every field a request line may
// carry. encoding/json matches struct fields case-insensitively even with
// DisallowUnknownFields, so a case-aliased key would both last-win a
// canonical one and record presence under a name the per-op shape check
// never reads; scanKeys therefore accepts only these spellings, byte-exact.
var canonicalKeys = map[string]bool{
	"id": true, "op": true, "adapter": true, "session_id": true, "after": true, "request": true,
	"allow_degraded_features": true,
}

// scanKeys walks the raw request object's keys, reporting each key's
// presence and refusing non-canonical spellings and repeats: Go's decoder
// keeps the last value of a repeated key, which would make the executed
// request host-parser-dependent, so a duplicate key is a framing defect
// rather than a silent last-wins.
func scanKeys(frame []byte) (map[string]bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(frame))
	open, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := open.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("request is not a JSON object")
	}
	present := make(map[string]bool)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, isString := keyToken.(string)
		if !isString {
			return nil, errors.New("request key is not a string")
		}
		if !canonicalKeys[key] {
			return nil, fmt.Errorf("field %q must use its exact protocol spelling", key)
		}
		if present[key] {
			return nil, fmt.Errorf("repeated key %q", key)
		}
		present[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
	}
	return present, nil
}

// offerLine puts one refusal on the writer's queue if the queue can take it
// now, and reports whether it did. It never waits: see the call site for why
// the serving loop must not block on output.
func (s *Server) offerLine(lines chan<- []byte, request requestLine, werr *wireError) bool {
	line, err := json.Marshal(responseLine{ID: *request.ID, OK: false, Result: json.RawMessage("null"), Error: werr})
	if err != nil || len(line) > s.frameLimit {
		return false
	}
	select {
	case lines <- line:
		return true
	default:
		return false
	}
}

// send marshals one output line onto the writer channel, abandoning the send
// when the context ends so a full queue behind a stopped consumer cannot
// park the caller past cancellation. The frame limit bounds both directions:
// a line whose encoding exceeds it could not be carried by a host enforcing
// the same limit, so it is refused (and the caller surfaces its own bounded
// terminal condition) rather than emitted; a value that cannot marshal —
// near-unreachable, as every line type is a closed struct over already
// validated JSON — is refused the same way.
func (s *Server) send(ctx context.Context, lines chan<- []byte, value any) error {
	line, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode line: %w", err)
	}
	if len(line) > s.frameLimit {
		return ErrLineTooLarge
	}
	select {
	case lines <- line:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

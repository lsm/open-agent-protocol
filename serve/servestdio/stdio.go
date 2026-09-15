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
// atomic and no response is ever broken by interleaving. stderr carries
// bounded diagnostics only.
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
// the envelope, derived from the daemon's explicit shared address bound —
// serve.MaxAddressBytes — and from nothing else: no constant here mirrors
// a limit of the other transport. Three request params carry addressing
// (adapter, session_id, after); each is refused beyond the bound decoded
// by the request-shape check both transports share, and a bounded address
// costs at worst 6x decoded inside a JSON string token (\uXXXX of a
// one-byte control), so 18x the bound covers all three at their worst-case
// encoded width, with the fixed wrapper — id, op, JSON punctuation — well
// inside the 4096-byte slack (op is not an address: it is answered from a
// closed route set, and an id beyond int64 fails the decode closed). With
// both transports refusing the same addresses at the same bound before
// size can matter, their acceptance sets agree by construction; the
// allowance family that regenerated across review rounds (64 KiB → 1 MiB
// → 2 MiB → the +4 KiB slop) is closed by the shared bound, not re-sized
// against it. Provenance: the bound and this derivation are the B′
// design's structural end for that family (GH #17).
const wrapperAllowance = 18*serve.MaxAddressBytes + 4096

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

// defaultWriteQueue bounds the lines buffered for the writer goroutine and,
// as the in-flight bound of admitted work, how far the frontend runs ahead
// of the host's draining: beyond it the decode loop parks, the reader stops
// reading, and the host's own writes block — the backpressure onto the
// single consumer of stdout.
const defaultWriteQueue = 256

// DefaultShutdownTimeout bounds each stage of Run's teardown — the grace for
// a serving loop still stuck in a synchronous op, then the wait for in-flight
// work and the final output drain — once the host ends the session; a host
// that stops draining stdout cannot stretch shutdown past it.
const DefaultShutdownTimeout = 5 * time.Second

// Options tunes the frontend. The zero value is usable.
type Options struct {
	// FrameLimit bounds one line in bytes in both directions; zero means
	// DefaultFrameLimit.
	FrameLimit int
	// WriteQueue bounds the lines buffered for the writer goroutine and the
	// requests admitted in flight before the decode loop parks — beyond it
	// the host's own writes block, the backpressure onto the single
	// consumer of stdout; zero means defaultWriteQueue.
	WriteQueue int
	// ShutdownTimeout bounds each stage of Run's teardown independently —
	// the grace for a serving loop still stuck in a synchronous op, then the
	// wait for in-flight work and the final output drain. The writer may be
	// parked inside out.Write on a pipe the host stopped reading, and
	// shutdown never depends on the host's pipe; zero means
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
	shutdown    time.Duration
	logger      *log.Logger
	nextIDValue atomic.Uint64

	// mu guards shuttingDown and the work WaitGroup's admission gate: once
	// teardown begins no further workers may be Added, because a late
	// positive Add — from a decode loop abandoned mid-op that then finishes
	// and dispatches another frame — would race the Wait that may already
	// have seen the counter reach zero, which the WaitGroup contract
	// forbids, and its worker would never be waited for.
	mu           sync.Mutex
	shuttingDown bool
	work         sync.WaitGroup

	// inFlight bounds concurrently admitted workers at the write-queue
	// depth, so a host that pipelines faster than it drains stdout stops
	// the frontend at the bound instead of growing goroutines and retained
	// frames without limit: admission parks, which parks the decode loop,
	// which parks the reader, so the host's own stdin writes become the
	// backpressure. shutdownCh, closed exactly once at teardown after
	// shuttingDown is set, releases an admission parked on the bound so the
	// abandoned loop still ends inside its grace window.
	inFlight   chan struct{}
	shutdownCh chan struct{}
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
	shutdown := options.ShutdownTimeout
	if shutdown <= 0 {
		shutdown = DefaultShutdownTimeout
	}
	logger := options.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Server{
		hub: hub, schema: schema, frameLimit: frameLimit, writeQueue: writeQueue,
		shutdown: shutdown, logger: logger,
		inFlight: make(chan struct{}, writeQueue), shutdownCh: make(chan struct{}),
	}, nil
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

// ErrShutdownStalled reports that a teardown stage outlived its bounded
// window and was abandoned rather than waited on: the serving loop never
// finished inside its grace (a synchronous op that hung), admitted work
// never settled (a dispatch ignoring the teardown cancellation), or the
// final output drain never completed — the writer demonstrably parked
// inside out.Write on a pipe the host stopped reading. The caller's
// bounded session sweep and the process exit still happen. It is the
// summary of last resort: a latched protocol fault, write failure, or
// read failure outranks it at the single selection site.
var ErrShutdownStalled = errors.New("servestdio: shutdown outlived its bounded window; the stalled stage was abandoned")

// terminal is a write-once custody cell: its single owning goroutine
// stores its terminal condition and closes done exactly once, and any
// number of readers may wait on done and read err, which never moves
// (INV-B). A closed channel is a broadcast, so no reader can steal the
// value from another, and a reporter abandoned into a cell nobody reads
// loses nothing — the sole-buffered-value handoffs this type replaced
// were the defect class of the fourth review round: a zombie consumer
// could drain the one copy of the writer's failure into a channel the
// owner had already abandoned.
type terminal struct {
	once sync.Once
	err  error         // set before done closes; safe to read after
	done chan struct{} // closed exactly once, after err is set
}

func newTerminal() *terminal { return &terminal{done: make(chan struct{})} }

// report stores the cell's terminal condition. Exactly one goroutine — the
// cell's owner — reports, at most once; the once drops any defensive
// second report an abandoned owner might race into.
func (t *terminal) report(err error) { t.once.Do(func() { t.err = err; close(t.done) }) }

// ready exposes the write latch for selects.
func (t *terminal) ready() <-chan struct{} { return t.done }

// outcome waits for the latch and reads the value.
func (t *terminal) outcome() error { <-t.done; return t.err }

// peek reads the value if the latch has fired. The selection site samples
// every cell this way: a report that lands between its stage's window
// expiring and the single read point still informs the outcome.
func (t *terminal) peek() (error, bool) {
	select {
	case <-t.done:
		return t.err, true
	default:
		return nil, false
	}
}

// writerProbe is the output-bound evidence the supervision owner reads
// when a drain window expires (INV-A): inWrite is latched around every
// out.Write call and progress bumps once per completed write, so a pending
// line with the writer demonstrably inside out.Write — the one mid-flight
// stall that may appear in any termination decision — is observable as
// the truthful cause of an abandoned drain. The probe is evidence for the
// report, never a trigger: nothing reads it to end anything.
type writerProbe struct {
	inWrite  atomic.Bool
	progress atomic.Uint64
}

// Run serves requests from in until the host closes it (clean end), the
// context ends, the output fails, or a line violates the framing (fail
// closed). Responses are written to out by exactly one writer goroutine, one
// JSON object per LF-terminated line; a write failure ends serving — a host
// that stopped reading stdout receives nothing further, so no work is
// admitted behind its back. The frontend is a codec and owns no session
// lifetime: the caller sweeps the hub after Run returns, on every exit path.
// Run itself never writes to stderr; it returns the failure for the caller
// to report. The frontend serves one session: teardown closes admission for
// good, so a Server is not reusable across Run calls — embed one frontend
// per session, as the CLI does per process.
//
// The supervision owner. Run alone may end the session, and only on
// host-bound evidence — exactly these reports, each owned by one custody
// cell (INV-A):
//
//   - readerEnd: the host's input ended — io.EOF at a line boundary (the
//     clean close, and how a host process exit manifests on stdin), a
//     framing defect, or the input's own read failure;
//   - loopEnd: the host sent a line that fails the framing or request
//     shape — the loop's numbered *MalformedLineError translation;
//   - writerFail: the host's output failed — out.Write returned an error
//     (the host closed stdout);
//   - the caller's context: the embedding host ended the session.
//
// Everything else — busy workers, queued frames, a saturated admission
// bound, a full lines queue — is work-bound saturation: backpressure onto
// the host, never evidence, and no timer exists anywhere in serving, so
// nothing mid-flight can time out (the false-kill class of the fourth
// round, where a delivery window armed on consumer idleness made
// ShutdownTimeout a request-execution timeout for busy-but-draining
// sessions, is unexpressible). The first report moves the machine from
// serving to hostEnded; teardown then runs three bounded stages — the
// grace for a loop still stuck in a synchronous op, the wait for admitted
// work, and the final output drain — each abandoned, not waited on, when
// its window expires. The single selection site at the end returns
// exactly one error by the documented precedence.
func (s *Server) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	// A worker's dispatch runs on the frontend's own context, cancelled at
	// hostEnded below so in-flight handlers detach from the hub and settle
	// as error responses; its response send runs on the caller's context,
	// so a send parked behind a slow consumer delivers through the
	// writer's bounded drain rather than being dropped by the frontend's
	// own teardown, and is cut loose only when the caller abandons the
	// session.
	callerCtx := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	lines := make(chan []byte, s.writeQueue)
	stop := make(chan struct{})
	writerFail, writerEnd := newTerminal(), newTerminal()
	probe := &writerProbe{}
	go writeLines(out, lines, stop, writerFail, writerEnd, probe)

	// The reader reports its own end on its custody cell before the final
	// frame is delivered, because the loop may be parked in admission
	// behind a saturated bound and never take that frame: the owner stays
	// bounded even then.
	readerEnd := newTerminal()
	frames := make(chan frameResult)
	go readFrames(in, s.frameLimit, frames, readerEnd)

	loopEnd := newTerminal()
	go func() { loopEnd.report(s.decodeLoop(ctx, callerCtx, frames, lines, writerFail)) }()

	// serving: the state's event set is exactly the four host-bound
	// reports above — no timer case, no saturation case is expressible
	// here, which is the invariant itself.
	select {
	case <-readerEnd.ready():
	case <-loopEnd.ready():
	case <-writerFail.ready():
	case <-callerCtx.Done():
	}

	// hostEnded: cancel detaches in-flight handlers from the hub and
	// releases the loop's admission parks; the admission latch closes
	// atomically with it, so no late WaitGroup Add can race the work Wait
	// below.
	cancel()
	s.mu.Lock()
	s.shuttingDown = true
	s.mu.Unlock()
	close(s.shutdownCh)

	// The grace window is created here, at the host's end — never at Run
	// start — so a session that served longer than the window still grants
	// the loop its full grace. A loop that outlives the window is
	// abandoned: it parks wherever it is and is reclaimed at process exit.
	grace := time.NewTimer(s.shutdown)
	defer grace.Stop()
	graceSettled := true
	select {
	case <-loopEnd.ready():
	case <-grace.C:
		graceSettled = false
	}

	// tearingDown, stage one: the admitted work. A worker that ignores the
	// cancellation holds this window only, then is abandoned — its
	// response send parks and is reclaimed at process exit.
	workDone := make(chan struct{})
	go func() { s.work.Wait(); close(workDone) }()
	workWindow := time.NewTimer(s.shutdown)
	defer workWindow.Stop()
	workSettled := true
	select {
	case <-workDone:
	case <-workWindow.C:
		workSettled = false
	}

	// tearingDown, stage two: the final drain. The writer may be parked
	// inside out.Write on a pipe the host stopped reading, and teardown
	// never depends on the host's pipe: the window bounds the wait, and
	// the writer is abandoned past it.
	close(stop)
	drainWindow := time.NewTimer(s.shutdown)
	defer drainWindow.Stop()
	drainSettled := true
	select {
	case <-writerEnd.ready():
	case <-drainWindow.C:
		drainSettled = false
		// Output-bound evidence, read for the report only: a writer
		// latched inside out.Write holds a pending line it demonstrably
		// cannot write, which is the truthful cause of this abandonment.
		if probe.inWrite.Load() {
			s.logger.Printf("servestdio: drain abandoned with the writer parked inside out.Write (progress %d)", probe.progress.Load())
		}
	}

	// returned: the single selection site. Every cell is sampled once,
	// best-effort — a report landing between its stage's expiry and this
	// read still informs the outcome — and the documented precedence picks
	// the one error returned.
	report := sessionReport{
		loopAbandoned:  !graceSettled,
		workAbandoned:  !workSettled,
		drainAbandoned: !drainSettled,
	}
	if loopErr, ok := loopEnd.peek(); ok {
		report.loopErr = loopErr
	}
	if writeErr, ok := writerFail.peek(); ok {
		report.writerErr = writeErr
	}
	if readerErr, ok := readerEnd.peek(); ok {
		report.readerErr = readerErr
	}
	return selectOutcome(report)
}

// sessionReport is the latched state the single selection site reads: each
// custody cell's outcome (nil where a cell latched clean or never latched)
// and which teardown stages outlived their windows. It is the model
// harness's subject — selectOutcome below is a pure function of it, and
// the harness enumerates its whole reachable input space.
type sessionReport struct {
	loopErr                                      error // the loop's numbered protocol-fault translation, or nil
	writerErr                                    error // the output's own failure, or nil
	readerErr                                    error // the input's terminal: io.EOF, a defect, or a read failure
	loopAbandoned, workAbandoned, drainAbandoned bool
}

// selectOutcome is the supervision owner's one error-selection site
// (INV-B). Precedence, first match wins:
//
//  1. loopErr — the host sent a line that fails the protocol's framing or
//     shape; the loop's numbered translation is the one bounded diagnostic
//     the caller reports.
//  2. writerErr — the output's own failure outranks every stall: a host
//     that broke stdout owns the session's end, even when the loop or the
//     work wait was abandoned first.
//  3. readerErr, unless io.EOF — the host's input ended on a framing
//     defect or its own read failure, including the case where the
//     abandoned loop never produced its numbered translation; the raw
//     condition is still the honest report.
//  4. ErrShutdownStalled — some teardown stage outlived its bounded
//     window and was abandoned; the caller's sweep and exit still happen.
//  5. nil — the clean end.
func selectOutcome(report sessionReport) error {
	switch {
	case report.loopErr != nil:
		return report.loopErr
	case report.writerErr != nil:
		return report.writerErr
	case report.readerErr != nil && !errors.Is(report.readerErr, io.EOF):
		return report.readerErr
	case report.loopAbandoned || report.workAbandoned || report.drainAbandoned:
		return ErrShutdownStalled
	default:
		return nil
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
// outcome. Its terminal read outcome — io.EOF for the clean host close, a
// framing defect, or the input's own read failure — is reported to its
// custody cell before the final frame is delivered, because the decode
// loop may be parked in admission behind a saturated bound and never take
// that frame; the owner's teardown stays bounded even then. A delivery
// park is backpressure and never ends anything: no delivery-stall window
// exists — the window this replaced armed on consumer idleness and killed
// busy-but-draining sessions (INV-A) — and a draining consumer, however
// slow, frees the whole chain: each delivered line frees the writer, the
// bound, and the delivery in turn. Neither channel is ever closed: a
// reader abandoned by teardown parks on its send and is reclaimed at
// process exit, the same discipline as the writer's channel.
func readFrames(in io.Reader, limit int, frames chan<- frameResult, end *terminal) {
	reader := bufio.NewReader(in)
	for {
		frame, err := readFrame(reader, limit)
		if err != nil {
			end.report(err)
			frames <- frameResult{frame: frame, err: err}
			return
		}
		frames <- frameResult{frame: frame}
	}
}

// decodeLoop consumes request frames, dispatching each as it arrives, until
// a clean end, an output failure, or a framing defect. Each request is
// dispatched to its own admitted worker goroutine, so requests pipeline and
// run concurrently, correlated by id — one worker per request, exactly as
// the HTTP server runs one handler per connection — and the loop itself
// never blocks on an op, only on the next frame or, when the in-flight
// bound is reached, on admission: that park is backpressure — the
// backpressure that stops a host writing faster than it drains — and never
// a termination signal (INV-A). A worker's dispatch runs on the frontend's
// context — cancelled at hostEnded so in-flight handlers detach from the
// hub — while its response send runs on the caller's, so parked sends
// deliver through the writer's bounded drain. Reading runs on its own
// goroutine so a context end is observed while waiting for the next line,
// not only between lines — an embedding host that cancels without also
// closing stdin still gets Run back — and the writer's failure arrives the
// same way, as a custody cell latch: the loop observes it, ends, and
// returns nil — it never copies the failure into its own cell, because
// custody is not transferable (INV-B); the value stays in the writer's
// cell where the owner, and any other reader, can still read it even after
// this loop is long gone. The loop-top pre-check narrows, but cannot
// eliminate — a select chooses uniformly among ready cases, so a frame
// arriving with the cancellation can still be taken — the dispatch of
// frames under an already-dead context; the admission gate and the bounded
// teardown own what remains. A framing defect — anything readFrame or
// decodeRequest refuses — fails the frontend closed as
// *MalformedLineError; op-level refusals are responses the host can
// correct, and the loop reads on past them.
func (s *Server) decodeLoop(ctx, callerCtx context.Context, frames <-chan frameResult, lines chan<- []byte, writerFail *terminal) error {
	number := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-writerFail.ready():
			return nil // the failure lives in its own cell; custody is not copied
		default:
		}
		select {
		case <-ctx.Done():
			return nil
		case <-writerFail.ready():
			return nil // the failure lives in its own cell; custody is not copied
		case result := <-frames:
			number++
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {
					return nil
				}
				var defect *frameDefect
				if errors.As(result.err, &defect) {
					return &MalformedLineError{Line: number, Detail: defect.Error()}
				}
				// A read failure is not the host's protocol fault: fail
				// closed, but let the caller see the input's own error.
				return result.err
			}
			request, err := decodeRequest(result.frame)
			if err != nil {
				return &MalformedLineError{Line: number, Detail: err.Error()}
			}
			if s.admit() {
				go func(request requestLine) {
					defer func() {
						s.work.Done()
						<-s.inFlight
					}()
					s.serveRequest(ctx, callerCtx, request, lines)
				}(request)
			}
		}
	}
}

// writeLines is the single ordered writer: it appends the LF terminator to
// every marshaled line and writes it whole, so lines never interleave. It
// ends when stopped, draining the lines already queued; a write failure
// (the host closed stdout) is reported to the fail cell at the first
// failure — a broadcast latch, so the decode loop and the supervision
// owner both observe the same value without either consuming it — while
// the drain continues, so producers blocked on the channel still hand off
// instead of deadlocking. Its own outcome — the remembered failure, or nil
// once the drain completes — is reported to the end cell, and the probe's
// inWrite/progress pair is maintained around every write for the owner's
// stall evidence. The channel is never closed: an abandoned producer parks
// on its send and is reclaimed by process exit rather than panicking on a
// closed channel.
func writeLines(out io.Writer, lines <-chan []byte, stop <-chan struct{}, fail, end *terminal, probe *writerProbe) error {
	var failure error
	write := func(line []byte) {
		if failure != nil {
			return
		}
		probe.inWrite.Store(true)
		_, err := out.Write(append(line, '\n'))
		probe.inWrite.Store(false)
		probe.progress.Add(1)
		if err != nil {
			failure = err
			fail.report(failure) // broadcast: every reader observes the same value
		}
	}
	for {
		select {
		case line := <-lines:
			write(line)
		case <-stop:
			for {
				select {
				case line := <-lines:
					write(line)
				default:
					end.report(failure)
					return failure
				}
			}
		}
	}
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

// admit registers one worker with the teardown WaitGroup, refusing once
// shutdown began: a late positive Add — from a decode loop abandoned mid-op
// that then finishes and dispatches another frame — would race the Wait that
// may already have seen the counter reach zero, which the WaitGroup contract
// forbids, and its worker would never be waited for. A refused request is
// dropped without a response: shutdown owns the process, and the host that
// ended the session is not waiting for one.
//
// The in-flight bound is acquired first: when every slot is held by a
// worker whose response has not reached the writer, admission parks — and
// with it the decode loop and the reader — so a host that pipelines faster
// than it drains stdout is stopped at the bound rather than growing
// unbounded workers, each holding its request frame and marshaled response.
// A slot acquired in the race against teardown is checked against the latch
// under mu and released unused, so closing the bound and refusing late
// Adds stay one atomic decision.
func (s *Server) admit() bool {
	select {
	case s.inFlight <- struct{}{}:
	case <-s.shutdownCh:
		return false
	}
	s.mu.Lock()
	if s.shuttingDown {
		s.mu.Unlock()
		<-s.inFlight // the slot was acquired as teardown began; it is not used
		return false
	}
	s.work.Add(1)
	s.mu.Unlock()
	return true
}

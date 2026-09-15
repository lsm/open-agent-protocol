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
	"time"
	"unicode/utf8"

	"github.com/lsm/open-agent-protocol/serve"
)

// DefaultFrameLimit bounds one NDJSON line in both directions. It matches
// servehttp's request-body budget so both transports of the same daemon
// accept the same requests, with the adapters' rpc-codec discipline — a
// bounded line, refused rather than split — applied to the framing.
const DefaultFrameLimit = 16 << 20

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
	// WriteQueue bounds the lines buffered for the writer goroutine before
	// producers block; zero means defaultWriteQueue.
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
	hub        *serve.Hub
	frameLimit int
	writeQueue int
	shutdown   time.Duration
	logger     *log.Logger

	// mu guards shuttingDown and the work WaitGroup's admission gate: once
	// teardown begins no further workers may be Added, because a late
	// positive Add — from a decode loop abandoned mid-op that then finishes
	// and dispatches another frame — would race the Wait that may already
	// have seen the counter reach zero, which the WaitGroup contract
	// forbids, and its worker would never be waited for.
	mu           sync.Mutex
	shuttingDown bool
	work         sync.WaitGroup
}

// New returns a frontend over the hub.
func New(hub *serve.Hub, options Options) (*Server, error) {
	if hub == nil {
		return nil, errors.New("servestdio: hub is required")
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
	return &Server{hub: hub, frameLimit: frameLimit, writeQueue: writeQueue, shutdown: shutdown, logger: logger}, nil
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

// ErrShutdownStalled reports that shutdown outlived its bounded windows:
// the host ended the session and either the serving loop never finished —
// a synchronous op that hung, a response send parked behind a stopped
// consumer — or in-flight work or the final output drain never completed
// because the host stopped reading stdout. The stalled stage is abandoned
// rather than waited on, so the caller's bounded session sweep and the
// process exit still happen.
var ErrShutdownStalled = errors.New("servestdio: shutdown outlived its bounded window; the stalled stage was abandoned")

// Run serves requests from in until the host closes it (clean end), the
// context ends, the output fails, or a line violates the framing (fail
// closed). Responses are written to out by exactly one writer goroutine, one
// JSON object per LF-terminated line; a write failure ends serving — a host
// that stopped reading stdout receives nothing further, so no work is
// admitted behind its back. The frontend is a codec and owns no session
// lifetime: the caller sweeps the hub after Run returns, on every exit path.
// Run itself never writes to stderr; it returns the failure for the caller
// to report.
//
// Shutdown is bounded from the moment the host ends the session — stdin
// closed, the read failed, or the context cancelled — not from when the
// serving loop notices: the loop may be stuck inside a synchronous op (a
// hung adapter call, a response send parked behind a stopped consumer), and
// Options.ShutdownTimeout bounds each stage independently. A stage that
// outlives its window is abandoned and Run returns ErrShutdownStalled, so
// the caller's bounded session sweep and the process exit still happen;
// nothing ever waits indefinitely on the host's pipe or a stuck op. The
// frontend serves one session: teardown closes admission for good, so a
// Server is not reusable across Run calls — embed one frontend per
// session, as the CLI does per process.
func (s *Server) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	// A worker's dispatch runs on the frontend's own context, cancelled in
	// the teardown below so in-flight handlers detach from the hub and
	// settle as error responses; its response send runs on the caller's
	// context, so a send parked behind a slow consumer delivers through the
	// writer's bounded drain rather than being dropped by the frontend's
	// own teardown, and is cut loose only when the caller abandons the
	// session.
	callerCtx := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	lines := make(chan []byte, s.writeQueue)
	stop := make(chan struct{})
	writerFailed := make(chan struct{}, 1)
	writerDone := make(chan error, 1)
	go func() { writerDone <- writeLines(out, lines, stop, writerFailed) }()

	// The reader reports its own end on a buffered side channel, because
	// the serving loop may be stuck inside a synchronous op and never
	// consume the final frame: shutdown must be bounded even then.
	readerDone := make(chan error, 1)
	frames := make(chan frameResult)
	go readFrames(in, s.frameLimit, frames, readerDone)

	serveDone := make(chan error, 1)
	go func() { serveDone <- s.decodeLoop(ctx, callerCtx, frames, lines, writerFailed) }()

	// End of input: the serving loop returning is the session's normal
	// end. When the host has ended the session while the loop is stuck, a
	// fresh window — started at the host's end, never at Run's start, so a
	// daemon that served longer than the window still grants the full
	// grace — bounds how much longer the stuck op gets before it is
	// abandoned.
	serveGrace := func() (result error, finished bool) {
		window := time.NewTimer(s.shutdown)
		defer window.Stop()
		select {
		case result = <-serveDone:
			return result, true
		case <-window.C:
			return nil, false
		}
	}
	var err error
	stuck := false
	select {
	case err = <-serveDone:
	case <-readerDone:
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

	// Teardown: cancel detaches in-flight handlers from the hub and the
	// lines channel, and the admission gate closes so a decode loop
	// abandoned mid-op cannot Add a late worker racing this Wait. The
	// second window bounds waiting for the admitted work and for the
	// writer's final drain — the writer may be parked inside out.Write on a
	// pipe the host stopped reading, and shutdown must never depend on the
	// host's pipe. The lines channel is never closed: producers abandoned
	// by the window park on their send and are reclaimed by process exit.
	cancel()
	s.mu.Lock()
	s.shuttingDown = true
	s.mu.Unlock()
	workDone := make(chan struct{})
	go func() { s.work.Wait(); close(workDone) }()
	drainWindow := time.NewTimer(s.shutdown)
	defer drainWindow.Stop()
	drained := false
	select {
	case <-workDone:
		close(stop)
		select {
		case writeErr := <-writerDone:
			drained = true
			if err == nil {
				err = writeErr
			}
		case <-drainWindow.C:
		}
	case <-drainWindow.C:
	}
	if (stuck || !drained) && err == nil {
		// A stall is reported only when it is the session's own terminal
		// condition: under an already-reported error — the malformed line,
		// the read failure, the writer failure — joining a second error
		// would break the one-bounded-diagnostic fail-closed contract, and
		// the caller's sweep and exit happen regardless.
		err = ErrShutdownStalled
	}
	return err
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
// outcome. The terminal read outcome — io.EOF for the clean host close, a
// framing defect, or the input's own read failure — is also reported on
// done, buffered and before the final frame is delivered, because the
// consumer may be stuck inside a synchronous op and never take that frame;
// shutdown stays bounded even then. Neither channel is ever closed: a reader
// abandoned by a context end parks on its send and is reclaimed by process
// exit, the same discipline as the writer's channel.
func readFrames(in io.Reader, limit int, frames chan<- frameResult, done chan<- error) {
	reader := bufio.NewReader(in)
	for {
		frame, err := readFrame(reader, limit)
		if err != nil {
			done <- err
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
// never blocks on an op, only on the next frame. A worker's dispatch runs
// on the frontend's context — cancelled at teardown so in-flight handlers
// detach from the hub — while its response send runs on the caller's, so
// parked sends deliver through the writer's bounded drain. Reading runs on
// its own goroutine so a context end is observed while waiting for the next
// line, not only between lines — an embedding host that cancels without also
// closing stdin still gets Run back — and writerFailed carries the writer's
// failure the same way, so a host that stopped reading stdout ends serving
// instead of admitting work whose responses are silently discarded. The
// loop-top pre-check narrows, but cannot eliminate — a select chooses
// uniformly among ready cases, so a frame arriving with the cancellation can
// still be taken — the dispatch of frames under an already-dead context; the
// admission gate and the bounded teardown own what remains. A framing
// defect — anything readFrame or decodeRequest refuses — fails the frontend
// closed as *MalformedLineError; op-level refusals are responses the host
// can correct, and the loop reads on past them.
func (s *Server) decodeLoop(ctx, callerCtx context.Context, frames <-chan frameResult, lines chan<- []byte, writerFailed <-chan struct{}) error {
	number := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-writerFailed:
			return nil
		default:
		}
		select {
		case <-ctx.Done():
			return nil
		case <-writerFailed:
			return nil
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
					defer s.work.Done()
					s.serveRequest(ctx, callerCtx, request, lines)
				}(request)
			}
		}
	}
}

// writeLines is the single ordered writer: it appends the LF terminator to
// every marshaled line and writes it whole, so lines never interleave. It
// ends when stopped, draining the lines already queued; a write failure (the
// host closed stdout) is remembered while the drain continues, so producers
// blocked on the channel still hand off instead of deadlocking, and is
// signalled on failed exactly once so serving stops admitting work behind a
// dead output. The channel is never closed: an abandoned producer parks on
// its send and is reclaimed by process exit rather than panicking on a
// closed channel.
func writeLines(out io.Writer, lines <-chan []byte, stop <-chan struct{}, failed chan<- struct{}) error {
	var failure error
	write := func(line []byte) {
		if failure != nil {
			return
		}
		if _, err := out.Write(append(line, '\n')); err != nil {
			failure = err
			failed <- struct{}{} // buffered one-slot signal, sent only on the first failure
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
func (s *Server) admit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shuttingDown {
		return false
	}
	s.work.Add(1)
	return true
}

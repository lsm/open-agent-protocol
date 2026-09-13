// Package servestdio exposes a serve.Hub over newline-delimited JSON on a
// subprocess's stdin and stdout: the transport of `oap serve --stdio`, for
// hosts that spawn the oap binary as a child process — the spawn-a-binary
// embedding model — with no port, TLS, or authentication story, since
// spawning the process is the authorization. The env allowlist of the
// registry config still governs adapter credentials exactly as over HTTP.
//
// The operations mirror serve/servehttp one to one with identical semantics;
// only the framing differs. Host → daemon lines are one JSON object each,
// {"id":N,"op":...,...params}, and every op is repeatable and may be sent
// concurrently, correlated by id. Daemon → host lines are responses
// {"id":N,"ok":true,"result":...} / {"id":N,"ok":false,"error":...}, run-event
// lines {"event":"envelope",...} for each `events` op's subscription, and
// named signal lines for the replay-gap, overflow, and session-closed
// conditions that SSE carries as named events. One writer goroutine
// interleaves them, so every line is atomic and per-session event order is
// never broken by interleaving; stderr carries bounded diagnostics only.
//
// Framing is strict: exactly one JSON object per LF-terminated line with a
// bounded line length (the same discipline the adapters' internal rpc codecs
// apply to their own child stdio). A line that is not a valid request frame
// fails closed — Run returns *MalformedLineError, the host reads one bounded
// diagnostic, and the process exits non-zero, mirroring how every OAP adapter
// treats a malformed frame from its own agent.
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

// DefaultFrameLimit bounds one NDJSON line in both directions. It matches
// servehttp's request-body budget so both transports of the same daemon
// accept the same requests, with the adapters' rpc-codec discipline — a
// bounded line, refused rather than split — applied to the framing.
const DefaultFrameLimit = 16 << 20

// minFrameLimit is the smallest usable FrameLimit: every control line — an
// error response with no result, a terminal signal — encodes well under it,
// so a correlated answer always exists even when a result has to be refused.
// New rejects smaller limits rather than accepting a configuration whose
// own refusals could not be delivered.
const minFrameLimit = 256

// defaultWriteQueue bounds the lines buffered for the writer goroutine;
// beyond it producers block, which is the documented backpressure onto the
// single consumer of stdout.
const defaultWriteQueue = 256

// DefaultShutdownTimeout bounds Run's teardown wait for in-flight work and
// the final output drain once the host ends the session; a host that stops
// draining stdout cannot stretch shutdown past it.
const DefaultShutdownTimeout = 5 * time.Second

// Options tunes the frontend. The zero value is usable.
type Options struct {
	// FrameLimit bounds one line in bytes in both directions; zero means
	// DefaultFrameLimit.
	FrameLimit int
	// WriteQueue bounds the lines buffered for the writer goroutine before
	// producers block; zero means defaultWriteQueue.
	WriteQueue int
	// ShutdownTimeout bounds Run's teardown wait for in-flight work and the
	// final output drain once the host ends the session; zero means
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

	// mu guards shuttingDown and the work-WaitGroup admission it gates: once
	// teardown begins no further workers may be Added, because a late
	// positive Add — from a decodeLoop abandoned mid-op that then finishes —
	// would race the Wait that may already have seen the counter reach zero.
	mu           sync.Mutex
	shuttingDown bool
	work         sync.WaitGroup
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
	}, nil
}

// Hub returns the hub the frontend serves.
func (s *Server) Hub() *serve.Hub { return s.hub }

// MalformedLineError reports one line that violates the NDJSON framing or the
// request shape. The daemon fails closed on it: Run stops reading, already
// admitted work is settled and flushed, and the caller exits non-zero after
// printing this one bounded diagnostic.
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
// the host ended the session and either a synchronous op never finished —
// an adapter open that hung, a response send blocked behind a stopped
// consumer — or the final output drain never completed because the host
// stopped reading stdout. The stalled stage is abandoned rather than waited
// on, so the caller's bounded session sweep and the process exit still
// happen.
var ErrShutdownStalled = errors.New("servestdio: shutdown outlived its bounded window; the stalled stage was abandoned")

// frameResult is one line read from the host: frame carries the line without
// its terminator, err the condition that ended the read (io.EOF for the clean
// host close, a framing defect, or a read failure).
type frameResult struct {
	frame []byte
	err   error
}

// Run serves requests from in until the host closes it (clean shutdown), the
// context ends, or a line violates the framing (fail closed). Responses and
// events are interleaved onto out by exactly one writer goroutine, one JSON
// object per LF-terminated line. The frontend is a codec and owns no session
// lifetime: the caller sweeps the hub after Run returns, as the CLI does on
// every exit path. Run itself never writes to stderr.
//
// Shutdown is bounded from the moment the host ends the session — stdin
// closed, the read failed, or the context cancelled — not from when the
// serving loop notices: the loop may be stuck inside a synchronous op (a
// hung adapter open, a response send blocked behind a stopped consumer),
// and Options.ShutdownTimeout bounds each stage independently. A stage that
// outlives its window is abandoned and Run returns ErrShutdownStalled, so
// the caller's bounded session sweep and the process exit still happen;
// nothing ever waits indefinitely on the host's pipe or a stuck adapter.
func (s *Server) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	lines := make(chan []byte, s.writeQueue)
	stop := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() { writerDone <- writeLines(out, lines, stop) }()

	// The reader reports its own end on a buffered side channel, because
	// decodeLoop may be stuck inside a synchronous op and never consume the
	// final frame: shutdown must be bounded even then.
	readerDone := make(chan error, 1)
	frames := make(chan frameResult)
	go readFrames(in, s.frameLimit, frames, readerDone)

	serveDone := make(chan error, 1)
	go func() { serveDone <- s.decodeLoop(ctx, frames, lines) }()

	// End of input: decodeLoop returning is the session's normal end. When
	// the host has ended the session while decodeLoop is stuck, a fresh
	// window — started at the host's end, never at Run's start, so a daemon
	// that served longer than the window still grants the full grace —
	// bounds how much longer the stuck op gets before it is abandoned.
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

	// Teardown: cancel detaches pumps and in-flight handlers from the hub,
	// and admission closes so a decodeLoop abandoned mid-op cannot Add a
	// late worker racing this Wait; the second window bounds waiting for the
	// workers and for the writer's final drain — the writer may be blocked
	// inside out.Write on a pipe the host stopped reading, and shutdown must
	// never depend on the host's pipe.
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
		err = ErrShutdownStalled
	}
	return err
}

// writeLines is the single ordered writer: it appends the LF terminator to
// every marshaled line and writes it whole, so lines never interleave. It
// ends when stopped, draining the lines already queued; a write failure (the
// host closed stdout) is remembered while the drain continues, so producers
// blocked on the channel still hand off instead of deadlocking. The channel
// is never closed: an abandoned producer parks on its send and is reclaimed
// by process exit rather than panicking on a closed channel.
func writeLines(out io.Writer, lines <-chan []byte, stop <-chan struct{}) error {
	var failure error
	write := func(line []byte) {
		if failure != nil {
			return
		}
		if _, err := out.Write(append(line, '\n')); err != nil {
			failure = err
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

// readFrames reads bounded NDJSON lines from in and forwards each with its
// outcome. The framing rules mirror the adapter rpc codecs: a line is
// LF-terminated, carries no CR, is non-empty, valid UTF-8, and within the
// limit; an unterminated final line is a defect, while EOF at a line boundary
// is the clean host close. The terminal read outcome is also reported on
// done — buffered, and before the final frame is delivered — because the
// consumer may be stuck and never take that frame; shutdown stays bounded
// even then.
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

func readFrame(reader *bufio.Reader, limit int) ([]byte, error) {
	frame := make([]byte, 0, min(limit, 4096))
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(frame)+len(fragment) > limit+1 {
			return nil, fmt.Errorf("line exceeds the %d-byte frame limit", limit)
		}
		frame = append(frame, fragment...)
		switch {
		case err == nil:
			frame = frame[:len(frame)-1]
			switch {
			case bytes.IndexByte(frame, '\r') >= 0:
				return nil, errors.New("carriage return is not valid framing")
			case len(frame) == 0:
				return nil, errors.New("empty line")
			case !utf8.Valid(frame):
				return nil, errors.New("line is not UTF-8")
			}
			return frame, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(frame) > 0:
			return nil, errors.New("unterminated final line")
		default:
			return nil, err
		}
	}
}

// decodeLoop consumes request frames, dispatching each as it arrives. The
// registration ops run synchronously here: `open` registers its session and
// `events` registers its subscription before the next line is read, so a
// host that pipelines the canonical sequence — open, events, submit —
// cannot race the registration and miss the run's first envelope; this is
// the stdio counterpart of the HTTP client's ack-before-addressing and
// subscribe-before-submit disciplines. The execution ops run concurrently,
// one goroutine per request, exactly as the HTTP server runs one handler
// per connection.
func (s *Server) decodeLoop(ctx context.Context, frames <-chan frameResult, lines chan<- []byte) error {
	number := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case result := <-frames:
			number++
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {
					return nil
				}
				return &MalformedLineError{Line: number, Detail: result.err.Error()}
			}
			request, err := decodeRequest(result.frame)
			if err != nil {
				return &MalformedLineError{Line: number, Detail: err.Error()}
			}
			switch request.Op {
			case opEvents:
				s.serveEvents(ctx, request, lines)
			case opOpen:
				s.serveOpen(ctx, request, lines)
			default:
				if s.admit() {
					go func(request requestLine) {
						defer s.work.Done()
						s.serveRequest(ctx, request, lines)
					}(request)
				}
			}
		}
	}
}

// requestLine is one host → daemon line. The id correlates the response; the
// params each op accepts are enforced per op, so a well-formed line carrying
// params its op does not define is refused as a response, not a framing
// defect.
type requestLine struct {
	ID        *int64          `json:"id"`
	Op        string          `json:"op"`
	Adapter   string          `json:"adapter,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	After     json.RawMessage `json:"after,omitempty"`
	Request   json.RawMessage `json:"request,omitempty"`
}

// decodeRequest parses one frame into a request line. Anything that is not a
// JSON object with exactly the protocol's fields and a numeric id — invalid
// JSON, a non-object, an unknown field, a mistyped or missing id — is a
// framing defect the daemon fails closed on: the host speaks a protocol this
// frontend cannot correlate.
func decodeRequest(frame []byte) (requestLine, error) {
	var request requestLine
	decoder := json.NewDecoder(bytes.NewReader(frame))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return requestLine{}, fmt.Errorf("invalid JSON request: %v", trimMessage(err.Error()))
	}
	if decoder.More() {
		return requestLine{}, errors.New("invalid JSON request: trailing data after the request object")
	}
	if request.ID == nil {
		return requestLine{}, errors.New("invalid JSON request: id is required")
	}
	if request.Op == "" {
		return requestLine{}, errors.New("invalid JSON request: op is required")
	}
	return request, nil
}

// send marshals one output line onto the writer channel. The frame limit
// bounds both directions: a line whose encoding exceeds it could not be
// carried by a host enforcing the same limit, so it is refused (and the
// caller surfaces its own bounded terminal condition) rather than emitted; a
// value that cannot marshal — near-unreachable, as every line type is a
// closed struct over already validated JSON — is refused the same way.
func (s *Server) send(lines chan<- []byte, value any) error {
	line, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode line: %w", err)
	}
	if len(line) > s.frameLimit {
		return ErrLineTooLarge
	}
	lines <- line
	return nil
}

func (s *Server) nextID(kind string) string {
	return fmt.Sprintf("oap-%s-%d", kind, s.nextIDValue.Add(1))
}

// admit registers one worker with the teardown WaitGroup, refusing once
// shutdown began: a late positive Add — from a decodeLoop abandoned mid-op
// that then finished and dispatched another frame — would race the Wait that
// may already have seen the counter reach zero, which the WaitGroup contract
// forbids, and its worker would never be waited for.
func (s *Server) admit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shuttingDown {
		return false
	}
	s.work.Add(1)
	return true
}

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
	"unicode/utf8"

	"github.com/lsm/open-agent-protocol/serve"
	"github.com/lsm/open-agent-protocol/validation"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// DefaultFrameLimit bounds one NDJSON line in both directions, matching the
// adapter rpc codecs' frame budget.
const DefaultFrameLimit = 8 << 20

// defaultWriteQueue bounds the lines buffered for the writer goroutine;
// beyond it producers block, which is the documented backpressure onto the
// single consumer of stdout.
const defaultWriteQueue = 256

// Options tunes the frontend. The zero value is usable.
type Options struct {
	// FrameLimit bounds one request line in bytes; zero means
	// DefaultFrameLimit.
	FrameLimit int
	// WriteQueue bounds the lines buffered for the writer goroutine before
	// producers block; zero means defaultWriteQueue.
	WriteQueue int
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
	logger      *log.Logger
	nextIDValue atomic.Uint64
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
	writeQueue := options.WriteQueue
	if writeQueue <= 0 {
		writeQueue = defaultWriteQueue
	}
	logger := options.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Server{
		hub: hub, schema: schema, frameLimit: frameLimit, writeQueue: writeQueue, logger: logger,
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
func (s *Server) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	lines := make(chan []byte, s.writeQueue)
	var work sync.WaitGroup
	writerDone := make(chan error, 1)
	go func() { writerDone <- writeLines(out, lines) }()

	// The reader goroutine owns blocking reads off the select below, so a
	// context cancellation does not have to wait for the next stdin byte.
	// Its channel is never closed: after cancellation the goroutine parks on
	// the send and is abandoned (the process is exiting), and it never
	// touches the line channel, so the writer teardown below stays safe.
	frames := make(chan frameResult)
	go readFrames(in, s.frameLimit, frames)

	serveDone := make(chan error, 1)
	go func() { serveDone <- s.decodeLoop(ctx, frames, lines, &work) }()
	err := <-serveDone

	// Cancel first so pumps and in-flight handlers detach from the hub, then
	// wait for them: they may still be delivering their final lines, which
	// the writer drains before the channel close ends it.
	cancel()
	work.Wait()
	close(lines)
	if writeErr := <-writerDone; err == nil {
		err = writeErr
	}
	return err
}

// writeLines is the single ordered writer: it appends the LF terminator to
// every marshaled line and writes it whole, so lines never interleave. A
// write failure (the host closed stdout) is remembered while the writer keeps
// draining, so producers blocked on the channel always drain instead of
// deadlocking the teardown.
func writeLines(out io.Writer, lines <-chan []byte) error {
	var failure error
	for line := range lines {
		if failure != nil {
			continue
		}
		if _, err := out.Write(append(line, '\n')); err != nil {
			failure = err
		}
	}
	return failure
}

// readFrames reads bounded NDJSON lines from in and forwards each with its
// outcome. The framing rules mirror the adapter rpc codecs: a line is
// LF-terminated, carries no CR, is non-empty, valid UTF-8, and within the
// limit; an unterminated final line is a defect, while EOF at a line boundary
// is the clean host close.
func readFrames(in io.Reader, limit int, frames chan<- frameResult) {
	reader := bufio.NewReader(in)
	for {
		frame, err := readFrame(reader, limit)
		frames <- frameResult{frame: frame, err: err}
		if err != nil {
			return
		}
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
func (s *Server) decodeLoop(ctx context.Context, frames <-chan frameResult, lines chan<- []byte, work *sync.WaitGroup) error {
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
				s.serveEvents(ctx, request, lines, work)
			case opOpen:
				result, werr := s.dispatch(ctx, request)
				s.respond(lines, request, result, werr)
			default:
				work.Add(1)
				go func(request requestLine) {
					defer work.Done()
					s.serveRequest(ctx, request, lines)
				}(request)
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

// send marshals one output line onto the writer channel; a value that cannot
// marshal (near-unreachable: every line type is a closed struct over already
// validated JSON) is logged and skipped rather than killing the writer.
func (s *Server) send(lines chan<- []byte, value any) {
	line, err := json.Marshal(value)
	if err != nil {
		s.logger.Printf("servestdio: encode line: %v", err)
		return
	}
	lines <- line
}

func (s *Server) nextID(kind string) string {
	return fmt.Sprintf("oap-%s-%d", kind, s.nextIDValue.Add(1))
}

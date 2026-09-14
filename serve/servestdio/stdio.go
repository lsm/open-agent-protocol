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
	"unicode/utf8"

	"github.com/lsm/open-agent-protocol/serve"
)

// DefaultFrameLimit bounds one NDJSON line in both directions. It matches
// servehttp's request-body budget so both transports of the same daemon
// accept the same requests, with the adapters' rpc-codec discipline — a
// bounded line, refused rather than split — applied to the framing.
const DefaultFrameLimit = 16 << 20

// minFrameLimit is the smallest usable FrameLimit: every control line — an
// error response with no result — encodes well under it, so a correlated
// answer always exists even when a result has to be refused. New rejects
// smaller limits rather than accepting a configuration whose own refusals
// could not be delivered.
const minFrameLimit = 256

// defaultWriteQueue bounds the lines buffered for the writer goroutine;
// beyond it producers block, which is the backpressure onto the single
// consumer of stdout.
const defaultWriteQueue = 256

// Options tunes the frontend. The zero value is usable.
type Options struct {
	// FrameLimit bounds one line in bytes in both directions; zero means
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
	hub        *serve.Hub
	frameLimit int
	writeQueue int
	logger     *log.Logger
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
	logger := options.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Server{hub: hub, frameLimit: frameLimit, writeQueue: writeQueue, logger: logger}, nil
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

// Run serves requests from in until the host closes it (clean end), the
// context ends, or a line violates the framing (fail closed). Responses are
// written to out by exactly one writer goroutine, one JSON object per
// LF-terminated line; that writer drains every line already admitted before
// Run returns. The frontend is a codec and owns no session lifetime: the
// caller sweeps the hub after Run returns, on every exit path. Run itself
// never writes to stderr; it returns the failure for the caller to report.
func (s *Server) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	lines := make(chan []byte, s.writeQueue)
	stop := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() { writerDone <- writeLines(out, lines, stop) }()

	err := s.serveLoop(ctx, in, lines)

	close(stop)
	if writeErr := <-writerDone; err == nil {
		err = writeErr
	}
	return err
}

// frameResult is one line read from the host: frame carries the line without
// its terminator, err the condition that ended the read (io.EOF for the clean
// host close, a framing defect, or a read failure).
type frameResult struct {
	frame []byte
	err   error
}

// readFrames reads bounded NDJSON lines from in and forwards each with its
// outcome. The frames channel is never closed: a reader abandoned by a
// context end parks on its send and is reclaimed by process exit, the same
// discipline as the writer's channel.
func readFrames(in io.Reader, limit int, frames chan<- frameResult) {
	reader := bufio.NewReader(in)
	for {
		frame, err := readFrame(reader, limit)
		if err != nil {
			frames <- frameResult{frame: frame, err: err}
			return
		}
		frames <- frameResult{frame: frame}
	}
}

// serveLoop reads request frames and serves each in order until a clean end
// or a framing defect. Reading runs on its own goroutine so a context end is
// observed while waiting for the next line, not only between lines — an
// embedding host that cancels without also closing stdin still gets Run
// back, and the pre-check keeps a frame that raced the cancellation from
// being dispatched under an already-dead context. A framing defect —
// anything readFrame or decodeRequest refuses — fails the frontend closed as
// *MalformedLineError; op-level refusals are responses the host can correct,
// and the loop reads on past them.
func (s *Server) serveLoop(ctx context.Context, in io.Reader, lines chan<- []byte) error {
	frames := make(chan frameResult)
	go readFrames(in, s.frameLimit, frames)
	number := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
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
			s.serveRequest(ctx, request, lines)
		}
	}
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

// readFrame reads one NDJSON line from the reader, without its terminator.
// The framing rules mirror the adapter rpc codecs: a line is LF-terminated,
// carries no CR, is non-empty, valid UTF-8, and within the limit; an
// unterminated final line is a defect, while EOF at a line boundary is the
// clean host close.
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
	// More() is not an end-of-input check — it also reports false for a
	// stray closing token, which would let frames like {...}} through — so
	// the only complete frame is one whose next decode reaches end of input.
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
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

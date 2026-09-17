// Package serveendpoint serves one adapter as an OAP *endpoint*: raw OAP
// envelopes, one per line, over a pipe pair. It is the shape a harness
// implementing OAP natively takes, and the counterpart of the binding in
// drafts/endpoint-stdio.md.
//
// It is deliberately not serve/servestdio. That frontend exposes a hub — an
// adapter dimension, twelve ops, cursor replay, and several subscriptions
// multiplexed over one pipe, each line wrapping an envelope inside a
// transport object. An endpoint is one agent loop: it carries the envelopes
// themselves, correlates with the `id` and `in_reply_to` the protocol already
// defines, and streams its session's events with no subscribe request,
// because it has exactly one consumer and that consumer is already attached.
package serveendpoint

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
)

// DefaultFrameLimit bounds one line. A frame over the bound is a framing
// defect rather than an oversized payload the host could shrink and resend,
// so the endpoint reports it and stops instead of truncating.
const DefaultFrameLimit = 1 << 20

// DefaultWriteStall bounds how long a line waits for a writer that is making
// no progress at all. It is deliberately far longer than any pause a reading
// host would cause, because the queue only fills when nothing is being
// consumed; it exists so a host that has gone away ends the process instead
// of parking it forever.
const DefaultWriteStall = 2 * time.Minute

// writeQueue bounds how far the endpoint runs ahead of a host that is reading
// slowly. Past it, producers wait — which is backpressure rather than a
// fault, and is cancellable.
const writeQueue = 64

// ErrFrameTooLarge reports a host line over the frame limit. It ends Run, so
// the process exits non-zero and the host learns its framing is at fault.
var ErrFrameTooLarge = errors.New("serveendpoint: line exceeds the frame limit")

// ErrMalformedLine reports a line that is not one JSON OAP envelope.
var ErrMalformedLine = errors.New("serveendpoint: line is not a JSON envelope")

// ErrShutdownStalled reports that the endpoint could not deliver what it had
// admitted before its teardown window expired, so the exit is not the clean
// one stdin EOF otherwise promises.
var ErrShutdownStalled = errors.New("serveendpoint: shutdown outlived its bounded window; the stalled run stream was abandoned")

// Options configures the endpoint. Adapter names the single adapter this
// endpoint is; there is no adapter dimension on the wire.
type Options struct {
	Adapter    string
	FrameLimit int
	// Shutdown bounds the teardown's wait for run pumps. Zero takes
	// serve.DefaultShutdownTimeout.
	Shutdown time.Duration
	// WriteStall bounds how long a line may wait for a writer that is making
	// no progress. Zero takes DefaultWriteStall.
	WriteStall time.Duration
	Logger     *log.Logger
}

// Server is one endpoint over one adapter.
type Server struct {
	hub        *serve.Hub
	adapter    string
	frameLimit int
	shutdown   time.Duration
	writeStall time.Duration
	logger     *log.Logger

	ids atomic.Uint64

	// participant is the control identity protocol.initialize.request
	// declared, read by every session this endpoint opens. Without it an
	// endpoint would open sessions under a name the host never named, and
	// every gate it raised would be addressed to a participant the trace
	// cannot see declared — which is what unknown_participant reports.
	participantMu sync.Mutex
	participant   protocol.ParticipantID

	// lines is the only path to stdout. Producers hand a framed line over
	// and one goroutine owns the write, so a peer that has stopped reading
	// parks that goroutine alone instead of whoever happened to be holding a
	// lock. Every earlier deadlock here — a teardown flush, a request
	// handler, a second pump — was the same mutex held across a write to a
	// pipe nobody was draining.
	lines chan []byte

	pumps sync.WaitGroup
}

// New returns an endpoint serving one registered adapter.
func New(hub *serve.Hub, options Options) (*Server, error) {
	if hub == nil {
		return nil, errors.New("serveendpoint: hub is required")
	}
	if options.Adapter == "" {
		return nil, errors.New("serveendpoint: an adapter name is required")
	}
	if _, found := hub.Registry().Lookup(options.Adapter); !found {
		return nil, fmt.Errorf("serveendpoint: no adapter %q", options.Adapter)
	}
	limit := options.FrameLimit
	if limit <= 0 {
		limit = DefaultFrameLimit
	}
	logger := options.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	shutdown := options.Shutdown
	if shutdown <= 0 {
		shutdown = serve.DefaultShutdownTimeout
	}
	stall := options.WriteStall
	if stall <= 0 {
		stall = DefaultWriteStall
	}
	return &Server{hub: hub, adapter: options.Adapter, frameLimit: limit, shutdown: shutdown, writeStall: stall, logger: logger}, nil
}

// Run reads request envelopes from in and writes response and event envelopes
// to out until in reaches EOF or ctx ends.
//
// EOF is the session's close: the endpoint stops reading, waits for the run
// pumps it started so their events reach the host, flushes, and returns nil so
// the process exits zero. A malformed or oversized line is the host's framing
// defect and is returned, so the process exits non-zero — the exit code is
// part of the binding, and a host piping an endpoint must be able to tell a
// clean end from a framing fault without parsing stderr.
func (s *Server) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	if in == nil || out == nil {
		return errors.New("serveendpoint: both stdin and stdout are required")
	}
	s.lines = make(chan []byte, writeQueue)
	writerDone := make(chan error, 1)
	go func() { writerDone <- s.runWriter(out) }()

	streams, stopStreams := context.WithCancel(ctx)
	defer stopStreams()

	// Reading happens on its own goroutine so the loop can select on the
	// context. An endpoint spends almost all of its life parked waiting for
	// the next request, and a loop that only checked the context between
	// lines would ignore SIGINT and SIGTERM for exactly that whole time —
	// which is every idle moment. The caller installs a signal context and
	// thereby suppresses the default termination, so a loop that cannot see
	// the cancellation leaves a supervisor no option but SIGKILL, skipping
	// the session sweep and orphaning whatever children an adapter holds.
	frames := make(chan readResult, 1)
	go s.readFrames(bufio.NewReaderSize(in, 64*1024), frames)

	var runErr error
reading:
	for {
		select {
		case <-ctx.Done():
			break reading
		case result, open := <-frames:
			if !open {
				break reading
			}
			if result.err != nil {
				if !errors.Is(result.err, io.EOF) {
					runErr = result.err
				}
				break reading
			}
			if strings.TrimSpace(string(result.frame)) == "" {
				continue
			}
			if err := s.handle(ctx, streams, result.frame); err != nil {
				runErr = err
				break reading
			}
		}
	}

	if s.settle(stopStreams, runErr != nil) {
		if runErr == nil {
			runErr = ErrShutdownStalled
		}
		// The abandoned pump may still hold a line it never handed over, so
		// the channel is left open rather than closed under it, and the
		// writer is abandoned with it. Nothing more can reach a pipe that is
		// not draining; the non-zero return is the report.
		return runErr
	}
	// Every producer is finished, so closing the channel is safe and is what
	// tells the writer to flush and stop. The wait is bounded for the same
	// reason the pump wait is: the last flush goes to the same pipe.
	close(s.lines)
	var flushErr error
	select {
	case flushErr = <-writerDone:
	case <-time.After(s.shutdown):
		flushErr = ErrShutdownStalled
	}
	if runErr != nil {
		return runErr
	}
	return flushErr
}

// runWriter owns stdout. It keeps draining after a write fails so a producer
// can never block on a channel nobody reads — the failure is reported once,
// and the lines that follow are discarded rather than deadlocking the loop
// that produced them.
func (s *Server) runWriter(out io.Writer) error {
	writer := bufio.NewWriter(out)
	var failure error
	for data := range s.lines {
		if failure != nil {
			continue
		}
		if _, err := writer.Write(data); err != nil {
			failure = err
			continue
		}
		if err := writer.Flush(); err != nil {
			failure = err
		}
	}
	if failure != nil {
		return failure
	}
	return writer.Flush()
}

// send hands one framed line to the writer, giving up when the context ends
// or when the queue has stayed full for longer than a host could plausibly
// still be reading.
//
// Both bounds are needed and neither covers the other. The context carries
// the operator's signal. The stall bound carries the case no signal reaches:
// a host that closed stdin and stopped reading its stdout. Handlers run
// inline in the read loop, so once the queue fills behind a parked writer the
// loop cannot reach the frame carrying stdin EOF either — end of input is
// unobservable precisely when it matters, because the reader is parked
// handing over the frame before it. Bounding the handoff is what turns that
// into the exit the binding promises rather than a wait for SIGKILL.
//
// The bound is generous because a slow host is not a gone one: it governs a
// queue that has not moved at all, not the pace of a host keeping up.
func (s *Server) send(ctx context.Context, data []byte) error {
	stall := time.NewTimer(s.writeStall)
	defer stall.Stop()
	select {
	case s.lines <- data:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-stall.C:
		return ErrShutdownStalled
	}
}

// readResult is one frame or the error that ended the input.
type readResult struct {
	frame []byte
	err   error
}

// readFrames feeds the loop. It may outlive Run, parked in a read on an input
// that never closes; the process exit collects it, and it holds nothing the
// teardown needs.
func (s *Server) readFrames(reader *bufio.Reader, frames chan<- readResult) {
	defer close(frames)
	for {
		frame, err := s.readLine(reader)
		frames <- readResult{frame: frame, err: err}
		if err != nil {
			return
		}
	}
}

// settle waits for the run pumps and reports whether it gave up on them.
//
// The wait is bounded rather than open-ended. A pump writes synchronously to
// stdout, so a host that closed its stdin but stopped reading its stdout —
// the hung-up host this binding describes — fills the pipe and parks the pump
// inside the write. Waiting on that forever would turn the promised exit into
// a hang, which is worse than either outcome the exit code is supposed to
// distinguish. Cancelling first would be worse still in the ordinary case: it
// would drop events the host was already acknowledged for, so cancellation is
// what remains after the window rather than what starts the teardown.
func (s *Server) settle(stopStreams context.CancelFunc, alreadyFailed bool) bool {
	if alreadyFailed {
		stopStreams()
	}
	done := make(chan struct{})
	go func() {
		s.pumps.Wait()
		close(done)
	}()
	window := time.NewTimer(s.shutdown)
	defer window.Stop()
	select {
	case <-done:
		return false
	case <-window.C:
	}
	stopStreams()
	cancelled := time.NewTimer(s.shutdown)
	defer cancelled.Stop()
	select {
	case <-done:
		return false
	case <-cancelled.C:
		// The pumps outlived both windows, which a cancelled context cannot
		// fix: a write parked on a full pipe is not waiting on the context.
		// They are abandoned so the process can exit, and the non-zero exit
		// says the endpoint could not deliver what it had admitted.
		s.logger.Printf("serveendpoint: shutdown outlived its window; the stalled run stream was abandoned")
		return true
	}
}

// readLine reads one newline-terminated frame, failing closed over the limit
// rather than delivering a truncated envelope.
func (s *Server) readLine(reader *bufio.Reader) ([]byte, error) {
	var frame []byte
	for {
		chunk, more, err := reader.ReadLine()
		if err != nil {
			return nil, err
		}
		frame = append(frame, chunk...)
		if len(frame) > s.frameLimit {
			return nil, fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, len(frame))
		}
		if !more {
			return frame, nil
		}
	}
}

// write queues one envelope for stdout. A line reaches the writer whole, so
// it stays atomic even while a pump and a request handler both have something
// to say.
func (s *Server) write(ctx context.Context, envelope protocol.Envelope) error {
	data, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if len(data)+1 > s.frameLimit {
		return fmt.Errorf("%w: response is %d bytes", ErrFrameTooLarge, len(data))
	}
	return s.send(ctx, append(data, '\n'))
}

func (s *Server) nextID(kind string) protocol.EnvelopeID {
	return protocol.EnvelopeID(fmt.Sprintf("oap-%s-%d", kind, s.ids.Add(1)))
}

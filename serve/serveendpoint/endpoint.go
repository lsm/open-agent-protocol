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

const DefaultFrameLimit = 1 << 20

const DefaultWriteStall = 2 * time.Minute

const writeQueue = 64

var ErrFrameTooLarge = errors.New("serveendpoint: line exceeds the frame limit")

var ErrMalformedLine = errors.New("serveendpoint: line is not a JSON envelope")

var ErrShutdownStalled = errors.New("serveendpoint: shutdown outlived its bounded window; the stalled run stream was abandoned")

type Options struct {
	Adapter    string
	FrameLimit int

	Shutdown time.Duration

	WriteStall time.Duration
	Logger     *log.Logger
}

type Server struct {
	hub        *serve.Hub
	adapter    string
	frameLimit int
	shutdown   time.Duration
	writeStall time.Duration
	logger     *log.Logger

	ids atomic.Uint64

	participantMu sync.Mutex
	participant   protocol.ParticipantID

	lines chan []byte

	pumps sync.WaitGroup
}

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

func (s *Server) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	if in == nil || out == nil {
		return errors.New("serveendpoint: both stdin and stdout are required")
	}
	s.lines = make(chan []byte, writeQueue)
	writerDone := make(chan error, 1)
	go func() { writerDone <- s.runWriter(out) }()

	streams, stopStreams := context.WithCancel(ctx)
	defer stopStreams()

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

		return runErr
	}

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

type readResult struct {
	frame []byte
	err   error
}

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

		s.logger.Printf("serveendpoint: shutdown outlived its window; the stalled run stream was abandoned")
		return true
	}
}

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

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

	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
)

// DefaultFrameLimit bounds one line. A frame over the bound is a framing
// defect rather than an oversized payload the host could shrink and resend,
// so the endpoint reports it and stops instead of truncating.
const DefaultFrameLimit = 1 << 20

// ErrFrameTooLarge reports a host line over the frame limit. It ends Run, so
// the process exits non-zero and the host learns its framing is at fault.
var ErrFrameTooLarge = errors.New("serveendpoint: line exceeds the frame limit")

// ErrMalformedLine reports a line that is not one JSON OAP envelope.
var ErrMalformedLine = errors.New("serveendpoint: line is not a JSON envelope")

// Options configures the endpoint. Adapter names the single adapter this
// endpoint is; there is no adapter dimension on the wire.
type Options struct {
	Adapter    string
	FrameLimit int
	Logger     *log.Logger
}

// Server is one endpoint over one adapter.
type Server struct {
	hub        *serve.Hub
	adapter    string
	frameLimit int
	logger     *log.Logger

	ids atomic.Uint64

	mu  sync.Mutex
	out *bufio.Writer

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
	return &Server{hub: hub, adapter: options.Adapter, frameLimit: limit, logger: logger}, nil
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
	s.mu.Lock()
	s.out = bufio.NewWriter(out)
	s.mu.Unlock()

	streams, stopStreams := context.WithCancel(ctx)
	defer stopStreams()

	reader := bufio.NewReaderSize(in, 64*1024)
	var runErr error
	for {
		line, err := s.readLine(reader)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				runErr = err
			}
			break
		}
		if strings.TrimSpace(string(line)) == "" {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		if err := s.handle(ctx, streams, line); err != nil {
			runErr = err
			break
		}
	}

	// The pumps are waited for rather than cancelled: their events are the
	// answer to work the host was already acknowledged for, and a pump ends at
	// its run's terminal on its own. A host that hung up has stopped reading,
	// and the writer below will fail rather than block forever on a dead pipe.
	if runErr == nil {
		s.pumps.Wait()
	} else {
		stopStreams()
		s.pumps.Wait()
	}
	s.mu.Lock()
	flushErr := s.out.Flush()
	s.mu.Unlock()
	if runErr != nil {
		return runErr
	}
	return flushErr
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

// write serialises one envelope onto stdout. One writer holds the lock, so a
// line is atomic even while a pump and a request handler both have something
// to say.
func (s *Server) write(envelope protocol.Envelope) error {
	data, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if len(data)+1 > s.frameLimit {
		return fmt.Errorf("%w: response is %d bytes", ErrFrameTooLarge, len(data))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.out.Write(append(data, '\n')); err != nil {
		return err
	}
	return s.out.Flush()
}

func (s *Server) nextID(kind string) protocol.EnvelopeID {
	return protocol.EnvelopeID(fmt.Sprintf("oap-%s-%d", kind, s.ids.Add(1)))
}

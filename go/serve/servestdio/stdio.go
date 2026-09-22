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

	"github.com/lsm/open-agent-protocol/go/serve"
	"github.com/lsm/open-agent-protocol/go/validation"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const maxEnvelopeBytes = 16 << 20

const wrapperAllowance = 2 << 20

const DefaultFrameLimit = maxEnvelopeBytes + wrapperAllowance

const minFrameLimit = 256

const defaultWriteQueue = 256

const DefaultShutdownTimeout = 5 * time.Second

type Options struct {
	FrameLimit int

	WriteQueue int

	MaxConcurrentOps int

	MaxSubscriptions int

	ShutdownTimeout time.Duration

	Logger *log.Logger
}

type Server struct {
	hub         *serve.Hub
	schema      *jsonschema.Schema
	frameLimit  int
	writeQueue  int
	maxOps      int
	maxAttach   int
	shutdown    time.Duration
	logger      *log.Logger
	nextIDValue atomic.Uint64
}

const maxConcurrentOps = 16

const maxSubscriptions = 64

const admissionBytes = maxEnvelopeBytes

type outLine struct {
	data    []byte
	refusal bool
}

type refusalTally struct {
	owed      atomic.Int64
	delivered atomic.Int64
}

func (t *refusalTally) undelivered() int64 { return t.owed.Load() - t.delivered.Load() }

type runState struct {
	maxOps    int
	maxBytes  int
	maxAttach int

	mu           sync.Mutex
	work         sync.WaitGroup
	shuttingDown bool
	ops          int
	bytes        int
	attached     int

	pumps     context.Context
	stopPumps context.CancelFunc
}

func newRunState(ctx context.Context, maxOps, maxBytes, maxAttach int) *runState {
	if maxOps <= 0 {
		maxOps = maxConcurrentOps
	}
	if maxBytes <= 0 {
		maxBytes = admissionBytes
	}
	if maxAttach <= 0 {
		maxAttach = maxSubscriptions
	}
	pumps, stopPumps := context.WithCancel(ctx)
	return &runState{maxOps: maxOps, maxBytes: maxBytes, maxAttach: maxAttach, pumps: pumps, stopPumps: stopPumps}
}

type admission int

const (
	admitted admission = iota

	refused

	closedToWork
)

func (r *runState) offer(size int) (admission, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.shuttingDown {
		return closedToWork, ""
	}
	if r.ops == 0 || (r.ops < r.maxOps && r.bytes+size <= r.maxBytes) {
		r.ops++
		r.bytes += size
		r.work.Add(1)
		return admitted, ""
	}
	if r.ops >= r.maxOps {
		return refused, fmt.Sprintf("the frontend is already running %d operations", r.maxOps)
	}
	return refused, fmt.Sprintf("the requests already in flight fill the frontend's %d-byte budget", r.maxBytes)
}

func (r *runState) attach() (context.Context, admission, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.shuttingDown {
		return nil, closedToWork, ""
	}
	if r.attached >= r.maxAttach {
		return nil, refused, fmt.Sprintf("the frontend is already serving %d subscriptions", r.maxAttach)
	}
	r.attached++
	r.work.Add(1)
	return r.pumps, admitted, ""
}

func (r *runState) detach() {
	r.mu.Lock()
	r.attached--
	r.mu.Unlock()
	r.work.Done()
}

func (r *runState) release(size int) {
	r.mu.Lock()
	r.ops--
	r.bytes -= size
	r.mu.Unlock()
	r.work.Done()
}

func (r *runState) closeAdmission() {
	r.mu.Lock()
	r.shuttingDown = true
	r.stopPumps()
	r.mu.Unlock()
}

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
	return &Server{hub: hub, schema: schema, frameLimit: frameLimit, writeQueue: writeQueue, maxOps: maxOps, maxAttach: options.MaxSubscriptions, shutdown: shutdown, logger: logger}, nil
}

func (s *Server) Hub() *serve.Hub { return s.hub }

type MalformedLineError struct {
	Line   int
	Detail string
}

func (e *MalformedLineError) Error() string {
	return fmt.Sprintf("line %d is not a valid request: %s", e.Line, e.Detail)
}

var ErrLineTooLarge = errors.New("servestdio: encoded line exceeds the frame limit")

var ErrRequestsDropped = errors.New("servestdio: requests read before the host's end were dropped unserved")

var ErrShutdownStalled = errors.New("servestdio: shutdown outlived its bounded window; the stalled stage was abandoned")

func (s *Server) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	lines := make(chan outLine, s.writeQueue)
	stop := make(chan struct{})
	abandon := make(chan struct{})
	writerFailed := make(chan struct{}, 1)
	outputFailed := &outputFailure{}
	refusals := &refusalTally{}
	writerDone := make(chan error, 1)
	go func() { writerDone <- writeLines(out, lines, stop, abandon, writerFailed, outputFailed, refusals) }()

	readerDone := make(chan readEnd, 1)

	frames := make(chan frameResult, 1)
	go readFrames(in, s.frameLimit, frames, readerDone)

	run := newRunState(ctx, s.maxOps, 0, s.maxAttach)
	serveDone := make(chan error, 1)
	go func() { serveDone <- s.serveLoop(ctx, run, frames, lines, writerFailed, refusals) }()

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

	run.closeAdmission()
	workDone := make(chan struct{})
	go func() { run.work.Wait(); close(workDone) }()
	workWindow := time.NewTimer(s.shutdown)
	defer workWindow.Stop()
	drained := false
	select {
	case <-workDone:
		close(stop)

		drainWindow := time.NewTimer(s.shutdown)
		defer drainWindow.Stop()
		select {
		case writeErr := <-writerDone:
			drained = true

			err = note(err, writeErr)
		case <-drainWindow.C:

			close(abandon)

			err = note(err, outputFailed.get())
		}
	case <-workWindow.C:

		cancel()
		close(abandon)

		err = note(err, outputFailed.get())
	}

	if stuck {

		select {
		case ended = <-readerDone:
		default:
		}
		err = note(err, ended.terminal())
	}

	var wait <-chan time.Time
	if stuck {
		loopWindow := time.NewTimer(s.shutdown)
		defer loopWindow.Stop()
		wait = loopWindow.C
	}

	loopErr, released := collectLoop(serveDone, wait)
	err = note(err, loopErr)

	if (stuck && !released) || !drained {
		err = note(err, ErrShutdownStalled)
	}

	if refusals.undelivered() > 0 {
		err = note(err, ErrRequestsDropped)
	}
	return err
}

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

type readEnd struct {
	line int
	err  error
}

func (e readEnd) terminal() error {
	switch {
	case e.err == nil, errors.Is(e.err, io.EOF):
		return nil
	case errors.As(e.err, new(*frameDefect)):
		return &MalformedLineError{Line: e.line, Detail: e.err.Error()}
	default:

		return e.err
	}
}

type frameResult struct {
	frame []byte
	err   error
}

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

func (s *Server) serveLoop(ctx context.Context, run *runState, frames <-chan frameResult, lines chan<- outLine, writerFailed <-chan struct{}, refusals *refusalTally) error {
	number := 0

	var pending []outLine
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-writerFailed:
			return nil
		default:
		}

		var out chan<- outLine
		var head outLine
		if len(pending) > 0 {
			out, head = lines, pending[0]
		}
		select {
		case <-ctx.Done():
			return nil
		case <-writerFailed:
			return nil
		case out <- head:
			pending = pending[1:]
		case result := <-frames:
			number++
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {

					return flushPending(ctx, lines, pending, writerFailed)
				}
				var defect *frameDefect
				if errors.As(result.err, &defect) {
					return &MalformedLineError{Line: number, Detail: defect.Error()}
				}

				return result.err
			}
			request, err := decodeRequest(result.frame)
			if err != nil {
				return &MalformedLineError{Line: number, Detail: err.Error()}
			}
			size := len(result.frame)
			outcome, refusal := run.offer(size)
			switch outcome {
			case closedToWork:

				return ErrRequestsDropped
			case refused:

				line, ok := refusalLine(request, refusal, s.frameLimit)
				if !ok {

					refusals.owed.Add(1)
					continue
				}

				refusals.owed.Add(1)
				select {
				case lines <- line:
				default:
					pending = append(pending, line)
					if len(pending) > s.writeQueue {

						return ErrRequestsDropped
					}
				}
				continue
			}
			go func(request requestLine, size int) {
				defer run.release(size)
				s.serveRequest(ctx, run, request, lines)
			}(request, size)
		}
	}
}

func writeLines(out io.Writer, lines <-chan outLine, stop, abandon <-chan struct{}, failed chan<- struct{}, record *outputFailure, refusals *refusalTally) error {
	var failure error
	write := func(line outLine) {
		if failure != nil {
			return
		}

		select {
		case <-abandon:
			return
		default:
		}
		if _, err := out.Write(append(line.data, '\n')); err != nil {
			failure = err
			record.set(err)
			failed <- struct{}{}
			return
		}
		if line.refusal {

			refusals.delivered.Add(1)
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

type frameDefect struct {
	detail string
}

func (e *frameDefect) Error() string { return e.detail }

func readFrame(reader *bufio.Reader, limit int) ([]byte, error) {
	frame := make([]byte, 0, min(limit, 4096))
	for {
		fragment, err := reader.ReadSlice('\n')

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

type requestLine struct {
	ID        *int64          `json:"id"`
	Op        string          `json:"op"`
	Adapter   string          `json:"adapter,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	RunID     string          `json:"run_id,omitempty"`
	After     json.RawMessage `json:"after,omitempty"`
	Request   json.RawMessage `json:"request,omitempty"`

	AllowDegradedFeatures []string `json:"allow_degraded_features,omitempty"`

	present map[string]bool
}

func decodeRequest(frame []byte) (requestLine, error) {
	var request requestLine
	decoder := json.NewDecoder(bytes.NewReader(frame))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return requestLine{}, fmt.Errorf("invalid JSON request: %v", trimMessage(err.Error()))
	}

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

var canonicalKeys = map[string]bool{
	"id": true, "op": true, "adapter": true, "session_id": true, "run_id": true, "after": true, "request": true,
	"allow_degraded_features": true,
}

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

func flushPending(ctx context.Context, lines chan<- outLine, pending []outLine, writerFailed <-chan struct{}) error {
	for _, line := range pending {
		select {
		case lines <- line:
		case <-ctx.Done():
			return nil
		case <-writerFailed:
			return nil
		}
	}
	return nil
}

func refusalLine(request requestLine, why string, limit int) (outLine, bool) {
	line, err := json.Marshal(responseLine{ID: *request.ID, OK: false, Result: json.RawMessage("null"), Error: &wireError{
		Code:    "busy",
		Message: why + "; send this request again",
	}})
	if err != nil || len(line) > limit {
		return outLine{}, false
	}
	return outLine{data: line, refusal: true}, true
}

func (s *Server) send(ctx context.Context, lines chan<- outLine, value any) error {
	line, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode line: %w", err)
	}
	if len(line) > s.frameLimit {
		return ErrLineTooLarge
	}
	select {
	case lines <- outLine{data: line}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

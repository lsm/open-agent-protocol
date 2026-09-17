// Package conformance drives an OAP endpoint over the stdio binding in
// drafts/endpoint-stdio.md, assembles the exchange into a trace, and hands
// that trace to the real validator.
//
// The verdict deliberately does not come from this package's own opinion.
// This package drives; validation judges; they are different code, and the
// validator is the one every adapter in this repository is already held to.
// A runner that both drove and judged would be the fault the corpus review
// criticised — authored artefacts agreeing with each other.
package conformance

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/lsm/open-agent-protocol/protocol"
)

// DefaultLineDeadline bounds how long the runner waits for the endpoint's
// next line.
//
// It is generous on purpose. The binding places no bound on inter-line
// latency, and a real endpoint pauses for as long as its model or its tools
// take — a gap of minutes between run events is ordinary, not a fault. A
// tight deadline would report non-conformance for a conformant binary, which
// is the runner's primary use failing in the worst direction. It is still
// bounded, because a deadlocked endpoint has to end the run somehow.
const DefaultLineDeadline = 5 * time.Minute

// maxExitGrace caps the wait for a process to leave after its stdout has
// closed. A conformant endpoint has already settled whatever it admitted by
// then, so this is generous rather than tight: it exists so a binary that
// closes its output and stays alive ends this run instead of owning it.
const maxExitGrace = 30 * time.Second

// exitGrace is that cap, or the caller's own line deadline when it is
// shorter. A caller who asked for a snappy runner gets one here too, and a
// caller who asked for patience does not get to wait longer for an exit than
// for a line.
func (c *Client) exitGrace() time.Duration {
	if c.deadline > 0 && c.deadline < maxExitGrace {
		return c.deadline
	}
	return maxExitGrace
}

// ErrEndpointGone reports that the endpoint's stdout ended before the line
// this client was waiting for.
var ErrEndpointGone = errors.New("conformance: the endpoint produced no more lines")

// Client speaks the endpoint binding to a spawned process.
//
// Responses and events share one pipe and the binding promises no ordering
// between them, so every read takes the kind it wants and holds the other.
// A reader that discarded the kind it was not waiting for would drop a gate
// event that happened to precede an acknowledgement, and would do it
// intermittently — which is the failure this shape exists to prevent.
type Client struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	stdin  io.WriteCloser
	lines  chan line
	closed bool

	reap    sync.Once
	waitErr error

	mu        sync.Mutex
	responses []protocol.Envelope
	events    []protocol.Envelope
	controls  []ControlFrame

	transcript []protocol.Envelope
	// probes are the ids of deliberately-wrong requests. Neither they nor
	// their answers reach the trace: see Probe.
	probes   map[protocol.EnvelopeID]bool
	deadline time.Duration
	// dead is sticky. Once the endpoint has stopped producing lines, every
	// later wait would pay the full deadline again for the same answer.
	dead error
}

type line struct {
	envelope protocol.Envelope
	control  *ControlFrame
	raw      string
	err      error
}

// ControlFrame is one binding control line. Control frames are this
// transport's own business — cursor replay is the only one the binding
// defines — so they never enter the trace the validator judges.
type ControlFrame struct {
	Control   string             `json:"control"`
	ID        string             `json:"id,omitempty"`
	SessionID protocol.SessionID `json:"session_id,omitempty"`
	RunID     protocol.RunID     `json:"run_id,omitempty"`
	After     *uint64            `json:"after,omitempty"`

	RequestedAfter  uint64 `json:"requested_after,omitempty"`
	OldestAvailable uint64 `json:"oldest_available,omitempty"`
	LatestAvailable uint64 `json:"latest_available,omitempty"`

	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// Spawn starts the endpoint command and begins reading its stdout, waiting
// DefaultLineDeadline for each line. SpawnWithDeadline takes another bound.
func Spawn(ctx context.Context, name string, args []string, stderr io.Writer) (*Client, error) {
	return SpawnWithDeadline(ctx, name, args, stderr, DefaultLineDeadline)
}

// SpawnWithDeadline is Spawn with an explicit per-line bound.
func SpawnWithDeadline(ctx context.Context, name string, args []string, stderr io.Writer, deadline time.Duration) (*Client, error) {
	// The command gets its own cancellable context rather than the caller's.
	// The caller's is typically never cancelled, which would leave a spawned
	// binary running after the runner gave up on it — and judging arbitrary
	// third-party binaries is what this tool is for, so one that starts and
	// then neither speaks nor exits is a primary input, not an edge case.
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	if deadline <= 0 {
		deadline = DefaultLineDeadline
	}
	c := &Client{cmd: cmd, cancel: cancel, stdin: stdin, lines: make(chan line, 64), deadline: deadline}
	go c.read(stdout)
	return c, nil
}

// read turns the endpoint's stdout into decoded lines. A line that is not one
// JSON envelope is reported rather than skipped: on this binding that is a
// conformance failure, not noise to tolerate.
func (c *Client) read(stdout io.Reader) {
	defer close(c.lines)
	reader := bufio.NewReaderSize(stdout, 1<<20)
	for {
		text, err := reader.ReadString('\n')
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			var shape struct {
				Protocol string `json:"protocol"`
				Control  string `json:"control"`
			}
			if decodeErr := json.Unmarshal([]byte(trimmed), &shape); decodeErr != nil {
				c.lines <- line{raw: trimmed, err: fmt.Errorf("stdout carried a line that is not JSON: %v", decodeErr)}
			} else if shape.Protocol == "" && shape.Control != "" {
				var frame ControlFrame
				if decodeErr := json.Unmarshal([]byte(trimmed), &frame); decodeErr != nil {
					c.lines <- line{raw: trimmed, err: fmt.Errorf("stdout carried a malformed control frame: %v", decodeErr)}
				} else {
					c.lines <- line{control: &frame, raw: trimmed}
				}
			} else {
				var envelope protocol.Envelope
				if decodeErr := json.Unmarshal([]byte(trimmed), &envelope); decodeErr != nil {
					c.lines <- line{raw: trimmed, err: fmt.Errorf("stdout carried a line that is not an OAP envelope: %v", decodeErr)}
				} else {
					c.lines <- line{envelope: envelope, raw: trimmed}
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// Send writes one request envelope and records it in the transcript.
func (c *Client) Send(envelope protocol.Envelope) error {
	return c.send(envelope, true)
}

// Probe writes one request that is deliberately wrong and keeps it, and its
// answer, out of the trace.
//
// Some obligations can only be checked by sending something no conformant host
// would send — a stale capability revision, a cancel for a settled run. The
// endpoint's answer is the thing under test, and it is an answer a conformant
// endpoint must give. But the exchange itself is not conformant traffic, and
// folding it into the assembled trace would have the validator convict the
// endpoint of the fault the runner committed on purpose.
func (c *Client) Probe(envelope protocol.Envelope) error {
	c.mu.Lock()
	if c.probes == nil {
		c.probes = map[protocol.EnvelopeID]bool{}
	}
	c.probes[envelope.ID] = true
	c.mu.Unlock()
	return c.send(envelope, false)
}

func (c *Client) send(envelope protocol.Envelope, record bool) error {
	data, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if record {
		c.mu.Lock()
		c.transcript = append(c.transcript, envelope)
		c.mu.Unlock()
	}
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

// pull reads one more line from the endpoint, records it, and files it under
// its kind. A response is any envelope correlated to a request; everything
// else is an event.
func (c *Client) pull() error { return c.pullWithin(c.deadline, true) }

// ErrControlUnanswered is a control frame the endpoint never answered. It is
// distinct from a dead endpoint because it is not one: a control is not an
// envelope, the binding lets an endpoint implement none of them, and the
// endpoint that answers nothing is still there and still owes answers to
// everything else. Waiting for it must therefore not be fatal.
var ErrControlUnanswered = errors.New("conformance: the endpoint answered no control frame")

// pullWithin reads one more line, waiting at most budget. A fatal wait that
// expires kills the endpoint; a non-fatal one leaves it alone.
//
// The distinction matters more than it looks. An endpoint that owes a
// correlated response and sends nothing is neither talking nor leaving, and
// killing it there is right. An endpoint that ignores a control frame has
// done something the binding forbids but is otherwise alive and answering, and
// killing it turns one defect into a failure for every later check — which
// then reports the closed pipe rather than anything about the endpoint, and
// reads as though the endpoint died. That cost a maintainer a wrong diagnosis:
// six derived failures hid which one was real.
func (c *Client) pullWithin(budget time.Duration, fatal bool) error {
	c.mu.Lock()
	dead := c.dead
	c.mu.Unlock()
	if dead != nil {
		return dead
	}
	select {
	case l, ok := <-c.lines:
		if !ok {
			return ErrEndpointGone
		}
		if l.err != nil {
			return l.err
		}
		c.mu.Lock()
		if l.control != nil {
			c.controls = append(c.controls, *l.control)
			c.mu.Unlock()
			return nil
		}
		if !c.probes[l.envelope.InReplyTo] {
			c.transcript = append(c.transcript, l.envelope)
		}
		if l.envelope.InReplyTo != "" {
			c.responses = append(c.responses, l.envelope)
		} else {
			c.events = append(c.events, l.envelope)
		}
		c.mu.Unlock()
		return nil
	case <-time.After(budget):
		if !fatal {
			return ErrControlUnanswered
		}
		err := fmt.Errorf("conformance: the endpoint produced no line within %s", budget)
		c.mu.Lock()
		c.dead = err
		c.mu.Unlock()
		// It is neither talking nor leaving, so it is killed here rather
		// than left for a later wait to discover at the cost of another
		// full deadline — and rather than left running at all.
		c.Close()
		return err
	}
}

// Response returns the correlated answer to one request, holding any events
// that arrive first.
func (c *Client) Response(inReplyTo protocol.EnvelopeID) (protocol.Envelope, error) {
	for {
		c.mu.Lock()
		for i, candidate := range c.responses {
			if candidate.InReplyTo == inReplyTo {
				c.responses = append(c.responses[:i], c.responses[i+1:]...)
				c.mu.Unlock()
				return candidate, nil
			}
		}
		c.mu.Unlock()
		if err := c.pull(); err != nil {
			return protocol.Envelope{}, fmt.Errorf("waiting for the answer to %s: %w", inReplyTo, err)
		}
	}
}

// Event returns the next run event, holding any responses that arrive first.
func (c *Client) Event() (protocol.Envelope, error) {
	for {
		c.mu.Lock()
		if len(c.events) > 0 {
			next := c.events[0]
			c.events = c.events[1:]
			c.mu.Unlock()
			return next, nil
		}
		c.mu.Unlock()
		if err := c.pull(); err != nil {
			return protocol.Envelope{}, err
		}
	}
}

// SendControl writes one binding control frame. It is not recorded in the
// transcript: a control frame is transport, not protocol, and a trace is
// protocol.
func (c *Client) SendControl(frame ControlFrame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

// Control returns the control frame answering one request, holding envelopes
// and responses that arrive first.
func (c *Client) Control(id string) (ControlFrame, error) {
	for {
		c.mu.Lock()
		for i, candidate := range c.controls {
			if candidate.ID == id {
				c.controls = append(c.controls[:i], c.controls[i+1:]...)
				c.mu.Unlock()
				return candidate, nil
			}
		}
		c.mu.Unlock()
		if err := c.pullWithin(c.controlBudget(), false); err != nil {
			if errors.Is(err, ErrControlUnanswered) {
				return ControlFrame{}, ErrControlUnanswered
			}
			return ControlFrame{}, fmt.Errorf("waiting for the control answer to %s: %w", id, err)
		}
	}
}

// ControlAnswerBudget bounds the wait for a control answer.
//
// It is an absolute cap rather than a fraction of the line deadline, because
// the two bound different things. The line deadline is generous — five minutes
// — because a response can be behind a model call, and an endpoint that is
// working is not a hung one. Answering a control is neither: an endpoint
// either implements the control or does not, and either answer is a local
// decision it can make immediately. A fraction of five minutes would make the
// runner sit for well over a minute to learn something no conformant endpoint
// needs a second to say.
const ControlAnswerBudget = 10 * time.Second

func (c *Client) controlBudget() time.Duration {
	if c.deadline > 0 && c.deadline < ControlAnswerBudget {
		return c.deadline
	}
	return ControlAnswerBudget
}

// ClosedByRunner reports whether this runner killed the endpoint after a wait
// expired, so a later failure can say who closed the pipe. "file already
// closed" on its own reads as the endpoint having died.
func (c *Client) ClosedByRunner() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dead
}

// CloseInput closes the endpoint's stdin, which on this binding is the
// session's close.
func (c *Client) CloseInput() error {
	if c.closed {
		return nil
	}
	c.closed = true
	return c.stdin.Close()
}

// Close kills the endpoint if it is still running and reaps it. It is
// idempotent and safe to defer alongside Wait.
func (c *Client) Close() {
	if c.cancel != nil {
		c.cancel()
	}
	_ = c.reapProcess()
}

// reapProcess waits for the child exactly once, since os/exec forbids a
// second Wait and both Close and Wait can reach it.
func (c *Client) reapProcess() error {
	c.reap.Do(func() { c.waitErr = c.cmd.Wait() })
	return c.waitErr
}

// Wait drains whatever the endpoint still had to say and returns its exit
// code. Draining first is required rather than tidy: the endpoint flushes its
// last lines before exiting, and reading them after Wait is the ordering the
// os/exec documentation calls incorrect.
//
// An endpoint that stops producing lines without exiting is killed rather
// than waited on: there is no exit code coming, and leaving it running is
// how a conformance run over several binaries accumulates orphans.
func (c *Client) Wait() (int, error) {
	for {
		if err := c.pull(); err != nil {
			if errors.Is(err, ErrEndpointGone) {
				break
			}
			c.Close()
			return -1, err
		}
	}
	// The reap is bounded too. Closing stdout is not leaving: an endpoint can
	// do the first and never the second, and waiting on it unbounded would
	// hand this run's lifetime to the binary it is judging — which is the one
	// thing a harness for arbitrary binaries must not do.
	reaped := make(chan error, 1)
	go func() { reaped <- c.reapProcess() }()
	var err error
	select {
	case err = <-reaped:
	case <-time.After(c.exitGrace()):
		// Killed directly rather than through Close, which funnels into the
		// same sync.Once the reap is already inside and would therefore wait
		// on the very cmd.Wait this is bounding.
		if c.cancel != nil {
			c.cancel()
		}
		select {
		case <-reaped:
		case <-time.After(c.exitGrace()):
		}
		return -1, fmt.Errorf("conformance: the endpoint closed its stdout but had still not exited %s later", c.exitGrace())
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// Transcript is every envelope sent and received, each id once, ordered so a
// run's events follow the response that admitted it.
//
// Two departures from pipe-arrival order, both required rather than tidy.
//
// Each envelope id is kept once, because a replay re-delivers envelopes the
// host already holds: the same envelope crossing the pipe twice is one event
// delivered twice, and a trace keeping both copies fails on duplicate ids for
// a reason that has nothing to do with the endpoint.
//
// And a run's events are held until the response admitting that run has been
// emitted. The binding promises no ordering between a response and an event —
// an endpoint may emit a run's first events while still inside its submit
// handling, and over HTTP the two arrive on genuinely separate connections —
// but a trace is a logical record, and admission precedes started in it. A
// host that recorded arrival order would flag a conformant endpoint with
// illegal_run_transition, intermittently, depending on which side won a race.
// Ordering here is what makes the wire free to be unordered.
//
// Events whose run is never admitted are emitted at the end rather than
// dropped: that is an endpoint defect, and the validator should be the one to
// say so.
func (c *Client) Transcript() []protocol.Envelope {
	c.mu.Lock()
	defer c.mu.Unlock()

	seen := make(map[protocol.EnvelopeID]bool, len(c.transcript))
	trace := make([]protocol.Envelope, 0, len(c.transcript))
	admitted := map[protocol.RunID]bool{}
	held := map[protocol.RunID][]protocol.Envelope{}
	var order []protocol.RunID

	emit := func(envelope protocol.Envelope) {
		trace = append(trace, envelope)
	}
	release := func(run protocol.RunID) {
		for _, envelope := range held[run] {
			emit(envelope)
		}
		delete(held, run)
	}

	for _, envelope := range c.transcript {
		if envelope.ID != "" && seen[envelope.ID] {
			continue
		}
		seen[envelope.ID] = true

		if run := envelope.RunID; run != "" && envelope.Sequence != nil && !admitted[run] {
			if _, holding := held[run]; !holding {
				order = append(order, run)
			}
			held[run] = append(held[run], envelope)
			continue
		}
		emit(envelope)
		if run := admittedRun(envelope); run != "" && !admitted[run] {
			admitted[run] = true
			release(run)
		}
	}
	for _, run := range order {
		release(run)
	}
	return trace
}

// admittedRun reports the run a response admits, which is what a run's events
// must follow. The run is read from the payload rather than the envelope
// label, because labelling the envelope is this repository's convention and
// not something the binding requires of anyone else.
func admittedRun(envelope protocol.Envelope) protocol.RunID {
	switch envelope.Type {
	case protocol.TypeSessionMessageSubmitResponse:
		var admission protocol.MessageSubmitResponse
		if envelope.DecodePayload(&admission) == nil {
			return admission.RunID
		}
	case protocol.TypeSessionOpenResponse:
		var state protocol.SessionState
		if envelope.DecodePayload(&state) == nil {
			return state.ActiveRunID
		}
	}
	return ""
}

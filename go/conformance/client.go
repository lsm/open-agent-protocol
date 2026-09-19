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

	"github.com/lsm/open-agent-protocol/go/protocol"
)

const DefaultLineDeadline = 5 * time.Minute

const maxExitGrace = 30 * time.Second

func (c *Client) exitGrace() time.Duration {
	if c.deadline > 0 && c.deadline < maxExitGrace {
		return c.deadline
	}
	return maxExitGrace
}

var ErrEndpointGone = errors.New("conformance: the endpoint produced no more lines")

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

	probes   map[protocol.EnvelopeID]bool
	deadline time.Duration

	dead error
}

type line struct {
	envelope protocol.Envelope
	control  *ControlFrame
	raw      string
	err      error
}

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

func Spawn(ctx context.Context, name string, args []string, stderr io.Writer) (*Client, error) {
	return SpawnWithDeadline(ctx, name, args, stderr, DefaultLineDeadline)
}

func SpawnWithDeadline(ctx context.Context, name string, args []string, stderr io.Writer, deadline time.Duration) (*Client, error) {

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

func (c *Client) Send(envelope protocol.Envelope) error {
	return c.send(envelope, true)
}

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

func (c *Client) pull() error { return c.pullWithin(c.deadline, true) }

var ErrControlUnanswered = errors.New("conformance: the endpoint answered no control frame")

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

		c.Close()
		return err
	}
}

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

func (c *Client) SendControl(frame ControlFrame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

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

const ControlAnswerBudget = 10 * time.Second

func (c *Client) controlBudget() time.Duration {
	if c.deadline > 0 && c.deadline < ControlAnswerBudget {
		return c.deadline
	}
	return ControlAnswerBudget
}

func (c *Client) ClosedByRunner() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dead
}

func (c *Client) CloseInput() error {
	if c.closed {
		return nil
	}
	c.closed = true
	return c.stdin.Close()
}

func (c *Client) Close() {
	if c.cancel != nil {
		c.cancel()
	}
	_ = c.reapProcess()
}

func (c *Client) reapProcess() error {
	c.reap.Do(func() { c.waitErr = c.cmd.Wait() })
	return c.waitErr
}

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

	reaped := make(chan error, 1)
	go func() { reaped <- c.reapProcess() }()
	var err error
	select {
	case err = <-reaped:
	case <-time.After(c.exitGrace()):

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

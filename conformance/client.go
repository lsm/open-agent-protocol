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
	stdin  io.WriteCloser
	lines  chan line
	closed bool

	mu        sync.Mutex
	responses []protocol.Envelope
	events    []protocol.Envelope

	transcript []protocol.Envelope
	deadline   time.Duration
}

type line struct {
	envelope protocol.Envelope
	raw      string
	err      error
}

// Spawn starts the endpoint command and begins reading its stdout.
func Spawn(ctx context.Context, name string, args []string, stderr io.Writer) (*Client, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &Client{cmd: cmd, stdin: stdin, lines: make(chan line, 64), deadline: 20 * time.Second}
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
			var envelope protocol.Envelope
			if decodeErr := json.Unmarshal([]byte(trimmed), &envelope); decodeErr != nil {
				c.lines <- line{raw: trimmed, err: fmt.Errorf("stdout carried a line that is not an OAP envelope: %v", decodeErr)}
			} else {
				c.lines <- line{envelope: envelope, raw: trimmed}
			}
		}
		if err != nil {
			return
		}
	}
}

// Send writes one request envelope and records it in the transcript.
func (c *Client) Send(envelope protocol.Envelope) error {
	data, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.transcript = append(c.transcript, envelope)
	c.mu.Unlock()
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

// pull reads one more line from the endpoint, records it, and files it under
// its kind. A response is any envelope correlated to a request; everything
// else is an event.
func (c *Client) pull() error {
	select {
	case l, ok := <-c.lines:
		if !ok {
			return ErrEndpointGone
		}
		if l.err != nil {
			return l.err
		}
		c.mu.Lock()
		c.transcript = append(c.transcript, l.envelope)
		if l.envelope.InReplyTo != "" {
			c.responses = append(c.responses, l.envelope)
		} else {
			c.events = append(c.events, l.envelope)
		}
		c.mu.Unlock()
		return nil
	case <-time.After(c.deadline):
		return fmt.Errorf("conformance: the endpoint produced no line within %s", c.deadline)
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

// CloseInput closes the endpoint's stdin, which on this binding is the
// session's close.
func (c *Client) CloseInput() error {
	if c.closed {
		return nil
	}
	c.closed = true
	return c.stdin.Close()
}

// Wait drains whatever the endpoint still had to say and returns its exit
// code. Draining first is required rather than tidy: the endpoint flushes its
// last lines before exiting, and reading them after Wait is the ordering the
// os/exec documentation calls incorrect.
func (c *Client) Wait() (int, error) {
	for {
		if err := c.pull(); err != nil {
			if errors.Is(err, ErrEndpointGone) {
				break
			}
			return -1, err
		}
	}
	err := c.cmd.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// Transcript is every envelope sent and received, in the order it crossed the
// pipe. It is what becomes the trace the validator judges.
func (c *Client) Transcript() []protocol.Envelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]protocol.Envelope(nil), c.transcript...)
}

package rpc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lsm/open-agent-protocol/adapter/hermes/internal/native"
)

const defaultStderrLimit = 64 << 10

var ErrHandshake = errors.New("hermes rpc: gateway ready handshake failed")

type ProcessConfig struct {
	Path string
	Args []string
	Dir  string
	// Env nil inherits the parent environment; non-nil replaces it verbatim.
	// An empty non-nil slice is a valid empty allowlist.
	Env                []string
	FrameLimit         int
	QueueCapacity      int
	WriteQueueCapacity int
	StderrLimit        int
	ExitTimeout        time.Duration
}

type Process struct {
	Client     *Client
	Ready      native.ReadyPayload
	command    *exec.Cmd
	stdin      io.WriteCloser
	pipes      *pipeCloser
	stderrPipe io.ReadCloser
	stderr     *limitedBuffer
	stderrDone chan struct{}
	waitDone   chan struct{}
	waitMu     sync.Mutex
	waitErr    error
	timeout    time.Duration
	close      sync.Once
	closeErr   error
}

// Start spawns the pinned gateway entry point (the TUI launches
// `<python> -m tui_gateway.entry` with piped stdio) and completes the ready
// handshake: the gateway emits gateway.ready — carrying the replay epoch —
// before reading any input. Any other first observation is foreign activity
// at this boundary and fails the handshake closed.
func Start(ctx context.Context, config ProcessConfig) (*Process, error) {
	if config.Path == "" {
		return nil, fmt.Errorf("%w: executable path is required", ErrHandshake)
	}
	command := exec.Command(config.Path, config.Args...)
	command.Dir = config.Dir
	if config.Env != nil {
		command.Env = append([]string{}, config.Env...)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := command.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	limit := config.StderrLimit
	if limit <= 0 {
		limit = defaultStderrLimit
	}
	stderr := &limitedBuffer{limit: limit}
	stderrDone := make(chan struct{})
	go func() { _, _ = io.Copy(stderr, stderrPipe); close(stderrDone) }()
	pipes := &pipeCloser{read: stdout, write: stdin}
	process := &Process{command: command, stdin: stdin, pipes: pipes, stderrPipe: stderrPipe, stderr: stderr, stderrDone: stderrDone, waitDone: make(chan struct{}), timeout: config.ExitTimeout}
	if process.timeout <= 0 {
		process.timeout = 10 * time.Second
	}
	process.Client = NewClient(stdout, stdin, ClientOptions{FrameLimit: config.FrameLimit, QueueCapacity: config.QueueCapacity, WriteQueueCapacity: config.WriteQueueCapacity, CloseReadWriter: pipes})
	go process.wait()

	// Activate the ordered inbound stream before observing anything: the
	// ready frame is an event notification like every later one.
	inbound := process.Client.Inbound()
	select {
	case message := <-inbound:
		ready, err := readyFromMessage(message)
		if err != nil {
			_ = process.abort()
			return nil, fmt.Errorf("%w: %v; stderr: %s", ErrHandshake, err, process.Stderr())
		}
		if err := native.ValidateReady(&ready); err != nil {
			_ = process.abort()
			return nil, fmt.Errorf("%w: %v; stderr: %s", ErrHandshake, err, process.Stderr())
		}
		process.Ready = ready
	case <-ctx.Done():
		_ = process.abort()
		return nil, fmt.Errorf("%w: %v; stderr: %s", ErrHandshake, ctx.Err(), process.Stderr())
	case <-process.Client.Done():
		_ = process.abort()
		return nil, fmt.Errorf("%w: %v; stderr: %s", ErrHandshake, process.Client.Err(), process.Stderr())
	}
	return process, nil
}

// readyFromMessage accepts only the gateway.ready event as the handshake's
// first observation; a barrier means the peer's very first frame was a
// response, which no request could have produced.
func readyFromMessage(message InboundMessage) (native.ReadyPayload, error) {
	if message.Barrier != nil {
		close(message.Barrier)
		return native.ReadyPayload{}, errors.New("response preceded the ready event")
	}
	if message.Request != nil {
		return native.ReadyPayload{}, errors.New("reverse request preceded the ready event")
	}
	event, ok := message.Notification.Value.(*native.Event)
	if !ok || event.Type != native.EventGatewayReady {
		return native.ReadyPayload{}, fmt.Errorf("first observation was %q, not gateway.ready", firstObservationType(message))
	}
	var ready native.ReadyPayload
	if err := native.DecodeStrict(event.Payload, &ready); err != nil {
		return native.ReadyPayload{}, fmt.Errorf("invalid ready payload: %w", err)
	}
	return ready, nil
}

func firstObservationType(message InboundMessage) string {
	if message.Notification != nil {
		if event, ok := message.Notification.Value.(*native.Event); ok {
			return event.Type
		}
		return message.Notification.Method
	}
	return "unknown"
}

func (p *Process) Stderr() string        { return redact(p.stderr.String()) }
func (p *Process) Done() <-chan struct{} { return p.waitDone }
func (p *Process) WaitError() error      { p.waitMu.Lock(); defer p.waitMu.Unlock(); return p.waitErr }

// Close tears the gateway down the way the pinned TUI does: close stdin. The
// gateway's read loop sees a genuine EOF, breaks, flushes sessions via
// atexit, and exits 0. A graceful SIGTERM (the gateway drains within ~1 s)
// and then a kill bound the wait; a non-zero or timed-out exit is the error.
func (p *Process) Close(ctx context.Context) error {
	p.close.Do(func() {
		_ = p.stdin.Close()
		select {
		case <-p.waitDone:
			p.closeErr = p.WaitError()
		case <-time.After(p.timeout):
			_ = p.command.Process.Signal(os.Interrupt)
			select {
			case <-p.waitDone:
			case <-time.After(2 * time.Second):
				_ = p.command.Process.Kill()
				<-p.waitDone
			}
			p.closeErr = errors.New("hermes rpc: gateway did not exit on stdin EOF")
		case <-ctx.Done():
			_ = p.command.Process.Kill()
			<-p.waitDone
			p.closeErr = ctx.Err()
		}
		p.Client.closeWith(ErrClosed)
	})
	return p.closeErr
}

func (p *Process) wait() {
	err := p.command.Wait()
	// A descendant that inherited stderr can hold the read end open after
	// the parent exits; closing our side bounds the drain.
	_ = p.stderrPipe.Close()
	<-p.stderrDone
	p.waitMu.Lock()
	p.waitErr = err
	p.waitMu.Unlock()
	_ = p.pipes.Close()
	p.Client.shutdown(processExitError(err))
	close(p.waitDone)
}

func processExitError(err error) error {
	if err == nil {
		return errors.New("hermes rpc: process exited")
	}
	return fmt.Errorf("hermes rpc: process exited: %w", err)
}

func (p *Process) abort() error { _ = p.command.Process.Kill(); <-p.waitDone; return p.WaitError() }

type pipeCloser struct {
	read  io.Closer
	write io.Closer
	once  sync.Once
}

func (p *pipeCloser) Close() error {
	var out error
	p.once.Do(func() {
		if p.write != nil {
			out = p.write.Close()
		}
		if p.read != nil {
			if err := p.read.Close(); out == nil {
				out = err
			}
		}
	})
	return out
}

type limitedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = true
		return n, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(data)
	return n, nil
}
func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	v := b.buffer.String()
	if b.truncated {
		v += " [truncated]"
	}
	return v
}

var secretLine = regexp.MustCompile(`(?i)(authorization|x-api-key|api[_-]?key|auth[_-]?token|password|secret)(\s*[:=]\s*)(?:Bearer\s+)?([^\s,;]+)`)

func redact(v string) string {
	return strings.TrimSpace(secretLine.ReplaceAllString(v, `$1$2[REDACTED]`))
}

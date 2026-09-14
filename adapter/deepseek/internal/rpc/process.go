package rpc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lsm/open-agent-protocol/adapter/deepseek/internal/native"
)

const defaultStderrLimit = 64 << 10

var ErrHandshake = errors.New("deepseek rpc: initialization handshake failed")

type ProcessConfig struct {
	Path string
	Args []string
	Dir  string
	// Env nil inherits the parent environment; non-nil replaces it verbatim.
	Env                []string
	FrameLimit         int
	QueueCapacity      int
	WriteQueueCapacity int
	StderrLimit        int
	ShutdownTimeout    time.Duration
	Initialize         native.InitializeParams
}

type Process struct {
	Client     *Client
	Initialize native.InitializeResult
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

func Start(ctx context.Context, config ProcessConfig) (*Process, error) {
	if config.Path == "" {
		return nil, fmt.Errorf("%w: executable path is required", ErrHandshake)
	}
	if err := native.ValidateInitializeParams(config.Initialize); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrHandshake, err)
	}
	command := exec.Command(config.Path, append([]string(nil), config.Args...)...)
	command.Dir = config.Dir
	// slices.Clone preserves non-nilness: an explicitly empty allowlist stays
	// empty instead of collapsing to nil and inheriting the parent.
	if config.Env != nil {
		command.Env = slices.Clone(config.Env)
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
	process := &Process{command: command, stdin: stdin, pipes: pipes, stderrPipe: stderrPipe, stderr: stderr, stderrDone: stderrDone, waitDone: make(chan struct{}), timeout: config.ShutdownTimeout}
	if process.timeout <= 0 {
		process.timeout = 5 * time.Second
	}
	process.Client = NewClient(stdout, stdin, ClientOptions{FrameLimit: config.FrameLimit, QueueCapacity: config.QueueCapacity, WriteQueueCapacity: config.WriteQueueCapacity, CloseReadWriter: pipes, StrictResponseIDs: true})
	go process.wait()

	// Activate the ordered inbound stream before the handshake so the
	// initialize response barriers behind every earlier notification. Any
	// native observation preceding the handshake is foreign activity at this
	// boundary: the runtime owns no sessions before initialize returns, so
	// fail closed rather than buffering events of unknown provenance.
	inbound := process.Client.Inbound()
	type initOutcome struct {
		result native.InitializeResult
		err    error
	}
	ready := make(chan initOutcome, 1)
	go func() {
		var initialized native.InitializeResult
		err := process.Client.Call(ctx, native.MethodInitialize, config.Initialize, &initialized)
		ready <- initOutcome{result: initialized, err: err}
	}()
	var initialized native.InitializeResult
handshake:
	for {
		select {
		case outcome := <-ready:
			if outcome.err != nil {
				_ = process.abort()
				return nil, fmt.Errorf("%w: %v; stderr: %s", ErrHandshake, outcome.err, process.Stderr())
			}
			initialized = outcome.result
			break handshake
		case message := <-inbound:
			if message.Barrier != nil {
				close(message.Barrier)
				continue
			}
			_ = process.abort()
			return nil, fmt.Errorf("%w: native observation preceded initialize response; stderr: %s", ErrHandshake, process.Stderr())
		case <-ctx.Done():
			_ = process.abort()
			return nil, fmt.Errorf("%w: %v; stderr: %s", ErrHandshake, ctx.Err(), process.Stderr())
		case <-process.Client.Done():
			_ = process.abort()
			return nil, fmt.Errorf("%w: %v; stderr: %s", ErrHandshake, process.Client.Err(), process.Stderr())
		}
	}
	if err := native.ValidateInitializeResult(initialized); err != nil {
		_ = process.abort()
		return nil, fmt.Errorf("%w: %v; stderr: %s", ErrHandshake, err, process.Stderr())
	}
	process.Initialize = initialized
	return process, nil
}

func (p *Process) Stderr() string        { return redact(p.stderr.String()) }
func (p *Process) Done() <-chan struct{} { return p.waitDone }
func (p *Process) WaitError() error      { p.waitMu.Lock(); defer p.waitMu.Unlock(); return p.waitErr }
func (p *Process) Close(ctx context.Context) error {
	p.close.Do(func() {
		// The shutdown response is delivered only after its ordering barrier is
		// acknowledged by an inbound consumer. During teardown the semantic
		// consumer may already be gone, so drain the stream here: acknowledge
		// barriers and discard residual observations — the session is closing
		// and they can no longer affect reducer state.
		drained := make(chan struct{})
		go func() {
			defer close(drained)
			for {
				select {
				case message := <-p.Client.Inbound():
					if message.Barrier != nil {
						close(message.Barrier)
					}
				case <-p.Client.Done():
					return
				}
			}
		}()
		shutdownCtx, cancel := context.WithTimeout(ctx, p.timeout)
		defer cancel()
		var result struct{}
		err := p.Client.Call(shutdownCtx, native.MethodShutdown, nil, &result)
		if err == nil {
			_ = p.stdin.Close()
		}
		select {
		case <-p.waitDone:
			p.closeErr = p.WaitError()
		case <-ctx.Done():
			p.killAndRelease()
			<-p.waitDone
			p.closeErr = ctx.Err()
		case <-shutdownCtx.Done():
			p.killAndRelease()
			<-p.waitDone
			p.closeErr = errors.New("deepseek rpc: shutdown timed out")
		}
		p.Client.closeWith(ErrClosed)
		<-drained
	})
	return p.closeErr
}
func (p *Process) wait() {
	// Drain stdout before reaping. Cmd.Wait closes the stdout pipe, so the
	// reader must finish routing every frame already buffered there before the
	// pipe is closed; otherwise a response the child wrote immediately before
	// exiting is reported as a process-exit failure on the pending call.
	<-p.Client.ReadDone()
	p.drainStderr()
	err := p.command.Wait()
	p.waitMu.Lock()
	p.waitErr = err
	p.waitMu.Unlock()
	_ = p.pipes.Close()
	p.Client.shutdown(processExitError(err))
	close(p.waitDone)
}
func processExitError(err error) error {
	if err == nil {
		return errors.New("deepseek rpc: process exited")
	}
	return fmt.Errorf("deepseek rpc: process exited: %w", err)
}
func (p *Process) abort() error { p.killAndRelease(); <-p.waitDone; return p.WaitError() }

// drainStderr waits for the stderr copier to finish before the child is reaped.
// Cmd.Wait closes the pipes it created as soon as the child exits, and
// StderrPipe's contract is that every read must complete first: a copier that
// has not yet consumed the buffered bytes fails on a closed file and the bytes
// are lost, which is how a handshake failure ended up composing an empty
// stderr. A descendant that inherited stderr can keep the read end from
// reaching EOF, so bound the drain and close our side to release the copier,
// exactly as the shutdown paths bound the stdout drain.
func (p *Process) drainStderr() {
	timer := time.NewTimer(p.timeout)
	defer timer.Stop()
	select {
	case <-p.stderrDone:
	case <-timer.C:
		_ = p.stderrPipe.Close()
		<-p.stderrDone
	}
}

// killAndRelease kills the child and retires the client before reaping. Reaping
// waits for the reader to drain (see wait), but a reader blocked on a response
// barrier — or on a stdout a descendant still holds open — would never reach
// EOF. Retiring the client closes the pipe and releases the barrier so the
// drain completes instead of stalling teardown.
func (p *Process) killAndRelease() {
	_ = p.command.Process.Kill()
	// Release stderr too, not just the client's stdout. Forced shutdown has
	// already spent its budget; without this the drain in wait() would start a
	// fresh full timeout against a descendant-held stderr and Close would
	// overrun its configured bound by a second timeout.
	_ = p.stderrPipe.Close()
	p.Client.closeWith(processExitError(nil))
}

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

var secretLine = regexp.MustCompile(`(?i)(authorization|x-api-key|api[_-]?key|auth[_-]?token|password|secret|token)(\s*[:=]\s*)(?:Bearer\s+)?([^\s,;]+)`)
var secretQuoted = regexp.MustCompile(`(?i)((?:authorization|x-api-key|api[_-]?key|auth[_-]?token|password|secret|token)"?\s*[:=]\s*)"[^"]*"`)

func redact(v string) string {
	v = secretQuoted.ReplaceAllString(v, `$1"[REDACTED]"`)
	return strings.TrimSpace(secretLine.ReplaceAllString(v, `$1$2[REDACTED]`))
}

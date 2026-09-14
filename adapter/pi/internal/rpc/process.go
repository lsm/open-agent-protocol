package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lsm/open-agent-protocol/adapter/pi/internal/native"
)

const defaultStderrLimit = 64 << 10

var ErrHandshake = errors.New("pi rpc: startup handshake failed")

type ProcessConfig struct {
	Path               string
	Args               []string
	Dir                string
	Env                []string
	FrameLimit         int
	QueueCapacity      int
	WriteQueueCapacity int
	StderrLimit        int
	ShutdownTimeout    time.Duration
}

type Process struct {
	Client       *Client
	InitialState native.SessionState
	command      *exec.Cmd
	stdin        io.WriteCloser
	pipes        *pipeCloser
	stderrPipe   io.ReadCloser
	stderr       *limitedBuffer
	stderrDone   chan struct{}
	waitDone     chan struct{}
	waitMu       sync.Mutex
	waitErr      error
	timeout      time.Duration
	close        sync.Once
	closeErr     error
}

func Start(ctx context.Context, config ProcessConfig) (*Process, error) {
	if config.Path == "" {
		return nil, fmt.Errorf("%w: executable path is required", ErrHandshake)
	}
	args := append([]string(nil), config.Args...)
	args = append(args, "--mode", "rpc")
	command := exec.Command(config.Path, args...)
	command.Dir = config.Dir
	// A nil environment inherits the complete parent environment. A supplied
	// slice is installed verbatim so callers can provide a complete hermetic one.
	// slices.Clone preserves non-nilness, so an explicitly empty allowlist stays
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
	process.Client = NewClient(stdout, stdin, ClientOptions{FrameLimit: config.FrameLimit, QueueCapacity: config.QueueCapacity, WriteQueueCapacity: config.WriteQueueCapacity, CloseReadWriter: pipes})
	go process.wait()

	// Pi has no ready frame, so get_state is the handshake. Drive its response
	// barrier here without leaving a competing inbound consumer behind for the
	// semantic adapter. Any observation before readiness is ambiguous and fails
	// startup rather than being silently discarded.
	type stateResult struct {
		state native.SessionState
		err   error
	}
	ready := make(chan stateResult, 1)
	go func() {
		var state native.SessionState
		err := process.Client.Call(ctx, native.Command{Type: native.CommandGetState}, &state)
		ready <- stateResult{state: state, err: err}
	}()
	var state native.SessionState
readiness:
	for {
		select {
		case result := <-ready:
			if result.err != nil {
				_ = process.abort()
				return nil, fmt.Errorf("%w: %v; stderr: %s", ErrHandshake, result.err, process.Stderr())
			}
			state = result.state
			break readiness
		case inbound := <-process.Client.Inbound():
			if inbound.Barrier != nil {
				close(inbound.Barrier)
				continue
			}
			_ = process.abort()
			return nil, fmt.Errorf("%w: native observation preceded get_state response; stderr: %s", ErrHandshake, process.Stderr())
		case <-ctx.Done():
			_ = process.abort()
			return nil, fmt.Errorf("%w: %v; stderr: %s", ErrHandshake, ctx.Err(), process.Stderr())
		case <-process.Client.Done():
			_ = process.abort()
			return nil, fmt.Errorf("%w: %v; stderr: %s", ErrHandshake, process.Client.Err(), process.Stderr())
		}
	}
	if !validInitialState(state) {
		_ = process.abort()
		return nil, fmt.Errorf("%w: invalid get_state response; stderr: %s", ErrHandshake, process.Stderr())
	}
	process.InitialState = state
	return process, nil
}

func acknowledgeBarriers(client *Client) {
	select {
	case message := <-client.Inbound():
		if message.Barrier != nil {
			close(message.Barrier)
		}
	case <-client.Done():
	}
}

func validInitialState(state native.SessionState) bool {
	validMode := func(mode native.QueueMode) bool { return mode == native.QueueAll || mode == native.QueueOneAtATime }
	validThinking := func(level native.ThinkingLevel) bool {
		switch level {
		case native.ThinkingOff, native.ThinkingMinimal, native.ThinkingLow, native.ThinkingMedium, native.ThinkingHigh, native.ThinkingXHigh, native.ThinkingMax:
			return true
		default:
			return false
		}
	}
	return state.SessionID != "" && validThinking(state.ThinkingLevel) && validMode(state.SteeringMode) &&
		validMode(state.FollowUpMode) && state.MessageCount >= 0 && state.PendingMessageCount >= 0 &&
		(len(state.Model) == 0 || json.Valid(state.Model))
}
func (p *Process) Stderr() string        { return redact(p.stderr.String()) }
func (p *Process) Done() <-chan struct{} { return p.waitDone }
func (p *Process) WaitError() error      { p.waitMu.Lock(); defer p.waitMu.Unlock(); return p.waitErr }
func (p *Process) Close(ctx context.Context) error {
	p.close.Do(func() {
		_ = p.stdin.Close()
		timer := time.NewTimer(p.timeout)
		defer timer.Stop()
		select {
		case <-p.waitDone:
			p.closeErr = p.WaitError()
		case <-ctx.Done():
			p.killAndRelease()
			<-p.waitDone
			p.closeErr = ctx.Err()
		case <-timer.C:
			p.killAndRelease()
			<-p.waitDone
			p.closeErr = errors.New("pi rpc: shutdown timed out")
		}
		p.Client.closeWith(ErrClosed)
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
// waits for the reader to drain (see wait), but a reader blocked on a stdout a
// descendant still holds open would never reach EOF. Retiring the client closes
// the pipe so the drain completes instead of stalling teardown.
func (p *Process) killAndRelease() {
	_ = p.command.Process.Kill()
	p.Client.closeWith(processExitError(nil))
}
func processExitError(err error) error {
	if err == nil {
		return errors.New("pi rpc: process exited")
	}
	return fmt.Errorf("pi rpc: process exited: %w", err)
}

type pipeCloser struct {
	read  io.Closer
	write io.Closer
	once  sync.Once
}

func (p *pipeCloser) Close() error {
	var result error
	p.once.Do(func() {
		if p.write != nil {
			result = p.write.Close()
		}
		if p.read != nil {
			if err := p.read.Close(); result == nil {
				result = err
			}
		}
	})
	return result
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
	original := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(data)
	return original, nil
}
func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	value := b.buffer.String()
	if b.truncated {
		value += " [truncated]"
	}
	return value
}

var secretLine = regexp.MustCompile(`(?i)(authorization|x-api-key|api[_-]?key|auth[_-]?token|password|secret|token)(\s*[:=]\s*)(?:Bearer\s+)?([^\s,;]+)`)
var secretQuoted = regexp.MustCompile(`(?i)((?:authorization|x-api-key|api[_-]?key|auth[_-]?token|password|secret|token)"?\s*[:=]\s*)"[^"]*"`)

func redact(value string) string {
	value = secretQuoted.ReplaceAllString(value, `$1"[REDACTED]"`)
	return strings.TrimSpace(secretLine.ReplaceAllString(value, `$1$2[REDACTED]`))
}

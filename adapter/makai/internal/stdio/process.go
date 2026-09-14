package stdio

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
)

const defaultStderrLimit = 64 << 10

var ErrHandshake = errors.New("makai stdio: ready handshake failed")

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
	ShutdownTimeout    time.Duration
	// stderrTap wraps the stderr read end before the copier is started. It is a
	// package-private seam for tests that need the copier to lag the child's
	// exit; production callers leave it nil.
	stderrTap func(io.ReadCloser) io.ReadCloser
}
type Process struct {
	Client     *Client
	command    *exec.Cmd
	stdin      io.WriteCloser
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
	args := append([]string(nil), config.Args...)
	args = append(args, "--stdio")
	cmd := exec.Command(config.Path, args...)
	cmd.Dir = config.Dir
	// slices.Clone preserves non-nilness: an explicitly empty allowlist stays
	// empty instead of collapsing to nil and inheriting the parent.
	cmd.Env = slices.Clone(config.Env)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	limit := config.StderrLimit
	if limit <= 0 {
		limit = defaultStderrLimit
	}
	if config.stderrTap != nil {
		stderrPipe = config.stderrTap(stderrPipe)
	}
	stderr := &limitedBuffer{limit: limit}
	stderrDone := make(chan struct{})
	go func() { _, _ = io.Copy(stderr, stderrPipe); close(stderrDone) }()
	p := &Process{command: cmd, stdin: stdin, stderrPipe: stderrPipe, stderr: stderr, stderrDone: stderrDone, waitDone: make(chan struct{}), timeout: config.ShutdownTimeout}
	if p.timeout <= 0 {
		p.timeout = 5 * time.Second
	}
	decoder := NewDecoder(stdout, config.FrameLimit)
	ready := make(chan error, 1)
	go func() {
		frame, err := decoder.Decode()
		if err == nil && frame.Ready == nil {
			err = fmt.Errorf("%w: first frame is not ready", ErrHandshake)
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = p.abortBeforeWait()
			return nil, fmt.Errorf("%w: %v; stderr: %s", ErrHandshake, err, p.Stderr())
		}
	case <-ctx.Done():
		_ = p.abortBeforeWait()
		return nil, fmt.Errorf("%w: %v; stderr: %s", ErrHandshake, ctx.Err(), p.Stderr())
	}
	// The client owns the stdout pipe so forced shutdown can release a reader
	// blocked on a stdout that a descendant still holds open (see killAndRelease).
	p.Client = newClient(decoder, stdin, ClientOptions{FrameLimit: config.FrameLimit, QueueCapacity: config.QueueCapacity, WriteQueueCapacity: config.WriteQueueCapacity, CloseReadWriter: stdout})
	go p.wait()
	return p, nil
}
func (p *Process) Stderr() string { return redact(p.stderr.String()) }
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
			p.closeErr = errors.New("makai stdio: shutdown timed out")
		}
		p.Client.shutdown(ErrClosed)
	})
	return p.closeErr
}
func (p *Process) WaitError() error { p.waitMu.Lock(); defer p.waitMu.Unlock(); return p.waitErr }
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
	_ = p.stdin.Close()
	p.Client.shutdown(processExitError(err))
	close(p.waitDone)
}

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
func (p *Process) abortBeforeWait() error {
	_ = p.command.Process.Kill()
	p.drainStderr()
	return p.command.Wait()
}
func processExitError(err error) error {
	if err == nil {
		return errors.New("makai stdio: process exited")
	}
	return fmt.Errorf("makai stdio: process exited: %w", err)
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

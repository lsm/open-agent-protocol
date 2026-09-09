package rpc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
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
	if config.Env != nil {
		command.Env = append([]string(nil), config.Env...)
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
			_ = p.command.Process.Kill()
			<-p.waitDone
			p.closeErr = ctx.Err()
		case <-shutdownCtx.Done():
			_ = p.command.Process.Kill()
			<-p.waitDone
			p.closeErr = errors.New("deepseek rpc: shutdown timed out")
		}
		p.Client.closeWith(ErrClosed)
	})
	return p.closeErr
}
func (p *Process) wait() {
	err := p.command.Wait()
	// A descendant that inherited stderr can hold the read end open after the
	// parent exits; closing our side bounds the drain instead of hanging
	// teardown indefinitely on a leaked grandchild.
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
		return errors.New("deepseek rpc: process exited")
	}
	return fmt.Errorf("deepseek rpc: process exited: %w", err)
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

var secretLine = regexp.MustCompile(`(?i)(authorization|x-api-key|api[_-]?key|auth[_-]?token)(\s*[:=]\s*)(?:Bearer\s+)?([^\s,;]+)`)

func redact(v string) string {
	return strings.TrimSpace(secretLine.ReplaceAllString(v, `$1$2[REDACTED]`))
}

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
)

const defaultStderrLimit = 64 << 10

var ErrProcessClosed = errors.New("claude rpc: process did not exit after stdin EOF")

type ProcessConfig struct {
	Path string
	Args []string
	Dir  string

	Env                []string
	FrameLimit         int
	QueueCapacity      int
	WriteQueueCapacity int
	StderrLimit        int
	ExitTimeout        time.Duration
}

type Process struct {
	Client     *Client
	command    *exec.Cmd
	stdin      io.WriteCloser
	pipes      *pipeCloser
	stderrPipe io.ReadCloser
	stderr     *limitedBuffer
	stderrDone chan struct{}
	waitDone   chan struct{}
	waitMu     sync.Mutex
	waitErr    error
	grace      time.Duration
	close      sync.Once
	closeErr   error
}

func Start(ctx context.Context, config ProcessConfig) (*Process, error) {
	if config.Path == "" {
		return nil, errors.New("claude rpc: executable path is required")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
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
	process := &Process{command: command, stdin: stdin, pipes: pipes, stderrPipe: stderrPipe, stderr: stderr, stderrDone: stderrDone, waitDone: make(chan struct{}), grace: config.ExitTimeout}

	if process.grace <= 0 {
		process.grace = 5 * time.Second
	}
	process.Client = NewClient(stdout, stdin, ClientOptions{FrameLimit: config.FrameLimit, QueueCapacity: config.QueueCapacity, WriteQueueCapacity: config.WriteQueueCapacity, CloseReadWriter: pipes})
	go process.wait()
	return process, nil
}

func (p *Process) Stderr() string        { return redact(p.stderr.String()) }
func (p *Process) Done() <-chan struct{} { return p.waitDone }
func (p *Process) WaitError() error      { p.waitMu.Lock(); defer p.waitMu.Unlock(); return p.waitErr }

func (p *Process) Close(ctx context.Context) error {
	p.close.Do(func() {
		_ = p.stdin.Close()
		select {
		case <-p.waitDone:
		case <-time.After(p.grace):
			_ = p.command.Process.Signal(os.Interrupt)
			select {
			case <-p.waitDone:
			case <-time.After(p.grace):
				p.killAndRelease()
				<-p.waitDone
			}
			p.closeErr = ErrProcessClosed
		case <-ctx.Done():
			p.killAndRelease()
			<-p.waitDone
			p.closeErr = ctx.Err()
		}
		p.Client.closeWith(ErrClosed)
	})
	return p.closeErr
}

func (p *Process) wait() {

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

func (p *Process) drainStderr() { p.drainStderrWithin(p.grace) }

func (p *Process) drainStderrWithin(limit time.Duration) {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-p.stderrDone:
	case <-timer.C:
		_ = p.stderrPipe.Close()
		<-p.stderrDone
	}
}

func processExitError(err error) error {
	if err == nil {
		return errors.New("claude rpc: process exited")
	}
	return fmt.Errorf("claude rpc: process exited: %w", err)
}

func (p *Process) abort() error {
	_ = p.command.Process.Kill()

	p.drainStderrWithin(abortStderrGrace)
	p.killAndRelease()
	<-p.waitDone
	return p.WaitError()
}

func (p *Process) killAndRelease() {
	_ = p.command.Process.Kill()

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

const abortStderrGrace = 250 * time.Millisecond

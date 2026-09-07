package stdio

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
)

const defaultStderrLimit = 64 << 10

var ErrHandshake = errors.New("makai stdio: ready handshake failed")

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
	Client     *Client
	command    *exec.Cmd
	stdin      io.WriteCloser
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
	cmd.Env = append([]string(nil), config.Env...)
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
	stderr := &limitedBuffer{limit: limit}
	stderrDone := make(chan struct{})
	go func() { _, _ = io.Copy(stderr, stderrPipe); close(stderrDone) }()
	p := &Process{command: cmd, stdin: stdin, stderr: stderr, stderrDone: stderrDone, waitDone: make(chan struct{}), timeout: config.ShutdownTimeout}
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
	p.Client = newClient(decoder, stdin, ClientOptions{FrameLimit: config.FrameLimit, QueueCapacity: config.QueueCapacity, WriteQueueCapacity: config.WriteQueueCapacity, CloseReadWriter: nil})
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
			_ = p.command.Process.Kill()
			<-p.waitDone
			p.closeErr = ctx.Err()
		case <-timer.C:
			_ = p.command.Process.Kill()
			<-p.waitDone
			p.closeErr = errors.New("makai stdio: shutdown timed out")
		}
		p.Client.shutdown(ErrClosed)
	})
	return p.closeErr
}
func (p *Process) WaitError() error { p.waitMu.Lock(); defer p.waitMu.Unlock(); return p.waitErr }
func (p *Process) wait() {
	err := p.command.Wait()
	<-p.stderrDone
	p.waitMu.Lock()
	p.waitErr = err
	p.waitMu.Unlock()
	_ = p.stdin.Close()
	p.Client.shutdown(processExitError(err))
	close(p.waitDone)
}
func (p *Process) abortBeforeWait() error {
	_ = p.command.Process.Kill()
	err := p.command.Wait()
	<-p.stderrDone
	return err
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

var secretLine = regexp.MustCompile(`(?i)(authorization|x-api-key|api[_-]?key|auth[_-]?token)(\s*[:=]\s*)([^\s,;]+)`)

func redact(v string) string {
	return strings.TrimSpace(secretLine.ReplaceAllString(v, `$1$2[REDACTED]`))
}

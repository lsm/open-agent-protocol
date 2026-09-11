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
	"strings"
	"sync"
	"time"
)

const defaultStderrLimit = 64 << 10

var ErrHandshake = errors.New("codex app-server rpc: initialization handshake failed")

type ClientInfo struct {
	Name    string  `json:"name"`
	Title   *string `json:"title,omitempty"`
	Version string  `json:"version"`
}

type InitializeCapabilities struct {
	ExperimentalAPI                bool                       `json:"experimentalApi,omitempty"`
	RequestAttestation             bool                       `json:"requestAttestation,omitempty"`
	MCPServerOpenAIFormElicitation bool                       `json:"mcpServerOpenaiFormElicitation,omitempty"`
	OptOutNotificationMethods      []string                   `json:"optOutNotificationMethods,omitempty"`
	Extensions                     map[string]json.RawMessage `json:"extensions,omitempty"`
}

type InitializeResponse struct {
	UserAgent      string `json:"userAgent"`
	CodexHome      string `json:"codexHome"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOS     string `json:"platformOs"`
}

type ProcessConfig struct {
	Path            string
	Args            []string
	Dir             string
	Env             []string
	FrameLimit      int
	QueueCapacity   int
	StderrLimit     int
	ShutdownTimeout time.Duration
	ClientInfo      ClientInfo
	Capabilities    *InitializeCapabilities
}

type Process struct {
	Client     *Client
	Initialize InitializeResponse

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
	if config.Path == "" || config.ClientInfo.Name == "" || config.ClientInfo.Version == "" {
		return nil, fmt.Errorf("%w: executable path and client info are required", ErrHandshake)
	}
	args := append([]string(nil), config.Args...)
	args = append(args, "app-server", "--listen", "stdio://")
	command := exec.Command(config.Path, args...)
	command.Dir = config.Dir
	command.Env = append([]string(nil), config.Env...)
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
	stderrLimit := config.StderrLimit
	if stderrLimit <= 0 {
		stderrLimit = defaultStderrLimit
	}
	stderr := &limitedBuffer{limit: stderrLimit}
	stderrDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(stderr, stderrPipe)
		close(stderrDone)
	}()

	process := &Process{
		command: command, stdin: stdin, stderr: stderr, stderrDone: stderrDone, waitDone: make(chan struct{}),
		timeout: config.ShutdownTimeout,
	}
	if process.timeout <= 0 {
		process.timeout = 5 * time.Second
	}
	process.Client = NewClient(stdout, stdin, ClientOptions{FrameLimit: config.FrameLimit, QueueCapacity: config.QueueCapacity, FirstRequestID: -1, StrictResponseIDs: true})
	go process.wait()

	params := struct {
		ClientInfo   ClientInfo              `json:"clientInfo"`
		Capabilities *InitializeCapabilities `json:"capabilities,omitempty"`
	}{ClientInfo: config.ClientInfo, Capabilities: config.Capabilities}
	var initialized InitializeResponse
	if err := process.Client.Call(ctx, "initialize", params, &initialized); err != nil {
		_ = process.abort()
		return nil, fmt.Errorf("%w: %v; stderr: %s", ErrHandshake, err, process.Stderr())
	}
	if err := process.Client.Notify(ctx, "initialized", nil); err != nil {
		_ = process.abort()
		return nil, fmt.Errorf("%w: notify initialized: %v; stderr: %s", ErrHandshake, err, process.Stderr())
	}
	process.Initialize = initialized
	return process, nil
}

func (process *Process) Stderr() string { return redact(process.stderr.String()) }

func (process *Process) Close(ctx context.Context) error {
	process.close.Do(func() {
		_ = process.stdin.Close()
		timer := time.NewTimer(process.timeout)
		defer timer.Stop()
		select {
		case <-process.waitDone:
			process.closeErr = process.WaitError()
		case <-ctx.Done():
			_ = process.command.Process.Kill()
			<-process.waitDone
			process.closeErr = ctx.Err()
		case <-timer.C:
			_ = process.command.Process.Kill()
			<-process.waitDone
			process.closeErr = fmt.Errorf("codex app-server rpc: shutdown timed out")
		}
		process.Client.shutdown(ErrClosed)
	})
	return process.closeErr
}

func (process *Process) WaitError() error {
	process.waitMu.Lock()
	defer process.waitMu.Unlock()
	return process.waitErr
}

func (process *Process) wait() {
	// Drain stdout before reaping. Cmd.Wait closes the stdout pipe, so the
	// reader must finish delivering every frame already buffered there before
	// shutdown fails the pending calls; otherwise a response the child wrote
	// immediately before exiting is reported as a process-exit error.
	<-process.Client.ReadDone()
	err := process.command.Wait()
	<-process.stderrDone
	process.waitMu.Lock()
	process.waitErr = err
	process.waitMu.Unlock()
	_ = process.stdin.Close()
	process.Client.shutdown(processExitError(err))
	close(process.waitDone)
}

func processExitError(err error) error {
	if err == nil {
		return errors.New("codex app-server rpc: process exited")
	}
	return fmt.Errorf("codex app-server rpc: process exited: %w", err)
}

func (process *Process) abort() error {
	_ = process.command.Process.Kill()
	<-process.waitDone
	return process.WaitError()
}

type limitedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	original := len(data)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining <= 0 {
		buffer.truncated = true
		return original, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		buffer.truncated = true
	}
	_, _ = buffer.buffer.Write(data)
	return original, nil
}

func (buffer *limitedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	value := buffer.buffer.String()
	if buffer.truncated {
		value += " [truncated]"
	}
	return value
}

var secretLine = regexp.MustCompile(`(?i)(authorization|x-api-key|api[_-]?key|auth[_-]?token)(\s*[:=]\s*)([^\s,;]+)`)

func redact(value string) string {
	value = secretLine.ReplaceAllString(value, `$1$2[REDACTED]`)
	return strings.TrimSpace(value)
}

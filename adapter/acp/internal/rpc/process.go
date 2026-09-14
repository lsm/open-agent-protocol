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
)

const (
	ProtocolVersion    = 1
	defaultStderrLimit = 64 << 10
)

var ErrHandshake = errors.New("acp rpc: initialization handshake failed")

type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

// ClientCapabilities is intentionally structural and permissive at its leaves.
// The transport preserves negotiated v1 capability documents for the semantic
// adapter without interpreting them.
type ClientCapabilities map[string]json.RawMessage
type AgentCapabilities map[string]json.RawMessage

type InitializeRequest struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities"`
	ClientInfo         *Implementation    `json:"clientInfo,omitempty"`
}

type InitializeResponse struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	AuthMethods       []json.RawMessage `json:"authMethods,omitempty"`
	AgentInfo         *Implementation   `json:"agentInfo,omitempty"`
}

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
	ProtocolVersion    int
	ClientCapabilities ClientCapabilities
	ClientInfo         *Implementation
}

type Process struct {
	Client     *Client
	Initialize InitializeResponse

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
	version := config.ProtocolVersion
	if version == 0 {
		version = ProtocolVersion
	}
	if version != ProtocolVersion {
		return nil, fmt.Errorf("%w: unsupported requested protocol version %d", ErrHandshake, version)
	}
	if config.ClientInfo != nil && (config.ClientInfo.Name == "" || config.ClientInfo.Version == "") {
		return nil, fmt.Errorf("%w: client info name and version are required", ErrHandshake)
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

	pipes := &pipeCloser{read: stdout, write: stdin}
	process := &Process{
		command: command, stdin: stdin, pipes: pipes, stderrPipe: stderrPipe, stderr: stderr, stderrDone: stderrDone,
		waitDone: make(chan struct{}), timeout: config.ShutdownTimeout,
	}
	if process.timeout <= 0 {
		process.timeout = 5 * time.Second
	}
	process.Client = NewClient(stdout, stdin, ClientOptions{
		FrameLimit: config.FrameLimit, QueueCapacity: config.QueueCapacity,
		WriteQueueCapacity: config.WriteQueueCapacity, FirstRequestID: 0,
		CloseReadWriter: pipes, StrictResponseIDs: true,
	})
	go process.wait()

	capabilities := cloneMap(config.ClientCapabilities)
	if capabilities == nil {
		capabilities = ClientCapabilities{}
	}
	params := InitializeRequest{
		ProtocolVersion: version, ClientCapabilities: capabilities, ClientInfo: config.ClientInfo,
	}
	var initialized InitializeResponse
	if err := process.Client.Call(ctx, "initialize", params, &initialized); err != nil {
		_ = process.abort()
		return nil, fmt.Errorf("%w: %v; stderr: %s", ErrHandshake, err, process.Stderr())
	}
	if initialized.ProtocolVersion != ProtocolVersion {
		_ = process.abort()
		return nil, fmt.Errorf("%w: agent selected unsupported protocol version %d; stderr: %s", ErrHandshake, initialized.ProtocolVersion, process.Stderr())
	}
	if initialized.AgentCapabilities == nil {
		_ = process.abort()
		return nil, fmt.Errorf("%w: agentCapabilities is required; stderr: %s", ErrHandshake, process.Stderr())
	}
	process.Initialize = initialized
	return process, nil
}

func (process *Process) Stderr() string        { return redact(process.stderr.String()) }
func (process *Process) Done() <-chan struct{} { return process.waitDone }

func (process *Process) Close(ctx context.Context) error {
	process.close.Do(func() {
		_ = process.stdin.Close()
		timer := time.NewTimer(process.timeout)
		defer timer.Stop()
		select {
		case <-process.waitDone:
			process.closeErr = process.WaitError()
		case <-ctx.Done():
			process.killAndRelease()
			<-process.waitDone
			process.closeErr = ctx.Err()
		case <-timer.C:
			process.killAndRelease()
			<-process.waitDone
			process.closeErr = errors.New("acp rpc: shutdown timed out")
		}
		process.Client.closeWith(ErrClosed)
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
	// reader must finish routing every frame already buffered there before the
	// pipes are closed; otherwise a response the child wrote immediately before
	// exiting becomes a closed-pipe error on the pending call.
	<-process.Client.ReadDone()
	process.drainStderr()
	err := process.command.Wait()
	process.waitMu.Lock()
	process.waitErr = err
	process.waitMu.Unlock()
	_ = process.pipes.Close()
	process.Client.shutdown(processExitError(err))
	close(process.waitDone)
}

// drainStderr waits for the stderr copier to finish before the child is reaped.
// Cmd.Wait closes the pipes it created as soon as the child exits, and
// StderrPipe's contract is that every read must complete first: a copier that
// has not yet consumed the buffered bytes fails on a closed file and the bytes
// are lost, which is how a handshake failure ended up composing an empty
// stderr. A descendant that inherited stderr can keep the read end from
// reaching EOF, so bound the drain and close our side to release the copier,
// exactly as the shutdown paths bound the stdout drain.
func (process *Process) drainStderr() {
	timer := time.NewTimer(process.timeout)
	defer timer.Stop()
	select {
	case <-process.stderrDone:
	case <-timer.C:
		_ = process.stderrPipe.Close()
		<-process.stderrDone
	}
}

func processExitError(err error) error {
	if err == nil {
		return errors.New("acp rpc: process exited")
	}
	return fmt.Errorf("acp rpc: process exited: %w", err)
}
func (process *Process) abort() error {
	process.killAndRelease()
	<-process.waitDone
	return process.WaitError()
}

// killAndRelease kills the child and closes the read pipes before reaping. A
// surviving descendant could hold stdout or stderr open, and wait() drains both
// before reaping; closing our ends forces those drains to finish. Releasing
// stderr matters as much as stdout here: forced shutdown has already spent its
// budget, and leaving stderr open would start a fresh full timeout inside
// drainStderr and overrun the configured bound by a second timeout.
func (process *Process) killAndRelease() {
	_ = process.command.Process.Kill()
	_ = process.pipes.Close()
	_ = process.stderrPipe.Close()
}

type pipeCloser struct {
	read  io.Closer
	write io.Closer
	once  sync.Once
}

func (closer *pipeCloser) Close() error {
	var result error
	closer.once.Do(func() {
		if closer.write != nil {
			result = closer.write.Close()
		}
		if closer.read != nil {
			if err := closer.read.Close(); result == nil {
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

var secretLine = regexp.MustCompile(`(?i)(authorization|x-api-key|api[_-]?key|auth[_-]?token|password|secret|token)(\s*[:=]\s*)(?:Bearer\s+)?([^\s,;]+)`)
var secretQuoted = regexp.MustCompile(`(?i)((?:authorization|x-api-key|api[_-]?key|auth[_-]?token|password|secret|token)"?\s*[:=]\s*)"[^"]*"`)

func redact(value string) string {
	value = secretQuoted.ReplaceAllString(value, `$1"[REDACTED]"`)
	return strings.TrimSpace(secretLine.ReplaceAllString(value, `$1$2[REDACTED]`))
}
func cloneMap(source ClientCapabilities) ClientCapabilities {
	if source == nil {
		return nil
	}
	result := make(ClientCapabilities, len(source))
	for key, value := range source {
		result[key] = cloneRaw(value)
	}
	return result
}

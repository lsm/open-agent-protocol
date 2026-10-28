package rpc

import (
	"io"
	"os/exec"
	"sync"
	"time"
)

type ProcessConfig struct {
	Path          string
	Args          []string
	Dir           string
	Env           []string
	FrameLimit    int
	QueueCapacity int
}

type Process struct {
	Client  *Client
	command *exec.Cmd
	done    chan struct{}
	waitErr error
}

type processWire struct {
	io.Reader
	io.Writer
	command *exec.Cmd
	stdin   io.Closer
	stdout  io.Closer
	once    sync.Once
}

func (w *processWire) Close() error {
	w.once.Do(func() { _ = w.stdin.Close(); _ = w.stdout.Close(); _ = w.command.Process.Kill() })
	return nil
}

func Start(config ProcessConfig) (*Process, error) {
	command := exec.Command(config.Path, config.Args...)
	command.Dir = config.Dir
	command.Env = make([]string, len(config.Env))
	copy(command.Env, config.Env)
	command.Stderr = io.Discard
	command.WaitDelay = time.Second
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	p := &Process{command: command, done: make(chan struct{})}
	p.Client = NewClient(&processWire{Reader: stdout, Writer: stdin, stdin: stdin, stdout: stdout, command: command}, config.FrameLimit, config.QueueCapacity)
	go func() { p.waitErr = command.Wait(); close(p.done) }()
	return p, nil
}

func (p *Process) Done() <-chan struct{} { return p.done }
func (p *Process) WaitError() error      { <-p.done; return p.waitErr }
func (p *Process) Close() error          { _ = p.Client.Close(); <-p.done; return nil }

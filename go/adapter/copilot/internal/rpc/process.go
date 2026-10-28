package rpc

type ProcessConfig struct {
	Path          string
	Args          []string
	Dir           string
	Env           []string
	FrameLimit    int
	QueueCapacity int
}

type Process struct{ Client *Client }

func Start(ProcessConfig) (*Process, error) { return nil, ErrClosed }
func (*Process) Done() <-chan struct{}      { return nil }
func (*Process) WaitError() error           { return nil }
func (*Process) Close() error               { return nil }

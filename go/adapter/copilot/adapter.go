package copilot

import (
	"context"
	"errors"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
)

type Config struct {
	Executable       string
	Args             []string
	Environment      []string
	WorkingDirectory string
	Model            string
	Clock            base.Clock
	IDs              base.IDGenerator
	JournalCapacity  int
	FrameLimit       int
	QueueCapacity    int
	RequestTimeout   time.Duration
}

type Adapter struct{ config Config }

var _ base.Adapter = (*Adapter)(nil)

func New(config Config) (*Adapter, error)                       { return &Adapter{config: config}, nil }
func (*Adapter) Probe(context.Context) (base.Descriptor, error) { return base.Descriptor{}, nil }
func (*Adapter) Open(context.Context, base.OpenRequest) (base.Session, error) {
	return nil, errors.New("Copilot adapter is not connected")
}

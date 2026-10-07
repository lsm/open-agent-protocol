package serve

import (
	"context"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
)

type closingAdapter struct {
	base.Adapter
	closed int
}

func (c *closingAdapter) Close(context.Context) error {
	c.closed++
	return nil
}

func TestShuttingDownTheHubClosesEachAdapterThatHoldsAProcess(t *testing.T) {
	registry := NewRegistry()
	closing := &closingAdapter{Adapter: base.NewMemory(base.Config{})}
	if err := registry.Register("closing", closing); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("plain", base.NewMemory(base.Config{})); err != nil {
		t.Fatal(err)
	}
	New(registry, Options{}).CloseSessions(context.Background())
	if closing.closed != 1 {
		t.Fatalf("the adapter was closed %d times at shutdown", closing.closed)
	}
}

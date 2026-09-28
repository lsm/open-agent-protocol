package serve

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/binding"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type closedAtOpenAdapter struct {
	adapter.Adapter
}

type closedAtOpenSession struct {
	adapter.Session
}

func (a *closedAtOpenAdapter) Open(ctx context.Context, request adapter.OpenRequest) (adapter.Session, error) {
	session, err := a.Adapter.Open(ctx, request)
	if err != nil {
		return nil, err
	}
	return closedAtOpenSession{Session: session}, nil
}

func (s closedAtOpenSession) State(ctx context.Context) (protocol.SessionState, error) {
	state, err := s.Session.State(ctx)
	if err != nil {
		return state, err
	}
	state.Status = protocol.SessionClosed
	return state, nil
}

func TestASessionThatSettlesAtOpenLeavesNothingForItsReleaseToRecord(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("settling", &closedAtOpenAdapter{Adapter: adapter.NewMemory(adapter.Config{JournalCapacity: 8})}); err != nil {
		t.Fatal(err)
	}
	store, err := binding.File(filepath.Join(t.TempDir(), "bindings.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	hub := New(registry, Options{StreamQueue: 8, Bindings: store})
	entry, _, err := hub.Open(context.Background(), "settling", adapter.OpenRequest{SessionID: "session-settled"})
	if err == nil {
		t.Fatal("the open of a session that settles at open was admitted, so nothing settled")
	}
	if entry == nil {
		t.Fatal("a session that settles at open returned no entry, so the hub lost the handle it must settle")
	}
	if entry.binding.SessionID != "" {
		t.Fatalf("the entry still carries the record %+v, so its release can write a close of its own, in a second critical section than the open it belongs with",
			entry.binding)
	}
}

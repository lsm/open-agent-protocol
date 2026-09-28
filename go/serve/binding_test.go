package serve_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/binding"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

func TestAnOpenIsRecordedAsABindingAndNothingSensitiveIs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.jsonl")
	store, err := binding.File(path)
	if err != nil {
		t.Fatal(err)
	}
	hub := boundHub(t, serve.Options{Bindings: store, Home: "/home/op"})
	ctx := context.Background()
	_, _, err = hub.Open(ctx, "memory", openRequestFor("session-bound"))
	if err != nil {
		t.Fatal(err)
	}
	entry, found, err := store.Latest(ctx, "session-bound")
	if err != nil || !found {
		t.Fatalf("latest found=%v err=%v", found, err)
	}
	if entry.Action != binding.ActionOpened {
		t.Fatalf("action = %q, want the open recorded as an open", entry.Action)
	}
	if entry.Record.Adapter != "memory" || entry.Record.Home != "/home/op" {
		t.Fatalf("record = %+v, want the adapter and the home the hub was given", entry.Record)
	}
	if entry.Record.SessionID != "session-bound" {
		t.Fatalf("record names %q, want the session the caller asked for", entry.Record.SessionID)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"TOKEN", "secret-value", "sk-live", "password", "environment"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("the store holds %q: %s", forbidden, raw)
		}
	}
}

func TestAHubWithNoStoreRecordsNothingAndSaysSo(t *testing.T) {
	hub := boundHub(t, serve.Options{})
	if hub.Binding() != nil {
		t.Fatal("a hub built with no store reports one")
	}
	if _, _, err := hub.Open(context.Background(), "memory", openRequestFor("session-unbound")); err != nil {
		t.Fatal(err)
	}
}

func TestTwoOpensOfDifferentSessionsAreTwoRecords(t *testing.T) {
	store, err := binding.File(filepath.Join(t.TempDir(), "bindings.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	hub := boundHub(t, serve.Options{Bindings: store})
	ctx := context.Background()
	for _, id := range []string{"session-a", "session-b"} {
		if _, _, err := hub.Open(ctx, "memory", openRequestFor(protocol.SessionID(id))); err != nil {
			t.Fatal(err)
		}
	}
	history, err := store.History(ctx, "session-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Record.SessionID != "session-a" {
		t.Fatalf("history for the first session = %+v", history)
	}
}

func boundHub(t *testing.T, options serve.Options) *serve.Hub {
	t.Helper()
	registry := serve.NewRegistry()
	if err := registry.Register("memory", base.NewMemory(base.Config{JournalCapacity: 8})); err != nil {
		t.Fatal(err)
	}
	options.StreamQueue = 8
	return serve.New(registry, options)
}

func openRequestFor(id protocol.SessionID) base.OpenRequest {
	return base.OpenRequest{SessionID: id, Metadata: map[string]any{"note": "a title"}}
}

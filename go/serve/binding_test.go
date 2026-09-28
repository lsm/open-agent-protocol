package serve_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestARolledBackOpenIsRecordedAsOpenedAndThenClosed(t *testing.T) {
	store, err := binding.File(filepath.Join(t.TempDir(), "bindings.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	hub := boundHub(t, serve.Options{Bindings: store})
	ctx := context.Background()
	if _, err := serve.OpenCompound(ctx, hub, "memory", base.OpenRequest{SessionID: "session-rolled-back"},
		serve.CompoundOpen{Message: &protocol.OpenMessage{}}); err == nil {
		t.Fatal("the compound open succeeded, so nothing was rolled back")
	}
	history, err := store.History(ctx, "session-rolled-back")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("history has %d entries, want the open and the close a rollback leaves", len(history))
	}
	if history[0].Action != binding.ActionOpened || history[1].Action != binding.ActionClosed {
		t.Fatalf("history = %+v, want opened then closed", history)
	}
	latest, found, err := store.Latest(ctx, "session-rolled-back")
	if err != nil || !found {
		t.Fatalf("latest found=%v err=%v", found, err)
	}
	if latest.Action != binding.ActionClosed {
		t.Fatalf("the last action is %q, so a host reading the binding sees a session that is still open", latest.Action)
	}
}

func TestAClosedSessionIsRecordedAsClosed(t *testing.T) {
	store, err := binding.File(filepath.Join(t.TempDir(), "bindings.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	hub := boundHub(t, serve.Options{Bindings: store})
	ctx := context.Background()
	session, _, err := hub.Open(ctx, "memory", base.OpenRequest{SessionID: "session-closed"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	history, err := store.History(ctx, "session-closed")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[1].Action != binding.ActionClosed {
		t.Fatalf("history = %+v, want the open and the close", history)
	}
	if history[1].TimeMS <= history[0].TimeMS {
		t.Fatalf("the close is stamped %d and the open %d, want the close later than the open it records", history[1].TimeMS, history[0].TimeMS)
	}
	if history[1].TimeMS < time.Now().Add(-time.Minute).UnixMilli() {
		t.Fatalf("the close is stamped %d, which is before the test ran, so it is not the close's own time", history[1].TimeMS)
	}
}

func TestARefusedDuplicateOpenIsRecordedAsOpenedAndThenRefused(t *testing.T) {
	store, err := binding.File(filepath.Join(t.TempDir(), "bindings.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	hub := boundHub(t, serve.Options{Bindings: store})
	ctx := context.Background()
	if _, _, err := hub.Open(ctx, "memory", base.OpenRequest{SessionID: "session-twice"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := hub.Open(ctx, "memory", base.OpenRequest{SessionID: "session-twice"}); err == nil {
		t.Fatal("the second open was admitted, so nothing was refused")
	}
	history, err := store.History(ctx, "session-twice")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("history has %d entries, want the first open, then the refused open's opened and refusal", len(history))
	}
	if history[2].Action != binding.ActionRefused {
		t.Fatalf("the last action is %q, want a refusal rather than a close: the first session is still running", history[2].Action)
	}
	if binding.Live(history[2]) {
		t.Fatal("a refusal reads as a live session, so a host would reopen one the hub already holds")
	}
	if history[0].Action != binding.ActionOpened {
		t.Fatalf("the first entry is %q, want the open that published the session", history[0].Action)
	}
	state, found := binding.State(history)
	if !found || state.Action != binding.ActionOpened {
		t.Fatalf("the last state of this session is %+v, want the open that is still running", state)
	}
	if !binding.Live(state) {
		t.Fatal("the running session does not read as live")
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

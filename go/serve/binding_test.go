package serve_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestTheRecordsDirectoryIsTheAdaptersOwnAndIsOmittedWhenUnknown(t *testing.T) {
	store, err := binding.File(filepath.Join(t.TempDir(), "bindings.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	configured := boundHub(t, serve.Options{Bindings: store})
	configured.SetWorkingDirectory("memory", "/work/repo")
	if _, _, err := configured.Open(ctx, "memory", base.OpenRequest{SessionID: "session-configured"}); err != nil {
		t.Fatal(err)
	}
	entry, found, err := store.Latest(ctx, "session-configured")
	if err != nil || !found {
		t.Fatalf("latest found=%v err=%v", found, err)
	}
	if entry.Record.Directory != "/work/repo" {
		t.Fatalf("the record names %q, want the directory the adapter was configured with", entry.Record.Directory)
	}
	plain := boundHub(t, serve.Options{Bindings: store})
	if _, _, err := plain.Open(ctx, "memory", base.OpenRequest{SessionID: "session-unknown-dir"}); err != nil {
		t.Fatal(err)
	}
	other, found, err := store.Latest(ctx, "session-unknown-dir")
	if err != nil || !found {
		t.Fatalf("latest found=%v err=%v", found, err)
	}
	if other.Record.Directory != "" {
		t.Fatalf("the record names %q, want nothing rather than the daemon's working directory", other.Record.Directory)
	}
}

func TestARefusedDuplicateOpenIsRecordedAsARefusalAndNothingElse(t *testing.T) {
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
	if len(history) != 2 {
		t.Fatalf("history has %d entries, want the open that published the session and one refusal: a duplicate that never ran is not an open", len(history))
	}
	if history[0].Action != binding.ActionOpened || history[1].Action != binding.ActionRefused {
		t.Fatalf("history = %+v, want opened then refused", history[:2])
	}
	if binding.Live(history[1]) {
		t.Fatal("a refusal reads as a live session, so a host would reopen one the hub already holds")
	}
	state, found := binding.State(history)
	if !found || state.Action != binding.ActionOpened || !binding.Live(state) {
		t.Fatalf("the last state of this session is %+v, want the open that is still running", state)
	}
}
func TestACloseAndADuplicateOpenCannotInterleaveTheirRecords(t *testing.T) {
	store, err := binding.File(filepath.Join(t.TempDir(), "bindings.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	hub := boundHub(t, serve.Options{Bindings: store})
	ctx := context.Background()
	if _, _, err := hub.Open(ctx, "memory", base.OpenRequest{SessionID: "session-race"}); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		_, _, _ = hub.Open(ctx, "memory", base.OpenRequest{SessionID: "session-race"})
	}()
	go func() {
		defer group.Done()
		session, err := hub.Session("session-race")
		if err != nil {
			t.Error(err)
			return
		}
		if err := session.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	group.Wait()
	history, err := store.History(ctx, "session-race")
	if err != nil {
		t.Fatal(err)
	}
	opens := 0
	closes := 0
	for _, entry := range history {
		switch entry.Action {
		case binding.ActionOpened:
			opens++
		case binding.ActionClosed:
			closes++
		}
	}
	if closes > opens {
		t.Fatalf("the file has %d closes and %d opens, so a close has no open to end", closes, opens)
	}
	if state, found := binding.State(history); found && state.Action == binding.ActionClosed && !binding.Live(state) {
		if _, stillOpen := hub.Session("session-race"); stillOpen == nil {
			t.Fatal("the file says closed and the hub says running, which is the interleaving this lock prevents")
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

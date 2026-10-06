package serve_test

import (
	"context"
	"errors"
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

type closedAtOpenAdapter struct {
	base.Adapter
}

type closedAtOpenSession struct {
	base.Session
}

func (a *closedAtOpenAdapter) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
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

func TestASessionThatSettlesAtOpenRecordsBothEndsInOneGo(t *testing.T) {
	store, err := binding.File(filepath.Join(t.TempDir(), "bindings.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	registry := serve.NewRegistry()
	if err := registry.Register("memory", base.NewMemory(base.Config{JournalCapacity: 8})); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{Bindings: store, StreamQueue: 8})
	ctx := context.Background()
	if _, _, err := hub.Open(ctx, "memory", base.OpenRequest{SessionID: "session-live"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("settling", &closedAtOpenAdapter{Adapter: base.NewMemory(base.Config{JournalCapacity: 8})}); err != nil {
		t.Fatal(err)
	}
	entry, _, err := hub.Open(ctx, "settling", base.OpenRequest{SessionID: "session-settled"})
	if err == nil {
		t.Fatal("the open of a session that settles at open was admitted, so nothing was recorded")
	}
	if err := entry.Close(ctx); err != nil {
		t.Fatalf("closing a session that settled at open: %v", err)
	}
	history, err := store.History(ctx, "session-settled")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("history has %d entries, want the open and the one close that ended it, with the release writing none of its own: %+v", len(history), history)
	}
	if history[0].Action != binding.ActionOpened || history[1].Action != binding.ActionClosed {
		t.Fatalf("history = %+v, want opened then closed", history[:2])
	}
	if state, found := binding.State(history); !found || state.Action != binding.ActionClosed {
		t.Fatalf("the last state is %+v, want the close", state)
	}
}

func TestAReopenIsRecordedAsReopenedAfterTheClose(t *testing.T) {
	store, err := binding.File(filepath.Join(t.TempDir(), "bindings.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	hub := boundHub(t, serve.Options{Bindings: store})
	ctx := context.Background()
	session, _, err := hub.Open(ctx, "memory", base.OpenRequest{SessionID: "session-reopened"})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	_, state, err := hub.Open(ctx, "memory", base.OpenRequest{SessionID: "session-reopened", Reopen: true})
	if err != nil {
		t.Fatal(err)
	}
	if state.Recovery == nil || !state.Recovery.Recovered {
		t.Fatalf("recovery = %+v, want the reopened state to declare itself recovered", state.Recovery)
	}
	history, err := store.History(ctx, "session-reopened")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 || history[2].Action != binding.ActionReopened {
		t.Fatalf("history = %+v, want the open, the close and the reopen", history)
	}
}

type noReopenAdapter struct {
	base.Adapter
}

func (a noReopenAdapter) Probe(ctx context.Context) (base.Descriptor, error) {
	descriptor, err := a.Adapter.Probe(ctx)
	if err != nil {
		return descriptor, err
	}
	features := map[string]protocol.FeatureSupport{}
	for key, support := range descriptor.Capabilities.Features {
		if key != protocol.FeatureOpenReopen {
			features[key] = support
		}
	}
	descriptor.Capabilities.Features = features
	return descriptor, nil
}

func TestTheElectionGateRefusesAReopenTheAdapterDoesNotAdvertise(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("plain", noReopenAdapter{Adapter: base.NewMemory(base.Config{JournalCapacity: 8})}); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{StreamQueue: 8})
	_, err := serve.ElectionGate(context.Background(), hub, "plain", "", protocol.SessionOpenRequest{SessionID: "s1", Reopen: true})
	var refusal *base.UnsupportedControlError
	if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureOpenReopen || refusal.Reason != base.ControlUnadvertised {
		t.Fatalf("gate answered %v, want an unadvertised refusal naming %s", err, protocol.FeatureOpenReopen)
	}
	if _, err := serve.ElectionGate(context.Background(), hub, "plain", "", protocol.SessionOpenRequest{SessionID: "s1"}); err != nil {
		t.Fatalf("an open electing nothing was refused: %v", err)
	}
}

func TestAReopenOfALiveSessionIsSessionExistsBeforeTheAdapterIsAsked(t *testing.T) {
	hub := boundHub(t, serve.Options{})
	ctx := context.Background()
	if _, _, err := hub.Open(ctx, "memory", base.OpenRequest{SessionID: "live"}); err != nil {
		t.Fatal(err)
	}
	_, _, err := hub.Open(ctx, "memory", base.OpenRequest{SessionID: "live", Reopen: true})
	if !errors.Is(err, serve.ErrSessionExists) {
		t.Fatalf("reopening a live session answered %v, want serve.ErrSessionExists rather than the adapter's unknown_session", err)
	}
}

type nativeAdapter struct {
	base.Adapter
	asked []base.OpenRequest
}

type nativeSession struct {
	base.Session
}

func (s nativeSession) NativeSessionID() string { return "native-thread-7" }

func (a *nativeAdapter) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	a.asked = append(a.asked, request)
	session, err := a.Adapter.Open(ctx, request)
	if err != nil {
		return nil, err
	}
	return nativeSession{Session: session}, nil
}

func TestAReopenHandsTheAdapterTheNativeIDItsBindingRecorded(t *testing.T) {
	store, err := binding.File(filepath.Join(t.TempDir(), "bindings.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &nativeAdapter{Adapter: base.NewMemory(base.Config{JournalCapacity: 8})}
	registry := serve.NewRegistry()
	if err := registry.Register("native", wrapped); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{StreamQueue: 8, Bindings: store})
	ctx := context.Background()
	session, _, err := hub.Open(ctx, "native", base.OpenRequest{SessionID: "bound"})
	if err != nil {
		t.Fatal(err)
	}
	latest, found, err := store.Latest(ctx, "bound")
	if err != nil || !found || latest.Record.NativeSessionID != "native-thread-7" {
		t.Fatalf("binding = %+v found=%v err=%v, want the session's native id recorded at open", latest, found, err)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := hub.Open(ctx, "native", base.OpenRequest{SessionID: "bound", Reopen: true}); err != nil {
		t.Fatal(err)
	}
	if asked := wrapped.asked[len(wrapped.asked)-1]; asked.NativeSessionID != "native-thread-7" {
		t.Fatalf("the reopen asked the adapter with native id %q, want the bound one", asked.NativeSessionID)
	}
	before := len(wrapped.asked)
	if _, _, err := hub.Open(ctx, "native", base.OpenRequest{SessionID: "unbound", Reopen: true}); !errors.Is(err, serve.ErrUnknownSession) {
		t.Fatalf("a reopen with no binding answered %v, want serve.ErrUnknownSession", err)
	}
	if len(wrapped.asked) != before {
		t.Fatal("a reopen with no binding reached the adapter; the code must come from the binding")
	}
}

func TestAReopenAfterARestartTheAdapterCannotLoadIsUnsupportedFeature(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.jsonl")
	ctx := context.Background()
	open := func() *serve.Hub {
		store, err := binding.File(path)
		if err != nil {
			t.Fatal(err)
		}
		registry := serve.NewRegistry()
		if err := registry.Register("memory", base.NewMemory(base.Config{JournalCapacity: 8})); err != nil {
			t.Fatal(err)
		}
		return serve.New(registry, serve.Options{StreamQueue: 8, Bindings: store})
	}
	session, _, err := open().Open(ctx, "memory", base.OpenRequest{SessionID: "kept"})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, err = open().Open(ctx, "memory", base.OpenRequest{SessionID: "kept", Reopen: true})
	var refusal *base.UnsupportedControlError
	if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureOpenReopen || refusal.Reason != base.ControlUnsatisfiable {
		t.Fatalf("reopening a bound session the restarted adapter lost answered %v, want unsupported_feature naming %s", err, protocol.FeatureOpenReopen)
	}
}

func TestAnOpenRecordsTheSettingsItAskedFor(t *testing.T) {
	store, err := binding.File(filepath.Join(t.TempDir(), "bindings.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	hub := boundHub(t, serve.Options{Bindings: store})
	ctx := context.Background()
	request := openRequestFor("session-settings")
	request.ReasoningLevel = "high"
	request.CompactionPolicy = &protocol.CompactionPolicy{Kind: protocol.CompactionShare, SharePercent: 70}
	if _, _, err := hub.Open(ctx, "memory", request); err != nil {
		t.Fatal(err)
	}
	entry, found, err := store.Latest(ctx, "session-settings")
	if err != nil || !found {
		t.Fatalf("latest found=%v err=%v", found, err)
	}
	if entry.Record.ReasoningLevel != "high" || entry.Record.CompactionPolicy == nil || *entry.Record.CompactionPolicy != *request.CompactionPolicy {
		t.Fatalf("record = %+v, want the reasoning level and compaction policy the open asked for", entry.Record)
	}
}

func TestAShutdownLeavesItsSessionsLiveInTheHistoryAndAClientCloseDoesNot(t *testing.T) {
	store, err := binding.File(filepath.Join(t.TempDir(), "bindings.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	hub := boundHub(t, serve.Options{Bindings: store})
	ctx := context.Background()
	if _, _, err := hub.Open(ctx, "memory", openRequestFor("kept")); err != nil {
		t.Fatal(err)
	}
	dropped, _, err := hub.Open(ctx, "memory", openRequestFor("dropped"))
	if err != nil {
		t.Fatal(err)
	}
	if err := dropped.Close(ctx); err != nil {
		t.Fatal(err)
	}
	hub.CloseSessions(ctx)
	for id, want := range map[string]binding.Action{"kept": binding.ActionOpened, "dropped": binding.ActionClosed} {
		entry, found, err := store.Latest(ctx, id)
		if err != nil || !found {
			t.Fatalf("%s: found=%v err=%v", id, found, err)
		}
		if entry.Action != want {
			t.Fatalf("%s: action = %q, want %q", id, entry.Action, want)
		}
	}
}

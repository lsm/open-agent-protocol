package workwire

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/binding"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

type scripted struct {
	mu         sync.Mutex
	opened     []base.OpenRequest
	sessions   []*scriptedSession
	features   map[string]protocol.FeatureSupport
	revision   string
	transcript map[string][]base.NativeTurn
	readFails  bool
}

type scriptedSession struct {
	id      protocol.SessionID
	native  string
	mu      sync.Mutex
	status  protocol.SessionStatus
	run     protocol.RunID
	stream  chan base.Result
	runs    int
	updated int64
}

func (a *scripted) Probe(context.Context) (base.Descriptor, error) {
	revision := "scripted-v1"
	if a.revision == "-" {
		revision = ""
	}
	return base.Descriptor{Capabilities: protocol.CapabilityDescriptor{Endpoint: protocol.EndpointDescriptor{ID: "scripted", Name: "Scripted", Version: "1"}, Features: a.features}, CapabilityRevision: revision}, nil
}

func (a *scripted) Open(_ context.Context, request base.OpenRequest) (base.Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.opened = append(a.opened, request)
	id := request.SessionID
	if id == "" {
		id = protocol.SessionID("s" + string(rune('1'+len(a.sessions))))
	}
	session := &scriptedSession{id: id, native: request.NativeSessionID, status: protocol.SessionIdle, updated: 50}
	if session.native == "" {
		session.native = "native-" + string(id)
	}
	a.sessions = append(a.sessions, session)
	return session, nil
}

func (a *scripted) NativeRead(_ context.Context, request base.NativeReadRequest) ([]base.NativeTurn, error) {
	if a.readFails {
		return nil, context.DeadlineExceeded
	}
	return a.transcript[request.NativeID], nil
}

func (s *scriptedSession) NativeSessionID() string { return s.native }

func (s *scriptedSession) Submit(_ context.Context, request base.SubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs++
	s.run = protocol.RunID("run-" + string(rune('0'+s.runs)))
	s.status = protocol.SessionRunning
	s.stream = make(chan base.Result, 16)
	return protocol.MessageSubmitResponse{SessionID: s.id, Accepted: true, SubmissionID: "sub", RequestedDelivery: protocol.DeliveryAuto, EffectiveDelivery: protocol.DeliveryStart, Admission: protocol.AdmissionStarted, RunID: s.run, Status: protocol.RunRunning}, s.stream, nil
}

func (s *scriptedSession) State(context.Context) (protocol.SessionState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := protocol.SessionState{SessionID: s.id, Status: s.status, UpdatedAtMS: s.updated}
	if s.status == protocol.SessionRunning {
		state.ActiveRunID = s.run
	}
	return state, nil
}

func (s *scriptedSession) emit(t *testing.T, typ protocol.EnvelopeType, payload any, settle bool) {
	t.Helper()
	s.mu.Lock()
	envelope, err := protocol.NewEnvelope(typ, protocol.EnvelopeID("e-"+string(typ)), payload)
	if err != nil {
		t.Fatal(err)
	}
	envelope.SessionID, envelope.RunID = s.id, s.run
	stream := s.stream
	s.mu.Unlock()
	stream <- base.Result{Envelope: envelope}
	if settle {
		close(stream)
	}
}

func (s *scriptedSession) idle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = protocol.SessionIdle
}

func terminate(t *testing.T, front *Front, session *scriptedSession, typ protocol.EnvelopeType, payload any) {
	t.Helper()
	before := len(encoded(t)(front.Read(context.Background(), string(session.id), nil, nil))["turns"].([]any))
	session.emit(t, typ, payload, true)
	deadline := time.Now().Add(5 * time.Second)
	for len(encoded(t)(front.Read(context.Background(), string(session.id), nil, nil))["turns"].([]any)) == before {
		if time.Now().After(deadline) {
			t.Fatalf("the hub never recorded %s", typ)
		}
		time.Sleep(2 * time.Millisecond)
	}
	session.idle()
}

func (s *scriptedSession) Resolve(context.Context, base.InteractionResolution) error { return nil }

func (s *scriptedSession) Cancel(context.Context, protocol.RunID) (protocol.RunCancelResponse, error) {
	return protocol.RunCancelResponse{}, nil
}

func (s *scriptedSession) Resume(context.Context, base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	return base.Recovery{}, nil, base.ErrUnsupportedInput
}

func (s *scriptedSession) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = protocol.SessionClosed
	return nil
}

func harness(t *testing.T, store binding.Store) (*Front, *scripted) {
	t.Helper()
	adapter := &scripted{transcript: map[string][]base.NativeTurn{}}
	registry := serve.NewRegistry()
	if err := registry.Register("scripted", adapter); err != nil {
		t.Fatal(err)
	}
	registry.SetWorkingDirectory("scripted", "/work/a")
	return New(serve.New(registry, serve.Options{Bindings: store})), adapter
}

func encoded(t *testing.T) func(any, *Refusal) map[string]any {
	return func(answer any, refusal *Refusal) map[string]any { return decode(t, answer, refusal) }
}

func decode(t *testing.T, answer any, refusal *Refusal) map[string]any {
	t.Helper()
	if refusal != nil {
		t.Fatalf("refused %s: %s", refusal.Code, refusal.Message)
	}
	raw, err := json.Marshal(answer)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func settle(t *testing.T, front *Front, id string, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status := encoded(t)(front.Status(context.Background(), id))
		if status["status"] == want || time.Now().After(deadline) {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func start(t *testing.T, front *Front, message string) (string, *scriptedSession, *scripted) {
	t.Helper()
	started := encoded(t)(front.Start(context.Background(), "scripted", json.RawMessage(`{"message":"`+message+`","title":"named"}`)))
	id := started["ref"].(map[string]any)["session_id"].(string)
	adapter := front.hub.Registry()
	implementation, _ := adapter.Lookup("scripted")
	scriptedAdapter := implementation.(*scripted)
	return id, scriptedAdapter.sessions[len(scriptedAdapter.sessions)-1], scriptedAdapter
}

func TestWorkStatusProjectsRunningThenEachTerminalAndKeepsTheReplyOfADoneRun(t *testing.T) {
	cases := []struct {
		terminal protocol.EnvelopeType
		payload  any
		status   string
		reply    any
	}{
		{protocol.TypeRunCompleted, map[string]any{"session_id": "s1", "run_id": "run-1", "final_response": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "po"}, map[string]any{"type": "text", "text": "ng"}}}}, "done", "pong"},
		{protocol.TypeRunFailed, map[string]any{"session_id": "s1", "run_id": "run-1"}, "failed", nil},
		{protocol.TypeRunCancelled, map[string]any{"session_id": "s1", "run_id": "run-1"}, "stopped", nil},
	}
	for _, each := range cases {
		front, _ := harness(t, nil)
		id, session, _ := start(t, front, "hi")
		running := encoded(t)(front.Status(context.Background(), id))
		if running["status"] != "running" || running["run_id"] != "run-1" || running["title"] != "named" || running["directory"] != "/work/a" {
			t.Fatalf("a started run answered %v", running)
		}
		terminate(t, front, session, each.terminal, each.payload)
		settled := settle(t, front, id, each.status)
		if settled["status"] != each.status || settled["last_reply"] != each.reply || settled["run_id"] != "run-1" {
			t.Fatalf("%s answered %v, want %s with reply %v", each.terminal, settled, each.status, each.reply)
		}
	}
}

func TestWorkStatusReadsNeedsYouFromTheActiveRunsLatestStatusUpdateWithOrWithoutAnInteraction(t *testing.T) {
	front, _ := harness(t, nil)
	id, session, _ := start(t, front, "hi")
	session.emit(t, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: session.id, RunID: session.run, Status: protocol.RunWaitingForInput, PendingUserInputID: "gate-1"}, false)
	waiting := settle(t, front, id, "needs_you")
	if waiting["status"] != "needs_you" || waiting["pending"].(map[string]any)["interaction_id"] != "gate-1" {
		t.Fatalf("a named gate answered %v", waiting)
	}
	session.emit(t, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: session.id, RunID: session.run, Status: protocol.RunRunning}, false)
	if resumed := settle(t, front, id, "running"); resumed["status"] != "running" || resumed["pending"] != nil {
		t.Fatalf("a resumed run answered %v", resumed)
	}
	session.emit(t, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: session.id, RunID: session.run, Status: protocol.RunWaitingForInput}, false)
	if unnamed := settle(t, front, id, "needs_you"); unnamed["status"] != "needs_you" || unnamed["pending"] != nil {
		t.Fatalf("an unnamed wait answered %v", unnamed)
	}
}

func TestWorkReadAnswersTheRecordedTurnsPagedAndTheHarnessTranscriptWhenTheAdapterHasOne(t *testing.T) {
	front, adapter := harness(t, nil)
	id, session, _ := start(t, front, "first")
	terminate(t, front, session, protocol.TypeRunCompleted, map[string]any{"session_id": string(session.id), "run_id": "run-1", "final_response": map[string]any{"role": "assistant", "content": "one"}})
	read := encoded(t)(front.Read(context.Background(), id, nil, nil))["turns"].([]any)
	if len(read) != 2 || read[0].(map[string]any)["text"] != "first" || read[1].(map[string]any)["outcome"] != "completed" || read[1].(map[string]any)["text"] != "one" {
		t.Fatalf("recorded turns read %v", read)
	}
	after, limit := uint64(0), int64(1)
	paged := encoded(t)(front.Read(context.Background(), id, &after, &limit))["turns"].([]any)
	if len(paged) != 1 || paged[0].(map[string]any)["index"] != float64(1) {
		t.Fatalf("after 0 limit 1 read %v", paged)
	}
	adapter.transcript[session.native] = []base.NativeTurn{{Role: "user", Text: "before serve", AtMS: 1}, {Role: "assistant", Text: "kept", AtMS: 2}}
	native := encoded(t)(front.Read(context.Background(), id, nil, nil))["turns"].([]any)
	if len(native) != 2 || native[0].(map[string]any)["text"] != "before serve" || native[1].(map[string]any)["run_id"] != nil {
		t.Fatalf("a readable transcript read %v", native)
	}
	adapter.readFails = true
	if fallback := encoded(t)(front.Read(context.Background(), id, nil, nil))["turns"].([]any); len(fallback) != 2 || fallback[0].(map[string]any)["text"] != "first" {
		t.Fatalf("an unreadable transcript read %v, want serve's own turns", fallback)
	}
	zero := int64(0)
	if _, refusal := front.Read(context.Background(), id, nil, &zero); refusal == nil || refusal.Message != "work.read: limit must be from 1 to 500" {
		t.Fatalf("limit 0 answered %v", refusal)
	}
}

func TestWorkListGroupsHeldAndUnheldSessionsAndShowsClosedOnesOnlyOnRequest(t *testing.T) {
	store := binding.Memory()
	ctx := context.Background()
	if err := store.Append(ctx, binding.Entry{Action: binding.ActionOpened, TimeMS: 10, Record: binding.Record{SessionID: "left", Adapter: "scripted", Directory: "/work/b"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, binding.Entry{Action: binding.ActionClosed, TimeMS: 20, Record: binding.Record{SessionID: "ended", Adapter: "scripted", Directory: "/work/b"}}); err != nil {
		t.Fatal(err)
	}
	front, _ := harness(t, store)
	start(t, front, "hi")
	groups := encoded(t)(front.List(ctx, false, false))["groups"].([]any)
	if len(groups) != 2 || groups[0].(map[string]any)["directory"] != "/work/a" {
		t.Fatalf("listed %v", groups)
	}
	left := groups[1].(map[string]any)["work"].([]any)
	if len(left) != 1 || left[0].(map[string]any)["held"] != false || left[0].(map[string]any)["state"] != "live" {
		t.Fatalf("the unheld group listed %v", left)
	}
	all := encoded(t)(front.List(ctx, true, false))["groups"].([]any)[1].(map[string]any)["work"].([]any)
	if len(all) != 2 || all[0].(map[string]any)["state"] != "closed" {
		t.Fatalf("include_closed listed %v", all)
	}
	unheld := encoded(t)(front.Status(ctx, "left"))
	if unheld["held"] != false || unheld["state"] != "live" {
		t.Fatalf("an unheld status answered %v", unheld)
	}
}

func TestWorkCapabilitiesLeavesOutAVerbWhoseFeatureTheAdapterDeclaresUnavailableOrDoesNotDeclare(t *testing.T) {
	front, adapter := harness(t, nil)
	adapter.features = map[string]protocol.FeatureSupport{"run.cancel": {Level: protocol.SupportUnavailable}, "session.message.submit": {Level: protocol.SupportNative}}
	reach := encoded(t)(front.Capabilities(context.Background()))["adapters"].([]any)[0].(map[string]any)
	verbs := reach["verbs"].([]any)
	if len(verbs) != 5 {
		t.Fatalf("verbs %v, want work.stop left out", verbs)
	}
	for _, verb := range verbs {
		if verb == "work.stop" {
			t.Fatalf("verbs %v kept work.stop", verbs)
		}
	}
	if reach["native"].(map[string]any)["read"] != true || reach["native"].(map[string]any)["list"] != false || reach["directory"] != "/work/a" {
		t.Fatalf("reach %v", reach)
	}
	adapter.features = map[string]protocol.FeatureSupport{"session.message.submit": {Level: protocol.SupportNative}}
	undeclared := encoded(t)(front.Capabilities(context.Background()))["adapters"].([]any)[0].(map[string]any)["verbs"].([]any)
	if len(undeclared) != 5 {
		t.Fatalf("verbs %v, want work.stop left out when run.cancel is undeclared", undeclared)
	}
	adapter.revision = "-"
	unbound := encoded(t)(front.Capabilities(context.Background()))
	if len(unbound["adapters"].([]any)) != 0 || unbound["unavailable"].([]any)[0].(map[string]any)["message"] != "adapter descriptor carries no capability revision" {
		t.Fatalf("an unbound descriptor answered %v", unbound)
	}
}

func TestWorkStartWithANativeIdAdoptsItAndAReopenFromItsBindingIsAdoptedToo(t *testing.T) {
	store := binding.Memory()
	ctx := context.Background()
	front, adapter := harness(t, store)
	adopted := encoded(t)(front.Start(ctx, "scripted", json.RawMessage(`{"native_id":"thread-x","message":"continue"}`)))
	id := adopted["ref"].(map[string]any)["session_id"].(string)
	if first := adapter.opened[0]; !first.Adopted || !first.Reopen || first.NativeSessionID != "thread-x" {
		t.Fatalf("the adopting open was %+v", first)
	}
	latest, _, err := store.Latest(ctx, id)
	if err != nil || !latest.Record.Adopted {
		t.Fatalf("the binding recorded %+v, %v", latest, err)
	}
	again := encoded(t)(front.Start(ctx, "scripted", json.RawMessage(`{"native_id":"thread-x","message":"more"}`)))
	if again["ref"].(map[string]any)["session_id"] != id || len(adapter.opened) != 1 {
		t.Fatalf("a second adoption answered %v after %d opens", again, len(adapter.opened))
	}
}

type listingScripted struct {
	*scripted
	listErr error
	running bool
}

func (l *listingScripted) NativeList(context.Context, base.NativeListRequest) ([]base.NativeListing, error) {
	if l.listErr != nil {
		return nil, l.listErr
	}
	return []base.NativeListing{{NativeID: "thread-x", Running: l.running}}, nil
}

func TestWorkStartRefusesToAdoptASessionItCannotTellIsNotRunning(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		adapter *listingScripted
		code    string
	}{
		{"the list failed", &listingScripted{scripted: &scripted{transcript: map[string][]base.NativeTurn{}}, listErr: errors.New("app-server gone")}, "backend_failed"},
		{"the harness runs it", &listingScripted{scripted: &scripted{transcript: map[string][]base.NativeTurn{}}, running: true}, "run_active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := serve.NewRegistry()
			if err := registry.Register("scripted", tc.adapter); err != nil {
				t.Fatal(err)
			}
			registry.SetWorkingDirectory("scripted", "/work/a")
			front := New(serve.New(registry, serve.Options{Bindings: binding.Memory()}))
			_, refusal := front.Start(ctx, "scripted", json.RawMessage(`{"native_id":"thread-x","message":"continue"}`))
			if refusal == nil || refusal.Code != tc.code {
				t.Fatalf("refusal = %+v, want %s", refusal, tc.code)
			}
			if len(tc.adapter.opened) != 0 {
				t.Fatalf("the adapter was opened %d times", len(tc.adapter.opened))
			}
		})
	}
}

func TestWorkRefusesAClosedOrUnknownSessionInTheWordsZigUses(t *testing.T) {
	front := &Front{}
	for _, c := range []struct {
		err     error
		code    string
		message string
	}{
		{base.ErrSessionClosed, "session_closed", `the session "s1" is closed`},
		{base.ErrUnknownSession, "unknown_session", `no session "s1"`},
	} {
		refusal := front.stateRefusal(c.err, "s1")
		if refusal.Code != c.code || refusal.Message != c.message {
			t.Fatalf("%v refused %s %q, want %s %q", c.err, refusal.Code, refusal.Message, c.code, c.message)
		}
	}
}

package servehttp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

func TestSSEEnvelopeOrderAndSequences(t *testing.T) {
	server := newMemoryServer(t, 0)
	openSession(t, server, "memory", "sse-order")
	_, envelopes := goldenRun(t, server, "sse-order", "submit-sse-order")
	want := []protocol.EnvelopeType{
		protocol.TypeRunStarted,
		protocol.TypeContentDelta,
		protocol.TypeActionCallRequested,
		protocol.TypeActionPermissionRequested,
		protocol.TypeActionPermissionResolved,
		protocol.TypeActionCallStarted,
		protocol.TypeActionCallCompleted,
		protocol.TypeUserInputRequested,
		protocol.TypeRunStatusUpdated,
		protocol.TypeUserInputResolved,
		protocol.TypeContentDelta,
		protocol.TypeRunCompleted,
	}
	if len(envelopes) != len(want) {
		t.Fatalf("got %d envelopes, want %d", len(envelopes), len(want))
	}
	for index, typ := range want {
		if envelopes[index].Type != typ {
			t.Fatalf("envelope %d type %s, want %s", index, envelopes[index].Type, typ)
		}
		if envelopes[index].SessionID != "sse-order" {
			t.Fatalf("envelope %d session scope: %q", index, envelopes[index].SessionID)
		}
	}
}

func TestSSEReplayAfterCursor(t *testing.T) {
	server := newMemoryServer(t, 0)
	openSession(t, server, "memory", "sse-replay")
	runID, _ := goldenRun(t, server, "sse-replay", "submit-sse-replay")

	stream := connectSSE(t, server, "/sessions/sse-replay/events?after=9", "")
	replayed := stream.drainUntil(protocol.TypeRunCompleted)
	requireSequence(t, replayed, 10)
	if len(replayed) != 3 {
		t.Fatalf("replayed %d envelopes, want 3", len(replayed))
	}
	for _, envelope := range replayed {
		if envelope.RunID != runID {
			t.Fatalf("replayed envelope run %q, want %q", envelope.RunID, runID)
		}
	}
}

func TestSSELastEventIDReconnect(t *testing.T) {
	server := newMemoryServer(t, 0)
	openSession(t, server, "memory", "sse-last-event")
	goldenRun(t, server, "sse-last-event", "submit-sse-last-event")

	stream := connectSSE(t, server, "/sessions/sse-last-event/events", "9")
	replayed := stream.drainUntil(protocol.TypeRunCompleted)
	requireSequence(t, replayed, 10)
	if len(replayed) != 3 {
		t.Fatalf("replayed %d envelopes, want 3", len(replayed))
	}
}

func TestSSELastEventIDReadsARunQualifiedID(t *testing.T) {
	server := newMemoryServer(t, 0)
	openSession(t, server, "memory", "sse-qualified")
	first := driveOneRun(t, server, "sse-qualified", "one", false)
	driveOneRun(t, server, "sse-qualified", "two", true)

	stream := connectSSE(t, server, "/sessions/sse-qualified/events", fmt.Sprintf("%s:9", first))
	replayed := stream.drainUntil(protocol.TypeRunCompleted)
	requireSequence(t, replayed, 10)
	if replayed[0].RunID != first {
		t.Fatalf("resumed on %s, want %s", replayed[0].RunID, first)
	}

	stale := connectSSE(t, server, "/sessions/sse-qualified/events?run_id=elsewhere", fmt.Sprintf("%s:9", first))
	if envelope := stale.envelope(); envelope.RunID != first {
		t.Fatalf("a stale run_id moved the cursor to %s, want %s", envelope.RunID, first)
	}
}

func TestSSERefusesARunQualifiedCursorItCannotRead(t *testing.T) {
	server := newMemoryServer(t, 0)
	openSession(t, server, "memory", "sse-bad-id")
	goldenRun(t, server, "sse-bad-id", "submit-sse-bad-id")
	for _, id := range []string{":1", "run:x"} {
		request, err := http.NewRequest(http.MethodGet, server.URL+"/sessions/sse-bad-id/events", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Last-Event-ID", id)
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("id %q answered %d", id, response.StatusCode)
		}
	}
	response, err := server.Client().Get(server.URL + "/sessions/sse-bad-id/events?after=run:1")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("a run-qualified after answered %d", response.StatusCode)
	}
}

func TestAnEventIDNamesItsRunOnlyWhenItHasOne(t *testing.T) {
	if id, writable := eventID("run-1", 3); id != "run-1:3" || !writable {
		t.Fatalf("id %q, %v", id, writable)
	}
	if id, writable := eventID("", 3); id != "3" || !writable {
		t.Fatalf("id %q, %v", id, writable)
	}
	for _, run := range []protocol.RunID{"a\ndata: x", "a\rb", "a\x00b"} {
		if id, writable := eventID(run, 3); writable {
			t.Fatalf("run %q wrote id %q", run, id)
		}
	}
}

func TestSSEQueryCursorWinsOverHeader(t *testing.T) {
	server := newMemoryServer(t, 0)
	openSession(t, server, "memory", "sse-cursor-precedence")
	goldenRun(t, server, "sse-cursor-precedence", "submit-sse-precedence")

	request, err := http.NewRequest(http.MethodGet, server.URL+"/sessions/sse-cursor-precedence/events?after=11", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Last-Event-ID", "4")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	stream := &sseStream{t: t, response: response, events: make(chan sseEvent, 128)}
	t.Cleanup(stream.close)
	go stream.scan()
	replayed := stream.drainUntil(protocol.TypeRunCompleted)
	requireSequence(t, replayed, 12)
	if len(replayed) != 1 {
		t.Fatalf("replayed %d envelopes, want 1", len(replayed))
	}
}

func TestSSEReplayGap(t *testing.T) {

	server := newMemoryServer(t, 2)
	openSession(t, server, "memory", "sse-gap")
	goldenRun(t, server, "sse-gap", "submit-sse-gap")

	stream := connectSSE(t, server, "/sessions/sse-gap/events?after=1", "")
	payload := stream.signal(sseEventReplayGap)
	if payload["requested_after"] != float64(1) || payload["oldest_available"] != float64(11) || payload["latest_available"] != float64(12) {
		t.Fatalf("gap signal: %v", payload)
	}

	stream = connectSSE(t, server, "/sessions/sse-gap/events?after=10", "")
	replayed := stream.drainUntil(protocol.TypeRunCompleted)
	requireSequence(t, replayed, 11)
	if len(replayed) != 2 {
		t.Fatalf("retained replay %d envelopes, want 2", len(replayed))
	}
}

func TestSSECursorErrors(t *testing.T) {
	server := newMemoryServer(t, 0)
	openSession(t, server, "memory", "sse-cursor-errors")
	goldenRun(t, server, "sse-cursor-errors", "submit-sse-cursor")

	response, err := server.Client().Get(server.URL + "/sessions/sse-cursor-errors/events?after=soon")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid cursor status %d", response.StatusCode)
	}
	if want := "application/json"; response.Header.Get("Content-Type") != want {
		t.Fatalf("invalid cursor content type %q", response.Header.Get("Content-Type"))
	}
	response.Body.Close()

	response, err = server.Client().Get(server.URL + "/sessions/sse-cursor-errors/events?after=99")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("future cursor status %d", response.StatusCode)
	}
	response.Body.Close()
}

func TestSSENoRunToResume(t *testing.T) {
	server := newMemoryServer(t, 0)
	openSession(t, server, "memory", "sse-no-run")

	response, err := server.Client().Get(server.URL + "/sessions/sse-no-run/events?after=0")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("no-run status %d", response.StatusCode)
	}
}

func TestSSEUnknownSession(t *testing.T) {
	server := newMemoryServer(t, 0)
	response, err := server.Client().Get(server.URL + "/sessions/ghost/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session status %d", response.StatusCode)
	}
}

func TestSSELiveSubscriptionMidRun(t *testing.T) {
	server := newMemoryServer(t, 0)
	openSession(t, server, "memory", "sse-mid-run")
	stream := connectSSE(t, server, "/sessions/sse-mid-run/events", "")
	_, admission := submitRun(t, server, "sse-mid-run", "submit-mid-run")
	initial := stream.drainUntil(protocol.TypeActionPermissionRequested)

	late := connectSSE(t, server, "/sessions/sse-mid-run/events", "")
	joined := late.signal(sseEventSubscribed)
	if joined["run_id"] != string(admission.RunID) {
		t.Fatalf("the late subscriber was told it joined run %v, want %s", joined["run_id"], admission.RunID)
	}
	if joined["joined_after"] != float64(*initial[len(initial)-1].Sequence) {
		t.Fatalf("the late subscriber was told it joined after %v, want %d", joined["joined_after"], *initial[len(initial)-1].Sequence)
	}
	resolvePermission(t, server, permissionRequestAt(t, initial), "resolve-mid-1")
	middle := stream.drainUntil(protocol.TypeRunStatusUpdated)
	lateSeen := late.drainUntil(protocol.TypeRunStatusUpdated)
	if len(lateSeen) == 0 {
		t.Fatal("late subscriber observed no live events")
	}
	for _, envelope := range lateSeen {
		if envelope.Sequence == nil || *envelope.Sequence <= *middle[0].Sequence-1 {
			t.Fatalf("late subscriber saw sequence %v", envelope.Sequence)
		}
	}
	resolveInput(t, server, inputRequestAt(t, middle), "resolve-mid-2")
	stream.drainUntil(protocol.TypeRunCompleted)
	late.drainUntil(protocol.TypeRunCompleted)
}

func TestSSEStreamEndsOnSessionClose(t *testing.T) {
	server := newMemoryServer(t, 0)
	openSession(t, server, "memory", "sse-close")

	stream := connectSSE(t, server, "/sessions/sse-close/events", "")
	response, data := post(t, server, "/sessions/sse-close/close", "", nil)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("close status %d: %s", response.StatusCode, data)
	}
	stream.expectEnd()
}

func TestSSEHangupOnAnIdleStreamEndsItsHandler(t *testing.T) {
	daemon, err := New(serve.New(memoryRegistry(0), serve.Options{}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	handler := daemon.Handler()
	ended := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		if strings.HasSuffix(r.URL.Path, "/events") {
			close(ended)
		}
	}))
	t.Cleanup(server.Close)
	openSession(t, server, "memory", "sse-hangup")

	stream := connectSSE(t, server, "/sessions/sse-hangup/events", "")
	stream.close()
	select {
	case <-ended:
	case <-time.After(testTimeout):
		t.Fatal("the handler outlived its client on an idle stream")
	}
}

func TestSSEWritesAKeepaliveOnAQuietStream(t *testing.T) {
	_, server := newServer(t, memoryRegistry(0), Options{Keepalive: 20 * time.Millisecond})
	openSession(t, server, "memory", "sse-quiet")

	response, err := server.Client().Get(server.URL + "/sessions/sse-quiet/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	for range 2 {
		comment, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if comment != ": keepalive\n" {
			t.Fatalf("line %q, want the keepalive comment", comment)
		}
		if blank, err := reader.ReadString('\n'); err != nil || blank != "\n" {
			t.Fatalf("line %q, %v, want the end of the comment", blank, err)
		}
	}
}

func TestAKeepaliveDefaultsToFifteenSeconds(t *testing.T) {
	daemon, err := New(serve.New(memoryRegistry(0), serve.Options{}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if daemon.keepalive != 15*time.Second {
		t.Fatalf("keepalive %s", daemon.keepalive)
	}
}

func TestSSEOverflowSignalLive(t *testing.T) {

	_, server := newServer(t, fakeRegistry(64, 2, 0), Options{})
	openSession(t, server, "fake", "sse-overflow-live")
	stream := connectSSE(t, server, "/sessions/sse-overflow-live/events", "")
	submitRun(t, server, "sse-overflow-live", "submit-overflow-live")

	first := stream.envelope()
	if first.Type != protocol.TypeRunStarted || first.Sequence == nil || *first.Sequence != 1 {
		t.Fatalf("first envelope: %s sequence %v", first.Type, first.Sequence)
	}
	payload := stream.signal(sseEventOverflow)
	if payload["last_sequence"] != float64(1) {
		t.Fatalf("overflow signal: %v", payload)
	}
	stream.expectEnd()
}

func TestSSEOverflowSignalReplay(t *testing.T) {
	_, server := newServer(t, fakeRegistry(64, 0, 2), Options{})
	openSession(t, server, "fake", "sse-overflow-replay")
	goldenRun(t, server, "sse-overflow-replay", "submit-overflow-replay")

	stream := connectSSE(t, server, "/sessions/sse-overflow-replay/events?after=9", "")
	first := stream.envelope()
	if first.Sequence == nil || *first.Sequence != 10 {
		t.Fatalf("replayed envelope sequence %v", first.Sequence)
	}
	payload := stream.signal(sseEventOverflow)
	if payload["last_sequence"] != float64(10) {
		t.Fatalf("overflow signal: %v", payload)
	}
}

type fakeAdapter struct {
	memory           base.Adapter
	liveOverflowAt   int
	replayOverflowAt int
}

func newFakeAdapter(capacity, liveOverflowAt, replayOverflowAt int) *fakeAdapter {
	return &fakeAdapter{
		memory:         base.NewMemory(base.Config{JournalCapacity: capacity}),
		liveOverflowAt: liveOverflowAt, replayOverflowAt: replayOverflowAt,
	}
}

func (f *fakeAdapter) Probe(ctx context.Context) (base.Descriptor, error) {
	return f.memory.Probe(ctx)
}

func (f *fakeAdapter) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	session, err := f.memory.Open(ctx, request)
	if err != nil {
		return nil, err
	}
	return &fakeSession{Session: session, liveOverflowAt: f.liveOverflowAt, replayOverflowAt: f.replayOverflowAt}, nil
}

type fakeSession struct {
	base.Session
	liveOverflowAt   int
	replayOverflowAt int
}

func (f *fakeSession) Submit(ctx context.Context, submit base.SubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	admission, stream, err := f.Session.Submit(ctx, submit)
	if err != nil {
		return admission, stream, err
	}
	return admission, inject(stream, f.liveOverflowAt), nil
}

func (f *fakeSession) Resume(ctx context.Context, request base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	recovery, stream, err := f.Session.Resume(ctx, request)
	if err != nil {
		return recovery, stream, err
	}
	return recovery, inject(stream, f.replayOverflowAt), nil
}

func inject(stream base.EventStream, overflowAt int) base.EventStream {
	out := make(chan base.Result, 32)
	go func() {
		defer close(out)
		index := 0
		for result := range stream {
			index++
			if overflowAt != 0 && index == overflowAt {
				out <- base.Result{Error: base.ErrEventStreamOverflow}
			}
			out <- result
		}
	}()
	return out
}

func fakeRegistry(capacity, liveOverflowAt, replayOverflowAt int) *serve.Registry {
	registry := serve.NewRegistry()
	if err := registry.Register("fake", newFakeAdapter(capacity, liveOverflowAt, replayOverflowAt)); err != nil {
		panic(err)
	}
	return registry
}

func TestSSEOnClosedSession(t *testing.T) {

	server := newMemoryServer(t, 0)
	openSession(t, server, "memory", "sse-closed")
	response, data := post(t, server, "/sessions/sse-closed/close", "", nil)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("close status %d: %s", response.StatusCode, data)
	}

	events, err := server.Client().Get(server.URL + "/sessions/sse-closed/events")
	if err != nil {
		t.Fatal(err)
	}
	closed, _ := io.ReadAll(events.Body)
	events.Body.Close()
	if events.StatusCode != http.StatusNotFound {
		t.Fatalf("closed live stream status %d: %s", events.StatusCode, closed)
	}
	envelope, err := protocol.ParseEnvelope(closed)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorResponse(t, events.StatusCode, http.StatusNotFound, envelope, "unknown_session")

	cursor, err := server.Client().Get(server.URL + "/sessions/sse-closed/events?after=0")
	if err != nil {
		t.Fatal(err)
	}
	cursorData, _ := io.ReadAll(cursor.Body)
	cursor.Body.Close()
	if cursor.StatusCode != http.StatusNotFound {
		t.Fatalf("closed cursor stream status %d: %s", cursor.StatusCode, cursorData)
	}
	cursorEnvelope, err := protocol.ParseEnvelope(cursorData)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorResponse(t, cursor.StatusCode, http.StatusNotFound, cursorEnvelope, "unknown_session")
}

func getSSE(t *testing.T, server *httptest.Server, path string) (*http.Response, []byte) {
	t.Helper()
	response, err := server.Client().Get(server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, body
}

func driveOneRun(t *testing.T, server *httptest.Server, sessionID, prefix string, afterAnEarlierRun bool) protocol.RunID {
	t.Helper()
	stream := connectSSE(t, server, "/sessions/"+sessionID+"/events", "")
	if afterAnEarlierRun {
		stream.signal(sseEventSubscribed)
	}
	_, admission := submitRun(t, server, sessionID, prefix+"-submit")
	initial := stream.drainUntil(protocol.TypeActionPermissionRequested)
	resolvePermission(t, server, permissionRequestAt(t, initial), prefix+"-permission")
	middle := stream.drainUntil(protocol.TypeUserInputRequested)
	resolveInput(t, server, inputRequestAt(t, middle), prefix+"-input")
	stream.drainUntil(protocol.TypeRunCompleted)
	return admission.RunID
}

func TestSSECursorFollowsTheRunItNames(t *testing.T) {
	server := newMemoryServer(t, 0)
	openSession(t, server, "memory", "sse-two-runs")
	first := driveOneRun(t, server, "sse-two-runs", "one", false)
	second := driveOneRun(t, server, "sse-two-runs", "two", true)
	if first == second {
		t.Fatal("the second submission reused the first run id")
	}

	named := connectSSE(t, server, "/sessions/sse-two-runs/events?after=1&run_id="+string(first), "")
	if envelope := named.envelope(); envelope.RunID != first {
		t.Fatalf("replay began on run %q, want the named run %q", envelope.RunID, first)
	}
	named.drainUntil(protocol.TypeRunCompleted)

	unnamed := connectSSE(t, server, "/sessions/sse-two-runs/events?after=1", "")
	if envelope := unnamed.envelope(); envelope.RunID != second {
		t.Fatalf("an unqualified cursor replayed run %q, want the current run %q", envelope.RunID, second)
	}
	unnamed.drainUntil(protocol.TypeRunCompleted)
}

func TestSSERefusesARunWithoutACursor(t *testing.T) {
	server := newMemoryServer(t, 0)
	openSession(t, server, "memory", "sse-bare-run")
	runID := driveOneRun(t, server, "sse-bare-run", "bare", false)

	response, body := getSSE(t, server, "/sessions/sse-bare-run/events?run_id="+string(runID))
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("run_id without a cursor answered %d: %s", response.StatusCode, body)
	}
	if !strings.Contains(string(body), "invalid_cursor") {
		t.Fatalf("refusal body %s, want invalid_cursor", body)
	}
}

func TestSSERefusesARunTheSessionNeverHad(t *testing.T) {
	server := newMemoryServer(t, 0)
	openSession(t, server, "memory", "sse-ghost-run")
	driveOneRun(t, server, "sse-ghost-run", "ghost", false)

	response, body := getSSE(t, server, "/sessions/sse-ghost-run/events?after=1&run_id=run-never")
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("a cursor naming an unknown run answered %d: %s", response.StatusCode, body)
	}
	if !strings.Contains(string(body), "run_not_found") {
		t.Fatalf("refusal body %s, want run_not_found", body)
	}
}

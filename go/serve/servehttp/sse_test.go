package servehttp

import (
	"context"
	"io"
	"net/http"
	"testing"

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
	stream.expectEnd()
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
	stream.expectEnd()
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
	stream.expectEnd()
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
	stream.expectEnd()

	stream = connectSSE(t, server, "/sessions/sse-gap/events?after=10", "")
	replayed := stream.drainUntil(protocol.TypeRunCompleted)
	requireSequence(t, replayed, 11)
	if len(replayed) != 2 {
		t.Fatalf("retained replay %d envelopes, want 2", len(replayed))
	}
	stream.expectEnd()
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
	stream.expectEnd()
	late.expectEnd()
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
	stream.expectEnd()
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

func (f *fakeSession) Submit(ctx context.Context, request protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	admission, stream, err := f.Session.Submit(ctx, request)
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
	if events.StatusCode != http.StatusConflict {
		t.Fatalf("closed live stream status %d: %s", events.StatusCode, closed)
	}
	envelope, err := protocol.ParseEnvelope(closed)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorResponse(t, events.StatusCode, http.StatusConflict, envelope, "session_closed")

	cursor, err := server.Client().Get(server.URL + "/sessions/sse-closed/events?after=0")
	if err != nil {
		t.Fatal(err)
	}
	cursorData, _ := io.ReadAll(cursor.Body)
	cursor.Body.Close()
	if cursor.StatusCode != http.StatusConflict {
		t.Fatalf("closed cursor stream status %d: %s", cursor.StatusCode, cursorData)
	}
	cursorEnvelope, err := protocol.ParseEnvelope(cursorData)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorResponse(t, cursor.StatusCode, http.StatusConflict, cursorEnvelope, "session_closed")
}

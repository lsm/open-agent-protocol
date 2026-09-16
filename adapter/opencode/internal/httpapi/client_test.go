package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/adapter/opencode/internal/native"
)

const sessionInfo = `{"id":"ses_test0000000000000000","projectID":"prj_1","cost":0,"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":1,"updated":1},"title":"","location":{"directory":"/w"}}`

func newTestClient(t *testing.T, handler http.Handler, options Options) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(server.URL, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestCreateSessionDecodesStrictlyAndSendsAuth(t *testing.T) {
	var gotAuth string
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/session" || r.Method != http.MethodPost {
			t.Fatalf("request %s %s", r.Method, r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":` + sessionInfo + `}`))
	}), Options{Username: "opencode", Password: "secret"})
	info, err := client.CreateSession(context.Background(), CreateSessionRequest{ID: "ses_test0000000000000000"})
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != native.SessionID("ses_test0000000000000000") {
		t.Fatalf("info=%+v", info)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("opencode:secret"))
	if gotAuth != want {
		t.Fatalf("auth=%q", gotAuth)
	}
}

func TestCreateSessionRejectsUnknownResponseFields(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":` + sessionInfo + `,"surprise":1}`))
	}), Options{})
	_, err := client.CreateSession(context.Background(), CreateSessionRequest{})
	if err == nil || !strings.Contains(err.Error(), "surprise") {
		t.Fatalf("err=%v", err)
	}
}

func TestPromptReturnsAdmittedAndTypedConflicts(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/session/ses_a/event" {
			if r.URL.Path == "/api/session/ses_a/prompt" {
				var body native.PromptRequest
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body.ID == native.MessageID("msg_conflict") {
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write([]byte(`{"_tag":"ConflictError","message":"id in use"}`))
					return
				}
				_, _ = w.Write([]byte(`{"data":{"admittedSeq":3,"id":"` + body.ID + `","sessionID":"ses_a","prompt":{"text":"hi"},"delivery":"steer","timeCreated":9}}`))
				return
			}
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}), Options{})
	admitted, err := client.Prompt(context.Background(), "ses_a", native.PromptRequest{ID: "msg_ok", Prompt: native.Prompt{Text: "hi"}, Delivery: native.DeliverySteer})
	if err != nil || admitted.AdmittedSeq != 3 || admitted.ID != native.MessageID("msg_ok") {
		t.Fatalf("admitted=%+v err=%v", admitted, err)
	}
	_, err = client.Prompt(context.Background(), "ses_a", native.PromptRequest{ID: "msg_conflict"})
	var apiErr *native.APIError
	if !errors.As(err, &apiErr) || !apiErr.IsConflict() || apiErr.Status != http.StatusConflict {
		t.Fatalf("err=%v", err)
	}
}

func TestPromptRejectsForeignAdmissionReceipt(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"admittedSeq":1,"id":"msg_1","sessionID":"ses_other","prompt":{"text":"hi"},"delivery":"steer","timeCreated":1}}`))
	}), Options{})
	_, err := client.Prompt(context.Background(), "ses_a", native.PromptRequest{ID: "msg_1"})
	if !errors.Is(err, ErrSubscription) {
		t.Fatalf("err=%v", err)
	}
}

// The pinned server does not implement the wait route: its handler resolves
// the session and then always fails with ServiceUnavailableError, so a live
// session answers 503 and a missing one 404. Nothing may read that as idle.
// This is why settlement corroborates with the active set instead, and the
// test exists so a future reader does not mistake the route's declared
// success contract below for observed behaviour.
func TestWaitIdleSurfacesPinnedUnavailable(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"_tag":"ServiceUnavailableError","message":"Session wait is not available yet","service":"session.wait"}`))
	}), Options{})
	err := client.WaitIdle(context.Background(), "ses_a")
	if err == nil {
		t.Fatal("wait must not report idle when the route is unavailable")
	}
	var apiErr *native.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err=%v, want a native APIError", err)
	}
	if apiErr.Status != http.StatusServiceUnavailable || apiErr.Tag != "ServiceUnavailableError" {
		t.Fatalf("status=%d tag=%q", apiErr.Status, apiErr.Tag)
	}
}

// The route's declared contract is 204; the handler above is what the pinned
// server actually returns. Both are pinned so the gap stays visible.
func TestInterruptAndWaitAcceptNoContent(t *testing.T) {
	paths := map[string]bool{}
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths[r.URL.Path] = true
		w.WriteHeader(http.StatusNoContent)
	}), Options{})
	if err := client.Interrupt(context.Background(), "ses_a"); err != nil {
		t.Fatal(err)
	}
	if err := client.WaitIdle(context.Background(), "ses_a"); err != nil {
		t.Fatal(err)
	}
	if !paths["/api/session/ses_a/interrupt"] || !paths["/api/session/ses_a/wait"] {
		t.Fatalf("paths=%v", paths)
	}
}

func TestActiveFiltersRunningSessions(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"ses_a":{"type":"running"},"ses_b":{"type":"idle"}}}`))
	}), Options{})
	active, err := client.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || !active["ses_a"] {
		t.Fatalf("active=%v", active)
	}
}

func durableEvent(seq int64, typ string, data string) string {
	return fmt.Sprintf(`{"id":"evt_%03d","type":%q,"durable":{"aggregateID":"ses_a","seq":%d,"version":1},"data":%s}`, seq, typ, seq, data)
}

func TestHistoryDecodesDurableEvents(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after") != "4" {
			t.Fatalf("after=%q", r.URL.Query().Get("after"))
		}
		_, _ = w.Write([]byte(`{"data":[` + durableEvent(5, "session.next.text.ended", `{"timestamp":1,"sessionID":"ses_a","assistantMessageID":"msg_9","textID":"t1","text":"done"}`) + `],"hasMore":false}`))
	}), Options{})
	page, err := client.History(context.Background(), "ses_a", 4, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].Durable.Seq != 5 || page.HasMore {
		t.Fatalf("page=%+v", page)
	}
}

func TestSubscribeReplaysThenFailsOnRegression(t *testing.T) {
	done := make(chan struct{})
	frames := []string{
		durableEvent(1, "session.next.prompted", `{"timestamp":1,"sessionID":"ses_a","messageID":"msg_1","prompt":{"text":"hi"},"delivery":"steer"}`),
		durableEvent(1, "session.next.prompted", `{"timestamp":1,"sessionID":"ses_a","messageID":"msg_1","prompt":{"text":"hi"},"delivery":"steer"}`),
	}
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after") != "0" {
			t.Fatalf("after=%q", r.URL.Query().Get("after"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, frame := range frames {
			_, _ = w.Write([]byte("event: message\ndata: " + frame + "\n\n"))
			flusher.Flush()
		}
		<-done
	}), Options{})
	t.Cleanup(func() { close(done) })
	subscription, err := client.Subscribe(context.Background(), "ses_a", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = subscription.Close() }()
	first := <-subscription.Events()
	if first.Durable.Seq != 1 {
		t.Fatalf("first=%+v", first)
	}
	select {
	case _, ok := <-subscription.Events():
		if ok {
			t.Fatal("regression delivered")
		}
	case <-subscription.Done():
		if !errors.Is(subscription.Err(), ErrSubscription) {
			t.Fatalf("err=%v", subscription.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("no subscription failure")
	}
}

func TestSubscribeRejectsMalformedPayload(t *testing.T) {
	done := make(chan struct{})
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: not-json\n\n"))
		w.(http.Flusher).Flush()
		<-done
	}), Options{})
	t.Cleanup(func() { close(done) })
	subscription, err := client.Subscribe(context.Background(), "ses_a", -1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = subscription.Close() }()
	select {
	case <-subscription.Done():
		if !errors.Is(subscription.Err(), native.ErrInvalidWire) {
			t.Fatalf("err=%v", subscription.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("no failure")
	}
}

func TestSubscribeSurfacesHTTPErrors(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"_tag":"SessionNotFoundError","sessionID":"ses_missing","message":"no"}`))
	}), Options{})
	_, err := client.Subscribe(context.Background(), "ses_missing", -1)
	var apiErr *native.APIError
	if !errors.As(err, &apiErr) || !apiErr.IsSessionNotFound() {
		t.Fatalf("err=%v", err)
	}
}

// TestCreateSessionRejectsDuplicateKeys pins strict decoding: encoding/json
// silently keeps the last duplicate key, so the duplicate walker must reject
// the body. Before the fix the duplicate top-level "data" key was accepted.
func TestCreateSessionRejectsDuplicateKeys(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":` + sessionInfo + `,"data":` + sessionInfo + `}`))
	}), Options{})
	_, err := client.CreateSession(context.Background(), CreateSessionRequest{})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate key accepted: err=%v", err)
	}
}

// TestCreateSessionRejectsOversizedBody pins that a body longer than the frame
// limit is rejected, not truncated: LimitReader's artificial EOF would
// otherwise let a valid JSON prefix pass with its trailing bytes discarded.
func TestCreateSessionRejectsOversizedBody(t *testing.T) {
	valid := `{"data":` + sessionInfo + `}`
	body := valid + strings.Repeat(" ", 32) + `{"tail":true}`
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}), Options{FrameLimit: len(valid) + 8})
	_, err := client.CreateSession(context.Background(), CreateSessionRequest{})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized body accepted: err=%v", err)
	}
}

// TestSubscriptionCloseInterruptsBlockedRead pins that Close tears down an idle
// SSE stream instead of leaking the pump goroutine and connection. The server
// flushes headers then sends nothing; Close must cancel the request so the
// blocked Decode returns and the server observes the disconnect. Before the
// fix Close only closed done, the pump stayed blocked in Decode, and the
// server's request context was never cancelled.
func TestSubscriptionCloseInterruptsBlockedRead(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseNow := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseNow) // never leave the handler blocking server Close
	handlerDone := make(chan struct{})
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
		close(handlerDone)
	}), Options{})
	// The stream context lets cleanup force teardown even when Close fails to
	// (before the fix), so a leaked connection cannot hang httptest.Server.
	streamCtx, cancelStream := context.WithCancel(context.Background())
	t.Cleanup(cancelStream)
	subscription, err := client.Subscribe(streamCtx, "ses_a", -1)
	if err != nil {
		t.Fatal(err)
	}
	// Subscribe returns once the headers are flushed; let the pump goroutine
	// reach and park in Decode so Close must interrupt the read rather than
	// racing the done check. Without this the pump can observe done before its
	// first Decode and its deferred Body.Close() would tear the connection
	// down even without the fix.
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	if err := subscription.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Close blocked for %v", elapsed)
	}
	// The server only observes its request context end (or a client
	// disconnect) once the HTTP request carrying the idle stream is torn
	// down. That teardown is what closes the response body the pump is
	// blocked reading, so its firing proves the blocked Decode was
	// interrupted and the goroutine can exit.
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not disconnect the idle SSE request")
	}
}

// TestSubscribeReturnsWhenHeadersAreDeferred pins the live server behavior:
// the pinned session-scoped SSE endpoint withholds response headers until the
// stream carries an event. Subscribe must not block the caller waiting for
// them; the subscription becomes usable and events arrive once the stream
// starts. Before the fix this blocked until the caller context expired.
func TestSubscribeReturnsWhenHeadersAreDeferred(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseNow := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseNow) // never leave the deferred handler blocking server Close
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/session/ses_a/event" {
			t.Errorf("path %s", r.URL.Path)
		}
		<-release // defer response headers until an event is ready
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, `data: {"id":"evt_1","type":"session.next.prompted","durable":{"aggregateID":"ses_a","seq":1,"version":1},"data":{"timestamp":1,"sessionID":"ses_a","messageID":"msg_1","prompt":{"text":"hi"},"delivery":"steer"}}`+"\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}), Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	subscription, err := client.Subscribe(ctx, "ses_a", -1)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Subscribe blocked for %v awaiting deferred headers", elapsed)
	}
	// The deferred stream is still live: once the server flushes, the event
	// must arrive through the subscription.
	releaseNow()
	select {
	case event := <-subscription.Events():
		if event.Type != native.TypePrompted || event.Durable.Seq != 1 {
			t.Fatalf("event = %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deferred stream never delivered its event")
	}
}

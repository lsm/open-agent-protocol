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
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
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
		if r.URL.Path != "/api/session/ses_a/prompt" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var body native.PromptRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.ID == native.MessageID("msg_conflict") {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"_tag":"ConflictError","message":"id in use"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"id":"` + string(body.ID) + `","sessionID":"ses_a","time":{"created":9},"type":"user","payload":{"text":"hi"},"delivery":"` + string(body.Delivery) + `"}}`))
	}), Options{})
	admitted, err := client.Prompt(context.Background(), "ses_a", native.PromptRequest{ID: "msg_ok", Text: "hi", Delivery: native.DeliveryQueue})
	if err != nil || admitted.ID != native.MessageID("msg_ok") || admitted.Delivery != native.DeliveryQueue || admitted.Time.Created != 9 {
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
		_, _ = w.Write([]byte(`{"data":{"id":"msg_1","sessionID":"ses_other","time":{"created":1},"type":"user","payload":{"text":"hi"},"delivery":"steer"}}`))
	}), Options{})
	_, err := client.Prompt(context.Background(), "ses_a", native.PromptRequest{ID: "msg_1"})
	if !errors.Is(err, ErrSubscription) {
		t.Fatalf("err=%v", err)
	}
}

func TestInterruptReportsWhetherAnExecutionWasInterrupted(t *testing.T) {
	answer := `{"interrupted":true}`
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/session/ses_a/interrupt" || r.Method != http.MethodPost {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(answer))
	}), Options{})
	interrupted, err := client.Interrupt(context.Background(), "ses_a")
	if err != nil || !interrupted {
		t.Fatalf("interrupted=%v err=%v", interrupted, err)
	}
	answer = `{"interrupted":false}`
	interrupted, err = client.Interrupt(context.Background(), "ses_a")
	if err != nil || interrupted {
		t.Fatalf("an idle interrupt answered interrupted=%v err=%v", interrupted, err)
	}
}

func TestCancelInboxDeletesTheInput(t *testing.T) {
	var seen string
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Method + " " + r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}), Options{})
	if err := client.CancelInbox(context.Background(), "ses_a", "msg_1"); err != nil {
		t.Fatal(err)
	}
	if seen != "DELETE /api/session/ses_a/inbox/msg_1" {
		t.Fatalf("request = %q", seen)
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

const connected = `{"id":"evt_connected","type":"server.connected","data":{}}`

func sessionEvent(session string, seq int64, typ string, data string) string {
	return fmt.Sprintf(`{"id":"evt_%s%03d","created":1,"type":%q,"location":{"directory":"/w"},"durable":{"aggregateID":%q,"seq":%d,"version":1},"data":%s}`, strings.TrimPrefix(session, "ses_"), seq, typ, session, seq, data)
}

func streamFrames(t *testing.T, done <-chan struct{}, frames ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/event" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, frame := range frames {
			_, _ = w.Write([]byte("data: " + frame + "\n\n"))
			flusher.Flush()
		}
		select {
		case <-done:
		case <-r.Context().Done():
		}
	})
}

func TestSubscribeFollowsOneSessionOnTheGlobalStream(t *testing.T) {
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	client := newTestClient(t, streamFrames(t, done,
		connected,
		sessionEvent("ses_b", 1, "session.execution.started", `{"sessionID":"ses_b"}`),
		sessionEvent("ses_b", 2, "session.brand.new", `{"sessionID":"ses_b"}`),
		`{"id":"evt_undurable","created":1,"type":"session.execution.started","data":{"sessionID":"ses_b"}}`,
		`{"id":"evt_project","created":1,"type":"project.updated","data":{"id":"p"}}`,
		sessionEvent("ses_a", 1, "session.execution.started", `{"sessionID":"ses_a"}`),
		`{"id":"evt_delta","created":1,"type":"session.text.delta","data":{"sessionID":"ses_a","assistantMessageID":"msg_1","ordinal":0,"delta":"h"}}`,
	), Options{})
	subscription, err := client.Subscribe(context.Background(), "ses_a")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = subscription.Close() }()
	first := <-subscription.Events()
	if first.Type != native.TypeExecutionStarted || first.SessionID != "ses_a" || first.Durable.Seq != 1 {
		t.Fatalf("first=%+v", first)
	}
	second := <-subscription.Events()
	if second.Type != native.TypeTextDelta || second.Durable != nil {
		t.Fatalf("second=%+v", second)
	}
}

func TestSubscribeFailsOnARegressingSequence(t *testing.T) {
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	frame := sessionEvent("ses_a", 1, "session.execution.started", `{"sessionID":"ses_a"}`)
	client := newTestClient(t, streamFrames(t, done, connected, frame, frame), Options{})
	subscription, err := client.Subscribe(context.Background(), "ses_a")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = subscription.Close() }()
	if first := <-subscription.Events(); first.Durable.Seq != 1 {
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

func TestSubscribeFailsOnAnUnsupportedEventOfItsOwnSession(t *testing.T) {
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	client := newTestClient(t, streamFrames(t, done, connected, sessionEvent("ses_a", 1, "session.brand.new", `{"sessionID":"ses_a"}`)), Options{})
	subscription, err := client.Subscribe(context.Background(), "ses_a")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = subscription.Close() }()
	select {
	case <-subscription.Done():
		if !errors.Is(subscription.Err(), native.ErrUnsupportedType) {
			t.Fatalf("err=%v", subscription.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("no failure")
	}
}

func TestSubscribeRefusesAStreamThatFailsBeforeItConnects(t *testing.T) {
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	client := newTestClient(t, streamFrames(t, done, "not-json"), Options{})
	_, err := client.Subscribe(context.Background(), "ses_a")
	if !errors.Is(err, ErrSubscription) || !errors.Is(err, native.ErrInvalidWire) {
		t.Fatalf("err=%v", err)
	}
}

func TestSubscribeSurfacesHTTPErrors(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"_tag":"UnauthorizedError","message":"no"}`))
	}), Options{})
	_, err := client.Subscribe(context.Background(), "ses_a")
	var apiErr *native.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("err=%v", err)
	}
}

func TestCreateSessionRejectsDuplicateKeys(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":` + sessionInfo + `,"data":` + sessionInfo + `}`))
	}), Options{})
	_, err := client.CreateSession(context.Background(), CreateSessionRequest{})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate key accepted: err=%v", err)
	}
}

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

func TestSubscriptionCloseInterruptsBlockedRead(t *testing.T) {
	handlerDone := make(chan struct{})
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + connected + "\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(handlerDone)
	}), Options{})

	streamCtx, cancelStream := context.WithCancel(context.Background())
	t.Cleanup(cancelStream)
	subscription, err := client.Subscribe(streamCtx, "ses_a")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := subscription.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Close blocked for %v", elapsed)
	}
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not disconnect the idle SSE request")
	}
}

func TestSessionsAsksForTheDirectorysRootSessionsNewestFirstAndRefusesARowThatIsNotASession(t *testing.T) {
	var target string
	body := `{"data":[{"id":"ses_a","projectID":"p","title":"t","cost":0,"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":1,"updated":2},"location":{"directory":"/x/R&D"}}],"cursor":{"next":"n"}}`
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target = r.URL.RequestURI()
		_, _ = w.Write([]byte(body))
	}), Options{})
	listed, err := client.Sessions(context.Background(), "/x/R&D+a=b c~", 3)
	if err != nil {
		t.Fatal(err)
	}
	if target != "/api/session?directory=%2Fx%2FR%26D%2Ba%3Db+c~&limit=3&order=desc&parentID=null" {
		t.Fatalf("asked %s", target)
	}
	if len(listed) != 1 || listed[0].Title != "t" || listed[0].Time.Updated != 2 {
		t.Fatalf("listed %+v", listed)
	}
	if _, err := client.Sessions(context.Background(), "", 3); err != nil || target != "/api/session?limit=3&order=desc&parentID=null" {
		t.Fatalf("an unscoped list asked %s, %v", target, err)
	}
	body = `{"data":[{"id":"ses_a","projectID":"","time":{"created":1,"updated":2}}],"cursor":{}}`
	if _, err := client.Sessions(context.Background(), "", 3); err == nil {
		t.Fatal("a row with no project was admitted")
	}
	body = `{"data":[{"id":"ses_a","projectID":"p","surprise":1}],"cursor":{}}`
	if _, err := client.Sessions(context.Background(), "", 3); err == nil {
		t.Fatal("a row with an unknown member was admitted")
	}
}

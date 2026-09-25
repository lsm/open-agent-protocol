package serveendpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

type parkingWriter struct {
	mu       sync.Mutex
	accepted int
	limit    int
	parked   chan struct{}
	once     sync.Once
}

func (w *parkingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.accepted++
	over := w.accepted > w.limit
	w.mu.Unlock()
	if !over {
		return len(p), nil
	}
	w.once.Do(func() { close(w.parked) })
	select {}
}

func TestTeardownStopsWhenAWriteParksForever(t *testing.T) {
	registry, err := serve.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	server, err := New(hub, Options{Adapter: "memory", Shutdown: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	open := requestLine(t, protocol.TypeSessionOpenRequest, "open-1",
		protocol.SessionOpenRequest{SessionID: "parked"}, "parked")
	submit := requestLine(t, protocol.TypeSessionMessageSubmitRequest, "submit-1",
		protocol.MessageSubmitRequest{
			SessionID: "parked", Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
		}, "parked")

	out := &parkingWriter{limit: 2, parked: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- server.Run(context.Background(), strings.NewReader(open+"\n"+submit+"\n"), out)
	}()

	select {
	case <-out.parked:
	case <-time.After(10 * time.Second):
		t.Fatal("the endpoint never reached a parked write, so this test proved nothing")
	}

	select {
	case err := <-done:
		if !errors.Is(err, ErrShutdownStalled) {
			t.Fatalf("Run returned %v, want ErrShutdownStalled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run never returned: the teardown blocked on the writer lock the abandoned pump still holds")
	}
}

func requestLine(t *testing.T, typ protocol.EnvelopeType, id string, payload any, session protocol.SessionID) string {
	t.Helper()
	envelope, err := protocol.NewEnvelope(typ, protocol.EnvelopeID(id), payload)
	if err != nil {
		t.Fatal(err)
	}
	envelope.SessionID = session
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s", data)
}

func TestPipelinedRequestsStayCancellableWhileTheWriterIsParked(t *testing.T) {
	registry, err := serve.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	server, err := New(hub, Options{Adapter: "memory", Shutdown: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	var input strings.Builder
	input.WriteString(requestLine(t, protocol.TypeSessionOpenRequest, "open-1",
		protocol.SessionOpenRequest{SessionID: "wedged"}, "wedged") + "\n")
	input.WriteString(requestLine(t, protocol.TypeSessionMessageSubmitRequest, "submit-1",
		protocol.MessageSubmitRequest{
			SessionID: "wedged", Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
		}, "wedged") + "\n")

	for i := 0; i < writeQueue*4; i++ {
		input.WriteString(requestLine(t, protocol.TypeSessionStateRequest,
			fmt.Sprintf("state-%d", i),
			protocol.SessionStateRequest{SessionID: "wedged"}, "wedged") + "\n")
	}

	out := &parkingWriter{limit: 2, parked: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, strings.NewReader(input.String()), out) }()

	select {
	case <-out.parked:
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("the endpoint never reached a parked write, so this test proved nothing")
	}

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run ignored its cancelled context: a handler is blocked behind the parked writer")
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

func TestASecondSubmitDoesNotDuplicateTheStream(t *testing.T) {
	registry, err := serve.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	server, err := New(hub, Options{Adapter: "memory", Shutdown: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	message := func(text string) protocol.MessageSubmitRequest {
		return protocol.MessageSubmitRequest{
			SessionID: "twice", Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent(text)}},
		}
	}
	input := strings.Join([]string{
		requestLine(t, protocol.TypeSessionOpenRequest, "open-1", protocol.SessionOpenRequest{SessionID: "twice"}, "twice"),
		requestLine(t, protocol.TypeSessionMessageSubmitRequest, "submit-1", message("one"), "twice"),
		requestLine(t, protocol.TypeSessionMessageSubmitRequest, "submit-2", message("two"), "twice"),
	}, "\n") + "\n"

	out := &syncBuffer{}
	_ = server.Run(context.Background(), strings.NewReader(input), out)

	seen := map[protocol.EnvelopeID]int{}
	events := 0
	for _, raw := range strings.Split(out.String(), "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		var envelope protocol.Envelope
		if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
			continue
		}
		if envelope.InReplyTo != "" || envelope.Sequence == nil {
			continue
		}
		events++
		seen[envelope.ID]++
	}
	if events == 0 {
		t.Fatal("no run events were streamed, so this test proved nothing")
	}
	for id, count := range seen {
		if count > 1 {
			t.Errorf("envelope %s was delivered %d times", id, count)
		}
	}
}

func TestHungUpHostExitsWithoutASignal(t *testing.T) {
	registry, err := serve.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	server, err := New(hub, Options{
		Adapter: "memory", Shutdown: 100 * time.Millisecond, WriteStall: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	var input strings.Builder
	input.WriteString(requestLine(t, protocol.TypeSessionOpenRequest, "open-1",
		protocol.SessionOpenRequest{SessionID: "hungup"}, "hungup") + "\n")
	input.WriteString(requestLine(t, protocol.TypeSessionMessageSubmitRequest, "submit-1",
		protocol.MessageSubmitRequest{
			SessionID: "hungup", Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
		}, "hungup") + "\n")
	for i := 0; i < writeQueue*4; i++ {
		input.WriteString(requestLine(t, protocol.TypeSessionStateRequest,
			fmt.Sprintf("state-%d", i),
			protocol.SessionStateRequest{SessionID: "hungup"}, "hungup") + "\n")
	}

	out := &parkingWriter{limit: 2, parked: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), strings.NewReader(input.String()), out) }()

	select {
	case <-out.parked:
	case <-time.After(10 * time.Second):
		t.Fatal("the endpoint never reached a parked write, so this test proved nothing")
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrShutdownStalled) {
			t.Fatalf("Run returned %v, want ErrShutdownStalled", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run never returned: end of input went unobserved behind a parked handler, so only SIGKILL would exit")
	}
}

func refusalFor(t *testing.T, out string, request protocol.EnvelopeID) string {
	t.Helper()
	for _, raw := range strings.Split(out, "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		var envelope protocol.Envelope
		if json.Unmarshal([]byte(raw), &envelope) != nil {
			continue
		}
		if envelope.InReplyTo != request || envelope.Type != protocol.TypeErrorResponse {
			continue
		}
		var failure protocol.ErrorResponse
		if err := envelope.DecodePayload(&failure); err != nil {
			t.Fatal(err)
		}
		return failure.Error.Code
	}
	t.Fatalf("no error.response answering %s", request)
	return ""
}

func TestPayloadScopeIsRefusedUnderItsOwnCode(t *testing.T) {
	registry, err := serve.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	server, err := New(hub, Options{Adapter: "memory", Shutdown: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	input := strings.Join([]string{
		requestLine(t, protocol.TypeSessionOpenRequest, "open-1", protocol.SessionOpenRequest{SessionID: "scoped"}, "scoped"),
		requestLine(t, protocol.TypeSessionMessageSubmitRequest, "submit-elsewhere",
			protocol.MessageSubmitRequest{
				SessionID: "somewhere-else", Delivery: protocol.DeliveryAuto,
				Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
			}, "scoped"),
		requestLine(t, protocol.TypeRunCancelRequest, "cancel-elsewhere",
			protocol.RunCancelRequest{SessionID: "somewhere-else", RunID: "run-1"}, "scoped"),
	}, "\n") + "\n"

	out := &syncBuffer{}
	_ = server.Run(context.Background(), strings.NewReader(input), out)

	if code := refusalFor(t, out.String(), "submit-elsewhere"); code != "scope_mismatch" {
		t.Errorf("a submit naming another session was refused %q, want scope_mismatch", code)
	}
	if code := refusalFor(t, out.String(), "cancel-elsewhere"); code != "scope_mismatch" {
		t.Errorf("a cancel naming another session was refused %q, want scope_mismatch", code)
	}
}

func TestALostStreamNamesItsOwnRun(t *testing.T) {
	server := &Server{
		lines:      make(chan []byte, 4),
		frameLimit: DefaultFrameLimit,
		writeStall: time.Second,
		logger:     log.New(io.Discard, "", 0),
	}
	server.reportLostStream(context.Background(), "run-2", 0,
		&serve.OverflowError{RunID: "run-1", LastSequence: 9})

	var frame controlFrame
	if err := json.Unmarshal(<-server.lines, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Control != controlStreamLost {
		t.Fatalf("control %q, want %q", frame.Control, controlStreamLost)
	}
	if frame.RunID != "run-2" {
		t.Errorf("the lost frame names run %q, want the run this pump served", frame.RunID)
	}
	if frame.After == nil || *frame.After != 0 {
		t.Errorf("the resume cursor is %v, want this pump's own delivered position", frame.After)
	}
	if frame.Code != "overflow" {
		t.Errorf("code %q, want overflow", frame.Code)
	}
}

func TestAddressableEnvelopeIsAnsweredNotFatal(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		fatal   bool
		missing string
	}{
		{name: "unknown type is answered", line: `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"nonsense.request","id":"c1","payload":{}}`},
		{name: "undecodable payload is answered", line: `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.open.request","id":"c2","payload":{"session_id":42}}`},
		{name: "missing version is answered", line: `{"protocol":"open-agent-protocol","profile":"open-agent-protocol.agent-control-core","type":"capabilities.request","id":"c3","payload":{}}`, missing: "version"},
		{name: "missing profile is answered", line: `{"protocol":"open-agent-protocol","version":"0.1","type":"capabilities.request","id":"c4","payload":{}}`, missing: "profile"},
		{name: "missing payload is answered", line: `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.request","id":"c5"}`, missing: "payload"},
		{name: "missing type is answered", line: `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","id":"c6","payload":{}}`, missing: "type"},
		{name: "null version counts as missing", line: `{"protocol":"open-agent-protocol","version":null,"profile":"open-agent-protocol.agent-control-core","type":"capabilities.request","id":"c7","payload":{}}`, missing: "version"},
		{name: "no id cannot be answered", line: `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.request"}`, fatal: true},
		{name: "neither protocol nor control", line: `{"hello":"world"}`, fatal: true},
		{name: "not json at all", line: `this is not an envelope`, fatal: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			registry, err := serve.DefaultRegistry()
			if err != nil {
				t.Fatal(err)
			}
			server, err := New(serve.New(registry, serve.Options{}), Options{Adapter: "memory"})
			if err != nil {
				t.Fatal(err)
			}
			out := &syncBuffer{}
			runErr := server.Run(context.Background(), strings.NewReader(testCase.line+"\n"), out)
			written := out.String()
			if testCase.fatal {
				if !errors.Is(runErr, ErrMalformedLine) {
					t.Fatalf("Run returned %v, want ErrMalformedLine", runErr)
				}
				if strings.Contains(written, "\"protocol\"") {
					t.Fatalf("a framing fault wrote an envelope to stdout: %q", written)
				}
				return
			}
			if runErr != nil {
				t.Fatalf("Run returned %v: an addressable envelope that is merely wrong is a protocol error", runErr)
			}
			if !strings.Contains(written, "error.response") || !strings.Contains(written, "in_reply_to") {
				t.Fatalf("want a correlated error.response, got %q", written)
			}
			if testCase.missing != "" && !strings.Contains(written, testCase.missing) {
				t.Fatalf("the refusal does not name the omitted member %q: %q", testCase.missing, written)
			}
		})
	}
}

func TestOpenHonoursItsElectionsInsteadOfDroppingThem(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    []string
	}{
		{name: "a local tool source is attached", payload: `{"session_id":"a","tool_sources":[{"id":"notes","kind":"local","display_name":"Notes"}]}`, want: []string{`"session.open.response"`, `"id":"notes"`}},
		{name: "an unconfigured process source is refused", payload: `{"session_id":"b","tool_sources":[{"id":"fs","kind":"process"}]}`, want: []string{`"unsupported_feature"`, `"source":"fs"`, `no tool source of that id is configured`}},
		{name: "a wire command is refused", payload: `{"session_id":"c","tool_sources":[{"id":"fs","kind":"local","command":"/bin/sh"}]}`, want: []string{`"unsupported_feature"`, `does not accept a command`}},
		{name: "a compound open is refused, not dropped", payload: `{"session_id":"d","message":{"delivery":"auto","messages":[{"role":"user","content":"hi"}]}}`, want: []string{`"unsupported_feature"`, `"field":"message"`, `"feature":"session.message.submit"`}},
		{name: "a subscription the adapter streams anyway is admitted", payload: `{"session_id":"e","subscribe":true}`, want: []string{`"session.open.response"`}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			registry, err := serve.DefaultRegistry()
			if err != nil {
				t.Fatal(err)
			}
			server, err := New(serve.New(registry, serve.Options{}), Options{Adapter: "memory"})
			if err != nil {
				t.Fatal(err)
			}
			out := &syncBuffer{}
			line := `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.open.request","id":"o1","payload":` + testCase.payload + `}`
			if err := server.Run(context.Background(), strings.NewReader(line+"\n"), out); err != nil {
				t.Fatal(err)
			}
			for _, fragment := range testCase.want {
				if !strings.Contains(out.String(), fragment) {
					t.Fatalf("want %s in %q", fragment, out.String())
				}
			}
		})
	}
}

package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/adapter/claude/internal/native"
)

type peer struct {
	t       *testing.T
	client  *Client
	writeIn io.Writer
	frames  chan Message
}

func newPeer(t *testing.T) *peer {
	t.Helper()
	upstreamRead, upstreamWrite := io.Pipe()
	downstreamRead, downstreamWrite := io.Pipe()
	client := NewClient(upstreamRead, downstreamWrite, ClientOptions{})
	t.Cleanup(func() { _ = client.Close() })
	frames := make(chan Message, 64)
	go func() {
		reader := bufio.NewReader(downstreamRead)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				close(frames)
				return
			}
			message, err := ParseMessage([]byte(strings.TrimSuffix(line, "\n")))
			if err != nil {
				t.Errorf("client wrote an invalid frame: %v", err)
				close(frames)
				return
			}
			frames <- message
		}
	}()
	return &peer{t: t, client: client, writeIn: upstreamWrite, frames: frames}
}

func (p *peer) send(line string) {
	p.t.Helper()
	if _, err := p.writeIn.Write([]byte(line + "\n")); err != nil {
		p.t.Fatalf("peer write: %v", err)
	}
}

func (p *peer) readFrame() (Message, error) {
	select {
	case message, ok := <-p.frames:
		if !ok {
			return Message{}, io.EOF
		}
		return message, nil
	case <-time.After(5 * time.Second):
		return Message{}, errors.New("peer read timed out")
	}
}

func observe(t *testing.T, client *Client) *ObservationMessage {
	t.Helper()
	select {
	case message := <-client.Inbound():
		if message.Observation == nil {
			t.Fatalf("expected an observation, got %+v", message)
		}
		return message.Observation
	case <-time.After(5 * time.Second):
		t.Fatal("observation timed out")
		return nil
	}
}

func barrier(t *testing.T, client *Client) {
	t.Helper()
	select {
	case message := <-client.Inbound():
		if message.Barrier == nil {
			t.Fatalf("expected a barrier, got %+v", message)
		}
		close(message.Barrier)
	case <-time.After(5 * time.Second):
		t.Fatal("barrier timed out")
	}
}

func TestClientControlCallBarrierOrdersEarlierObservations(t *testing.T) {
	p := newPeer(t)
	result := make(chan error, 1)
	go func() {
		result <- p.client.Call(context.Background(), json.RawMessage(`{"subtype":"initialize","hooks":null}`), nil)
	}()

	request, err := p.readFrame()
	if err != nil {
		t.Fatal(err)
	}
	if request.Kind != KindControlRequest || request.RequestID == "" {
		t.Fatalf("request = %+v", request)
	}

	p.send(`{"type":"command_lifecycle","command_uuid":"u1","state":"queued","session_id":"s1","uuid":"c1"}`)
	p.send(`{"type":"control_response","response":{"subtype":"success","request_id":"` + request.RequestID + `","response":{}}}`)

	observed := make(chan struct{})
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		sawObservation := false
		for {
			select {
			case message := <-p.client.Inbound():
				if message.Barrier != nil {
					if !sawObservation {
						t.Error("barrier acknowledged position lost: observation not yet delivered")
					}
					close(message.Barrier)
					return
				}
				if message.Observation != nil {
					sawObservation = true
					close(observed)
				}
			case <-time.After(5 * time.Second):
				t.Error("inbound consumer timed out")
				return
			}
		}
	}()
	select {
	case <-observed:
	case <-time.After(5 * time.Second):
		t.Fatal("wire-earlier observation never delivered")
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("call timed out (barrier never released)")
	}
	select {
	case <-consumerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("consumer never saw the barrier")
	}
}

func TestClientReverseControlRoundTrip(t *testing.T) {
	p := newPeer(t)
	p.send(`{"type":"control_request","request_id":"ask-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"},"tool_use_id":"t1"}}`)
	var control *IncomingControl
	select {
	case message := <-p.client.Inbound():
		control = message.Control
	case <-time.After(5 * time.Second):
		t.Fatal("control request timed out")
	}
	if control == nil || control.Subtype != native.ControlCanUseTool {
		t.Fatalf("control = %+v", control)
	}
	ask, ok := control.Value.(*native.CanUseToolRequest)
	if !ok || ask.ToolName != "Bash" || ask.ToolUseID != "t1" {
		t.Fatalf("typed value = %#v", control.Value)
	}
	if err := control.Respond(context.Background(), json.RawMessage(`{"behavior":"allow","updatedInput":{"command":"ls"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := control.Respond(context.Background(), json.RawMessage(`{"behavior":"deny"}`)); !errors.Is(err, ErrReverseResolved) {
		t.Fatalf("second answer err = %v", err)
	}
	answer, err := p.readFrame()
	if err != nil {
		t.Fatal(err)
	}
	if answer.Kind != KindControlResponse || !answer.Response.Success || answer.Response.RequestID != "ask-1" {
		t.Fatalf("answer = %+v", answer)
	}
	var decision struct {
		Behavior     string          `json:"behavior"`
		UpdatedInput json.RawMessage `json:"updatedInput"`
	}
	if err := json.Unmarshal(answer.Response.Response, &decision); err != nil {
		t.Fatal(err)
	}
	if decision.Behavior != "allow" || string(decision.UpdatedInput) != `{"command":"ls"}` {
		t.Fatalf("decision = %s", answer.Response.Response)
	}
}

func TestClientControlCancelRetiresReverseRequest(t *testing.T) {
	p := newPeer(t)
	p.send(`{"type":"control_request","request_id":"ask-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"t1"}}`)
	var control *IncomingControl
	select {
	case message := <-p.client.Inbound():
		control = message.Control
	case <-time.After(5 * time.Second):
		t.Fatal("control request timed out")
	}
	p.send(`{"type":"control_cancel_request","request_id":"ask-1"}`)
	select {
	case message := <-p.client.Inbound():
		if message.Cancel == nil || message.Cancel.RequestID != "ask-1" {
			t.Fatalf("cancel = %+v", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel timed out")
	}
	if err := control.Respond(context.Background(), json.RawMessage(`{"behavior":"allow"}`)); !errors.Is(err, ErrReverseResolved) {
		t.Fatalf("answer after cancel err = %v", err)
	}
}

func TestClientIgnoresUnmatchedControlResponse(t *testing.T) {
	p := newPeer(t)
	p.send(`{"type":"control_response","response":{"subtype":"success","request_id":"never-issued","response":{}}}`)

	p.send(`{"type":"keep_alive"}`)
	observe(t, p.client)
	p.send(`{"type":"keep_alive"}`)
	observe(t, p.client)
	select {
	case <-p.client.Done():
		t.Fatal("unmatched response retired the client")
	default:
	}
}

func TestClientTypedFrameViolationRetiresTransport(t *testing.T) {
	p := newPeer(t)
	p.send(`{"type":"result","subtype":"success","duration_ms":1,"duration_api_ms":1,"is_error":false,"num_turns":1}`)
	select {
	case <-p.client.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("invalid result frame did not retire the client")
	}
	if p.client.Err() == nil {
		t.Fatal("no error surfaced")
	}
}

func TestClientObservationDecodesTypedValues(t *testing.T) {
	p := newPeer(t)
	p.send(`{"type":"command_lifecycle","command_uuid":"u1","state":"completed","session_id":"s1","uuid":"c1"}`)
	observation := observe(t, p.client)
	if observation.Type != TypeCommandLifecycle {
		t.Fatalf("type = %q", observation.Type)
	}
}

func TestClientCallerCancellationRetiresConnection(t *testing.T) {
	p := newPeer(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- p.client.Call(ctx, json.RawMessage(`{"subtype":"interrupt"}`), nil)
	}()
	if _, err := p.readFrame(); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("call did not return")
	}
	select {
	case <-p.client.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("client not retired after ambiguous cancellation")
	}
}

func TestClientWriteUserRoundTrips(t *testing.T) {
	p := newPeer(t)
	frame := json.RawMessage(`{"type":"user","message":{"role":"user","content":"hello"},"parent_tool_use_id":null,"session_id":"default","uuid":"u1","origin":{"kind":"human"}}`)
	if err := p.client.WriteUser(context.Background(), frame); err != nil {
		t.Fatal(err)
	}
	written, err := p.readFrame()
	if err != nil {
		t.Fatal(err)
	}
	if written.Kind != KindObservation || written.Type != TypeUser {
		t.Fatalf("written = %+v", written)
	}
}

func TestConcurrentCallsStayCorrelated(t *testing.T) {
	p := newPeer(t)

	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for {
			select {
			case message := <-p.client.Inbound():
				if message.Barrier != nil {
					close(message.Barrier)
				}
			case <-p.client.Done():
				return
			case <-time.After(10 * time.Second):
				return
			}
		}
	}()

	dispatcherDone := make(chan struct{})
	go func() {
		defer close(dispatcherDone)
		for {
			request, err := p.readFrame()
			if err != nil {
				return
			}
			var sent struct {
				Request struct {
					N int `json:"n"`
				} `json:"request"`
			}
			if json.Unmarshal(request.Raw, &sent) != nil || sent.Request.N == 0 {
				t.Errorf("request %s carries no tag: %s", request.RequestID, request.Raw)
				return
			}
			p.send(`{"type":"control_response","response":{"subtype":"success","request_id":"` + request.RequestID + `","response":{"n":` + strconv.Itoa(sent.Request.N) + `}}}`)
		}
	}()
	var wg sync.WaitGroup
	for i := 1; i <= 4; i++ {
		wg.Add(1)
		go func(tag int) {
			defer wg.Done()
			type echo struct {
				N int `json:"n"`
			}
			var reply echo
			if err := p.client.Call(context.Background(), json.RawMessage(fmt.Sprintf(`{"subtype":"interrupt","n":%d}`, tag)), &reply); err != nil {
				t.Errorf("call %d: %v", tag, err)
				return
			}
			if reply.N != tag {
				t.Errorf("call %d received response %d", tag, reply.N)
			}
		}(i)
	}
	wg.Wait()

	_ = consumerDone
	_ = dispatcherDone
}

func TestClientDeliversResponseParkedInBarrierWhenTransportRetires(t *testing.T) {
	p := newPeer(t)
	result := make(chan error, 1)
	go func() {
		result <- p.client.Call(context.Background(), json.RawMessage(`{"subtype":"initialize","hooks":null}`), nil)
	}()
	request, err := p.readFrame()
	if err != nil {
		t.Fatal(err)
	}
	p.send(`{"type":"control_response","response":{"subtype":"success","request_id":"` + request.RequestID + `","response":{}}}`)

	select {
	case message := <-p.client.Inbound():
		if message.Barrier == nil {
			t.Fatalf("expected the response barrier first, got %+v", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no barrier was delivered")
	}

	_ = p.client.Close()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("the parked response lost to the transport's death: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("call never settled")
	}
}

func TestClientFailsPendingCallsWithoutCloserWhenAnotherCallCancels(t *testing.T) {
	p := newPeer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := make(chan error, 1)
	go func() {
		cancelled <- p.client.Call(ctx, json.RawMessage(`{"subtype":"initialize","hooks":null}`), nil)
	}()
	other := make(chan error, 1)
	go func() {
		other <- p.client.Call(context.Background(), json.RawMessage(`{"subtype":"interrupt"}`), nil)
	}()

	for i := 0; i < 2; i++ {
		if _, err := p.readFrame(); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	select {
	case err := <-cancelled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled call err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled call never returned")
	}
	select {
	case err := <-other:
		if err == nil {
			t.Fatal("the other call reported success with no response")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the other pending call hung waiting for a reader nothing can interrupt")
	}
}

func TestClientCallSurvivesResponseFollowedByMalformedFrame(t *testing.T) {
	const runs = 500
	for i := 0; i < runs; i++ {
		upstreamRead, upstreamWrite := io.Pipe()
		downstreamRead, downstreamWrite := io.Pipe()
		client := NewClient(upstreamRead, downstreamWrite, ClientOptions{})
		go func() {
			for {
				message, ok := <-client.Inbound()
				if !ok {
					return
				}
				if message.Barrier != nil {
					close(message.Barrier)
				}
			}
		}()
		go func() {
			line, err := bufio.NewReader(downstreamRead).ReadString('\n')
			if err != nil {
				return
			}
			request, err := ParseMessage([]byte(strings.TrimSuffix(line, "\n")))
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = upstreamWrite.Write([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"` + request.RequestID + `","response":{}}}` + "\n{not json\n"))
			_ = upstreamWrite.Close()
		}()
		if err := client.Call(context.Background(), json.RawMessage(`{"subtype":"initialize","hooks":null}`), nil); err != nil {
			t.Fatalf("run %d: the response lost to the transport's death: %v", i, err)
		}

		<-client.ReadDone()
		if client.Err() == nil {
			t.Fatalf("run %d: the malformed frame did not retire the client", i)
		}
		_ = client.Close()
		_ = downstreamRead.Close()
		_ = upstreamRead.Close()
	}
}

func TestClientWriteBlockedWithoutCloserReturnsWhenReaderRetires(t *testing.T) {
	upstreamRead, upstreamWrite := io.Pipe()
	blocked := &blockedPumpWriter{entered: make(chan struct{}), release: make(chan struct{})}
	defer close(blocked.release)
	client := NewClient(upstreamRead, blocked, ClientOptions{})
	result := make(chan error, 1)
	go func() {
		result <- client.Call(context.Background(), json.RawMessage(`{"subtype":"initialize","hooks":null}`), nil)
	}()

	select {
	case <-blocked.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("pump never entered Encode")
	}

	if _, err := upstreamWrite.Write([]byte("{not json\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("call reported success for a frame that never fully left")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call hung inside write on a pump nothing can unblock")
	}
	_ = upstreamWrite.Close()
}

func TestClientRefusesResponseWhenBarrierCannotBeEnqueued(t *testing.T) {
	upstreamRead, upstreamWrite := io.Pipe()
	downstreamRead, downstreamWrite := io.Pipe()
	client := NewClient(upstreamRead, downstreamWrite, ClientOptions{QueueCapacity: 1})
	result := make(chan error, 1)
	go func() {
		result <- client.Call(context.Background(), json.RawMessage(`{"subtype":"initialize","hooks":null}`), nil)
	}()
	line, err := bufio.NewReader(downstreamRead).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	request, err := ParseMessage([]byte(strings.TrimSuffix(line, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, downstreamRead) }()

	if _, err := upstreamWrite.Write([]byte(`{"type":"command_lifecycle","command_uuid":"u1","state":"queued","session_id":"s1","uuid":"c1"}` + "\n" + `{"type":"control_response","response":{"subtype":"success","request_id":"` + request.RequestID + `","response":{}}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrObservationQueue) {
			t.Fatalf("call settled with %v, want the queue overflow", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call never settled")
	}
	_ = upstreamWrite.Close()
	_ = downstreamRead.Close()
}

type transportCloseCounter struct {
	inner io.Closer
	calls atomic.Int32
}

func (c *transportCloseCounter) Close() error { c.calls.Add(1); return c.inner.Close() }

func TestClientClosesTransportExactlyOnce(t *testing.T) {
	for _, order := range []string{"close-then-shutdown", "shutdown-then-close"} {
		reader, writer := io.Pipe()
		closer := &transportCloseCounter{inner: reader}
		client := NewClient(reader, io.Discard, ClientOptions{CloseReadWriter: closer})

		if order == "close-then-shutdown" {
			_ = client.Close()
			client.shutdown(errors.New("late"))
		} else {
			client.shutdown(errors.New("direct"))
			_ = client.Close()
		}

		<-client.ReadDone()
		if n := closer.calls.Load(); n != 1 {
			t.Fatalf("%s: transport closed %d times, want exactly once", order, n)
		}
		_ = writer.Close()
	}
}

var errEncodeAfterDelivery = errors.New("encode failed after the frame was delivered")

type releasedFailingWriter struct {
	inner   io.Writer
	release chan struct{}
}

func (w *releasedFailingWriter) Write(p []byte) (int, error) {
	n, err := w.inner.Write(p)
	if err != nil {
		return n, err
	}
	<-w.release
	return n, errEncodeAfterDelivery
}

func TestClientCallSettlesOnResponseWhenEncodeFailsAfterDelivery(t *testing.T) {
	upstreamRead, upstreamWrite := io.Pipe()
	downstreamRead, downstreamWrite := io.Pipe()
	writer := &releasedFailingWriter{inner: downstreamWrite, release: make(chan struct{})}
	client := NewClient(upstreamRead, writer, ClientOptions{QueueCapacity: 8})
	inbound := client.Inbound()
	result := make(chan error, 1)
	go func() {
		result <- client.Call(context.Background(), json.RawMessage(`{"subtype":"initialize","hooks":null}`), nil)
	}()
	line, err := bufio.NewReader(downstreamRead).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	request, err := ParseMessage([]byte(strings.TrimSuffix(line, "\n")))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := upstreamWrite.Write([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"` + request.RequestID + `","response":{}}}` + "\n")); err != nil {
		t.Fatal(err)
	}

	select {
	case message := <-inbound:
		if message.Barrier == nil {
			t.Fatalf("expected the response barrier first, got %+v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no barrier was delivered")
	}
	close(writer.release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("the answered frame lost to its encode failure: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call never settled")
	}
	_ = upstreamWrite.Close()
	_ = downstreamRead.Close()
}

type blockedPumpWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockedPumpWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}

func TestPumpSettlesIsPerRequest(t *testing.T) {
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	client := NewClient(reader, io.Discard, ClientOptions{})
	past := writeRequest{started: make(chan struct{}), encoded: make(chan struct{}), result: make(chan error, 1)}
	close(past.started)
	close(past.encoded)
	blocked := writeRequest{started: make(chan struct{}), encoded: make(chan struct{}), result: make(chan error, 1)}
	close(blocked.started)
	if !client.pumpSettles(past) {
		t.Fatal("a frame past Encode must settle on its own result")
	}
	if client.pumpSettles(blocked) {
		t.Fatal("a frame still inside Encode without a closer must not be waited on")
	}
}

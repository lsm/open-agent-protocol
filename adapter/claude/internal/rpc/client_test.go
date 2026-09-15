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

	"github.com/lsm/open-agent-protocol/adapter/claude/internal/native"
)

// peer is a scripted stream-json counterpart over real pipes. io.Pipe writes
// block until consumed, so a dedicated reader drains the client's writes
// into a channel the test receives from.
type peer struct {
	t       *testing.T
	client  *Client
	writeIn io.Writer
	frames  chan Message
}

func newPeer(t *testing.T) *peer {
	t.Helper()
	upstreamRead, upstreamWrite := io.Pipe()     // peer -> client
	downstreamRead, downstreamWrite := io.Pipe() // client -> peer
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

// readFrame returns the next frame the client wrote.
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

// observe drains one inbound message, failing on anything else.
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

	// The control request must be on the wire before the response is sent.
	request, err := p.readFrame()
	if err != nil {
		t.Fatal(err)
	}
	if request.Kind != KindControlRequest || request.RequestID == "" {
		t.Fatalf("request = %+v", request)
	}
	// Turn frames stream while the request is in flight; the barrier must
	// order the call's return behind them.
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
	// The reference hosts ignore responses for ids they are not waiting on;
	// the client must stay alive and keep reducing observations.
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
	// Control responses barrier behind wire-earlier frames; acknowledge the
	// barriers continuously so concurrent calls can interleave.
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
	// A single dispatcher answers each request by echoing its tag, so every
	// caller can verify it received its own response — a mis-delivered
	// response fails whichever caller got it.
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
	// The parked goroutines end when the client retires (t.Cleanup) or their
	// own deadlines; all four calls have already returned.
	_ = consumerDone
	_ = dispatcherDone
}

// Regression: a control response parsed off the ordered stream before the
// transport died must win over the death. shutdown used to retire the pending
// map while the reader was still parked in the response barrier, so the
// genuine reply — already decoded from the wire — was discarded and the call
// returned the shutdown reason instead.
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
	// Take the barrier without acknowledging it: the reader is now parked
	// holding a fully decoded response for this control request.
	select {
	case message := <-p.client.Inbound():
		if message.Barrier == nil {
			t.Fatalf("expected the response barrier first, got %+v", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no barrier was delivered")
	}
	// The transport dies underneath the parked reader.
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

// A client built without a CloseReadWriter cannot interrupt a reader parked
// in Decode, so settling the pending calls must never wait for the reader to
// stop: when one call's cancellation retires the client, the other pending
// call has to fail promptly instead of hanging until the peer closes stdout.
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
	// Both requests are on the wire; nothing ever answers them.
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

// The malformed-frame corpus shape at the rpc layer: the peer answers the
// control request, then writes a corrupt line and exits. Both the response and
// the transport's death sit on one ordered stream, back to back, and the
// response must win — including when the caller's write is still waiting for
// the pump's result as the reader retires the client.
func TestClientCallSurvivesResponseFollowedByMalformedFrame(t *testing.T) {
	const runs = 500
	for i := 0; i < runs; i++ {
		upstreamRead, upstreamWrite := io.Pipe()     // peer -> client
		downstreamRead, downstreamWrite := io.Pipe() // client -> peer
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
		// The call may return before the reader reaches the corrupt line;
		// judge the retirement only once the reader has stopped.
		<-client.ReadDone()
		if client.Err() == nil {
			t.Fatalf("run %d: the malformed frame did not retire the client", i)
		}
		_ = client.Close()
		_ = downstreamRead.Close()
		_ = upstreamRead.Close()
	}
}

// A pump blocked inside Encode — the peer stopped reading stdin — cannot be
// woken without a closer. When the reader then retires the client, a caller
// waiting on that frame must be released with the death rather than held
// until the peer happens to exit.
func TestClientWriteBlockedWithoutCloserReturnsWhenReaderRetires(t *testing.T) {
	upstreamRead, upstreamWrite := io.Pipe() // peer -> client
	blocked := &blockedPumpWriter{entered: make(chan struct{}), release: make(chan struct{})}
	defer close(blocked.release)
	client := NewClient(upstreamRead, blocked, ClientOptions{})
	result := make(chan error, 1)
	go func() {
		result <- client.Call(context.Background(), json.RawMessage(`{"subtype":"initialize","hooks":null}`), nil)
	}()
	// Wait for the pump to be inside Encode, blocked in the writer.
	select {
	case <-blocked.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("pump never entered Encode")
	}
	// The peer emits garbage: the reader retires the client while the pump
	// is still blocked.
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

// A response whose ordering barrier cannot be enqueued must not be delivered:
// the client is retiring on queue overflow with wire-earlier observations
// still unreduced, so releasing the reply would let it overtake them. Only a
// barrier that was enqueued and then overtaken by the transport's death
// releases the response.
func TestClientRefusesResponseWhenBarrierCannotBeEnqueued(t *testing.T) {
	upstreamRead, upstreamWrite := io.Pipe()     // peer -> client
	downstreamRead, downstreamWrite := io.Pipe() // client -> peer
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
	// One wire-earlier observation fills the queue; the response's barrier
	// then cannot be enqueued.
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

// transportCloseCounter records how many times the client closed the
// supplied CloseReadWriter. io.Closer does not promise idempotence, so a
// retirement must close it exactly once whichever path reaches it first.
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
		// The reader's own retirement on the closed pipe must not close it
		// again either.
		<-client.ReadDone()
		if n := closer.calls.Load(); n != 1 {
			t.Fatalf("%s: transport closed %d times, want exactly once", order, n)
		}
		_ = writer.Close()
	}
}

var errEncodeAfterDelivery = errors.New("encode failed after the frame was delivered")

// releasedFailingWriter forwards each frame to the peer, then holds the write
// open until released and reports a failure for it: a frame the peer received
// and answered whose Encode nevertheless returned an error.
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

// A frame the peer received and answered must settle on its response even when
// Encode reports a failure for it: the client retires before the failure is
// published, so the caller settles on its channel rather than removing a
// pending id whose reply is already in hand.
//
// Unlike the other regression tests here this one also passes against the
// client it fixes: there the pump published the failure before closing done,
// and losing that window needs the caller scheduled in the instant between the
// two, which 300 runs never hit. It guards the invariant rather than
// reproducing the defect.
func TestClientCallSettlesOnResponseWhenEncodeFailsAfterDelivery(t *testing.T) {
	upstreamRead, upstreamWrite := io.Pipe()     // peer -> client
	downstreamRead, downstreamWrite := io.Pipe() // client -> peer
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
	// The peer answers while the pump still holds the write open.
	if _, err := upstreamWrite.Write([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"` + request.RequestID + `","response":{}}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	// Take the barrier without acknowledging it: the reply is decoded and in
	// hand while the pump still holds the write open.
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

// blockedPumpWriter reports when the pump has entered Write and holds it there
// until the test releases it: a peer that stopped reading stdin.
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

// The settle decision belongs to the request, not to the pump: a later frame
// blocked in Encode says nothing about a frame that already left it. With a
// shared flag, A's caller reported the transport's death for a request the
// peer had received in full because B happened to be encoding.
func TestPumpSettlesIsPerRequest(t *testing.T) {
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	client := NewClient(reader, io.Discard, ClientOptions{}) // no closer
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

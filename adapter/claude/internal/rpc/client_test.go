package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
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
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := make(chan error, 1)
			go func() {
				result <- p.client.Call(context.Background(), json.RawMessage(`{"subtype":"interrupt"}`), nil)
			}()
			request, err := p.readFrame()
			if err != nil {
				t.Error(err)
				return
			}
			p.send(`{"type":"control_response","response":{"subtype":"success","request_id":"` + request.RequestID + `","response":{"still_queued":[]}}}`)
			select {
			case err := <-result:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("call timed out")
			}
		}()
	}
	wg.Wait()
	// The consumer goroutine parks until the client retires (t.Cleanup
	// closes it) or its own deadline; all four calls have already returned,
	// so nothing is left to assert on it.
	_ = consumerDone
}

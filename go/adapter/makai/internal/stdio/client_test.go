package stdio

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/adapter/makai/internal/native"
)

func TestClientCorrelatesAndOrdersResponse(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	c := NewClient(clientSide, clientSide, ClientOptions{CloseReadWriter: clientSide, QueueCapacity: 8})
	defer c.Close()
	request := testEnvelope(t, native.TypeAgentStatus, "01ARZ3NDEKTSV4RRFFQ69G5FAV", 1, native.AgentStatusRequest{SessionID: "Abcdefghijklmnopqrstu"})
	observation := testEnvelope(t, native.TypeAgentEvent, "01ARZ3NDEKTSV4RRFFQ69G5FAW", 1, native.AgentEvent{EventJSON: `{"type":"agent_start","payload":{}}`})
	response := testEnvelope(t, native.TypeSessionInfo, "01ARZ3NDEKTSV4RRFFQ69G5FAX", 2, native.SessionInfo{SessionID: "Abcdefghijklmnopqrstu", Status: native.StatusReady, Model: "x"})
	reply := request.MessageID
	response.InReplyTo = &reply
	serverDone := make(chan error, 1)
	go func() {
		d := NewDecoder(serverSide, 0)
		if _, err := d.Decode(); err != nil {
			serverDone <- err
			return
		}
		e := NewEncoder(serverSide)
		if err := e.Encode(observation); err != nil {
			serverDone <- err
			return
		}
		serverDone <- e.Encode(response)
	}()
	result := make(chan native.Envelope, 1)
	errs := make(chan error, 1)
	go func() {
		env, err := c.Call(context.Background(), request, native.TypeSessionInfo, native.TypeAgentError)
		result <- env
		errs <- err
	}()
	in := <-c.Inbound()
	if in.Envelope == nil || in.Envelope.MessageID != observation.MessageID {
		t.Fatalf("unexpected inbound %+v", in)
	}
	select {
	case <-result:
		t.Fatal("response crossed semantic barrier")
	case <-time.After(20 * time.Millisecond):
	}
	barrier := <-c.Inbound()
	if barrier.Barrier == nil {
		t.Fatal("missing barrier")
	}
	close(barrier.Barrier)
	if env := <-result; env.MessageID != response.MessageID {
		t.Fatalf("got %s", env.MessageID)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestClientToleratesAllocatedSequenceGapsAndReorder(t *testing.T) {

	frames := []string{
		strings.Replace(strings.Replace(strings.Replace(testLine, `"type":"ping"`, `"type":"agent_event"`, 1), `"sequence":1`, `"sequence":2`, 1), `"payload":{}`, `"payload":{"event_json":"{}"}`, 1),
		strings.Replace(strings.Replace(strings.Replace(strings.Replace(testLine, `"type":"ping"`, `"type":"agent_event"`, 1), `"sequence":1`, `"sequence":4`, 1), `01ARZ3NDEKTSV4RRFFQ69G5FAV`, `01ARZ3NDEKTSV4RRFFQ69G5FAX`, 1), `"payload":{}`, `"payload":{"event_json":"{}"}`, 1),
		strings.Replace(strings.Replace(strings.Replace(strings.Replace(testLine, `"type":"ping"`, `"type":"agent_event"`, 1), `"sequence":1`, `"sequence":3`, 1), `01ARZ3NDEKTSV4RRFFQ69G5FAV`, `01ARZ3NDEKTSV4RRFFQ69G5FAY`, 1), `"payload":{}`, `"payload":{"event_json":"{}"}`, 1),
	}
	c := NewClient(strings.NewReader(strings.Join(frames, "\n")+"\n"), io.Discard, ClientOptions{})
	for i, want := range []uint64{2, 4, 3} {
		in := <-c.Inbound()
		if in.Envelope == nil || in.Envelope.Sequence != want {
			t.Fatalf("frame %d: inbound=%+v want sequence %d", i+1, in.Envelope, want)
		}
	}
	select {
	case <-c.Done():
		if !errors.Is(c.Err(), io.EOF) {
			t.Fatalf("closed with %v", c.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("reader did not finish")
	}
}

func TestClientRejectsSequenceAndCorrelationFaults(t *testing.T) {
	cases := []struct {
		name string
		line string
		want error
	}{
		{"zero sequence", strings.Replace(strings.Replace(strings.Replace(testLine, `"type":"ping"`, `"type":"agent_error"`, 1), `"sequence":1`, `"sequence":0`, 1), `"payload":{}`, `"payload":{"code":"internal_error","message":"boom"}`, 1) + "\n", ErrSequence},
		{"unmatched reply", strings.Replace(testLine, `"payload":{}`, `"in_reply_to":"01ARZ3NDEKTSV4RRFFQ69G5FAW","payload":{}`, 1) + "\n", ErrReplyNotPending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient(strings.NewReader(tc.line), io.Discard, ClientOptions{})
			select {
			case <-c.Done():
			case <-time.After(time.Second):
				t.Fatal("client did not close")
			}
			if !errors.Is(c.Err(), tc.want) {
				t.Fatalf("got %v want %v", c.Err(), tc.want)
			}
		})
	}
}

func TestClientDuplicateMessageIDCloses(t *testing.T) {
	input := testLine + "\n" + testLine + "\n"
	c := NewClient(strings.NewReader(input), io.Discard, ClientOptions{QueueCapacity: 4})
	<-c.Inbound()
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("did not close")
	}
	if !errors.Is(c.Err(), ErrDuplicateMessageID) {
		t.Fatalf("got %v", c.Err())
	}
}
func TestClientBoundedInbound(t *testing.T) {
	second := strings.Replace(testLine, "01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW", 1)
	second = strings.Replace(second, `"sequence":1`, `"sequence":2`, 1)
	c := NewClient(strings.NewReader(testLine+"\n"+second+"\n"), io.Discard, ClientOptions{QueueCapacity: 1})
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("did not close")
	}
	if !errors.Is(c.Err(), ErrInboundQueue) {
		t.Fatalf("got %v", c.Err())
	}
}

func TestClientFullInboundUnblocksCorrelatedCall(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	c := NewClient(clientSide, clientSide, ClientOptions{CloseReadWriter: clientSide, QueueCapacity: 1})
	defer c.Close()
	request := testEnvelope(t, native.TypeAgentStatus, "01ARZ3NDEKTSV4RRFFQ69G5FAV", 1, native.AgentStatusRequest{SessionID: "Abcdefghijklmnopqrstu"})
	observation := testEnvelope(t, native.TypeAgentEvent, "01ARZ3NDEKTSV4RRFFQ69G5FAW", 1, native.AgentEvent{EventJSON: `{}`})
	response := testEnvelope(t, native.TypeSessionInfo, "01ARZ3NDEKTSV4RRFFQ69G5FAX", 2, native.SessionInfo{SessionID: "Abcdefghijklmnopqrstu", Status: native.StatusReady})
	response.InReplyTo = &request.MessageID
	go func() {
		d := NewDecoder(serverSide, 0)
		_, _ = d.Decode()
		e := NewEncoder(serverSide)
		_ = e.Encode(observation)
		_ = e.Encode(response)
	}()
	result := make(chan error, 1)
	go func() {
		_, err := c.Call(context.Background(), request, native.TypeSessionInfo)
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, ErrInboundQueue) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("correlated call remained blocked after queue overflow")
	}
}
func TestClientRoutesCorrelatedAgentMessageErrorAsObservation(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	c := NewClient(clientSide, clientSide, ClientOptions{CloseReadWriter: clientSide, QueueCapacity: 4})
	defer c.Close()
	request := testEnvelope(t, native.TypeAgentMessage, "01ARZ3NDEKTSV4RRFFQ69G5FAV", 2, native.AgentMessage{SessionID: "Abcdefghijklmnopqrstu", MessageJSON: `{}`})
	go func() {
		d := NewDecoder(serverSide, 0)
		_, _ = d.Decode()
		_, _ = io.WriteString(serverSide, `{"type":"agent_error","session_id":"Abcdefghijklmnopqrstu","message_id":"01ARZ3NDEKTSV4RRFFQ69G5FAW","sequence":0,"timestamp":1,"version":1,"in_reply_to":"01ARZ3NDEKTSV4RRFFQ69G5FAV","payload":{"code":"internal_error","message":"failed"}}`+"\n")
	}()
	if err := c.Send(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	select {
	case in := <-c.Inbound():
		if in.Envelope == nil || in.Envelope.Type != native.TypeAgentError || in.Envelope.InReplyTo == nil || *in.Envelope.InReplyTo != request.MessageID {
			t.Fatalf("unexpected inbound: %+v", in)
		}
	case <-time.After(time.Second):
		t.Fatal("missing correlated agent error")
	}
	select {
	case <-c.Done():
		t.Fatalf("transport closed: %v", c.Err())
	default:
	}
}

func TestClientConcurrentWritesRemainFrames(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	c := NewClient(clientSide, clientSide, ClientOptions{CloseReadWriter: clientSide, QueueCapacity: 32})
	defer c.Close()
	const n = 12
	got := make(chan error, 1)
	go func() {
		d := NewDecoder(serverSide, 0)
		for i := 0; i < n; i++ {
			if _, err := d.Decode(); err != nil {
				got <- err
				return
			}
		}
		got <- nil
	}()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := native.MessageID([]string{"01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW", "01ARZ3NDEKTSV4RRFFQ69G5FAX", "01ARZ3NDEKTSV4RRFFQ69G5FAY", "01ARZ3NDEKTSV4RRFFQ69G5FAZ", "01ARZ3NDEKTSV4RRFFQ69G5FB0", "01ARZ3NDEKTSV4RRFFQ69G5FB1", "01ARZ3NDEKTSV4RRFFQ69G5FB2", "01ARZ3NDEKTSV4RRFFQ69G5FB3", "01ARZ3NDEKTSV4RRFFQ69G5FB4", "01ARZ3NDEKTSV4RRFFQ69G5FB5", "01ARZ3NDEKTSV4RRFFQ69G5FB6"}[i])
			if err := c.Send(context.Background(), testEnvelope(t, native.TypePing, id, uint64(i+1), native.Empty{})); err != nil {
				t.Errorf("send: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if err := <-got; err != nil {
		t.Fatal(err)
	}
}
func TestCallCancellationRetiresTransport(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	c := NewClient(a, a, ClientOptions{CloseReadWriter: a})
	req := testEnvelope(t, native.TypeAgentStatus, "01ARZ3NDEKTSV4RRFFQ69G5FAV", 1, native.AgentStatusRequest{SessionID: "Abcdefghijklmnopqrstu"})
	go io.Copy(io.Discard, b)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.Call(ctx, req, native.TypeSessionInfo)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("transport not retired")
	}
}

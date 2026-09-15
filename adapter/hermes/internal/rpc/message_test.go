package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestParseMessageAcceptsPinnedShapes(t *testing.T) {
	cases := []struct {
		name  string
		frame string
		kind  MessageKind
	}{
		{"response", `{"jsonrpc":"2.0","id":1,"result":{"status":"streaming"}}`, MessageResponse},
		{"error", `{"jsonrpc":"2.0","id":"a","error":{"code":4001,"message":"session not found","data":{"x":1}}}`, MessageError},
		{"event notification", `{"jsonrpc":"2.0","method":"event","params":{"type":"message.start","session_id":"s","seq":1}}`, MessageNotification},
		{"global notification", `{"jsonrpc":"2.0","method":"event","params":{"type":"gateway.ready","payload":{"skin":{},"change_events":true,"replay_epoch":"0123456789abcdef0123456789abcdef"}}}`, MessageNotification},
	}
	for _, test := range cases {
		message, err := ParseMessage([]byte(test.frame))
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if message.Kind != test.kind {
			t.Fatalf("%s: kind %d", test.name, message.Kind)
		}
	}
}

func TestParseMessageRejectsNativeToleratedInput(t *testing.T) {
	cases := map[string]string{
		"leading space":         ` {"jsonrpc":"2.0","id":1,"result":{}}`,
		"trailing CR":           "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\r",
		"non-object":            `[1,2]`,
		"trailing value":        `{"jsonrpc":"2.0","id":1,"result":{}} {"x":1}`,
		"duplicate key":         `{"jsonrpc":"2.0","id":1,"id":2,"result":{}}`,
		"nested duplicate key":  `{"jsonrpc":"2.0","id":1,"result":{"a":1,"a":2}}`,
		"null id":               `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`,
		"no jsonrpc":            `{"id":1,"result":{}}`,
		"wrong version":         `{"jsonrpc":"1.0","id":1,"result":{}}`,
		"unknown member":        `{"jsonrpc":"2.0","id":1,"result":{},"extra":true}`,
		"array params":          `{"jsonrpc":"2.0","id":1,"method":"m","params":[1]}`,
		"result and error":      `{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":1,"message":"m"}}`,
		"method with response":  `{"jsonrpc":"2.0","id":1,"method":"m","result":{}}`,
		"error without message": `{"jsonrpc":"2.0","id":1,"error":{"code":1}}`,
		"string error code":     `{"jsonrpc":"2.0","id":1,"error":{"code":"x","message":"m"}}`,
	}
	for name, frame := range cases {
		if _, err := ParseMessage([]byte(frame)); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func TestParseMessageAcceptsNullParams(t *testing.T) {
	message, err := ParseMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"m","params":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if message.Kind != MessageRequest || string(message.Params) != "null" {
		t.Fatalf("message = %+v params=%s", message, message.Params)
	}
}

func TestMessageRoundTrip(t *testing.T) {
	id := IntegerID(7)
	data, err := json.Marshal(Request(id, "prompt.submit", json.RawMessage(`{"session_id":"s","text":"hi"}`)))
	if err != nil {
		t.Fatal(err)
	}
	// MarshalJSON builds from a map, so members land in Go's sorted key order.
	want := `{"id":7,"jsonrpc":"2.0","method":"prompt.submit","params":{"session_id":"s","text":"hi"}}`
	if string(data) != want {
		t.Fatalf("encoded %s", data)
	}
	parsed, err := ParseMessage(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Method != "prompt.submit" || parsed.ID != id {
		t.Fatalf("parsed %+v", parsed)
	}
	if _, err := json.Marshal(Request(StringID(""), "m", nil)); err == nil {
		t.Fatal("empty string id marshalled")
	}
}

func TestDecoderFraming(t *testing.T) {
	split := "{\"jsonrpc\":\"2.0\",\"id\":1,\"re" + "sult\":{\"text\":\"héllo\"}}\n"
	decoder := NewDecoder(strings.NewReader(split), 0)
	message, err := decoder.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if message.Kind != MessageResponse {
		t.Fatalf("kind %d", message.Kind)
	}
	for name, stream := range map[string]string{
		"empty frame":    "\n",
		"CR frame":       "{}\r\n",
		"unterminated":   "{}",
		"bare scalar":    "42\n",
		"stacked frames": "{}\n{}\n",
	} {
		if _, err := NewDecoder(strings.NewReader(stream), 0).Decode(); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	if _, err := NewDecoder(strings.NewReader(strings.Repeat("a", 32)+"\n"), 16).Decode(); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversized frame err = %v", err)
	}
}

func TestClientOrdersNotificationBeforeResponseBarrier(t *testing.T) {
	// Two pipes so the test can sequence the server side deterministically:
	// once the outbound request has been read from the wire, its pending
	// entry is registered, and the reply frames can follow in order.
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	client := NewClient(clientReader, clientWriter, ClientOptions{QueueCapacity: 8})
	inbound := client.Inbound()
	type outcome struct {
		value any
		err   error
	}
	results := make(chan outcome, 1)
	go func() {
		var result struct {
			Status string `json:"status"`
		}
		err := client.CallID(context.Background(), IntegerID(1), "prompt.submit", nil, &result)
		results <- outcome{result, err}
	}()
	// Drain the request from the client→server pipe (registration is done).
	request := make([]byte, 64)
	if _, err := io.ReadFull(serverReader, request[:1]); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, serverReader) }()
	// Wire order: an event notification, then the response to request 1.
	_, _ = serverWriter.Write([]byte("{\"jsonrpc\":\"2.0\",\"method\":\"event\",\"params\":{\"type\":\"message.start\",\"session_id\":\"s\",\"seq\":1}}\n"))
	_, _ = serverWriter.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"status":"streaming"}}` + "\n"))
	var observed []string
	for len(observed) < 2 {
		select {
		case message := <-inbound:
			switch {
			case message.Barrier != nil:
				// The response must wait until the notification ahead of it
				// has been consumed.
				if len(observed) != 1 {
					t.Fatalf("barrier after %d observations", len(observed))
				}
				close(message.Barrier)
				observed = append(observed, "barrier")
			case message.Notification != nil:
				observed = append(observed, "notification")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("reader stalled")
		}
	}
	select {
	case result := <-results:
		if result.err != nil {
			t.Fatal(result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("response never delivered")
	}
	client.Close()
}

// A relay acquires the ordered inbound stream lazily. If it needed the route
// lock, it would deadlock against a reader parked in a response barrier, which
// holds that lock while it waits for the relay to acknowledge the barrier.
func TestClientInboundDoesNotBlockWhileReaderWaitsOnBarrier(t *testing.T) {
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	client := NewClient(clientReader, clientWriter, ClientOptions{QueueCapacity: 8})
	// Activate the ordered stream up front, as the process handshake does when
	// it consumes the ready frame.
	inbound := client.Inbound()
	results := make(chan error, 1)
	go func() {
		results <- client.CallID(context.Background(), IntegerID(1), "prompt.submit", nil, nil)
	}()
	if _, err := io.ReadFull(serverReader, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, serverReader) }()
	if _, err := serverWriter.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	// Consume the barrier without acknowledging it, so the reader stays parked
	// inside route() holding the route lock.
	select {
	case message := <-inbound:
		if message.Barrier == nil {
			t.Fatalf("expected the response barrier first, got %+v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no barrier was delivered")
	}
	acquired := make(chan struct{})
	go func() { _ = client.Inbound(); close(acquired) }()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("Inbound blocked while the reader held the route lock")
	}
	client.Close()
}

func TestClientFailsClosedOnUnmatchedResponse(t *testing.T) {
	reader, writer := io.Pipe()
	client := NewClient(reader, writer, ClientOptions{})
	inbound := client.Inbound()
	_, _ = writer.Write([]byte(`{"jsonrpc":"2.0","id":99,"result":{}}` + "\n"))
	// Responses route through the ordering barrier: acknowledge it so the
	// unmatched delivery is reached and the client retires fail-closed.
	select {
	case message := <-inbound:
		if message.Barrier != nil {
			close(message.Barrier)
		}
	case <-time.After(time.Second):
		t.Fatal("barrier never arrived")
	}
	select {
	case <-client.Done():
	case <-time.After(time.Second):
		t.Fatal("unmatched response did not retire the client")
	}
}

func TestClientRejectsWriteAfterClose(t *testing.T) {
	reader, writer := io.Pipe()
	client := NewClient(reader, writer, ClientOptions{})
	_ = client.Inbound()
	client.Close()
	if err := client.Call(context.Background(), "m", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v", err)
	}
}

func TestClientSurvivesSequentialResponses(t *testing.T) {
	// Regression: route() inverted deliver()'s continuation contract, so the
	// read loop retired the client after the FIRST successfully delivered
	// response. Every earlier test issued at most one response per client, so
	// only a second sequential call exposed it.
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	client := NewClient(clientReader, clientWriter, ClientOptions{QueueCapacity: 8})
	defer client.Close()
	// Acknowledge ordering barriers as they arrive so responses release.
	go func() {
		for message := range client.Inbound() {
			if message.Barrier != nil {
				close(message.Barrier)
			}
		}
	}()
	server := bufio.NewReader(serverReader)
	call := func(id int64, reply string, wantErr bool) {
		results := make(chan error, 1)
		go func() {
			var result struct {
				Status string `json:"status"`
			}
			results <- client.CallID(context.Background(), IntegerID(id), "prompt.submit", nil, &result)
		}()
		// Consume the request line before answering it.
		if _, err := server.ReadBytes('\n'); err != nil {
			t.Fatal(err)
		}
		if _, err := serverWriter.Write([]byte(reply + "\n")); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-results:
			if wantErr != (err != nil) {
				t.Fatalf("call %d: err = %v, wantErr = %v", id, err, wantErr)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("call %d never settled", id)
		}
	}
	call(1, `{"id":1,"jsonrpc":"2.0","result":{"status":"streaming"}}`, false)
	call(2, `{"id":2,"jsonrpc":"2.0","result":{"status":"streaming"}}`, false)
	call(3, `{"id":3,"jsonrpc":"2.0","error":{"code":5004,"message":"busy"}}`, true)
	select {
	case <-client.Done():
		t.Fatal("client retired after sequential responses")
	default:
	}
}

// Regression: a response parsed off the ordered stream before the transport
// died must win over the death. shutdown used to retire the pending map while
// the reader was still parked in the response barrier, so the genuine reply —
// already decoded from the wire — was discarded, the call returned the
// shutdown reason, and deliver raised ErrResponseNotFound over the real cause.
func TestClientDeliversResponseParkedInBarrierWhenTransportRetires(t *testing.T) {
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	client := NewClient(clientReader, clientWriter, ClientOptions{QueueCapacity: 8})
	inbound := client.Inbound()
	results := make(chan error, 1)
	go func() {
		var result struct {
			Status string `json:"status"`
		}
		err := client.CallID(context.Background(), IntegerID(1), "prompt.submit", nil, &result)
		if err == nil && result.Status != "streaming" {
			err = fmt.Errorf("status = %q", result.Status)
		}
		results <- err
	}()
	// Drain the whole request line, then push a notification through the same
	// serialized writer and drain that too. The writer hands back each frame's
	// result before it accepts the next, so once the notification is on the
	// wire the call is certainly parked on its response rather than still
	// inside a write that the close would fail.
	serverLines := bufio.NewReader(serverReader)
	if _, err := serverLines.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	synced := make(chan error, 1)
	go func() { synced <- client.Notify(context.Background(), "sync", nil) }()
	if _, err := serverLines.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if err := <-synced; err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, serverLines) }()
	if _, err := serverWriter.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"status":"streaming"}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	// Take the barrier without acknowledging it: the reader is now parked
	// holding a fully decoded response for request 1.
	select {
	case message := <-inbound:
		if message.Barrier == nil {
			t.Fatalf("expected the response barrier first, got %+v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no barrier was delivered")
	}
	// The transport dies underneath the parked reader.
	client.Close()
	select {
	case err := <-results:
		if err != nil {
			t.Fatalf("the parked response lost to the transport's death: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call never settled")
	}
	if err := client.Err(); errors.Is(err, ErrResponseNotFound) {
		t.Fatalf("a concurrently retired id was reported as unmatched: %v", err)
	}
	_ = serverWriter.Close()
}

// The malformed-frame corpus shape at the rpc layer: the gateway answers the
// request, then writes a corrupt line and exits. Both the response and the
// transport's death sit on one ordered stream, back to back, and the response
// must win. Before the fix roughly one run in five settled on the death
// instead — the pump had put the request on the wire but the caller's write
// was still waiting for the pump's result when the reader retired the client.
func TestClientCallSurvivesResponseFollowedByMalformedFrame(t *testing.T) {
	const runs = 500
	for i := 0; i < runs; i++ {
		serverReader, clientWriter := io.Pipe()
		clientReader, serverWriter := io.Pipe()
		client := NewClient(clientReader, clientWriter, ClientOptions{QueueCapacity: 8, CloseReadWriter: &pipeCloser{read: clientReader, write: clientWriter}})
		inbound := client.Inbound()
		go func() {
			for {
				select {
				case message := <-inbound:
					if message.Barrier != nil {
						close(message.Barrier)
					}
				case <-client.ReadDone():
					return
				}
			}
		}()
		go func() {
			_, _ = bufio.NewReader(serverReader).ReadString('\n')
			_, _ = serverWriter.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}` + "\n{not json\n"))
			_ = serverWriter.Close()
			_ = serverReader.Close()
		}()
		if err := client.CallID(context.Background(), IntegerID(1), "prompt.submit", nil, nil); err != nil {
			t.Fatalf("run %d: the response lost to the transport's death: %v", i, err)
		}
		// The call may return before the reader reaches the corrupt line;
		// judge the retirement cause only once the reader has stopped.
		<-client.ReadDone()
		if err := client.Err(); !errors.Is(err, ErrInvalidMessage) {
			t.Fatalf("run %d: retirement cause = %v, want the malformed frame", i, err)
		}
		_ = client.Close()
	}
}

// A client built without a CloseReadWriter cannot interrupt a reader parked
// in Decode, so settling the pending calls must never wait for the reader to
// stop: when one call's cancellation retires the client, the other pending
// call has to fail promptly instead of hanging until the peer closes stdout.
func TestClientFailsPendingCallsWithoutCloserWhenAnotherCallCancels(t *testing.T) {
	serverReader, clientWriter := io.Pipe()
	clientReader, clientWriterUnused := io.Pipe()
	defer clientWriterUnused.Close()
	client := NewClient(clientReader, clientWriter, ClientOptions{QueueCapacity: 8})
	_ = client.Inbound()
	go func() { _, _ = io.Copy(io.Discard, serverReader) }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := make(chan error, 1)
	go func() { cancelled <- client.CallID(ctx, IntegerID(1), "a", nil, nil) }()
	other := make(chan error, 1)
	go func() { other <- client.CallID(context.Background(), IntegerID(2), "b", nil, nil) }()
	// Both requests are on the wire; nothing ever answers them.
	time.Sleep(20 * time.Millisecond)
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

// A pump blocked inside Encode — the peer stopped reading stdin — cannot be
// woken without a closer. When the reader then retires the client, a caller
// waiting on that frame must be released with the death rather than held
// until the peer happens to exit.
func TestClientWriteBlockedWithoutCloserReturnsWhenReaderRetires(t *testing.T) {
	serverReader, clientWriter := io.Pipe() // never read: Encode blocks
	clientReader, serverWriter := io.Pipe()
	client := NewClient(clientReader, clientWriter, ClientOptions{QueueCapacity: 8})
	result := make(chan error, 1)
	go func() { result <- client.CallID(context.Background(), IntegerID(1), "prompt.submit", nil, nil) }()
	// Wait for the pump to be inside Encode, blocked on the unread pipe.
	deadline := time.Now().Add(2 * time.Second)
	for {
		client.mu.Lock()
		encoding := client.encoding
		client.mu.Unlock()
		if encoding {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pump never entered Encode")
		}
		time.Sleep(time.Millisecond)
	}
	// The peer emits garbage: the reader retires the client while the pump
	// is still blocked.
	if _, err := serverWriter.Write([]byte("{not json\n")); err != nil {
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
	_ = serverReader.Close()
	_ = serverWriter.Close()
}

// A response whose ordering barrier cannot be enqueued must not be delivered:
// the client is retiring on queue overflow with wire-earlier observations
// still unreduced, so releasing the reply would let it overtake them. Only a
// barrier that was enqueued and then overtaken by the transport's death
// releases the response.
func TestClientRefusesResponseWhenBarrierCannotBeEnqueued(t *testing.T) {
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	client := NewClient(clientReader, clientWriter, ClientOptions{QueueCapacity: 1})
	_ = client.Inbound() // ordered stream active, nobody consuming
	result := make(chan error, 1)
	go func() { result <- client.CallID(context.Background(), IntegerID(1), "prompt.submit", nil, nil) }()
	if _, err := bufio.NewReader(serverReader).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, serverReader) }()
	// One wire-earlier observation fills the queue; the response's barrier
	// then cannot be enqueued.
	if _, err := serverWriter.Write([]byte(`{"jsonrpc":"2.0","id":"r1","method":"reverse","params":{}}` + "\n" + `{"jsonrpc":"2.0","id":1,"result":{}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrNotificationQueue) {
			t.Fatalf("call settled with %v, want the queue overflow", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call never settled")
	}
	_ = serverWriter.Close()
	_ = serverReader.Close()
}

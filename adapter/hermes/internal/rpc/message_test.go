package rpc

import (
	"context"
	"encoding/json"
	"errors"
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

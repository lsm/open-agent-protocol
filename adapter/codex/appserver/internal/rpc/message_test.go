package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestRequestIDRoundTrip(t *testing.T) {
	t.Parallel()
	ids := []RequestID{StringID("1"), StringID(""), IntegerID(1), IntegerID(-1), IntegerID(math.MaxInt64), IntegerID(math.MinInt64)}
	for _, id := range ids {
		data, err := json.Marshal(id)
		if err != nil {
			t.Fatal(err)
		}
		var decoded RequestID
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded != id {
			t.Fatalf("round trip: got %#v want %#v", decoded, id)
		}
	}
	if StringID("1") == IntegerID(1) {
		t.Fatal("string and integer request IDs collided")
	}
}

func TestRequestIDRejectsOtherJSONTypes(t *testing.T) {
	t.Parallel()
	for _, input := range []string{`null`, `true`, `1.5`, `9223372036854775808`, `{}`, `[]`} {
		var id RequestID
		if err := json.Unmarshal([]byte(input), &id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("%s: got %v", input, err)
		}
	}
}

func TestParseMessageShapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		kind  MessageKind
	}{
		{`{"id":0,"method":"initialize","params":null}`, MessageRequest},
		{`{"method":"initialized"}`, MessageNotification},
		{`{"id":"x","result":null}`, MessageResponse},
		{`{"id":-1,"error":{"code":-32600,"message":"bad"}}`, MessageError},
	}
	for _, test := range tests {
		message, err := ParseMessage([]byte(test.input))
		if err != nil {
			t.Fatalf("%s: %v", test.input, err)
		}
		if message.Kind != test.kind {
			t.Errorf("%s: got %v want %v", test.input, message.Kind, test.kind)
		}
	}
}

func TestParseMessageRejectsAmbiguousOrForeignFrames(t *testing.T) {
	t.Parallel()
	inputs := []string{
		`[]`, `{}`, `{"id":1}`, `{"result":null}`, `{"method":"x","result":null}`,
		`{"id":1,"result":null,"error":{"code":1,"message":"bad"}}`,
		`{"jsonrpc":"2.0","id":1,"result":null}`, `{"method":""}`, `{"id":null,"method":"x"}`,
		`{"method":"x"}{"method":"y"}`,
	}
	for _, input := range inputs {
		if _, err := ParseMessage([]byte(input)); !errors.Is(err, ErrInvalidMessage) && !errors.Is(err, ErrInvalidID) {
			t.Errorf("%s: got %v", input, err)
		}
	}
}

// A repeated key would otherwise be collapsed with last-value-wins before any
// shape check runs, which can route or decode a response differently from the
// sender's interpretation, so the raw frame must be rejected first.
func TestParseMessageRejectsDuplicateKeys(t *testing.T) {
	t.Parallel()
	inputs := []string{
		`{"id":1,"id":2,"result":null}`,
		`{"id":1,"result":null,"result":{"ok":true}}`,
		`{"method":"x","params":{"a":1,"a":2}}`,
		`{"id":1,"error":{"code":1,"code":2,"message":"bad"}}`,
	}
	for _, input := range inputs {
		if _, err := ParseMessage([]byte(input)); !errors.Is(err, ErrInvalidMessage) {
			t.Errorf("%s: got %v, want ErrInvalidMessage", input, err)
		}
	}
}

func TestEncodedMessagesOmitJSONRPC(t *testing.T) {
	t.Parallel()
	messages := []Message{
		Request(IntegerID(1), "initialize", json.RawMessage(`{"clientInfo":{}}`)),
		Notification("initialized", nil),
		Response(StringID("request"), json.RawMessage(`null`)),
		ErrorResponse(IntegerID(2), ErrorObject{Code: -32601, Message: "unsupported"}),
	}
	for _, message := range messages {
		data, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("jsonrpc")) {
			t.Fatalf("foreign jsonrpc member in %s", data)
		}
		if _, err := ParseMessage(data); err != nil {
			t.Fatalf("parse encoded %s: %v", data, err)
		}
	}
}

func TestCodecBoundAndUnterminatedFrame(t *testing.T) {
	t.Parallel()
	decoder := NewDecoder(strings.NewReader(`{"method":"abcd"}`+"\n"), 8)
	if _, err := decoder.Decode(); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversized: %v", err)
	}
	decoder = NewDecoder(strings.NewReader(`{"method":"x"}`), 64)
	if _, err := decoder.Decode(); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("unterminated: %v", err)
	}
}

func TestCodecHandlesLargeSplitFrame(t *testing.T) {
	t.Parallel()
	input := `{"method":"event","params":{"text":"` + strings.Repeat("x", 70<<10) + `"}}` + "\n"
	message, err := NewDecoder(strings.NewReader(input), 128<<10).Decode()
	if err != nil {
		t.Fatal(err)
	}
	if message.Kind != MessageNotification || message.Method != "event" {
		t.Fatalf("unexpected message: %+v", message)
	}
}

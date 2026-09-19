package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestRequestIDRoundTripAndDomains(t *testing.T) {
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
		t.Fatal("string and integer IDs collided")
	}
}

func TestRequestIDRejectsOtherJSONTypes(t *testing.T) {
	t.Parallel()
	for _, input := range []string{`null`, `true`, `1.0`, `1.5`, `1e0`, `9223372036854775808`, `{}`, `[]`} {
		var id RequestID
		if err := json.Unmarshal([]byte(input), &id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("%s: got %v", input, err)
		}
	}
}

func TestParseMessageClassifiesStrictJSONRPC(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		kind  MessageKind
	}{
		{`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}`, MessageRequest},
		{`{"jsonrpc":"2.0","method":"session/update","params":[]}`, MessageNotification},
		{`{"jsonrpc":"2.0","id":"x","result":null}`, MessageResponse},
		{`{"jsonrpc":"2.0","id":-1,"error":{"code":-32600,"message":"bad"}}`, MessageError},
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

func TestParseMessageRejectsInvalidShapes(t *testing.T) {
	t.Parallel()
	inputs := []string{
		`[]`, `{}`, `{"jsonrpc":"2.0"}`, `{"jsonrpc":"2.0","id":1}`,
		`{"jsonrpc":"2.0","result":null}`, `{"jsonrpc":"2.0","method":"x","result":null}`,
		`{"jsonrpc":"2.0","id":1,"result":null,"error":{"code":1,"message":"bad"}}`,
		`{"id":1,"result":null}`, `{"jsonrpc":"1.0","id":1,"result":null}`,
		`{"jsonrpc":2,"id":1,"result":null}`, `{"jsonrpc":"2.0","method":""}`,
		`{"jsonrpc":"2.0","id":null,"method":"x"}`, `{"jsonrpc":"2.0","method":"x","params":null}`,
		`{"jsonrpc":"2.0","method":"x","params":"bad"}`,
		`{"jsonrpc":"2.0","id":1,"error":null}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":"x","message":"bad"}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":1}}`,
		`{"jsonrpc":"2.0","method":"x"}{"jsonrpc":"2.0","method":"y"}`,
	}
	for _, input := range inputs {
		if _, err := ParseMessage([]byte(input)); !errors.Is(err, ErrInvalidMessage) && !errors.Is(err, ErrInvalidID) {
			t.Errorf("%s: got %v", input, err)
		}
	}
	if _, err := ParseMessage([]byte{'{', 0xff, '}'}); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("invalid UTF-8: %v", err)
	}
}

func TestParseMessageRejectsDuplicateKeys(t *testing.T) {
	t.Parallel()
	inputs := []string{
		`{"jsonrpc":"2.0","id":1,"id":2,"result":null}`,
		`{"jsonrpc":"2.0","id":1,"result":null,"result":{"ok":true}}`,
		`{"jsonrpc":"2.0","method":"x","params":{"a":1,"a":2}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":1,"code":2,"message":"bad"}}`,
	}
	for _, input := range inputs {
		if _, err := ParseMessage([]byte(input)); !errors.Is(err, ErrInvalidMessage) {
			t.Errorf("%s: got %v, want ErrInvalidMessage", input, err)
		}
	}
}

func TestEncodedMessagesRequireJSONRPC(t *testing.T) {
	t.Parallel()
	messages := []Message{
		Request(IntegerID(1), "initialize", json.RawMessage(`{"protocolVersion":1}`)),
		Notification("session/cancel", nil),
		Response(StringID("request"), json.RawMessage(`null`)),
		ErrorResponse(IntegerID(2), ErrorObject{Code: -32601, Message: "unsupported"}),
	}
	for _, message := range messages {
		data, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(data, []byte(`"jsonrpc":"2.0"`)) {
			t.Fatalf("missing JSON-RPC marker in %s", data)
		}
		if _, err := ParseMessage(data); err != nil {
			t.Fatalf("parse encoded %s: %v", data, err)
		}
	}
}

func TestCodecBoundsUTF8AndTermination(t *testing.T) {
	t.Parallel()
	valid := `{"jsonrpc":"2.0","method":"x"}`
	for name, test := range map[string]struct {
		input  string
		limit  int
		target error
	}{
		"oversized":    {valid + "\n", 8, ErrFrameTooLarge},
		"unterminated": {valid, 64, ErrInvalidMessage},
		"empty":        {"\n", 64, ErrInvalidMessage},
		"crlf":         {valid + "\r\n", 64, ErrInvalidMessage},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewDecoder(strings.NewReader(test.input), test.limit).Decode(); !errors.Is(err, test.target) {
				t.Fatalf("got %v", err)
			}
		})
	}
	badUTF8 := append([]byte(valid[:len(valid)-1]), 0xff, '}', '\n')
	if _, err := NewDecoder(bytes.NewReader(badUTF8), 64).Decode(); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("UTF-8: %v", err)
	}
}

func TestCodecHandlesLargeSplitFrameAndExactLimit(t *testing.T) {
	t.Parallel()
	input := `{"jsonrpc":"2.0","method":"event","params":{"text":"` + strings.Repeat("x", 70<<10) + `"}}`
	message, err := NewDecoder(strings.NewReader(input+"\n"), len(input)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	if message.Kind != MessageNotification || message.Method != "event" {
		t.Fatalf("unexpected message: %+v", message)
	}
}

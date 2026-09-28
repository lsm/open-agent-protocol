package rpc

import (
	"strings"
	"testing"
)

func TestParseMessageRefusesAKeyWrittenTwice(t *testing.T) {
	for _, frame := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"a","params":{"k":1,"k":2}}`,
		`{"jsonrpc":"2.0","id":1,"method":"a","params":{"k":{"n":1,"n":2}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"a","method":"b"}`,
		`{"jsonrpc":"2.0","id":1,"result":{"k":1,"k":2}}`,
	} {
		_, err := ParseMessage([]byte(frame))
		if err == nil {
			t.Fatalf("ParseMessage admitted %q, whose key is written twice", frame)
		}
		if !strings.Contains(err.Error(), "duplicate object key") {
			t.Fatalf("ParseMessage refused %q for another reason: %v", frame, err)
		}
	}
}

func TestParseMessageStillAdmitsTheSameFrameWithoutTheRepeatedKey(t *testing.T) {
	message, err := ParseMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"a","params":{"k":1}}`))
	if err != nil {
		t.Fatalf("ParseMessage refused a well-formed frame: %v", err)
	}
	if message.Method != "a" {
		t.Fatalf("the frame decoded to the method %q, so the parser is not reading the same bytes it used to", message.Method)
	}
}

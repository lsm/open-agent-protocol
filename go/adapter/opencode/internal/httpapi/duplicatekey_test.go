package httpapi

import (
	"strings"
	"testing"
)

func TestTheResponseDecoderRefusesAKeyWrittenTwice(t *testing.T) {
	for _, body := range []string{
		`{"a":1,"a":2}`,
		`{"a":{"b":1,"b":2}}`,
		`{"a":[1,{"b":1,"b":2}]}`,
		`{"a":1,"a":2,"a":3}`,
	} {
		var value map[string]any
		if err := decodeStrict([]byte(body), &value); err == nil {
			t.Fatalf("the response decoder admitted %q, whose key is written twice", body)
		} else if !strings.Contains(err.Error(), "duplicate object key") {
			t.Fatalf("the response decoder refused %q for another reason: %v", body, err)
		}
	}
}

func TestTheResponseDecoderStillAdmitsTheSameBodyWithoutTheRepeatedKey(t *testing.T) {
	var value map[string]any
	if err := decodeStrict([]byte(`{"a":{"b":[1,2]}}`), &value); err != nil {
		t.Fatalf("the response decoder refused a well-formed body: %v", err)
	}
	if _, present := value["a"]; !present {
		t.Fatalf("the response decoder read %v, so it is not decoding the bytes it used to", value)
	}
}

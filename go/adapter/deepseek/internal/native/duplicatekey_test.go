package native

import (
	"strings"
	"testing"
)

func TestDecodeStrictRefusesAKeyWrittenTwice(t *testing.T) {
	for _, raw := range []string{
		`{"a":1,"a":2}`,
		`{"a":{"b":1,"b":2}}`,
		`{"a":[{"b":1,"b":2}]}`,
		`{"a":1,"a":2,"a":3}`,
	} {
		var value map[string]any
		if err := DecodeStrict([]byte(raw), &value); err == nil {
			t.Fatalf("DecodeStrict admitted %q, whose key is written twice", raw)
		} else if !strings.Contains(err.Error(), "duplicate object key") {
			t.Fatalf("DecodeStrict refused %q for another reason: %v", raw, err)
		}
	}
}

func TestDecodeStrictStillAdmitsTheSameBytesWithoutTheRepeatedKey(t *testing.T) {
	var value map[string]any
	if err := DecodeStrict([]byte(`{"a":{"b":[1,2]}}`), &value); err != nil {
		t.Fatalf("DecodeStrict refused well-formed JSON: %v", err)
	}
	if _, present := value["a"]; !present {
		t.Fatalf("DecodeStrict read %v, so it is not decoding the bytes it used to", value)
	}
}

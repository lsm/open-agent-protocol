package rpc

import (
	"strings"
	"testing"
)

func TestParseObjectRefusesAKeyWrittenTwice(t *testing.T) {
	for _, frame := range []string{
		`{"k":1,"k":2}`,
		`{"k":{"n":1,"n":2}}`,
		`{"k":[1,{"n":1,"n":2}]}`,
		`{"a":1,"a":2,"a":3}`,
	} {
		if _, err := parseObject([]byte(frame)); err == nil {
			t.Fatalf("parseObject admitted %q, whose key is written twice", frame)
		} else if !strings.Contains(err.Error(), "duplicate object key") {
			t.Fatalf("parseObject refused %q for another reason: %v", frame, err)
		}
	}
}

func TestParseObjectStillAdmitsTheSameFrameWithoutTheRepeatedKey(t *testing.T) {
	object, err := parseObject([]byte(`{"k":1}`))
	if err != nil {
		t.Fatalf("parseObject refused a well-formed frame: %v", err)
	}
	if _, present := object["k"]; !present {
		t.Fatalf("parseObject read %v, so the parser is not reading the same bytes it used to", object)
	}
}

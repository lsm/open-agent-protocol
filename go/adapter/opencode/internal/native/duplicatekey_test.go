package native

import (
	"strings"
	"testing"
)

func TestDecodeEventRefusesAKeyWrittenTwice(t *testing.T) {
	for _, raw := range []string{
		`{"id":"evt_1","type":"session.next.prompted","type":"session.switched","durable":{"aggregateID":"ses_1","seq":1,"version":1},"data":{}}`,
		`{"id":"evt_1","type":"session.next.prompted","durable":{"aggregateID":"ses_1","seq":1,"version":1},"data":{"a":1,"a":2}}`,
		`{"id":"evt_1","type":"session.next.prompted","durable":{"aggregateID":"ses_1","seq":1,"seq":2,"version":1},"data":{}}`,
	} {
		if _, err := DecodeEvent([]byte(raw)); err == nil {
			t.Fatalf("DecodeEvent admitted %q, whose key is written twice", raw)
		} else if !strings.Contains(err.Error(), "duplicate object key") {
			t.Fatalf("DecodeEvent refused %q for another reason: %v", raw, err)
		}
	}
}

func TestDecodeEventStillAdmitsTheSameEventWithoutTheRepeatedKey(t *testing.T) {
	event, err := DecodeEvent([]byte(`{"id":"evt_1","type":"session.next.prompted","durable":{"aggregateID":"ses_1","seq":1,"version":1},"data":{}}`))
	if err != nil {
		t.Fatalf("DecodeEvent refused a well-formed event: %v", err)
	}
	if !event.ID.Valid() {
		t.Fatalf("the event decoded to the id %q, so the decoder is not reading the same bytes it used to", event.ID)
	}
}

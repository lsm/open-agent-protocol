package native

import (
	"fmt"
	"testing"
)

func TestArchiveEventsAreDurable(t *testing.T) {
	for _, kind := range []string{"session.archived", "session.unarchived"} {
		raw := fmt.Sprintf(`{"id":"evt_1","type":%q,"data":{"sessionID":"ses_a"},"durable":{"aggregateID":"ses_a","seq":1,"version":1}}`, kind)
		e, err := DecodeEvent([]byte(raw))
		if err != nil || !e.Type.Durable() {
			t.Fatalf("%s: event=%+v err=%v", kind, e, err)
		}
	}
}

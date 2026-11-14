package servehttp

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lsm/oxap/go/protocol"
)

func TestSessionSSEEventDoesNotReplaceRunCursor(t *testing.T) {
	one := uint64(1)
	session := protocol.Envelope{Type: protocol.TypeSessionTaskStarted, SessionID: "s", Sequence: &one, Payload: []byte(`{}`)}
	var out bytes.Buffer
	if err := writeSSE(&out, session); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "id:") || !strings.HasPrefix(out.String(), "data: ") {
		t.Fatalf("session frame: %s", out.String())
	}
	run := session
	run.RunID = "r"
	out.Reset()
	if err := writeSSE(&out, run); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "id: r:1\n") {
		t.Fatalf("run frame: %s", out.String())
	}
}

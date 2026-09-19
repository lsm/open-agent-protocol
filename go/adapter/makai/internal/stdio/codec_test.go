package stdio

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/adapter/makai/internal/native"
)

const testLine = `{"type":"ping","session_id":"Abcdefghijklmnopqrstu","message_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","sequence":1,"timestamp":1,"version":1,"payload":{}}`

func testEnvelope(t *testing.T, typ native.Type, id native.MessageID, sequence uint64, payload any) native.Envelope {
	t.Helper()
	env, err := native.NewEnvelope(typ, "Abcdefghijklmnopqrstu", id, sequence, 1, payload)
	if err != nil {
		t.Fatal(err)
	}
	return env
}
func TestDecoderReadyAndEnvelope(t *testing.T) {
	d := NewDecoder(strings.NewReader("{\"type\":\"ready\",\"protocol_version\":\"1\"}\n"+testLine+"\n"), 1024)
	f, err := d.Decode()
	if err != nil || f.Ready == nil {
		t.Fatalf("frame=%+v err=%v", f, err)
	}
	f, err = d.Decode()
	if err != nil || f.Envelope == nil || f.Envelope.Type != native.TypePing {
		t.Fatalf("frame=%+v err=%v", f, err)
	}
}
func TestDecoderStrictFaults(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		limit       int
		want        error
	}{
		{"empty", "\n", 100, ErrInvalidFrame}, {"crlf", testLine + "\r\n", 1000, ErrInvalidFrame}, {"unterminated", testLine, 1000, ErrInvalidFrame}, {"oversized", testLine + "\n", 10, ErrFrameTooLarge}, {"stdout contamination", "hello\n", 100, ErrInvalidFrame}, {"unknown field", strings.TrimSuffix(testLine, "}") + `,"extra":1}` + "\n", 1000, ErrInvalidFrame}, {"missing timestamp", strings.Replace(testLine, `,"timestamp":1`, "", 1) + "\n", 1000, ErrInvalidFrame}, {"missing error sequence", strings.Replace(strings.Replace(strings.Replace(testLine, `"type":"ping"`, `"type":"agent_error"`, 1), `,"sequence":1`, "", 1), `"payload":{}`, `"payload":{"code":"internal_error","message":"boom"}`, 1) + "\n", 1000, ErrInvalidFrame}, {"duplicate key", strings.Replace(testLine, `"payload":{}`, `"payload":{},"payload":{}`, 1) + "\n", 1000, ErrInvalidFrame}, {"trailing malformed", testLine + "!\n", 1000, ErrInvalidFrame}, {"bad version", strings.Replace(testLine, `"version":1`, `"version":2`, 1) + "\n", 1000, ErrInvalidFrame}, {"nested malformed", strings.Replace(strings.Replace(testLine, `"type":"ping"`, `"type":"agent_event"`, 1), `"payload":{}`, `"payload":{"event_json":"{"}`, 1) + "\n", 1000, ErrInvalidFrame},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewDecoder(strings.NewReader(tc.input), tc.limit).Decode()
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
}
func TestEncoderPartialWritesAndLimit(t *testing.T) {
	env := testEnvelope(t, native.TypePing, "01ARZ3NDEKTSV4RRFFQ69G5FAV", 1, native.Empty{})
	w := &shortWriter{max: 3}
	if err := NewEncoder(w, 1000).Encode(env); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(w.buf.Bytes(), []byte("\n")) {
		t.Fatal("missing newline")
	}
	if err := NewEncoder(io.Discard, 10).Encode(env); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("got %v", err)
	}
}

type shortWriter struct {
	buf bytes.Buffer
	max int
}

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > w.max {
		p = p[:w.max]
	}
	return w.buf.Write(p)
}

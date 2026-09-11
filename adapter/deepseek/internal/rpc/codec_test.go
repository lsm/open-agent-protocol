package rpc

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestDecoderStrictLFObjectFrames(t *testing.T) {
	valid := "{\"jsonrpc\":\"2.0\",\"method\":\"session.status\",\"params\":{\"sessionId\":\"s\",\"status\":\"idle\"}}\n"
	message, err := NewDecoder(strings.NewReader(valid), 0).Decode()
	if err != nil {
		t.Fatal(err)
	}
	if message.Kind != MessageNotification {
		t.Fatalf("kind %v", message.Kind)
	}
	invalid := []string{
		"{\"jsonrpc\":\"2.0\",\"method\":\"x\"}\r\n",
		`{"jsonrpc":"2.0","method":"x"}`,
		"{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"method\":\"y\"}\n",
		"{\"jsonrpc\":\"2.0\",\"method\":\"x\"} garbage\n",
		" {\"jsonrpc\":\"2.0\",\"method\":\"x\"}\n",
		"{\"jsonrpc\":\"2.0\",\"method\":\"x\"} \n",
		"[]\n",
	}
	for _, raw := range invalid {
		if _, err := NewDecoder(strings.NewReader(raw), 0).Decode(); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	bad := append([]byte(`{"jsonrpc":"2.0","method":"`), byte(0xff))
	bad = append(bad, '"', '}', '\n')
	if _, err := NewDecoder(bytes.NewReader(bad), 0).Decode(); err == nil {
		t.Fatal("accepted invalid UTF-8")
	}
}

func TestCodecFrameLimitAndPartialWrites(t *testing.T) {
	frame := `{"jsonrpc":"2.0","method":"x"}`
	if _, err := NewDecoder(strings.NewReader(frame+"\n"), len(frame)).Decode(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDecoder(strings.NewReader(frame+" \n"), len(frame)).Decode(); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("got %v", err)
	}
	writer := &shortWriter{max: 2}
	if err := NewEncoder(writer).Encode(Notification("x", nil)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(writer.String(), "\n") {
		t.Fatalf("missing LF: %q", writer.String())
	}
	if err := NewEncoder(&bytes.Buffer{}, 4).Encode(Notification("x", nil)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("got %v", err)
	}
}

type shortWriter struct {
	bytes.Buffer
	max int
}

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > w.max {
		p = p[:w.max]
	}
	return w.Buffer.Write(p)
}

func TestRequestIDDomainsAndMessageStrictness(t *testing.T) {
	for _, raw := range []string{`{"jsonrpc":"2.0","id":"1","result":{}}`, `{"jsonrpc":"2.0","id":1,"result":{}}`} {
		if _, err := ParseMessage([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	invalid := []string{`{"jsonrpc":"2.0","id":1.5,"result":{}}`, `{"jsonrpc":"2.0","id":1,"result":{},"extra":1}`, `{"jsonrpc":"2.0","id":1,"error":{"code":1,"message":"x","extra":1}}`, `{"jsonrpc":"2.0","id":1,"result":{}} garbage`}
	for _, raw := range invalid {
		if _, err := ParseMessage([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

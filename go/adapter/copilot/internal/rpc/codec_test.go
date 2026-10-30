package rpc

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

type fragments struct{ io.Reader }

func (r fragments) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}

type shortWriter struct{ bytes.Buffer }

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > 2 {
		p = p[:2]
	}
	return w.Buffer.Write(p)
}

type stalledWriter struct{}

func (stalledWriter) Write([]byte) (int, error) { return 0, nil }

func TestFragmentedAndCoalescedFrames(t *testing.T) {
	body := []byte(`{"text":"你好"}`)
	var written shortWriter
	encoder := NewCodec(nil, &written, 0)
	if err := encoder.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Write([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	decoder := NewCodec(fragments{bytes.NewReader(written.Bytes())}, nil, 0)
	for _, want := range [][]byte{body, []byte(`{}`)} {
		got, err := decoder.Read()
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("read %q, error %v; want %q", got, err, want)
		}
	}
	if _, err := decoder.Read(); !errors.Is(err, io.EOF) {
		t.Fatalf("end: %v", err)
	}
	withType := NewCodec(strings.NewReader("Content-Type: application/json\r\ncontent-length:  2\r\n\r\n{}"), nil, 0)
	if got, err := withType.Read(); err != nil || string(got) != "{}" {
		t.Fatalf("optional header: %q %v", got, err)
	}
}

func TestFrameRejections(t *testing.T) {
	for _, frame := range []string{
		"Content-Length: 0\r\n\r\n", "Content-Length: -1\r\n\r\nx", "Content-Length: +1\r\n\r\nx",
		"Content-Length: 1a\r\n\r\nx", "Content-Length: 1\r\nContent-Length: 1\r\n\r\nx",
		"Content-Length 1\r\n\r\nx", "X: y\r\n\r\nx", "Content-Length: 5\r\n\r\n{}",
		"Content-Length: 1\n\nx", "Content-Length: 1\r\n\r\n\xff",
		"Content-Length: 999999999999999999999999\r\n\r\n", strings.Repeat("A", 1025) + "\r\n\r\n",
		strings.Repeat("X: y\r\n", 180) + "Content-Length: 2\r\n\r\n{}",
	} {
		if _, err := NewCodec(strings.NewReader(frame), nil, 4).Read(); err == nil {
			t.Errorf("accepted %q", frame)
		}
	}
	if err := NewCodec(nil, stalledWriter{}, 0).Write([]byte(`{}`)); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("stalled write: %v", err)
	}
	for _, body := range [][]byte{nil, []byte("12345"), {0xff}} {
		if err := NewCodec(nil, io.Discard, 4).Write(body); !errors.Is(err, ErrFrame) {
			t.Fatalf("invalid body accepted: %q %v", body, err)
		}
	}
}

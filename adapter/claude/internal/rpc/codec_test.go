package rpc

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestDecoderRejectsCarriageReturns(t *testing.T) {
	if _, err := NewDecoder(strings.NewReader("{\"type\":\"user\"}\r\n"), 0).Decode(); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("CR-framed line err = %v", err)
	}
}

func TestDecoderRejectsEmptyFrames(t *testing.T) {
	if _, err := NewDecoder(strings.NewReader("\n{\"type\":\"user\"}\n"), 0).Decode(); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("empty frame err = %v", err)
	}
}

func TestDecoderRejectsUnterminatedTail(t *testing.T) {
	if _, err := NewDecoder(strings.NewReader("{\"type\":\"user\""), 0).Decode(); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("unterminated tail err = %v", err)
	}
}

func TestDecoderRejectsNonUTF8(t *testing.T) {
	frame := []byte("{\"type\":\"user\",\"bad\":\"\xff\xfe\"}\n")
	if _, err := NewDecoder(bytes.NewReader(frame), 0).Decode(); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("non-UTF-8 frame err = %v", err)
	}
}

func TestDecoderFrameLimit(t *testing.T) {
	frame := `{"type":"user","pad":"xxxxxxxxxx"}` + "\n"
	limit := len(frame) - 1
	if _, err := NewDecoder(strings.NewReader(frame), limit).Decode(); err != nil {
		t.Fatalf("exact-limit frame rejected: %v", err)
	}
	if _, err := NewDecoder(strings.NewReader(frame), limit-1).Decode(); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversize frame err = %v", err)
	}
}

func TestDecoderServesConsecutiveFrames(t *testing.T) {
	decoder := NewDecoder(strings.NewReader("{\"type\":\"user\"}\n{\"type\":\"keep_alive\"}\n"), 0)
	first, err := decoder.Decode()
	if err != nil || first.Type != TypeUser {
		t.Fatalf("first = %+v err=%v", first, err)
	}
	second, err := decoder.Decode()
	if err != nil || second.Type != TypeKeepAlive {
		t.Fatalf("second = %+v err=%v", second, err)
	}
}

func TestEncoderRejectsOversizeMessages(t *testing.T) {
	message := UserTurnMessage([]byte(`{"type":"user","message":{"role":"user","content":"hello"},"pad":"xxxxxxxxxx"}`))
	var out bytes.Buffer
	if err := NewEncoder(&out, 40).Encode(message); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversize encode err = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("oversize encode wrote %d bytes", out.Len())
	}
}

func TestEncoderWritesLFTerminatedFrames(t *testing.T) {
	message := UserTurnMessage([]byte(`{"type":"user","message":{"role":"user","content":"hello"}}`))
	var out bytes.Buffer
	if err := NewEncoder(&out, 0).Encode(message); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(out.Bytes(), []byte("\n")) || bytes.Count(out.Bytes(), []byte("\n")) != 1 {
		t.Fatalf("encoded frame = %q", out.Bytes())
	}
}

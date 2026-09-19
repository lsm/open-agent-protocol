package httpapi

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestSSEDecodesEventMessageFrames(t *testing.T) {
	stream := "event: message\ndata: {\"a\":1}\n\n" +
		"data: {\"b\":2}\n\n" +
		": heartbeat\n\n" +
		"event: message\nid: evt_1\ndata: line1\ndata: line2\n\n"
	decoder := NewSSEDecoder(strings.NewReader(stream), 0)
	first, err := decoder.Decode()
	if err != nil || first.Name != "message" || string(first.Data) != `{"a":1}` {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := decoder.Decode()
	if err != nil || second.Name != "" || string(second.Data) != `{"b":2}` {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	third, err := decoder.Decode()
	if err != nil || third.ID != "evt_1" || string(third.Data) != "line1\nline2" {
		t.Fatalf("third=%+v err=%v", third, err)
	}
	if _, err := decoder.Decode(); !errors.Is(err, io.EOF) {
		t.Fatalf("tail err=%v", err)
	}
}

func TestSSERejectsMalformedFraming(t *testing.T) {
	cases := map[string]struct {
		stream  string
		wantErr error
	}{
		"bare CR":        {"event: message\rdata: {}\n\n", ErrInvalidFrame},
		"CRLF accepted":  {"event: message\r\ndata: {}\r\n\r\n", nil},
		"no data":        {"event: message\n\n", ErrInvalidFrame},
		"duplicate name": {"event: a\nevent: b\ndata: {}\n\n", ErrInvalidFrame},
		"duplicate id":   {"id: 1\nid: 2\ndata: {}\n\n", ErrInvalidFrame},
		"unterminated":   {"data: {}", ErrInvalidFrame},
	}
	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			decoder := NewSSEDecoder(strings.NewReader(tc.stream), 0)
			for {
				_, err := decoder.Decode()
				if err == nil {
					continue
				}
				if tc.wantErr == nil {
					if !errors.Is(err, io.EOF) {
						t.Fatalf("err=%v", err)
					}
					return
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err=%v want %v", err, tc.wantErr)
				}
				return
			}
		})
	}
}

func TestSSEIgnoresUnknownFieldsAndEnforcesLimit(t *testing.T) {
	decoder := NewSSEDecoder(strings.NewReader("custom: 1\ndata: {}\n\n"), 0)
	event, err := decoder.Decode()
	if err != nil || string(event.Data) != "{}" {
		t.Fatalf("event=%+v err=%v", event, err)
	}
	limited := NewSSEDecoder(strings.NewReader("data: {\"a\":\""+strings.Repeat("x", 64)+"\"}\n\n"), 16)
	if _, err := limited.Decode(); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err=%v", err)
	}
}

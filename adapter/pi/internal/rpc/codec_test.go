package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/adapter/pi/internal/native"
)

func TestDecoderFrameKinds(t *testing.T) {
	tests := []struct {
		name, wire string
		check      func(t *testing.T, frame Frame)
	}{
		{"response", `{"id":"r","type":"response","command":"prompt","success":true}` + "\n", func(t *testing.T, f Frame) {
			if f.Response == nil || f.Response.ID != "r" {
				t.Fatalf("%+v", f)
			}
		}},
		{"failed response", `{"id":"r","type":"response","command":"prompt","success":false,"error":"no"}` + "\n", func(t *testing.T, f Frame) {
			if f.Response == nil || f.Response.Error != "no" {
				t.Fatalf("%+v", f)
			}
		}},
		{"event", `{"type":"message_update","usage":{"input":1},"assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"x"}}` + "\n", func(t *testing.T, f Frame) {
			if f.Event == nil || f.Event.Type != native.EventMessageUpdate {
				t.Fatalf("%+v", f)
			}
		}},
		{"extension", `{"type":"extension_ui_request","id":"ui","method":"confirm","title":"T","message":"M"}` + "\n", func(t *testing.T, f Frame) {
			if f.ExtensionRequest == nil || f.ExtensionRequest.Method != native.ExtensionConfirm {
				t.Fatalf("%+v", f)
			}
		}},
		{"extension error", `{"type":"extension_error","extensionPath":"x","event":"y","error":"z"}` + "\n", func(t *testing.T, f Frame) {
			if f.Event == nil || f.Event.Type != native.EventExtensionError {
				t.Fatalf("%+v", f)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frame, err := NewDecoder(strings.NewReader(test.wire), 0).Decode()
			if err != nil {
				t.Fatal(err)
			}
			test.check(t, frame)
		})
	}
}

func TestDecoderRejectsInvalidFrames(t *testing.T) {
	tests := []string{
		"\n", "{}\n", "[]\n", `{"type":"unknown"}` + "\n", `{"type":"agent_start","type":"agent_end"}` + "\n",
		`{"type":"response","command":"prompt","success":false}` + "\n", `{"type":"response","command":"bogus","success":true}` + "\n",
		`{"type":"extension_ui_request","id":"","method":"confirm"}` + "\n", "{\"type\":\"agent_start\"}\r\n", `{"type":"agent_start"}`,
	}
	for _, wire := range tests {
		if _, err := NewDecoder(strings.NewReader(wire), 0).Decode(); err == nil {
			t.Errorf("accepted %q", wire)
		}
	}
}

func TestDecoderRejectsMalformedTypedEvents(t *testing.T) {
	for _, wire := range []string{
		`{"type":"tool_execution_end"}` + "\n",
		`{"type":"agent_start","unexpected":true}` + "\n",
		`{"type":"queue_update","steering":[]}` + "\n",
	} {
		if _, err := NewDecoder(strings.NewReader(wire), 0).Decode(); !errors.Is(err, ErrInvalidFrame) {
			t.Errorf("%q: %v", wire, err)
		}
	}
}

func TestDecoderFrameLimitAndUTF8(t *testing.T) {
	if _, err := NewDecoder(strings.NewReader(`{"type":"agent_start"}`+"\n"), 5).Decode(); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("%v", err)
	}
	if _, err := NewDecoder(bytes.NewReader(append([]byte(`{"type":"agent_start","x":"`), 0xff, '"', '}', '\n')), 0).Decode(); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("%v", err)
	}
}

type byteWriter struct{ bytes.Buffer }

func (w *byteWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	return w.Buffer.Write(data[:1])
}

func TestEncoderStrictLFAndPartialWrites(t *testing.T) {
	writer := &byteWriter{}
	message := "hello"
	if err := NewEncoder(writer).Encode(native.Command{ID: "x", Type: native.CommandPrompt, Message: &message}); err != nil {
		t.Fatal(err)
	}
	if got := writer.String(); got != `{"id":"x","type":"prompt","message":"hello"}`+"\n" {
		t.Fatalf("%q", got)
	}
	var decoded map[string]any
	if err := json.Unmarshal(bytes.TrimSuffix(writer.Bytes(), []byte{'\n'}), &decoded); err != nil {
		t.Fatal(err)
	}
}

func TestEncoderRejectsInvalidAndOversize(t *testing.T) {
	if err := NewEncoder(io.Discard).Encode(native.Command{Type: native.CommandPrompt}); !errors.Is(err, native.ErrInvalid) {
		t.Fatalf("%v", err)
	}
	if err := NewEncoder(io.Discard).Encode(struct{}{}); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("%v", err)
	}
	message := strings.Repeat("x", 100)
	if err := NewEncoder(io.Discard, 10).Encode(native.Command{Type: native.CommandPrompt, Message: &message}); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("%v", err)
	}
}

package native

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDecodeNotificationObservablePromptCorrelation(t *testing.T) {
	raw := []byte(`{"sessionId":"s","event":{"type":"user/message","seq":4,"time":10,"data":{"id":"message-1","role":"user","content":[{"type":"text","text":"hi"}],"source":{"kind":"user"}},"surfaceOp":"append"}}`)
	value, err := DecodeNotification(NotifySessionEvent, raw)
	if err != nil {
		t.Fatal(err)
	}
	event := value.(*SessionEventNotification).Event
	var message UserMessage
	if err := event.DataAs(&message); err != nil {
		t.Fatal(err)
	}
	if message.ID != "message-1" || message.Source.Kind != "user" {
		t.Fatalf("lost correlation identity: %#v", message)
	}
}

func TestEventRecognizedVariantsAreStrict(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"zero turn", `{"sessionId":"s","event":{"type":"turn/start","seq":0,"time":1,"data":{"turn":0}}}`},
		{"unknown payload member", `{"sessionId":"s","event":{"type":"step/start","seq":0,"time":1,"data":{"turn":1,"step":1,"extra":true}}}`},
		{"bad tool arguments", `{"sessionId":"s","event":{"type":"tool/call","seq":0,"time":1,"data":{"turn":1,"step":1,"callId":"c","name":"x","arguments":"{"}}}`},
		{"false ignorable", `{"sessionId":"s","event":{"type":"future/event","seq":0,"time":1,"data":{},"ignorable":false}}`},
		{"surface metadata", `{"sessionId":"s","event":{"type":"turn/start","seq":0,"time":1,"data":{"turn":1},"surfaceOp":"append"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeNotification(NotifySessionEvent, []byte(tt.raw)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestUnknownEventRequiresLiteralIgnorableTrue(t *testing.T) {
	value, err := DecodeNotification(NotifySessionEvent, []byte(`{"sessionId":"s","event":{"type":"future/event","seq":1,"time":2,"data":{"anything":true},"ignorable":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if value.(*SessionEventNotification).Event.Type != "future/event" {
		t.Fatal("event lost")
	}
	if _, err := DecodeNotification(NotifySessionEvent, []byte(`{"sessionId":"s","event":{"type":"future/event","seq":1,"time":2,"data":{}}}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v", err)
	}
}

func TestNotificationVariantsAndStrictJSON(t *testing.T) {
	valid := map[string]string{
		NotifySessionStatus:    `{"sessionId":"s","status":"running"}`,
		NotifySubagentStarted:  `{"parentSessionId":"p","childSessionId":"c"}`,
		NotifySubagentFinished: `{"provider":"deepseek","agentId":"c","parentSessionId":"p","childSessionId":"c","status":"ok","stopReason":"completed","lastAssistantMessage":[{"type":"text","text":"done"}]}`,
	}
	for method, raw := range valid {
		if _, err := DecodeNotification(method, []byte(raw)); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
	}
	invalid := []string{
		`{"sessionId":"s","status":"idle","status":"running"}`,
		`{"sessionId":"s","status":"idle"} garbage`,
		`{"sessionId":"s","status":"idle","extra":1}`,
	}
	for _, raw := range invalid {
		if _, err := DecodeNotification(NotifySessionStatus, []byte(raw)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %q: %v", raw, err)
		}
	}
}

func TestInitializeAndPromptValidation(t *testing.T) {
	zero := int64(0)
	if ValidateInitializeParams(InitializeParams{Cwd: "/tmp", Provider: "p", Model: "m"}) != nil {
		t.Fatal("valid initialize rejected")
	}
	if !errors.Is(ValidateInitializeParams(InitializeParams{Cwd: "/tmp", Provider: "p", Model: "m", MaxTokens: &zero}), ErrInvalid) {
		t.Fatal("zero max tokens accepted")
	}
	if !errors.Is(ValidateInitializeResult(InitializeResult{ServerInfo: ServerInfo{Name: "other", Version: ServerVersion}}), ErrInvalid) {
		t.Fatal("wrong identity accepted")
	}
	if ValidatePrompt(SessionPromptParams{SessionID: "s", ContentBlocks: []ContentBlock{{Type: "text", Text: "x"}}}) != nil {
		t.Fatal("valid prompt rejected")
	}
}

func TestDecodeStrictRejectsNestedDuplicate(t *testing.T) {
	var dst map[string]json.RawMessage
	if err := DecodeStrict([]byte(`{"x":{"a":1,"a":2}}`), &dst); err == nil {
		t.Fatal("nested duplicate accepted")
	}
}

func TestEventContentBoundaryStrictness(t *testing.T) {
	// Every case must fail closed against the pinned content/source contracts.
	raw := []string{
		// text/reasoning blocks require their text member and reject
		// cross-variant fields.
		`{"sessionId":"s","event":{"type":"user/message","seq":1,"time":1,"data":{"id":"m","role":"user","content":[{"type":"text"}],"source":{"kind":"user"}}}}`,
		`{"sessionId":"s","event":{"type":"user/message","seq":1,"time":1,"data":{"id":"m","role":"user","content":[{"type":"text","text":"ok","arguments":"{}"}],"source":{"kind":"user"}}}}`,
		`{"sessionId":"s","event":{"type":"user/message","seq":1,"time":1,"data":{"id":"m","role":"user","content":[{"type":"reasoning","text":"r","attachment":{"attachmentId":"a","mediaType":"image/png"}}],"source":{"kind":"user"}}}}`,
		// image attachment must be a structured reference.
		`{"sessionId":"s","event":{"type":"user/message","seq":1,"time":1,"data":{"id":"m","role":"user","content":[{"type":"image","attachment":"https://example.invalid/token"}],"source":{"kind":"user"}}}}`,
		`{"sessionId":"s","event":{"type":"user/message","seq":1,"time":1,"data":{"id":"m","role":"user","content":[{"type":"image","attachment":{"mediaType":"image/png"}}],"source":{"kind":"user"}}}}`,
		// sources must not carry contradictory provenance.
		`{"sessionId":"s","event":{"type":"user/message","seq":1,"time":1,"data":{"id":"m","role":"user","content":[{"type":"text","text":"x"}],"source":{"kind":"user","plugin":"p"}}}}`,
		`{"sessionId":"s","event":{"type":"user/message","seq":1,"time":1,"data":{"id":"m","role":"user","content":[{"type":"text","text":"x"}],"source":{"kind":"plugin","plugin":"p","model":"m"}}}}`,
		// inbox insertions validate their message content too.
		`{"sessionId":"s","event":{"type":"agent/inbox/spliced","seq":1,"time":1,"data":{"target":"next-turn","start":0,"inserted":[{"id":"m","role":"user","content":[{"type":"text"}],"source":{"kind":"user"}}]}}}`,
		// assistant message content is validated from raw, not just typed.
		`{"sessionId":"s","event":{"type":"assistant/message","seq":1,"time":1,"data":{"turn":1,"step":1,"message":{"id":"a","role":"assistant","content":[{"type":"text"}],"source":{"kind":"model","provider":"p","model":"m"}}}}}`,
		// tool/result content is exactly one matching tool-result block.
		`{"sessionId":"s","event":{"type":"tool/result","seq":1,"time":1,"data":{"turn":1,"step":1,"message":{"id":"t","role":"user","content":[],"source":{"kind":"tool","callId":"c"}}}}}`,
		`{"sessionId":"s","event":{"type":"tool/result","seq":1,"time":1,"data":{"turn":1,"step":1,"message":{"id":"t","role":"user","content":[{"type":"text","text":"x"}],"source":{"kind":"tool","callId":"c"}}}}}`,
		`{"sessionId":"s","event":{"type":"tool/result","seq":1,"time":1,"data":{"turn":1,"step":1,"message":{"id":"t","role":"user","content":[{"type":"tool-result","toolCallId":"other","content":[{"type":"text","text":"x"}]}],"source":{"kind":"tool","callId":"c"}}}}}`,
		// request/header must carry the pinned EpochHeader shape.
		`{"sessionId":"s","event":{"type":"request/header","seq":1,"time":1,"data":{"header":null,"reason":"initial"}}}`,
		`{"sessionId":"s","event":{"type":"request/header","seq":1,"time":1,"data":{"header":{"apiKey":"secret"},"reason":"initial"}}}`,
		`{"sessionId":"s","event":{"type":"request/header","seq":1,"time":1,"data":{"header":{"config":{"provider":"","model":"m"}},"reason":"initial"}}}`,
		// subagent output content is validated.
		`{"provider":"deepseek","agentId":"c","parentSessionId":"p","childSessionId":"c","status":"ok","stopReason":"completed","lastAssistantMessage":[{"type":"text"}]}`,
	}
	for _, data := range raw {
		method := NotifySessionEvent
		if strings.HasPrefix(data, `{"provider":"deepseek"`) {
			method = NotifySubagentFinished
		}
		if _, err := DecodeNotification(method, []byte(data)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %q: %v", data, err)
		}
	}
}

func TestEventChunkAndReasonStrictness(t *testing.T) {
	raw := []string{
		// unknown or incomplete stream chunk variants.
		`{"sessionId":"s","event":{"type":"assistant/chunk","seq":1,"time":1,"data":{"turn":1,"step":1,"chunk":{"type":"future-chunk"}}}}`,
		`{"sessionId":"s","event":{"type":"assistant/chunk","seq":1,"time":1,"data":{"turn":1,"step":1,"chunk":{"type":"text-delta"}}}}`,
		`{"sessionId":"s","event":{"type":"assistant/chunk","seq":1,"time":1,"data":{"turn":1,"step":1,"chunk":{"type":"block-end","index":0,"block":{"type":"text"}}}}}`,
		`{"sessionId":"s","event":{"type":"assistant/chunk","seq":1,"time":1,"data":{"turn":1,"step":1,"chunk":{"type":"usage"}}}}`,
		`{"sessionId":"s","event":{"type":"assistant/chunk","seq":1,"time":1,"data":{"turn":1,"step":1,"chunk":{"type":"finish","reason":{"kind":"stop","failure":{"message":"x","code":"y"}}}}}}`,
		// turn/end reasons must be structurally complete.
		`{"sessionId":"s","event":{"type":"turn/end","seq":1,"time":1,"data":{"turn":1,"reason":{"kind":"aborted","reason":"anything"}}}}`,
		`{"sessionId":"s","event":{"type":"turn/end","seq":1,"time":1,"data":{"turn":1,"reason":{"kind":"aborted","reason":{"kind":"hook"}}}}}`,
		`{"sessionId":"s","event":{"type":"turn/end","seq":1,"time":1,"data":{"turn":1,"reason":{"kind":"error","error":{"apiKey":"secret"}}}}}`,
		`{"sessionId":"s","event":{"type":"turn/end","seq":1,"time":1,"data":{"turn":1,"reason":{"kind":"error","error":{"message":"x"}}}}}`,
		`{"sessionId":"s","event":{"type":"turn/end","seq":1,"time":1,"data":{"turn":1,"reason":{"kind":"completed","reason":{"kind":"user"}}}}}`,
	}
	for _, data := range raw {
		if _, err := DecodeNotification(NotifySessionEvent, []byte(data)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %q: %v", data, err)
		}
	}
	// Pinned shapes still decode.
	valid := []string{
		`{"sessionId":"s","event":{"type":"assistant/chunk","seq":1,"time":1,"data":{"turn":1,"step":1,"chunk":{"type":"text-delta","index":0,"text":"hi"}}}}`,
		`{"sessionId":"s","event":{"type":"assistant/chunk","seq":2,"time":1,"data":{"turn":1,"step":1,"chunk":{"type":"finish","reason":{"kind":"stop"}}}}}`,
		`{"sessionId":"s","event":{"type":"turn/end","seq":3,"time":1,"data":{"turn":1,"reason":{"kind":"aborted","reason":{"kind":"user"}}}}}`,
		`{"sessionId":"s","event":{"type":"turn/end","seq":4,"time":1,"data":{"turn":1,"reason":{"kind":"error","error":{"message":"boom","code":"PROVIDER"}}}}}`,
		`{"sessionId":"s","event":{"type":"turn/end","seq":5,"time":1,"data":{"turn":1,"reason":{"kind":"max-tokens"}}}}`,
	}
	for _, data := range valid {
		if _, err := DecodeNotification(NotifySessionEvent, []byte(data)); err != nil {
			t.Fatalf("rejected %q: %v", data, err)
		}
	}
}

func TestMaxTokensSafeIntegerBound(t *testing.T) {
	over := int64(9007199254740992)
	if !errors.Is(ValidateInitializeParams(InitializeParams{Cwd: "/tmp", Provider: "p", Model: "m", MaxTokens: &over}), ErrInvalid) {
		t.Fatal("unsafe maxTokens accepted")
	}
	safe := int64(9007199254740991)
	if err := ValidateInitializeParams(InitializeParams{Cwd: "/tmp", Provider: "p", Model: "m", MaxTokens: &safe}); err != nil {
		t.Fatal("safe maxTokens rejected")
	}
}

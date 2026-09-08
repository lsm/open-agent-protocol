package native

import (
	"encoding/json"
	"errors"
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

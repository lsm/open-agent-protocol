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

	raw := []string{

		`{"sessionId":"s","event":{"type":"user/message","seq":1,"time":1,"data":{"id":"m","role":"user","content":[{"type":"text"}],"source":{"kind":"user"}}}}`,
		`{"sessionId":"s","event":{"type":"user/message","seq":1,"time":1,"data":{"id":"m","role":"user","content":[{"type":"text","text":"ok","arguments":"{}"}],"source":{"kind":"user"}}}}`,
		`{"sessionId":"s","event":{"type":"user/message","seq":1,"time":1,"data":{"id":"m","role":"user","content":[{"type":"reasoning","text":"r","attachment":{"attachmentId":"a","mediaType":"image/png"}}],"source":{"kind":"user"}}}}`,

		`{"sessionId":"s","event":{"type":"user/message","seq":1,"time":1,"data":{"id":"m","role":"user","content":[{"type":"image","attachment":"https://example.invalid/token"}],"source":{"kind":"user"}}}}`,
		`{"sessionId":"s","event":{"type":"user/message","seq":1,"time":1,"data":{"id":"m","role":"user","content":[{"type":"image","attachment":{"mediaType":"image/png"}}],"source":{"kind":"user"}}}}`,

		`{"sessionId":"s","event":{"type":"user/message","seq":1,"time":1,"data":{"id":"m","role":"user","content":[{"type":"text","text":"x"}],"source":{"kind":"user","plugin":"p"}}}}`,
		`{"sessionId":"s","event":{"type":"user/message","seq":1,"time":1,"data":{"id":"m","role":"user","content":[{"type":"text","text":"x"}],"source":{"kind":"plugin","plugin":"p","model":"m"}}}}`,

		`{"sessionId":"s","event":{"type":"agent/inbox/spliced","seq":1,"time":1,"data":{"target":"next-turn","start":0,"inserted":[{"id":"m","role":"user","content":[{"type":"text"}],"source":{"kind":"user"}}]}}}`,

		`{"sessionId":"s","event":{"type":"assistant/message","seq":1,"time":1,"data":{"turn":1,"step":1,"message":{"id":"a","role":"assistant","content":[{"type":"text"}],"source":{"kind":"model","provider":"p","model":"m"}}}}}`,

		`{"sessionId":"s","event":{"type":"tool/result","seq":1,"time":1,"data":{"turn":1,"step":1,"message":{"id":"t","role":"user","content":[],"source":{"kind":"tool","callId":"c"}}}}}`,
		`{"sessionId":"s","event":{"type":"tool/result","seq":1,"time":1,"data":{"turn":1,"step":1,"message":{"id":"t","role":"user","content":[{"type":"text","text":"x"}],"source":{"kind":"tool","callId":"c"}}}}}`,
		`{"sessionId":"s","event":{"type":"tool/result","seq":1,"time":1,"data":{"turn":1,"step":1,"message":{"id":"t","role":"user","content":[{"type":"tool-result","toolCallId":"other","content":[{"type":"text","text":"x"}]}],"source":{"kind":"tool","callId":"c"}}}}}`,

		`{"sessionId":"s","event":{"type":"request/header","seq":1,"time":1,"data":{"header":null,"reason":"initial"}}}`,
		`{"sessionId":"s","event":{"type":"request/header","seq":1,"time":1,"data":{"header":{"apiKey":"secret"},"reason":"initial"}}}`,
		`{"sessionId":"s","event":{"type":"request/header","seq":1,"time":1,"data":{"header":{"config":{"provider":"","model":"m"}},"reason":"initial"}}}`,

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

func assistantAttempt(stream string) string {
	return `{"sessionId":"s","event":{"type":"assistant/attempt","seq":1,"time":1,"data":{"turn":1,"step":1,"stream":` + stream + `}}}`
}

func assistantMessage(stream string) string {
	return `{"sessionId":"s","event":{"type":"assistant/message","seq":1,"time":1,"data":{"turn":1,"step":1,"stream":` + stream +
		`,"message":{"id":"a","role":"assistant","content":[{"type":"text","text":"hi"}],"source":{"kind":"model","provider":"p","model":"m"}}}}}`
}

func TestEventChunkAndReasonStrictness(t *testing.T) {
	raw := []string{

		assistantAttempt(`[{"type":"chunk","time":1,"chunk":{"type":"future-chunk"}}]`),
		assistantAttempt(`[{"type":"chunk","time":1,"chunk":{"type":"text-delta"}}]`),
		assistantAttempt(`[{"type":"chunk","time":1,"chunk":{"type":"block-end","index":0,"block":{"type":"text"}}}]`),
		assistantAttempt(`[{"type":"chunk","time":1,"chunk":{"type":"usage"}}]`),
		assistantAttempt(`[{"type":"chunk","time":1,"chunk":{"type":"finish","reason":{"kind":"stop","failure":{"message":"x","code":"y"}}}}]`),

		assistantAttempt(`[{"type":"text-chunks","time0":0,"index":0,"dt":[0,1],"texts":["hi"]}]`),
		assistantAttempt(`[{"type":"text-chunks","time0":0,"index":0,"dt":[],"texts":[]}]`),
		assistantAttempt(`[{"type":"bogus-record"}]`),
		assistantAttempt(`[{"type":"text-chunks","time0":0,"index":-1,"dt":[0],"texts":["hi"]}]`),
		assistantAttempt(`[{"type":"tool-call-chunks","time0":0,"index":0,"dt":[0],"id":"","args":["{}"]}]`),
		assistantAttempt(`[{"type":"text-chunks","time0":0,"index":0,"dt":[0],"texts":["hi"],"extra":true}]`),

		`{"sessionId":"s","event":{"type":"assistant/message","seq":1,"time":1,"data":{"turn":1,"step":1,"message":{"id":"a","role":"assistant","content":[{"type":"text","text":"hi"}],"source":{"kind":"model","provider":"p","model":"m"}}}}}`,

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

	valid := []string{
		assistantAttempt(`[{"type":"text-chunks","time0":0,"index":0,"dt":[],"texts":["hi"]}]`),
		assistantAttempt(`[{"type":"reasoning-chunks","time0":0,"index":0,"dt":[0],"texts":["a","b"]}]`),
		assistantAttempt(`[{"type":"tool-call-chunks","time0":0,"index":0,"dt":[],"id":"c","name":"f","args":["{}"]}]`),
		assistantAttempt(`[{"type":"chunk","time":1,"chunk":{"type":"text-delta","index":0,"text":"hi"}}]`),
		assistantAttempt(`[{"type":"chunk","time":2,"chunk":{"type":"finish","reason":{"kind":"stop"}}}]`),
		assistantMessage(`[{"type":"chunk","time":1,"chunk":{"type":"text-delta","index":0,"text":"hi"}}]`),
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

func TestOffloadedMarkerRidesOnlyAnImageBlock(t *testing.T) {
	accepted := []string{
		`{"sessionId":"s","event":{"type":"user/message","seq":1,"time":1,"data":{"id":"m","role":"user","content":[{"type":"image","attachment":{"attachmentId":"a","mediaType":"image/png"},"offloaded":true}],"source":{"kind":"user"}}}}`,
		`{"sessionId":"s","event":{"type":"user/message","seq":1,"time":1,"data":{"id":"m","role":"user","content":[{"type":"image","attachment":{"attachmentId":"a","mediaType":"image/png"}}],"source":{"kind":"user"}}}}`,
	}
	for _, data := range accepted {
		if _, err := DecodeNotification(NotifySessionEvent, []byte(data)); err != nil {
			t.Fatalf("refused %q: %v", data, err)
		}
	}
	refused := []string{
		`{"sessionId":"s","event":{"type":"user/message","seq":1,"time":1,"data":{"id":"m","role":"user","content":[{"type":"image","attachment":{"attachmentId":"a","mediaType":"image/png"},"offloaded":false}],"source":{"kind":"user"}}}}`,
		`{"sessionId":"s","event":{"type":"user/message","seq":1,"time":1,"data":{"id":"m","role":"user","content":[{"type":"text","text":"x","offloaded":true}],"source":{"kind":"user"}}}}`,
	}
	for _, data := range refused {
		if _, err := DecodeNotification(NotifySessionEvent, []byte(data)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %q: %v", data, err)
		}
	}
}

func TestToolErrorCarriesTheUserFacingReason(t *testing.T) {
	data := `{"sessionId":"s","event":{"type":"tool/result","seq":1,"time":1,"data":{"turn":1,"step":1,"error":{"name":"ToolError","code":"ENOENT","reason":"a.txt is not there"},"message":{"id":"t","role":"user","content":[{"type":"tool-result","toolCallId":"c","content":[{"type":"text","text":"x"}],"isError":true}],"source":{"kind":"tool","callId":"c"}}}}}`
	value, err := DecodeNotification(NotifySessionEvent, []byte(data))
	if err != nil {
		t.Fatalf("refused a tool error carrying a reason: %v", err)
	}
	var result ToolResult
	if err := value.(*SessionEventNotification).Event.DataAs(&result); err != nil {
		t.Fatal(err)
	}
	if result.Error == nil || result.Error.Reason != "a.txt is not there" {
		t.Fatalf("reason = %+v", result.Error)
	}
}

func TestWorkspaceChangesIsObservedOnlyAndImageOffloadIsNot(t *testing.T) {
	if !ObservedOnly("workspace/changes") {
		t.Fatal("workspace/changes is required, and this adapter maps nothing of it")
	}
	if ObservedOnly("image/offload") {
		t.Fatal("image/offload rewrites the projected message, so it cannot be skipped unmapped")
	}
}

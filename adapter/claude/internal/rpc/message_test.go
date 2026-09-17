package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestParseMessageClassifications(t *testing.T) {
	cases := []struct {
		name  string
		frame string
		kind  MessageKind
		typ   string
		sub   string
	}{
		{"user", `{"type":"user","message":{"role":"user","content":"hi"},"parent_tool_use_id":null,"session_id":"default","uuid":"u1"}`, KindObservation, TypeUser, ""},
		{"assistant", `{"type":"assistant","message":{"id":"m1","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hi"}]},"parent_tool_use_id":null,"uuid":"a1","session_id":"s1"}`, KindObservation, TypeAssistant, ""},
		{"system init", `{"type":"system","subtype":"init","session_id":"s1","model":"m","tools":[],"uuid":"i1"}`, KindObservation, TypeSystem, "init"},
		{"result", `{"type":"result","subtype":"success","duration_ms":1,"duration_api_ms":1,"is_error":false,"num_turns":1,"session_id":"s1"}`, KindObservation, TypeResult, "success"},
		{"stream event", `{"type":"stream_event","event":{"type":"message_start"},"session_id":"s1","uuid":"e1"}`, KindObservation, TypeStreamEvent, ""},
		{"command lifecycle", `{"type":"command_lifecycle","command_uuid":"u1","state":"queued","session_id":"s1","uuid":"c1"}`, KindObservation, TypeCommandLifecycle, ""},
		{"keep alive", `{"type":"keep_alive"}`, KindObservation, TypeKeepAlive, ""},
		{"unknown type", `{"type":"future_frame","payload":{"anything":true}}`, KindObservation, "future_frame", ""},
		{"control request", `{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"t1"}}`, KindControlRequest, TypeControlRequest, "can_use_tool"},
		{"control response", `{"type":"control_response","response":{"subtype":"success","request_id":"r1","response":{"still_queued":[]}}}`, KindControlResponse, TypeControlResponse, ""},
		{"control cancel", `{"type":"control_cancel_request","request_id":"r1"}`, KindControlCancel, TypeControlCancel, ""},
	}
	for _, testCase := range cases {
		message, err := ParseMessage([]byte(testCase.frame))
		if err != nil {
			t.Fatalf("%s: %v", testCase.name, err)
		}
		if message.Kind != testCase.kind || message.Type != testCase.typ || message.Subtype != testCase.sub {
			t.Fatalf("%s: got kind=%d type=%q subtype=%q", testCase.name, message.Kind, message.Type, message.Subtype)
		}
	}
}

func TestParseControlResponseShapes(t *testing.T) {
	message, err := ParseMessage([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"r1","response":{"still_queued":["a","b"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !message.Response.Success || message.Response.RequestID != "r1" {
		t.Fatalf("response = %+v", message.Response)
	}
	var receipt struct {
		StillQueued []string `json:"still_queued"`
	}
	if err := json.Unmarshal(message.Response.Response, &receipt); err != nil || len(receipt.StillQueued) != 2 {
		t.Fatalf("receipt = %+v err=%v", receipt, err)
	}

	message, err = ParseMessage([]byte(`{"type":"control_response","response":{"subtype":"error","request_id":"r2","error":"boom"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if message.Response.Success || message.Response.Error != "boom" {
		t.Fatalf("error response = %+v", message.Response)
	}
}

func TestParseMessageRejections(t *testing.T) {
	cases := map[string]string{
		"empty":                ``,
		"array":                `["user"]`,
		"leading whitespace":   ` {"type":"user"}`,
		"trailing json":        `{"type":"user"} {"type":"user"}`,
		"duplicate keys":       `{"type":"user","type":"user"}`,
		"missing type":         `{"message":{}}`,
		"empty type":           `{"type":""}`,
		"non-string type":      `{"type":42}`,
		"empty subtype":        `{"type":"system","subtype":""}`,
		"control no id":        `{"type":"control_request","request":{"subtype":"interrupt"}}`,
		"control no request":   `{"type":"control_request","request_id":"r1"}`,
		"control no subtype":   `{"type":"control_request","request_id":"r1","request":{}}`,
		"response no envelope": `{"type":"control_response"}`,
		"response bad state":   `{"type":"control_response","response":{"subtype":"weird","request_id":"r1"}}`,
		"response error empty": `{"type":"control_response","response":{"subtype":"error","request_id":"r1","error":""}}`,
		"cancel no id":         `{"type":"control_cancel_request"}`,
	}
	for name, frame := range cases {
		if _, err := ParseMessage([]byte(frame)); err == nil {
			t.Fatalf("%s: accepted", name)
		} else if !errors.Is(err, ErrInvalidMessage) && !errors.Is(err, ErrInvalidControl) {
			t.Fatalf("%s: wrong error %v", name, err)
		}
	}
}

func TestMarshalCanonicalFrames(t *testing.T) {
	user := UserTurnMessage(json.RawMessage(`{"type":"user","message":{"role":"user","content":"hi"},"parent_tool_use_id":null,"session_id":"default","uuid":"u1","origin":{"kind":"human"}}`))
	encoded, err := user.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}

	want := `{"message":{"role":"user","content":"hi"},"origin":{"kind":"human"},"parent_tool_use_id":null,"session_id":"default","type":"user","uuid":"u1"}`
	if !bytes.Equal(encoded, []byte(want)) {
		t.Fatalf("user frame = %s", encoded)
	}
	if _, err := ParseMessage(encoded); err != nil {
		t.Fatalf("round trip: %v", err)
	}

	request := ControlRequestMessage("req_1_ab", json.RawMessage(`{"subtype":"interrupt"}`))
	encoded, err = request.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	want = `{"request":{"subtype":"interrupt"},"request_id":"req_1_ab","type":"control_request"}`
	if !bytes.Equal(encoded, []byte(want)) {
		t.Fatalf("control request = %s", encoded)
	}

	response := ControlResponseMessage(json.RawMessage(`{"subtype":"success","request_id":"req_1_ab","response":{"behavior":"allow"}}`))
	encoded, err = response.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	want = `{"response":{"request_id":"req_1_ab","response":{"behavior":"allow"},"subtype":"success"},"type":"control_response"}`
	if !bytes.Equal(encoded, []byte(want)) {
		t.Fatalf("control response = %s", encoded)
	}
}

func TestMarshalRejections(t *testing.T) {
	if _, err := (Message{Kind: KindObservation, Type: TypeAssistant, Raw: json.RawMessage(`{"type":"assistant"}`)}).MarshalJSON(); err == nil {
		t.Fatal("non-user observation write accepted")
	}
	if _, err := (Message{Kind: KindObservation, Type: TypeUser, Raw: json.RawMessage(`{"type":"user"}`)}).MarshalJSON(); err == nil {
		t.Fatal("user frame without message accepted")
	}
	if _, err := (Message{Kind: KindControlRequest, RequestID: "", Raw: json.RawMessage(`{"subtype":"interrupt"}`)}).MarshalJSON(); err == nil {
		t.Fatal("control request without id accepted")
	}
	if _, err := (Message{Kind: KindControlRequest, RequestID: "r1", Raw: json.RawMessage(`{}`)}).MarshalJSON(); err == nil {
		t.Fatal("control request without subtype accepted")
	}
	if _, err := (Message{Kind: KindControlResponse, Raw: json.RawMessage(`{"subtype":"success"}`)}).MarshalJSON(); err == nil {
		t.Fatal("control response without request_id accepted")
	}
}

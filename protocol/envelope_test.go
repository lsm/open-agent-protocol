package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEnvelopeUnknownFieldAndPayloadRoundTrip(t *testing.T) {
	input := `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"content.delta","id":"e1","session_id":"s1","run_id":"r1","sequence":1,"future":{"enabled":true},"payload":{"session_id":"s1","run_id":"r1","part":{"type":"text","text":"hello"},"payload_future":7}}`
	envelope, err := ParseEnvelope([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := envelope.Unknown["future"]; !ok {
		t.Fatal("unknown field was not retained")
	}
	var payload ContentDeltaPayload
	if err := envelope.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Part.Text != "hello" {
		t.Fatalf("text = %q", payload.Part.Text)
	}
	output, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["future"]) != `{"enabled":true}` {
		t.Fatalf("future = %s", got["future"])
	}
	var rawPayload map[string]json.RawMessage
	if err := json.Unmarshal(got["payload"], &rawPayload); err != nil {
		t.Fatal(err)
	}
	if string(rawPayload["payload_future"]) != "7" {
		t.Fatalf("payload_future = %s", rawPayload["payload_future"])
	}
}

func TestDecodeSingleArrayAndJSONL(t *testing.T) {
	one := `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.request","id":"e1","payload":{}}`
	two := strings.Replace(one, `"e1"`, `"e2"`, 1)

	tests := []struct {
		name string
		data string
		want int
	}{
		{"single", one, 1},
		{"array", "[" + one + "," + two + "]", 2},
		{"jsonl", one + "\n\n" + two + "\n", 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelopes, err := Decode(strings.NewReader(test.data))
			if err != nil {
				t.Fatal(err)
			}
			if len(envelopes) != test.want {
				t.Fatalf("len = %d, want %d", len(envelopes), test.want)
			}
		})
	}
}

func TestNewEnvelopeAndTypedPayload(t *testing.T) {
	request := MessageSubmitRequest{
		SessionID: "s1",
		Messages:  []Message{{ID: "m1", Role: RoleUser, Content: TextContent("hello")}},
		Delivery:  DeliveryAuto,
	}
	envelope, err := NewEnvelope(TypeSessionMessageSubmitRequest, "e1", request)
	if err != nil {
		t.Fatal(err)
	}
	var decoded MessageSubmitRequest
	if err := envelope.DecodePayload(&decoded); err != nil {
		t.Fatal(err)
	}
	text, ok := decoded.Messages[0].Content.Text()
	if !ok || text != "hello" {
		t.Fatalf("content = %q, %v", text, ok)
	}

	var output bytes.Buffer
	if err := Encode(&output, []Envelope{envelope}, FormatJSONL); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(output.Bytes(), []byte("\n")) {
		t.Fatal("JSONL output lacks newline")
	}
}

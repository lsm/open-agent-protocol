package protocol

import (
	"encoding/json"
	"testing"
)

func TestAProviderEnvelopeCarriesItsInferenceAndSequenceThroughARoundTrip(t *testing.T) {
	input := `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.part.delta","id":"e2","in_reply_to":"e1","inference_id":"inf-1","sequence":3,"timestamp_ms":9,"capability_revision":"rev-1","extensions":{"x.y":1},"payload":{"part_index":0,"delta":"hi"}}`
	envelope, err := ParseProviderEnvelope([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if envelope.InferenceID != "inf-1" || envelope.Sequence == nil || *envelope.Sequence != 3 || envelope.TimestampMS == nil || *envelope.TimestampMS != 9 {
		t.Fatalf("parsed %+v", envelope)
	}
	if envelope.Type != "inference.part.delta" || envelope.InReplyTo != "e1" || envelope.CapabilityRevision != "rev-1" || string(envelope.Extensions["x.y"]) != "1" {
		t.Fatalf("parsed %+v", envelope)
	}
	var payload struct {
		Delta string `json:"delta"`
	}
	if err := envelope.DecodePayload(&payload); err != nil || payload.Delta != "hi" {
		t.Fatalf("payload %+v, %v", payload, err)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseProviderEnvelope(encoded)
	if err != nil || again.InferenceID != "inf-1" || *again.Sequence != 3 {
		t.Fatalf("the round trip read %+v, %v", again, err)
	}
}

func TestANewProviderEnvelopeIsInTheProviderProfileAndParsingRefusesTrailingData(t *testing.T) {
	envelope, err := NewProviderEnvelope("inference.cancel.request", "e1", map[string]any{"reason": "caller_closed"})
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Protocol != Protocol || envelope.Version != Version || envelope.Profile != ProviderProfile || envelope.InferenceID != "" {
		t.Fatalf("built %+v", envelope)
	}
	encoded, _ := json.Marshal(envelope)
	var members map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &members); err != nil {
		t.Fatal(err)
	}
	if _, present := members["inference_id"]; present {
		t.Fatalf("an envelope with no inference carries inference_id: %s", encoded)
	}
	if _, err := ParseProviderEnvelope(append(encoded, []byte(" {}")...)); err == nil {
		t.Fatal("a provider envelope followed by another value parsed")
	}
}

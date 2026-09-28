package sdk

import (
	"encoding/json"
	"testing"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

func TestTheOAPPathBuildsAProtocolEnvelopeAndTheLegacyPathAFrame(t *testing.T) {
	envelope := oapFrame(oapAgent, "session.open.request", map[string]any{"session_id": "s1"})
	if envelope.Protocol != oapProtocol || envelope.Version != oapVersion || envelope.Profile != oapAgent {
		t.Fatalf("the OAP path built %+v, which is not the envelope the protocol package describes", envelope)
	}
	if envelope.Type != protocol.TypeSessionOpenRequest {
		t.Fatalf("the OAP path built the type %q, so it is naming the wire in its own vocabulary", envelope.Type)
	}
	if envelope.ID == "" || len(envelope.Payload) == 0 {
		t.Fatalf("the OAP path built an envelope with no id or payload: %+v", envelope)
	}
	legacy := &frame{Type: "agent_start", ID: newULID(), Version: 1}
	if legacy.Version == nil {
		t.Fatal("the legacy frame lost its untyped version, which the V1 wire needs")
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || !json.Valid(encoded) {
		t.Fatalf("the legacy frame no longer marshals: %q", encoded)
	}
}

func TestTheEnvelopeTheOAPPathWritesIsTheOneTheProtocolPackageDecodes(t *testing.T) {
	envelope := oapFrame(oapProvider, "inference.create.request", map[string]any{"model_ref": "fixture/other:test"})
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var decoded protocol.Envelope
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("the protocol package cannot read back what the SDK writes: %v", err)
	}
	if decoded.Type != envelope.Type || decoded.ID != envelope.ID || decoded.Profile != envelope.Profile {
		t.Fatalf("the round trip lost the envelope: wrote %+v, read %+v", envelope, decoded)
	}
	if decoded.CapabilityRevision != "" {
		t.Fatalf("a request carries the capability revision %q before the transport stamps it", decoded.CapabilityRevision)
	}
}

func TestAFrameOnTheOAPWireBecomesTheEnvelopeTheProtocolPackageDescribes(t *testing.T) {
	f := &frame{Protocol: oapProtocol, Version: "0.1", Profile: oapAgent, Type: "capabilities.response",
		ID: "env-1", InReplyTo: "env-0", SessionID: "s1", RunID: "run-1", CapabilityRevision: "rev-1",
		Sequence: 7, Payload: json.RawMessage(`{"capability_revision":"rev-1"}`)}
	envelope := asEnvelope(f)
	if envelope.Type != "capabilities.response" || envelope.ID != "env-1" || envelope.InReplyTo != "env-0" {
		t.Fatalf("the conversion lost the envelope's own members: %+v", envelope)
	}
	if envelope.SessionID != "s1" || envelope.RunID != "run-1" || envelope.CapabilityRevision != "rev-1" {
		t.Fatalf("the conversion lost the addressing members: %+v", envelope)
	}
	if envelope.Sequence == nil || *envelope.Sequence != 7 {
		t.Fatalf("the conversion read the sequence as %v, want 7", envelope.Sequence)
	}
	if payload := envelopePayload(envelope); payload.str("capability_revision") != "rev-1" {
		t.Fatalf("the payload is not readable through the envelope: %+v", payload)
	}
}

func TestTheConversionSubstitutesTheOAPVersionForALegacyNumberAndKeepsTheOAPOne(t *testing.T) {
	legacy := asEnvelope(&frame{Protocol: oapProtocol, Profile: oapAgent, Type: "ready", Version: float64(1)})
	if legacy.Version != oapVersion {
		t.Fatalf("a legacy frame carrying the number 1 became version %q, want the OAP version %q: the conversion runs on the OAP path, where a numeric version cannot be the wire's", legacy.Version, oapVersion)
	}
	oap := asEnvelope(&frame{Protocol: oapProtocol, Profile: oapAgent, Type: "capabilities.response", Version: "0.1"})
	if oap.Version != oapVersion {
		t.Fatalf("the conversion rewrote the OAP version to %q, want %q", oap.Version, oapVersion)
	}
}

package inferenceserve

import (
	"encoding/json"
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/provider"
)

func TestTheFirstScopedEnvelopeOpensTheSequenceAtOneAndItNeverRepeats(t *testing.T) {
	state := NewState("i1", "m")
	if _, err := state.Started(1); err != nil {
		t.Fatal(err)
	}
	var last int
	for _, typ := range []string{"inference.part.started", "inference.part.delta", "inference.part.ended", "inference.completed"} {
		envelope, err := state.emit(typ, "", struct{}{})
		if err != nil {
			t.Fatal(err)
		}
		if envelope.Sequence <= last {
			t.Fatalf("%s carries sequence %d after %d: the validator fails a repeat or a gap", typ, envelope.Sequence, last)
		}
		last = envelope.Sequence
	}
	if last != 5 {
		t.Errorf("the sequence reached %d, want 5: started plus four scoped events, numbered from one", last)
	}
}
func TestEveryEnvelopeCarriesTheProtocolHeader(t *testing.T) {
	state := NewState("i1", "m")
	started, err := state.Started(1)
	if err != nil {
		t.Fatal(err)
	}
	part, err := state.emit("inference.part.started", "", Part{PartIndex: 0, PartKind: "text"})
	if err != nil {
		t.Fatal(err)
	}
	all := []Envelope{started, part}
	for _, envelope := range all {
		if envelope.Protocol != Protocol || envelope.Version != Version || envelope.Profile != Profile {
			t.Errorf("%s carries %q/%q/%q, want the header every fixture declares", envelope.Type, envelope.Protocol, envelope.Version, envelope.Profile)
		}
		if envelope.ID == "" {
			t.Errorf("%s carries no id", envelope.Type)
		}
		if envelope.InferenceID != "i1" {
			t.Errorf("%s carries inference_id %q, want i1", envelope.Type, envelope.InferenceID)
		}
	}
}
func TestAnEmptyTerminalStillCarriesAPartArray(t *testing.T) {
	state := NewState("i1", "m")
	envelope, err := state.emit("inference.completed", "", struct {
		StopReason string          `json:"stop_reason"`
		Message    TerminalMessage `json:"message"`
	}{StopReason: "stop", Message: TerminalMessage{Role: "assistant", Content: partsOf(nil)}})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(envelope.Payload)
	if !contains(raw, `"content":[]`) {
		t.Errorf("the terminal payload = %s, want an empty array rather than null: the validator fails content that is not a part array", raw)
	}
}
func TestOnlyTheScopedTypesCarryASequence(t *testing.T) {
	scopedTypes := map[string]bool{
		"inference.started": true, "inference.part.started": true, "inference.part.delta": true,
		"inference.part.ended": true, "inference.completed": true, "inference.failed": true,
	}
	all := map[string]bool{
		"inference.create.request": true, "inference.create.response": true,
		"inference.started": true, "inference.part.started": true, "inference.part.delta": true,
		"inference.part.ended": true, "inference.completed": true, "inference.failed": true,
	}
	for typ := range all {
		if got := scoped(typ); got != scopedTypes[typ] {
			t.Errorf("scoped(%q) = %v, want %v: the validator's scoped set is exactly these six", typ, got, scopedTypes[typ])
		}
	}
	if scoped("inference.create.response") {
		t.Error("a create response is not scoped: the validator reads no sequence on it")
	}
}
func TestACreateResponseCarriesNoSequenceNorInferenceID(t *testing.T) {
	envelope := Envelope{
		Protocol: Protocol, Version: Version, Profile: Profile,
		Type: "inference.create.response", ID: "c1", InReplyTo: "c0",
		Payload: []byte(`{"accepted":true}`),
	}
	held, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(held, &document); err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{"sequence", "inference_id"} {
		if _, present := document[member]; present {
			t.Errorf("the create response carries %q, want it absent: a refused or accepted create allocates nothing scoped", member)
		}
	}
	if document["in_reply_to"] != "c0" {
		t.Errorf("the create response = %v, want it in reply to c0", document)
	}
}
func TestACreateResponseIsUnscopedWhileTheStartedEnvelopeOpensTheSequence(t *testing.T) {
	state := NewState("i1", "m")
	accepted, err := state.Accepted("c0", map[string]string{"include_snapshot": "never"})
	if err != nil {
		t.Fatal(err)
	}
	held, err := json.Marshal(accepted)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(held, &document); err != nil {
		t.Fatal(err)
	}
	if _, present := document["sequence"]; present {
		t.Error("an accepted create response carries a sequence: it is not a scoped event")
	}
	if document["in_reply_to"] != "c0" || document["type"] != "inference.create.response" {
		t.Errorf("the create response = %v", document)
	}
	started, err := state.Started(1)
	if err != nil {
		t.Fatal(err)
	}
	if started.Sequence != 1 {
		t.Errorf("the first scoped envelope carries sequence %d, want 1: the create response must not have consumed one", started.Sequence)
	}
}
func TestARefusedCreateAllocatesNoInferenceAndNothingScopedFollows(t *testing.T) {
	held2 := NewState("i-already-allocated", "m")
	refused, err := held2.Refused("c0", "model_not_found", "no such model")
	if err != nil {
		t.Fatal(err)
	}
	body := payloads(t, []Envelope{refused})[0]
	if body["accepted"] != false {
		t.Errorf("a refused create = %v, want accepted false", body)
	}
	failure, ok := body["error"].(map[string]any)
	if !ok || failure["code"] != "model_not_found" || failure["message"] != "no such model" {
		t.Errorf("the refusal = %v, want its code and message", body["error"])
	}
	held, err := json.Marshal(refused)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(held, &document); err != nil {
		t.Fatal(err)
	}
	if _, present := document["inference_id"]; present {
		t.Errorf("a refused create carries inference_id %v, want none: the fixture shows a refusal allocating nothing, and a state that already holds an id must not leak it here", document["inference_id"])
	}
}

func textEvent(kind provider.EventKind, index int, delta string) provider.Event {
	return provider.Event{Kind: kind, ContentIndex: index, Delta: delta}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func typesOf(envelopes []Envelope) []string {
	out := make([]string, 0, len(envelopes))
	for _, envelope := range envelopes {
		out = append(out, envelope.Type)
	}
	return out
}

func sameTypes(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func payloads(t *testing.T, envelopes []Envelope) []map[string]any {
	t.Helper()
	out := make([]map[string]any, 0, len(envelopes))
	for _, envelope := range envelopes {
		var document map[string]any
		if err := json.Unmarshal(envelope.Payload, &document); err != nil {
			t.Fatalf("the payload of %s is not json: %v", envelope.Type, err)
		}
		out = append(out, document)
	}
	return out
}

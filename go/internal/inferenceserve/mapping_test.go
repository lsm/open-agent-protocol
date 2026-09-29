package inferenceserve

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/provider"
)

func body(t *testing.T, envelope Envelope) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(envelope.Payload, &document); err != nil {
		t.Fatalf("the payload of %s is not json: %v", envelope.Type, err)
	}
	return document
}

func keysOf(t *testing.T, value any) []string {
	t.Helper()
	held, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(held, &generic); err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(generic))
	for name := range generic {
		out = append(out, name)
	}
	return out
}

func has(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func only(names []string, allowed ...string) string {
	for _, name := range names {
		permitted := false
		for _, ok := range allowed {
			if name == ok {
				permitted = true
			}
		}
		if !permitted {
			return name
		}
	}
	return ""
}

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

func TestOnlyTheSixScopedTypesCarryASequenceAndAnInferenceID(t *testing.T) {
	scopedTypes := []string{"inference.started", "inference.part.started", "inference.part.delta",
		"inference.part.ended", "inference.completed", "inference.failed"}
	every := append([]string{"inference.create.request", "inference.create.response"}, scopedTypes...)
	for _, typ := range every {
		want := false
		for _, candidate := range scopedTypes {
			if typ == candidate {
				want = true
			}
		}
		if got := scoped(typ); got != want {
			t.Errorf("scoped(%q) = %v, want %v: the validator's scoped set is exactly these six", typ, got, want)
		}
	}
	state := NewState("i1", "m")
	for _, typ := range every {
		envelope, err := state.emit(typ, "", struct{}{})
		if err != nil {
			t.Fatal(err)
		}
		if scoped(typ) && (envelope.Sequence == 0 || envelope.InferenceID != "i1") {
			t.Errorf("%s is scoped but carries sequence %d and inference_id %q", typ, envelope.Sequence, envelope.InferenceID)
		}
		if !scoped(typ) && (envelope.Sequence != 0 || envelope.InferenceID != "") {
			t.Errorf("%s is not scoped but carries sequence %d and inference_id %q", typ, envelope.Sequence, envelope.InferenceID)
		}
	}
}

func TestACreateResponseIsUnscopedAndTheStartedEnvelopeStillOpensAtOne(t *testing.T) {
	state := NewState("i1", "m")
	accepted, err := state.Accepted("c0", Honoured{IncludeSnapshot: "never"})
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
	if _, present := document["inference_id"]; present {
		t.Error("an accepted create response carries an inference id before the inference has started")
	}
	if document["in_reply_to"] != "c0" {
		t.Errorf("the create response = %v, want it in reply to c0", document)
	}
	started, err := state.Started(1)
	if err != nil {
		t.Fatal(err)
	}
	if started.Sequence != 1 {
		t.Errorf("the first scoped envelope carries sequence %d, want 1: the create response must not have consumed one", started.Sequence)
	}
}

func TestAnAcceptedCreateCarriesItsHonouredAndNothingElse(t *testing.T) {
	accepted, err := NewState("i1", "m").Accepted("c0", Honoured{IncludeSnapshot: "on_part_end"})
	if err != nil {
		t.Fatal(err)
	}
	document := body(t, accepted)
	if document["accepted"] != true {
		t.Errorf("an accepted create = %v, want accepted true", document)
	}
	honoured, ok := document["honoured"].(map[string]any)
	if !ok || honoured["include_snapshot"] != "on_part_end" {
		t.Errorf("honoured = %v, want the snapshot policy the caller asked for", document["honoured"])
	}
	if names := keysOf(t, document); has(names, "error") {
		t.Errorf("an accepted create carries an error too: %v", names)
	}
	if stray := only(keysOf(t, honoured), "include_snapshot"); stray != "" {
		t.Errorf("honoured carries %q, want include_snapshot alone: the schema forbids the rest", stray)
	}
}

func TestARefusedCreateAllocatesNoInferenceAndNothingScopedCanFollowIt(t *testing.T) {
	held := NewState("i-already-allocated", "m")
	refused, err := held.Refused("c0", "model_not_found", "no such model")
	if err != nil {
		t.Fatal(err)
	}
	document := body(t, refused)
	if document["accepted"] != false {
		t.Errorf("a refused create = %v, want accepted false", document)
	}
	failure, ok := document["error"].(map[string]any)
	if !ok || failure["code"] != "model_not_found" || failure["message"] != "no such model" {
		t.Errorf("the refusal = %v, want its code and message", document["error"])
	}
	raw, err := json.Marshal(refused)
	if err != nil {
		t.Fatal(err)
	}
	var whole map[string]any
	if err := json.Unmarshal(raw, &whole); err != nil {
		t.Fatal(err)
	}
	if _, present := whole["inference_id"]; present {
		t.Errorf("a refused create carries inference_id %v, want none: a state that already holds an id must not leak it", whole["inference_id"])
	}
	if _, present := document["honoured"]; present {
		t.Error("a refused create carries honoured: the schema pairs accepted false with an error, not a policy")
	}
	if _, err := held.Started(1); !errors.Is(err, ErrRefused) {
		t.Errorf("a scoped envelope after a refusal = %v, want it refused: there is no inference to scope it to", err)
	}
}

func TestEveryEnvelopeCarriesTheProtocolHeader(t *testing.T) {
	state := NewState("i1", "m")
	started, err := state.Started(1)
	if err != nil {
		t.Fatal(err)
	}
	part, err := state.emit("inference.part.started", "", PartStarted{PartIndex: 0, PartKind: "text"})
	if err != nil {
		t.Fatal(err)
	}
	for _, envelope := range []Envelope{started, part} {
		if envelope.Protocol != Protocol || envelope.Version != Version || envelope.Profile != Profile {
			t.Errorf("%s carries %q/%q/%q, want the header every fixture declares", envelope.Type, envelope.Protocol, envelope.Version, envelope.Profile)
		}
		if envelope.ID == "" {
			t.Errorf("%s carries no id", envelope.Type)
		}
	}
}

func TestARefusalIsAFailurePayloadAndNotAReason(t *testing.T) {
	envelope, err := NewState("i1", "m").Failed("provider_unavailable", "the provider stream failed")
	if err != nil {
		t.Fatal(err)
	}
	document := body(t, envelope)
	if document["code"] != "provider_unavailable" || document["message"] != "the provider stream failed" {
		t.Errorf("the failure = %v", document)
	}
	if names := keysOf(t, document); len(names) != 2 {
		t.Errorf("a failure payload carries %v, want a code and a message alone", names)
	}
}

func TestAToolCallPartStartedIsFlatAndATextPartCarriesNoIdentity(t *testing.T) {
	call := PartStarted{PartIndex: 0, PartKind: "tool_call", ToolCallID: "tc1", Name: "lookup"}
	names := keysOf(t, call)
	if !has(names, "tool_call_id") || !has(names, "name") {
		t.Errorf("a tool call part.started = %v, want its id and name at the top level", names)
	}
	if stray := only(names, "part_index", "part_kind", "tool_call_id", "name"); stray != "" {
		t.Errorf("part.started carries %q, which the schema forbids on this shape", stray)
	}
	text := PartStarted{PartIndex: 1, PartKind: "text"}
	names = keysOf(t, text)
	if has(names, "tool_call_id") || has(names, "name") {
		t.Errorf("a text part.started = %v, want no identity: the schema forbids it on this branch", names)
	}
}

func TestAToolCallPartEndedNestsItsCallAndCarriesNoText(t *testing.T) {
	ended := PartEnded{PartIndex: 0, PartKind: "tool_call", ToolCall: &EndedToolCall{
		ToolCallID: "tc1", Name: "lookup", ArgumentsJSON: json.RawMessage(`{"city":"Kyoto"}`),
	}}
	names := keysOf(t, ended)
	if has(names, "text") {
		t.Errorf("a tool call part.ended = %v, want no text: the schema forbids it on this branch", names)
	}
	if has(names, "tool_call_id") || has(names, "name") || has(names, "arguments_json") {
		t.Errorf("a tool call part.ended = %v, want the call nested under tool_call, not flat", names)
	}
	if stray := only(names, "part_index", "part_kind", "text", "tool_call", "carry"); stray != "" {
		t.Errorf("part.ended carries %q, which the schema forbids", stray)
	}
	call := ended.ToolCall
	if call == nil || call.ToolCallID != "tc1" || call.Name != "lookup" {
		t.Fatalf("the nested call = %+v, want its id and name", call)
	}
	arguments, ok := decode(t, call.ArgumentsJSON).(map[string]any)
	if !ok || arguments["city"] != "Kyoto" {
		t.Errorf("arguments_json = %v, want the arguments as an object", call.ArgumentsJSON)
	}
	envelope, err := NewState("i1", "m").emit("inference.part.ended", "", ended)
	if err != nil {
		t.Fatal(err)
	}
	nested, ok := body(t, envelope)["tool_call"].(map[string]any)
	if !ok {
		t.Fatalf("the emitted payload = %s, want a nested tool_call object carrying the three members the schema requires", envelope.Payload)
	}
	for _, required := range []string{"tool_call_id", "name", "arguments_json"} {
		if _, present := nested[required]; !present {
			t.Errorf("the nested call lacks %q: the schema requires all three", required)
		}
	}
}

func TestATextPartEndedCarriesItsTextAndNoCall(t *testing.T) {
	held := "hello"
	ended := PartEnded{PartIndex: 0, PartKind: "text", Text: &held}
	names := keysOf(t, ended)
	if has(names, "tool_call") {
		t.Errorf("a text part.ended = %v, want no tool_call", names)
	}
	if ended.Text == nil || *ended.Text != "hello" {
		t.Errorf("the ended text = %v, want it present", ended.Text)
	}
	empty := ""
	blank := PartEnded{PartIndex: 1, PartKind: "text", Text: &empty}
	if !has(keysOf(t, blank), "text") {
		t.Error("an empty text part.ended omits text: the schema requires it, and a pointer is what distinguishes empty from absent")
	}
	terminal := partsOf([]provider.AssistantBlock{{Text: &provider.TextPart{Text: ""}}})
	if len(terminal) != 1 {
		t.Errorf("a turn whose only text is empty assembled %d blocks, want 1: dropping it would silently lose a part the stream did open", len(terminal))
	}
}

func TestNoPayloadAnywhereCarriesAThoughtSignature(t *testing.T) {
	terminal := partsOf([]provider.AssistantBlock{{ToolCall: &provider.ToolCall{
		ID: "tc1", Name: "lookup", Arguments: `{"city":"Kyoto"}`, ThoughtSig: "sig-1",
	}}})
	if len(terminal) != 1 {
		t.Fatalf("the terminal content = %v, want one block", terminal)
	}
	block := terminal[0]
	if block.Carry != "sig-1" {
		t.Errorf("carry = %q, want the signature: the schema has no thought_signature member and a signature that does not round-trip is lost", block.Carry)
	}
	names := keysOf(t, block)
	if has(names, "thought_signature") {
		t.Errorf("a terminal tool_call block = %v, want no thought_signature: no schema defines one", names)
	}
	if stray := only(names, "type", "tool_call_id", "name", "arguments_json", "carry"); stray != "" {
		t.Errorf("a terminal tool_call block carries %q, which the schema forbids", stray)
	}
	if block.ArgumentsJSON == nil {
		t.Error("a terminal tool_call block omits arguments_json: the schema requires it")
	}
}

func TestAToolCallWithNoArgumentsStillCarriesTheMemberTheSchemaRequires(t *testing.T) {
	terminal := partsOf([]provider.AssistantBlock{{ToolCall: &provider.ToolCall{ID: "tc1", Name: "lookup"}}})
	arguments, ok := decode(t, terminal[0].ArgumentsJSON).(map[string]any)
	if !ok || len(arguments) != 0 {
		t.Errorf("arguments_json = %v, want an empty object rather than an absent member", terminal[0].ArgumentsJSON)
	}
}

func TestTheTerminalAssemblesTheEndedPartsInOrder(t *testing.T) {
	thinking := provider.ThinkingPart{Thinking: "think", Signature: "sig-1"}
	text := "hello"
	terminal := partsOf([]provider.AssistantBlock{
		{Thinking: &thinking},
		{Text: &provider.TextPart{Text: text}},
		{ToolCall: &provider.ToolCall{ID: "tc1", Name: "lookup", Arguments: `{"city":"Kyoto"}`}},
	})
	if len(terminal) != 3 {
		t.Fatalf("the terminal content = %v, want three parts", terminal)
	}
	if terminal[0].Type != "reasoning" || terminal[0].Reasoning != "think" || terminal[0].Carry != "sig-1" {
		t.Errorf("the first block = %+v, want the reasoning and its carry", terminal[0])
	}
	if terminal[1].Type != "text" || terminal[1].Text == nil || *terminal[1].Text != "hello" {
		t.Errorf("the second block = %+v, want the text", terminal[1])
	}
	if terminal[2].Type != "tool_call" || terminal[2].ToolCallID != "tc1" {
		t.Errorf("the third block = %+v, want the call in the order the parts ended", terminal[2])
	}
	envelope, err := NewState("i1", "m").Completed("tool_use", terminal)
	if err != nil {
		t.Fatal(err)
	}
	document := body(t, envelope)
	if document["stop_reason"] != "tool_use" {
		t.Errorf("the stop reason = %v", document["stop_reason"])
	}
	message := document["message"].(map[string]any)
	if message["role"] != "assistant" {
		t.Errorf("the terminal role = %v, want assistant", message["role"])
	}
	if len(message["content"].([]any)) != 3 {
		t.Errorf("the terminal content = %v, want the three ended parts assembled back", message["content"])
	}
}

func TestAnEmptyTerminalStillCarriesAPartArray(t *testing.T) {
	envelope, err := NewState("i1", "m").Completed("stop", partsOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	raw := string(envelope.Payload)
	if !contains(raw, `"content":[]`) {
		t.Errorf("the terminal payload = %s, want an empty array rather than null: the validator fails content that is not a part array", raw)
	}
}

func decode(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("not json: %v", err)
	}
	return value
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

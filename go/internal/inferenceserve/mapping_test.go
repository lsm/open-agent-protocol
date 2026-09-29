package inferenceserve

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/provider"
	"github.com/lsm/open-agent-protocol/go/validation"
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
	state := NewState(&Ids{}, "i1", "m")
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
	state := NewState(&Ids{}, "i1", "m")
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
	state := NewState(&Ids{}, "i1", "m")
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
	if document["inference_id"] != "i1" {
		t.Errorf("an accepted create carries inference_id %v, want i1: the envelope schema requires it on the accepted branch and both fixtures carry it", document["inference_id"])
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
	accepted, err := NewState(&Ids{}, "i1", "m").Accepted("c0", Honoured{IncludeSnapshot: "on_part_end"})
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
	held := NewState(&Ids{}, "i-already-allocated", "m")
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
	state := NewState(&Ids{}, "i1", "m")
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

func TestAFailureNestsItsErrorTheWayTheSchemaRequires(t *testing.T) {
	envelope, err := NewState(&Ids{}, "i1", "m").Failed("provider_unavailable", "the provider stream failed")
	if err != nil {
		t.Fatal(err)
	}
	document := body(t, envelope)
	failure, ok := document["error"].(map[string]any)
	if !ok {
		t.Fatalf("the failure payload = %s, want the code and message nested under error", envelope.Payload)
	}
	if failure["code"] != "provider_unavailable" || failure["message"] != "the provider stream failed" {
		t.Errorf("the failure = %v", failure)
	}
	if names := keysOf(t, document); len(names) != 1 || !has(names, "error") {
		t.Errorf("a failure payload carries %v, want error alone: a flat code or message is not a member the schema knows", names)
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
	envelope, err := NewState(&Ids{}, "i1", "m").emit("inference.part.ended", "", ended)
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

func TestNoPayloadThisLayerCanEmitCarriesAThoughtSignature(t *testing.T) {
	call := provider.ToolCall{ID: "tc1", Name: "lookup", Arguments: `{"city":"Kyoto"}`, ThoughtSig: "sig-1"}
	thinking := provider.ThinkingPart{Thinking: "think", Signature: "sig-1"}
	text := "hello"
	state := NewState(&Ids{}, "i1", "m")
	var envelopes []Envelope
	add := func(envelope Envelope, err error) {
		if err != nil {
			t.Fatal(err)
		}
		envelopes = append(envelopes, envelope)
	}
	add(state.Accepted("c0", Honoured{IncludeSnapshot: "never"}))
	add(state.Started(1))
	add(state.emit("inference.part.started", "", PartStarted{PartIndex: 0, PartKind: "text"}))
	add(state.emit("inference.part.started", "", PartStarted{PartIndex: 0, PartKind: "tool_call", ToolCallID: "tc1", Name: "lookup"}))
	add(state.emit("inference.part.delta", "", struct {
		PartIndex int    `json:"part_index"`
		Delta     string `json:"delta"`
	}{PartIndex: 0, Delta: "a"}))
	add(state.emit("inference.part.ended", "", PartEnded{PartIndex: 0, PartKind: "text", Text: &text}))
	add(state.emit("inference.part.ended", "", PartEnded{PartIndex: 1, PartKind: "reasoning", Text: &thinking.Thinking, Carry: thinking.Signature}))
	add(state.emit("inference.part.ended", "", PartEnded{PartIndex: 2, PartKind: "tool_call", ToolCall: &EndedToolCall{
		ToolCallID: call.ID, Name: call.Name, ArgumentsJSON: json.RawMessage(call.Arguments),
	}}))
	add(state.Completed("tool_use", partsOf([]provider.AssistantBlock{
		{Thinking: &thinking}, {Text: &provider.TextPart{Text: text}}, {ToolCall: &call},
	})))
	add(state.Failed("provider_unavailable", "the provider stream failed"))
	refused, err := state.Refused("c1", "model_not_found", "no such model")
	if err != nil {
		t.Fatal(err)
	}
	envelopes = append(envelopes, refused)

	if len(envelopes) < 10 {
		t.Fatalf("only %d envelopes were built, want the whole set: a scan of a subset is not a scan", len(envelopes))
	}
	for _, envelope := range envelopes {
		raw := string(envelope.Payload)
		if strings.Contains(raw, "thought_signature") {
			t.Errorf("%s carries a thought_signature: %s", envelope.Type, raw)
		}
	}
	block := partsOf([]provider.AssistantBlock{{ToolCall: &call}})[0]
	if block.Carry != "sig-1" {
		t.Errorf("carry = %q, want the signature: no schema has a thought_signature, and a signature that does not round trip is lost", block.Carry)
	}
	if stray := only(keysOf(t, block), "type", "tool_call_id", "name", "arguments_json", "carry"); stray != "" {
		t.Errorf("a terminal tool_call block carries %q, which the schema forbids", stray)
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
	if terminal[0].Type != "reasoning" || terminal[0].Reasoning == nil || *terminal[0].Reasoning != "think" || terminal[0].Carry != "sig-1" {
		t.Errorf("the first block = %+v, want the reasoning and its carry", terminal[0])
	}
	if terminal[1].Type != "text" || terminal[1].Text == nil || *terminal[1].Text != "hello" {
		t.Errorf("the second block = %+v, want the text", terminal[1])
	}
	if terminal[2].Type != "tool_call" || terminal[2].ToolCallID != "tc1" {
		t.Errorf("the third block = %+v, want the call in the order the parts ended", terminal[2])
	}
	envelope, err := NewState(&Ids{}, "i1", "m").Completed("tool_use", terminal)
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

func TestAnEmptyTerminalCarriesTheEmptyStringTheSchemaAllows(t *testing.T) {
	envelope, err := NewState(&Ids{}, "i1", "m").Completed("stop", partsOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	message := body(t, envelope)["message"].(map[string]any)
	held, present := message["content"]
	if !present {
		t.Fatalf("the terminal payload = %s, want a content member: message requires it", envelope.Payload)
	}
	if text, ok := held.(string); !ok || text != "" {
		t.Errorf("content = %v, want the empty string: the schema allows a string or an array of one or more, and an empty array is neither", held)
	}
}

func TestAReasoningBlockWithEmptyTextStillCarriesItsMember(t *testing.T) {
	terminal := partsOf([]provider.AssistantBlock{{Thinking: &provider.ThinkingPart{Thinking: ""}}})
	if len(terminal) != 1 {
		t.Fatalf("a turn whose only reasoning is empty assembled %d blocks, want 1", len(terminal))
	}
	names := keysOf(t, terminal[0])
	if !has(names, "reasoning") {
		t.Errorf("the reasoning block = %v, want its member present: a plain string with omitempty drops an empty one, which the schema requires", names)
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

func buildFullTrace(t *testing.T) []Envelope {
	t.Helper()
	call := provider.ToolCall{ID: "tc1", Name: "lookup", Arguments: `{"city":"Kyoto"}`, ThoughtSig: "sig-1"}
	thinking := provider.ThinkingPart{Thinking: "think", Signature: "sig-1"}
	state := NewState(&Ids{}, "i1", "openai/openai-completions@gpt-4o")
	var envelopes []Envelope
	add := func(envelope Envelope, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		envelopes = append(envelopes, envelope)
	}
	add(state.Accepted("c0", Honoured{IncludeSnapshot: "never"}))
	add(state.Started(1700000000000))
	add(state.emit("inference.part.started", "", PartStarted{PartIndex: 0, PartKind: "reasoning"}))
	add(state.emit("inference.part.delta", "", struct {
		PartIndex int    `json:"part_index"`
		Delta     string `json:"delta"`
	}{PartIndex: 0, Delta: "think"}))
	add(state.emit("inference.part.ended", "", PartEnded{PartIndex: 0, PartKind: "reasoning", Text: &thinking.Thinking, Carry: thinking.Signature}))
	add(state.emit("inference.part.started", "", PartStarted{PartIndex: 1, PartKind: "tool_call", ToolCallID: "tc1", Name: "lookup"}))
	add(state.emit("inference.part.ended", "", PartEnded{PartIndex: 1, PartKind: "tool_call", Carry: call.ThoughtSig, ToolCall: &EndedToolCall{
		ToolCallID: call.ID, Name: call.Name, ArgumentsJSON: json.RawMessage(call.Arguments),
	}}))
	add(state.Completed("tool_use", partsOf([]provider.AssistantBlock{{Thinking: &thinking}, {ToolCall: &call}})))
	return envelopes
}

func TestAFullTraceThisLayerBuildsPassesTheTreeOwnProviderValidator(t *testing.T) {
	validator, err := validation.NewProviderValidator()
	if err != nil {
		t.Fatalf("the provider validator is not available: %v", err)
	}
	envelopes := buildFullTrace(t)
	if len(envelopes) != 8 {
		t.Fatalf("the trace is %d envelopes, want 8: a validator run over a subset proves nothing about the rest", len(envelopes))
	}
	var document []byte
	document = append(document, '[')
	for i, envelope := range envelopes {
		held, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			document = append(document, ',')
		}
		document = append(document, held...)
	}
	document = append(document, ']')
	result := validator.Validate(bytes.NewReader(document), "built-by-this-layer")
	if len(result.Diagnostics) != 0 {
		for _, diagnostic := range result.Diagnostics {
			t.Errorf("%s %s: %s", diagnostic.Phase, diagnostic.Code, diagnostic.Message)
		}
		t.Fatalf("a trace this layer built failed the validator: %s", document)
	}
}

func TestTheValidatorRejectsATraceThisLayerWouldHaveEmittedBeforeTheFixes(t *testing.T) {
	validator, err := validation.NewProviderValidator()
	if err != nil {
		t.Fatal(err)
	}
	state := NewState(&Ids{}, "i1", "m")
	broken, err := state.emit("inference.failed", "", struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: "provider_unavailable", Message: "flat"})
	if err != nil {
		t.Fatal(err)
	}
	held, err := json.Marshal(broken)
	if err != nil {
		t.Fatal(err)
	}
	result := validator.Validate(bytes.NewReader(append(append([]byte("["), held...), ']')), "flat-failure")
	if len(result.Diagnostics) == 0 {
		t.Fatal("the validator accepted a flattened failure payload, so it cannot be the oracle this test leans on")
	}
	if len(result.Diagnostics) == 1 && result.Diagnostics[0].Code == validation.CodeSchemaInvalid &&
		result.Diagnostics[0].Phase == validation.PhaseSchema {
		return
	}
	for _, diagnostic := range result.Diagnostics {
		t.Logf("the validator said %s at %s: %s", diagnostic.Code, diagnostic.Pointer, diagnostic.Message)
	}
	t.Errorf("a flattened failure payload was rejected for %v, want %s in the schema phase: any other code or phase would let a later scope rule make this pass for the wrong reason", codesOf(result.Diagnostics), validation.CodeSchemaInvalid)
}

func codesOf(diagnostics []validation.Diagnostic) []string {
	out := make([]string, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		out = append(out, diagnostic.Code)
	}
	return out
}

func TestTheCarryOnAToolCallPartMustBeTheOneTheTerminalCarries(t *testing.T) {
	call := provider.ToolCall{ID: "tc1", Name: "lookup", Arguments: `{}`, ThoughtSig: "sig-1"}
	ended := PartEnded{PartIndex: 0, PartKind: "tool_call", Carry: call.ThoughtSig, ToolCall: &EndedToolCall{
		ToolCallID: call.ID, Name: call.Name, ArgumentsJSON: json.RawMessage(call.Arguments),
	}}
	terminal := partsOf([]provider.AssistantBlock{{ToolCall: &call}})
	if terminal[0].Carry != ended.Carry {
		t.Errorf("the terminal carries %q and the ended part carried %q: the validator fails that pair as not an assembly", terminal[0].Carry, ended.Carry)
	}
	un := PartEnded{PartIndex: 1, PartKind: "tool_call", ToolCall: &EndedToolCall{ToolCallID: "tc2", Name: "other", ArgumentsJSON: json.RawMessage("{}")}}
	if partsOf([]provider.AssistantBlock{{ToolCall: &provider.ToolCall{ID: "tc2", Name: "other"}}})[0].Carry != un.Carry {
		t.Error("an unsigned call must carry nothing on either side")
	}
}

func TestTwoInferencesOnOneConnectionNeverRepeatAnEnvelopeID(t *testing.T) {
	ids := &Ids{}
	first := NewState(ids, "i-first", "m")
	second := NewState(ids, "i-second", "m")
	seen := map[string]string{}
	for _, state := range []*State{first, second} {
		if _, err := state.Started(1); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			envelope, err := state.emit("inference.part.delta", "", struct {
				PartIndex int    `json:"part_index"`
				Delta     string `json:"delta"`
			}{Delta: "x"})
			if err != nil {
				t.Fatal(err)
			}
			if owner, taken := seen[envelope.ID]; taken {
				t.Fatalf("the envelope id %q was issued twice, to %s and %s: a host deduplicating by id would drop one inference's events silently", envelope.ID, owner, state.inferenceID)
			}
			seen[envelope.ID] = state.inferenceID
		}
	}
	if len(seen) != 6 {
		t.Errorf("%d distinct ids across two inferences, want 6: the started envelopes are issued through the same allocator", len(seen))
	}
}

func TestEachInferenceKeepsItsOwnSequenceDomainOnOneConnection(t *testing.T) {
	ids := &Ids{}
	first := NewState(ids, "i-first", "m")
	second := NewState(ids, "i-second", "m")
	if _, err := first.Started(1); err != nil {
		t.Fatal(err)
	}
	secondStart, err := second.Started(1)
	if err != nil {
		t.Fatal(err)
	}
	if secondStart.Sequence != 1 {
		t.Errorf("the second inference's started carries sequence %d, want 1: each inference has its own domain and the draft says sequences from two inferences are never compared", secondStart.Sequence)
	}
	if secondStart.InferenceID != "i-second" {
		t.Errorf("the second inference's started carries inference_id %q", secondStart.InferenceID)
	}
}

func TestMalformedToolCallArgumentsEndTheInferenceRatherThanDroppingTheTerminal(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	if _, err := state.Started(1); err != nil {
		t.Fatal(err)
	}
	envelope, err := state.Completed("tool_use", []TerminalBlock{{
		Type:          "tool_call",
		ToolCallID:    "tc1",
		Name:          "lookup",
		ArgumentsJSON: json.RawMessage(`{"city":`),
	}})
	if err != nil {
		t.Fatalf("Completed returned %v, want a failure envelope instead: the terminal must not be lost to a marshalling error", err)
	}
	if envelope.Type != "inference.failed" {
		t.Fatalf("got a %s, want inference.failed", envelope.Type)
	}
	failure, ok := body(t, envelope)["error"].(map[string]any)
	if !ok {
		t.Fatalf("the payload = %s, want the code and message under error", envelope.Payload)
	}
	if failure["code"] != CodeProtocolViolation {
		t.Errorf("the failure code = %v, want %s: the provider is streaming and sent a malformed tool call, and reporting it unavailable is a Retry-class falsehood about a vendor that did nothing wrong", failure["code"], CodeProtocolViolation)
	}
	if !strings.Contains(failure["message"].(string), "arguments_json") {
		t.Errorf("the failure message = %v, want it to say the arguments are not json", failure["message"])
	}
}

func TestTheFailureTheMalformedArgumentsProducePassesTheValidator(t *testing.T) {
	validator, err := validation.NewProviderValidator()
	if err != nil {
		t.Fatal(err)
	}
	state := NewState(&Ids{}, "i1", "m")
	started, err := state.Started(1700000000000)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := state.Completed("tool_use", []TerminalBlock{{
		Type: "tool_call", ToolCallID: "tc1", Name: "lookup", ArgumentsJSON: json.RawMessage(`{"city":`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := json.Marshal(started)
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(failed)
	if err != nil {
		t.Fatal(err)
	}
	document := append(append(append([]byte("["), first...), ','), second...)
	result := validator.Validate(bytes.NewReader(append(document, ']')), "malformed-arguments")
	if len(result.Diagnostics) != 0 {
		for _, diagnostic := range result.Diagnostics {
			t.Errorf("%s %s: %s", diagnostic.Phase, diagnostic.Code, diagnostic.Message)
		}
	}
}

func TestTwoInferencesIssuingIdsAtOnceNeverRepeatOne(t *testing.T) {
	ids := &Ids{}
	states := []*State{NewState(ids, "i-a", "m"), NewState(ids, "i-b", "m"), NewState(ids, "i-c", "m")}
	var mu sync.Mutex
	seen := map[string]bool{}
	var wg sync.WaitGroup
	for _, state := range states {
		wg.Add(1)
		go func(state *State) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				envelope, err := state.emit("inference.part.delta", "", struct {
					PartIndex int    `json:"part_index"`
					Delta     string `json:"delta"`
				}{Delta: "x"})
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				if seen[envelope.ID] {
					t.Errorf("the envelope id %q was issued twice under concurrency", envelope.ID)
					mu.Unlock()
					return
				}
				seen[envelope.ID] = true
				mu.Unlock()
			}
		}(state)
	}
	wg.Wait()
	if len(seen) != 600 {
		t.Errorf("%d distinct ids from three concurrent inferences, want 600", len(seen))
	}
}

func TestAMalformedToolCallIsNotReportedAsAnUnavailableProvider(t *testing.T) {
	// drafts/model-provider-core.md:1562 names this exact shape as the anti-pattern:
	// reporting a provider unavailable because the pump could not assemble a
	// terminal fills a trace with evidence against a vendor that did nothing wrong.
	state := NewState(&Ids{}, "i1", "m")
	if _, err := state.Started(1); err != nil {
		t.Fatal(err)
	}
	envelope, err := state.Completed("tool_use", []TerminalBlock{{
		Type: "tool_call", ToolCallID: "tc1", Name: "lookup", ArgumentsJSON: json.RawMessage(`{"city":`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	code := body(t, envelope)["error"].(map[string]any)["code"]
	if code == CodeProviderUnavailable {
		t.Errorf("a malformed tool call is reported as %s, which is Retry-class: a host would back off and resend, and a trace would record unavailability that never happened", code)
	}
	if code != CodeProtocolViolation {
		t.Errorf("the code = %v, want %s: the provider is streaming, so the peer is broken and someone reads a log", code, CodeProtocolViolation)
	}
}

func TestTheCodesThisLayerEmitsAreAllInTheSchemasSet(t *testing.T) {
	permitted := map[string]bool{
		"rate_limited": true, "provider_unavailable": true, "resource_exhausted": true,
		"endpoint_error": true, "credential_expired": true, "credential_missing": true,
		"credential_rejected": true, "invalid_request": true, "protocol_violation": true,
		"unsupported_version": true, "unsupported_feature": true, "model_not_found": true,
		"aborted": true,
	}
	state := NewState(&Ids{}, "i1", "m")
	if _, err := state.Started(1); err != nil {
		t.Fatal(err)
	}
	emitted := []Envelope{}
	settled, err := state.Completed("tool_use", []TerminalBlock{{
		Type: "tool_call", ToolCallID: "tc1", Name: "lookup", ArgumentsJSON: json.RawMessage(`{`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	emitted = append(emitted, settled)
	failed, err := state.Failed(CodeProviderUnavailable, "the provider stream failed")
	if err != nil {
		t.Fatal(err)
	}
	emitted = append(emitted, failed)
	for _, envelope := range emitted {
		code, ok := body(t, envelope)["error"].(map[string]any)["code"].(string)
		if !ok {
			t.Errorf("%s carries no error code", envelope.Type)
			continue
		}
		if !permitted[code] {
			t.Errorf("%s settles with %q, which is not in the profile's set: an implementation inventing a code is the divergence the taxonomy was closed to catch", envelope.Type, code)
		}
	}
}

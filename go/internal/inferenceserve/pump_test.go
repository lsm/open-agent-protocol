package inferenceserve

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/provider"
	"github.com/lsm/open-agent-protocol/go/validation"
)

func runTrace(t *testing.T, state *State, events ...provider.Event) []Envelope {
	t.Helper()
	var all []Envelope
	started, err := state.Started(1700000000000)
	if err != nil {
		t.Fatal(err)
	}
	all = append(all, started)
	for _, event := range events {
		emitted, err := Pump(state, event)
		if err != nil {
			t.Fatalf("the %s event: %v", event.Kind, err)
		}
		all = append(all, emitted...)
	}
	return all
}

func validate(t *testing.T, envelopes []Envelope) {
	t.Helper()
	validator, err := validation.NewProviderValidator()
	if err != nil {
		t.Fatalf("the provider validator is not available: %v", err)
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
	result := validator.Validate(bytes.NewReader(document), "built-by-pump")
	if len(result.Diagnostics) != 0 {
		for _, diagnostic := range result.Diagnostics {
			t.Errorf("%s %s at %s: %s", diagnostic.Phase, diagnostic.Code, diagnostic.Pointer, diagnostic.Message)
		}
		t.Errorf("a trace this package built failed the validator: %s", document)
	}
}

func textEvents(parts ...string) []provider.Event {
	out := []provider.Event{{Kind: provider.EventTextStart, ContentIndex: 0}}
	for _, part := range parts {
		out = append(out, provider.Event{Kind: provider.EventTextDelta, ContentIndex: 0, Delta: part})
	}
	return out
}

func TestATextTurnPumpProducesATraceTheValidatorAccepts(t *testing.T) {
	state := NewState(&Ids{}, "i1", "openai/openai-completions@gpt-4o")
	events := textEvents("hel", "lo")
	events = append(events,
		provider.Event{Kind: provider.EventTextEnd, ContentIndex: 0, Delta: "hello"},
		provider.Event{Kind: provider.EventDone, Message: &provider.AssistantMessage{
			StopReason: "stop",
			Content:    []provider.AssistantBlock{{Text: &provider.TextPart{Text: "hello"}}},
		}},
	)
	all := runTrace(t, state, events...)
	if len(all) != 6 {
		t.Fatalf("the trace is %d envelopes, want 6", len(all))
	}
	validate(t, all)
	types := make([]string, 0, len(all))
	for _, envelope := range all {
		types = append(types, envelope.Type)
	}
	want := []string{"inference.started", "inference.part.started", "inference.part.delta",
		"inference.part.delta", "inference.part.ended", "inference.completed"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Errorf("the trace is %v, want %v", types, want)
	}
}

func TestAReasoningTurnPumpCarriesTheSignatureToBothTheEndedPartAndTheTerminal(t *testing.T) {
	state := NewState(&Ids{}, "i1", "anthropic/anthropic-messages@claude")
	thinking := provider.ThinkingPart{Thinking: "think", Signature: "sig-1"}
	all := runTrace(t, state,
		provider.Event{Kind: provider.EventThinkingStart, ContentIndex: 0},
		provider.Event{Kind: provider.EventThinkingDelta, ContentIndex: 0, Delta: "think"},
		provider.Event{Kind: provider.EventThinkingEnd, ContentIndex: 0, Delta: "think",
			Partial: provider.PartialMessage{Content: []provider.AssistantBlock{{Thinking: &thinking}}}},
		provider.Event{Kind: provider.EventDone, Message: &provider.AssistantMessage{
			StopReason: "stop",
			Content:    []provider.AssistantBlock{{Thinking: &thinking}},
		}},
	)
	validate(t, all)
	held := body(t, all[3])
	if held["part_kind"] != "reasoning" || held["text"] != "think" || held["carry"] != "sig-1" {
		t.Errorf("the ended reasoning part = %v, want its text and its carry", held)
	}
	blocks := body(t, all[4])["message"].(map[string]any)["content"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("the terminal content = %v, want the one ended part", blocks)
	}
	first := blocks[0].(map[string]any)
	if first["carry"] != held["carry"] {
		t.Errorf("the terminal carries %v and the ended part carried %v: the validator fails that pair as not an assembly", first["carry"], held["carry"])
	}
}

func TestAToolCallTurnPumpNestsItsCallAndTheValidatorAcceptsIt(t *testing.T) {
	state := NewState(&Ids{}, "i1", "openai/openai-completions@gpt-4o")
	call := provider.ToolCall{ID: "tc1", Name: "lookup", Arguments: `{"city":"Kyoto"}`}
	all := runTrace(t, state,
		provider.Event{Kind: provider.EventToolCallStart, ContentIndex: 0, ID: "tc1", Name: "lookup"},
		provider.Event{Kind: provider.EventToolCallDelta, ContentIndex: 0, Delta: `{"city":`},
		provider.Event{Kind: provider.EventToolCallDelta, ContentIndex: 0, Delta: `"Kyoto"}`},
		provider.Event{Kind: provider.EventToolCallEnd, ContentIndex: 0, ToolCall: &call},
		provider.Event{Kind: provider.EventDone, Message: &provider.AssistantMessage{
			StopReason: "tool_use",
			Content:    []provider.AssistantBlock{{ToolCall: &call}},
		}},
	)
	validate(t, all)
	ended := body(t, all[len(all)-2])
	nested, ok := ended["tool_call"].(map[string]any)
	if !ok {
		t.Fatalf("the ended tool call = %v, want the call nested", ended)
	}
	if nested["tool_call_id"] != "tc1" || nested["name"] != "lookup" {
		t.Errorf("the nested call = %v, want its id and name", nested)
	}
	if _, flat := ended["tool_call_id"]; flat {
		t.Error("the ended tool call carries tool_call_id at the top level, which the schema forbids")
	}
}

func TestASignedToolCallPumpCarriesTheSignatureOnBothSides(t *testing.T) {
	state := NewState(&Ids{}, "i1", "anthropic/anthropic-messages@claude")
	call := provider.ToolCall{ID: "tc1", Name: "lookup", Arguments: `{}`, ThoughtSig: "sig-1", HasThought: true}
	all := runTrace(t, state,
		provider.Event{Kind: provider.EventToolCallStart, ContentIndex: 0, ID: "tc1", Name: "lookup"},
		provider.Event{Kind: provider.EventToolCallEnd, ContentIndex: 0, ToolCall: &call},
		provider.Event{Kind: provider.EventDone, Message: &provider.AssistantMessage{
			StopReason: "tool_use",
			Content:    []provider.AssistantBlock{{ToolCall: &call}},
		}},
	)
	validate(t, all)
	if got := body(t, all[2])["carry"]; got != "sig-1" {
		t.Errorf("the ended tool call carries %v, want the signature: a terminal that carries it and an ended part that does not is not an assembly", got)
	}
}

func TestAnErrorEventSettlesAFailureAndNothingFollowsIt(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	all := runTrace(t, state, provider.Event{Kind: provider.EventError})
	validate(t, all)
	if all[len(all)-1].Type != "inference.failed" {
		t.Fatalf("got a %s, want inference.failed", all[len(all)-1].Type)
	}
	failure := body(t, all[len(all)-1])["error"].(map[string]any)
	if failure["code"] != CodeProviderUnavailable {
		t.Errorf("the failure code = %v, want %s: an error with no reason is the stream failing, which is what the source settles (runtime.zig:149)", failure["code"], CodeProviderUnavailable)
	}
	after, err := Pump(state, provider.Event{Kind: provider.EventDone, Message: &provider.AssistantMessage{StopReason: "stop"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Errorf("a done after the failure emitted %v, want nothing: a settled inference stays settled", typesOf(after))
	}
}

func TestAKeepalivePumpEmitsNothingAndConsumesNoSequence(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	if _, err := state.Started(1); err != nil {
		t.Fatal(err)
	}
	first, err := Pump(state, provider.Event{Kind: provider.EventTextStart, ContentIndex: 0})
	if err != nil {
		t.Fatal(err)
	}
	emitted, err := Pump(state, provider.Event{Kind: provider.EventKeepalive})
	if err != nil {
		t.Fatal(err)
	}
	if len(emitted) != 0 {
		t.Errorf("a keepalive emitted %v, want nothing: forwarding it would leave a hole in the sequence", typesOf(emitted))
	}
	second, err := Pump(state, provider.Event{Kind: provider.EventTextEnd, ContentIndex: 0, Delta: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if second[0].Sequence != first[0].Sequence+1 {
		t.Errorf("the sequence went %d then %d, want them adjacent across a keepalive", first[0].Sequence, second[0].Sequence)
	}
}

func TestPumpRefusesADeltaOrEndForAPartThatIsNotTheOneOpen(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	if _, err := Pump(state, provider.Event{Kind: provider.EventToolCallEnd, ContentIndex: 0}); err != ErrPartIndexMismatch {
		t.Errorf("a tool call end with no part open = %v, want a mismatch: nothing can be ended that was never opened", err)
	}
	if _, err := Pump(state, provider.Event{Kind: provider.EventTextStart, ContentIndex: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := Pump(state, provider.Event{Kind: provider.EventTextEnd, ContentIndex: 1, Delta: "a"}); err != ErrPartIndexMismatch {
		t.Errorf("ending part 1 while 0 is open = %v, want a mismatch", err)
	}
	if _, err := Pump(state, provider.Event{Kind: provider.EventTextDelta, ContentIndex: 1, Delta: "a"}); err != ErrPartIndexMismatch {
		t.Errorf("a delta for part 1 while 0 is open = %v, want a mismatch", err)
	}
}

func TestADeltaWithNoPartOpenOpensOneSoAClientThatNeverStartsPartsStillMaps(t *testing.T) {
	state := NewState(&Ids{}, "i1", "openai/openai-completions@gpt-4o")
	opened, err := Pump(state, provider.Event{Kind: provider.EventTextDelta, ContentIndex: 0, Delta: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(opened) != 2 || opened[0].Type != "inference.part.started" || opened[1].Type != "inference.part.delta" {
		t.Fatalf("got %v, want the part opened and the delta that opened it: a consumer reading incrementally would never see the first fragment otherwise", typesOf(opened))
	}
	if got := body(t, opened[0])["part_kind"]; got != "text" {
		t.Errorf("the implicit part kind = %v, want text", got)
	}
	if got := body(t, opened[1])["delta"]; got != "a" {
		t.Errorf("the opening delta = %v, want a", got)
	}
	next, err := Pump(state, provider.Event{Kind: provider.EventTextDelta, ContentIndex: 0, Delta: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].Type != "inference.part.delta" {
		t.Errorf("the second delta emitted %v, want a delta rather than a second open", typesOf(next))
	}
}

func TestPumpGatesAToolCallIdentityBothWays(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	if _, err := Pump(state, provider.Event{Kind: provider.EventToolCallStart, ContentIndex: 0}); err != ErrToolCallIdentity {
		t.Errorf("a tool call with no id or name = %v, want it required", err)
	}
	if _, err := Pump(state, provider.Event{Kind: provider.EventTextStart, ContentIndex: 0, ID: "tc1", Name: "lookup"}); err != ErrToolCallIdentityOnly {
		t.Errorf("a text part carrying an id and a name = %v, want it refused: the identity is gated both ways", err)
	}
}

func TestAPartslessDoneCompletesWithEmptyContentTheWayTheSourceDoes(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	all := runTrace(t, state, provider.Event{Kind: provider.EventDone, Message: &provider.AssistantMessage{StopReason: "stop"}})
	validate(t, all)
	terminal := all[len(all)-1]
	if terminal.Type != "inference.completed" {
		t.Fatalf("got a %s, want inference.completed: contentFromParts returns the empty text when no parts ended, and the schema allows a string content", terminal.Type)
	}
	message, ok := body(t, terminal)["message"].(map[string]any)
	if !ok {
		t.Fatalf("the terminal payload = %s, want a message", terminal.Payload)
	}
	if content, ok := message["content"].(string); !ok || content != "" {
		t.Errorf("content = %v, want the empty string rather than an empty array: an array would need minItems 1", message["content"])
	}
}

func TestADoneWithAPartStillOpenSettlesEndpointError(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	all := runTrace(t, state,
		provider.Event{Kind: provider.EventTextStart, ContentIndex: 0},
		provider.Event{Kind: provider.EventTextDelta, ContentIndex: 0, Delta: "a"},
		provider.Event{Kind: provider.EventDone, Message: &provider.AssistantMessage{StopReason: "stop"}},
	)
	validate(t, all)
	terminal := all[len(all)-1]
	if terminal.Type != "inference.failed" {
		t.Fatalf("got a %s, want inference.failed: a completed with a part.started that never ends is an invalid frame", terminal.Type)
	}
	failure := body(t, terminal)["error"].(map[string]any)
	if failure["code"] != CodeEndpointError {
		t.Errorf("the code = %v, want %s: that is what the shipped path answers for a terminal it could not deliver, and the two codes sit in different taxonomy classes so a host would branch differently on each tree", failure["code"], CodeEndpointError)
	}
}

func TestAFailureAbandonsTheOpenPartSoNothingScopedCanFollow(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	all := runTrace(t, state,
		provider.Event{Kind: provider.EventTextStart, ContentIndex: 0},
		provider.Event{Kind: provider.EventTextDelta, ContentIndex: 0, Delta: "a"},
		provider.Event{Kind: provider.EventError},
	)
	validate(t, all)
	after, err := Pump(state, provider.Event{Kind: provider.EventTextEnd, ContentIndex: 0, Delta: "a"})
	if err != ErrAfterTerminal {
		t.Fatalf("a part ending after the failure = %v, want it refused as after the terminal, so it cannot reach the wire", err)
	}
	if len(after) != 0 {
		t.Errorf("a part ending after the failure emitted %v, want nothing: the failure abandoned the open part and settled the inference, so this part belongs to neither", typesOf(after))
	}
}

func TestASignatureIsReadFromThePartThatIsEndingNotTheFirstOne(t *testing.T) {
	first := provider.ThinkingPart{Thinking: "one", Signature: "sig-first"}
	second := provider.ThinkingPart{Thinking: "two", Signature: "sig-second"}
	state := NewState(&Ids{}, "i1", "anthropic/anthropic-messages@claude")
	all := runTrace(t, state,
		provider.Event{Kind: provider.EventThinkingStart, ContentIndex: 0},
		provider.Event{Kind: provider.EventThinkingEnd, ContentIndex: 0, Delta: "one",
			Partial: provider.PartialMessage{Content: []provider.AssistantBlock{{Thinking: &first}, {Thinking: &second}}}},
		provider.Event{Kind: provider.EventThinkingStart, ContentIndex: 1},
		provider.Event{Kind: provider.EventThinkingEnd, ContentIndex: 1, Delta: "two",
			Partial: provider.PartialMessage{Content: []provider.AssistantBlock{{Thinking: &first}, {Thinking: &second}}}},
		provider.Event{Kind: provider.EventDone, Message: &provider.AssistantMessage{
			StopReason: "stop",
			Content:    []provider.AssistantBlock{{Thinking: &first}, {Thinking: &second}},
		}},
	)
	validate(t, all)
	if got := body(t, all[2])["carry"]; got != "sig-first" {
		t.Errorf("the first part carries %v, want sig-first", got)
	}
	if got := body(t, all[4])["carry"]; got != "sig-second" {
		t.Errorf("the second part carries %v, want sig-second: the signature is read by content_index, so reading the first thinking block would put the other part's signature on this one", got)
	}
}

func TestAPartEndingBeyondThePartialHasNoSignature(t *testing.T) {
	thinking := provider.ThinkingPart{Thinking: "one", Signature: "sig-first"}
	state := NewState(&Ids{}, "i1", "m")
	all := runTrace(t, state,
		provider.Event{Kind: provider.EventThinkingStart, ContentIndex: 0},
		provider.Event{Kind: provider.EventThinkingEnd, ContentIndex: 0, Delta: "one",
			Partial: provider.PartialMessage{Content: []provider.AssistantBlock{{Thinking: &thinking}}}},
		provider.Event{Kind: provider.EventThinkingStart, ContentIndex: 1},
		provider.Event{Kind: provider.EventThinkingEnd, ContentIndex: 1, Delta: "two",
			Partial: provider.PartialMessage{Content: nil}},
		provider.Event{Kind: provider.EventDone, Message: &provider.AssistantMessage{
			StopReason: "stop", Content: []provider.AssistantBlock{{Thinking: &thinking}},
		}},
	)
	validate(t, all)
	if got := body(t, all[4])["carry"]; got != nil {
		t.Errorf("a part ending past the end of the partial carries %v, want nothing: the source yields null when the index is not a thinking part", got)
	}
}

func TestMalformedArgumentsFromTheProviderSettleAFailureNotALostTerminal(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	call := provider.ToolCall{ID: "tc1", Name: "lookup", Arguments: `{"city":`}
	all := runTrace(t, state,
		provider.Event{Kind: provider.EventToolCallStart, ContentIndex: 0, ID: "tc1", Name: "lookup"},
		provider.Event{Kind: provider.EventToolCallEnd, ContentIndex: 0, ToolCall: &call},
		provider.Event{Kind: provider.EventDone, Message: &provider.AssistantMessage{
			StopReason: "tool_use",
			Content:    []provider.AssistantBlock{{ToolCall: &call}},
		}},
	)
	validate(t, all)
	terminal := all[len(all)-1]
	if terminal.Type != "inference.failed" {
		t.Fatalf("got a %s, want inference.failed: a stream that stops with no terminal is what a host cannot tell from a slow provider", terminal.Type)
	}
}

func TestTwoInferencesPumpedOnOneConnectionValidateTogether(t *testing.T) {
	ids := &Ids{}
	first := NewState(ids, "i-first", "m")
	second := NewState(ids, "i-second", "m")
	var all []Envelope
	accept1, err := first.Accepted("c0", Honoured{IncludeSnapshot: "never"})
	if err != nil {
		t.Fatal(err)
	}
	all = append(all, accept1)
	all = append(all, runTrace(t, first, append(textEvents("a"),
		provider.Event{Kind: provider.EventTextEnd, ContentIndex: 0, Delta: "a"},
		provider.Event{Kind: provider.EventDone, Message: &provider.AssistantMessage{
			StopReason: "stop", Content: []provider.AssistantBlock{{Text: &provider.TextPart{Text: "a"}}},
		}})...)...)
	all = append(all, runTrace(t, second, append(textEvents("b"),
		provider.Event{Kind: provider.EventTextEnd, ContentIndex: 0, Delta: "b"},
		provider.Event{Kind: provider.EventDone, Message: &provider.AssistantMessage{
			StopReason: "stop", Content: []provider.AssistantBlock{{Text: &provider.TextPart{Text: "b"}}},
		}})...)...)
	validate(t, all)
	seen := map[string]bool{}
	for _, envelope := range all {
		if seen[envelope.ID] {
			t.Errorf("the envelope id %q appears twice in one connection's trace", envelope.ID)
		}
		seen[envelope.ID] = true
	}
}

func typesOf(envelopes []Envelope) []string {
	out := make([]string, 0, len(envelopes))
	for _, envelope := range envelopes {
		out = append(out, envelope.Type)
	}
	return out
}

func TestAThoughtSignatureReachesBothSidesWhenTheSignatureOnlyArrivesOnTheDone(t *testing.T) {
	thinking := provider.ThinkingPart{Thinking: "think", Signature: "sig-1"}
	state := NewState(&Ids{}, "i1", "anthropic/anthropic-messages@claude")
	all := runTrace(t, state,
		provider.Event{Kind: provider.EventThinkingStart, ContentIndex: 0},
		provider.Event{Kind: provider.EventThinkingDelta, ContentIndex: 0, Delta: "think"},
		provider.Event{Kind: provider.EventThinkingEnd, ContentIndex: 0, Delta: "think",
			Partial: provider.PartialMessage{Content: nil}},
		provider.Event{Kind: provider.EventDone, Message: &provider.AssistantMessage{
			StopReason: "stop",
			Content:    []provider.AssistantBlock{{Thinking: &thinking}},
		}},
	)
	validate(t, all)
	ended := body(t, all[3])
	blocks := body(t, all[4])["message"].(map[string]any)["content"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("the terminal content = %v, want the one ended part", blocks)
	}
	if blocks[0].(map[string]any)["carry"] != nil {
		t.Errorf("the terminal carries %v, want nothing: the terminal is assembled from the ended parts, and that part ended without one", blocks[0].(map[string]any)["carry"])
	}
	if ended["carry"] != nil {
		t.Errorf("the ended reasoning part carries %v, want nothing: the client puts the signature only on the done message, so a part reading it from two places is a divergence the parity test would see", ended["carry"])
	}
}

func TestAnErrorAfterACompletedIsRefused(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	all := runTrace(t, state, append(textEvents("a"),
		provider.Event{Kind: provider.EventTextEnd, ContentIndex: 0, Delta: "a"},
		provider.Event{Kind: provider.EventDone, Message: &provider.AssistantMessage{
			StopReason: "stop", Content: []provider.AssistantBlock{{Text: &provider.TextPart{Text: "a"}}},
		}})...)
	validate(t, all)
	after, err := Pump(state, provider.Event{Kind: provider.EventError})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Errorf("an error after the completed emitted %v, want nothing: the validator calls that event_after_terminal", typesOf(after))
	}
}

func TestASecondPartStartIsRefused(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	if _, err := Pump(state, provider.Event{Kind: provider.EventTextStart, ContentIndex: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := Pump(state, provider.Event{Kind: provider.EventThinkingStart, ContentIndex: 1}); err != ErrPartAlreadyOpen {
		t.Errorf("a second part start while one is open = %v, want it refused", err)
	}
}

func TestASignatureThePartNeverSawIsNotInventedOnTheTerminal(t *testing.T) {
	thinking := provider.ThinkingPart{Thinking: "think", Signature: "sig-1"}
	state := NewState(&Ids{}, "i1", "anthropic/anthropic-messages@claude")
	all := runTrace(t, state,
		provider.Event{Kind: provider.EventThinkingStart, ContentIndex: 0},
		provider.Event{Kind: provider.EventThinkingEnd, ContentIndex: 0, Delta: "think",
			Partial: provider.PartialMessage{Content: nil}},
		provider.Event{Kind: provider.EventDone, Message: &provider.AssistantMessage{
			StopReason: "stop",
			Content:    []provider.AssistantBlock{{Thinking: &thinking}},
		}},
	)
	validate(t, all)
	terminal := all[len(all)-1]
	if terminal.Type != "inference.completed" {
		t.Fatalf("got a %s, want a completed", terminal.Type)
	}
	message, ok := body(t, terminal)["message"].(map[string]any)
	if !ok {
		t.Fatalf("the terminal payload = %s, want a message", terminal.Payload)
	}
	blocks, ok := message["content"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("the terminal content = %v, want the one ended part", message["content"])
	}
	first, ok := blocks[0].(map[string]any)
	if !ok {
		t.Fatalf("the terminal block = %v, want an object", blocks[0])
	}
	if first["reasoning"] != "think" {
		t.Errorf("the terminal reasoning = %v, want the accumulated text", first["reasoning"])
	}
	if _, present := first["carry"]; present {
		t.Errorf("the terminal carries %v, want no carry: the part ended without one and the terminal is not a second source", first["carry"])
	}
}

func TestAStopReasonTheProfileDoesNotDefineSettlesAFailure(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	started, err := state.Started(1700000000000)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := state.Completed("nearly_stopped", nil)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Type != "inference.failed" {
		t.Fatalf("got a %s, want inference.failed: stop_reason is a closed enum, so anything else is a frame the schema refuses", envelope.Type)
	}
	for _, reason := range []string{"stop", "length", "tool_use", "content_filter", "error", "aborted"} {
		if !SettableStopReason(reason) {
			t.Errorf("%q is refused, want it accepted: it is in the profile's enum", reason)
		}
	}
	validate(t, []Envelope{started, envelope})
}

func TestAPartAfterAMidStreamSettleIsRefused(t *testing.T) {
	state := NewState(&Ids{}, "i1", "anthropic/anthropic-messages@claude")
	broken := provider.ToolCall{ID: "tc1", Name: "lookup", Arguments: `{"city":`}
	all := runTrace(t, state,
		provider.Event{Kind: provider.EventToolCallStart, ContentIndex: 0, ID: "tc1", Name: "lookup"},
		provider.Event{Kind: provider.EventToolCallEnd, ContentIndex: 0, ToolCall: &broken},
	)
	validate(t, all)
	if all[len(all)-1].Type != "inference.failed" {
		t.Fatalf("got a %s, want inference.failed", all[len(all)-1].Type)
	}
	for _, event := range []provider.Event{
		{Kind: provider.EventTextStart, ContentIndex: 1},
		{Kind: provider.EventTextDelta, ContentIndex: 1, Delta: "a"},
		{Kind: provider.EventTextEnd, ContentIndex: 1, Delta: "a"},
	} {
		after, err := Pump(state, event)
		if err != ErrAfterTerminal {
			t.Errorf("a %s after the mid-stream settle = %v, want it refused as after the terminal", event.Kind, err)
		}
		if len(after) != 0 {
			t.Errorf("a %s after the mid-stream settle emitted %v, want nothing: the validator calls that event_after_terminal", event.Kind, typesOf(after))
		}
	}
}

func TestACancelledInferenceSettlesAbortedRatherThanBlamingTheVendor(t *testing.T) {
	sink := &provider.EventSink{}
	reads := 0
	read := provider.ReadChunkFunc(func() ([]byte, error) {
		reads++
		switch reads {
		case 1:
			return []byte("data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"), nil
		case 2:
			return []byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"a\"}}\n\n"), nil
		default:
			return nil, nil
		}
	})
	cancelled := provider.CancelledFunc(func() bool { return reads >= 2 })
	provider.StreamAnthropic(sink, provider.Model{ID: "claude", API: "anthropic-messages", Provider: "anthropic", MaxTokens: 100, HasCompat: true},
		provider.Context{}, provider.AnthropicOptions{}, read, cancelled, nil)

	state := NewState(&Ids{}, "i1", "openai/openai-completions@gpt-4o")
	started, err := state.Started(1700000000000)
	if err != nil {
		t.Fatal(err)
	}
	var all []Envelope
	all = append(all, started)
	sawError := false
	for _, event := range sinkEvents(sink) {
		if event.Kind == provider.EventError {
			sawError = true
		}
		emitted, err := Pump(state, event)
		if err != nil {
			t.Fatalf("the %s event: %v", event.Kind, err)
		}
		all = append(all, emitted...)
	}
	if !sawError {
		t.Fatal("the cancelled run produced no error event, so this is not the script under test")
	}
	validate(t, all)
	terminal := all[len(all)-1]
	if terminal.Type != "inference.completed" {
		t.Fatalf("got a %s, want inference.completed: the draft says an aborted call ends as a completed with stop_reason aborted, because cancellation at this boundary is a stop reason and not a third terminal", terminal.Type)
	}
	if got := body(t, terminal)["stop_reason"]; got != "aborted" {
		t.Errorf("the stop reason = %v, want aborted", got)
	}
}

func TestAStreamErrorIsClassifiedByWhoseFaultItIs(t *testing.T) {
	cases := []struct {
		reason string
		code   string
		why    string
	}{
		{"", CodeProviderUnavailable, "no reason at all is the stream failing, which is what the source settles"},
		{"read error", CodeEndpointError, "a local read failure is the endpoint's own, and endpoint_error is the code for that"},
		{"overloaded_error: the vendor is busy", CodeProviderUnavailable, "a vendor-reported message is the vendor's fault and says so"},
	}
	for _, one := range cases {
		state := NewState(&Ids{}, "i1", "m")
		all := runTrace(t, state, provider.Event{Kind: provider.EventError, Reason: one.reason})
		validate(t, all)
		failure := body(t, all[len(all)-1])["error"].(map[string]any)
		if failure["code"] != one.code {
			t.Errorf("a %q settled as %v, want %s: %s", one.reason, failure["code"], one.code, one.why)
		}
		if one.reason != "" && !strings.Contains(failure["message"].(string), one.reason[:4]) {
			t.Errorf("a %q settled with the message %v, want it to carry the reason", one.reason, failure["message"])
		}
	}
}

func sinkEvents(sink *provider.EventSink) []provider.Event {
	return sink.Drain()
}

func TestASettleThatCannotBeBuiltReturnsTheErrorRatherThanAnEnvelope(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	if _, err := state.Started(1); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Refused("c0", "model_not_found", "no such model"); err != nil {
		t.Fatal(err)
	}
	envelope, err := settleFailed(state, CodeEndpointError, "the provider stream failed")
	if err == nil {
		t.Fatalf("got an envelope %+v, want the error: a headerless inference.failed has no id, no inference_id, no sequence and no error member, so it is something no validator accepts and must not be handed to a caller as a settlement", envelope)
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("the error = %v, want ErrRefused", err)
	}
	if envelope.Payload != nil || envelope.ID != "" {
		t.Errorf("the discarded envelope = %+v, want it empty", envelope)
	}
	completed, err := settleCompleted(state, provider.Event{Kind: provider.EventDone, Message: &provider.AssistantMessage{StopReason: "stop"}})
	if err == nil {
		t.Errorf("got a %+v, want the error from the completed path too", completed)
	}
}

func TestACancellationAfterAPartEndedStillCarriesTheStreamedContent(t *testing.T) {
	state := NewState(&Ids{}, "i1", "anthropic/anthropic-messages@claude")
	all := runTrace(t, state,
		provider.Event{Kind: provider.EventTextStart, ContentIndex: 0},
		provider.Event{Kind: provider.EventTextDelta, ContentIndex: 0, Delta: "half a thought"},
		provider.Event{Kind: provider.EventTextEnd, ContentIndex: 0, Delta: "half a thought"},
		provider.Event{Kind: provider.EventError, Reason: "request cancelled"},
	)
	validate(t, all)
	terminal := all[len(all)-1]
	if terminal.Type != "inference.completed" {
		t.Fatalf("got a %s, want inference.completed", terminal.Type)
	}
	if got := body(t, terminal)["stop_reason"]; got != "aborted" {
		t.Errorf("the stop reason = %v, want aborted", got)
	}
	message, ok := body(t, terminal)["message"].(map[string]any)
	if !ok {
		t.Fatalf("the terminal payload = %s, want a message", terminal.Payload)
	}
	blocks, ok := message["content"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("the terminal content = %v, want the ended part: content that was streamed and ended cannot vanish from the one message callers replay", message["content"])
	}
	if blocks[0].(map[string]any)["text"] != "half a thought" {
		t.Errorf("the terminal text = %v, want what the part ended with", blocks[0])
	}
}

func TestTheOpenaiClientOutputThisLayerPumps(t *testing.T) {
	sink := &provider.EventSink{}
	chunks := []string{
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hel\"}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
		"data: [DONE]\n\n",
	}
	next := 0
	read := provider.ReadChunkFunc(func() ([]byte, error) {
		if next >= len(chunks) {
			return nil, nil
		}
		held := chunks[next]
		next++
		return []byte(held), nil
	})
	provider.Stream(sink, provider.Model{ID: "gpt-4o", API: "openai-completions", Provider: "openai", MaxTokens: 100, HasCompat: true},
		provider.Context{}, provider.StreamOptions{}, read, nil)

	state := NewState(&Ids{}, "i1", "openai/openai-completions@gpt-4o")
	started, err := state.Started(1700000000000)
	if err != nil {
		t.Fatal(err)
	}
	all := []Envelope{started}
	for _, event := range sink.Drain() {
		emitted, err := Pump(state, event)
		if err != nil {
			t.Fatalf("the %s event at index %d: %v", event.Kind, event.ContentIndex, err)
		}
		all = append(all, emitted...)
	}
	validate(t, all)
	terminal := all[len(all)-1]
	if terminal.Type != "inference.completed" {
		t.Fatalf("got a %s, want inference.completed", terminal.Type)
	}
	message := body(t, terminal)["message"].(map[string]any)
	blocks, ok := message["content"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("the terminal content = %v, want the text this client streamed", message["content"])
	}
	if blocks[0].(map[string]any)["text"] != "hello" {
		t.Errorf("the terminal text = %v, want hello: this client emits deltas with no part start, so the accumulated text has to reach the terminal", blocks[0])
	}
}

func TestAVendorMessageMentioningCancelIsNotACancellation(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	all := runTrace(t, state, provider.Event{Kind: provider.EventError, Reason: "the model cancelled the request upstream"})
	validate(t, all)
	if all[len(all)-1].Type != "inference.failed" {
		t.Errorf("got a %s, want inference.failed: the reason is vendor text that happens to contain cancel, and only this repo's own literal is a cancellation", all[len(all)-1].Type)
	}
	held := NewState(&Ids{}, "i2", "m")
	cancelled := runTrace(t, held, provider.Event{Kind: provider.EventError, Reason: ReasonCancelled})
	validate(t, cancelled)
	if cancelled[len(cancelled)-1].Type != "inference.completed" {
		t.Errorf("the repo's own cancel settled as %s, want a completed with stop_reason aborted", cancelled[len(cancelled)-1].Type)
	}
}

func TestTextFollowedByAToolCallOnTheOpenaiWireLosesTheCallAtItsOwnIndex(t *testing.T) {
	sink := &provider.EventSink{}
	chunks := []string{
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"tc1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\"}}]}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n",
		"data: [DONE]\n\n",
	}
	next := 0
	read := provider.ReadChunkFunc(func() ([]byte, error) {
		if next >= len(chunks) {
			return nil, nil
		}
		held := chunks[next]
		next++
		return []byte(held), nil
	})
	provider.Stream(sink, provider.Model{ID: "gpt-4o", API: "openai-completions", Provider: "openai", MaxTokens: 100, HasCompat: true},
		provider.Context{}, provider.StreamOptions{}, read, nil)

	state := NewState(&Ids{}, "i1", "openai/openai-completions@gpt-4o")
	var started, ended int
	var refused error
	events := sink.Drain()
	for _, event := range events {
		_, err := Pump(state, event)
		if err != nil {
			refused = err
			break
		}
		switch event.Kind {
		case provider.EventTextDelta, provider.EventTextStart:
			started++
		case provider.EventToolCallEnd, provider.EventTextEnd:
			ended++
		}
	}
	if !errors.Is(refused, ErrPartIndexMismatch) {
		t.Errorf("the call's end at its own index = %v, want a mismatch: the client numbers the start and the end of one call differently, and this layer will not guess which part the end belongs to", refused)
	}
	if started == 0 {
		t.Error("the fixture produced no text, so it is not the script under test")
	}
}

func TestACancelMidPartEndsThePartItWasStreaming(t *testing.T) {
	state := NewState(&Ids{}, "i1", "openai/openai-completions@gpt-4o")
	all := runTrace(t, state,
		provider.Event{Kind: provider.EventTextStart, ContentIndex: 0},
		provider.Event{Kind: provider.EventTextDelta, ContentIndex: 0, Delta: "half"},
		provider.Event{Kind: provider.EventError, Reason: ReasonCancelled},
	)
	validate(t, all)
	types := typesOf(all)
	if types[len(types)-1] != "inference.completed" {
		t.Fatalf("the trace ends in %v, want a completed", types)
	}
	var started, ended int
	for _, typ := range types {
		switch typ {
		case "inference.part.started":
			started++
		case "inference.part.ended":
			ended++
		}
	}
	if started != ended {
		t.Errorf("%d parts started and %d ended, want equal: a terminal over a part still open is an invariant the draft lists to refuse at emission", started, ended)
	}
	blocks, ok := body(t, all[len(all)-1])["message"].(map[string]any)["content"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("the terminal content = %v, want the part the cancel interrupted", body(t, all[len(all)-1])["message"])
	}
	if blocks[0].(map[string]any)["text"] != "half" {
		t.Errorf("the terminal text = %v, want half: text streamed before the cancel was streamed to the caller", blocks[0])
	}
}

func TestACancelMidToolCallSettlesWithoutAnIdentitylessPart(t *testing.T) {
	state := NewState(&Ids{}, "i1", "anthropic/anthropic-messages@claude")
	all := runTrace(t, state,
		provider.Event{Kind: provider.EventToolCallStart, ContentIndex: 0, ID: "tc1", Name: "lookup"},
		provider.Event{Kind: provider.EventToolCallDelta, ContentIndex: 0, Delta: "{"},
		provider.Event{Kind: provider.EventError, Reason: ReasonCancelled},
	)
	validate(t, all)
	terminal := all[len(all)-1]
	if terminal.Type != "inference.completed" {
		t.Fatalf("got a %s, want a completed", terminal.Type)
	}
	for _, envelope := range all {
		if envelope.Type != "inference.part.ended" {
			continue
		}
		held := body(t, envelope)
		if held["part_kind"] == "tool_call" {
			if _, present := held["tool_call"]; !present {
				t.Errorf("a tool call part ended without its call: %s, which the schema refuses", envelope.Payload)
			}
			if _, present := held["text"]; present {
				t.Errorf("a tool call part ended carrying text: %s, which the schema forbids on that branch", envelope.Payload)
			}
		}
	}
}

func TestAToolCallDeltaWithNoPartOpenIsRefusedRatherThanInventingAnIdentity(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	if _, err := Pump(state, provider.Event{Kind: provider.EventToolCallDelta, ContentIndex: 0, Delta: "{"}); err != ErrPartIndexMismatch {
		t.Fatalf("a tool call delta with nothing open = %v, want it refused: an implicit tool_call part would need an id and a name this layer does not have, and part.started requires both", err)
	}
	all := runTrace(t, state, provider.Event{Kind: provider.EventTextDelta, ContentIndex: 0, Delta: "a"})
	validate(t, all)
}

func TestAPartStartOverAnImplicitPartReturnsBothEnvelopes(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	opened, err := Pump(state, provider.Event{Kind: provider.EventTextDelta, ContentIndex: 0, Delta: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	emitted, err := Pump(state, provider.Event{Kind: provider.EventThinkingStart, ContentIndex: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(emitted) != 2 || emitted[0].Type != "inference.part.ended" || emitted[1].Type != "inference.part.started" {
		t.Fatalf("got %v, want the implicit part closed and the new one opened: returning only the ended one spends its sequence and id, and the caller sees a gap", typesOf(emitted))
	}
	if emitted[1].Sequence != opened[len(opened)-1].Sequence+2 {
		t.Errorf("the new part opened at sequence %d after %d, want them adjacent: a dropped envelope leaves a gap the validator fails", emitted[1].Sequence, opened[len(opened)-1].Sequence)
	}
	all := append([]Envelope{opened[0], opened[1]}, emitted...)
	closed, err := Pump(state, provider.Event{Kind: provider.EventThinkingEnd, ContentIndex: 1, Delta: "t"})
	if err != nil {
		t.Fatal(err)
	}
	validate(t, append(all, closed...))
}

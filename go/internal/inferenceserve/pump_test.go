package inferenceserve

import (
	"bytes"
	"encoding/json"
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
		t.Errorf("the failure code = %v, want %s: the stream itself failed, which is what this code is for", failure["code"], CodeProviderUnavailable)
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

func TestPumpRefusesAPartThatIsNotTheOneOpen(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	if _, err := Pump(state, provider.Event{Kind: provider.EventTextDelta, ContentIndex: 0, Delta: "a"}); err != ErrPartIndexMismatch {
		t.Errorf("a delta with no part open = %v, want a mismatch", err)
	}
	if _, err := Pump(state, provider.Event{Kind: provider.EventTextStart, ContentIndex: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := Pump(state, provider.Event{Kind: provider.EventTextEnd, ContentIndex: 1, Delta: "a"}); err != ErrPartIndexMismatch {
		t.Errorf("ending part 1 while 0 is open = %v, want a mismatch", err)
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

func TestATerminalWithNoContentSettlesAFailureTheValidatorAccepts(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	all := runTrace(t, state, provider.Event{Kind: provider.EventDone, Message: &provider.AssistantMessage{StopReason: "stop"}})
	validate(t, all)
	terminal := all[len(all)-1]
	if terminal.Type != "inference.failed" {
		t.Fatalf("got a %s, want inference.failed: an empty terminal would be a content array the schema refuses", terminal.Type)
	}
	failure := body(t, terminal)["error"].(map[string]any)
	if failure["code"] != CodeProtocolViolation {
		t.Errorf("the code = %v, want %s", failure["code"], CodeProtocolViolation)
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

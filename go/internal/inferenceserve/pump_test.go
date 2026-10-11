package inferenceserve

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/provider"
	"github.com/lsm/open-agent-protocol/go/validation"
)

func dataFrame(payload string) string {
	return "data: " + payload + "\n\n"
}

func framesReader(frames []string) provider.ReadChunkFunc {
	i := 0
	return func() ([]byte, error) {
		if i >= len(frames) {
			return nil, nil
		}
		i++
		return []byte(frames[i-1]), nil
	}
}

func completionsEvents(frames ...string) []provider.Event {
	sink := &provider.EventSink{}
	model := provider.Model{ID: "deepseek-v4-pro", API: "openai-completions", Provider: "deepseek", BaseURL: "https://api.deepseek.com", HasBaseURL: true}
	provider.Stream(sink, model, provider.Context{}, provider.StreamOptions{}, framesReader(frames), nil)
	return sink.Drain()
}

func anthropicEvents(frames ...string) []provider.Event {
	sink := &provider.EventSink{}
	model := provider.Model{ID: "claude-sonnet-4-5", API: provider.AnthropicWire, Provider: "anthropic", BaseURL: "https://api.anthropic.com", HasBaseURL: true, MaxTokens: 30000}
	provider.StreamAnthropic(sink, model, provider.Context{}, provider.AnthropicOptions{}, framesReader(frames), nil, func() string { return "" })
	return sink.Drain()
}

func pumpAll(t *testing.T, events []provider.Event) ([]Envelope, []error) {
	t.Helper()
	state := NewState(&Ids{}, "i1", "deepseek/openai-completions@deepseek-v4-pro")
	accepted, err := state.Accepted("c1", Honoured{IncludeSnapshot: "never"})
	if err != nil {
		t.Fatal(err)
	}
	envelopes := []Envelope{accepted}
	var refused []error
	for _, event := range events {
		out, err := state.Pump(event, 1700000000000)
		if err != nil {
			refused = append(refused, err)
			continue
		}
		envelopes = append(envelopes, out...)
	}
	return envelopes, refused
}

func typesOf(envelopes []Envelope) []string {
	out := make([]string, len(envelopes))
	for i, envelope := range envelopes {
		out[i] = envelope.Type
	}
	return out
}

func assertValid(t *testing.T, envelopes []Envelope) {
	t.Helper()
	validator, err := validation.NewProviderValidator()
	if err != nil {
		t.Fatal(err)
	}
	create := map[string]any{
		"protocol": Protocol, "version": Version, "profile": Profile, "type": "inference.create.request", "id": "c1",
		"payload": map[string]any{"model_ref": "deepseek/openai-completions@deepseek-v4-pro", "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
	}
	lines := []any{create}
	for _, envelope := range envelopes {
		lines = append(lines, envelope)
	}
	document, err := json.Marshal(lines)
	if err != nil {
		t.Fatal(err)
	}
	result := validator.Validate(bytes.NewReader(document), "pumped")
	for _, diagnostic := range result.Diagnostics {
		t.Errorf("%s %s at %s: %s", diagnostic.Phase, diagnostic.Code, diagnostic.Pointer, diagnostic.Message)
	}
}

func payloadOf(t *testing.T, envelope Envelope) map[string]any {
	t.Helper()
	var held map[string]any
	if err := json.Unmarshal(envelope.Payload, &held); err != nil {
		t.Fatal(err)
	}
	return held
}

func TestATextThenToolCallTurnPumpsAValidTraceWithItsUsage(t *testing.T) {
	envelopes, refused := pumpAll(t, completionsEvents(
		dataFrame(`{"choices":[{"index":0,"delta":{"content":"look"}}]}`),
		dataFrame(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`),
		dataFrame(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"a\"}"}}]}}]}`),
		dataFrame(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`),
		"data: [DONE]\n\n",
	))
	if len(refused) != 0 {
		t.Fatalf("refused = %v", refused)
	}
	want := []string{
		"inference.create.response", "inference.started",
		"inference.part.started", "inference.part.delta",
		"inference.part.ended",
		"inference.part.started", "inference.part.delta", "inference.part.ended",
		"inference.completed",
	}
	if got := typesOf(envelopes); !slices.Equal(got, want) {
		t.Fatalf("types = %v, want %v", got, want)
	}
	completed := payloadOf(t, envelopes[len(envelopes)-1])
	usage, _ := completed["usage"].(map[string]any)
	if completed["stop_reason"] != "tool_use" || usage["input_tokens"] != 7.0 || usage["output_tokens"] != 3.0 || usage["total_tokens"] != 10.0 {
		t.Fatalf("completed = %v", completed)
	}
	assertValid(t, envelopes)
}

func TestAnAnthropicReasoningPartEndsWithItsSignatureAsCarry(t *testing.T) {
	envelopes, refused := pumpAll(t, anthropicEvents(
		dataFrame(`{"type":"message_start","message":{"id":"m","usage":{"input_tokens":5,"output_tokens":0}}}`),
		dataFrame(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		dataFrame(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"ponder"}}`),
		dataFrame(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-9"}}`),
		dataFrame(`{"type":"content_block_stop","index":0}`),
		dataFrame(`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
		dataFrame(`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}`),
		dataFrame(`{"type":"content_block_stop","index":1}`),
		dataFrame(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`),
		dataFrame(`{"type":"message_stop"}`),
	))
	if len(refused) != 0 {
		t.Fatalf("refused = %v", refused)
	}
	var ended []map[string]any
	for _, envelope := range envelopes {
		if envelope.Type == "inference.part.ended" {
			ended = append(ended, payloadOf(t, envelope))
		}
	}
	if len(ended) != 2 || ended[0]["part_kind"] != "reasoning" || ended[0]["carry"] != "sig-9" || ended[0]["text"] != "ponder" || ended[1]["text"] != "answer" {
		t.Fatalf("ended = %v", ended)
	}
	assertValid(t, envelopes)
}

func TestAProviderFailureNamingHTTP401SettlesACredentialRejection(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	out, err := state.Pump(provider.Event{Kind: provider.EventError, Reason: "deepseek request failed: HTTP 401 (check DEEPSEEK_API_KEY)"}, 1)
	if err != nil || len(out) != 1 {
		t.Fatalf("out = %v, err = %v", out, err)
	}
	failure := payloadOf(t, out[0])["error"].(map[string]any)
	if out[0].Type != "inference.failed" || failure["code"] != CodeCredentialRejected || failure["message"] != "deepseek request failed: HTTP 401 (check DEEPSEEK_API_KEY)" {
		t.Fatalf("failed = %s %v", out[0].Type, failure)
	}
}

func TestFailureCodeReadsOnlyAnExactStatus(t *testing.T) {
	cases := map[string]string{
		"anthropic request failed: HTTP 401 (check ANTHROPIC_API_KEY is valid)": CodeCredentialRejected,
		"deepseek request failed: HTTP 403{\"error\":\"forbidden\"}":            CodeCredentialRejected,
		"openai request failed: HTTP 500":                                       CodeProviderUnavailable,
		"zai-coding-plan request failed: HTTP 429 (Rate limit reached)":         CodeRateLimited,
		"openai request failed: HTTP 4010":                                      CodeProviderUnavailable,
		"connection reset":                                                      CodeProviderUnavailable,
		"request failed: HTTP ":                                                 CodeProviderUnavailable,
	}
	for message, want := range cases {
		if got := FailureCode(message); got != want {
			t.Errorf("FailureCode(%q) = %q, want %q", message, got, want)
		}
	}
}

func TestACancelledStreamSettlesAbortedWithoutEndingItsOpenPart(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	for _, event := range []provider.Event{
		{Kind: provider.EventStart},
		{Kind: provider.EventTextStart, ContentIndex: 0},
		{Kind: provider.EventTextDelta, ContentIndex: 0, Delta: "par"},
	} {
		if _, err := state.Pump(event, 1); err != nil {
			t.Fatal(err)
		}
	}
	out, err := state.Pump(provider.Event{Kind: provider.EventError, Reason: ReasonCancelled}, 1)
	if err != nil || len(out) != 1 || out[0].Type != "inference.failed" {
		t.Fatalf("out = %v, err = %v", out, err)
	}
	if code := payloadOf(t, out[0])["error"].(map[string]any)["code"]; code != CodeAborted {
		t.Fatalf("code = %v, want aborted", code)
	}
}

func TestADoneWithAPartStillOpenSettlesAnEndpointError(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	state.Pump(provider.Event{Kind: provider.EventStart}, 1)
	state.Pump(provider.Event{Kind: provider.EventToolCallStart, ContentIndex: 0, ID: "c", Name: "n"}, 1)
	out, err := state.Pump(provider.Event{Kind: provider.EventDone, Message: &provider.AssistantMessage{StopReason: provider.StopToolUse}}, 1)
	if err != nil || len(out) != 1 || out[0].Type != "inference.failed" {
		t.Fatalf("out = %v, err = %v", out, err)
	}
	if code := payloadOf(t, out[0])["error"].(map[string]any)["code"]; code != CodeEndpointError {
		t.Fatalf("code = %v, want endpoint_error", code)
	}
}

func TestADoneWhoseStopReasonIsErrorSettlesAFailureWithUsage(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	out, err := state.Pump(provider.Event{Kind: provider.EventDone, Message: &provider.AssistantMessage{StopReason: provider.StopError, Usage: provider.Usage{InputTokens: 2, OutputTokens: 1}}}, 1)
	if err != nil || len(out) != 1 || out[0].Type != "inference.failed" {
		t.Fatalf("out = %v, err = %v", out, err)
	}
	held := payloadOf(t, out[0])
	if held["error"].(map[string]any)["code"] != CodeProviderUnavailable || held["usage"].(map[string]any)["total_tokens"] != 3.0 {
		t.Fatalf("failed = %v", held)
	}
}

func TestNothingFollowsATerminal(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	state.Pump(provider.Event{Kind: provider.EventError, Reason: "boom"}, 1)
	for _, event := range []provider.Event{
		{Kind: provider.EventDone, Message: &provider.AssistantMessage{StopReason: provider.StopStop}},
		{Kind: provider.EventError, Reason: "again"},
		{Kind: provider.EventTextStart},
	} {
		if out, err := state.Pump(event, 1); !errors.Is(err, ErrTerminalEmitted) || len(out) != 0 {
			t.Fatalf("%s after a terminal: out = %v, err = %v", event.Kind, out, err)
		}
	}
	if out, err := state.Pump(provider.Event{Kind: provider.EventKeepalive}, 1); err != nil || len(out) != 0 {
		t.Fatalf("keepalive after a terminal: out = %v, err = %v", out, err)
	}
}

func TestAKeepaliveEmitsNothingAndConsumesNoSequence(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	if out, err := state.Pump(provider.Event{Kind: provider.EventKeepalive}, 1); err != nil || len(out) != 0 {
		t.Fatalf("out = %v, err = %v", out, err)
	}
	out, err := state.Pump(provider.Event{Kind: provider.EventStart}, 1)
	if err != nil || out[0].Sequence != 1 {
		t.Fatalf("started = %v, err = %v, want sequence 1", out, err)
	}
}

func TestThePartGatesRefuseWhatZigRefuses(t *testing.T) {
	cases := []struct {
		name  string
		setup []provider.Event
		event provider.Event
		want  error
	}{
		{"a second start", []provider.Event{{Kind: provider.EventTextStart}}, provider.Event{Kind: provider.EventTextStart, ContentIndex: 1}, ErrPartAlreadyOpen},
		{"a tool call without a name", nil, provider.Event{Kind: provider.EventToolCallStart, ID: "c"}, ErrToolCallIdentityNeeded},
		{"a text part with an id", nil, provider.Event{Kind: provider.EventTextStart, ID: "c"}, ErrToolCallIdentityRefused},
		{"a delta with nothing open", nil, provider.Event{Kind: provider.EventThinkingDelta, Delta: "x"}, ErrNoOpenPart},
		{"a delta for another part", []provider.Event{{Kind: provider.EventTextStart}}, provider.Event{Kind: provider.EventTextDelta, ContentIndex: 1}, ErrPartIndexMismatch},
		{"an end for another part", []provider.Event{{Kind: provider.EventTextStart}}, provider.Event{Kind: provider.EventTextEnd, ContentIndex: 1}, ErrPartIndexMismatch},
		{"a text end on a tool call", []provider.Event{{Kind: provider.EventToolCallStart, ID: "c", Name: "n"}}, provider.Event{Kind: provider.EventTextEnd}, ErrToolCallNeedsCall},
		{"a reasoning end on text", []provider.Event{{Kind: provider.EventTextStart}}, provider.Event{Kind: provider.EventThinkingEnd}, ErrPartKindMismatch},
		{"a tool call end on text", []provider.Event{{Kind: provider.EventTextStart}}, provider.Event{Kind: provider.EventToolCallEnd, ToolCall: &provider.ToolCall{ID: "c", Name: "n"}}, ErrPartKindMismatch},
		{"a tool call end without its call", []provider.Event{{Kind: provider.EventToolCallStart, ID: "c", Name: "n"}}, provider.Event{Kind: provider.EventToolCallEnd}, ErrToolCallNeedsCall},
	}
	for _, c := range cases {
		state := NewState(&Ids{}, "i1", "m")
		for _, event := range c.setup {
			if _, err := state.Pump(event, 1); err != nil {
				t.Fatalf("%s: setup refused: %v", c.name, err)
			}
		}
		before := state.sequence
		out, err := state.Pump(c.event, 1)
		if !errors.Is(err, c.want) || len(out) != 0 || state.sequence != before {
			t.Errorf("%s: out = %v, err = %v, want %v and no sequence consumed", c.name, out, err, c.want)
		}
	}
}

func TestAToolCallWithMalformedArgumentsSettlesAProtocolViolation(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	state.Pump(provider.Event{Kind: provider.EventToolCallStart, ContentIndex: 0, ID: "c", Name: "n"}, 1)
	out, err := state.Pump(provider.Event{Kind: provider.EventToolCallEnd, ContentIndex: 0, ToolCall: &provider.ToolCall{ID: "c", Name: "n", Arguments: `{"a":`}}, 1)
	if err != nil || len(out) != 1 || out[0].Type != "inference.failed" {
		t.Fatalf("out = %v, err = %v", out, err)
	}
	if code := payloadOf(t, out[0])["error"].(map[string]any)["code"]; code != CodeProtocolViolation {
		t.Fatalf("code = %v", code)
	}
}

func TestAToolCallWithNoArgumentsEndsWithAnEmptyObject(t *testing.T) {
	state := NewState(&Ids{}, "i1", "m")
	state.Pump(provider.Event{Kind: provider.EventToolCallStart, ContentIndex: 0, ID: "c", Name: "n"}, 1)
	out, err := state.Pump(provider.Event{Kind: provider.EventToolCallEnd, ContentIndex: 0, ToolCall: &provider.ToolCall{ID: "c", Name: "n"}}, 1)
	if err != nil || len(out) != 1 {
		t.Fatalf("out = %v, err = %v", out, err)
	}
	call := payloadOf(t, out[0])["tool_call"].(map[string]any)
	if arguments, _ := json.Marshal(call["arguments_json"]); string(arguments) != "{}" {
		t.Fatalf("arguments_json = %s", arguments)
	}
}

func TestACompletionsReasoningThenTextTurnPumpsTwoPartsAndAValidTrace(t *testing.T) {
	envelopes, refused := pumpAll(t, completionsEvents(
		dataFrame(`{"choices":[{"index":0,"delta":{"reasoning_content":"think"}}]}`),
		dataFrame(`{"choices":[{"index":0,"delta":{"content":"answer"}}]}`),
		dataFrame(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
		"data: [DONE]\n\n",
	))
	if len(refused) != 0 {
		t.Fatalf("refused = %v", refused)
	}
	var kinds []string
	for _, envelope := range envelopes {
		if envelope.Type == "inference.part.ended" {
			ended := payloadOf(t, envelope)
			kinds = append(kinds, ended["part_kind"].(string)+":"+ended["text"].(string))
		}
	}
	if !slices.Equal(kinds, []string{"reasoning:think", "text:answer"}) {
		t.Fatalf("ended parts = %v", kinds)
	}
	assertValid(t, envelopes)
}

func TestACompletionsTurnWithTwoToolCallsPumpsAValidTrace(t *testing.T) {
	envelopes, refused := pumpAll(t, completionsEvents(
		dataFrame(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read","arguments":"{\"p\":\"a\"}"}}]}}]}`),
		dataFrame(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"read","arguments":"{\"p\":\"b\"}"}}]}}]}`),
		dataFrame(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`),
		"data: [DONE]\n\n",
	))
	if len(refused) != 0 {
		t.Fatalf("refused = %v", refused)
	}
	ended := 0
	for _, envelope := range envelopes {
		if envelope.Type == "inference.part.ended" {
			ended++
		}
	}
	if ended != 2 || envelopes[len(envelopes)-1].Type != "inference.completed" {
		t.Fatalf("types = %v", typesOf(envelopes))
	}
	assertValid(t, envelopes)
}

package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func chunkReader(chunks []string) ReadChunkFunc {
	i := 0
	return func() ([]byte, error) {
		if i >= len(chunks) {
			return nil, nil
		}
		out := []byte(chunks[i])
		i++
		return out, nil
	}
}

func sseFrame(payload string) string {
	return "data: " + payload + "\n\n"
}

func runStream(t *testing.T, model Model, frames ...string) []Event {
	t.Helper()
	sink := &EventSink{}
	Stream(sink, model, Context{}, StreamOptions{}, chunkReader(frames), nil)
	return sink.Drain()
}

func kindsOf(events []Event) []EventKind {
	out := make([]EventKind, 0, len(events))
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

func findEvent(t *testing.T, events []Event, kind EventKind) Event {
	t.Helper()
	for _, e := range events {
		if e.Kind == kind {
			return e
		}
	}
	t.Fatalf("no %q event in %v", kind, kindsOf(events))
	return Event{}
}

func countKind(events []Event, kind EventKind) int {
	n := 0
	for _, e := range events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func streamModel() Model {
	return Model{
		ID: "local-model", API: "openai-completions", Provider: "local",
		BaseURL: "http://127.0.0.1:8080/v1", HasBaseURL: true, MaxTokens: 100, HasCompat: true,
	}
}

func TestTheStreamOpensWithAStartBeforeAnyByte(t *testing.T) {
	events := runStream(t, streamModel())
	if len(events) == 0 || events[0].Kind != EventStart {
		t.Fatalf("got %v, want a start first", kindsOf(events))
	}
	start := events[0]
	if start.Partial.Usage != (Usage{}) {
		t.Errorf("the start's usage = %+v, want the zero usage", start.Partial.Usage)
	}
	if start.Partial.StopReason != "stop" {
		t.Errorf("the start's stop reason = %q, want stop", start.Partial.StopReason)
	}
	if start.Partial.Model != "local-model" {
		t.Errorf("the start's model = %q", start.Partial.Model)
	}
}

func TestTextArrivesAsDeltasAndTheTerminalCarriesTheWhole(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"content":"Hel"}}]}`),
		sseFrame(`{"choices":[{"delta":{"content":"lo"}}]}`),
	)
	deltas := []string{}
	for _, e := range events {
		if e.Kind == EventTextDelta {
			deltas = append(deltas, e.Delta)
		}
	}
	if strings.Join(deltas, "") != "Hello" {
		t.Errorf("deltas = %v, want them to join to Hello", deltas)
	}
	done := findEvent(t, events, EventDone)
	if done.Message.Content[0].Text.Text != "Hello" {
		t.Errorf("the terminal's text = %q, want Hello", done.Message.Content[0].Text.Text)
	}
}

func TestAStreamThatProducedNothingStillYieldsOneEmptyTextPart(t *testing.T) {
	events := runStream(t, streamModel(), sseFrame("[DONE]"))
	done := findEvent(t, events, EventDone)
	if len(done.Message.Content) != 1 {
		t.Fatalf("got %d content blocks, want exactly one", len(done.Message.Content))
	}
	if done.Message.Content[0].Text == nil || done.Message.Content[0].Text.Text != "" {
		t.Errorf("the block = %+v, want an empty text part", done.Message.Content[0])
	}
}

func TestTheTerminalOrdersThinkingThenTextThenToolCalls(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"content":"answer"}}]}`),
		sseFrame(`{"choices":[{"delta":{"reasoning_content":"pondering"}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read"}}]}}]}`),
	)
	done := findEvent(t, events, EventDone)
	blocks := done.Message.Content
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want thinking, text and one tool call", len(blocks))
	}
	if blocks[0].Thinking == nil {
		t.Errorf("the first block = %+v, want the thinking", blocks[0])
	}
	if blocks[1].Text == nil {
		t.Errorf("the second block = %+v, want the text", blocks[1])
	}
	if blocks[2].ToolCall == nil {
		t.Errorf("the third block = %+v, want the tool call", blocks[2])
	}
}

func TestTheThinkingBlockCarriesTheSignatureFieldNameItSaw(t *testing.T) {
	events := runStream(t, streamModel(), sseFrame(`{"choices":[{"delta":{"reasoning":"pondering"}}]}`))
	done := findEvent(t, events, EventDone)
	thinking := done.Message.Content[0].Thinking
	if thinking.Signature != "reasoning" {
		t.Errorf("the signature = %q, want the field name the delta used", thinking.Signature)
	}
}

func TestAStreamWithNoThinkingCarriesNoSignature(t *testing.T) {
	events := runStream(t, streamModel(), sseFrame(`{"choices":[{"delta":{"content":"x"}}]}`))
	done := findEvent(t, events, EventDone)
	if done.Message.Content[0].Thinking != nil {
		t.Error("a text-only stream has no thinking block to carry a signature")
	}
}

func TestKimiThinkingIsNeverStreamedAsADelta(t *testing.T) {
	model := streamModel()
	model.Provider = "kimi"
	events := runStream(t, model, sseFrame(`{"choices":[{"delta":{"reasoning_content":"pondering"}}]}`))
	if countKind(events, EventThinkingDelta) != 0 {
		t.Error("kimi's thinking is dropped: no thinking delta is emitted")
	}
	plain := runStream(t, streamModel(), sseFrame(`{"choices":[{"delta":{"reasoning_content":"pondering"}}]}`))
	if countKind(plain, EventThinkingDelta) != 1 {
		t.Error("every other model streams its thinking")
	}
}

func TestAToolCallEndCarriesItsPositionNotTheIndexItsStartUsed(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"content":"working"}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`),
	)
	start := findEvent(t, events, EventToolCallStart)
	end := findEvent(t, events, EventToolCallEnd)
	if start.ContentIndex != 0 {
		t.Errorf("the start's content index = %d, want 0: it counted tool calls alone", start.ContentIndex)
	}
	if end.ContentIndex != 1 {
		t.Errorf("the end's content index = %d, want 1: it is the position in the final array, which holds the text", end.ContentIndex)
	}
}

func TestEachToolCallEndsWithTheContentSoFarAndThePartialGrows(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"content":"ab"}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c2","function":{"name":"g"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"2"}}]}}]}`),
	)
	var partials [][]AssistantBlock
	for _, e := range events {
		if e.Kind == EventToolCallEnd {
			partials = append(partials, e.Partial.Content)
		}
	}
	if len(partials) != 2 {
		t.Fatalf("got %d tool call ends, want 2", len(partials))
	}
	describe := func(blocks []AssistantBlock) string {
		out := []string{}
		for _, b := range blocks {
			switch {
			case b.Text != nil:
				out = append(out, "text:"+b.Text.Text)
			case b.Thinking != nil:
				out = append(out, "thinking:"+b.Thinking.Thinking)
			case b.ToolCall != nil:
				out = append(out, "call:"+b.ToolCall.ID)
			}
		}
		return strings.Join(out, ",")
	}
	if got := describe(partials[0]); got != "text:ab" {
		t.Errorf("the first end's partial = %q, want only what came before it", got)
	}
	if got := describe(partials[1]); got != "text:ab,call:c1" {
		t.Errorf("the second end's partial = %q, want the text and the first finished call, but not the call it ends", got)
	}
}

func TestAContentIndexIsAssignedByArrivalNotByTheApiIndex(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"second","function":{"name":"g"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"first","function":{"name":"f"}}]}}]}`),
	)
	starts := map[string]int{}
	var ends []Event
	for _, e := range events {
		if e.Kind == EventToolCallStart {
			starts[e.ID] = e.ContentIndex
		}
		if e.Kind == EventToolCallEnd {
			ends = append(ends, e)
		}
	}
	if starts["second"] != 0 || starts["first"] != 1 {
		t.Errorf("starts = %v, want the counter to follow arrival: the api index keys the tracker, the counter assigns the content index", starts)
	}
	if len(ends) != 2 {
		t.Fatalf("got %d ends, want 2", len(ends))
	}
	if ends[0].ToolCall.ID != "second" || ends[1].ToolCall.ID != "first" {
		t.Errorf("ends carry %q then %q, want the content order the starts established", ends[0].ToolCall.ID, ends[1].ToolCall.ID)
	}
}

func TestTheApiIndexKeysTheTrackerNotTheContentIndex(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":7,"id":"c7","function":{"name":"f"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":7,"function":{"arguments":"{}"}}]}}]}`),
	)
	if countKind(events, EventToolCallStart) != 1 {
		t.Errorf("a repeated api index must not open a second call: %v", kindsOf(events))
	}
	end := findEvent(t, events, EventToolCallEnd)
	if end.ToolCall.Arguments != "{}" {
		t.Errorf("the delta landed on the wrong call: %q", end.ToolCall.Arguments)
	}
}

func TestArgumentDeltasAreReEmittedUnderTheIndexTheTrackerHolds(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`),
	)
	joined := ""
	for _, e := range events {
		if e.Kind == EventToolCallDelta {
			if e.ContentIndex != 0 {
				t.Errorf("a delta at index %d, want the tracker's index 0", e.ContentIndex)
			}
			joined += e.Delta
		}
	}
	if joined != `{"a":1}` {
		t.Errorf("the joined arguments = %q", joined)
	}
	end := findEvent(t, events, EventToolCallEnd)
	if end.ToolCall.Arguments != `{"a":1}` {
		t.Errorf("the completed call's arguments = %q", end.ToolCall.Arguments)
	}
}

func TestAnEmptyArgumentStringIsNotADelta(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":""}}]}}]}`),
	)
	if countKind(events, EventToolCallDelta) != 0 {
		t.Error("an empty arguments string produces no delta")
	}
}

func TestUsageComesFromTheChunkAndTheTotalIsBackfilled(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"content":"x"}}]}`),
		sseFrame(`{"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}}`),
	)
	done := findEvent(t, events, EventDone)
	if done.Message.Usage.TotalTokens != 14 {
		t.Errorf("the total = %d, want the reported 14", done.Message.Usage.TotalTokens)
	}
	backfilled := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"content":"x"}}]}`),
		sseFrame(`{"usage":{"prompt_tokens":10,"completion_tokens":4}}`),
	)
	got := findEvent(t, backfilled, EventDone)
	if got.Message.Usage.TotalTokens != 14 {
		t.Errorf("a zero total is backfilled to %d, want 10+4", got.Message.Usage.TotalTokens)
	}
}

func TestCachedTokensComeOutOfTheInputAndReasoningIntoTheOutput(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"usage":{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":40},"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":7}}}`),
	)
	done := findEvent(t, events, EventDone)
	if done.Message.Usage.InputTokens != 60 {
		t.Errorf("the input = %d, want 100 less the 40 cached", done.Message.Usage.InputTokens)
	}
	if done.Message.Usage.OutputTokens != 12 {
		t.Errorf("the output = %d, want 5 plus the 7 reasoning", done.Message.Usage.OutputTokens)
	}
}

func TestTheFinishReasonIsMappedToTheRuntimesOwn(t *testing.T) {
	cases := map[string]StopReason{
		"stop":           StopStop,
		"length":         StopLength,
		"tool_calls":     StopToolUse,
		"content_filter": StopError,
		"anything else":  StopStop,
	}
	for finish, want := range cases {
		payload := fmt.Sprintf(`{"choices":[{"finish_reason":%q}]}`, finish)
		done := findEvent(t, runStream(t, streamModel(), sseFrame(payload)), EventDone)
		if done.Message.StopReason != want {
			t.Errorf("finish %q = %q, want %q", finish, done.Message.StopReason, want)
		}
	}
}

func TestOnlyTheFirstChoiceIsRead(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"content":"first"}},{"delta":{"content":"second"}}]}`),
	)
	done := findEvent(t, events, EventDone)
	if done.Message.Content[0].Text.Text != "first" {
		t.Errorf("the text = %q, want only the first choice", done.Message.Content[0].Text.Text)
	}
}

func TestToolCallsWinOverThinkingAndThinkingOverText(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"content":"text","reasoning":"thought","tool_calls":[{"index":0,"id":"c1","function":{"name":"f"}}]}}]}`),
	)
	if countKind(events, EventToolCallStart) != 1 {
		t.Error("a delta carrying tool calls is a tool call, not text or thinking")
	}
	if countKind(events, EventThinkingDelta) != 0 || countKind(events, EventTextDelta) != 0 {
		t.Error("the text and the thinking are not emitted alongside the tool call")
	}
}

func TestTheFirstNonEmptyReasoningFieldWins(t *testing.T) {
	for _, field := range []string{"reasoning_content", "reasoning", "reasoning_text"} {
		payload := fmt.Sprintf(`{"choices":[{"delta":{%q:"pondering"}}]}`, field)
		done := findEvent(t, runStream(t, streamModel(), sseFrame(payload)), EventDone)
		if done.Message.Content[0].Thinking.Signature != field {
			t.Errorf("field %q = signature %q", field, done.Message.Content[0].Thinking.Signature)
		}
	}
	both := `{"choices":[{"delta":{"reasoning_content":"first","reasoning":"second"}}]}`
	done := findEvent(t, runStream(t, streamModel(), sseFrame(both)), EventDone)
	if done.Message.Content[0].Thinking.Thinking != "first" {
		t.Errorf("with two fields the first wins, got %q", done.Message.Content[0].Thinking.Thinking)
	}
}

func TestAnEmptyReasoningStringIsSkipped(t *testing.T) {
	events := runStream(t, streamModel(), sseFrame(`{"choices":[{"delta":{"reasoning_content":"","content":"text"}}]}`))
	done := findEvent(t, events, EventDone)
	if done.Message.Content[0].Text.Text != "text" {
		t.Errorf("an empty reasoning string falls through to the content, got %+v", done.Message.Content[0])
	}
}

func TestReasoningDetailsAttachToTheToolCallTheyName(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.encrypted","id":"c1","data":"blob"}]}}]}`),
	)
	end := findEvent(t, events, EventToolCallEnd)
	if !end.ToolCall.HasThought || end.ToolCall.ThoughtSig == "" {
		t.Fatalf("the completed call = %+v, want the thought signature attached", end.ToolCall)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(end.ToolCall.ThoughtSig), &detail); err != nil {
		t.Fatalf("the signature is not the detail json: %v", err)
	}
	if detail["type"] != "reasoning.encrypted" || detail["id"] != "c1" || detail["data"] != "blob" {
		t.Errorf("the detail = %v, want the type, id and data", detail)
	}
}

func TestAnUnencryptedReasoningDetailIsIgnored(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"reasoning_details":[{"type":"other","id":"c1","data":"blob"}]}}]}`),
	)
	end := findEvent(t, events, EventToolCallEnd)
	if end.ToolCall.HasThought {
		t.Error("only an encrypted detail counts as a thought signature")
	}
}

func TestCancellationEndsTheStreamWithTheCancelsReason(t *testing.T) {
	sink := &EventSink{}
	read := false
	Stream(sink, streamModel(), Context{}, StreamOptions{}, func() ([]byte, error) {
		read = true
		return nil, nil
	}, func() bool { return true })
	events := sink.Drain()
	if read {
		t.Error("a cancelled call must not read")
	}
	if len(events) == 0 {
		t.Fatal("no events")
	}
	last := events[len(events)-1]
	if last.Kind != EventError || last.Reason != "request cancelled" {
		t.Errorf("the terminal = %+v, want the cancel reason", last)
	}
}

func TestCancellationMidStreamEndsWithTheCancelsReason(t *testing.T) {
	sink := &EventSink{}
	calls := 0
	Stream(sink, streamModel(), Context{}, StreamOptions{}, func() ([]byte, error) {
		calls++
		return []byte(sseFrame(`{"choices":[{"delta":{"content":"x"}}]}`)), nil
	}, func() bool { return calls > 1 })
	events := sink.Drain()
	last := events[len(events)-1]
	if last.Kind != EventError || last.Reason != "request cancelled" {
		t.Errorf("the terminal = %+v, want the cancel reason once a chunk has been read", last)
	}
	if countKind(events, EventDone) != 0 {
		t.Error("a cancelled stream has no terminal message")
	}
}

func TestAnEmptyChoicesArrayStopsNothingAndEndsTheStream(t *testing.T) {
	events := runStream(t, streamModel(), sseFrame(`{"choices":[]}`))
	if countKind(events, EventDone) != 1 {
		t.Errorf("got %v, want a terminal", kindsOf(events))
	}
}

func TestAMalformedChunkIsSwallowedAndTheStreamStillCompletes(t *testing.T) {
	sink := &EventSink{}
	Stream(sink, streamModel(), Context{}, StreamOptions{}, chunkReader([]string{
		sseFrame(`{"choices":[{"delta":{"content":"kept"}}]}`),
		sseFrame("not json at all"),
	}), nil)
	events := sink.Drain()
	if countKind(events, EventError) != 0 {
		t.Error("zig's parseChunk swallows a malformed chunk with catch return, so no error reaches the stream")
	}
	done := findEvent(t, events, EventDone)
	if done.Message.Content[0].Text.Text != "kept" {
		t.Errorf("the text = %q, want what came before the malformed chunk", done.Message.Content[0].Text.Text)
	}
}

func TestThePartialTextRuleHoldsOnlyWithNoToolCallAndSomeText(t *testing.T) {
	cases := []struct {
		text, thinking, calls int
		want                  bool
	}{
		{text: 5, thinking: 0, calls: 0, want: true},
		{text: 0, thinking: 4, calls: 0, want: true},
		{text: 5, thinking: 4, calls: 0, want: true},
		{text: 5, thinking: 0, calls: 1, want: false},
		{text: 0, thinking: 0, calls: 0, want: false},
	}
	for _, c := range cases {
		if got := CanCompletePartialTextOnStreamError(c.text, c.thinking, c.calls); got != c.want {
			t.Errorf("text=%d thinking=%d calls=%d = %v, want %v", c.text, c.thinking, c.calls, got, c.want)
		}
	}
}

func TestAStreamErrorCompletesAsLengthOnlyWhenTheRuleHolds(t *testing.T) {
	state := newStreamState(streamModel())
	state.text = "partial"
	sink := &EventSink{}
	if !state.completeOnStreamError(sink) {
		t.Fatal("with text and no tool call the partial completes")
	}
	done := findEvent(t, sink.Drain(), EventDone)
	if done.Message.StopReason != "length" {
		t.Errorf("the stop reason = %q, want length", done.Message.StopReason)
	}
	if done.Message.Content[0].Text.Text != "partial" {
		t.Errorf("the text = %q, want the partial kept", done.Message.Content[0].Text.Text)
	}

	opened := newStreamState(streamModel())
	opened.text = "partial"
	opened.toolCalls = 1
	opened.tracker.startCall(0, 0, "c1", "f")
	if opened.completeOnStreamError(&EventSink{}) {
		t.Error("a tool call was opened, so the partial must not complete")
	}
}

func TestTheParserErrorSpellingReachesTheStream(t *testing.T) {
	sink := &EventSink{}
	Stream(sink, streamModel(), Context{}, StreamOptions{}, chunkReader([]string{strings.Repeat("x", 1024*1024+1)}), nil)
	events := sink.Drain()
	last := events[len(events)-1]
	if last.Kind != EventError || last.Reason != "sse line too large" {
		t.Errorf("the terminal = %+v, want the line too large spelling", last)
	}
}

func TestEventsAreDeliveredInTheOrderTheyWereEmitted(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"content":"a"}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`),
	)
	want := []EventKind{EventStart, EventTextDelta, EventToolCallStart, EventToolCallDelta, EventToolCallEnd, EventDone}
	got := kindsOf(events)
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %q, want %q (full %v)", i, got[i], want[i], got)
		}
	}
}

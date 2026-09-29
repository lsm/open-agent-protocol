package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func sseFrame(payload string) string {
	return "data: " + payload + "\n\n"
}

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

func TestTheTerminalIsTheAssemblyInPartIndexOrder(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"reasoning_content":"pondering"}}]}`),
		sseFrame(`{"choices":[{"delta":{"content":"answer"}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read"}}]}}]}`),
	)
	blocks := findEvent(t, events, EventDone).Message.Content
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

func TestTheTerminalFollowsTheWireWhenTextArrivesBeforeReasoning(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"content":"answer"}}]}`),
		sseFrame(`{"choices":[{"delta":{"reasoning_content":"pondering"}}]}`),
	)
	blocks := findEvent(t, events, EventDone).Message.Content
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want two", len(blocks))
	}
	if blocks[0].Text == nil {
		t.Errorf("the first block = %+v, want the text: it streamed first, so it holds index 0", blocks[0])
	}
	if blocks[1].Thinking == nil {
		t.Errorf("the second block = %+v, want the reasoning", blocks[1])
	}
	textIndex, thinkingIndex := -1, -1
	for _, event := range events {
		switch event.Kind {
		case EventTextDelta:
			textIndex = event.ContentIndex
		case EventThinkingDelta:
			thinkingIndex = event.ContentIndex
		}
	}
	if textIndex >= thinkingIndex {
		t.Errorf("the text holds index %d and the reasoning %d, want the text lower: the terminal's order is the wire's", textIndex, thinkingIndex)
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

func TestAToolCallEndsAtTheIndexItsStartUsed(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"content":"working"}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`),
	)
	start := findEvent(t, events, EventToolCallStart)
	end := findEvent(t, events, EventToolCallEnd)
	if start.ContentIndex != 1 {
		t.Errorf("the start's content index = %d, want 1: the text part holds 0, and a part's index is its place in the content", start.ContentIndex)
	}
	if end.ContentIndex != start.ContentIndex {
		t.Errorf("the end's content index = %d and the start's = %d: one call occupies one index, and a consumer that opened the part it started cannot end a part it never opened", end.ContentIndex, start.ContentIndex)
	}
}

func TestEveryToolCallEndsAtItsOwnStartIndexWithSeveralCalls(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"content":"working"}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c2","function":{"name":"write"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}},{"index":1,"function":{"arguments":"{}"}}]}}]}`),
	)
	starts := map[string]int{}
	for _, event := range events {
		if event.Kind == EventToolCallStart {
			starts[event.ID] = event.ContentIndex
		}
	}
	ends := 0
	for _, event := range events {
		if event.Kind != EventToolCallEnd {
			continue
		}
		ends++
		opened, known := starts[event.ToolCall.ID]
		if !known {
			t.Errorf("the call %q ended but never started", event.ToolCall.ID)
			continue
		}
		if event.ContentIndex != opened {
			t.Errorf("the call %q opened at %d and ended at %d", event.ToolCall.ID, opened, event.ContentIndex)
		}
	}
	if ends != len(starts) {
		t.Errorf("%d calls ended and %d started, want the same", ends, len(starts))
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
	if starts["first"] != ends[1].ContentIndex || starts["second"] != ends[0].ContentIndex {
		t.Errorf("starts %v against ends %d and %d: a call's index is its place in the content, and nothing may renumber it between the two", starts, ends[0].ContentIndex, ends[1].ContentIndex)
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
	want := []EventKind{
		EventStart, EventTextStart, EventTextDelta,
		EventToolCallStart, EventToolCallDelta,
		EventTextEnd, EventToolCallEnd, EventDone,
	}
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

func TestASinkWithACallbackKeepsNothingForALaterReader(t *testing.T) {
	var seen []Event
	sink := &EventSink{OnEvent: func(event Event) { seen = append(seen, event) }}
	Stream(sink, streamModel(), Context{}, StreamOptions{}, chunkReader([]string{sseFrame(`{"choices":[{"delta":{"content":"hi"}}]}`)}), nil)
	if len(seen) == 0 {
		t.Fatal("a sink with a callback saw nothing")
	}
	if held := sink.Drain(); len(held) != 0 {
		t.Errorf("a sink with a callback still retained %d events, want none: a long reply would keep every event alive", len(held))
	}
}

func TestASinkWithACallbackDoesNotAlsoRetainForItsReader(t *testing.T) {
	var seen int
	sink := &EventSink{OnEvent: func(Event) { seen++ }}
	Stream(sink, streamModel(), Context{}, StreamOptions{}, chunkReader([]string{sseFrame(`{"choices":[{"delta":{"content":"hi"}}]}`)}), nil)
	held := sink.Drain()
	if len(held) != 0 || seen == 0 {
		t.Errorf("a callback saw %d events and a reader %d, want the callback to take the stream and the reader nothing", seen, len(held))
	}
}

func TestATextPartIsDeclaredStartedAndEndedOnTheOpenaiWire(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"content":"hel"}}]}`),
		sseFrame(`{"choices":[{"delta":{"content":"lo"}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`),
	)
	kinds := kindsOf(events)
	var start, end *Event
	for i, event := range events {
		switch event.Kind {
		case EventTextStart:
			start = &events[i]
		case EventTextEnd:
			end = &events[i]
		}
	}
	if start == nil {
		t.Fatalf("no text_start in %v: a part a consumer never saw opened cannot be assembled, and this wire has no block boundary to infer one from", kinds)
	}
	if end == nil {
		t.Fatalf("no text_end in %v: a part.started with no part.ended leaves the terminal over an open part", kinds)
	}
	if end.Delta != "hello" {
		t.Errorf("the text_end carries %q, want the accumulated text: a consumer reading incrementally has only the deltas and needs the whole", end.Delta)
	}
	if start.ContentIndex != end.ContentIndex {
		t.Errorf("the text part opened at %d and ended at %d, want them equal", start.ContentIndex, end.ContentIndex)
	}
	startAt, endAt := -1, -1
	for i, kind := range kinds {
		if kind == EventTextStart {
			startAt = i
		}
		if kind == EventTextEnd {
			endAt = i
		}
	}
	for i, kind := range kinds {
		if kind == EventTextDelta && (i <= startAt || i >= endAt) {
			t.Errorf("the text_delta at %d sits outside the part's %d..%d", i, startAt, endAt)
		}
	}
}

func TestATextOnlyTurnStillDeclaresBothEndsOfItsPart(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"content":"alone"}}]}`),
	)
	kinds := kindsOf(events)
	var hasStart, hasEnd bool
	for _, kind := range kinds {
		switch kind {
		case EventTextStart:
			hasStart = true
		case EventTextEnd:
			hasEnd = true
		}
	}
	if !hasStart || !hasEnd {
		t.Errorf("got %v, want a text_start and a text_end around the text: a single-part turn is the common case, not an exception", kinds)
	}
}

func TestATurnWithNoTextDeclaresNoTextPart(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`),
	)
	for _, kind := range kindsOf(events) {
		if kind == EventTextStart || kind == EventTextEnd {
			t.Errorf("got a %s in a turn that streamed no text, want no text part declared", kind)
		}
	}
}

func TestReasoningThenTextHoldDifferentParts(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"reasoning_content":"think"}}]}`),
		sseFrame(`{"choices":[{"delta":{"content":"answer"}}]}`),
	)
	seen := map[int]EventKind{}
	for _, event := range events {
		switch event.Kind {
		case EventThinkingDelta, EventTextDelta:
			if previous, held := seen[event.ContentIndex]; held {
				t.Errorf("%s and %s both hold content index %d: two parts cannot occupy one index, and a consumer opens a part by index", previous, event.Kind, event.ContentIndex)
			}
			seen[event.ContentIndex] = event.Kind
		}
	}
	if len(seen) != 2 {
		t.Errorf("the turn streamed %d distinct parts, want 2: reasoning and text are different parts and the terminal holds both", len(seen))
	}
	thinkingAt, thoughtThere := seen[0]
	if !thoughtThere || thinkingAt != EventThinkingDelta {
		t.Errorf("index 0 holds %v, want the reasoning: the content array puts thinking before text", seen[0])
	}
	if body := findEvent(t, events, EventDone).Message; len(body.Content) != 2 {
		t.Errorf("the terminal holds %d blocks, want 2", len(body.Content))
	}
}

func TestAReasoningOnlyTurnHoldsIndexZero(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"reasoning_content":"think"}}]}`),
	)
	delta := findEvent(t, events, EventThinkingDelta)
	if delta.ContentIndex != 0 {
		t.Errorf("the reasoning delta holds index %d, want 0: with nothing ahead of it, reasoning is the first part", delta.ContentIndex)
	}
}

func TestNoTwoPartsShareAContentIndexWhateverOrderTheyArriveIn(t *testing.T) {
	cases := map[string][]string{
		"text then reasoning": {
			`{"choices":[{"delta":{"content":"a"}}]}`,
			`{"choices":[{"delta":{"reasoning_content":"t"}}]}`,
		},
		"reasoning then text": {
			`{"choices":[{"delta":{"reasoning_content":"t"}}]}`,
			`{"choices":[{"delta":{"content":"a"}}]}`,
		},
		"a call then trailing text": {
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f"}}]}}]}`,
			`{"choices":[{"delta":{"content":"after"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`,
		},
		"text then a call": {
			`{"choices":[{"delta":{"content":"a"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`,
		},
	}
	for name, frames := range cases {
		built := make([]string, 0, len(frames))
		for _, frame := range frames {
			built = append(built, sseFrame(frame))
		}
		events := runStream(t, streamModel(), built...)
		claimed := map[int]EventKind{}
		for _, event := range events {
			switch event.Kind {
			case EventTextDelta, EventThinkingDelta, EventToolCallStart:
			default:
				continue
			}
			if previous, held := claimed[event.ContentIndex]; held {
				t.Errorf("%s: %s and %s both hold index %d, and a consumer opens a part by its index", name, previous, event.Kind, event.ContentIndex)
			}
			claimed[event.ContentIndex] = event.Kind
		}
	}
}

func TestAContentIndexIsReservedByTheFirstPartThatClaimsIt(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"content":"after"}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`),
	)
	start := findEvent(t, events, EventToolCallStart)
	end := findEvent(t, events, EventToolCallEnd)
	var text *Event
	for _, event := range events {
		if event.Kind == EventTextDelta {
			held := event
			text = &held
		}
	}
	if text == nil {
		t.Fatal("the fixture streamed no text, so it is not the script under test")
	}
	if text.ContentIndex == start.ContentIndex {
		t.Errorf("the trailing text and the call both hold index %d: the call claimed it first, so the text is a second part", text.ContentIndex)
	}
	if end.ContentIndex != start.ContentIndex {
		t.Errorf("the call opened at %d and ended at %d, want them equal", start.ContentIndex, end.ContentIndex)
	}
}

func TestAPartKeepsTheIndexItStartedAtAndTheTerminalFollows(t *testing.T) {
	cases := map[string][]string{
		"text then reasoning then a call": {
			`{"choices":[{"delta":{"content":"answer"}}]}`,
			`{"choices":[{"delta":{"reasoning_content":"pondering"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read"}}]}}]}`,
		},
		"reasoning then text": {
			`{"choices":[{"delta":{"reasoning_content":"pondering"}}]}`,
			`{"choices":[{"delta":{"content":"answer"}}]}`,
		},
		"a call then trailing text": {
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read"}}]}}]}`,
			`{"choices":[{"delta":{"content":"after"}}]}`,
		},
		"text then a call": {
			`{"choices":[{"delta":{"content":"answer"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read"}}]}}]}`,
		},
		"text then two calls": {
			`{"choices":[{"delta":{"content":"a"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c2","function":{"name":"g"}}]}}]}`,
		},
	}
	wireKind := map[EventKind]string{
		EventTextDelta: "text", EventThinkingDelta: "reasoning", EventToolCallStart: "tool_call",
	}
	for name, frames := range cases {
		built := make([]string, 0, len(frames))
		for _, frame := range frames {
			built = append(built, sseFrame(frame))
		}
		events := runStream(t, streamModel(), built...)
		terminal := findEvent(t, events, EventDone)
		at := make(map[int]string, len(terminal.Message.Content))
		for i, block := range terminal.Message.Content {
			switch {
			case block.Thinking != nil:
				at[i] = "reasoning"
			case block.Text != nil:
				at[i] = "text"
			case block.ToolCall != nil:
				at[i] = "tool_call"
			}
		}
		claimed := make(map[int]EventKind, len(at))
		for _, event := range events {
			switch event.Kind {
			case EventTextDelta, EventThinkingDelta, EventToolCallStart:
			default:
				continue
			}
			if previous, held := claimed[event.ContentIndex]; held {
				t.Errorf("%s: %s and %s both hold index %d", name, previous, event.Kind, event.ContentIndex)
			}
			claimed[event.ContentIndex] = event.Kind
		}
		for index, kind := range claimed {
			want, held := at[index]
			if !held {
				t.Errorf("%s: a part claims index %d and the terminal holds %d blocks", name, index, len(at))
				continue
			}
			if wireKind[kind] != want {
				t.Errorf("%s: index %d is %s on the wire and %s in the terminal, and the assembly check compares them", name, index, wireKind[kind], want)
			}
		}
		if len(claimed) != len(at) {
			t.Errorf("%s: %d parts on the wire and %d in the terminal, want the same", name, len(claimed), len(at))
		}
	}
}

func TestPartsEndInIndexOrder(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"content":"before"}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"content":"after"}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`),
	)
	var ends []string
	for _, event := range events {
		if event.Kind == EventTextEnd {
			ends = append(ends, "text@"+itoa(event.ContentIndex))
		}
		if event.Kind == EventToolCallEnd {
			ends = append(ends, "call@"+itoa(event.ContentIndex))
		}
	}
	if got, want := strings.Join(ends, " "), "text@0 call@1"; got != want {
		t.Errorf("the parts end %s, want %s: a consumer applies them in the order they arrive", got, want)
	}
}

func TestAPartEndsAfterEveryPartHoldingALowerIndex(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"content":"a"}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c2","function":{"name":"g"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"content":"b"}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{}"}}]}}]}`),
	)
	var ends []string
	for _, event := range events {
		if event.Kind == EventTextEnd {
			ends = append(ends, "text@"+itoa(event.ContentIndex))
		}
		if event.Kind == EventToolCallEnd {
			ends = append(ends, "call@"+itoa(event.ContentIndex))
		}
	}
	if got, want := strings.Join(ends, " "), "text@0 call@1 call@2"; got != want {
		t.Errorf("the parts end %s, want %s", got, want)
	}
}

func TestACallEndingAfterTheTextKeepsTheTextInItsPartial(t *testing.T) {
	events := runStream(t, streamModel(),
		sseFrame(`{"choices":[{"delta":{"content":"before"}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f"}}]}}]}`),
		sseFrame(`{"choices":[{"delta":{"content":"after"}}]}`),
		sseFrame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`),
	)
	end := findEvent(t, events, EventToolCallEnd)
	if len(end.Partial.Content) != 1 {
		t.Fatalf("the call's end partial holds %d blocks, want only the text ahead of it: the call it ends is not part of the content before it",
			len(end.Partial.Content))
	}
	if end.Partial.Content[0].Text == nil || end.Partial.Content[0].Text.Text != "beforeafter" {
		t.Errorf("the call's end partial = %+v, want the whole text ahead of it", end.Partial.Content)
	}
}

func TestATextEndIsOnlyEmittedForAPartThatOpened(t *testing.T) {
	state := newStreamState(streamModel())
	state.text = "partial"
	sink := &EventSink{}
	if !state.completeOnStreamError(sink) {
		t.Fatal("with text and no tool call the partial completes")
	}
	events := sink.Drain()
	for _, event := range events {
		if event.Kind == EventTextEnd {
			t.Errorf("got a text_end at index %d, want none: no text part ever opened", event.ContentIndex)
		}
	}
	done := findEvent(t, events, EventDone)
	if done.Message.Content[0].Text == nil || done.Message.Content[0].Text.Text != "partial" {
		t.Errorf("the terminal = %+v, want the partial text it kept", done.Message.Content[0])
	}
}

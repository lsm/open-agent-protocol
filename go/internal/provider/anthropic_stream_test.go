package provider

import (
	"strings"
	"testing"
)

func runAnthropic(t *testing.T, model Model, frames ...string) []Event {
	t.Helper()
	return runAnthropicWith(t, model, AnthropicOptions{}, "", frames...)
}

func runAnthropicWith(t *testing.T, model Model, options AnthropicOptions, raw string, frames ...string) []Event {
	t.Helper()
	sink := &EventSink{}
	StreamAnthropic(sink, model, Context{}, options, chunkReader(frames), nil, func() string { return raw })
	return sink.Drain()
}

func anthropicStreamModel() Model {
	return Model{
		ID: "claude-sonnet-4-5", API: AnthropicWire, Provider: "anthropic",
		BaseURL: "https://api.anthropic.com", HasBaseURL: true, MaxTokens: 30000, HasCompat: true,
	}
}

func textBlockFrame(index int, text string) string {
	return sseFrame(`{"type":"content_block_start","index":`+itoa(index)+`,"content_block":{"type":"text","text":""}}`) +
		sseFrame(`{"type":"content_block_delta","index":`+itoa(index)+`,"delta":{"type":"text_delta","text":"`+text+`"}}`) +
		sseFrame(`{"type":"content_block_stop","index":`+itoa(index)+`}`)
}

func TestTheStreamOpensWithAStartAndEndsWithADone(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(), textBlockFrame(0, "hello"))
	if events[0].Kind != EventStart {
		t.Fatalf("got %v, want a start first", kindsOf(events))
	}
	if events[len(events)-1].Kind != EventDone {
		t.Errorf("got %v, want a done last", kindsOf(events))
	}
}

func TestATextBlockEmitsStartDeltaAndEnd(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(),
		sseFrame(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		sseFrame(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"one"}}`),
		sseFrame(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"two"}}`),
		sseFrame(`{"type":"content_block_stop","index":0}`),
	)
	want := []EventKind{EventStart, EventTextStart, EventTextDelta, EventTextDelta, EventTextEnd, EventDone}
	got := kindsOf(events)
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, got[i], want[i])
		}
	}
	if end := findEvent(t, events, EventTextEnd); end.Delta != "onetwo" {
		t.Errorf("the end carries %q, want the whole block, not the last delta", end.Delta)
	}
}

func TestAThinkingBlockCarriesItsSignatureWithoutAnEventOfItsOwn(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(),
		sseFrame(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		sseFrame(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"pondering"}}`),
		sseFrame(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-9"}}`),
		sseFrame(`{"type":"content_block_stop","index":0}`),
	)
	kinds := kindsOf(events)
	for _, kind := range kinds {
		if kind == EventThinkingEnd && countKind(events, EventThinkingStart) == 0 {
			t.Error("a thinking start must precede its end")
		}
	}
	if countKind(events, EventThinkingEnd) != 1 {
		t.Errorf("got %v, want one thinking end", kinds)
	}
	done := findEvent(t, events, EventDone)
	thinking := done.Message.Content[0].Thinking
	if thinking == nil || thinking.Thinking != "pondering" || thinking.Signature != "sig-9" {
		t.Errorf("the block = %+v, want the thinking and its signature", done.Message.Content[0])
	}
}

func TestASignatureDeltaEmitsNoEventOfItsOwn(t *testing.T) {
	withSignature := runAnthropic(t, anthropicStreamModel(),
		sseFrame(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		sseFrame(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"s"}}`),
		sseFrame(`{"type":"content_block_stop","index":0}`),
	)
	if countKind(withSignature, EventThinkingDelta) != 0 {
		t.Errorf("got %v, want a signature delta to accumulate without emitting", kindsOf(withSignature))
	}
	joined := runAnthropic(t, anthropicStreamModel(),
		sseFrame(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		sseFrame(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"one"}}`),
		sseFrame(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"s1"}}`),
		sseFrame(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"s2"}}`),
		sseFrame(`{"type":"content_block_stop","index":0}`),
	)
	thinking := findEvent(t, joined, EventDone).Message.Content[0].Thinking
	if thinking.Signature != "s1s2" {
		t.Errorf("the signature = %q, want both signature deltas accumulated", thinking.Signature)
	}
}

func TestAThinkingBlockWithNoSignatureCarriesNone(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(),
		sseFrame(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		sseFrame(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"t"}}`),
		sseFrame(`{"type":"content_block_stop","index":0}`),
	)
	thinking := findEvent(t, events, EventDone).Message.Content[0].Thinking
	if thinking.Signature != "" {
		t.Errorf("signature = %q, want none: it is carried only when non-empty", thinking.Signature)
	}
}

func TestABlockIndexIsAssignedFromTheCompletedListAtStart(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(),
		textBlockFrame(0, "first"),
		textBlockFrame(1, "second"),
	)
	starts := []Event{}
	for _, e := range events {
		if e.Kind == EventTextStart {
			starts = append(starts, e)
		}
	}
	if len(starts) != 2 {
		t.Fatalf("got %d text starts, want 2", len(starts))
	}
	if starts[0].ContentIndex != 0 || starts[1].ContentIndex != 1 {
		t.Errorf("starts at %d and %d, want 0 and 1 from the completed list", starts[0].ContentIndex, starts[1].ContentIndex)
	}
	done := findEvent(t, events, EventDone)
	if len(done.Message.Content) != 2 {
		t.Fatalf("got %d blocks, want both texts", len(done.Message.Content))
	}
	if done.Message.Content[0].Text.Text != "first" || done.Message.Content[1].Text.Text != "second" {
		t.Errorf("the blocks = %q, %q", done.Message.Content[0].Text.Text, done.Message.Content[1].Text.Text)
	}
}

func TestTheWireIndexAndTheContentIndexAreDifferentNumbers(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(),
		sseFrame(`{"type":"content_block_start","index":7,"content_block":{"type":"text","text":""}}`),
		sseFrame(`{"type":"content_block_delta","index":7,"delta":{"type":"text_delta","text":"x"}}`),
		sseFrame(`{"type":"content_block_stop","index":7}`),
	)
	start := findEvent(t, events, EventTextStart)
	if start.ContentIndex != 0 {
		t.Errorf("the content index = %d, want 0: the wire index 7 only keys the map", start.ContentIndex)
	}
	delta := findEvent(t, events, EventTextDelta)
	if delta.ContentIndex != 0 {
		t.Errorf("the delta's content index = %d, want the mapped one", delta.ContentIndex)
	}
}

func TestADeltaForAnUnknownBlockIsDropped(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(),
		sseFrame(`{"type":"content_block_delta","index":4,"delta":{"type":"text_delta","text":"orphan"}}`),
		textBlockFrame(0, "kept"),
	)
	if countKind(events, EventTextDelta) != 1 {
		t.Errorf("got %v, want the orphan delta dropped", kindsOf(events))
	}
	done := findEvent(t, events, EventDone)
	if done.Message.Content[0].Text.Text != "kept" {
		t.Errorf("the text = %q, want only the known block", done.Message.Content[0].Text.Text)
	}
}

func TestAnUnmodelledBlockTypeIsSkippedNotAnError(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(),
		sseFrame(`{"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"x"}}`),
		textBlockFrame(1, "kept"),
	)
	if countKind(events, EventTextStart) != 1 {
		t.Errorf("got %v, want the unmodelled block skipped and the text one kept", kindsOf(events))
	}
	if countKind(events, EventError) != 0 {
		t.Error("an unmodelled block is skipped, not an error")
	}
}

func TestAToolUseBlockAssemblesItsArgumentsFromTheDeltas(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(),
		sseFrame(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"Read"}}`),
		sseFrame(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`),
		sseFrame(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"a\"}"}}`),
		sseFrame(`{"type":"content_block_stop","index":0}`),
	)
	start := findEvent(t, events, EventToolCallStart)
	if start.ID != "toolu_1" || start.Name != "Read" {
		t.Errorf("the start = %+v, want the id and name off the block", start)
	}
	end := findEvent(t, events, EventToolCallEnd)
	if end.ToolCall.Arguments != `{"path":"a"}` {
		t.Errorf("the arguments = %q, want the deltas joined", end.ToolCall.Arguments)
	}
}

func TestAToolCallEndCarriesItsPositionInTheFinalArray(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(),
		textBlockFrame(0, "before"),
		sseFrame(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t","name":"f"}}`),
		sseFrame(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`),
		sseFrame(`{"type":"content_block_stop","index":1}`),
	)
	if start := findEvent(t, events, EventToolCallStart); start.ContentIndex != 1 {
		t.Errorf("the start's content index = %d, want 1", start.ContentIndex)
	}
	if end := findEvent(t, events, EventToolCallEnd); end.ContentIndex != 1 {
		t.Errorf("the end's content index = %d, want 1: its position in the completed array", end.ContentIndex)
	}
}

func TestMessageStartCarriesUsageWithoutTakingTheCacheOutOfTheInput(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(),
		sseFrame(`{"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":1,"cache_read_input_tokens":40,"cache_creation_input_tokens":7}}}`),
		textBlockFrame(0, "x"),
	)
	done := findEvent(t, events, EventDone)
	usage := done.Message.Usage
	if usage.InputTokens != 100 {
		t.Errorf("the input = %d, want 100: the cache tokens are not subtracted here", usage.InputTokens)
	}
	if usage.CacheReadTokens != 40 || usage.CacheWriteTokens != 7 {
		t.Errorf("the cache terms = %d, %d, want them kept separate", usage.CacheReadTokens, usage.CacheWriteTokens)
	}
}

func TestTheTotalTokensBackfillAddsOnlyInputAndOutput(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(),
		sseFrame(`{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":99,"cache_creation_input_tokens":99}}}`),
		textBlockFrame(0, "x"),
	)
	usage := findEvent(t, events, EventDone).Message.Usage
	if usage.TotalTokens != 15 {
		t.Errorf("the total = %d, want 10+5: the cache terms are not added here", usage.TotalTokens)
	}
}

func TestAMessageDeltaReassignsTheOutputUnconditionally(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(),
		sseFrame(`{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":5}}}`),
		textBlockFrame(0, "x"),
		sseFrame(`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`),
	)
	usage := findEvent(t, events, EventDone).Message.Usage
	if usage.OutputTokens != 0 {
		t.Errorf("the output = %d, want 0: a message_delta with no usage reassigns it", usage.OutputTokens)
	}
	if usage.InputTokens != 10 {
		t.Errorf("the input = %d, want it untouched", usage.InputTokens)
	}
}

func TestTheStopReasonMappingForThisWire(t *testing.T) {
	cases := map[string]StopReason{
		"max_tokens": StopLength,
		"tool_use":   StopToolUse,
		"end_turn":   StopStop,
		"anything":   StopStop,
	}
	for finish, want := range cases {
		events := runAnthropic(t, anthropicStreamModel(),
			textBlockFrame(0, "x"),
			sseFrame(`{"type":"message_delta","delta":{"stop_reason":"`+finish+`"}}`),
		)
		done := findEvent(t, events, EventDone)
		if done.Message.StopReason != want {
			t.Errorf("%q = %q, want %q", finish, done.Message.StopReason, want)
		}
	}
}

func TestAnEmptyResponseIsAnErrorNotAnEmptyTextBlock(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(), sseFrame(`{"type":"message_stop"}`))
	last := events[len(events)-1]
	if last.Kind != EventError {
		t.Fatalf("the terminal = %+v, want an error", last)
	}
	if last.Reason != "anthropic returned empty response with no content blocks" {
		t.Errorf("the reason = %q", last.Reason)
	}
	if countKind(events, EventDone) != 0 {
		t.Error("there is no terminal message: an empty response fails")
	}
}

func TestTheEmptyResponseErrorNamesTheRawBodyWhenItIsNotAnError(t *testing.T) {
	events := runAnthropicWith(t, anthropicStreamModel(), AnthropicOptions{}, "<html>502</html>")
	last := events[len(events)-1]
	if !strings.HasPrefix(last.Reason, "anthropic: empty response (") {
		t.Errorf("the reason = %q, want the raw byte count", last.Reason)
	}
}

func TestTheEmptyResponseErrorTakesTheMessageFromAnErrorBody(t *testing.T) {
	events := runAnthropicWith(t, anthropicStreamModel(), AnthropicOptions{}, `{"type":"error","error":{"message":"overloaded"}}`)
	last := events[len(events)-1]
	if last.Reason != "overloaded" {
		t.Errorf("the reason = %q, want the body's own message", last.Reason)
	}
}

func TestUnterminatedTextStillBecomesABlock(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(),
		sseFrame(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		sseFrame(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"unfinished"}}`),
	)
	done := findEvent(t, events, EventDone)
	if done.Message.Content[0].Text.Text != "unfinished" {
		t.Errorf("the text = %q, want the accumulated text to become a block", done.Message.Content[0].Text.Text)
	}
	if countKind(events, EventTextEnd) != 0 {
		t.Error("a block that never stopped emits no end event")
	}
}

func TestAnErrorEventEndsTheStreamWithItsMessage(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(),
		sseFrame(`{"type":"error","error":{"type":"overloaded_error","message":"overloaded"}}`),
	)
	last := events[len(events)-1]
	if last.Kind != EventError || last.Reason != "overloaded" {
		t.Errorf("the terminal = %+v, want the error's message", last)
	}
}

func TestAnErrorEventWithNoMessageUsesTheLiteral(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(), sseFrame(`{"type":"error","error":{}}`))
	if events[len(events)-1].Reason != "anthropic api error" {
		t.Errorf("the reason = %q, want the literal default", events[len(events)-1].Reason)
	}
}

func TestAPingEventOnTheWireIsIgnored(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(),
		sseFrame(`{"type":"ping"}`),
		textBlockFrame(0, "x"),
	)
	if countKind(events, EventKeepalive) != 0 {
		t.Error("the parser does not handle ping: it falls through to none")
	}
	if countKind(events, EventDone) != 1 {
		t.Errorf("got %v, want the stream to continue past a ping", kindsOf(events))
	}
}

func TestTheLoopFlushesATrailingEventWithASyntheticBlankLine(t *testing.T) {
	sink := &EventSink{}
	StreamAnthropic(sink, anthropicStreamModel(), Context{}, AnthropicOptions{},
		chunkReader([]string{
			sseFrame(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
			sseFrame(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"tail"}}`),
			"data: " + `{"type":"error","error":{"message":"late failure"}}`,
		}), nil, func() string { return "" })
	events := sink.Drain()
	last := events[len(events)-1]
	if last.Kind != EventError || last.Reason != "late failure" {
		t.Errorf("the terminal = %+v, want the error the trailing frame carried to be found by the tail", last)
	}
}

func TestATrailingNonErrorFrameIsParsedButNotApplied(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(),
		sseFrame(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		sseFrame(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"tail"}}`),
		"data: "+`{"type":"content_block_stop","index":0}`,
	)
	if countKind(events, EventError) != 0 {
		t.Fatalf("got %v, want no failure from an unterminated frame", kindsOf(events))
	}
	if countKind(events, EventTextEnd) != 0 {
		t.Errorf("got %v, want the trailing stop not applied: the tail only inspects an error", kindsOf(events))
	}
	done := findEvent(t, events, EventDone)
	if done.Message.Content[0].Text.Text != "tail" {
		t.Errorf("the text = %q, want the unterminated block to fall through to the accumulator", done.Message.Content[0].Text.Text)
	}
}

func TestTheTailOnlyLooksForAnError(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(),
		textBlockFrame(0, "done"),
		"data: "+`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ignored"}}`,
	)
	if countKind(events, EventTextDelta) != 1 {
		t.Errorf("got %v, want the trailing delta not applied: the tail only inspects an error", kindsOf(events))
	}
	if findEvent(t, events, EventDone).Message.Content[0].Text.Text != "done" {
		t.Error("the completed text stands")
	}
}

func TestAMalformedPayloadIsNeverAnError(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(),
		"data: not json at all\n\n",
		"data: [1,2,3]\n\n",
		"data: {\"nope\":1}\n\n",
		"data: {\"type\":5}\n\n",
		textBlockFrame(0, "x"),
	)
	if countKind(events, EventError) != 0 {
		t.Errorf("got %v, want every malformed payload skipped", kindsOf(events))
	}
	if countKind(events, EventDone) != 1 {
		t.Error("the stream still completes")
	}
}

func TestAnOverlongIndexIsNotAccepted(t *testing.T) {
	events := runAnthropic(t, anthropicStreamModel(),
		sseFrame(`{"type":"content_block_start","index":"0","content_block":{"type":"text","text":""}}`),
		sseFrame(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}`),
		sseFrame(`{"type":"content_block_stop","index":0}`),
	)
	if countKind(events, EventTextStart) != 0 {
		t.Errorf("got %v, want a non-integer index refused", kindsOf(events))
	}
}

func TestCancellationEndsTheAnthropicStreamWithTheCancelsReason(t *testing.T) {
	sink := &EventSink{}
	StreamAnthropic(sink, anthropicStreamModel(), Context{}, AnthropicOptions{},
		func() ([]byte, error) { return nil, nil }, func() bool { return true }, nil)
	events := sink.Drain()
	last := events[len(events)-1]
	if last.Kind != EventError || last.Reason != "request cancelled" {
		t.Errorf("the terminal = %+v, want the cancel reason", last)
	}
}

func TestTheAnthropicKeepaliveFiresOnThePingInterval(t *testing.T) {
	now := int64(0)
	reads := 0
	sink := &EventSink{}
	StreamAnthropic(sink, anthropicStreamModel(), Context{}, AnthropicOptions{
		Now:        func() int64 { return now },
		PingMillis: 50,
	}, func() ([]byte, error) {
		now += 60
		reads++
		if reads > 3 {
			return nil, nil
		}
		return []byte(textBlockFrame(reads-1, "x")), nil
	}, nil, nil)
	if countKind(sink.Drain(), EventKeepalive) == 0 {
		t.Error("want a keepalive once the interval elapsed")
	}
}

func TestCancellationIsRecheckedBetweenBufferedEvents(t *testing.T) {
	sink := &EventSink{}
	seen := 0
	StreamAnthropic(sink, anthropicStreamModel(), Context{}, AnthropicOptions{},
		func() ([]byte, error) {
			frames := sseFrame(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
				sseFrame(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a"}}`) +
				sseFrame(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"b"}}`) +
				sseFrame(`{"type":"content_block_stop","index":0}`)
			return []byte(frames), nil
		}, func() bool {
			seen++
			return seen > 2
		}, nil)
	events := sink.Drain()
	last := events[len(events)-1]
	if last.Kind != EventError || last.Reason != "request cancelled" {
		t.Errorf("the terminal = %+v, want the cancel reason: one buffered chunk carries several events", last)
	}
	if countKind(events, EventTextDelta) > 1 {
		t.Errorf("got %d text deltas, want the drain to stop at the cancel rather than applying the rest", countKind(events, EventTextDelta))
	}
}

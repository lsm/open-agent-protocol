package provider

import (
	"strings"
	"testing"
)

func mustFeed(t *testing.T, p *SSEParser, chunk string) []SSEEvent {
	t.Helper()
	events, err := p.Feed([]byte(chunk))
	if err != nil {
		t.Fatalf("feed %q: %v", chunk, err)
	}
	return events
}

func wantOne(t *testing.T, events []SSEEvent, wantType string, hasType bool, wantData string) {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	got := events[0]
	if got.HasType != hasType || (hasType && got.Type != wantType) {
		t.Errorf("type = %q present=%v, want %q present=%v", got.Type, got.HasType, wantType, hasType)
	}
	if got.Data != wantData {
		t.Errorf("data = %q, want %q", got.Data, wantData)
	}
}

func TestABlankLineSeparatesTheEventAndNothingElseDoes(t *testing.T) {
	parser := NewSSEParser()
	wantOne(t, mustFeed(t, parser, "data: hello world\n\n"), "", false, "hello world")
}

func TestTheBlankLineIsTheSeparatorEvenWithNoBytesBuffered(t *testing.T) {
	parser := NewSSEParser()
	got := mustFeed(t, parser, "data: first\n\n\n\ndata: second\n\n")
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2: the run of blank lines is one separator plus two no-ops", len(got))
	}
	if got[0].Data != "first" || got[1].Data != "second" {
		t.Errorf("data = %q, %q, want first, second", got[0].Data, got[1].Data)
	}
}

func TestNoBlankLineMeansNoEvent(t *testing.T) {
	parser := NewSSEParser()
	if got := mustFeed(t, parser, "data: hello\n"); len(got) != 0 {
		t.Fatalf("got %d events, want 0: a newline finishes the line, not the event", len(got))
	}
	if got := mustFeed(t, parser, "\n"); len(got) != 1 {
		t.Fatalf("got %d events, want 1 once the separator arrives", len(got))
	}
}

func TestRepeatedDataLinesAreJoinedWithANewline(t *testing.T) {
	parser := NewSSEParser()
	wantOne(t, mustFeed(t, parser, "data: first line\ndata: second line\ndata: third line\n\n"), "", false, "first line\nsecond line\nthird line")
}

func TestAnEmptyDataLineStillContributesItsSeparator(t *testing.T) {
	parser := NewSSEParser()
	wantOne(t, mustFeed(t, parser, "data\ndata: value\n\n"), "", false, "\nvalue")
	coloned := mustFeed(t, parser, "data:\ndata: value\n\n")
	if len(coloned) != 1 || coloned[0].Data != "\nvalue" {
		t.Errorf("a colon with nothing after it is a data field too, got %+v", coloned)
	}
}

func TestASoleEmptyDataLineIsStillAnEvent(t *testing.T) {
	parser := NewSSEParser()
	wantOne(t, mustFeed(t, parser, "data\n\n"), "", false, "")
}

func TestAnEventWithNoDataEmitsNothing(t *testing.T) {
	parser := NewSSEParser()
	if got := mustFeed(t, parser, "event: message\n\n"); len(got) != 0 {
		t.Fatalf("got %d events, want 0: an event line alone is not an event", len(got))
	}
}

func TestAnEventWithNoDataLeavesItsTypeForTheNextEvent(t *testing.T) {
	parser := NewSSEParser()
	if got := mustFeed(t, parser, "event: stale\n\n"); len(got) != 0 {
		t.Fatalf("got %d events, want 0", len(got))
	}
	wantOne(t, mustFeed(t, parser, "data: value\n\n"), "stale", true, "value")
}

func TestAColonlessRecognisedFieldHasAPresentEmptyValue(t *testing.T) {
	parser := NewSSEParser()
	wantOne(t, mustFeed(t, parser, "event: stale\nevent\ndata: value\n\n"), "", true, "value")
}

func TestTheTypeIsClearedOnceTheEventIsEmitted(t *testing.T) {
	parser := NewSSEParser()
	wantOne(t, mustFeed(t, parser, "event: update\ndata: a\n\n"), "update", true, "a")
	wantOne(t, mustFeed(t, parser, "data: b\n\n"), "", false, "b")
}

func TestExactlyOneLeadingSpaceIsRemoved(t *testing.T) {
	parser := NewSSEParser()
	wantOne(t, mustFeed(t, parser, "data:no space\n\n"), "", false, "no space")
	wantOne(t, mustFeed(t, parser, "data:  two spaces\n\n"), "", false, " two spaces")
}

func TestOnlyTheFirstColonSplitsFieldFromValue(t *testing.T) {
	parser := NewSSEParser()
	wantOne(t, mustFeed(t, parser, "data: key:value:more\n\n"), "", false, "key:value:more")
}

func TestCommentsAndUnknownFieldsAreDropped(t *testing.T) {
	parser := NewSSEParser()
	wantOne(t, mustFeed(t, parser, ": this is a comment\ndata: actual data\n: another comment\n\n"), "", false, "actual data")
	parser = NewSSEParser()
	wantOne(t, mustFeed(t, parser, "event: update\ndata: first\ndata\ndata: third\nunknown\nid: 7\nretry: 10\n\n"), "update", true, "first\n\nthird")
}

func TestACarriageReturnAndALineFeedAreOneDelimiterAcrossAChunkBoundary(t *testing.T) {
	parser := NewSSEParser()
	for _, chunk := range []string{"event: update\r", "\ndata: first\r", "\ndata\r", "\ndata: third\r", "\nunknown\r"} {
		if got := mustFeed(t, parser, chunk); len(got) != 0 {
			t.Fatalf("feed %q produced %d events, want 0 before the separator", chunk, len(got))
		}
	}
	wantOne(t, mustFeed(t, parser, "\n\r"), "update", true, "first\n\nthird")
	if got := mustFeed(t, parser, "\n"); len(got) != 0 {
		t.Errorf("the swallowed line feed is not a second delimiter: got %d events", len(got))
	}
}

func TestALoneCarriageReturnEndsALine(t *testing.T) {
	parser := NewSSEParser()
	wantOne(t, mustFeed(t, parser, "event: update\rdata: first\rdata\rdata: third\runknown\r\r"), "update", true, "first\n\nthird")
}

func TestFramingIsIndependentOfWhereTheChunksSplit(t *testing.T) {
	const fixture = "event: update\ndata: first\ndata\ndata: third\nunknown\n\n"
	for split := 0; split <= len(fixture); split++ {
		parser := NewSSEParser()
		seen := 0
		collect := func(events []SSEEvent) {
			for _, event := range events {
				seen++
				if event.Type != "update" || !event.HasType {
					t.Fatalf("split %d: type = %q present=%v", split, event.Type, event.HasType)
				}
				if event.Data != "first\n\nthird" {
					t.Fatalf("split %d: data = %q", split, event.Data)
				}
			}
		}
		collect(mustFeed(t, parser, fixture[:split]))
		collect(mustFeed(t, parser, fixture[split:]))
		if seen != 1 {
			t.Fatalf("split %d produced %d events, want exactly 1", split, seen)
		}
	}
}

func TestOneByteAtATimePreservesTheEvents(t *testing.T) {
	const input = "event: message\ndata: {\"delta\":\"a\"}\n\ndata: [DONE]\n\n"
	parser := NewSSEParser()
	var seen int
	for i := 0; i < len(input); i++ {
		for _, event := range mustFeed(t, parser, input[i:i+1]) {
			seen++
			switch seen {
			case 1:
				if event.Type != "message" || !event.HasType || event.Data != "{\"delta\":\"a\"}" {
					t.Fatalf("first event = %+v", event)
				}
			case 2:
				if event.HasType || event.Data != "[DONE]" {
					t.Fatalf("second event = %+v, want no type and the done sentinel", event)
				}
			}
		}
	}
	if seen != 2 {
		t.Fatalf("saw %d events, want 2", seen)
	}
}

func TestALineOfExactlyTheLimitIsAcceptedAndOneByteOverIsRefused(t *testing.T) {
	parser := NewSSEParserWithLimits(SSELimits{LineBytes: 4, EventBytes: 32})
	if _, err := parser.Feed([]byte("abcd")); err != nil {
		t.Fatalf("a line of exactly the limit is within it: %v", err)
	}
	if _, err := parser.Feed([]byte("e")); err != ErrLineTooLarge {
		t.Fatalf("err = %v, want ErrLineTooLarge", err)
	}
}

func TestTheEventLimitCountsTheTypeAndTheData(t *testing.T) {
	parser := NewSSEParserWithLimits(SSELimits{LineBytes: 32, EventBytes: 8})
	if _, err := parser.Feed([]byte("event: type\n")); err != nil {
		t.Fatalf("a type within the limit alone is fine: %v", err)
	}
	if _, err := parser.Feed([]byte("data: value\n")); err != ErrEventTooLarge {
		t.Fatalf("err = %v, want ErrEventTooLarge: five data bytes do not fit in four", err)
	}
}

func TestTheEventLimitCountsTheSeparatorBetweenDataLines(t *testing.T) {
	parser := NewSSEParserWithLimits(SSELimits{LineBytes: 64, EventBytes: 3})
	if _, err := parser.Feed([]byte("data: xxx\n")); err != nil {
		t.Fatalf("three data bytes fill the budget exactly: %v", err)
	}
	if _, err := parser.Feed([]byte("data: x\n")); err != ErrEventTooLarge {
		t.Fatalf("err = %v, want ErrEventTooLarge: the separator alone needs a fourth byte", err)
	}
	parser = NewSSEParserWithLimits(SSELimits{LineBytes: 64, EventBytes: 3})
	if _, err := parser.Feed([]byte("data: xx\ndata: x\n")); err != ErrEventTooLarge {
		t.Fatalf("err = %v, want ErrEventTooLarge: two bytes and a separator do not fit in three", err)
	}
}

func TestATypeArrivingAfterDataIsMeasuredAgainstWhatIsLeft(t *testing.T) {
	parser := NewSSEParserWithLimits(SSELimits{LineBytes: 64, EventBytes: 8})
	if _, err := parser.Feed([]byte("data: 1234\n")); err != nil {
		t.Fatalf("four data bytes fit: %v", err)
	}
	if _, err := parser.Feed([]byte("event: type\n")); err != nil {
		t.Fatalf("a four byte type fills the eight byte budget exactly beside four bytes of data: %v", err)
	}
	over := NewSSEParserWithLimits(SSELimits{LineBytes: 64, EventBytes: 8})
	if _, err := over.Feed([]byte("data: 1234\n")); err != nil {
		t.Fatalf("four data bytes fit: %v", err)
	}
	if _, err := over.Feed([]byte("event: types\n")); err != ErrEventTooLarge {
		t.Fatalf("err = %v, want ErrEventTooLarge: a five byte type does not fit beside four bytes of data", err)
	}
}

func TestResetDropsTheEventInProgressAndRecoversAfterARefusal(t *testing.T) {
	parser := NewSSEParser()
	mustFeed(t, parser, "event: test\ndata: partial")
	parser.Reset()
	wantOne(t, mustFeed(t, parser, "data: complete\n\n"), "", false, "complete")

	limited := NewSSEParserWithLimits(SSELimits{LineBytes: 9, EventBytes: 3})
	wantOne(t, mustFeed(t, limited, "data: ok\n\n"), "", false, "ok")
	if _, err := limited.Feed([]byte("1234567890")); err != ErrLineTooLarge {
		t.Fatalf("err = %v, want ErrLineTooLarge", err)
	}
	limited.Reset()
	if _, err := limited.Feed([]byte("data: xx\ndata: x\n")); err != ErrEventTooLarge {
		t.Fatalf("err = %v, want ErrEventTooLarge", err)
	}
	limited.Reset()
	wantOne(t, mustFeed(t, limited, "data: ok\n\n"), "", false, "ok")
}

func TestAnEventFromAnEarlierFeedIsNotOverwrittenByTheNext(t *testing.T) {
	parser := NewSSEParser()
	first := mustFeed(t, parser, "data: a\n\n")
	wantOne(t, first, "", false, "a")
	mustFeed(t, parser, "data: b\n\ndata: c\n\n")
	wantOne(t, first, "", false, "a")
}

func TestSeveralEventsInOneChunkArriveInOrder(t *testing.T) {
	parser := NewSSEParser()
	got := mustFeed(t, parser, "data: first\n\ndata: second\n\ndata: third\n\n")
	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d", len(got), len(want))
	}
	for i, event := range got {
		if event.Data != want[i] {
			t.Errorf("event %d data = %q, want %q", i, event.Data, want[i])
		}
	}
}

func TestTheLimitNamesSurviveIntoTheErrorMessage(t *testing.T) {
	if got := SSEErrorMessage(ErrLineTooLarge); got != "sse line too large" {
		t.Errorf("got %q, want the line spelling", got)
	}
	if got := SSEErrorMessage(ErrEventTooLarge); got != "sse event too large" {
		t.Errorf("got %q, want the event spelling", got)
	}
	if got := SSEErrorMessage(ErrSSEParse); got != "sse parse error" {
		t.Errorf("got %q, want the catch-all spelling", got)
	}
}

func TestTheParserHoldsAProviderErrorEventWhole(t *testing.T) {
	parser := NewSSEParser()
	wantOne(t, mustFeed(t, parser, "event: error\ndata: {\"error\":{\"type\":\"invalid_request_error\",\"message\":\"bad input\"}}\n\n"), "error", true, "{\"error\":{\"type\":\"invalid_request_error\",\"message\":\"bad input\"}}")
}

func TestTheStandardLimitsAreTheOnesTheDefaultParserUses(t *testing.T) {
	parser := NewSSEParser()
	if parser.limits.LineBytes != 1024*1024 || parser.limits.EventBytes != 4*1024*1024 {
		t.Fatalf("limits = %+v, want a mebibyte per line and four per event", parser.limits)
	}
	if _, err := parser.Feed([]byte(strings.Repeat("x", 1024*1024))); err != nil {
		t.Fatalf("a mebibyte line is within the limit: %v", err)
	}
}

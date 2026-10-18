package adapter

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

func userItem(offset uint64, text string) TranscriptItem {
	return TranscriptItem{Offset: offset, AtMS: 1000, Message: &protocol.Message{Role: protocol.RoleUser, Content: protocol.TextContent(text)}}
}

func TestATranscriptPageHoldsItsLimitAndItsCursorReadsOnFromTheEntryAfterTheLast(t *testing.T) {
	items := []TranscriptItem{userItem(0, "one"), userItem(40, "two"), {Offset: 80, Compaction: "kept"}}
	first, err := TranscriptPage(TranscriptRequest{NativeID: "s1", Limit: 2}, items)
	if err != nil || len(first.Entries) != 2 || first.Complete || first.Entries[0].EntryID != "e0" || first.Entries[0].Kind != protocol.TranscriptMessage {
		t.Fatalf("first page %+v, %v", first, err)
	}
	second, err := TranscriptPage(TranscriptRequest{NativeID: "s1", Limit: 100, Cursor: first.NextCursor}, items)
	if err != nil || !second.Complete || second.Gap != nil || len(second.Entries) != 1 || second.Entries[0].Compaction.Summary != "kept" {
		t.Fatalf("second page %+v, %v", second, err)
	}
	caughtUp, err := TranscriptPage(TranscriptRequest{NativeID: "s1", Limit: 100, Cursor: second.NextCursor}, items)
	if err != nil || len(caughtUp.Entries) != 0 || !caughtUp.Complete || caughtUp.NextCursor != second.NextCursor {
		t.Fatalf("a caught-up read %+v, %v", caughtUp, err)
	}
	if TranscriptTail("s1", items) != second.NextCursor {
		t.Fatalf("the tail cursor is not the one after the newest entry")
	}
}

func TestACursorWhoseEntryIsGoneResumesAtTheOldestEntryPrunedOrRewritten(t *testing.T) {
	read, _ := TranscriptPage(TranscriptRequest{NativeID: "s1", Limit: 2}, []TranscriptItem{userItem(0, "one"), userItem(40, "two"), userItem(80, "three")})
	pruned, err := TranscriptPage(TranscriptRequest{NativeID: "s1", Limit: 100, Cursor: read.NextCursor}, []TranscriptItem{userItem(120, "four")})
	if err != nil || pruned.Gap == nil || pruned.Gap.Reason != protocol.TranscriptGapPruned || pruned.Gap.ResumedAt != "e120" || pruned.Entries[0].EntryID != "e120" {
		t.Fatalf("after a prune %+v, %v", pruned, err)
	}
	rewritten, err := TranscriptPage(TranscriptRequest{NativeID: "s1", Limit: 100, Cursor: read.NextCursor}, []TranscriptItem{userItem(0, "one"), userItem(50, "two, rewritten")})
	if err != nil || rewritten.Gap == nil || rewritten.Gap.Reason != protocol.TranscriptGapRewritten || rewritten.Gap.ResumedAt != "e0" || len(rewritten.Entries) != 2 {
		t.Fatalf("after a rewrite %+v, %v", rewritten, err)
	}
	empty, err := TranscriptPage(TranscriptRequest{NativeID: "s1", Limit: 100, Cursor: read.NextCursor}, nil)
	if err != nil || empty.Gap == nil || empty.Gap.Reason != protocol.TranscriptGapPruned || empty.Gap.ResumedAt != "" || !empty.Complete {
		t.Fatalf("an emptied store %+v, %v", empty, err)
	}
}

func TestACursorThatDoesNotDecodeOrNamesAnotherSessionIsInvalidNeverAGap(t *testing.T) {
	items := []TranscriptItem{userItem(0, "one"), userItem(40, "two")}
	read, _ := TranscriptPage(TranscriptRequest{NativeID: "s1", Limit: 1}, items)
	for _, cursor := range []string{read.NextCursor, "not a cursor!", "Mgpz"} {
		nativeID := "s1"
		if cursor == read.NextCursor {
			nativeID = "s2"
		}
		if _, err := TranscriptPage(TranscriptRequest{NativeID: nativeID, Limit: 100, Cursor: cursor}, items); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("cursor %q for %s answered %v", cursor, nativeID, err)
		}
	}
}

func TestAnEmptyTranscriptMintsACursorThatReadsFromTheStartOnceEntriesArrive(t *testing.T) {
	empty, err := TranscriptPage(TranscriptRequest{NativeID: "s1", Limit: 100}, nil)
	if err != nil || !empty.Complete || empty.NextCursor == "" {
		t.Fatalf("an empty transcript %+v, %v", empty, err)
	}
	arrived, err := TranscriptPage(TranscriptRequest{NativeID: "s1", Limit: 100, Cursor: empty.NextCursor}, []TranscriptItem{userItem(0, "one")})
	if err != nil || arrived.Gap != nil || len(arrived.Entries) != 1 || arrived.Entries[0].EntryID != "e0" {
		t.Fatalf("entries after an empty cursor %+v, %v", arrived, err)
	}
}

func TestAPageStopsBeforeItsByteBoundAndAnOversizeEntryComesAloneCutOnACodePoint(t *testing.T) {
	half := strings.Repeat("x", protocol.TranscriptPageBytes/2)
	huge := []byte(strings.Repeat("y", protocol.TranscriptPageBytes+2))
	copy(huge[protocol.TranscriptPageBytes/2-1:], "é")
	items := []TranscriptItem{userItem(0, half), userItem(1, half), userItem(2, string(huge))}
	first, _ := TranscriptPage(TranscriptRequest{NativeID: "s1", Limit: 100}, items)
	if len(first.Entries) != 1 || first.Complete || first.Entries[0].Truncated {
		t.Fatalf("the first page held %d entries", len(first.Entries))
	}
	second, _ := TranscriptPage(TranscriptRequest{NativeID: "s1", Limit: 100, Cursor: first.NextCursor}, items)
	third, _ := TranscriptPage(TranscriptRequest{NativeID: "s1", Limit: 100, Cursor: second.NextCursor}, items)
	if len(second.Entries) != 1 || len(third.Entries) != 1 || !third.Complete || !third.Entries[0].Truncated {
		t.Fatalf("pages held %d and %d entries", len(second.Entries), len(third.Entries))
	}
	text, _ := third.Entries[0].Message.Content.Text()
	if len(text) >= protocol.TranscriptPageBytes || !utf8.ValidString(text) {
		t.Fatalf("the cut text is %d bytes, valid %v", len(text), utf8.ValidString(text))
	}
}

func TestAnEntryWhoseTextEscapesToSeveralTimesItsLengthIsCutUntilItsEncodingFits(t *testing.T) {
	controls := strings.Repeat("\x01", protocol.TranscriptPageBytes)
	page, _ := TranscriptPage(TranscriptRequest{NativeID: "s1", Limit: 100}, []TranscriptItem{userItem(0, controls)})
	encoded, _ := json.Marshal(page.Entries[0])
	if !page.Entries[0].Truncated || len(encoded) > protocol.TranscriptPageBytes {
		t.Fatalf("the cut entry encodes to %d bytes", len(encoded))
	}
}

func TestAnOversizeEntryCutsOnlyItsTextBearingFieldsAndNeverANegativeTime(t *testing.T) {
	long := strings.Repeat("z", protocol.TranscriptPageBytes)
	parts := []protocol.ContentPart{{Type: protocol.ContentText, Text: long}}
	for range 40000 {
		parts = append(parts, protocol.ContentPart{Type: protocol.ContentToolCall, ToolCallID: "call-identifier", Name: "a_tool_name", ArgumentsJSON: json.RawMessage(`"raw arguments"`)})
	}
	item := TranscriptItem{Offset: 0, AtMS: -5, Message: &protocol.Message{Role: protocol.RoleAssistant, Content: protocol.PartsContent(parts)}}
	page, _ := TranscriptPage(TranscriptRequest{NativeID: "s1", Limit: 100}, []TranscriptItem{item})
	entry := page.Entries[0]
	if !entry.Truncated || entry.AtMS != 0 {
		t.Fatalf("truncated %v at %d", entry.Truncated, entry.AtMS)
	}
	cut, _ := entry.Message.Content.Parts()
	call := cut[len(cut)-1]
	if call.Type != protocol.ContentToolCall || call.ToolCallID != "call-identifier" || call.Name != "a_tool_name" {
		t.Fatalf("a structural field was cut: %+v", call)
	}
	if arguments := string(call.ArgumentsJSON); strings.HasPrefix(arguments, `"\"`) {
		t.Fatalf("arguments were cut from their quoted encoding: %s", arguments)
	}
}

package adapter

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

type TranscriptItem struct {
	Offset     uint64
	AtMS       int64
	Message    *protocol.Message
	Compaction string
}

func TranscriptPage(request TranscriptRequest, items []TranscriptItem) (protocol.TranscriptLoadResponse, error) {
	ids := transcriptIDs(items)
	start := 0
	var gap *protocol.TranscriptGap
	if request.Cursor != "" {
		entry, offset, err := decodeTranscriptCursor(request.NativeID, request.Cursor)
		if err != nil {
			return protocol.TranscriptLoadResponse{}, err
		}
		switch found := indexOfID(ids, entry); {
		case entry == "":
		case found >= 0:
			start = found + 1
		default:
			gap = &protocol.TranscriptGap{Reason: protocol.TranscriptGapRewritten}
			if len(items) == 0 || items[0].Offset > offset {
				gap.Reason = protocol.TranscriptGapPruned
			}
			if len(items) > 0 {
				gap.ResumedAt = ids[0]
			}
		}
	}
	limit := max(request.Limit, 1)
	entries := []protocol.TranscriptEntry{}
	bytes, next := 0, start
	for ; next < len(items) && len(entries) < limit; next++ {
		entry, size := transcriptEntry(ids[next], items[next], transcriptCut{bound: -1})
		if bytes+size > protocol.TranscriptPageBytes {
			if len(entries) > 0 {
				break
			}
			entry, size = fitTranscriptEntry(ids[next], items[next])
		}
		bytes += size
		entries = append(entries, entry)
	}
	page := protocol.TranscriptLoadResponse{Entries: entries, Complete: next >= len(items), Gap: gap}
	switch {
	case next > start:
		page.NextCursor = encodeTranscriptCursor(request.NativeID, ids[next-1], items[next-1].Offset)
	case start > 0:
		page.NextCursor = encodeTranscriptCursor(request.NativeID, ids[start-1], items[start-1].Offset)
	default:
		page.NextCursor = encodeTranscriptCursor(request.NativeID, "", 0)
	}
	return page, nil
}

func TranscriptTail(nativeID string, items []TranscriptItem) string {
	if len(items) == 0 {
		return encodeTranscriptCursor(nativeID, "", 0)
	}
	ids := transcriptIDs(items)
	return encodeTranscriptCursor(nativeID, ids[len(ids)-1], items[len(items)-1].Offset)
}

func transcriptIDs(items []TranscriptItem) []protocol.TranscriptEntryID {
	ids := make([]protocol.TranscriptEntryID, len(items))
	for index, item := range items {
		repeat := 0
		for repeat < index && items[index-repeat-1].Offset == item.Offset {
			repeat++
		}
		id := "e" + strconv.FormatUint(item.Offset, 10)
		if repeat > 0 {
			id += "." + strconv.Itoa(repeat)
		}
		ids[index] = protocol.TranscriptEntryID(id)
	}
	return ids
}

func indexOfID(ids []protocol.TranscriptEntryID, wanted protocol.TranscriptEntryID) int {
	for index, id := range ids {
		if id == wanted {
			return index
		}
	}
	return -1
}

func encodeTranscriptCursor(nativeID string, entry protocol.TranscriptEntryID, offset uint64) string {
	return base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, "1\n%s\n%s\n%d", nativeID, entry, offset))
}

func decodeTranscriptCursor(nativeID, cursor string) (protocol.TranscriptEntryID, uint64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", 0, ErrInvalidCursor
	}
	fields := strings.Split(string(raw), "\n")
	if len(fields) != 4 || fields[0] != "1" || fields[1] != nativeID {
		return "", 0, ErrInvalidCursor
	}
	offset, err := strconv.ParseUint(fields[3], 10, 64)
	if err != nil {
		return "", 0, ErrInvalidCursor
	}
	return protocol.TranscriptEntryID(fields[2]), offset, nil
}

type transcriptCut struct {
	bound  int
	reduce bool
	keep   int
}

func fitTranscriptEntry(id protocol.TranscriptEntryID, item TranscriptItem) (protocol.TranscriptEntry, int) {
	parts := transcriptParts(item)
	cut := transcriptCut{bound: protocol.TranscriptPageBytes / (parts * 2), keep: parts}
	entry, size := transcriptEntry(id, item, cut)
	for size > protocol.TranscriptPageBytes && cut.bound > 0 {
		cut.bound /= 2
		entry, size = transcriptEntry(id, item, cut)
	}
	if size > protocol.TranscriptPageBytes {
		cut.reduce = true
		entry, size = transcriptEntry(id, item, cut)
	}
	for size > protocol.TranscriptPageBytes && cut.keep > 1 {
		cut.keep /= 2
		entry, size = transcriptEntry(id, item, cut)
	}
	return entry, size
}

func transcriptParts(item TranscriptItem) int {
	if item.Message == nil {
		return 1
	}
	var parts []json.RawMessage
	if json.Unmarshal(item.Message.Content, &parts) != nil {
		return 1
	}
	return max(len(parts), 1)
}

func transcriptEntry(id protocol.TranscriptEntryID, item TranscriptItem, cut transcriptCut) (protocol.TranscriptEntry, int) {
	entry := protocol.TranscriptEntry{EntryID: id, Kind: protocol.TranscriptMessage, AtMS: max(item.AtMS, 0), Truncated: cut.bound >= 0}
	if item.Message != nil {
		message := *item.Message
		if cut.bound >= 0 {
			message.Content = cutContent(message.Content, cut)
		}
		entry.Message = &message
	} else {
		entry.Kind = protocol.TranscriptCompaction
		entry.Compaction = &protocol.TranscriptCompactionEntry{Summary: cutText(item.Compaction, cut.bound)}
	}
	encoded, _ := json.Marshal(entry)
	return entry, len(encoded)
}

func cutContent(content protocol.MessageContent, cut transcriptCut) protocol.MessageContent {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return protocol.TextContent(cutText(text, cut.bound))
	}
	var parts []json.RawMessage
	if json.Unmarshal(content, &parts) != nil {
		return content
	}
	kept := make([]json.RawMessage, 0, min(len(parts), cut.keep))
	for _, part := range parts[:min(len(parts), cut.keep)] {
		kept = append(kept, cutPart(part, cut))
	}
	encoded, _ := json.Marshal(kept)
	return encoded
}

func cutPart(part json.RawMessage, cut transcriptCut) json.RawMessage {
	var members map[string]json.RawMessage
	if json.Unmarshal(part, &members) != nil {
		return part
	}
	var kind string
	_ = json.Unmarshal(members["type"], &kind)
	if kind == "image" || (cut.reduce && kind != "text" && kind != "reasoning") {
		encoded, _ := json.Marshal(map[string]string{"type": "text", "text": kind})
		return encoded
	}
	changed := false
	for key, member := range members {
		if !cutField[key] {
			continue
		}
		var text string
		if json.Unmarshal(member, &text) != nil {
			text = string(member)
		}
		if cutted := cutText(text, cut.bound); cutted != text {
			members[key], _ = json.Marshal(cutted)
			changed = true
		}
	}
	if !changed {
		return part
	}
	encoded, _ := json.Marshal(members)
	return encoded
}

var cutField = map[string]bool{"text": true, "reasoning": true, "result": true, "arguments_json": true}

func cutText(text string, bound int) string {
	if bound < 0 || len(text) <= bound {
		return text
	}
	at := bound
	for at > 0 && !utf8.RuneStart(text[at]) {
		at--
	}
	return text[:at]
}

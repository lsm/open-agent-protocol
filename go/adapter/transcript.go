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
		entry, size := transcriptEntry(ids[next], items[next], -1)
		if bytes+size > protocol.TranscriptPageBytes {
			if len(entries) > 0 {
				break
			}
			bound := protocol.TranscriptPageBytes / (transcriptParts(items[next]) * 2)
			for entry, size = transcriptEntry(ids[next], items[next], bound); size > protocol.TranscriptPageBytes && bound > 0; entry, size = transcriptEntry(ids[next], items[next], bound) {
				bound /= 2
			}
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

func transcriptEntry(id protocol.TranscriptEntryID, item TranscriptItem, bound int) (protocol.TranscriptEntry, int) {
	entry := protocol.TranscriptEntry{EntryID: id, Kind: protocol.TranscriptMessage, AtMS: max(item.AtMS, 0), Truncated: bound >= 0}
	if item.Message != nil {
		message := *item.Message
		if bound >= 0 {
			var content any
			if json.Unmarshal(message.Content, &content) == nil {
				message.Content, _ = json.Marshal(cutTranscript(content, bound))
			}
		}
		entry.Message = &message
	} else {
		entry.Kind = protocol.TranscriptCompaction
		entry.Compaction = &protocol.TranscriptCompactionEntry{Summary: cutText(item.Compaction, bound)}
	}
	encoded, _ := json.Marshal(entry)
	return entry, len(encoded)
}

func cutTranscript(value any, bound int) any {
	switch held := value.(type) {
	case string:
		return cutText(held, bound)
	case []any:
		out := make([]any, len(held))
		for index, item := range held {
			out[index] = cutTranscript(item, bound)
		}
		return out
	case map[string]any:
		kind, _ := held["type"].(string)
		if kind == "image" {
			return map[string]any{"type": "text", "text": "image"}
		}
		out := make(map[string]any, len(held))
		for key, member := range held {
			switch text, isText := member.(string); {
			case !cutField[key]:
				out[key] = member
			case isText:
				out[key] = cutText(text, bound)
			default:
				encoded, _ := json.Marshal(member)
				out[key] = cutText(string(encoded), bound)
			}
		}
		return out
	}
	return value
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

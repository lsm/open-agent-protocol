package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
)

const nativeReadLimit = 64 * 1024 * 1024

var claudeWrappers = []string{"command-name", "command-message", "command-args", "local-command-stdout", "local-command-stderr", "local-command-caveat", "system-reminder", "bash-input", "bash-stdout", "bash-stderr"}

func (a *Adapter) NativeRead(_ context.Context, request base.NativeReadRequest) ([]base.NativeTurn, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil
	}
	directory := request.Directory
	if directory == "" {
		directory = a.config.WorkingDirectory
	}
	return readSession(home, directory, request.NativeID), nil
}

func readSession(home, directory, nativeID string) []base.NativeTurn {
	if home == "" || !safeSessionID(nativeID) {
		return nil
	}
	name := nativeID + ".jsonl"
	projects := filepath.Join(home, ".claude", "projects")
	if directory != "" {
		if data, ok := transcriptSnapshot(filepath.Join(projects, projectDirName(directory), name)); ok {
			return transcriptTurns(data)
		}
	}
	entries, err := os.ReadDir(projects)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if data, ok := transcriptSnapshot(filepath.Join(projects, entry.Name(), name)); ok {
			return transcriptTurns(data)
		}
	}
	return nil
}

func safeSessionID(nativeID string) bool {
	if nativeID == "" {
		return false
	}
	for i := 0; i < len(nativeID); i++ {
		b := nativeID[i]
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_') {
			return false
		}
	}
	return true
}

func transcriptSnapshot(path string) ([]byte, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, false
	}
	start := int64(0)
	if info.Size() > nativeReadLimit {
		start = info.Size() - nativeReadLimit
	}
	whole := make([]byte, info.Size()-start)
	got, err := file.ReadAt(whole, start)
	if err != nil && err != io.EOF {
		return nil, false
	}
	whole = whole[:got]
	if start > 0 {
		if at := bytes.IndexByte(whole, '\n'); at >= 0 {
			whole = whole[at:]
		} else {
			whole = whole[len(whole):]
		}
	}
	end := bytes.LastIndexByte(whole, '\n')
	if end < 0 {
		end = 0
	}
	return whole[:end], true
}

func transcriptTurns(data []byte) []base.NativeTurn {
	var found []base.NativeTurn
	var reply strings.Builder
	replyID := ""
	var replyAt int64
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var entry map[string]json.RawMessage
		if json.Unmarshal(line, &entry) != nil {
			continue
		}
		var kind string
		if json.Unmarshal(entry["type"], &kind) != nil {
			continue
		}
		if rawFlag(entry["isSidechain"]) || rawFlag(entry["isMeta"]) {
			continue
		}
		var message map[string]json.RawMessage
		if json.Unmarshal(entry["message"], &message) != nil || message == nil {
			continue
		}
		at := isoMillis(rawText(entry["timestamp"]))
		switch kind {
		case "user":
			said, ok := userText(message["content"])
			if !ok {
				continue
			}
			if reply.Len() > 0 {
				found = append(found, base.NativeTurn{Role: "assistant", Text: reply.String(), AtMS: replyAt})
			}
			reply.Reset()
			replyID = ""
			found = append(found, base.NativeTurn{Role: "user", Text: said, AtMS: at})
		case "assistant":
			id := rawText(message["id"])
			piece := assistantText(message["content"])
			if piece == "" {
				continue
			}
			if id != replyID || id == "" {
				reply.Reset()
			}
			reply.WriteString(piece)
			replyID, replyAt = id, at
		}
	}
	if reply.Len() > 0 {
		found = append(found, base.NativeTurn{Role: "assistant", Text: reply.String(), AtMS: replyAt})
	}
	return found
}

func rawFlag(raw json.RawMessage) bool {
	var flag bool
	return json.Unmarshal(raw, &flag) == nil && flag
}

func isoMillis(text string) int64 {
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return 0
	}
	return parsed.UnixMilli()
}

func userText(content json.RawMessage) (string, bool) {
	var joined strings.Builder
	var plain string
	if json.Unmarshal(content, &plain) == nil {
		joined.WriteString(plain)
	} else {
		var parts []json.RawMessage
		if json.Unmarshal(content, &parts) != nil {
			return "", false
		}
		for _, raw := range parts {
			var part map[string]json.RawMessage
			if json.Unmarshal(raw, &part) != nil || rawText(part["type"]) != "text" {
				continue
			}
			var piece string
			if json.Unmarshal(part["text"], &piece) != nil {
				continue
			}
			if joined.Len() > 0 {
				joined.WriteByte('\n')
			}
			joined.WriteString(piece)
		}
	}
	said := withoutWrappers(joined.String())
	return said, said != ""
}

func withoutWrappers(said string) string {
	rest := strings.Trim(said, " \t\r\n")
outer:
	for rest != "" && rest[0] == '<' {
		for _, name := range claudeWrappers {
			if !strings.HasPrefix(rest[1:], name+">") {
				continue
			}
			closing := "</" + name + ">"
			at := strings.Index(rest, closing)
			if at < 0 {
				return rest
			}
			rest = strings.Trim(rest[at+len(closing):], " \t\r\n")
			continue outer
		}
		break
	}
	return rest
}

func assistantText(content json.RawMessage) string {
	var parts []json.RawMessage
	if json.Unmarshal(content, &parts) != nil {
		var plain string
		_ = json.Unmarshal(content, &plain)
		return plain
	}
	var joined strings.Builder
	for _, raw := range parts {
		var part map[string]json.RawMessage
		if json.Unmarshal(raw, &part) != nil || rawText(part["type"]) != "text" {
			continue
		}
		joined.WriteString(rawText(part["text"]))
	}
	return joined.String()
}

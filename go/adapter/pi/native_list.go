package pi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	base "github.com/lsm/open-agent-protocol/go/adapter"
)

const (
	nativeHeadBytes  = 256 * 1024
	nativeReadLimit  = 64 * 1024 * 1024
	nativeTitleLimit = 120
)

func storeDirName(directory string) string {
	trimmed := strings.TrimLeft(directory, `/\`)
	return "--" + strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' {
			return '-'
		}
		return r
	}, trimmed) + "--"
}

func agentDir(environment []string, home string) string {
	const prefix = "PI_CODING_AGENT_DIR="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) && len(entry) > len(prefix) {
			return entry[len(prefix):]
		}
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".pi", "agent")
}

func (a *Adapter) NativeList(_ context.Context, request base.NativeListRequest) ([]base.NativeListing, error) {
	directory := request.Directory
	if directory == "" {
		directory = a.config.WorkingDirectory
	}
	home, _ := os.UserHomeDir()
	return listSessions(agentDir(a.config.Environment, home), directory, request.Limit), nil
}

func (a *Adapter) NativeRead(_ context.Context, request base.NativeReadRequest) ([]base.NativeTurn, error) {
	return readSession(request.NativeID), nil
}

func listSessions(agent, directory string, limit int) []base.NativeListing {
	if agent == "" || directory == "" {
		return nil
	}
	store := filepath.Join(agent, "sessions", storeDirName(directory))
	entries, err := os.ReadDir(store)
	if err != nil {
		return nil
	}
	var found []base.NativeListing
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(store, entry.Name())
		head, ok := readHead(path)
		if !ok {
			continue
		}
		header, ok := firstLine(head)
		if !ok || textOf(header["type"]) != "session" {
			continue
		}
		id, ok := header["id"].(string)
		if !ok {
			continue
		}
		cwd, ok := header["cwd"].(string)
		if !ok {
			cwd = directory
		}
		var updated int64
		if info, err := entry.Info(); err == nil {
			updated = info.ModTime().UnixMilli()
		}
		found = append(found, base.NativeListing{NativeID: bindingText(id, path), Title: titleOf(head), Directory: cwd, UpdatedAtMS: updated})
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].UpdatedAtMS > found[j].UpdatedAtMS })
	if limit >= 0 && len(found) > limit {
		found = found[:limit]
	}
	return found
}

func bindingText(id, path string) string {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(sessionBinding{SessionID: id, SessionFile: path})
	return strings.TrimSuffix(out.String(), "\n")
}

func readHead(path string) ([]byte, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	head, err := io.ReadAll(io.LimitReader(file, nativeHeadBytes))
	return head, err == nil
}

func firstLine(data []byte) (map[string]any, bool) {
	line, _, _ := bytes.Cut(data, []byte{'\n'})
	var object map[string]any
	if json.Unmarshal(line, &object) != nil || object == nil {
		return nil, false
	}
	return object, true
}

func titleOf(head []byte) string {
	var named, firstUser string
	var haveNamed, haveUser bool
	scanner := bufio.NewScanner(bytes.NewReader(head))
	scanner.Buffer(make([]byte, 0, 64*1024), nativeHeadBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.Contains(line, []byte(`"session_info"`)) && (haveUser || !bytes.Contains(line, []byte(`"user"`))) {
			continue
		}
		var object map[string]any
		if json.Unmarshal(line, &object) != nil || object == nil {
			continue
		}
		switch textOf(object["type"]) {
		case "session_info":
			if name := textOf(object["name"]); name != "" {
				named, haveNamed = name, true
			}
		case "message":
			if haveUser {
				continue
			}
			message, ok := object["message"].(map[string]any)
			if !ok || textOf(message["role"]) != "user" {
				continue
			}
			firstUser, haveUser = textParts(message["content"], "\n"), true
		}
	}
	chosen := firstUser
	if haveNamed {
		chosen = named
	} else if !haveUser {
		return ""
	}
	trimmed := strings.Trim(chosen, " \t\r\n")
	line, _, _ := strings.Cut(trimmed, "\n")
	cut := min(len(line), nativeTitleLimit)
	for cut > 0 && cut < len(line) && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return line[:cut]
}

func readSession(nativeID string) []base.NativeTurn {
	var binding sessionBinding
	if json.Unmarshal([]byte(nativeID), &binding) != nil {
		return nil
	}
	if !filepath.IsAbs(binding.SessionFile) || !strings.HasSuffix(binding.SessionFile, ".jsonl") {
		return nil
	}
	file, err := os.Open(binding.SessionFile)
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil
	}
	start := int64(0)
	if info.Size() > nativeReadLimit {
		start = info.Size() - nativeReadLimit
	}
	whole := make([]byte, info.Size()-start)
	got, err := file.ReadAt(whole, start)
	if err != nil && err != io.EOF {
		return nil
	}
	whole = whole[:got]
	if start > 0 {
		_, after, found := bytes.Cut(whole, []byte{'\n'})
		if !found {
			return nil
		}
		whole = after
	} else if header, ok := firstLine(whole); !ok || textOf(header["id"]) != binding.SessionID {
		return nil
	}
	end := bytes.LastIndexByte(whole, '\n')
	if end < 0 {
		return nil
	}
	return turnsOf(whole[:end])
}

func turnsOf(data []byte) []base.NativeTurn {
	var found []base.NativeTurn
	var reply string
	var replyAt int64
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var object map[string]any
		if json.Unmarshal(line, &object) != nil || textOf(object["type"]) != "message" {
			continue
		}
		message, ok := object["message"].(map[string]any)
		if !ok {
			continue
		}
		at := isoMillis(textOf(object["timestamp"]))
		switch textOf(message["role"]) {
		case "user":
			said := strings.Trim(textParts(message["content"], "\n"), " \t\r\n")
			if said == "" {
				continue
			}
			if reply != "" {
				found = append(found, base.NativeTurn{Role: "assistant", Text: reply, AtMS: replyAt})
			}
			reply = ""
			found = append(found, base.NativeTurn{Role: "user", Text: said, AtMS: at})
		case "assistant":
			if piece := textParts(message["content"], ""); piece != "" {
				reply, replyAt = piece, at
			}
		}
	}
	if reply != "" {
		found = append(found, base.NativeTurn{Role: "assistant", Text: reply, AtMS: replyAt})
	}
	return found
}

func isoMillis(text string) int64 {
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return 0
	}
	return parsed.UnixMilli()
}

func textOf(value any) string {
	text, _ := value.(string)
	return text
}

func textParts(content any, separator string) string {
	switch body := content.(type) {
	case string:
		return body
	case []any:
		var joined strings.Builder
		for _, part := range body {
			object, ok := part.(map[string]any)
			if !ok || textOf(object["type"]) != "text" {
				continue
			}
			piece, ok := object["text"].(string)
			if !ok {
				continue
			}
			if joined.Len() > 0 {
				joined.WriteString(separator)
			}
			joined.WriteString(piece)
		}
		return joined.String()
	}
	return ""
}

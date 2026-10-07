package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	base "github.com/lsm/open-agent-protocol/go/adapter"
)

const (
	nativeHeadBytes  = 256 * 1024
	nativeTailBytes  = 64 * 1024
	nativeTitleLimit = 120
	linkCacheTTL     = 10 * time.Second
)

type desktopRecord struct {
	title    string
	archived bool
	localID  string
}

type linkCache struct {
	mu     sync.Mutex
	read   time.Time
	loaded bool
	ids    map[string]string
}

var links linkCache

func projectDirName(directory string) string {
	name := []byte(directory)
	for i, b := range name {
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-') {
			name[i] = '-'
		}
	}
	return string(name)
}

func desktopSessionsRoot(home string) string {
	if runtime.GOOS != "darwin" || home == "" {
		return ""
	}
	return filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions")
}

func (a *Adapter) NativeList(_ context.Context, request base.NativeListRequest) ([]base.NativeListing, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil
	}
	directory := request.Directory
	if directory == "" {
		directory = a.config.WorkingDirectory
	}
	return listSessions(home, desktopSessionsRoot(home), directory, request.Limit), nil
}

func (a *Adapter) NativeLink(nativeID string) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	links.mu.Lock()
	defer links.mu.Unlock()
	if !links.loaded || time.Since(links.read) > linkCacheTTL {
		home, _ := os.UserHomeDir()
		links.ids = map[string]string{}
		for cli, record := range desktopRecords(desktopSessionsRoot(home)) {
			if record.localID != "" {
				links.ids[cli] = record.localID
			}
		}
		links.read, links.loaded = time.Now(), true
	}
	if local := links.ids[nativeID]; local != "" {
		return appLink(local)
	}
	return ""
}

func appLink(localID string) string {
	return "claude://claude.ai/epitaxy/" + localID
}

func listSessions(home, desktopRoot, directory string, limit int) []base.NativeListing {
	if directory == "" || home == "" {
		return nil
	}
	project := filepath.Join(home, ".claude", "projects", projectDirName(directory))
	entries, err := os.ReadDir(project)
	if err != nil {
		return nil
	}
	live := liveSessions(home)
	desktop := desktopRecords(desktopRoot)
	var found []base.NativeListing
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".jsonl")
		known, isKnown := desktop[id]
		if isKnown && known.archived {
			continue
		}
		var updated int64
		if info, err := entry.Info(); err == nil {
			updated = info.ModTime().UnixMilli()
		}
		title := known.title
		if title == "" {
			title = transcriptTitle(filepath.Join(project, entry.Name()))
		}
		link := ""
		if known.localID != "" {
			link = appLink(known.localID)
		}
		found = append(found, base.NativeListing{NativeID: id, Title: title, Directory: directory, UpdatedAtMS: updated, Running: live[id], Link: link})
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].UpdatedAtMS > found[j].UpdatedAtMS })
	if limit >= 0 && len(found) > limit {
		found = found[:limit]
	}
	return found
}

func liveSessions(home string) map[string]bool {
	live := map[string]bool{}
	registry := filepath.Join(home, ".claude", "sessions")
	entries, err := os.ReadDir(registry)
	if err != nil {
		return live
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := readLimited(filepath.Join(registry, entry.Name()), 64*1024)
		if err != nil {
			continue
		}
		var record struct {
			SessionID *string          `json:"sessionId"`
			PID       *json.RawMessage `json:"pid"`
		}
		if json.Unmarshal(data, &record) != nil || record.SessionID == nil || record.PID == nil {
			continue
		}
		var pid int64
		if json.Unmarshal(*record.PID, &pid) != nil || !alive(pid) {
			continue
		}
		live[*record.SessionID] = true
	}
	return live
}

func alive(pid int64) bool {
	if runtime.GOOS == "windows" {
		return true
	}
	if pid <= 0 || pid > 1<<31-1 {
		return false
	}
	process, err := os.FindProcess(int(pid))
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

func desktopRecords(root string) map[string]desktopRecord {
	records := map[string]desktopRecord{}
	if root == "" {
		return records
	}
	accounts, err := os.ReadDir(root)
	if err != nil {
		return records
	}
	for _, account := range accounts {
		if !account.IsDir() {
			continue
		}
		workspaces, err := os.ReadDir(filepath.Join(root, account.Name()))
		if err != nil {
			continue
		}
		for _, workspace := range workspaces {
			if !workspace.IsDir() {
				continue
			}
			dir := filepath.Join(root, account.Name(), workspace.Name())
			files, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, file := range files {
				if !file.Type().IsRegular() || !strings.HasSuffix(file.Name(), ".json") {
					continue
				}
				data, err := readLimited(filepath.Join(dir, file.Name()), 4*1024*1024)
				if err != nil {
					continue
				}
				var object map[string]json.RawMessage
				if json.Unmarshal(data, &object) != nil {
					continue
				}
				var cli string
				if json.Unmarshal(object["cliSessionId"], &cli) != nil {
					continue
				}
				var archived bool
				_ = json.Unmarshal(object["isArchived"], &archived)
				records[cli] = desktopRecord{title: rawText(object["title"]), archived: archived, localID: rawText(object["sessionId"])}
			}
		}
	}
	return records
}

func readLimited(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > limit {
		return nil, errors.New("too large")
	}
	return io.ReadAll(file)
}

func rawText(raw json.RawMessage) string {
	var text string
	_ = json.Unmarshal(raw, &text)
	return text
}

func transcriptTitle(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	size := info.Size()
	if size > nativeHeadBytes {
		tail := make([]byte, nativeTailBytes)
		got, _ := file.ReadAt(tail, size-nativeTailBytes)
		if title, ok := customTitle(tail[:got]); ok {
			return title
		}
	}
	head := make([]byte, min(size, nativeHeadBytes))
	got, _ := file.ReadAt(head, 0)
	if size <= nativeHeadBytes {
		if title, ok := customTitle(head[:got]); ok {
			return title
		}
	}
	return firstUserLine(head[:got])
}

func customTitle(data []byte) (string, bool) {
	latest, found := "", false
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if !bytes.Contains(line, []byte(`"custom-title"`)) {
			continue
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(line, &object) != nil {
			continue
		}
		var title string
		if json.Unmarshal(object["customTitle"], &title) != nil || title == "" || title == "New session" {
			continue
		}
		latest, found = title, true
	}
	return latest, found
}

func withoutLeadingTags(text string) string {
	rest := strings.Trim(text, " \t\r\n")
	for len(rest) > 1 && rest[0] == '<' {
		nameEnd := strings.IndexAny(rest, "> \n")
		if nameEnd < 0 {
			break
		}
		name := rest[1:nameEnd]
		if name == "" || name[0] == '/' || len(name)+3 > 128 {
			break
		}
		closing := "</" + name + ">"
		at := strings.Index(rest, closing)
		if at < 0 {
			break
		}
		rest = strings.Trim(rest[at+len(closing):], " \t\r\n")
	}
	return rest
}

func firstUserLine(data []byte) string {
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if !bytes.Contains(line, []byte(`"type":"user"`)) {
			continue
		}
		var entry struct {
			Message *struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &entry) != nil || entry.Message == nil {
			continue
		}
		text, ok := firstText(entry.Message.Content)
		if !ok {
			continue
		}
		trimmed := withoutLeadingTags(text)
		if trimmed == "" || trimmed[0] == '<' {
			continue
		}
		first, _, _ := strings.Cut(trimmed, "\n")
		cut := min(len(first), nativeTitleLimit)
		for cut > 0 && cut < len(first) && !utf8.RuneStart(first[cut]) {
			cut--
		}
		return first[:cut]
	}
	return ""
}

func firstText(content json.RawMessage) (string, bool) {
	var plain string
	if json.Unmarshal(content, &plain) == nil {
		return plain, true
	}
	var parts []json.RawMessage
	if json.Unmarshal(content, &parts) != nil {
		return "", false
	}
	for _, raw := range parts {
		var part map[string]json.RawMessage
		if json.Unmarshal(raw, &part) != nil {
			continue
		}
		var text string
		if json.Unmarshal(part["text"], &text) == nil {
			return text, true
		}
	}
	return "", false
}

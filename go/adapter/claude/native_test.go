package claude

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAWorkingDirectoryMapsToTheProjectFolderClaudeCodeNamesAfterIt(t *testing.T) {
	if got := projectDirName("/Users/me/work/.claude/worktrees/a-b"); got != "-Users-me-work--claude-worktrees-a-b" {
		t.Fatalf("projectDirName = %q", got)
	}
	if got := projectDirName("/w/é"); got != "-w---" {
		t.Fatalf("a two-byte rune maps to %q, not one dash per byte", got)
	}
}

func TestAProjectsTranscriptsListNewestFirstTitledByTheLatestCustomTitleOrTheFirstUserLineAndAnArchivedOneIsLeftOut(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, ".claude", "projects", "-w")
	workspace := filepath.Join(root, "desktop", "account", "workspace")
	for _, dir := range []string{project, workspace} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, data string, at time.Time) {
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if !at.IsZero() {
			if err := os.Chtimes(path, at, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	older := time.UnixMilli(1_000_000)
	write(filepath.Join(project, "aaa.jsonl"), "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"<command-name>/init</command-name>\"}}\n{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"<unclosed markup\"}}\n{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"<system-reminder>\\nnoise\\n</system-reminder>\\nfix the parser\\nplease\"}]}}\n", older)
	write(filepath.Join(project, "bbb.jsonl"), "{\"type\":\"custom-title\",\"customTitle\":\"Named work\"}\n{\"type\":\"custom-title\",\"customTitle\":\"New session\"}\n", older.Add(time.Second))
	write(filepath.Join(project, "ccc.jsonl"), "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"archived\"}}\n", older.Add(2*time.Second))
	write(filepath.Join(project, "notes.txt"), "ignored", time.Time{})
	write(filepath.Join(workspace, "local_c.json"), `{"cliSessionId":"ccc","title":"gone","isArchived":true}`, time.Time{})
	write(filepath.Join(workspace, "local_b.json"), `{"sessionId":"local_b","cliSessionId":"bbb","title":"","isArchived":false}`, time.Time{})
	listed := listSessions(root, filepath.Join(root, "desktop"), "/w", 10)
	if len(listed) != 2 || listed[0].NativeID != "bbb" || listed[0].Title != "Named work" || listed[0].Link != "claude://claude.ai/epitaxy/local_b" || listed[1].NativeID != "aaa" || listed[1].Title != "fix the parser" || listed[1].Link != "" {
		t.Fatalf("listed %+v", listed)
	}
	for _, session := range listed {
		if session.Directory != "/w" || session.Running {
			t.Fatalf("session %+v", session)
		}
	}
	if limited := listSessions(root, "", "/w", 1); len(limited) != 1 || limited[0].NativeID != "ccc" {
		t.Fatalf("without desktop records, limit 1 listed %+v", limited)
	}
}

func TestALiveRegistryEntryMarksItsSessionRunningOnlyWhileItsProcessLives(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, ".claude", "projects", "-w")
	registry := filepath.Join(root, ".claude", "sessions")
	for _, dir := range []string{project, registry} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"live", "dead"} {
		if err := os.WriteFile(filepath.Join(project, id+".jsonl"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	entries := map[string]string{"a.json": `{"sessionId":"live","pid":` + strconv.Itoa(os.Getpid()) + `}`, "b.json": `{"sessionId":"dead","pid":2147483647}`}
	for name, data := range entries {
		if err := os.WriteFile(filepath.Join(registry, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	running := map[string]bool{}
	for _, session := range listSessions(root, "", "/w", 10) {
		running[session.NativeID] = session.Running
	}
	if !running["live"] || running["dead"] {
		t.Fatalf("running %v", running)
	}
}

func TestATranscriptReadsAsItsUserMessagesAndTheRepliesBetweenThemLeavingOutToolResultsThinkingMetaSideChainsAndClaudeCodesOwnWrappersButNotAMessageThatOpensWithMarkup(t *testing.T) {
	lines := strings.Join([]string{
		`{"type":"queue-operation"}`,
		`{"type":"user","timestamp":"2026-10-06T20:12:33.250Z","message":{"role":"user","content":"Remember the word mango."}}`,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":"meta noise"}}`,
		`{"type":"assistant","timestamp":"2026-10-06T20:12:34.000Z","message":{"id":"m1","content":[{"type":"thinking","thinking":"hm"}]}}`,
		`{"type":"assistant","timestamp":"2026-10-06T20:12:35.000Z","message":{"id":"m1","content":[{"type":"text","text":"ok"}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"x"}]}}`,
		`{"type":"user","isSidechain":true,"message":{"role":"user","content":"side"}}`,
		`{"type":"user","message":{"role":"user","content":"<command-name>/clear</command-name>"}}`,
		`{"type":"user","message":{"role":"user","content":"<div>pasted markup</div> what is this?"}}`,
		`{"type":"user","message":{"role":"user","content":"<system-reminder>noise</system-reminder>\nafter the reminder"}}`,
		`{"type":"user","timestamp":"2026-10-06T20:13:00.000Z","message":{"role":"user","content":[{"type":"text","text":"Which word?"}]}}`,
		`{"type":"assistant","message":{"id":"m2","content":[{"type":"text","text":"Checking."},{"type":"tool_use","id":"t2","name":"x","input":{}}]}}`,
		`{"type":"assistant","message":{"id":"m3","content":[{"type":"text","text":"mango"}]}}`,
	}, "\n")
	turns := transcriptTurns([]byte(lines))
	texts := make([]string, len(turns))
	for i, turn := range turns {
		texts[i] = turn.Role + ":" + turn.Text
	}
	want := []string{"user:Remember the word mango.", "assistant:ok", "user:<div>pasted markup</div> what is this?", "user:after the reminder", "user:Which word?", "assistant:mango"}
	if strings.Join(texts, "|") != strings.Join(want, "|") || turns[0].AtMS != 1791317553250 {
		t.Fatalf("turns %v at %d", texts, turns[0].AtMS)
	}
}

func TestANativeReadFindsTheTranscriptUnderItsDirectoryOrAnyProjectAndRefusesAnUnsafeID(t *testing.T) {
	root := t.TempDir()
	other := filepath.Join(root, ".claude", "projects", "-elsewhere")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "abc_1.jsonl"), []byte("{\"type\":\"user\",\"message\":{\"content\":\"hi\"}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if turns := readSession(root, "/w", "abc_1"); len(turns) != 1 || turns[0].Text != "hi" {
		t.Fatalf("read %+v", turns)
	}
	var ids []string
	for _, id := range []string{"../-elsewhere/abc_1", "abc 1", ""} {
		if len(readSession(root, "/w", id)) != 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) != 0 {
		t.Fatalf("unsafe ids read: %v", ids)
	}
}

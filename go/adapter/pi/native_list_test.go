package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/pi/internal/native"
)

func TestAWorkingDirectoryMapsToTheStoreFolderPiNamesAfterIt(t *testing.T) {
	if got := storeDirName("/Users/me/work/a:b"); got != "--Users-me-work-a-b--" {
		t.Fatalf("storeDirName = %q", got)
	}
}

func TestASessionFileReadsAsItsUserMessagesAndTheLastReplyBeforeEachLeavingOutThinkingAndToolResults(t *testing.T) {
	lines := strings.Join([]string{
		`{"type":"session","version":3,"id":"abc","timestamp":"2026-03-28T17:06:36.479Z","cwd":"/w"}`,
		`{"type":"model_change","id":"m1","parentId":null}`,
		`{"type":"message","id":"1","timestamp":"2026-03-28T17:06:52.478Z","message":{"role":"user","content":[{"type":"text","text":"what tools?"}]}}`,
		`{"type":"message","id":"2","timestamp":"2026-03-28T17:06:57.760Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"Let me look."}]}}`,
		`{"type":"message","id":"3","message":{"role":"toolResult","content":[{"type":"text","text":"ls output"}]}}`,
		`{"type":"message","id":"4","timestamp":"2026-03-28T17:07:11.954Z","message":{"role":"assistant","content":[{"type":"text","text":"None are "},{"type":"text","text":"installed."}]}}`,
		`{"type":"message","id":"5","message":{"role":"user","content":"thanks"}}`,
	}, "\n")
	turns := turnsOf([]byte(lines))
	if len(turns) != 3 {
		t.Fatalf("turns = %+v", turns)
	}
	if turns[0].Text != "what tools?" || turns[0].AtMS != 1774717612478 {
		t.Fatalf("first turn = %+v", turns[0])
	}
	if turns[1].Role != "assistant" || turns[1].Text != "None are installed." {
		t.Fatalf("reply = %+v", turns[1])
	}
	if turns[2].Text != "thanks" {
		t.Fatalf("last turn = %+v", turns[2])
	}
}

func TestAProjectsSessionFilesListNewestFirstNamedBySessionInfoOrTheFirstUserMessageWithAnIdAReopenAcceptsAndReadBackThroughIt(t *testing.T) {
	agent := filepath.Join(t.TempDir(), "agent")
	store := filepath.Join(agent, "sessions", "--w--")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, data string, at time.Time) {
		path := filepath.Join(store, name)
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	older := time.UnixMilli(1_000_000)
	write("a.jsonl", "{\"type\":\"session\",\"id\":\"aaa\",\"cwd\":\"/w\"}\n{\"type\":\"message\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"fix the parser\\nplease\"}]}}\n", older)
	write("b.jsonl", "{\"type\":\"session\",\"id\":\"bbb\",\"cwd\":\"/w\"}\n{\"type\":\"session_info\",\"name\":\"Named work\"}\n", older.Add(time.Second))
	write("c.jsonl", "not a session\n", older.Add(2*time.Second))
	adapter := &Adapter{config: Config{Environment: []string{"PI_CODING_AGENT_DIR=" + agent}, WorkingDirectory: "/w"}}
	listed, err := adapter.NativeList(context.Background(), base.NativeListRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Title != "Named work" || listed[1].Title != "fix the parser" {
		t.Fatalf("listed = %+v", listed)
	}
	if listed[0].UpdatedAtMS != older.Add(time.Second).UnixMilli() || listed[0].Directory != "/w" {
		t.Fatalf("newest = %+v", listed[0])
	}
	var binding sessionBinding
	if err := json.Unmarshal([]byte(listed[1].NativeID), &binding); err != nil || binding.SessionID != "aaa" || binding.SessionFile != filepath.Join(store, "a.jsonl") {
		t.Fatalf("binding = %+v, %v", binding, err)
	}
	read, err := adapter.NativeRead(context.Background(), base.NativeReadRequest{NativeID: listed[1].NativeID})
	if err != nil || len(read) != 1 || read[0].Text != "fix the parser\nplease" {
		t.Fatalf("read = %+v, %v", read, err)
	}
	if mismatched := readSession(`{"sessionId":"other","sessionFile":"` + filepath.Join(store, "a.jsonl") + `"}`); len(mismatched) != 0 {
		t.Fatalf("a binding naming another session read %+v", mismatched)
	}
	if limited, _ := adapter.NativeList(context.Background(), base.NativeListRequest{Limit: 1}); len(limited) != 1 || limited[0].Title != "Named work" {
		t.Fatalf("limit 1 listed %+v", limited)
	}
	if elsewhere, _ := adapter.NativeList(context.Background(), base.NativeListRequest{Directory: "/elsewhere", Limit: 10}); len(elsewhere) != 0 {
		t.Fatalf("another directory listed %+v", elsewhere)
	}
}

func TestASessionFileLargerThanTheReadLimitReadsItsTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	filler := `{"type":"message","message":{"role":"toolResult","content":"` + strings.Repeat("x", 1024) + `"}}` + "\n"
	var data strings.Builder
	data.WriteString(`{"type":"session","id":"big"}` + "\n")
	for data.Len() < nativeReadLimit+4096 {
		data.WriteString(filler)
	}
	data.WriteString(`{"type":"message","message":{"role":"user","content":"the last word"}}` + "\n")
	if err := os.WriteFile(path, []byte(data.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	turns := readSession(bindingText("big", path))
	if len(turns) != 1 || turns[0].Text != "the last word" {
		t.Fatalf("turns = %+v", turns)
	}
}

func TestAListedSessionAndTheBindingAReopenRecordsSpellTheSameNativeIDForAPathWithMarkup(t *testing.T) {
	path := "/w/a&b<c>.jsonl"
	listed := bindingText("abc", path)
	if held := encodeBinding(native.SessionState{SessionID: "abc", SessionFile: path}); held != listed {
		t.Fatalf("the reopen binding %s differs from the listed id %s", held, listed)
	}
	if listed != `{"sessionId":"abc","sessionFile":"/w/a&b<c>.jsonl"}` {
		t.Fatalf("the native id is %s, not Zig's spelling", listed)
	}
}

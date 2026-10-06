package binding

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

func fileStoreIn(t *testing.T) Store {
	t.Helper()
	store, err := File(filepath.Join(t.TempDir(), "nested", "bindings.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func sample() Record {
	return FromOpen("session-1", "memory", "memory-oap-v1", "a-model", "/home/op", "/work/repo", []string{"fs"})
}

func TestAStoreAppendsAndReadsBackEveryRecord(t *testing.T) {
	ctx := context.Background()
	store := fileStoreIn(t)
	for i := 0; i < 4; i++ {
		if err := store.Append(ctx, Opened(sample(), int64(i))); err != nil {
			t.Fatal(err)
		}
	}
	history, err := store.History(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 4 {
		t.Fatalf("history has %d entries, want 4", len(history))
	}
	latest, found, err := store.Latest(ctx, "session-1")
	if err != nil || !found {
		t.Fatalf("latest=%v found=%v err=%v", latest, found, err)
	}
	if latest.TimeMS != 3 {
		t.Fatalf("latest time_ms = %d, want the last append", latest.TimeMS)
	}
}

func TestAReopenIsAppendedRatherThanReplacingTheOpen(t *testing.T) {
	ctx := context.Background()
	store := fileStoreIn(t)
	opened := sample()
	if err := store.Append(ctx, Opened(opened, 1)); err != nil {
		t.Fatal(err)
	}
	reopened := Reopened(opened, "native-7", 2)
	if err := store.Append(ctx, reopened); err != nil {
		t.Fatal(err)
	}
	history, err := store.History(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Action != ActionOpened || history[1].Action != ActionReopened {
		t.Fatalf("history = %+v, want the open then the reopen", history)
	}
	if history[1].Record.NativeSessionID != "native-7" {
		t.Fatalf("the reopen did not carry the native id: %+v", history[1].Record)
	}
	if history[0].Record.NativeSessionID != "" {
		t.Fatalf("the open carried a native id it could not have known: %+v", history[0].Record)
	}
}

func TestATornAppendIsDetectedAndNeverRead(t *testing.T) {
	ctx := context.Background()
	store := fileStoreIn(t)
	if err := store.Append(ctx, Opened(sample(), 1)); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, Reopened(sample(), "native-7", 2)); err != nil {
		t.Fatal(err)
	}
	torn := strings.TrimSuffix(string(encodeMust(t, Reopened(sample(), "native-9", 3))), "\n")
	if err := os.WriteFile(store.(*fileStore).path, append(readFile(t, store), []byte(torn)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Latest(ctx, "session-1"); !errors.Is(err, ErrTorn) {
		t.Fatalf("latest err = %v, want a torn record refused", err)
	}
	if _, err := store.History(ctx, "session-1"); !errors.Is(err, ErrTorn) {
		t.Fatalf("history err = %v, want a torn record refused", err)
	}
}

func TestATornTailIsTruncatedSoTheStoreKeepsWorking(t *testing.T) {
	ctx := context.Background()
	store := fileStoreIn(t)
	path := store.(*fileStore).path
	if err := store.Append(ctx, Opened(sample(), 1)); err != nil {
		t.Fatal(err)
	}
	partial := append(readFile(t, store), []byte("3f1a9b2c {\"action\":\"opene")...)
	if err := os.WriteFile(path, partial, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Latest(ctx, "session-1"); !errors.Is(err, ErrTorn) {
		t.Fatalf("latest err = %v, want the torn tail reported before it is repaired", err)
	}
	if err := store.Append(ctx, Reopened(sample(), "native-7", 2)); err != nil {
		t.Fatal(err)
	}
	history, err := store.History(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("history has %d entries, want the whole record and the one written after the repair", len(history))
	}
	if history[1].Action != ActionReopened {
		t.Fatalf("the last entry is %q, want the reopen", history[1].Action)
	}
}

func TestATornTailIsTruncatedWhenTheStoreIsOpenedAgain(t *testing.T) {
	ctx := context.Background()
	store := fileStoreIn(t)
	path := store.(*fileStore).path
	if err := store.Append(ctx, Opened(sample(), 1)); err != nil {
		t.Fatal(err)
	}
	partial := append(readFile(t, store), []byte("3f1a9b2c {\"action\":\"clo")...)
	if err := os.WriteFile(path, partial, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := File(path)
	if err != nil {
		t.Fatal(err)
	}
	history, err := reopened.History(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Action != ActionOpened {
		t.Fatalf("history = %+v, want the whole record the file already held", history)
	}
}

func TestACorruptLineThatKeepsItsNewlineIsTruncatedToo(t *testing.T) {
	ctx := context.Background()
	store := fileStoreIn(t)
	path := store.(*fileStore).path
	if err := store.Append(ctx, Opened(sample(), 1)); err != nil {
		t.Fatal(err)
	}
	corrupt := "3f1a9b2c {\"action\":\"reopened\",\"record\":{\"session_id\":\"session-1\"}}\n"
	if err := os.WriteFile(path, append(readFile(t, store), []byte(corrupt)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Latest(ctx, "session-1"); !errors.Is(err, ErrTorn) {
		t.Fatalf("latest err = %v, want the corrupt line reported before it is repaired", err)
	}
	if err := store.Append(ctx, Reopened(sample(), "native-7", 2)); err != nil {
		t.Fatal(err)
	}
	history, err := store.History(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[1].Action != ActionReopened {
		t.Fatalf("history = %+v, want the whole record and the one written after the repair", history)
	}
}

func TestACorruptLineIsTruncatedWhenTheStoreIsOpenedAgain(t *testing.T) {
	ctx := context.Background()
	store := fileStoreIn(t)
	path := store.(*fileStore).path
	if err := store.Append(ctx, Opened(sample(), 1)); err != nil {
		t.Fatal(err)
	}
	corrupt := "3f1a9b2c {\"action\":\"closed\",\"record\":{\"session_id\":\"session-1\"}}\n"
	if err := os.WriteFile(path, append(readFile(t, store), []byte(corrupt)...), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := File(path)
	if err != nil {
		t.Fatal(err)
	}
	history, err := reopened.History(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Action != ActionOpened {
		t.Fatalf("history = %+v, want the whole record the file already held", history)
	}
}

func TestARecordWhoseBytesDoNotMatchItsChecksumIsRefused(t *testing.T) {
	ctx := context.Background()
	store := fileStoreIn(t)
	if err := store.Append(ctx, Opened(sample(), 1)); err != nil {
		t.Fatal(err)
	}
	raw := readFile(t, store)
	edited := strings.Replace(string(raw), `"session-1"`, `"session-2"`, 1)
	if edited == string(raw) {
		t.Fatal("the record does not name its session, so the test cannot corrupt it")
	}
	if err := os.WriteFile(store.(*fileStore).path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Latest(ctx, "session-1"); !errors.Is(err, ErrTorn) {
		t.Fatalf("latest err = %v, want a record whose bytes do not match its checksum refused", err)
	}
}

func TestLiveReadsTheEntryThatClaimsToOpenTheSession(t *testing.T) {
	if !Live(Opened(sample(), 1)) {
		t.Fatal("an open does not read as live")
	}
	if !Live(Reopened(sample(), "native-7", 2)) {
		t.Fatal("a reopen does not read as live")
	}
	if Live(Closed(sample(), 3)) {
		t.Fatal("a close reads as live")
	}
	if Live(Entry{Action: ActionRefused, TimeMS: 4, Record: sample()}) {
		t.Fatal("a refusal reads as live, and a refusal is not a state")
	}
}

func TestStateIgnoresARefusalForADuplicateThatNeverRan(t *testing.T) {
	first := FromOpen("session-1", "memory", "memory-oap-v1", "a-model", "/home/op", "/work", []string{"fs"})
	second := FromOpen("session-1", "memory", "memory-oap-v1", "another-model", "/home/op", "/work", []string{"git"})
	history := []Entry{
		Opened(first, 1),
		{Action: ActionRefused, TimeMS: 2, Record: second},
	}
	state, found := State(history)
	if !found {
		t.Fatal("no state found")
	}
	if state.Action != ActionOpened || state.Record.Model != "a-model" {
		t.Fatalf("state = %+v, want the open that is actually running", state)
	}
	if len(state.Record.ToolSourceIDs) != 1 || state.Record.ToolSourceIDs[0] != "fs" {
		t.Fatalf("state carries the refused request's tool sources: %+v", state.Record.ToolSourceIDs)
	}
}
func TestStateOfOnlyRefusalsIsUnknown(t *testing.T) {
	if _, found := State([]Entry{{Action: ActionRefused, TimeMS: 1, Record: sample()}}); found {
		t.Fatal("a history of refusals reported a state")
	}
}

func TestOneSessionsRecordsAreNotAnotherSessions(t *testing.T) {
	ctx := context.Background()
	store := fileStoreIn(t)
	other := sample()
	other.SessionID = "session-2"
	if err := store.Append(ctx, Opened(sample(), 1)); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, Opened(other, 2)); err != nil {
		t.Fatal(err)
	}
	history, err := store.History(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("history for one session has %d entries, want 1", len(history))
	}
	if _, found, err := store.Latest(ctx, "session-9"); err != nil || found {
		t.Fatalf("latest for an unknown session found=%v err=%v", found, err)
	}
}

func encodeMust(t *testing.T, entry Entry) []byte {
	t.Helper()
	line, err := encode(entry)
	if err != nil {
		t.Fatal(err)
	}
	return line
}

func readFile(t *testing.T, store Store) []byte {
	t.Helper()
	raw, err := os.ReadFile(store.(*fileStore).path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestManyAppendsStayLinearInWhatTheyRead(t *testing.T) {
	ctx := context.Background()
	store := fileStoreIn(t)
	for i := 0; i < 200; i++ {
		if err := store.Append(ctx, Opened(sample(), int64(i))); err != nil {
			t.Fatal(err)
		}
	}
	store.(*fileStore).validated = 0
	store.(*fileStore).confirmed = false
	started := time.Now()
	if err := store.Append(ctx, Opened(sample(), 201)); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("one append over 200 records took %v, so the repair is reading the whole log every time", elapsed)
	}
	history, err := store.History(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 201 {
		t.Fatalf("history has %d entries, want 201", len(history))
	}
}

func TestTheStoreFileIsNotWorldReadable(t *testing.T) {
	store := fileStoreIn(t)
	if err := store.Append(context.Background(), Opened(sample(), 1)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.(*fileStore).path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("the store file is %v, want no group or other access", info.Mode().Perm())
	}
}

func TestASameLengthEditOfAValidatedRecordIsFoundAndThenRepaired(t *testing.T) {
	ctx := context.Background()
	store := fileStoreIn(t)
	first := Opened(sample(), 1)
	second := Opened(sample(), 2)
	if err := store.Append(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, second); err != nil {
		t.Fatal(err)
	}
	editor := store.(*fileStore)
	before, err := os.ReadFile(editor.path)
	if err != nil {
		t.Fatal(err)
	}
	corrupted := make([]byte, len(before))
	copy(corrupted, before)
	corrupted[0] ^= 'a' ^ 'z'
	if string(corrupted) == string(before) {
		t.Fatal("the edit changed nothing, so the test proves nothing")
	}
	if len(corrupted) != len(before) {
		t.Fatal("the edit changed the length, so this is not the same-length case")
	}
	if err := os.WriteFile(editor.path, corrupted, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.History(ctx, "session-1"); !errors.Is(err, ErrTorn) {
		t.Fatalf("the read reported %v, so the edit was not noticed", err)
	}
	third := Opened(sample(), 3)
	if err := store.Append(ctx, third); err != nil {
		t.Fatal(err)
	}
	if _, err := store.History(ctx, "session-1"); err != nil {
		t.Fatalf("the read after the repair reported %v", err)
	}
	history, err := store.History(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("the repaired log holds %d entries, want the records the walk kept: %+v", len(history), history)
	}
}

func fromALongRecord() Record {
	first := FromOpen("session-longer-than-the-cached-offset", "memory", "memory-oap-v1", "a-model", "/home/op", "/work", []string{"fs"})
	first.Model = strings.Repeat("m", 200)
	return first
}

func TestAHostThatEmptiesTheStoreDoesNotLeaveTheCachePointingIntoNothing(t *testing.T) {
	ctx := context.Background()
	store := fileStoreIn(t)
	if err := store.Append(ctx, Opened(sample(), 1)); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, Opened(sample(), 2)); err != nil {
		t.Fatal(err)
	}
	editor := store.(*fileStore)
	if !editor.confirmed || editor.lastStart != 0 || editor.validated == 0 {
		t.Fatalf("the store cached validated=%d confirmed=%t lastStart=%d, so the stale-offset case this test needs is not the one it built",
			editor.validated, editor.confirmed, editor.lastStart)
	}
	if err := os.WriteFile(editor.path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	long := Entry{Action: ActionOpened, TimeMS: 9, Record: fromALongRecord()}
	encoded, err := encode(long)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(encoded)) <= editor.validated {
		t.Fatalf("the record written into the emptied store is %d bytes and the cached offset is %d, so the repair cannot truncate into it",
			len(encoded), editor.validated)
	}
	if err := store.Append(ctx, long); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, long); err != nil {
		t.Fatal(err)
	}
	history, err := store.History(ctx, long.Record.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("the store holds %d records after a host emptied it, want the two appended since: %+v", len(history), history)
	}
}

func TestTheLineIsTheOneTheZigStoreWrites(t *testing.T) {
	const zig = "c66c4573 {\"action\":\"opened\",\"time_ms\":7,\"record\":{\"session_id\":\"s\",\"adapter\":\"codex\",\"native_session_id\":\"thread-1\"}}\n"
	line, err := encode(Entry{Action: ActionOpened, TimeMS: 7, Record: Record{SessionID: "s", Adapter: "codex", NativeSessionID: "thread-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(line) != zig {
		t.Fatalf("Go writes %q, the Zig store writes %q", line, zig)
	}
	entry, err := decode(zig)
	if err != nil || entry.Record.NativeSessionID != "thread-1" {
		t.Fatalf("decode = %+v, %v", entry, err)
	}
}

func TestTheOpensSettingsAreWrittenAsTheZigStoreWritesThem(t *testing.T) {
	const zig = "b3827233 {\"action\":\"opened\",\"time_ms\":7,\"record\":{\"session_id\":\"s\",\"adapter\":\"pi\",\"model\":\"m\",\"reasoning_level\":\"high\",\"compaction_policy\":{\"kind\":\"share\",\"share_percent\":80}}}\n"
	line, err := encode(Entry{Action: ActionOpened, TimeMS: 7, Record: Record{SessionID: "s", Adapter: "pi", Model: "m", ReasoningLevel: "high", CompactionPolicy: &protocol.CompactionPolicy{Kind: protocol.CompactionShare, SharePercent: 80}}})
	if err != nil {
		t.Fatal(err)
	}
	if string(line) != zig {
		t.Fatalf("Go writes %q, the Zig store writes %q", line, zig)
	}
	entry, err := decode(zig)
	if err != nil || entry.Record.ReasoningLevel != "high" || entry.Record.CompactionPolicy == nil || entry.Record.CompactionPolicy.SharePercent != 80 {
		t.Fatalf("decode = %+v, %v", entry, err)
	}
}

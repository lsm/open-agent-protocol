package binding

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

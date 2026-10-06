package binding_test

import (
	"errors"
	"testing"

	"github.com/lsm/open-agent-protocol/go/binding"
)

func entry(action binding.Action, timeMS int64, sessionID string) binding.Entry {
	return binding.Entry{Action: action, TimeMS: timeMS, Record: binding.Record{SessionID: sessionID, Adapter: "memory"}}
}

func ids(entries []binding.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Record.SessionID)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSessionsKeepsEachSessionsLatestEntryThatIsNotRefused(t *testing.T) {
	sessions := binding.Sessions([]binding.Entry{
		entry(binding.ActionOpened, 1, "a"),
		entry(binding.ActionRefused, 2, "only-refused"),
		entry(binding.ActionOpened, 3, "b"),
		entry(binding.ActionClosed, 4, "a"),
		entry(binding.ActionRefused, 5, "a"),
	})
	if got := ids(sessions); !equal(got, []string{"a", "b"}) {
		t.Fatalf("sessions = %v, want a and b and no session whose only entry was refused", got)
	}
	if sessions[0].Action != binding.ActionClosed || sessions[0].TimeMS != 4 {
		t.Fatalf("a = %+v, want its close, the latest entry that was not refused", sessions[0])
	}
}

func TestAListIsNewestFirstWithTiesBrokenBySessionID(t *testing.T) {
	page, err := binding.List([]binding.Entry{
		entry(binding.ActionOpened, 1, "old"),
		entry(binding.ActionOpened, 5, "tie-b"),
		entry(binding.ActionOpened, 9, "new"),
		entry(binding.ActionOpened, 5, "tie-a"),
	}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(page.Entries); !equal(got, []string{"new", "tie-a", "tie-b", "old"}) {
		t.Fatalf("order = %v", got)
	}
	if page.NextCursor != "" {
		t.Fatalf("a list that fits one page carries next_cursor %q", page.NextCursor)
	}
}

func TestAListPagesOnItsCursorUntilNoneRemain(t *testing.T) {
	sessions := []binding.Entry{
		entry(binding.ActionOpened, 1, "e"),
		entry(binding.ActionOpened, 2, "d"),
		entry(binding.ActionOpened, 3, "c"),
		entry(binding.ActionOpened, 4, "b"),
		entry(binding.ActionOpened, 5, "a"),
	}
	var seen []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 3 {
			t.Fatalf("paging did not end: %v", seen)
		}
		page, err := binding.List(sessions, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Entries) > 2 {
			t.Fatalf("a page of limit 2 held %d", len(page.Entries))
		}
		seen = append(seen, ids(page.Entries)...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if !equal(seen, []string{"a", "b", "c", "d", "e"}) {
		t.Fatalf("pages = %v, want every session once, newest first", seen)
	}
}

func TestAListDefaultsItsLimitAndRefusesOneOutOfRange(t *testing.T) {
	var sessions []binding.Entry
	for i := 0; i < binding.DefaultLimit+1; i++ {
		sessions = append(sessions, entry(binding.ActionOpened, int64(i), string(rune('a'+i%26))+string(rune('a'+i/26))))
	}
	page, err := binding.List(sessions, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != binding.DefaultLimit || page.NextCursor == "" {
		t.Fatalf("an absent limit held %d with next_cursor %q, want %d and more to come", len(page.Entries), page.NextCursor, binding.DefaultLimit)
	}
	for _, limit := range []int{-1, binding.MaxLimit + 1} {
		if _, err := binding.List(sessions, "", limit); !errors.Is(err, binding.ErrInvalidLimit) {
			t.Fatalf("limit %d: err = %v, want ErrInvalidLimit", limit, err)
		}
	}
	if _, err := binding.List(sessions, "", binding.MaxLimit); err != nil {
		t.Fatalf("limit %d: %v", binding.MaxLimit, err)
	}
}

func TestACursorTheListDidNotIssueIsRefused(t *testing.T) {
	for _, cursor := range []string{"not base64!", "bm8tY29sb24", "eDpzZXNzaW9u", "MTI6"} {
		if _, err := binding.List(nil, cursor, 0); !errors.Is(err, binding.ErrInvalidCursor) {
			t.Fatalf("cursor %q: err = %v, want ErrInvalidCursor", cursor, err)
		}
	}
}

func TestACursorIsTheBytesTheZigHubIssuesForTheSamePosition(t *testing.T) {
	page, err := binding.List([]binding.Entry{
		entry(binding.ActionOpened, 1, "old"),
		entry(binding.ActionOpened, 5, "tie-b"),
		entry(binding.ActionOpened, 9, "new"),
		entry(binding.ActionOpened, 5, "tie-a"),
	}, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if page.NextCursor != "NTp0aWUtYQ" {
		t.Fatalf("next_cursor = %q, want the bytes the Zig hub's binding test pins", page.NextCursor)
	}
}

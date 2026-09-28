package binding

import (
	"context"
	"errors"
)

var ErrTorn = errors.New("binding: a record was not written whole")

type Action string

const (
	ActionOpened   Action = "opened"
	ActionReopened Action = "reopened"
	ActionClosed   Action = "closed"
	ActionRefused  Action = "refused"
)

// Live reports whether a session was still open when an entry was written: the
// entry is one that opened or reopened it, and not a later close. A refusal is
// not a state, so it never answers either way.
func Live(entry Entry) bool {
	return entry.Action == ActionOpened || entry.Action == ActionReopened
}

// State is the last entry that claims to be a state of the session, which is
// what a host reads when a refusal for the same id is in the history: a
// refused duplicate open says the hub already held the id, and says nothing
// about the session that holds it.
func State(history []Entry) (Entry, bool) {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Action != ActionRefused {
			return history[i], true
		}
	}
	return Entry{}, false
}

type Record struct {
	SessionID       string   `json:"session_id"`
	Adapter         string   `json:"adapter"`
	HarnessVersion  string   `json:"harness_version,omitempty"`
	NativeSessionID string   `json:"native_session_id,omitempty"`
	Home            string   `json:"home,omitempty"`
	Directory       string   `json:"directory,omitempty"`
	Model           string   `json:"model,omitempty"`
	ToolSourceIDs   []string `json:"tool_source_ids,omitempty"`
}

type Entry struct {
	Action Action `json:"action"`
	TimeMS int64  `json:"time_ms"`
	Record Record `json:"record"`
}

type Store interface {
	Append(ctx context.Context, entry Entry) error
	Latest(ctx context.Context, sessionID string) (Entry, bool, error)
	History(ctx context.Context, sessionID string) ([]Entry, error)
}

func FromOpen(sessionID, adapter, harnessVersion, model, home, directory string, toolSourceIDs []string) Record {
	return Record{
		SessionID:      sessionID,
		Adapter:        adapter,
		HarnessVersion: harnessVersion,
		Home:           home,
		Directory:      directory,
		Model:          model,
		ToolSourceIDs:  append([]string(nil), toolSourceIDs...),
	}
}

func Reopened(previous Record, nativeSessionID string, timeMS int64) Entry {
	record := previous
	if nativeSessionID != "" {
		record.NativeSessionID = nativeSessionID
	}
	return Entry{Action: ActionReopened, TimeMS: timeMS, Record: record}
}

func Opened(record Record, timeMS int64) Entry {
	return Entry{Action: ActionOpened, TimeMS: timeMS, Record: record}
}

func Closed(record Record, timeMS int64) Entry {
	return Entry{Action: ActionClosed, TimeMS: timeMS, Record: record}
}

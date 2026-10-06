package binding

import (
	"context"
	"errors"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

var ErrTorn = errors.New("binding: a record was not written whole")

type Action string

const (
	ActionOpened   Action = "opened"
	ActionReopened Action = "reopened"
	ActionClosed   Action = "closed"
	ActionRefused  Action = "refused"
)

func Live(entry Entry) bool {
	return entry.Action == ActionOpened || entry.Action == ActionReopened
}

func State(history []Entry) (Entry, bool) {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Action == ActionRefused {
			continue
		}
		return history[i], true
	}
	return Entry{}, false
}

type Record struct {
	SessionID        string                     `json:"session_id"`
	Adapter          string                     `json:"adapter"`
	HarnessVersion   string                     `json:"harness_version,omitempty"`
	NativeSessionID  string                     `json:"native_session_id,omitempty"`
	Home             string                     `json:"home,omitempty"`
	Directory        string                     `json:"directory,omitempty"`
	Model            string                     `json:"model,omitempty"`
	ReasoningLevel   string                     `json:"reasoning_level,omitempty"`
	CompactionPolicy *protocol.CompactionPolicy `json:"compaction_policy,omitempty"`
	ToolSourceIDs    []string                   `json:"tool_source_ids,omitempty"`
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
	Sessions(ctx context.Context) ([]Entry, error)
}

func Sessions(entries []Entry) []Entry {
	latest := make(map[string]int)
	var states []Entry
	for _, entry := range entries {
		if entry.Action == ActionRefused {
			continue
		}
		if at, seen := latest[entry.Record.SessionID]; seen {
			states[at] = entry
			continue
		}
		latest[entry.Record.SessionID] = len(states)
		states = append(states, entry)
	}
	return states
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

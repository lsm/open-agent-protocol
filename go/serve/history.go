package serve

import (
	"context"
	"errors"

	"github.com/lsm/open-agent-protocol/go/binding"
)

var ErrNoSessionHistory = errors.New("serve: this hub keeps no session history")

type HistoryEntry struct {
	SessionID      string `json:"session_id"`
	Adapter        string `json:"adapter"`
	HarnessVersion string `json:"harness_version,omitempty"`
	State          string `json:"state"`
	UpdatedAtMS    int64  `json:"updated_at_ms"`
	Model          string `json:"model,omitempty"`
	Directory      string `json:"directory,omitempty"`
}

type HistoryPage struct {
	Sessions   []HistoryEntry `json:"sessions"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

func (h *Hub) SessionHistory(ctx context.Context, cursor string, limit int) (HistoryPage, error) {
	if h.bindings == nil {
		return HistoryPage{}, ErrNoSessionHistory
	}
	sessions, err := h.bindings.Sessions(ctx)
	if err != nil {
		return HistoryPage{}, err
	}
	page, err := binding.List(sessions, cursor, limit)
	if err != nil {
		return HistoryPage{}, err
	}
	result := HistoryPage{Sessions: make([]HistoryEntry, 0, len(page.Entries)), NextCursor: page.NextCursor}
	for _, entry := range page.Entries {
		state := "closed"
		if binding.Live(entry) {
			state = "live"
		}
		result.Sessions = append(result.Sessions, HistoryEntry{
			SessionID: entry.Record.SessionID, Adapter: entry.Record.Adapter,
			HarnessVersion: entry.Record.HarnessVersion, State: state, UpdatedAtMS: entry.TimeMS,
			Model: entry.Record.Model, Directory: entry.Record.Directory,
		})
	}
	return result, nil
}

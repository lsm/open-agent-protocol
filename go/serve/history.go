package serve

import (
	"context"
	"errors"

	"github.com/lsm/open-agent-protocol/go/binding"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

var ErrNoSessionHistory = errors.New("serve: this hub keeps no session history")

func (h *Hub) SessionHistory(ctx context.Context, cursor string, limit int) (protocol.SessionListResponse, error) {
	if h.bindings == nil {
		return protocol.SessionListResponse{}, ErrNoSessionHistory
	}
	sessions, err := h.bindings.Sessions(ctx)
	if err != nil {
		return protocol.SessionListResponse{}, err
	}
	page, err := binding.List(sessions, cursor, limit)
	if err != nil {
		return protocol.SessionListResponse{}, err
	}
	result := protocol.SessionListResponse{Sessions: make([]protocol.SessionListEntry, 0, len(page.Entries)), NextCursor: page.NextCursor}
	for _, entry := range page.Entries {
		state := protocol.SessionListClosed
		if binding.Live(entry) {
			state = protocol.SessionListLive
		}
		result.Sessions = append(result.Sessions, protocol.SessionListEntry{
			SessionID: protocol.SessionID(entry.Record.SessionID), Adapter: entry.Record.Adapter,
			HarnessVersion: entry.Record.HarnessVersion, State: state, UpdatedAtMS: entry.TimeMS,
			Model: entry.Record.Model, Directory: entry.Record.Directory,
		})
	}
	return result, nil
}

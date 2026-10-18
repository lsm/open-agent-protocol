package serve

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

func AskedShutdown(raw json.RawMessage) (protocol.ShutdownMode, bool) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", true
	}
	var asked struct {
		Mode *protocol.ShutdownMode `json:"mode"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&asked); err != nil {
		return "", false
	}
	if asked.Mode == nil {
		return "", true
	}
	switch *asked.Mode {
	case protocol.ShutdownGraceful, protocol.ShutdownForce:
		return *asked.Mode, true
	}
	return "", false
}

func (h *Hub) NonterminalRuns(ctx context.Context) []protocol.ShutdownRun {
	runs := []protocol.ShutdownRun{}
	for _, status := range h.Sessions(ctx) {
		for _, run := range status.ActiveRuns {
			runs = append(runs, protocol.ShutdownRun{SessionID: status.SessionID, RunID: run.RunID, Status: run.Status, PendingInteractions: run.PendingInteractions})
		}
		if len(status.ActiveRuns) > 0 || status.ActiveRunID == "" {
			continue
		}
		state := protocol.RunRunning
		switch status.Status {
		case protocol.SessionQueued:
			state = protocol.RunQueued
		case protocol.SessionWaitingForInput:
			state = protocol.RunWaitingForInput
		}
		runs = append(runs, protocol.ShutdownRun{SessionID: status.SessionID, RunID: status.ActiveRunID, Status: state})
	}
	return runs
}

func (h *Hub) CancelQueued(ctx context.Context, run protocol.ShutdownRun) bool {
	entry, err := h.Session(run.SessionID)
	if err != nil {
		return false
	}
	_, err = entry.Cancel(ctx, run.RunID)
	return err == nil
}

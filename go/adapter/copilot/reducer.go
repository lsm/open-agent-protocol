package copilot

import (
	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/copilot/internal/native"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type run struct {
	id                protocol.RunID
	messageID         protocol.MessageID
	nativeMessage     string
	status            protocol.RunStatus
	started, terminal bool
	next              uint64
	parts             []protocol.ContentPart
}
type reducer struct {
	sessionID protocol.SessionID
	nativeID  string
	ids       base.IDGenerator
	clock     base.Clock
	runs      []*run
	current   *run
	events    []protocol.Envelope
}

func newReducer(sessionID protocol.SessionID, nativeID string, ids base.IDGenerator, clock base.Clock) *reducer {
	return &reducer{sessionID: sessionID, nativeID: nativeID, ids: ids, clock: clock}
}
func (*reducer) admit(bool) (*run, error)   { return nil, base.ErrRunActive }
func (*reducer) observe(native.Event) error { return nil }

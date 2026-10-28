package copilot

import (
	"context"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/copilot/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type session struct {
	reducer *reducer
	process *rpc.Process
}

func newSession(r *reducer, p *rpc.Process, capacity int, timeout time.Duration) *session {
	return &session{reducer: r, process: p}
}
func (*session) State(context.Context) (protocol.SessionState, error) {
	return protocol.SessionState{}, base.ErrSessionClosed
}
func (*session) Resume(context.Context, base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	return base.Recovery{}, nil, base.ErrSessionClosed
}
func (s *session) Close(context.Context) error { return s.process.Close() }

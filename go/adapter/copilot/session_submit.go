package copilot

import (
	"context"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

var _ base.Session = (*session)(nil)

func (*session) Submit(context.Context, base.SubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	return protocol.MessageSubmitResponse{}, nil, base.ErrInvalidSubmission
}
func (*session) Resolve(context.Context, base.InteractionResolution) error {
	return base.ErrInteractionNotFound
}
func (*session) Cancel(context.Context, protocol.RunID) (protocol.RunCancelResponse, error) {
	return protocol.RunCancelResponse{}, &base.UnsupportedControlError{Feature: "run.cancel", Reason: base.ControlUnadvertised}
}

package opencode

import (
	"context"
	"errors"
	"fmt"
	"strings"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const reopenSupportReason = "GET /api/session/:id attaches to the bound server session and follows its events from the attach on; an unknown or running session is refused"
const reopenRecoveryReason = "OpenCode attached to the bound server session and reports the model it records; OAP runs and cursors remain process-local"

func (s *session) NativeSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.nativeID)
}

func reopenRefusal(err error) error {
	return &base.UnsupportedControlError{Feature: protocol.FeatureOpenReopen, Reason: base.ControlUnsatisfiable, Detail: fmt.Sprintf("OpenCode could not attach its binding: %v", err)}
}

func reloadBinding(ctx context.Context, client Client, bound string) (native.SessionInfo, error) {
	if strings.TrimSpace(bound) == "" {
		return native.SessionInfo{}, errors.New("the binding names no session")
	}
	id := native.SessionID(bound)
	info, err := client.Session(ctx, id)
	if err != nil {
		var missing *native.APIError
		if errors.As(err, &missing) && missing.Tag == "SessionNotFoundError" {
			return info, fmt.Errorf("the server holds no session %s", bound)
		}
		return info, err
	}
	active, err := client.Active(ctx)
	if err != nil {
		return info, err
	}
	if active[id] {
		return info, fmt.Errorf("session %s is still running on the server", bound)
	}
	return info, nil
}

func (s *session) restoreState() {
	s.state.Recovery = &protocol.RecoveryMetadata{Recovered: true, Reason: reopenRecoveryReason}
}

var _ base.NativeSession = (*session)(nil)

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

const reopenSupportReason = "GET /api/session/:id attaches to the bound server session and its events resume after the last stored sequence; an unknown or running session is refused"
const reopenRecoveryReason = "OpenCode attached to the bound server session and reports the model it records; OAP runs and cursors remain process-local"

const historyPageLimit = 100

func (s *session) NativeSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.nativeID)
}

func reopenRefusal(err error) error {
	return &base.UnsupportedControlError{Feature: protocol.FeatureOpenReopen, Reason: base.ControlUnsatisfiable, Detail: fmt.Sprintf("OpenCode could not attach its binding: %v", err)}
}

func reloadBinding(ctx context.Context, client Client, bound string) (native.SessionInfo, int64, error) {
	if strings.TrimSpace(bound) == "" {
		return native.SessionInfo{}, -1, errors.New("the binding names no session")
	}
	id := native.SessionID(bound)
	info, err := client.Session(ctx, id)
	if err != nil {
		var missing *native.APIError
		if errors.As(err, &missing) && missing.Tag == "SessionNotFoundError" {
			return info, -1, fmt.Errorf("the server holds no session %s", bound)
		}
		return info, -1, err
	}
	active, err := client.Active(ctx)
	if err != nil {
		return info, -1, err
	}
	if active[id] {
		return info, -1, fmt.Errorf("session %s is still running on the server", bound)
	}
	last, err := lastDurableSeq(ctx, client, id)
	if err != nil {
		return info, -1, err
	}
	return info, last, nil
}

func lastDurableSeq(ctx context.Context, client Client, id native.SessionID) (int64, error) {
	var last int64
	for {
		page, err := client.History(ctx, id, last, historyPageLimit)
		if err != nil {
			return 0, err
		}
		advanced := false
		for _, event := range page.Events {
			if event.Durable != nil && event.Durable.Seq > last {
				last, advanced = event.Durable.Seq, true
			}
		}
		if !page.HasMore || !advanced {
			return last, nil
		}
	}
}

func (s *session) restoreState(last int64) {
	s.state.Recovery = &protocol.RecoveryMetadata{Recovered: true, Reason: reopenRecoveryReason}
	if last > 0 {
		s.lastSeq = last
		s.state.TranscriptCursor = formatSeq(last)
	}
}

var _ base.NativeSession = (*session)(nil)

package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/hermes/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/hermes/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const reopenSupportReason = "session.resume reloads the bound stored session; one Hermes would auto-continue after a crash is refused and its process ended, taking the scheduled turn with it"
const reopenRecoveryReason = "Hermes restored the bound conversation and reports the model it last ran under; OAP runs and cursors remain process-local"

const hermesSessionNotFound = 4007

var errPendingAutoContinue = errors.New("Hermes would restart the turn a crash interrupted, which a reopen must not do; open it in Hermes to settle the turn, or wait for Hermes to retire the marker")

type reopeningFactory interface {
	Reopen(context.Context, string) (Client, native.SessionResumeResult, error)
}

type storedSession interface {
	storedSessionID() string
}

func storedSessionID(client Client) string {
	if stored, ok := client.(storedSession); ok {
		return stored.storedSessionID()
	}
	return ""
}

func (s *Session) NativeSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.storedID
}

func reopenRefusal(err error) error {
	return &base.UnsupportedControlError{Feature: protocol.FeatureOpenReopen, Reason: base.ControlUnsatisfiable, Detail: fmt.Sprintf("Hermes could not reload its binding: %v", err)}
}

func reopenBinding(ctx context.Context, factory ClientFactory, stored string) (Client, native.SessionResumeResult, error) {
	if strings.TrimSpace(stored) == "" {
		return nil, native.SessionResumeResult{}, reopenRefusal(errors.New("the binding names no stored session"))
	}
	reopening, ok := factory.(reopeningFactory)
	if !ok {
		return nil, native.SessionResumeResult{}, reopenRefusal(errors.New("this client factory cannot reload a stored session"))
	}
	client, resumed, err := reopening.Reopen(ctx, stored)
	if err != nil {
		if ctx.Err() != nil {
			return nil, resumed, ctx.Err()
		}
		return nil, resumed, reopenRefusal(err)
	}
	return client, resumed, nil
}

func resumeStored(ctx context.Context, client Client, stored string) (native.SessionResumeResult, error) {
	var resumed native.SessionResumeResult
	if err := client.Call(ctx, native.MethodSessionResume, native.SessionResumeParams{SessionID: stored}, &resumed); err != nil {
		var remote *rpc.RemoteError
		if errors.As(err, &remote) && remote.Object.Code == hermesSessionNotFound {
			return resumed, fmt.Errorf("Hermes has no stored session %q", stored)
		}
		return resumed, err
	}
	if resumed.SessionID == "" {
		return resumed, errors.New("Hermes omitted the reloaded session id")
	}
	if pendingAutoContinue(resumed.AutoContinue) {
		return resumed, errPendingAutoContinue
	}
	if resumed.Running || (resumed.Status != "" && resumed.Status != "idle") {
		return resumed, fmt.Errorf("Hermes reattached the stored session while it was %s", resumedStatus(resumed))
	}
	return resumed, nil
}

func pendingAutoContinue(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}

func resumedStatus(resumed native.SessionResumeResult) string {
	if resumed.Running {
		return "running"
	}
	return resumed.Status
}

func (s *Session) restoreState(resumed native.SessionResumeResult) {
	s.state.Recovery = &protocol.RecoveryMetadata{Recovered: true, Reason: reopenRecoveryReason}
	var info native.SessionInfoPayload
	if len(resumed.Info) > 0 && json.Unmarshal(resumed.Info, &info) == nil && info.Model != "" {
		s.state.CurrentModelID = info.Model
	}
}

var _ base.NativeSession = (*Session)(nil)

package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/pi/internal/native"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const reopenSupportReason = "switch_session loads the bound session file; an absent, empty or mismatched file is refused"
const reopenRecoveryReason = "Pi restored the bound conversation and reports the current model, thinking level and compaction switch; OAP runs and cursors remain process-local"

type sessionBinding struct {
	SessionID   string `json:"sessionId"`
	SessionFile string `json:"sessionFile"`
}

func (s *Session) NativeSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nativeBinding
}

func encodeBinding(state native.SessionState) string {
	if state.SessionID == "" || !filepath.IsAbs(state.SessionFile) {
		return ""
	}
	raw, _ := json.Marshal(sessionBinding{SessionID: state.SessionID, SessionFile: state.SessionFile})
	return string(raw)
}

func readBinding(raw string) (sessionBinding, error) {
	var binding sessionBinding
	if err := native.DecodeStrict(json.RawMessage(raw), &binding); err != nil {
		return binding, err
	}
	if binding.SessionID == "" || !filepath.IsAbs(binding.SessionFile) {
		return binding, errors.New("binding omitted Pi's session id or absolute file")
	}
	info, err := os.Stat(binding.SessionFile)
	if err != nil {
		return binding, err
	}
	if !info.Mode().IsRegular() {
		return binding, errors.New("bound session file is not a regular file")
	}
	file, err := os.Open(binding.SessionFile)
	if err != nil {
		return binding, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return binding, err
	}
	if !stat.Mode().IsRegular() {
		return binding, errors.New("bound session file is not a regular file")
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), (64<<10)+1)
	if !scanner.Scan() {
		return binding, errors.New("bound session file has no readable header")
	}
	if len(scanner.Bytes()) > 64<<10 {
		return binding, errors.New("bound session header exceeds limit")
	}
	var header struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	var fields map[string]json.RawMessage
	if err := native.DecodeStrict(scanner.Bytes(), &fields); err != nil {
		return binding, err
	}
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return binding, err
	}
	if header.Type != "session" || header.ID != binding.SessionID {
		return binding, errors.New("bound session header does not match its recorded id")
	}
	return binding, nil
}

func reopenRefusal(err error) error {
	return &base.UnsupportedControlError{Feature: protocol.FeatureOpenReopen, Reason: base.ControlUnsatisfiable, Detail: fmt.Sprintf("Pi could not load its binding: %v", err)}
}

func reopenSession(ctx context.Context, client Client, binding sessionBinding) (native.SessionState, error) {
	var switched struct {
		Cancelled *bool `json:"cancelled"`
	}
	if err := openingCall(ctx, client, native.Command{Type: native.CommandSwitchSession, SessionPath: &binding.SessionFile}, &switched); err != nil {
		return native.SessionState{}, err
	}
	if switched.Cancelled == nil || *switched.Cancelled {
		return native.SessionState{}, errors.New("Pi did not confirm the session switch")
	}
	var state native.SessionState
	if err := openingCall(ctx, client, native.Command{Type: native.CommandGetState}, &state); err != nil {
		return state, err
	}
	if err := validateState(state); err != nil {
		return state, err
	}
	if state.IsStreaming || state.IsCompacting || state.SessionID != binding.SessionID || state.SessionFile != binding.SessionFile {
		return state, errors.New("Pi did not load the idle bound session")
	}
	return state, nil
}

func openingCall(ctx context.Context, client Client, command native.Command, result any) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	type reply struct {
		raw json.RawMessage
		err error
	}
	replies := make(chan reply, 1)
	go func() { var raw json.RawMessage; err := client.Call(ctx, command, &raw); replies <- reply{raw, err} }()
	for {
		select {
		case r := <-replies:
			if r.err != nil {
				return r.err
			}
			if len(r.raw) == 0 || strings.TrimSpace(string(r.raw)) == "null" {
				return errors.New("Pi omitted the reload response")
			}
			if command.Type == native.CommandGetState {
				var fields map[string]json.RawMessage
				if err := native.DecodeStrict(r.raw, &fields); err != nil {
					return err
				}
				for _, key := range []string{"isStreaming", "isCompacting", "autoCompactionEnabled"} {
					var value *bool
					if err := json.Unmarshal(fields[key], &value); err != nil || value == nil {
						return fmt.Errorf("Pi omitted or malformed %s", key)
					}
				}
			}
			return native.DecodeStrict(r.raw, result)
		case inbound, ok := <-client.Inbound():
			if !ok {
				return errors.New("Pi closed during reload")
			}
			if inbound.Barrier != nil {
				close(inbound.Barrier)
			}
		case <-client.Done():
			return errors.New("Pi closed during reload")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

var _ base.NativeSession = (*Session)(nil)

func (s *Session) restoreState(initial native.SessionState) {
	s.state.Recovery = &protocol.RecoveryMetadata{Recovered: true, Reason: reopenRecoveryReason}
	s.reportsLevel = true
	s.state.ReasoningLevel = protocol.ReasoningLevel(initial.ThinkingLevel)
	kind := protocol.CompactionOff
	if initial.AutoCompactionEnabled {
		kind = protocol.CompactionAuto
	}
	s.state.CompactionPolicy = &protocol.CompactionPolicy{Kind: kind}
}

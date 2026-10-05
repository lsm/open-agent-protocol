package claude

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const reopenReason = "Claude Code restored the conversation; model, effort and compaction settings belong to the loader, not the stored session"

func reopenRefusal(detail string) error {
	return &base.UnsupportedControlError{Feature: protocol.FeatureOpenReopen, Reason: base.ControlUnsatisfiable, Detail: detail}
}

func validSessionUUID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func hasSessionSelector(args []string) bool {
	for _, arg := range args {
		key, _, _ := strings.Cut(arg, "=")
		switch key {
		case "--", "--resume", "-r", "--continue", "-c", "--fork-session", "--session-id":
			return true
		}
	}
	return false
}

func nativeSessionUUID() string {
	var bytes [16]byte
	_, _ = rand.Read(bytes[:])
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:])
}

func (a *Adapter) start(ctx context.Context, req base.OpenRequest) (Client, string, error) {
	if req.Reopen && !validSessionUUID(req.NativeSessionID) {
		return nil, "", reopenRefusal("the binding must name a Claude Code session UUID")
	}
	if a.processConfig == nil {
		if req.Reopen {
			return nil, "", reopenRefusal("the supplied client factory cannot select a bound CLI conversation")
		}
		client, err := a.config.Factory.Start(ctx)
		return client, "", err
	}
	selected := hasSessionSelector(a.config.Args)
	if req.Reopen && selected {
		return nil, "", reopenRefusal("configured session selection conflicts with the binding")
	}
	pc := *a.processConfig
	id := ""
	if req.Reopen {
		id = req.NativeSessionID
		pc.Args = append(append([]string{}, pc.Args...), "--resume", id)
	} else if !selected {
		id = nativeSessionUUID()
		boundary := len(pc.Args) - len(a.config.Args)
		pc.Args = append(append(append([]string{}, pc.Args[:boundary]...), "--session-id", id), a.config.Args...)
	}
	process, err := a.config.ProcessFactory.Start(ctx, pc)
	if err != nil {
		return nil, "", err
	}
	return &sessionClient{Client: process.ClientHandle(), bridge: process}, id, nil
}

func (s *Session) NativeSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nativeSessionID
}

var _ base.NativeSession = (*Session)(nil)

func (s *Session) reopened(ctx context.Context, id string) error {
	var settings struct {
		Applied struct {
			Model  string                  `json:"model"`
			Effort protocol.ReasoningLevel `json:"effort"`
		} `json:"applied"`
		Effective struct {
			AutoCompactEnabled *bool  `json:"autoCompactEnabled"`
			AutoCompactWindow  *int64 `json:"autoCompactWindow"`
		} `json:"effective"`
	}
	settingsCtx, cancel := context.WithTimeout(ctx, initializeTimeout)
	defer cancel()
	if err := s.client.Call(settingsCtx, map[string]any{"subtype": "get_settings"}, &settings); err != nil {
		return fmt.Errorf("claude adapter: get_settings after resume: %w", err)
	}
	if settings.Applied.Model == "" {
		return fmt.Errorf("%w: get_settings returned no applied model", ErrNativeProtocol)
	}
	level := settings.Applied.Effort
	switch level {
	case protocol.ReasoningLow, protocol.ReasoningMedium, protocol.ReasoningHigh, protocol.ReasoningXHigh, protocol.ReasoningMax:
	default:
		level = ""
	}
	policy := &protocol.CompactionPolicy{Kind: protocol.CompactionAuto}
	if enabled := settings.Effective.AutoCompactEnabled; enabled != nil && !*enabled {
		policy.Kind = protocol.CompactionOff
	} else if window := settings.Effective.AutoCompactWindow; window != nil && *window > 0 {
		policy.Kind = protocol.CompactionTokens
		policy.Tokens = *window
	}
	encoded, _ := json.Marshal(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nativeSessionID != "" && s.nativeSessionID != id {
		return fmt.Errorf("%w: resume selected a different session", ErrNativeProtocol)
	}
	s.nativeSessionID = id
	s.state.Metadata = map[string]json.RawMessage{"claude_native_session_id": encoded}
	s.state.CurrentModelID = settings.Applied.Model
	s.state.ReasoningLevel = level
	s.state.CompactionPolicy = policy
	s.state.Recovery = &protocol.RecoveryMetadata{Recovered: true, Reason: reopenReason}
	return nil
}

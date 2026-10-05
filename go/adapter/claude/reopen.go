package claude

import (
	"context"
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

func (a *Adapter) start(ctx context.Context, req base.OpenRequest) (Client, error) {
	if !req.Reopen {
		return a.config.Factory.Start(ctx)
	}
	if !validSessionUUID(req.NativeSessionID) {
		return nil, reopenRefusal("the binding must name a Claude Code session UUID")
	}
	if a.processConfig == nil {
		return nil, reopenRefusal("the supplied client factory cannot select a bound CLI conversation")
	}
	for _, arg := range a.config.Args {
		key, _, _ := strings.Cut(arg, "=")
		switch key {
		case "--", "--resume", "-r", "--continue", "-c", "--fork-session", "--session-id":
			return nil, reopenRefusal("configured session selection conflicts with the binding")
		}
	}
	pc := *a.processConfig
	pc.Args = append(append([]string{}, pc.Args...), "--resume", req.NativeSessionID)
	process, err := a.config.ProcessFactory.Start(ctx, pc)
	if err != nil {
		return nil, err
	}
	return &sessionClient{Client: process.ClientHandle(), bridge: process}, nil
}

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

package acp

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const reopenSupportReason = "session/load when loadSession is advertised, otherwise session/resume when advertised; agents offering neither refuse the binding"
const reopenReason = "ACP restored the bound conversation; configOptions reports the agent's current settings, and omitted settings remain the loader's configuration"

func reopenRefusal(detail string) error {
	return &base.UnsupportedControlError{Feature: protocol.FeatureOpenReopen, Reason: base.ControlUnsatisfiable, Detail: detail}
}

func reopenMethod(capabilities rpc.AgentCapabilities) string {
	var load bool
	if json.Unmarshal(capabilities["loadSession"], &load) == nil && load {
		return native.MethodSessionLoad
	}
	var session struct {
		Resume json.RawMessage `json:"resume"`
	}
	if json.Unmarshal(capabilities["sessionCapabilities"], &session) == nil {
		var offered map[string]json.RawMessage
		if json.Unmarshal(session.Resume, &offered) == nil && offered != nil {
			return native.MethodSessionResume
		}
	}
	return ""
}

func callReopen(ctx context.Context, client Client, method string, params any, result any) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	inbound := client.Inbound()
	done := make(chan error, 1)
	go func() { done <- client.Call(ctx, method, params, result) }()
	for {
		select {
		case err := <-done:
			return err
		case message, ok := <-inbound:
			if !ok {
				return rpc.ErrClosed
			}
			if message.Barrier != nil {
				close(message.Barrier)
			}
			if message.Request != nil {
				if err := message.Request.RespondError(ctx, -32601, "method not supported during session reload", nil); err != nil {
					return err
				}
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func resumedSettings(options []native.ConfigOption) (string, protocol.ReasoningLevel) {
	var model string
	var level protocol.ReasoningLevel
	for _, option := range options {
		if option.Category == "model" && option.CurrentValue != "" && option.CurrentValue != "default" && option.CurrentValue != "current" {
			model = option.CurrentValue
		}
		if option.Category != native.CategoryThoughtLevel {
			continue
		}
		if parsed := resumedLevel(option.CurrentValue); parsed != "" {
			level = parsed
			continue
		}
		for _, value := range option.Values() {
			if value.Value == option.CurrentValue {
				if parsed := resumedLevel(value.Name); parsed != "" {
					level = parsed
				}
				break
			}
		}
	}
	return model, level
}

func resumedLevel(value string) protocol.ReasoningLevel {
	switch level := protocol.ReasoningLevel(strings.ToLower(value)); level {
	case protocol.ReasoningOff, protocol.ReasoningLow, protocol.ReasoningMedium, protocol.ReasoningHigh, protocol.ReasoningXHigh, protocol.ReasoningMax:
		return level
	}
	return ""
}

func (s *session) NativeSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nativeID
}

var _ base.NativeSession = (*session)(nil)

type openingClient struct{ Client }

func (c openingClient) Call(ctx context.Context, method string, params, result any) error {
	return callReopen(ctx, c.Client, method, params, result)
}

package acp

import (
	"context"
	"errors"
	"fmt"
	"slices"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func usableModel(value string) bool {
	return value != "" && value != "default" && value != "current"
}

func (s *session) recordModels(options []native.ConfigOption) {
	for _, option := range options {
		if option.Category != "model" {
			continue
		}
		s.models = option.Values()
		s.modelOption = option.ID
		s.offersModels = true
		if s.state.CurrentModelID == "" && usableModel(option.CurrentValue) {
			s.state.CurrentModelID = option.CurrentValue
		}
		return
	}
}

func (s *session) Models(ctx context.Context, request protocol.ModelsRequest) (base.Catalog, error) {
	if err := ctx.Err(); err != nil {
		return base.Catalog{}, err
	}
	if !request.AllowsDegraded(protocol.FeatureModelsList) {
		return base.Catalog{}, &base.DegradedControlError{Feature: protocol.FeatureModelsList}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return base.Catalog{}, base.ErrSessionClosed
	}
	if request.SessionID != "" && request.SessionID != s.state.SessionID {
		return base.Catalog{}, base.ErrRunNotFound
	}
	if !s.offersModels {
		return base.Catalog{}, &base.UnsupportedControlError{Feature: protocol.FeatureModelsList, Reason: base.ControlUnsatisfiable, Detail: "the ACP agent reports no configOption whose category is model"}
	}
	catalog := protocol.ModelsResponse{SessionID: s.state.SessionID, CurrentModelID: s.state.CurrentModelID, Models: []protocol.ModelDescriptor{}}
	for _, value := range s.models {
		catalog.Models = append(catalog.Models, protocol.ModelDescriptor{ID: value.Value, DisplayName: value.Name, Default: value.Value == s.state.CurrentModelID})
	}
	return base.Catalog{Revision: CapabilityRevision, Models: catalog}, nil
}

func (s *session) SwitchModel(ctx context.Context, request protocol.SessionModelSwitchRequest) (protocol.SessionModelSwitchResponse, protocol.SessionState, error) {
	if err := ctx.Err(); err != nil {
		return protocol.SessionModelSwitchResponse{}, protocol.SessionState{}, err
	}
	if !request.AllowsDegraded(protocol.FeatureSessionModelSwitch) {
		return protocol.SessionModelSwitchResponse{}, protocol.SessionState{}, &base.DegradedControlError{Feature: protocol.FeatureSessionModelSwitch}
	}
	refuse := func(detail string) error {
		return &base.UnsupportedControlError{Feature: protocol.FeatureSessionModelSwitch, Reason: base.ControlUnsatisfiable, Field: "model_id", Detail: detail}
	}
	s.switchMu.Lock()
	defer s.switchMu.Unlock()
	s.mu.Lock()
	switch {
	case s.closed:
		s.mu.Unlock()
		return protocol.SessionModelSwitchResponse{}, protocol.SessionState{}, base.ErrSessionClosed
	case request.SessionID != s.state.SessionID:
		s.mu.Unlock()
		return protocol.SessionModelSwitchResponse{}, protocol.SessionState{}, fmt.Errorf("%w: switch names session %q", base.ErrInvalidSubmission, request.SessionID)
	case !s.offersModels:
		s.mu.Unlock()
		return protocol.SessionModelSwitchResponse{}, protocol.SessionState{}, &base.UnsupportedControlError{Feature: protocol.FeatureSessionModelSwitch, Reason: base.ControlUnsatisfiable, Detail: "the ACP agent reports no configOption whose category is model"}
	case !slices.ContainsFunc(s.models, func(v native.ConfigValue) bool { return v.Value == request.ModelID }):
		s.mu.Unlock()
		return protocol.SessionModelSwitchResponse{}, protocol.SessionState{}, &base.ModelNotFoundError{ModelID: request.ModelID}
	}
	option := s.modelOption
	s.mu.Unlock()
	var set native.SetConfigOptionResult
	if err := s.client.Call(ctx, native.MethodSessionSetConfigOption, native.SetConfigOptionParams{SessionID: s.nativeID, ConfigID: option, Value: request.ModelID}, &set); err != nil {
		var remote *rpc.RemoteError
		if !errors.As(err, &remote) {
			return protocol.SessionModelSwitchResponse{}, protocol.SessionState{}, err
		}
		return protocol.SessionModelSwitchResponse{}, protocol.SessionState{}, refuse("the agent refused its model option: " + remote.Object.Message)
	}
	if confirmed, ok := configuredOption(set.ConfigOptions, option); !ok || confirmed.CurrentValue != request.ModelID {
		return protocol.SessionModelSwitchResponse{}, protocol.SessionState{}, refuse("the agent did not confirm its model option")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.state.CurrentModelID
	_, s.state.ReasoningLevel = resumedSettings(set.ConfigOptions)
	s.state.CurrentModelID = request.ModelID
	s.state.UpdatedAtMS = s.clock.Now().UnixMilli()
	return protocol.SessionModelSwitchResponse{SessionID: s.state.SessionID, ModelID: request.ModelID, PreviousModelID: previous}, s.state, nil
}

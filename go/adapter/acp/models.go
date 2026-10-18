package acp

import (
	"context"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/native"
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

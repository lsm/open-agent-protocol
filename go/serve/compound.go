package serve

import (
	"context"
	"github.com/lsm/open-agent-protocol/go/binding"
	"slices"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type CompoundOpen struct {
	Subscribe bool
	Stream    context.Context
	Message   *protocol.OpenMessage
	RequestID protocol.EnvelopeID
}

type CompoundResult struct {
	Session      *Session
	State        protocol.SessionState
	Subscription *Subscription
	Admission    *protocol.MessageSubmitResponse
}

func ElectionGate(ctx context.Context, hub *Hub, name string, revision string, request protocol.SessionOpenRequest) (string, error) {
	var elected []string
	if request.Subscribe {
		elected = append(elected, protocol.FeatureOpenSubscribe)
	}
	if request.Reopen {
		elected = append(elected, protocol.FeatureOpenReopen)
	}
	settings := base.OpenSettingKeys(request.ReasoningLevel, request.CompactionPolicy)
	elected = append(elected, settings...)
	if len(elected) == 0 {
		return "", nil
	}
	descriptor, err := hub.Probe(ctx, name)
	if err != nil {
		return "", err
	}
	if revision != "" && revision != descriptor.CapabilityRevision {
		return "", &StaleRevisionError{Expected: descriptor.CapabilityRevision, Current: revision}
	}
	for _, key := range elected {
		support, advertised := descriptor.Capabilities.EffectiveSupport(key)
		if !advertised || support.Level == "" || support.Level == protocol.SupportUnavailable {
			return "", &base.UnsupportedControlError{Feature: key, Reason: base.ControlUnadvertised}
		}
		if slices.Contains(settings, key) && !support.DisclosesMode(protocol.ModeSessionOpen) {
			return "", &base.UnsupportedControlError{Feature: key, Reason: base.ControlUnadvertised, Field: base.OpenSettingField(key)}
		}
		if support.Level == protocol.SupportDegraded && !request.AllowsDegraded(key) {
			return "", &base.DegradedControlError{Feature: key}
		}
	}
	return descriptor.CapabilityRevision, nil
}

func OpenCompound(ctx context.Context, hub *Hub, name string, open base.OpenRequest, compound CompoundOpen) (CompoundResult, error) {
	entry, state, err := hub.Open(ctx, name, open)
	if err != nil {
		return CompoundResult{}, err
	}
	result := CompoundResult{Session: entry, State: state}
	if compound.Subscribe {
		stream := compound.Stream
		if stream == nil {
			stream = ctx
		}
		subscription, err := hub.Subscribe(stream, entry.ID())
		if err != nil {
			rollbackCompound(hub, entry)
			return CompoundResult{}, err
		}
		result.Subscription = subscription
	}
	if compound.Message == nil {
		return result, nil
	}
	admission, err := entry.Submit(ctx, base.SubmitRequest{Request: compound.Message.Submit(entry.ID()), EnvelopeID: compound.RequestID})
	if err != nil {
		if result.Subscription != nil {
			result.Subscription.Close()
		}
		rollbackCompound(hub, entry)
		return CompoundResult{}, err
	}
	result.Admission = &admission
	result.State = withAdmittedRun(state, admission, compound.RequestID)
	return result, nil
}

func Rollback(ctx context.Context, hub *Hub, entry *Session) error {
	err := entry.Close(ctx)
	hub.bindingMu.Lock()
	defer hub.bindingMu.Unlock()
	hub.sessions.remove(entry.ID(), entry)
	if err != nil {
		hub.recordBinding(ctx, entry.binding, binding.ActionClosed, hub.now())
	}
	return err
}

func rollbackCompound(hub *Hub, entry *Session) {
	rollback, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), DefaultShutdownTimeout)
	defer cancel()
	_ = Rollback(rollback, hub, entry)
}

func withAdmittedRun(state protocol.SessionState, admission protocol.MessageSubmitResponse, request protocol.EnvelopeID) protocol.SessionState {
	if admission.RunID == "" {
		return state
	}
	entry := protocol.ActiveRun{
		RunID:        admission.RunID,
		Status:       admission.Status,
		Relationship: protocol.RelationshipPrimary,
	}
	if request != "" {
		entry.AdmittedSubmitRequests = []protocol.EnvelopeID{request}
	}
	if admission.Status == protocol.RunQueued {
		position := 1
		for _, existing := range state.ActiveRuns {
			if existing.Status == protocol.RunQueued {
				position++
			}
		}
		entry.QueuePosition = &position
	}
	state.ActiveRuns = append(append([]protocol.ActiveRun(nil), state.ActiveRuns...), entry)
	if admission.Status != protocol.RunQueued {
		state.ActiveRunID = admission.RunID
		state.Status = protocol.SessionRunning
	} else if state.Status == protocol.SessionIdle {
		state.Status = protocol.SessionQueued
	}
	return state
}

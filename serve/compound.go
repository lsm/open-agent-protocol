package serve

import (
	"context"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
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

func SubscribeGate(ctx context.Context, hub *Hub, name string, revision string, request protocol.SessionOpenRequest) (string, error) {
	if !request.Subscribe {
		return "", nil
	}
	descriptor, err := hub.Probe(ctx, name)
	if err != nil {
		return "", err
	}
	if revision != "" && revision != descriptor.CapabilityRevision {
		return "", &StaleRevisionError{Expected: descriptor.CapabilityRevision, Current: revision}
	}
	support, advertised := descriptor.Capabilities.EffectiveSupport(protocol.FeatureOpenSubscribe)
	if !advertised || support.Level == "" || support.Level == protocol.SupportUnavailable {
		return "", &base.UnsupportedControlError{Feature: protocol.FeatureOpenSubscribe, Reason: base.ControlUnadvertised}
	}
	if support.Level == protocol.SupportDegraded && !request.AllowsDegraded(protocol.FeatureOpenSubscribe) {
		return "", &base.DegradedControlError{Feature: protocol.FeatureOpenSubscribe}
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
	admission, err := entry.Submit(ctx, compound.Message.Submit(entry.ID()))
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
	hub.sessions.remove(entry.ID())
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

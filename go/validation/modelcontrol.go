package validation

import (
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type switchObservation struct {
	model    string
	seen     bool
	accepted bool
	response protocol.Envelope
	index    int
	line     int
}

func (s *state) modelSwitchRequest(i, line int, e protocol.Envelope) {
	var p protocol.SessionModelSwitchRequest
	_ = e.DecodePayload(&p)
	s.checkScope(i, line, e, p.SessionID, "")
	st := s.track(p.SessionID)
	if st.switchObservations == nil {
		st.switchObservations = map[protocol.EnvelopeID]*switchObservation{}
	}
	st.switchObservations[e.ID] = &switchObservation{
		model: p.ModelID,
		seen:  st.currentKnown && st.currentModel == p.ModelID,
	}
}

func (s *state) modelSwitchResponse(i, line int, e protocol.Envelope) {
	var p protocol.SessionModelSwitchResponse
	_ = e.DecodePayload(&p)
	s.checkScope(i, line, e, p.SessionID, "")
	req := s.requests[e.InReplyTo]
	if req == nil {
		return
	}
	var requested protocol.SessionModelSwitchRequest
	_ = req.envelope.DecodePayload(&requested)
	if p.ModelID != requested.ModelID {
		s.addExpected(CodeUnappliedControl, i, line, e, "/payload/model_id", "model switch response did not apply the requested model", requested.ModelID, p.ModelID, string(e.InReplyTo))
		return
	}
	st := s.track(p.SessionID)
	if st.catalog.binds(s.currentCapability) && !st.catalog.ids[p.ModelID] {
		s.addExpected(CodeModelNotInCatalog, i, line, e, "/payload/model_id", "model switch accepted an id absent from the session catalog", "a listed model id", p.ModelID, string(e.InReplyTo))
	}
	if obs := st.switchObservations[e.InReplyTo]; obs != nil {
		obs.accepted = true
		obs.response = e
		obs.index = i
		obs.line = line
	}
	st.currentModel, st.currentKnown = p.ModelID, true
	st.mutated = true
	st.guardDefault = false
	s.observeModelSwitch(p.SessionID, p.ModelID, e.InReplyTo)
}

func (s *state) observeSwitchState(st *sessionTrack, currentModel string) {
	for _, obs := range st.switchObservations {
		if obs.model == currentModel {
			obs.seen = true
		}
	}
}

func (s *state) closeSwitchObservations() {
	for _, st := range s.sessions {
		for id, obs := range st.switchObservations {
			if obs.accepted && !obs.seen {
				s.addExpected(CodeSessionStateMismatch, obs.index, obs.line, obs.response, "/payload/model_id", "accepted model switch was not reflected in session.state.updated", obs.model, "no matching state update", string(id))
			}
		}
	}
}

func (s *state) checkProviderAttachModes(i, line int, e protocol.Envelope, p protocol.CapabilitiesResponse) {
	support, ok := p.EffectiveSupport(protocol.FeatureProvidersAttach)
	if !ok || !affirmative(support.Level) || support.DisclosesMode(protocol.ModeSessionLive) {
		return
	}
	s.addExpected(CodeUndisclosedAttachModes, i, line, e, "/payload/features/action.providers.attach/modes", "action.providers.attach is advertised without the session_live mode", protocol.ModeSessionLive, describeModes(support.Modes))
}

func (s *state) providerAttachRequest(i, line int, e protocol.Envelope) {
	var p protocol.SessionProviderAttachRequest
	_ = e.DecodePayload(&p)
	s.checkScope(i, line, e, p.SessionID, "")
}

func (s *state) providerAttachResponse(i, line int, e protocol.Envelope) {
	var p protocol.SessionProviderAttachResponse
	_ = e.DecodePayload(&p)
	s.checkScope(i, line, e, p.SessionID, "")
	req := s.requests[e.InReplyTo]
	if req == nil {
		return
	}
	var requested protocol.SessionProviderAttachRequest
	_ = req.envelope.DecodePayload(&requested)
	if p.ProviderID != requested.Provider.ID {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/provider_id", "attach response must name the session-local provider alias the caller requested", requested.Provider.ID, p.ProviderID, string(e.InReplyTo))
		return
	}
	level := s.features[protocol.FeatureProvidersAttach]
	support := s.featureSupports[protocol.FeatureProvidersAttach]
	if !affirmative(level) || !support.DisclosesMode(protocol.ModeSessionLive) {
		s.addExpected(CodeUnavailableCapability, i, line, e, "/type", "provider attachment accepted without advertised session_live support", protocol.FeatureProvidersAttach+" session_live", string(e.Type), string(e.InReplyTo))
	}
	if level == protocol.SupportDegraded && !requested.AllowsDegraded(protocol.FeatureProvidersAttach) {
		s.addExpected(CodeDegradedWithoutOptin, i, line, e, "/type", "degraded provider attachment accepted without opt-in", protocol.FeatureProvidersAttach+" in allow_degraded_features", string(e.Type), string(e.InReplyTo))
	}
	st := s.track(p.SessionID)
	if st.attachedProviders == nil {
		st.attachedProviders = map[string]protocol.ProviderAttachment{}
	}
	if old, exists := st.attachedProviders[p.ProviderID]; exists {
		if old != requested.Provider {
			s.addExpected(CodeDuplicateProvider, i, line, e, "/payload/provider_id", "attach reused a provider alias for a different OAP service/provider", "the original binding", "a conflicting binding", p.ProviderID)
		}
		return
	}
	st.attachedProviders[p.ProviderID] = requested.Provider
	st.catalog = nil
}

func (s *state) settleModelControlRefusal(i, line int, e protocol.Envelope) {
	req := s.requests[e.InReplyTo]
	if req == nil {
		return
	}
	var refusal protocol.ErrorResponse
	_ = e.DecodePayload(&refusal)
	switch req.typ {
	case protocol.TypeSessionModelSwitchRequest:
		var requested protocol.SessionModelSwitchRequest
		_ = req.envelope.DecodePayload(&requested)
		st := s.track(requested.SessionID)
		delete(st.switchObservations, e.InReplyTo)
		if st.catalog.binds(s.currentCapability) {
			if st.catalog.ids[requested.ModelID] && refusal.Error.Code == errorModelNotFound {
				s.addExpected(CodeModelNotInCatalog, i, line, e, "/payload/error", "switch refused a model that the session catalog lists", "a successful switch", describeRefusal(refusal.Error), requested.ModelID)
			}
			if !st.catalog.ids[requested.ModelID] {
				id, ok := refusal.Error.Details["model_id"].(string)
				if refusal.Error.Code != errorModelNotFound || !ok || id != requested.ModelID {
					s.addExpected(CodeModelNotInCatalog, i, line, e, "/payload/error", "switch refusal for an unlisted model must identify the missing id", errorModelNotFound+" model_id="+requested.ModelID, describeRefusal(refusal.Error), requested.ModelID)
				}
			}
		}
	case protocol.TypeSessionProviderAttachRequest:
		level := s.features[protocol.FeatureProvidersAttach]
		if !affirmative(level) {
			feature, ok := refusal.Error.Details["feature"].(string)
			if refusal.Error.Code != errorUnsupportedFeature || !ok || feature != protocol.FeatureProvidersAttach {
				s.addExpected(CodeUnavailableCapability, i, line, e, "/payload/error", "unadvertised provider attachment needs a typed feature refusal", errorUnsupportedFeature+" "+protocol.FeatureProvidersAttach, describeRefusal(refusal.Error), string(e.InReplyTo))
			}
		}
	}
}

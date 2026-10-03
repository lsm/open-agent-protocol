package validation

import (
	"fmt"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

type pendingSettings struct {
	expectations []*controlExpectation
	level        protocol.ReasoningLevel
	policy       *protocol.CompactionPolicy
}

type settingsUpdate struct {
	level    protocol.ReasoningLevel
	policy   *protocol.CompactionPolicy
	accepted bool
	seen     bool
	response protocol.Envelope
	index    int
	line     int
}

func (s *state) settingsGate(i, line int, e protocol.Envelope, p protocol.SessionOpenRequest) {
	s.gateSettings(i, line, e, p.ReasoningLevel, p.CompactionPolicy, p.AllowsDegraded, protocol.ModeSessionOpen, "an open")
}

func (s *state) gateSettings(i, line int, e protocol.Envelope, reasoning protocol.ReasoningLevel, policy *protocol.CompactionPolicy, allows func(string) bool, mode, subject string) {
	type setting struct {
		key, pointer string
		present      bool
	}
	settings := []setting{
		{protocol.FeatureSessionReasoning, "/payload/reasoning_level", reasoning != ""},
		{protocol.FeatureCompactionPolicy, "/payload/compaction_policy", policy != nil},
	}
	if reasoning == "" && policy == nil {
		return
	}
	pending := &pendingSettings{level: reasoning, policy: policy}
	s.pendingSettings[e.ID] = pending
	for _, each := range settings {
		if !each.present {
			continue
		}
		level, judged := s.controlDescriptor(i, line, e, each.key)
		if !judged {
			return
		}
		if !affirmative(level) || !s.featureDetail(each.key).DisclosesMode(mode) {
			pending.expectations = append(pending.expectations, &controlExpectation{
				rung: rungCapability, key: each.key, pointer: each.pointer,
				code: errorUnsupportedFeature, reason: reasonUnadvertised,
				detailName: "feature", detailValue: each.key,
				diagnostic: CodeUnavailableCapability,
				message:    subject + " supplies a session setting the endpoint has not advertised for " + mode,
			})
			continue
		}
		if level == protocol.SupportDegraded && !allows(each.key) {
			pending.expectations = append(pending.expectations, &controlExpectation{
				rung: rungDegradation, key: each.key, pointer: each.pointer,
				code: errorCapabilityDegraded, detailName: "feature", detailValue: each.key,
				diagnostic: CodeDegradedWithoutOptin,
				message:    subject + " supplies a degraded session setting without the caller's opt-in",
			})
		}
	}
}

func (s *state) settleSettingsRefusal(i, line int, e protocol.Envelope) {
	pending := s.pendingSettings[e.InReplyTo]
	if pending == nil {
		return
	}
	var payload protocol.ErrorResponse
	_ = e.DecodePayload(&payload)
	attribution := s.attributeRefusal(e.InReplyTo, payload.Error)
	for _, expectation := range pending.expectations {
		if attribution.owns(expectation) {
			s.addExpected(expectation.diagnostic, i, line, e, "/payload/error",
				"refusal does not tell the caller what to change",
				expectation.describe(), describeRefusal(payload.Error), string(e.InReplyTo))
			return
		}
	}
}

func (s *state) settingsResponse(i, line int, e protocol.Envelope, p protocol.SessionState) {
	pending := s.pendingSettings[e.InReplyTo]
	if pending == nil {
		return
	}
	delete(s.pendingSettings, e.InReplyTo)
	for _, expectation := range pending.expectations {
		s.addExpected(expectation.diagnostic, i, line, e, expectation.pointer,
			expectation.message,
			"a typed refusal naming "+expectation.key, "an admitted open", string(e.InReplyTo))
	}
	if len(pending.expectations) > 0 {
		return
	}
	if pending.level != "" && p.ReasoningLevel != "" && p.ReasoningLevel != pending.level {
		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/reasoning_level",
			"an admitted open runs at the reasoning level it was asked for, or refuses it",
			string(pending.level), string(p.ReasoningLevel), string(p.SessionID))
	}
	if pending.policy != nil && p.CompactionPolicy != nil && *p.CompactionPolicy != *pending.policy {
		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/compaction_policy",
			"an admitted open runs under the compaction policy it was asked for, or refuses it",
			describePolicy(*pending.policy), describePolicy(*p.CompactionPolicy), string(p.SessionID))
	}
}

func describePolicy(policy protocol.CompactionPolicy) string {
	switch policy.Kind {
	case protocol.CompactionShare:
		return fmt.Sprintf("share %d%%", policy.SharePercent)
	case protocol.CompactionTokens:
		return fmt.Sprintf("tokens %d", policy.Tokens)
	}
	return string(policy.Kind)
}

func (s *state) settingsUpdateRequest(i, line int, e protocol.Envelope) {
	var p protocol.SessionSettingsUpdateRequest
	_ = e.DecodePayload(&p)
	s.checkScope(i, line, e, p.SessionID, "")
	s.gateSettings(i, line, e, p.ReasoningLevel, p.CompactionPolicy, p.AllowsDegraded, protocol.ModeSessionLive, "an update")
	st := s.track(p.SessionID)
	if st.settingsUpdates == nil {
		st.settingsUpdates = map[protocol.EnvelopeID]*settingsUpdate{}
	}
	st.settingsUpdates[e.ID] = &settingsUpdate{level: p.ReasoningLevel, policy: p.CompactionPolicy}
}

func (s *state) settingsUpdateResponse(i, line int, e protocol.Envelope) {
	var p protocol.SessionSettingsUpdateResponse
	_ = e.DecodePayload(&p)
	s.checkScope(i, line, e, p.SessionID, "")
	req := s.requests[e.InReplyTo]
	if req == nil {
		return
	}
	var requested protocol.SessionSettingsUpdateRequest
	_ = req.envelope.DecodePayload(&requested)
	if p.SessionID != requested.SessionID {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/session_id", "settings update response changed the requested session", string(requested.SessionID), string(p.SessionID), string(e.InReplyTo))
		return
	}
	if pending := s.pendingSettings[e.InReplyTo]; pending != nil {
		delete(s.pendingSettings, e.InReplyTo)
		for _, expectation := range pending.expectations {
			s.addExpected(expectation.diagnostic, i, line, e, expectation.pointer,
				expectation.message,
				"a typed refusal naming "+expectation.key, "an admitted update", string(e.InReplyTo))
		}
		if len(pending.expectations) > 0 {
			return
		}
	}
	if requested.ReasoningLevel != "" && p.ReasoningLevel != requested.ReasoningLevel {
		s.addExpected(CodeUnappliedControl, i, line, e, "/payload/reasoning_level",
			"an admitted update repeats the reasoning level it was asked for, or refuses it",
			string(requested.ReasoningLevel), describeLevel(p.ReasoningLevel), string(e.InReplyTo))
	}
	if requested.CompactionPolicy != nil && (p.CompactionPolicy == nil || *p.CompactionPolicy != *requested.CompactionPolicy) {
		s.addExpected(CodeUnappliedControl, i, line, e, "/payload/compaction_policy",
			"an admitted update repeats the compaction policy it was asked for, or refuses it",
			describePolicy(*requested.CompactionPolicy), describeOptionalPolicy(p.CompactionPolicy), string(e.InReplyTo))
	}
	if update := s.track(p.SessionID).settingsUpdates[e.InReplyTo]; update != nil {
		update.accepted = true
		update.response = e
		update.index = i
		update.line = line
	}
}

func (s *state) observeSettingsState(st *sessionTrack, p protocol.SessionState) {
	for _, update := range st.settingsUpdates {
		if update.seen {
			continue
		}
		levelHeld := update.level == "" || p.ReasoningLevel == "" || p.ReasoningLevel == update.level
		policyHeld := update.policy == nil || p.CompactionPolicy == nil || *p.CompactionPolicy == *update.policy
		if levelHeld && policyHeld {
			update.seen = true
		}
	}
}

func (s *state) settingsBeforeRun(i, line int, e protocol.Envelope, session protocol.SessionID) {
	st := s.sessions[session]
	if st == nil {
		return
	}
	for id, update := range st.settingsUpdates {
		if update.accepted && !update.seen {
			s.addExpected(CodeSessionStateMismatch, i, line, e, "/type",
				"a run started after an accepted settings update before session.state.updated reported it",
				"session.state.updated before the next run starts", string(e.Type), string(id))
			delete(st.settingsUpdates, id)
		}
	}
}

func (s *state) closeSettingsUpdates() {
	for _, st := range s.sessions {
		for id, update := range st.settingsUpdates {
			if update.accepted && !update.seen {
				s.addExpected(CodeSessionStateMismatch, update.index, update.line, update.response, "/type",
					"accepted settings update was not reflected in session.state.updated",
					"a matching session.state.updated", "none", string(id))
			}
		}
	}
}

func describeLevel(level protocol.ReasoningLevel) string {
	if level == "" {
		return "absent"
	}
	return string(level)
}

func describeOptionalPolicy(policy *protocol.CompactionPolicy) string {
	if policy == nil {
		return "absent"
	}
	return describePolicy(*policy)
}

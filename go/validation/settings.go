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

func (s *state) settingsGate(i, line int, e protocol.Envelope, p protocol.SessionOpenRequest) {
	type setting struct {
		key, pointer string
		present      bool
	}
	settings := []setting{
		{protocol.FeatureSessionReasoning, "/payload/reasoning_level", p.ReasoningLevel != ""},
		{protocol.FeatureCompactionPolicy, "/payload/compaction_policy", p.CompactionPolicy != nil},
	}
	if p.ReasoningLevel == "" && p.CompactionPolicy == nil {
		return
	}
	pending := &pendingSettings{level: p.ReasoningLevel, policy: p.CompactionPolicy}
	s.pendingSettings[e.ID] = pending
	for _, each := range settings {
		if !each.present {
			continue
		}
		level, judged := s.controlDescriptor(i, line, e, each.key)
		if !judged {
			return
		}
		if !affirmative(level) || !s.featureDetail(each.key).DisclosesMode(protocol.ModeSessionOpen) {
			pending.expectations = append(pending.expectations, &controlExpectation{
				rung: rungCapability, key: each.key, pointer: each.pointer,
				code: errorUnsupportedFeature, reason: reasonUnadvertised,
				detailName: "feature", detailValue: each.key,
				diagnostic: CodeUnavailableCapability,
				message:    "an open supplies a session setting the endpoint has not advertised for session_open",
			})
			continue
		}
		if level == protocol.SupportDegraded && !p.AllowsDegraded(each.key) {
			pending.expectations = append(pending.expectations, &controlExpectation{
				rung: rungDegradation, key: each.key, pointer: each.pointer,
				code: errorCapabilityDegraded, detailName: "feature", detailValue: each.key,
				diagnostic: CodeDegradedWithoutOptin,
				message:    "an open supplies a degraded session setting without the caller's opt-in",
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

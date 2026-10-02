package validation

import (
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type pendingReopen struct {
	expectation *controlExpectation
}

func (s *state) reopenGate(i, line int, e protocol.Envelope, p protocol.SessionOpenRequest) {
	if !p.Reopen {
		return
	}
	pending := &pendingReopen{}
	s.pendingReopens[e.ID] = pending
	level, judged := s.controlDescriptor(i, line, e, protocol.FeatureOpenReopen)
	if !judged {
		return
	}
	if !affirmative(level) {
		pending.expectation = &controlExpectation{
			rung: rungCapability, key: protocol.FeatureOpenReopen, pointer: "/payload/reopen",
			code: errorUnsupportedFeature, reason: reasonUnadvertised,
			detailName: "feature", detailValue: protocol.FeatureOpenReopen,
			diagnostic: CodeUnavailableCapability,
			message:    "an open asks to reopen a session against an endpoint that has not affirmatively advertised it",
		}
		return
	}
	if level == protocol.SupportDegraded && !p.AllowsDegraded(protocol.FeatureOpenReopen) {
		pending.expectation = &controlExpectation{
			rung: rungDegradation, key: protocol.FeatureOpenReopen, pointer: "/payload/reopen",
			code: errorCapabilityDegraded, detailName: "feature", detailValue: protocol.FeatureOpenReopen,
			diagnostic: CodeDegradedWithoutOptin,
			message:    "open asks for a degraded reopen without the caller's opt-in",
		}
	}
}

func (s *state) settleReopenRefusal(i, line int, e protocol.Envelope) {
	pending := s.pendingReopens[e.InReplyTo]
	if pending == nil || pending.expectation == nil {
		return
	}
	var payload protocol.ErrorResponse
	_ = e.DecodePayload(&payload)
	attribution := s.attributeRefusal(e.InReplyTo, payload.Error)
	if attribution.owns(pending.expectation) {
		s.addExpected(pending.expectation.diagnostic, i, line, e, "/payload/error",
			"refusal does not tell the caller what to change",
			pending.expectation.describe(), describeRefusal(payload.Error), string(e.InReplyTo))
	}
}

func (s *state) reopenResponse(i, line int, e protocol.Envelope, p protocol.SessionOpenResponse) {
	pending := s.pendingReopens[e.InReplyTo]
	if pending == nil {
		return
	}
	delete(s.pendingReopens, e.InReplyTo)
	if pending.expectation != nil {
		s.addExpected(pending.expectation.diagnostic, i, line, e, "/payload",
			pending.expectation.message,
			"a typed refusal naming "+pending.expectation.key, "an admitted reopen", string(e.InReplyTo))
		return
	}
	if p.Recovery == nil || !p.Recovery.Recovered {
		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/recovery/recovered",
			"a reopened session's state document declares that it was recovered",
			"true", "absent or false", string(p.SessionID))
	}
	req := s.requests[e.InReplyTo]
	carried := req != nil && req.carriesMessage
	if !carried && (p.ActiveRunID != "" || len(p.ActiveRuns) > 0) {
		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/active_runs",
			"a reopen loads a session with no run under way, since close was refused while one was",
			"none", s.activeRunCount(p), string(p.SessionID))
	}
}

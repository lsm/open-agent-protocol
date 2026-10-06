package validation

import (
	"strconv"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

type pendingSessionList struct {
	expectation *controlExpectation
	limit       int
}

func (s *state) sessionListRequest(i, line int, e protocol.Envelope, p protocol.SessionListRequest) {
	pending := &pendingSessionList{limit: p.Limit}
	if pending.limit == 0 {
		pending.limit = protocol.SessionListDefaultLimit
	}
	s.pendingSessionLists[e.ID] = pending
	level, judged := s.controlDescriptor(i, line, e, protocol.FeatureSessionList)
	if !judged {
		return
	}
	switch {
	case !affirmative(level):
		pending.expectation = &controlExpectation{
			rung: rungCapability, key: protocol.FeatureSessionList, pointer: "/payload",
			code: errorUnsupportedFeature, reason: reasonUnadvertised,
			detailName: "feature", detailValue: protocol.FeatureSessionList,
			diagnostic: CodeUnavailableCapability,
			message:    "a session list asked of an endpoint that has not affirmatively advertised session.list",
		}
	case level == protocol.SupportDegraded && !p.AllowsDegraded(protocol.FeatureSessionList):
		pending.expectation = &controlExpectation{
			rung: rungDegradation, key: protocol.FeatureSessionList, pointer: "/payload/allow_degraded_features",
			code: errorCapabilityDegraded, detailName: "feature", detailValue: protocol.FeatureSessionList,
			diagnostic: CodeDegradedWithoutOptin,
			message:    "a session list omits the opt-in a degraded session.list requires",
		}
	}
}

func (s *state) settleSessionListRefusal(i, line int, e protocol.Envelope) {
	pending := s.pendingSessionLists[e.InReplyTo]
	if pending == nil {
		return
	}
	delete(s.pendingSessionLists, e.InReplyTo)
	if pending.expectation == nil {
		return
	}
	var payload protocol.ErrorResponse
	_ = e.DecodePayload(&payload)
	if !conformingRefusal(payload.Error, pending.expectation) {
		s.addExpected(pending.expectation.diagnostic, i, line, e, "/payload/error", "refusal does not tell the caller what to change", pending.expectation.describe(), describeRefusal(payload.Error), string(e.InReplyTo))
	}
}

func (s *state) sessionListResponse(i, line int, e protocol.Envelope, p protocol.SessionListResponse) {
	s.featureKeys(i, line, e, []string{protocol.FeatureSessionList})
	pending := s.pendingSessionLists[e.InReplyTo]
	delete(s.pendingSessionLists, e.InReplyTo)
	if pending != nil && pending.expectation != nil && pending.expectation.rung == rungDegradation {
		s.addExpected(pending.expectation.diagnostic, i, line, e, "/payload", pending.expectation.message, "a typed refusal naming "+pending.expectation.key, "a served list", string(e.InReplyTo))
	}
	if pending != nil && len(p.Sessions) > pending.limit {
		s.addExpected(CodeSessionListOverLimit, i, line, e, "/payload/sessions", "a session list page holds more entries than the request's limit allows", strconv.Itoa(pending.limit), strconv.Itoa(len(p.Sessions)), string(e.InReplyTo))
	}
	seen := make(map[protocol.SessionID]bool, len(p.Sessions))
	for index, entry := range p.Sessions {
		pointer := "/payload/sessions/" + strconv.Itoa(index)
		if seen[entry.SessionID] {
			s.addExpected(CodeDuplicateSessionEntry, i, line, e, pointer+"/session_id", "a session list page lists one session twice", "one entry per session", string(entry.SessionID))
			continue
		}
		seen[entry.SessionID] = true
		if index == 0 {
			continue
		}
		previous := p.Sessions[index-1]
		if previous.UpdatedAtMS < entry.UpdatedAtMS || (previous.UpdatedAtMS == entry.UpdatedAtMS && previous.SessionID > entry.SessionID) {
			s.addExpected(CodeSessionListOrder, i, line, e, pointer, "a session list is newest first by updated_at_ms, ties broken by session_id", "after "+string(previous.SessionID), string(entry.SessionID))
		}
	}
}

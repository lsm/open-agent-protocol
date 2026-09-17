package validation

import (
	"github.com/lsm/open-agent-protocol/protocol"
)

type refusalAttribution struct {
	answered bool
	aside    bool
	speaker  *controlExpectation
}

func (a refusalAttribution) discharged() bool {
	return a.answered || a.aside
}

func (a refusalAttribution) owns(expectation *controlExpectation) bool {
	return !a.discharged() && (a.speaker == nil || a.speaker == expectation)
}

func (s *state) retainedExpectations(request protocol.EnvelopeID) []*controlExpectation {
	var retained []*controlExpectation
	if pending := s.pendingControls[request]; pending != nil && pending.expectation != nil {
		retained = append(retained, pending.expectation)
	}
	if pending := s.pendingOpens[request]; pending != nil {
		if pending.expectation != nil {
			retained = append(retained, pending.expectation)
		}
		if pending.limitRefusal != nil {
			retained = append(retained, pending.limitRefusal)
		}
	}
	if pending := s.pendingSubscribes[request]; pending != nil && pending.expectation != nil {
		retained = append(retained, pending.expectation)
	}
	return retained
}

func (s *state) attributeRefusal(request protocol.EnvelopeID, err protocol.ProtocolError) refusalAttribution {
	retained := s.retainedExpectations(request)
	if len(retained) == 0 {
		return refusalAttribution{}
	}
	var speaker *controlExpectation
	for _, expectation := range retained {
		if conformingRefusal(err, expectation) {
			return refusalAttribution{answered: true}
		}
		if speaker == nil || expectation.less(speaker) {
			speaker = expectation
		}
	}
	if s.isOpenRequest(request) && openLevelRefusals[err.Code] {
		return refusalAttribution{aside: true}
	}
	return refusalAttribution{speaker: speaker}
}

var openLevelRefusals = map[string]bool{
	"session_exists":     true,
	"unknown_adapter":    true,
	"session_closed":     true,
	"stale_capabilities": true,
}

func (s *state) isOpenRequest(request protocol.EnvelopeID) bool {
	req := s.requests[request]
	return req != nil && req.typ == protocol.TypeSessionOpenRequest
}

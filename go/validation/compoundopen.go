package validation

import (
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func (s *state) compoundOpenRequest(i, line int, e protocol.Envelope, p protocol.SessionOpenRequest) {
	s.subscribeGate(i, line, e, p)
	if p.Message == nil {
		return
	}
	if req := s.requests[e.ID]; req != nil {

		req.carriesMessage = true
	}
	submission := p.Message.Submit(p.SessionID)

	s.submitControls(i, line, e, submission)
	if submission.Delivery != protocol.DeliveryAuto && submission.Delivery != protocol.DeliveryQueue &&
		!(s.tolerant && foreignRequestedDelivery(submission.Delivery)) {

		s.feature(i, line, e, "delivery."+string(submission.Delivery))
	}
}

func (s *state) subscribeGate(i, line int, e protocol.Envelope, p protocol.SessionOpenRequest) {
	if !p.Subscribe {
		return
	}
	level, judged := s.controlDescriptor(i, line, e, protocol.FeatureOpenSubscribe)
	if !judged {
		return
	}
	if !affirmative(level) {
		s.pendingSubscribes[e.ID] = &pendingSubscribe{
			expectation: &controlExpectation{
				rung: rungCapability, key: protocol.FeatureOpenSubscribe, pointer: "/payload/subscribe",
				code: errorUnsupportedFeature, reason: reasonUnadvertised,
				detailName: "feature", detailValue: protocol.FeatureOpenSubscribe,
				diagnostic: CodeUnavailableCapability,
				message:    "an open elects subscribe against an endpoint that has not affirmatively advertised it",
			},
		}
		return
	}
	if level == protocol.SupportDegraded && !p.AllowsDegraded(protocol.FeatureOpenSubscribe) {

		s.pendingSubscribes[e.ID] = &pendingSubscribe{
			expectation: &controlExpectation{
				rung: rungDegradation, key: protocol.FeatureOpenSubscribe, pointer: "/payload/subscribe",
				code: errorCapabilityDegraded, detailName: "feature", detailValue: protocol.FeatureOpenSubscribe,
				diagnostic: CodeDegradedWithoutOptin,
				message:    "open elects a degraded subscribe without the caller's opt-in",
			},
		}
		return
	}
	s.pendingSubscribes[e.ID] = &pendingSubscribe{honour: true}
}

type pendingSubscribe struct {
	expectation *controlExpectation
	honour      bool
}

func (s *state) settleSubscribeRefusal(i, line int, e protocol.Envelope) {
	pending := s.pendingSubscribes[e.InReplyTo]
	if pending == nil {
		return
	}
	var payload protocol.ErrorResponse
	_ = e.DecodePayload(&payload)
	attribution := s.attributeRefusal(e.InReplyTo, payload.Error)
	delete(s.pendingSubscribes, e.InReplyTo)
	if attribution.discharged() {
		return
	}
	if pending.expectation != nil {
		if attribution.owns(pending.expectation) {
			s.addExpected(pending.expectation.diagnostic, i, line, e, "/payload/error",
				"refusal does not tell the caller what to change",
				pending.expectation.describe(), describeRefusal(payload.Error), string(e.InReplyTo))
		}
		return
	}
	if !pending.honour || !refusesSubscribe(payload.Error) {
		return
	}
	s.addExpected(CodeUnhonouredCapability, i, line, e, "/payload/error",
		"an open electing subscribe was refused for a key the endpoint advertises",
		"a session opened with its subscription", describeRefusal(payload.Error), string(e.InReplyTo))
}

func (s *state) compoundOpenResponse(i, line int, e protocol.Envelope, p protocol.SessionOpenResponse) {

	if pending := s.pendingSubscribes[e.InReplyTo]; pending != nil && pending.expectation != nil {
		s.addExpected(pending.expectation.diagnostic, i, line, e, "/payload",
			pending.expectation.message,
			"a typed refusal naming "+pending.expectation.key, "an admitted open", string(e.InReplyTo))
	}
	delete(s.pendingSubscribes, e.InReplyTo)
	req := s.requests[e.InReplyTo]
	if req == nil || !req.carriesMessage {
		return
	}

	if req.session == "" {
		req.session = p.SessionID
	}
	s.bindOpenSubmission(e.InReplyTo, p.SessionID)
	entry, ok := s.soleAdmittedRun(p)
	if !ok {

		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/active_runs",
			"a compound open's response names the one run its message admitted",
			"one newly admitted run", s.activeRunCount(p), string(p.SessionID))
		return
	}
	s.admitSubmission(i, line, e, protocol.MessageSubmitResponse{
		SessionID:         p.SessionID,
		Accepted:          true,
		SubmissionID:      protocol.SubmissionID(e.InReplyTo),
		RequestedDelivery: requestedSubmission(req).Delivery,
		EffectiveDelivery: effectiveFor(entry.Status),
		Admission:         admissionFor(entry.Status),
		RunID:             entry.RunID,
		Status:            entry.Status,
		ModelID:           admittedModel(requestedSubmission(req), p),
	})
}

func (s *state) bindOpenSubmission(request protocol.EnvelopeID, session protocol.SessionID) {
	pending := s.pendingControls[request]
	if pending == nil || pending.session == session {
		return
	}
	previous := pending.session
	pending.session = session
	queue := s.openSubmits[previous]
	for i, entry := range queue {
		if entry == pending {
			s.openSubmits[previous] = append(queue[:i:i], queue[i+1:]...)
			break
		}
	}
	if len(s.openSubmits[previous]) == 0 {
		delete(s.openSubmits, previous)
	}
	s.openSubmits[session] = append(s.openSubmits[session], pending)
	s.refreshQueueWindows(session)
}

func (s *state) soleAdmittedRun(p protocol.SessionOpenResponse) (protocol.ActiveRun, bool) {
	var found protocol.ActiveRun
	count := 0
	for _, entry := range p.ActiveRuns {
		if _, known := s.runs[entry.RunID]; known {
			continue
		}
		found, count = entry, count+1
	}
	return found, count == 1
}

func admittedModel(request protocol.MessageSubmitRequest, p protocol.SessionOpenResponse) string {
	if request.ModelID != nil {
		return *request.ModelID
	}
	return p.CurrentModelID
}

func refusesSubscribe(err protocol.ProtocolError) bool {
	if err.Code != errorUnsupportedFeature && err.Code != errorCapabilityDegraded {
		return false
	}
	named, ok := err.Details["feature"].(string)
	if !ok {
		return true
	}
	return named == protocol.FeatureOpenSubscribe
}

func (s *state) activeRunCount(p protocol.SessionOpenResponse) string {
	switch count := 0; func() int {
		for _, entry := range p.ActiveRuns {
			if _, known := s.runs[entry.RunID]; !known {
				count++
			}
		}
		return count
	}() {
	case 0:
		return "none"
	case 1:
		return "one"
	}
	return "several"
}

func admissionFor(status protocol.RunStatus) protocol.Admission {
	if status == protocol.RunQueued {
		return protocol.AdmissionQueued
	}
	return protocol.AdmissionStarted
}

func effectiveFor(status protocol.RunStatus) protocol.EffectiveDeliveryMode {
	if status == protocol.RunQueued {
		return protocol.EffectiveDeliveryQueue
	}
	return protocol.DeliveryStart
}

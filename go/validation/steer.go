package validation

import (
	"fmt"
	"slices"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

type steerTrack struct {
	request               protocol.EnvelopeID
	messages              []protocol.MessageID
	admittedAt, settledAt int
}

func (s *state) steerTarget(p protocol.MessageSubmitRequest) (*runState, string) {
	if p.TargetRunID != "" {
		r := s.runs[p.TargetRunID]
		switch {
		case r == nil:
			return nil, "unknown_target"
		case r.session != p.SessionID:
			return r, "cross_session"
		case r.terminal:
			return r, "terminal"
		case r.status == protocol.RunCancelling:
			return r, "not_steerable"
		case !r.started:
			return r, "queued"
		default:
			return r, ""
		}
	}
	if st := s.sessions[p.SessionID]; st != nil {
		for _, id := range st.order {
			if r := s.runs[id]; r != nil && r.started && !r.terminal {
				if r.status == protocol.RunCancelling {
					return r, "not_steerable"
				}
				return r, ""
			}
		}
	}
	return nil, "no_active_run"
}

func (s *state) steerAdmission(i, line int, e protocol.Envelope, p protocol.MessageSubmitResponse) {
	s.settleSubmitAdmission(i, line, e, p)
	if pending := s.pendingControls[e.InReplyTo]; pending != nil && pending.expectation != nil {
		return
	}
	req := s.requests[e.InReplyTo]
	if req == nil {
		return
	}
	request := requestedSubmission(req)
	if request.Delivery != protocol.DeliverySteer || p.EffectiveDelivery != protocol.EffectiveDeliverySteer {
		s.add(CodeIllegalRunTransition, i, line, e, "/payload/admission", "steering requires explicit steer delivery")
		return
	}
	r, reason := s.steerTarget(request)
	if reason != "" {
		s.addExpected(CodeIllegalRunTransition, i, line, e, "/payload/run_id", "steer target cannot take guidance", "a started nonterminal steerable run", reason)
		return
	}
	if p.RunID != r.id {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/run_id", "steer admission must name its target", string(r.id), string(p.RunID))
		return
	}
	if p.TargetSequence == nil || *p.TargetSequence != r.next-1 {
		actual := "absent"
		if p.TargetSequence != nil {
			actual = uintString(*p.TargetSequence)
		}
		s.addExpected(CodeIllegalRunTransition, i, line, e, "/payload/target_sequence", "steer admission must name the target cursor at response", uintString(r.next-1), actual)
	}
	if p.Status != r.status {
		s.addExpected(CodeIllegalRunTransition, i, line, e, "/payload/status", "steer admission must report target status", string(r.status), string(p.Status))
	}
	if r.steers == nil {
		r.steers = map[protocol.SubmissionID]*steerTrack{}
	}
	if r.steers[p.SubmissionID] != nil {
		s.add(CodeDuplicateSteer, i, line, e, "/payload/submission_id", "steer submission identity was already admitted")
		return
	}
	r.steers[p.SubmissionID] = &steerTrack{request: e.InReplyTo, messages: p.MessageIDs, admittedAt: i, settledAt: -1}
}

func (s *state) steerSettlement(i, line int, e protocol.Envelope, r *runState) {
	s.featureKeys(i, line, e, []string{protocol.FeatureDeliverySteer})
	var applied protocol.RunSteerAppliedPayload
	var dropped protocol.RunSteerDroppedPayload
	var id protocol.SubmissionID
	var request protocol.EnvelopeID
	if e.Type == protocol.TypeRunSteerApplied {
		_ = e.DecodePayload(&applied)
		id, request = applied.SubmissionID, applied.RequestID
		s.checkScope(i, line, e, applied.SessionID, applied.RunID)
	} else {
		_ = e.DecodePayload(&dropped)
		id, request = dropped.SubmissionID, dropped.RequestID
		s.checkScope(i, line, e, dropped.SessionID, dropped.RunID)
	}
	track := r.steers[id]
	if track == nil {
		s.add(CodeUnmatchedSteer, i, line, e, "/payload/submission_id", "settlement names no admitted steer")
		return
	}
	if track.settledAt >= 0 {
		s.add(CodeDuplicateSteer, i, line, e, "/payload/submission_id", "steer settled more than once")
		return
	}
	if request != track.request {
		s.add(CodeUnmatchedSteer, i, line, e, "/payload/request_id", "settlement does not name the admitting request")
	}
	if e.Type == protocol.TypeRunSteerApplied && track.messages != nil && !slices.Equal(track.messages, applied.MessageIDs) {
		s.add(CodeUnmatchedSteer, i, line, e, "/payload/message_ids", "settlement differs from admitted guidance")
	}
	track.settledAt = i
}

func (s *state) steerRefusal(i, line int, e protocol.Envelope) {
	req := s.requests[e.InReplyTo]
	if req == nil {
		return
	}
	request := requestedSubmission(req)
	if request.Delivery != protocol.DeliverySteer {
		return
	}
	if pending := s.pendingControls[e.InReplyTo]; pending != nil && pending.expectation != nil {
		return
	}
	_, reason := s.steerTarget(request)
	if reason == "" {
		return
	}
	var p protocol.ErrorResponse
	_ = e.DecodePayload(&p)
	if p.Error.Code != "invalid_steer_target" {
		s.addExpected(CodeIllegalRunTransition, i, line, e, "/payload/error/code", "refusal must describe the invalid steer target", "invalid_steer_target", p.Error.Code)
	} else if p.Error.Details["reason"] != reason {
		s.addExpected(CodeIllegalRunTransition, i, line, e, "/payload/error/details/reason", "refusal must describe the highest-ranked target condition", reason, fmt.Sprint(p.Error.Details["reason"]))
	}
}

func (s *state) steerTerminal(i, line int, e protocol.Envelope, r *runState) {
	for _, track := range r.steers {
		if track.settledAt < 0 {
			s.add(CodePendingSteerAtTerminal, i, line, e, "/type", "run terminated with unsettled guidance")
			return
		}
	}
}

func (s *state) steerState(i, line int, e protocol.Envelope, p protocol.SessionState) {
	for _, entry := range p.ActiveRuns {
		r := s.runs[entry.RunID]
		if r == nil {
			continue
		}
		position := i
		if e.Type == protocol.TypeSessionStateResponse {
			if req := s.requests[e.InReplyTo]; req != nil {
				position = req.index
			}
		}
		listed := map[protocol.SubmissionID]protocol.EnvelopeID{}
		for _, pending := range entry.PendingSteers {
			track := r.steers[pending.SubmissionID]
			if _, duplicate := listed[pending.SubmissionID]; duplicate || track == nil || track.request != pending.RequestID || (track.settledAt >= 0 && track.settledAt < position) {
				s.add(CodeSessionStateMismatch, i, line, e, "/payload/active_runs", "snapshot names an incorrect pending steer")
			}
			listed[pending.SubmissionID] = pending.RequestID
		}
		for id, track := range r.steers {
			if track.admittedAt < position && (track.settledAt < 0 || track.settledAt >= i) && listed[id] == "" {
				s.add(CodeSessionStateMismatch, i, line, e, "/payload/active_runs", "snapshot omits admitted pending guidance")
			}
		}
	}
}

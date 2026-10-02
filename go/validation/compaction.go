package validation

import (
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type compactionTrack struct {
	reason    protocol.CompactionReason
	outcome   protocol.CompactionOutcome
	startedAt int
	endedAt   int
}

func (s *state) compactRequest(i, line int, e protocol.Envelope) {
	var p protocol.SessionCompactRequest
	_ = e.DecodePayload(&p)
	s.checkScope(i, line, e, p.SessionID, "")
	if s.capabilitiesStale {
		s.add(CodeStaleCapabilityRevision, i, line, e, "/capability_revision", "compaction request occurred before refreshed capabilities")
	}
	s.featureKeys(i, line, e, []string{protocol.FeatureSessionCompact})
	switch p.Delivery {
	case "", protocol.DeliveryAuto, protocol.DeliveryQueue:
	default:
		s.add(CodeIllegalRunTransition, i, line, e, "/payload/delivery", "a compaction accepts auto or queue delivery only")
	}
	if p.Focus == nil {
		return
	}
	level, known := s.advertisedLevel(protocol.FeatureSessionCompact)
	if !known || level != protocol.SupportDegraded || p.AllowsDegraded(protocol.FeatureSessionCompact) {
		return
	}
	if s.pendingControls == nil {
		s.pendingControls = map[protocol.EnvelopeID]*pendingSubmit{}
	}
	s.pendingControls[e.ID] = &pendingSubmit{
		satisfiable: map[string]bool{}, index: i, line: line, session: p.SessionID, revision: s.currentCapability,
		expectation: &controlExpectation{
			rung: rungDegradation, key: protocol.FeatureSessionCompact, pointer: "/payload/focus",
			code: errorCapabilityDegraded, detailName: "feature", detailValue: protocol.FeatureSessionCompact,
			diagnostic: CodeDegradedWithoutOptin,
			message:    "a compaction focus was admitted without the caller's opt-in",
		},
	}
}

func (s *state) compactResponse(i, line int, e protocol.Envelope) {
	var p protocol.SessionCompactResponse
	_ = e.DecodePayload(&p)
	req := s.requests[e.InReplyTo]
	if req == nil || req.typ != protocol.TypeSessionCompactRequest {
		s.add(CodeUnmatchedResponse, i, line, e, "/in_reply_to", "compaction response answers no compaction request")
		return
	}
	s.checkScope(i, line, e, p.SessionID, p.RunID)
	var request protocol.SessionCompactRequest
	_ = req.envelope.DecodePayload(&request)
	if p.SessionID != request.SessionID {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/session_id", "compaction response must answer its request's session", string(request.SessionID), string(p.SessionID), string(e.InReplyTo))
	}
	requested := request.Delivery
	if requested == "" {
		requested = protocol.DeliveryAuto
	}
	if p.RequestedDelivery != requested {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/requested_delivery", "compaction response must repeat the requested delivery", string(requested), string(p.RequestedDelivery), string(e.InReplyTo))
	}
	if !p.Accepted {
		s.addExpected(CodeIllegalRunTransition, i, line, e, "/payload/accepted", "a refused compaction is a correlated error.response, not a compaction response", string(protocol.TypeErrorResponse), string(e.Type), string(e.InReplyTo))
		return
	}
	if p.RunID == "" {
		s.add(CodeIllegalRunTransition, i, line, e, "/payload/run_id", "an accepted compaction must reserve a run identity")
		return
	}
	queued := p.Admission == protocol.AdmissionQueued
	switch p.Admission {
	case protocol.AdmissionStarted:
		if p.EffectiveDelivery != protocol.DeliveryStart || p.Status != protocol.RunRunning {
			s.add(CodeIllegalRunTransition, i, line, e, "/payload/admission", "a started compaction resolves to effective start with a running run")
		}
	case protocol.AdmissionQueued:
		if p.EffectiveDelivery != protocol.EffectiveDeliveryQueue || p.Status != protocol.RunQueued {
			s.add(CodeIllegalRunTransition, i, line, e, "/payload/admission", "a queued compaction resolves to effective queue with a queued run")
		}
	default:
		s.add(CodeIllegalRunTransition, i, line, e, "/payload/admission", "a compaction admits a started or queued run, never a steered or side one")
		return
	}
	if old := s.runs[p.RunID]; old != nil {
		s.add(CodeIllegalRunTransition, i, line, e, "/payload/run_id", "run was admitted more than once")
		return
	}
	st := s.sessions[p.SessionID]
	if st == nil {
		st = &sessionTrack{}
		s.sessions[p.SessionID] = st
	}
	submitted := protocol.MessageSubmitResponse{
		SessionID: p.SessionID, RunID: p.RunID, RequestedDelivery: p.RequestedDelivery,
		EffectiveDelivery: p.EffectiveDelivery, DeliveryResolution: p.DeliveryResolution,
		Admission: p.Admission, Status: p.Status,
	}
	if !s.queueOverlap(i, line, e, submitted, st) {
		s.queueAdmission(i, line, e, submitted, st)
	}
	s.settleSubmitAdmission(i, line, e, submitted)
	status := protocol.RunRunning
	if queued {
		status = protocol.RunQueued
	}
	run := &runState{
		id: p.RunID, session: p.SessionID, admitted: true, next: 1, lastIndex: i, lastLine: line, status: status,
		tools: map[protocol.ToolCallID]toolTrack{}, interactions: map[protocol.InteractionID]*interactionState{},
		compactions:   map[protocol.CompactionID]*compactionTrack{},
		compactionRun: true, compactionContinue: request.Continue,
	}
	run.order = len(st.order)
	run.admittedQueued = queued
	run.admittedAt = i
	run.submitRequest = e.InReplyTo
	s.runs[p.RunID] = run
	st.order = append(st.order, p.RunID)
	if !queued && (st.active == "" || s.runs[st.active] == nil || s.runs[st.active].terminal) {
		st.active = p.RunID
	}
	s.refreshQueueWindows(p.SessionID)
}

func (s *state) compactionEvent(i, line int, e protocol.Envelope, r *runState) {
	s.featureKeys(i, line, e, []string{protocol.FeatureRunCompaction})
	if r.compactions == nil {
		r.compactions = map[protocol.CompactionID]*compactionTrack{}
	}
	switch e.Type {
	case protocol.TypeRunCompactionStarted:
		var p protocol.RunCompactionStartedPayload
		_ = e.DecodePayload(&p)
		s.checkScope(i, line, e, p.SessionID, p.RunID)
		if r.compactionRun != (p.Reason == protocol.CompactionRequested) {
			s.add(CodeCompactionRunMismatch, i, line, e, "/payload/reason", "a requested compaction belongs to the run a session.compact.request admitted, and no other run asks for one")
		}
		if r.openCompaction != "" {
			s.add(CodeCompactionUnpaired, i, line, e, "/payload/compaction_id", "a compaction started before the previous one ended")
			return
		}
		if r.compactions[p.CompactionID] != nil {
			s.add(CodeCompactionUnpaired, i, line, e, "/payload/compaction_id", "compaction identity was already used")
			return
		}
		r.openCompaction = p.CompactionID
		r.compactionOpened = true
		r.compactions[p.CompactionID] = &compactionTrack{reason: p.Reason, startedAt: i, endedAt: -1}
	case protocol.TypeRunCompactionEnded:
		var p protocol.RunCompactionEndedPayload
		_ = e.DecodePayload(&p)
		s.checkScope(i, line, e, p.SessionID, p.RunID)
		if p.Outcome == protocol.CompactionFailed && p.Error == nil {
			s.add(CodeCompactionFailedWithoutError, i, line, e, "/payload/error", "a failed compaction carries the error that failed it")
		}
		track := r.compactions[p.CompactionID]
		if track == nil || r.openCompaction != p.CompactionID {
			s.add(CodeCompactionEndedWithoutStart, i, line, e, "/payload/compaction_id", "compaction ended without its started event")
			return
		}
		track.endedAt = i
		track.outcome = p.Outcome
		r.openCompaction = ""
	}
}

func (s *state) compactionOpening(i, line int, e protocol.Envelope, r *runState) {
	if !r.compactionRun || !r.started || r.compactionOpened || isTerminal(e.Type) {
		return
	}
	switch e.Type {
	case protocol.TypeRunStarted, protocol.TypeRunCompactionStarted:
		return
	}
	s.add(CodeCompactionRunMismatch, i, line, e, "/type", "a run admitted by session.compact.request opens with its compaction")
}

func (s *state) compactionTerminal(i, line int, e protocol.Envelope, r *runState) {
	if r.openCompaction != "" {
		s.add(CodeCompactionOpenAtTerminal, i, line, e, "/type", "run terminated while a compaction was open")
	}
	if !r.compactionRun || !r.started {
		return
	}
	if len(r.compactions) != 1 {
		s.add(CodeCompactionRunMismatch, i, line, e, "/type", "a run admitted by session.compact.request carries exactly one compaction")
		return
	}
	if r.compactionContinue || e.Type != protocol.TypeRunCompleted {
		return
	}
	for _, track := range r.compactions {
		if track.outcome != protocol.CompactionCompleted {
			return
		}
	}
	var p protocol.RunCompletedPayload
	_ = e.DecodePayload(&p)
	if p.StopReason != "compacted" {
		s.addExpected(CodeCompactionStopReasonMismatch, i, line, e, "/payload/stop_reason", "a compaction run that did not continue settles on its compaction", "compacted", p.StopReason, string(r.id))
	}
}

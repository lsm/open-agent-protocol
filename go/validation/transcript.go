package validation

import (
	"strconv"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

type pendingTranscriptLoad struct {
	expectation *controlExpectation
	limit       int
	cursor      bool
}

func (s *state) transcriptLoadRequest(i, line int, e protocol.Envelope, p protocol.TranscriptLoadRequest) {
	pending := &pendingTranscriptLoad{limit: p.Limit, cursor: p.Cursor != ""}
	if pending.limit == 0 {
		pending.limit = protocol.TranscriptLoadDefaultLimit
	}
	s.pendingTranscriptLoads[e.ID] = pending
	level, judged := s.controlDescriptor(i, line, e, protocol.FeatureTranscriptLoad)
	if !judged {
		return
	}
	switch {
	case !affirmative(level):
		pending.expectation = &controlExpectation{
			rung: rungCapability, key: protocol.FeatureTranscriptLoad, pointer: "/payload",
			code: errorUnsupportedFeature, reason: reasonUnadvertised,
			detailName: "feature", detailValue: protocol.FeatureTranscriptLoad,
			diagnostic: CodeUnavailableCapability,
			message:    "a transcript load asked of an endpoint that has not affirmatively advertised session.transcript.load",
		}
	case level == protocol.SupportDegraded && !p.AllowsDegraded(protocol.FeatureTranscriptLoad):
		pending.expectation = &controlExpectation{
			rung: rungDegradation, key: protocol.FeatureTranscriptLoad, pointer: "/payload/allow_degraded_features",
			code: errorCapabilityDegraded, detailName: "feature", detailValue: protocol.FeatureTranscriptLoad,
			diagnostic: CodeDegradedWithoutOptin,
			message:    "a transcript load omits the opt-in a degraded session.transcript.load requires",
		}
	}
}

func (s *state) settleTranscriptLoadRefusal(i, line int, e protocol.Envelope) {
	pending := s.pendingTranscriptLoads[e.InReplyTo]
	if pending == nil {
		return
	}
	delete(s.pendingTranscriptLoads, e.InReplyTo)
	if pending.expectation == nil {
		return
	}
	var payload protocol.ErrorResponse
	_ = e.DecodePayload(&payload)
	if !conformingRefusal(payload.Error, pending.expectation) {
		s.addExpected(pending.expectation.diagnostic, i, line, e, "/payload/error", "refusal does not tell the caller what to change", pending.expectation.describe(), describeRefusal(payload.Error), string(e.InReplyTo))
	}
}

func (s *state) transcriptLoadResponse(i, line int, e protocol.Envelope, p protocol.TranscriptLoadResponse) {
	s.featureKeys(i, line, e, []string{protocol.FeatureTranscriptLoad})
	pending := s.pendingTranscriptLoads[e.InReplyTo]
	delete(s.pendingTranscriptLoads, e.InReplyTo)
	if pending != nil && pending.expectation != nil && pending.expectation.rung == rungDegradation {
		s.addExpected(pending.expectation.diagnostic, i, line, e, "/payload", pending.expectation.message, "a typed refusal naming "+pending.expectation.key, "a served page", string(e.InReplyTo))
	}
	if pending != nil && len(p.Entries) > pending.limit {
		s.addExpected(CodeTranscriptOverLimit, i, line, e, "/payload/entries", "a transcript page holds more entries than the request's limit allows", strconv.Itoa(pending.limit), strconv.Itoa(len(p.Entries)), string(e.InReplyTo))
	}
	seen := make(map[protocol.TranscriptEntryID]bool, len(p.Entries))
	for index, entry := range p.Entries {
		if seen[entry.EntryID] {
			s.addExpected(CodeDuplicateTranscriptEntry, i, line, e, "/payload/entries/"+strconv.Itoa(index)+"/entry_id", "a transcript page returns one entry twice", "one occurrence per entry", string(entry.EntryID))
		}
		seen[entry.EntryID] = true
	}
	if p.Gap == nil {
		return
	}
	switch {
	case pending != nil && !pending.cursor:
		s.addExpected(CodeTranscriptGapMismatch, i, line, e, "/payload/gap", "a read with no cursor starts at the oldest entry held, so it has no gap", "no gap", string(p.Gap.Reason), string(e.InReplyTo))
	case len(p.Entries) == 0 && p.Gap.ResumedAt != "":
		s.addExpected(CodeTranscriptGapMismatch, i, line, e, "/payload/gap/resumed_at", "a gap resumes at the first entry it returns, and this page returns none", "no resumed_at", string(p.Gap.ResumedAt))
	case len(p.Entries) > 0 && p.Gap.ResumedAt != p.Entries[0].EntryID:
		s.addExpected(CodeTranscriptGapMismatch, i, line, e, "/payload/gap/resumed_at", "a gap resumes at the first entry it returns", string(p.Entries[0].EntryID), string(p.Gap.ResumedAt))
	}
}

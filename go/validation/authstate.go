package validation

import (
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type authFlow struct {
	provider string
	next     uint64
	prompt   protocol.AuthPromptID
	terminal bool
	start    protocol.Envelope
	index    int
	line     int
}

func (s *state) authStartResponse(i, line int, e protocol.Envelope) {
	var p protocol.AuthLoginStartResponse
	_ = e.DecodePayload(&p)
	req := s.requests[e.InReplyTo]
	if req == nil || req.typ != protocol.TypeAuthLoginStartRequest {
		return
	}
	if _, exists := s.authFlows[p.FlowID]; exists {
		s.add(CodeAuthFlowOrder, i, line, e, "/payload/flow_id", "login start reused an active flow id")
		return
	}
	var requested protocol.AuthLoginStartRequest
	_ = req.envelope.DecodePayload(&requested)
	s.authFlows[p.FlowID] = &authFlow{
		provider: requested.ProviderID,
		next:     1,
		start:    e,
		index:    i,
		line:     line,
	}
}

func (s *state) authEvent(i, line int, e protocol.Envelope) {
	var p protocol.AuthLoginEvent
	_ = e.DecodePayload(&p)
	flow := s.authFlowFor(i, line, e, p.FlowID)
	if flow == nil {
		return
	}
	s.authSequence(i, line, e, flow)
	if p.ProviderID != flow.provider {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/provider_id", "auth event names a different provider", flow.provider, p.ProviderID)
	}
	if p.Kind == "prompt" {
		if flow.prompt != "" {
			s.add(CodeAuthFlowOrder, i, line, e, "/payload/prompt_id", "flow has an unanswered prompt")
		}
		flow.prompt = p.PromptID
	}
}

func (s *state) authReplyRequest(i, line int, e protocol.Envelope) {
	var p protocol.AuthLoginReplyRequest
	_ = e.DecodePayload(&p)
	flow := s.authFlowFor(i, line, e, p.FlowID)
	if flow == nil {
		return
	}
	if flow.prompt == "" || flow.prompt != p.PromptID {
		s.addExpected(CodeAuthPromptMismatch, i, line, e, "/payload/prompt_id", "auth reply does not answer the pending prompt", string(flow.prompt), string(p.PromptID))
	}
}

func (s *state) authReplyResponse(i, line int, e protocol.Envelope) {
	var p protocol.AuthLoginReplyResponse
	_ = e.DecodePayload(&p)
	req := s.requests[e.InReplyTo]
	if req == nil || req.typ != protocol.TypeAuthLoginReplyRequest {
		return
	}
	var requested protocol.AuthLoginReplyRequest
	_ = req.envelope.DecodePayload(&requested)
	if p.FlowID != requested.FlowID || p.PromptID != requested.PromptID {
		s.add(CodeAuthPromptMismatch, i, line, e, "/payload", "auth reply response changed the flow or prompt id")
		return
	}
	if flow := s.authFlows[p.FlowID]; flow != nil && p.Accepted && flow.prompt == p.PromptID {
		flow.prompt = ""
	}
}

func (s *state) authCancelRequest(i, line int, e protocol.Envelope) {
	var p protocol.AuthLoginCancelRequest
	_ = e.DecodePayload(&p)
	_ = s.authFlowFor(i, line, e, p.FlowID)
}

func (s *state) authCancelResponse(i, line int, e protocol.Envelope) {
	var p protocol.AuthLoginCancelResponse
	_ = e.DecodePayload(&p)
	req := s.requests[e.InReplyTo]
	if req == nil || req.typ != protocol.TypeAuthLoginCancelRequest {
		return
	}
	var requested protocol.AuthLoginCancelRequest
	_ = req.envelope.DecodePayload(&requested)
	if p.FlowID != requested.FlowID {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/flow_id", "auth cancel response changed the flow id", string(requested.FlowID), string(p.FlowID))
	}
}

func (s *state) authCompleted(i, line int, e protocol.Envelope) {
	var p protocol.AuthLoginCompleted
	_ = e.DecodePayload(&p)
	flow := s.authFlowFor(i, line, e, p.FlowID)
	if flow == nil {
		return
	}
	s.authSequence(i, line, e, flow)
	if p.ProviderID != flow.provider {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/provider_id", "auth terminal names a different provider", flow.provider, p.ProviderID)
	}
	flow.terminal = true
}

func (s *state) authFlowFor(i, line int, e protocol.Envelope, id protocol.AuthFlowID) *authFlow {
	flow := s.authFlows[id]
	if flow == nil {
		s.add(CodeAuthFlowOrder, i, line, e, "/payload/flow_id", "auth flow event or command preceded its start response")
		return nil
	}
	if flow.terminal {
		s.add(CodeEventAfterTerminal, i, line, e, "/payload/flow_id", "auth flow continued after its terminal")
		return nil
	}
	return flow
}

func (s *state) authSequence(i, line int, e protocol.Envelope, flow *authFlow) {
	if e.Sequence == nil {
		return
	}
	if *e.Sequence < flow.next {
		s.addExpected(CodeSequenceRegression, i, line, e, "/sequence", "auth flow sequence regressed", uintString(flow.next), uintString(*e.Sequence))
	} else if *e.Sequence > flow.next {
		s.addExpected(CodeSequenceGap, i, line, e, "/sequence", "auth flow sequence skipped an event", uintString(flow.next), uintString(*e.Sequence))
	}
	flow.next = *e.Sequence + 1
}

func (s *state) closeAuthFlows() {
	for id, flow := range s.authFlows {
		if flow.terminal {
			continue
		}
		s.add(CodeMissingAuthTerminal, flow.index, flow.line, flow.start, "/payload/flow_id", "accepted auth login flow has no auth.login.completed terminal")
		s.diagnostics[len(s.diagnostics)-1].RelatedIDs = []string{string(id)}
	}
}

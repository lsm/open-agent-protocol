package sdk

import (
	"context"
	"fmt"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

type AgentService struct {
	transport *transport
	timeout   time.Duration
}

func (s *AgentService) Run(ctx context.Context, req AgentRequest) (*CompletionResponse, error) {
	stream, err := s.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	for stream.Next() {
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	if stream.state == nil || stream.state.response == nil {
		return nil, &StreamError{Kind: KindTransportError, Message: "agent run ended without completion"}
	}
	return stream.state.response, nil
}

func (s *AgentService) Stream(ctx context.Context, req AgentRequest) (*AgentStream, error) {
	state, err := s.oapBegin(ctx, req)
	if err != nil {
		return nil, err
	}
	return &AgentStream{state: state}, nil
}

type AgentStream struct {
	state   *oapAgentState
	current AgentEvent
	err     error
	done    bool
}

func (s *AgentStream) Next() bool {
	if s.done || s.state == nil {
		return false
	}
	state := s.state
	for {
		in, err := state.sub.next(state.ctx, state.timeout, "OAP agent run")
		if err != nil {
			s.fail(err)
			return false
		}
		if run := in.run(); run != "" && run != state.runID {
			continue
		}
		if in.broken != nil {
			state.settled = true
			s.fail(in.broken)
			return false
		}
		p := in.body()
		switch in.kind() {
		case "run.started":
			if modelRef := p.str("model_id"); modelRef != "" {
				state.modelRef = modelRef
			}
			s.current = &AgentStart{SessionID: state.sessionID}
			return true
		case "content.delta":
			part := p.obj("part")
			switch part.str("type") {
			case "text":
				s.current = &TextDelta{Delta: part.str("text")}
				return true
			case "reasoning":
				s.current = &ThinkingDelta{Delta: part.str("reasoning")}
				return true
			}
		case "run.completed":
			modelRef := state.modelRef
			if modelRef == "" {
				modelRef = p.str("model_id")
			}
			response, err := oapResponse(p, modelRef, "final_response")
			if err != nil {
				state.settled = true
				s.fail(err)
				return false
			}
			state.response = response
			state.settled = true
			s.current = agentEndFromResponse(state.response)
			s.done = true
			return true
		case "run.failed", "run.cancelled", "error.response":
			state.settled = true
			s.fail(in.failure(providerIDFromRef(state.modelRef)))
			return false
		case "action.call.requested":
			if p.str("execution_owner") != sdkParticipant {
				state.callNames[p.str("tool_call_id")] = p.str("name")
				continue
			}
			if err := state.resolveCall(p); err != nil {
				s.fail(err)
				return false
			}
			continue
		case "action.call.started":
			s.current = &ToolExecutionStart{ToolCallID: p.str("tool_call_id"), ToolName: p.str("name")}
			return true
		case "action.call.completed", "action.call.failed", "action.call.cancelled":
			s.current = &ToolExecutionEnd{ToolCallID: p.str("tool_call_id"), IsError: in.kind() != "action.call.completed"}
			return true
		case "action.permission.requested":
			if p.str("responded_by") != sdkParticipant {
				continue
			}
			if err := state.resolvePermission(p); err != nil {
				state.settled = true
				s.fail(err)
				return false
			}
			continue
		case "action.permission.resolve.response":
			if accepted, _ := p["accepted"].(bool); !accepted {
				state.settled = true
				s.fail(&ProtocolError{Code: CodeMalformedResponse, Message: "the endpoint refused a permission answer"})
				return false
			}
			continue
		case "action.permission.resolved":
			continue
		case "action.call.resolve.response":
			if accepted, _ := p["accepted"].(bool); !accepted && p.str("reason") != "already_resolved" {
				state.settled = true
				s.fail(&ProtocolError{Code: CodeMalformedResponse, Message: "the endpoint refused a tool result: " + p.str("reason")})
				return false
			}
			continue
		case "session.state.updated", "run.status.updated":
			continue
		default:
			s.fail(&StreamError{Kind: KindTransportError, Message: fmt.Sprintf("unexpected OAP agent event %q", in.kind())})
			return false
		}
	}
}

func (s *AgentStream) Event() AgentEvent { return s.current }

func (s *AgentStream) Err() error { return s.err }

func (s *AgentStream) Close() error {
	state := s.state
	if state == nil {
		return s.err
	}
	if !state.settled {
		cancel := oapFrame(oapAgent, "run.cancel.request", map[string]any{
			"session_id": state.sessionID, "run_id": state.runID, "reason": "caller_closed"})
		cancel.SessionID = protocol.SessionID(state.sessionID)
		cancel.RunID = protocol.RunID(state.runID)
		state.transport.sendEnvelopeBestEffort(cancel)
	}
	state.sub.close()
	s.state = nil
	s.done = true
	return s.err
}

func (s *AgentStream) fail(err error) {
	s.err = err
	s.done = true
	s.current = nil
}

func agentEndFromResponse(response *CompletionResponse) *AgentEnd {
	return &AgentEnd{
		StopReason:   response.StopReason,
		Usage:        response.Usage,
		ErrorMessage: response.ErrorMessage,
		ProviderID:   response.ProviderID,
		API:          response.API,
	}
}

func findTool(tools []Tool, name string) *Tool {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}

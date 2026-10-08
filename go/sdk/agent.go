package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type AgentService struct {
	transport *transport
	timeout   time.Duration
}

func (s *AgentService) Run(ctx context.Context, req AgentRequest) (*CompletionResponse, error) {
	if s.transport != nil && !s.transport.legacyWire {
		return s.oapRun(ctx, req)
	}
	run, err := s.begin(ctx, req)
	if err != nil {
		return nil, err
	}
	defer run.close()

	var events []AgentEvent
	for {
		batch, err := run.pump()
		if err != nil {
			run.teardownAfter(err)
			return nil, withSessionID(err, run.sessionID)
		}
		if run.result != nil {
			run.teardown("completed", true)
			return responseOrAuthError(run.result, run.fallbackProvider)
		}
		for _, event := range batch {
			if errEvent, ok := event.(*ErrorEvent); ok {
				failure := streamErrorFromEvent(errEvent, run.fallbackProvider, run.sessionID)
				run.teardown("completed", true)
				return nil, failure
			}
			events = append(events, event)
			if _, ok := event.(*AgentEnd); ok {
				run.teardown("completed", true)
				return responseOrAuthError(buildResponseFromEvents(events), run.fallbackProvider)
			}
		}
	}
}

func (s *AgentService) Stream(ctx context.Context, req AgentRequest) (*AgentStream, error) {
	if s.transport != nil && !s.transport.legacyWire {
		return s.oapStream(ctx, req)
	}
	run, err := s.begin(ctx, req)
	if err != nil {
		return nil, err
	}
	return &AgentStream{run: run}, nil
}

func (s *AgentService) begin(ctx context.Context, req AgentRequest) (*agentRun, error) {
	if err := validateExecutionRequest(req.ModelRef, req.Messages); err != nil {
		return nil, err
	}
	sessionID := req.sessionID()
	if sessionID == "" {
		return nil, &ProtocolError{
			Code:    CodeInvalidRequest,
			Message: "options.session_id must be a 21-character alphanumeric NanoID",
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, abortError(err, "agent run")
	}

	run := &agentRun{
		ctx:               ctx,
		transport:         s.transport,
		timeout:           s.timeout,
		request:           req,
		sessionID:         sessionID,
		idClientGenerated: req.Options == nil || req.Options.SessionID == "",
		fallbackProvider:  providerIDFromRef(req.ModelRef),
		toolBuf:           newToolBuffer(),
		nextSequence:      1,
	}
	run.sub = s.transport.subscribeSession(sessionID)

	start := newSessionEnvelope("agent_start", sessionID, 1, map[string]any{
		"session_id": sessionID,

		"resume_session_id": sessionID,
		"config_json":       string(mustMarshal(map[string]any{"model_ref": req.ModelRef, "tools": serializeTools(req.Tools)})),
	})
	run.startMessageID = start.MessageID
	run.sub.correlate(start.MessageID)

	if err := s.transport.send(start); err != nil {
		run.sub.close()
		return nil, err
	}
	run.nextSequence = 2
	return run, nil
}

func (r AgentRequest) sessionID() string {
	if r.Options == nil || r.Options.SessionID == "" {
		return newNanoID()
	}
	if !isNanoID(r.Options.SessionID) {
		return ""
	}
	return r.Options.SessionID
}

type AgentStream struct {
	oapState *oapAgentState
	run      *agentRun
	pending  []AgentEvent
	current  AgentEvent
	err      error
	done     bool
	started  bool
}

func (s *AgentStream) Next() bool {
	if s.oapState != nil {
		return s.oapNext()
	}
	if s.done {
		return false
	}
	for {
		if len(s.pending) > 0 {
			event := s.pending[0]
			s.pending = s.pending[1:]

			switch value := event.(type) {
			case *ErrorEvent:
				s.fail(streamErrorFromEvent(value, s.run.fallbackProvider, s.run.sessionID))
				return false
			case *AgentEnd:

				if s.run.aggregateUsage != nil {
					value.Usage = s.run.aggregateUsage
				}
				s.done = true
				if value.StopReason == "error" && isAuthFailureMessage(value.ErrorMessage, value.API) {
					s.fail(newAuthRequiredError(firstNonEmpty(value.ProviderID, s.run.fallbackProvider), value.ErrorMessage))
					return false
				}
			case *MessageEnd:
				s.run.aggregateUsage = s.run.aggregateUsage.add(value.Usage)
			}

			if !s.started {
				s.started = true
				if _, ok := event.(*AgentStart); !ok {
					s.pending = append([]AgentEvent{event}, s.pending...)
					s.current = &AgentStart{SessionID: s.run.sessionID}
					s.done = false
					return true
				}
			}
			s.current = event
			return true
		}

		batch, err := s.run.pump()
		if err != nil {
			s.fail(withSessionID(err, s.run.sessionID))
			return false
		}
		if s.run.result != nil {

			s.pending = append(s.pending, agentEndFromResponse(s.run.result))
			s.run.result = nil
			continue
		}
		s.pending = append(s.pending, batch...)
	}
}

func (s *AgentStream) Event() AgentEvent { return s.current }

func (s *AgentStream) Err() error { return s.err }

func (s *AgentStream) Close() error {
	if s.oapState != nil {
		return s.oapClose()
	}
	if s.run == nil {
		return s.err
	}
	if s.err != nil && isAbort(s.err) {
		s.run.teardown("client aborted", false)
	} else {
		s.run.teardown("completed", true)
	}
	s.run.close()
	s.run = nil
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

type agentRun struct {
	ctx       context.Context
	transport *transport
	sub       *subscription
	timeout   time.Duration
	request   AgentRequest

	sessionID         string
	idClientGenerated bool
	fallbackProvider  string
	toolBuf           *toolBuffer

	startMessageID   string
	messageMessageID string
	startAccepted    bool
	messageSent      bool

	nextSequence int64

	unresolvedMessageSequence int64

	startReplyObserved bool
	foreignSession     bool
	stopped            bool

	aggregateUsage *Usage
	result         *CompletionResponse
}

func (r *agentRun) pump() ([]AgentEvent, error) {
	for {
		f, err := r.sub.nextFrame(r.ctx, r.timeout, "agent stream event")
		if err != nil {
			return nil, err
		}
		if f.Type == "ack" || f.Type == "agent_stopped" {
			continue
		}

		if !r.startAccepted && f.Type != "agent_started" && f.Type != "nack" && f.Type != "agent_error" {
			continue
		}

		switch f.Type {
		case "nack", "agent_error":
			if !r.startAccepted && f.InReplyTo != "" && f.InReplyTo != r.startMessageID {
				continue
			}
			r.startReplyObserved = true
			if r.messageSent && f.InReplyTo == r.messageMessageID {
				r.rollbackMessage()
			}
			var failure error
			if f.Type == "nack" {
				failure = nackToError(f, r.fallbackProvider, "", r.sessionID)
			} else {
				failure = errorFrameToError(f, r.fallbackProvider, "", r.sessionID)
			}
			r.noteForeignSession(failure)
			return nil, failure

		case "agent_started":
			if f.InReplyTo != "" && f.InReplyTo != r.startMessageID {
				continue
			}
			r.startAccepted = true
			r.startReplyObserved = true
			if !r.messageSent {
				if err := r.sendMessage(); err != nil {
					return nil, err
				}
			}
			continue

		case "tool_execute":
			r.unresolvedMessageSequence = 0
			if err := r.executeTool(f); err != nil {
				return nil, err
			}
			continue

		case "agent_result":
			r.unresolvedMessageSequence = 0
			payload, err := f.jsonPayload("result_json")
			if err != nil {
				return nil, err
			}
			r.result = parseAgentRunResponse(payload)
			return nil, nil

		case "result", "complete_response":
			r.unresolvedMessageSequence = 0
			r.result = parseCompletionResponse(f.payload())
			return nil, nil
		}

		events, err := normalizeAgentFrame(f, r.toolBuf)
		if err != nil {
			return nil, err
		}
		if len(events) == 0 {

			if f.Type == "agent_event" || f.Type == "event" {
				continue
			}
			return nil, &StreamError{
				Kind:      KindTransportError,
				Message:   fmt.Sprintf("unexpected frame type %q while awaiting an agent result", f.Type),
				SessionID: r.sessionID,
			}
		}
		r.unresolvedMessageSequence = 0
		return events, nil
	}
}

func (r *agentRun) sendMessage() error {
	messageJSON := map[string]any{
		"model_ref": r.request.ModelRef,
		"messages":  agentMessages(r.request.Messages),
		"tools":     serializeTools(r.request.Tools),
	}
	payload := map[string]any{
		"session_id":   r.sessionID,
		"message_json": string(mustMarshal(messageJSON)),
	}
	if options := serializeOptions(r.request.Options); len(options) > 0 {
		payload["options_json"] = string(mustMarshal(options))
	}

	envelope := newSessionEnvelope("agent_message", r.sessionID, 2, payload)
	r.messageMessageID = envelope.MessageID
	r.sub.correlate(envelope.MessageID)
	if err := r.transport.send(envelope); err != nil {
		return err
	}
	r.messageSent = true
	r.nextSequence = 3
	r.unresolvedMessageSequence = 2
	return nil
}

func (r *agentRun) rollbackMessage() {
	if r.unresolvedMessageSequence == 0 {
		return
	}
	r.nextSequence = r.unresolvedMessageSequence
	r.unresolvedMessageSequence = 0
}

func (r *agentRun) noteForeignSession(err error) {
	var streamErr *StreamError
	if asStreamError(err, &streamErr) && streamErr.Code == CodeAgentBusy && !r.startAccepted {
		r.foreignSession = true
	}
}

func (r *agentRun) executeTool(f *frame) error {
	payload := f.payload()
	invocation := ToolInvocation{
		ToolCallID:    payload.str("tool_call_id"),
		ToolName:      payload.str("tool_name"),
		ArgumentsJSON: payload.strOrDefault("{}", "args_json"),
	}

	tool := findTool(r.request.Tools, invocation.ToolName)
	if tool == nil || tool.Execute == nil {
		return r.sendToolResult(f, invocation.ToolCallID,
			fmt.Sprintf("Tool %q is not executable by this client", invocation.ToolName), true)
	}

	result, err := tool.Execute(r.ctx, invocation)
	if err != nil {

		return r.sendToolResult(f, invocation.ToolCallID, err.Error(), true)
	}
	return r.sendToolResult(f, invocation.ToolCallID, result, false)
}

func (r *agentRun) sendToolResult(request *frame, toolCallID, text string, isError bool) error {
	resultJSON, err := json.Marshal([]map[string]any{{"type": "text", "text": text}})
	if err != nil {
		return transportErrorf(err, "cannot encode tool result for %s", toolCallID)
	}
	return r.transport.send(newReplyEnvelope("tool_result", request, map[string]any{
		"tool_call_id": toolCallID,
		"result_json":  string(resultJSON),
		"is_error":     isError,
	}))
}

func (r *agentRun) teardown(reason string, probe bool) {
	if r.stopped {
		return
	}
	r.stopped = true
	if r.foreignSession {
		return
	}
	if !r.startReplyObserved && !r.idClientGenerated {
		return
	}
	if !probe || r.unresolvedMessageSequence == 0 {
		stopAgent(r.transport, r.sessionID, r.nextSequence, reason)
		r.sub.drain(drainIdle, drainBudget)
		return
	}
	r.stopWithSequenceProbe(reason)
}

func (r *agentRun) teardownAfter(err error) {
	if isAbort(err) {
		r.teardown("client aborted", false)
		return
	}
	r.teardown("completed", true)
}

func (r *agentRun) stopWithSequenceProbe(reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), drainBudget)
	defer cancel()

	candidates := []int64{r.unresolvedMessageSequence}
	if r.nextSequence != r.unresolvedMessageSequence {
		candidates = append(candidates, r.nextSequence)
	}
	for _, sequence := range candidates {
		messageID := stopAgent(r.transport, r.sessionID, sequence, reason)
		settled, retry := r.awaitStopReply(ctx, messageID)
		if settled || !retry {
			return
		}
	}
}

func (r *agentRun) awaitStopReply(ctx context.Context, messageID string) (settled, retry bool) {
	for {
		f, err := r.sub.nextFrame(ctx, drainBudget, "agent_stopped")
		if err != nil {
			return false, false
		}
		if f.InReplyTo != messageID {
			continue
		}
		if f.Type == "agent_stopped" {
			return true, false
		}
		if f.Type != "agent_error" && f.Type != "nack" {
			continue
		}
		payload := f.payload()
		switch payload.str("code", "error_code") {
		case CodeInvalidRequest, "invalid_sequence":
			return false, true
		default:
			return false, false
		}
	}
}

func (r *agentRun) close() {
	if r.sub != nil {
		r.sub.close()
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

func agentMessages(messages []Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		entry := map[string]any{"role": string(message.Role)}
		if len(message.Parts) > 0 {
			entry["content"] = message.Parts
		} else {
			entry["content"] = message.Text
		}
		if message.Name != "" {
			entry["name"] = message.Name
		}
		if message.ToolCallID != "" {
			entry["tool_call_id"] = message.ToolCallID
		}
		out = append(out, entry)
	}
	return out
}

func streamErrorFromEvent(event *ErrorEvent, fallbackProvider, sessionID string) error {
	providerID := firstNonEmpty(event.ProviderID, "")
	if providerID == "" && isAuthCode(event.Code) {
		providerID = fallbackProvider
	}
	if event.Code == CodeAuthRequired {
		return newAuthRequiredError(providerID, event.Message)
	}
	return &StreamError{
		Kind:       KindProviderError,
		Code:       event.Code,
		ProviderID: providerID,
		Message:    event.Message,
		SessionID:  sessionID,
	}
}

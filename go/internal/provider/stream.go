package provider

import (
	"encoding/json"
)

var reasoningFields = []string{"reasoning_content", "reasoning", "reasoning_text"}

func findReasoningField(delta map[string]any) (string, string, bool) {
	for _, field := range reasoningFields {
		value, ok := delta[field].(string)
		if ok && value != "" {
			return field, value, true
		}
	}
	return "", "", false
}

func calculateCost(usage Usage, rates Cost) Cost {
	perMillion := func(tokens int, rate float64) float64 {
		return float64(tokens) / 1_000_000.0 * rate
	}
	cost := Cost{
		Input:      perMillion(usage.InputTokens, rates.Input),
		Output:     perMillion(usage.OutputTokens, rates.Output),
		CacheRead:  perMillion(usage.CacheReadTokens, rates.CacheRead),
		CacheWrite: perMillion(usage.CacheWriteTokens, rates.CacheWrite),
	}
	cost.Total = cost.Input + cost.Output + cost.CacheRead + cost.CacheWrite
	return cost
}

func CanCompletePartialTextOnStreamError(textLen, thinkingLen, toolCallCount int) bool {
	return toolCallCount == 0 && (textLen > 0 || thinkingLen > 0)
}

func asObject(v any) (map[string]any, bool) {
	obj, ok := v.(map[string]any)
	return obj, ok
}

func asInt(v any) (int, bool) {
	number, ok := v.(float64)
	if !ok {
		return 0, false
	}
	return int(number), true
}

func parseUsageInto(container map[string]any, usage *Usage) {
	if value, ok := asInt(container["prompt_tokens"]); ok {
		usage.InputTokens = value
	}
	if details, ok := asObject(container["prompt_tokens_details"]); ok {
		if cached, ok := asInt(details["cached_tokens"]); ok {
			usage.CacheReadTokens = cached
			if usage.InputTokens > cached {
				usage.InputTokens -= cached
			}
		}
	}
	completion, _ := asInt(container["completion_tokens"])
	reasoning := 0
	if details, ok := asObject(container["completion_tokens_details"]); ok {
		if value, ok := asInt(details["reasoning_tokens"]); ok {
			reasoning = value
		}
	}
	usage.OutputTokens = completion + reasoning
	if value, ok := asInt(container["total_tokens"]); ok {
		usage.TotalTokens = value
	}
}

func stopReasonFor(finish string) StopReason {
	switch finish {
	case "length":
		return StopLength
	case "tool_calls":
		return StopToolUse
	case "content_filter":
		return StopError
	}
	return StopStop
}

func parseToolCallEvents(delta map[string]any) []toolCallEvent {
	list, ok := delta["tool_calls"].([]any)
	if !ok {
		return nil
	}
	var out []toolCallEvent
	for _, entry := range list {
		call, ok := asObject(entry)
		if !ok {
			continue
		}
		apiIndex := 0
		if value, ok := asInt(call["index"]); ok {
			apiIndex = value
		}
		if id, ok := call["id"].(string); ok && id != "" {
			name := ""
			if fn, ok := asObject(call["function"]); ok {
				if value, ok := fn["name"].(string); ok {
					name = value
				}
			}
			out = append(out, toolCallEvent{apiIndex: apiIndex, isStart: true, id: id, name: name})
		}
		if fn, ok := asObject(call["function"]); ok {
			if args, ok := fn["arguments"].(string); ok && args != "" {
				out = append(out, toolCallEvent{apiIndex: apiIndex, arguments: args, hasArguments: true})
			}
		}
	}
	return out
}

func parseReasoningDetails(delta map[string]any) []reasoningDetail {
	list, ok := delta["reasoning_details"].([]any)
	if !ok {
		return nil
	}
	var out []reasoningDetail
	for _, entry := range list {
		detail, ok := asObject(entry)
		if !ok {
			continue
		}
		kind, ok := detail["type"].(string)
		if !ok || kind != "reasoning.encrypted" {
			continue
		}
		id, ok := detail["id"].(string)
		if !ok || id == "" {
			continue
		}
		data, ok := detail["data"].(string)
		if !ok || data == "" {
			continue
		}
		out = append(out, reasoningDetail{
			toolCallID: id,
			detailJSON: `{"type":"reasoning.encrypted","id":` + encodeString(id) + `,"data":` + encodeString(data) + `}`,
		})
	}
	return out
}

func ParseChunk(data string) chunkResult {
	return parseChunkWithUsage(data, Usage{})
}

func parseChunkWithUsage(data string, current Usage) chunkResult {
	result := chunkResult{}
	if data == "[DONE]" {
		return result
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(data), &root); err != nil {
		return result
	}
	if container, ok := asObject(root["usage"]); ok {
		usage := current
		parseUsageInto(container, &usage)
		result.usage = &usage
	}
	choices, ok := root["choices"].([]any)
	if !ok || len(choices) == 0 {
		return result
	}
	choice, ok := asObject(choices[0])
	if !ok {
		return result
	}
	if finish, ok := choice["finish_reason"].(string); ok {
		result.stopReason = stopReasonFor(finish)
		result.hasStop = true
	}
	delta, ok := asObject(choice["delta"])
	if !ok {
		return result
	}
	_, result.hasToolCallsKey = delta["tool_calls"]
	result.toolCalls = parseToolCallEvents(delta)
	result.details = parseReasoningDetails(delta)
	if !result.hasToolCallsKey {
		if field, value, found := findReasoningField(delta); found {
			result.signatureField = field
			result.thinkDelta = value
		} else if content, ok := delta["content"].(string); ok && content != "" {
			result.textDelta = content
		}
	}
	return result
}

func (r chunkResult) hasThinking() bool { return r.thinkDelta != "" }

func (s *streamState) applyData(data string) {
	result := parseChunkWithUsage(data, s.usage)
	if result.usage != nil {
		s.usage = *result.usage
	}
	if result.hasStop {
		s.stopReason = result.stopReason
	}
	if result.signatureField != "" && !s.hasSig {
		s.signature = result.signatureField
		s.hasSig = true
	}
	s.thinking += result.thinkDelta
	s.text += result.textDelta
	s.lastToolCalls = result.toolCalls
	s.lastDetails = result.details
}

func canCarryPartial(s *streamState) bool {
	return CanCompletePartialTextOnStreamError(len(s.text), len(s.thinking), s.toolCalls)
}

func (s *streamState) assignPartIndexes() {
	base := s.nextIndex
	if s.thinking != "" {
		s.thinkingIndex = base
		base++
	}
	if s.text != "" {
		s.textIndex = base
		base++
	}
	s.textToolBase = base
}

func (s *streamState) emitFor(sink *EventSink) {
	partial := s.partial(nil)
	s.assignPartIndexes()
	if len(s.text) > s.prevText {
		sink.emit(Event{Kind: EventTextDelta, ContentIndex: s.textIndex, Delta: s.text[s.prevText:], Partial: partial})
	}
	s.prevText = len(s.text)
	if len(s.thinking) > s.prevThink && !IsKimiModel(s.model) {
		sink.emit(Event{Kind: EventThinkingDelta, ContentIndex: s.thinkingIndex, Delta: s.thinking[s.prevThink:], Partial: partial})
	}
	s.prevThink = len(s.thinking)
	for _, detail := range s.lastDetails {
		s.tracker.setSignatureByID(detail.toolCallID, detail.detailJSON)
	}
	for _, call := range s.lastToolCalls {
		if call.isStart {
			index := s.textToolBase
			s.textToolBase++
			s.tracker.startCall(call.apiIndex, index, call.id, call.name)
			s.nextIndex++
			s.toolCalls++
			sink.emit(Event{Kind: EventToolCallStart, ContentIndex: index, ID: call.id, Name: call.name, Partial: partial})
			continue
		}
		if !call.hasArguments {
			continue
		}
		if !s.tracker.appendDelta(call.apiIndex, call.arguments) {
			continue
		}
		if index, ok := s.tracker.contentIndex(call.apiIndex); ok {
			sink.emit(Event{Kind: EventToolCallDelta, ContentIndex: index, Delta: call.arguments, Partial: partial})
		}
	}
	s.lastToolCalls = nil
	s.lastDetails = nil
}

func (s *streamState) finish(sink *EventSink) {
	if s.usage.TotalTokens == 0 {
		s.usage.TotalTokens = s.usage.InputTokens + s.usage.OutputTokens + s.usage.CacheReadTokens + s.usage.CacheWriteTokens
	}
	s.usage.Cost = calculateCost(s.usage, s.model.Cost)
	hasThinking := s.thinking != ""
	hasText := s.text != ""
	if !hasThinking && !hasText && s.toolCalls == 0 {
		sink.emit(Event{
			Kind:    EventDone,
			Message: &AssistantMessage{Content: []AssistantBlock{{Text: &TextPart{}}}, API: s.model.API, Provider: s.model.Provider, Model: s.model.ID, Usage: s.usage, StopReason: s.stopReason, Timestamp: s.clock.millis()},
		})
		return
	}

	content := []AssistantBlock{}
	if hasThinking {
		thinking := &ThinkingPart{Thinking: s.thinking}
		if s.hasSig {
			thinking.Signature = s.signature
		}
		content = append(content, AssistantBlock{Thinking: thinking})
	}
	if hasText {
		content = append(content, AssistantBlock{Text: &TextPart{Text: s.text}})
	}
	for _, call := range s.tracker.inContentOrder() {
		completed, ok := s.tracker.completeCall(call.apiIndex)
		if !ok {
			continue
		}
		content = append(content, AssistantBlock{ToolCall: &completed})
		index, ok := s.tracker.contentIndex(call.apiIndex)
		if !ok {
			continue
		}
		grown := append([]AssistantBlock{}, content[:len(content)-1]...)
		sink.emit(Event{
			Kind:         EventToolCallEnd,
			ContentIndex: index,
			ToolCall:     &completed,
			Partial:      s.partial(grown),
		})
	}
	sink.emit(Event{
		Kind: EventDone,
		Message: &AssistantMessage{
			Content:    content,
			API:        s.model.API,
			Provider:   s.model.Provider,
			Model:      s.model.ID,
			Usage:      s.usage,
			StopReason: s.stopReason,
			Timestamp:  s.clock.millis(),
		},
	})
}

func (s *streamState) startEvent() Event {
	return Event{
		Kind: EventStart,
		Partial: PartialMessage{
			API:        s.model.API,
			Provider:   s.model.Provider,
			Model:      s.model.ID,
			StopReason: StopStop,
			Timestamp:  s.clock.millis(),
		},
	}
}

func (s *streamState) completeOnStreamError(sink *EventSink) bool {
	if !CanCompletePartialTextOnStreamError(len(s.text), len(s.thinking), s.toolCalls) {
		return false
	}
	s.stopReason = StopLength
	s.finish(sink)
	return true
}

func (s *EventSink) fail(reason string) {
	s.err = reason
	s.done = true
	s.emit(Event{Kind: EventError, Reason: reason})
}

type ReadChunkFunc func() ([]byte, error)

type CancelledFunc func() bool

func Stream(sink *EventSink, model Model, ctx Context, options StreamOptions, read ReadChunkFunc, cancelled CancelledFunc) {
	state := newStreamState(model)
	state.clock = &streamClock{now: options.Now, pingMillis: options.PingMillis}
	sink.emit(state.startEvent())
	parser := NewSSEParser()
	for {
		if state.clock.pingMillis > 0 {
			now := state.clock.millis()
			if now-state.clock.lastPing >= state.clock.pingMillis {
				sink.emit(Event{Kind: EventKeepalive})
				state.clock.lastPing = now
			}
		}
		if cancelled != nil && cancelled() {
			sink.fail("request cancelled")
			return
		}
		chunk, err := read()
		if err != nil {
			sink.fail("read error")
			return
		}
		if len(chunk) == 0 {
			break
		}
		events, feedErr := parser.Feed(chunk)
		if feedErr != nil {
			sink.fail(SSEErrorMessage(feedErr))
			return
		}
		for _, event := range events {
			if cancelled != nil && cancelled() {
				sink.fail("request cancelled")
				return
			}
			state.lastToolCalls = nil
			state.lastDetails = nil
			state.applyData(event.Data)
			state.emitFor(sink)
		}
	}
	state.finish(sink)
}

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

func (s *streamState) openIndex(kind string) (int, bool) {
	if !s.open {
		return 0, false
	}
	last := len(s.parts) - 1
	if s.parts[last].kind != kind {
		return 0, false
	}
	return last, true
}

func (s *streamState) openToolCall(apiIndex int) (int, bool) {
	index, ok := s.openIndex("tool_call")
	if !ok || s.parts[index].apiIndex != apiIndex {
		return 0, false
	}
	return index, true
}

func (s *streamState) openToolCallNamed(id string) bool {
	index, ok := s.openIndex("tool_call")
	if !ok {
		return false
	}
	call, tracked := s.tracker.byAPI[s.parts[index].apiIndex]
	return tracked && call.id == id
}

func (s *streamState) begin(part streamPart) int {
	s.parts = append(s.parts, part)
	s.open = true
	return len(s.parts) - 1
}

func (s *streamState) continueOrBegin(sink *EventSink, kind string, start int) int {
	if index, ok := s.openIndex(kind); ok {
		return index
	}
	s.close(sink)
	index := s.begin(streamPart{kind: kind, start: start})
	if kind == "text" {
		sink.emit(Event{Kind: EventTextStart, ContentIndex: index, Partial: s.partial(nil)})
	} else {
		sink.emit(Event{Kind: EventThinkingStart, ContentIndex: index, Partial: s.partial(nil)})
	}
	return index
}

func (s *streamState) block(part streamPart) AssistantBlock {
	switch part.kind {
	case "text":
		return AssistantBlock{Text: &TextPart{Text: s.text[part.start:part.end]}}
	case "thinking":
		thinking := &ThinkingPart{Thinking: s.thinking[part.start:part.end]}
		if s.hasSig {
			thinking.Signature = s.signature
		}
		return AssistantBlock{Thinking: thinking}
	}
	call, _ := s.tracker.completeCall(part.apiIndex)
	return AssistantBlock{ToolCall: &call}
}

func (s *streamState) covered(kind string) int {
	covered := 0
	for _, part := range s.parts {
		if part.kind == kind {
			covered = part.end
		}
	}
	return covered
}

func (s *streamState) finishTail(sink *EventSink, kind string) {
	buffer := s.text
	deltaKind := EventTextDelta
	if kind == "thinking" {
		buffer = s.thinking
		deltaKind = EventThinkingDelta
	}
	start := s.covered(kind)
	if len(buffer) <= start {
		return
	}
	index := s.continueOrBegin(sink, kind, start)
	sink.emit(Event{Kind: deltaKind, ContentIndex: index, Delta: buffer[start:], Partial: s.partial(nil)})
	s.close(sink)
}

func (s *streamState) close(sink *EventSink) {
	if !s.open {
		return
	}
	s.open = false
	index := len(s.parts) - 1
	part := &s.parts[index]
	switch part.kind {
	case "text":
		part.end = len(s.text)
	case "thinking":
		part.end = len(s.thinking)
	}
	blocks := make([]AssistantBlock, 0, index+1)
	for _, earlier := range s.parts[:index+1] {
		blocks = append(blocks, s.block(earlier))
	}
	ended := blocks[index]
	switch part.kind {
	case "text":
		sink.emit(Event{Kind: EventTextEnd, ContentIndex: index, Delta: ended.Text.Text, Partial: s.partial(blocks)})
	case "thinking":
		sink.emit(Event{Kind: EventThinkingEnd, ContentIndex: index, Delta: ended.Thinking.Thinking, Partial: s.partial(blocks)})
	default:
		sink.emit(Event{Kind: EventToolCallEnd, ContentIndex: index, ToolCall: ended.ToolCall, Partial: s.partial(blocks[:index])})
	}
}

func (s *streamState) emitFor(sink *EventSink) {
	for _, detail := range s.lastDetails {
		if s.openToolCallNamed(detail.toolCallID) {
			s.tracker.setSignatureByID(detail.toolCallID, detail.detailJSON)
		}
	}
	if len(s.thinking) > s.prevThink && !IsKimiModel(s.model) {
		index := s.continueOrBegin(sink, "thinking", s.prevThink)
		sink.emit(Event{Kind: EventThinkingDelta, ContentIndex: index, Delta: s.thinking[s.prevThink:], Partial: s.partial(nil)})
	}
	s.prevThink = len(s.thinking)
	if len(s.text) > s.prevText {
		index := s.continueOrBegin(sink, "text", s.prevText)
		sink.emit(Event{Kind: EventTextDelta, ContentIndex: index, Delta: s.text[s.prevText:], Partial: s.partial(nil)})
	}
	s.prevText = len(s.text)
	for _, call := range s.lastToolCalls {
		if call.isStart {
			if _, tracked := s.tracker.byAPI[call.apiIndex]; tracked {
				continue
			}
			s.close(sink)
			index := s.begin(streamPart{kind: "tool_call", apiIndex: call.apiIndex})
			s.tracker.startCall(call.apiIndex, index, call.id, call.name)
			s.toolCalls++
			sink.emit(Event{Kind: EventToolCallStart, ContentIndex: index, ID: call.id, Name: call.name, Partial: s.partial(nil)})
			continue
		}
		if !call.hasArguments {
			continue
		}
		index, open := s.openToolCall(call.apiIndex)
		if !open || !s.tracker.appendDelta(call.apiIndex, call.arguments) {
			continue
		}
		sink.emit(Event{Kind: EventToolCallDelta, ContentIndex: index, Delta: call.arguments, Partial: s.partial(nil)})
	}
	s.lastToolCalls = nil
	s.lastDetails = nil
}

func (s *streamState) finish(sink *EventSink) {
	if s.usage.TotalTokens == 0 {
		s.usage.TotalTokens = s.usage.InputTokens + s.usage.OutputTokens + s.usage.CacheReadTokens + s.usage.CacheWriteTokens
	}
	s.usage.Cost = calculateCost(s.usage, s.model.Cost)
	s.close(sink)
	s.finishTail(sink, "thinking")
	s.finishTail(sink, "text")
	content := make([]AssistantBlock, 0, len(s.parts))
	for _, part := range s.parts {
		content = append(content, s.block(part))
	}
	if len(content) == 0 {
		content = []AssistantBlock{{Text: &TextPart{}}}
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

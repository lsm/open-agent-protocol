package provider

import "encoding/json"

type anthropicEventKind string

const (
	anthropicNone         anthropicEventKind = ""
	anthropicMessageStart anthropicEventKind = "message_start"
	anthropicBlockStart   anthropicEventKind = "content_block_start"
	anthropicBlockDelta   anthropicEventKind = "content_block_delta"
	anthropicBlockStop    anthropicEventKind = "content_block_stop"
	anthropicMessageDelta anthropicEventKind = "message_delta"
	anthropicMessageStop  anthropicEventKind = "message_stop"
	anthropicAPIError     anthropicEventKind = "error"
)

type anthropicBlockType string

const (
	blockText     anthropicBlockType = "text"
	blockThinking anthropicBlockType = "thinking"
	blockToolUse  anthropicBlockType = "tool_use"
)

type anthropicDeltaType string

const (
	deltaNone      anthropicDeltaType = ""
	deltaText      anthropicDeltaType = "text_delta"
	deltaThinking  anthropicDeltaType = "thinking_delta"
	deltaSignature anthropicDeltaType = "signature_delta"
	deltaInputJSON anthropicDeltaType = "input_json_delta"
)

type anthropicEvent struct {
	kind      anthropicEventKind
	blockType anthropicBlockType
	deltaType anthropicDeltaType
	index     int
	text      string
	toolID    string
	toolName  string
	usage     Usage
	stop      string
	errMsg    string
}

func parseAnthropicEvent(data string) anthropicEvent {
	var root map[string]any
	if err := json.Unmarshal([]byte(data), &root); err != nil {
		return anthropicEvent{}
	}
	rawType, ok := root["type"].(string)
	if !ok {
		return anthropicEvent{}
	}

	switch anthropicEventKind(rawType) {
	case anthropicMessageStart:
		message, ok := asObject(root["message"])
		if !ok {
			return anthropicEvent{}
		}
		usageObj, ok := asObject(message["usage"])
		if !ok {
			return anthropicEvent{}
		}
		event := anthropicEvent{kind: anthropicMessageStart}
		if value, ok := asInt(usageObj["input_tokens"]); ok {
			event.usage.InputTokens = value
		}
		if value, ok := asInt(usageObj["output_tokens"]); ok {
			event.usage.OutputTokens = value
		}
		if value, ok := asInt(usageObj["cache_read_input_tokens"]); ok {
			event.usage.CacheReadTokens = value
		}
		if value, ok := asInt(usageObj["cache_creation_input_tokens"]); ok {
			event.usage.CacheWriteTokens = value
		}
		return event

	case anthropicBlockStart:
		index, ok := asInt(root["index"])
		if !ok {
			return anthropicEvent{}
		}
		block, ok := asObject(root["content_block"])
		if !ok {
			return anthropicEvent{}
		}
		rawBlock, ok := block["type"].(string)
		if !ok {
			return anthropicEvent{}
		}
		blockType := anthropicBlockType(rawBlock)
		switch blockType {
		case blockText, blockThinking, blockToolUse:
		default:
			return anthropicEvent{}
		}
		event := anthropicEvent{kind: anthropicBlockStart, blockType: blockType, index: index}
		if blockType == blockToolUse {
			if value, ok := block["id"].(string); ok {
				event.toolID = value
			}
			if value, ok := block["name"].(string); ok {
				event.toolName = value
			}
		}
		return event

	case anthropicBlockDelta:
		index, ok := asInt(root["index"])
		if !ok {
			return anthropicEvent{}
		}
		delta, ok := asObject(root["delta"])
		if !ok {
			return anthropicEvent{}
		}
		rawDelta, ok := delta["type"].(string)
		if !ok {
			return anthropicEvent{}
		}
		event := anthropicEvent{kind: anthropicBlockDelta, index: index}
		switch anthropicDeltaType(rawDelta) {
		case deltaText:
			if value, ok := delta["text"].(string); ok {
				event.deltaType, event.text = deltaText, value
			}
		case deltaThinking:
			if value, ok := delta["thinking"].(string); ok {
				event.deltaType, event.text = deltaThinking, value
			}
		case deltaSignature:
			if value, ok := delta["signature"].(string); ok {
				event.deltaType, event.text = deltaSignature, value
			}
		case deltaInputJSON:
			if value, ok := delta["partial_json"].(string); ok {
				event.deltaType, event.text = deltaInputJSON, value
			}
		}
		return event

	case anthropicBlockStop:
		index, ok := asInt(root["index"])
		if !ok {
			return anthropicEvent{}
		}
		return anthropicEvent{kind: anthropicBlockStop, index: index}

	case anthropicMessageDelta:
		event := anthropicEvent{kind: anthropicMessageDelta, stop: "stop"}
		if delta, ok := asObject(root["delta"]); ok {
			if reason, ok := delta["stop_reason"].(string); ok {
				switch reason {
				case "max_tokens":
					event.stop = "length"
				case "tool_use":
					event.stop = "tool_use"
				default:
					event.stop = "stop"
				}
			}
		}
		if usageObj, ok := asObject(root["usage"]); ok {
			if value, ok := asInt(usageObj["output_tokens"]); ok {
				event.usage.OutputTokens = value
			}
		}
		return event

	case anthropicMessageStop:
		return anthropicEvent{kind: anthropicMessageStop}

	case anthropicAPIError:
		errMsg := "anthropic api error"
		if holder, ok := asObject(root["error"]); ok {
			if message, ok := holder["message"].(string); ok {
				errMsg = message
			}
		}
		return anthropicEvent{kind: anthropicAPIError, errMsg: errMsg}
	}
	return anthropicEvent{}
}

type blockInfo struct {
	contentType  anthropicBlockType
	contentIndex int
}

type anthropicState struct {
	model       Model
	clock       *streamClock
	usage       Usage
	stopReason  string
	blocks      map[int]blockInfo
	completed   []AssistantBlock
	currentText string
	thinking    string
	signature   string
	tracker     *toolTracker
}

func newAnthropicState(model Model) *anthropicState {
	return &anthropicState{
		model:      model,
		clock:      &streamClock{},
		stopReason: "stop",
		blocks:     map[int]blockInfo{},
		tracker:    newToolTracker(),
	}
}

func (s *anthropicState) partial(content []AssistantBlock) PartialMessage {
	return PartialMessage{
		Content:    content,
		API:        s.model.API,
		Provider:   s.model.Provider,
		Model:      s.model.ID,
		Usage:      s.usage,
		StopReason: s.stopReason,
		Timestamp:  s.clock.millis(),
	}
}

func (s *anthropicState) apply(event anthropicEvent, sink *EventSink) bool {
	switch event.kind {
	case anthropicMessageStart:
		s.usage.InputTokens = event.usage.InputTokens
		s.usage.OutputTokens = event.usage.OutputTokens
		s.usage.CacheReadTokens = event.usage.CacheReadTokens
		s.usage.CacheWriteTokens = event.usage.CacheWriteTokens

	case anthropicBlockStart:
		contentIndex := len(s.completed)
		switch event.blockType {
		case blockText:
			s.currentText = ""
			sink.emit(Event{Kind: EventTextStart, ContentIndex: contentIndex, Partial: s.partial(nil)})
		case blockThinking:
			s.thinking = ""
			s.signature = ""
			sink.emit(Event{Kind: EventThinkingStart, ContentIndex: contentIndex, Partial: s.partial(nil)})
		case blockToolUse:
			s.tracker.startCall(event.index, contentIndex, event.toolID, event.toolName)
			sink.emit(Event{
				Kind:         EventToolCallStart,
				ContentIndex: contentIndex,
				ID:           event.toolID,
				Name:         event.toolName,
				Partial:      s.partial(nil),
			})
		}
		s.blocks[event.index] = blockInfo{contentType: event.blockType, contentIndex: contentIndex}

	case anthropicBlockDelta:
		info, known := s.blocks[event.index]
		if !known {
			return true
		}
		switch event.deltaType {
		case deltaText:
			s.currentText += event.text
			sink.emit(Event{Kind: EventTextDelta, ContentIndex: info.contentIndex, Delta: event.text, Partial: s.partial(nil)})
		case deltaThinking:
			s.thinking += event.text
			sink.emit(Event{Kind: EventThinkingDelta, ContentIndex: info.contentIndex, Delta: event.text, Partial: s.partial(nil)})
		case deltaSignature:
			s.signature += event.text
		case deltaInputJSON:
			s.tracker.appendDelta(event.index, event.text)
			if index, ok := s.tracker.contentIndex(event.index); ok {
				sink.emit(Event{Kind: EventToolCallDelta, ContentIndex: index, Delta: event.text, Partial: s.partial(nil)})
			}
		}

	case anthropicBlockStop:
		info, known := s.blocks[event.index]
		if !known {
			return true
		}
		switch info.contentType {
		case blockText:
			s.completed = append(s.completed, AssistantBlock{Text: &TextPart{Text: s.currentText}})
			sink.emit(Event{
				Kind:         EventTextEnd,
				ContentIndex: info.contentIndex,
				Delta:        s.currentText,
				Partial:      s.partial(nil),
			})
		case blockThinking:
			thinking := &ThinkingPart{Thinking: s.thinking}
			if s.signature != "" {
				thinking.Signature = s.signature
			}
			s.completed = append(s.completed, AssistantBlock{Thinking: thinking})
			sink.emit(Event{
				Kind:         EventThinkingEnd,
				ContentIndex: info.contentIndex,
				Delta:        s.thinking,
				Partial:      s.partial(nil),
			})
		case blockToolUse:
			completed, ok := s.tracker.completeCall(event.index)
			if !ok {
				return true
			}
			s.completed = append(s.completed, AssistantBlock{ToolCall: &completed})
			sink.emit(Event{
				Kind:         EventToolCallEnd,
				ContentIndex: len(s.completed) - 1,
				ToolCall:     &completed,
				Partial:      s.partial(nil),
			})
		}

	case anthropicMessageDelta:
		s.stopReason = event.stop
		s.usage.OutputTokens = event.usage.OutputTokens
		s.usage.Cost = calculateCost(s.usage, s.model.Cost)
	}
	return true
}

func (s *anthropicState) finish(sink *EventSink) {
	sink.emit(Event{
		Kind: EventStart,
		Partial: PartialMessage{
			API: s.model.API, Provider: s.model.Provider, Model: s.model.ID,
			StopReason: "stop", Timestamp: s.clock.millis(),
		},
	})
}

func emptyResponseError(rawBody string) string {
	if rawBody != "" {
		var body map[string]any
		if err := json.Unmarshal([]byte(rawBody), &body); err == nil {
			if kind, ok := body["type"].(string); ok && kind == "error" {
				if holder, ok := asObject(body["error"]); ok {
					if message, ok := holder["message"].(string); ok {
						return message
					}
				}
				return "anthropic api error"
			}
		}
		return "anthropic: empty response (" + itoa(len(rawBody)) + " raw bytes, no SSE events)"
	}
	return "anthropic returned empty response with no content blocks"
}

func itoa(n int) string {
	encoded, _ := json.Marshal(n)
	return string(encoded)
}

func StreamAnthropic(sink *EventSink, model Model, ctx Context, options AnthropicOptions, read ReadChunkFunc, cancelled CancelledFunc, rawBody func() string) {
	state := newAnthropicState(model)
	state.clock = &streamClock{now: options.Now, pingMillis: options.PingMillis}
	state.finish(sink)

	parser := NewSSEParser()
	handle := func(events []SSEEvent) bool {
		for _, event := range events {
			parsed := parseAnthropicEvent(event.Data)
			if parsed.kind == anthropicAPIError {
				sink.fail(parsed.errMsg)
				return false
			}
			state.apply(parsed, sink)
		}
		return true
	}

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
		parsed, feedErr := parser.Feed(chunk)
		if feedErr != nil {
			sink.fail(SSEErrorMessage(feedErr))
			return
		}
		if !handle(parsed) {
			return
		}
	}

	tail, tailErr := parser.Feed([]byte("\n\n"))
	if tailErr != nil {
		sink.fail(SSEErrorMessage(tailErr))
		return
	}
	for _, event := range tail {
		flushed := parseAnthropicEvent(event.Data)
		if flushed.kind == anthropicAPIError {
			sink.fail(flushed.errMsg)
			return
		}
	}

	if state.usage.TotalTokens == 0 {
		state.usage.TotalTokens = state.usage.InputTokens + state.usage.OutputTokens
	}
	state.usage.Cost = calculateCost(state.usage, state.model.Cost)

	if len(state.completed) == 0 && state.currentText != "" {
		state.completed = append(state.completed, AssistantBlock{Text: &TextPart{Text: state.currentText}})
	}
	if len(state.completed) == 0 {
		body := ""
		if rawBody != nil {
			body = rawBody()
		}
		sink.fail(emptyResponseError(body))
		return
	}
	sink.emit(Event{
		Kind: EventDone,
		Message: &AssistantMessage{
			Content:    state.completed,
			API:        model.API,
			Provider:   model.Provider,
			Model:      model.ID,
			Usage:      state.usage,
			StopReason: state.stopReason,
			Timestamp:  state.clock.millis(),
		},
	})
}

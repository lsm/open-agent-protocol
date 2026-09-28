package provider

import "sort"

type EventKind string

const (
	EventStart         EventKind = "start"
	EventTextStart     EventKind = "text_start"
	EventTextDelta     EventKind = "text_delta"
	EventTextEnd       EventKind = "text_end"
	EventThinkingStart EventKind = "thinking_start"
	EventThinkingDelta EventKind = "thinking_delta"
	EventThinkingEnd   EventKind = "thinking_end"
	EventToolCallStart EventKind = "toolcall_start"
	EventToolCallDelta EventKind = "toolcall_delta"
	EventToolCallEnd   EventKind = "toolcall_end"
	EventDone          EventKind = "done"
	EventError         EventKind = "error"
	EventKeepalive     EventKind = "keepalive"
)

type Usage struct {
	InputTokens      int
	OutputTokens     int
	TotalTokens      int
	CacheReadTokens  int
	CacheWriteTokens int
	Cost             Cost
}

type Cost struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
	Total      float64
}

type PartialMessage struct {
	Content    []AssistantBlock
	API        string
	Provider   string
	Model      string
	Usage      Usage
	StopReason string
	Timestamp  int64
}

type Event struct {
	Kind         EventKind
	ContentIndex int
	Delta        string
	ID           string
	Name         string
	ToolCall     *ToolCall
	Partial      PartialMessage
	Reason       string
	Message      *AssistantMessage
}

type AssistantMessage struct {
	Content    []AssistantBlock
	API        string
	Provider   string
	Model      string
	Usage      Usage
	StopReason string
	Timestamp  int64
}

type EventSink struct {
	events []Event
	err    string
	done   bool
}

func (s *EventSink) emit(event Event) {
	s.events = append(s.events, event)
}

func (s *EventSink) take() []Event {
	out := s.events
	s.events = nil
	return out
}

type trackedCall struct {
	apiIndex     int
	contentIndex int
	id           string
	name         string
	arguments    string
	signature    string
	hasSignature bool
}

type toolTracker struct {
	byAPI     map[int]*trackedCall
	completed []int
}

func newToolTracker() *toolTracker {
	return &toolTracker{byAPI: map[int]*trackedCall{}}
}

func (t *toolTracker) startCall(apiIndex, contentIndex int, id, name string) {
	t.byAPI[apiIndex] = &trackedCall{apiIndex: apiIndex, contentIndex: contentIndex, id: id, name: name}
}

func (t *toolTracker) appendDelta(apiIndex int, delta string) bool {
	call, ok := t.byAPI[apiIndex]
	if !ok {
		return false
	}
	call.arguments += delta
	return true
}

func (t *toolTracker) contentIndex(apiIndex int) (int, bool) {
	call, ok := t.byAPI[apiIndex]
	if !ok {
		return 0, false
	}
	return call.contentIndex, true
}

func (t *toolTracker) setSignatureByID(id, signature string) {
	for _, call := range t.byAPI {
		if call.id == id {
			call.signature = signature
			call.hasSignature = true
		}
	}
}

func (t *toolTracker) inContentOrder() []*trackedCall {
	order := make([]*trackedCall, 0, len(t.byAPI))
	for _, call := range t.byAPI {
		order = append(order, call)
	}
	sort.SliceStable(order, func(i, j int) bool {
		return order[i].contentIndex < order[j].contentIndex
	})
	return order
}

func (t *toolTracker) completeCall(apiIndex int) (ToolCall, bool) {
	call, ok := t.byAPI[apiIndex]
	if !ok {
		return ToolCall{}, false
	}
	return ToolCall{
		ID:         call.id,
		Name:       call.name,
		Arguments:  call.arguments,
		ThoughtSig: call.signature,
		HasThought: call.hasSignature,
	}, true
}

type streamClock struct {
	now        func() int64
	pingMillis int64
	lastPing   int64
}

func (c *streamClock) millis() int64 {
	if c.now == nil {
		return 0
	}
	return c.now()
}

type streamState struct {
	model         Model
	clock         *streamClock
	usage         Usage
	stopReason    string
	thinking      string
	text          string
	signature     string
	hasSig        bool
	nextIndex     int
	toolCalls     int
	tracker       *toolTracker
	prevText      int
	prevThink     int
	lastToolCalls []toolCallEvent
	lastDetails   []reasoningDetail
}

func newStreamState(model Model) *streamState {
	return &streamState{model: model, stopReason: "stop", tracker: newToolTracker(), clock: &streamClock{}}
}

func (s *streamState) partial(content []AssistantBlock) PartialMessage {
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

type toolCallEvent struct {
	apiIndex     int
	isStart      bool
	id           string
	name         string
	arguments    string
	hasArguments bool
}

type reasoningDetail struct {
	toolCallID string
	detailJSON string
}

type chunkResult struct {
	usage           *Usage
	stopReason      string
	hasStop         bool
	toolCalls       []toolCallEvent
	details         []reasoningDetail
	hasToolCallsKey bool
	textDelta       string
	thinkDelta      string
	signatureField  string
}

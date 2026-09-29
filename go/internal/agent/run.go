package agent

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/lsm/open-agent-protocol/go/internal/provider"
)

const eventBuffer = 64

const roomPoll = time.Millisecond

type Config struct {
	Model         provider.Model
	Streamer      Streamer
	MaxIterations int
	Options       provider.StreamOptions
}

type Result struct {
	Messages     []provider.Message
	FinalMessage provider.AssistantContent
	Turns        int
	Termination  Termination
}

type Run struct {
	config Config
	ctx    context.Context
	cancel context.CancelFunc

	events chan Event
	done   chan struct{}

	mu      sync.Mutex
	settled bool
	result  Result
	err     error
	pending map[string]pendingCall
}

func Start(ctx context.Context, config Config, prompts []provider.Message) *Run {
	runCtx, cancel := context.WithCancel(ctx)
	run := &Run{
		config:  config,
		ctx:     runCtx,
		cancel:  cancel,
		events:  make(chan Event, eventBuffer),
		done:    make(chan struct{}),
		pending: map[string]pendingCall{},
	}
	go func() {
		defer close(run.done)
		defer close(run.events)
		defer cancel()
		run.loop(prompts)
	}()
	return run
}

func (r *Run) Events() <-chan Event { return r.events }

func (r *Run) Wait() Result { <-r.done; return r.Result() }

func (r *Run) Result() Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result
}

func (r *Run) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *Run) Cancel() { r.cancel() }

type pendingCall struct {
	call   provider.ToolCall
	waiter chan provider.ToolResult
}

func (r *Run) ResolveTool(toolCallID string, result provider.ToolResult) error {
	r.mu.Lock()
	pending, found := r.pending[toolCallID]
	if found {
		delete(r.pending, toolCallID)
	}
	r.mu.Unlock()
	if !found {
		return fmt.Errorf("agent: no tool call %q is waiting for a result", toolCallID)
	}
	result.ToolCallID = pending.call.ID
	result.ToolName = pending.call.Name
	pending.waiter <- result
	close(pending.waiter)
	return nil
}

func (r *Run) emit(event Event) {
	if event.IsTerminal() {
		r.events <- event
		return
	}
	for len(r.events) >= eventBuffer-1 {
		select {
		case <-r.ctx.Done():
			return
		case <-time.After(roomPoll):
		}
	}
	r.events <- event
}

func (r *Run) settle(result Result) {
	r.mu.Lock()
	if r.settled {
		r.mu.Unlock()
		return
	}
	r.settled = true
	r.result = result
	r.mu.Unlock()
	final := result.FinalMessage
	r.emit(Event{Kind: AgentEnd, Termination: result.Termination, Assistant: &final, Result: result})
}

func (r *Run) fail(reason string) {
	r.mu.Lock()
	if r.settled {
		r.mu.Unlock()
		return
	}
	r.settled = true
	r.err = fmt.Errorf("agent: %s", reason)
	r.mu.Unlock()
	r.emit(Event{Kind: RunFailed, Reason: reason})
}

func (r *Run) withinLimit(turns int) bool {
	if r.config.MaxIterations <= 0 {
		return true
	}
	return turns < r.config.MaxIterations
}

const closedWithoutTerminal = "the provider stream closed without a terminal event"

func (r *Run) loop(prompts []provider.Message) {
	if r.config.Streamer == nil {
		r.fail("a run needs a streamer to reach a model")
		return
	}
	var history []provider.Message
	for _, prompt := range prompts {
		copied := prompt
		r.emit(Event{Kind: MessageStart, Message: &copied})
		history = append(history, copied)
		r.emit(Event{Kind: MessageEnd, Message: &copied})
	}
	if len(history) == 0 {
		r.fail("a run needs at least one message to answer")
		return
	}
	r.emit(Event{Kind: AgentStart})

	turns := 0
	cutOffToolTurns := 0
	ended := false
	termination := TerminationClean

	for r.withinLimit(turns) {
		if r.ctx.Err() != nil {
			termination = TerminationCanceled
			ended = true
			break
		}

		r.emit(Event{Kind: TurnStart})
		assistant, closedWithout := r.streamTurn(history)
		turns++
		history = append(history, provider.Message{Assistant: &assistant})

		if closedWithout != "" {
			r.emit(Event{Kind: TurnEnd, Assistant: &assistant})
			if r.ctx.Err() != nil {
				r.settle(Result{Messages: history, FinalMessage: assistant, Turns: turns, Termination: TerminationCanceled})
				return
			}
			r.fail(closedWithout)
			return
		}

		outcome := TurnOutcome(assistant, cutOffToolTurns)
		if outcome == OutcomeCalledTools && assistant.StopReason == provider.StopLength {
			cutOffToolTurns++
		} else {
			cutOffToolTurns = 0
		}

		switch outcome {
		case OutcomeFailed, OutcomeAnswered:
			r.emit(Event{Kind: TurnEnd, Assistant: &assistant})
			ended = true
		case OutcomeCalledTools:
			results, live := r.runToolCalls(assistant)
			r.emit(Event{Kind: TurnEnd, Assistant: &assistant})
			for index := range results {
				result := results[index]
				message := provider.Message{ToolResult: &result}
				r.emit(Event{Kind: MessageStart, Message: &message})
				history = append(history, message)
				r.emit(Event{Kind: MessageEnd, Message: &message})
			}
			if !live {
				r.settle(Result{Messages: history, FinalMessage: r.lastAssistant(history), Turns: turns, Termination: TerminationCanceled})
				return
			}
		}

		if ended {
			break
		}
	}

	if !ended {
		termination = TerminationMaxTurns
	}
	if r.ctx.Err() != nil {
		termination = TerminationCanceled
	}
	r.settle(Result{Messages: history, FinalMessage: r.lastAssistant(history), Turns: turns, Termination: termination})
}

func (r *Run) runToolCalls(assistant provider.AssistantContent) ([]provider.ToolResult, bool) {
	results := make([]provider.ToolResult, 0, len(assistant.Parts))
	live := true
	for _, part := range assistant.Parts {
		if part.ToolCall == nil {
			continue
		}
		call := *part.ToolCall
		if assistant.StopReason == provider.StopLength {
			results = append(results, cutOffResult(call))
			continue
		}
		if call.Name == "" {
			results = append(results, namelessResult(call))
			continue
		}
		if !live {
			results = append(results, cancelledResult(call))
			continue
		}
		result, answered := r.awaitToolResult(call)
		live = answered
		results = append(results, result)
	}
	return results, live
}

func (r *Run) cancelCall(call provider.ToolCall) provider.ToolResult {
	result := cancelledResult(call)
	r.emit(Event{Kind: ToolCallCancelled, Call: &call, ToolResult: &result})
	return result
}

func (r *Run) awaitToolResult(call provider.ToolCall) (provider.ToolResult, bool) {
	if r.ctx.Err() != nil {
		return cancelledResult(call), false
	}
	waiter := make(chan provider.ToolResult, 1)
	r.mu.Lock()
	r.pending[call.ID] = pendingCall{call: call, waiter: waiter}
	r.mu.Unlock()
	r.emit(Event{Kind: ToolCallRequested, Call: &call})
	select {
	case result := <-waiter:
		if r.ctx.Err() != nil {
			return r.cancelCall(call), false
		}
		r.emit(Event{Kind: ToolCallResolved, Call: &call, ToolResult: &result})
		return result, true
	case <-r.ctx.Done():
		r.mu.Lock()
		delete(r.pending, call.ID)
		r.mu.Unlock()
		return r.cancelCall(call), false
	}
}

func cutOffResult(call provider.ToolCall) provider.ToolResult {
	return errorResult(call, fmt.Sprintf("Tool call %q was not run: the reply hit the output token limit, so its arguments may be cut off. Call the tool again with complete arguments.", call.Name))
}

func namelessResult(call provider.ToolCall) provider.ToolResult {
	return errorResult(call, fmt.Sprintf("Tool call %s was not run: the reply named no tool, so there is nothing to run. Call a tool by name.", quotedID(call)))
}

func quotedID(call provider.ToolCall) string {
	if call.ID == "" {
		return "with no id"
	}
	return strconv.Quote(call.ID)
}

func cancelledResult(call provider.ToolCall) provider.ToolResult {
	return errorResult(call, fmt.Sprintf("Tool call %q was not run: the run was cancelled while it waited for a result.", call.Name))
}

func errorResult(call provider.ToolCall, reason string) provider.ToolResult {
	return provider.ToolResult{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Parts:      []provider.ContentPart{{Text: &provider.TextPart{Text: reason}}},
		IsError:    true,
	}
}

func (r *Run) lastAssistant(history []provider.Message) provider.AssistantContent {
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].Assistant != nil {
			return *history[index].Assistant
		}
	}
	return provider.AssistantContent{
		Parts:      []provider.ContentPart{{Text: &provider.TextPart{}}},
		StopReason: provider.StopStop,
		API:        r.config.Model.API,
		Provider:   r.config.Model.Provider,
		Model:      r.config.Model.ID,
	}
}

func (r *Run) errorContent(reason string) provider.AssistantContent {
	return provider.AssistantContent{
		Parts:      []provider.ContentPart{{Text: &provider.TextPart{Text: reason}}},
		StopReason: provider.StopError,
		API:        r.config.Model.API,
		Provider:   r.config.Model.Provider,
		Model:      r.config.Model.ID,
	}
}

func (r *Run) streamTurn(history []provider.Message) (provider.AssistantContent, string) {
	turn := r.config.Streamer.Stream(r.ctx, TurnRequest{
		Model:    r.config.Model,
		Messages: history,
		Options:  r.config.Options,
	})
	var assistant *provider.AssistantContent
	refusal := ""
	for event := range turn.Events {
		switch event.Kind {
		case provider.EventTextDelta:
			r.emit(Event{Kind: TextDelta, Delta: event.Delta})
		case provider.EventThinkingDelta:
			r.emit(Event{Kind: ReasoningDelta, Delta: event.Delta})
		case provider.EventDone:
			if event.Message != nil {
				assistant = contentOf(*event.Message)
			}
		case provider.EventError:
			refusal = event.Reason
		}
	}
	if assistant != nil {
		return *assistant, ""
	}
	if r.ctx.Err() != nil {
		return r.errorContent("the run was cancelled"), ""
	}
	if refusal != "" {
		return r.errorContent(refusal), ""
	}
	return r.errorContent(closedWithoutTerminal), closedWithoutTerminal
}

func contentOf(message provider.AssistantMessage) *provider.AssistantContent {
	parts := make([]provider.ContentPart, 0, len(message.Content))
	for _, block := range message.Content {
		parts = append(parts, provider.ContentPart{Text: block.Text, Thinking: block.Thinking, ToolCall: block.ToolCall})
	}
	return &provider.AssistantContent{
		Parts:      parts,
		StopReason: message.StopReason,
		API:        message.API,
		Provider:   message.Provider,
		Model:      message.Model,
	}
}

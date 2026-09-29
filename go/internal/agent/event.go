package agent

import "github.com/lsm/open-agent-protocol/go/internal/provider"

type EventKind string

const (
	AgentStart        EventKind = "agent_start"
	TurnStart         EventKind = "turn_start"
	TextDelta         EventKind = "text_delta"
	ReasoningDelta    EventKind = "reasoning_delta"
	MessageStart      EventKind = "message_start"
	MessageEnd        EventKind = "message_end"
	ToolCallRequested EventKind = "tool_call_requested"
	ToolCallResolved  EventKind = "tool_call_resolved"
	TurnEnd           EventKind = "turn_end"
	AgentEnd          EventKind = "agent_end"
	RunFailed         EventKind = "run_failed"
)

type Termination string

const (
	TerminationClean    Termination = ""
	TerminationMaxTurns Termination = "max_turns"
	TerminationCanceled Termination = "cancelled"
)

type Event struct {
	Kind        EventKind
	Delta       string
	Reason      string
	Termination Termination
	Message     *provider.Message
	Assistant   *provider.AssistantContent
	Call        *provider.ToolCall
	ToolResult  *provider.ToolResult
	Result      Result
}

func (e Event) IsTerminal() bool {
	return e.Kind == AgentEnd || e.Kind == RunFailed
}

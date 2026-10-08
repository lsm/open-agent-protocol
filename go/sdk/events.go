package sdk

type ProviderEvent interface {
	isProviderEvent()
}

type AgentEvent interface {
	isAgentEvent()
}

type MessageStart struct {
	ProviderID string
	API        string
	ModelID    string
}

type TextDelta struct {
	Delta string
}

type ThinkingDelta struct {
	Delta string
}

type ToolCallEvent struct {
	ToolCallID    string
	Name          string
	ArgumentsJSON string
}

type MessageEnd struct {
	Usage        *Usage
	StopReason   string
	ErrorMessage string
}

type ErrorEvent struct {
	Message    string
	Code       string
	ProviderID string
}

type AgentStart struct {
	SessionID string
}

type AgentEnd struct {
	StopReason string

	Usage *Usage

	ErrorMessage string
	ProviderID   string
	API          string
}

type TurnStart struct{}

type TurnEnd struct {
	StopReason   string
	ErrorMessage string
}

type ToolExecutionStart struct {
	ToolCallID string
	ToolName   string
}

type ToolExecutionEnd struct {
	ToolCallID string
	IsError    bool
}

func (*MessageStart) isProviderEvent()  {}
func (*TextDelta) isProviderEvent()     {}
func (*ThinkingDelta) isProviderEvent() {}
func (*ToolCallEvent) isProviderEvent() {}
func (*MessageEnd) isProviderEvent()    {}
func (*ErrorEvent) isProviderEvent()    {}

func (*MessageStart) isAgentEvent()       {}
func (*TextDelta) isAgentEvent()          {}
func (*ThinkingDelta) isAgentEvent()      {}
func (*ToolCallEvent) isAgentEvent()      {}
func (*MessageEnd) isAgentEvent()         {}
func (*ErrorEvent) isAgentEvent()         {}
func (*AgentStart) isAgentEvent()         {}
func (*AgentEnd) isAgentEvent()           {}
func (*TurnStart) isAgentEvent()          {}
func (*TurnEnd) isAgentEvent()            {}
func (*ToolExecutionStart) isAgentEvent() {}
func (*ToolExecutionEnd) isAgentEvent()   {}

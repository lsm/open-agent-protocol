package agent

import "github.com/lsm/open-agent-protocol/go/internal/provider"

const MaxCutOffToolTurns = 3

type Outcome int

const (
	OutcomeFailed Outcome = iota
	OutcomeAnswered
	OutcomeCalledTools
)

func TurnOutcome(assistant provider.AssistantContent, cutOffToolTurns int) Outcome {
	switch assistant.StopReason {
	case provider.StopError, provider.StopAborted:
		return OutcomeFailed
	case provider.StopContentFilter:
		return OutcomeAnswered
	case provider.StopLength:
		if cutOffToolTurns >= MaxCutOffToolTurns {
			return OutcomeAnswered
		}
	}
	for _, part := range assistant.Parts {
		if part.ToolCall != nil {
			return OutcomeCalledTools
		}
	}
	return OutcomeAnswered
}

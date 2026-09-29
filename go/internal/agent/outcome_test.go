package agent

import (
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/provider"
)

func reply(stopReason provider.StopReason, parts ...provider.ContentPart) provider.AssistantContent {
	return provider.AssistantContent{Parts: parts, StopReason: stopReason}
}

func text(body string) provider.ContentPart {
	return provider.ContentPart{Text: &provider.TextPart{Text: body}}
}

func call(id, name string) provider.ContentPart {
	return provider.ContentPart{ToolCall: &provider.ToolCall{ID: id, Name: name, Arguments: "{}"}}
}

func TestAReplyRunsItsToolCallsWhateverStopReasonItReports(t *testing.T) {
	for _, stopReason := range []provider.StopReason{provider.StopToolUse, provider.StopStop, provider.StopLength} {
		if got := TurnOutcome(reply(stopReason, text("reading"), call("call_1", "read")), 0); got != OutcomeCalledTools {
			t.Errorf("a %q reply carrying a tool call: got %v, want its tools to run", stopReason, got)
		}
	}
}

func TestAReplyWithoutAToolCallEndsTheRunEvenOneReportingToolUse(t *testing.T) {
	for _, stopReason := range []provider.StopReason{provider.StopToolUse, provider.StopStop, provider.StopLength} {
		if got := TurnOutcome(reply(stopReason, text("done")), 0); got != OutcomeAnswered {
			t.Errorf("a %q reply with no tool call: got %v, want the run to end", stopReason, got)
		}
	}
	if got := TurnOutcome(reply(provider.StopLength), 0); got != OutcomeAnswered {
		t.Errorf("a length reply with no content at all: got %v, want the run to end", got)
	}
}

func TestTheToolCallsOfAFailedOrAbortedReplyNeverRun(t *testing.T) {
	if got := TurnOutcome(reply(provider.StopError, call("call_1", "read")), 0); got != OutcomeFailed {
		t.Errorf("a failed reply carrying a tool call: got %v, want the turn to fail rather than run it", got)
	}
	if got := TurnOutcome(reply(provider.StopAborted, call("call_1", "read")), 0); got != OutcomeFailed {
		t.Errorf("an aborted reply carrying a tool call: got %v, want the turn to fail rather than run it", got)
	}
}

func TestAFilteredReplyEndsTheRunWithoutRunningItsToolCalls(t *testing.T) {
	if got := TurnOutcome(reply(provider.StopContentFilter, call("call_1", "read")), 0); got != OutcomeAnswered {
		t.Errorf("a filtered reply carrying a tool call: got %v, want the run to end without running it: a wire that keeps content_filter distinct reaches this path", got)
	}
}

func TestACutOffToolCallIsRetriedThreeTimesAndThenTheRunEnds(t *testing.T) {
	if MaxCutOffToolTurns != 3 {
		t.Errorf("a cut-off call is retried %d times, want 3: a model that keeps truncating its arguments is answered rather than retried forever", MaxCutOffToolTurns)
	}
	cut := reply(provider.StopLength, call("call_1", "write"))
	if got := TurnOutcome(cut, 0); got != OutcomeCalledTools {
		t.Errorf("the first cut-off call: got %v, want it answered by the caller", got)
	}
	if got := TurnOutcome(cut, MaxCutOffToolTurns-1); got != OutcomeCalledTools {
		t.Errorf("the last retried cut-off call: got %v, want it answered by the caller", got)
	}
	if got := TurnOutcome(cut, MaxCutOffToolTurns); got != OutcomeAnswered {
		t.Errorf("a cut-off call after %d in a row: got %v, want the run to end", MaxCutOffToolTurns, got)
	}
}

func TestACompleteToolCallRunsAfterThreeCutOffOnesInARow(t *testing.T) {
	if got := TurnOutcome(reply(provider.StopToolUse, call("call_1", "write")), MaxCutOffToolTurns); got != OutcomeCalledTools {
		t.Errorf("a complete tool call after %d cut-off ones: got %v, want it to run: the cut-off count is consecutive, not cumulative", MaxCutOffToolTurns, got)
	}
	if got := TurnOutcome(reply(provider.StopToolUse, call("call_1", "write")), 0); got != OutcomeCalledTools {
		t.Errorf("a complete tool call with no cut-off ones before it: got %v, want it to run", got)
	}
}

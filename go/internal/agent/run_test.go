package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/internal/provider"
)

type scriptedTurn struct {
	frames   []string
	fail     string
	silent   bool
	dropDone bool
	hold     chan struct{}
	once     bool
}

type scripted struct {
	mu    sync.Mutex
	turns []scriptedTurn
	turn  int
	seen  [][]provider.Message
}

func (s *scripted) Stream(ctx context.Context, request TurnRequest) Turn {
	s.mu.Lock()
	s.seen = append(s.seen, append([]provider.Message(nil), request.Messages...))
	if s.turn >= len(s.turns) {
		s.mu.Unlock()
		return Turn{Events: frames(ctx, scriptedTurn{silent: true})}
	}
	held := s.turns[s.turn]
	s.turn++
	s.mu.Unlock()
	return Turn{Events: frames(ctx, held)}
}

func frames(ctx context.Context, turn scriptedTurn) <-chan provider.Event {
	out := make(chan provider.Event, 64)
	go func() {
		defer close(out)
		if turn.silent {
			return
		}
		sink := &provider.EventSink{OnEvent: func(event provider.Event) { out <- event }}
		if turn.hold != nil {
			read := func() ([]byte, error) {
				if !turn.once {
					turn.once = true
					return []byte(frame(`{"choices":[{"delta":{"content":"half"}}]}`)), nil
				}
				select {
				case <-turn.hold:
				case <-ctx.Done():
				}
				return []byte(frame(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)), nil
			}
			provider.Stream(&provider.EventSink{OnEvent: func(event provider.Event) {
				select {
				case out <- event:
				case <-ctx.Done():
				}
			}}, completionsModel(), provider.Context{}, provider.StreamOptions{}, read, func() bool { return ctx.Err() != nil })
			return
		}
		if turn.dropDone {
			for _, held := range turn.frames {
				out <- provider.Event{Kind: provider.EventTextDelta, Delta: held}
			}
			return
		}
		read := chunkReader(turn.frames)
		if turn.fail != "" {
			read = func() ([]byte, error) { return nil, fmt.Errorf("%s", turn.fail) }
		}
		provider.Stream(sink, completionsModel(), provider.Context{}, provider.StreamOptions{}, read, nil)
	}()
	return out
}

func drain(t *testing.T, run *Run) []Event {
	t.Helper()
	return drainActing(t, run, nil)
}

func drainActing(t *testing.T, run *Run, act func(Event)) []Event {
	t.Helper()
	var out []Event
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event, open := <-run.Events():
			if !open {
				return out
			}
			out = append(out, event)
			if act != nil {
				act(event)
			}
		case <-deadline:
			t.Fatalf("the run did not end within five seconds: %s", joinKinds(kindsOf(out)))
		}
	}
}

func kindsOf(events []Event) []EventKind {
	out := make([]EventKind, 0, len(events))
	for _, event := range events {
		out = append(out, event.Kind)
	}
	return out
}

func joinKinds(kinds []EventKind) string {
	parts := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		parts = append(parts, string(kind))
	}
	return strings.Join(parts, " ")
}

func prompts(body string) []provider.Message {
	return []provider.Message{{User: &provider.UserContent{Text: body, HasText: true}}}
}

func terminalOf(t *testing.T, events []Event) Event {
	t.Helper()
	var found []Event
	for _, event := range events {
		if event.IsTerminal() {
			found = append(found, event)
		}
	}
	if len(found) != 1 {
		t.Fatalf("a run ends with exactly one terminal, got %d: %s", len(found), joinKinds(kindsOf(events)))
	}
	return found[0]
}

func textTurn(body string) scriptedTurn {
	delta := fmt.Sprintf(`{"choices":[{"delta":{"content":%q}}]}`, body)
	done := `{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`
	return scriptedTurn{frames: []string{frame(delta), frame(done)}}
}

func toolTurn(id, name, arguments string) scriptedTurn {
	delta := fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":%q,"function":{"name":%q,"arguments":%q}}]}}]}`, id, name, arguments)
	done := `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`
	return scriptedTurn{frames: []string{frame(delta), frame(done)}}
}

func TestATextRunStreamsItsDeltasAndEndsOnce(t *testing.T) {
	script := &scripted{turns: []scriptedTurn{textTurn("hello there")}}
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: script}, prompts("hi"))
	events := drain(t, run)
	want := "message_start message_end agent_start turn_start text_delta turn_end agent_end"
	if got := joinKinds(kindsOf(events)); got != want {
		t.Errorf("a text run emits %s, want %s", got, want)
	}
	terminal := terminalOf(t, events)
	if terminal.Kind != AgentEnd {
		t.Errorf("a run that answered ends with %q, want agent_end", terminal.Kind)
	}
	if terminal.Termination != TerminationClean {
		t.Errorf("a run that answered terminates %q, want a clean finish", terminal.Termination)
	}
	result := run.Result()
	if result.FinalMessage.StopReason != provider.StopStop {
		t.Errorf("the final message's stop reason is %q, want stop", result.FinalMessage.StopReason)
	}
	if len(result.Messages) != 2 {
		t.Errorf("a text run kept %d messages, want the prompt and the reply", len(result.Messages))
	}
	if result.Turns != 1 {
		t.Errorf("a text run took %d turns, want 1", result.Turns)
	}
}

func TestTheTerminalCarriesTheResultTheRunSettledWith(t *testing.T) {
	script := &scripted{turns: []scriptedTurn{textTurn("done")}}
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: script}, prompts("hi"))
	terminal := terminalOf(t, drain(t, run))
	if terminal.Result.Termination != terminal.Termination {
		t.Errorf("the terminal reports termination %q and carries %q, want one answer", terminal.Termination, terminal.Result.Termination)
	}
	if terminal.Result.Turns != 1 || terminal.Assistant == nil {
		t.Errorf("the terminal carries %+v, want the run's result and the message it settled with", terminal.Result)
	}
}

func TestAProviderThatRefusesEndsTheRunRatherThanFailingIt(t *testing.T) {
	script := &scripted{turns: []scriptedTurn{{fail: "the provider refused the request"}}}
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: script}, prompts("hi"))
	events := drain(t, run)
	terminal := terminalOf(t, events)
	if terminal.Kind != AgentEnd {
		t.Fatalf("a provider that refuses ends with %q, want agent_end: a run that got far enough to end is not run_failed", terminal.Kind)
	}
	if terminal.Assistant == nil || terminal.Assistant.StopReason != provider.StopError {
		t.Errorf("the terminal carries stop reason %+v, want error", terminal.Assistant)
	}
	if terminal.Assistant.Parts[0].Text.Text == "" {
		t.Error("the terminal's text is empty, want the reason the stream gave for failing")
	}
	if terminal.Termination != TerminationClean {
		t.Errorf("a provider refusal terminates %q, and termination encodes only max_turns or cancelled", terminal.Termination)
	}
	if run.Err() != nil {
		t.Errorf("a provider refusal is not a run failure, got %v", run.Err())
	}
}

func TestAStreamThatClosesWithoutATerminalIsAFailedTurn(t *testing.T) {
	script := &scripted{turns: []scriptedTurn{{dropDone: true}}}
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: script}, prompts("hi"))
	events := drain(t, run)
	terminal := terminalOf(t, events)
	if terminal.Kind != RunFailed {
		t.Fatalf("a stream that dropped its terminal ends with %q, want run_failed: a provider bug must not settle as a finished run", terminal.Kind)
	}
	if run.Err() == nil || !strings.Contains(run.Err().Error(), "closed without a terminal") {
		t.Errorf("run_failed says %v, want the reason the stream closed without one", run.Err())
	}
}

func TestAStreamThatClosesAfterADeltaWithoutATerminalIsAFailedTurn(t *testing.T) {
	script := &scripted{turns: []scriptedTurn{{dropDone: true, frames: []string{"half a th"}}}}
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: script}, prompts("hi"))
	events := drain(t, run)
	var deltas int
	for _, event := range events {
		if event.Kind == TextDelta {
			deltas++
		}
	}
	if deltas == 0 {
		t.Fatal("the turn delivered no delta, so this test never exercised a close after one")
	}
	terminal := terminalOf(t, events)
	if terminal.Kind != RunFailed {
		t.Fatalf("a stream that delivered text and then closed with no terminal ends with %q, want run_failed: a provider bug must not settle as a finished run", terminal.Kind)
	}
}

func TestARunCancelledMidTurnSettlesAsCancelledNotFailed(t *testing.T) {
	hold := make(chan struct{})
	script := &scripted{turns: []scriptedTurn{{hold: hold}}}
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: script}, prompts("hi"))
	events := drainActing(t, run, func(event Event) {
		if event.Kind == TextDelta {
			run.Cancel()
		}
	})
	terminal := terminalOf(t, events)
	if terminal.Kind != AgentEnd {
		t.Fatalf("a run the loop itself cancelled ends with %q, want agent_end: the loop caused this close, so it is a cancellation and not a provider that dropped its terminal", terminal.Kind)
	}
	if terminal.Termination != TerminationCanceled {
		t.Errorf("a cancelled run terminates %q, want cancelled", terminal.Termination)
	}
	if run.Err() != nil {
		t.Errorf("a run the loop cancelled is not a failure, got %v", run.Err())
	}
	if run.Result().Turns != 1 {
		t.Errorf("a cancelled run counted %d turns, want the one it was in", run.Result().Turns)
	}
}

func TestARunCancelledBeforeItsFirstTurnStillEndsOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	script := &scripted{turns: []scriptedTurn{textTurn("hi")}}
	run := Start(ctx, Config{Model: completionsModel(), Streamer: script}, prompts("hi"))
	events := drain(t, run)
	terminal := terminalOf(t, events)
	if terminal.Kind != AgentEnd {
		t.Errorf("a run cancelled before its first turn ends with %q, want agent_end", terminal.Kind)
	}
	if terminal.Termination != TerminationCanceled {
		t.Errorf("a run cancelled before its first turn terminates %q, want cancelled", terminal.Termination)
	}
	if script.turn != 0 {
		t.Errorf("a run cancelled before its first turn asked the model %d times, want 0", script.turn)
	}
}

func TestATurnLimitIsWhatStopsARunFromAskingTheModelForever(t *testing.T) {
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: &scripted{}, MaxIterations: 2}, prompts("go"))
	for _, turns := range []int{0, 1} {
		if !run.withinLimit(turns) {
			t.Errorf("a limit of two turns stopped the run at %d turns, want it to keep going", turns)
		}
	}
	for _, turns := range []int{2, 3, 100} {
		if run.withinLimit(turns) {
			t.Errorf("a limit of two turns let the run reach %d turns, want it stopped at 2", turns)
		}
	}
	run.Wait()
}

func TestATurnLimitIsReachedOnlyByARunThatKeepsGoing(t *testing.T) {
	script := &scripted{turns: []scriptedTurn{textTurn("done")}}
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: script, MaxIterations: 5}, prompts("go"))
	result := run.Wait()
	if result.Termination != TerminationClean {
		t.Errorf("a run that answered terminates %q, want a clean finish rather than the turn limit", result.Termination)
	}
}

func TestARunWithNoTurnLimitIsNeverStoppedByTheLimit(t *testing.T) {
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: &scripted{}, MaxIterations: 0}, prompts("go"))
	for _, turns := range []int{0, 1, 2, 1000000} {
		if !run.withinLimit(turns) {
			t.Errorf("a run with no limit stopped at %d turns, want nothing but the model to stop it", turns)
		}
	}
	run.Wait()
}

func TestACallTheCallerAnswersBecomesAMessageAndTheRunKeepsGoing(t *testing.T) {
	script := &scripted{turns: []scriptedTurn{toolTurn("call_1", "read", `{"path":"a"}`), textTurn("I read it.")}}
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: script}, prompts("read a"))
	events := drainActing(t, run, func(event Event) {
		if event.Kind != ToolCallRequested {
			return
		}
		if err := run.ResolveTool(event.Call.ID, provider.ToolResult{
			ToolName: event.Call.Name,
			Parts:    []provider.ContentPart{{Text: &provider.TextPart{Text: "a"}}},
		}); err != nil {
			t.Errorf("resolving %q: %v", event.Call.ID, err)
		}
	})
	terminal := terminalOf(t, events)
	if terminal.Kind != AgentEnd {
		t.Fatalf("a run that got its tool answered ends with %q, want agent_end", terminal.Kind)
	}
	result := run.Result()
	if result.Turns != 2 {
		t.Errorf("a call and then an answer is %d turns, want 2", result.Turns)
	}
	if result.Termination != TerminationClean {
		t.Errorf("a run that got its tool answered terminates %q, want a clean finish", result.Termination)
	}
	if len(script.seen) != 2 {
		t.Fatalf("the model was asked %d times, want 2", len(script.seen))
	}
	second := script.seen[1]
	if len(second) != 3 {
		t.Fatalf("the second turn carried %d messages, want the prompt, the reply and the result", len(second))
	}
	if second[2].ToolResult == nil || second[2].ToolResult.Parts[0].Text.Text != "a" {
		t.Errorf("the second turn carried %+v, want the caller's own result as a message", second[2])
	}
}

func TestTheAnswerIsAnnouncedOnTheCallItAnswers(t *testing.T) {
	script := &scripted{turns: []scriptedTurn{toolTurn("call_1", "read", "{}"), textTurn("done")}}
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: script}, prompts("read a"))
	events := drainActing(t, run, func(event Event) {
		if event.Kind == ToolCallRequested {
			_ = run.ResolveTool(event.Call.ID, provider.ToolResult{Parts: []provider.ContentPart{{Text: &provider.TextPart{Text: "a"}}}})
		}
	})
	var asked, resolved []string
	for _, event := range events {
		switch event.Kind {
		case ToolCallRequested:
			asked = append(asked, event.Call.ID)
		case ToolCallResolved:
			resolved = append(resolved, event.ToolResult.ToolCallID)
		}
	}
	if strings.Join(asked, ",") != "call_1" {
		t.Errorf("the loop asked for %v, want the call the model wrote", asked)
	}
	if strings.Join(resolved, ",") != "call_1" {
		t.Errorf("the loop announced %v as resolved, want the call it asked about", resolved)
	}
}

func TestTheAnswerTakesTheCallIdTheLoopAskedWith(t *testing.T) {
	script := &scripted{turns: []scriptedTurn{toolTurn("call_1", "read", "{}"), textTurn("done")}}
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: script}, prompts("read a"))
	drainActing(t, run, func(event Event) {
		if event.Kind != ToolCallRequested {
			return
		}
		_ = run.ResolveTool(event.Call.ID, provider.ToolResult{ToolCallID: "something_else", Parts: []provider.ContentPart{{Text: &provider.TextPart{Text: "a"}}}})
	})
	for _, message := range run.Result().Messages {
		if message.ToolResult == nil {
			continue
		}
		if message.ToolResult.ToolCallID != "call_1" {
			t.Errorf("the result answers %q, want call_1: a caller's own id would not correlate with the call the model made", message.ToolResult.ToolCallID)
		}
	}
}

func TestEveryCallInAReplyIsAskedForAndAnswered(t *testing.T) {
	first := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read","arguments":"{\"path\":\"a\"}"}}]}}]}`
	second := `{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","function":{"name":"read","arguments":"{\"path\":\"b\"}"}}]}}]}`
	turn := scriptedTurn{frames: []string{frame(first), frame(second), frame(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)}}
	script := &scripted{turns: []scriptedTurn{turn, textTurn("read both")}}
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: script}, prompts("read a and b"))
	events := drainActing(t, run, func(event Event) {
		if event.Kind == ToolCallRequested {
			_ = run.ResolveTool(event.Call.ID, provider.ToolResult{Parts: []provider.ContentPart{{Text: &provider.TextPart{Text: "ok"}}}})
		}
	})
	var asked []string
	for _, event := range events {
		if event.Kind == ToolCallRequested {
			asked = append(asked, event.Call.ID)
		}
	}
	if strings.Join(asked, ",") != "call_1,call_2" {
		t.Errorf("the loop asked for %v, want both calls in the order the model wrote them", asked)
	}
	if run.Result().Turns != 2 {
		t.Errorf("two calls in one reply is %d turns, want 2", run.Result().Turns)
	}
}

func TestResolvingACallNoOneIsWaitingOnIsRefusedByName(t *testing.T) {
	script := &scripted{turns: []scriptedTurn{textTurn("hello")}}
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: script}, prompts("hi"))
	drain(t, run)
	err := run.ResolveTool("call_nonexistent", provider.ToolResult{Parts: []provider.ContentPart{{Text: &provider.TextPart{}}}})
	if err == nil {
		t.Fatal("resolving a call no one is waiting on must be refused rather than silently dropped")
	}
	if !strings.Contains(err.Error(), "call_nonexistent") {
		t.Errorf("the refusal says %q, want it to name the call it could not match", err)
	}
}

func TestACutOffCallIsAnsweredWithoutAskingTheCaller(t *testing.T) {
	cut := scriptedTurn{frames: []string{
		frame(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"write","arguments":"{\"text\":\"cut"}}]}}]}`),
		frame(`{"choices":[{"delta":{},"finish_reason":"length"}]}`),
	}}
	script := &scripted{turns: []scriptedTurn{cut, textTurn("done")}}
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: script}, prompts("write"))
	events := drain(t, run)
	for _, event := range events {
		if event.Kind == ToolCallRequested {
			t.Fatalf("a cut-off call asked the caller to run arguments that may be truncated: %s", joinKinds(kindsOf(events)))
		}
	}
	var found bool
	for _, message := range run.Result().Messages {
		if message.ToolResult == nil {
			continue
		}
		found = true
		if !message.ToolResult.IsError {
			t.Error("a cut-off call is an error result, so the model knows it did not run")
		}
		if message.ToolResult.ToolCallID != "call_1" {
			t.Errorf("the cut-off call's result answers %q, want call_1", message.ToolResult.ToolCallID)
		}
	}
	if !found {
		t.Errorf("a cut-off call left no result in the run's history: %+v", run.Result().Messages)
	}
}

func TestARunCancelledWhileACallIsWaitingSettlesCancelledNotFailed(t *testing.T) {
	script := &scripted{turns: []scriptedTurn{toolTurn("call_1", "read", "{}")}}
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: script}, prompts("read a"))
	events := drainActing(t, run, func(event Event) {
		if event.Kind == ToolCallRequested {
			run.Cancel()
		}
	})
	terminal := terminalOf(t, events)
	if terminal.Kind != AgentEnd {
		t.Fatalf("a run cancelled while a call was waiting ends with %q, want agent_end", terminal.Kind)
	}
	if terminal.Termination != TerminationCanceled {
		t.Errorf("a cancelled run terminates %q, want cancelled", terminal.Termination)
	}
	if run.Err() != nil {
		t.Errorf("a run the loop cancelled is not a failure, got %v", run.Err())
	}
	for _, message := range run.Result().Messages {
		if message.ToolResult == nil || !message.ToolResult.IsError {
			continue
		}
		if !strings.Contains(message.ToolResult.Parts[0].Text.Text, "cancelled") {
			t.Errorf("the unanswered call's result says %q, want it to say the run was cancelled", message.ToolResult.Parts[0].Text.Text)
		}
	}
}

func TestAnAnswerArrivingAfterACancelIsRefusedRatherThanBlocking(t *testing.T) {
	script := &scripted{turns: []scriptedTurn{toolTurn("call_1", "read", "{}")}}
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: script}, prompts("read a"))
	events := drainActing(t, run, func(event Event) {
		if event.Kind == ToolCallRequested {
			run.Cancel()
		}
	})
	terminalOf(t, events)
	settled := make(chan error, 1)
	go func() { settled <- run.ResolveTool("call_1", provider.ToolResult{}) }()
	select {
	case err := <-settled:
		if err == nil {
			t.Error("an answer for a call whose run was cancelled must be refused: nothing is waiting for it and the run has ended")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resolving a call after its run was cancelled blocked: the waiter is already gone")
	}
}

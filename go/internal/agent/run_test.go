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

func TestARunWithNoStreamerFailsRatherThanEndingQuietly(t *testing.T) {
	run := Start(context.Background(), Config{Model: completionsModel()}, prompts("hi"))
	events := drain(t, run)
	if got := terminalOf(t, events).Kind; got != RunFailed {
		t.Errorf("a run with no streamer ends with %q, want run_failed", got)
	}
	if run.Err() == nil {
		t.Error("run_failed without an error is a failure a consumer cannot read")
	}
}

func TestARunWithNoMessageFailsRatherThanAskingTheModel(t *testing.T) {
	script := &scripted{turns: []scriptedTurn{textTurn("hi")}}
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: script}, nil)
	events := drain(t, run)
	if got := terminalOf(t, events).Kind; got != RunFailed {
		t.Errorf("a run with nothing to run ends with %q, want run_failed", got)
	}
	if script.turn != 0 {
		t.Errorf("a run with nothing to run asked the model %d times, want 0", script.turn)
	}
}

func manyDeltaFrames() []string {
	frames := make([]string, 0, 201)
	for i := 0; i < 200; i++ {
		frames = append(frames, frame(`{"choices":[{"delta":{"content":"x"}}]}`))
	}
	return append(frames, frame(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`))
}

func TestTheTerminalLandsOnAFullBufferAfterACancelOnEveryAttempt(t *testing.T) {
	for attempt := 0; attempt < 30; attempt++ {
		run := Start(context.Background(), Config{
			Model:    completionsModel(),
			Streamer: &scripted{turns: []scriptedTurn{{frames: manyDeltaFrames()}}},
		}, prompts("hi"))
		settled := make(chan struct{})
		go func() { run.Wait(); close(settled) }()
		time.Sleep(150 * time.Millisecond)
		run.Cancel()
		select {
		case <-settled:
		case <-time.After(5 * time.Second):
			t.Fatalf("attempt %d: Wait did not return after a cancel with a full buffer; buffered=%d", attempt, len(run.events))
		}
		var terminals int
		var kinds []EventKind
		for event := range run.Events() {
			if event.IsTerminal() {
				terminals++
			}
			kinds = append(kinds, event.Kind)
		}
		if terminals != 1 {
			t.Fatalf("attempt %d: a cancelled run delivered %d terminals (%s), want exactly one: the last buffer slot is the terminal's, so a full buffer cannot cost the run its only terminal", attempt, terminals, joinKinds(kinds))
		}
	}
}

func TestANonTerminalNeverOccupiesTheSlotsReservedForTheTerminal(t *testing.T) {
	run := Start(context.Background(), Config{
		Model:    completionsModel(),
		Streamer: &scripted{turns: []scriptedTurn{{frames: manyDeltaFrames()}}},
	}, prompts("hi"))
	for i := 0; i < 10; i++ {
		time.Sleep(20 * time.Millisecond)
		if held := len(run.events); held > eventBuffer-1 {
			t.Fatalf("a running run held %d events, want at most %d: the last slot is the terminal's", held, eventBuffer-1)
		}
	}
	run.Cancel()
}

func TestATerminalReachesAConsumerThatIsStillReading(t *testing.T) {
	script := &scripted{turns: []scriptedTurn{{frames: manyDeltaFrames()}}}
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: script}, prompts("hi"))
	terminal := terminalOf(t, drain(t, run))
	if terminal.Kind != AgentEnd {
		t.Errorf("a run whose event buffer filled ends with %q, want agent_end: the consumer was still reading", terminal.Kind)
	}
}

func TestAFinishedRunReleasesItsOwnContext(t *testing.T) {
	for i := 0; i < 5; i++ {
		script := &scripted{turns: []scriptedTurn{textTurn("done")}}
		run := Start(context.Background(), Config{Model: completionsModel(), Streamer: script}, prompts("hi"))
		run.Wait()
		for i := 0; i < 50 && run.ctx.Err() == nil; i++ {
			time.Sleep(2 * time.Millisecond)
		}
		if run.ctx.Err() == nil {
			t.Fatalf("a run that finished left its context live, so a parent starting many runs keeps one context per run")
		}
	}
}

func TestReleasingTheContextAtTheEndLeavesTheTerminalDelivered(t *testing.T) {
	script := &scripted{turns: []scriptedTurn{{frames: manyDeltaFrames()}}}
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: script}, prompts("hi"))
	events := drain(t, run)
	terminal := terminalOf(t, events)
	if terminal.Kind != AgentEnd {
		t.Errorf("a run that released its own context ends with %q, want agent_end", terminal.Kind)
	}
	if terminal.Termination != TerminationClean {
		t.Errorf("a run that released its own context terminates %q, want a clean finish: releasing the context is not a cancellation", terminal.Termination)
	}
}

func TestTheTerminalKindsAreTheOnesThatEndARun(t *testing.T) {
	for _, kind := range []EventKind{AgentEnd, RunFailed} {
		if !(Event{Kind: kind}).IsTerminal() {
			t.Errorf("%q ends a run, so IsTerminal must say so", kind)
		}
	}
	for _, kind := range []EventKind{AgentStart, TurnStart, TurnEnd, TextDelta, ReasoningDelta, MessageStart, MessageEnd, ToolCallRequested, ToolCallResolved} {
		if (Event{Kind: kind}).IsTerminal() {
			t.Errorf("%q does not end a run, so IsTerminal must not claim it does", kind)
		}
	}
}

func TestNoCallAfterTheFirstIsAskedOnceTheRunIsCancelled(t *testing.T) {
	first := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read","arguments":"{}"}}]}}]}`
	second := `{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","function":{"name":"write","arguments":"{}"}}]}}]}`
	third := `{"choices":[{"delta":{"tool_calls":[{"index":2,"id":"call_3","function":{"name":"write","arguments":"{}"}}]}}]}`
	turn := scriptedTurn{frames: []string{frame(first), frame(second), frame(third), frame(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)}}
	script := &scripted{turns: []scriptedTurn{turn, textTurn("done")}}
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: script}, prompts("go"))
	var asked []string
	events := drainActing(t, run, func(event Event) {
		if event.Kind != ToolCallRequested {
			return
		}
		asked = append(asked, event.Call.ID)
		run.Cancel()
	})
	terminal := terminalOf(t, events)
	if terminal.Kind != AgentEnd || terminal.Termination != TerminationCanceled {
		t.Errorf("a run cancelled at its first call ends with %q/%q, want agent_end/cancelled", terminal.Kind, terminal.Termination)
	}
	if len(asked) != 1 {
		t.Errorf("the loop asked for %v after the run was cancelled, want only the call it was already waiting on: a client-executed tool is side-effecting, and the later answers are refused", asked)
	}
	answered := map[string]bool{}
	for _, message := range run.Result().Messages {
		if message.ToolResult == nil {
			continue
		}
		answered[message.ToolResult.ToolCallID] = true
		if !message.ToolResult.IsError {
			t.Errorf("the call %q left a successful result, want an error: nothing ran it", message.ToolResult.ToolCallID)
		}
	}
	for _, id := range []string{"call_1", "call_2", "call_3"} {
		if !answered[id] {
			t.Errorf("the call %q left no result at all, so the history has a tool call with nothing answering it", id)
		}
	}
}

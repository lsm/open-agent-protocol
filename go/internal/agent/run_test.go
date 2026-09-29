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

func TestAReplyThatCallsAToolFailsTheRunRatherThanIgnoringIt(t *testing.T) {
	script := &scripted{turns: []scriptedTurn{toolTurn("call_1", "read", "{}")}}
	run := Start(context.Background(), Config{Model: completionsModel(), Streamer: script}, prompts("read a"))
	terminal := terminalOf(t, drain(t, run))
	if terminal.Kind != RunFailed {
		t.Fatalf("a reply carrying a tool call ends with %q, want run_failed: this loop cannot run tools, and answering as if it had would be a lie", terminal.Kind)
	}
	if run.Err() == nil || !strings.Contains(run.Err().Error(), "nowhere to send them") {
		t.Errorf("the failure says %v, want it to name the tool call it could not act on", run.Err())
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

func TestTheTerminalKindsAreTheOnesThatEndARun(t *testing.T) {
	for _, kind := range []EventKind{AgentEnd, RunFailed} {
		if !(Event{Kind: kind}).IsTerminal() {
			t.Errorf("%q ends a run, so IsTerminal must say so", kind)
		}
	}
	for _, kind := range []EventKind{AgentStart, TurnStart, TurnEnd, TextDelta, ReasoningDelta, MessageStart, MessageEnd} {
		if (Event{Kind: kind}).IsTerminal() {
			t.Errorf("%q does not end a run, so IsTerminal must not claim it does", kind)
		}
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

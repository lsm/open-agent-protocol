package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/internal/provider"
)

func chunkReader(chunks []string) provider.ReadChunkFunc {
	index := 0
	return func() ([]byte, error) {
		if index >= len(chunks) {
			return nil, nil
		}
		out := []byte(chunks[index])
		index++
		return out, nil
	}
}

func frame(payload string) string {
	return "data: " + payload + "\n\n"
}

func anthropicModel() provider.Model {
	return provider.Model{ID: "claude-sonnet-4-5", API: "anthropic-messages", Provider: "anthropic", MaxTokens: 4096, Reasoning: true, HasCompat: true}
}

func completionsModel() provider.Model {
	return provider.Model{ID: "local-model", API: "openai-completions", Provider: "local", BaseURL: "http://127.0.0.1:8080/v1", HasBaseURL: true, MaxTokens: 100, HasCompat: true}
}

func kinds(events []provider.Event) []provider.EventKind {
	out := make([]provider.EventKind, 0, len(events))
	for _, event := range events {
		out = append(out, event.Kind)
	}
	return out
}

func joined(events []provider.Event) string {
	parts := make([]string, 0, len(events))
	for _, event := range events {
		parts = append(parts, string(event.Kind))
	}
	return strings.Join(parts, " ")
}

func take(t *testing.T, turn Turn) []provider.Event {
	t.Helper()
	return takeUntil(t, turn, nil, nil)
}

func takeUntil(t *testing.T, turn Turn, stop func(), enough func([]provider.Event) bool) []provider.Event {
	t.Helper()
	var out []provider.Event
	deadline := time.After(5 * time.Second)
	for {
		if enough != nil && enough(out) {
			if stop != nil {
				stop()
			}
			return out
		}
		select {
		case event, open := <-turn.Events:
			if !open {
				return out
			}
			out = append(out, event)
		case <-deadline:
			t.Fatalf("the turn did not finish within five seconds: %s", joined(out))
		}
	}
}

func textDelta(body string) string {
	return frame(fmt.Sprintf(`{"choices":[{"delta":{"content":%q}}]}`, body))
}

func finishStop() string {
	return frame(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`)
}

func TestATurnIsTheProvidersOwnEventsOnAChannel(t *testing.T) {
	turn := ChunkStreamer{}.Stream(context.Background(), TurnRequest{
		Model:    completionsModel(),
		Messages: []provider.Message{{User: &provider.UserContent{Text: "hi", HasText: true}}},
		Read:     chunkReader([]string{textDelta("hello"), finishStop()}),
	})
	events := take(t, turn)
	if got, want := joined(events), "start text_delta done"; got != want {
		t.Errorf("a text turn is %s, want %s", got, want)
	}
	done := events[len(events)-1]
	if done.Message == nil || done.Message.StopReason != provider.StopStop {
		t.Errorf("the terminal carries %+v, want a completed message with a stop reason", done.Message)
	}
}

func TestTheTerminalCarriesTheWholeReplyRatherThanOnlyItsDeltas(t *testing.T) {
	turn := ChunkStreamer{}.Stream(context.Background(), TurnRequest{
		Model: completionsModel(),
		Read:  chunkReader([]string{textDelta("hel"), textDelta("lo"), finishStop()}),
	})
	events := take(t, turn)
	done := events[len(events)-1]
	if done.Message == nil || done.Message.Content[0].Text.Text != "hello" {
		t.Errorf("the terminal carries %+v, want the whole text, not the last delta", done.Message)
	}
}

func TestAToolCallArrivesWholeOnTheTerminal(t *testing.T) {
	call := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read","arguments":"{\"path\":\"a\"}"}}]}}]}`
	turn := ChunkStreamer{}.Stream(context.Background(), TurnRequest{
		Model: completionsModel(),
		Read:  chunkReader([]string{frame(call), frame(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)}),
	})
	events := take(t, turn)
	var found bool
	for _, event := range events {
		if event.Kind != provider.EventToolCallEnd || event.ToolCall == nil {
			continue
		}
		found = true
		if event.ToolCall.ID != "call_1" || event.ToolCall.Name != "read" || event.ToolCall.Arguments != `{"path":"a"}` {
			t.Errorf("the call is %+v, want call_1 running read with complete arguments", event.ToolCall)
		}
	}
	if !found {
		t.Errorf("a turn that called a tool never reported it: %s", joined(events))
	}
}

func TestAStreamThatBreaksMidTurnReportsAnErrorRatherThanACompletedReply(t *testing.T) {
	chunks := []string{textDelta("half a th")}
	index := 0
	turn := ChunkStreamer{}.Stream(context.Background(), TurnRequest{
		Model: completionsModel(),
		Read: func() ([]byte, error) {
			if index >= len(chunks) {
				return nil, fmt.Errorf("the connection dropped")
			}
			out := []byte(chunks[index])
			index++
			return out, nil
		},
	})
	events := take(t, turn)
	if last := events[len(events)-1]; last.Kind != provider.EventError {
		t.Errorf("a stream that broke mid-turn is %s, want an error rather than a completed reply: the run decides what a failure settles as", joined(events))
	}
}

func TestAStreamThatBrokeWithNothingToCarryReportsAnError(t *testing.T) {
	turn := ChunkStreamer{}.Stream(context.Background(), TurnRequest{
		Model: completionsModel(),
		Read:  func() ([]byte, error) { return nil, fmt.Errorf("the connection dropped") },
	})
	events := take(t, turn)
	last := events[len(events)-1]
	if last.Kind != provider.EventError {
		t.Errorf("a stream that broke with nothing to carry is %s, want an error the run can end on", joined(events))
	}
	if last.Reason == "" {
		t.Error("an error event with no reason is a failure the loop cannot put in its reply")
	}
}

func TestACancelledTurnStopsReadingAndStillFinishes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	read := 0
	turn := ChunkStreamer{}.Stream(ctx, TurnRequest{
		Model: completionsModel(),
		Read: func() ([]byte, error) {
			read++
			return []byte(textDelta("never")), nil
		},
	})
	events := take(t, turn)
	if read != 0 {
		t.Errorf("a turn cancelled before it started read %d times, want 0", read)
	}
	if len(events) != 0 {
		t.Errorf("a turn cancelled before it started emitted %s, want nothing", joined(events))
	}
}

func TestAnAnthropicTurnRunsTheAnthropicClientRatherThanTheCompletionsOne(t *testing.T) {
	model := provider.Model{ID: "claude", API: "anthropic-messages", Provider: "anthropic", MaxTokens: 64, HasCompat: true}
	frames := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":11}}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
	turn := ChunkStreamer{}.Stream(context.Background(), TurnRequest{Model: model, Read: chunkReader(frames)})
	events := take(t, turn)
	if got := kinds(events); len(got) == 0 || got[0] != provider.EventStart {
		t.Fatalf("an anthropic turn is %s, want it to open with a start", joined(events))
	}
	var text string
	for _, event := range events {
		if event.Kind == provider.EventDone && event.Message != nil {
			if event.Message.Content[0].Text != nil {
				text = event.Message.Content[0].Text.Text
			}
		}
	}
	if text != "hi" {
		t.Errorf("an anthropic turn carried %q, want hi: the completions client would not have read these frames at all", text)
	}
}

func TestATurnWithNoReaderFinishesRatherThanBlocking(t *testing.T) {
	turn := ChunkStreamer{}.Stream(context.Background(), TurnRequest{Model: completionsModel()})
	events := take(t, turn)
	if got := joined(events); got != "start done" {
		t.Errorf("a turn with nothing to read is %s, want an empty reply rather than a hang", got)
	}
}

func TestADeltaReachesTheChannelWhileTheStreamIsStillReading(t *testing.T) {
	release := make(chan struct{})
	served := 0
	read := func() ([]byte, error) {
		served++
		if served == 1 {
			return []byte(textDelta("half")), nil
		}
		<-release
		return []byte(finishStop()), nil
	}
	turn := ChunkStreamer{}.Stream(context.Background(), TurnRequest{Model: completionsModel(), Read: read})
	var seen []provider.EventKind
	deadline := time.After(5 * time.Second)
	for len(seen) < 2 {
		select {
		case event, open := <-turn.Events:
			if !open {
				t.Fatalf("the turn closed after %v, want a delta delivered while the reader was still blocked", seen)
			}
			seen = append(seen, event.Kind)
		case <-deadline:
			close(release)
			t.Fatalf("only %v arrived within five seconds of a blocked reader", seen)
		}
	}
	close(release)
	if seen[0] != provider.EventStart || seen[1] != provider.EventTextDelta {
		t.Errorf("a turn delivers %v, want a start then the delta, both before the reader finished", seen)
	}
}

func TestAForcedToolNamesItselfOnTheAnthropicWire(t *testing.T) {
	options := provider.StreamOptions{
		HasToolChoice: true,
		ToolChoice:    provider.ToolChoice{Mode: provider.ToolChoiceFunction, Function: "read"},
	}
	turn := provider.Context{
		Tools:    []provider.Tool{{Name: "read", Parameters: []byte(`{"type":"object"}`)}},
		Messages: []provider.Message{{User: &provider.UserContent{Text: "go", HasText: true}}},
	}
	body, _ := provider.BuildAnthropicRequestBody(anthropicModel(), turn, anthropicOptions(options), "")
	if !strings.Contains(string(body), `"tool_choice":{"type":"tool","name":"read"}`) {
		t.Errorf("a forced tool on the anthropic wire is %s, want the choice naming it: an empty name asks for a tool that does not exist", body)
	}
	dropped := anthropicOptions(provider.StreamOptions{HasToolChoice: true, ToolChoice: provider.ToolChoice{Mode: provider.ToolChoiceFunction}})
	without, _ := provider.BuildAnthropicRequestBody(anthropicModel(), turn, dropped, "")
	if !strings.Contains(string(without), `"name":""`) {
		t.Errorf("dropping the choice's name yields %s, want the empty name this test distinguishes from the working one", without)
	}
}

func TestAReasoningLevelReachesTheAnthropicWireAsTheEffortThatWireSpells(t *testing.T) {
	cases := map[string]string{
		"minimal": "low",
		"low":     "low",
		"medium":  "medium",
		"high":    "high",
		"xhigh":   "max",
	}
	for level, want := range cases {
		mapped := anthropicOptions(provider.StreamOptions{ReasoningEffort: level})
		if !mapped.ThinkingEnabled || mapped.ThinkingEffort != want {
			t.Errorf("the level %q maps to %+v, want thinking at effort %q: the wire does not spell the level the schema does", level, mapped, want)
		}
	}
}

func TestTheOffLevelIsNotThinkingEnabled(t *testing.T) {
	mapped := anthropicOptions(provider.StreamOptions{ReasoningEffort: "off"})
	if mapped.ThinkingEnabled {
		t.Errorf("the level \"off\" maps to %+v, want thinking disabled", mapped)
	}
	body, _ := provider.BuildAnthropicRequestBody(anthropicModel(), provider.Context{
		Messages: []provider.Message{{User: &provider.UserContent{Text: "go", HasText: true}}},
	}, mapped, "")
	if strings.Contains(string(body), `"thinking"`) {
		t.Errorf("a request at the off level is %s, want no thinking block", body)
	}
}

func TestAReasoningLevelThatAsksForThinkingPutsOneOnTheWire(t *testing.T) {
	mapped := anthropicOptions(provider.StreamOptions{ReasoningEffort: "high"})
	body, _ := provider.BuildAnthropicRequestBody(anthropicModel(), provider.Context{
		Messages: []provider.Message{{User: &provider.UserContent{Text: "go", HasText: true}}},
	}, mapped, "")
	if !strings.Contains(string(body), `"thinking"`) {
		t.Errorf("a request at the high level is %s, want a thinking block", body)
	}
}

func TestATurnCancelledMidStreamDeliversTheSameEventsEveryTime(t *testing.T) {
	var counts []int
	for attempt := 0; attempt < 20; attempt++ {
		blocked := make(chan struct{})
		var once bool
		read := func() ([]byte, error) {
			if !once {
				once = true
				return []byte(textDelta("half")), nil
			}
			<-blocked
			return nil, context.Canceled
		}
		ctx, cancel := context.WithCancel(context.Background())
		turn := ChunkStreamer{}.Stream(ctx, TurnRequest{Model: completionsModel(), Read: read})
		var events []provider.Event
		events = takeUntil(t, turn, cancel, func(seen []provider.Event) bool { return len(seen) >= 2 })
		cancel()
		close(blocked)
		events = append(events, take(t, turn)...)
		counts = append(counts, len(events))
	}
	for attempt, count := range counts {
		if count != counts[0] {
			t.Fatalf("a cancelled turn delivered %v events over twenty runs, want one answer every time: which events survive a cancellation cannot be a coin toss", counts[:attempt+1])
		}
	}
	if counts[0] != 2 {
		t.Errorf("a cancelled turn delivered %d events, want the start and the delta it read before the cancellation and nothing after", counts[0])
	}
}

func TestATurnCancelledBeforeItStartedDeliversNothing(t *testing.T) {
	for attempt := 0; attempt < 12; attempt++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		turn := ChunkStreamer{}.Stream(ctx, TurnRequest{Model: completionsModel(), Read: func() ([]byte, error) {
			t.Error("a turn cancelled before it started read from the model")
			return nil, nil
		}})
		if events := take(t, turn); len(events) != 0 {
			t.Fatalf("a turn cancelled before it started delivered %v, want nothing", joined(events))
		}
	}
}

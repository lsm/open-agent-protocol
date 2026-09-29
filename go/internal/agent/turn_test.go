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
	var out []provider.Event
	deadline := time.After(5 * time.Second)
	for {
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

func TestAForcedToolCarriesItsNameOnBothWires(t *testing.T) {
	options := provider.StreamOptions{
		HasToolChoice: true,
		ToolChoice:    provider.ToolChoice{Mode: provider.ToolChoiceFunction, Function: "read"},
	}
	completions := provider.BuildRequestBody(completionsModel(), provider.Context{
		Tools:    []provider.Tool{{Name: "read", Parameters: []byte(`{"type":"object"}`)}},
		Messages: []provider.Message{{User: &provider.UserContent{Text: "read a", HasText: true}}},
	}, options)
	if !strings.Contains(string(completions), `"tool_choice"`) || !strings.Contains(string(completions), "read") {
		t.Errorf("a forced tool on the completions wire is %s", completions)
	}
	anthropic, _ := provider.BuildAnthropicRequestBody(completionsModel(), provider.Context{
		Tools:    []provider.Tool{{Name: "read", Parameters: []byte(`{"type":"object"}`)}},
		Messages: []provider.Message{{User: &provider.UserContent{Text: "read a", HasText: true}}},
	}, anthropicOptions(options), "")
	if !strings.Contains(string(anthropic), `"name":"read"`) {
		t.Errorf("a forced tool on the anthropic wire is %s, want the tool's name: an empty name asks for a tool that does not exist", anthropic)
	}
}

func TestAReasoningEffortReachesTheAnthropicWireAsThinking(t *testing.T) {
	model := provider.Model{ID: "claude-sonnet-4-5", API: "anthropic-messages", Provider: "anthropic", MaxTokens: 4096, Reasoning: true, HasCompat: true}
	options := provider.StreamOptions{ReasoningEffort: "high"}
	mapped := anthropicOptions(options)
	if !mapped.ThinkingEnabled || mapped.ThinkingEffort != "high" {
		t.Errorf("a reasoning effort maps to %+v, want thinking enabled at that effort: the completions wire sends reasoning_effort, so dropping it here would make one request mean two things", mapped)
	}
	body, _ := provider.BuildAnthropicRequestBody(model, provider.Context{
		Messages: []provider.Message{{User: &provider.UserContent{Text: "think", HasText: true}}},
	}, mapped, "")
	if !strings.Contains(string(body), "thinking") {
		t.Errorf("an anthropic body with a reasoning effort is %s, want a thinking block", body)
	}
}

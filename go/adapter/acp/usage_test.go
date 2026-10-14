package acp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const devinTurnUsage = `{"totalTokens":7301,"inputTokens":7224,"outputTokens":77,"cachedReadTokens":6976}`

func settle(t *testing.T, f *fakeClient, stream base.EventStream, outcome promptOutcome) protocol.Envelope {
	t.Helper()
	drained := make(chan struct{})
	f.inbound <- rpc.InboundMessage{Barrier: drained}
	<-drained
	f.prompt <- outcome
	events := collect(t, stream)
	if len(events) == 0 {
		t.Fatal("no events")
	}
	return events[len(events)-1]
}

func extensionOf(t *testing.T, e protocol.Envelope, key string) map[string]any {
	t.Helper()
	raw, ok := e.Extensions[key]
	if !ok {
		return nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestTheFirstTurnOfANewSessionReportsTheAgentsTokenCountsAsRunUsageAndKeepsTheRestInExtensions(t *testing.T) {
	s, f := openTest(t, 64)
	_, stream := submit(t, s)
	<-f.promptStarted
	update := map[string]any{"sessionUpdate": "usage_update", "used": 7224, "size": 262000, "_meta": map[string]any{"cognition.ai/inputTokens": 7224}}
	f.update(t, update)
	f.update(t, update)
	terminal := settle(t, f, stream, promptOutcome{result: native.PromptResult{StopReason: "end_turn", Usage: json.RawMessage(devinTurnUsage)}})
	var completed protocol.RunCompletedPayload
	if err := terminal.DecodePayload(&completed); err != nil {
		t.Fatal(err)
	}
	if completed.Usage == nil || *completed.Usage != (protocol.Usage{InputTokens: 7224, OutputTokens: 77, TotalTokens: 7301}) {
		t.Fatalf("usage=%+v", completed.Usage)
	}
	turn := extensionOf(t, terminal, turnUsageExtension)
	if !reflect.DeepEqual(turn, map[string]any{"total_tokens": 7301.0, "input_tokens": 7224.0, "output_tokens": 77.0, "cached_read_tokens": 6976.0}) {
		t.Fatalf("turn usage=%v", turn)
	}
	if context := extensionOf(t, terminal, contextExtension); !reflect.DeepEqual(context, map[string]any{"used_tokens": 7224.0, "size_tokens": 262000.0}) {
		t.Fatalf("context=%v", context)
	}
}

func TestALaterTurnKeepsTheAgentsCountsOutOfRunUsageBecauseACPLeavesTheirScopeOpen(t *testing.T) {
	s, f := openTest(t, 64)
	_, first := submit(t, s)
	<-f.promptStarted
	settle(t, f, first, promptOutcome{result: native.PromptResult{StopReason: "end_turn", Usage: json.RawMessage(devinTurnUsage)}})
	_, second := submit(t, s)
	<-f.promptStarted
	terminal := settle(t, f, second, promptOutcome{result: native.PromptResult{StopReason: "end_turn", Usage: json.RawMessage(`{"totalTokens":9000,"inputTokens":8800,"outputTokens":200}`)}})
	var completed protocol.RunCompletedPayload
	if err := terminal.DecodePayload(&completed); err != nil {
		t.Fatal(err)
	}
	if completed.Usage != nil {
		t.Fatalf("a second turn projected %+v as the run's own usage", completed.Usage)
	}
	if turn := extensionOf(t, terminal, turnUsageExtension); turn["total_tokens"] != 9000.0 {
		t.Fatalf("turn usage=%v", turn)
	}
}

func TestAReopenedSessionsFirstTurnKeepsTheAgentsCountsOutOfRunUsage(t *testing.T) {
	client := &reopenClient{fakeClient: newFake()}
	opened, err := reopenAdapter(t, client, `{"loadSession":true}`).Open(context.Background(), base.OpenRequest{SessionID: "s", Participant: protocol.Participant{ID: "user"}, Reopen: true, NativeSessionID: "bound-native"})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close(context.Background())
	_, stream, err := opened.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "s", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}}})
	if err != nil {
		t.Fatal(err)
	}
	<-client.promptStarted
	terminal := settle(t, client.fakeClient, stream, promptOutcome{result: native.PromptResult{StopReason: "end_turn", Usage: json.RawMessage(devinTurnUsage)}})
	var completed protocol.RunCompletedPayload
	if err := terminal.DecodePayload(&completed); err != nil {
		t.Fatal(err)
	}
	if completed.Usage != nil {
		t.Fatalf("a reloaded session projected %+v as the run's own usage", completed.Usage)
	}
	if extensionOf(t, terminal, turnUsageExtension) == nil {
		t.Fatal("the agent's counts were dropped")
	}
}

func TestTurnUsageThatIsMissingOrMalformedIsReportedAsUnavailableAndABadOptionalCountDropsOnlyItself(t *testing.T) {
	for _, scenario := range []struct {
		name, usage string
		want        *protocol.Usage
		turn        map[string]any
	}{
		{"absent", "", nil, nil},
		{"null", `null`, nil, nil},
		{"negative input", `{"totalTokens":10,"inputTokens":-1,"outputTokens":11}`, nil, nil},
		{"missing output", `{"totalTokens":10,"inputTokens":10}`, nil, nil},
		{"not an object", `[1,2]`, nil, nil},
		{"measured zero", `{"totalTokens":0,"inputTokens":0,"outputTokens":0}`, &protocol.Usage{}, map[string]any{"total_tokens": 0.0, "input_tokens": 0.0, "output_tokens": 0.0}},
		{"negative thought", `{"totalTokens":12,"inputTokens":10,"outputTokens":2,"thoughtTokens":-4,"cachedWriteTokens":3}`, &protocol.Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}, map[string]any{"total_tokens": 12.0, "input_tokens": 10.0, "output_tokens": 2.0, "cached_write_tokens": 3.0}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			s, f := openTest(t, 64)
			_, stream := submit(t, s)
			<-f.promptStarted
			terminal := settle(t, f, stream, promptOutcome{result: native.PromptResult{StopReason: "end_turn", Usage: json.RawMessage(scenario.usage)}})
			var completed protocol.RunCompletedPayload
			if err := terminal.DecodePayload(&completed); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(completed.Usage, scenario.want) {
				t.Fatalf("usage=%+v want %+v", completed.Usage, scenario.want)
			}
			if turn := extensionOf(t, terminal, turnUsageExtension); !reflect.DeepEqual(turn, scenario.turn) {
				t.Fatalf("turn usage=%v want %v", turn, scenario.turn)
			}
		})
	}
}

func TestAMalformedUsageUpdateKeepsTheLastGoodContextAndABadCostDropsOnlyTheCost(t *testing.T) {
	s, f := openTest(t, 64)
	_, stream := submit(t, s)
	<-f.promptStarted
	f.update(t, map[string]any{"sessionUpdate": "usage_update", "used": 100, "size": 1000, "cost": map[string]any{"amount": 0.25, "currency": "USD"}})
	f.update(t, map[string]any{"sessionUpdate": "usage_update", "used": -1, "size": 1000})
	f.update(t, native.AgentMessageChunk{SessionUpdate: "agent_message_chunk", Content: native.ContentBlock{Type: "text", Text: "hi"}})
	waitCursor(t, s, "2")
	f.update(t, map[string]any{"sessionUpdate": "usage_update", "used": 200, "size": 1000, "cost": map[string]any{"amount": "lots", "currency": "USD"}})
	f.update(t, native.AgentMessageChunk{SessionUpdate: "agent_message_chunk", Content: native.ContentBlock{Type: "text", Text: "!"}})
	waitCursor(t, s, "3")
	terminal := settle(t, f, stream, promptOutcome{result: native.PromptResult{StopReason: "end_turn"}})
	if terminal.Type != protocol.TypeRunCompleted {
		t.Fatalf("a malformed usage update ended the run as %s", terminal.Type)
	}
	if context := extensionOf(t, terminal, contextExtension); !reflect.DeepEqual(context, map[string]any{"used_tokens": 200.0, "size_tokens": 1000.0}) {
		t.Fatalf("context=%v", context)
	}
}

func TestAReportedSessionCostIsCarriedAsReportedAndNoneIsInventedWithoutOne(t *testing.T) {
	s, f := openTest(t, 64)
	_, stream := submit(t, s)
	<-f.promptStarted
	f.update(t, map[string]any{"sessionUpdate": "usage_update", "used": 100, "size": 1000, "cost": map[string]any{"amount": 0.25, "currency": "USD"}})
	terminal := settle(t, f, stream, promptOutcome{result: native.PromptResult{StopReason: "end_turn", Usage: json.RawMessage(devinTurnUsage)}})
	if context := extensionOf(t, terminal, contextExtension); !reflect.DeepEqual(context["cost"], map[string]any{"amount": 0.25, "currency": "USD"}) {
		t.Fatalf("context=%v", context)
	}

	s, f = openTest(t, 64)
	_, stream = submit(t, s)
	<-f.promptStarted
	terminal = settle(t, f, stream, promptOutcome{result: native.PromptResult{StopReason: "end_turn", Usage: json.RawMessage(devinTurnUsage)}})
	if _, ok := terminal.Extensions[contextExtension]; ok {
		t.Fatal("a session that reported no usage_update was given a context")
	}
	if _, ok := extensionOf(t, terminal, turnUsageExtension)["cost"]; ok {
		t.Fatal("a cost was derived from token counts")
	}
}

func TestAFailedPromptCarriesTheContextItSawButNoTurnCounts(t *testing.T) {
	s, f := openTest(t, 64)
	_, stream := submit(t, s)
	<-f.promptStarted
	f.update(t, map[string]any{"sessionUpdate": "usage_update", "used": 512, "size": 262000})
	f.update(t, native.AgentMessageChunk{SessionUpdate: "agent_message_chunk", Content: native.ContentBlock{Type: "text", Text: "partial"}})
	waitCursor(t, s, "2")
	terminal := settle(t, f, stream, promptOutcome{err: errors.New("agent crashed")})
	var failed protocol.RunFailedPayload
	if err := terminal.DecodePayload(&failed); err != nil || terminal.Type != protocol.TypeRunFailed {
		t.Fatalf("terminal=%s err=%v", terminal.Type, err)
	}
	if failed.Usage != nil || terminal.Extensions[turnUsageExtension] != nil {
		t.Fatalf("a failed prompt reported turn counts: %+v", failed.Usage)
	}
	if context := extensionOf(t, terminal, contextExtension); context["used_tokens"] != 512.0 {
		t.Fatalf("context=%v", context)
	}
}

func TestAUsageUpdateBetweenTurnsIsTheContextTheNextTurnReports(t *testing.T) {
	s, f := openTest(t, 64)
	f.update(t, map[string]any{"sessionUpdate": "usage_update", "used": 0, "size": 262000})
	deadline := time.Now().Add(2 * time.Second)
	for {
		impl := s.(*session)
		impl.mu.Lock()
		seen := impl.context != nil
		impl.mu.Unlock()
		if seen {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("an update outside a run was dropped")
		}
		time.Sleep(time.Millisecond)
	}
	_, stream := submit(t, s)
	<-f.promptStarted
	terminal := settle(t, f, stream, promptOutcome{result: native.PromptResult{StopReason: "end_turn"}})
	if context := extensionOf(t, terminal, contextExtension); context["size_tokens"] != 262000.0 {
		t.Fatalf("context=%v", context)
	}
}

func TestAPromptAnsweredAfterItsRunAlreadySettledStillCountsAsTheSessionsTurn(t *testing.T) {
	s, f := openTest(t, 64)
	_, first := submit(t, s)
	<-f.promptStarted
	f.update(t, map[string]any{"sessionUpdate": "agent_message_chunk", "content": "not a block"})
	events := collect(t, first)
	if events[len(events)-1].Type != protocol.TypeRunFailed {
		t.Fatalf("events=%v", types(events))
	}
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn", Usage: json.RawMessage(devinTurnUsage)}}
	impl := s.(*session)
	deadline := time.Now().Add(2 * time.Second)
	for {
		impl.mu.Lock()
		prompted := impl.prompted
		impl.mu.Unlock()
		if prompted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the late answer was never settled")
		}
		time.Sleep(time.Millisecond)
	}
	_, second := submit(t, s)
	<-f.promptStarted
	terminal := settle(t, f, second, promptOutcome{result: native.PromptResult{StopReason: "end_turn", Usage: json.RawMessage(devinTurnUsage)}})
	var completed protocol.RunCompletedPayload
	if err := terminal.DecodePayload(&completed); err != nil {
		t.Fatal(err)
	}
	if completed.Usage != nil {
		t.Fatalf("the turn after a settled run was taken for the session's first: %+v", completed.Usage)
	}
}

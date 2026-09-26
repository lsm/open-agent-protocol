package acp

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/native"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func callNames(t *testing.T, events []protocol.Envelope) map[protocol.EnvelopeType]string {
	t.Helper()
	names := map[protocol.EnvelopeType]string{}
	for _, envelope := range events {
		switch envelope.Type {
		case protocol.TypeActionCallRequested, protocol.TypeActionCallStarted, protocol.TypeActionCallCompleted:
			var call protocol.ActionCallPayload
			if err := envelope.DecodePayload(&call); err != nil {
				t.Fatal(err)
			}
			names[envelope.Type] = call.Name
		}
	}
	return names
}

func TestToolNamePrefersProgrammaticNameAndIgnoresWrongType(t *testing.T) {
	s, f := openTest(t, 64)
	admission, stream := submit(t, s)
	<-f.promptStarted
	f.update(t, native.ToolCall{SessionUpdate: "tool_call", ToolCallID: "tool", Title: "Read", Name: json.RawMessage(`"read_file"`), Status: "pending"})
	status := "in_progress"
	f.update(t, native.ToolCallUpdate{SessionUpdate: "tool_call_update", ToolCallID: "tool", Status: &status, Name: json.RawMessage(`7`)})
	status = "completed"
	f.update(t, native.ToolCallUpdate{SessionUpdate: "tool_call_update", ToolCallID: "tool", Status: &status, Name: json.RawMessage(`"grep"`), RawOutput: json.RawMessage(`{"ok":true}`)})
	waitCursor(t, s, "4")
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
	events := collect(t, stream)
	want := map[protocol.EnvelopeType]string{protocol.TypeActionCallRequested: "read_file", protocol.TypeActionCallStarted: "read_file", protocol.TypeActionCallCompleted: "grep"}
	if got := callNames(t, events); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("names=%v", got)
	}
	assertValidTrace(t, admission, events)
}

func TestToolNameFallsBackToTitle(t *testing.T) {
	s, f := openTest(t, 64)
	admission, stream := submit(t, s)
	<-f.promptStarted
	f.update(t, native.ToolCall{SessionUpdate: "tool_call", ToolCallID: "tool", Title: "Read", Name: json.RawMessage(`null`), Status: "completed"})
	waitCursor(t, s, "4")
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
	events := collect(t, stream)
	if got := callNames(t, events)[protocol.TypeActionCallRequested]; got != "Read" {
		t.Fatalf("name=%q", got)
	}
	assertValidTrace(t, admission, events)
}

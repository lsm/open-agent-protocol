package makai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/adapter/makai/internal/native"
	"github.com/lsm/open-agent-protocol/adapter/makai/internal/stdio"
	"github.com/lsm/open-agent-protocol/protocol"
)

// probe is the real descriptor, which the traces here have to be judged
// against: these runs exercise an optional capability, so a synthetic
// descriptor that does not advertise it reads every event as unavailable.
func probe(t *testing.T) base.Descriptor {
	t.Helper()
	implementation, err := New(Config{
		Factory:          ClientFactoryFunc(func(context.Context) (Client, error) { return newFakeClient(), nil }),
		WorkingDirectory: "/workspace", AgentConfig: json.RawMessage(`{"model":"test"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := implementation.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

func providedTool() protocol.ToolDefinition {
	return protocol.ToolDefinition{
		Name: "lookup", Description: "A tool the control layer executes.",
		InputSchema:    json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
		ExecutionOwner: "user",
	}
}

// openProviding opens one session with a control-owned catalog.
func openProviding(t *testing.T, tools ...protocol.ToolDefinition) (base.Session, *fakeClient) {
	t.Helper()
	client := newFakeClient()
	implementation, err := New(Config{
		Factory:          ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil }),
		WorkingDirectory: "/workspace", AgentConfig: json.RawMessage(`{"model":"test"}`),
		Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := implementation.Open(context.Background(), base.OpenRequest{
		SessionID: "session", Participant: protocol.Participant{ID: "user"}, Tools: tools,
	})
	if err != nil {
		t.Fatal(err)
	}
	return session, client
}

// toolExecute is the native frame that asks the client to run a tool.
func toolExecute(t *testing.T, client *fakeClient, name string) {
	t.Helper()
	env, err := native.NewEnvelope(native.TypeToolExecute, "Abcdefghijklmnopqrstu", "00000000000000000000000199", 2, 1,
		native.ToolExecute{ToolCallID: "native-call-1", ToolName: name, ArgsJSON: `{"q":"oap"}`})
	if err != nil {
		t.Fatal(err)
	}
	client.inbound <- stdio.Inbound{Envelope: &env}
}

// endRun publishes the agent_end that settles the scripted run.
func endRun(t *testing.T, client *fakeClient) {
	t.Helper()
	client.event(t, 9, map[string]any{"type": "agent_end", "stop_reason": "stop"})
}

// pendingCall waits for the reducer to publish the run's control-owned call.
// Frames arrive through a channel, so reading the reducer without waiting
// races the goroutine that drains it.
func pendingCall(t *testing.T, open base.Session, runID protocol.RunID) *callState {
	t.Helper()
	inner := open.(*session)
	deadline := time.Now().Add(time.Second)
	for {
		inner.mu.Lock()
		var call *callState
		if run := inner.runs[runID]; run != nil {
			call = run.call
		}
		inner.mu.Unlock()
		if call != nil {
			return call
		}
		if time.Now().After(deadline) {
			t.Fatal("no control-owned call was published")
		}
		time.Sleep(time.Millisecond)
	}
}

func callRequest(call *callState, runID protocol.RunID) protocol.ActionCallResolveRequest {
	return protocol.ActionCallResolveRequest{
		InteractionID: call.interaction, SessionID: "session", RunID: runID,
		ToolCallID: call.toolCallID, RequestedBy: "agent", RespondedBy: "user",
		Result: json.RawMessage(`{"hits":2}`),
	}
}

func resolveCall(t *testing.T, open base.Session, id protocol.EnvelopeID, request protocol.ActionCallResolveRequest) protocol.ActionCallResolveResponse {
	t.Helper()
	answer, err := open.(base.CallResolver).ResolveCall(context.Background(), base.CallResolution{RequestID: id, Request: request})
	if err != nil {
		t.Fatalf("resolve call: %v", err)
	}
	return answer
}

// TestUnprovidedToolStillRefuses pins the boundary the unit does not move. A
// tool_execute naming something the control layer never provided has no owner
// to route to, so it keeps the refusal this adapter has always given. The
// protocol gained a place for the frames it can route, not for every frame.
func TestUnprovidedToolStillRefuses(t *testing.T) {
	session, client := openProviding(t, providedTool())
	admission, stream := submitTest(t, session)
	toolExecute(t, client, "not_provided")
	events := adaptertest.Drain(t, stream, time.Second)
	last := events[len(events)-1]
	if last.Type != protocol.TypeRunFailed {
		t.Fatalf("the run settled %s, want run.failed", last.Type)
	}
	var failure protocol.RunFailedPayload
	if err := last.DecodePayload(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != "makai_tool_executor_unavailable" {
		t.Fatalf("failure code %q, want the unrouted-executor refusal", failure.Error.Code)
	}
	adaptertest.AssertProtocolValidWithDescriptor(t, admission, probe(t), events)
}

// TestProvidedToolBridgeWritesBackAndSettles is the round trip through the
// native bridge: the harness asks, the participant answers, the adapter writes
// the answer to makai and only then publishes the terminal. The write has to
// come first — an endpoint that published the terminal first would tell the
// control layer its answer had landed before it had.
func TestProvidedToolBridgeWritesBackAndSettles(t *testing.T) {
	session, client := openProviding(t, providedTool())
	admission, stream := submitTest(t, session)
	toolExecute(t, client, "lookup")
	call := pendingCall(t, session, admission.RunID)

	if answer := resolveCall(t, session, "resolve-1", callRequest(call, admission.RunID)); !answer.Accepted {
		t.Fatalf("a valid resolution was refused %q", answer.Reason)
	}
	client.mu.Lock()
	sent := append([]native.Envelope(nil), client.sends...)
	client.mu.Unlock()
	written := sent[len(sent)-1]
	if written.Type != native.TypeToolResult {
		t.Fatalf("wrote %q back, want tool_result", written.Type)
	}
	result, err := native.DecodePayload[native.ToolResult](written)
	if err != nil {
		t.Fatal(err)
	}
	if result.ToolCallID != "native-call-1" || result.IsError || result.ResultJSON != `{"hits":2}` {
		t.Fatalf("tool_result = %+v", result)
	}

	endRun(t, client)
	events := adaptertest.Drain(t, stream, time.Second)
	adaptertest.AssertProtocolValidWithDescriptor(t, admission, probe(t), events)
}

// TestErrorArmSetsNativeIsError pins that a failed execution travels makai's
// one result channel with is_error set, because the pin classifies by that
// flag rather than by frame type.
func TestErrorArmSetsNativeIsError(t *testing.T) {
	session, client := openProviding(t, providedTool())
	admission, stream := submitTest(t, session)
	toolExecute(t, client, "lookup")
	call := pendingCall(t, session, admission.RunID)

	request := callRequest(call, admission.RunID)
	request.Result = nil
	request.Error = &protocol.ProtocolError{Code: "lookup_failed", Message: "the control layer could not run it"}
	if answer := resolveCall(t, session, "resolve-1", request); !answer.Accepted {
		t.Fatalf("the error arm was refused %q", answer.Reason)
	}
	client.mu.Lock()
	written := client.sends[len(client.sends)-1]
	client.mu.Unlock()
	result, err := native.DecodePayload[native.ToolResult](written)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("a failed execution must set is_error")
	}
	endRun(t, client)
	events := adaptertest.Drain(t, stream, time.Second)
	var failed bool
	for _, event := range events {
		if event.Type == protocol.TypeActionCallFailed {
			failed = true
		}
	}
	if !failed {
		t.Fatal("an accepted error arm derives action.call.failed")
	}
	adaptertest.AssertProtocolValidWithDescriptor(t, admission, probe(t), events)
}

// TestMakaiResolveLadder walks the refusal ladder over the native bridge. Each
// case satisfies the rung below it too, because the property is that the
// endpoint reports the highest reason present rather than that each name is
// reachable alone.
func TestMakaiResolveLadder(t *testing.T) {
	t.Run("unknown interaction outranks a foreign responder", func(t *testing.T) {
		session, client := openProviding(t, providedTool())
		admission, _ := submitTest(t, session)
		toolExecute(t, client, "lookup")
		call := pendingCall(t, session, admission.RunID)
		request := callRequest(call, admission.RunID)
		request.InteractionID = "no-such-interaction"
		request.RespondedBy = "someone-else"
		answer := resolveCall(t, session, "resolve-1", request)
		if answer.Accepted || answer.Reason != protocol.ReasonUnknownInteraction {
			t.Fatalf("got accepted=%v reason=%q", answer.Accepted, answer.Reason)
		}
	})

	t.Run("wrong responder outranks a settled call", func(t *testing.T) {
		session, client := openProviding(t, providedTool())
		admission, _ := submitTest(t, session)
		toolExecute(t, client, "lookup")
		call := pendingCall(t, session, admission.RunID)
		if answer := resolveCall(t, session, "resolve-1", callRequest(call, admission.RunID)); !answer.Accepted {
			t.Fatalf("resolution refused %q", answer.Reason)
		}
		request := callRequest(call, admission.RunID)
		request.RespondedBy = "someone-else"
		answer := resolveCall(t, session, "resolve-2", request)
		if answer.Accepted || answer.Reason != protocol.ReasonWrongResponder {
			t.Fatalf("got accepted=%v reason=%q", answer.Accepted, answer.Reason)
		}
		// A foreign sender learns only that it is foreign: the state of a
		// call it does not own is not its business.
		if answer.Details != nil {
			t.Fatal("a wrong_responder refusal carries no settlement")
		}
	})

	t.Run("already resolved names its settlement", func(t *testing.T) {
		session, client := openProviding(t, providedTool())
		admission, stream := submitTest(t, session)
		toolExecute(t, client, "lookup")
		call := pendingCall(t, session, admission.RunID)
		if answer := resolveCall(t, session, "resolve-1", callRequest(call, admission.RunID)); !answer.Accepted {
			t.Fatalf("resolution refused %q", answer.Reason)
		}
		answer := resolveCall(t, session, "resolve-2", callRequest(call, admission.RunID))
		if answer.Accepted || answer.Reason != protocol.ReasonAlreadyResolved {
			t.Fatalf("got accepted=%v reason=%q", answer.Accepted, answer.Reason)
		}
		if answer.Details == nil || answer.Details.SettlementID == "" {
			t.Fatal("an already_resolved refusal names the settlement the trace carries")
		}
		endRun(t, client)
		events := adaptertest.Drain(t, stream, time.Second)
		var found bool
		for _, event := range events {
			if event.ID == answer.Details.SettlementID {
				found = true
			}
		}
		if !found {
			t.Fatal("the named settlement is not in the trace")
		}
	})

	t.Run("repeated acknowledgement", func(t *testing.T) {
		session, client := openProviding(t, providedTool())
		admission, _ := submitTest(t, session)
		toolExecute(t, client, "lookup")
		call := pendingCall(t, session, admission.RunID)
		first := callRequest(call, admission.RunID)
		first.Result, first.Started = nil, &protocol.ResolveArmStarted{}
		if answer := resolveCall(t, session, "resolve-1", first); !answer.Accepted {
			t.Fatalf("first acknowledgement refused %q", answer.Reason)
		}
		answer := resolveCall(t, session, "resolve-2", first)
		if answer.Accepted || answer.Reason != protocol.ReasonRepeatedAcknowledgement {
			t.Fatalf("got accepted=%v reason=%q", answer.Accepted, answer.Reason)
		}
	})
}

// TestAcknowledgementWritesNothingBack pins the difference an acknowledgement
// makes and the one it does not: it releases the start event, and it tells the
// harness nothing, because the harness is waiting for a result.
func TestAcknowledgementWritesNothingBack(t *testing.T) {
	session, client := openProviding(t, providedTool())
	admission, _ := submitTest(t, session)
	toolExecute(t, client, "lookup")
	call := pendingCall(t, session, admission.RunID)
	client.mu.Lock()
	before := len(client.sends)
	client.mu.Unlock()

	request := callRequest(call, admission.RunID)
	request.Result, request.Started = nil, &protocol.ResolveArmStarted{}
	if answer := resolveCall(t, session, "resolve-1", request); !answer.Accepted {
		t.Fatalf("acknowledgement refused %q", answer.Reason)
	}
	client.mu.Lock()
	after := len(client.sends)
	client.mu.Unlock()
	if after != before {
		t.Fatal("an acknowledgement is not a resolution and writes nothing back")
	}
}

// TestRunSettlementClosesAPendingCall pins that a run never terminates with a
// control-owned call still pending: nobody is going to answer it once the
// harness is gone.
func TestRunSettlementClosesAPendingCall(t *testing.T) {
	session, client := openProviding(t, providedTool())
	admission, stream := submitTest(t, session)
	toolExecute(t, client, "lookup")
	pendingCall(t, session, admission.RunID)
	endRun(t, client)
	events := adaptertest.Drain(t, stream, time.Second)
	var closed bool
	for _, event := range events {
		if event.Type == protocol.TypeActionCallFailed || event.Type == protocol.TypeActionCallCancelled {
			closed = true
		}
	}
	if !closed {
		t.Fatal("a settling run must close its pending control-owned call")
	}
	adaptertest.AssertProtocolValidWithDescriptor(t, admission, probe(t), events)
}

// TestMakaiProvisioningRefusals walks the admission rules. Each refusal names
// the entry to change, because a refusal that says only that something was
// wrong gives the caller nothing to act on.
func TestMakaiProvisioningRefusals(t *testing.T) {
	cases := []struct {
		name, tool, want string
		tools            []protocol.ToolDefinition
	}{
		{
			name: "foreign owner", tool: "lookup", want: "execution_owner",
			tools: []protocol.ToolDefinition{func() protocol.ToolDefinition {
				tool := providedTool()
				tool.ExecutionOwner = "somebody-else"
				return tool
			}()},
		},
		{
			name: "dangling source", tool: "lookup", want: "resolves to no declared or attached source",
			tools: []protocol.ToolDefinition{func() protocol.ToolDefinition {
				tool := providedTool()
				tool.Source = "nowhere"
				return tool
			}()},
		},
		{
			name: "duplicate name", tool: "lookup", want: "provided twice",
			tools: []protocol.ToolDefinition{providedTool(), providedTool()},
		},
		{
			name: "foreign schema dialect", tool: "lookup", want: "dialect",
			tools: []protocol.ToolDefinition{func() protocol.ToolDefinition {
				tool := providedTool()
				tool.InputSchema = json.RawMessage(`{"$schema":"http://json-schema.org/draft-07/schema#"}`)
				return tool
			}()},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			client := newFakeClient()
			implementation, err := New(Config{
				Factory:          ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil }),
				WorkingDirectory: "/workspace", AgentConfig: json.RawMessage(`{"model":"test"}`),
				Clock: &fakeClock{}, IDs: &fakeIDs{},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = implementation.Open(context.Background(), base.OpenRequest{
				SessionID: "session", Participant: protocol.Participant{ID: "user"}, Tools: testCase.tools,
			})
			var refusal *base.UnsupportedControlError
			if !errors.As(err, &refusal) {
				t.Fatalf("open returned %v, want a typed unsupported_feature", err)
			}
			if refusal.Feature != protocol.FeatureToolsProvide || refusal.Tool != testCase.tool || !strings.Contains(refusal.Detail, testCase.want) {
				t.Fatalf("refusal = %+v", refusal)
			}
			// The refusal lands before a process starts, so no child is left
			// behind by an open that never returned a session.
			client.mu.Lock()
			sends := len(client.sends)
			client.mu.Unlock()
			if sends != 0 {
				t.Fatal("a refused open wrote to the harness")
			}
		})
	}
}

// TestProvidedCatalogReachesEveryMessage pins the narrowing this adapter does:
// makai provisions per message, the unit provisions per session, so the
// definitions fixed at open are repeated verbatim on every agent_message and a
// submit can neither add a tool nor drop one.
func TestProvidedCatalogReachesEveryMessage(t *testing.T) {
	session, client := openProviding(t, providedTool())
	submitTest(t, session)
	client.mu.Lock()
	sent := append([]native.Envelope(nil), client.sends...)
	client.mu.Unlock()
	if len(sent) == 0 {
		t.Fatal("no native message was written")
	}
	message, err := native.DecodePayload[native.AgentMessage](sent[0])
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Tools []native.ToolDefinition `json:"tools"`
	}
	if err := json.Unmarshal([]byte(message.MessageJSON), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Tools) != 1 || decoded.Tools[0].Name != "lookup" {
		t.Fatalf("agent_message carries %+v, want the provided catalog", decoded.Tools)
	}
}

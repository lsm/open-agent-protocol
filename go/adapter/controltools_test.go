package adapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func providedTool() protocol.ToolDefinition {
	return protocol.ToolDefinition{
		Name: "lookup", Description: "A tool the control layer executes.",
		InputSchema:    json.RawMessage(`{"type":"object","properties":{"operation":{"type":"string"}}}`),
		ExecutionOwner: "user",
	}
}

func openProviding(t *testing.T, tools ...protocol.ToolDefinition) adapter.Session {
	t.Helper()
	implementation := adapter.NewMemory(adapter.Config{Clock: &fixedClock{}, IDs: &fixedIDs{}})
	session, err := implementation.Open(context.Background(), adapter.OpenRequest{
		SessionID: "control-tools", Participant: protocol.Participant{ID: "user"}, Tools: tools,
	})
	if err != nil {
		t.Fatalf("open providing tools: %v", err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	return session
}

func referenceDescriptor(t *testing.T) adapter.Descriptor {
	t.Helper()
	descriptor, err := adapter.NewMemory(adapter.Config{}).Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

var providedCallSubmit = protocol.MessageSubmitRequest{
	SessionID: "control-tools", Delivery: protocol.DeliveryAuto,
	Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
}

func submitProvidedCall(t *testing.T, session adapter.Session) (protocol.MessageSubmitResponse, adapter.EventStream, []protocol.Envelope, protocol.ActionCallPayload) {
	t.Helper()
	admission, stream, err := session.Submit(context.Background(), providedCallSubmit)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	events := []protocol.Envelope{
		adaptertest.Next(t, stream, time.Second),
		adaptertest.Next(t, stream, time.Second),
		adaptertest.Next(t, stream, time.Second),
	}
	requested := events[len(events)-1]
	if requested.Type != protocol.TypeActionCallRequested {
		t.Fatalf("expected a call request, got %s", requested.Type)
	}
	var call protocol.ActionCallPayload
	if err := requested.DecodePayload(&call); err != nil {
		t.Fatal(err)
	}
	if call.InteractionID == "" || call.RespondedBy == "" {
		t.Fatal("a control-owned call carries an interaction and a responder, or nobody can resolve it")
	}
	if call.ExecutionOwner != "user" {
		t.Fatalf("execution_owner = %q, want the opening participant", call.ExecutionOwner)
	}
	return admission, stream, events, call
}

func resolve(t *testing.T, session adapter.Session, id protocol.EnvelopeID, request protocol.ActionCallResolveRequest) protocol.ActionCallResolveResponse {
	t.Helper()
	resolver, ok := session.(adapter.CallResolver)
	if !ok {
		t.Fatal("the reference session does not resolve control-owned calls")
	}
	answer, err := resolver.ResolveCall(context.Background(), adapter.CallResolution{RequestID: id, Request: request})
	if err != nil {
		t.Fatalf("resolve call: %v", err)
	}
	return answer
}

func resultArm(call protocol.ActionCallPayload) protocol.ActionCallResolveRequest {
	return protocol.ActionCallResolveRequest{
		InteractionID: call.InteractionID, SessionID: call.SessionID, RunID: call.RunID,
		ToolCallID: call.ToolCallID, RequestedBy: call.RequestedBy, RespondedBy: call.RespondedBy,
		Result: json.RawMessage(`{"found":true}`),
	}
}

func answerInput(t *testing.T, session adapter.Session, event protocol.Envelope) {
	t.Helper()
	var prompt protocol.UserInputRequestedPayload
	if err := event.DecodePayload(&prompt); err != nil {
		t.Fatal(err)
	}
	err := session.Resolve(context.Background(), adapter.InteractionResolution{
		RunID: prompt.RunID, RespondedBy: prompt.RespondedBy,
		Input: &protocol.UserInputResolveRequest{
			InteractionID: prompt.InteractionID, SessionID: prompt.SessionID, RunID: prompt.RunID,
			RequestedBy: prompt.RequestedBy, RespondedBy: prompt.RespondedBy,
			Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}},
		},
	})
	if err != nil {
		t.Fatalf("resolve input: %v", err)
	}
}

func TestProvidedCallRoundTripValidates(t *testing.T) {
	session := openProviding(t, providedTool())
	admission, stream, events, call := submitProvidedCall(t, session)

	acknowledge := resultArm(call)
	acknowledge.Result = nil
	acknowledge.Started = &protocol.ResolveArmStarted{}
	if answer := resolve(t, session, "resolve-ack", acknowledge); !answer.Accepted {
		t.Fatalf("a first acknowledgement must be accepted, got %q", answer.Reason)
	}
	if answer := resolve(t, session, "resolve-result", resultArm(call)); !answer.Accepted {
		t.Fatalf("a valid resolution must be accepted, got %q", answer.Reason)
	}

	events = append(events, adaptertest.Next(t, stream, time.Second))
	if events[len(events)-1].Type != protocol.TypeActionCallStarted {
		t.Fatalf("an accepted acknowledgement releases the start, got %s", events[len(events)-1].Type)
	}
	for {
		event := adaptertest.Next(t, stream, time.Second)
		events = append(events, event)
		if event.Type == protocol.TypeUserInputRequested {
			answerInput(t, session, event)
		}
		if event.Type == protocol.TypeRunCompleted {
			break
		}
	}
	adaptertest.AssertProtocolValidWithSubmit(t, providedCallSubmit, admission, referenceDescriptor(t), events)

	var completed protocol.ActionCallPayload
	for _, event := range events {
		if event.Type == protocol.TypeActionCallCompleted {
			if err := event.DecodePayload(&completed); err != nil {
				t.Fatal(err)
			}
		}
	}
	if string(completed.Result) != `{"found":true}` {
		t.Fatalf("the terminal carries %s, want the accepted result", completed.Result)
	}
	if completed.RequestID != "resolve-result" {
		t.Fatalf("request_id = %q, want the resolve request that authorized the terminal", completed.RequestID)
	}
}

func TestUnacknowledgedResolutionStillStarts(t *testing.T) {
	session := openProviding(t, providedTool())
	_, stream, _, call := submitProvidedCall(t, session)
	if answer := resolve(t, session, "resolve-1", resultArm(call)); !answer.Accepted {
		t.Fatalf("resolution refused %q", answer.Reason)
	}
	first := adaptertest.Next(t, stream, time.Second)
	second := adaptertest.Next(t, stream, time.Second)
	if first.Type != protocol.TypeActionCallStarted || second.Type != protocol.TypeActionCallCompleted {
		t.Fatalf("got %s then %s, want the start immediately before the terminal", first.Type, second.Type)
	}
}

func TestResolveLadderReportsTheHighestReason(t *testing.T) {
	t.Run("unknown interaction", func(t *testing.T) {
		session := openProviding(t, providedTool())
		_, _, _, call := submitProvidedCall(t, session)
		request := resultArm(call)
		request.InteractionID = "no-such-interaction"

		request.RespondedBy = "someone-else"
		answer := resolve(t, session, "resolve-1", request)
		if answer.Accepted || answer.Reason != protocol.ReasonUnknownInteraction {
			t.Fatalf("got accepted=%v reason=%q", answer.Accepted, answer.Reason)
		}
	})

	t.Run("wrong responder outranks progress", func(t *testing.T) {
		session := openProviding(t, providedTool())
		_, _, _, call := submitProvidedCall(t, session)
		if answer := resolve(t, session, "resolve-1", resultArm(call)); !answer.Accepted {
			t.Fatalf("resolution refused %q", answer.Reason)
		}

		request := resultArm(call)
		request.RespondedBy = "someone-else"
		answer := resolve(t, session, "resolve-2", request)
		if answer.Accepted || answer.Reason != protocol.ReasonWrongResponder {
			t.Fatalf("got accepted=%v reason=%q", answer.Accepted, answer.Reason)
		}
		if answer.Details != nil {
			t.Fatal("only already_resolved carries a settlement, because only it has one to point at")
		}
	})

	t.Run("already resolved names its settlement", func(t *testing.T) {
		session := openProviding(t, providedTool())
		_, stream, _, call := submitProvidedCall(t, session)
		if answer := resolve(t, session, "resolve-1", resultArm(call)); !answer.Accepted {
			t.Fatalf("resolution refused %q", answer.Reason)
		}
		adaptertest.Next(t, stream, time.Second)
		terminal := adaptertest.Next(t, stream, time.Second)

		answer := resolve(t, session, "resolve-2", resultArm(call))
		if answer.Accepted || answer.Reason != protocol.ReasonAlreadyResolved {
			t.Fatalf("got accepted=%v reason=%q", answer.Accepted, answer.Reason)
		}
		if answer.Details == nil || answer.Details.SettlementID != terminal.ID {
			t.Fatalf("details = %+v, want the terminal the trace carries", answer.Details)
		}
	})

	t.Run("repeated acknowledgement", func(t *testing.T) {
		session := openProviding(t, providedTool())
		_, _, _, call := submitProvidedCall(t, session)
		first := resultArm(call)
		first.Result, first.Started = nil, &protocol.ResolveArmStarted{}
		if answer := resolve(t, session, "resolve-1", first); !answer.Accepted {
			t.Fatalf("first acknowledgement refused %q", answer.Reason)
		}
		answer := resolve(t, session, "resolve-2", first)
		if answer.Accepted || answer.Reason != protocol.ReasonRepeatedAcknowledgement {
			t.Fatalf("got accepted=%v reason=%q", answer.Accepted, answer.Reason)
		}
	})

}

func TestCancelClosesAPendingProvidedCall(t *testing.T) {
	session := openProviding(t, providedTool())
	admission, stream, events, _ := submitProvidedCall(t, session)
	if _, err := session.Cancel(context.Background(), admission.RunID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	var cancelledCall bool
	for {
		event := adaptertest.Next(t, stream, time.Second)
		events = append(events, event)
		if event.Type == protocol.TypeActionCallCancelled {
			cancelledCall = true
		}
		if event.Type == protocol.TypeRunCancelled {
			break
		}
	}
	if !cancelledCall {
		t.Fatal("a cancelled run must close its pending control-owned call")
	}
	adaptertest.AssertProtocolValidWithSubmitAndCancellation(t, providedCallSubmit, admission, referenceDescriptor(t), events)
}

func TestProvidedCatalogAndAcknowledgedSubset(t *testing.T) {
	session := openProviding(t, providedTool())
	lister, ok := session.(adapter.ToolLister)
	if !ok {
		t.Fatal("the reference session does not serve a catalog")
	}
	catalog, err := lister.Tools(context.Background(), protocol.ToolsListRequest{SessionID: "control-tools"})
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	var listed bool
	for _, tool := range catalog.Tools.Tools {
		if tool.Name == "lookup" {
			listed = tool.ExecutionOwner == "user"
		}
	}
	if !listed {
		t.Fatal("a provided tool joins the catalog with the opener as execution_owner")
	}

	_, _, _, call := submitProvidedCall(t, session)
	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ActiveRuns) != 1 || len(state.ActiveRuns[0].PendingInteractions) != 1 {
		t.Fatalf("the call must be listed as pending, got %+v", state.ActiveRuns)
	}
	if len(state.ActiveRuns[0].AcknowledgedInteractions) != 0 {
		t.Fatal("nothing has been acknowledged yet")
	}

	acknowledge := resultArm(call)
	acknowledge.Result, acknowledge.Started = nil, &protocol.ResolveArmStarted{}
	if answer := resolve(t, session, "resolve-1", acknowledge); !answer.Accepted {
		t.Fatalf("acknowledgement refused %q", answer.Reason)
	}
	state, err = session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(state.ActiveRuns[0].PendingInteractions) != 1 {
		t.Fatal("an acknowledgement does not settle the call")
	}
	if len(state.ActiveRuns[0].AcknowledgedInteractions) != 1 {
		t.Fatal("an accepted acknowledgement must be reported in the subset")
	}
}

func TestProvisioningRefusalsNameTheOffendingTool(t *testing.T) {
	cases := []struct {
		name  string
		tools []protocol.ToolDefinition
		tool  string
		want  string
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
			name: "colliding name", tool: "scripted_tool", want: "already resolves to a catalog entry",
			tools: []protocol.ToolDefinition{func() protocol.ToolDefinition {
				tool := providedTool()
				tool.Name = "scripted_tool"
				return tool
			}()},
		},
		{
			name: "outside the disclosed name pattern", tool: "Lookup", want: "name_pattern",
			tools: []protocol.ToolDefinition{func() protocol.ToolDefinition {
				tool := providedTool()
				tool.Name = "Lookup"
				return tool
			}()},
		},
		{
			name: "foreign schema dialect", tool: "lookup", want: "dialect",
			tools: []protocol.ToolDefinition{func() protocol.ToolDefinition {
				tool := providedTool()
				tool.InputSchema = json.RawMessage(`{"$schema":"http://json-schema.org/draft-07/schema#"}`)
				return tool
			}()},
		},
		{
			name: "over the disclosed ceiling", tool: "third", want: "at most 2 tools",
			tools: []protocol.ToolDefinition{
				func() protocol.ToolDefinition { tool := providedTool(); tool.Name = "first"; return tool }(),
				func() protocol.ToolDefinition { tool := providedTool(); tool.Name = "second"; return tool }(),
				func() protocol.ToolDefinition { tool := providedTool(); tool.Name = "third"; return tool }(),
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			implementation := adapter.NewMemory(adapter.Config{Clock: &fixedClock{}, IDs: &fixedIDs{}})
			_, err := implementation.Open(context.Background(), adapter.OpenRequest{
				SessionID: "control-tools", Participant: protocol.Participant{ID: "user"}, Tools: testCase.tools,
			})
			var refusal *adapter.UnsupportedControlError
			if !errors.As(err, &refusal) {
				t.Fatalf("open returned %v, want a typed unsupported_feature", err)
			}
			if refusal.Feature != protocol.FeatureToolsProvide || refusal.Reason != adapter.ControlUnsatisfiable {
				t.Fatalf("refusal = %+v", refusal)
			}
			if refusal.Tool != testCase.tool {
				t.Fatalf("refusal names %q, want %q", refusal.Tool, testCase.tool)
			}
			if !strings.Contains(refusal.Detail, testCase.want) {
				t.Fatalf("detail %q does not explain %q", refusal.Detail, testCase.want)
			}
		})
	}
}

func TestProvisioningAdmitsWhatItDiscloses(t *testing.T) {
	first := providedTool()
	second := providedTool()
	second.Name = "second_lookup"
	second.Source = "reference-mcp"
	session := openProviding(t, first, second)
	lister, ok := session.(adapter.ToolLister)
	if !ok {
		t.Fatal("the reference session does not serve a catalog")
	}
	catalog, err := lister.Tools(context.Background(), protocol.ToolsListRequest{SessionID: "control-tools"})
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	if len(catalog.Tools.Tools) != 3 {
		t.Fatalf("catalog carries %d tools, want the scripted one plus both provided", len(catalog.Tools.Tools))
	}
}

func TestUnprovidedSessionIsUnchanged(t *testing.T) {
	implementation := adapter.NewMemory(adapter.Config{Clock: &fixedClock{}, IDs: &fixedIDs{}})
	session, err := implementation.Open(context.Background(), adapter.OpenRequest{
		SessionID: "plain", Participant: protocol.Participant{ID: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	_, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{
		SessionID: "plain", Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.Next(t, stream, time.Second)
	adaptertest.Next(t, stream, time.Second)
	call := adaptertest.Next(t, stream, time.Second)
	gate := adaptertest.Next(t, stream, time.Second)
	if call.Type != protocol.TypeActionCallRequested || gate.Type != protocol.TypeActionPermissionRequested {
		t.Fatalf("got %s then %s, want the unchanged harness-owned script", call.Type, gate.Type)
	}
	var payload protocol.ActionCallPayload
	if err := call.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.InteractionID != "" {
		t.Fatal("a harness-owned call is not an interaction")
	}
}

func secondProvidedTool() protocol.ToolDefinition {
	return protocol.ToolDefinition{
		Name: "annotate", Description: "A second tool the control layer executes.",
		InputSchema:    json.RawMessage(`{"type":"object","properties":{"operation":{"type":"string"}}}`),
		ExecutionOwner: "user",
	}
}

func TestEveryListedToolIsSelectableAndReachable(t *testing.T) {
	lister, ok := openProviding(t, providedTool(), secondProvidedTool()).(adapter.ToolLister)
	if !ok {
		t.Fatal("the reference session lists tools")
	}
	listed, err := lister.Tools(context.Background(), protocol.ToolsListRequest{SessionID: "control-tools"})
	if err != nil {
		t.Fatal(err)
	}
	var provided []string
	for _, tool := range listed.Tools.Tools {
		if tool.ExecutionOwner == "user" {
			provided = append(provided, tool.Name)
		}
	}
	if !slices.Equal(provided, []string{providedTool().Name, secondProvidedTool().Name}) {
		t.Fatalf("the session lists %v as control-owned, want both provided tools", provided)
	}
	for _, name := range provided {
		session := openProviding(t, providedTool(), secondProvidedTool())
		_, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{
			SessionID:  "control-tools",
			Messages:   []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
			ToolChoice: json.RawMessage(`{"allowed":["` + name + `"]}`),
		})
		if err != nil {
			t.Fatalf("tool_choice naming the listed tool %q was refused: %v", name, err)
		}
		adaptertest.Next(t, stream, time.Second)
		adaptertest.Next(t, stream, time.Second)
		call := adaptertest.Next(t, stream, time.Second)
		if call.Type != protocol.TypeActionCallRequested {
			t.Fatalf("got %s, want a call for %q", call.Type, name)
		}
		var payload protocol.ActionCallPayload
		if err := call.DecodePayload(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Name != name {
			t.Fatalf("the run called %q, but the policy named %q", payload.Name, name)
		}
	}
}

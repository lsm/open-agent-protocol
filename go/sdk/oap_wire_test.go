package sdk

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func oapFakeReply(request *frame, kind string, payload any) *frame {
	reply := oapHostFrame(request.Profile, kind, payload)
	reply.InReplyTo = request.ID
	return reply
}

func oapHostFrame(profile, kind string, payload any) *frame {
	return &frame{Protocol: oapProtocol, Version: oapVersion, Profile: profile,
		Type: kind, ID: newULID(), Payload: mustMarshal(payload)}
}

func runOAPHost(scenario string) {
	reader := newFrameReader(os.Stdin)
	providedTool, openSettings := "", ""
	for {
		request, err := nextTestFrame(reader)
		if err != nil {
			return
		}
		if scenario == scenarioExitMidRequest && request.Type != "protocol.initialize.request" && request.Type != "capabilities.request" {
			os.Exit(0)
		}
		if request.Profile == oapAgent && request.Type != "protocol.initialize.request" && request.Type != "capabilities.request" && request.CapabilityRevision != "fake-rev-1" {
			fakeEmit(oapFakeReply(request, "error.response", map[string]any{"error": map[string]any{"code": "stale_capabilities", "message": "missing capability revision"}}))
			continue
		}
		switch request.Type {
		case "protocol.initialize.request":
			switch scenario {
			case scenarioSilent:
				continue
			case scenarioHandshakeError:
				fakeEmit(oapFakeReply(request, "error.response", map[string]any{"error": map[string]any{"code": "startup_failed", "message": "runtime could not start"}}))
				continue
			}
			version := "0.1"
			if scenario == scenarioBadVersion {
				version = "9.9"
			}
			fakeEmit(oapFakeReply(request, "protocol.initialize.response", map[string]any{
				"protocol_version": version, "profile": oapAgent, "endpoint": map[string]any{"id": "fake"},
			}))
		case "capabilities.request":
			response := oapFakeReply(request, "capabilities.response", map[string]any{"endpoint": map[string]any{"id": "oapx.agent"},
				"features": map[string]any{"action.tools.provide": map[string]any{"level": "native"}},
				"sources":  []map[string]any{{"id": "attached-files", "kind": "process"}, {"id": "oapx", "kind": "native"}}})
			response.CapabilityRevision = "fake-rev-1"
			fakeEmit(response)
			switch scenario {
			case scenarioExitAfterReady:
				os.Exit(0)
			case scenarioIgnoreStdin:
				blockForever()
			}
		case "provider.models.list.request":
			if selected, ok := selectedCatalogModel(); ok {
				fakeEmit(oapFakeReply(request, "provider.models.list.response", map[string]any{"models": []map[string]any{selected}}))
				break
			}
			fakeEmit(oapFakeReply(request, "provider.models.list.response", map[string]any{"models": []map[string]any{{
				"model_ref": "fixture/other:test@ok", "model_id": "ok", "provider_id": "fixture", "wire": "other",
				"auth_status": "authenticated", "lifecycle": "stable", "source": "fallback",
				"capabilities":     []string{"chat", "streaming"},
				"cost":             map[string]any{"input": 3, "output": 15, "cache_read": 0.3, "cache_write": 3.75},
				"input_modalities": []string{"text", "image"}, "reasoning_levels": []string{"low", "medium", "high"},
				"release_date": "2025-09-29", "family": "claude-sonnet",
			}}, "catalog": map[string]any{"observed_at_ms": 1_759_100_000_000, "complete": true}}))
		case "inference.create.request":
			response := oapFakeReply(request, "inference.create.response", map[string]any{"accepted": true})
			response.InferenceID = "inference-1"
			fakeEmit(response)
			if request.payload().str("model_ref") == "fixture/other:test@silent" {
				continue
			}
			if request.payload().str("model_ref") == "fixture/other:test@parts" {
				completed := oapHostFrame(oapProvider, "inference.completed", map[string]any{
					"message": map[string]any{"role": "assistant", "content": []map[string]any{
						{"type": "text", "text": "before "},
						{"type": "reasoning", "reasoning": "thought", "carry": "sig"},
						{"type": "image", "image": map[string]any{"url": "https://example.invalid/image.png"}},
						{"type": "tool_call", "tool_call_id": "call-1", "name": "weather", "arguments_json": map[string]any{"city": "SF"}, "carry": "opaque"},
					}}, "stop_reason": "tool_use",
				})
				completed.InferenceID = "inference-1"
				fakeEmit(completed)
				continue
			}
			if request.payload().str("model_ref") == "fixture/other:test@auth-once" {
				failure := oapHostFrame(oapProvider, "inference.failed", map[string]any{"error": map[string]any{
					"code": "credential_missing", "message": "login required",
				}})
				failure.InferenceID = "inference-1"
				fakeEmit(failure)
				continue
			}
			for _, item := range []struct {
				kind    string
				payload any
			}{
				{"inference.started", map[string]any{"model_ref": "fixture/other:test@ok"}},
				{"inference.part.started", map[string]any{"part_index": 0, "part_kind": "text"}},
				{"inference.part.delta", map[string]any{"part_index": 0, "delta": "hello"}},
				{"inference.completed", map[string]any{"message": map[string]any{"role": "assistant", "content": "hello"}, "stop_reason": "stop"}},
			} {
				event := oapHostFrame(oapProvider, item.kind, item.payload)
				event.InferenceID = "inference-1"
				fakeEmit(event)
			}
		case "session.open.request":
			sessionID := request.payload().str("session_id")
			if sessionID == "" {
				sessionID = "session-1"
			}
			opening := request.payload()
			if tools, ok := opening["tools"].([]any); ok && len(tools) > 0 {
				first, _ := tools[0].(map[string]any)
				providedTool = fmt.Sprintf("%v owned by %v from %v", first["name"], first["execution_owner"], first["source"])
			}
			oapxSettings := opening.obj("metadata").obj("oapx")
			openSettings = fmt.Sprintf("reasoning=%v output=%v user_input=%v permission_mode=%v", opening["reasoning_level"], oapxSettings["output"], oapxSettings["user_input"], oapxSettings["permission_mode"])
			response := oapFakeReply(request, "session.open.response", map[string]any{"session_id": sessionID, "status": "idle"})
			response.SessionID = sessionID
			fakeEmit(response)
		case "session.model.switch.request":
			p := request.payload()
			response := oapFakeReply(request, "session.model.switch.response", map[string]any{"session_id": p.str("session_id"), "model_id": p.str("model_id")})
			response.SessionID = p.str("session_id")
			fakeEmit(response)
		case "models.request":
			fakeEmit(oapFakeReply(request, "models.response", map[string]any{
				"session_id": request.payload().str("session_id"), "models": []map[string]any{{"id": "fixture/other:test@ok", "default": true}},
			}))
		case "session.message.submit.request":
			sessionID := request.payload().str("session_id")
			selectedModel := request.payload().str("model_id")
			if selectedModel == "" {
				selectedModel = "fixture/other:test@ok"
			}
			response := oapFakeReply(request, "session.message.submit.response", map[string]any{"accepted": true, "run_id": "run-1", "model_id": selectedModel})
			response.SessionID = sessionID
			fakeEmit(response)
			if selectedModel == "fixture/other:test@tool" || selectedModel == "fixture/other:test@settings" {
				started := oapHostFrame(oapAgent, "run.started", map[string]any{"session_id": sessionID, "run_id": "run-1"})
				started.SessionID, started.RunID = sessionID, "run-1"
				fakeEmit(started)
				event := oapHostFrame(oapAgent, "action.call.requested", map[string]any{"session_id": sessionID, "run_id": "run-1",
					"tool_call_id": "call-1", "name": "lookup", "execution_owner": "sdk", "interaction_id": "interaction-1",
					"requested_by": "fake", "responded_by": "sdk", "arguments_json": map[string]any{"word": "oap"}})
				if selectedModel == "fixture/other:test@settings" {
					event = oapHostFrame(oapAgent, "run.completed", map[string]any{"session_id": sessionID, "run_id": "run-1",
						"final_response": map[string]any{"role": "assistant", "content": openSettings}, "stop_reason": "stop"})
				}
				event.SessionID, event.RunID = sessionID, "run-1"
				fakeEmit(event)
				continue
			}
			if selectedModel == "fixture/other:test@permission" {
				for _, item := range []struct {
					kind    string
					payload any
				}{
					{"run.started", map[string]any{"session_id": sessionID, "run_id": "run-1"}},
					{"action.call.requested", map[string]any{"session_id": sessionID, "run_id": "run-1", "tool_call_id": "call-1", "name": "Shell", "execution_owner": "fake", "arguments_json": map[string]any{"command": "ls"}}},
					{"action.permission.requested", map[string]any{"session_id": sessionID, "run_id": "run-1", "tool_call_id": "call-1", "interaction_id": "permission-1",
						"requested_by": "fake", "responded_by": "sdk", "title": "Run ls", "arguments_json": map[string]any{"command": "ls"},
						"choices": []map[string]any{{"id": "approve", "label": "Approve"}, {"id": "deny", "label": "Deny"}}}},
				} {
					event := oapHostFrame(oapAgent, item.kind, item.payload)
					event.SessionID, event.RunID = sessionID, "run-1"
					fakeEmit(event)
				}
				continue
			}
			if selectedModel == "fixture/other:test@auth-once" {
				started := oapHostFrame(oapAgent, "run.started", map[string]any{"session_id": sessionID, "run_id": "run-1", "model_id": selectedModel})
				started.SessionID, started.RunID = sessionID, "run-1"
				fakeEmit(started)
				failure := oapHostFrame(oapAgent, "run.failed", map[string]any{"session_id": sessionID, "run_id": "run-1", "error": map[string]any{
					"code": "credential_missing", "message": "login required",
				}})
				failure.SessionID, failure.RunID = sessionID, "run-1"
				fakeEmit(failure)
				continue
			}
			for _, item := range []struct {
				kind    string
				payload any
			}{
				{"run.started", map[string]any{"session_id": sessionID, "run_id": "run-1"}},
				{"content.delta", map[string]any{"session_id": sessionID, "run_id": "run-1", "part": map[string]any{"type": "text", "text": "agent"}}},
				{"run.completed", map[string]any{"session_id": sessionID, "run_id": "run-1", "final_response": map[string]any{"role": "assistant", "content": "agent"}, "model_id": "fixture/other:test@ok", "stop_reason": "stop"}},
			} {
				event := oapHostFrame(oapAgent, item.kind, item.payload)
				event.SessionID = sessionID
				event.RunID = "run-1"
				fakeEmit(event)
			}
		case "auth.providers.request":
			fakeEmit(oapFakeReply(request, "auth.providers.response", map[string]any{"providers": []map[string]any{
				{"id": "fixture", "name": "Fixture", "auth_kinds": []string{"api_key", "oauth"}, "auth_status": "login_required"},
				{"id": "weird", "name": "Weird", "auth_kinds": []string{"api_key", "passkey"}, "auth_status": "login_required", "override_host": "proxy.example"},
				{"id": "old", "name": "Old", "auth_status": "login_required"},
			}}))
		case "auth.login.start.request":
			fakeEmit(oapFakeReply(request, "auth.login.start.response", map[string]any{"flow_id": "flow-1"}))
			providerID := request.payload().str("provider_id")
			for index, eventPayload := range []map[string]any{
				{"flow_id": "flow-1", "provider_id": providerID, "kind": "url", "url": "https://example.invalid/auth"},
				{"flow_id": "flow-1", "provider_id": providerID, "kind": "progress", "message": "Login completed in browser"},
			} {
				if providerID == "manual" && index == 1 {
					eventPayload = map[string]any{"flow_id": "flow-1", "provider_id": providerID, "kind": "prompt", "prompt_id": "prompt-1", "message": "Enter code", "allow_empty": false}
				}
				event := oapHostFrame(oapAgent, "auth.login.event", eventPayload)
				event.Sequence = int64(index + 1)
				fakeEmit(event)
			}
			if providerID == "manual" {
				continue
			}
			terminal := oapHostFrame(oapAgent, "auth.login.completed", map[string]any{
				"flow_id": "flow-1", "provider_id": "fixture", "status": "success",
			})
			terminal.Sequence = 3
			fakeEmit(terminal)
		case "auth.login.reply.request":
			return
		case "auth.login.cancel.request":
			fakeEmit(oapFakeReply(request, "auth.login.cancel.response", map[string]any{"flow_id": "flow-1", "accepted": true}))
		case "action.call.resolve.request":
			p := request.payload()
			sessionID := p.str("session_id")
			response := oapFakeReply(request, "action.call.resolve.response", map[string]any{"interaction_id": p.str("interaction_id"),
				"session_id": sessionID, "run_id": "run-1", "tool_call_id": p.str("tool_call_id"), "accepted": true})
			response.SessionID = sessionID
			fakeEmit(response)
			said := fmt.Sprintf("%s said %v (error %v) as %s", providedTool, p["result"], p.obj("error")["message"], p.str("responded_by"))
			for _, item := range []struct {
				kind    string
				payload any
			}{
				{"action.call.started", map[string]any{"session_id": sessionID, "run_id": "run-1", "tool_call_id": "call-1", "name": "lookup"}},
				{"action.call.completed", map[string]any{"session_id": sessionID, "run_id": "run-1", "tool_call_id": "call-1", "name": "lookup", "result": p["result"]}},
				{"run.completed", map[string]any{"session_id": sessionID, "run_id": "run-1", "final_response": map[string]any{"role": "assistant", "content": said}, "stop_reason": "stop"}},
			} {
				event := oapHostFrame(oapAgent, item.kind, item.payload)
				event.SessionID, event.RunID = sessionID, "run-1"
				fakeEmit(event)
			}
		case "action.permission.resolve.request":
			p := request.payload()
			sessionID := p.str("session_id")
			response := oapFakeReply(request, "action.permission.resolve.response", map[string]any{"interaction_id": p.str("interaction_id"),
				"session_id": sessionID, "run_id": "run-1", "accepted": true})
			response.SessionID = sessionID
			fakeEmit(response)
			said := fmt.Sprintf("%s granted=%v by %s", p.str("choice_id"), p["granted"], p.str("responded_by"))
			for _, item := range []struct {
				kind    string
				payload any
			}{
				{"action.permission.resolved", map[string]any{"session_id": sessionID, "run_id": "run-1", "interaction_id": p.str("interaction_id"), "outcome": "resolved", "choice_id": p.str("choice_id"), "granted": p["granted"]}},
				{"run.completed", map[string]any{"session_id": sessionID, "run_id": "run-1", "final_response": map[string]any{"role": "assistant", "content": said}, "stop_reason": "stop"}},
			} {
				event := oapHostFrame(oapAgent, item.kind, item.payload)
				event.SessionID, event.RunID = sessionID, "run-1"
				fakeEmit(event)
			}
		case "run.cancel.request", "inference.cancel.request":
		default:
			fakeEmit(oapFakeReply(request, "error.response", map[string]any{"error": map[string]any{"code": "unsupported_feature", "message": request.Type}}))
		}
	}
}

func TestOAPAgentAnswersAPermissionWithTheHandlersDecisionAndDeniesWithoutOne(t *testing.T) {
	client := newTestClient(t, scenarioOAP)
	ctx := context.Background()
	var asked PermissionRequest
	response, err := client.Agent.Run(ctx, AgentRequest{ModelRef: "fixture/other:test@permission", Messages: []Message{UserMessage("hi")},
		Permit: func(_ context.Context, request PermissionRequest) bool {
			asked = request
			return request.ToolName == "Shell"
		}})
	if err != nil || response.Message.Text != "approve granted=true by sdk" {
		t.Fatalf("permitted: %v, %+v", err, response)
	}
	if asked.ToolCallID != "call-1" || asked.ToolName != "Shell" || asked.Title != "Run ls" || asked.ArgumentsJSON != `{"command":"ls"}` {
		t.Fatalf("the handler was not asked about the endpoint's call: %+v", asked)
	}
	response, err = client.Agent.Run(ctx, AgentRequest{ModelRef: "fixture/other:test@permission", Messages: []Message{UserMessage("hi")}})
	if err != nil || response.Message.Text != "deny granted=false by sdk" {
		t.Fatalf("no handler: %v, %+v", err, response)
	}
	response, err = client.Agent.Run(ctx, AgentRequest{ModelRef: "fixture/other:test@settings", Messages: []Message{UserMessage("hi")},
		Permit: func(context.Context, PermissionRequest) bool { return false }})
	if err != nil || response.Message.Text != "reasoning=<nil> output=<nil> user_input=false permission_mode=ask" {
		t.Fatalf("a handler did not open the loop in ask mode: %v, %+v", err, response)
	}
}

func TestOAPManualAuthNeverSendsAnAnswer(t *testing.T) {
	client := newTestClient(t, scenarioOAP)
	err := client.Auth.Login(context.Background(), "manual", LoginHandlers{})
	var auth *AuthError
	if !errors.As(err, &auth) || auth.Code != "auth_input_unavailable" {
		t.Fatalf("manual OAP login = %v", err)
	}
}

func TestOAPCombinedFakeHost(t *testing.T) {
	client := newTestClient(t, scenarioOAP)
	ctx := context.Background()
	models, err := client.Models.List(ctx, ListModelsRequest{})
	if err != nil || len(models.Models) != 1 {
		t.Fatalf("models: %v, %+v", err, models)
	}
	if models.Models[0].Source == nil || *models.Models[0].Source != SourceStaticFallback {
		t.Fatalf("fallback source was not normalized: %v", models.Models[0].Source)
	}
	response, err := client.Provider.Complete(ctx, CompletionRequest{ModelRef: models.Models[0].ModelRef, Messages: []Message{UserMessage("hi")}})
	if err != nil || response.Message.Text != "hello" {
		t.Fatalf("provider: %v, %+v", err, response)
	}
	sessionID, err := client.Agent.OpenSession(ctx, "session-1")
	if err != nil || sessionID != "session-1" {
		t.Fatalf("open: %v, %q", err, sessionID)
	}
	_, err = client.Agent.SwitchModel(ctx, sessionID, models.Models[0].ModelRef)
	if err != nil {
		t.Fatal(err)
	}
	available, _, err := client.Agent.ListSessionModels(ctx, sessionID)
	if err != nil || len(available) != 1 {
		t.Fatalf("agent models: %v, %+v", err, available)
	}
	response, err = client.Agent.Run(ctx, AgentRequest{Messages: []Message{UserMessage("hi")}, Options: &RunOptions{SessionID: sessionID}})
	if err != nil || response.Message.Text != "agent" {
		t.Fatalf("agent: %v, %+v", err, response)
	}
	var invoked ToolInvocation
	response, err = client.Agent.Run(ctx, AgentRequest{ModelRef: "fixture/other:test@tool", Messages: []Message{UserMessage("hi")},
		Tools: []Tool{{Name: "lookup", ParametersSchemaJSON: "{}", Execute: func(_ context.Context, call ToolInvocation) (string, error) {
			invoked = call
			return "open agent protocol", nil
		}}}})
	if err != nil || response.Message.Text != "lookup owned by sdk from oapx said open agent protocol (error <nil>) as sdk" {
		t.Fatalf("provided tool: %v, %+v", err, response)
	}
	if invoked.ToolCallID != "call-1" || invoked.ToolName != "lookup" || invoked.ArgumentsJSON != `{"word":"oap"}` {
		t.Fatalf("the tool was not called with the endpoint's call: %+v", invoked)
	}
	response, err = client.Agent.Run(ctx, AgentRequest{ModelRef: "fixture/other:test@settings", Messages: []Message{UserMessage("hi")},
		Options: &RunOptions{MaxTokens: MaxTokens(10), ReasoningEffort: ReasoningHigh}})
	if err != nil || response.Message.Text != "reasoning=high output=10 user_input=false permission_mode=<nil>" {
		t.Fatalf("agent settings did not reach the open: %v, %+v", err, response)
	}
	for _, options := range []*RunOptions{{MaxTokens: MaxTokens(0)}, {ReasoningEffort: ReasoningMinimal}} {
		_, err = client.Agent.Run(ctx, AgentRequest{ModelRef: "fixture/other:test@settings", Messages: []Message{UserMessage("hi")}, Options: options})
		var refused *ProtocolError
		if !errors.As(err, &refused) {
			t.Fatalf("expected %+v refused before the open, got %v", options, err)
		}
	}
	temperature := 0.5
	_, err = client.Agent.Run(ctx, AgentRequest{ModelRef: models.Models[0].ModelRef,
		Messages: []Message{UserMessage("hi")}, Options: &RunOptions{Temperature: &temperature}})
	var protocol *ProtocolError
	if !errors.As(err, &protocol) || protocol.Code != "unsupported_feature" {
		t.Fatalf("expected explicit temperature refusal, got %v", err)
	}
	_, err = client.Agent.AttachProvider(ctx, sessionID, ProviderAttachment{ID: "alias", ProviderID: "fixture"})
	var stream *StreamError
	if !errors.As(err, &stream) || stream.Code != "unsupported_feature" {
		t.Fatalf("expected explicit attachment refusal, got %v", err)
	}
	_, err = client.Provider.Complete(ctx, CompletionRequest{ModelRef: "fixture/other:test@auth-once", Messages: []Message{UserMessage("hi")}})
	var auth *AuthRequiredError
	if !errors.As(err, &auth) || auth.ProviderID != "fixture" {
		t.Fatalf("expected typed provider auth failure, got %v", err)
	}
	structured, err := client.Provider.Complete(ctx, CompletionRequest{ModelRef: "fixture/other:test@parts", Messages: []Message{UserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	parts := structured.Message.Parts
	if structured.Message.Text != "before " || structured.StopReason != "tool_use" || len(parts) != 4 ||
		parts[1].Type != PartThinking || parts[1].Thinking != "thought" || parts[1].ThinkingSignature != "sig" ||
		parts[2].Type != PartImage || parts[2].ImageURL != "https://example.invalid/image.png" ||
		parts[3].Type != PartToolCall || parts[3].ToolCallID != "call-1" || parts[3].Name != "weather" ||
		parts[3].ArgumentsJSON != `{"city":"SF"}` || parts[3].ToolCallCarry != "opaque" {
		t.Fatalf("structured OAP completion lost content: %+v", structured)
	}
	replayed, err := oapMessages([]Message{{Role: RoleAssistant, Parts: parts}})
	if err != nil {
		t.Fatalf("replay structured content: %v", err)
	}
	replayParts := replayed[0]["content"].([]map[string]any)
	if replayParts[1]["type"] != "reasoning" || replayParts[1]["carry"] != "sig" ||
		replayParts[2]["image"].(map[string]any)["url"] != "https://example.invalid/image.png" ||
		replayParts[3]["carry"] != "opaque" {
		t.Fatalf("structured OAP content did not round-trip: %+v", replayParts)
	}
	_, err = client.Agent.Run(ctx, AgentRequest{ModelRef: "fixture/other:test@auth-once", Messages: []Message{UserMessage("hi")}})
	if !errors.As(err, &auth) || auth.ProviderID != "fixture" {
		t.Fatalf("expected typed agent auth failure, got %v", err)
	}
	providers, err := client.Auth.ListProviders(ctx)
	if err != nil || len(providers) != 3 {
		t.Fatalf("auth providers: %v, %+v", err, providers)
	}
	var events []AuthEventType
	err = client.Auth.Login(ctx, "fixture", LoginHandlers{
		OnEvent: func(event AuthEvent) { events = append(events, event.Type) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0] != AuthEventURL || events[1] != AuthEventProgress || events[2] != AuthEventSuccess {
		t.Fatalf("auth events: %+v", events)
	}
}

func TestOAPCombinedHostWire(t *testing.T) {
	binary := "../../zig/zig-out/bin/oapx"
	if _, err := os.Stat(binary); err != nil {
		t.Skip("built oapx not available")
	}
	client, err := New(context.Background(), &Options{BinaryPath: binary})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	models, err := client.Models.List(context.Background(), ListModelsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(models.Models) == 0 {
		t.Fatal("OAP provider returned no models")
	}
	sessionID, err := client.Agent.OpenSession(context.Background(), "sdk-go-live-smoke")
	if err != nil {
		t.Fatal(err)
	}
	available, _, err := client.Agent.ListSessionModels(context.Background(), sessionID)
	if err != nil || len(available) == 0 {
		t.Fatalf("agent models: %v, %+v", err, available)
	}
	switched, err := client.Agent.SwitchModel(context.Background(), sessionID, available[0].ID)
	if err != nil || switched.ModelID != available[0].ID {
		t.Fatalf("switch: %v, %+v", err, switched)
	}
	providers, err := client.Auth.ListProviders(context.Background())
	if err != nil || len(providers) == 0 {
		t.Fatalf("auth providers: %v, %+v", err, providers)
	}
}

func selectedCatalogModel() (map[string]any, bool) {
	sourceShape, lifecycleShape, authShape := "", "", ""
	for _, entry := range os.Environ() {
		if value, ok := strings.CutPrefix(entry, "OAPX_TEST_CATALOG_SOURCE="); ok {
			sourceShape = value
		}
		if value, ok := strings.CutPrefix(entry, "OAPX_TEST_CATALOG_LIFECYCLE="); ok {
			lifecycleShape = value
		}
		if value, ok := strings.CutPrefix(entry, "OAPX_TEST_CATALOG_AUTH="); ok {
			authShape = value
		}
	}
	if sourceShape == "" && lifecycleShape == "" && authShape == "" {
		return nil, false
	}
	identity := firstNonEmpty(lifecycleShape, sourceShape, authShape)
	model := map[string]any{
		"model_ref": "fixture/other:selected@" + lifecycleShape + sourceShape + authShape,
		"model_id":  identity, "provider_id": "fixture",
		"wire":         "other",
		"capabilities": []string{"chat", "streaming"},
	}
	if authShape != "" {
		switch authShape {
		case "authenticated":
			model["auth_status"] = "authenticated"
		case "login_required":
			model["auth_status"] = "login_required"
		case "expired":
			model["auth_status"] = "expired"
		case "refreshing":
			model["auth_status"] = "refreshing"
		case "login_in_progress":
			model["auth_status"] = "login_in_progress"
		case "failed":
			model["auth_status"] = "failed"
		case "unknown":
			model["auth_status"] = "unknown"
		case "absent":
			delete(model, "auth_status")
		case "null":
			model["auth_status"] = nil
		case "number":
			model["auth_status"] = float64(7)
		case "invented":
			model["auth_status"] = "retired"
		default:
			fmt.Fprintf(os.Stderr, "unsupported OAPX_TEST_CATALOG_AUTH selector %q\n", authShape)
			os.Exit(3)
		}
		model["model_ref"] = "fixture/other:selected@" + identity
	}
	if lifecycleShape != "" {
		switch lifecycleShape {
		case "absent":
		case "stable", "preview", "deprecated":
			model["lifecycle"] = lifecycleShape
		case "null":
			model["lifecycle"] = nil
		case "empty":
			model["lifecycle"] = ""
		case "number", "wrong-type":
			model["lifecycle"] = float64(7)
		case "invented":
			model["lifecycle"] = "retired"
		}
	}
	if sourceShape != "" {
		switch sourceShape {
		case "absent":
		case "discovered", "fallback":
			model["source"] = sourceShape
		case "null":
			model["source"] = nil
		case "empty":
			model["source"] = ""
		case "wrong-type":
			model["source"] = float64(7)
		case "invented":
			model["source"] = "invented-source"
		case "shared-alias-dynamic":
			model["source"] = "dynamic"
		case "shared-alias-static-fallback":
			model["source"] = "static_fallback"
		}
	}
	return model, true
}

func TestOAPOpenRefusesWhatTheEndpointWouldIgnore(t *testing.T) {
	bare := &transport{agentEndpoint: "oapx.agent-control", agentFeatures: map[string]bool{}}
	for name, req := range map[string]AgentRequest{
		"tools":      {Tools: []Tool{{Name: "lookup", ParametersSchemaJSON: "{}"}}},
		"max tokens": {Options: &RunOptions{MaxTokens: MaxTokens(10)}},
	} {
		_, err := oapOpenPayload(bare, "session-1", req)
		var refused *ProtocolError
		if !errors.As(err, &refused) || refused.Code != "unsupported_feature" {
			t.Fatalf("%s: an endpoint that would ignore it should be refused client-side, got %v", name, err)
		}
	}
	payload, err := oapOpenPayload(bare, "session-1", AgentRequest{})
	if err != nil || payload["metadata"] != nil {
		t.Fatalf("an endpoint other than oapx.agent was sent oapx settings: %v, %+v", err, payload)
	}
}

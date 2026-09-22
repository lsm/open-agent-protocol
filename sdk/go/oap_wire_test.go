package makai

import (
	"context"
	"errors"
	"os"
	"testing"
)

func oapFakeReply(request *frame, kind string, payload any) *frame {
	reply := oapFrame(request.Profile, kind, payload)
	reply.InReplyTo = request.ID
	return reply
}

func runOAPHost() {
	reader := newFrameReader(os.Stdin)
	for {
		request, err := reader.next()
		if err != nil {
			return
		}
		switch request.Type {
		case "protocol.initialize.request":
			fakeEmit(oapFakeReply(request, "protocol.initialize.response", map[string]any{
				"protocol_version": "0.1", "profile": oapAgent, "endpoint": map[string]any{"id": "fake"},
			}))
		case "provider.models.list.request":
			fakeEmit(oapFakeReply(request, "provider.models.list.response", map[string]any{"models": []map[string]any{{
				"model_ref": "fixture/other:test@ok", "model_id": "ok", "provider_id": "fixture", "wire": "other",
				"auth_status": "authenticated", "lifecycle": "stable", "source": "fallback",
				"capabilities": []string{"chat", "streaming"},
			}}}))
		case "inference.create.request":
			response := oapFakeReply(request, "inference.create.response", map[string]any{"accepted": true})
			response.InferenceID = "inference-1"
			fakeEmit(response)
			if request.payload().str("model_ref") == "fixture/other:test@parts" {
				completed := oapFrame(oapProvider, "inference.completed", map[string]any{
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
				failure := oapFrame(oapProvider, "inference.failed", map[string]any{"error": map[string]any{
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
				event := oapFrame(oapProvider, item.kind, item.payload)
				event.InferenceID = "inference-1"
				fakeEmit(event)
			}
		case "session.open.request":
			sessionID := request.payload().str("session_id")
			if sessionID == "" {
				sessionID = "session-1"
			}
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
			if selectedModel == "fixture/other:test@auth-once" {
				started := oapFrame(oapAgent, "run.started", map[string]any{"session_id": sessionID, "run_id": "run-1", "model_id": selectedModel})
				started.SessionID, started.RunID = sessionID, "run-1"
				fakeEmit(started)
				failure := oapFrame(oapAgent, "run.failed", map[string]any{"session_id": sessionID, "run_id": "run-1", "error": map[string]any{
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
				event := oapFrame(oapAgent, item.kind, item.payload)
				event.SessionID = sessionID
				event.RunID = "run-1"
				fakeEmit(event)
			}
		case "auth.providers.request":
			fakeEmit(oapFakeReply(request, "auth.providers.response", map[string]any{"providers": []map[string]any{{
				"id": "fixture", "name": "Fixture", "auth_status": "login_required",
			}}}))
		case "auth.login.start.request":
			fakeEmit(oapFakeReply(request, "auth.login.start.response", map[string]any{"flow_id": "flow-1"}))
			for index, eventPayload := range []map[string]any{
				{"flow_id": "flow-1", "provider_id": "fixture", "kind": "url", "url": "https://example.invalid/auth"},
				{"flow_id": "flow-1", "provider_id": "fixture", "kind": "prompt", "prompt_id": "prompt-1", "message": "Enter code", "allow_empty": false},
			} {
				event := oapFrame(oapAgent, "auth.login.event", eventPayload)
				event.Sequence = int64(index + 1)
				fakeEmit(event)
			}
		case "auth.login.reply.request":
			if request.payload().str("answer") != "test-code" {
				return
			}
			fakeEmit(oapFakeReply(request, "auth.login.reply.response", map[string]any{
				"flow_id": "flow-1", "prompt_id": "prompt-1", "accepted": true,
			}))
			terminal := oapFrame(oapAgent, "auth.login.completed", map[string]any{
				"flow_id": "flow-1", "provider_id": "fixture", "status": "success",
			})
			terminal.Sequence = 3
			fakeEmit(terminal)
		case "auth.login.cancel.request":
			fakeEmit(oapFakeReply(request, "auth.login.cancel.response", map[string]any{"flow_id": "flow-1", "accepted": true}))
		case "run.cancel.request", "inference.cancel.request":
		default:
			fakeEmit(oapFakeReply(request, "error.response", map[string]any{"error": map[string]any{"code": "unsupported_feature", "message": request.Type}}))
		}
	}
}

func TestOAPCombinedFakeHost(t *testing.T) {
	client := newTestClient(t, scenarioOAP)
	ctx := context.Background()
	models, err := client.Models.List(ctx, ListModelsRequest{})
	if err != nil || len(models.Models) != 1 {
		t.Fatalf("models: %v, %+v", err, models)
	}
	if models.Models[0].Source != SourceStaticFallback {
		t.Fatalf("fallback source was not normalized: %q", models.Models[0].Source)
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
	_, err = client.Agent.Run(ctx, AgentRequest{ModelRef: models.Models[0].ModelRef,
		Messages: []Message{UserMessage("hi")}, Tools: []Tool{{Name: "tool", ParametersSchemaJSON: "{}"}}})
	var protocol *ProtocolError
	if !errors.As(err, &protocol) || protocol.Code != "unsupported_feature" {
		t.Fatalf("expected explicit tool refusal, got %v", err)
	}
	_, err = client.Agent.Run(ctx, AgentRequest{ModelRef: models.Models[0].ModelRef,
		Messages: []Message{UserMessage("hi")}, Options: &RunOptions{MaxTokens: MaxTokens(10)}})
	if !errors.As(err, &protocol) || protocol.Code != "unsupported_feature" {
		t.Fatalf("expected explicit agent option refusal, got %v", err)
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
	if err != nil || len(providers) != 1 {
		t.Fatalf("auth providers: %v, %+v", err, providers)
	}
	var events []AuthEventType
	err = client.Auth.Login(ctx, "fixture", LoginHandlers{
		OnEvent:  func(event AuthEvent) { events = append(events, event.Type) },
		OnPrompt: func(context.Context, AuthPrompt) (string, error) { return "test-code", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0] != AuthEventURL || events[1] != AuthEventPrompt || events[2] != AuthEventSuccess {
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

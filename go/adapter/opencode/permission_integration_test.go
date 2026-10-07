package opencode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func shellCallingProvider(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools    []json.RawMessage `json:"tools"`
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		answered := false
		for _, message := range request.Messages {
			answered = answered || message.Role == "tool"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(delta any, finish string) string {
			finishField := any(nil)
			if finish != "" {
				finishField = finish
			}
			encoded, _ := json.Marshal(map[string]any{"id": "c1", "object": "chat.completion.chunk", "created": 1, "model": "model", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finishField}}})
			return "data: " + string(encoded) + "\n\n"
		}
		if len(request.Tools) > 0 && !answered {
			arguments, _ := json.Marshal(map[string]string{"command": "echo hi", "description": "say hi"})
			_, _ = io.WriteString(w, chunk(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call_1", "type": "function", "function": map[string]any{"name": "shell", "arguments": string(arguments)}}}}, ""))
			_, _ = io.WriteString(w, chunk(map[string]any{}, "tool_calls"))
		} else {
			_, _ = io.WriteString(w, chunk(map[string]any{"role": "assistant", "content": "done"}, ""))
			_, _ = io.WriteString(w, chunk(map[string]any{}, "stop"))
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	return server.URL + "/v1"
}

func TestOpenCodeServerAsksPermissionForAToolCallAndRunsItOnceAllowed(t *testing.T) {
	events, asked := answerLivePermission(t, "once")
	completedTool, resolved := false, false
	for _, event := range events {
		completedTool = completedTool || (event.Type == protocol.TypeActionCallCompleted && event.ToolCallID == asked.ToolCallID)
		resolved = resolved || event.Type == protocol.TypeActionPermissionResolved
	}
	if !resolved || !completedTool || events[len(events)-1].Type != protocol.TypeRunCompleted {
		t.Fatalf("the allowed call did not run to a completed turn: %v", types(events))
	}
}

func TestOpenCodeServerEndsTheRunAsDeclinedWhenThePermissionIsRejected(t *testing.T) {
	events, _ := answerLivePermission(t, "reject")
	var failed protocol.RunFailedPayload
	last := events[len(events)-1]
	if last.Type != protocol.TypeRunFailed || last.DecodePayload(&failed) != nil || failed.Error.Code != "opencode_permission_declined" {
		t.Fatalf("a rejected call ended %s %s", last.Type, last.Payload)
	}
}

func answerLivePermission(t *testing.T, choice string) ([]protocol.Envelope, protocol.PermissionRequestedPayload) {
	t.Helper()
	if os.Getenv("OAP_OPENCODE_INTEGRATION") != "1" {
		t.Skip("set OAP_OPENCODE_INTEGRATION=1 and absolute OAP_OPENCODE_BIN to run the pinned server gate; optionally set OAP_OPENCODE_SHA256 (64 hex characters) for exact-artifact evidence")
	}
	binary := adaptertest.VerifiedBinary(t, "OAP_OPENCODE_BIN", "OAP_OPENCODE_SHA256", "an opencode "+PinnedTag+" binary")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	config := `{"provider":{"fixture":{"npm":"@ai-sdk/openai-compatible","name":"fixture","options":{"baseURL":` + strconv.Quote(shellCallingProvider(t)) + `,"apiKey":"fixture"},"models":{"model":{"name":"model"}}}},"model":"fixture/model","small_model":"fixture/model","permissions":[{"action":"shell","resource":"*","effect":"ask"}]}`
	endpoint := startPinnedServer(t, ctx, binary, []string{"OPENCODE_CONFIG_CONTENT=" + config})
	adapter, err := New(integrationConfig(endpoint, &native.ModelRef{ID: "model", ProviderID: "fixture"}))
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := adapter.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := adapter.Open(ctx, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "integration"}})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close(context.Background())
	response, stream, err := opened.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run it")}}}})
	if err != nil {
		t.Fatal(err)
	}
	var seen []protocol.Envelope
	var asked protocol.PermissionRequestedPayload
	for asked.InteractionID == "" {
		event := adaptertest.Next(t, stream, 60*time.Second)
		seen = append(seen, event)
		if event.Type == protocol.TypeActionPermissionRequested {
			if err := event.DecodePayload(&asked); err != nil {
				t.Fatal(err)
			}
		}
	}
	if asked.Title != "shell: echo hi" || asked.ToolCallID == "" {
		t.Fatalf("asked %+v", asked)
	}
	if err := opened.Resolve(ctx, base.InteractionResolution{RunID: response.RunID, RespondedBy: "integration", Permission: &protocol.PermissionResolveRequest{InteractionID: asked.InteractionID, RequestedBy: asked.RequestedBy, RespondedBy: "integration", SessionID: "session", RunID: response.RunID, ChoiceID: choice, Granted: choice != "reject"}}); err != nil {
		t.Fatalf("answer: %v", err)
	}
	events := append(seen, adaptertest.Drain(t, stream, 60*time.Second)...)
	adaptertest.AssertProtocolValidWithDescriptor(t, response, descriptor, events)
	return events, asked
}

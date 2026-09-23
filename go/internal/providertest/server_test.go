package providertest

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	openAIKey    = "test-openai-key"
	anthropicKey = "test-anthropic-key"
)

func TestRoutesAuthStreamsAndCaptures(t *testing.T) {
	server := New(t, Config{OpenAIKey: openAIKey, AnthropicKey: anthropicKey})
	tests := []struct {
		name, path, model string
		api               API
		headers           map[string]string
		wantEvents        []string
	}{
		{name: "responses", api: OpenAIResponses, path: ResponsesPath, model: "responses-model", headers: map[string]string{"Authorization": "Bearer " + openAIKey}, wantEvents: []string{"response.created", "response.output_item.added", "response.content_part.added", "response.output_text.delta", "response.output_text.done", "response.content_part.done", "response.output_item.done", "response.completed"}},
		{name: "messages", api: AnthropicMessages, path: MessagesPath, model: "messages-model", headers: map[string]string{"x-api-key": anthropicKey, "anthropic-version": "2023-06-01"}, wantEvents: []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}},
		{name: "chat", api: OpenAIChatCompletion, path: ChatCompletionPath, model: "chat-model", headers: map[string]string{"Authorization": "Bearer " + openAIKey}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server.Enqueue(test.api, Success)
			response := post(t, server.URL+test.path, test.model, test.headers)
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
				t.Fatalf("status=%d content-type=%q body=%s", response.StatusCode, response.Header.Get("Content-Type"), body)
			}
			if test.api == OpenAIChatCompletion {
				if !strings.Contains(string(body), `"content":"fixture response"`) || !strings.HasSuffix(string(body), "data: [DONE]\n\n") {
					t.Fatalf("chat stream: %s", body)
				}
			} else if got := EventNames(string(body)); strings.Join(got, ",") != strings.Join(test.wantEvents, ",") {
				t.Fatalf("events=%v want=%v", got, test.wantEvents)
			}
			requests := server.RequestsFor(test.api)
			if len(requests) != 1 || requests[0].Path != test.path || requests[0].Model != test.model {
				t.Fatalf("capture: %+v", requests)
			}
		})
	}
}

func TestToolStreams(t *testing.T) {
	server := New(t, Config{OpenAIKey: openAIKey, AnthropicKey: anthropicKey})
	for _, test := range []struct {
		api     API
		path    string
		headers map[string]string
		marker  string
	}{
		{OpenAIResponses, ResponsesPath, map[string]string{"Authorization": "Bearer " + openAIKey}, "response.function_call_arguments.done"},
		{AnthropicMessages, MessagesPath, map[string]string{"x-api-key": anthropicKey, "anthropic-version": "2023-06-01"}, "input_json_delta"},
		{OpenAIChatCompletion, ChatCompletionPath, map[string]string{"Authorization": "Bearer " + openAIKey}, `"finish_reason":"tool_calls"`},
	} {
		server.Enqueue(test.api, Tool)
		response := post(t, server.URL+test.path, "fixture-model", test.headers)
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK || !strings.Contains(string(body), test.marker) || !strings.Contains(string(body), FixtureToolName) || !strings.Contains(string(body), `\"value\":\"fixture\"`) {
			t.Fatalf("%s tool stream: status=%d body=%s", test.api, response.StatusCode, body)
		}
	}
}

func TestScriptedToolCallsStreamTheirOwnIDNameAndArguments(t *testing.T) {
	server := New(t, Config{OpenAIKey: openAIKey, AnthropicKey: anthropicKey})
	for _, test := range []struct {
		api     API
		path    string
		headers map[string]string
	}{
		{OpenAIResponses, ResponsesPath, map[string]string{"Authorization": "Bearer " + openAIKey}},
		{AnthropicMessages, MessagesPath, map[string]string{"x-api-key": anthropicKey, "anthropic-version": "2023-06-01"}},
		{OpenAIChatCompletion, ChatCompletionPath, map[string]string{"Authorization": "Bearer " + openAIKey}},
	} {
		server.EnqueueToolCall(test.api, ToolCall{ID: "scripted_id", Name: "Read", Arguments: `{"file_path":"/w/a \"b\".md"}`})
		response := post(t, server.URL+test.path, "fixture-model", test.headers)
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		for _, want := range []string{`"scripted_id"`, `"name":"Read"`, `\"file_path\":\"/w/a \\\"b\\\".md\"`} {
			if response.StatusCode != http.StatusOK || !strings.Contains(string(body), want) {
				t.Fatalf("%s scripted tool stream lacks %s: status=%d body=%s", test.api, want, response.StatusCode, body)
			}
		}
		if strings.Contains(string(body), FixtureToolName) {
			t.Fatalf("%s scripted tool stream still names the fixture tool: %s", test.api, body)
		}
	}
}

func TestErrorAndScenarioQueue(t *testing.T) {
	server := New(t, Config{OpenAIKey: openAIKey})
	server.Enqueue(OpenAIResponses, Error, Success)
	first := post(t, server.URL+ResponsesPath, "model", map[string]string{"Authorization": "Bearer " + openAIKey})
	firstBody, _ := io.ReadAll(first.Body)
	_ = first.Body.Close()
	if first.StatusCode != http.StatusTooManyRequests || first.Header.Get("Retry-After") != "1" || !strings.Contains(string(firstBody), "rate_limit_error") {
		t.Fatalf("error response: status=%d body=%s", first.StatusCode, firstBody)
	}
	second := post(t, server.URL+ResponsesPath, "model", map[string]string{"Authorization": "Bearer " + openAIKey})
	_ = second.Body.Close()
	if second.StatusCode != http.StatusOK || len(server.RequestsFor(OpenAIResponses)) != 2 {
		t.Fatalf("queued response status=%d requests=%d", second.StatusCode, len(server.RequestsFor(OpenAIResponses)))
	}
}

func TestMalformedAfterOpeningFrame(t *testing.T) {
	server := New(t, Config{OpenAIKey: openAIKey})
	server.Enqueue(OpenAIResponses, Malformed)
	response := post(t, server.URL+ResponsesPath, "model", map[string]string{"Authorization": "Bearer " + openAIKey})
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	blocks := strings.Split(strings.TrimSpace(string(body)), "\n\n")
	if len(blocks) != 2 || !strings.Contains(blocks[0], "response.created") || !strings.Contains(blocks[1], `data: {"type":`) {
		t.Fatalf("malformed stream: %q", body)
	}
	var value any
	payload := strings.TrimPrefix(blocks[1], "data: ")
	if json.Unmarshal([]byte(payload), &value) == nil {
		t.Fatalf("malformed payload decoded: %q", payload)
	}
}

func TestSlowReleaseAndCancellation(t *testing.T) {
	t.Run("release", func(t *testing.T) {
		server := New(t, Config{OpenAIKey: openAIKey})
		server.Enqueue(OpenAIResponses, Slow)
		response := post(t, server.URL+ResponsesPath, "model", map[string]string{"Authorization": "Bearer " + openAIKey})
		reader := bufio.NewReader(response.Body)
		first, err := reader.ReadString('\n')
		if err != nil || first != "event: response.created\n" {
			t.Fatalf("first frame=%q err=%v", first, err)
		}
		done := make(chan []byte, 1)
		go func() {
			body, _ := io.ReadAll(reader)
			done <- body
		}()
		select {
		case <-done:
			t.Fatal("slow stream completed before release")
		case <-time.After(20 * time.Millisecond):
		}
		server.Release(OpenAIResponses)
		select {
		case rest := <-done:
			if !strings.Contains(string(rest), "response.completed") {
				t.Fatalf("released stream: %s", rest)
			}
		case <-time.After(time.Second):
			t.Fatal("slow stream did not complete")
		}
		_ = response.Body.Close()
	})

	t.Run("context cancellation", func(t *testing.T) {
		server := New(t, Config{OpenAIKey: openAIKey})
		server.Enqueue(OpenAIResponses, Slow)
		ctx, cancel := context.WithCancel(context.Background())
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+ResponsesPath, strings.NewReader(`{"model":"model","stream":true}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+openAIKey)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		_, _ = io.ReadAll(response.Body)
		_ = response.Body.Close()
	})
}

func TestRejectsInvalidRequestsAndClonesCaptures(t *testing.T) {
	server := New(t, Config{OpenAIKey: openAIKey})
	for _, test := range []struct {
		name, path, method, body, auth string
		want                           int
	}{
		{"unknown path", "/unknown", http.MethodPost, `{}`, "", http.StatusNotFound},
		{"wrong method", ResponsesPath, http.MethodGet, `{}`, "Bearer " + openAIKey, http.StatusMethodNotAllowed},
		{"wrong auth", ResponsesPath, http.MethodPost, `{"model":"m","stream":true}`, "Bearer wrong", http.StatusUnauthorized},
		{"bad json", ResponsesPath, http.MethodPost, `{`, "Bearer " + openAIKey, http.StatusBadRequest},
		{"stream false", ResponsesPath, http.MethodPost, `{"model":"m"}`, "Bearer " + openAIKey, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, _ := http.NewRequest(test.method, server.URL+test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			if test.auth != "" {
				request.Header.Set("Authorization", test.auth)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != test.want {
				t.Fatalf("status=%d want=%d", response.StatusCode, test.want)
			}
		})
	}
	captures := server.Requests()
	if len(captures) == 0 {
		t.Fatal("no routed captures")
	}
	captures[0].Header.Set("X-Mutated", "yes")
	captures[0].Body[0] = 'x'
	fresh := server.Requests()
	if fresh[0].Header.Get("X-Mutated") != "" || fresh[0].Body[0] == 'x' {
		t.Fatal("capture mutation reached server state")
	}
}

func post(t *testing.T, url, model string, headers map[string]string) *http.Response {
	t.Helper()
	body := `{"model":` + quote(model) + `,"stream":true}`
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func quote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

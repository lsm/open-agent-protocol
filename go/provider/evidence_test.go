package provider_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/internal/providertest"
	"github.com/lsm/open-agent-protocol/go/provider"
)

func TestZAIChinaCodingPlanPresets(t *testing.T) {
	presets := provider.ZAIChinaCodingPlan()
	if len(presets) != 4 {
		t.Fatalf("presets=%d", len(presets))
	}
	seen := map[string]bool{}
	for _, preset := range presets {
		if seen[preset.ID] || preset.BaseURL == "" || preset.Path == "" || preset.Model == "" || preset.SourceURL == "" || preset.Qualification == "" {
			t.Fatalf("invalid preset: %+v", preset)
		}
		seen[preset.ID] = true
	}
	presets[0].Model = "mutated"
	fresh, err := provider.ZAIPreset("zai-cn-responses-control")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Model != "glm-5.3" || fresh.EvidenceClass != provider.DocumentedControl {
		t.Fatalf("control preset: %+v", fresh)
	}
	if _, err := provider.ZAIPreset("missing"); err == nil {
		t.Fatal("unknown preset succeeded")
	}
}

func TestRunEvidenceAgainstProviderWires(t *testing.T) {
	server := providertest.New(t, providertest.Config{OpenAIKey: "fixture-openai", AnthropicKey: "fixture-anthropic"})
	for _, test := range []struct {
		name, id, baseURL, path, model, key string
		wire                                provider.Wire
		api                                 providertest.API
		want                                string
	}{
		{"responses", "responses", server.OpenAIBaseURL(), "/responses", "responses-model", "fixture-openai", provider.OpenAIResponses, providertest.OpenAIResponses, "response.completed"},
		{"messages", "messages", server.AnthropicBaseURL(), "/v1/messages", "messages-model", "fixture-anthropic", provider.AnthropicMessages, providertest.AnthropicMessages, "message_stop"},
		{"chat", "chat", server.OpenAIBaseURL(), "/chat/completions", "chat-model", "fixture-openai", provider.OpenAIChat, providertest.OpenAIChatCompletion, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			server.Enqueue(test.api, providertest.Success)
			preset := provider.Preset{ID: test.id, Wire: test.wire, BaseURL: test.baseURL, Path: test.path, Model: test.model}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result, err := provider.RunEvidence(ctx, http.DefaultClient, preset, test.key)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Completed || result.StatusCode != http.StatusOK || result.Model != test.model {
				t.Fatalf("result: %+v", result)
			}
			if test.want != "" && !contains(result.Events, test.want) {
				t.Fatalf("events=%v lack %q", result.Events, test.want)
			}
			requests := server.RequestsFor(test.api)
			request := requests[len(requests)-1]
			if request.Model != test.model || !strings.Contains(string(request.Body), `"stream":true`) {
				t.Fatalf("request: %+v", request)
			}
		})
	}
}

func TestRunEvidenceRejectsUnsafeOrInvalidResults(t *testing.T) {
	server := providertest.New(t, providertest.Config{OpenAIKey: "fixture-key"})
	base := provider.Preset{ID: "fixture", Wire: provider.OpenAIResponses, BaseURL: server.OpenAIBaseURL(), Path: "/responses", Model: "model"}
	if _, err := provider.RunEvidence(context.Background(), nil, base, ""); err == nil {
		t.Fatal("empty credential succeeded")
	}
	invalid := base
	invalid.BaseURL = "://invalid"
	if _, err := provider.RunEvidence(context.Background(), nil, invalid, "fixture-key"); err == nil {
		t.Fatal("invalid base URL succeeded")
	}
	server.Enqueue(providertest.OpenAIResponses, providertest.Error, providertest.Malformed)
	if result, err := provider.RunEvidence(context.Background(), nil, base, "fixture-key"); err == nil || result.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("provider error result=%+v err=%v", result, err)
	}
	if _, err := provider.RunEvidence(context.Background(), nil, base, "fixture-key"); err == nil || !strings.Contains(err.Error(), "malformed SSE") {
		t.Fatalf("malformed stream err=%v", err)
	}
}

func TestRunEvidenceDoesNotForwardCredentialsAcrossRedirects(t *testing.T) {
	var reached bool
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	preset := provider.Preset{ID: "fixture", Wire: provider.OpenAIResponses, BaseURL: redirect.URL, Path: "/responses", Model: "model"}
	result, err := provider.RunEvidence(context.Background(), nil, preset, "fixture-key")
	if err == nil || result.StatusCode != http.StatusTemporaryRedirect || reached {
		t.Fatalf("result=%+v err=%v reached=%v", result, err, reached)
	}
}

func TestRunEvidenceHonorsContextCancellation(t *testing.T) {
	server := providertest.New(t, providertest.Config{OpenAIKey: "fixture-key"})
	server.Enqueue(providertest.OpenAIResponses, providertest.Slow)
	preset := provider.Preset{ID: "fixture", Wire: provider.OpenAIResponses, BaseURL: server.OpenAIBaseURL(), Path: "/responses", Model: "model"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := provider.RunEvidence(ctx, nil, preset, "fixture-key")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

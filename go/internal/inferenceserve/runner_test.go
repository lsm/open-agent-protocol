package inferenceserve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/provider"
	"github.com/lsm/open-agent-protocol/go/providercatalog"
)

func TestParseModelRefNamesTheProfilesWiresAndNothingElse(t *testing.T) {
	cases := map[string]ModelRef{
		"deepseek/openai-chat-completions@deepseek-v4-pro": {ProviderID: "deepseek", Wire: "openai-completions", ModelID: "deepseek-v4-pro"},
		"anthropic/anthropic-messages@claude":              {ProviderID: "anthropic", Wire: "anthropic-messages", ModelID: "claude"},
	}
	for ref, want := range cases {
		if got, ok := ParseModelRef(ref); !ok || got != want {
			t.Errorf("ParseModelRef(%q) = %+v, %v, want %+v", ref, got, ok, want)
		}
	}
	for _, ref := range []string{"noslash", "/openai-chat-completions@m", "deepseek/openai-completions@m", "deepseek/openai-chat-completions@", "deepseek/@m"} {
		if got, ok := ParseModelRef(ref); ok {
			t.Errorf("ParseModelRef(%q) = %+v, want refused", ref, got)
		}
	}
}

func runnerOutcome(t *testing.T, runner *Runner, ref string, messages string) []Envelope {
	t.Helper()
	return runnerOutcomeWith(t, runner, `{"model_ref":"`+ref+`","messages":`+messages+`}`)
}

func runnerOutcomeWith(t *testing.T, runner *Runner, payload string) []Envelope {
	t.Helper()
	var request CreateRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		t.Fatal(err)
	}
	var out []Envelope
	runner.Create(context.Background(), "c0", request, func(envelope Envelope) { out = append(out, envelope) })
	return out
}

func refusalCode(t *testing.T, envelope Envelope) string {
	t.Helper()
	held := payloadOf(t, envelope)
	if held["accepted"] != false {
		t.Fatalf("%s accepted, want a refusal", envelope.Type)
	}
	return held["error"].(map[string]any)["code"].(string)
}

func servingDeepSeek() *Runner {
	return &Runner{
		Catalog:     providercatalog.Catalog{Providers: []providercatalog.Provider{{ID: "deepseek", Auth: []string{"api_key"}, CredentialEnv: []string{"DEEPSEEK_API_KEY"}}}},
		Environment: []providercatalog.EnvironmentValue{{Name: "DEEPSEEK_API_KEY", Value: "sk-oap-test"}},
		Models:      []provider.ListedModel{{Model: provider.Model{ID: "deepseek-v4-pro", API: "openai-completions", Provider: "deepseek"}}},
		NewID:       func() string { return "i1" },
	}
}

func TestACreateIsRefusedBeforeAnythingIsAllocatedWhenItCannotBeServed(t *testing.T) {
	cases := []struct {
		name     string
		runner   *Runner
		ref      string
		messages string
		want     string
	}{
		{"a malformed model ref", servingDeepSeek(), "deepseek", `[]`, CodeInvalidRequest},
		{"a provider this endpoint does not serve", servingDeepSeek(), "openai/openai-chat-completions@gpt", `[]`, CodeModelNotFound},
		{"a message this endpoint does not carry yet", servingDeepSeek(), parityModelRef, `[{"role":"tool","content":"x"}]`, CodeUnsupportedFeature},
		{"a provider with no credential", func() *Runner { r := servingDeepSeek(); r.Environment = nil; return r }(), parityModelRef, `[]`, CodeCredentialMissing},
	}
	for _, c := range cases {
		out := runnerOutcome(t, c.runner, c.ref, c.messages)
		if len(out) != 1 || out[0].Type != "inference.create.response" || out[0].InferenceID != "" {
			t.Fatalf("%s: out = %v, want one unscoped create response", c.name, out)
		}
		if code := refusalCode(t, out[0]); code != c.want {
			t.Errorf("%s: code = %s, want %s", c.name, code, c.want)
		}
	}
}

func TestAModelTheProviderDoesNotServeIsAcceptedThenFailsAsOapxDoes(t *testing.T) {
	out := runnerOutcome(t, servingDeepSeek(), "deepseek/openai-chat-completions@no-such-model", `[{"role":"user","content":"hi"}]`)
	if len(out) != 2 || out[0].Type != "inference.create.response" || out[1].Type != "inference.failed" {
		t.Fatalf("types = %v", typesOf(out))
	}
	if code := payloadOf(t, out[1])["error"].(map[string]any)["code"]; code != CodeModelNotFound {
		t.Fatalf("code = %v, want model_not_found", code)
	}
}

func TestACreateNamingAFieldThisEndpointDoesNotForwardIsRefused(t *testing.T) {
	base := `"model_ref":"` + parityModelRef + `","messages":[{"role":"user","content":"hi"}]`
	for name, extra := range map[string]string{
		"a snapshot policy": `"include_snapshot":"on_part_end"`,
		"a unary inference": `"stream":false`,
		"top_p":             `"top_p":0.9`,
		"request headers":   `"headers":{"x-a":"b"}`,
		"an output schema":  `"output_schema":{"type":"object"}`,
		"reasoning options": `"reasoning":{"enabled":true}`,
	} {
		out := runnerOutcomeWith(t, servingDeepSeek(), "{"+base+","+extra+"}")
		if len(out) != 1 || refusalCode(t, out[0]) != CodeUnsupportedFeature {
			t.Errorf("%s: out = %v, want an unsupported_feature refusal", name, typesOf(out))
		}
	}
}

func TestBothWiresCarryTheRequestedTokenCapAndTemperature(t *testing.T) {
	for _, wire := range []string{"openai-completions", "anthropic-messages"} {
		var body map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		runner := servingDeepSeek()
		runner.Catalog.Providers[0].Endpoints = []providercatalog.Endpoint{{Wire: wire, BaseURL: server.URL}}
		runner.Models[0].Model.API = wire
		runner.Models[0].Model.BaseURL, runner.Models[0].Model.HasBaseURL = server.URL, true
		oapWire := map[string]string{"openai-completions": "openai-chat-completions", "anthropic-messages": "anthropic-messages"}[wire]
		runnerOutcomeWith(t, runner, `{"model_ref":"deepseek/`+oapWire+`@deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],"max_output_tokens":100,"temperature":0.25}`)
		server.Close()
		tokens := body["max_tokens"]
		if tokens == nil {
			tokens = body["max_completion_tokens"]
		}
		if tokens != 100.0 || body["temperature"] != 0.25 {
			t.Errorf("%s sent max_tokens=%v temperature=%v, want 100 and 0.25", wire, tokens, body["temperature"])
		}
	}
}

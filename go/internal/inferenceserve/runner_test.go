package inferenceserve

import (
	"context"
	"encoding/json"
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
	var request CreateRequest
	if err := json.Unmarshal([]byte(`{"model_ref":"`+ref+`","messages":`+messages+`}`), &request); err != nil {
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

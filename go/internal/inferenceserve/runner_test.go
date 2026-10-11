package inferenceserve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/provider"
	"github.com/lsm/open-agent-protocol/go/providercatalog"
)

func TestParseModelRefNamesTheProfilesWiresAndNothingElse(t *testing.T) {
	cases := map[string]ModelRef{
		"deepseek/openai-chat-completions@deepseek-v4-pro": {ProviderID: "deepseek", Wire: "openai-completions", ModelID: "deepseek-v4-pro"},
		"anthropic/anthropic-messages@claude":              {ProviderID: "anthropic", Wire: "anthropic-messages", ModelID: "claude"},
		"openai/openai-responses@gpt-5":                    {ProviderID: "openai", Wire: "openai-responses", ModelID: "gpt-5"},
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
		Catalog: providercatalog.Catalog{Providers: []providercatalog.Provider{
			{ID: "deepseek", Auth: []string{"api_key"}, CredentialEnv: []string{"DEEPSEEK_API_KEY"}, Wires: []string{"openai-completions"}},
			{ID: "ollama", Auth: []string{"none", "api_key"}, Wires: []string{"ollama"}},
		}},
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
		{"a provider served on another wire", servingDeepSeek(), "deepseek/anthropic-messages@deepseek-v4-pro", `[]`, CodeInvalidRequest},
		{"the openai-responses wire this layer does not serve yet", servingDeepSeek(), "openai/openai-responses@gpt-5", `[]`, CodeUnsupportedFeature},
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

func TestBothWiresCarryTheRequestedTokenCapTemperatureAndToolChoice(t *testing.T) {
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
		runnerOutcomeWith(t, runner, `{"model_ref":"deepseek/`+oapWire+`@deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],"max_output_tokens":100,"temperature":0.25,"tool_choice":{"function":"read"},"tools":[{"name":"read","input_schema":{"type":"object"}}]}`)
		server.Close()
		tokens := body["max_tokens"]
		if tokens == nil {
			tokens = body["max_completion_tokens"]
		}
		if tokens != 100.0 || body["temperature"] != 0.25 {
			t.Errorf("%s sent max_tokens=%v temperature=%v, want 100 and 0.25", wire, tokens, body["temperature"])
		}
		if choice, _ := json.Marshal(body["tool_choice"]); !strings.Contains(string(choice), `"read"`) {
			t.Errorf("%s sent tool_choice %s, want the named function", wire, choice)
		}
	}
}

func TestEverySystemAndDeveloperMessageJoinsTheSystemPromptAsOapxJoinsThem(t *testing.T) {
	var request CreateRequest
	if err := json.Unmarshal([]byte(`{"model_ref":"m","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"},{"role":"developer","content":[{"type":"text","text":"use tools"}]}]}`), &request); err != nil {
		t.Fatal(err)
	}
	conversation, err := conversationOf(request)
	if err != nil {
		t.Fatal(err)
	}
	if !conversation.HasSystem || conversation.SystemPrompt != "be brief\n\nuse tools" {
		t.Fatalf("system prompt = %q, want both joined by a blank line", conversation.SystemPrompt)
	}
	if len(conversation.Messages) != 1 || conversation.Messages[0].User == nil {
		t.Fatalf("messages = %+v, want only the user turn", conversation.Messages)
	}
}

func TestAProviderThisEndpointDoesNotServeIsRefusedAsOapxRefusesIt(t *testing.T) {
	unserved := func(edit func(*Runner)) *Runner {
		r := servingDeepSeek()
		r.Models = nil
		edit(r)
		return r
	}
	cases := []struct {
		name   string
		runner *Runner
		ref    string
		want   string
	}{
		{"a catalogued provider with no credential", unserved(func(r *Runner) { r.Environment = nil }), parityModelRef, CodeCredentialMissing},
		{"a catalogued provider whose key discovery refused", unserved(func(r *Runner) { r.KeyRefused = []string{"deepseek"} }), parityModelRef, CodeCredentialRejected},
		{"a catalogued provider whose models have not loaded", unserved(func(*Runner) {}), parityModelRef, CodeProviderUnavailable},
		{"a provider that needs no credential", unserved(func(*Runner) {}), "ollama/openai-chat-completions@m", CodeModelNotFound},
		{"a provider the catalog does not hold", unserved(func(*Runner) {}), "nope/openai-chat-completions@m", CodeModelNotFound},
	}
	for _, c := range cases {
		out := runnerOutcome(t, c.runner, c.ref, `[]`)
		if len(out) != 1 || refusalCode(t, out[0]) != c.want {
			t.Errorf("%s: out = %v, want %s", c.name, typesOf(out), c.want)
		}
	}
}

func TestAServedProviderWithNoCredentialIsAcceptedThenFailsAsOapxDoes(t *testing.T) {
	runner := servingDeepSeek()
	runner.Environment = nil
	out := runnerOutcome(t, runner, parityModelRef, `[{"role":"user","content":"hi"}]`)
	if len(out) != 2 || out[0].Type != "inference.create.response" || out[1].Type != "inference.failed" {
		t.Fatalf("types = %v", typesOf(out))
	}
	if code := payloadOf(t, out[1])["error"].(map[string]any)["code"]; code != CodeCredentialMissing {
		t.Fatalf("code = %v, want credential_missing", code)
	}
}

func TestTheFieldRefusalsFollowOapxsOrder(t *testing.T) {
	out := runnerOutcomeWith(t, servingDeepSeek(), `{"model_ref":"`+parityModelRef+`","messages":[{"role":"tool","content":"x"}],"top_p":0.5,"output_schema":{}}`)
	if len(out) != 1 {
		t.Fatalf("out = %v", typesOf(out))
	}
	if message := payloadOf(t, out[0])["error"].(map[string]any)["message"]; message != "this endpoint does not forward top_p" {
		t.Fatalf("message = %v, want top_p refused before the message and the schema", message)
	}
	out = runnerOutcomeWith(t, servingDeepSeek(), `{"model_ref":"`+parityModelRef+`","messages":[{"role":"tool","content":"x"}],"output_schema":{}}`)
	if message := payloadOf(t, out[0])["error"].(map[string]any)["message"]; message == "this endpoint does not forward a structured output schema" {
		t.Fatalf("message = %v, want the message refused before the schema", message)
	}
}

func TestAProviderRefusalIsDescribedAsOapxDescribesIt(t *testing.T) {
	completions := provider.Model{Provider: "deepseek", API: "openai-completions"}
	anthropic := provider.Model{Provider: "anthropic", API: provider.AnthropicWire}
	cases := []struct {
		model provider.Model
		body  string
		want  string
	}{
		{completions, `{"error":{"message":"bad key","type":"authentication_error"}}`, "deepseek request failed: HTTP 401 (authentication_error: bad key)"},
		{completions, `{"error":{"type":"overloaded"}}`, "deepseek request failed: HTTP 401 (overloaded)"},
		{completions, `{"error":"plain"}`, "deepseek request failed: HTTP 401 (plain)"},
		{completions, `{"error":null,"detail":"ignored"}`, "deepseek request failed: HTTP 401"},
		{completions, `{"detail":"from detail"}`, "deepseek request failed: HTTP 401 (from detail)"},
		{completions, `{"message":"from message"}`, "deepseek request failed: HTTP 401 (from message)"},
		{completions, `not json`, "deepseek request failed: HTTP 401"},
		{anthropic, `{"error":{"message":"bad key","type":"authentication_error"}}`, "anthropic request failed: HTTP 401 (check ANTHROPIC_API_KEY is valid) (authentication_error: bad key)"},
		{anthropic, `{"error":{"type":"overloaded"}}`, "anthropic request failed: HTTP 401 (check ANTHROPIC_API_KEY is valid)"},
	}
	for _, c := range cases {
		if got := httpFailure(c.model, 401, []byte(c.body)); got != c.want {
			t.Errorf("%s %s = %q, want %q", c.model.API, c.body, got, c.want)
		}
	}
}

func TestAMessageOapxRefusesIsRefusedInOapxsWords(t *testing.T) {
	cases := []struct {
		name     string
		messages string
		want     string
	}{
		{"a tool message of plain text", `[{"role":"tool","content":"x"}]`, refusalMisplacedPart},
		{"a tool message of text parts and no result", `[{"role":"tool","content":[{"type":"text","text":"x"}]}]`, refusalMisplacedPart},
		{"an image", `[{"role":"user","content":[{"type":"image","url":"https://x"}]}]`, refusalTextOnly},
		{"reasoning outside an assistant message", `[{"role":"user","content":[{"type":"reasoning","reasoning":"r"}]}]`, refusalMisplacedPart},
		{"a tool result in an assistant message", `[{"role":"assistant","content":[{"type":"tool_result","tool_call_id":"c","content":"r"}]}]`, refusalMisplacedPart},
		{"an image after a message only this layer cannot carry", `[{"role":"tool","content":[{"type":"tool_result","tool_call_id":"c","content":"r"}]},{"role":"user","content":[{"type":"image","url":"https://x"}]}]`, refusalTextOnly},
	}
	for _, c := range cases {
		out := runnerOutcome(t, servingDeepSeek(), parityModelRef, c.messages)
		if len(out) != 1 {
			t.Fatalf("%s: out = %v", c.name, typesOf(out))
		}
		if message := payloadOf(t, out[0])["error"].(map[string]any)["message"]; message != c.want {
			t.Errorf("%s: message = %v, want %q", c.name, message, c.want)
		}
	}
}

func TestAnAnthropicReplyWithNoEventsFailsWithTheErrorItsBodyCarries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	}))
	defer server.Close()
	runner := servingDeepSeek()
	runner.Catalog.Providers[0].Endpoints = []providercatalog.Endpoint{{Wire: "anthropic-messages", BaseURL: server.URL}}
	runner.Models[0].Model.API = "anthropic-messages"
	runner.Models[0].Model.BaseURL, runner.Models[0].Model.HasBaseURL = server.URL, true
	out := runnerOutcome(t, runner, "deepseek/anthropic-messages@deepseek-v4-pro", `[{"role":"user","content":"hi"}]`)
	last := out[len(out)-1]
	if last.Type != "inference.failed" || payloadOf(t, last)["error"].(map[string]any)["message"] != "Overloaded" {
		t.Fatalf("settled %s %v, want a failure naming the body's error", last.Type, payloadOf(t, last))
	}
}

func TestACredentialRefOrAMalformedToolChoiceIsRefused(t *testing.T) {
	base := `"model_ref":"` + parityModelRef + `","messages":[{"role":"user","content":"hi"}]`
	cases := []struct {
		name  string
		extra string
		want  string
	}{
		{"a credential_ref this endpoint holds no grant for", `"credential_ref":"cr1"`, CodeCredentialMissing},
		{"a tool_choice naming no mode", `"tool_choice":"sometimes"`, CodeInvalidRequest},
		{"a tool_choice object without a function", `"tool_choice":{}`, CodeInvalidRequest},
	}
	for _, c := range cases {
		out := runnerOutcomeWith(t, servingDeepSeek(), "{"+base+","+c.extra+"}")
		if len(out) != 1 || refusalCode(t, out[0]) != c.want {
			t.Errorf("%s: out = %v, want %s", c.name, typesOf(out), c.want)
		}
	}
}

func TestANullOutputSchemaOrReasoningIsAbsentRatherThanRefused(t *testing.T) {
	out := runnerOutcomeWith(t, servingDeepSeek(), `{"model_ref":"deepseek/openai-chat-completions@no-such-model","messages":[{"role":"user","content":"hi"}],"output_schema":null,"reasoning":null}`)
	if len(out) == 0 || out[0].Type != "inference.create.response" || payloadOf(t, out[0])["accepted"] != true {
		t.Fatalf("out = %v, want the create accepted", typesOf(out))
	}
}

func TestAnUnlistedModelFailsBeforeAMissingCredentialIsLookedFor(t *testing.T) {
	runner := servingDeepSeek()
	runner.Environment = nil
	out := runnerOutcome(t, runner, "deepseek/openai-chat-completions@no-such-model", `[{"role":"user","content":"hi"}]`)
	if code := payloadOf(t, out[len(out)-1])["error"].(map[string]any)["code"]; code != CodeModelNotFound {
		t.Fatalf("code = %v, want model_not_found, which oapx settles before any credential", code)
	}
}

func TestACancelledCreateSettlesAbortedAsOapxDoes(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"par\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	for _, when := range []string{"before the reply", "mid-stream"} {
		runner := servingDeepSeek()
		runner.Catalog.Providers[0].Endpoints = []providercatalog.Endpoint{{Wire: "openai-completions", BaseURL: server.URL}}
		runner.Models[0].Model.BaseURL, runner.Models[0].Model.HasBaseURL = server.URL, true
		ctx, cancel := context.WithCancel(context.Background())
		if when == "before the reply" {
			cancel()
		}
		var request CreateRequest
		json.Unmarshal([]byte(`{"model_ref":"`+parityModelRef+`","messages":[{"role":"user","content":"hi"}]}`), &request)
		var out []Envelope
		runner.Create(ctx, "c0", request, func(envelope Envelope) {
			out = append(out, envelope)
			if envelope.Type == "inference.part.delta" {
				cancel()
			}
		})
		cancel()
		last := out[len(out)-1]
		if last.Type != "inference.failed" || payloadOf(t, last)["error"].(map[string]any)["code"] != CodeAborted {
			t.Errorf("%s: settled %s %v, want aborted", when, last.Type, payloadOf(t, last))
		}
	}
}

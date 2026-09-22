package validation

import (
	"encoding/json"
	"strings"
	"testing"
)

func authEnvelope(typ, id, reply string, sequence uint64, payload any) map[string]any {
	e := map[string]any{
		"protocol": "open-agent-protocol", "version": "0.1",
		"profile": "open-agent-protocol.agent-control-core",
		"type":    typ, "id": id, "payload": payload,
	}
	if reply != "" {
		e["in_reply_to"] = reply
	}
	if sequence != 0 {
		e["sequence"] = sequence
	}
	return e
}

func authTrace() []map[string]any {
	return []map[string]any{
		authEnvelope("auth.providers.request", "providers-req", "", 0, map[string]any{}),
		authEnvelope("auth.providers.response", "providers-resp", "providers-req", 0, map[string]any{
			"providers": []map[string]any{{"id": "anthropic", "name": "Anthropic", "auth_status": "login_required"}},
		}),
		authEnvelope("auth.login.start.request", "start-req", "", 0, map[string]any{"provider_id": "anthropic"}),
		authEnvelope("auth.login.start.response", "start-resp", "start-req", 0, map[string]any{"flow_id": "flow-1"}),
		authEnvelope("auth.login.event", "url-ev", "", 1, map[string]any{
			"flow_id": "flow-1", "provider_id": "anthropic", "kind": "url", "url": "https://example.invalid/login",
		}),
		authEnvelope("auth.login.event", "prompt-ev", "", 2, map[string]any{
			"flow_id": "flow-1", "provider_id": "anthropic", "kind": "prompt", "prompt_id": "prompt-1", "message": "Code?", "allow_empty": false,
		}),
		authEnvelope("auth.login.reply.request", "reply-req", "", 0, map[string]any{
			"flow_id": "flow-1", "prompt_id": "prompt-1", "answer": "sensitive-test-value",
		}),
		authEnvelope("auth.login.reply.response", "reply-resp", "reply-req", 0, map[string]any{
			"flow_id": "flow-1", "prompt_id": "prompt-1", "accepted": true,
		}),
		authEnvelope("auth.login.completed", "terminal", "", 3, map[string]any{
			"flow_id": "flow-1", "provider_id": "anthropic", "status": "success",
		}),
	}
}

func validateAuthTrace(t *testing.T, trace []map[string]any) Result {
	t.Helper()
	data, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	return MustNew().ValidateBytes(data, t.Name())
}

func TestAuthFlowHasStandaloneStatusPromptReplyAndTerminal(t *testing.T) {
	if result := validateAuthTrace(t, authTrace()); !result.Valid() {
		t.Fatalf("valid auth flow diagnosed: %+v", result.Diagnostics)
	}
}

func TestAuthFlowRequiresStartBeforeEvents(t *testing.T) {
	trace := authTrace()
	trace[3], trace[4] = trace[4], trace[3]
	if result := validateAuthTrace(t, trace); !result.HasCode(CodeAuthFlowOrder) {
		t.Fatalf("event before start response not diagnosed: %+v", result.Diagnostics)
	}
}

func TestAuthFlowRequiresContiguousSequenceAndOneTerminal(t *testing.T) {
	trace := authTrace()
	trace[5]["sequence"] = uint64(4)
	if result := validateAuthTrace(t, trace); !result.HasCode(CodeSequenceGap) {
		t.Fatalf("auth sequence gap not diagnosed: %+v", result.Diagnostics)
	}

	trace = authTrace()
	trace = trace[:len(trace)-1]
	if result := validateAuthTrace(t, trace); !result.HasCode(CodeMissingAuthTerminal) {
		t.Fatalf("missing auth terminal not diagnosed: %+v", result.Diagnostics)
	}

	trace = authTrace()
	trace = append(trace, authEnvelope("auth.login.event", "late-ev", "", 4, map[string]any{
		"flow_id": "flow-1", "provider_id": "anthropic", "kind": "progress", "message": "too late",
	}))
	if result := validateAuthTrace(t, trace); !result.HasCode(CodeEventAfterTerminal) {
		t.Fatalf("event after auth terminal not diagnosed: %+v", result.Diagnostics)
	}
}

func TestAuthPromptMismatchNeverPrintsAnswer(t *testing.T) {
	trace := authTrace()
	trace[6]["payload"].(map[string]any)["prompt_id"] = "wrong-prompt"
	result := validateAuthTrace(t, trace)
	if !result.HasCode(CodeAuthPromptMismatch) {
		t.Fatalf("wrong prompt not diagnosed: %+v", result.Diagnostics)
	}
	for _, diagnostic := range result.Diagnostics {
		if strings.Contains(diagnostic.Error(), "sensitive-test-value") || strings.Contains(diagnostic.Actual, "sensitive-test-value") {
			t.Fatalf("prompt answer leaked into diagnostic: %+v", diagnostic)
		}
	}
}

func TestAuthReplySchemaAndDecodeDiagnosticsRedactAnswer(t *testing.T) {
	const secret = "SENSITIVE_OAUTH_CODE_8f7bc1"
	longAnswer := strings.Repeat(secret, 200)
	validJSON, err := json.Marshal(authEnvelope("auth.login.reply.request", "reply-overlong", "", 0, map[string]any{
		"flow_id": "flow-1", "prompt_id": "prompt-1", "answer": longAnswer,
	}))
	if err != nil {
		t.Fatal(err)
	}
	malformed := []byte(`{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"auth.login.reply.request","id":"bad","payload":{"flow_id":"f","prompt_id":"p","answer":"` + secret)
	escapedType := []byte(strings.Replace(string(validJSON), "auth.login.reply.request", `auth.login.reply\u002erequest`, 1))
	for name, input := range map[string][]byte{"overlong": validJSON, "escaped-type": escapedType, "malformed": malformed} {
		t.Run(name, func(t *testing.T) {
			result := MustNew().ValidateBytes(input, t.Name())
			if result.Valid() {
				t.Fatal("invalid auth reply was accepted")
			}
			for _, diagnostic := range result.Diagnostics {
				encoded, err := json.Marshal(diagnostic)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(encoded), secret) {
					t.Fatalf("auth answer leaked into diagnostic: %s", encoded)
				}
			}
		})
	}
}

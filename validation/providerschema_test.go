package validation_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/validation"
)

func providerSchema(t *testing.T) func(string) error {
	t.Helper()
	compiled, err := validation.CompileProviderSchema()
	if err != nil {
		t.Fatalf("compile provider schema: %v", err)
	}
	return func(raw string) error {
		var document any
		if err := json.Unmarshal([]byte(raw), &document); err != nil {
			t.Fatalf("decode fixture: %v", err)
		}
		return compiled.Validate(document)
	}
}

func envelope(kind, payload string) string {
	return `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"` + kind + `","id":"e1",` + payload + `}`
}

func TestProviderSchemaAdmitsEachEnvelope(t *testing.T) {
	validate := providerSchema(t)
	for _, testCase := range []struct{ name, raw string }{
		{"describe request", envelope("provider.describe.request", `"payload":{}`)},
		{"describe response", envelope("provider.describe.response", `"in_reply_to":"e0","capability_revision":"r1","payload":{"protocol_versions":["0.1"],"profile_revision":"draft-2026-09-17","providers":[{"id":"ollama","wire":"other","wire_id":"ollama-chat","framing":"ndjson","allows_anonymous":true,"credential_grant":"none"}]}`)},
		{"models list response", envelope("provider.models.list.response", `"in_reply_to":"e0","capability_revision":"r1","payload":{"models":[{"model_ref":"ollama/other:ollama-chat@llama3","model_id":"llama3","provider_id":"ollama","wire":"other","auth_status":"authenticated","source":"fallback"}]}`)},
		{"create accepted", envelope("inference.create.response", `"in_reply_to":"e0","inference_id":"i1","payload":{"accepted":true,"honoured":{"include_snapshot":"on_part_end"}}`)},
		{"create refused", envelope("inference.create.response", `"in_reply_to":"e0","payload":{"accepted":false,"error":{"code":"model_not_found","message":"no such provider"}}`)},
		{"tool call start", envelope("inference.part.started", `"inference_id":"i1","sequence":2,"payload":{"part_index":0,"part_kind":"tool_call","tool_call_id":"t1","name":"search"}`)},
		{"text start", envelope("inference.part.started", `"inference_id":"i1","sequence":2,"payload":{"part_index":0,"part_kind":"text"}`)},
		{"tool call end", envelope("inference.part.ended", `"inference_id":"i1","sequence":3,"payload":{"part_index":0,"part_kind":"tool_call","tool_call":{"tool_call_id":"t1","name":"search","arguments_json":{"q":"zig"}},"carry":"opaque"}`)},
		{"delta with partial snapshot", envelope("inference.part.delta", `"inference_id":"i1","sequence":4,"payload":{"part_index":0,"delta":"\"zig\"}","snapshot":[{"role":"assistant","content":[{"type":"tool_call","tool_call_id":"t1","name":"search","arguments_partial":"{\"q\":"}]}]}`)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := validate(testCase.raw); err != nil {
				t.Fatalf("rejected a conformant envelope: %v", err)
			}
		})
	}
}

func TestProviderSchemaRefusesWhatTheDraftForbids(t *testing.T) {
	validate := providerSchema(t)
	for _, testCase := range []struct{ name, raw, why string }{
		{"agent control profile", strings.Replace(envelope("provider.describe.request", `"payload":{}`), "model-provider-core", "agent-control-core", 1), "profile"},
		{"tool call start without identity", envelope("inference.part.started", `"inference_id":"i1","sequence":2,"payload":{"part_index":0,"part_kind":"tool_call"}`), "tool_call"},
		{"text start carrying identity", envelope("inference.part.started", `"inference_id":"i1","sequence":2,"payload":{"part_index":0,"part_kind":"text","tool_call_id":"t1","name":"search"}`), "text"},
		{"wire_id on a named wire", envelope("provider.describe.response", `"in_reply_to":"e0","capability_revision":"r1","payload":{"protocol_versions":["0.1"],"providers":[{"id":"openai","wire":"openai-responses","wire_id":"shadow","framing":"sse"}]}`), "wire_id"},
		{"refusal allocating an inference", envelope("inference.create.response", `"in_reply_to":"e0","inference_id":"i1","payload":{"accepted":false,"error":{"code":"model_not_found","message":"no"}}`), "inference_id"},
		{"acceptance without honoured", envelope("inference.create.response", `"in_reply_to":"e0","inference_id":"i1","payload":{"accepted":true}`), "honoured"},
		{"create request carrying an inference_id", envelope("inference.create.request", `"inference_id":"i1","payload":{"model_ref":"p/other:x@m","messages":[]}`), "inference_id"},
		{"both argument forms", envelope("inference.part.delta", `"inference_id":"i1","sequence":4,"payload":{"part_index":0,"delta":"x","snapshot":[{"role":"assistant","content":[{"type":"tool_call","tool_call_id":"t1","name":"s","arguments_json":{},"arguments_partial":"{"}]}]}`), "arguments"},
		{"partial arguments in a terminal", envelope("inference.completed", `"inference_id":"i1","sequence":9,"payload":{"stop_reason":"stop","message":{"role":"assistant","content":[{"type":"tool_call","tool_call_id":"t1","name":"s","arguments_partial":"{"}]}}`), "terminal"},
		{"action on a protocol error", envelope("inference.failed", `"inference_id":"i1","sequence":9,"payload":{"error":{"code":"rate_limited","message":"slow down","action":"retry"}}`), "action"},
		{"payload repeating the envelope scope", envelope("inference.started", `"inference_id":"i1","sequence":1,"payload":{"inference_id":"i1","model_ref":"p/other:x@m"}`), "inference_id"},
		{"acceptance with no envelope scope", envelope("inference.create.response", `"in_reply_to":"e0","payload":{"accepted":true,"honoured":{"include_snapshot":"never"}}`), "inference_id"},
		{"event without a sequence", envelope("inference.started", `"inference_id":"i1","payload":{"model_ref":"p/other:x@m"}`), "sequence"},
		{"unknown wire", envelope("provider.describe.response", `"in_reply_to":"e0","capability_revision":"r1","payload":{"protocol_versions":["0.1"],"providers":[{"id":"p","wire":"google-generative-ai","framing":"sse"}]}`), "wire"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := validate(testCase.raw); err == nil {
				t.Fatalf("admitted an envelope the draft forbids (%s)", testCase.why)
			}
		})
	}
}

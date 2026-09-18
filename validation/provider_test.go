package validation_test

import (
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/validation"
)

func providerTrace(t *testing.T, frames ...string) validation.Result {
	t.Helper()
	v, err := validation.NewProviderValidator()
	if err != nil {
		t.Fatalf("new provider validator: %v", err)
	}
	return v.Validate(strings.NewReader("["+strings.Join(frames, ",")+"]"), "trace.json")
}

func codes(result validation.Result) []string {
	found := make([]string, 0, len(result.Diagnostics))
	for _, d := range result.Diagnostics {
		found = append(found, d.Code)
	}
	return found
}

const (
	textEnded = `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.part.ended","id":"e2","inference_id":"i1","sequence":2,"payload":{"part_index":0,"part_kind":"text","text":"hello"}}`
	toolEnded = `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.part.ended","id":"e3","inference_id":"i1","sequence":3,"payload":{"part_index":1,"part_kind":"tool_call","tool_call":{"tool_call_id":"t1","name":"search","arguments_json":{"q":"zig"}}}}`
)

func completed(content string) string {
	return `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.completed","id":"e9","inference_id":"i1","sequence":9,"payload":{"stop_reason":"tool_use","message":{"role":"assistant","content":` + content + `}}}`
}

func TestProviderTerminalIsTheAssemblyOfItsParts(t *testing.T) {
	assembled := `[{"type":"text","text":"hello"},{"type":"tool_call","tool_call_id":"t1","name":"search","arguments_json":{"q":"zig"}}]`
	if diags := codes(providerTrace(t, textEnded, toolEnded, completed(assembled))); len(diags) != 0 {
		t.Fatalf("a terminal agreeing with its parts was rejected: %v", providerTrace(t, textEnded, toolEnded, completed(assembled)).Diagnostics)
	}
}

func TestProviderTerminalDisagreeingWithItsParts(t *testing.T) {
	for _, testCase := range []struct{ name, content string }{
		{"fewer parts", `[{"type":"text","text":"hello"}]`},
		{"different text", `[{"type":"text","text":"hell"},{"type":"tool_call","tool_call_id":"t1","name":"search","arguments_json":{"q":"zig"}}]`},
		{"different kind", `[{"type":"reasoning","reasoning":"hello"},{"type":"tool_call","tool_call_id":"t1","name":"search","arguments_json":{"q":"zig"}}]`},
		{"different arguments", `[{"type":"text","text":"hello"},{"type":"tool_call","tool_call_id":"t1","name":"search","arguments_json":{"q":"rust"}}]`},
		{"different tool", `[{"type":"text","text":"hello"},{"type":"tool_call","tool_call_id":"t2","name":"search","arguments_json":{"q":"zig"}}]`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result := providerTrace(t, textEnded, toolEnded, completed(testCase.content))
			for _, d := range result.Diagnostics {
				if d.Code == validation.CodeTerminalNotAssembly {
					return
				}
			}
			t.Fatalf("a terminal disagreeing with its parts was admitted: %v", codes(result))
		})
	}
}

func TestProviderTerminalWithNoStreamedPartsIsOutsideTheRule(t *testing.T) {
	unary := completed(`[{"type":"text","text":"whatever the provider said"}]`)
	if diags := codes(providerTrace(t, unary)); len(diags) != 0 {
		t.Fatalf("a unary terminal was judged against parts it never streamed: %v", diags)
	}
}

func TestProviderTraceCarryingACredentialExchange(t *testing.T) {
	for _, frame := range []string{
		`{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"provider.credential.grant.request","id":"g1","payload":{"provider_id":"p","nonce":"n1"}}`,
		`{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"provider.credential.grant.channel","id":"g2","in_reply_to":"g1","payload":{"nonce":"n1","channel":"/tmp/s.sock"}}`,
		`{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"provider.credential.grant.response","id":"g3","in_reply_to":"g1","payload":{"accepted":true,"credential_ref":"c1"}}`,
	} {
		result := providerTrace(t, frame)
		found := false
		for _, d := range result.Diagnostics {
			if d.Code == validation.CodeCredentialInTrace {
				found = true
			}
		}
		if !found {
			t.Fatalf("a trace carrying a credential exchange was admitted: %v", codes(result))
		}
	}
}

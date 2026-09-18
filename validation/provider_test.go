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
		{"different kind and payload with it", `[{"type":"reasoning","reasoning":"hello"},{"type":"tool_call","tool_call_id":"t1","name":"search","arguments_json":{"q":"zig"}}]`},
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

func TestProviderTerminalOfADifferentKindCarryingNothingToCompare(t *testing.T) {
	emptyText := `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.part.ended","id":"e2","inference_id":"i1","sequence":2,"payload":{"part_index":0,"part_kind":"text","text":""}}`
	result := providerTrace(t, emptyText, completed(`[{"type":"reasoning","reasoning":"thinking"}]`))
	if !hasCode(result, validation.CodeTerminalNotAssembly) {
		t.Fatalf("a terminal of the wrong kind was admitted because there was nothing left to compare: %v", codes(result))
	}
}

func TestProviderPartsEndingOutOfIndexOrder(t *testing.T) {
	assembled := `[{"type":"text","text":"hello"},{"type":"tool_call","tool_call_id":"t1","name":"search","arguments_json":{"q":"zig"}}]`
	result := providerTrace(t, toolEnded, textEnded, completed(assembled))
	if diags := codes(result); len(diags) != 0 {
		t.Fatalf("a terminal in part_index order was judged against arrival order: %v", result.Diagnostics)
	}
}

func TestProviderTerminalInArrivalOrderRatherThanIndexOrder(t *testing.T) {
	byArrival := `[{"type":"tool_call","tool_call_id":"t1","name":"search","arguments_json":{"q":"zig"}},{"type":"text","text":"hello"}]`
	result := providerTrace(t, toolEnded, textEnded, completed(byArrival))
	for _, d := range result.Diagnostics {
		if d.Code == validation.CodeTerminalNotAssembly {
			return
		}
	}
	t.Fatalf("a terminal ordered by arrival rather than part_index was admitted: %v", codes(result))
}

func TestProviderPartEndingAfterItsTerminal(t *testing.T) {
	assembled := `[{"type":"text","text":"hello"}]`
	result := providerTrace(t, textEnded, completed(assembled), toolEnded)
	var assembly, after bool
	for _, d := range result.Diagnostics {
		switch d.Code {
		case validation.CodeTerminalNotAssembly:
			assembly = true
		case validation.CodeEventAfterTerminal:
			after = true
		}
	}
	if !assembly {
		t.Fatalf("a terminal judged against only the parts that preceded it: %v", codes(result))
	}
	if !after {
		t.Fatalf("a part ending after its inference settled was admitted: %v", codes(result))
	}
}

func TestProviderTraceWithADuplicateKey(t *testing.T) {
	duplicate := `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.started","id":"e1","id":"e2","inference_id":"i1","sequence":1,"payload":{"model_ref":"p/other:x@m"}}`
	result := providerTrace(t, duplicate)
	for _, d := range result.Diagnostics {
		if d.Code == validation.CodeDuplicateKey {
			return
		}
	}
	t.Fatalf("a trace the agent-control validator rejects was admitted here: %v", codes(result))
}

func TestProviderTerminalWithNoStreamedPartsIsOutsideTheRule(t *testing.T) {
	unary := completed(`[{"type":"text","text":"whatever the provider said"}]`)
	if diags := codes(providerTrace(t, unary)); len(diags) != 0 {
		t.Fatalf("a unary terminal was judged against parts it never streamed: %v", diags)
	}
}

func TestProviderTraceCarryingACredentialValue(t *testing.T) {
	onEnvelope := `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"provider.credential.grant.request","id":"g1","payload":{"provider_id":"p","nonce":"n1","value":"sk-abc"}}`
	result := providerTrace(t, onEnvelope)
	for _, d := range result.Diagnostics {
		if d.Code == validation.CodeCredentialInTrace {
			return
		}
	}
	t.Fatalf("a trace carrying a credential value was admitted: %v", codes(result))
}

func TestProviderTraceOfAnOutOfBandGrant(t *testing.T) {
	for _, frame := range []string{
		`{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"provider.credential.grant.request","id":"g1","payload":{"provider_id":"p","nonce":"n1"}}`,
		`{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"provider.credential.grant.channel","id":"g2","in_reply_to":"g1","payload":{"nonce":"n1","channel":"/tmp/s.sock"}}`,
		`{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"provider.credential.grant.response","id":"g3","in_reply_to":"g1","payload":{"accepted":true,"credential_ref":"c1"}}`,
	} {
		if diags := codes(providerTrace(t, frame)); len(diags) != 0 {
			t.Fatalf("a tier-1 grant frame carrying no secret was rejected: %v", diags)
		}
	}
}

func createWithHeaders(headers string) string {
	return `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.create.request","id":"c1","payload":{"model_ref":"p1/m1","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"headers":` + headers + `}}`
}

func describeWithHeaders(headers string) string {
	return `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"provider.describe.response","id":"d1","in_reply_to":"d0","capability_revision":"r1","payload":{"protocol_versions":["0.1"],"providers":[{"id":"p1","wire":"openai-responses","framing":"sse","headers":` + headers + `}]}}`
}

func hasCode(result validation.Result, code string) bool {
	for _, d := range result.Diagnostics {
		if d.Code == code {
			return true
		}
	}
	return false
}

func TestProviderCredentialInCallerHeaders(t *testing.T) {
	for _, testCase := range []struct{ name, headers string }{
		{"authorization", `{"Authorization":"Basic abc"}`},
		{"lowercased", `{"authorization":"Basic abc"}`},
		{"proxy authorization", `{"Proxy-Authorization":"Basic abc"}`},
		{"api key", `{"X-Api-Key":"sk-abc"}`},
		{"bare api key", `{"Api-Key":"sk-abc"}`},
		{"bearer under another name", `{"X-Custom":"Bearer sk-abc"}`},
		{"bearer with leading space", `{"X-Custom":"  bearer sk-abc"}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if !hasCode(providerTrace(t, createWithHeaders(testCase.headers)), validation.CodeCredentialInHeaders) {
				t.Fatalf("a credential in a header was admitted: %v", codes(providerTrace(t, createWithHeaders(testCase.headers))))
			}
		})
	}
}

func TestProviderCredentialInAPublishedDescriptorHeader(t *testing.T) {
	if !hasCode(providerTrace(t, describeWithHeaders(`{"Authorization":"Bearer sk-abc"}`)), validation.CodeCredentialInHeaders) {
		t.Fatal("a descriptor publishing a credential header was admitted")
	}
	if result := providerTrace(t, describeWithHeaders(`{"X-Tenant":"acme"}`)); len(result.Diagnostics) != 0 {
		t.Fatalf("a descriptor publishing a tenancy header was rejected: %v", result.Diagnostics)
	}
}

func TestProviderHeadersCarryingTenancyAreAdmitted(t *testing.T) {
	for _, testCase := range []struct{ name, headers string }{
		{"tenant", `{"X-Tenant":"acme"}`},
		{"opaque value", `{"X-Request-Signature":"sk-looking-but-unnamed"}`},
		{"the word bearer alone", `{"X-Role":"bearer"}`},
		{"bearer inside a value", `{"X-Note":"the bearer of this"}`},
		{"no headers at all", `{}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result := providerTrace(t, createWithHeaders(testCase.headers))
			if len(result.Diagnostics) != 0 {
				t.Fatalf("a legitimate header was rejected: %v", result.Diagnostics)
			}
		})
	}
}

func TestProviderHeaderScanStaysInsideTheDocumentedLocations(t *testing.T) {
	envelope := `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.create.request","id":"c1","payload":{"model_ref":"p1/m1","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"tools":[{"name":"http_request","input_schema":{"properties":{"headers":{"description":"Bearer auth is injected by the gateway"}}}}]}}`
	if hasCode(providerTrace(t, envelope), validation.CodeCredentialInHeaders) {
		t.Fatal("a tool's input_schema describing a headers property is not a credential in a header")
	}
}

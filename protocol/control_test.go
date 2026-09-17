package protocol

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestAttachmentLimitsIgnoreADisclosureNoRequestCanSatisfy(t *testing.T) {
	limits := func(key string, raw string) FeatureSupport {
		return FeatureSupport{Level: SupportNative, Limits: map[string]json.RawMessage{key: json.RawMessage(raw)}}
	}
	t.Run("a ceiling below one", func(t *testing.T) {
		for _, raw := range []string{"0", "-1", `"two"`} {
			if value, ok := limits(LimitMaxSources, raw).MaxSources(); ok {
				t.Fatalf("max_sources %s disclosed a ceiling of %d", raw, value)
			}
		}
		if value, ok := limits(LimitMaxSources, "2").MaxSources(); !ok || value != 2 {
			t.Fatalf("max_sources 2 read as %d/%v", value, ok)
		}
	})
	t.Run("a transport no attachment can take", func(t *testing.T) {

		for _, raw := range []string{`["bogus"]`, `[]`, `["stdio","http"]`, `"process"`} {
			if kinds, ok := limits(LimitTransports, raw).Transports(); ok {
				t.Fatalf("transports %s disclosed %v", raw, kinds)
			}
		}

		kinds, ok := limits(LimitTransports, `["process","bogus","local"]`).Transports()
		if !ok || !slices.Equal(kinds, []string{ToolSourceProcess, ToolSourceLocal}) {
			t.Fatalf("transports read as %v/%v", kinds, ok)
		}
	})

	bare := FeatureSupport{Level: SupportNative}
	if _, ok := bare.MaxSources(); ok {
		t.Fatal("an endpoint with no limits disclosed a ceiling")
	}
	if _, ok := bare.Transports(); ok {
		t.Fatal("an endpoint with no limits disclosed transports")
	}
}

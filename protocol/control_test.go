package protocol

import (
	"encoding/json"
	"slices"
	"testing"
)

// The attachment limits exist so a refusal is checkable: an endpoint that
// advertises the key and refuses every well-formed array honours nothing it
// advertised. A limit that no request can satisfy is that same loophole worn
// as a disclosure, so neither accessor reports one. The schema refuses both
// shapes outright; these are the Go decoders holding the same line for a
// descriptor that never passed through it.
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
		// An attachment's kind is one of the five, so a list naming only
		// values outside them puts every possible attachment outside the
		// limit. It discloses nothing, and the endpoint is held to accepting
		// every well-formed array.
		for _, raw := range []string{`["bogus"]`, `[]`, `["stdio","http"]`, `"process"`} {
			if kinds, ok := limits(LimitTransports, raw).Transports(); ok {
				t.Fatalf("transports %s disclosed %v", raw, kinds)
			}
		}
		// A usable entry beside an unusable one still discloses the usable
		// one: dropping the entry an attachment cannot take is not the same
		// as discarding the disclosure it sits in.
		kinds, ok := limits(LimitTransports, `["process","bogus","local"]`).Transports()
		if !ok || !slices.Equal(kinds, []string{ToolSourceProcess, ToolSourceLocal}) {
			t.Fatalf("transports read as %v/%v", kinds, ok)
		}
	})
	// An endpoint that disclosed no limit at all is unconstrained, which is a
	// different answer from one whose disclosure was unusable only by degree.
	bare := FeatureSupport{Level: SupportNative}
	if _, ok := bare.MaxSources(); ok {
		t.Fatal("an endpoint with no limits disclosed a ceiling")
	}
	if _, ok := bare.Transports(); ok {
		t.Fatal("an endpoint with no limits disclosed transports")
	}
}

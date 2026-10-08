package sdk

import (
	"strings"
	"testing"
)

func TestParseModelRefHandlesCanonicalRefs(t *testing.T) {
	for ref, want := range map[string]parsedModelRef{
		"anthropic/anthropic-messages@claude-sonnet-4-5": {"anthropic", "anthropic-messages", "claude-sonnet-4-5"},

		"ollama/ollama@gemma4%3A31b": {"ollama", "ollama", "gemma4:31b"},
		"p/a@m":                      {"p", "a", "m"},
	} {
		got, ok := parseModelRef(ref)
		if !ok {
			t.Errorf("parseModelRef(%q) failed", ref)
			continue
		}
		if got != want {
			t.Errorf("parseModelRef(%q) = %+v, want %+v", ref, got, want)
		}
	}
}

func TestParseModelRefRejectsNonCanonicalRefs(t *testing.T) {
	for _, ref := range []string{
		"",
		"no-separators",
		"/anthropic-messages@model",
		"@anthropic/anthropic-messages",
		"anthropic/anthropic-messages",
		"anthropic/anthropic-messages@model@extra",
		"anthropic/anthropic-messages@model/extra",

		"ollama/ollama@gemma4:31b",
		"anthropic/anthropic-messages@",
	} {
		if _, ok := parseModelRef(ref); ok {
			t.Errorf("parseModelRef(%q) should have failed", ref)
		}
	}
}

func TestProviderIDFromRefFallsBackToALooseSplit(t *testing.T) {

	if got := providerIDFromRef("ollama/ollama@gemma4:31b"); got != "ollama" {
		t.Errorf("providerIDFromRef = %q, want ollama", got)
	}
	if got := providerIDFromRef("opaque-handle"); got != "" {
		t.Errorf("providerIDFromRef = %q, want empty for an opaque ref", got)
	}
}

func TestValidateExecutionRequestEnforcesSegmentLimits(t *testing.T) {
	messages := []Message{UserMessage("hi")}

	longProvider := strings.Repeat("p", maxIdentifierLength+1) + "/a@m"
	if err := validateExecutionRequest(longProvider, messages); err == nil {
		t.Error("an oversized provider segment should be rejected")
	}
	longAPI := "p/" + strings.Repeat("a", maxIdentifierLength+1) + "@m"
	if err := validateExecutionRequest(longAPI, messages); err == nil {
		t.Error("an oversized api segment should be rejected")
	}
	longModel := "p/a@" + strings.Repeat("m", maxModelFieldLength+1)
	if err := validateExecutionRequest(longModel, messages); err == nil {
		t.Error("an oversized model segment should be rejected")
	}
	longOpaque := strings.Repeat("x", maxOpaqueRefLength+1)
	if err := validateExecutionRequest(longOpaque, messages); err == nil {
		t.Error("an oversized opaque ref should be rejected")
	}
	if err := validateExecutionRequest("p/a@m", messages); err != nil {
		t.Errorf("a valid ref was rejected: %v", err)
	}
	if err := validateExecutionRequest("opaque", messages); err != nil {
		t.Errorf("a short opaque ref was rejected: %v", err)
	}
}

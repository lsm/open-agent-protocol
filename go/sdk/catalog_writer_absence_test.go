package sdk

import (
	"encoding/json"
	"testing"
)

func minimalDescriptor() ModelDescriptor {
	return ModelDescriptor{ModelRef: "p/wire@m", ModelID: "m", DisplayName: "M", ProviderID: "p",
		API: "wire", AuthStatus: AuthAuthenticated, Capabilities: []ModelCapability{CapabilityChat}}
}

func TestAnOmittedLifecycleIsNotPublishedAsNull(t *testing.T) {
	encoded, err := json.Marshal(minimalDescriptor())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var probe map[string]any
	if err := json.Unmarshal(encoded, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	value, present := probe["Lifecycle"]
	if present {
		t.Errorf("Lifecycle published as %v, want the key absent: an omitted member must not become a stated null", value)
	}
}

func TestAnOmittedSourceIsNotPublishedAsNull(t *testing.T) {
	encoded, err := json.Marshal(minimalDescriptor())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var probe map[string]any
	if err := json.Unmarshal(encoded, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	value, present := probe["Source"]
	if present {
		t.Errorf("Source published as %v, want the key absent: an omitted member must not become a stated null", value)
	}
}

func TestAStatedLifecycleAndSourceAreStillPublished(t *testing.T) {
	deprecated := LifecycleDeprecated
	staticFallback := SourceStaticFallback
	descriptor := minimalDescriptor()
	descriptor.Lifecycle = &deprecated
	descriptor.Source = &staticFallback
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var probe map[string]any
	if err := json.Unmarshal(encoded, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if probe["Lifecycle"] != string(LifecycleDeprecated) {
		t.Errorf("Lifecycle = %v, want %q", probe["Lifecycle"], LifecycleDeprecated)
	}
	if probe["Source"] != string(SourceStaticFallback) {
		t.Errorf("Source = %v, want %q", probe["Source"], SourceStaticFallback)
	}
}

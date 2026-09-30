package sdk

import "testing"

func TestAnInvalidStatedSourceIsRejected(t *testing.T) {
	raw := wireModelDescriptor{ModelRef: "p/wire@m", ModelID: "m", DisplayName: "M", ProviderID: "p",
		API: "wire", AuthStatus: "authenticated", Lifecycle: "stable"}
	capabilities := []string{"chat"}
	raw.Capabilities = &capabilities
	if _, err := parseModelDescriptor(raw, 0, "s"); err != nil {
		t.Errorf("a descriptor with no source must decode as unknown, not fail: %v", err)
	}
	bogus := "invented-source"
	raw.Source = &bogus
	if _, err := parseModelDescriptor(raw, 0, "s"); err == nil {
		t.Error("an invalid stated source must still be rejected, not defaulted")
	}
}

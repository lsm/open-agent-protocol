package harness

import (
	"testing"

	"github.com/lsm/open-agent-protocol/harnesses"
)

func TestCopilotPinIsPresentInThePublicCatalog(t *testing.T) {
	if _, err := harnesses.Files.ReadFile("copilot-cli.json"); err != nil {
		t.Fatalf("the Copilot harness has no shared catalog pin: %v", err)
	}
	catalog := loadCatalog(t)
	entry := harnessNamed(t, &catalog, "copilot-cli")
	current := currentOf(t, entry)
	if current.EndpointVersion == "" || current.CapabilityRevision == "" || current.Corpus == "" {
		t.Fatal("the current Copilot pin must name its endpoint, descriptor and replay corpus")
	}
}

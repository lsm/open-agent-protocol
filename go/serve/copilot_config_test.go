package serve

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lsm/open-agent-protocol/go/adapter/copilot"
)

func TestRegistryBuildsTheCopilotAdapterWithoutStartingIt(t *testing.T) {
	directory := t.TempDir()
	data, err := json.Marshal(map[string]any{"adapters": map[string]any{"copilot": map[string]any{"type": "copilot", "executable": filepath.Join(directory, "not-started"), "working_directory": directory, "environment": []string{"HOME"}}}})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := LoadRegistry(writeConfig(t, string(data)), staticEnviron(map[string]string{"HOME": directory}))
	if err != nil {
		t.Fatal(err)
	}
	implementation, ok := registry.Lookup("copilot")
	if !ok {
		t.Fatal("Copilot is absent from the public registry")
	}
	descriptor, err := implementation.Probe(context.Background())
	if err != nil || descriptor.CapabilityRevision != copilot.CapabilityRevision || descriptor.Capabilities.Endpoint.Version != copilot.PinnedVersion {
		t.Fatalf("registry descriptor %+v %v", descriptor, err)
	}
	if registry.WorkingDirectory("copilot") != directory {
		t.Fatal("configured directory was lost")
	}
}

func TestTheExampleCopilotEntryUsesAnExplicitNativeEnvironment(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "oap-serve-go-copilot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file configFile
	if err := file.UnmarshalJSON(data); err != nil {
		t.Fatal(err)
	}
	entry, ok := file.Adapters["copilot"]
	if !ok || entry.Type != "copilot" || entry.Executable == "" || entry.WorkingDirectory == "" || len(entry.Environment) == 0 {
		t.Fatalf("incomplete Copilot example %+v", entry)
	}
}

package hermes

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheGatePathDropsEveryDirectoryHoldingACopilotExecutable(t *testing.T) {
	root := t.TempDir()
	holding := filepath.Join(root, "holding")
	other := filepath.Join(root, "other")
	named := filepath.Join(root, "named")
	for _, directory := range []string{holding, other, filepath.Join(named, "copilot")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(holding, "copilot"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	separator := string(os.PathListSeparator)
	path := gatePath(strings.Join([]string{holding, other, named}, separator))
	if want := strings.Join([]string{other, named}, separator); path != want {
		t.Fatalf("gate PATH is %q, want %q", path, want)
	}
	t.Setenv("PATH", path)
	if found, err := exec.LookPath("copilot"); err == nil {
		t.Fatalf("copilot still resolves to %s", found)
	}
}

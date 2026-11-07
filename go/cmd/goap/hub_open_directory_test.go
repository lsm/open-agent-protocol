package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestHubOpenDirectory(t *testing.T) {
	binaries := map[string]string{"go": oapBinary(t)}
	if binary := os.Getenv("OAP_OAPX_BIN"); binary != "" {
		binaries["zig"] = binary
	}
	for name, binary := range binaries {
		t.Run(name, func(t *testing.T) {
			script, err := filepath.Abs(filepath.Join("..", "..", "..", ".claude", "skills", "verify", "scripts", "hub-open-directory.py"))
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command("python3", script, "--binary", binary, "--output-dir", filepath.Join(t.TempDir(), "proof"))
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("public hub placement failed: %v\n%s", err, output)
			} else {
				t.Log(string(output))
			}
		})
	}
}

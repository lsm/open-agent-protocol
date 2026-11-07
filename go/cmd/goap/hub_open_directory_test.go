package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHubOpenDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory journey requires POSIX pipes and process groups")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("directory journey requires python3 on PATH")
	}
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
			command := exec.Command(python, script, "--binary", binary, "--output-dir", filepath.Join(t.TempDir(), "proof"))
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("public hub placement failed: %v\n%s", err, output)
			} else {
				t.Log(string(output))
			}
		})
	}
}

package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestHubOpenDirectorySkipsWithoutPython(t *testing.T) {
	goTool := os.Getenv("OAP_GO")
	if goTool == "" {
		goTool = "go"
	}
	goTool, err := exec.LookPath(goTool)
	if err != nil {
		t.Skip("no Go toolchain available")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "-test.run=^TestHubOpenDirectory$", "-test.v")
	command.Env = append(os.Environ(), "PATH="+t.TempDir(), "OAP_GO="+goTool, "CGO_ENABLED=0", "GOFLAGS=-buildvcs=false")
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "--- SKIP: TestHubOpenDirectory") {
		t.Fatalf("expected the directory journey to skip without Python: %v\n%s", err, output)
	}
	t.Log(string(output))
}

//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHubRefusesASessionHistoryAGoapHubHolds(t *testing.T) {
	oapx := os.Getenv("OAP_OAPX_BIN")
	if oapx == "" {
		t.Skip("set OAP_OAPX_BIN to an oapx binary to check it honours goap's session history lock")
	}
	path := filepath.Join(t.TempDir(), "sessions.jsonl")
	held, err := lockSessionHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	command := exec.Command(oapx, "hub", "--stdio", "--session-history="+path)
	command.Stdin = strings.NewReader("")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("oapx hub started on a session history goap holds:\n%s", output)
	}
	if !strings.Contains(string(output), "another oapx hub is using the session history") {
		t.Fatalf("oapx hub did not say the history is held:\n%s", output)
	}
}

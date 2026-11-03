package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHubStdioAnswersGoapAndRustTheSame(t *testing.T) {
	rust := os.Getenv("OAP_RUST_HUB_BIN")
	if rust == "" {
		t.Skip("set OAP_RUST_HUB_BIN to an oap-rust-hub binary to compare its stdio wire with goap's")
	}
	if !filepath.IsAbs(rust) {
		t.Fatalf("OAP_RUST_HUB_BIN must be absolute, got %q", rust)
	}
	for name, lines := range hubParityScenarios {
		t.Run(name, func(t *testing.T) {
			assertSameHubAnswers(t, name, lines, runHubScript(t, oapBinary(t), lines), runHubScript(t, rust, lines))
		})
	}
}

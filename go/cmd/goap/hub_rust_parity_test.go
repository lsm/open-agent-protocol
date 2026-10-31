package main

import (
	"os"
	"path/filepath"
	"testing"
)

var rustHubUnported = map[string]string{
	"the work verbs refuse what they cannot serve and list an empty hub": "the work verbs",
	"the work verbs refuse malformed requests and filter an empty list":  "the work verbs",
	"the transcript op refuses the same refusals":                        "the transcript op",
}

func TestHubStdioAnswersGoapAndRustTheSame(t *testing.T) {
	rust := os.Getenv("OAP_RUST_HUB_BIN")
	if rust == "" {
		t.Skip("set OAP_RUST_HUB_BIN to an oap-rust-hub binary to compare its stdio wire with goap's")
	}
	if !filepath.IsAbs(rust) {
		t.Fatalf("OAP_RUST_HUB_BIN must be absolute, got %q", rust)
	}
	for name, lines := range hubParityScenarios {
		if _, unported := rustHubUnported[name]; unported {
			continue
		}
		t.Run(name, func(t *testing.T) {
			assertSameHubAnswers(t, name, lines, runHubScript(t, oapBinary(t), lines), runHubScript(t, rust, lines))
		})
	}
}

func TestHubStdioRustSkipsOnlyScenariosThatExist(t *testing.T) {
	for name := range rustHubUnported {
		if _, exists := hubParityScenarios[name]; !exists {
			t.Errorf("the Rust hub skips %q, which is no scenario", name)
		}
	}
}

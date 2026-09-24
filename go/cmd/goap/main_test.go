package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/validation"
)

func TestCheck(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"check"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("check: %v\nstderr: %s", err, stderr.String())
	}
	for _, want := range []string{"PASS schemas", "PASS harnesses", "PASS fixtures", "PASS golden", "PASS cancellation", "PASS check"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, stdout.String())
		}
	}
}

func TestValidateJSON(t *testing.T) {
	file := filepath.Join(repositoryRoot(), "fixtures", "valid", "core-completed.json")
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"validate", "--format=json", file}, nil, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var reports []struct {
		Valid bool `json:"valid"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &reports); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || !reports[0].Valid {
		t.Fatalf("unexpected reports: %+v", reports)
	}
}

func TestProvidersZAICN(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"providers", "zai-cn", "--format=json"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("providers: %v\nstderr: %s", err, stderr.String())
	}
	var presets []struct {
		ID            string `json:"id"`
		Model         string `json:"model"`
		EvidenceClass string `json:"evidence_class"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &presets); err != nil {
		t.Fatal(err)
	}
	if len(presets) != 4 || presets[0].ID != "zai-cn-responses-control" || presets[0].Model != "glm-5.3" || presets[0].EvidenceClass != "documented-control" {
		t.Fatalf("presets: %+v", presets)
	}
}

func TestValidateInvalidReturnsError(t *testing.T) {
	file := filepath.Join(repositoryRoot(), "fixtures", "schema-invalid", "missing-envelope-id.json")
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"validate", file}, nil, &stdout, &stderr); err == nil {
		t.Fatal("invalid fixture succeeded")
	}
	if !strings.Contains(stdout.String(), "FAIL ") {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestDiagnosticAnchorNamesTheFirstEnvelope(t *testing.T) {
	anchor := diagnosticAnchor(validation.Diagnostic{
		Index:      0,
		Type:       "protocol.initialize.response",
		EnvelopeID: "env_1",
		Pointer:    "/payload",
	})
	for _, want := range []string{"envelope 0", "protocol.initialize.response", "id env_1", "/payload"} {
		if !strings.Contains(anchor, want) {
			t.Fatalf("anchor %q lacks %q", anchor, want)
		}
	}
}

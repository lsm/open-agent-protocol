package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"check"}, &stdout, &stderr); err != nil {
		t.Fatalf("check: %v\nstderr: %s", err, stderr.String())
	}
	for _, want := range []string{"PASS schemas", "PASS fixtures", "PASS golden", "PASS cancellation", "PASS check"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, stdout.String())
		}
	}
}

func TestValidateJSON(t *testing.T) {
	file := filepath.Join(repositoryRoot(), "fixtures", "valid", "core-completed.json")
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"validate", "--format=json", file}, &stdout, &stderr); err != nil {
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

func TestValidateInvalidReturnsError(t *testing.T) {
	file := filepath.Join(repositoryRoot(), "fixtures", "schema-invalid", "missing-envelope-id.json")
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"validate", file}, &stdout, &stderr); err == nil {
		t.Fatal("invalid fixture succeeded")
	}
	if !strings.Contains(stdout.String(), "FAIL ") {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

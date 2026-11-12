package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMatrixMatchesTheCurrentCatalogAndCoversEveryColumn(t *testing.T) {
	matrix, err := loadMatrix()
	if err != nil {
		t.Fatal(err)
	}
	if len(matrix.Rows) != 8 {
		t.Fatalf("rows = %d, want the eight current Go harness pins", len(matrix.Rows))
	}
	for _, row := range matrix.Rows {
		for _, cell := range row.Cells {
			for _, binding := range cell.Evidence {
				if !strings.HasPrefix(binding.Test, "Test") {
					t.Fatalf("%s %s: %q is not a test id", row.Harness, cell.Column, binding.Test)
				}
			}
		}
	}
}

func stream(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.json")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func event(action, pkg, test string) string {
	body, err := json.Marshal(testEvent{Action: action, Package: modulePrefix + pkg, Test: test})
	if err != nil {
		panic(err)
	}
	return string(body)
}

func TestEvaluatePublishesPassAndUnsupportedAndFailsOnBadEvidence(t *testing.T) {
	matrix := matrixFile{
		Columns: []column{{Key: "one", Title: "One"}, {Key: "two", Title: "Two"}},
		Rows: []rowBinding{{
			Harness: "acp", Pin: "pinned", CapabilityRevision: "rev", Corpus: "corpus",
			Cells: []cellBinding{
				{Column: "one", Claim: "capability", Evidence: []evidenceBinding{{Package: "go/adapter/acp", Test: "TestPositive", Kind: "positive"}}},
				{Column: "two", Claim: "refusal", Evidence: []evidenceBinding{{Package: "go/adapter/acp", Test: "TestRefusal", Kind: "refusal"}}},
			},
		}},
	}
	passing := stream(t,
		event("run", "go/adapter/acp", "TestPositive"),
		event("pass", "go/adapter/acp", "TestPositive"),
		event("run", "go/adapter/acp", "TestRefusal"),
		event("pass", "go/adapter/acp", "TestRefusal"),
	)
	statuses, _, err := readTestEvents(passing)
	if err != nil {
		t.Fatal(err)
	}
	result := evaluate(matrix, statuses)
	if len(result.Errors) != 0 {
		t.Fatalf("errors = %v", result.Errors)
	}
	if result.Rows[0].Cells[0].Status != "pass" || result.Rows[0].Cells[1].Status != "unsupported" {
		t.Fatalf("statuses = %s, %s", result.Rows[0].Cells[0].Status, result.Rows[0].Cells[1].Status)
	}
	rendered := renderMarkdown(result, matrix)
	if !strings.Contains(rendered, "**pass**") || !strings.Contains(rendered, "**unsupported**") || !strings.Contains(rendered, "not a claim about a live external runtime") {
		t.Fatalf("markdown misses statuses or the evidence boundary: %s", rendered)
	}
	for _, failure := range []struct {
		name  string
		lines []string
		want  string
	}{
		{name: "failed", lines: []string{event("fail", "go/adapter/acp", "TestPositive"), event("pass", "go/adapter/acp", "TestRefusal")}, want: "failed"},
		{name: "skipped", lines: []string{event("skip", "go/adapter/acp", "TestPositive"), event("pass", "go/adapter/acp", "TestRefusal")}, want: "was skipped"},
		{name: "missing", lines: []string{event("pass", "go/adapter/acp", "TestRefusal")}, want: "is missing from the test output"},
	} {
		statuses, _, err := readTestEvents(stream(t, failure.lines...))
		if err != nil {
			t.Fatal(err)
		}
		result := evaluate(matrix, statuses)
		if len(result.Errors) == 1 && strings.Contains(result.Errors[0], failure.want) {
			continue
		}
		t.Fatalf("%s: errors = %v, want one naming that the evidence %s", failure.name, result.Errors, failure.want)
	}
}

func fullPassStream(t *testing.T) string {
	t.Helper()
	matrix, err := loadMatrix()
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, row := range matrix.Rows {
		for _, cell := range row.Cells {
			for _, binding := range cell.Evidence {
				lines = append(lines, event("pass", binding.Package, binding.Test))
			}
		}
	}
	return stream(t, lines...)
}

func TestRunRejectsAnUnknownHarnessThroughTheEntryPoint(t *testing.T) {
	passing := fullPassStream(t)
	original := matrixData
	defer func() { matrixData = original }()
	var matrix matrixFile
	if err := json.Unmarshal(matrixData, &matrix); err != nil {
		t.Fatal(err)
	}
	matrix.Rows[0].Harness = "no-such-harness"
	doctored, err := json.Marshal(matrix)
	if err != nil {
		t.Fatal(err)
	}
	matrixData = doctored
	out := filepath.Join(t.TempDir(), "out")
	code := run([]string{"-test-output", passing, "-out", out})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 for a binding naming an unknown harness", code)
	}
	diagnostics, err := os.ReadFile(filepath.Join(out, "generation-failed.md"))
	if err != nil {
		t.Fatalf("no diagnostics written: %v", err)
	}
	if !strings.Contains(string(diagnostics), "no-such-harness") {
		t.Fatalf("diagnostics = %s", diagnostics)
	}
}

func TestRunReplacesThePriorAttemptArtifactsInTheSameDirectory(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	good := fullPassStream(t)
	if code := run([]string{"-test-output", good, "-out", out, "-run-url", "https://example.test/run/1"}); code != 0 {
		t.Fatalf("publish exit = %d", code)
	}
	if code := run([]string{"-test-output", stream(t, "not json"), "-out", out}); code != 1 {
		t.Fatalf("failure exit = %d", code)
	}
	if _, err := os.Stat(filepath.Join(out, "harness-feature-matrix.md")); !os.IsNotExist(err) {
		t.Fatal("a failed attempt left the previous matrix in place")
	}
	if _, err := os.Stat(filepath.Join(out, "matrix-test-evidence.jsonl")); !os.IsNotExist(err) {
		t.Fatal("a failed attempt left the previous evidence stream in place")
	}
	failure, err := os.ReadFile(filepath.Join(out, "generation-failed.md"))
	if err != nil {
		t.Fatalf("failed attempt wrote no diagnostics: %v", err)
	}
	if !strings.Contains(string(failure), "generation failed") {
		t.Fatalf("diagnostics = %s", failure)
	}
	if code := run([]string{"-test-output", good, "-out", out, "-run-url", "https://example.test/run/1"}); code != 0 {
		t.Fatalf("republish exit = %d", code)
	}
	if _, err := os.Stat(filepath.Join(out, "generation-failed.md")); !os.IsNotExist(err) {
		t.Fatal("a successful attempt left the previous failure diagnostics in place")
	}
	published, err := os.ReadFile(filepath.Join(out, "harness-feature-matrix.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(published), "`[") {
		t.Fatal("evidence links are wrapped in a code span and cannot be clicked")
	}
	if !strings.Contains(string(published), "](https://example.test/run/1)") {
		t.Fatal("evidence test ids are not linked to the producing run")
	}
}

func TestWriteFailureLeavesDiagnosticsForEveryExitPath(t *testing.T) {
	dir := t.TempDir()
	writeFailure(dir, []string{"binding rejected: pin moved"}, nil)
	written, err := os.ReadFile(filepath.Join(dir, "generation-failed.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "generation failed") || !strings.Contains(string(written), "pin moved") {
		t.Fatalf("diagnostics = %s", written)
	}
}

func TestLoadMatrixRejectsARowBoundToAMovedPin(t *testing.T) {
	original := matrixData
	defer func() { matrixData = original }()
	var matrix matrixFile
	if err := json.Unmarshal(matrixData, &matrix); err != nil {
		t.Fatal(err)
	}
	matrix.Rows[0].Pin = "v0.0.0-moved"
	changed, err := json.Marshal(matrix)
	if err != nil {
		t.Fatal(err)
	}
	matrixData = changed
	if _, err := loadMatrix(); err == nil || !strings.Contains(err.Error(), "re-bind the evidence") {
		t.Fatalf("err = %v, want the stale pin named", err)
	}
}

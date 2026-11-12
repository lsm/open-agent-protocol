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

package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lsm/oxap/harnesses"
)

const modulePrefix = "github.com/lsm/oxap/"

type column struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

type evidenceBinding struct {
	Package string `json:"package"`
	Test    string `json:"test"`
	Kind    string `json:"kind"`
	Fixture string `json:"fixture,omitempty"`
}

type cellBinding struct {
	Column   string            `json:"column"`
	Claim    string            `json:"claim"`
	Evidence []evidenceBinding `json:"evidence"`
}

type rowBinding struct {
	Harness            string        `json:"harness"`
	Pin                string        `json:"pin"`
	CapabilityRevision string        `json:"capability_revision"`
	Corpus             string        `json:"corpus"`
	Cells              []cellBinding `json:"cells"`
}

type matrixFile struct {
	Columns []column     `json:"columns"`
	Rows    []rowBinding `json:"rows"`
}

type testEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
	Output  string `json:"Output"`
}

type evidenceResult struct {
	Package string `json:"package"`
	Test    string `json:"test"`
	Kind    string `json:"kind"`
	Fixture string `json:"fixture,omitempty"`
	Status  string `json:"status"`
}

type cellResult struct {
	Column   string           `json:"column"`
	Claim    string           `json:"claim"`
	Status   string           `json:"status"`
	Evidence []evidenceResult `json:"evidence"`
}

type componentRecord struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Repository string `json:"repository"`
}

type sourceRecord struct {
	Component  string `json:"component"`
	Tag        string `json:"tag,omitempty"`
	Commit     string `json:"commit"`
	Repository string `json:"repository,omitempty"`
}

type rowResult struct {
	Harness            string            `json:"harness"`
	Pin                string            `json:"pin"`
	CapabilityRevision string            `json:"capability_revision"`
	Corpus             string            `json:"corpus"`
	SourceURL          string            `json:"source_url,omitempty"`
	Components         []componentRecord `json:"components"`
	Sources            []sourceRecord    `json:"sources"`
	Cells              []cellResult      `json:"cells"`
}

type report struct {
	Generation  string      `json:"generation"`
	OAPSHA      string      `json:"oap_sha"`
	RunURL      string      `json:"run_url"`
	TestCommand string      `json:"test_command"`
	Rows        []rowResult `json:"rows"`
	Errors      []string    `json:"errors,omitempty"`
}

func main() {
	os.Exit(run(os.Args[1:]))
}

var ownedOutputs = []string{"harness-feature-matrix.md", "harness-feature-matrix.json", "matrix-test-evidence.jsonl", "generation-failed.md"}

func run(args []string) int {
	flags := flag.NewFlagSet("featurematrix", flag.ContinueOnError)
	testOutput := flags.String("test-output", "", "path to a go test -json stream")
	out := flags.String("out", "", "directory that receives the rendered matrix")
	sha := flags.String("sha", "", "the OAP commit the evidence was produced on")
	runURL := flags.String("run-url", "", "URL of the CI run that produced the evidence")
	testCommand := flags.String("test-command", "go test -json -count=1 ./go/adapter/...", "command that produced the test stream")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *testOutput == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "featurematrix: -test-output and -out are required")
		return 2
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		reportError(err)
		return 1
	}
	if err := clearOwnedOutputs(*out); err != nil {
		reportError(err)
		return 1
	}
	matrix, err := loadMatrix()
	if err != nil {
		_ = writeFailure(*out, []string{err.Error()}, nil)
		reportError(err)
		return 1
	}
	statuses, packageOutput, err := readTestEvents(*testOutput)
	if err != nil {
		_ = writeFailure(*out, []string{err.Error()}, nil)
		reportError(err)
		return 1
	}
	result := evaluate(matrix, statuses)
	result.OAPSHA = *sha
	result.RunURL = *runURL
	result.TestCommand = *testCommand
	if len(result.Errors) > 0 {
		result.Generation = "failed"
	} else {
		result.Generation = "published"
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		reportError(err)
		return 1
	}
	if err := os.WriteFile(filepath.Join(*out, "harness-feature-matrix.json"), append(data, '\n'), 0o644); err != nil {
		reportError(err)
		return 1
	}
	if err := writeFilteredEvents(*testOutput, filepath.Join(*out, "matrix-test-evidence.jsonl"), matrix); err != nil {
		reportError(err)
		return 1
	}
	if len(result.Errors) > 0 {
		if err := writeFailure(*out, result.Errors, packageOutput); err != nil {
			reportError(err)
			return 1
		}
		for _, failure := range result.Errors {
			fmt.Fprintf(os.Stderr, "featurematrix: %s\n", failure)
		}
		return 1
	}
	if err := os.WriteFile(filepath.Join(*out, "harness-feature-matrix.md"), []byte(renderMarkdown(result, matrix)), 0o644); err != nil {
		reportError(err)
		return 1
	}
	return 0
}

func clearOwnedOutputs(dir string) error {
	for _, name := range ownedOutputs {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func writeFailure(dir string, failures []string, packageOutput map[string][]string) error {
	var diagnostics bytes.Buffer
	fmt.Fprintf(&diagnostics, "# Harness feature matrix generation failed\n\n")
	for _, failure := range failures {
		fmt.Fprintf(&diagnostics, "- %s\n", failure)
	}
	for pkg, lines := range packageOutput {
		if len(lines) == 0 {
			continue
		}
		fmt.Fprintf(&diagnostics, "\n## %s\n\n%s", pkg, strings.Join(lines, ""))
	}
	return os.WriteFile(filepath.Join(dir, "generation-failed.md"), diagnostics.Bytes(), 0o644)
}

func reportError(err error) {
	fmt.Fprintf(os.Stderr, "featurematrix: %v\n", err)
}

func currentPin(harness string) (pin harnesses.Pin, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			pin = harnesses.Pin{}
			err = fmt.Errorf("harness catalog lookup for %q failed: %v", harness, recovered)
		}
	}()
	return harnesses.Current(harness), nil
}

func loadMatrix() (matrixFile, error) {
	var matrix matrixFile
	decoder := json.NewDecoder(bytes.NewReader(matrixData))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&matrix); err != nil {
		return matrix, err
	}
	titles := map[string]string{}
	for _, col := range matrix.Columns {
		titles[col.Key] = col.Title
	}
	if len(matrix.Rows) == 0 || len(titles) != len(matrix.Columns) {
		return matrix, fmt.Errorf("matrix.json: columns must be non-empty and unique")
	}
	for _, row := range matrix.Rows {
		pin, err := currentPin(row.Harness)
		if err != nil {
			return matrix, err
		}
		if pin.Label != row.Pin {
			return matrix, fmt.Errorf("matrix.json: %s pins %q but the catalog's current version is %q; re-bind the evidence to the new pin before publishing", row.Harness, row.Pin, pin.Label)
		}
		if pin.CapabilityRevision != row.CapabilityRevision {
			return matrix, fmt.Errorf("matrix.json: %s records revision %q but the catalog's current revision is %q", row.Harness, row.CapabilityRevision, pin.CapabilityRevision)
		}
		if pin.Corpus != row.Corpus {
			return matrix, fmt.Errorf("matrix.json: %s records corpus %q but the catalog's current corpus is %q", row.Harness, row.Corpus, pin.Corpus)
		}
		seen := map[string]bool{}
		for _, cell := range row.Cells {
			if _, ok := titles[cell.Column]; !ok {
				return matrix, fmt.Errorf("matrix.json: %s has a cell for unknown column %q", row.Harness, cell.Column)
			}
			if seen[cell.Column] {
				return matrix, fmt.Errorf("matrix.json: %s repeats column %q", row.Harness, cell.Column)
			}
			seen[cell.Column] = true
			if len(cell.Evidence) == 0 {
				return matrix, fmt.Errorf("matrix.json: %s column %q has no evidence", row.Harness, cell.Column)
			}
			for _, binding := range cell.Evidence {
				if binding.Kind != "positive" && binding.Kind != "refusal" {
					return matrix, fmt.Errorf("matrix.json: %s column %q has evidence of unknown kind %q", row.Harness, cell.Column, binding.Kind)
				}
				if !strings.HasPrefix(binding.Package, "go/") {
					return matrix, fmt.Errorf("matrix.json: %s column %q evidence package %q is not a repository-relative Go package", row.Harness, cell.Column, binding.Package)
				}
			}
		}
		for key := range titles {
			if !seen[key] {
				return matrix, fmt.Errorf("matrix.json: %s has no cell for column %q", row.Harness, key)
			}
		}
	}
	return matrix, nil
}

func readTestEvents(path string) (map[string]string, map[string][]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	statuses := map[string]string{}
	packageOutput := map[string][]string{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event testEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		switch event.Action {
		case "pass", "fail", "skip":
			if event.Test == "" {
				continue
			}
			statuses[event.Package+"\t"+event.Test] = event.Action
		case "output":
			if event.Test == "" {
				packageOutput[event.Package] = append(packageOutput[event.Package], event.Output)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	return statuses, packageOutput, nil
}

func evaluate(matrix matrixFile, statuses map[string]string) report {
	var result report
	for _, row := range matrix.Rows {
		pin := harnesses.Current(row.Harness)
		built := rowResult{Harness: row.Harness, Pin: row.Pin, CapabilityRevision: row.CapabilityRevision, Corpus: row.Corpus}
		for _, component := range pin.Components {
			built.Components = append(built.Components, componentRecord{Name: component.Name, Version: component.Version, Repository: componentRepository(row.Harness, component.Name)})
		}
		for _, source := range pin.Sources {
			repository := componentRepository(row.Harness, source.Component)
			built.Sources = append(built.Sources, sourceRecord{Component: source.Component, Tag: source.Tag, Commit: source.Commit, Repository: repository})
		}
		if len(pin.Sources) > 0 && built.Sources[0].Repository != "" {
			built.SourceURL = strings.TrimSuffix(built.Sources[0].Repository, "/") + "/tree/" + built.Sources[0].Commit
		}
		for _, cell := range row.Cells {
			evaluated := cellResult{Column: cell.Column, Claim: cell.Claim}
			cellStatus := "pass"
			allRefusals := true
			for _, binding := range cell.Evidence {
				status, ok := statuses[modulePrefix+binding.Package+"\t"+binding.Test]
				if !ok {
					status = "missing"
					result.Errors = append(result.Errors, fmt.Sprintf("%s %s: test %s in package %s is missing from the test output; a publishable matrix needs a result for every bound test", row.Harness, cell.Column, binding.Test, binding.Package))
				} else if status == "skip" {
					result.Errors = append(result.Errors, fmt.Sprintf("%s %s: test %s in package %s was skipped; a skipped test cannot back a cell", row.Harness, cell.Column, binding.Test, binding.Package))
				} else if status == "fail" {
					result.Errors = append(result.Errors, fmt.Sprintf("%s %s: test %s in package %s failed", row.Harness, cell.Column, binding.Test, binding.Package))
				}
				evaluated.Evidence = append(evaluated.Evidence, evidenceResult{Package: binding.Package, Test: binding.Test, Kind: binding.Kind, Fixture: binding.Fixture, Status: status})
				if status == "fail" || status == "missing" {
					cellStatus = "fail"
				}
				if binding.Kind != "refusal" {
					allRefusals = false
				}
			}
			if cellStatus == "pass" && allRefusals {
				cellStatus = "unsupported"
			}
			evaluated.Status = cellStatus
			built.Cells = append(built.Cells, evaluated)
		}
		result.Rows = append(result.Rows, built)
	}
	return result
}

func componentRepository(harness, name string) string {
	data, err := harnesses.Files.ReadFile(harness + ".json")
	if err != nil {
		return ""
	}
	var entry struct {
		Versions []struct {
			Status     string `json:"status"`
			Components []struct {
				Name       string `json:"name"`
				Repository string `json:"repository"`
			} `json:"components"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		return ""
	}
	for _, version := range entry.Versions {
		if version.Status != "current" {
			continue
		}
		for _, component := range version.Components {
			if component.Name == name {
				return component.Repository
			}
		}
	}
	return ""
}

func writeFilteredEvents(source, destination string, matrix matrixFile) error {
	wanted := map[string]bool{}
	for _, row := range matrix.Rows {
		for _, cell := range row.Cells {
			for _, binding := range cell.Evidence {
				wanted[modulePrefix+binding.Package+"\t"+binding.Test] = true
			}
		}
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer out.Close()
	writer := bufio.NewWriter(out)
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event testEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return err
		}
		if event.Test != "" && wanted[event.Package+"\t"+event.Test] {
			if _, err := writer.Write(append(line, '\n')); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return writer.Flush()
}

func renderMarkdown(result report, matrix matrixFile) string {
	var buffer bytes.Buffer
	fmt.Fprintf(&buffer, "# Harness feature matrix\n\n")
	fmt.Fprintf(&buffer, "Every cell records what a Go adapter test run proved on the pinned captured or synthesized native wire of the named harness version. This is adapter behavior against the pin, not a claim about a live external runtime, and the ACP row records one protocol adapter, not every ACP client.\n\n")
	if result.RunURL != "" {
		fmt.Fprintf(&buffer, "Produced by CI run %s at OAP commit %s from `%s`.\n\n", result.RunURL, result.OAPSHA, result.TestCommand)
	} else {
		fmt.Fprintf(&buffer, "Produced at OAP commit %s from `%s`.\n\n", result.OAPSHA, result.TestCommand)
	}
	fmt.Fprintf(&buffer, "`pass` marks a positive capability assertion that passed. `unsupported` marks an explicit refusal test that passed: the adapter rejects the request with a named error and writes nothing native. Per-cell claims say what the bound tests assert.\n\n")
	headers := []string{"Harness (pin)"}
	for _, col := range matrix.Columns {
		headers = append(headers, col.Title)
	}
	fmt.Fprintf(&buffer, "| %s |\n", strings.Join(headers, " | "))
	fmt.Fprintf(&buffer, "|%s\n", strings.Repeat("---|", len(headers)))
	for _, row := range result.Rows {
		label := fmt.Sprintf("%s %s", harnessName(row.Harness), row.Pin)
		if row.SourceURL != "" {
			label = fmt.Sprintf("[%s](%s)", label, row.SourceURL)
		}
		cells := []string{label}
		byColumn := map[string]cellResult{}
		for _, cell := range row.Cells {
			byColumn[cell.Column] = cell
		}
		for _, col := range matrix.Columns {
			cell := byColumn[col.Key]
			parts := []string{fmt.Sprintf("**%s**", cell.Status)}
			for _, evidence := range cell.Evidence {
				if result.RunURL != "" {
					parts = append(parts, fmt.Sprintf("[%s](%s)", evidence.Test, result.RunURL))
				} else {
					parts = append(parts, fmt.Sprintf("`%s`", evidence.Test))
				}
			}
			parts = append(parts, cell.Claim)
			cells = append(cells, strings.Join(parts, "<br>"))
		}
		fmt.Fprintf(&buffer, "| %s |\n", strings.Join(cells, " | "))
	}
	return buffer.String()
}

func harnessName(id string) string {
	switch id {
	case "acp":
		return "ACP"
	case "claude-code":
		return "Claude Code"
	case "codex-app-server":
		return "Codex app-server"
	case "copilot-cli":
		return "Copilot CLI"
	case "deepseek-harness":
		return "DeepSeek Harness"
	case "hermes":
		return "Hermes"
	case "opencode":
		return "OpenCode"
	case "pi":
		return "Pi"
	}
	return id
}

//go:embed matrix.json
var matrixData []byte

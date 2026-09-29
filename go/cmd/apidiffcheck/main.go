package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lsm/open-agent-protocol/go/internal/publicset"
)

const apidiffVersion = "v0.0.0-20260908205506-85c1c2202aba"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "apidiffcheck:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("apidiffcheck", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	description := flags.String("description", "", "a file holding the pull request description, whose Breaking changes section records an incompatible change (default: PR_BODY, which the workflow sets from the event payload)")
	baseRoot := flags.String("base-root", "", "a checkout of the merge base, for the per-pull-request comparison (default: PR_BASE_ROOT)")
	root := flags.String("root", ".", "the module root at the pull request head")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *description == "" {
		*description = os.Getenv("PR_BODY")
	}
	if *baseRoot == "" {
		*baseRoot = os.Getenv("PR_BASE_ROOT")
	}
	if *baseRoot == "" {
		return errors.New("the gate needs a checkout of the merge base: pass -base-root, or set PR_BASE_ROOT in the workflow")
	}
	return check(*root, *baseRoot, readDescription(*description), stdout)
}

func readDescription(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func check(root, baseRoot, description string, stdout io.Writer) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	module, err := modulePath(root)
	if err != nil {
		return err
	}
	head, err := publicPackages(root)
	if err != nil {
		return err
	}
	base, err := publicPackages(baseRoot)
	if err != nil {
		return err
	}
	removed, added := setDifference(base, head), setDifference(head, base)
	report, err := apidiffReport(root, baseRoot, module, head, added)
	if err != nil {
		return err
	}
	changed := incompatible(report, module)
	changed = append(changed, removed...)
	if len(added) > 0 {
		fmt.Fprintf(stdout, "compatibility: %s added since the base, which is a compatible change\n", strings.Join(added, ", "))
	}
	if len(changed) == 0 {
		fmt.Fprintf(stdout, "PASS compatibility: no incompatible public API change in this pull request\n")
		return nil
	}
	if missing := missingFrom(changed, recordedChanges(description)); len(missing) > 0 {
		return fmt.Errorf("this pull request changes %s incompatibly without its Breaking changes section naming them in backticks: %s. Add a '## Breaking changes' section to the description naming each one, or say why the break is intended. A package that is gone counts: removing a public package is the most incompatible change there is", strings.Join(changed, ", "), strings.Join(missing, ", "))
	}
	fmt.Fprintf(stdout, "PASS compatibility: recorded incompatible change in %s\n", strings.Join(changed, ", "))
	return nil
}

func modulePath(root string) (string, error) {
	data, err := os.ReadFile(path.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if module, found := strings.CutPrefix(strings.TrimSpace(line), "module "); found {
			return strings.TrimSpace(module), nil
		}
	}
	return "", errors.New("go.mod names no module")
}

func missingFrom(changed []string, section string) []string {
	var missing []string
	for _, name := range changed {
		if !strings.Contains(section, "`"+name+"`") {
			missing = append(missing, name)
		}
	}
	return missing
}

func setDifference(from, other []string) []string {
	present := map[string]bool{}
	for _, name := range other {
		present[name] = true
	}
	var difference []string
	for _, name := range from {
		if !present[name] {
			difference = append(difference, name)
		}
	}
	return difference
}

func publicPackages(root string) ([]string, error) {
	dirs, err := packageDirs(root)
	if err != nil {
		return nil, err
	}
	var public []string
	for _, dir := range dirs {
		relative, found := strings.CutPrefix(dir, strings.TrimSuffix(root, "/")+"/")
		if !found || publicset.Internal(relative) {
			continue
		}
		if publicset.Public(relative) {
			public = append(public, relative)
		}
	}
	if len(public) == 0 {
		return nil, fmt.Errorf("no public package found under %s", root)
	}
	sort.Strings(public)
	return public, nil
}

func packageDirs(root string) ([]string, error) {
	command := exec.Command("go", "list", "-f", "{{.Dir}}", "./...")
	command.Dir = root
	out, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("go list in %s: %w", root, err)
	}
	dirs := strings.Fields(string(out))
	if len(dirs) == 0 {
		return nil, fmt.Errorf("go list in %s found no packages", root)
	}
	return dirs, nil
}

func apidiffReport(head, baseRoot, module string, packages, added []string) (string, error) {
	skip := map[string]bool{}
	for _, name := range added {
		skip[name] = true
	}
	work, err := os.MkdirTemp("", "apidiffcheck")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	var report strings.Builder
	for i, name := range packages {
		if skip[name] {
			continue
		}
		importPath := module + "/" + name
		oldExport := path.Join(work, fmt.Sprintf("old-%d", i))
		newExport := path.Join(work, fmt.Sprintf("new-%d", i))
		if err := writeExport(baseRoot, importPath, oldExport); err != nil {
			return "", fmt.Errorf("export data for the base of %s: %w", name, err)
		}
		if err := writeExport(head, importPath, newExport); err != nil {
			return "", fmt.Errorf("export data for %s: %w", name, err)
		}
		output, err := apidiff(oldExport, newExport)
		if err != nil {
			return "", err
		}
		if !strings.Contains(output, "Incompatible changes:") {
			continue
		}
		fmt.Fprintf(&report, "\n# %s\n%s\n", module+"/"+name, output)
	}
	return report.String(), nil
}

func writeExport(root, importPath, out string) error {
	command := exec.Command("go", "run", "golang.org/x/exp/cmd/apidiff@"+apidiffVersion, "-w", out, importPath)
	command.Dir = root
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return errors.New(strings.TrimSpace(stderr.String()))
	}
	return nil
}

func apidiff(oldExport, newExport string) (string, error) {
	command := exec.Command("go", "run", "golang.org/x/exp/cmd/apidiff@"+apidiffVersion, oldExport, newExport)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("apidiff: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

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
	"regexp"
	"strings"

	"github.com/lsm/open-agent-protocol/go/internal/publicset"
)

const goreleaseVersion = "v0.0.0-20260908205506-85c1c2202aba"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "apidiffcheck:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("apidiffcheck", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	base := flags.String("base", "", "the release tag to compare against (default: the newest v* tag)")
	changelog := flags.String("changelog", "CHANGELOG.md", "the changelog an incompatible change must be recorded in")
	root := flags.String("root", ".", "the module root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !filepath.IsAbs(*changelog) {
		*changelog = filepath.Join(*root, *changelog)
	}
	if err := check(*root, *base, *changelog, stdout); err != nil {
		return err
	}
	return nil
}

func check(root, base, changelogPath string, stdout io.Writer) error {
	module, err := modulePath(root)
	if err != nil {
		return err
	}
	if base == "" {
		if base, err = newestTag(root); err != nil {
			return err
		}
	}
	report, err := gorelease(root, module, base)
	if err != nil {
		return err
	}
	unreleased, err := unreleasedSection(changelogPath)
	if err != nil {
		return err
	}
	missing := unrecorded(report, module, unreleased)
	if len(missing) > 0 {
		return fmt.Errorf("these packages changed incompatibly against %s without the Unreleased section naming them in backticks: %s. Record each one, or say why the break is intended", base, strings.Join(missing, ", "))
	}
	fmt.Fprintf(stdout, "PASS compatibility: no unrecorded incompatible change against %s\n", base)
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

func newestTag(root string) (string, error) {
	out, err := exec.Command("git", "-C", root, "tag", "--list", "v*", "--sort=-v:refname").Output()
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			return line, nil
		}
	}
	return "", errors.New("no v* tag to compare against; pass -base or fetch the tags")
}

func gorelease(root, module, base string) (string, error) {
	command := exec.Command("go", "run", "golang.org/x/exp/cmd/gorelease@"+goreleaseVersion, "-base", module+"@"+base)
	command.Dir = root
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("gorelease against %s: %v: %s", base, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

var sectionHeader = regexp.MustCompile(`^## (incompatible|compatible) changes$`)

func incompatiblePackages(report, module string) []string {
	seen := map[string]bool{}
	var packages []string
	current, incompatible := "", false
	for _, line := range strings.Split(report, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "# "):
			current, incompatible = strings.TrimSpace(strings.TrimPrefix(line, "# ")), false
		case sectionHeader.MatchString(line):
			incompatible = strings.HasPrefix(line, "## incompatible")
		case line != "" && current != "" && incompatible:
			name, found := strings.CutPrefix(current, module+"/")
			if !found || seen[name] || !publicset.Public(name) {
				continue
			}
			seen[name] = true
			packages = append(packages, name)
		}
	}
	return packages
}

func names(section, name string) bool {
	return strings.Contains(section, "`"+name+"`")
}

func unrecorded(report, module, unreleased string) []string {
	var missing []string
	for _, name := range incompatiblePackages(report, module) {
		if !names(unreleased, name) {
			missing = append(missing, name)
		}
	}
	return missing
}

func unreleasedSection(changelogPath string) (string, error) {
	data, err := os.ReadFile(changelogPath)
	if err != nil {
		return "", err
	}
	text := string(data)
	const heading = "## Unreleased"
	start := strings.Index(text, heading)
	if start < 0 {
		return "", fmt.Errorf("%s has no Unreleased section", changelogPath)
	}
	rest := text[start+len(heading):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	return rest, nil
}

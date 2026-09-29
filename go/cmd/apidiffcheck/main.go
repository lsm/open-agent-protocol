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
	description := flags.String("description", "", "a file holding the pull request description, whose Breaking changes section records an incompatible change (default: PR_BODY, which the workflow sets from the event payload)")
	root := flags.String("root", ".", "the module root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *description == "" {
		*description = os.Getenv("PR_BODY")
	}
	var recorded string
	if *description != "" {
		data, err := os.ReadFile(*description)
		if err != nil {
			return fmt.Errorf("reading the pull request description: %w", err)
		}
		recorded = string(data)
	}
	if err := check(*root, *base, recorded, stdout); err != nil {
		return err
	}
	return nil
}

func check(root, base, description string, stdout io.Writer) error {
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
	missing := unrecorded(report, module, recordedChanges(description))
	if len(missing) > 0 {
		return fmt.Errorf("these packages changed incompatibly against %s without the pull request's Breaking changes section naming them in backticks: %s. Add a '## Breaking changes' section to the pull request description naming each one, or say why the break is intended. A package that is gone counts: removing a public package is the most incompatible change there is", base, strings.Join(missing, ", "))
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
			if !found || seen[name] || publicset.Internal(name) {
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

var breakingHeading = regexp.MustCompile(`(?m)^##[ \t]+Breaking changes[ \t]*$`)

func recordedChanges(description string) string {
	if description == "" {
		return ""
	}
	match := breakingHeading.FindStringIndex(description)
	if match == nil {
		return ""
	}
	rest := description[match[1]:]
	if end := regexp.MustCompile(`(?m)^##\s`).FindStringIndex(rest); end != nil {
		rest = rest[:end[0]]
	}
	return rest
}

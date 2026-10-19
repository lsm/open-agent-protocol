package validation

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"
)

var (
	ruleRow  = regexp.MustCompile("^\\| `([^`]+)` \\|")
	ruleID   = regexp.MustCompile(`^(\d{4}|[a-z][a-z-]*)#([a-z0-9_-]+)/[1-9][0-9]*$`)
	backtick = regexp.MustCompile("`([^`]+)`")
	heading  = regexp.MustCompile(`^#{1,4} (.+)$`)
)

type registeredRule struct {
	file  string
	codes []string
}

func headingAnchor(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

func ruleSource(t *testing.T, root, prefix string) string {
	t.Helper()
	if len(prefix) == 4 && strings.Trim(prefix, "0123456789") == "" {
		matches, err := filepath.Glob(filepath.Join(root, "decisions", prefix+"-*.md"))
		if err != nil || len(matches) != 1 {
			return ""
		}
		return matches[0]
	}
	return filepath.Join(root, "drafts", prefix+".md")
}

func documentAnchors(t *testing.T, path string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	anchors := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		if m := heading.FindStringSubmatch(scanner.Text()); m != nil {
			anchors[headingAnchor(m[1])] = true
		}
	}
	return anchors
}

func loadRuleRegister(t *testing.T, root string) map[string]registeredRule {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root, "drafts", "validator-rules", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	rules := map[string]registeredRule{}
	anchors := map[string]map[string]bool{}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(file)
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			m := ruleRow.FindStringSubmatch(line)
			if m == nil || strings.HasPrefix(m[1], "OPEN-") {
				continue
			}
			id := m[1]
			parts := ruleID.FindStringSubmatch(id)
			if parts == nil {
				t.Errorf("%s: rule id %q is not <decision or draft>#<anchor>/<n>", name, id)
				continue
			}
			if previous, ok := rules[id]; ok {
				t.Errorf("%s: rule id %q is already registered in %s", name, id, previous.file)
				continue
			}
			source := ruleSource(t, root, parts[1])
			if anchors[source] == nil {
				anchors[source] = documentAnchors(t, source)
			}
			if anchors[source] == nil {
				t.Errorf("%s: rule %q names no decision or draft %q", name, id, parts[1])
			} else if strings.Trim(parts[2], "0123456789") != "" && !anchors[source][parts[2]] {
				t.Errorf("%s: rule %q names no heading of %s", name, id, filepath.Base(source))
			}
			cells := strings.Split(line, "|")
			var codes []string
			if len(cells) > 5 {
				for _, code := range backtick.FindAllStringSubmatch(cells[4], -1) {
					codes = append(codes, code[1])
				}
			}
			rules[id] = registeredRule{file: name, codes: codes}
		}
	}
	return rules
}

func TestFixturesCiteRegisteredRulesThatDrawTheirCodesOrAreListedUncited(t *testing.T) {
	root := repositoryRoot(t)
	rules := loadRuleRegister(t, root)
	manifest, err := LoadManifest(filepath.Join(root, "fixtures", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "drafts", "validator-rules", "uncited-fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var uncited struct {
		Version  int      `json:"version"`
		Fixtures []string `json:"fixtures"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&uncited); err != nil {
		t.Fatalf("uncited-fixtures.json: %v", err)
	}
	if uncited.Version != 1 || !slices.IsSorted(uncited.Fixtures) || len(slices.Compact(slices.Clone(uncited.Fixtures))) != len(uncited.Fixtures) {
		t.Fatalf("uncited-fixtures.json: want version 1 and sorted, unique ids")
	}
	listed := map[string]bool{}
	for _, id := range uncited.Fixtures {
		listed[id] = true
	}
	for _, entry := range manifest.Fixtures {
		if listed[entry.ID] {
			delete(listed, entry.ID)
			if len(entry.Rules) > 0 {
				t.Errorf("%s cites rules and is still listed uncited; delete it from uncited-fixtures.json", entry.ID)
			}
			continue
		}
		if len(entry.Rules) == 0 {
			t.Errorf("%s cites no rule and is not listed uncited", entry.ID)
			continue
		}
		drawn := map[string]bool{}
		for _, id := range entry.Rules {
			rule, ok := rules[id]
			if !ok {
				t.Errorf("%s cites %q, which drafts/validator-rules does not register", entry.ID, id)
				continue
			}
			for _, code := range rule.codes {
				drawn[code] = true
			}
		}
		for _, code := range entry.Codes {
			if !drawn[code] {
				t.Errorf("%s expects %s, which none of the rules it cites draws", entry.ID, code)
			}
		}
	}
	for id := range listed {
		t.Errorf("uncited-fixtures.json lists %q, which is not a manifest fixture", id)
	}
}

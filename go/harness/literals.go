package harness

import (
	"fmt"
	"go/scanner"
	"go/token"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const CodePinLiteral Code = "pin_literal"

var LiteralRoots = []string{"go", "zig/src", "zig/build.zig"}

type pinValue struct {
	harness string
	version string
	field   string
}

func CheckLiterals(tree fs.FS, catalog Catalog) []Finding {
	values := pinValues(catalog)
	var findings []Finding
	for _, root := range LiteralRoots {
		_ = fs.WalkDir(tree, root, func(name string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			var literals []sourceLiteral
			switch path.Ext(name) {
			case ".go":
				literals = goLiterals(tree, name)
			case ".zig":
				literals = zigLiterals(tree, name)
			default:
				return nil
			}
			for _, literal := range literals {
				if value, pinned := values[literal.text]; pinned {
					findings = append(findings, Finding{Harness: value.harness, Version: value.version, Code: CodePinLiteral, Detail: fmt.Sprintf("%s:%d spells the catalog's %s %q; read it from harnesses/%s.json", name, literal.line, value.field, literal.text, value.harness)})
				}
			}
			return nil
		})
	}
	sort.SliceStable(findings, func(i, j int) bool { return findings[i].Detail < findings[j].Detail })
	return findings
}

func pinValues(catalog Catalog) map[string]pinValue {
	values := map[string]pinValue{}
	add := func(harness, version, field, text string) {
		if text != "" {
			values[text] = pinValue{harness: harness, version: version, field: field}
		}
	}
	for _, harness := range catalog.Harnesses {
		for _, version := range harness.Versions {
			if version.Status == StatusRetired {
				continue
			}
			add(harness.ID, version.Label, "label", version.Label)
			add(harness.ID, version.Label, "endpoint_version", version.EndpointVersion)
			add(harness.ID, version.Label, "capability_revision", version.CapabilityRevision)
			add(harness.ID, version.Label, "corpus", version.Corpus)
			for _, admitted := range version.Admits {
				add(harness.ID, version.Label, "admits", admitted)
			}
			for _, component := range version.Components {
				add(harness.ID, version.Label, "component version", component.Version)
			}
			for _, source := range version.Sources {
				add(harness.ID, version.Label, "source tag", source.Tag)
				add(harness.ID, version.Label, "source commit", source.Commit)
				add(harness.ID, version.Label, "source tree", source.Tree)
			}
			for _, artifact := range version.Artifacts {
				add(harness.ID, version.Label, "artifact digest", artifact.SHA256)
			}
		}
	}
	return values
}

type sourceLiteral struct {
	text string
	line int
}

func goLiterals(tree fs.FS, name string) []sourceLiteral {
	src, err := fs.ReadFile(tree, name)
	if err != nil {
		return nil
	}
	files := token.NewFileSet()
	file := files.AddFile(name, files.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, 0)
	var literals []sourceLiteral
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return literals
		}
		if tok != token.STRING {
			continue
		}
		if text, err := strconv.Unquote(lit); err == nil {
			literals = append(literals, sourceLiteral{text: text, line: files.Position(pos).Line})
		}
	}
}

var zigString = regexp.MustCompile(`"((?:[^"\\\n]|\\.)*)"`)

func zigLiterals(tree fs.FS, name string) []sourceLiteral {
	src, err := fs.ReadFile(tree, name)
	if err != nil {
		return nil
	}
	var literals []sourceLiteral
	var multiline []string
	start := 0
	for i, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if content, ok := strings.CutPrefix(trimmed, `\\`); ok {
			if multiline == nil {
				start = i + 1
			}
			multiline = append(multiline, content)
			continue
		}
		if multiline != nil {
			literals = append(literals, sourceLiteral{text: strings.Join(multiline, "\n"), line: start})
			multiline = nil
		}
		for _, match := range zigString.FindAllStringSubmatch(line, -1) {
			literals = append(literals, sourceLiteral{text: match[1], line: i + 1})
		}
	}
	if multiline != nil {
		literals = append(literals, sourceLiteral{text: strings.Join(multiline, "\n"), line: start})
	}
	return literals
}

package providercatalog

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

const (
	CodeDuplicateID  = "provider_duplicate_id"
	CodeMissingID    = "provider_missing_id"
	CodeEndpointLone = "provider_endpoint_without_wire"
	CodeBaseURLText  = "provider_base_url_literal"
	CodeAliasSelf    = "provider_alias_self"
	CodeOffering     = "provider_offering_unknown"
)

var LiteralRoots = []string{"go", "zig/src", "zig/build.zig"}

type Finding struct {
	Provider string
	Code     string
	Detail   string
}

func (f Finding) Error() string { return f.Detail }

func Check(catalog Catalog) []Finding {
	var findings []Finding
	seen := map[string]struct{}{}
	for _, provider := range catalog.Providers {
		if provider.ID == "" {
			findings = append(findings, Finding{Provider: provider.ID, Code: CodeMissingID, Detail: "a catalogued provider names no id"})
			continue
		}
		if _, repeated := seen[provider.ID]; repeated {
			findings = append(findings, Finding{Provider: provider.ID, Code: CodeDuplicateID, Detail: fmt.Sprintf("provider %q is catalogued twice; one row is the record", provider.ID)})
		}
		seen[provider.ID] = struct{}{}
		for _, endpoint := range provider.Endpoints {
			if endpoint.Wire == "" {
				findings = append(findings, Finding{Provider: provider.ID, Code: CodeEndpointLone, Detail: fmt.Sprintf("provider %q has an endpoint naming no wire", provider.ID)})
			}
		}
		switch provider.Offering {
		case "coding_plan", "api_key", "":
		default:
			findings = append(findings, Finding{Provider: provider.ID, Code: CodeOffering, Detail: fmt.Sprintf("provider %q offers %q, which is neither a coding plan nor an api key", provider.ID, provider.Offering)})
		}
		if provider.AliasOf == provider.ID && provider.ID != "" {
			findings = append(findings, Finding{Provider: provider.ID, Code: CodeAliasSelf, Detail: fmt.Sprintf("provider %q names itself as the row it stands for", provider.ID)})
		}
	}
	sort.SliceStable(findings, func(i, j int) bool { return findings[i].Detail < findings[j].Detail })
	return findings
}

type baseURLValue struct {
	provider string
	wire     string
	region   string
	text     string
}

func catalogBaseUrls(catalog Catalog) map[string]baseURLValue {
	values := map[string]baseURLValue{}
	for _, provider := range catalog.Providers {
		for _, endpoint := range provider.Endpoints {
			if endpoint.BaseURL == "" {
				continue
			}
			values[endpoint.BaseURL] = baseURLValue{
				provider: provider.ID,
				wire:     endpoint.Wire,
				region:   endpoint.Region,
				text:     endpoint.BaseURL,
			}
		}
		for _, origin := range oauthOrigins(provider) {
			values[origin] = baseURLValue{provider: provider.ID, text: origin}
		}
	}
	return values
}

func oauthOrigins(provider Provider) []string {
	if provider.OAuthOrigin == nil {
		return nil
	}
	return provider.OAuthOrigin.Exact
}

func CheckLiterals(tree fs.FS, catalog Catalog) []Finding {
	values := catalogBaseUrls(catalog)
	if len(values) == 0 {
		return nil
	}
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
				if literal.inTest {
					continue
				}
				value, catalogued := values[literal.text]
				if !catalogued {
					continue
				}
				findings = append(findings, Finding{Provider: value.provider, Code: CodeBaseURLText, Detail: fmt.Sprintf("%s:%d spells the catalog's base URL %q for %s; read it from providers/catalog.json", name, literal.line, value.text, describe(value))})
			}
			return nil
		})
	}
	sort.SliceStable(findings, func(i, j int) bool { return findings[i].Detail < findings[j].Detail })
	return findings
}

func describe(value baseURLValue) string {
	if value.wire == "" {
		return value.provider
	}
	if value.region == "" {
		return fmt.Sprintf("%s on %s", value.provider, value.wire)
	}
	return fmt.Sprintf("%s on %s in %s", value.provider, value.wire, value.region)
}

type sourceLiteral struct {
	text   string
	line   int
	inTest bool
}

func goLiterals(tree fs.FS, name string) []sourceLiteral {
	src, err := fs.ReadFile(tree, name)
	if err != nil {
		return nil
	}
	if strings.HasSuffix(name, "_test.go") {
		return nil
	}
	files := token.NewFileSet()
	file := files.AddFile(name, files.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, 0)
	var literals []sourceLiteral
	inTest := false
	depth := 0
	for {
		pos, tok, lit := s.Scan()
		switch tok {
		case token.EOF:
			return literals
		case token.FUNC:
			inTest = false
			depth = 0
			if _, next, text := s.Scan(); next == token.IDENT {
				inTest = strings.HasPrefix(text, "Test")
			}
		case token.LBRACE:
			if inTest {
				depth++
			}
		case token.RBRACE:
			if inTest && depth > 0 {
				depth--
			}
		case token.STRING:
			if inTest {
				continue
			}
			if text, err := strconv.Unquote(lit); err == nil {
				literals = append(literals, sourceLiteral{text: text, line: files.Position(pos).Line})
			}
		}
	}
}

var zigString = regexp.MustCompile(`"((?:[^"\\\n]|\\.)*)"`)

func zigLiterals(tree fs.FS, name string) []sourceLiteral {
	src, err := fs.ReadFile(tree, name)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(src), "\n")
	inTest := zigTestLines(lines)
	var literals []sourceLiteral
	var multiline []string
	start := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if content, ok := strings.CutPrefix(trimmed, `\\`); ok {
			if multiline == nil {
				start = i
			}
			multiline = append(multiline, content)
			continue
		}
		if multiline != nil {
			literals = append(literals, sourceLiteral{text: strings.Join(multiline, "\n"), line: start + 1, inTest: inTest[start]})
			multiline = nil
		}
		for _, match := range zigString.FindAllStringSubmatch(line, -1) {
			literals = append(literals, sourceLiteral{text: match[1], line: i + 1, inTest: inTest[i]})
		}
	}
	if multiline != nil {
		literals = append(literals, sourceLiteral{text: strings.Join(multiline, "\n"), line: start + 1, inTest: inTest[start]})
	}
	return literals
}

func zigTestLines(lines []string) []bool {
	mark := make([]bool, len(lines)+2)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "test ") && trimmed != "test{" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		for j := i; j < len(lines); j++ {
			mark[j] = true
			row := lines[j]
			rowIndent := len(row) - len(strings.TrimLeft(row, " \t"))
			if strings.TrimSpace(row) == "}" && rowIndent == indent {
				break
			}
		}
	}
	return mark
}

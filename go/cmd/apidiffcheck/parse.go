package main

import (
	"strings"

	"github.com/lsm/open-agent-protocol/go/internal/publicset"
)

func incompatible(report, module string) []string {
	var names []string
	for _, block := range strings.Split(report, "\n# ") {
		header, rest, found := strings.Cut(block, "\n")
		if !found {
			continue
		}
		name, isPackage := strings.CutPrefix(header, module+"/")
		if !isPackage {
			name = header
		}
		if publicset.Internal(name) {
			continue
		}
		if !hasIncompatibleEntry(rest) {
			continue
		}
		names = append(names, name)
	}
	return names
}

func hasIncompatibleEntry(rest string) bool {
	_, tail, found := strings.Cut(rest, "Incompatible changes:\n")
	if !found {
		return false
	}
	end := compatibleHeading.FindStringIndex(tail)
	if end != nil {
		tail = tail[:end[0]]
	}
	for _, line := range strings.Split(tail, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") {
			return true
		}
	}
	return false
}

func recordedChanges(description string) string {
	if description == "" {
		return ""
	}
	match := breakingHeading.FindStringIndex(description)
	if match == nil {
		return ""
	}
	rest := description[match[1]:]
	if end := nextHeading.FindStringIndex(rest); end != nil {
		rest = rest[:end[0]]
	}
	return rest
}

package main

import (
	"regexp"
	"strings"
)

var breakingHeading = regexp.MustCompile(`(?m)^##[ \t]+Breaking changes[ \t]*$`)

var nextHeading = regexp.MustCompile(`(?m)^##[ \t]`)

var compatibleHeading = regexp.MustCompile(`(?m)^Compatible changes:`)

func unrecorded(report, module string, packages []string, section string) []string {
	watched := map[string]bool{}
	for _, name := range packages {
		watched[name] = true
	}
	var missing []string
	for _, name := range incompatible(report, module) {
		if !watched[name] {
			continue
		}
		if !strings.Contains(section, "`"+name+"`") {
			missing = append(missing, name)
		}
	}
	return missing
}

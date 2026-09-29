package main

import "regexp"

var breakingHeading = regexp.MustCompile(`(?m)^##[ \t]+Breaking changes[ \t]*$`)

var nextHeading = regexp.MustCompile(`(?m)^##[ \t]`)

var compatibleHeading = regexp.MustCompile(`(?m)^Compatible changes:`)

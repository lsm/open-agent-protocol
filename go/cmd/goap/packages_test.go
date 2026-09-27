package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestTheCheckWalksTheModuleRootAndAcceptsTheTree(t *testing.T) {
	var out bytes.Buffer
	if err := checkGoPackages(&out); err != nil {
		t.Fatalf("the package walk: %v", err)
	}
	if !strings.Contains(out.String(), "PASS go packages") {
		t.Fatalf("output = %q, want the walk to report what it counted", out.String())
	}
}

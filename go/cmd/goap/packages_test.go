package main

import "testing"

func TestAPackageIsNotPublicUntilItIsNamed(t *testing.T) {
	for _, name := range []string{"go/newthing", "go/serve/newbinding", "go/adapter/newharness/internal/native"} {
		if publicGoPackage(name) {
			t.Fatalf("%s is public, want undecided until it is named", name)
		}
	}
	if !internalGoPackage("go/adapter/newharness/internal/native") {
		t.Fatal("an internal element is internal whatever else the path says")
	}
}

func TestThePackagesAGoProgramNeedsArePublic(t *testing.T) {
	for _, name := range []string{
		"go/protocol",
		"go/adapter",
		"go/adapter/claude",
		"go/adapter/acp",
		"go/adapter/codex/appserver",
		"go/adapter/deepseek",
		"go/adapter/hermes",
		"go/adapter/opencode",
		"go/adapter/pi",
		"go/client",
		"go/harness",
		"go/providercatalog",
		"go/serve",
		"go/serve/servehttp",
		"go/serve/servestdio",
		"go/validation",
	} {
		if !publicGoPackage(name) {
			t.Fatalf("%s is not public, want it named: hyperneo-review imports the adapter tree and the protocol", name)
		}
	}
}

func TestTheMovedPackagesAreInternal(t *testing.T) {
	for _, name := range []string{"go/internal/provider", "go/internal/conformance", "go/internal/providertest"} {
		if publicGoPackage(name) {
			t.Fatalf("%s is public, want internal: only goap reads it", name)
		}
	}
}

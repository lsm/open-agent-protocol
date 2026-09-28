package publicset

import "testing"

func TestAPackageIsNotPublicUntilItIsNamed(t *testing.T) {
	for _, name := range []string{"go/newthing", "go/serve/newbinding", "go/adapter/newharness/internal/native", "go/serve/internal", "newroot"} {
		if Public(name) {
			t.Fatalf("%s is public, want undecided until it is named", name)
		}
	}
	for _, name := range []string{"go/adapter/newharness/internal/native", "go/internal", "go/serve/internal", "go/adapter/internal"} {
		if !Internal(name) {
			t.Fatalf("%s is not internal, want an internal element to be internal wherever it sits, including at the end of the path", name)
		}
	}
	for _, name := range []string{"go/serve/internalendpoint", "go/internalish", "go/client"} {
		if Internal(name) {
			t.Fatalf("%s reads as internal, want only an element that is exactly internal", name)
		}
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
		"go/binding",
		"go/harness",
		"go/providercatalog",
		"go/serve",
		"go/serve/servehttp",
		"go/serve/servestdio",
		"go/sdk",
		"go/validation",
		"harnesses",
		"providers",
		"schema",
	} {
		if !Public(name) {
			t.Fatalf("%s is not public, want it named: hyperneo-review imports the adapter tree and the protocol", name)
		}
	}
}

func TestTheMovedPackagesAreInternal(t *testing.T) {
	for _, name := range []string{"go/internal/provider", "go/internal/conformance", "go/internal/providertest"} {
		if Public(name) {
			t.Fatalf("%s is public, want internal: only goap reads it", name)
		}
	}
}

package main

import (
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/publicset"
)

func TestTheCheckWalksTheModuleRoot(t *testing.T) {
	if !publicset.Public("go/adapter") || publicset.Public("go/newthing") {
		t.Fatal("the shared public set is not what this check enforces")
	}
}

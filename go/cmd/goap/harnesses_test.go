package main

import (
	"testing"

	"github.com/lsm/open-agent-protocol/go/harness"
	"github.com/lsm/open-agent-protocol/harnesses"
)

func TestEveryCatalogHarnessHasAGoAdapterPin(t *testing.T) {
	catalog, err := harness.Load(harnesses.Files)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range catalog.Harnesses {
		if _, ok := adapterPins[entry.ID]; !ok {
			t.Errorf("goap check compares no Go adapter against %s", entry.ID)
		}
	}
	if len(adapterPins) != len(catalog.Harnesses) {
		t.Errorf("goap check pins %d adapters against %d harnesses", len(adapterPins), len(catalog.Harnesses))
	}
}

package sdk

import "testing"

func TestAModelEntryCarriesTheFactsItPublished(t *testing.T) {
	client := newTestClient(t, scenarioOAP)
	defer client.Close()

	listed, err := client.Models.List(testContext(t), ListModelsRequest{})
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	if len(listed.Models) == 0 {
		t.Fatal("the listing returned no models")
	}
	model := listed.Models[0]

	if model.Cost == nil {
		t.Fatal("cost is nil for an entry that published one: a quotation that is present must not read as unknown")
	}
	if model.Cost.Input != 3 || model.Cost.Output != 15 {
		t.Errorf("cost input/output = %v/%v, want 3/15", model.Cost.Input, model.Cost.Output)
	}
	if model.Cost.CacheRead != 0.3 || model.Cost.CacheWrite != 3.75 {
		t.Errorf("cost cache read/write = %v/%v, want 0.3/3.75: a fractional rate is a fact, and an int would round it into a different one", model.Cost.CacheRead, model.Cost.CacheWrite)
	}
	if len(model.InputModalities) != 2 || model.InputModalities[0] != "text" || model.InputModalities[1] != "image" {
		t.Errorf("input modalities = %v, want [text image]", model.InputModalities)
	}
	if len(model.ReasoningLevels) != 3 || model.ReasoningLevels[2] != "high" {
		t.Errorf("reasoning levels = %v, want three ending in high", model.ReasoningLevels)
	}
	if model.ReleaseDate != "2025-09-29" {
		t.Errorf("release date = %q, want 2025-09-29", model.ReleaseDate)
	}
	if model.Family != "claude-sonnet" {
		t.Errorf("family = %q, want claude-sonnet", model.Family)
	}
	if listed.Catalog == nil {
		t.Fatal("catalog is nil for a listing that published one")
	}
	if listed.Catalog.ObservedAtMS != 1_759_100_000_000 || !listed.Catalog.Complete {
		t.Errorf("catalog = %+v, want observed 1759100000000 and complete", listed.Catalog)
	}
}

func TestAnAbsentFactStaysAbsentRatherThanBecomingAZero(t *testing.T) {
	client := newTestClient(t, scenarioOAP)
	defer client.Close()

	listed, err := client.Models.List(testContext(t), ListModelsRequest{})
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	if len(listed.Models) == 0 {
		t.Fatal("the listing returned no models")
	}
	model := listed.Models[0]

	if model.OutputModalities != nil {
		t.Errorf("output modalities = %v, want nil: the entry published none, and an absent list is unknown rather than an empty one", model.OutputModalities)
	}
	if model.Cost == nil {
		t.Error("cost is nil for an entry that did publish one, which is the other half of this rule and would mean the mapping dropped it")
	}
}

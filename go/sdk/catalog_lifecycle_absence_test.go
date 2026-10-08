package sdk

import (
	"testing"
)

func lifecycleList(t *testing.T, shape string) (*ListModelsResponse, error) {
	t.Helper()
	client := newTestClient(t, scenarioOAP, "OAPX_TEST_CATALOG_LIFECYCLE="+shape)
	defer client.Close()
	return client.Models.List(testContext(t), ListModelsRequest{})
}

func lifecycleListFilteringDeprecated(t *testing.T, shape string, includeDeprecated bool) (*ListModelsResponse, error) {
	t.Helper()
	client := newTestClient(t, scenarioOAP, "OAPX_TEST_CATALOG_LIFECYCLE="+shape)
	defer client.Close()
	return client.Models.List(testContext(t), ListModelsRequest{IncludeDeprecated: &includeDeprecated})
}

func lifecycleResolve(t *testing.T, shape string) (*ModelDescriptor, error) {
	t.Helper()
	client := newTestClient(t, scenarioOAP, "OAPX_TEST_CATALOG_LIFECYCLE="+shape)
	defer client.Close()
	return client.Models.Resolve(testContext(t), ResolveModelRequest{ModelID: shape})
}

func TestAnOmittedLifecycleIsNilThroughThePublicSeam(t *testing.T) {
	listed, err := lifecycleList(t, "absent")
	if err != nil {
		t.Fatalf("an omitted lifecycle must not fail the list: %v", err)
	}
	if len(listed.Models) == 0 {
		t.Fatal("the listing returned no models")
	}
	for _, model := range listed.Models {
		if model.Lifecycle != nil {
			t.Errorf("Lifecycle = %v, want nil: an omitted lifecycle is unknown, not a chosen value", *model.Lifecycle)
		}
	}
}

func TestAStatedLifecycleKeepsItsMappingThroughThePublicSeam(t *testing.T) {
	for shape, want := range map[string]ModelLifecycle{
		"stable": LifecycleStable, "preview": LifecyclePreview, "deprecated": LifecycleDeprecated,
	} {
		listed, err := lifecycleList(t, shape)
		if err != nil {
			t.Fatalf("a stated lifecycle must list: %v", err)
		}
		if len(listed.Models) != 1 {
			t.Fatalf("the selected scenario must publish exactly one model, got %d", len(listed.Models))
		}
		if listed.Models[0].Lifecycle == nil || *listed.Models[0].Lifecycle != want {
			t.Errorf("%s decoded to %v, want %q", shape, listed.Models[0].Lifecycle, want)
		}
	}
}

func TestAPresentButInvalidLifecycleIsRefusedThroughThePublicSeam(t *testing.T) {
	for shape, what := range map[string]string{
		"null": "an explicit null", "invented": "an invented literal",
		"empty": "an empty string", "wrong-type": "a number",
	} {
		if _, err := lifecycleList(t, shape); err == nil {
			t.Errorf("%s must be refused, not read as an unknown lifecycle", what)
		}
	}
}

func TestResolveSeesTheSameLifecycleAbsenceAndRefusal(t *testing.T) {
	resolved, err := lifecycleResolve(t, "absent")
	if err != nil {
		t.Fatalf("resolve must answer an omitted lifecycle: %v", err)
	}
	if resolved.Lifecycle != nil {
		t.Errorf("Lifecycle = %v, want nil on resolve too", *resolved.Lifecycle)
	}

	if _, err := lifecycleResolve(t, "null"); err == nil {
		t.Error("resolve must refuse an explicit null lifecycle too")
	}
}

func TestAnUnknownLifecycleIsNotFilteredOutAsDeprecated(t *testing.T) {
	listed, err := lifecycleListFilteringDeprecated(t, "absent", false)
	if err != nil {
		t.Fatalf("the list must answer: %v", err)
	}
	if len(listed.Models) != 1 {
		t.Errorf("got %d models, want 1: a model that did not state a lifecycle must not be dropped by the deprecation filter",
			len(listed.Models))
	}

	deprecated, err := lifecycleListFilteringDeprecated(t, "deprecated", false)
	if err != nil {
		t.Fatalf("the list must answer: %v", err)
	}
	if len(deprecated.Models) != 0 {
		t.Errorf("got %d models, want 0: a stated deprecated lifecycle must still be filtered out",
			len(deprecated.Models))
	}

	included, err := lifecycleListFilteringDeprecated(t, "deprecated", true)
	if err != nil {
		t.Fatalf("the list must answer: %v", err)
	}
	if len(included.Models) != 1 {
		t.Errorf("got %d models, want 1: asking for deprecated models must include them", len(included.Models))
	}
}

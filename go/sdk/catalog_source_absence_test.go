package sdk

import (
	"encoding/json"
	"strings"
	"testing"
)

func listSelected(t *testing.T, shape string) (*ListModelsResponse, error) {
	t.Helper()
	client := newTestClient(t, scenarioOAP, "OAPX_TEST_CATALOG_SOURCE="+shape)
	defer client.Close()
	return client.Models.List(testContext(t), ListModelsRequest{})
}

func TestAnOmittedSourceIsNilThroughThePublicSeam(t *testing.T) {
	listed, err := listSelected(t, "absent")
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	if len(listed.Models) != 1 {
		t.Fatalf("the selected scenario must publish exactly one model, got %d", len(listed.Models))
	}
	model, ok := findModel(listed.Models, "absent")
	if !ok {
		t.Fatalf("the selected model is missing from %d listed", len(listed.Models))
	}
	if model.Source != nil {
		t.Errorf("Source = %v, want nil: the entry stated none and nothing may be invented", *model.Source)
	}
}

func TestAStatedSourceKeepsItsMappingThroughThePublicSeam(t *testing.T) {
	for shape, want := range map[string]ModelSource{
		"discovered": SourceDynamic,
		"fallback":   SourceStaticFallback,
	} {
		listed, err := listSelected(t, shape)
		if err != nil {
			t.Fatalf("%s: list models: %v", shape, err)
		}
		model, ok := findModel(listed.Models, shape)
		if !ok {
			t.Fatalf("%s: the selected model is missing", shape)
		}
		if model.Source == nil || *model.Source != want {
			t.Errorf("%s: Source = %v, want %q", shape, model.Source, want)
		}
	}
}

func TestAPresentButInvalidSourceIsRefusedThroughThePublicSeam(t *testing.T) {
	for _, shape := range []string{"invented", "empty", "null", "wrong-type", "shared-alias-dynamic", "shared-alias-static-fallback"} {
		if _, err := listSelected(t, shape); err == nil {
			t.Errorf("%s: a present but invalid source must be refused, not read as absent", shape)
		}
	}
}

func TestResolveSeesTheSameAbsenceAndRefusal(t *testing.T) {
	client := newTestClient(t, scenarioOAP, "OAPX_TEST_CATALOG_SOURCE=absent")
	defer client.Close()
	model, err := client.Models.Resolve(testContext(t), ResolveModelRequest{ModelID: "absent"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if model.Source != nil {
		t.Errorf("resolved Source = %v, want nil", *model.Source)
	}

	bad := newTestClient(t, scenarioOAP, "OAPX_TEST_CATALOG_SOURCE=invented")
	defer bad.Close()
	if _, err := bad.Models.Resolve(testContext(t), ResolveModelRequest{ModelID: "invented"}); err == nil {
		t.Error("resolve must refuse a present but invalid source too")
	}
}

func TestTheSharedResultSeesAMissingSourceAsNilAndStillRejectsABadOne(t *testing.T) {
	capabilities := []string{"chat"}
	raw := wireModelDescriptor{ModelRef: "p/wire@m", ModelID: "m", DisplayName: "M", ProviderID: "p",
		API: "wire", AuthStatus: "authenticated", Lifecycle: json.RawMessage(`"stable"`), Capabilities: &capabilities}
	model, err := parseModelDescriptor(raw, 0, "s")
	if err != nil {
		t.Fatalf("a descriptor with no source must decode as unknown, not fail: %v", err)
	}
	if model.Source != nil {
		t.Errorf("Source = %v, want nil", *model.Source)
	}
	bogus := json.RawMessage(`"invented-source"`)
	raw.Source = bogus
	_, err = parseModelDescriptor(raw, 0, "stream-7")
	if err == nil {
		t.Error("an invalid stated source must still be rejected, not defaulted")
	} else {
		protocol, isProtocol := err.(*ProtocolError)
		if !isProtocol {
			t.Fatalf("error is %T, want *ProtocolError", err)
		}
		if !strings.Contains(protocol.Message, "models[0].source has unknown value") {
			t.Errorf("Message = %q, want the field path and the offending value", protocol.Message)
		}
		if strings.Contains(protocol.Message, "oap sdk") {
			t.Errorf("Message = %q, must carry the field path, not the rendered error", protocol.Message)
		}
		if protocol.StreamID != "stream-7" {
			t.Errorf("StreamID = %q, want stream-7: the closure must keep it", protocol.StreamID)
		}
	}
	raw.Source = json.RawMessage(`null`)
	if _, err := parseModelDescriptor(raw, 0, "s"); err == nil {
		t.Error("an explicit null source must be rejected, not read as absent")
	}
	raw.Source = json.RawMessage(`"static_fallback"`)
	stated, err := parseModelDescriptor(raw, 0, "s")
	if err != nil {
		t.Fatalf("a stated source must still decode: %v", err)
	}
	if stated.Source == nil || *stated.Source != SourceStaticFallback {
		t.Errorf("stated Source = %v, want %q", stated.Source, SourceStaticFallback)
	}
}

func findModel(models []ModelDescriptor, modelID string) (ModelDescriptor, bool) {
	for _, model := range models {
		if model.ModelID == modelID {
			return model, true
		}
	}
	return ModelDescriptor{}, false
}

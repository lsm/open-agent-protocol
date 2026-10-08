package sdk

import (
	"context"
	"errors"
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

func TestEachAuthSelectorActuallyChangesTheRow(t *testing.T) {
	for _, want := range []AuthStatus{AuthAuthenticated, AuthLoginRequired, AuthExpired,
		AuthRefreshing, AuthLoginInProgress, AuthFailed, AuthUnknown} {
		client := newTestClient(t, scenarioOAP, "OAPX_TEST_CATALOG_AUTH="+string(want))
		listed, err := client.Models.List(testContext(t), ListModelsRequest{IncludeLoginRequired: boolPtr(true)})
		if err != nil {
			t.Fatalf("auth selector %q must list: %v", want, err)
		}
		if len(listed.Models) != 1 {
			t.Fatalf("auth selector %q must publish exactly one row, got %d", want, len(listed.Models))
		}
		if listed.Models[0].AuthStatus != want {
			t.Errorf("auth selector %q produced AuthStatus %q: the selector did not take effect",
				want, listed.Models[0].AuthStatus)
		}
	}
}

func TestEachMalformedAuthSelectorIsRefused(t *testing.T) {
	for _, shape := range []string{"null", "number", "invented"} {
		client := newTestClient(t, scenarioOAP, "OAPX_TEST_CATALOG_AUTH="+shape)
		if _, err := client.Models.List(testContext(t), ListModelsRequest{IncludeLoginRequired: boolPtr(true)}); err == nil {
			t.Fatalf("auth selector %q must be refused", shape)
		}
	}
}

func TestAnUnsupportedAuthSelectorFailsTheListing(t *testing.T) {
	for _, selector := range []string{"stated:expired", "no-such-selector", "expired "} {
		func() {
			t.Setenv(EnvBinaryPath, "")
			t.Setenv(EnvBinaryURL, "")
			opts := &Options{
				BinaryPath: osArgsZero(),
				Args:       []string{},
				Env:        fakeHostEnv(scenarioOAP, "OAPX_TEST_CATALOG_AUTH="+selector),
			}
			client, err := New(context.Background(), opts)
			if err != nil {
				return
			}
			defer func() {
				if closeErr := client.Close(); closeErr == nil {
					t.Errorf("auth selector %q must not let the fake host exit clean", selector)
				}
			}()
			listed, listErr := client.Models.List(testContext(t), ListModelsRequest{IncludeLoginRequired: boolPtr(true)})
			if listErr == nil {
				t.Errorf("auth selector %q must not yield a listing, got %d models", selector, len(listed.Models))
			}
		}()
	}
}

func TestAMalformedPresentAuthStatusIsRefusedEvenWhenAFilterWouldDropTheRow(t *testing.T) {
	for _, shape := range []string{"null", "number", "invented"} {
		client := newTestClient(t, scenarioOAP, "OAPX_TEST_CATALOG_AUTH="+shape)
		_, err := client.Models.List(testContext(t), ListModelsRequest{API: "no-such-wire"})
		if err == nil {
			t.Fatalf("auth selector %q must be refused before a filter drops its row", shape)
		}
		var protoErr *ProtocolError
		if !errors.As(err, &protoErr) || protoErr.Code != CodeMalformedResponse {
			t.Errorf("auth selector %q refused with %v, want malformed_response", shape, err)
		}
	}
}

func TestAnOmittedAuthStatusReachesTheRowWithNoValueAtAll(t *testing.T) {
	client := newTestClient(t, scenarioOAP, "OAPX_TEST_CATALOG_AUTH=absent")
	defer client.Close()
	listed, err := client.Models.List(testContext(t), ListModelsRequest{IncludeLoginRequired: boolPtr(true)})
	if err != nil {
		t.Fatalf("an omitted auth_status must not be refused: %v", err)
	}
	if len(listed.Models) != 1 {
		t.Fatalf("an omitted auth_status must publish one row, got %d", len(listed.Models))
	}
	if got := listed.Models[0].AuthStatus; got != AuthUnknown {
		t.Errorf("an omitted auth_status read as %q, want the unknown value", got)
	}
}

func TestAnAuthOnlyShapeStillNamesItsRow(t *testing.T) {
	client := newTestClient(t, scenarioOAP, "OAPX_TEST_CATALOG_AUTH=login_required")
	defer client.Close()
	listed, err := client.Models.List(testContext(t), ListModelsRequest{IncludeLoginRequired: boolPtr(true)})
	if err != nil {
		t.Fatalf("an auth-only shape must still list its row: %v", err)
	}
	if len(listed.Models) != 1 {
		t.Fatalf("an auth-only shape must publish exactly one row, got %d", len(listed.Models))
	}
	model := listed.Models[0]
	if model.ModelID == "" {
		t.Error("an auth-only shape must give the row a model_id, not an empty one")
	}
	if model.ModelRef == "" || !strings.Contains(model.ModelRef, model.ModelID) {
		t.Errorf("model_ref %q must be coherent with model_id %q", model.ModelRef, model.ModelID)
	}
	if model.AuthStatus != AuthLoginRequired {
		t.Errorf("AuthStatus = %q, want %q", model.AuthStatus, AuthLoginRequired)
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

func findModel(models []ModelDescriptor, modelID string) (ModelDescriptor, bool) {
	for _, model := range models {
		if model.ModelID == modelID {
			return model, true
		}
	}
	return ModelDescriptor{}, false
}

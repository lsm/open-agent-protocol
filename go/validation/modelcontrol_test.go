package validation

import (
	"encoding/json"
	"testing"
)

func modelControlEnvelope(typ, id, reply, session string, payload any) map[string]any {
	e := map[string]any{
		"protocol": "open-agent-protocol", "version": "0.1",
		"profile": "open-agent-protocol.agent-control-core",
		"type":    typ, "id": id, "payload": payload,
	}
	if reply != "" {
		e["in_reply_to"] = reply
	}
	if session != "" {
		e["session_id"] = session
	}
	if typ != "capabilities.request" {
		e["capability_revision"] = "rev-1"
	}
	return e
}

func modelControlStart(features map[string]any) []map[string]any {
	return []map[string]any{
		modelControlEnvelope("capabilities.request", "caps-req", "", "", map[string]any{}),
		modelControlEnvelope("capabilities.response", "caps-resp", "caps-req", "", map[string]any{
			"endpoint": map[string]any{"id": "fixture"}, "features": features,
		}),
		modelControlEnvelope("session.open.request", "open-req", "", "s1", map[string]any{"session_id": "s1"}),
		modelControlEnvelope("session.open.response", "open-resp", "open-req", "s1", map[string]any{
			"session_id": "s1", "status": "idle", "current_model_id": "m1",
		}),
	}
}

func modelControlCatalog(req, resp, current string, models []map[string]any, providers []map[string]any) []map[string]any {
	payload := map[string]any{"session_id": "s1", "models": models}
	if current != "" {
		payload["current_model_id"] = current
	}
	if providers != nil {
		payload["providers"] = providers
	}
	return []map[string]any{
		modelControlEnvelope("models.request", req, "", "s1", map[string]any{"session_id": "s1"}),
		modelControlEnvelope("models.response", resp, req, "s1", payload),
	}
}

func validateModelControl(t *testing.T, trace []map[string]any) Result {
	t.Helper()
	data, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	return MustNew().ValidateBytes(data, t.Name())
}

func TestCoreModelSwitchPersistsAndAnchorsCatalog(t *testing.T) {
	features := map[string]any{
		"models.list":          map[string]any{"level": "native"},
		"session.model.switch": map[string]any{"level": "native"},
	}
	models := []map[string]any{{"id": "m1"}, {"id": "m2"}}
	trace := modelControlStart(features)
	trace = append(trace, modelControlCatalog("models-req-1", "models-resp-1", "m1", models, nil)...)
	trace = append(trace,
		modelControlEnvelope("session.model.switch.request", "switch-req", "", "s1", map[string]any{"session_id": "s1", "model_id": "m2"}),
		modelControlEnvelope("session.model.switch.response", "switch-resp", "switch-req", "s1", map[string]any{"session_id": "s1", "model_id": "m2", "previous_model_id": "m1"}),
	)
	state := modelControlEnvelope("session.state.updated", "state-ev", "", "s1", map[string]any{"session_id": "s1", "status": "idle", "current_model_id": "m2"})
	state["sequence"] = 1
	trace = append(trace, state)
	trace = append(trace, modelControlCatalog("models-req-2", "models-resp-2", "m2", models, nil)...)
	trace[len(trace)-1]["payload"].(map[string]any)["as_of_model_event"] = map[string]any{"switch_request_id": "switch-req"}
	if result := validateModelControl(t, trace); !result.Valid() {
		t.Fatalf("valid switch diagnosed: %+v", result.Diagnostics)
	}
}

func TestCoreModelSwitchRejectsUnlistedAdmission(t *testing.T) {
	features := map[string]any{"models.list": map[string]any{"level": "native"}}
	trace := modelControlStart(features)
	trace = append(trace, modelControlCatalog("models-req", "models-resp", "m1", []map[string]any{{"id": "m1"}}, nil)...)
	trace = append(trace,
		modelControlEnvelope("session.model.switch.request", "switch-req", "", "s1", map[string]any{"session_id": "s1", "model_id": "absent"}),
		modelControlEnvelope("session.model.switch.response", "switch-resp", "switch-req", "s1", map[string]any{"session_id": "s1", "model_id": "absent"}),
	)
	if result := validateModelControl(t, trace); !result.HasCode(CodeModelNotInCatalog) {
		t.Fatalf("unlisted switch was not diagnosed: %+v", result.Diagnostics)
	}
}

func TestCoreModelSwitchResponseKeepsRequestSession(t *testing.T) {
	trace := modelControlStart(map[string]any{
		"session.model.switch": map[string]any{"level": "native"},
	})
	trace = append(trace,
		modelControlEnvelope("session.model.switch.request", "switch-req", "", "s1", map[string]any{"session_id": "s1", "model_id": "m1"}),
		modelControlEnvelope("session.model.switch.response", "switch-resp", "switch-req", "s2", map[string]any{"session_id": "s2", "model_id": "m1"}),
	)
	if result := validateModelControl(t, trace); !result.HasCode(CodeScopeMismatch) {
		t.Fatalf("cross-session switch response was not diagnosed: %+v", result.Diagnostics)
	}
}

func TestLiveProviderAttachInvalidatesOnlyItsSessionCatalog(t *testing.T) {
	features := map[string]any{
		"models.list":             map[string]any{"level": "native"},
		"action.providers.attach": map[string]any{"level": "native", "modes": []string{"session_live"}},
	}
	trace := modelControlStart(features)
	trace = append(trace, modelControlCatalog("models-req-1", "models-resp-1", "m1",
		[]map[string]any{{"id": "m1", "provider_id": "p1"}}, []map[string]any{{"id": "p1"}})...)
	trace = append(trace,
		modelControlEnvelope("session.provider.attach.request", "attach-req", "", "s1", map[string]any{
			"session_id": "s1", "provider": map[string]any{"id": "p2", "provider_id": "upstream-p2"},
		}),
		modelControlEnvelope("session.provider.attach.response", "attach-resp", "attach-req", "s1", map[string]any{"session_id": "s1", "provider_id": "p2"}),
	)
	trace = append(trace, modelControlCatalog("models-req-2", "models-resp-2", "m1",
		[]map[string]any{{"id": "m1", "provider_id": "p1"}, {"id": "m2", "provider_id": "p2"}},
		[]map[string]any{{"id": "p1"}, {"id": "p2", "upstream_provider_id": "upstream-p2"}})...)
	if result := validateModelControl(t, trace); !result.Valid() {
		t.Fatalf("accepted attach diagnosed: %+v", result.Diagnostics)
	}
}

func TestProviderOnlyCatalogChangeRequiresAcceptedAttach(t *testing.T) {
	features := map[string]any{
		"models.list":             map[string]any{"level": "native"},
		"action.providers.attach": map[string]any{"level": "native", "modes": []string{"session_live"}},
	}
	models := []map[string]any{{"id": "m1", "provider_id": "p1"}}
	first := modelControlCatalog("models-req-1", "models-resp-1", "m1", models, []map[string]any{{"id": "p1"}})
	second := modelControlCatalog("models-req-2", "models-resp-2", "m1", models, []map[string]any{{"id": "p1"}, {"id": "p2", "upstream_provider_id": "upstream-p2"}})

	unannounced := modelControlStart(features)
	unannounced = append(unannounced, first...)
	unannounced = append(unannounced, second...)
	if result := validateModelControl(t, unannounced); !result.HasCode(CodeUnannouncedCatalogChange) {
		t.Fatalf("provider-only catalog change was not diagnosed: %+v", result.Diagnostics)
	}

	attached := modelControlStart(features)
	attached = append(attached, first...)
	attached = append(attached,
		modelControlEnvelope("session.provider.attach.request", "attach-req", "", "s1", map[string]any{
			"session_id": "s1", "provider": map[string]any{"id": "p2", "provider_id": "upstream-p2"},
		}),
		modelControlEnvelope("session.provider.attach.response", "attach-resp", "attach-req", "s1", map[string]any{"session_id": "s1", "provider_id": "p2"}),
	)
	attached = append(attached, second...)
	if result := validateModelControl(t, attached); !result.Valid() {
		t.Fatalf("accepted attach did not exempt provider-only catalog expansion: %+v", result.Diagnostics)
	}
}

func TestUnadvertisedProviderAttachCannotBeAccepted(t *testing.T) {
	trace := modelControlStart(map[string]any{})
	trace = append(trace,
		modelControlEnvelope("session.provider.attach.request", "attach-req", "", "s1", map[string]any{
			"session_id": "s1", "provider": map[string]any{"id": "p2", "provider_id": "upstream-p2"},
		}),
		modelControlEnvelope("session.provider.attach.response", "attach-resp", "attach-req", "s1", map[string]any{"session_id": "s1", "provider_id": "p2"}),
	)
	if result := validateModelControl(t, trace); !result.HasCode(CodeUnavailableCapability) {
		t.Fatalf("unadvertised attach was not diagnosed: %+v", result.Diagnostics)
	}
}

func TestProviderAttachResponseKeepsRequestSession(t *testing.T) {
	trace := modelControlStart(map[string]any{
		"action.providers.attach": map[string]any{"level": "native", "modes": []string{"session_live"}},
	})
	trace = append(trace,
		modelControlEnvelope("session.provider.attach.request", "attach-req", "", "s1", map[string]any{
			"session_id": "s1", "provider": map[string]any{"id": "p2", "provider_id": "upstream-p2"},
		}),
		modelControlEnvelope("session.provider.attach.response", "attach-resp", "attach-req", "s2", map[string]any{"session_id": "s2", "provider_id": "p2"}),
	)
	if result := validateModelControl(t, trace); !result.HasCode(CodeScopeMismatch) {
		t.Fatalf("cross-session attach response was not diagnosed: %+v", result.Diagnostics)
	}
}

func TestProviderAttachCannotReuseServedProviderAlias(t *testing.T) {
	for _, tc := range []struct {
		name      string
		current   string
		models    []map[string]any
		providers []map[string]any
	}{
		{name: "provider without models", models: []map[string]any{}, providers: []map[string]any{{"id": "p1"}}},
		{name: "model attributed provider", current: "m1", models: []map[string]any{{"id": "m1", "provider_id": "p1"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trace := modelControlStart(map[string]any{
				"models.list":             map[string]any{"level": "native"},
				"action.providers.attach": map[string]any{"level": "native", "modes": []string{"session_live"}},
			})
			trace = append(trace, modelControlCatalog("models-req", "models-resp", tc.current, tc.models, tc.providers)...)
			trace = append(trace,
				modelControlEnvelope("session.provider.attach.request", "attach-req", "", "s1", map[string]any{
					"session_id": "s1", "provider": map[string]any{"id": "p1", "provider_id": "different-upstream"},
				}),
				modelControlEnvelope("session.provider.attach.response", "attach-resp", "attach-req", "s1", map[string]any{"session_id": "s1", "provider_id": "p1"}),
			)
			if result := validateModelControl(t, trace); !result.HasCode(CodeDuplicateProvider) {
				t.Fatalf("served provider alias collision was not diagnosed: %+v", result.Diagnostics)
			}
		})
	}
}

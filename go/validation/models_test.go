package validation

import (
	"slices"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

func modelsTrace(features, query, answer string, rest ...string) []byte {
	descriptor := `{` + controlsCore + `,"type":"capabilities.response","id":"caps-resp","in_reply_to":"caps-req","capability_revision":"rev-1","payload":{"endpoint":{"id":"fixture"},"features":` + features + `}}`
	envelopes := []string{
		`{` + controlsCore + `,"type":"capabilities.request","id":"caps-req","payload":{}}`,
		descriptor,
	}
	if query != "" {
		envelopes = append(envelopes, query)
	}
	if answer != "" {
		envelopes = append(envelopes, answer)
	}
	envelopes = append(envelopes, rest...)
	return []byte(`[` + strings.Join(envelopes, ",") + `]`)
}

func modelsQuery(optin string) string {
	payload := `{"session_id":"s1"`
	if optin != "" {
		payload += `,"allow_degraded_features":` + optin
	}
	payload += `}`
	return `{` + controlsCore + `,"type":"models.request","id":"models-req","session_id":"s1","capability_revision":"rev-1","payload":` + payload + `}`
}

func modelsCatalog(body string) string {
	return `{` + controlsCore + `,"type":"models.response","id":"models-resp","in_reply_to":"models-req","session_id":"s1","capability_revision":"rev-1","payload":{"session_id":"s1",` + body + `}}`
}

func modelSubmit(model string) []string {
	return []string{
		`{` + controlsCore + `,"type":"session.message.submit.request","id":"submit-req","session_id":"s1","capability_revision":"rev-1","payload":{"session_id":"s1","messages":[{"role":"user","content":"go"}],"delivery":"auto","model_id":"` + model + `"}}`,
		`{` + controlsCore + `,"type":"session.message.submit.response","id":"submit-resp","in_reply_to":"submit-req","session_id":"s1","run_id":"r1","capability_revision":"rev-1","payload":{"session_id":"s1","accepted":true,"submission_id":"sub1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"r1","status":"running","model_id":"` + model + `"}}`,
		`{` + controlsCore + `,"type":"run.started","id":"ev-start","session_id":"s1","run_id":"r1","sequence":1,"capability_revision":"rev-1","payload":{"session_id":"s1","run_id":"r1","status":"running","model_id":"` + model + `"}}`,
		`{` + controlsCore + `,"type":"run.completed","id":"ev-done","session_id":"s1","run_id":"r1","sequence":2,"capability_revision":"rev-1","payload":{"session_id":"s1","run_id":"r1","final_response":{"role":"assistant","content":"ok"},"stop_reason":"end_turn","model_id":"` + model + `"}}`,
	}
}

const modelsNative = `{"models.list":{"level":"native"},"run.model_selection":{"level":"emulated","mode":"per_run"},"session.message.submit":{"level":"native"},"session.message.delivery.auto":{"level":"native"},"session.state":{"level":"native"}}`

func TestDegradedCatalogDoesNotBindAdmissions(t *testing.T) {
	v := MustNew()
	degraded := `{"models.list":{"level":"degraded","reason":"refreshed per turn"},"run.model_selection":{"level":"emulated","mode":"per_run"},"session.message.submit":{"level":"native"},"session.message.delivery.auto":{"level":"native"}}`
	trace := modelsTrace(degraded, modelsQuery(`["models.list"]`), modelsCatalog(`"current_model_id":"m1","models":[{"id":"m1","default":true}]`), modelSubmit("m2")...)
	if got := v.ValidateBytes(trace, "degraded-catalog"); !got.Valid() {
		t.Fatalf("a degraded catalog bound an admission: %+v", got.Diagnostics)
	}

	bound := modelsTrace(modelsNative, modelsQuery(""), modelsCatalog(`"current_model_id":"m1","models":[{"id":"m1","default":true}]`), modelSubmit("m2")...)
	if got := v.ValidateBytes(bound, "native-catalog"); !got.HasCode(CodeModelNotInCatalog) {
		t.Fatalf("want %s when a native catalog omits the admitted model: %+v", CodeModelNotInCatalog, got.Diagnostics)
	}
}

func TestDegradedCatalogMayChangeWithinARevision(t *testing.T) {
	v := MustNew()
	second := strings.NewReplacer("models-req", "models-req2", "models-resp", "models-resp2")
	degraded := `{"models.list":{"level":"degraded","reason":"refreshed per turn"},"session.message.submit":{"level":"native"}}`
	first := modelsCatalog(`"current_model_id":"m1","models":[{"id":"m1","default":true}]`)
	changed := second.Replace(modelsCatalog(`"current_model_id":"m1","models":[{"id":"m1","default":true},{"id":"m2"}]`))
	trace := modelsTrace(degraded, modelsQuery(`["models.list"]`), first, second.Replace(modelsQuery(`["models.list"]`)), changed)
	if got := v.ValidateBytes(trace, "degraded-change"); !got.Valid() {
		t.Fatalf("a disclosed per-turn refresh was diagnosed: %+v", got.Diagnostics)
	}
}

func TestUnreportedSessionModelContradictsNothing(t *testing.T) {
	v := MustNew()
	trace := modelsTrace(modelsNative, modelsQuery(""), modelsCatalog(`"current_model_id":"m1","models":[{"id":"m1","default":true}]`))
	if got := v.ValidateBytes(trace, "unreported-model"); !got.Valid() {
		t.Fatalf("a catalog was judged against a model the trace never reported: %+v", got.Diagnostics)
	}
}

func TestEmptyCatalogIsWellFormed(t *testing.T) {
	v := MustNew()
	trace := modelsTrace(modelsNative, modelsQuery(""), modelsCatalog(`"models":[]`))
	if got := v.ValidateBytes(trace, "empty-catalog"); !got.Valid() {
		t.Fatalf("an empty catalog was diagnosed: %+v", got.Diagnostics)
	}
}

func TestUnreachedCatalogPositionIsNotDiagnosed(t *testing.T) {
	v := MustNew()
	answer := modelsCatalog(`"current_model_id":"m9","models":[{"id":"m9","default":true}],"as_of_model_event":{"run_id":"r7","sequence":4}`)
	trace := modelsTrace(modelsNative, modelsQuery(""), answer)
	if got := v.ValidateBytes(trace, "unreached-position"); !got.Valid() {
		t.Fatalf("a catalog ahead of the trace was diagnosed: %+v", got.Diagnostics)
	}
}

func TestModelsDiagnosticsAreRegistered(t *testing.T) {
	codes := diagnosticCodes()
	for _, code := range []string{CodeModelNotInCatalog, CodeAmbiguousDefaultModel, CodeDuplicateModelID, CodeUnannouncedCatalogChange} {
		if !codes[code] {
			t.Fatalf("diagnostic %q is not registered, so no fixture may declare it", code)
		}
	}
}

func TestModelsCapabilitiesAreRegistered(t *testing.T) {
	owned := unitCapabilities["models"]
	if !slices.Contains(owned, protocol.FeatureModelsList) {
		t.Fatalf("the models unit does not own %q: %v", protocol.FeatureModelsList, owned)
	}
	if len(honourDeferred) != 0 {
		t.Fatalf("no key's honour aspect is deferred any more: %v", honourDeferred)
	}
}

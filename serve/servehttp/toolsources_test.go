package servehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
	"github.com/lsm/open-agent-protocol/validation"
)

// recordingAdapter keeps the open request the daemon forwarded, so the
// credential rule is proved against what the adapter actually received rather
// than against what the route was handed.
type recordingAdapter struct{ request base.OpenRequest }

func (a *recordingAdapter) Probe(ctx context.Context) (base.Descriptor, error) {
	return base.NewMemory(base.Config{}).Probe(ctx)
}

func (a *recordingAdapter) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	a.request = request
	// The reference adapter refuses an unknown transport, and the operator's
	// source is a process one, so the recorded open is admitted on its own
	// terms by opening a plain reference session beside it.
	return base.NewMemory(base.Config{}).Open(ctx, base.OpenRequest{SessionID: request.SessionID, Participant: request.Participant})
}

// registryWithToolSource returns a registry carrying one operator-configured
// process source, which is the only way a wire caller can attach one.
func registryWithToolSource(t *testing.T) *serve.Registry {
	t.Helper()
	registry := memoryRegistry(64)
	if err := registry.RegisterToolSource("workspace-files", protocol.ToolSourceAttachment{
		Kind: protocol.ToolSourceProcess, Protocol: protocol.ToolSourceMCP,
		DisplayName: "Workspace Files", Endpoint: "stdio:workspace-files",
		Command: "/usr/local/bin/mcp-filesystem", Args: []string{"--root", "/workspace"},
		Environment: []string{"MCP_TOKEN=operator-secret"},
	}); err != nil {
		t.Fatal(err)
	}
	return registry
}

func openSessionWith(t *testing.T, server *httptest.Server, id string, request protocol.SessionOpenRequest) (int, protocol.Envelope) {
	t.Helper()
	request.SessionID = protocol.SessionID(id)
	return postEnvelope(t, server, "/adapters/memory/sessions", requestEnvelope(t, protocol.TypeSessionOpenRequest, "open-"+id, request, id, "", ""))
}

// TestToolsRouteServesTheSessionCatalog drives the new route end to end: the
// catalog arrives as a correlated action.tools.list.response naming the
// session, the attached source is listed with the members the operator
// configured, and every tool resolves to a declared source.
func TestToolsRouteServesTheSessionCatalog(t *testing.T) {
	_, server := newServer(t, registryWithToolSource(t), Options{})
	status, opened := openSessionWith(t, server, "catalog", protocol.SessionOpenRequest{
		ToolSources: []protocol.ToolSourceAttachment{{ID: "workspace-files", Kind: protocol.ToolSourceProcess}},
	})
	if status != http.StatusOK {
		t.Fatalf("open status %d: %s", status, opened.Payload)
	}
	var openResponse protocol.SessionOpenResponse
	if err := opened.DecodePayload(&openResponse); err != nil {
		t.Fatal(err)
	}
	// The open response publishes the sanitized projection: the operator's
	// command and its literal environment are nowhere in it.
	raw, _ := json.Marshal(openResponse)
	if bytes.Contains(raw, []byte("operator-secret")) || bytes.Contains(raw, []byte("mcp-filesystem")) {
		t.Fatalf("the open response leaked an attachment-only member: %s", raw)
	}
	found := false
	for _, source := range openResponse.Sources {
		if source.ID == "workspace-files" {
			found = true
			if source.Endpoint != "stdio:workspace-files" {
				t.Fatalf("attached source published as %+v", source)
			}
		}
	}
	if !found {
		t.Fatalf("the open response omits the attached source: %+v", openResponse.Sources)
	}

	var envelope protocol.Envelope
	if status := getJSON(t, server, "/sessions/catalog/tools", &envelope); status != http.StatusOK {
		t.Fatalf("tools status %d", status)
	}
	if envelope.Type != protocol.TypeActionToolsListResponse || envelope.SessionID != "catalog" {
		t.Fatalf("tools response %s scoped to %q", envelope.Type, envelope.SessionID)
	}
	// The catalog is bound to the descriptor snapshot that governs it. It comes
	// back with the listing rather than from a descriptor read at another
	// moment: a listing a caller cannot bind to a revision can be neither cached
	// nor invalidated by capabilities.updated, and the schema requires the field
	// on this response as it does on the models one.
	if envelope.CapabilityRevision != base.CapabilityRevision {
		t.Fatalf("tools response carries revision %q, want %q", envelope.CapabilityRevision, base.CapabilityRevision)
	}
	var catalog protocol.ToolsListResponse
	if err := envelope.DecodePayload(&catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.SessionID != "catalog" {
		t.Fatalf("catalog payload names session %q", catalog.SessionID)
	}
	declared := map[string]bool{}
	for _, source := range catalog.Sources {
		declared[source.ID] = true
	}
	if !declared["workspace-files"] {
		t.Fatalf("the session catalog omits the attached source: %+v", catalog.Sources)
	}
	for _, tool := range catalog.Tools {
		if tool.Source == "" || !declared[tool.Source] {
			t.Fatalf("catalog entry %q names source %q, which no declared source matches", tool.Name, tool.Source)
		}
	}
}

// TestToolsRouteRefusesAnEndpointWithNoCatalog pins the typed refusal: an
// adapter that serves no portable catalog says which capability to stop
// requesting rather than failing generically.
func TestToolsRouteRefusesAnEndpointWithNoCatalog(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("memory", catalogLessAdapter{base.NewMemory(base.Config{})}); err != nil {
		t.Fatal(err)
	}
	_, server := newServer(t, registry, Options{})
	if status, _ := openSessionWith(t, server, "bare", protocol.SessionOpenRequest{}); status != http.StatusOK {
		t.Fatalf("open status %d", status)
	}
	var envelope protocol.Envelope
	if status := getJSON(t, server, "/sessions/bare/tools", &envelope); status != http.StatusBadRequest {
		t.Fatalf("tools status %d, want 400", status)
	}
	var failure protocol.ErrorResponse
	if err := envelope.DecodePayload(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != "unsupported_feature" {
		t.Fatalf("refusal code %q", failure.Error.Code)
	}
	if failure.Error.Details["feature"] != protocol.FeatureToolsList || failure.Error.Details["reason"] != base.ControlUnadvertised {
		t.Fatalf("refusal details %+v", failure.Error.Details)
	}
}

// catalogLessAdapter hides the reference adapter's optional catalog surface,
// so the boundary's own refusal is what the test observes.
type catalogLessAdapter struct{ *base.Memory }

func (a catalogLessAdapter) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	session, err := a.Memory.Open(ctx, request)
	if err != nil {
		return nil, err
	}
	return catalogLessSession{session}, nil
}

type catalogLessSession struct{ base.Session }

// TestDaemonRefusesWireSuppliedProcessCredentials is the T3b binding rule:
// "loopback, single-user" describes the transport, not the origin of a
// request on it, so the route that a page in the user's browser can reach
// accepts no command, no arguments, and no literal environment value — only
// an operator-configured id.
func TestDaemonRefusesWireSuppliedProcessCredentials(t *testing.T) {
	cases := []struct {
		name       string
		attachment protocol.ToolSourceAttachment
	}{
		{"a command", protocol.ToolSourceAttachment{ID: "workspace-files", Kind: protocol.ToolSourceProcess, Command: "/bin/sh"}},
		{"arguments", protocol.ToolSourceAttachment{ID: "workspace-files", Kind: protocol.ToolSourceProcess, Args: []string{"-c", "id"}}},
		{"a literal environment value", protocol.ToolSourceAttachment{ID: "workspace-files", Kind: protocol.ToolSourceProcess, Environment: []string{"LD_PRELOAD=/tmp/evil.so"}}},
		{"an unconfigured id", protocol.ToolSourceAttachment{ID: "never-configured", Kind: protocol.ToolSourceProcess}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			hub, server := newServer(t, registryWithToolSource(t), Options{})
			status, envelope := openSessionWith(t, server, "refused", protocol.SessionOpenRequest{
				ToolSources: []protocol.ToolSourceAttachment{testCase.attachment},
			})
			if status != http.StatusBadRequest {
				t.Fatalf("open status %d, want 400: %s", status, envelope.Payload)
			}
			var failure protocol.ErrorResponse
			if err := envelope.DecodePayload(&failure); err != nil {
				t.Fatal(err)
			}
			if failure.Error.Code != "unsupported_feature" {
				t.Fatalf("refusal code %q", failure.Error.Code)
			}
			if failure.Error.Details["feature"] != protocol.FeatureToolSourcesAttach ||
				failure.Error.Details["reason"] != base.ControlUnsatisfiable ||
				failure.Error.Details["source"] != testCase.attachment.ID {
				t.Fatalf("refusal details %+v", failure.Error.Details)
			}
			// Refused before the open is forwarded: no session exists.
			if sessions := hub.Sessions(context.Background()); len(sessions) != 0 {
				t.Fatalf("a refused open left %d sessions behind", len(sessions))
			}
		})
	}
}

// TestDaemonFillsTheRegistrysCommand pins the other half of the rule: an
// attachment naming a configured id is forwarded carrying the operator's own
// command, arguments, and environment, which the caller never sent.
func TestDaemonFillsTheRegistrysCommand(t *testing.T) {
	registry := registryWithToolSource(t)
	recorder := &recordingAdapter{}
	if err := registry.Register("recorder", recorder); err != nil {
		t.Fatal(err)
	}
	_, server := newServer(t, registry, Options{})
	envelope := requestEnvelope(t, protocol.TypeSessionOpenRequest, "open-filled", protocol.SessionOpenRequest{
		SessionID:   "filled",
		ToolSources: []protocol.ToolSourceAttachment{{ID: "workspace-files", Kind: protocol.ToolSourceProcess}},
	}, "filled", "", "")
	if status, response := postEnvelope(t, server, "/adapters/recorder/sessions", envelope); status != http.StatusOK {
		t.Fatalf("open status %d: %s", status, response.Payload)
	}
	if len(recorder.request.ToolSources) != 1 {
		t.Fatalf("the adapter received %d attachments", len(recorder.request.ToolSources))
	}
	forwarded := recorder.request.ToolSources[0]
	if forwarded.Command != "/usr/local/bin/mcp-filesystem" || len(forwarded.Args) != 2 {
		t.Fatalf("the daemon forwarded %+v instead of the registry's command", forwarded)
	}
	if len(forwarded.Environment) != 1 || forwarded.Environment[0] != "MCP_TOKEN=operator-secret" {
		t.Fatalf("the daemon forwarded environment %v", forwarded.Environment)
	}
}

// TestOpenRelaysTheAdaptersAttachmentRefusal pins the other end of the
// refusal path: a source the adapter itself cannot attach is reported under
// the typed code with the details that name it, not as a generic open
// failure, so a caller learns which source to drop.
func TestOpenRelaysTheAdaptersAttachmentRefusal(t *testing.T) {
	_, server := newServer(t, memoryRegistry(64), Options{})
	// The reference adapter declares reference-mcp, so attaching an id it
	// already resolves is a collision it refuses by naming the source.
	status, envelope := openSessionWith(t, server, "collide", protocol.SessionOpenRequest{
		ToolSources: []protocol.ToolSourceAttachment{{ID: "reference-mcp", Kind: protocol.ToolSourceLocal}},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("open status %d, want 400: %s", status, envelope.Payload)
	}
	var failure protocol.ErrorResponse
	if err := envelope.DecodePayload(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != "unsupported_feature" {
		t.Fatalf("refusal code %q", failure.Error.Code)
	}
	if failure.Error.Details["feature"] != protocol.FeatureToolSourcesAttach ||
		failure.Error.Details["reason"] != base.ControlUnsatisfiable ||
		failure.Error.Details["source"] != "reference-mcp" {
		t.Fatalf("refusal details %+v", failure.Error.Details)
	}
}

// degradedAttachAdapter advertises attachment at `degraded` and refuses an
// open that does not opt in. No in-repo adapter advertises it at that level,
// so the route's DegradedControlError mapping would otherwise be unpinned.
// The route now reads the same disclosure and answers first, so the adapter's
// own refusal is the backstop: the two must agree on the code, which is what
// makes an embedder calling hub.Open directly see the same answer as a wire
// caller.
type degradedAttachAdapter struct{ *base.Memory }

func (a degradedAttachAdapter) Probe(ctx context.Context) (base.Descriptor, error) {
	descriptor, err := a.Memory.Probe(ctx)
	if err != nil {
		return descriptor, err
	}
	descriptor.Capabilities.Features[protocol.FeatureToolSourcesAttach] = protocol.FeatureSupport{
		Level: protocol.SupportDegraded, Modes: []string{protocol.ModeSessionOpen},
		Reason: "sources are attached but never health-checked",
	}
	return descriptor, nil
}

func (a degradedAttachAdapter) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	if len(request.ToolSources) > 0 && !request.AllowsDegraded(protocol.FeatureToolSourcesAttach) {
		return nil, &base.DegradedControlError{Feature: protocol.FeatureToolSourcesAttach}
	}
	return a.Memory.Open(ctx, request)
}

// TestOpenRelaysADegradedAttachRefusal is the other typed refusal the open
// route owes: a degraded capability elected without consent asks the caller
// for an opt-in it can simply add and reissue, which a generic open_failed
// would never tell it.
func TestOpenRelaysADegradedAttachRefusal(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("memory", degradedAttachAdapter{base.NewMemory(base.Config{})}); err != nil {
		t.Fatal(err)
	}
	_, server := newServer(t, registry, Options{})
	// A `local` source, so the daemon's own credential rule — which refuses a
	// `process` attachment naming no configured id — is out of the picture
	// and the consent question is the only one on the table.
	attachment := protocol.ToolSourceAttachment{ID: "files", Kind: protocol.ToolSourceLocal}
	status, envelope := openSessionWith(t, server, "degraded", protocol.SessionOpenRequest{
		ToolSources: []protocol.ToolSourceAttachment{attachment},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("open status %d, want 400: %s", status, envelope.Payload)
	}
	var failure protocol.ErrorResponse
	if err := envelope.DecodePayload(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != "capability_degraded" {
		t.Fatalf("refusal code %q, want capability_degraded", failure.Error.Code)
	}
	if failure.Error.Details["feature"] != protocol.FeatureToolSourcesAttach {
		t.Fatalf("refusal details %+v", failure.Error.Details)
	}

	// The same open carrying the opt-in is admitted, so the refusal really is
	// asking for consent rather than hiding an unusable capability.
	status, envelope = openSessionWith(t, server, "consented", protocol.SessionOpenRequest{
		ToolSources:           []protocol.ToolSourceAttachment{attachment},
		AllowDegradedFeatures: []string{protocol.FeatureToolSourcesAttach},
	})
	if status != http.StatusOK {
		t.Fatalf("consented open status %d: %s", status, envelope.Payload)
	}
}

// TestReadRequestRefusesBrowserOrigins pins the origin boundary that lands
// beside the registry allowlist: a simple cross-origin POST from a page is
// refused, and so is a body that does not declare itself JSON, which turns
// that simple request into a preflight the daemon never answers.
func TestReadRequestRefusesBrowserOrigins(t *testing.T) {
	server := newMemoryServer(t, 64)
	body, err := json.Marshal(requestEnvelope(t, protocol.TypeSessionOpenRequest, "open-origin", protocol.SessionOpenRequest{SessionID: "origin"}, "origin", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, server.URL+"/adapters/memory/sessions", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://evil.example")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin open status %d, want 403", response.StatusCode)
	}

	if status, _ := post(t, server, "/adapters/memory/sessions", "text/plain", body); status.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain open status %d, want 415", status.StatusCode)
	}
}

// reprobedAdapter republishes the reference descriptor with one capability
// rewritten, which is how a test states what the endpoint disclosed without
// reimplementing an adapter. A nil support removes the key entirely.
type reprobedAdapter struct {
	*base.Memory
	key     string
	support *protocol.FeatureSupport
}

func (a reprobedAdapter) Probe(ctx context.Context) (base.Descriptor, error) {
	descriptor, err := a.Memory.Probe(ctx)
	if err != nil {
		return descriptor, err
	}
	if a.support == nil {
		delete(descriptor.Capabilities.Features, a.key)
		return descriptor, nil
	}
	descriptor.Capabilities.Features[a.key] = *a.support
	return descriptor, nil
}

// TestOpenAnswersTheCapabilityRungBeforeItsOwnConstraint pins the refusal
// order on the open route. The ladder is capability, then degradation, then
// unsatisfiability, and the daemon's credential rule — which refuses a process
// attachment carrying a command, or naming no configured id — is an
// unsatisfiability of the daemon's own. Applying it first would answer a
// question the caller never asked: told to name a configured source, a caller
// would keep reissuing opens against an endpoint that attaches nothing, never
// learning the capability is missing. Every attachment below would trip the
// credential rule, so only the ordering can produce the expected refusal.
func TestOpenAnswersTheCapabilityRungBeforeItsOwnConstraint(t *testing.T) {
	// A command and an unconfigured id: two separate credential-rule
	// violations, so neither refusal below can be the daemon's by accident.
	credentialed := protocol.ToolSourceAttachment{ID: "never-configured", Kind: protocol.ToolSourceProcess, Command: "/bin/sh"}
	remoteOnly := protocol.FeatureSupport{Level: protocol.SupportNative, Modes: []string{protocol.ModeRemote}}
	degraded := protocol.FeatureSupport{Level: protocol.SupportDegraded, Modes: []string{protocol.ModeSessionOpen}, Reason: "sources are attached but never health-checked"}
	for _, testCase := range []struct {
		name    string
		support *protocol.FeatureSupport
		code    string
		details map[string]any
	}{
		{
			"an unadvertised capability", nil, "unsupported_feature",
			map[string]any{"feature": protocol.FeatureToolSourcesAttach, "reason": base.ControlUnadvertised},
		},
		{
			// Advertised, but for a mode that is not session open: an
			// attachment at open elects a capability this endpoint does not
			// offer there, which is the capability rung, not a constraint on
			// the source.
			"a capability disclosing no session-open mode", &remoteOnly, "unsupported_feature",
			map[string]any{"feature": protocol.FeatureToolSourcesAttach, "reason": base.ControlUnadvertised},
		},
		{
			"a degraded capability without the opt-in", &degraded, "capability_degraded",
			map[string]any{"feature": protocol.FeatureToolSourcesAttach},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			registry := serve.NewRegistry()
			if err := registry.Register("memory", reprobedAdapter{base.NewMemory(base.Config{}), protocol.FeatureToolSourcesAttach, testCase.support}); err != nil {
				t.Fatal(err)
			}
			hub, server := newServer(t, registry, Options{})
			status, envelope := openSessionWith(t, server, "ordered", protocol.SessionOpenRequest{
				ToolSources: []protocol.ToolSourceAttachment{credentialed},
			})
			if status != http.StatusBadRequest {
				t.Fatalf("open status %d, want 400: %s", status, envelope.Payload)
			}
			var failure protocol.ErrorResponse
			if err := envelope.DecodePayload(&failure); err != nil {
				t.Fatal(err)
			}
			if failure.Error.Code != testCase.code {
				t.Fatalf("refusal code %q, want %q: %+v", failure.Error.Code, testCase.code, failure.Error.Details)
			}
			for key, want := range testCase.details {
				if failure.Error.Details[key] != want {
					t.Fatalf("refusal details %+v, want %s=%v", failure.Error.Details, key, want)
				}
			}
			// The capability and degradation rungs say nothing about a
			// particular source; naming one would point at the wrong fix.
			if _, named := failure.Error.Details["source"]; named {
				t.Fatalf("a capability-rung refusal named a source: %+v", failure.Error.Details)
			}
			if sessions := hub.Sessions(context.Background()); len(sessions) != 0 {
				t.Fatalf("a refused open left %d sessions behind", len(sessions))
			}
		})
	}
}

// layeredAdapter republishes the reference descriptor with one capability
// moved out of the top-level features and into a named layer. A descriptor may
// publish a key that way — layers are the sections a descriptor may split
// itself into — and the validator reads such a descriptor as advertising the
// key, so the daemon must too.
type layeredAdapter struct {
	*base.Memory
	key   string
	layer string
}

func (a layeredAdapter) Probe(ctx context.Context) (base.Descriptor, error) {
	descriptor, err := a.Memory.Probe(ctx)
	if err != nil {
		return descriptor, err
	}
	support := descriptor.Capabilities.Features[a.key]
	delete(descriptor.Capabilities.Features, a.key)
	descriptor.Capabilities.Layers = map[string]protocol.CapabilityLayer{
		a.layer: {Features: map[string]protocol.FeatureSupport{a.key: support}},
	}
	return descriptor, nil
}

// TestOpenReadsAttachmentSupportFromALayer pins the descriptor shape the gate
// must resolve. A key published under a layer alone is advertised, and a
// top-level-only lookup would refuse every attachment such an endpoint can
// honour — the route refusing what the validator accepts, which is what a
// second normalization beside the validator's buys.
func TestOpenReadsAttachmentSupportFromALayer(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("memory", layeredAdapter{base.NewMemory(base.Config{}), protocol.FeatureToolSourcesAttach, "action"}); err != nil {
		t.Fatal(err)
	}
	_, server := newServer(t, registry, Options{})
	status, envelope := openSessionWith(t, server, "layered", protocol.SessionOpenRequest{
		ToolSources: []protocol.ToolSourceAttachment{{ID: "files", Kind: protocol.ToolSourceLocal}},
	})
	if status != http.StatusOK {
		t.Fatalf("open status %d, want 200: %s", status, envelope.Payload)
	}
	var opened protocol.SessionOpenResponse
	if err := envelope.DecodePayload(&opened); err != nil {
		t.Fatal(err)
	}
	attached := false
	for _, source := range opened.Sources {
		if source.ID == "files" {
			attached = true
		}
	}
	if !attached {
		t.Fatalf("the open published %+v, without the source it attached", opened.Sources)
	}
}

// unprobableAdapter fails every probe, the way an adapter behind a dead
// context or an unreachable endpoint does.
type unprobableAdapter struct{ *base.Memory }

func (unprobableAdapter) Probe(context.Context) (base.Descriptor, error) {
	return base.Descriptor{}, errors.New("the endpoint could not be described")
}

// TestOpenReportsAProbeItCouldNotRead keeps the precedence when the descriptor
// is unavailable. Falling through to the daemon's own credential rule would
// answer an open whose capability rung was never settled, telling a caller to
// name an operator-configured source when the endpoint may attach nothing at
// all — the precedence the pre-gate exists to establish, undone in the one
// case where nothing is known. No rung has an answer, so none is invented.
func TestOpenReportsAProbeItCouldNotRead(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("memory", unprobableAdapter{base.NewMemory(base.Config{})}); err != nil {
		t.Fatal(err)
	}
	hub, server := newServer(t, registry, Options{})
	// An attachment the daemon's own rule would refuse as unsatisfiable, so
	// the wrong answer is available and only the ordering withholds it.
	status, envelope := openSessionWith(t, server, "unprobable", protocol.SessionOpenRequest{
		ToolSources: []protocol.ToolSourceAttachment{{ID: "never-configured", Kind: protocol.ToolSourceProcess, Command: "/bin/sh"}},
	})
	if status != http.StatusInternalServerError {
		t.Fatalf("open status %d, want 500: %s", status, envelope.Payload)
	}
	var failure protocol.ErrorResponse
	if err := envelope.DecodePayload(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != "probe_failed" {
		t.Fatalf("refusal code %q, want probe_failed", failure.Error.Code)
	}
	// Not a capability verdict: an unread descriptor refuses nothing by name.
	if len(failure.Error.Details) != 0 {
		t.Fatalf("an unread descriptor produced a typed refusal: %+v", failure.Error.Details)
	}
	if sessions := hub.Sessions(context.Background()); len(sessions) != 0 {
		t.Fatalf("a failed open left %d sessions behind", len(sessions))
	}

	// An open attaching nothing never consults the descriptor, so it is not
	// held hostage to a probe it does not need.
	if status, envelope := openSessionWith(t, server, "plain", protocol.SessionOpenRequest{}); status != http.StatusOK {
		t.Fatalf("a non-attaching open status %d: %s", status, envelope.Payload)
	}
}

// TestEveryRouteRefusesABrowserOrigin holds the origin boundary where the
// README and Decision 0008 put it: on the daemon, not on the four routes that
// happen to parse a request envelope. It was enforced inside readRequest,
// which POST /sessions/{id}/close never calls, so a page in the user's browser
// could drop a live session and its in-flight runs with one no-cors POST. The
// sweep below walks every registered route, so a route added later is covered
// by the boundary rather than by whoever remembers to call the right helper.
func TestEveryRouteRefusesABrowserOrigin(t *testing.T) {
	hub, server := newServer(t, memoryRegistry(64), Options{})
	if status, envelope := openSessionWith(t, server, "guarded", protocol.SessionOpenRequest{}); status != http.StatusOK {
		t.Fatalf("open status %d: %s", status, envelope.Payload)
	}
	// Every route the mux registers, in its own order. A route missing from
	// this list is a route the boundary was never checked on.
	routes := []struct{ method, path string }{
		{http.MethodGet, "/adapters"},
		{http.MethodGet, "/adapters/memory/capabilities"},
		{http.MethodPost, "/adapters/memory/sessions"},
		{http.MethodGet, "/sessions"},
		{http.MethodGet, "/sessions/guarded/events"},
		{http.MethodGet, "/sessions/guarded/state"},
		{http.MethodGet, "/sessions/guarded/tools"},
		{http.MethodPost, "/sessions/guarded/submit"},
		{http.MethodPost, "/sessions/guarded/resolve"},
		{http.MethodPost, "/sessions/guarded/cancel"},
		{http.MethodPost, "/sessions/guarded/close"},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			// No body and no content type: the shape a simple cross-origin
			// POST from a page takes, which a media-type rule alone would not
			// reach on a route that parses no body.
			request, err := http.NewRequest(route.method, server.URL+route.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Origin", "https://evil.example")
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusForbidden {
				t.Fatalf("status %d, want 403", response.StatusCode)
			}
			var envelope protocol.Envelope
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			var failure protocol.ErrorResponse
			if err := envelope.DecodePayload(&failure); err != nil {
				t.Fatal(err)
			}
			if failure.Error.Code != "cross_origin_request" {
				t.Fatalf("refusal code %q", failure.Error.Code)
			}
		})
	}
	// The refusals were refusals, not silent successes: the session the page
	// tried to close is still live.
	if _, err := hub.Session("guarded"); err != nil {
		t.Fatalf("a cross-origin close took the session down: %v", err)
	}
	if sessions := hub.Sessions(context.Background()); len(sessions) != 1 {
		t.Fatalf("the sweep left %d sessions, want the one it opened", len(sessions))
	}
}

// TestTheOriginBoundaryHoldsWithoutAHostAllowlist pins the one asymmetry with
// the host restriction beside it: that allowlist is an operator's
// configuration and is absent by default, while the origin boundary is what
// the daemon promises whatever it is configured with. Wrapping it inside the
// conditional would have made the documented boundary configuration-dependent.
func TestTheOriginBoundaryHoldsWithoutAHostAllowlist(t *testing.T) {
	for _, options := range []Options{{}, {HostAllowlist: []string{"localhost", "127.0.0.1"}}} {
		_, server := newServer(t, memoryRegistry(64), options)
		request, err := http.NewRequest(http.MethodPost, server.URL+"/sessions/absent/close", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Origin", "https://evil.example")
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("host allowlist %v: status %d, want 403", options.HostAllowlist, response.StatusCode)
		}
	}
}

// TestOpenExchangeValidatesAsATrace is the test no route test was: every other
// one here decodes a response envelope on its own, and the corpus hands the
// adapter an attachment the daemon has already rewritten, so nothing put the
// request and the response side by side and asked the validator whether they
// agree. They must. A caller names an operator-configured source by id, the
// daemon publishes the operator's full descriptor back, and the assembled
// exchange is exactly the trace a conformance run would collect from the wire.
func TestOpenExchangeValidatesAsATrace(t *testing.T) {
	hub, server := newServer(t, registryWithToolSource(t), Options{})
	descriptor, err := hub.Probe(context.Background(), "memory")
	if err != nil {
		t.Fatal(err)
	}
	// The id and the kind, and nothing else: the shape the route is for.
	request := requestEnvelope(t, protocol.TypeSessionOpenRequest, "open-trace", protocol.SessionOpenRequest{
		SessionID:   "trace",
		ToolSources: []protocol.ToolSourceAttachment{{ID: "workspace-files", Kind: protocol.ToolSourceProcess}},
	}, "trace", "", string(descriptor.CapabilityRevision))
	status, response := postEnvelope(t, server, "/adapters/memory/sessions", request)
	if status != http.StatusOK {
		t.Fatalf("open status %d: %s", status, response.Payload)
	}

	capabilitiesRequest, err := protocol.NewEnvelope(protocol.TypeCapabilitiesRequest, "capabilities-request", protocol.CapabilitiesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := protocol.NewEnvelope(protocol.TypeCapabilitiesResponse, "capabilities-response", descriptor.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	capabilities.InReplyTo, capabilities.CapabilityRevision = capabilitiesRequest.ID, descriptor.CapabilityRevision
	trace, err := json.Marshal([]protocol.Envelope{capabilitiesRequest, capabilities, request, response})
	if err != nil {
		t.Fatal(err)
	}
	if result := validation.MustNew().Validate(bytes.NewReader(trace), "open-exchange"); !result.Valid() {
		t.Fatalf("the open exchange is not a valid trace: %v\ntrace: %s", result.Diagnostics, trace)
	}

	// What the caller left blank came back filled from the operator's entry,
	// which is the half of the rule the trace check would also accept if the
	// daemon published nothing at all.
	var opened protocol.SessionOpenResponse
	if err := response.DecodePayload(&opened); err != nil {
		t.Fatal(err)
	}
	published := false
	for _, source := range opened.Sources {
		if source.ID != "workspace-files" {
			continue
		}
		published = true
		if source.DisplayName != "Workspace Files" || source.Protocol != protocol.ToolSourceMCP || source.Endpoint != "stdio:workspace-files" {
			t.Fatalf("the open published %+v instead of the operator's descriptor", source)
		}
	}
	if !published {
		t.Fatalf("the open response omits the attached source: %+v", opened.Sources)
	}
}

// TestOpenRefusesAWireSuppliedDescriptorMember is the other half. The registry
// entry is authoritative for every published member, so a caller cannot label
// the operator's own MCP server in the catalog a user reads. Overwriting the
// value silently would be worse than refusing it: the request and the response
// would then disagree about one source, and a caller could not tell an endpoint
// that honoured its attachment from one that changed it.
func TestOpenRefusesAWireSuppliedDescriptorMember(t *testing.T) {
	for _, testCase := range []struct {
		member     string
		attachment protocol.ToolSourceAttachment
	}{
		{"display_name", protocol.ToolSourceAttachment{ID: "workspace-files", Kind: protocol.ToolSourceProcess, DisplayName: "Payroll (read-only)"}},
		{"protocol", protocol.ToolSourceAttachment{ID: "workspace-files", Kind: protocol.ToolSourceProcess, Protocol: "not-mcp"}},
		{"endpoint", protocol.ToolSourceAttachment{ID: "workspace-files", Kind: protocol.ToolSourceProcess, Endpoint: "stdio:somewhere-else"}},
	} {
		t.Run(testCase.member, func(t *testing.T) {
			hub, server := newServer(t, registryWithToolSource(t), Options{})
			status, envelope := openSessionWith(t, server, "spoof", protocol.SessionOpenRequest{
				ToolSources: []protocol.ToolSourceAttachment{testCase.attachment},
			})
			if status != http.StatusBadRequest {
				t.Fatalf("open status %d, want 400: %s", status, envelope.Payload)
			}
			var failure protocol.ErrorResponse
			if err := envelope.DecodePayload(&failure); err != nil {
				t.Fatal(err)
			}
			if failure.Error.Code != "unsupported_feature" || failure.Error.Details["source"] != "workspace-files" {
				t.Fatalf("refusal %q details %+v", failure.Error.Code, failure.Error.Details)
			}
			if !strings.Contains(failure.Error.Message, testCase.member) {
				t.Fatalf("refusal %q does not name the member it refused", failure.Error.Message)
			}
			if sessions := hub.Sessions(context.Background()); len(sessions) != 0 {
				t.Fatalf("a refused open left %d sessions behind", len(sessions))
			}
		})
	}

	// Repeating the operator's own values contradicts nothing, so it is
	// admitted: the rule is about disagreement, not about mentioning a member.
	_, server := newServer(t, registryWithToolSource(t), Options{})
	status, envelope := openSessionWith(t, server, "echoed", protocol.SessionOpenRequest{
		ToolSources: []protocol.ToolSourceAttachment{{
			ID: "workspace-files", Kind: protocol.ToolSourceProcess, Protocol: protocol.ToolSourceMCP,
			DisplayName: "Workspace Files", Endpoint: "stdio:workspace-files",
		}},
	})
	if status != http.StatusOK {
		t.Fatalf("an attachment echoing the operator's own members was refused: %s", envelope.Payload)
	}
}

// registryWithLocalToolSource configures the same id as a `local` source, so a
// wire caller claiming `process` for it is claiming a transport the operator
// never configured.
func registryWithLocalToolSource(t *testing.T) *serve.Registry {
	t.Helper()
	registry := memoryRegistry(64)
	if err := registry.RegisterToolSource("workspace-files", protocol.ToolSourceAttachment{
		Kind: protocol.ToolSourceLocal, DisplayName: "Workspace Files", Endpoint: "local:workspace-files",
	}); err != nil {
		t.Fatal(err)
	}
	return registry
}

// TestOpenRefusesAWireSuppliedKind closes the member the authoritative list
// omitted, and the hole was on both sides of it because `kind` used to decide
// whether the operator's entry was consulted at all. A `local` attachment
// naming a configured id was forwarded verbatim, never checked against that
// entry; a `process` attachment naming a `local` entry took the operator's
// `local` descriptor back under a request that said `process`, which is the
// silent substitution the whole round before this one was about. The lookup now
// happens first: a configured id is the operator's source, whatever kind the
// caller claims, and `kind` is the member where claiming otherwise matters most
// because it selects how the source is reached.
func TestOpenRefusesAWireSuppliedKind(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		registry func(*testing.T) *serve.Registry
		kind     string
	}{
		{"a process claim over a local entry", registryWithLocalToolSource, protocol.ToolSourceProcess},
		{"a local claim over a process entry", registryWithToolSource, protocol.ToolSourceLocal},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			hub, server := newServer(t, testCase.registry(t), Options{})
			status, envelope := openSessionWith(t, server, "kind", protocol.SessionOpenRequest{
				ToolSources: []protocol.ToolSourceAttachment{{ID: "workspace-files", Kind: testCase.kind}},
			})
			if status != http.StatusBadRequest {
				t.Fatalf("open status %d, want 400: %s", status, envelope.Payload)
			}
			var failure protocol.ErrorResponse
			if err := envelope.DecodePayload(&failure); err != nil {
				t.Fatal(err)
			}
			if failure.Error.Code != "unsupported_feature" || failure.Error.Details["source"] != "workspace-files" {
				t.Fatalf("refusal %q details %+v", failure.Error.Code, failure.Error.Details)
			}
			if !strings.Contains(failure.Error.Message, "kind") {
				t.Fatalf("refusal %q does not name the member it refused", failure.Error.Message)
			}
			if sessions := hub.Sessions(context.Background()); len(sessions) != 0 {
				t.Fatalf("a refused open left %d sessions behind", len(sessions))
			}
		})
	}

	// The operator's own kind is admitted and the exchange validates as a
	// trace, which is what the substitution would have broken: a `local` entry
	// attached as `local` publishes a `local` descriptor under a request that
	// said `local`.
	hub, server := newServer(t, registryWithLocalToolSource(t), Options{})
	descriptor, err := hub.Probe(context.Background(), "memory")
	if err != nil {
		t.Fatal(err)
	}
	request := requestEnvelope(t, protocol.TypeSessionOpenRequest, "open-local", protocol.SessionOpenRequest{
		SessionID:   "local",
		ToolSources: []protocol.ToolSourceAttachment{{ID: "workspace-files", Kind: protocol.ToolSourceLocal}},
	}, "local", "", string(descriptor.CapabilityRevision))
	status, response := postEnvelope(t, server, "/adapters/memory/sessions", request)
	if status != http.StatusOK {
		t.Fatalf("open status %d: %s", status, response.Payload)
	}
	capabilitiesRequest, err := protocol.NewEnvelope(protocol.TypeCapabilitiesRequest, "capabilities-request", protocol.CapabilitiesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := protocol.NewEnvelope(protocol.TypeCapabilitiesResponse, "capabilities-response", descriptor.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	capabilities.InReplyTo, capabilities.CapabilityRevision = capabilitiesRequest.ID, descriptor.CapabilityRevision
	trace, err := json.Marshal([]protocol.Envelope{capabilitiesRequest, capabilities, request, response})
	if err != nil {
		t.Fatal(err)
	}
	if result := validation.MustNew().Validate(bytes.NewReader(trace), "local-exchange"); !result.Valid() {
		t.Fatalf("the open exchange is not a valid trace: %v\ntrace: %s", result.Diagnostics, trace)
	}
}

// TestOpenRefusesAnUnconfiguredIDWhateverItsKind pins the other half of moving
// the lookup first. A `local` attachment naming no configured id still reaches
// the adapter, because the daemon has nothing to run for it and nothing
// configured to contradict — that is the path every capability-rung test here
// depends on, and it must not become a daemon refusal.
func TestOpenRefusesAnUnconfiguredIDWhateverItsKind(t *testing.T) {
	_, server := newServer(t, registryWithToolSource(t), Options{})
	status, envelope := openSessionWith(t, server, "unconfigured-local", protocol.SessionOpenRequest{
		ToolSources: []protocol.ToolSourceAttachment{{ID: "never-configured", Kind: protocol.ToolSourceLocal}},
	})
	// The reference adapter admits a `local` source it has never heard of, so
	// reaching it is the whole assertion: the daemon did not answer first.
	if status != http.StatusOK {
		t.Fatalf("an unconfigured local attachment was refused by the daemon: %s", envelope.Payload)
	}
}

package servehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
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

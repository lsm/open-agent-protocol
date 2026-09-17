package servehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
	"github.com/lsm/open-agent-protocol/validation"
)

type recordingAdapter struct{ request base.OpenRequest }

func (a *recordingAdapter) Probe(ctx context.Context) (base.Descriptor, error) {
	return base.NewMemory(base.Config{}).Probe(ctx)
}

func (a *recordingAdapter) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	a.request = request

	return base.NewMemory(base.Config{}).Open(ctx, base.OpenRequest{SessionID: request.SessionID, Participant: request.Participant})
}

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

func adapterRevision(t *testing.T, server *httptest.Server, name string) string {
	t.Helper()
	var envelope protocol.Envelope
	if status := getJSON(t, server, "/adapters/"+name+"/capabilities", &envelope); status != http.StatusOK {
		return ""
	}
	return envelope.CapabilityRevision
}

func openSessionWith(t *testing.T, server *httptest.Server, id string, request protocol.SessionOpenRequest) (int, protocol.Envelope) {
	t.Helper()
	request.SessionID = protocol.SessionID(id)

	revision := adapterRevision(t, server, "memory")
	return postEnvelope(t, server, "/adapters/memory/sessions", requestEnvelope(t, protocol.TypeSessionOpenRequest, "open-"+id, request, id, "", revision))
}

func TestAttachingOpenPinsOnlyWhatItCites(t *testing.T) {
	_, server := newServer(t, registryWithToolSource(t), Options{})
	current := adapterRevision(t, server, "memory")
	if current == "" {
		t.Fatal("the daemon serves no capability revision")
	}
	attaching := func(id, revision string) (int, protocol.Envelope) {
		return postEnvelope(t, server, "/adapters/memory/sessions", requestEnvelope(t, protocol.TypeSessionOpenRequest, "open-"+id, protocol.SessionOpenRequest{
			SessionID:   protocol.SessionID(id),
			ToolSources: []protocol.ToolSourceAttachment{{ID: "workspace-files", Kind: protocol.ToolSourceProcess}},
		}, id, "", revision))
	}

	status, response := attaching("stale", "reference-memory-v1")
	if status != http.StatusConflict {
		t.Fatalf("open status %d, want 409: %s", status, response.Payload)
	}
	var failure protocol.ErrorResponse
	if err := response.DecodePayload(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != "stale_capabilities" {
		t.Fatalf("error code %q", failure.Error.Code)
	}
	if details := failure.Error.Details; details["expected_revision"] != current || details["current_revision"] != "reference-memory-v1" {
		t.Fatalf("details %+v, want the endpoint's %q beside the caller's", details, current)
	}

	status, response = attaching("cited", current)
	if status != http.StatusOK {
		t.Fatalf("open status %d: %s", status, response.Payload)
	}
	if response.CapabilityRevision != current {
		t.Fatalf("open response cites %q, want %q", response.CapabilityRevision, current)
	}

	status, response = attaching("unpinned", "")
	if status != http.StatusOK {
		t.Fatalf("an unpinned attaching open was refused: %d %s", status, response.Payload)
	}
	if response.CapabilityRevision != current {
		t.Fatalf("an unpinned open was admitted under %q, want the revision used for admission %q", response.CapabilityRevision, current)
	}

	status, response = postEnvelope(t, server, "/adapters/memory/sessions", requestEnvelope(t, protocol.TypeSessionOpenRequest, "open-plain", protocol.SessionOpenRequest{SessionID: "plain"}, "plain", "", ""))
	if status != http.StatusOK {
		t.Fatalf("a plain open was refused: %d %s", status, response.Payload)
	}
	if response.CapabilityRevision != "" {
		t.Fatalf("a plain open was stamped with %q", response.CapabilityRevision)
	}
}

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

type catalogLessAdapter struct{ *base.Memory }

func (a catalogLessAdapter) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	session, err := a.Memory.Open(ctx, request)
	if err != nil {
		return nil, err
	}
	return catalogLessSession{session}, nil
}

type catalogLessSession struct{ base.Session }

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

			if sessions := hub.Sessions(context.Background()); len(sessions) != 0 {
				t.Fatalf("a refused open left %d sessions behind", len(sessions))
			}
		})
	}
}

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
	}, "filled", "", adapterRevision(t, server, "recorder"))
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

func TestCallerEnvironmentNeverNamesAVariableTwice(t *testing.T) {
	recorder := &recordingAdapter{}
	registry := registryWithToolSource(t)
	if err := registry.Register("recorder", recorder); err != nil {
		t.Fatal(err)
	}
	_, server := newServer(t, registry, Options{})
	envelope := requestEnvelope(t, protocol.TypeSessionOpenRequest, "open-env", protocol.SessionOpenRequest{
		SessionID: "env",
		ToolSources: []protocol.ToolSourceAttachment{{
			ID: "workspace-files", Kind: protocol.ToolSourceProcess,

			Environment: []string{"MCP_TOKEN", "EXTRA_TOKEN"},
		}},
	}, "env", "", adapterRevision(t, server, "recorder"))
	if status, response := postEnvelope(t, server, "/adapters/recorder/sessions", envelope); status != http.StatusOK {
		t.Fatalf("open status %d: %s", status, response.Payload)
	}
	if len(recorder.request.ToolSources) != 1 {
		t.Fatalf("the adapter received %d attachments", len(recorder.request.ToolSources))
	}
	forwarded := recorder.request.ToolSources[0].Environment
	seen := map[string]int{}
	for _, entry := range forwarded {
		name, _, _ := strings.Cut(entry, "=")
		seen[name]++
	}
	for name, count := range seen {
		if count != 1 {
			t.Fatalf("variable %q reaches the adapter %d times: %v", name, count, forwarded)
		}
	}

	if !slices.Contains(forwarded, "MCP_TOKEN=operator-secret") {
		t.Fatalf("the operator's own value did not survive: %v", forwarded)
	}

	if !slices.Contains(forwarded, "EXTRA_TOKEN") {
		t.Fatalf("an unconfigured name was dropped: %v", forwarded)
	}
}

func TestOpenRelaysTheAdaptersAttachmentRefusal(t *testing.T) {
	_, server := newServer(t, memoryRegistry(64), Options{})

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

func TestOpenRelaysADegradedAttachRefusal(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("memory", degradedAttachAdapter{base.NewMemory(base.Config{})}); err != nil {
		t.Fatal(err)
	}
	_, server := newServer(t, registry, Options{})

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

	status, envelope = openSessionWith(t, server, "consented", protocol.SessionOpenRequest{
		ToolSources:           []protocol.ToolSourceAttachment{attachment},
		AllowDegradedFeatures: []string{protocol.FeatureToolSourcesAttach},
	})
	if status != http.StatusOK {
		t.Fatalf("consented open status %d: %s", status, envelope.Payload)
	}
}

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

func TestOpenAnswersTheCapabilityRungBeforeItsOwnConstraint(t *testing.T) {

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

			if _, named := failure.Error.Details["source"]; named {
				t.Fatalf("a capability-rung refusal named a source: %+v", failure.Error.Details)
			}
			if sessions := hub.Sessions(context.Background()); len(sessions) != 0 {
				t.Fatalf("a refused open left %d sessions behind", len(sessions))
			}
		})
	}
}

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

type unprobableAdapter struct{ *base.Memory }

func (unprobableAdapter) Probe(context.Context) (base.Descriptor, error) {
	return base.Descriptor{}, errors.New("the endpoint could not be described")
}

func TestOpenReportsAProbeItCouldNotRead(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("memory", unprobableAdapter{base.NewMemory(base.Config{})}); err != nil {
		t.Fatal(err)
	}
	hub, server := newServer(t, registry, Options{})

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

	if len(failure.Error.Details) != 0 {
		t.Fatalf("an unread descriptor produced a typed refusal: %+v", failure.Error.Details)
	}
	if sessions := hub.Sessions(context.Background()); len(sessions) != 0 {
		t.Fatalf("a failed open left %d sessions behind", len(sessions))
	}

	if status, envelope := openSessionWith(t, server, "plain", protocol.SessionOpenRequest{}); status != http.StatusOK {
		t.Fatalf("a non-attaching open status %d: %s", status, envelope.Payload)
	}
}

func TestEveryRouteRefusesABrowserOrigin(t *testing.T) {
	hub, server := newServer(t, memoryRegistry(64), Options{})
	if status, envelope := openSessionWith(t, server, "guarded", protocol.SessionOpenRequest{}); status != http.StatusOK {
		t.Fatalf("open status %d: %s", status, envelope.Payload)
	}

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

	if _, err := hub.Session("guarded"); err != nil {
		t.Fatalf("a cross-origin close took the session down: %v", err)
	}
	if sessions := hub.Sessions(context.Background()); len(sessions) != 1 {
		t.Fatalf("the sweep left %d sessions, want the one it opened", len(sessions))
	}
}

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

func TestOpenExchangeValidatesAsATrace(t *testing.T) {
	hub, server := newServer(t, registryWithToolSource(t), Options{})
	descriptor, err := hub.Probe(context.Background(), "memory")
	if err != nil {
		t.Fatal(err)
	}

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

func TestOpenRefusesAnUnconfiguredIDWhateverItsKind(t *testing.T) {
	_, server := newServer(t, registryWithToolSource(t), Options{})
	status, envelope := openSessionWith(t, server, "unconfigured-local", protocol.SessionOpenRequest{
		ToolSources: []protocol.ToolSourceAttachment{{ID: "never-configured", Kind: protocol.ToolSourceLocal}},
	})

	if status != http.StatusOK {
		t.Fatalf("an unconfigured local attachment was refused by the daemon: %s", envelope.Payload)
	}
}

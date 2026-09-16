package adapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/protocol"
)

// openWithSources opens one reference session, attaching the given sources.
func openWithSources(t *testing.T, sources ...protocol.ToolSourceAttachment) adapter.Session {
	t.Helper()
	implementation := adapter.NewMemory(adapter.Config{Clock: &fixedClock{}, IDs: &fixedIDs{}})
	session, err := implementation.Open(context.Background(), adapter.OpenRequest{
		SessionID: "tool-sources", Participant: protocol.Participant{ID: "user"}, ToolSources: sources,
	})
	if err != nil {
		t.Fatalf("open with sources: %v", err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	return session
}

// TestReferenceCatalogResolvesEverySource drives the served catalog through
// the real validator rather than reading the descriptor: every tool's source
// resolves to a declared descriptor, the ids are unique, and the attachment
// the open made is listed with the members it was attached with.
func TestReferenceCatalogResolvesEverySource(t *testing.T) {
	attachment := protocol.ToolSourceAttachment{
		ID: "workspace-files", Kind: protocol.ToolSourceProcess, Protocol: protocol.ToolSourceMCP,
		DisplayName: "Workspace Files", Endpoint: "stdio:workspace-files",
		Command: "/usr/local/bin/mcp-filesystem", Environment: []string{"MCP_TOKEN"},
	}
	session := openWithSources(t, attachment)
	lister, ok := session.(adapter.ToolLister)
	if !ok {
		t.Fatal("the reference session does not serve a catalog")
	}
	request := protocol.ToolsListRequest{SessionID: "tool-sources"}
	catalog, err := lister.Tools(context.Background(), request)
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	implementation := adapter.NewMemory(adapter.Config{})
	descriptor, err := implementation.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.AssertToolCatalog(t, descriptor, protocol.SessionOpenRequest{
		SessionID: "tool-sources", ToolSources: []protocol.ToolSourceAttachment{attachment},
	}, request, catalog)

	// The published projection is the point: the attachment's command and its
	// environment allowlist never reach a client.
	for _, source := range catalog.Tools.Sources {
		if source.ID != attachment.ID {
			continue
		}
		if source.Endpoint != attachment.Endpoint || source.Kind != attachment.Kind || source.Protocol != attachment.Protocol {
			t.Fatalf("attached source published as %+v", source)
		}
	}
	found := false
	for _, tool := range catalog.Tools.Tools {
		if tool.Source == "" {
			t.Fatalf("catalog entry %q names no source", tool.Name)
		}
		found = true
	}
	if !found {
		t.Fatal("the reference catalog is empty")
	}
}

// TestAttachmentRefusalsNameTheSource pins every refusal the reference
// adapter owes: each names the offending source, so a caller and the
// validator agree on which entry to change, and each violates a limit the
// descriptor actually discloses — an endpoint that refused an array within
// every disclosed limit would honour nothing it advertised.
func TestAttachmentRefusalsNameTheSource(t *testing.T) {
	cases := []struct {
		name    string
		sources []protocol.ToolSourceAttachment
		source  string
	}{
		{
			name: "an id the descriptor already declares",
			sources: []protocol.ToolSourceAttachment{
				{ID: "reference-mcp", Kind: protocol.ToolSourceProcess},
			},
			source: "reference-mcp",
		},
		{
			name: "two attachments sharing one id",
			sources: []protocol.ToolSourceAttachment{
				{ID: "twice", Kind: protocol.ToolSourceProcess},
				{ID: "twice", Kind: protocol.ToolSourceLocal},
			},
			source: "twice",
		},
		{
			name: "a transport the descriptor does not disclose",
			sources: []protocol.ToolSourceAttachment{
				{ID: "far-away", Kind: protocol.ToolSourceRemote, Endpoint: "https://example.invalid"},
			},
			source: "far-away",
		},
		{
			name: "more sources than the disclosed ceiling",
			sources: []protocol.ToolSourceAttachment{
				{ID: "one", Kind: protocol.ToolSourceProcess},
				{ID: "two", Kind: protocol.ToolSourceProcess},
				{ID: "three", Kind: protocol.ToolSourceProcess},
			},
			source: "three",
		},
	}
	implementation := adapter.NewMemory(adapter.Config{Clock: &fixedClock{}, IDs: &fixedIDs{}})
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			session, err := implementation.Open(context.Background(), adapter.OpenRequest{
				SessionID: "refused", Participant: protocol.Participant{ID: "user"}, ToolSources: testCase.sources,
			})
			if err == nil {
				_ = session.Close(context.Background())
				t.Fatal("the open was admitted, want a typed refusal")
			}
			var refusal *adapter.UnsupportedControlError
			if !errors.As(err, &refusal) {
				t.Fatalf("refusal is %v, want *adapter.UnsupportedControlError", err)
			}
			if refusal.Feature != protocol.FeatureToolSourcesAttach || refusal.Reason != adapter.ControlUnsatisfiable {
				t.Fatalf("refusal names %s/%s", refusal.Feature, refusal.Reason)
			}
			if refusal.Source != testCase.source {
				t.Fatalf("refusal names source %q, want %q", refusal.Source, testCase.source)
			}
		})
	}
}

// TestSessionStatePublishesTheSourceUnion pins the state surface: the union
// of the descriptor's declared sources and the open's attachments, in the
// descriptor shape, so an attachment-only member cannot reach a client
// through state any more than through a catalog.
func TestSessionStatePublishesTheSourceUnion(t *testing.T) {
	session := openWithSources(t, protocol.ToolSourceAttachment{
		ID: "workspace-files", Kind: protocol.ToolSourceLocal, Command: "/bin/true", Environment: []string{"TOKEN=secret"},
	})
	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, source := range state.Sources {
		ids[source.ID] = true
	}
	for _, id := range []string{"reference-native", "reference-mcp", "workspace-files"} {
		if !ids[id] {
			t.Fatalf("session state omits source %q: %+v", id, state.Sources)
		}
	}
}

// TestCatalogRefusesAnotherSessionsScope pins that a session's catalog is its
// own: a request naming another session is refused rather than answered with
// this session's tools under that session's id, which is the shape the
// validator reads as a scope mismatch.
func TestCatalogRefusesAnotherSessionsScope(t *testing.T) {
	session := openWithSources(t)
	lister := session.(adapter.ToolLister)
	if _, err := lister.Tools(context.Background(), protocol.ToolsListRequest{SessionID: "someone-else"}); err == nil {
		t.Fatal("a foreign session scope was answered")
	}
}

// TestUnscopedCatalogPublishesNoAttachment pins what an unscoped list means on
// an attached session. A request naming no session asks for the endpoint's own
// catalog, so it gets the declared sources alone: an attachment belongs to one
// session and is not part of what the endpoint publishes to everyone. The
// answer also carries no session id, which is what makes the leak invisible if
// it happens — the validator's lifetime rule ties an attached source to the
// session that attached it, and an unscoped response never reaches it, so a
// source presented as endpoint-wide here would be checked by nothing.
func TestUnscopedCatalogPublishesNoAttachment(t *testing.T) {
	attachment := protocol.ToolSourceAttachment{
		ID: "workspace-files", Kind: protocol.ToolSourceProcess, Protocol: protocol.ToolSourceMCP,
		DisplayName: "Workspace Files", Endpoint: "stdio:workspace-files",
		Command: "/usr/local/bin/mcp-filesystem", Environment: []string{"MCP_TOKEN"},
	}
	lister := openWithSources(t, attachment).(adapter.ToolLister)
	catalog, err := lister.Tools(context.Background(), protocol.ToolsListRequest{})
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	if catalog.Tools.SessionID != "" {
		t.Fatalf("an unscoped request was answered under session %q", catalog.Tools.SessionID)
	}
	for _, source := range catalog.Tools.Sources {
		if source.ID == attachment.ID {
			t.Fatalf("the endpoint catalog publishes a source one session attached: %+v", catalog.Tools.Sources)
		}
	}
	// It is the endpoint's own catalog, not an empty one: what the descriptor
	// declares is exactly what an unscoped list answers with.
	implementation := adapter.NewMemory(adapter.Config{})
	descriptor, err := implementation.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]protocol.ToolSourceDescriptor{}
	for _, source := range descriptor.Capabilities.Sources {
		declared[source.ID] = source
	}
	if len(catalog.Tools.Sources) != len(declared) {
		t.Fatalf("the endpoint catalog lists %d sources, the descriptor declares %d", len(catalog.Tools.Sources), len(declared))
	}
	for _, source := range catalog.Tools.Sources {
		if declared[source.ID] != source {
			t.Fatalf("source %q differs from the descriptor's: %+v", source.ID, source)
		}
	}
	// Every tool still resolves, which is the property an endpoint catalog
	// owes whatever its scope: dropping the attachments must not orphan one.
	for _, tool := range catalog.Tools.Tools {
		if tool.Source == "" {
			continue
		}
		if _, ok := declared[tool.Source]; !ok {
			t.Fatalf("tool %q names source %q, which the endpoint catalog does not declare", tool.Name, tool.Source)
		}
	}

	// The same session, asked in its own scope, still answers with the
	// attachment: the unscoped answer narrowed the question, not the session.
	scoped, err := lister.Tools(context.Background(), protocol.ToolsListRequest{SessionID: "tool-sources"})
	if err != nil {
		t.Fatalf("scoped tools: %v", err)
	}
	attached := false
	for _, source := range scoped.Tools.Sources {
		if source.ID == attachment.ID {
			attached = true
		}
	}
	if !attached {
		t.Fatalf("the session catalog dropped the attachment: %+v", scoped.Tools.Sources)
	}
}

// TestToolCatalogTraceCertifiesTheShapesTheProtocolAllows holds the test kit to
// the standard it exists to enforce. A helper whose whole purpose is to certify
// conforming adapters convicting one is worse than a helper that certifies
// nothing: the adapter's author reads a defect the endpoint does not have, and
// the honest fix looks like a regression.
//
// Neither shape below is exercised by any adapter in this repository — none is
// a degraded-attachment endpoint and none declares its sources under a layer —
// which is exactly why the suite passed over both. The cases are synthetic for
// that reason, not for convenience.
func TestToolCatalogTraceCertifiesTheShapesTheProtocolAllows(t *testing.T) {
	const revision = "kit-fixture-v1"
	files := protocol.ToolSourceDescriptor{
		ID: "files", Kind: protocol.ToolSourceProcess, Protocol: protocol.ToolSourceMCP,
		DisplayName: "Filesystem", Endpoint: "stdio:filesystem",
	}
	native := protocol.ToolSourceDescriptor{ID: "native", Kind: protocol.ToolSourceNative, DisplayName: "Harness tools"}
	base := func() map[string]protocol.FeatureSupport {
		return map[string]protocol.FeatureSupport{
			"session.open":                  {Level: protocol.SupportNative},
			"session.state":                 {Level: protocol.SupportNative},
			"session.message.submit":        {Level: protocol.SupportNative},
			"session.message.delivery.auto": {Level: protocol.SupportNative},
			"action.tools.list":             {Level: protocol.SupportNative},
		}
	}
	catalog := adapter.ToolCatalog{Revision: revision, Tools: protocol.ToolsListResponse{
		SessionID: "kit",
		Sources:   []protocol.ToolSourceDescriptor{native, files},
		Tools: []protocol.ToolDefinition{{
			Name: "grep", Source: "native", ExecutionOwner: "agent",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
	}}
	attachment := protocol.ToolSourceAttachment{ID: "files", Kind: protocol.ToolSourceProcess}
	list := protocol.ToolsListRequest{SessionID: "kit"}

	t.Run("a degraded attachment the caller consented to", func(t *testing.T) {
		// The open the caller actually made carries the consent. The helper
		// used to synthesize an open from the attachments alone, so it could
		// not carry one, and every conforming degraded-attachment endpoint
		// failed here for degraded_without_optin — a defect belonging to the
		// synthetic trace and not to the adapter.
		features := base()
		features[protocol.FeatureToolSourcesAttach] = protocol.FeatureSupport{
			Level: protocol.SupportDegraded, Reason: "sources are attached on a best-effort basis",
			Modes: []string{protocol.ModeSessionOpen},
		}
		descriptor := adapter.Descriptor{CapabilityRevision: revision, Capabilities: protocol.CapabilityDescriptor{
			Endpoint: protocol.EndpointDescriptor{ID: "kit.fixture", Name: "Kit fixture endpoint"},
			Features: features, Sources: []protocol.ToolSourceDescriptor{native},
		}}
		adaptertest.AssertToolCatalog(t, descriptor, protocol.SessionOpenRequest{
			SessionID:             "kit",
			ToolSources:           []protocol.ToolSourceAttachment{attachment},
			AllowDegradedFeatures: []string{protocol.FeatureToolSourcesAttach},
		}, list, catalog)
	})

	t.Run("sources declared under a layer alone", func(t *testing.T) {
		// A valid descriptor may publish its sources under a layer, exactly as
		// it may publish its catalog there, and the validator reads both that
		// way. The helper read the top level alone, so it expected a union it
		// had itself truncated and reported its own trace invalid.
		features := base()
		features[protocol.FeatureToolSourcesAttach] = protocol.FeatureSupport{
			Level: protocol.SupportNative, Modes: []string{protocol.ModeSessionOpen},
		}
		descriptor := adapter.Descriptor{CapabilityRevision: revision, Capabilities: protocol.CapabilityDescriptor{
			Endpoint: protocol.EndpointDescriptor{ID: "kit.fixture", Name: "Kit fixture endpoint"},
			Features: features,
			Layers: map[string]protocol.CapabilityLayer{
				"harness": {Sources: []protocol.ToolSourceDescriptor{native}},
			},
		}}
		adaptertest.AssertToolCatalog(t, descriptor, protocol.SessionOpenRequest{
			SessionID: "kit", ToolSources: []protocol.ToolSourceAttachment{attachment},
		}, list, catalog)
	})

	t.Run("an open response that filled a member the attachment left blank", func(t *testing.T) {
		// The third instance of the same pattern, found by auditing for it:
		// an attachment states an id and a kind and may state nothing else, and
		// the endpoint fills the rest — which the unit invites, because over the
		// daemon the operator's registry supplies them. The reconstructed open
		// response therefore publishes the catalog's own descriptor for an
		// attached id, not the bare attachment, or every later catalog would be
		// held to a description the endpoint never published.
		features := base()
		features[protocol.FeatureToolSourcesAttach] = protocol.FeatureSupport{
			Level: protocol.SupportNative, Modes: []string{protocol.ModeSessionOpen},
		}
		descriptor := adapter.Descriptor{CapabilityRevision: revision, Capabilities: protocol.CapabilityDescriptor{
			Endpoint: protocol.EndpointDescriptor{ID: "kit.fixture", Name: "Kit fixture endpoint"},
			Features: features, Sources: []protocol.ToolSourceDescriptor{native},
		}}
		// The attachment names the id and the kind; the catalog publishes the
		// display name, protocol, and endpoint the endpoint filled in.
		adaptertest.AssertToolCatalog(t, descriptor, protocol.SessionOpenRequest{
			SessionID: "kit", ToolSources: []protocol.ToolSourceAttachment{{ID: "files", Kind: protocol.ToolSourceProcess}},
		}, list, catalog)
	})
}

// TestReferenceAdapterRefusesOneVariableNamedTwice keeps the reference honest.
// It runs no client, so no child could be confused by the duplicate — but an
// attachment naming one variable twice is ill-formed wherever it is sent, and
// an endpoint the unit's rules are read off should not admit what they refuse.
// The same check runs in ACP, where a child really does receive the pair, and
// in the daemon's registry for the operator's own entries.
func TestReferenceAdapterRefusesOneVariableNamedTwice(t *testing.T) {
	implementation := adapter.NewMemory(adapter.Config{})
	_, err := implementation.Open(context.Background(), adapter.OpenRequest{
		SessionID:   "duplicate-env",
		Participant: protocol.Participant{ID: "user"},
		ToolSources: []protocol.ToolSourceAttachment{{
			ID: "files", Kind: protocol.ToolSourceProcess,
			Environment: []string{"TOKEN", "TOKEN=literal"},
		}},
	})
	var refusal *adapter.UnsupportedControlError
	if !errors.As(err, &refusal) {
		t.Fatalf("got %v, want an UnsupportedControlError", err)
	}
	if refusal.Feature != protocol.FeatureToolSourcesAttach || refusal.Source != "files" {
		t.Fatalf("refusal = %+v", refusal)
	}
	if !strings.Contains(refusal.Detail, "TOKEN") {
		t.Fatalf("refusal detail %q does not name the variable", refusal.Detail)
	}
}

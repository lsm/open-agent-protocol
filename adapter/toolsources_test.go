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

func TestCatalogRefusesAnotherSessionsScope(t *testing.T) {
	session := openWithSources(t)
	lister := session.(adapter.ToolLister)
	if _, err := lister.Tools(context.Background(), protocol.ToolsListRequest{SessionID: "someone-else"}); err == nil {
		t.Fatal("a foreign session scope was answered")
	}
}

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

	for _, tool := range catalog.Tools.Tools {
		if tool.Source == "" {
			continue
		}
		if _, ok := declared[tool.Source]; !ok {
			t.Fatalf("tool %q names source %q, which the endpoint catalog does not declare", tool.Name, tool.Source)
		}
	}

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

		features := base()
		features[protocol.FeatureToolSourcesAttach] = protocol.FeatureSupport{
			Level: protocol.SupportNative, Modes: []string{protocol.ModeSessionOpen},
		}
		descriptor := adapter.Descriptor{CapabilityRevision: revision, Capabilities: protocol.CapabilityDescriptor{
			Endpoint: protocol.EndpointDescriptor{ID: "kit.fixture", Name: "Kit fixture endpoint"},
			Features: features, Sources: []protocol.ToolSourceDescriptor{native},
		}}

		adaptertest.AssertToolCatalog(t, descriptor, protocol.SessionOpenRequest{
			SessionID: "kit", ToolSources: []protocol.ToolSourceAttachment{{ID: "files", Kind: protocol.ToolSourceProcess}},
		}, list, catalog)
	})
}

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

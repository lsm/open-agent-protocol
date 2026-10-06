package serveendpoint

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/binding"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

func listEndpoint(t *testing.T, store binding.Store, lines ...string) string {
	t.Helper()
	registry, err := serve.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(serve.New(registry, serve.Options{Bindings: store}), Options{Adapter: "memory", Shutdown: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	out := &syncBuffer{}
	_ = server.Run(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), out)
	return out.String()
}

func answerTo(t *testing.T, out string, request protocol.EnvelopeID) protocol.Envelope {
	t.Helper()
	for _, raw := range strings.Split(out, "\n") {
		var envelope protocol.Envelope
		if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &envelope) != nil {
			continue
		}
		if envelope.InReplyTo == request {
			return envelope
		}
	}
	t.Fatalf("no answer to %s in:\n%s", request, out)
	return protocol.Envelope{}
}

func TestAnEndpointWithASessionHistoryAdvertisesAndPagesTheList(t *testing.T) {
	out := listEndpoint(t, binding.Memory(),
		requestLine(t, protocol.TypeCapabilitiesRequest, "caps", protocol.CapabilitiesRequest{}, ""),
		requestLine(t, protocol.TypeSessionOpenRequest, "open-a", protocol.SessionOpenRequest{SessionID: "a"}, "a"),
		requestLine(t, protocol.TypeSessionOpenRequest, "open-b", protocol.SessionOpenRequest{SessionID: "b"}, "b"),
		requestLine(t, protocol.TypeSessionListRequest, "list", protocol.SessionListRequest{Limit: 1}, ""),
	)
	var descriptor protocol.CapabilitiesResponse
	caps := answerTo(t, out, "caps")
	if err := caps.DecodePayload(&descriptor); err != nil {
		t.Fatal(err)
	}
	if support := descriptor.Features[protocol.FeatureSessionList]; support.Level != protocol.SupportEmulated {
		t.Fatalf("session.list = %+v, want it advertised emulated", support)
	}
	listed := answerTo(t, out, "list")
	if listed.Type != protocol.TypeSessionListResponse || listed.CapabilityRevision != caps.CapabilityRevision {
		t.Fatalf("list answered %s at %q, want session.list.response citing %q", listed.Type, listed.CapabilityRevision, caps.CapabilityRevision)
	}
	var page protocol.SessionListResponse
	if err := listed.DecodePayload(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 1 || page.NextCursor == "" || page.Sessions[0].State != protocol.SessionListLive {
		t.Fatalf("page = %+v, want one live session and a cursor to the other", page)
	}
}

func TestAnEndpointWithNoSessionHistoryRefusesTheListAsUnadvertised(t *testing.T) {
	out := listEndpoint(t, nil,
		requestLine(t, protocol.TypeCapabilitiesRequest, "caps", protocol.CapabilitiesRequest{}, ""),
		requestLine(t, protocol.TypeSessionListRequest, "list", protocol.SessionListRequest{}, ""),
	)
	var descriptor protocol.CapabilitiesResponse
	caps := answerTo(t, out, "caps")
	if err := caps.DecodePayload(&descriptor); err != nil {
		t.Fatal(err)
	}
	if _, advertised := descriptor.Features[protocol.FeatureSessionList]; advertised {
		t.Fatalf("an endpoint with no session history advertised session.list")
	}
	refused := answerTo(t, out, "list")
	var failure protocol.ErrorResponse
	if err := refused.DecodePayload(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != "unsupported_feature" || failure.Error.Details["feature"] != protocol.FeatureSessionList || failure.Error.Details["reason"] != "unadvertised" {
		t.Fatalf("refusal = %+v, want unsupported_feature naming session.list, unadvertised", failure.Error)
	}
}

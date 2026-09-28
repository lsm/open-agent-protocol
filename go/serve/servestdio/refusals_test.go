package servestdio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type unencodableAdapter struct{}

func (unencodableAdapter) Probe(context.Context) (base.Descriptor, error) {
	return base.Descriptor{
		Capabilities: protocol.CapabilityDescriptor{
			Endpoint:         protocol.EndpointDescriptor{ID: "unencodable.test", Name: "Unencodable test adapter", Version: "0.1", Adapter: "process-memory-script"},
			ProtocolVersions: []string{protocol.Version},
			Profiles:         []string{protocol.Profile},
			Features: map[string]protocol.FeatureSupport{
				protocol.FeatureOpenSubscribe: {Level: protocol.SupportNative, Constraints: map[string]json.RawMessage{"broken": json.RawMessage(`{not json`)}},
			},
		},
		CapabilityRevision: "unencodable-v1",
	}, nil
}

func (unencodableAdapter) Open(context.Context, base.OpenRequest) (base.Session, error) {
	return nil, errors.New("unencodableAdapter opens no session")
}

func submitGolden(t *testing.T, f *frontend, session string, id int64) {
	t.Helper()
	request := requestEnvelope(t, fmt.Sprintf("submit-%d", id), protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: protocol.SessionID(session),
		Delivery:  protocol.DeliveryAuto,
		Messages:  []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
	}, session, "")
	f.send(fmt.Sprintf(`{"id":%d,"op":"submit","session_id":%q,"request":%s}`, id, session, request))
	requireOK(t, f.expectResponse(id))
}

func TestCapabilitiesOpReportsAProbeFailure(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	if err := hub.Registry().Register("broken", failingProbeAdapter{}); err != nil {
		t.Fatal(err)
	}
	f := startFrontend(t, hub, Options{})
	f.send(`{"id":1,"op":"capabilities","adapter":"broken"}`)
	response := f.expectResponse(1)
	requireCode(t, response, "probe_failed")
	if len(response.Error.Message) >= 400 {
		t.Fatalf("message is %d characters, want the wire's bound applied", len(response.Error.Message))
	}
}

func TestCapabilitiesOpReportsAnUnencodableDescriptorAsInternal(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	if err := hub.Registry().Register("unencodable", unencodableAdapter{}); err != nil {
		t.Fatal(err)
	}
	f := startFrontend(t, hub, Options{})
	f.send(`{"id":1,"op":"capabilities","adapter":"unencodable"}`)
	requireCode(t, f.expectResponse(1), "internal")
}

func requestEnvelopeWithRevision(t *testing.T, id string, typ protocol.EnvelopeType, payload any, revision string) json.RawMessage {
	t.Helper()
	envelope, err := protocol.NewEnvelope(typ, protocol.EnvelopeID(id), payload)
	if err != nil {
		t.Fatal(err)
	}
	envelope.CapabilityRevision = revision
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestOpenOpRefusesAStaleRevisionOnTheSubscribePath(t *testing.T) {
	const cited = "a-revision-from-another-release"
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})
	request := requestEnvelopeWithRevision(t, "req-open", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{
		SessionID: protocol.SessionID("stale-subscribe"),
		Subscribe: true,
	}, cited)
	f.send(fmt.Sprintf(`{"id":1,"op":"open","adapter":"memory","request":%s}`, request))
	response := f.expectResponse(1)
	requireCode(t, response, "stale_capabilities")
	if response.Error.Details["current_revision"] != cited {
		t.Fatalf("current_revision = %v, want the revision the request cited (%q)", response.Error.Details["current_revision"], cited)
	}
	probed, named := response.Error.Details["expected_revision"].(string)
	if !named || probed == cited || probed == "" {
		t.Fatalf("expected_revision = %v, want the revision the probe reported, which is not the one cited", response.Error.Details["expected_revision"])
	}
}

func TestResolveOpReportsAnUnknownRun(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})
	f.send(openLine(t, 1, "resolve-unknown"))
	requireOK(t, f.expectResponse(1))
	resolve := requestEnvelope(t, "req-resolve", protocol.TypeActionPermissionResolveRequest, protocol.PermissionResolveRequest{
		InteractionID: protocol.InteractionID("permission-01"),
		SessionID:     protocol.SessionID("resolve-unknown"),
		RunID:         protocol.RunID("run-does-not-exist"),
		RequestedBy:   protocol.ParticipantID("user"),
		RespondedBy:   protocol.ParticipantID("user"),
		ChoiceID:      "approve",
		Granted:       true,
	}, "resolve-unknown", "run-does-not-exist")
	f.send(fmt.Sprintf(`{"id":2,"op":"resolve","session_id":%q,"request":%s}`, "resolve-unknown", resolve))
	requireCode(t, f.expectResponse(2), "run_not_found")
}

func TestResolveOpReportsARefusedResolution(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})
	f.send(openLine(t, 1, "resolve-refused"))
	requireOK(t, f.expectResponse(1))
	submitGolden(t, f, "resolve-refused", 2)
	resolve := requestEnvelope(t, "req-resolve", protocol.TypeActionPermissionResolveRequest, protocol.PermissionResolveRequest{
		InteractionID: protocol.InteractionID("permission-01"),
		SessionID:     protocol.SessionID("resolve-refused"),
		RunID:         protocol.RunID("run-01"),
		RequestedBy:   protocol.ParticipantID("user"),
		RespondedBy:   protocol.ParticipantID("someone-else"),
		ChoiceID:      "approve",
		Granted:       true,
	}, "resolve-refused", "run-01")
	f.send(fmt.Sprintf(`{"id":3,"op":"resolve","session_id":%q,"request":%s}`, "resolve-refused", resolve))
	response := f.expectResponse(3)
	requireCode(t, response, "resolution_rejected")
	if response.Error.Message == "" {
		t.Fatal("the refusal carries no message")
	}
}

type closedOnOpenAdapter struct{}

func (closedOnOpenAdapter) Probe(ctx context.Context) (base.Descriptor, error) {
	return base.NewMemory(base.Config{}).Probe(ctx)
}

func (closedOnOpenAdapter) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	session, err := base.NewMemory(base.Config{}).Open(ctx, request)
	if err != nil {
		return nil, err
	}
	return &closedOnOpenSession{Session: session, id: request.SessionID}, nil
}

type closedOnOpenSession struct {
	base.Session
	id protocol.SessionID
}

func (s *closedOnOpenSession) State(context.Context) (protocol.SessionState, error) {
	return protocol.SessionState{SessionID: s.id, Status: protocol.SessionClosed}, base.ErrSessionClosed
}

func TestOpenOpRefusesASessionTheAdapterReportsClosed(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	if err := hub.Registry().Register("closed", closedOnOpenAdapter{}); err != nil {
		t.Fatal(err)
	}
	f := startFrontend(t, hub, Options{})
	request := requestEnvelope(t, "req-open", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{
		SessionID: protocol.SessionID("never-open"),
	}, "", "")
	f.send(fmt.Sprintf(`{"id":1,"op":"open","adapter":"closed","request":%s}`, request))
	requireCode(t, f.expectResponse(1), "session_closed")
	f.send(fmt.Sprintf(`{"id":2,"op":"open","adapter":"memory","request":%s}`, requestEnvelope(t, "req-open-2", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{
		SessionID: protocol.SessionID("never-open"),
	}, "", "")))
	requireOK(t, f.expectResponse(2))
}

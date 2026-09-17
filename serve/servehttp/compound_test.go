package servehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
	"github.com/lsm/open-agent-protocol/validation"
)

func openMessage() *protocol.OpenMessage {
	return &protocol.OpenMessage{
		Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("drive the scripted run")}},
	}
}

func capabilitiesPreamble(t *testing.T, descriptor base.Descriptor) []protocol.Envelope {
	t.Helper()
	request, err := protocol.NewEnvelope(protocol.TypeCapabilitiesRequest, "capabilities-request", protocol.CapabilitiesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	response, err := protocol.NewEnvelope(protocol.TypeCapabilitiesResponse, "capabilities-response", descriptor.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	response.InReplyTo, response.CapabilityRevision = request.ID, descriptor.CapabilityRevision
	return []protocol.Envelope{request, response}
}

func TestOpenCarriesItsMessage(t *testing.T) {
	hub, server := newServer(t, memoryRegistry(64), Options{})
	descriptor, err := hub.Probe(context.Background(), "memory")
	if err != nil {
		t.Fatal(err)
	}
	request := requestEnvelope(t, protocol.TypeSessionOpenRequest, "open-carry", protocol.SessionOpenRequest{
		SessionID: "carry",
		Message:   openMessage(),
	}, "carry", "", string(descriptor.CapabilityRevision))
	status, response := postEnvelope(t, server, "/adapters/memory/sessions", request)
	if status != http.StatusOK {
		t.Fatalf("open status %d: %s", status, response.Payload)
	}
	requireEnvelopeSchema(t, response)

	var opened protocol.SessionOpenResponse
	if err := response.DecodePayload(&opened); err != nil {
		t.Fatal(err)
	}
	if len(opened.ActiveRuns) != 1 {
		t.Fatalf("the open reports %d active runs, want the one its message admitted: %+v", len(opened.ActiveRuns), opened.ActiveRuns)
	}
	admitted := opened.ActiveRuns[0]
	if admitted.RunID == "" {
		t.Fatalf("the admitted run is unnamed: %+v", admitted)
	}
	if len(admitted.AdmittedSubmitRequests) != 1 || admitted.AdmittedSubmitRequests[0] != request.ID {
		t.Fatalf("the admitted run cites %v, want the open request %q", admitted.AdmittedSubmitRequests, request.ID)
	}

	exchange := append(capabilitiesPreamble(t, descriptor), request, response)
	exchange = append(exchange, driveScriptedRun(t, hub, server, "carry", string(descriptor.CapabilityRevision))...)
	trace, err := json.Marshal(exchange)
	if err != nil {
		t.Fatal(err)
	}
	if result := validation.MustNew().Validate(bytes.NewReader(trace), "compound-open"); !result.Valid() {
		t.Fatalf("the compound open is not a valid trace: %v\ntrace: %s", result.Diagnostics, trace)
	}
}

type refusingSubmitAdapter struct{ *base.Memory }

func (a refusingSubmitAdapter) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	session, err := a.Memory.Open(ctx, request)
	if err != nil {
		return nil, err
	}
	return refusingSubmitSession{Session: session}, nil
}

type refusingSubmitSession struct{ base.Session }

func (s refusingSubmitSession) Submit(context.Context, protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	return protocol.MessageSubmitResponse{}, nil, errors.New("the adapter refuses this submission")
}

func TestOpenRollsBackWhenItsMessageCannotBeAdmitted(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("memory", refusingSubmitAdapter{base.NewMemory(base.Config{})}); err != nil {
		t.Fatal(err)
	}
	hub, server := newServer(t, registry, Options{})
	status, envelope := openSessionWith(t, server, "rollback", protocol.SessionOpenRequest{Message: openMessage()})
	if status == http.StatusOK {
		t.Fatalf("an unadmittable message opened a session anyway: %s", envelope.Payload)
	}
	if _, err := hub.Session("rollback"); err == nil {
		t.Fatal("the refused open left its session behind")
	}
	if sessions := hub.Sessions(context.Background()); len(sessions) != 0 {
		t.Fatalf("the refused open left %d sessions behind", len(sessions))
	}
	status, envelope = openSessionWith(t, server, "rollback", protocol.SessionOpenRequest{})
	if status != http.StatusOK {
		t.Fatalf("reopening the rolled-back id answered %d: a host that receives a refusal holds no session id it must clean up: %s", status, envelope.Payload)
	}
}

func TestOpenCarryingAnUnschematicMessageIsRefusedAtTheGate(t *testing.T) {
	hub, server := newServer(t, memoryRegistry(64), Options{})
	message := openMessage()
	message.Delivery = "not-a-delivery-mode"
	status, response := openSessionWith(t, server, "ungated", protocol.SessionOpenRequest{Message: message})
	if status != http.StatusBadRequest {
		t.Fatalf("open status %d, want 400: %s", status, response.Payload)
	}
	var failure protocol.ErrorResponse
	if err := response.DecodePayload(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != "schema_invalid" {
		t.Fatalf("refusal code %q, want schema_invalid: the open message is gated by the envelope schema", failure.Error.Code)
	}
	if sessions := hub.Sessions(context.Background()); len(sessions) != 0 {
		t.Fatalf("a gate refusal left %d sessions behind", len(sessions))
	}
}

func TestOpenSubscriptionIsAdoptedByTheEventsRequest(t *testing.T) {
	hub, server := newServer(t, memoryRegistry(64), Options{})
	descriptor, err := hub.Probe(context.Background(), "memory")
	if err != nil {
		t.Fatal(err)
	}
	request := requestEnvelope(t, protocol.TypeSessionOpenRequest, "open-adopt", protocol.SessionOpenRequest{
		SessionID: "adopt",
		Subscribe: true,
		Message:   openMessage(),
	}, "adopt", "", string(descriptor.CapabilityRevision))
	status, response := postEnvelope(t, server, "/adapters/memory/sessions", request)
	if status != http.StatusOK {
		t.Fatalf("open status %d: %s", status, response.Payload)
	}

	stream := connectSSE(t, server, "/sessions/adopt/events", "")
	defer stream.close()
	first := stream.drainUntil(protocol.TypeActionPermissionRequested)
	if len(first) == 0 || first[0].Sequence == nil || *first[0].Sequence != 1 {
		t.Fatalf("the adopted stream starts at %v, want the run's first envelope: the subscription was registered at the open so that it could not miss one", first[0].Sequence)
	}
}

func TestOpenSubscriptionNotAdoptedIsReleased(t *testing.T) {
	hub := serve.New(memoryRegistry(64), serve.Options{})
	daemon, err := New(hub, Options{SubscriptionHold: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(daemon.Handler())
	t.Cleanup(server.Close)
	descriptor, err := hub.Probe(context.Background(), "memory")
	if err != nil {
		t.Fatal(err)
	}
	request := requestEnvelope(t, protocol.TypeSessionOpenRequest, "open-abandon", protocol.SessionOpenRequest{
		SessionID: "abandon",
		Subscribe: true,
		Message:   openMessage(),
	}, "abandon", "", string(descriptor.CapabilityRevision))
	if status, response := postEnvelope(t, server, "/adapters/memory/sessions", request); status != http.StatusOK {
		t.Fatalf("open status %d: %s", status, response.Payload)
	}
	daemon.mu.Lock()
	holding := len(daemon.held)
	daemon.mu.Unlock()
	if holding != 1 {
		t.Fatalf("the open held %d subscriptions, want 1", holding)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		daemon.mu.Lock()
		remaining := len(daemon.held)
		daemon.mu.Unlock()
		if remaining == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("an unadopted subscription was never released")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestOpenRefusesSubscribeAgainstAnEndpointThatNeverAdvertisedIt(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("memory", withoutSubscribeAtOpen{base.NewMemory(base.Config{})}); err != nil {
		t.Fatal(err)
	}
	_, server := newServer(t, registry, Options{})
	status, envelope := openSessionWith(t, server, "unadvertised", protocol.SessionOpenRequest{Subscribe: true})
	if status != http.StatusBadRequest {
		t.Fatalf("open status %d, want 400: %s", status, envelope.Payload)
	}
	var failure protocol.ErrorResponse
	if err := envelope.DecodePayload(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Details["reason"] != base.ControlUnadvertised {
		t.Fatalf("refusal details %+v, want the capability rung answered first", failure.Error.Details)
	}
}

type withoutSubscribeAtOpen struct{ *base.Memory }

func (a withoutSubscribeAtOpen) Probe(ctx context.Context) (base.Descriptor, error) {
	descriptor, err := a.Memory.Probe(ctx)
	if err != nil {
		return descriptor, err
	}
	features := make(map[string]protocol.FeatureSupport, len(descriptor.Capabilities.Features))
	for key, support := range descriptor.Capabilities.Features {
		if key == protocol.FeatureOpenSubscribe {
			continue
		}
		features[key] = support
	}
	descriptor.Capabilities.Features = features
	return descriptor, nil
}

func driveScriptedRun(t *testing.T, hub *serve.Hub, server *httptest.Server, session protocol.SessionID, revision string) []protocol.Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	subscription, err := hub.Subscribe(ctx, session, serve.After("", 0))
	if err != nil {
		t.Fatalf("replaying the admitted run: %v", err)
	}
	defer subscription.Close()

	until := func(want protocol.EnvelopeType) []protocol.Envelope {
		var seen []protocol.Envelope
		for {
			envelope, err := subscription.Next()
			if err != nil {
				t.Fatalf("draining to %s: %v", want, err)
			}
			seen = append(seen, envelope)
			if envelope.Type == want {
				return seen
			}
		}
	}
	resolve := func(id string, typ protocol.EnvelopeType, payload any, run protocol.RunID) []protocol.Envelope {
		request := requestEnvelope(t, typ, id, payload, string(session), string(run), revision)
		status, response := postEnvelope(t, server, "/sessions/"+string(session)+"/resolve", request)
		if status != http.StatusOK {
			t.Fatalf("%s status %d: %s", typ, status, response.Payload)
		}
		return []protocol.Envelope{request, response}
	}

	initial := until(protocol.TypeActionPermissionRequested)
	permission := permissionRequestAt(t, initial)
	trace := append([]protocol.Envelope(nil), initial...)
	trace = append(trace, resolve("compound-resolve-permission", protocol.TypeActionPermissionResolveRequest, protocol.PermissionResolveRequest{
		InteractionID: permission.InteractionID, RequestedBy: permission.RequestedBy, RespondedBy: permission.RespondedBy,
		SessionID: session, RunID: permission.RunID, ChoiceID: "approve", Granted: true,
	}, permission.RunID)...)

	middle := until(protocol.TypeUserInputRequested)
	input := inputRequestAt(t, middle)
	trace = append(trace, middle...)
	trace = append(trace, resolve("compound-resolve-input", protocol.TypeUserInputResolveRequest, protocol.UserInputResolveRequest{
		InteractionID: input.InteractionID, RequestedBy: input.RequestedBy, RespondedBy: input.RespondedBy,
		SessionID: session, RunID: input.RunID,
		Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}},
	}, input.RunID)...)

	return append(trace, until(protocol.TypeRunCompleted)...)
}

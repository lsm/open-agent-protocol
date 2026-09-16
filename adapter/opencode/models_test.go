package opencode

import (
	"context"
	"errors"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/adapter/opencode/internal/native"
	"github.com/lsm/open-agent-protocol/protocol"
)

func consent() protocol.ModelsRequest {
	return protocol.ModelsRequest{SessionID: "session", AllowDegradedFeatures: []string{protocol.FeatureModelsList}}
}

// The catalog is advertised degraded, so the query needs the caller's opt-in:
// serving a degraded capability without consent is the failure the opt-in
// exists to prevent, and refusing it names the key to consent to.
func TestModelsRequiresTheDegradedOptin(t *testing.T) {
	client := newFakeClient()
	client.model = &native.ModelRef{ID: "claude-sonnet", ProviderID: "anthropic"}
	session, _ := openTest(t, client, 32)
	lister, ok := session.(base.ModelLister)
	if !ok {
		t.Fatal("the OpenCode session does not serve a catalog")
	}
	_, err := lister.Models(context.Background(), protocol.ModelsRequest{SessionID: "session"})
	var refusal *base.DegradedControlError
	if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureModelsList {
		t.Fatalf("a query without the opt-in was answered with %v", err)
	}
	catalog, err := lister.Models(context.Background(), consent())
	if err != nil {
		t.Fatal(err)
	}
	// The listing comes back labelled with the revision it was produced
	// under, so nothing downstream has to pair it with a separate probe.
	if catalog.Revision != CapabilityRevision {
		t.Fatalf("catalog cites revision %q, want %q", catalog.Revision, CapabilityRevision)
	}
	if catalog.Models.SessionID != "session" || catalog.Models.CurrentModelID != "anthropic/claude-sonnet" {
		t.Fatalf("catalog: %+v", catalog.Models)
	}
	if len(catalog.Models.Models) != 1 || catalog.Models.Models[0].ID != "anthropic/claude-sonnet" ||
		catalog.Models.Models[0].ProviderID != "anthropic" || !catalog.Models.Models[0].Default {
		t.Fatalf("descriptors: %+v", catalog.Models.Models)
	}
}

// A session created without a model has no catalog to serve until the durable
// stream names one: the adapter reports what it has evidence for and nothing
// else, and the model a step names joins the catalog exactly once.
func TestCatalogGrowsWithDurableStepEvidence(t *testing.T) {
	client := newFakeClient()
	client.promoted = true
	// Hold the fake's idle report until the whole stream is enqueued, so the
	// first step's settlement cannot fence before the second is durable.
	delivered := make(chan struct{})
	client.idleGate = delivered
	session, _ := openTest(t, client, 32)
	lister := session.(base.ModelLister)

	empty, err := lister.Models(context.Background(), consent())
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Models.Models) != 0 || empty.Models.CurrentModelID != "" {
		t.Fatalf("a session with no model evidence served %+v", empty.Models)
	}

	response, stream := submitTest(t, session)
	client.emit(t, 1, native.TypePrompted, native.PromptedData{Timestamp: 1, SessionID: client.session, MessageID: native.MessageID(response.MessageIDs[0]), Prompt: native.Prompt{Text: "hello"}, Delivery: native.DeliverySteer})
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{Timestamp: 2, SessionID: client.session, AssistantMessage: "msg_a1", Agent: "build", Model: native.ModelRef{ID: "m", ProviderID: "p"}})
	client.emit(t, 3, native.TypeStepEnded, native.StepEndedData{Timestamp: 3, SessionID: client.session, AssistantMessage: "msg_a1", Finish: "tool_use"})
	// The second step names the same model, so the catalog gains no entry.
	client.emit(t, 4, native.TypeStepStarted, native.StepStartedData{Timestamp: 4, SessionID: client.session, AssistantMessage: "msg_a2", Agent: "build", Model: native.ModelRef{ID: "m", ProviderID: "p"}})
	client.emit(t, 5, native.TypeTextEnded, native.TextEndedData{Timestamp: 5, SessionID: client.session, AssistantMessage: "msg_a2", TextID: "t2", Text: "done"})
	client.emit(t, 6, native.TypeStepEnded, native.StepEndedData{Timestamp: 6, SessionID: client.session, AssistantMessage: "msg_a2", Finish: "stop", Tokens: tokenAccounting(2, 5)})
	close(delivered)
	events := adaptertest.Drain(t, stream, 2*time.Second)

	adapter, err := New(Config{Endpoint: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := adapter.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.AssertProtocolValidWithDescriptor(t, response, descriptor, events)

	catalog, err := lister.Models(context.Background(), consent())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models.Models) != 1 || catalog.Models.Models[0].ID != "p/m" || catalog.Models.Models[0].ProviderID != "p" {
		t.Fatalf("two steps naming one model produced %+v", catalog.Models.Models)
	}
	// The session holds no default of its own, so the catalog claims none:
	// an accepted model_id is what a run attributes to, and nothing here has
	// told the session which model it would otherwise use.
	if catalog.Models.CurrentModelID != "" || catalog.Models.Models[0].Default {
		t.Fatalf("a session with no default claimed one: %+v", catalog.Models)
	}
}

// A catalog query names one session, and a query naming another is refused
// rather than answered with this one's models.
func TestModelsRefusesAForeignSession(t *testing.T) {
	session, _ := openTest(t, newFakeClient(), 32)
	_, err := session.(base.ModelLister).Models(context.Background(), protocol.ModelsRequest{
		SessionID: "other", AllowDegradedFeatures: []string{protocol.FeatureModelsList},
	})
	if !errors.Is(err, base.ErrRunNotFound) {
		t.Fatalf("a foreign catalog query was answered with %v", err)
	}
}

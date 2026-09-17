package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

func TestCatalogGrowsWithDurableStepEvidence(t *testing.T) {
	client := newFakeClient()
	client.promoted = true

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

	if empty.Models.Models == nil {
		t.Fatal("an empty catalog is nil, and marshals as null")
	}
	if encoded, err := json.Marshal(empty.Models); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(string(encoded), `"models":[]`) {
		t.Fatalf("an empty catalog encodes as %s", encoded)
	}

	response, stream := submitTest(t, session)
	client.emit(t, 1, native.TypePrompted, native.PromptedData{Timestamp: 1, SessionID: client.session, MessageID: native.MessageID(response.MessageIDs[0]), Prompt: native.Prompt{Text: "hello"}, Delivery: native.DeliverySteer})
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{Timestamp: 2, SessionID: client.session, AssistantMessage: "msg_a1", Agent: "build", Model: native.ModelRef{ID: "m", ProviderID: "p"}})
	client.emit(t, 3, native.TypeStepEnded, native.StepEndedData{Timestamp: 3, SessionID: client.session, AssistantMessage: "msg_a1", Finish: "tool_use"})

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

	if catalog.Models.CurrentModelID != "" || catalog.Models.Models[0].Default {
		t.Fatalf("a session with no default claimed one: %+v", catalog.Models)
	}
}

func TestModelsRefusesAForeignSession(t *testing.T) {
	session, _ := openTest(t, newFakeClient(), 32)
	_, err := session.(base.ModelLister).Models(context.Background(), protocol.ModelsRequest{
		SessionID: "other", AllowDegradedFeatures: []string{protocol.FeatureModelsList},
	})
	if !errors.Is(err, base.ErrRunNotFound) {
		t.Fatalf("a foreign catalog query was answered with %v", err)
	}
}

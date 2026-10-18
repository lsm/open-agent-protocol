package acp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/native"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func TestTheAgentsModelOptionIsADegradedModelsListWithItsCurrentValue(t *testing.T) {
	c := configuredClient()
	c.options[1] = native.ConfigOption{ID: "model", Category: "model", CurrentValue: "sol-medium", Options: json.RawMessage(`[{"value":"adaptive","name":"Adaptive"},{"group":"gpt","name":"GPT","options":[{"value":"sol-medium","name":"Sol Medium"},{"value":"luna-medium"}]}]`)}
	s, err := configuredAdapter(t, c, nil).Open(context.Background(), base.OpenRequest{SessionID: "s1", Participant: protocol.Participant{ID: "reviewbot"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	lister := s.(base.ModelLister)

	var degraded *base.DegradedControlError
	if _, err := lister.Models(context.Background(), protocol.ModelsRequest{SessionID: "s1"}); !errors.As(err, &degraded) || degraded.Feature != protocol.FeatureModelsList {
		t.Fatalf("without the degrade allowance: %v", err)
	}
	catalog, err := lister.Models(context.Background(), protocol.ModelsRequest{SessionID: "s1", AllowDegradedFeatures: []string{protocol.FeatureModelsList}})
	if err != nil {
		t.Fatal(err)
	}
	want := []protocol.ModelDescriptor{{ID: "adaptive", DisplayName: "Adaptive"}, {ID: "sol-medium", DisplayName: "Sol Medium", Default: true}, {ID: "luna-medium"}}
	if catalog.Revision != CapabilityRevision || catalog.Models.CurrentModelID != "sol-medium" || !reflect.DeepEqual(catalog.Models.Models, want) {
		t.Fatalf("catalog = %+v", catalog)
	}
	state, err := s.State(context.Background())
	if err != nil || state.CurrentModelID != "sol-medium" {
		t.Fatalf("state = %+v, %v", state, err)
	}
	if _, err := lister.Models(context.Background(), protocol.ModelsRequest{SessionID: "elsewhere", AllowDegradedFeatures: []string{protocol.FeatureModelsList}}); !errors.Is(err, base.ErrRunNotFound) {
		t.Fatalf("another session: %v", err)
	}
}

func TestAnAgentWithNoModelOptionRefusesTheModelsList(t *testing.T) {
	c := &configClient{fakeClient: newFake()}
	s, err := configuredAdapter(t, c, nil).Open(context.Background(), base.OpenRequest{SessionID: "s1", Participant: protocol.Participant{ID: "reviewbot"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	var unsupported *base.UnsupportedControlError
	_, err = s.(base.ModelLister).Models(context.Background(), protocol.ModelsRequest{SessionID: "s1", AllowDegradedFeatures: []string{protocol.FeatureModelsList}})
	if !errors.As(err, &unsupported) || unsupported.Feature != protocol.FeatureModelsList {
		t.Fatalf("err = %v", err)
	}
	if state, _ := s.State(context.Background()); state.CurrentModelID != "" {
		t.Fatalf("state = %+v", state)
	}
}

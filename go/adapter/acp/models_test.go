package acp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/rpc"
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

func TestASwitchSetsTheAgentsModelOptionToAListedValueAndConfirmsIt(t *testing.T) {
	c := configuredClient()
	c.options[1] = native.ConfigOption{ID: "picker", Category: "model", CurrentValue: "sol-medium", Options: json.RawMessage(`[{"value":"sol-medium"},{"value":"luna-medium"},{"value":"adaptive"}]`)}
	s, err := configuredAdapter(t, c, nil).Open(context.Background(), base.OpenRequest{SessionID: "s1", Participant: protocol.Participant{ID: "reviewbot"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	switcher := s.(base.ModelSwitcher)
	allowed := []string{protocol.FeatureSessionModelSwitch}

	var degraded *base.DegradedControlError
	if _, _, err := switcher.SwitchModel(context.Background(), protocol.SessionModelSwitchRequest{SessionID: "s1", ModelID: "luna-medium"}); !errors.As(err, &degraded) || degraded.Feature != protocol.FeatureSessionModelSwitch {
		t.Fatalf("without the degrade allowance: %v", err)
	}
	var missing *base.ModelNotFoundError
	if _, _, err := switcher.SwitchModel(context.Background(), protocol.SessionModelSwitchRequest{SessionID: "s1", ModelID: "nope", AllowDegradedFeatures: allowed}); !errors.As(err, &missing) || len(c.calls) != 0 {
		t.Fatalf("an unlisted model: %v, calls %+v", err, c.calls)
	}
	response, state, err := switcher.SwitchModel(context.Background(), protocol.SessionModelSwitchRequest{SessionID: "s1", ModelID: "luna-medium", AllowDegradedFeatures: allowed})
	if err != nil {
		t.Fatal(err)
	}
	if want := (protocol.SessionModelSwitchResponse{SessionID: "s1", ModelID: "luna-medium", PreviousModelID: "sol-medium"}); response != want || state.CurrentModelID != "luna-medium" {
		t.Fatalf("response = %+v, state = %+v", response, state)
	}
	if want := (native.SetConfigOptionParams{SessionID: "native-session", ConfigID: "picker", Value: "luna-medium"}); len(c.calls) != 1 || c.calls[0] != want {
		t.Fatalf("calls = %+v", c.calls)
	}

	var unsupported *base.UnsupportedControlError
	c.hook = func(*configClient, native.SetConfigOptionParams) error {
		return &rpc.RemoteError{Object: rpc.ErrorObject{Code: -32602, Message: "Invalid params"}}
	}
	if _, _, err := switcher.SwitchModel(context.Background(), protocol.SessionModelSwitchRequest{SessionID: "s1", ModelID: "adaptive", AllowDegradedFeatures: allowed}); !errors.As(err, &unsupported) || unsupported.Field != "model_id" {
		t.Fatalf("a rejected value: %v", err)
	}
	c.hook = func(c *configClient, _ native.SetConfigOptionParams) error {
		c.options[1].CurrentValue = "luna-medium"
		return nil
	}
	if _, _, err := switcher.SwitchModel(context.Background(), protocol.SessionModelSwitchRequest{SessionID: "s1", ModelID: "adaptive", AllowDegradedFeatures: allowed}); !errors.As(err, &unsupported) || unsupported.Field != "model_id" {
		t.Fatalf("an unconfirmed value: %v", err)
	}
	if state, _ := s.State(context.Background()); state.CurrentModelID != "luna-medium" {
		t.Fatalf("state = %+v", state)
	}
}

func TestAnAgentWithNoModelOptionRefusesAModelSwitch(t *testing.T) {
	c := &configClient{fakeClient: newFake()}
	s, err := configuredAdapter(t, c, nil).Open(context.Background(), base.OpenRequest{SessionID: "s1", Participant: protocol.Participant{ID: "reviewbot"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	var unsupported *base.UnsupportedControlError
	_, _, err = s.(base.ModelSwitcher).SwitchModel(context.Background(), protocol.SessionModelSwitchRequest{SessionID: "s1", ModelID: "luna-medium", AllowDegradedFeatures: []string{protocol.FeatureSessionModelSwitch}})
	if !errors.As(err, &unsupported) || unsupported.Feature != protocol.FeatureSessionModelSwitch || len(c.calls) != 0 {
		t.Fatalf("err = %v, calls %+v", err, c.calls)
	}
}

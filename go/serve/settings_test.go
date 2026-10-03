package serve_test

import (
	"context"
	"errors"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

type settingsAdapter struct {
	base.Adapter
	features map[string]protocol.FeatureSupport
}

func (a settingsAdapter) Probe(ctx context.Context) (base.Descriptor, error) {
	descriptor, err := a.Adapter.Probe(ctx)
	if err != nil {
		return descriptor, err
	}
	features := map[string]protocol.FeatureSupport{}
	for key, support := range descriptor.Capabilities.Features {
		features[key] = support
	}
	for key, support := range a.features {
		features[key] = support
	}
	descriptor.Capabilities.Features = features
	return descriptor, nil
}

func settingsHub(t *testing.T, features map[string]protocol.FeatureSupport) *serve.Hub {
	t.Helper()
	registry := serve.NewRegistry()
	if err := registry.Register("settings", settingsAdapter{Adapter: base.NewMemory(base.Config{JournalCapacity: 8}), features: features}); err != nil {
		t.Fatal(err)
	}
	return serve.New(registry, serve.Options{StreamQueue: 8})
}

func TestTheElectionGateRefusesASettingItsFeatureDoesNotTakeAtOpen(t *testing.T) {
	hub := settingsHub(t, map[string]protocol.FeatureSupport{
		protocol.FeatureSessionReasoning: {Level: protocol.SupportNative, Modes: []string{"session_live"}},
		protocol.FeatureCompactionPolicy: {Level: protocol.SupportUnavailable},
	})
	_, err := serve.ElectionGate(context.Background(), hub, "settings", "", protocol.SessionOpenRequest{SessionID: "s1", ReasoningLevel: protocol.ReasoningHigh})
	var refusal *base.UnsupportedControlError
	if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureSessionReasoning || refusal.Field != "reasoning_level" {
		t.Fatalf("gate answered %v, want unsupported_feature naming %s and its field", err, protocol.FeatureSessionReasoning)
	}
	_, err = serve.ElectionGate(context.Background(), hub, "settings", "", protocol.SessionOpenRequest{SessionID: "s1", CompactionPolicy: &protocol.CompactionPolicy{Kind: protocol.CompactionOff}})
	if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureCompactionPolicy || refusal.Field != "compaction_policy" {
		t.Fatalf("gate answered %v, want an unadvertised compaction policy refused", err)
	}
}

func TestTheElectionGateAdmitsASettingAtOpenAndHoldsADegradedOneToItsOptIn(t *testing.T) {
	hub := settingsHub(t, map[string]protocol.FeatureSupport{
		protocol.FeatureSessionReasoning: {Level: protocol.SupportNative, Modes: []string{protocol.ModeSessionOpen}},
		protocol.FeatureCompactionPolicy: {Level: protocol.SupportDegraded, Modes: []string{protocol.ModeSessionOpen}},
	})
	if _, err := serve.ElectionGate(context.Background(), hub, "settings", "", protocol.SessionOpenRequest{SessionID: "s1", ReasoningLevel: protocol.ReasoningMax}); err != nil {
		t.Fatalf("an advertised setting was refused: %v", err)
	}
	policy := &protocol.CompactionPolicy{Kind: protocol.CompactionOff}
	_, err := serve.ElectionGate(context.Background(), hub, "settings", "", protocol.SessionOpenRequest{SessionID: "s1", CompactionPolicy: policy})
	var degraded *base.DegradedControlError
	if !errors.As(err, &degraded) || degraded.Feature != protocol.FeatureCompactionPolicy {
		t.Fatalf("gate answered %v, want capability_degraded for an unconsented degraded setting", err)
	}
	if _, err := serve.ElectionGate(context.Background(), hub, "settings", "", protocol.SessionOpenRequest{SessionID: "s1", CompactionPolicy: policy, AllowDegradedFeatures: []string{protocol.FeatureCompactionPolicy}}); err != nil {
		t.Fatalf("a consented degraded setting was refused: %v", err)
	}
}

func TestAnAdapterRefusesASettingItsDescriptorDoesNotTakeAtOpen(t *testing.T) {
	descriptor := protocol.CapabilityDescriptor{Features: map[string]protocol.FeatureSupport{
		protocol.FeatureCompactionPolicy: {Level: protocol.SupportNative, Modes: []string{protocol.ModeSessionOpen}},
	}}
	if err := base.RefuseUnadvertisedSettings(base.OpenRequest{CompactionPolicy: &protocol.CompactionPolicy{Kind: protocol.CompactionTokens, Tokens: 1000}}, descriptor); err != nil {
		t.Fatalf("an advertised policy was refused: %v", err)
	}
	err := base.RefuseUnadvertisedSettings(base.OpenRequest{ReasoningLevel: protocol.ReasoningLow}, descriptor)
	var refusal *base.UnsupportedControlError
	if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureSessionReasoning || refusal.Field != "reasoning_level" {
		t.Fatalf("refusal = %v, want unsupported_feature naming the reasoning key and field", err)
	}
}

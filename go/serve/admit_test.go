package serve_test

import (
	"context"
	"errors"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

type admittingAdapter struct {
	*base.Memory
	refusing bool
	admitted int
	opened   int
}

func (a *admittingAdapter) Admit(context.Context, base.OpenRequest) error {
	a.admitted++
	if a.refusing {
		return &base.UnsupportedControlError{Feature: protocol.FeatureCompactionPolicy, Reason: base.ControlUnsatisfiable}
	}
	return nil
}

func (a *admittingAdapter) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	a.opened++
	return a.Memory.Open(ctx, request)
}

func TestAnAdmissionRefusalOutranksATakenNameAndATakenNameIsNeverOpened(t *testing.T) {
	adapter := &admittingAdapter{Memory: base.NewMemory(base.Config{})}
	registry := serve.NewRegistry()
	if err := registry.Register("admitting", adapter); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	if _, _, err := hub.Open(ctx, "admitting", base.OpenRequest{SessionID: "alpha"}); err != nil {
		t.Fatal(err)
	}
	adapter.refusing = true
	var refused *base.UnsupportedControlError
	if _, _, err := hub.Open(ctx, "admitting", base.OpenRequest{SessionID: "alpha"}); !errors.As(err, &refused) || refused.Feature != protocol.FeatureCompactionPolicy {
		t.Fatalf("a refused duplicate answered %v, want the admission refusal ahead of session_exists", err)
	}
	adapter.refusing = false
	var taken *serve.SessionExistsError
	if _, _, err := hub.Open(ctx, "admitting", base.OpenRequest{SessionID: "alpha"}); !errors.As(err, &taken) {
		t.Fatalf("an admitted duplicate answered %v, want session_exists", err)
	}
	if adapter.admitted != 3 || adapter.opened != 1 {
		t.Fatalf("admitted %d and opened %d, want 3 admissions and only the first open", adapter.admitted, adapter.opened)
	}
}

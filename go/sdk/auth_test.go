package sdk

import (
	"errors"
	"testing"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

func TestAuthListProvidersOnTheOAPWireCarriesTheKinds(t *testing.T) {
	client := newTestClient(t, scenarioOAP)

	providers, err := client.Auth.ListProviders(testContext(t))
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(providers) != 3 {
		t.Fatalf("got %d providers, want 3", len(providers))
	}
	if len(providers[0].AuthKinds) != 2 ||
		providers[0].AuthKinds[0] != protocol.CredentialKindAPIKey ||
		providers[0].AuthKinds[1] != protocol.CredentialKindOAuth {
		t.Errorf("anthropic kinds = %v, want [api_key oauth]", providers[0].AuthKinds)
	}
	if len(providers[1].AuthKinds) != 1 || providers[1].AuthKinds[0] != protocol.CredentialKindAPIKey {
		t.Errorf("weird kinds = %v, want [api_key] -- the known kind survives the unknown one", providers[1].AuthKinds)
	}
	if len(providers[2].AuthKinds) != 0 {
		t.Errorf("old kinds = %v, want empty for a runtime predating the field", providers[2].AuthKinds)
	}
	if providers[1].OverrideHost != "proxy.example" || providers[0].OverrideHost != "" {
		t.Errorf("override hosts = %q, %q, want \"\" and proxy.example", providers[0].OverrideHost, providers[1].OverrideHost)
	}
}

func TestAuthLoginRequiresAProviderID(t *testing.T) {
	client := newTestClient(t, scenarioOAP)

	err := client.Auth.Login(testContext(t), "", LoginHandlers{})
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *AuthError, got %T: %v", err, err)
	}
}

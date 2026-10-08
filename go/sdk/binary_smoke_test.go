package sdk

import (
	"context"
	"errors"
	"github.com/lsm/open-agent-protocol/go/providercatalog"
	"github.com/lsm/open-agent-protocol/providers"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const smokeProviders = `{"providers":[
{"id":"smoke","name":"Smoke","api":"openai-completions","base_url":"http://127.0.0.1:9/v1","auth":"none","models":["smoke-model"]},
{"id":"smoke-keyed","name":"Smoke Keyed","api":"openai-completions","base_url":"http://127.0.0.1:9/v1","auth":{"env":"OAP_SMOKE_KEY_THAT_IS_NEVER_SET"},"models":["keyed-model"]}]}`

func smokeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".oapx")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "providers.json"), []byte(smokeProviders), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func smokeEnviron(t *testing.T) []string {
	t.Helper()
	catalog, err := providercatalog.Load(providers.Files)
	if err != nil {
		t.Fatalf("load the provider catalog: %v", err)
	}
	credentials := map[string]bool{}
	for _, row := range catalog.Providers {
		for _, name := range row.CredentialEnv {
			credentials[name] = true
		}
	}
	var env []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !credentials[name] {
			env = append(env, entry)
		}
	}
	return env
}

func newSmokeClient(t *testing.T) *Client {
	return newSmokeClientWithClosePolicy(t, false)
}

func newFixtureSmokeClient(t *testing.T) *Client {
	return newFixtureSmokeClientWithClosePolicy(t, false)
}

func newFixtureSmokeClientWithClosePolicy(t *testing.T, allowNonzeroExit bool) *Client {
	t.Helper()
	binary := os.Getenv(EnvBinaryPath)
	if binary == "" {
		t.Skip("OAP_SDK_BINARY_PATH is not set")
	}

	home := smokeHome(t)
	env := append(smokeEnviron(t),
		"HOME="+home,
		"XDG_CONFIG_HOME="+home,
		"OAPX_KEYCHAIN_SERVICE=com.makai.go-sdk-test."+newULID(),
		"OAPX_TEST_FIXTURE_PROVIDER=1",
	)

	client, err := New(context.Background(), &Options{
		BinaryPath:       binary,
		Env:              env,
		HandshakeTimeout: 15 * time.Second,
		RequestTimeout:   30 * time.Second,
	})
	if err != nil {
		t.Fatalf("New against the real runtime: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil && !allowNonzeroExit {
			t.Errorf("Close: %v", err)
		}
	})
	return client
}

func newSmokeClientWithClosePolicy(t *testing.T, allowNonzeroExit bool) *Client {
	t.Helper()
	binary := os.Getenv(EnvBinaryPath)
	if binary == "" {
		t.Skip("OAP_SDK_BINARY_PATH is not set")
	}

	home := smokeHome(t)
	env := append(smokeEnviron(t),
		"HOME="+home,
		"XDG_CONFIG_HOME="+home,
		"OAPX_KEYCHAIN_SERVICE=com.makai.go-sdk-test."+newULID(),
	)

	client, err := New(context.Background(), &Options{
		BinaryPath:       binary,
		Env:              env,
		HandshakeTimeout: 15 * time.Second,
		RequestTimeout:   30 * time.Second,
	})
	if err != nil {
		t.Fatalf("New against the real runtime: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil && !allowNonzeroExit {
			t.Errorf("Close: %v", err)
		}
	})
	return client
}

func TestSmokeHandshakeAndClose(t *testing.T) {
	client := newSmokeClient(t)
	if client.transport.cmd.Process == nil {
		t.Fatal("expected a running runtime process")
	}
}

func TestSmokeModelsList(t *testing.T) {
	client := newSmokeClient(t)

	response, err := client.Models.List(testContext(t), ListModelsRequest{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(response.Models) == 0 {
		t.Fatal("expected the runtime's catalog to list at least one model")
	}
	if response.FetchedAt.IsZero() {
		t.Error("FetchedAt was not populated")
	}

	for _, model := range response.Models {
		if model.ModelRef == "" || model.ModelID == "" || model.ProviderID == "" || model.API == "" {
			t.Fatalf("incomplete descriptor: %+v", model)
		}
		if !smokeAuthStatuses[model.AuthStatus] {
			t.Errorf("unknown auth status %q on %s", model.AuthStatus, model.ModelRef)
		}
	}
}

func TestSmokeModelsListFiltersByProvider(t *testing.T) {
	client := newSmokeClient(t)
	ctx := testContext(t)

	all, err := client.Models.List(ctx, ListModelsRequest{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	provider := all.Models[0].ProviderID

	filtered, err := client.Models.List(ctx, ListModelsRequest{ProviderID: provider})
	if err != nil {
		t.Fatalf("filtered List: %v", err)
	}
	if len(filtered.Models) == 0 {
		t.Fatalf("filtering by %q returned nothing", provider)
	}
	for _, model := range filtered.Models {
		if model.ProviderID != provider {
			t.Errorf("filter leaked %q into a %q listing", model.ProviderID, provider)
		}
	}
}

func TestSmokeModelsResolveRoundTrip(t *testing.T) {
	client := newSmokeClient(t)
	ctx := testContext(t)

	all, err := client.Models.List(ctx, ListModelsRequest{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := all.Models[0]

	resolved, err := client.Models.Resolve(ctx, ResolveModelRequest{
		ProviderID: want.ProviderID, API: want.API, ModelID: want.ModelID,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if resolved.ModelRef != want.ModelRef {
		t.Errorf("ModelRef = %q, want %q", resolved.ModelRef, want.ModelRef)
	}
}

func TestSmokeModelsResolveRejectsAnUnknownModel(t *testing.T) {
	client := newSmokeClient(t)

	_, err := client.Models.Resolve(testContext(t), ResolveModelRequest{
		ProviderID: "smoke", ModelID: "no-such-model-" + newULID(),
	})
	var protocolErr *ProtocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("expected *ProtocolError, got %T: %v", err, err)
	}
	if protocolErr.Code != CodeInvalidRequest {
		t.Errorf("Code = %q, want %q (message %q)", protocolErr.Code, CodeInvalidRequest, protocolErr.Message)
	}
}

func TestSmokeAuthListProvidersWithoutTheOptInOmitsTheFixture(t *testing.T) {
	client := newSmokeClient(t)

	providers, err := client.Auth.ListProviders(testContext(t))
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(providers) == 0 {
		t.Fatal("expected the runtime to list auth providers")
	}
	for _, provider := range providers {
		if provider.ID == "test-fixture" {
			t.Fatalf("a client that did not set the opt-in was offered the CI fixture: %+v", provider)
		}
	}
}

func TestSmokeAuthListProviders(t *testing.T) {
	client := newFixtureSmokeClient(t)

	providers, err := client.Auth.ListProviders(testContext(t))
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(providers) == 0 {
		t.Fatal("expected the runtime to list auth providers")
	}
	for _, provider := range providers {
		if provider.ID == "" || provider.Name == "" {
			t.Errorf("incomplete provider entry: %+v", provider)
		}
		if !smokeAuthStatuses[provider.Status] {
			t.Errorf("unknown status %q on %q", provider.Status, provider.ID)
		}
	}
	requireFixtureAuthProvider(t, client)
}

func requireFixtureAuthProvider(t *testing.T, client *Client) {
	t.Helper()
	providers, err := client.Auth.ListProviders(testContext(t))
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	for _, provider := range providers {
		if provider.ID == "test-fixture" {
			return
		}
	}
	t.Fatalf("the runtime was asked for the test-fixture auth provider by name and does not offer it")
}

func TestSmokeAuthManualCodeFailsClosed(t *testing.T) {
	client := newFixtureSmokeClient(t)
	requireFixtureAuthProvider(t, client)
	var events []AuthEventType
	err := client.Auth.Login(testContext(t), "test-fixture", LoginHandlers{
		OnEvent: func(event AuthEvent) { events = append(events, event.Type) },
	})
	var authErr *AuthError
	if !errors.As(err, &authErr) || authErr.Kind != AuthKindProviderError || authErr.Code != "auth_input_unavailable" {
		t.Fatalf("manual login should fail closed: %v", err)
	}
	var sawURL, sawProgress bool
	for _, event := range events {
		switch event {
		case AuthEventURL:
			sawURL = true
		case AuthEventProgress:
			sawProgress = true
		}
	}
	if !sawURL || !sawProgress {
		t.Errorf("events = %v; expected URL and progress before refusal", events)
	}
}

func TestSmokeAuthLoginRejectsAnUnknownProvider(t *testing.T) {
	client := newSmokeClient(t)

	err := client.Auth.Login(testContext(t), "definitely-not-a-provider", LoginHandlers{})
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *AuthError, got %T: %v", err, err)
	}
	if authErr.Kind == AuthKindTransportError && strings.Contains(authErr.Message, "timed out") {
		t.Errorf("an unknown provider should be rejected, not time out: %v", authErr)
	}
}

func TestSmokeProviderCompleteWithoutCredentials(t *testing.T) {
	client := newSmokeClient(t)
	ctx := testContext(t)

	models, err := client.Models.List(ctx, ListModelsRequest{ProviderID: "smoke-keyed"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models.Models) == 0 {
		t.Fatal("the runtime lists no model for the declared endpoint whose key is unset")
	}

	_, err = client.Provider.Complete(ctx, CompletionRequest{
		ModelRef: models.Models[0].ModelRef,
		Messages: []Message{UserMessage("hello")},
		Options:  &RunOptions{MaxTokens: MaxTokens(16)},
	})
	if err == nil {
		t.Fatal("a call to an endpoint whose key is unset completed")
	}
	var streamErr *StreamError
	if !errors.As(err, &streamErr) {
		t.Fatalf("expected *StreamError, got %T: %v", err, err)
	}
	assertAuthFailureIsTyped(t, err, streamErr)
	t.Logf("uncredentialed completion failed as expected: kind=%s code=%s", streamErr.Kind, streamErr.Code)
}

func TestSmokeProviderAsksForACredentialForACatalogProviderWithoutAKey(t *testing.T) {
	client := newSmokeClient(t)
	_, err := client.Provider.Complete(testContext(t), CompletionRequest{
		ModelRef: "anthropic/anthropic-messages@any-model",
		Messages: []Message{UserMessage("hello")},
		Options:  &RunOptions{MaxTokens: MaxTokens(16)},
	})
	var authErr *AuthRequiredError
	if !errors.As(err, &authErr) {
		t.Fatalf("a catalog provider with no key should ask for a credential, got %T: %v", err, err)
	}
	if authErr.ProviderID != "anthropic" {
		t.Errorf("ProviderID = %q, want anthropic", authErr.ProviderID)
	}
}

func assertAuthFailureIsTyped(t *testing.T, err error, streamErr *StreamError) {
	t.Helper()
	if streamErr.Code != CodeAuthRequired {
		return
	}
	var authErr *AuthRequiredError
	if !errors.As(err, &authErr) {
		t.Fatalf("an auth_required failure should surface as *AuthRequiredError, got %T", err)
	}
	if authErr.ProviderID == "" {
		t.Error("an *AuthRequiredError must name the provider to log in to")
	}
}

func TestSmokeAgentRunWithoutCredentials(t *testing.T) {
	client := newSmokeClient(t)
	ctx := testContext(t)

	sessionID, err := client.Agent.OpenSession(ctx, "")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	_, current, err := client.Agent.ListSessionModels(ctx, sessionID)
	if err != nil {
		t.Fatalf("ListSessionModels: %v", err)
	}
	if current == "" {
		t.Fatal("the agent names no current model")
	}

	_, err = client.Agent.Run(ctx, AgentRequest{
		ModelRef: current,
		Messages: []Message{UserMessage("hello")},
	})
	if err == nil {
		t.Fatal("a run on an endpoint whose key is unset completed")
	}
	var streamErr *StreamError
	if !errors.As(err, &streamErr) {
		t.Fatalf("expected *StreamError, got %T: %v", err, err)
	}
	if streamErr.Kind == KindTransportError && strings.Contains(streamErr.Message, "timed out") {
		t.Fatalf("the run should fail, not time out: %v", streamErr)
	}
	assertAuthFailureIsTyped(t, err, streamErr)
	t.Logf("uncredentialed agent run failed as expected: kind=%s code=%s", streamErr.Kind, streamErr.Code)

	if _, err := client.Models.List(ctx, ListModelsRequest{}); err != nil {
		t.Fatalf("the client should stay usable after a failed run: %v", err)
	}
}

func TestSmokeRejectsMalformedInboundFrames(t *testing.T) {
	client := newSmokeClientWithClosePolicy(t, true)
	ctx := testContext(t)

	if _, err := client.transport.stdin.Write([]byte("this is not a frame\n")); err != nil {
		t.Fatalf("writing junk: %v", err)
	}

	_, err := client.Models.List(ctx, ListModelsRequest{})
	if err == nil {
		t.Fatal("List after malformed input unexpectedly succeeded")
	}
	var streamErr *StreamError
	if !errors.As(err, &streamErr) || streamErr.Kind != KindTransportError {
		t.Fatalf("expected a transport failure after malformed input, got %T: %v", err, err)
	}
	if err := client.Close(); err == nil {
		t.Fatal("runtime accepted malformed input without an error exit")
	}
}

func TestSmokeConcurrentRequests(t *testing.T) {
	client := newSmokeClient(t)
	ctx := testContext(t)

	const calls = 4
	errs := make(chan error, calls*2)
	for i := 0; i < calls; i++ {
		go func() {
			_, err := client.Models.List(ctx, ListModelsRequest{})
			errs <- err
		}()
		go func() {
			_, err := client.Auth.ListProviders(ctx)
			errs <- err
		}()
	}
	for i := 0; i < calls*2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent request: %v", err)
		}
	}
}

func TestSmokeCloseTerminatesTheRuntime(t *testing.T) {
	binary := os.Getenv(EnvBinaryPath)
	if binary == "" {
		t.Skip("OAP_SDK_BINARY_PATH is not set")
	}
	home := t.TempDir()

	client, err := New(context.Background(), &Options{
		BinaryPath: binary,
		Env: append(os.Environ(),
			"HOME="+home,
			"OAPX_KEYCHAIN_SERVICE=com.makai.go-sdk-test."+newULID(),
		),
		HandshakeTimeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if client.transport.cmd.ProcessState == nil {
		t.Fatal("expected the runtime to be reaped")
	}

	if _, err := client.Models.List(testContext(t), ListModelsRequest{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed after Close, got %v", err)
	}
}

var smokeAuthStatuses = map[AuthStatus]bool{
	AuthAuthenticated: true, AuthLoginRequired: true, AuthExpired: true,
	AuthRefreshing: true, AuthLoginInProgress: true, AuthFailed: true, AuthUnknown: true,
}

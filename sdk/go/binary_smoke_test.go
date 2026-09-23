package makai

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// These tests drive a real `oapx --stdio` runtime. They are skipped unless
// OAP_SDK_BINARY_PATH names one:
//
//	zig build install --prefix /tmp/oapx-go
//	OAP_SDK_BINARY_PATH=/tmp/oapx-go/bin/oapx go test -race ./...
//
// newSmokeClient starts a client against the real runtime, with its
// credential storage isolated from the developer's own.
func newSmokeClient(t *testing.T) *Client {
	return newSmokeClientWithClosePolicy(t, false)
}

func newSmokeClientWithClosePolicy(t *testing.T, allowNonzeroExit bool) *Client {
	t.Helper()
	binary := os.Getenv(EnvBinaryPath)
	if binary == "" {
		t.Skip("OAP_SDK_BINARY_PATH is not set")
	}

	// The runtime reads and writes real credential storage. Point it at a
	// scratch home, and on macOS at a keychain service that does not exist,
	// so a smoke run cannot read, write or prompt for the developer's own
	// credentials. Without the keychain override the runtime blocks on an
	// interactive keychain prompt that a test process cannot answer.
	home := t.TempDir()
	env := append(os.Environ(),
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
		if !knownAuthStatuses[model.AuthStatus] {
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
	// A ref from the catalog resolves back to the same opaque handle.
	if resolved.ModelRef != want.ModelRef {
		t.Errorf("ModelRef = %q, want %q", resolved.ModelRef, want.ModelRef)
	}
}

func TestSmokeModelsResolveRejectsAnUnknownModel(t *testing.T) {
	client := newSmokeClient(t)

	_, err := client.Models.Resolve(testContext(t), ResolveModelRequest{
		ProviderID: "anthropic", ModelID: "no-such-model-" + newULID(),
	})
	var protocolErr *ProtocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("expected *ProtocolError, got %T: %v", err, err)
	}
	if protocolErr.Code != CodeInvalidRequest {
		t.Errorf("Code = %q, want %q (message %q)", protocolErr.Code, CodeInvalidRequest, protocolErr.Message)
	}
}

func TestSmokeAuthListProviders(t *testing.T) {
	client := newSmokeClient(t)

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
		if !knownAuthStatuses[provider.Status] {
			t.Errorf("unknown status %q on %q", provider.Status, provider.ID)
		}
	}
}

// requireFixtureAuthProvider skips unless the runtime offers its offline
// test-fixture auth provider.
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
	t.Skip("the runtime does not offer the test-fixture auth provider")
}

func TestSmokeAuthManualCodeFailsClosed(t *testing.T) {
	client := newSmokeClient(t)
	requireFixtureAuthProvider(t, client)
	var events []AuthEventType
	called := false
	err := client.Auth.Login(testContext(t), "test-fixture", LoginHandlers{
		OnEvent: func(event AuthEvent) { events = append(events, event.Type) },
		OnPrompt: func(ctx context.Context, prompt AuthPrompt) (string, error) {
			called = true
			return "SENSITIVE_TEST_CODE", nil
		},
	})
	var authErr *AuthError
	if !errors.As(err, &authErr) || authErr.Kind != AuthKindProviderError || authErr.Code != "auth_input_unavailable" {
		t.Fatalf("manual login should fail closed: %v", err)
	}
	if called {
		t.Fatal("OAP invoked the answer handler")
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

	models, err := client.Models.List(ctx, ListModelsRequest{ProviderID: "anthropic"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models.Models) == 0 {
		t.Skip("the runtime lists no anthropic models")
	}

	// With no credentials in the scratch home, the call must fail cleanly
	// rather than hanging or panicking. Which failure it is depends on how
	// far the runtime gets, so this asserts the shape, not the code.
	_, err = client.Provider.Complete(ctx, CompletionRequest{
		ModelRef: models.Models[0].ModelRef,
		Messages: []Message{UserMessage("hello")},
		Options:  &RunOptions{MaxTokens: MaxTokens(16)},
	})
	if err == nil {
		t.Skip("the runtime completed the call, so credentials were available after all")
	}
	var streamErr *StreamError
	if !errors.As(err, &streamErr) {
		t.Fatalf("expected *StreamError, got %T: %v", err, err)
	}
	assertAuthFailureIsTyped(t, err, streamErr)
	t.Logf("uncredentialed completion failed as expected: kind=%s code=%s", streamErr.Kind, streamErr.Code)
}

// assertAuthFailureIsTyped checks that a runtime failure carrying an auth
// code reaches the caller as the recoverable typed error, not just as a
// generic stream failure.
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

	models, err := client.Models.List(ctx, ListModelsRequest{ProviderID: "anthropic"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models.Models) == 0 {
		t.Skip("the runtime lists no anthropic models")
	}

	_, err = client.Agent.Run(ctx, AgentRequest{
		ModelRef: models.Models[0].ModelRef,
		Messages: []Message{UserMessage("hello")},
	})
	if err == nil {
		t.Skip("the runtime completed the run, so credentials were available after all")
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

	// The session was released, so the same client can start another run.
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

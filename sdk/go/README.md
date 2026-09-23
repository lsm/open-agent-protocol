# OAP Go SDK

The Go module at `sdk/go` uses the combined OAP 0.1 stdio endpoint by default:

```text
oapx serve agent,provider --stdio
```

One child process carries profiled `agent-control-core` and `model-provider-core` envelopes. The package remains named `makai` for source compatibility. The old Makai V1 wire is available only with `Options{LegacyWire: true}`; the SDK never silently falls back to it.

## Install and connect

```bash
go get github.com/lsm/open-agent-protocol/sdk/go
```

```go
import makai "github.com/lsm/open-agent-protocol/sdk/go"

client, err := makai.New(ctx, nil)
if err != nil { return err }
defer client.Close()
```

The binary resolver checks `OAP_SDK_BINARY_PATH`, an explicitly configured path or checksum-verified URL, local `zig-out/bin/oapx` or `zig/zig-out/bin/oapx`, then `PATH`. To select a binary directly, pass `&makai.Options{BinaryPath: "/path/to/oapx"}`. A context cancellation stops the current call without closing the client; `Close` owns and reaps the child process.

## Direct model-provider inference

`client.Models` uses the provider profile's model catalog. Model refs are returned by the runtime and must be treated as opaque.

```go
model, err := client.Models.Resolve(ctx, makai.ResolveModelRequest{
    ProviderID: "anthropic", ModelID: "claude-sonnet-4-5",
})
if err != nil { return err }

response, err := client.Provider.Complete(ctx, makai.CompletionRequest{
    ModelRef: model.ModelRef,
    Messages: []makai.Message{makai.UserMessage("Write a haiku about streams.")},
    Options: &makai.RunOptions{MaxTokens: makai.MaxTokens(128)},
})
if err != nil { return err }
fmt.Println(response.Message.Text)
```

`client.Provider.Stream` returns a `ProviderStream` with `Next`, `Event`, `Err`, and `Close`. Provider tool definitions map to OAP `input_schema`; a direct provider tool call is returned to the caller, not executed by the SDK.

## Agent sessions and live model switching

`client.Agent` uses the agent profile. A session can select its default model between runs, and a per-submit `ModelRef` overrides it for that run. `ListSessionModels` reads the session's effective catalog, which is distinct from direct provider discovery.

```go
sessionID, err := client.Agent.OpenSession(ctx, "my-session")
if err != nil { return err }
models, _, err := client.Agent.ListSessionModels(ctx, sessionID)
if err != nil { return err }
_, err = client.Agent.SwitchModel(ctx, sessionID, models[0].ID)
if err != nil { return err }

response, err := client.Agent.Run(ctx, makai.AgentRequest{
    Messages: []makai.Message{makai.UserMessage("Hello")},
    Options: &makai.RunOptions{SessionID: sessionID},
})
```

`SwitchModel` changes future runs, not a run already in progress. OAP session IDs are opaque nonempty strings; the old 21-character NanoID restriction applies only to explicit Makai V1 mode. `AttachProvider` sends the optional `session.provider.attach` extension. An endpoint that does not offer attachment refuses it with a typed `unsupported_feature` error; it does not attach implicitly. Remote provider services are a follow-up.

The current OAP agent endpoint does not advertise client-executed `+control-tools`. Passing `AgentRequest.Tools` fails explicitly with `unsupported_feature`; no callback is silently dropped. Agent per-run `MaxTokens`, `Temperature`, and `ReasoningEffort` also have no OAP 0.1 submit projection and fail explicitly. `RunOptions.Metadata` maps to submit metadata.

## Authentication

`client.Auth.ListProviders(ctx)` and `client.Auth.Login(ctx, providerID, handlers)` use agent-profile `+auth` on the same local stdio connection. URL and progress events reach the handlers. The runtime owns credentials; no login code or prompt answer travels in an OAP envelope. A manual-code flow without host-owned input fails with `auth_input_unavailable`.

```go
err := client.Auth.Login(ctx, "anthropic", makai.LoginHandlers{
    OnEvent: func(event makai.AuthEvent) {
        if event.Type == makai.AuthEventURL { fmt.Println(event.URL) }
    },
})
```

`OnPrompt` is only used with explicit Makai V1 compatibility mode, never OAP. Go uses manual retry: a typed `*AuthRequiredError` from provider or agent calls can be followed by `Login` and one new call. Only typed `auth_required` and `credential_*` failures are classified this way; arbitrary provider errors are not.

## Configuration and compatibility

`Options` can set `Args`, `Dir`, `Env`, timeouts, logger, and binary resolver settings. The default args are `[]string{"serve", "agent,provider", "--stdio"}`. For an old runtime only, set `LegacyWire: true`, which selects the old `--stdio` launch and V1 handshake. This is an explicit compatibility path, not a fallback.

Typed failures include `*StreamError`, `*AuthRequiredError`, `*ProtocolError`, and `*AuthError`. Unsupported OAP features carry code `unsupported_feature`.

Run `go test ./...` and `go vet ./...` from `sdk/go`. Fake-host tests need no API keys; a built `zig/zig-out/bin/oapx` enables the live OAP smoke test.

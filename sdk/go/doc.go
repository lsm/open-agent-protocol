// Package makai is a Go SDK for OAP 0.1 with a source-compatible package name.
//
// By default the SDK spawns `oapx serve agent,provider --stdio` and exposes
// four namespaces over profiled newline-delimited OAP envelopes:
//
//   - [Client.Auth] lists auth providers and drives interactive login flows.
//   - [Client.Models] lists and resolves models.
//   - [Client.Provider] runs direct provider completions, buffered or streamed.
//   - [Client.Agent] opens sessions, switches models, and runs the agent loop.
//
// Every call that performs I/O takes a [context.Context] as its first
// argument. Cancelling that context stops the call promptly, sends a
// best-effort cancellation frame to the runtime, and releases the call's frame
// route. It does not tear down the client: [Client.Close] owns the child
// process lifetime.
//
// A minimal session looks like this:
//
//	client, err := makai.New(ctx, nil)
//	if err != nil {
//		return err
//	}
//	defer client.Close()
//
//	model, err := client.Models.Resolve(ctx, makai.ResolveModelRequest{
//		ProviderID: "anthropic",
//		API:        "anthropic-messages",
//		ModelID:    "claude-sonnet-4-5",
//	})
//	if err != nil {
//		return err
//	}
//
//	resp, err := client.Provider.Complete(ctx, makai.CompletionRequest{
//		ModelRef: model.ModelRef,
//		Messages: []makai.Message{makai.UserMessage("Write a haiku about streams.")},
//	})
//
// # Opaque model refs
//
// ModelRef values are server-issued handles. Application code must treat them
// as opaque: do not parse them, do not build them by hand, and do not derive
// provider or model identity from their text. Obtain them from
// [ModelsService.List] or [ModelsService.Resolve].
//
// # Agent sessions
//
// Session IDs are opaque nonempty strings. [AgentService.SwitchModel] changes
// the session's default model for future runs. [AgentService.AttachProvider]
// sends the optional provider-attachment request; unsupported endpoints refuse
// it explicitly. Client-executed agent tools and per-run agent sampling/token
// options are not represented by the current OAP endpoint and are refused
// with unsupported_feature, not silently ignored.
//
// Set [Options.LegacyWire] only to connect to an old Makai V1 runtime. There
// is no automatic fallback from OAP to the legacy wire.
//
// # Errors
//
// Failures are returned as typed errors reachable through [errors.As]:
// [StreamError] for provider, transport and abort failures on provider/agent
// calls, [AuthRequiredError] (which unwraps to [StreamError]) when a provider
// needs a login, [ProtocolError] for model-discovery protocol failures, and
// [AuthError] for auth listing and login-flow failures. Errors caused by a
// cancelled context wrap the context error, so [errors.Is] against
// [context.Canceled] and [context.DeadlineExceeded] works.
package makai

# The Go library

`go/` is a Go library first and a command second. This guide says what the
library is for, what each public package does, and which file in `goap` uses
it — so a reader can go from a question to the code, without doc comments,
which the repository's zero-comment rule forbids.

Every cell in the last column is a grep over `go/cmd/goap`'s own imports, and
the cells that read `—` are read that way on purpose: nothing under
`go/cmd/goap` imports `go/client`, and `schema` is reached through
`go/validation` rather than directly.

## The tools are not libraries

Two `package main` programs live under `go/tools` and are **not** in the public
set, because a program cannot import them: `goprintable` walks the Unicode
scalar range through `strconv.IsPrint` and writes the printable spans into
`zig/src/adapter/goquote_table.zig`, which the Zig adapter's codec
binary-searches for `strconv.Quote`-faithful quoting — a generator for the other
tree, recorded in the ACP v1.7.0 ledger — and `nocomment` is the zero-comment
checker this repository gates itself with. Neither is in the table above:
`publicset.Public` returns false for each, `goap check` counts them as binaries
rather than checking them against the public set, and a program runs them with
`go run` rather than importing them.

It is not a tutorial. The three runnable examples are the tutorial, and they
are compiled and run by `go test`:

| what it shows | where |
| --- | --- |
| a prompt to a result, with interactions answered by a policy function | `ExampleRunToTerminal` in `go/adapter/prompt_example_test.go` |
| driving `goap serve` over HTTP + SSE from a Go program | `Example` in `go/client/example_test.go` |
| resuming an event stream after a gap | `ExampleSession_EventsAfter` in `go/client/example_test.go` |

## What it is for

A Go program uses this library to **drive a coding harness** — Claude Code,
Codex, ACP agents, pi, Hermes, OpenCode, DeepSeek — and to **serve OAP** to
other programs. Two directions, one set of types:

- **Outbound.** Open a harness, submit messages, stream a run, answer its
  interactions, cancel it, resume a run after a gap, and read the result. The
  harness runs as a child process or as a server; the adapter is the boundary.
- **Inbound.** Serve the protocol over stdio, HTTP + SSE, or as an endpoint
  embedded in your own binary, with a registry of adapters and tool sources
  behind it.

`Decision 0038` makes this the permanent Go tree, first-class beside the Zig
one, and `Decision 0032` is why the two trees are peers rather than one being
a reference implementation of the other.

## The public packages

This list is enforced, not documentation: `go/internal/publicset` names every
public and internal package, and `goap check` fails on a directory in neither
set. So a package cannot become public by being added, and cannot quietly stop
being one.

| package | what it is for | `goap` file that uses it |
| --- | --- | --- |
| `go/protocol` | The wire: envelope types, payloads, the status and code vocabularies, and the codecs. Every other package speaks in these types, and nothing else defines them. | `go/cmd/goap/main.go`, `conformance.go` |
| `go/validation` | The validator: the embedded schema bytes, the semantic rules, and the fixture gate. `goap validate` is the command over it — `oapx validate` is the Zig binary's own partial validator, which says so itself. | `go/cmd/goap/main.go`, `conformance.go` |
| `go/adapter` | The adapter boundary, and `RunToTerminal` — the shortest path from a prompt to a response. | `go/cmd/goap/main.go` |
| `go/adapter/claude` | Claude Code, over the Agent SDK's stdio protocol. | `go/cmd/goap/harnesses.go` |
| `go/adapter/codex/appserver` | The Codex app-server, over its JSON-RPC stdio protocol. | `go/cmd/goap/harnesses.go` |
| `go/adapter/acp` | Any agent speaking the Agent Client Protocol. | `go/cmd/goap/harnesses.go` |
| `go/adapter/pi` | pi, over its own JSONL command protocol. | `go/cmd/goap/harnesses.go` |
| `go/adapter/hermes` | Hermes, over the `tui_gateway` JSON-RPC protocol. | `go/cmd/goap/harnesses.go` |
| `go/adapter/opencode` | OpenCode, over its HTTP API and event stream. | `go/cmd/goap/harnesses.go` |
| `go/adapter/deepseek` | The DeepSeek harness, over its SDK wire. | `go/cmd/goap/harnesses.go` |
| `go/adapter/adaptertest` | The conformance harness the adapter corpora run under: the protocol trace helpers a port replays. Public so a port can run the same cases. | `go/cmd/goap/main.go` (the `demo` verb) |
| `go/harness` | Harness pins, tools and capabilities: the registry that loads `harnesses/*.json` and decides what a request may ask for. | `go/cmd/goap/harnesses.go` |
| `go/providercatalog` | Provider and model facts, and the rule that an unpublished entry is unknown. | `go/cmd/goap/providers.go` |
| `go/serve` | The hub: sessions, runs, subscriptions, the snapshot and the close semantics, over a transport-agnostic core. | `go/cmd/goap/serve.go`, `endpoint.go` |
| `go/serve/servehttp` | The HTTP + SSE binding of the hub, including the body gate, the allowlists and the stream endings. | `go/cmd/goap/serve.go` |
| `go/serve/serveendpoint` | The endpoint binding: raw OAP envelopes over a pair of streams, `Server.Run(ctx, io.Reader, io.Writer)`, with no listener and no HTTP — the stdio form the endpoint profile specifies. The embeddable **HTTP** handler is `servehttp.Server.Handler()`. | `go/cmd/goap/endpoint.go` |
| `go/serve/servestdio` | The stdio binding: fourteen ops over a pipe, with its own op set rather than the endpoint's raw envelopes. | `go/cmd/goap/serve.go` |
| `go/client` | A Go client for the hub's HTTP + SSE wire. Also the far-side proof that the wire is implementable from outside this module. | — |
| `go/sdk` | A client for a **running endpoint**: it spawns `oapx serve agent,provider --stdio` and exposes four namespaces over profiled newline-delimited OAP envelopes — `Auth`, `Models`, `Provider` and `Agent`. Where `go/client` speaks the hub's HTTP + SSE wire to a daemon you started, this speaks the stdio wire to a process it owns. | `go/cmd/goap` does not use it; it is the library a Go program embeds |
| `harnesses` | The embedded harness pin catalogue, so a consumer reads the same pins the repository does. | `go/cmd/goap/harnesses.go` |
| `providers` | The embedded provider catalogue. | `go/cmd/goap/providers.go` |
| `schema` | The embedded JSON Schema bytes for `v0.1`, which `go/validation` loads. | — (via `go/validation`) |

Everything else — `go/internal/...`, and every `internal/` directory under a
harness package, which holds that harness's native types and its client — is
internal, and the set is enforced the same way.

## `go/sdk`: a client for a running endpoint

`go/client` drives the hub over HTTP + SSE. `go/sdk` drives an *endpoint* over
stdio, and by default it owns the process: `sdk.New(ctx, nil)` spawns
`oapx serve agent,provider --stdio`, and the four namespaces hang off the
client.

| namespace | what it is for |
| --- | --- |
| `Client.Auth` | lists auth providers and drives interactive login flows |
| `Client.Models` | lists and resolves models |
| `Client.Provider` | runs direct provider completions, buffered or streamed |
| `Client.Agent` | opens sessions, switches models, and runs the agent loop |

A minimal provider call:

```go
client, err := sdk.New(ctx, nil)
if err != nil {
	return err
}
defer client.Close()

model, err := client.Models.Resolve(ctx, sdk.ResolveModelRequest{
	ProviderID: "anthropic",
	API:        "anthropic-messages",
	ModelID:    "claude-sonnet-4-5",
})
if err != nil {
	return err
}

resp, err := client.Provider.Complete(ctx, sdk.CompletionRequest{
	ModelRef: model.ModelRef,
	Messages: []sdk.Message{sdk.UserMessage("Write a haiku about streams.")},
})
```

Four things a caller has to know, and the reason each is a rule rather than a
convention:

- **Cancellation is per call.** Every call that performs I/O takes a
  `context.Context` first. Cancelling it stops the call promptly, sends a
  best-effort cancellation frame, and releases that call's frame route. It does
  **not** tear down the client: `Client.Close` owns the child process lifetime.
- **`ModelRef` is server-issued and opaque.** Do not parse it, do not build one
  by hand, and do not derive provider or model identity from its text. Obtain it
  from `Models.List` or `Models.Resolve`.
- **Session IDs are opaque nonempty strings.** `Agent.SwitchModel` changes the
  session's default model for future runs, and `Agent.AttachProvider` sends the
  optional provider-attachment request — which an endpoint that does not support
  refuses explicitly. Client-executed agent tools and per-run sampling or token
  options are **not** in the current OAP endpoint and are refused with
  `unsupported_feature` rather than silently ignored.
- **Failures are typed and reachable through `errors.As`.** `StreamError` for
  provider, transport and abort failures on provider and agent calls,
  `AuthRequiredError` (which unwraps to `StreamError`) when a provider needs a
  login, `ProtocolError` for model-discovery protocol failures, and `AuthError`
  for auth listing and login-flow failures. Errors caused by a cancelled context
  wrap the context error, so `errors.Is` against `context.Canceled` and
  `context.DeadlineExceeded` works.

## `goap` is the worked example

`goap` is not a separate implementation to keep in step: it is this library,
with a command in front of it. The map is in the table above, and the two
places worth reading first are:

- `go/cmd/goap/harnesses.go` — every adapter, every tool source, the registry,
  and the pins, assembled into the descriptor the wire serves. Reading it
  answers "how do I wire my own adapter in?".
- `go/cmd/goap/serve.go` — the hub over all three transports, including the
  stdio op set and the HTTP body gate.

CI builds `goap` and runs it, so neither can drift from the library.

## Where to go next

- To **drive a harness**, start at `ExampleRunToTerminal`, then
  `go/adapter`'s own test files, which show each capability's refusals.
- To **serve the protocol**, read `go/serve`'s hub tests for the semantics
  (close, the snapshot, a subscription's ending) and then one binding.
- To **know what a harness can do**, read its ledger under `research/`, which
  records the wire at the pinned version — the catalog entry names which.
- To **check your work**, run `go run ./go/cmd/goap check`, `go run ./go/cmd/goap validate` and the adapter
  corpora; `go/adapter/adaptertest` is what they run under.

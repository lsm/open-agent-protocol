# The Go library

`go/` is a Go library first and a command second. This guide says what the
library is for, what each public package does, and which file in `goap` uses
it — so a reader can go from a question to the code, without doc comments,
which the repository's zero-comment rule forbids.

It is not a tutorial. The three runnable examples are the tutorial, and they
are compiled and run by `go test`:

| what it shows | where |
| --- | --- |
| a prompt to a result, with interactions answered by a policy function | `ExampleRunToTerminal` in `go/adapter/prompt_example_test.go` |
| driving `goap hub` over HTTP + SSE from a Go program | `Example` in `go/client/example_test.go` |
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
| `go/protocol` | The wire: envelope types, payloads, the status and code vocabularies, and the codecs. Every other package speaks in these types, and nothing else defines them. | `go/cmd/goap/conformance.go` |
| `go/validation` | The validator: schema bytes, semantic rules, and the fixture gate. `oapx validate` is a thin command over it. | `go/cmd/goap/conformance.go`, `main.go` |
| `go/adapter` | The adapter boundary, and `RunToTerminal` — the shortest path from a prompt to a response. | `go/cmd/goap/main.go` |
| `go/adapter/claude` | Claude Code, over the Agent SDK's stdio protocol. | `go/cmd/goap/harnesses.go` |
| `go/adapter/codex/appserver` | The Codex app-server, over its JSON-RPC stdio protocol. | `go/cmd/goap/harnesses.go` |
| `go/adapter/acp` | Any agent speaking the Agent Client Protocol. | `go/cmd/goap/harnesses.go` |
| `go/adapter/pi` | pi, over its own JSONL command protocol. | `go/cmd/goap/harnesses.go` |
| `go/adapter/hermes` | Hermes, over the `tui_gateway` JSON-RPC protocol. | `go/cmd/goap/harnesses.go` |
| `go/adapter/opencode` | OpenCode, over its HTTP API and event stream. | `go/cmd/goap/harnesses.go` |
| `go/adapter/deepseek` | The DeepSeek harness, over its SDK wire. | `go/cmd/goap/harnesses.go` |
| `go/adapter/adaptertest` | The conformance harness the adapter corpora run under. Test-support, and public so a port can run the same cases. | — |
| `go/harness` | Harness pins, tools and capabilities: the registry that loads `harnesses/*.json` and decides what a request may ask for. | `go/cmd/goap/harnesses.go` |
| `go/providercatalog` | Provider and model facts, and the rule that an unpublished entry is unknown. | `go/cmd/goap/providers.go` |
| `go/serve` | The hub: sessions, runs, subscriptions, the snapshot and the close semantics, over a transport-agnostic core. | `go/cmd/goap/serve.go` |
| `go/serve/servehttp` | The HTTP + SSE binding of the hub, including the body gate, the allowlists and the stream endings. | `go/cmd/goap/serve.go` |
| `go/serve/serveendpoint` | The binding that puts the hub inside another process's own HTTP server, with no listener of its own. | `go/cmd/goap/endpoint.go` |
| `go/serve/servestdio` | The stdio binding: twelve ops over a pipe, with its own op set rather than the endpoint's raw envelopes. | `go/cmd/goap/serve.go` |
| `go/client` | A Go client for the hub's HTTP + SSE wire. Also the far-side proof that the wire is implementable from outside this module. | — |
| `go/tools/goprintable` | The rule that decides what may be printed, used by the provider paths. | — |
| `go/tools/nocomment` | The zero-comment checker itself, so a program can run the same gate the repository runs. | — |
| `harnesses` | The embedded harness pin catalogue, so a consumer reads the same pins the repository does. | `go/cmd/goap/harnesses.go` |
| `providers` | The embedded provider catalogue. | `go/cmd/goap/providers.go` |
| `schema` | The embedded JSON Schema bytes for `v0.1`. | `go/cmd/goap/conformance.go` |

Everything else — `go/internal/...`, and every `internal/` directory under a
harness package, which holds that harness's native types and its client — is
internal, and the set is enforced the same way.

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
- To **check your work**, run `goap check`, `goap validate` and the adapter
  corpora; `go/adapter/adaptertest` is what they run under.

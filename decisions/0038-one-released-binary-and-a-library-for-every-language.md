# Decision 0038: One Released Binary, and a Library for Every Language

Status: proposed
Date: 2026-09-26
Protocol: `open-agent-protocol` version `0.1`
Profiles: neither; this record governs what ships and what each language gets,
not the wire
Amends: [Decision 0032](0032-go-and-zig-are-peers.md), whose Go tree stays
first-class and permanent as libraries while its command becomes an internal
tool, and [the CLI draft](../drafts/cli.md), whose second binary is not released
Follows: [Decision 0019](0019-one-binary.md)'s "One binary ships, and it is
`oapx`", which 0032 left standing

## Context

Decision 0019 decided that one binary ships, `oapx`, with the validator inside
it. Decision 0032 made the Go tree permanent and named its binary `goap`, and
`drafts/cli.md` then described two binaries sharing one verb set. Only `oapx` has
ever been released: `release-binaries.yml` builds it for every platform, and
`goap` exists only as `go run ./go/cmd/goap`. The two-binary framing sends a user
looking for a download that does not exist, and holds the Go tree to command-line
parity — flags, exit codes, configuration defaults — that serves no user.

Splitting the TUI out of `oapx` so that the two binaries match would leave three
things to ship and explain rather than two.

Go has two libraries for one protocol. `go/` holds the protocol, the validator,
the harness adapters and the hub. `sdk/go` is a thin client in its own module,
package `makai`, that starts `oapx serve agent,provider --stdio` and carries its
own copies of the wire types; its 31 files are the whole of the zero-comment
allowlist. The four SDKs — TypeScript, Python, Go and Rust — are all thin clients
of `oapx` today, whatever their users expect.

## Decisions

### `oapx` is the only released binary

It carries the TUI, its own agent loop, the providers and login, the harness
backends, and the tools another implementation needs to prove itself: `validate`
and `conformance`. `oapx conformance` does not exist yet, and `oapx validate`
lacks extension packs, tolerant mode and the presentation profile.
Both are now product work. That reverses 0019's view that `conformance/` "moves
only if someone wants it in the product": every other language wants it.

### `goap` is an internal tool

The Go command stays in the repository because CI runs it: `goap check` guards
the catalog, ledgers and fixtures, `goap conformance` is the conformance runner
until `oapx` has one, the parity tests drive `goap serve agent`, and
`clients/ts`'s tests run `goap hub`. It is built from source, never released, and
not documented as something a user installs. Its verbs keep the meanings
`drafts/cli.md` gives them, and nothing binds its command line to match `oapx`'s.

### Parity is protocol behaviour, not the command line

Between the trees, parity is what crosses the wire: traces, the bytes a backend
writes to its child, the answers an endpoint gives. Verbs, flags, exit codes,
output formats and configuration defaults are `oapx`'s alone, and a difference
there is not a divergence either tree records.

### Each language gets a library, in one of two shapes

The shape follows what the language's users ship.

| Language | Shape | Where the work runs |
| --- | --- | --- |
| TypeScript, Python | thin | `oapx`, which the SDK starts and speaks OAP to |
| Go | native | natively; `oapx` for the agent loop and providers until Go has its own |
| Rust | native, grown from `sdk/rust` | `oapx` until each piece is ported |
| Java | native, when someone needs it | the same path |

TypeScript and Python users are used to a compiled engine inside a package, and a
thin SDK gives them one implementation of the hard parts — providers, login,
harness adapters, tools — rather than one per language. Talking to it over stdio
costs little beside model latency. Go, Rust and Java users expect one
self-contained program: easy cross-compiling, no child process to manage, nothing
extra for a security review. For them a spawned binary is an obstacle.

A native library covers at least the protocol types, the validator, a client, an
agent loop and providers. What it does not do natively yet it delegates to `oapx`
through the same API, or reports `unavailable`; it never approximates, as 0032
already requires. The TUI stays `oapx`'s.

### A library proves itself against the shared kit, not against Zig

The kit is the schemas, `fixtures/manifest.json`, the adapter corpora and the
conformance runner. A native library runs the fixtures through its validator,
replays the corpora through any adapter it carries, and passes the runner as an
endpoint. None of it depends on Go or Zig source, and the runner ships in `oapx`,
so implementing OAP in a new language never needs another language's toolchain.

### Go goes first

`sdk/go` merges into the main Go module as a package built on the shared
`protocol` types. It keeps its public API — `Agent`, `Provider`, `Auth`, `Models`
— and delegates the agent loop and providers to `oapx` until Go has native ones.
The `sdk/go` module is deprecated with a pointer to its replacement, and the
zero-comment allowlist goes with it. A Go agent loop and provider runtime are
what remain before Go is native in the sense above. Rust follows from `sdk/rust`;
Java waits for a user.

### The hub stays a Go library

The hub is `go/serve` and its bindings, reached today through `goap hub`. With no
Go binary released, a program that needs a multi-session daemon embeds
`go/serve`, and `clients/ts`'s tests build `goap hub` from source.

## Consequences

- `release-binaries.yml` does not change; it already builds only `oapx`.
- `drafts/cli.md` keeps its verb meanings. Its `goap` column describes a
  repository tool, and a rule it states for both binaries binds `oapx` alone.
- When this record is accepted, 0032's status line records the amendment: its Go
  binary is an internal tool, and its libraries stay first-class and permanent.
- The work this record creates, in order: `oapx conformance` and a complete
  `oapx validate`; the `sdk/go` merge; a Go agent loop and provider runtime; Rust
  from `sdk/rust`.
- The README's `goap` sections become instructions for repository tooling, and
  install instructions name `oapx` alone.

## What this decision does not admit

- A released `goap`, or any second released binary.
- Splitting the TUI out of `oapx`.
- A native library approximating what it has not implemented.
- A conformance kit that needs Go or Zig installed to run.

## Open questions

- Whether the hub becomes a product feature of `oapx`.

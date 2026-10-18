# Decision 0052: Rust Is the Third Peer, and Ports All of Go

Status: proposed (the owner approved it on 2026-10-18; it becomes accepted once
the `rust/` workspace and its comment check land on main)
Date: 2026-10-18
Protocol: `open-agent-protocol` version `0.1`
Profiles: neither; this record governs the implementations, not the wire
Amends: [Decision 0032](0032-go-and-zig-are-peers.md), whose two peers become
three; [Decision 0038](0038-one-released-binary-and-a-library-for-every-language.md),
whose Rust row says "native, grown from `sdk/rust`" without saying how far
Follows: [Decision 0038](0038-one-released-binary-and-a-library-for-every-language.md)'s
"Rust follows from `sdk/rust`"

## Context

0038 gave Rust a native library and left its scope to its own epic. Today
`sdk/rust` is a thin client of about 5,000 lines: it starts
`oapx serve agent,provider --stdio` and speaks OAP to it, which is what `go/sdk`
alone does in the Go tree. Go also carries the protocol types, the validator,
seven harness adapters, the hub and its HTTP and stdio bindings, the binding
store, the client, the conformance runner, and an agent loop and provider
runtime, about 65,000 production lines in all. A Rust user who wants to drive a
harness in their own process, or serve a hub, has nothing to call.

## Decisions

### Rust implements everything Go does

The Rust tree carries every public Go package and the internal agent loop and
providers behind them. Parity with Go is the parity 0038 defines: what crosses
the wire, compared as parsed JSON. A Rust API is idiomatic Rust; it does not
mirror Go's names or signatures.

Until a piece is ported, the Rust library delegates it to `oapx` through the
same API, as `sdk/rust` does now, or answers `unavailable`. It never
approximates.

### Rust is a peer, and the specification decides

0032's rules extend to three trees. No tree is the oracle; the arbiter is the
one 0032 lists. Differential tests that run Go against Zig take Rust as a third
side once the piece they cover is ported, and a disagreement is a finding
against whichever side the specification says is wrong. A divergence that
cannot be fixed yet is recorded in the harness's ledger, as the Go and Zig ones
are. `drafts/hub.md` names the Rust test pinning each rule beside the Go one.

A Go runtime quirk the corpora quote is reproduced in Rust exactly as Zig
reproduces it, until 0032's normalization removes it from the corpus.

### The tree is a `rust/` workspace

`rust/` sits beside `go/` and `zig/` and is one Cargo workspace:

| Go | Rust crate |
| --- | --- |
| `protocol`, `schema` | `oap-protocol` |
| `validation` | `oap-validation` |
| `harness` | `oap-harness` |
| `adapter`, `adapter/adaptertest` | `oap-adapter` (`adaptertest` behind a `testkit` feature) |
| `adapter/<harness>` | `oap-adapter-<harness>` |
| `binding` | `oap-binding` |
| `client` | `oap-client` |
| `serve`, `serve/{servehttp,servestdio,serveendpoint}` | `oap-serve` |
| `providercatalog` | `oap-provider-catalog` |
| `internal/agent`, `internal/provider` | `oap-agent`, `oap-provider` |
| `internal/conformance` | `oap-conformance` |
| `sdk` | `oap-sdk` |

An `oap` crate re-exports them. `sdk/rust` moves to `rust/sdk` and keeps its
public API. The crates read the schema, the harness catalog, the provider
catalog and the fixtures from the repository by path, so the workspace is
consumed by path or by git; publishing to crates.io is a later decision.

Neither `goap check` nor the generated Zig modules spell a pin, and Rust does
not either: a harness pin, a base URL or an OAuth origin is read from the
catalog at build time, never written as a literal.

### No comments in `.rs` files

The zero-comment policy covers every tracked `.rs` file, tests included.
`//`, `/* */`, `///` and `//!` are all comments; API documentation goes in
`docs/`. Attributes are not comments, so `#![doc = include_str!(...)]` stays
available for a crate's front page.

The check is `rust/tools/nocomment`, with `--check` and `--write` like
`go/tools/nocomment`. It finds comments with `ra-ap-rustc_lexer`, the lexer
`rustc` itself uses, so a `//` inside a string, a raw string or a char literal
is never a comment, and a lifetime is never mistaken for a char. It has no
allowlist: `sdk/rust`'s comments are removed when it moves.

### No Rust binary ships

0038 stands: `oapx` is the only released binary. A Rust executable in the
workspace is a test or repository tool, as `oap-protocol-fake` and `goap` are.

### Order

Each phase builds, passes and makes sense on its own, in small pull requests:

1. The workspace, the comment check, the move of `sdk/rust`, and CI.
2. `oap-protocol` and `oap-validation`, passing `fixtures/manifest.json`.
3. `oap-harness`, `oap-binding` and `oap-client`.
4. `oap-serve`: the hub, then its stdio and HTTP bindings, held to
   `drafts/hub.md`.
5. The adapters, one per pull request series, each replaying its corpus.
6. `oap-agent` and `oap-provider`, then `oap-sdk` switched from `oapx` to them
   piece by piece, and `oap-conformance`.

## Consequences

- 0032's and 0038's status lines record this amendment, and `CLAUDE.md` says
  three peers and describes `rust/` in the same change that adds it.
- The README gate table gains a Rust column as adapters land.
- CI's Rust job moves from `sdk/rust` to the workspace and runs the comment
  check, `cargo fmt --check`, `clippy` and the tests.

## What this decision does not admit

- A released Rust binary.
- A Rust API that copies Go's shape where Rust has its own idiom.
- An allowlist for comments in `.rs` files.
- A native piece that approximates what it has not implemented.

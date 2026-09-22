# CLAUDE.md

Open Agent Protocol (OAP): a CC0 draft protocol for the boundary between a
control layer and an agent loop, a Go core that proves it, and a Zig runtime
(`oapx`) the same adapters are being ported to. One invariant holds everywhere:
an adapter over a third-party harness must emit traces the shared validator
accepts. The Go tree is the oracle — when Zig and Go disagree, probe Go and
match it, or record the divergence.

Authority: `decisions/0001-*.md` and `0002-*.md` freeze semantics, amended only
by later decisions; `drafts/` holds the prose profiles, and
`drafts/conformance.md` defines profiles and `+unit` claims;
`research/protocol-feedback-2026-09.md` adjudicates every mismatch found
adapting eight harnesses; `research/<harness>-<pin>-mapping.md` is the spec for
one adapter. Read the decisions before changing protocol behavior. A `research/`
ledger records an upstream project's vocabulary — never rename its identifiers
to ours.

## Commands

Go 1.26. CI runs exactly these, in order, failing on any `gofmt -l` output:

```sh
test -z "$(gofmt -l .)"
go run ./go/tools/nocomment --check
go vet ./... && go test ./... && go test -race ./...
go run ./go/cmd/oap check
```

About 10 seconds. After changing the stdio binding, `serve/serveendpoint`, or
the memory adapter's script, also run
`go run ./go/cmd/oap conformance --command "go run ./go/cmd/oap endpoint"`; a
check it reports `skipped` is an obligation the endpoint does not carry, not one
it failed.

Zig 0.16.0, with `build.zig` in `zig/`. A root `Makefile` wraps the everyday
ones (`make build|tui|test|test-tui|check|clean|clean-all`) and is the only
place macOS codesigning is configured.

```sh
zig build --build-file zig/build.zig             # -> zig/zig-out/bin/oapx
zig build --build-file zig/build.zig test
zig build --build-file zig/build.zig test-unit-<group>
```

There is no per-test filter; the smallest runnable unit is a group step, and
`zig build --build-file zig/build.zig --help` lists them.

TypeScript: `npm ci && npm test` at the root (the SDK) and in `clients/ts` (the
daemon client). The latter builds `./go/cmd/oap`; set `OAP_GO` if `go` is not on
`PATH`, or `OAP_TS_SKIP_INTEGRATION=1` to skip it.

`OAP_SDK_BINARY_PATH` is what gates the SDK's real-binary coverage: every test
in `sdk/typescript/test/makai_binary_smoke.test.ts` skips when it is unset, so
`npm test` passes green with zero end-to-end binary coverage. A binary found
through `zig-out` or the `@oap-sdk/cli-*` platform package does not switch those
tests on, and that package outranks both local build paths, so a fresh
`zig build` alone is not what the SDK exercises. Export the variable when you
mean to.

Guardrails, which CI runs before unit tests:
`./scripts/check-zig-patterns.sh` and `node scripts/check-no-comments.mjs
--check`.

## Zero comments

Every tracked `.go`, `.zig` and `.ts` file carries no comments — `build.zig`,
tests, fixtures and `zig/vendor` included. Rationale goes in commit messages, PR
descriptions, `decisions/` and `drafts/`.

Two enforcers, two ratchets. `scripts/check-no-comments.mjs` covers `.zig` and
`.ts`, and its allowlist is retired, so the floor there is zero.
`go/tools/nocomment` covers `.go`, and its `allowlist.txt` still carries the 31
files of `sdk/go`, which arrived commented. That list only shrinks: an entry
goes when its comments go, and the check fails if a listed file disappears.
Never add a comment to a file that is not on it.

Exempt are the directives the toolchain honors and the few comments it makes
unremovable where they are load-bearing — an `Example`'s trailing `// Output:`,
a `Code generated` header, a canonical import comment, a cgo preamble. Position
is part of the test: prose that merely opens with "Output:" is counted like any
other sentence. `--write` keeps or removes a group whole, so prose sharing a
group with a directive survives a write while `--check` still counts it; that
file wants a hand edit.

**Do not encode a capability claim in a name.** A name is the only documentation
a reader gets, and a name contradicting its body is the one defect this policy
makes invisible: nothing goes stale, nothing fails to compile.
`handleSyncUnsupported` rots; `handleSync` cannot. Sharper for test names — a
test named for a policy its body never exercises claims something was checked
when it wasn't.

## Architecture — Go

A strict stack; lower layers never import higher ones.

| Package | Role |
| --- | --- |
| `protocol` | Envelope, typed ID domains, payload structs, `Type*` constants |
| `schema` | Embeds `schema/v0.1/*.json` (JSON Schema 2020-12) |
| `validation` | Decode (duplicate keys), schema, semantic state machine (`state.go`), typed diagnostic codes |
| `adapter` | `Adapter`/`Session`/`EventStream`, error sentinels, `ValidateInputAnswer`, and `Memory` |
| `adapter/adaptertest` | `Next`/`Drain`, `AssertProtocolValid*` |
| `adapter/{acp,claude,codex/appserver,deepseek,hermes,opencode,pi}` | One per pinned upstream harness |
| `serve` | Registry + multi-session hub, bounded fan-out, cursor replay; adds no semantics, never validates |
| `serve/{servehttp,servestdio}` | HTTP+SSE and newline-JSON over one hub; `parity_test.go` enforces the mirror |
| `client`, `clients/ts` | Far-side conformance proofs, invisible SSE resume |
| `cmd/oap` | Dispatcher; `serve.go` wires signals, loopback allowlist, bounded shutdown |

`endpoint` and `conformance` are the endpoint-role pair, a different layer from
`serve --stdio`. `serve --stdio` exposes the **hub** — twelve ops, an adapter
dimension, cursor replay, multiplexed subscriptions — each line wrapping an
envelope in a transport object. `endpoint` exposes **one agent loop** carrying
raw OAP envelopes, one per line, per `drafts/endpoint-stdio.md`.

## Architecture — Zig

`zig/src/`: hosts (`tools/makai.zig`, which builds the `oapx` binary, and
`tui/`) over the agent layer (`agent/`, `tools/`) over
`protocol/{auth,provider,agent,tool,oap}/` over `transport.zig` +
`transports/` over the streaming core (`ai_types`, `event_stream`,
`api_registry`, `stream`, `model_catalog`) over `providers/` and `compat/`
(wrappers over Zig 0.16 `std.Io`). `zig/src/adapter/` is the adapter port, run
against the same corpora as Go.

`oapx serve agent` serves `agent-control-core` and `oapx serve provider` serves
`model-provider-core`. Each refuses the other's profile at decode and names the
one it serves. They share the base envelope and vocabulary but not
`ProtocolError` — the code sets are disjoint. A role is a noun, so `serve` takes
an argument rather than a flag (decision 0019). State lives under `~/.oapx`; on
macOS credentials are Keychain-first under `ai.hyperneo.oap`, falling back to
`~/.oapx/auth.json`, so clearing that file alone leaves live credentials behind.

Rules the source will not tell you:

- `build.zig` declares a module per **root** with explicit `.imports`, a test per
  module, then wires that test into `test` **and** a `test-unit-*` group.
  Everything in `test` must also be in a group the CI matrix invokes, and vice
  versa; `test-unit-agent` is local-only, so a test wired only there runs in no
  CI job. A module with no `addTest` is invisible to this rule.
- A relative `@import` does not mean a file has no module. Decide from
  `build.zig` and the importers, never from the import syntax.
- `EventStream.owns_events` is an **ownership** flag, not a cloning switch:
  `push` deep-copies only when `clone_event_fn` is also set, and setting
  `owns_events` without cloning before push is a use-after-free. A stream ends
  via `complete`/`completeWithError`, never a `.done` event. Full contract:
  `docs/zig-stream-memory-ownership.md`.
- A struct literal must not allocate more than once: a later failing `dupe`
  leaks every field already built, and an `errdefer` cannot live inside a
  literal. Build fields into locals first. `check-zig-patterns.sh` catches this
  one shape and is a floor, not a detector — it does not see an `errdefer` that
  frees a container without its contents, a built value dropped in a hand-off
  like `try list.append(a, try build(a))`, or an `errdefer` left armed after
  ownership transfers. What finds those is
  `std.testing.checkAllAllocationFailures` over the allocating function, which
  any function that allocates and then hands off ownership should have.
- `deinit()` on critical types ends `self.* = undefined;`.
  `oom.unreachableOnOom` replaces `catch unreachable`, and `OwnedSlice(T)`
  replaces ownership flags.
- `compat.random` is the only source of security entropy. Every ordinary-entropy
  call under `zig/src` must be declared, with its rationale in the commit
  message, in `check-zig-patterns.sh`'s `expected_ordinary_entropy_sites`, and a
  declared site that disappears fails the check too.
- Artifact-store tests must open `common.TestArtifactRoot`; reaching the store
  without one panics in test builds rather than using the real cwd.
- Public constructors must not take `std.Io`
  (`docs/zig-0.16.0-io-architecture-decision.md`).

## Core semantic invariants

What the validator enforces and every reducer must produce. Violating one is a
protocol bug, not a style issue.

- Exactly one nonterminal run per session; `auto` delivery resolves to `start`
  or `queue` (Decision 0002). Admission is `started`+`start`+`running` or
  `queued`+`queue`+`queued`; nothing else.
- Every run-scoped event carries a positive, contiguous per-run `sequence`.
  Requests and responses do not consume it.
- Exactly one terminal per run. A run may settle `failed` or `cancelled` before
  `run.started`, never `completed`.
- `run.cancel.response` is intent, not settlement; `run.cancelled` needs an
  accepted cancel exchange as evidence.
- Resume, reconciliation and replay are distinct. An expired cursor returns
  `*adapter.ReplayGap`, never fake continuity.
- `capability_revision` is schema-required only where the whole content binds to
  one descriptor: `capabilities.response`, `capabilities.updated`,
  `models.response`, `action.tools.list.response`. The validator adds: a request
  supplying a revision must cite the current one (`protocol.initialize.request`
  and `capabilities.request` are exempt so discovery is never blocked), its
  successful response must repeat it, and any envelope exercising an optional
  feature must cite the active revision. Capabilities report effective fidelity
  (`native`/`emulated`/`degraded`/`unavailable`), never an idealized harness.
- Only the declared responder resolves an interaction, once, and answers go
  through `adapter.ValidateInputAnswer` before any native write.
- `action.tools.provide` is a third interaction kind, resolved through
  `adapter.CallResolver`: its answer is a response carrying one of five ranked
  refusal reasons. The endpoint reports the highest reason a request satisfies;
  an empty set obliges acceptance. An adapter without the unit calls
  `adapter.RefuseUnadvertisedTools` at open.
- The agent participant an adapter names as `requested_by` must be the endpoint
  id its `protocol.initialize.response` declares.

## Adapters and fixtures

Each adapter is pinned to one upstream commit or tag and is the executable form
of its `research/` ledger, which records provenance hashes, the wire boundary,
and every impedance mismatch with its classification. Mismatches are recorded,
never silently compensated.

The layout repeats across `adapter/<harness>/`, so copy a neighbour. What the
files do not say: `session.go` is a reducer the adapter *owns*, not a rename
table over native events; `internal/rpc/` keeps one reader dispatching and one
serialized writer sending, fail-closed with bounded lines; `internal/native/`
holds pinned upstream types and nothing else. Adding an adapter also means a
`case` in `serve/registry.go`, an entry in `examples/oap-serve.json`, and a
README gate-table row.

Corpus coverage is **not** uniform: what each manifest pins, what counts as a
blank expectation, and how much of the codec is exercised differ per adapter.
The README gate table and each `corpus_test.go` are the record.

Real-process gates are skipped unless `OAP_<HARNESS>_SMOKE=1` or
`OAP_<HARNESS>_INTEGRATION=1` is set with an absolute `OAP_<HARNESS>_BIN`. They
never run in CI, never download anything, and never pass ambient credentials to
a child. Credential presence alone must never enable network traffic.

`fixtures/manifest.json` is normative: positive traces plus `schema-invalid` and
`semantic-invalid` ones carrying the exact diagnostic codes expected. A
`"profile": "model-provider-core"` entry runs through
`validation.ProviderValidator` against `fixtures/provider/`; that corpus is
deliberately invalid-first, because every provider defect found so far was a
well-formed frame a positive trace would have passed. Each provider entry
declares a `"scope"` — `frame` when decidable from one envelope, `trace` when
the violation spans several — and the loader derives it from the codes and
refuses a mismatch. Add a fixture whenever validator behavior changes.
`examples/` is illustrative only; some files use staging vocabulary
deliberately absent from executable v0.1.

## Daemon trust model

`oap serve` is a single-user local service: loopback bind by default, no auth,
`Host` allowlisted to loopback names on a loopback bind, and a restart kills
every session. The registry config's `environment` list is an explicit
allowlist — a child never inherits ambient variables that were not listed. Keep
these when touching `serve/` or `cmd/oap/serve.go`.

## Conventions

- Commit subjects are `<area>: <imperative sentence>`, area being a package or
  adapter (`serve:`, `codex:`, `adapter:`, `zig:`, `fix:`).
- Changing `schema/v0.1/*.json` requires matching edits to `protocol/`, the
  validator, a fixture, and `clients/ts/src/protocol.ts`.
- The hub, codecs and clients never log envelope payloads or resolved
  environment values.
- Foreign-harness identifiers stay in namespaced `extensions`; they never become
  OAP identities.
- If behavior changes, update the spec or draft in the same PR.

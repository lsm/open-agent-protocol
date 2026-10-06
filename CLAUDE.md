# CLAUDE.md

Open Agent Protocol (OAP): a CC0 draft protocol for the boundary between a
control layer and an agent loop, with two peer implementations: the Zig runtime
`oapx`, which is the product, and a Go tree that serves Go users natively. The
Go tree's command is `goap`, and it is **internal to this repository**: it is
run with `go run ./go/cmd/goap`, it is never installed, and it is not a second
product (Decision 0038: one released binary, and a library for every
language). One invariant holds everywhere:
an adapter over a third-party harness must emit traces the shared validator
accepts. Neither tree is the oracle (Decision 0032): when Zig and Go disagree,
the decisions, drafts, schema, fixtures and corpora decide, the wrong side is
fixed or the divergence recorded in its ledger, and a Go runtime quirk is not
protocol behaviour until a decision says so.

Authority: `decisions/0001-*.md` and `0002-*.md` freeze semantics, amended only
by later decisions; `drafts/` holds the prose profiles, and
`drafts/conformance.md` defines profiles and `+unit` claims;
`research/protocol-feedback-2026-09.md` adjudicates every mismatch found
adapting eight harnesses; `research/<harness>-<pin>-mapping.md` is the spec for
one adapter. Read the decisions before changing protocol behavior.

The Zig runtime and the SDK wire have their own normative pair, which the
protocol records do not cover and which is easy to miss now that they live in
one file: `DESIGN.md` is authoritative for architecture, protocol boundaries,
ownership, sequencing and transport posture, and
`docs/v1-sdk-agent-provider-spec.md` is the normative SDK and protocol spec.
Read those before changing SDK or Zig protocol behavior.

A `research/` ledger and a `fixtures/adapters/` corpus both record an upstream
project's vocabulary rather than ours, so never rename an identifier inside
either. A sweep that renamed a native member in a corpus would rename it in the
expectation beside it, leaving every test green while the corpus quietly stopped
reproducing the wire it is pinned to.

## Commands

Go 1.26 or newer (the `go.mod` floor; CI uses 1.27.x). CI runs exactly these, in
order, failing on any `gofmt -l` output:

```sh
test -z "$(gofmt -l .)"
go run ./go/tools/nocomment --check
go vet ./... && go test ./... && go test -race ./...
go run ./go/cmd/goap check
```

About 10 seconds. After changing the stdio binding, `serve/serveendpoint`, or
the memory adapter's script, also run
`go run ./go/cmd/goap conformance --command "go run ./go/cmd/goap serve agent"`; a
check it reports `skipped` is an obligation the endpoint does not carry, not one
it failed.

Zig 0.16.0, with `build.zig` in `zig/`. A root `Makefile` wraps the everyday
ones (`make build|install|tui|test|test-tui|check|clean|clean-all`) and configures local
macOS codesigning. `make build` and `make tui` build ReleaseSafe; pass
`OPTIMIZE=Debug` for a debug build, but not to use the TUI, because Zig's debug
allocator records a stack trace for every allocation and a long session then
freezes. Release signing and notarization are configured separately,
in `.github/workflows/release-binaries.yml`, so a change to one is not a change
to the other.

```sh
zig build --build-file zig/build.zig             # -> zig/zig-out/bin/oapx
zig build --build-file zig/build.zig test
zig build --build-file zig/build.zig test-unit-<group>
```

There is no per-test filter; the smallest runnable unit is a group step, and
`zig build --build-file zig/build.zig --help` lists them.

TypeScript: `npm ci && npm test` at the root (the SDK) and in `clients/ts` (the
daemon client). The latter builds `./go/cmd/goap`; set `OAP_GO` if `go` is not on
`PATH`, or `OAP_TS_SKIP_INTEGRATION=1` to skip it.

`OAP_SDK_BINARY_PATH` is what gates the SDKs' real-binary coverage. Every test
in `sdk/typescript/test/makai_binary_smoke.test.ts` and in
`go/sdk/binary_smoke_test.go` skips when it is unset, so both SDKs pass green
with zero end-to-end binary coverage. A binary found through `zig-out` or the
`@oap-sdk/cli-*` platform package does not switch the TypeScript tests on, and
that package outranks both local build paths, so a fresh `zig build` alone is
not what either SDK exercises. Export the variable when you mean to. CI's
`go-sdk-smoke` job does exactly that: it builds `oapx` and runs
`go test -race ./go/sdk/...` with the variable set, which is the only place the
Go SDK talks to a real runtime.

Guardrails, which CI runs before unit tests:
`./scripts/check-zig-patterns.sh` and `node scripts/check-no-comments.mjs
--check`. After the Zig, SDK and TUI tests it runs
`./scripts/check-no-test-litter.sh`, which fails if a test left a credential
store (an `auth.json` under `.oapx` or `.makai`) inside the checkout; both stay
in `.gitignore`, so nothing else would notice one. A workspace's own `.oapx`
(tool artifacts, permissions) is expected state, not litter.

Three workflows run on `pull_request`: `ci.yml`, `ci-zig.yml` and
`benchmark-report.yml`. Each takes one concurrency group per pull request, so a
newer push to that pull request cancels the run the previous push started, and
that is all the group ever cancels. Both CI files push on `main` only, so a branch
push produces one run rather than a `push` twin of the `pull_request` one, and a
push takes no group it shares: each push's run survives on its own — the key
includes the event name and a run id, because GitHub drops the *pending* run of a
shared group by default, so a group keyed on `github.ref` would have cancelled
main's queued runs rather than none of them.

## Zero comments

Every tracked `.go`, `.zig` and `.ts` file carries no comments — `build.zig`,
tests, fixtures and `zig/vendor` included. Rationale goes in commit messages, PR
descriptions, `decisions/` and `drafts/`.

Two enforcers, and both floors are zero. `scripts/check-no-comments.mjs` covers
`.zig` and `.ts`; `go/tools/nocomment` covers `.go`. Neither carries an
allowlist any more: the Go one existed only for `sdk/go`, which arrived
commented, and the fold into `go/sdk` took the last 31 entries with it. A
comment in a Go file fails the check, with no exemption to add one to. When a
file needs explaining, the explanation belongs in `docs/`, which is prose the
policy does not reach.

Exempt in both are the directives the toolchain honors and the few comments it
makes unremovable where they are load-bearing — an `Example`'s trailing `// Output:`,
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
| `schema` (repo root, `schema/embed.go`) | Embeds `schema/v0.1/*.json` (JSON Schema 2020-12) |
| `harnesses` (repo root), `harness` | Embeds the harness catalog; `harness` loads it strictly and runs `goap check`'s drift rules |
| `validation` | Decode (duplicate keys), schema, semantic state machine (`state.go`), typed diagnostic codes |
| `adapter` | `Adapter`/`Session`/`EventStream`, error sentinels, `ValidateInputAnswer`, and `Memory` |
| `adapter/adaptertest` | `Next`/`Drain`, `AssertProtocolValid*` |
| `adapter/{acp,claude,codex/appserver,deepseek,hermes,opencode,pi}` | One per pinned upstream harness |
| `serve` | Registry + multi-session hub, bounded fan-out, cursor replay; adds no semantics, never validates |
| `serve/{servehttp,servestdio}` | HTTP+SSE and newline-JSON over one hub; `parity_test.go` enforces the mirror |
| `cmd/goap` parity fixtures | one per harness plus `memory`; what the job is for, and what the corpora cover instead, is [`docs/parity-job.md`](docs/parity-job.md) |
| `serve/serveendpoint`, `conformance` | One agent loop over raw envelopes, and the runner that checks it |
| `client`, `clients/ts` | Far-side conformance proofs, invisible SSE resume |
| `sdk` | The Go client for a running endpoint: it spawns `oapx serve agent,provider --stdio` and exposes `Auth`, `Models`, `Provider`, `Agent` over profiled envelopes. Its own private `frame` is still the one place Go hand-rolls an envelope; replacing it with `protocol.Envelope` is the follow-up |
| `provider`, `internal/providertest` | Provider wire evidence (Z.AI); no Go `model-provider-core` runtime yet |
| `cmd/goap` | Dispatcher; `serve.go` wires signals, loopback allowlist, bounded shutdown |

`serve agent` (alias `endpoint`) and `conformance` are the endpoint-role pair, a
different layer from `hub --stdio`. `hub --stdio` exposes the **hub** — thirteen ops, an adapter
dimension, cursor replay, multiplexed subscriptions — each line wrapping an
envelope in a transport object. `serve agent` exposes **one agent loop** carrying
raw OAP envelopes, one per line, per `drafts/endpoint-stdio.md`.

## Architecture — Zig

`zig/src/`: hosts (`tools/makai.zig`, which builds the `oapx` binary, and
`tui/`) over the agent layer (`agent/`, `tools/`) over
`protocol/{auth,provider,agent,tool,oap}/` over `transport.zig` +
`transports/` over the streaming core (`ai_types`, `event_stream`,
`api_registry`, `stream`, `model_catalog`) over `providers/` and `compat/`
(wrappers over Zig 0.16 `std.Io`). `zig/src/adapter/` is the adapter port, run
against the same corpora as Go.

`oapx serve agent` serves `agent-control-core` over stdio and `oapx serve
provider` serves `model-provider-core` over stdio or loopback HTTP+SSE
(`--http`, decision 0030). Each refuses the other's profile at decode and names the
one it serves. They share the base envelope and vocabulary but not
`ProtocolError` — the code sets are disjoint. A role is a noun, so `serve` takes
an argument rather than a flag (decision 0019). State lives under `~/.oapx`; on
macOS credentials are Keychain-first under `ai.hyperneo.oap`, falling back to
`~/.oapx/auth.json`, so clearing that file alone leaves live credentials behind.

**Never move credentials to a plain file.** The Keychain is awkward on macOS —
reads fail fast and fall back, writes still prompt, and an access list binds to
the code hash so every unsigned rebuild prompts again. None of that is a reason
to relocate them; it is a reason to sign the build. And because a write prompts,
a non-interactive shell never surfaces the prompt and the write **blocks**
rather than failing: bound any invocation that may persist credentials with an
external timeout, so a hang is visible instead of silent.

`OAPX_KEYCHAIN_SERVICE` redirects the whole store — every read and write, not
one item — so a local run can use a throwaway service. Use a genuinely unique
name per run (`makai-test-$(uuidgen)`; `$(date +%s)-$$` collides between
subshells). It is not isolation on its own, and the four gaps are why:

1. It does not cover `~/.oapx/auth.json`. With no item under the overridden
   service, `loadDefault` falls back to the file, so a supposedly isolated run
   can still consume real tokens. Redirect `HOME` too.
2. It does not cover the Codex CLI import, which reads the fixed `Codex Auth`
   service on `loadDefault` paths. `loadDefaultStoredOnly` passes
   `import_codex = false` and is not affected.
3. A unique service accumulates items. Anything persisting a credential writes
   one — including an ordinary request that refreshes an expired token — and
   nothing deletes them. Clean up on every exit path with
   `security delete-generic-password -s "$OAPX_KEYCHAIN_SERVICE"`.
4. The real-binary SDK tests cannot pass on macOS as written: `loadDefault`
   attaches the Keychain save callback when the service has no item, so login
   writes miss the temporary `HOME` and the assertions fail on `ENOENT`.
   `shouldUseKeychain()` is hardcoded to macOS non-test builds, so there is no
   switch. Run those on Linux.

Rules the source will not tell you:

- `build.zig` declares a module per **root** with explicit `.imports`, a test per
  module, then wires that test into `test` **and** a `test-unit-*` group.
  Everything in `test` must also be in a group the CI matrix invokes, and vice
  versa; `test-unit-agent` is local-only, so a test wired only there runs in no
  CI job. A module with no `addTest` is invisible to this rule.
- A relative `@import` does not mean a file has no module. Decide from
  `build.zig` and the importers, never from the import syntax.
- **A provider's base URL and OAuth origin live in `providers/catalog.json` and
  nowhere else.** `build.zig` turns that file into typed rows
  (`providerCatalogDataModule`), so a Zig call site reads `provider_catalog` and
  a value the catalog does not hold is a `compileError`; `goap check` fails a
  non-test Go or Zig literal that spells a catalogued base URL, and a test may
  still spell one because that is the assertion. A *credential or base-URL
  environment variable name* is read through the catalog but is not gated, so
  keep the read in the catalog and let `goap check` grow the rule when a second
  one appears. A provider id and a wire id are **not** catalog-only: code
  compares them as literals, and the catalog's id list is what `custom_providers`
  reserves. Hoist a lookup to a file-scope const — that is where a
  `compileError` or a `[0]` index fires; the same call inside a function body is
  not a build gate, which is why an env name is read as `baseUrlEnv(id)[0]` and
  not through a helper that errors.
- **`EventStream.ownership` is one setting, and its two values are the whole
  contract.** `.borrowed` (the default) stores what `push` is given and never
  frees it, so the producer must keep the backing storage alive until the
  consumer has drained the queue. `.{ .owned = clone_fn }` deep-copies on
  `push` and frees what it copied, so the stream can never free the producer's
  memory — the clone function travels *inside* the value, so there is no
  "owns but does not clone" setting to express. A producer that used to clone
  by hand before pushing (`pushOwnedEvent`) pushes directly now, and **frees
  its own event once the push returns**, because the stream holds a copy. A
  stream ends via `complete`/`completeWithError`, never a `.done` event. Full
  contract: `docs/zig-stream-memory-ownership.md`.
- **`ProtocolClient` is a separate contract, and the stream rules do not cover
  it.** Its terminal query hands back a deep copy the caller owns:
  `waitResult`/`waitResultFor` return an `ai_types.OwnedMessage`, so the result
  is still there after `removeStreamState` or `reset` frees the client's own
  copy. There is no borrowed spelling to reach for, so nobody has to remember
  to clone before cleanup. `deinit` it, or `intoMessage` it to hand the
  message on — a `?OwnedMessage` with no `defer` is a leak the allocator
  reports, not a silent one. `EventStream.cloneResult` is a different API and
  returns a bare message, not an `OwnedMessage`.
- **A run ends with exactly one event that ends it, and that is a type-level
  fact rather than a convention.** `AgentEvent.isTerminal` names them —
  `agent_end` and `run_failed` — and `runLoopThread` emits exactly one of the
  two from its `defer`, so every path out of the run ends it: the paths that
  return before the run starts, a failure inside it, and a failure *after* it
  has already ended, which must not end it twice. A failure that is only
  readable from a second channel is a failure a consumer that has not
  remembered to check the second channel for will wait on forever. Note the
  thing this does not change: a provider that refuses is a run that got far
  enough to end, so it ends with a normal `agent_end` and
  `final_message.stop_reason == .@"error"` is still where that shows. Treat
  `AgentEndPayload.termination` as evidence of nothing: it encodes only
  `max_turns` or `cancelled`, and is null both on a clean finish and on that
  provider failure.
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
- An allocation-failure sweep runs its probe once per allocation, and the
  testing allocator unwinds a DWARF stack trace on every allocation, so a sweep
  over a large load is quadratic in that cost: pass `std.heap.smp_allocator` as
  the backing allocator. The sweep's leak check is the failing allocator's own
  byte count, so it loses nothing.
- Every TUI test binary runs with `HOME` set to its own
  `zig/.zig-cache/test-home/<step>/<binary>`, wiped before each run
  (`isolatedHomeRun` in `build.zig`). Before that, `App.init` and the settings
  commands read and wrote the developer's real `~/.oapx`.
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

Each adapter is pinned to the versions its catalog entry names
(`harnesses/<id>.json`, Decision 0033) and is the executable form of their
`research/` ledgers, which record provenance hashes, the wire boundary, and
every impedance mismatch with its classification. Mismatches are recorded,
never silently compensated. The catalog is the one place a pin is written:
each version has a status (exactly one `current`; `supported`, `floor`,
`retired`), its endpoint version and capability revision, its ledgers and
corpus (`corpus_from` when the corpus was recorded at an older release), and
per-platform artifact digests. A floor's corpus runs through the current
adapter, so its expectations carry the current revision. `goap check` fails
when a ledger or corpus is missing, a digest is in none of its version's
ledgers, a corpus expects another revision, or a Go adapter's
`CapabilityRevision` or `CorpusDirectory` differs from the current version.
Neither tree spells a pin: Go reads `harnesses.Current("<id>")`, Zig reads the
`harness_pins` module `build.zig` generates from the catalog, and `goap check`
fails on any Go or Zig string literal equal to a catalog value. Both trees serve
one revision per version, the catalog's `capability_revision`: a served Zig
backend answers `capabilities.request` exactly as the Go adapter does, which
`TestBackendsMatchOapx` checks in CI. Move a pin by adding a version and a new corpus directory, never by editing one
in place.

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
`OAP_<HARNESS>_INTEGRATION=1` is set with an absolute `OAP_<HARNESS>_BIN`, and
every gate additionally accepts an optional `OAP_<HARNESS>_SHA256` binding the
exact artifact digest. Both rules live in `adaptertest.VerifiedBinary`, which is
the only implementation — a new gate gets them by calling it, and must, because
a gate that resolves its own binary is how two adapters came to ignore a digest
variable that looked like it worked. They
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

`goap hub` is a single-user local service: loopback bind by default, no auth,
`Host` allowlisted to loopback names on a loopback bind, and a restart kills
every session. The registry config's `environment` list is an explicit
allowlist — a child never inherits ambient variables that were not listed. Keep
these when touching `serve/` or `cmd/goap/serve.go`.

`drafts/hub.md` is that trust model and the rest of the hub's wire written down
as the specification both trees are judged against (Decision 0032). Every rule in
it names the Go test pinning it, and the rules no test pins are listed as gaps —
so read it before changing `serve/servehttp`, `serve/servestdio` or a future Zig
hub, and update it in the same PR. Where it and either tree disagree, it wins
and the wrong side is fixed or the divergence recorded in its own section.

## Conventions

- Commit subjects are `<area>: <imperative sentence>`, area being a package or
  adapter (`serve:`, `codex:`, `adapter:`, `zig:`, `fix:`).
- Changing `schema/v0.1/*.json` requires matching edits to `protocol/`, the
  validator, a fixture, and `clients/ts/src/protocol.ts`.
- The hub, codecs and clients never log envelope payloads or resolved
  environment values.
- Foreign-harness identifiers stay in namespaced `extensions`; they never become
  OAP identities.
- If behavior changes, update the spec or draft it touches in the same PR, and
  say what changed in the PR description, because the release notes are written
  from it. A pull request does not edit `CHANGELOG.md`: the release process
  writes it, in the Keep a Changelog shape, from the PRs merged since the last
  tag — [`docs/releasing.md`](docs/releasing.md) has the steps. So do not add an
  entry under `Unreleased`, and do not read a missing one as an unfinished PR.
- **An incompatible change to a public Go package is recorded in the pull
  request's own description**, under a `## Breaking changes` heading naming each
  package in backticks, because `apidiffcheck` reads that section and not the
  changelog. The heading is case sensitive; the release lifts the section into
  the notes under a `### Breaking changes` heading, level three because a `##`
  inside the version's own section would end it early. A package named only in
  prose does not count, and removing a public package counts as the most
  incompatible change there is — a `main` package does not, since nothing
  imports it.

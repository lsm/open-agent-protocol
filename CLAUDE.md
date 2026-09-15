# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repository is

Open Agent Protocol (OAP): a CC0 draft protocol for the boundary between a control layer and an agent loop, plus a Go executable core that proves it. The core is the `open-agent-protocol.agent-control-core` profile at schema v0.1. Everything in Go serves one invariant: an adapter over a third-party agent harness must emit event traces that the shared validator accepts. Semantics are frozen by `decisions/0001-*.md` and amended only by `decisions/0002-*.md`; `research/protocol-feedback-2026-09.md` is the adjudication record for every mismatch found while adapting eight harnesses. Read those three before changing protocol behavior.

## Commands

Go 1.27 module, no Makefile. CI (`.github/workflows/ci.yml`) runs exactly these, in order, and fails on any `gofmt -l` output:

```sh
test -z "$(gofmt -l .)"
go vet ./...
go test ./...
go test -race ./...
go run ./cmd/oap check      # compiles schemas, validates fixtures/manifest.json, drives the memory adapter demo
```

The full suite takes about 10 seconds. Single package or single test:

```sh
go test ./serve/...
go test ./adapter/hermes -run TestApprovalGateRoundTrip
go test ./adapter/codex/appserver -run EvidenceCorpus -v   # the hermetic corpus for one adapter
```

CLI subcommands (`go run ./cmd/oap <cmd>`): `check`, `validate [--format=json] <trace.json>...`, `fixtures [manifest]`, `demo`, `serve [--config path] [--addr host:port]`, `providers zai-cn`.

TypeScript client (`clients/ts`, zero runtime deps, Node 18+):

```sh
cd clients/ts && npm ci && npm test   # tsc build, then node --test over dist/test
```

Its integration test builds `./cmd/oap` and boots the memory adapter; set `OAP_GO` if `go` is not on `PATH`, or `OAP_TS_SKIP_INTEGRATION=1` to skip it.

### Opt-in real-process gates

Every `adapter/*/process_integration_test.go` (and `server_integration_test.go` for OpenCode) is skipped unless an `OAP_<HARNESS>_SMOKE=1` or `OAP_<HARNESS>_INTEGRATION=1` variable is set together with an absolute `OAP_<HARNESS>_BIN`. Hermes also requires `OAP_HERMES_ROOT`. Codex and Makai also require `OAP_CODEX_COMMIT` / `OAP_MAKAI_COMMIT` set to exactly the pinned commit, or the gate fails before running. `OAP_<HARNESS>_SHA256` is optional and binds the exact artifact digest. The README's "Real-process gate coverage" table lists them. These never run in CI, never download anything, and never pass ambient credentials to a child. Ordinary tests use fake keys and loopback mocks from `internal/providertest`. Live provider tests (`provider/live_zai_test.go`) are gated the same way (`OAP_LIVE_ZAI=1` plus explicit authorization variables); credential presence alone must never enable network traffic.

### Regenerating a corpus expectation

`adapter/<harness>/corpus_test.go` compares the reducer output against `expected-oap.json` in each case directory. An expectation is only rewritten when it is blank and `OAP_UPDATE_<HARNESS>_CORPUS=1` is set (`OAP_UPDATE_DSH_CORPUS` for DeepSeek); otherwise the test fails. Existing expectations are never overwritten silently, so to regenerate one, blank the file first. What counts as blank differs per adapter, because each corpus helper is hand-written:

| Adapters | Blank means |
| --- | --- |
| Hermes, Claude | zero-byte file or `[]` |
| DeepSeek, Pi, OpenCode | zero-byte file only (`[]` decodes to an empty expectation and fails the comparison) |
| ACP, Codex, Makai | `[]` only (a zero-byte file fails to decode) |

## Architecture

Packages form a strict stack; lower layers never import higher ones.

| Layer | Package | Role |
| --- | --- | --- |
| Wire model | `protocol` | Envelope, typed ID domains, every payload struct and `Type*` constant |
| Schema | `schema` | Embeds `schema/v0.1/*.json` (JSON Schema 2020-12) via `embed.FS` |
| Validator | `validation` | Three phases over a trace: decode (duplicate keys), schema, semantic state machine (`state.go`) emitting typed diagnostic codes |
| Adapter boundary | `adapter` | `Adapter`/`Session`/`EventStream` interfaces, shared error sentinels, `ValidateInputAnswer`, and `Memory`, the deterministic reference adapter |
| Test kit | `adapter/adaptertest` | `Next`/`Drain` stream helpers and `AssertProtocolValid*`, which assembles a full trace and runs the real validator |
| Harness adapters | `adapter/{acp,claude,codex/appserver,deepseek,hermes,makai,opencode,pi}` | One package per pinned upstream harness |
| Hub | `serve` | In-process registry + multi-session hub with bounded fan-out subscriptions and cursor replay; adds no protocol semantics and never validates |
| Codecs | `serve/servehttp`, `serve/servestdio` | HTTP+SSE daemon and newline-JSON stdio transport over the same hub; each route/op decodes, calls the hub, encodes. `servestdio` mirrors `servehttp` one-to-one and `parity_test.go` enforces it |
| Clients | `client` (Go), `clients/ts` | Far-side conformance proofs of the daemon wire, with invisible SSE resume |
| Entry | `cmd/oap` | Subcommand dispatcher; `serve.go` wires signals, loopback host allowlist, and bounded shutdown |

### Core semantic invariants

These are what the validator enforces and what every adapter's reducer must produce. Violating one is a protocol bug, not a style issue.

- Exactly one nonterminal run per session; `auto` delivery resolves to `start` or `queue` (Decision 0002). Admission is `started`+`start`+`running` or `queued`+`queue`+`queued`; any other combination is rejected.
- Every run-scoped event carries a positive, contiguous per-run `sequence`; requests/responses do not consume it.
- Exactly one terminal per run: `run.completed`, `run.failed`, or `run.cancelled`. A run may settle with `failed`/`cancelled` before `run.started` but never `completed`.
- `run.cancel.response` is intent, not settlement; `run.cancelled` needs an accepted cancel exchange as evidence (`AssertProtocolValidWithCancellation`).
- Resume (reattach), reconciliation (authoritative state), and replay (journal suffix from a cursor) are distinct. An expired cursor returns `*adapter.ReplayGap`, never fake continuity.
- `capability_revision` is schema-required only on `capabilities.response` and `capabilities.updated`. The validator adds: a request that supplies a revision must cite the current one (`protocol.initialize.request` and `capabilities.request` are exempt so discovery is never blocked) and its successful response must repeat it; and any envelope exercising an optional feature (non-`auto` delivery, tools, permissions, user input) must cite the active descriptor revision. As an implementation convention, not a validator rule, the adapters in this repo stamp the revision on every event they emit so consumers can bind an event to its descriptor snapshot. Capabilities report effective fidelity (`native`/`emulated`/`degraded`/`unavailable`), never an idealized harness.
- Only the declared responder resolves an interaction, once. Answers are validated with `adapter.ValidateInputAnswer` before any native write.

### Anatomy of a harness adapter

Each adapter is pinned to one upstream commit or tag and is the executable form of a mapping ledger in `research/<harness>-<pin>-mapping.md`. The ledger is the spec: it records provenance hashes, the wire boundary, every impedance mismatch, and its classification. Mismatches are recorded explicitly, never silently compensated.

Layout inside `adapter/<harness>/`:

- `adapter.go`: `Probe` (descriptor with fixed `CapabilityRevision`) and `Open`.
- `session.go`: the reducer and terminal arbiter that turn native frames into OAP envelopes. Adapters own this logic; they do not mechanically rename native events.
- `internal/native/`: the pinned upstream wire vocabulary (types only, with the commit in the package doc).
- `internal/rpc/` (or `internal/stdio/`, `internal/httpapi/`): a strict private codec plus child-process management. One reader dispatches; one serialized writer sends. Framing is fail-closed with bounded line lengths.
- `session_test.go`: unit tests against a scripted fake client, every emitted trace passed through `adaptertest.AssertProtocolValid*`.
- `corpus_test.go` (`Test<Harness>EvidenceCorpus`): the hermetic corpus. Reads `fixtures/adapters/<pin>/<case>/` (`native.jsonl` → production codec → production reducer → compared with `expected-oap.json`, with `omissions.json` naming each deliberately unmapped native frame and why, and `mapping.json` classifying each frame's fidelity). The corpus manifest pins the upstream commit, tree, and source blob hashes.
- `process_integration_test.go`: the opt-in real-process gate.
- `internal/rpc/process_test.go`: re-executes the test binary as a fake child (`-test.run=Test<X>Helper` with `OAP_<X>_RPC_HELPER=1`) to test transport death, oversized frames, and EOF teardown without a real harness.

Adding an adapter means all of the above plus: a `case` in `serve/registry.go`'s `buildAdapter`, an entry in `examples/oap-serve.json`, and a row in the README gate table.

### Fixtures and examples

- `fixtures/manifest.json` is normative: positive traces plus `schema-invalid` and `semantic-invalid` traces with the exact diagnostic codes the validator must emit, each with provenance back to a decision, draft, or example. Add a fixture whenever validator behavior changes.
- `examples/` is illustrative only; some files use staging vocabulary that is deliberately not in executable v0.1 (nested `scope`/`trace`, `model.content.delta`).
- `drafts/` holds the prose protocol drafts; `drafts/conformance.md` defines profiles and `+unit` claims.

### Daemon trust model

`oap serve` is a single-user local service: loopback bind by default, no auth, `Host` header allowlisted to loopback names on a loopback bind, restart kills every session. The registry config's `environment` list is an explicit allowlist; a child never inherits ambient variables that were not listed. Keep these properties when touching `serve/` or `cmd/oap/serve.go`.

## Conventions

- Commit subjects are `<area>: <imperative sentence>` where area is a package or adapter name (`serve:`, `codex:`, `adapter:`, `fix:`).
- Changing `schema/v0.1/*.json` requires matching edits to `protocol/`, the validator, a fixture, and `clients/ts/src/protocol.ts` (its `schema.test.ts` cross-checks the hand-written interfaces against the schema).
- The hub, codecs, and clients never log envelope payloads or resolved environment values.
- Foreign-harness identifiers stay in namespaced `extensions`; they never become OAP identities.

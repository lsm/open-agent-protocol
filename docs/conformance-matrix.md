# Conformance matrix: Zig, Go and Rust

What each tree implements of the protocol, as of `main` at `59880e0ab2`
(2026-10-19). **done** means implemented and judged by a shared check in CI;
**partial** means implemented in part, or implemented but not proven by a
shared check; **missing** means absent. The spec is the arbiter
([Decision 0032](../decisions/0032-go-and-zig-are-peers.md),
[Decision 0052](../decisions/0052-rust-is-the-third-peer.md)); this page
records where each tree stands against it. Rule-level validator coverage is in
[validation-coverage.md](validation-coverage.md).

## Matrix

| Spec item | Zig | Go | Rust |
| --- | --- | --- | --- |
| Envelope and payload types, agent profile | done | done | done |
| Envelope and payload types, provider profile | done | partial: validator only, no runtime | partial: SDK client types only |
| Validator: decode phase | partial: #1257 open | done | done |
| Validator: schema phase | done | done | done |
| Validator: agent semantic rules (77 codes) | partial: 62 codes; 9 in open PRs; 87 fixtures excused | done | missing |
| Validator: provider-profile rules | done | done | missing |
| Pack loading (20 codes) | partial: 5; #1261 adds 10 | done | missing |
| Harness catalog and pins | done | done | done: `oap-harness` reads the catalog and runs every catalog drift rule (#1442) |
| Adapters: acp, claude, codex, deepseek, hermes, opencode, pi | done (shared corpora) | done (shared corpora) | missing |
| Adapter: copilot | done | missing | missing |
| Memory reference adapter | done | done | done: `oap_adapter::memory`, which `oap-rust-hub serve agent` serves through every `goap conformance` check and the memory parity fixture |
| Hub, stdio | done | done | done |
| Hub, HTTP and SSE | done | done | partial: the gates, the read-only routes, tools, models, open with its held subscription, submit, resolve, cancel, settings, close, SSE events, transcript, the work routes and shutdown; `oap-rust-hub serve --addr` serves them on a loopback allowlist; no HTTP differential against goap yet |
| `serve agent` endpoint | done | done | done: `oap_serve::endpoint`, served by `oap-rust-hub serve agent`, passes every `goap conformance` check in CI and maps SIGINT and SIGTERM as Go does |
| Conformance runner | partial: asks no `session.list` | done | missing |
| Binding and run stores | done | done | done: `oap-binding`'s file and memory session histories and run store (#1473); the hub records opens, closes and refusals (#1489) |
| `transcript.load` (Decision 0051) | done | partial: hub route pending | partial: the stdio op and the HTTP route; no Rust adapter has a reader yet |
| Shutdown (Decision 0050) | partial: signals and restart recovery pending | partial: same | partial: the stdio op, `POST /shutdown`, the endpoint's request and its signals; the hub's signals and restart recovery pending |
| Client SDK | n/a (oapx is the runtime) | done | done (delegates to oapx) |
| Agent loop and provider runtime | done | done | missing |

Totals over the 19 rows that apply to all three trees (the SDK row does not
apply to Zig): Zig 14 done, 5 partial, 0 missing; Go 15 done, 3 partial, 1
missing; Rust 8 done, 4 partial, 7 missing.

## What proves alignment today

| Shared check | Zig | Go | Rust |
| --- | --- | --- | --- |
| `fixtures/manifest.json`, 785 traces | gates judge 669; skip codes not yet implemented | all 785, exact codes | 68 decode and schema traces only |
| Adapter corpora, byte for byte | yes | yes | no |
| Hub differential (`goap` vs `oapx` stdio) | yes | yes | yes, against `oap-rust-hub`, every scenario |
| Served backend parity (`TestBackendsMatchOapx`) | yes | yes | memory fixture only: `TestBackendsMatchRustHub` against `oap-rust-hub serve agent`; no Rust harness adapter yet |

The Zig semantic gate skips any code Zig has not implemented, so a green Zig
gate does not mean Zig agrees with Go on every trace. No check compares all
three validators trace by trace.

## Gaps, highest leverage first

Correctness and proof before features, smallest verifiable slice first.

1. **No check compares the three validators.** One CI job runs each tree's
   validator over the manifest and fails on any fixture where their codes
   differ, with a per-tree known-gap list instead of silent skips. Every later
   gap then shows up as a diff, not as an absence.
2. **The Zig gate hides unimplemented codes.** Make it strict: list each
   not-yet-judged fixture as a known gap, as `partial_code_gaps` already does
   for two codes.
3. **Rust has no semantic validator.** It judges 68 of 785 traces. Port the
   semantic phase in slices, each clearing a group of manifest fixtures.
4. **Zig's capture-position rules.** 87 excused fixtures for
   `session_state_mismatch` and `scope_mismatch`.
5. **Decision 0050 is unfinished in both trees**, and no validator judges it.
6. **Rust ports beyond the validator.** The harness catalog, memory adapter,
   stdio hub and endpoint are done, in Decision 0052's order; what is left is
   each harness adapter against the shared corpora, an HTTP differential
   against goap, the conformance runner, and the agent loop.

## Owners

| Gap | Owner |
| --- | --- |
| 1, 2, 4 | OAP zigzag |
| 3, 6 | OAP rust |
| 5, and validator gaps 2, 5, 8, 10, 12 of validation-coverage.md | OAP dev |
| Zig decode and pack loading (#1257, #1261), TUI quit (#1262) | OAP TUI |

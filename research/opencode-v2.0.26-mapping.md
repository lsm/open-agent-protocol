# OpenCode v2.0.26 mapping ledger

Status: the current pin, a move over
[`opencode-v2.0.24-mapping.md`](opencode-v2.0.24-mapping.md), which remains
the mapping. This one records only what moved between the two pins and the
evidence for it. Everything not named here is unchanged.

## Provenance

- Repository: `https://github.com/anomalyco/opencode`
- Release: `v2.0.26`, the latest v2 build on 2026-10-10 (the `2.0.27`
  artifact path answers `404`)
- Commit: `9b4ec5714d481559990db0a816d5dec19541a814`, committed 2026-10-08
- Commit tree: `7e5ecbf5d14e173aa916f6c1e3b8760a8853db2b`

```sh
git clone https://github.com/anomalyco/opencode.git
git -C opencode checkout 9b4ec5714d481559990db0a816d5dec19541a814
git -C opencode rev-parse HEAD 'HEAD^{tree}'  # 9b4ec57..., 7e5ecbf...
git -C opencode describe --tags               # v2.0.26
```

As at v2.0.24, the artifacts come from
`https://opencode.ai/files/bin/2.0.26/<name>`, not a GitHub release. The
darwin-arm64 binary reports `opencode v2.0.26`:

| Platform | Kind | Name | SHA-256 | Bytes |
| --- | --- | --- | --- | --- |
| linux-x64 | archive | `opencode-linux-x64.tar.gz` | `6d94dde0ac1b009327189772f5e242e13f7f7916a4fcd58bf39f9c15d6a517d1` | 98021311 |
| linux-x64 | binary | `opencode` | `d9123b9bcdb0a31f7ab9fe9812b58a9dc1768ad6d97e7b276dff1181813218c9` | 210109920 |
| darwin-arm64 | archive | `opencode-darwin-arm64.zip` | `325fa77c1e0f46e40a8297c168355501965230bd1f32923f367ec1b5ee19fac3` | 85177346 |
| darwin-arm64 | binary | `opencode` | `1b6418a3bd4211344d8a2b75d7d4367b24283ae548bd6b896cf717f6f8858f26` | 185808848 |

The catalog admits `2.0.26` on that evidence. Without an `admits` list a
host's version check passed any OpenCode, and hyperneo-review ran v2.0.18
against the v2.0.24 adapter and got no final text
(lsm/open-agent-protocol#1035).

## Source difference, v2.0.24 to v2.0.26

Every blob the corpus pins is byte-identical at both tags:
`packages/schema/src/session-event.ts` (`0cffae27…`),
`packages/schema/src/session-inbox.ts` (`361c0c49…`),
`packages/core/src/session/execution.ts` (`eeed241a…`),
`packages/protocol/src/groups/session.ts` (`d5bf2af1…`),
`packages/server/src/handlers/session.ts` (`73a07633…`) and
`packages/core/src/session.ts` (`39f5f36d…`). So are
`packages/schema/src/event.ts`, `packages/schema/src/permission.ts`,
`packages/protocol/src/groups/event.ts` and `packages/core/src/bus.ts` from
the v2.0.24 table.

The two blobs from that table which changed:

| Path | v2.0.24 | v2.0.26 | Change | Effect on the adapters |
| --- | --- | --- | --- | --- |
| `packages/server/src/routes.ts` | `c4f746f1…` | `f8e6da47…` | the MCP `clientInfo.name` is always `opencode` | none; the adapters speak no MCP to the server |
| `packages/cli/src/server-process.ts` | `48c036c4…` | `28ae6822…` | an optional remote tunnel, started only when the service config names a `remote` route, whose URL joins `/api/info`'s `urls` | none; the adapters write no service config, and check `urls` only as a list of strings, which it still is |

Neither change touches what the v2.0.24 ledger's empty durable log rests on:
`routes.ts` still passes `options.events?.persist`, and `server-process.ts`
still never sets it.

Elsewhere on the served path:

- `packages/server/src/server-info.ts`: a wildcard bind (`0.0.0.0`, `::`)
  now lists a loopback URL first in `urls`. The adapters bind `127.0.0.1`, so
  the list they see is unchanged.
- `packages/core/src/session/runner/step.ts`: a tool call whose arguments
  the parser left unfinished now fails with error type
  `tool.input-incomplete`. Both trees decode a tool failure's error `type`
  as an unconstrained string, as they already do `tool.result-missing`, so a
  new type needs no decoder change.
- `packages/core/src/session/runner/publish-llm-event.ts`: the start
  snapshot is captured while the provider request is in flight and awaited
  before `step.started`. The event's members and its order relative to the
  step's other events are unchanged.
- `packages/core/src/session/projector.ts`: `step.started`, `step.ended`
  and `step.failed` now update the session's `time_updated`. The adapters do
  not read it.
- `packages/schema/src/model.ts`: two optional compatibility flags,
  `supportsThinkingBlockBinding` and `supportsEffortUpdates`. The adapters
  write no model compatibility.

The adapter code does not change in either tree. The capability descriptor
is unchanged, so only the revision's version prefix moves:
`opencode-v2.0.26-oap-v5`.

## Corpus

`fixtures/adapters/opencode-v2.0.26` is the v2.0.24 corpus carried forward
(`corpus_from: v2.0.24`).
No case was re-recorded: every `native.jsonl` is byte-identical, and the
source blobs each case pins are unchanged. Only the provenance (the
manifest's and each case's `tag`, `commit` and `commit_tree`) and the
expectations' `capability_revision` moved, as did the revision and endpoint
version in `go/adapter/opencode/testdata/port-{goldens,scenarios}.json`.

## Process gate

Live, against the pinned darwin-arm64 binary (sha256 `1b6418a3…`), with
`OAP_OPENCODE_INTEGRATION=1` and `OAP_OPENCODE_SHA256` set: all six gates in
`server_integration_test.go`, `permission_integration_test.go` and
`reopen_integration_test.go` pass three times, among them
`TestOpenCodeServerRunsATurnToCompletionAgainstALocalProvider`, which
completes a real turn against an in-process OpenAI-compatible fake and so
reaches no network.

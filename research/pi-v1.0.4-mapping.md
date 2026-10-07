# Pi coding agent v1.0.4 mapping ledger

Status: implemented production OAP adapter, re-pinned from v1.0.1
([`pi-v1.0.1-mapping.md`](pi-v1.0.1-mapping.md), now retired) to v1.0.4.
Everything in that ledger carries over except where this one says otherwise.

## Provenance

- Repository: `https://github.com/earendil-works/pi`
- Release: `v1.0.4` (latest on 2026-10-06, published 2026-10-05T22:03:50Z)
- Commit: `7c10bd4337495ee613f2224843ecdf349b80d1df`, tree
  `de4e0ad2e4ca4d3bf80a6e89d1bec453edbc85a0`, committed 2026-10-05T19:22:49Z
- Release artifacts, digests from the release metadata and checked on
  download:
  - `pi-darwin-arm64.tar.gz`:
    `717dcd38a03849e919f9dec9daa96f5ca102e15ea33d804e5db57b1d47e513bc`
    (31016950 bytes), holding `pi/pi`:
    `6a5436fb5a1853d934a9e434dae9eb279b9760fb91c9704c95506e6ab95b3107`
    (76853858 bytes), which reports `1.0.4`
  - `pi-linux-x64.tar.gz`:
    `284c45dd28cf975a13cff6af34741dd0a0cdca6634e8bdfc0083ae7d452e86d6`
    (42563881 bytes), from release metadata only; not downloaded or run

Inspected source blobs (`git rev-parse v1.0.4:<path>`):

| Source | v1.0.1 blob | v1.0.4 blob |
|---|---|---|
| `modes/rpc/rpc-types.ts` | `7fc71516` | `7fc71516` (unchanged) |
| `modes/rpc/rpc-mode.ts` | `1c0995d3` | `1c0995d3` (unchanged) |
| `core/agent-session.ts` | `f641d6ec` | `95b96350` |
| `core/session-manager.ts` | `df5281a0` | `df5281a0` (unchanged) |
| `packages/agent/src/types.ts` | `6e17c3c8` | `6e17c3c8` (unchanged) |
| `rpc-entry.ts` | `11059a8d` | `11059a8d` (unchanged) |
| `cli/args.ts` | `9461c3e2` | `c994db01` |

## Changes from v1.0.1

57 commits and 221 files separate the tags. The RPC types and mode, the
session store and the agent's message types are byte-identical, so the wire
the adapters read and write does not change. What moved near it:

- **Tool allowlists match patterns and keep MCP tools.** `--tools` and
  `--exclude-tools` take names or `*` patterns, and a non-empty `--tools` list
  that names no `mcp__` tool keeps MCP tools registered for codemode and
  `tool_search` without declaring them. Neither adapter passes `--tools`.
- **`--no-mcp`** disables built-in MCP support. Neither adapter passes it.
- **`packages/ai/src/types.ts`**: the `azure-openai-responses` provider is
  renamed `azure`, and a model gains an optional `samplingParamsByThinkingLevel`.
  Neither is read: the adapters project the model's `provider` and `id` and
  admit unknown model members.

The adapters do not change. The descriptor changes only in the endpoint
version it reports, so the revision moves to `pi-v1.0.4-oap-v1`.

## Corpus

`fixtures/adapters/pi-v1.0.4` carries every `native.jsonl`, `mapping.json`
and `omissions.json` of the v1.0.1 corpus forward unchanged; none was
re-recorded. The manifest's and each case's provenance (tag, commit, tree and
the two changed blobs) and the expectations' revision changed.

## Real-process evidence

The darwin-arm64 binary above, through the Go adapter, with
`OAP_PI_SMOKE=1`, `OAP_PI_INTEGRATION=1` and `OAP_PI_SHA256=6a5436fb…`: the
whole package passes 3x, including `TestPiProcessAgainstResponsesMock`, the
threshold, requested and cancelled compaction runs, the live level and
compaction change, and the reopen of a bound session file.

## Native sessions

The adapter lists and reads the store read-only (Decision 0047): it is
`session-manager.ts`, unchanged at this pin.

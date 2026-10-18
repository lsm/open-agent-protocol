# Pi coding agent v1.3.0 mapping ledger

Status: implemented production OAP adapter, re-pinned from v1.1.0
([`pi-v1.1.0-mapping.md`](pi-v1.1.0-mapping.md), now retired) to v1.3.0.
Everything in that ledger carries over except where this one says otherwise.

## Provenance

- Repository: `https://github.com/earendil-works/pi`
- Release: `v1.3.0` (latest on 2026-10-17, published 2026-10-15T19:17:33Z); the
  tag is lightweight and names the commit directly
- Commit: `71023aec1546d6d677a65669c5665c0927ce0dd0`, tree
  `25e0557c8b13f98848bebd48fe7922908f66fe4a`, committed 2026-10-15T18:57:27Z
- Release artifacts, digests from the release metadata and checked on
  download:
  - `pi-darwin-arm64.tar.gz`:
    `1f9ae61cdeae04dd68caf1a1e8ab8ec9b292062f6ccb3252e0a9cbe93b5b16f1`
    (31071837 bytes), holding `pi/pi`:
    `f023d9d384bebed2f48c49c85fbece6c5ec1cc6f5780f21f5b36b24ee55b8b3d`
    (77085026 bytes), which prints `1.3.0` to stdout for `--version`
  - `pi-linux-x64.tar.gz`:
    `24f5be328b66d4da36876296989501974f43ab8434fbb57c35c523a3ea505ded`
    (42617111 bytes), holding `pi/pi`:
    `c2415d6e6f7c67b0a55744c5ace5054936e252fcc3cf56ce087bc314061315ca`
    (107174016 bytes); downloaded and hashed, not run

Inspected source blobs (`git rev-parse v1.3.0:<path>`):

| Source | v1.1.0 blob | v1.3.0 blob |
|---|---|---|
| `modes/rpc/rpc-types.ts` | `7fc71516` | `7fc71516` (unchanged) |
| `modes/rpc/rpc-mode.ts` | `1c0995d3` | `1c0995d3` (unchanged) |
| `core/agent-session.ts` | `5decccd8` | `8b330d55` |
| `core/session-manager.ts` | `df5281a0` | `df5281a0` (unchanged) |
| `packages/agent/src/types.ts` | `dbfba74a` | `dbfba74a` (unchanged) |
| `packages/ai/src/types.ts` | `4b5f2038` | `302f9558` |
| `rpc-entry.ts` | `11059a8d` | `11059a8d` (unchanged) |
| `cli/args.ts` | `38d268a9` | `38d268a9` (unchanged) |
| `docs/json.md`, `docs/rpc.md` | `ca76d632`, `a39cc8da` | unchanged |

## Changes from v1.1.0

716 files separate the tags. The RPC types and mode, the agent event types,
the session store and the CLI arguments are byte-identical. One member the
adapters' closed shapes did not admit now appears on the wire:

- **A tool call carries `argumentsError`** (`packages/ai/src/types.ts`), an
  optional string set when the model's arguments text was not a JSON object;
  `arguments` is then `{}` and the tool's argument validation fails with that
  text (`utils/json-parse.ts`, `utils/validation.ts`). It appears wherever a
  `toolCall` content block does: in an assistant message and in a
  `toolcall_end` provider event. A model that sends malformed arguments would
  have failed the run in both trees.
- `agent-session.ts` changed its prompt and abort plumbing (an abort requested
  before `agent_start` now aborts at it) without changing an event it emits.
- The rest of `packages/ai/src/types.ts` moves compatibility and model-schema
  types into their own modules; none reaches the RPC wire.

**The adapters change.** Both trees admit `argumentsError` (a string) on a
`toolCall` block, as an optional member. Neither projects it: the call already
fails through `tool_execution_end` with `isError`, which settles
`action.call.failed`. Every other member stays refused as before.

The descriptor changes only in the endpoint version it reports, so the
revision moves to `pi-v1.3.0-oap-v1`.

## Corpus

`fixtures/adapters/pi-v1.3.0` carries every `native.jsonl`, `mapping.json`
and `omissions.json` of the v1.1.0 corpus forward unchanged; none was
re-recorded. The manifest's and each case's provenance (tag, commit, tree and
the changed `agent-session.ts` blob) and the expectations' revision changed.
Unit tests in both trees cover the new member instead: a run whose final
message holds a tool call with `argumentsError` fails that call and completes,
a `toolcall_end` carrying it decodes, and a non-string `argumentsError` is
refused.

## Real-process evidence

The darwin-arm64 binary above, through the Go adapter, with
`OAP_PI_SMOKE=1`, `OAP_PI_INTEGRATION=1` and `OAP_PI_SHA256=f023d9d3…`, under
`sandbox-exec` denying the keychain daemons and every non-loopback connection,
with a temporary `HOME`, an empty environment and no credentials: the whole
package passes 3x, including `TestPiProcessAgainstResponsesMock`, the
threshold, requested and cancelled compaction runs, the level and compaction
change between runs, and the reopen of a bound session file. None of those
gates makes a tool call, which is why the new member is pinned by unit tests.

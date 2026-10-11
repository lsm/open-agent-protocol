# Pi coding agent v1.1.0 mapping ledger

Status: implemented production OAP adapter, re-pinned from v1.0.4
([`pi-v1.0.4-mapping.md`](pi-v1.0.4-mapping.md), now retired) to v1.1.0.
Everything in that ledger carries over except where this one says otherwise.

## Provenance

- Repository: `https://github.com/earendil-works/pi`
- Release: `v1.1.0` (latest on 2026-10-10, published 2026-10-07T22:26:31Z); the
  tag is lightweight and names the commit directly
- Commit: `abe508e1b89912adde45528136c3221eb69acdd7`, tree
  `77d78fae728879c16550b1f874785aed7851233c`, committed 2026-10-07T21:33:33Z
- Release artifacts, digests from the release metadata and checked on
  download:
  - `pi-darwin-arm64.tar.gz`:
    `3455b13de35c15a5893cdebc922678199e90a7ce99b06cbc23f37860e90d63c7`
    (31029214 bytes), holding `pi/pi`:
    `daea10db90f7806cd0ae5be89fcf819d47b5ed3df99b7c79c31186a1cd6a6501`
    (76919906 bytes), which prints `1.1.0` to stdout for `--version`
  - `pi-linux-x64.tar.gz`:
    `3faa94666cd3849d37af320ff749407d0271b07a9b94f420c87e30866e10e289`
    (42576785 bytes), holding `pi/pi`:
    `5d527eae57d99f80f9397a84d0228528a92b3595b2c5f3d969e45b02ea72a6bc`
    (106997888 bytes); downloaded and hashed, not run

Inspected source blobs (`git rev-parse v1.1.0:<path>`):

| Source | v1.0.4 blob | v1.1.0 blob |
|---|---|---|
| `modes/rpc/rpc-types.ts` | `7fc71516` | `7fc71516` (unchanged) |
| `modes/rpc/rpc-mode.ts` | `1c0995d3` | `1c0995d3` (unchanged) |
| `core/agent-session.ts` | `95b96350` | `5decccd8` |
| `core/session-manager.ts` | `df5281a0` | `df5281a0` (unchanged) |
| `packages/agent/src/types.ts` | `6e17c3c8` | `dbfba74a` |
| `rpc-entry.ts` | `11059a8d` | `11059a8d` (unchanged) |
| `cli/args.ts` | `c994db01` | `38d268a9` |

## Changes from v1.0.4

53 commits and 242 files separate the tags. The RPC types and mode and the
session store are byte-identical, but three members the adapters' closed
shapes did not admit now appear on the wire:

- **`agent_settled` carries `aborted`**, `true` when the run ended because it
  was aborted (`agent-session.ts`, and `docs/json.md`, which now shows
  `{"type":"agent_settled","aborted":false}`). Every run's settlement carries
  it, so v1.0.4's closed shape failed every run.
- **Messages and the tool end carry `durationMs`**: the assistant message the
  response stream finished (`packages/ai/src/types.ts`), a tool result message,
  and the `tool_execution_end` event (`packages/agent/src/types.ts`), each
  optional and measured with a monotonic clock.
- **`--tools`** takes `+name`/`-name` entries and reports a malformed list as
  a diagnostic (`cli/args.ts`). Neither adapter passes `--tools`.
- `packages/coding-agent/docs/message-types.md` no longer documents a system
  message's `replace`. Neither adapter admitted it, so nothing changes.

**The adapters change.** Both trees admit `aborted` (a boolean) on
`agent_settled` and `durationMs` on the assistant message, the tool result
message and `tool_execution_end`, as optional members, so the carried-forward
corpus, whose frames predate them, still decodes. Neither tree projects them:
cancellation is still settled by the adapter's own cancel exchange, and no OAP
member carries a duration. Every other member stays refused as before.

The descriptor changes only in the endpoint version it reports, so the
revision moves to `pi-v1.1.0-oap-v1`.

## Corpus

`fixtures/adapters/pi-v1.1.0` carries every `native.jsonl`, `mapping.json`
and `omissions.json` of the v1.0.4 corpus forward unchanged; none was
re-recorded. The manifest's and each case's provenance (tag, commit, tree and
the three changed blobs) and the expectations' revision changed. Unit tests in
both trees cover the new members instead: a run whose assistant message
carries `durationMs`, settled with `aborted: false`, completes; a tool run
whose `tool_execution_end` and tool result message carry `durationMs`
completes its call; and `agent_settled` still refuses a member it does not
declare. The tool run is a unit test because none of the real-process gates
below makes a tool call.

## Real-process evidence

The darwin-arm64 binary above, through the Go adapter, with
`OAP_PI_SMOKE=1`, `OAP_PI_INTEGRATION=1` and `OAP_PI_SHA256=daea10db…`, under
`sandbox-exec` denying the keychain daemons and every non-loopback connection,
with a temporary `HOME` and no credentials: the whole package passes 3x,
including `TestPiProcessAgainstResponsesMock`, the threshold, requested and
cancelled compaction runs, and the reopen of a bound session file.

Before the adapters admitted the new members, every one of those runs failed:
first `pi_invalid_message_end` (`json: unknown field "durationMs"`), then
`pi_invalid_event` for `aborted` on `agent_settled`. The same failure
reproduced outside the sandbox, so it was the release, not the sandbox.

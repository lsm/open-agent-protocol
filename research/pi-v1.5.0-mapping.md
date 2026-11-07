# Pi coding agent v1.5.0 mapping ledger

Status: re-pinned from v1.3.0. The [v1.3.0 ledger](pi-v1.3.0-mapping.md)
and its predecessors remain applicable except as described here.

## Provenance

- Repository: https://github.com/earendil-works/pi
- Tag `v1.5.0`: commit `f9a26365568127461c47a07a37dcb42c140a3ac5`, tree
  `b7734e15f2c1a10846284f77034a2214746a4da6`.
- Release metadata SHA-256: `pi-linux-x64.tar.gz`
  `df870ba0e344cb7b3358d8d8f19397b8b8d225a8669784736ad68a5c2169d53b`;
  `pi-darwin-arm64.tar.gz`
  `a9bf0d82d58d2a7d337f4ae4171b5fc9ebffe42509527a831364c4b65adf2cdf`.
  Artifacts were not downloaded or executed for this pin.

## Wire comparison

Comparing the two tag trees, `packages/coding-agent/src/modes/rpc/` and
`packages/coding-agent/src/modes/json-event.ts` are byte-identical. The latter
strips cumulative assistant snapshots and adds the tool ID and name to
`toolcall_start`. `packages/agent/src/types.ts` changes tool parameter typing,
but not the AgentEvent union. `packages/ai/src/types.ts` adds optional boolean
`usage.cost.totalFromProvider`: usage is opaque to the adapter and is not
projected. Other changes to this file concern request or model types rather
than message event members. The session manager and agent session changed; the
source comparison does not prove all possible runtime behavior is unchanged.
`packages/coding-agent/src/cli/args.ts` changes help text for MCP, session,
export and self-update options; its argument parsing code is unchanged,
including the `--mode` and `--no-extensions` flags used by the adapter.
This is source evidence only, not a v1.5.0 process check.

Pi now parses streaming tool arguments incrementally, retaining a final full
parse. The RPC serializer still sends deltas rather than cumulative snapshots.
Large individual JSONL frames remain subject to the adapters' existing 8 MiB
default limit; this pin does not claim arbitrarily large tool calls work.
Compaction summaries now use the model output limit rather than fractions of
reserveTokens, and custom instructions also reach split-turn summaries. The
OAP adapter observes the existing compaction events; it does not control that
model limit. Summary length and live compaction behavior were not verified.

## Corpus

`fixtures/adapters/pi-v1.5.0` carries the prior corpus forward with
updated provenance and capability revision; the retired v1.3.0 corpus is removed.
The completed-text case adds the
new cost flag; streaming-deltas adds synthetic tool-call start, fragmented
arguments totaling 16 KiB, and end. A first draft omitted the required start ID and name, and
failed the full Go corpus with `pi_invalid_message_update`; it was corrected
against `json-event.ts`. No adapter defect was established from that failure.
No real account, credentials, model call or new process recording was used for this upgrade.
The corpus is not evidence of behavior for every provider or large tool call.

# DeepSeek Harness dsh-v0.2.2-rc.2 release-candidate mapping

This release-candidate pin extends the
[dsh-v0.2.2-rc.1 ledger](deepseek-harness-dsh-v0.2.2-rc.1-mapping.md). No
stable dsh release is asserted. Source:
`https://github.com/deepseek-ai/deepseek-harness`, tag `dsh-v0.2.2-rc.2`,
commit `ddac96760b6edf8fa0fe17cbdcf0b512f4c12b2c`, tree
`3c4b8825db42206bec763295ce901af11d29ba61`. The source comparison was
read-only; no account, model, or live process was used.

## Wire review

The SDK protocol the adapter speaks is unchanged: `packages/sdk/protocol/src/
types.ts` and `transport.ts` are byte-identical to rc.1, the server keeps the
`0.0.1` identity, and `initialize`, `session/prompt` and `shutdown` keep
their shapes. `packages/sdk/server/src/user-questions.ts`,
`query-activity.ts`, `packages/interaction/user-questions/src/types.ts`,
`packages/bundle/sdk-minimal/cordis.patch.yml`, `packages/core/agent/src/
types.ts`, `packages/core/agent-loop/src/inbox.ts` and the three LLM sources
are also byte-identical.

The differences, and what each means here:

- `packages/core/session/src/known-event-types.ts` adds `team/mode` to the
  known event vocabulary. The adapter admits it as observed-only in all
  three trees (Go's `pinnedObservedOnlyEvents`, Zig's reducer vocabulary and
  Rust's `observed_only`), so a required `team/mode` event no longer fails a
  run as `deepseek_unknown_event`; it is observed without an OAP projection,
  like the other `team/*` events. Team-mode behavior is not otherwise
  mapped or verified.
- `packages/sdk/server/src/server.ts` tracks the delegating parent from the
  subagent end info (`info.parentSessionId`) instead of a service-owned
  scoped carrier, and settles a closing session on `waitForTreeWork` rather
  than `waitForChildren`, which is how the release's named multi-layer
  subagent teams stay running under one session. The adapter's children
  tracking keys on subagent start/end notifications and does not assume a
  single layer, so deeper trees flow through the same mechanism; multi-layer
  team settlement was not verified live.
- `packages/core/session/src/surface.ts` reads events through a `read(seq)`
  accessor instead of an array slice, and `packages/core/session/src/types.ts`
  adds the `SessionEventInput` utility type: internal shape only.
- `packages/core/agent/src/runtime-types.ts` documents queue-write failure
  propagation, and `packages/core/agent-loop/src/agent.ts` takes a lifetime
  abort signal: runtime-internal behavior, not adapter surface.

The release notes describe desktop product features (Git worktree review,
named multi-layer subagent teams, desktop update channels); the harness SDK
wire above is what reaches this adapter. Interactive user questions,
approvals, commands, scheduling and end-to-end execution remain not verified.

## Source blobs

| Source | Git blob |
| --- | --- |
| `packages/sdk/protocol/src/types.ts` | `6bcd2de7045e58c0fab0e5bacf956724d1ff6e68` (unchanged) |
| `packages/sdk/protocol/src/transport.ts` | `4ee65555c0342b276e43b03186659acb623cb6fe` (unchanged) |
| `packages/sdk/server/src/server.ts` | `bccfa6634e071c5bf1fad2f6b2fde80b0c8e503b` |
| `packages/sdk/server/src/user-questions.ts` | `fab6916c39810d3ba17c037af898f40a29a1ed73` (unchanged) |
| `packages/sdk/server/src/query-activity.ts` | `17a4fc9148e5f456c5e9e480cb71607cffeb0b24` (unchanged) |
| `packages/interaction/user-questions/src/types.ts` | `25340a3009ec182352e83aa0ba1c6cd8a25037f9` (unchanged) |
| `packages/bundle/sdk-minimal/cordis.patch.yml` | `e46eed1e0fb6520a48e7b9929c5ca343c2e2deba` (unchanged) |
| `packages/core/session/src/types.ts` | `0cf13c86a0313ba8869f36bb48004e61dc4cb685` |
| `packages/core/session/src/known-event-types.ts` | `466e76bb80845580ae24b2e72cc1bafbea88fc2b` |
| `packages/core/agent/src/types.ts` | `06d2c88ab1e5ada455214555aaae542077caa571` (unchanged) |
| `packages/core/agent-loop/src/inbox.ts` | `affe0c6b2f653888dc2177fd2987c6082a93f758` (unchanged) |
| `packages/core/agent/src/runtime-types.ts` | `7012dfdf50b7c78b07697fbe0cdc9defec7bb957` |
| `packages/core/agent-loop/src/agent.ts` | `7ec43dd1323edc6e2b488d7c9d35fdd2d323abbd` |
| `packages/llm/llm/src/message.ts` | `d96a63edb6d78d64508875ebc1cc6a1cc5a824b6` (unchanged) |
| `packages/llm/llm/src/types.ts` | `8a48b89895db0ddb8908ec6359eb67af60fe0f54` (unchanged) |
| `packages/llm/llm/src/assistant-stream.ts` | `7fca34c5d5bf432f7fad81bd48edfbdaf5083e83` (unchanged) |
| `packages/core/session/src/surface.ts` | `7a13bc1d7c1c87bae96a52ed7b160f8d6a635e3c` |

## Corpus

The corpus carries the rc.1 fixtures forward with updated provenance (tag,
commit, tree and the six changed source blobs) and the
`deepseek-harness-dsh-v0.2.2-rc.2-oap-v3` capability revision. No case was
re-recorded; no corpus case carries a `team/mode` event, so the new
vocabulary entry is exercised by a per-tree admission test (a `team/mode`
event inside a run settles nothing and fails nothing) rather than by a
replayed trace. The Go/Rust divergence records in the rc.1 ledger remain
unchanged.

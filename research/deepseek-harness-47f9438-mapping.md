# DeepSeek Harness 47f9438 mapping ledger

Status: approved pinned contract for the eighth production OAP adapter candidate.
This ledger freezes the selected JSON-RPC boundary and the conservative adapter
projection. It is an implementation input, not a claim about other Harness
surfaces. No adapter code exists yet.

## Provenance

- Repository: `https://github.com/deepseek-ai/deepseek-harness`
- Implementation target commit: `47f943859bef60e4160492346772ded9b24f765a`
- Exact commit title: `Merge pull request #2519 from deepseek-harness/feat/npm-public`
- Commit date: 2026-08-13
- Commit tree: `f904efab9ef435201d6ba4da88a34d6366568272`
- Commit body records release `dsh@0.1.0-rc.5` and public publication of the
  dsh package family. That release text is not the commit title.
- Newer inspected candidate: `82a5fd61a7cf5c293cec4bdff68f455398d685e9`
  (2026-09-07, release `0.1.3-alpha.2`) — design signal only; the target
  commit above is normative.

Reproduce the pin:

```sh
git clone https://github.com/deepseek-ai/deepseek-harness.git dsh
git -C dsh checkout 47f943859bef60e4160492346772ded9b24f765a
git -C dsh show -s --format='%H%n%s%n%T' HEAD
# 47f943859bef60e4160492346772ded9b24f765a
# Merge pull request #2519 from deepseek-harness/feat/npm-public
# f904efab9ef435201d6ba4da88a34d6366568272
```

Normative inspected sources and exact blobs:

| Source | Blob | Contract evidence |
|---|---|---|
| `packages/sdk/protocol/src/types.ts` | `533b5f23c5f019db924647916ffcde4a144541d5` | request, result, and notification wire types |
| `packages/sdk/protocol/src/transport.ts` | `36574f46bf3e34738be045408e25bb79932ff609` | native newline JSON-RPC transport |
| `packages/sdk/server/src/server.ts` | `195caa908de0343b59244ae966b4b7afe3cf93d2` | dispatch, lazy sessions, prompt receipt, notifications, teardown |
| `packages/core/session/src/types.ts` | `17aacd1dfc2f3a9d241a2fbdea59263323f57d51` | durable session-event envelope and turn/step lifecycle |
| `packages/core/session/src/known-event-types.ts` | `d65935f1b86934b1de957aa26f9032c296510d3c` | recognized event vocabulary and unknown-event rule |
| `packages/core/agent/src/types.ts` | `b54e56ea8f9dc61674167dff98dbbe7dc5c857e4` | durable `agent/inbox/spliced` event |
| `packages/core/agent/src/inbox.ts` | `c6b9204c92f497ae90b4e058fb6e0905427c4c8d` | synchronous append, claim deletion, and live notifications |
| `packages/core/agent/src/runtime-types.ts` | `7d713f8c77112f8e74150bc060ec677bb5107f90` | runtime-only `agent/inbox/claimed` event |
| `packages/core/agent-loop/src/agent.ts` | `668ef6582657ed0e1e4420777696ee50251371ad` | status, turn ordering, step entry, and terminal reasons |
| `packages/llm/llm/src/message.ts` | `608b56475df8dfdef72e104f180cf4dd024eb0be` | stable message identity and direct-user source shape |
| `packages/llm/llm/src/types.ts` | `326db1cb1473cb435ec98425c2df359977eba4fa` | content, finish, and usage vocabulary |
| `packages/core/session/src/invariant.ts` | `da7cd55964b7b49fc00d6bab0a65d50994b4f2c3` | turn/step/call relational validation |
| `packages/core/session/src/surface.ts` | `ba6c2dda800f36d64b370a7fac375db3f4486334` | append-surface message projection |

The project is a Cordis plugin runtime ("everything is a plugin") in developer
preview with declared compatibility-breaking changes. The commit, tree, and
source blobs above are therefore part of the adapter contract.

## Boundary selection

The adapter boundary is the SDK JSON-RPC runtime server
(`@deepseek-ai/dsh-sdk-jsonrpc-server`): newline-delimited JSON-RPC 2.0 over
stdio. The web app, ACP package (`packages/acp`), CLI, Typert gateway RPC
(`packages/api`), and in-process Cordis event bus are different surfaces. Facts
visible only on those surfaces may explain native behavior but cannot support an
advertised OAP capability at this boundary.

## Wire protocol and adapter codec

The SDK exposes three requests:

- `initialize { cwd, provider, model, maxTokens? }` returns
  `{ serverInfo: { name: "deepseek-harness-sdk-runtime", version: "0.0.1" } }`;
- `session/prompt { sessionId, contentBlocks }` returns `{ messageId }`;
- `shutdown` returns `{}` after pending creations and owned agents settle and
  the optionally mounted LLM fiber is disposed.

It exposes four notifications:

- `session.event { sessionId, event: SessionEvent }` forwards each durable
  session event when appended;
- `session.status { sessionId, status: "idle" | "running" }` reports
  whole-agent state;
- `subagent.started { parentSessionId, childSessionId }`;
- `subagent.finished { provider, agentId, parentSessionId, childSessionId,
  status: "ok" | "error", stopReason, lastAssistantMessage? }`, for local
  in-process children only.

There is no SDK cancel, steer, explicit queue operation, interaction, model
catalog, resume, or replay request at this pin.

### Initialization freeze

The pinned server's `initialize()` is mutable and repeatable: every call rewrites
`cwd`, `provider`, `model`, and `maxTokens`; there is no initialized guard. Its
source comment saying reinitialization is unsupported is not runtime
enforcement. Repeated calls can also replace `llmFiber` without first disposing
the prior fiber. The adapter therefore owns a stricter one-shot freeze: accept
exactly one successful initialization, snapshot the effective configuration,
and reject every later initialize request rather than pretending the upstream
runtime negotiated or froze it. Session creation and prompt admission are
forbidden before that adapter-owned success boundary.

### Framing strictness

Native `JsonRpcLineTransport` searches for LF, trims the complete line, skips
blank lines, thereby tolerates CRLF and surrounding JSON whitespace, ignores
JSON syntax errors and non-object JSON values, normalizes array/scalar `params`
to `{}`, and leaves an unterminated tail unparsed at EOF. Objects are classified
structurally by `id` and `method`; unknown notifications are dropped, missing
request handlers return `-32601`, and handler failures return `-32603`.

The production adapter codec intentionally accepts a narrower language. Every
frame must be a non-empty UTF-8 JSON **object** terminated by a bare LF within
the configured frame limit. CR/CRLF, surrounding or empty records,
unterminated EOF tails, arrays/scalars, duplicate object keys, and invalid
request/response/notification shapes fail the adapter transport instead of
being trimmed, normalized, or ignored. Thus `malformed-frame` behavior is an
adapter validation policy, not a claim that native transport is equally strict.

## Session events and unknown-event policy

`SessionEvent` is an immutable discriminated event-sourced union. Every event
carries a per-session monotonic `seq` and epoch-ms `time`. The core vocabulary
includes:

- `turn/start`, `turn/end { reason }`, `step/start`, and `step/end`;
- `user/message`, raw `assistant/chunk`, assembled `assistant/message` with
  optional usage, `tool/call`, and `tool/result`;
- `agent/inbox/spliced`, the durable pending-inbox mutation;
- log snapshots including `todo/write`, `request/header`, and
  `request/context`, plus `session/end-seed`;
- surface `append`/`replace` operations and `sourceEventSeqs` provenance.

For an unknown session-event type, `ignorable: true` means it may be retained as
an observed opaque record and skipped by the semantic reducer. Unknown events
without that exact marker are required: the adapter fails the affected session
and run closed rather than acknowledging past the event or silently
reconstructing incomplete state. Known events are still schema-validated;
`ignorable: true` does not excuse a malformed known event.

`agent/inbox/claimed { message, turn }` is a scoped, live Cordis runtime event,
not a `SessionEvent`. The SDK server subscribes to `session/event`, status, and
subagent lifecycle only, so **the selected wire never forwards
`agent/inbox/claimed`**. The durable claim operation instead appears as an
`agent/inbox/spliced` pure deletion, without the removed message in its payload.
A stateful projection can use that deletion to corroborate removal, but the
splice is not by itself turn ownership evidence.

## Identity and admission

| Native identity or observation | OAP treatment | Frozen rule |
|---|---|---|
| SDK process | endpoint/participant identity | Adapter allocates; `serverInfo` identifies the pinned runtime family. |
| caller `sessionId` | `session_id` association | Lazy native agent/session creation; adapter permits one foreground submission at a time per idle session. |
| prompt result `messageId` | `submission_id` input | Accepted/enqueued user-message identity only. It is neither `run_id` nor a standalone durability guarantee. |
| matching entered turn | `run_id` | Adapter allocates only after the retrospective ownership proof below. |
| session-event `seq` | native ordering evidence | Monotonic per session; OAP run sequence remains adapter-owned. |
| native `turn`/`step` | private lifecycle attribution | Never reused as globally scoped OAP identity. |
| `callId` | `tool_call_id` | Pairs `tool/call` and `tool/result` within the owned run. |
| child session/agent ids | child identities | Parent/child notifications require explicit correlation and settlement. |

`session/prompt` creates a `UserMessage`, and `followup()` synchronously appends
an `agent/inbox/spliced` insertion before the server returns its `messageId`.
The receipt therefore proves that this live agent accepted and enqueued that
identified message. It does **not** prove that storage has independently flushed
to durable media, that a model turn has begun, or that later activity belongs to
that message. The ledger consequently makes no generic "durable receipt" claim.

### Owned start admission

The adapter enforces one idle, non-overlapping outstanding submission per OAP
session. Native overlapping prompts remain possible but are outside the
advertised contract. For the accepted prompt:

1. remember the returned `messageId` and the observed inbox insertion;
2. buffer the next candidate `turn/start` and following native observations;
   do not expose a run yet;
3. require a later `step/start` for that same turn and a forwarded
   `user/message` in that step whose complete `message.id` equals the receipt
   and whose source is the direct user source (`{ kind: "user" }`);
4. only then allocate `run_id`, emit `run.started`, return
   `AdmissionStarted`, and replay buffered owned observations in original order.

This is retrospective ownership proof because native order is `turn/start`
**before** inbox claim and step entry. An earlier plan proposed
`agent/inbox/claimed -> owned turn/start`; that is invalid at this boundary:
the claimed event is unavailable on the wire and, even in process, follows
`turn/start`. A pure deletion `agent/inbox/spliced` may corroborate queue removal
when the adapter has the prior projection, but cannot replace the matching
`user/message` proof.

If interception, cancellation, pre-step rejection, an empty turn, malformed or
unknown-required evidence, transport loss, or any other path reaches a closing
boundary without the matching entered `user/message`, the adapter fails the
submission closed and emits no OAP run. A candidate turn must never be assigned
merely from adjacency, `session.status running`, or a deletion splice.

## Owned run lifecycle and settlement

| Observable native evidence | OAP meaning | Fidelity / policy |
|---|---|---|
| successful first `initialize` | endpoint ready with frozen descriptor | emulated freeze over mutable native method |
| prompt receipt plus synchronous insertion | submission accepted/enqueued | normalized; not started or independently durable |
| buffered `turn/start`, matching `step/start` + direct-user `user/message` | owned `run.started` and successful started admission | normalized, fail-closed proof |
| `assistant/chunk` | content delta | native token-level event |
| `assistant/message` | assembled output and optional usage | native |
| `tool/call` then `tool/result` | action requested then terminal | degraded; no native started/progress observation |
| `turn/end { reason: completed }` | successful terminal candidate | native candidate only |
| `turn/end { reason: max-tokens }` | failed terminal candidate | frozen adapter policy; never success |
| `turn/end { reason: aborted | blocked | error | interrupted }` | failed terminal candidate | normalized failure; SDK has no OAP cancel claim |
| later `session.status idle` | whole-agent quiescence corroboration | required after the owned `turn/end` |
| owned child `subagent.finished` | child terminal evidence | every child started under the run must settle |
| transport close before authority | one synthesized `run.failed` | adapter failure path |

`session.status running` is provisional whole-agent state, not ownership or
start evidence. `turn/end` is the per-turn outcome candidate, but the adapter
withholds the single OAP terminal until (a) the owned turn ended, (b) a later
`session.status idle` was observed, and (c) every child attributed to that run
has a matching `subagent.finished`. Child settlement may arrive before or after
the parent turn end; identity, not ordering, joins it. EOF/process exit or
shutdown failure before all three conditions produces exactly one failed
terminal and settles open child projections consistently.

Although the native subagent server has an option that can map `max-tokens` to
`ok`, the adapter never enables or inherits that deployment choice as OAP
success. Parent `turn/end { reason: { kind: "max-tokens" } }` and child
`subagent.finished { stopReason: "max-tokens" }` are failures: truncated output
is not successful completion.

Synthetic `user/message` values from `agent.inject()` have distinct source
metadata and cannot satisfy admission ownership. Later same-turn synthetic
context may be represented only after the direct-user match has established the
run.

## State, recovery, and unsupported controls

The Harness session subsystem has persisted event logs, seed boundaries, and
surface provenance, but the selected SDK wire has no query, cursor, resume, or
replay method. Internal persistence therefore supports the synchronous inbox
observation and native implementation semantics; it does not create an OAP
wire-recovery capability. OAP replay is limited to any explicitly bounded
adapter journal and is degraded, not native reconstruction.

There is no native cancellation request at this boundary. OAP cancellation is
`unavailable`; killing the process is transport failure, not cancellation.
There is likewise no selected-wire steer, queue-control, interaction/permission,
model-catalog, transcript, fork, or session-resume operation.

## Final advertised capability matrix

This matrix is exhaustive for the initial adapter descriptor. Anything absent is
unavailable.

| Capability | Level | Exact boundary claim |
|---|---|---|
| initialize / descriptor revision | `emulated` | adapter one-shot freeze; native initialize is repeatable and mutable |
| session association | `emulated` | caller id plus lazy native creation; one idle outstanding submission enforced |
| submission admission | `degraded` | accepted/enqueued receipt, withheld until matching entered-message start proof |
| run identity, status, sequence | `emulated` | adapter allocated after retrospective ownership proof |
| text streaming and assembled output | `native` | owned session events after admission proof |
| usage | `native` when present | carried by `assistant/message`; absence remains unknown |
| tool lifecycle | `degraded` | call/result only; no native started or progress event |
| child/subagent lifecycle | `degraded` | local children only; terminal waits for owned child settlement |
| reconciliation | `degraded` | live status corroboration only; no query/snapshot request |
| replay/resume | `degraded` | bounded adapter journal only; no native SDK replay/resume |
| cancellation | `unavailable` | no SDK request; teardown is not cancel |
| delivery `queue`, `steer`, or `btw` | `unavailable` | overlapping native followups are deliberately outside the contract |
| interactions / permissions | `unavailable` | no reverse interaction channel on selected wire |
| model catalog or model change | `unavailable` | provider/model frozen at adapter initialization |
| transcript reconstruction | `unavailable` | internal session persistence is not exposed on SDK wire |
| fork / branch / session replacement | `unavailable` | no selected-wire operations |

## Required evidence corpus

`fixtures/adapters/deepseek-harness-47f9438/` will use the standard five-file
case layout. The manifest and every case must pin the commit, tree, and source
blobs in this ledger. Required cases are:

- `initialize-minimal`, `initialize-repeat-rejected`, and
  `initialize-before-prompt`;
- `message-enqueued`, `owned-start`, `overlap-rejected`,
  `intercepted-no-run`, `blocked-no-run`, and `empty-no-run`;
- `completed-turn`, `max-tokens-failed`, `turn-end-reasons`,
  `streaming-chunks`, `tool-lifecycle`, `tool-failed`, and `injected-origin`;
- `subagent-run`, `child-after-turn-end`, `settlement`, `process-exit`, and
  `shutdown`;
- `unknown-ignorable`, `unknown-required`, `native-tolerant-frame`, and
  `adapter-strict-frame`;
- `no-native-cancel`, `no-wire-claim`, and `no-implied-replay`.

A gated live-process test may drive a caller-supplied executable over stdio with
a hermetic provider. It must not download at test time, and runtime version text
alone cannot prove this source pin; strong artifact identity requires a
caller-supplied digest or equivalently pinned build provenance.

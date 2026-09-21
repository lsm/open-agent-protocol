# DeepSeek Harness 47f9438 mapping ledger

Status: implemented pinned contract for the DeepSeek Harness OAP adapter.
This ledger freezes the selected JSON-RPC boundary and the conservative adapter
projection. It is an implementation input, not a claim about other Harness
surfaces.

Implementation status:

- `adapter/deepseek/internal/native` + `internal/rpc`: strict pinned wire
  vocabulary and JSON-RPC transport (commit `0ab56a0`), hardened by confirmed
  review findings (commit `ec231fe`): the initialize response now barriers
  behind every earlier observation and any pre-handshake observation fails the
  handshake closed; the pinned stream-chunk union, per-variant content-block
  members and exclusivity, structured image attachments, tool-result block
  correlation, `EpochHeader` identity, per-kind message-source provenance,
  structured turn-end causes, and the `maxTokens` safe-integer bound are all
  enforced; whitespace-surrounded frames are rejected per the frozen strictness
  policy; teardown closes the stderr read side so a leaked descendant cannot
  hang `Close`; writes stop at logical shutdown and the write pump drains.
- `adapter/deepseek` public adapter/reducer (commit `64da496`): reservation,
  receipt, buffered candidate turns, retrospective direct-user ownership proof,
  child-before-parent settlement, and terminal arbitration per this ledger.
- Gated real-process tests (commits `670d464`, `7dbd7c4`) drive a caller
  supplied runtime over the exact composition shapes pinned in
  `examples/jsonrpc-agent/*.cordis.yml` (top-level sequence of `id`/`name`/
  `config` rows).
- Executable evidence corpus: `fixtures/adapters/deepseek-harness-47f9438`
  driven by `adapter/deepseek/corpus_test.go`.
- Second independent review (reducer, corpus, transport, gates) confirmed six
  defects, each reproduced before and green after its focused fix:
  - `c3dc09b` admission semantics — a pre-receipt `subagent.finished` failed
    its own deferred `subagent.started`'s submission and the late reply then
    re-admitted the aborted run, wedging the session at `running` with no
    live run; the live and retrospective ownership proofs disagreed on
    later-step entered messages (the retrospective path recorded the first
    step's number and legal step-2 events then failed `sameStep`); and
    caller cancellation between reply and proof left the reservation
    permanently active. Pre-receipt finishes now defer like starts and
    reconcile at the admission replay, `evaluateAdmission` no-ops on
    terminal runs, both proof paths re-open the step window per same-turn
    `step/start` and record the step containing the entered message, and
    cancellation releases an unresolved reservation (a resolved admission
    stays authoritative).
  - `7b2209b` shutdown delivery — `Session.Close` stopped the dispatch loop
    before the shutdown round-trip, so the response's ordering barrier was
    never acknowledged, the call never delivered its result, and Close
    success was derived from exit status. Dispatch now stays alive through
    the handshake and `Process.Close` drains the inbound stream itself
    while tearing down.
  - `d363cad` child environment — an explicit empty `Config.Environment`
    was collapsed to nil and silently inherited the full ambient
    environment; nil-ness is now preserved (unset inherits, any explicit
    slice replaces verbatim).
  - `01625e9` reducer hygiene — write-only runState bookkeeping removed and
    settled runs pruned from the session registry.
  - `6831507` corpus determinism — the `unknown-events` case sent its
    invalid observation without first settling the second submission; the
    reply-vs-teardown race admitted or aborted nondeterministically. The
    missing `wait-submit` barrier is restored.
  - One speculative finding (the rpc handshake select could misattribute a
    post-response observation) is unreachable at this pin and documented
    here without a code change; the remaining noted gaps (no pre-receipt
    subagent or later-step corpus fixtures — both now covered by reducer
    regression tests; `initialize-repeat-rejected` evidenced as one
    initialize for N prompts rather than a rejection path; no
    golden-visible `in_reply_to` linkage) are accepted as recorded.

## Provenance

- Repository: `https://github.com/deepseek-ai/deepseek-harness`
- Implementation target: release `dsh-v0.1.5-rc.2`
- Tag commit: `fb2c4b9e698e30edb738bca4cf0618587db7d203`
  (`Merge pull request #3978 from deepseek-harness/worktree/release-dsh-0.1.5-rc.2`)
- Tag date: 2026-09-10
- Tag commit tree: `bd7dd6d90010a35d3d6ff9f12c1f6207d5b6fe38`
- Prior pin, retained below as superseded evidence:
  `47f943859bef60e4160492346772ded9b24f765a` (2026-08-13, tree
  `f904efab9ef435201d6ba4da88a34d6366568272`).

Reproduce the pin:

```sh
git clone https://github.com/deepseek-ai/deepseek-harness.git dsh
git -C dsh checkout fb2c4b9e698e30edb738bca4cf0618587db7d203
git -C dsh show -s --format='%H%n%s%n%T' HEAD
# fb2c4b9e698e30edb738bca4cf0618587db7d203
# Merge pull request #3978 from deepseek-harness/worktree/release-dsh-0.1.5-rc.2
# bd7dd6d90010a35d3d6ff9f12c1f6207d5b6fe38
```

Normative inspected sources and exact blobs at the 0.1.5-rc.2 pin:

| Source | Blob | Contract evidence |
|---|---|---|
| `packages/sdk/protocol/src/types.ts` | `605b97cc6945397563e6351e03ffde7a9436b23f` | request, result, and notification wire types; adds `reasoningEffort`, image blocks |
| `packages/sdk/protocol/src/transport.ts` | `36574f46bf3e34738be045408e25bb79932ff609` | native newline JSON-RPC transport (unchanged) |
| `packages/sdk/server/src/server.ts` | `1cc17059c9254c6bd4f809441bd9e43bc26a7d2d` | dispatch, initialize validation, lazy sessions, prompt receipt, notifications |
| `packages/core/session/src/types.ts` | `139fccd5a5660a8d0c4e1ef95f4e4d64b274230f` | durable session-event envelope; `assistant/attempt`, `assistant/message.stream` |
| `packages/core/session/src/known-event-types.ts` | `dd6411240b0527ec98d5ff51bcfb3e8b5f47e715` | recognized event vocabulary; `assistant/chunk` retired |
| `packages/core/agent/src/types.ts` | `d0be69ac58747a042ac937a250878705bcbf0d8f` | durable `agent/inbox/spliced` event |
| `packages/core/agent-loop/src/inbox.ts` | `db89cd3072677ebd6acbd40f7d496bab15c19cef` | synchronous append and claim deletion (moved from `core/agent/src/`) |
| `packages/core/agent/src/runtime-types.ts` | `31338e8d8da6ccb2e99fd459abbe2238bf5c1736` | runtime-only `agent/inbox/claimed` event |
| `packages/core/agent-loop/src/agent.ts` | `06e1f51b57277ba296698b6c8b810f0e455e3695` | status, turn ordering, step entry, attempt settlement, terminal reasons |
| `packages/llm/llm/src/message.ts` | `6f920fe0191d17c0a272fbc881eb7e37f8142815` | stable message identity and direct-user source shape |
| `packages/llm/llm/src/types.ts` | `bfddde7fc4b2a08144e2f76f8ca59e61a2b4e37f` | content, finish, and usage vocabulary |
| `packages/llm/llm/src/assistant-stream.ts` | `5d878020e8a2eab1a1a84d1867bf2923a409527a` | `AssistantStreamRecord` compact union (new) |
| `packages/core/session/src/invariant.ts` | `6ed0b6b3c5abf84dd4129281ed6029880c7e6ad3` | turn/step/call relational validation |
| `packages/core/session/src/surface.ts` | `5d8ce74fe2461cb2f777a7bc7556795f337f0c03` | append-surface message projection |

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
- `user/message`, assembled `assistant/message` with a compact `stream` and
  optional usage, evidence-only `assistant/attempt`, `tool/call`, and
  `tool/result`;
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
| `assistant/message.stream` | content delta | native; settlement-batched compact records |
| `assistant/attempt` | (no content projection) | evidence only; never model-visible |
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

## Live-gate findings (2026-09-10)

Both gates were run live against a runtime built from this exact pin
(`47f943859bef60e4160492346772ded9b24f765a`, tree
`f904efab9ef435201d6ba4da88a34d6366568272`) via the pinned build script
(`scripts/build-exe-for-python-sdk.ts`, single-file `dsh-jsonrpc-agent-pkg-linux-x64`).

### Fixed: the runtime numbers a session's events from zero

The reducer rejected the first native event of every session. The runtime's
per-session `seq` starts at **0** — the synchronous `agent/inbox/spliced`
insertion that precedes the prompt response — while the adapter's `lastSeq`
zero-initialized to `0` and rejected with `Seq <= lastSeq`. Every native event
of every real session would have failed the run with
`deepseek_invalid_sequence`. The hermetic corpus never caught it because every
recorded fixture was authored with `seq` starting at `1`.

Fix: `Session.seqSeen` separates "nothing observed yet" from a legitimate
`seq` of zero (`adapter/deepseek/session.go`). Regression tests
`TestNativeSequenceStartsAtZero` (fails before the fix) and
`TestNativeSequenceRegressionStillRejected` (guard still rejects a repeat)
pin both directions.

### Smoke gate: PASS

`OAP_DEEPSEEK_HARNESS_SMOKE=1` with the pinned runtime: credential-free
initialize, idle state, clean shutdown. Confirms the transport, the
`serverInfo` identity assertion, and teardown.

### Integration gate: turn does not complete — recorded, not yet resolved

`OAP_DEEPSEEK_HARNESS_INTEGRATION=1` now reaches owned admission and settlement
ordering (the `seq=0` fix), then stalls: the runtime emits
`agent/inbox/spliced(0)`, `session.status running`, `turn/start(1)`,
`agent/inbox/spliced(2)`, `step/start(3)`, `user/message(4)` and then **never
invokes the model**. The loopback mock records zero provider requests (and zero
connections: a deliberately dead `DEEPSEEK_BASE_URL` produces no error either,
so no call is attempted).

Reproduced across: the gate's synthetic `dsh-llm-pi-ai`/`openai-responses`
composition; the gate composition plus persistence/checkpoint plugins; the
repo's own `examples/jsonrpc-agent/minimal.cordis.yml`; and the bundled
`python/sdk-runtime/.../runtime/cordis.yml` — and with and without the gate's
proxy variables, and with `DEEPSEEK_BASE_URL` at both the mock root and
`/v1`. The stall is upstream of the adapter (admission and correlation behave
correctly); the pinned runtime's model-invocation path is not reached in any
composition exercised here. Not an adapter defect. Follow-up needed to
determine the required driver step or carrier before this gate can assert a
full turn. The gate remains skip-by-default and CI-safe.

## Re-pin to 0.1.5-rc.2 (2026-09-10)

Eleven of the thirteen previously pinned sources changed, one moved, and the
streaming wire changed shape. Findings that alter the adapter contract:

1. **`assistant/chunk` is retired; streaming settles per attempt.** The
   recognized vocabulary drops `assistant/chunk` and adds `assistant/attempt`
   (`{turn, step, stream: AssistantStreamRecord[]}`). `assistant/message` gains
   a required `stream` member of the same type. `AssistantStreamRecord` is a
   lossless compact union of packed delta runs — `text-chunks` /
   `reasoning-chunks` (`time0`, `index`, `dt[]`, `texts[]`),
   `tool-call-chunks` (`time0`, `index`, `dt[]`, `id`, `name?`, `args[]`), and
   a raw `chunk` for every other `StreamChunk` — appended once when the
   attempt settles (`packages/core/agent-loop/src/agent.ts`). The SDK server
   forwards durable `session/event` frames only, so **there is no live
   per-token event on this boundary**; the adapter derives deltas from the
   settled attempt/message stream. `run.streaming` is therefore genuinely
   "native but settlement-batched", not live-token streaming.
2. **`initialize` is now stateful and validated.** `reasoningEffort`
   (optional non-empty string) joins `maxTokens`; the server resolves the
   provider/model/reasoning route up front and requires initialize before
   `session/prompt` ("SDK server is not initialized"). The adapter's one-shot
   initialize remains correct; it may now surface a route error at open.
3. **`session/prompt` accepts inline images.** `contentBlocks` is now
   `ContentBlock | SdkEncodedImageBlock` (`{type:'image', data, mimeType}`),
   admitted into the runtime's attachment store. The adapter's text-only
   surface is unchanged; an image block from an OAP caller stays unsupported.
4. **New durable event vocabulary** beyond the chunk rename:
   `deliverables/presented`, `feedback/message-put`, `feedback/message-delete`,
   `model/selection`, `session-log-deepseek/delivery-accepted`,
   `tool/ptc-dispatch`, `tool/ptc-dispatch-start`, plus the subagent/team/
   tool-workflow families. Unknown-event policy is unchanged: `ignorable`
   still gates omission.
5. **Source relocation:** `core/agent/src/inbox.ts` moved to
   `core/agent-loop/src/inbox.ts`; the `agent/inbox/spliced` event and the
   synchronous append/claim semantics the admission proof relies on are
   unchanged.

The adapter, its corpus, and the process gates were re-pinned accordingly.

## Launch contract change at 0.1.5-rc.2

The process boundary changed and the adapter/gate were updated to match:

- **Positional cordis.yml is gone.** The runtime is now a profile launcher:
  `dsh --profile <name>` boots "an ordered stack of plugin-bundle patch
  layers" under `$DSH_HOME/profiles`, with repeatable `--patch <path>`
  overlays. The pinned SDK boundary is the shipped **`sdk` profile** —
  `@deepseek-ai/dsh-base` patched by `@deepseek-ai/dsh-sdk-app`, which mounts
  `dsh-sdk-jsonrpc-server` (its `cordis.patch.yml` states "Stdout belongs
  exclusively to JSON-RPC"). Verified live: `--profile sdk` answers
  `initialize` with `serverInfo.name = deepseek-harness-sdk-runtime`,
  `version 0.0.1` — unchanged.
- **`$DSH_HOME` is now part of the boundary.** The gate isolates it, along
  with `DSH_CWD` and `DSH_SESSION_ROOT`.
- **Loopback redirection works through the stock provider.** The built-in
  deepseek adapter reads `DEEPSEEK_BASE_URL`/`DEEPSEEK_API_KEY`; setting
  `DEEPSEEK_BASE_URL` to the loopback mock makes the runtime POST
  `/v1/chat/completions`. This is the vendor's own keyless-smoke recipe. It
  replaces the old `dsh-llm-pi-ai` composition, which the runtime never drove.
- **Artifact renamed.** The build now emits
  `deepseek-harness-sdk-runtime-linux-x64` (plus a `-rg` sibling) instead of
  `dsh-jsonrpc-agent-pkg-linux-x64`.

With the new profile launch, **the integration gate completes a full turn**:
initialize, admission, streamed content deltas, `assistant/message`,
`turn/end {completed}`, idle — the model-invocation stall recorded against the
previous pin does not reproduce at 0.1.5-rc.2.

## Codec corrections found by the live gate (2026-09-11)

The re-pin's hand-authored corpus and the production codec had diverged from
the pinned runtime. Because the Go transport strictly decodes every inbound
notification, the first divergence aborted the process rather than surfacing as
a decode error: the frame after `step/start` is a `system/message`
observed-only event carrying `surfaceOp:"append"`, which the codec rejected as
"surface metadata on non-surface event". The decode error closed the client
pipes, the runtime's next notification died on `EPIPE`, and the child exited 1
— so the mock saw zero requests and the gate read as a stall. Corrected:

1. **Observed-only events tolerate surface metadata.** The pin attaches
   surface coordinates (`append` and `{op:"replace",startSeq,endSeq}`) to
   events that are never projected. `Event.Validate` now returns after the
   envelope check for any type in the pinned vocabulary (`ObservedOnly`), and
   the reducer's default branch tolerates the same set instead of failing the
   run with `deepseek_unknown_event`.
2. **`dt` are gaps, not members.** `validateRun` in
   `packages/llm/llm/src/assistant-stream.ts` requires
   `len(dt) == len(texts) - 1` for `text-chunks`/`reasoning-chunks` and
   `len(dt) == len(args) - 1` for `tool-call-chunks`. The codec had required
   equality. `dt` is never consumed by the reducer, so no OAP mapping changed,
   but the corpus was not byte-faithful to the harness.
3. **Tool snapshots key `parameters`, not `input_schema`.** The runtime's
   `epoch/header` tool snapshot uses a different schema field name than OAP's
   own type; strict decoding rejected every tool snapshot.
4. **`TokenUsage` carries `totalTokens`.** The runtime emits it; strict
   decoding rejected it. It is now an optional, evidence-only field.
5. **The gate asserted with the wrong content reader.** `Content.Parts()`
   cannot succeed on the lone-text-part shape that both the adapter and the
   corpus canonicalize to a bare string; the gate now uses `Content.Text()`.
   This was a harness bug, not an adapter defect.

Outcome: both gates PASS at `fb2c4b9e69`, and the DeepSeek package, its
corpus, and the repository acceptance run are green under Go 1.27.

## What the Zig port does not validate (2026-09-21)

`zig/src/adapter/deepseek/` reproduces the reducer and the line codec. It does
not reproduce `internal/native`'s payload validation, and that absence is a
property of the port's failure mode, not only a gap in its coverage.

The Go adapter refuses at two layers. `rpc` refuses a frame that does not
decode; `native` refuses a frame whose payload fails a predicate. `Event.Validate`
requires a tool call to carry a non-empty `callId` and `name`, `validBlock`
requires the same of a `tool-call` content block, and `validUsage` refuses a
negative token count. The port carries the first layer and none of the second,
so a frame the oracle rejects before a session sees it reaches the reducer here
and is projected. Three reachable consequences:

| native member | where the oracle refuses | what the port emits |
| --- | --- | --- |
| tool call `name` | `Event.Validate`, `validBlock` | `action.call.requested` and `.started` carrying `"name": ""`, which the schema declares `nonEmptyString` |
| `usage.inputTokens`, `usage.outputTokens` | `validUsage` | `"input_tokens": -5`, which the schema declares `minimum: 0` |
| tool call `callId` | `Event.Validate` | nothing observable: the emitted `tool_call_id` is minted, so only the internal key is empty |

None of these is patched in the reducer. The oracle refuses them before a run
exists, so there is no `run.failed` to reproduce, and a reducer-level guard
would invent a terminal the oracle never emits. Matching the oracle includes
matching where it refuses, not only what it emits. Tracked as #143.

The direction of the gap is worth recording because it is the opposite of the
pi port's. pi carries a hand-written member validator, so its defects have been
over-strictness: it refused a `toolcall_end` whose nested `toolCall` was `null`,
and refused again when that object was merely incomplete, where the oracle
decodes both into a zero-valued `wireToolCallContent` without error. This port
cannot fail that way, because it asserts nothing about a payload's member set.
A port's failure modes follow from which of the oracle's layers it reproduced.

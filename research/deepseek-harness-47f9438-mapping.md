# DeepSeek Harness 47f9438 mapping ledger

Status: pinned evidence boundary for the eighth production OAP adapter
candidate. This is an implementation input, not an interoperability claim.
No adapter code exists yet.

## Provenance

- Repository: `https://github.com/deepseek-ai/deepseek-harness`
- Implementation target commit: `47f943859bef60e4160492346772ded9b24f765a`
  (2026-08-13, "feat/npm-public")
- Target commit tree: `f904efab9ef435201d6ba4da88a34d6366568272`
- Newer inspected candidate: `82a5fd61a7cf5c293cec4bdff68f455398d685e9`
  (2026-09-07, release `0.1.3-alpha.2`) — design signal only; the target
  commit above is normative.

Reproduce the pin:

```sh
git clone https://github.com/deepseek-ai/deepseek-harness.git dsh
git -C dsh checkout 47f943859bef60e4160492346772ded9b24f765a
git -C dsh rev-parse HEAD 'HEAD^{tree}'  # 47f9438..., f904efa...
```

The project is a Cordis plugin runtime ("everything is a plugin") in
developer preview with declared compatibility-breaking changes; pinning is
therefore mandatory before any adapter work.

Normative inspected sources:

- `packages/sdk/protocol/src/types.ts` — the SDK runtime wire types
- `packages/sdk/protocol/src/transport.ts` — newline-delimited JSON-RPC 2.0
  framing over byte streams
- `packages/sdk/server/src/server.ts` — method dispatch, lazy session
  creation, teardown
- `packages/core/session/src/types.ts` — the event-sourced session log

## Boundary selection

The adapter boundary is the **SDK JSON-RPC runtime server**
(`@deepseek-ai/dsh-sdk-jsonrpc-server`): newline-delimited JSON-RPC 2.0 over
stdio. The web app, ACP adapter package (`packages/acp`), CLI, and the
Typert gateway RPC (`packages/api`) are separate surfaces with different
fidelities; they are not this ledger's boundary.

## Wire protocol

Frames with `id`+`method` are requests, `id` alone responses, `method`
alone notifications; malformed lines are ignored; handler failures become
error frames; missing handlers return `-32601`, failures `-32603`.

**Requests (3):**

- `initialize { cwd, provider, model, maxTokens? }` →
  `{ serverInfo: { name: "deepseek-harness-sdk-runtime", version } }` —
  a real process-wide handshake
- `session/prompt { sessionId, contentBlocks }` → `{ messageId }` —
  documented as a **durable enqueue receipt**; an unknown `sessionId`
  lazily creates the agent+session pair (creation de-duplicated through a
  pending-creation map)
- `shutdown` → `{}` — awaits pending session creations, disposes sessions
  and the LLM fiber

**Notifications (4):**

- `session.event { sessionId, event: SessionEvent }` — every session-log
  event in the runtime, streamed as recorded
- `session.status { sessionId, status: "idle" | "running" }` — whole-agent
  state
- `subagent.started { parentSessionId, childSessionId }`
- `subagent.finished { provider, agentId, parentSessionId, childSessionId,
  status: "ok" | "error", stopReason, lastAssistantMessage? }` — local
  subagents only

There is no SDK cancel, steer, queue, interaction, or replay request at
this pin: the protocol is deliberately minimal.

## Session log

`SessionEvent` is an immutable, discriminated, event-sourced union — the
cleanest native event model of the eight pinned harnesses:

- every event carries `seq` (monotonic within the session) and `time`
  (epoch ms);
- `turn/start { turn }`, `turn/end { turn, reason: TurnEndReason }`,
  `step/start { turn, step }`, `step/end { turn, step }` — a two-level
  turn/step lifecycle (step = one model call plus its tool executions);
- `user/message` with `source` distinguishing human prompts, synthetic
  `agent.inject()` context (file-change notices, AGENTS.md, skills, cron),
  and goal-continuation rounds — **native origin attribution**;
- `assistant/chunk { turn, step, chunk }` — raw stream chunks at
  "token-level replay fidelity";
- `assistant/message` — the assembled message plus the step's usage;
- `tool/call { callId, name, arguments }` with raw unparsed arguments;
- `tool/result { message, error?, meta? }` — error is a typed
  `{ name, code }`, meta is tool-private JSON;
- `todo/write`, `request/header`, `request/context` — log-only snapshots;
- `session/end-seed` — a durable marker separating seed history (resume,
  fork, replay) from live events, the durable projection of
  `Session.firstLiveSeq`;
- surface events (`user/message`, `assistant/message`, `tool/result`)
  carry `surfaceOp: 'append' | { op: 'replace', start, end }` (compaction
  replaces surface ranges) and `sourceEventSeqs` provenance;
- an `ignorable?: true` marker lets readers skip unrecognized purely
  informational events, while an unrecognized **required** event makes the
  reader refuse reconstruction rather than silently drop it — an explicit
  forward-compatibility contract OAP should study.

## Identity domains

| Native identity | OAP identity | Rule |
|---|---|---|
| SDK process | endpoint | Adapter allocates; `serverInfo` identifies the runtime. |
| SDK-side `sessionId` | `session_id` | Caller-chosen; lazily creates the agent+session pair. |
| `messageId` (prompt receipt) | `submission_id` candidate | Native durable enqueue identity. |
| one turn (`turn/start`..`turn/end`) | `run_id` | Adapter allocates; native turn number is per-session. |
| session-log `seq` | native ordering evidence | Monotonic per session; OAP per-run sequence derived. |
| `turn` / `step` numbers | turn/step attribution | Nested lifecycle addressing. |
| `callId` | `tool_call_id` | Pairs `tool/call` with `tool/result`. |
| `childSessionId` / `agentId` | child identities | Subagent notifications correlate parent and child. |
| `sourceEventSeqs` | provenance | Citation chains; no OAP equivalent yet. |

## Lifecycle mapping

| Native observation | OAP meaning | Fidelity | Initial support | Required fixture |
|---|---|---|---|---|
| `initialize` handshake | initialize response | native | emulated descriptor | `initialize-minimal` |
| `session/prompt` -> `{messageId}` | admission (durable enqueue) | native | native | `message-admitted` |
| lazy session creation | association on first use | normalized | emulated | `lazy-session-create` |
| `session.status` `running` | `run.started` corroboration | normalized | degraded | `run-start` |
| `turn/start` | run opens | native | native | `completed-turn` |
| `assistant/chunk` | `content.delta` (token fidelity) | native | native | `streaming-chunks` |
| `assistant/message` | assembled message + usage | native | native | `completed-turn` |
| `tool/call` -> `tool/result` | action requested/terminal (error flag) | native | degraded (no started/progress events) | `tool-lifecycle`, `tool-failed` |
| `user/message` synthetic (`agent.inject`) | injected context, not a run | native | attribution rule | `injected-origin` |
| `turn/end { reason }` | terminal candidate per turn | native | native | `turn-end-reasons` |
| `session.status` `idle` | settlement corroboration | native | native | `settlement` |
| `subagent.started`/`finished` | child lifecycle with outcome | native | native | `subagent-run` |
| `shutdown` request | graceful endpoint teardown | native | session retirement | `shutdown` |
| transport close before settlement | one `run.failed` | synthesized | transport failure handling | `process-exit` |
| malformed JSON-RPC line | ignored by transport | native | native (verify) | `malformed-frame` |

### Terminal arbitration

`turn/end { reason }` is the native per-turn terminal, and `session.status
idle` corroborates whole-agent quiescence. `subagent.finished` settles the
child before the parent's terminal is authoritative. No contradictory
duplicate-terminal family exists at this pin, making DeepSeek Harness the
simplest terminal arbitration of the eight boundaries — useful as the
reducer's baseline proof.

### Cancellation

No native cancel exists in the SDK protocol at this pin. OAP
`run.cancel` must be advertised `unavailable` (not emulated by killing the
process): transport teardown is not cancellation semantics.

## Session state and recovery

The session log is durable and replayable by construction (SQLite/session
persistence packages), and `session/end-seed` plus `surfaceOp`/`sourceEventSeqs`
form a native resume/fork/replay marker family — but the **SDK wire protocol
exposes no replay or resume request**. Durable reconstruction is internal;
the adapter initially claims nothing for replay and records the gap.

## P0 mismatches

1. **SDK surface is minimal:** 3 requests, 4 notifications; no cancel,
   steer, queue, interaction, model catalog, or replay on the wire. The
   adapter must advertise these unavailable rather than reach into
   non-SDK surfaces.
2. **Turn is not run:** native turns are nested (turn/step); OAP run
   identity is adapter-allocated and one prompt may span a queued turn.
3. **Tool lifecycle has no start/progress events** on the log (call then
   result); OAP action.started is synthesized if required.
4. **Origin attribution is native and load-bearing** (`user/message`
   `source`): the injected-context rule needs an OAP decision, same as
   Claude Code's `origin`.
5. **`ignorable` vs required unknown events:** a forward-compatibility
   contract OAP's validator should consider adopting for unknown
   observation kinds.
6. **Lazy session creation:** association happens implicitly on first
   prompt; OAP session.open maps to initialize + first-use, which must be
   documented.
7. **Developer-preview churn:** protocol may break; the pin and a
   capability revision keyed to it are mandatory.

## Initial capabilities

- initialize: `native` handshake, `emulated` descriptor
- session association: `emulated` (lazy)
- admission: `native` (durable enqueue receipt with `messageId`)
- run identity/sequence: `emulated`
- run status: `native` (`session.status`)
- text streaming: `native` (token-fidelity chunks)
- tool lifecycle: `degraded` (no started/progress)
- subagent settlement: `native` (notifications)
- cancellation: `unavailable`
- steer/queue/btw: `unavailable`
- interactions/permissions: `unavailable`
- replay/reconstruction over the SDK wire: `unavailable` (durable
  internally, not exposed)
- model catalog: `unavailable` (initialize pins one provider/model)
- reconciliation: `degraded` (status notifications only)

## Evidence corpus plan

`fixtures/adapters/deepseek-harness-47f9438/`, standard five-file cases:
`initialize-minimal`, `message-admitted`, `lazy-session-create`,
`run-start`, `completed-turn`, `turn-end-reasons`, `streaming-chunks`,
`tool-lifecycle`, `tool-failed`, `injected-origin`, `subagent-run`,
`settlement`, `shutdown`, `process-exit`, `malformed-frame`,
`no-native-cancel`.

A gated live-process test drives the real SDK server over stdio with a
hermetic provider adapter, following the established pattern.

# Pi coding agent v0.85.1 mapping ledger

Status: pinned evidence boundary for the fifth production OAP adapter
candidate (after Codex app-server, ACP, Makai, and the Claude Code wrapper).
This is an implementation input, not an interoperability claim. No adapter
code exists yet.

## Provenance

- Repository: `https://github.com/earendil-works/pi`
- Release: `v0.85.1`
- Commit: `d981de1229ef899957bbe968bc8dcda02a21f477`
- Commit tree: `346294a615d2d0ad4f6e5fbccb4cee4ccd7b2d6c`

Reproduce the pin:

```sh
git clone https://github.com/earendil-works/pi.git
git -C pi checkout d981de1229ef899957bbe968bc8dcda02a21f477
git -C pi rev-parse HEAD 'HEAD^{tree}'   # d981de1..., 346294a...
git -C pi describe --tags                # v0.85.1
```

Normative inspected sources:

- `packages/coding-agent/src/modes/rpc/rpc-types.ts` — command, response,
  and extension-UI wire types
- `packages/coding-agent/src/modes/rpc/rpc-mode.ts` — command dispatch,
  event forwarding, admission, shutdown
- `packages/coding-agent/src/rpc-entry.ts` and `src/cli/args.ts` — process
  boundary and flags
- `packages/coding-agent/src/core/agent-session.ts` — session API, prompt /
  steer / follow-up / abort semantics, `AgentSessionEvent`
- `packages/coding-agent/src/core/session-manager.ts` — append-only session
  tree persistence
- `packages/agent/src/types.ts` — `AgentEvent`, tool execution lifecycle,
  queue modes

## Boundary selection

Two integration forms were anticipated by the interoperability study; this
pin confirms both exist:

1. **RPC mode (chosen adapter boundary):** `pi --mode rpc` (`rpc-entry.ts`
   invokes `main(["--mode", "rpc", ...])`). Commands arrive as JSON lines on
   stdin; responses, agent events, and extension UI requests are JSON lines
   on stdout. This is a language-neutral process boundary directly analogous
   to `makai --stdio` and Claude Code stream-json.
2. **In-process (`AgentSession`/`Agent`):** higher fidelity (direct event
   subscription, tool hosting, model runtime) but TypeScript-bound; not the
   Go adapter boundary. It remains the reference for semantics the RPC mode
   projects.

The adapter implements the RPC process boundary and advertises only what it
proves there.

## Wire protocol

- **Commands (host -> Pi):** one JSON object per line, discriminated by
  `type`, with an optional caller-chosen `id` echoed on the response.
  Commands cover prompting (`prompt`, `steer`, `follow_up`, `abort`,
  `clear_queue`, `new_session`), state (`get_state`), model and thinking
  selection and catalogs, queue modes (`set_steering_mode`,
  `set_follow_up_mode`: `"all" | "one-at-a-time"`), compaction, retry, bash
  (`bash`, `abort_bash`), session tree operations (`switch_session`, `fork`,
  `clone`, `get_fork_messages`, `get_entries` with `since`, `get_tree`,
  `get_entries`), messages, and command discovery (`get_commands`).
- **Responses (Pi -> host):** `{ type: "response", command, success, data? }`
  correlated only by the echoed `id` and the `command` name. There is no
  per-command strict request/response ordering guarantee beyond emission
  order; events interleave freely with responses.
- **Events (Pi -> host):** every `AgentSessionEvent` serialized via
  `toJsonEvent` and streamed as it occurs (`rpc-mode.ts` rebinds
  `session.subscribe` on session switches).
- **Extension UI (bidirectional):** `extension_ui_request` frames
  (`select`, `confirm`, `input`, `editor`, `notify`, `setStatus`,
  `setWidget`, `setTitle`, `set_editor_text`) answered by
  `extension_ui_response` frames correlated by `id`, with optional timeouts.
  This is a native reverse interaction channel — richer than Makai's (none)
  and comparable in role to Claude Code's `can_use_tool`, though
  extension-scoped rather than permission-scoped.
- **Framing robustness:** line-oriented JSON over stdio with explicit raw
  stdout backpressure handling (`waitForRawStdoutBackpressure` subscribed to
  agent events); SIGTERM/SIGHUP trigger tracked-children teardown and
  numbered exit (143/129).

There are no sequence numbers and no capability negotiation. The first
exchanged frame is whatever the host sends; readiness is implicit.

## Identity domains

| Native identity | OAP identity | Rule |
|---|---|---|
| Pi process | endpoint and participant IDs | Adapter allocates typed identities. |
| `sessionId` (from `get_state`) | `session_id` association | Correlation identity; durable session file backs it but possession alone proves nothing. |
| `sessionFile` path | private | Persistence location, never an OAP identity. |
| echoed command `id` | private request correlation | Caller-minted; never a run or submission identity. |
| accepted `prompt` command | `submission_id` | Adapter allocates; RPC has no submission identity. |
| one `prompt` execution (to `agent_settled`) | `run_id` | Adapter allocates; distinct from session and command id. |
| `AgentMessage` (session entry) | transcript `message_id` | Entries have stable tree ids (`entry.id`). |
| `toolCallId` | `tool_call_id` | Namespaced by endpoint and session; stable across start/update/end. |
| session tree `entry.id` / `parentId` | fork/branch navigation keys | Private unless OAP adopts tree semantics. |
| `extension_ui_request` `id` | interaction correlation | Reverse-channel interaction identity. |

## Lifecycle mapping

| Native observation | OAP meaning | Fidelity | Initial support | Required fixture |
|---|---|---|---|---|
| process start (`--mode rpc`) | initialize response and descriptor | synthesized | emulated | `initialize-minimal` |
| `prompt` response (post-preflight success) | admission; allocate submission and run | normalized | emulated | `message-admitted` |
| `prompt` error response (preflight failure) | submission rejected | normalized | native | `message-rejected` |
| `agent_start` event | `run.started` | normalized | native | `completed-text` |
| `message_start/update/end` (assistant) | portable message lifecycle; `message_update` carries the provider stream event | normalized | native | `streaming-deltas` |
| `turn_start` / `turn_end` | internal turn diagnostics | observed-only | no core claim | `multi-turn-tools` |
| `tool_execution_start/update/end` | action requested/started, progress, terminal (by `isError`) | normalized | degraded until fixtures | `tool-completed`, `tool-failed`, `tool-progress` |
| parallel tool completion order | completion-order terminals, source-order artifacts | lossy | document ordering rule | `tool-parallel-order` |
| `queue_update` (steering/followUp lists) | queue state observation | normalized | maps to OAP queue/steer support | `steer-queued` |
| `steer` command + later injection | steer delivery | normalized | degraded | `steer-injected` |
| `follow_up` command | queued follow-up run | normalized | degraded | `follow-up-run` |
| `abort` command response (emitted after `waitForIdle`) | cancellation **settlement**, not mere intent | normalized | native post-settlement ack | `cancel-settled` |
| `agent_end` (`willRetry`) | terminal candidate unless retry pending | normalized | retry-aware arbitration | `error-retry` |
| `auto_retry_start` / `auto_retry_end` | retry lifecycle; terminal deferred | observed-only | degraded | `error-retry` |
| `agent_settled` | authoritative run settlement boundary | normalized | native | `completed-text` |
| `compaction_start` / `compaction_end` | context maintenance, not run lifecycle | observed-only | no core claim | `compaction` |
| `entry_appended` | persistence observation | observed-only | no core claim | — |
| `extension_ui_request`/`response` | interaction requested/resolved | normalized | degraded pending fixture | `extension-dialog` |
| `get_state` | adapter-assisted reconciliation | normalized | emulated | `reconcile-state` |
| `get_entries` (`since`) | transcript reconstruction from durable tree | normalized | degraded, distinct from replay | `entries-since` |
| `fork` / `clone` / `navigateTree` | branch semantics | native input | reclassified; no OAP core claim yet | `fork-tree` |
| `switch_session` / `new_session` | session replacement in one process | normalized | session-scope transition | `switch-session` |
| stdout EOF / process exit before settlement | settle children, one `run.failed` | synthesized | transport failure handling | `process-exit` |
| malformed JSON line on stdin | command rejected; process continues | normalized | transport validation | `malformed-command` |

### Terminal arbitration

- `agent_end` carries `willRetry`; a retry-pending end is not a terminal.
  Terminal candidates are `agent_end` with `willRetry: false`, followed by
  the authoritative `agent_settled`.
- `abort()` aborts retry, compaction, branch summary, and the agent, then
  awaits idle before its response is written — so the RPC abort response is
  itself settlement-corroborating. OAP still treats the `agent_settled` /
  final event evidence as terminal authority and the response as
  acknowledgement.
- Abort during compaction or branch summary produces
  `compaction_end { aborted: true }` — cancellation evidence outside the
  agent-event family the reducer must route through one arbiter.

## Steering, queueing, and concurrency

Pi has the richest delivery model of the pinned harnesses:

- `prompt` while streaming is legal and queues per the active mode;
- `steer` and `follow_up` are distinct typed deliveries with their own
  queues and modes (`all` vs `one-at-a-time`);
- `clear_queue` returns the drained queues;
- queue contents are observable via `queue_update` events and
  `get_state.pendingMessageCount`.

This is the first boundary that can exercise OAP `delivery: steer` and
`queue` truthfully rather than degrading them to unavailable. Fixture
coverage must pin: queued prompt admission response timing (success is
emitted for queued prompts too — admission ≠ execution start), steer
injection point (between turns, after tool calls complete), and
one-at-a-time vs all drain behavior.

## Session state and recovery

- Sessions are append-only JSONL trees under `~/.pi/agent/sessions/`
  (cwd-encoded path), entries typed `message | thinking_level_change |
  model_change | compaction | branchSummary | custom | customMessage |
  label | session_info`, each with `id`, `parentId`, `timestamp`
  (`session-manager.ts`).
- `get_entries` supports `since` — a genuine cursor-shaped transcript
  reconstruction primitive, but reconstruction, not event replay: there is
  no redelivery contract for the live stream.
- `fork` (`entryId`), `clone`, `switch_session`, `new_session
  { parentSession }` form the tree/branch family. Any OAP claim here needs
  an explicit extension decision; core OAP has no tree semantics.
- Corrupt/oversized session files are skipped during discovery without
  failing the scan (defensive persistence).

## P0 mismatches

1. **No capability negotiation:** descriptor is synthesized; `get_state`,
   `get_available_models`, and `get_commands` provide post-hoc truth.
2. **No native submission/run identity:** adapter allocates both; the RPC
   `id` is caller-private.
3. **Admission is post-preflight and queue-aware:** a success response means
   accepted-for-eventual-execution, not execution started; OAP admission
   mapping must state this explicitly (it aligns with OAP's
   admission/settlement split).
4. **Settlement is two-stage** (`agent_end` + `agent_settled`) and
   retry-complicated (`willRetry`); one terminal arbiter required.
5. **Abort acknowledgement is post-settlement** (awaited idle) — the
   opposite default from Claude Code; the adapter must not treat Pi's ack
   as intent-only, nor Claude's as settlement.
6. **Steering/follow-up queues are first-class** — OAP delivery modes can be
   native here, but queue-mode switches (`one-at-a-time`) are process-wide
   state, not per-run.
7. **Extension UI interactions are not permissions:** they are a general
   dialog surface; OAP interaction mapping must not mislabel them.
8. **Parallel tool completion order differs from result emission order** —
   the action reducer must key strictly on `toolCallId`, not ordering.
9. **No sequence numbers:** OAP sequence is adapter-owned.
10. **Session tree semantics exceed core OAP:** fork/navigate/branch need an
    explicit extension decision before any claim.

## Initial capabilities

- initialize / capability revision: `emulated`
- session association: `emulated` (process + `get_state`)
- submission/admission: `emulated` (post-preflight response)
- run identity/status/sequence: `emulated`
- one foreground run per session, plus native queueing: enforced locally
- text streaming: `native` (`message_update` with provider stream events)
- tool lifecycle and progress: `degraded` until fixtures pass
- delivery `auto`/`queue`/`steer`: `native` candidates (first adapter with
  truthful steer support), degraded until fixtures pass
- cancellation: `native` candidate (post-settlement ack), degraded until
  the race fixtures pass
- interactions: `degraded` (extension UI channel exists, is not
  permission-shaped)
- reconciliation: `emulated` (`get_state`)
- transcript reconstruction: `degraded` (`get_entries since`)
- event replay: `unavailable`
- fork/branch navigation: `unavailable` pending OAP extension decision
- model catalog: `degraded` (`get_available_models` exists, unexercised)
- compaction control, retry control, bash passthrough: observed-only, no
  core claim

## Evidence corpus plan

`fixtures/adapters/pi-v0.85.1/` with the standard five-file case shape,
covering at minimum: `initialize-minimal`, `message-admitted`,
`message-rejected`, `completed-text`, `streaming-deltas`,
`multi-turn-tools`, `tool-completed`, `tool-failed`, `tool-progress`,
`tool-parallel-order`, `steer-queued`, `steer-injected`, `follow-up-run`,
`cancel-settled`, `error-retry`, `compaction`, `extension-dialog`,
`reconcile-state`, `entries-since`, `switch-session`, `process-exit`,
`malformed-command`, `fork-tree`, `no-implied-replay`.

A gated live-process test against the real `pi --mode rpc` binary with a
hermetic provider follows the Makai integration-gate pattern; the standing
credential policy applies unchanged.

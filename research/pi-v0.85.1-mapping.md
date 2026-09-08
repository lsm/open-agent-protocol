# Pi coding agent v0.85.1 mapping ledger

Status: implemented fifth production OAP adapter (after Codex app-server,
ACP, Makai, and the Claude Code wrapper), pinned to the RPC evidence boundary.
The executable corpus below proves the documented projection; native surfaces
outside the advertised OAP v0.1 capabilities remain evidence, not support
claims.

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
  numbered exit (143/129). The adapter's production codec adopts a strict LF
  policy: every frame must end in LF, CR is rejected rather than accepted as
  CRLF, empty or unterminated frames fail, and UTF-8 plus the configured frame
  limit are checked before dispatch.
- **Discriminator facts:** ordinary inbound events are discriminated directly
  by their top-level `type`; `response` and `extension_ui_request` are distinct
  frame families. Persisted entries carry their own `type`, `id`, and
  `parentId`; notably the wire spellings include `branch_summary` and
  `custom_message`. `get_entries { since }` is cursor-shaped transcript
  reconstruction, not live-event replay. `entry_appended` is not a complete
  history feed at this pin; it is visibly emitted for extension custom-entry
  writes.
- **Known union gap:** the pinned TypeScript declared RPC union omits
  `extension_error`, although the pinned runtime emits that top-level frame
  when an extension callback fails. Production native types explicitly accept
  this observed runtime variant rather than mistaking it for an ordinary
  agent event.

There are no sequence numbers and no capability negotiation. The first
exchanged frame is whatever the host sends; readiness is implicit. The
adapter performs `get_state` as its readiness handshake and treats an idle
snapshot as provisional: native events or process failure may follow.

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
| successful `prompt` response, then `agent_start` | canonical admission; allocate submission and run only once started semantics are proven | normalized | emulated | `message-admitted` |
| `prompt` error response, slash command without an agent run, or accepted prompt with no later `agent_start` | rejected/failed pre-start submission; no canonical accepted run | normalized | native/degraded | `message-rejected` |
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

- Sessions are persisted as append-only JSONL trees under
  `~/.pi/agent/sessions/` (cwd-encoded path), with entry discriminators
  `message | thinking_level_change | model_change | compaction |
  branch_summary | custom | custom_message | label | session_info`; each entry
  has `id`, `parentId`, and `timestamp` (`session-manager.ts`). The live
  `entry_appended` event is not a comprehensive feed of those writes.
- `get_entries` supports `since` — a genuine cursor-shaped transcript
  reconstruction primitive, but reconstruction, not event replay: there is
  no redelivery contract for the live stream.
- `fork` (`entryId`), `clone`, `switch_session`, `new_session
  { parentSession }` form the tree/branch family. Any OAP claim here needs
  an explicit extension decision; core OAP has no tree semantics.
- Corrupt/oversized session files are skipped during discovery without
  failing the scan (defensive persistence).

## P0 mismatches and implemented policy

1. **No capability negotiation:** the descriptor is synthesized; `get_state`,
   `get_available_models`, and `get_commands` provide post-hoc truth.
2. **No native submission/run identity:** the adapter allocates both; the RPC
   `id` is caller-private.
3. **Native prompt success is not canonical OAP acceptance:** Pi can report
   success for queued prompts, slash commands, and extension-consumed input
   without starting an agent. OAP v0.1 canonical accepted submissions require
   started semantics, so the adapter withholds success until `agent_start`.
   A no-agent/slash path fails pre-start rather than fabricating a run.
4. **Production extensions are disabled:** the spawned process always receives
   `--no-extensions`, explicit extension flags are rejected, and the descriptor
   advertises `InteractiveGates=false`. The corpus extension-dialog case uses
   an injected client solely as reducer evidence and is explicitly
   noncanonical; extension UI is neither a production capability nor a
   permission claim.
5. **Settlement is two-stage** (`agent_end` + `agent_settled`) and
   retry-complicated (`willRetry`); one terminal arbiter is used.
6. **Cancellation before `agent_start`:** local abort intent is retained across
   the pre-start boundary. A late `agent_start` is immediately followed by the
   native abort, and no successful admission is exposed merely because Pi had
   acknowledged the prompt.
7. **Abort acknowledgement is post-idle natively**, but the adapter still uses
   `agent_settled` as terminal authority and handles completion/cancellation
   races through one arbiter.
8. **Steering/follow-up queues are first-class natively but unavailable in the
   current OAP surface:** the codec corpus records them; it does not imply an
   advertised delivery claim. `auto` alone is exposed for submission.
9. **Parallel tool completion order differs from result emission order** —
   action state is keyed strictly on `toolCallId`, not ordering.
10. **No sequence numbers:** OAP sequence is adapter-owned and replay is only
    the bounded adapter journal. `get_entries since` does not imply replay.
11. **Session tree and process session replacement exceed core OAP:** fork,
    navigation, `switch_session`, and `new_session` remain codec evidence only.
12. **Provisional idle and transport failure:** initial/get-state idle is a
    reconciliation observation, not settlement. EOF/process exit before
    authoritative settlement produces one synthesized `run.failed`.

## Advertised capabilities

- initialize / capability revision: `emulated`
- session association and state: `emulated` (process plus `get_state`)
- submission/admission: `emulated`; native prompt success is held until
  `agent_start`
- run identity/status/sequence: `emulated`
- one foreground run per OAP session: enforced locally
- text streaming: `native` (`message_update` provider stream events)
- tool lifecycle/progress: `degraded` observed lifecycle; Pi owns execution
- delivery `auto`: `emulated`; `queue` and `steer`: `unavailable`
- cancellation: `degraded`; native abort with `agent_settled` authority
- interactions and permissions: `unavailable`; production extensions disabled
  and `InteractiveGates=false`
- reconciliation: `emulated` (`get_state`)
- run resume/replay: `degraded`, bounded process-memory OAP journal only
- transcript reconstruction, fork/tree navigation, and session switching:
  `unavailable` at the OAP boundary despite native codec evidence
- compaction, retry, queue controls, and bash passthrough: observed-only, no
  core claim

## Executable evidence corpus

`fixtures/adapters/pi-v0.85.1/` contains ten compact cases, each with exactly
`case.json`, `native.jsonl`, `mapping.json`, `omissions.json`, and
`expected-oap.json`. The manifest and every case pin the tag, commit, tree, and
seven inspected source blobs. Strict inventory checks reject unlisted files;
classification checks account for every native line; canonical cases execute
through the production decoder/reducer and validate exact OAP traces, while
codec-only or injected-extension mismatches are named explicitly. Golden trace
updates require `OAP_UPDATE_PI_CORPUS=1`.

The ten representative cases cover each of the 24 ledger fixture labels
exactly once. `message-rejected` is now a distinct executable case driven
through `Session.Submit`; native-control outcome labels require matching decoded
responses or queue observations, while unsupported controls remain explicitly
command/codec evidence rather than advertised OAP execution support:
`initialize-minimal`, `message-admitted`, `message-rejected`,
`completed-text`, `streaming-deltas`, `multi-turn-tools`, `tool-completed`,
`tool-failed`, `tool-progress`, `tool-parallel-order`, `steer-queued`,
`steer-injected`, `follow-up-run`, `cancel-settled`, `error-retry`,
`compaction`, `extension-dialog`, `reconcile-state`, `entries-since`,
`switch-session`, `process-exit`, `malformed-command`, `fork-tree`, and
`no-implied-replay`.

Separately gated real-process tests use a caller-supplied Pi v0.85.1 executable:
`OAP_PI_SMOKE=1` exercises startup/readiness without credentials, while
`OAP_PI_INTEGRATION=1` exercises a complete response against a hermetic loopback
provider. Both require an absolute `OAP_PI_BIN`, remain skipped in ordinary test
and CI runs, perform no download, and assert clean session shutdown. The checked
`--version` is runtime-version evidence only and does not prove `PinnedCommit`;
when strong artifact identity is needed, `OAP_PI_SHA256` must supply the expected
SHA-256 digest and the gate verifies it before execution.

The pinned ledger records the extension wire protocol and disabling flag, but it
does not pin the extension authoring/discovery API needed to construct a safe,
deterministic no-agent fixture. The real-process gate therefore does not claim
to prove extension discovery behavior. Production `--no-extensions` enforcement
and rejection of caller extension flags are instead unit-tested at the adapter
argv/config boundary.

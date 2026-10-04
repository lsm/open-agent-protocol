# Pi coding agent v1.0.1 mapping ledger

Status: implemented production OAP adapter, re-pinned from v0.87.1
(`pi-v0.87.1-mapping.md`, now retired) to v1.0.1. Everything below is carried
from that ledger except where "Changes from v0.87.1" says otherwise.
The executable corpus below proves the documented projection; native surfaces
outside the advertised OAP v0.1 capabilities remain evidence, not support
claims.

## Provenance

- Repository: `https://github.com/earendil-works/pi`
- Release: `v1.0.1` (published 2026-10-03)
- Commit: `a7229ddc21810d6245105978033b7df645ecc2f7`
- Commit tree: `e4f6b2120e4fefc5b464fab0451e16366c921490`

Reproduce the pin:

```sh
git clone https://github.com/earendil-works/pi.git
git -C pi checkout a7229ddc21810d6245105978033b7df645ecc2f7
git -C pi rev-parse HEAD 'HEAD^{tree}'   # a7229dd..., e4f6b21...
git -C pi describe --tags                # v1.0.1
```

Inspected source blobs at this pin (`git rev-parse v1.0.1:<path>`):

| Source | v0.87.1 blob | v1.0.1 blob |
|---|---|---|
| `modes/rpc/rpc-types.ts` | `1cbd49a8` | `7fc71516` |
| `modes/rpc/rpc-mode.ts` | `f4857ffb` | `1c0995d3` |
| `core/agent-session.ts` | `338d0e0d` | `f641d6ec` |
| `core/session-manager.ts` | `fd87a2d8` | `df5281a0` |
| `packages/agent/src/types.ts` | `8da843f5` | `6e17c3c8` |
| `rpc-entry.ts` | `11059a8d` | `11059a8d` (unchanged) |
| `cli/args.ts` | `a3587980` | `9461c3e2` |

Full blob ids are in `fixtures/adapters/pi-v1.0.1/manifest.json`.

## Changes from v0.87.1

Found by diffing the seven sources above and `packages/ai/src/types.ts`
between the two tags, and by running the released `pi-darwin-arm64` v1.0.1
binary through both adapters behind the loopback Responses mock.

Wire changes:

1. **`thinkingLevel` on an assistant message (new).** `AssistantMessage`
   gains `thinkingLevel?: ModelThinkingLevel`, the level the agent loop asked
   for, and v1.0.1 sets it on every assistant message it emits (`"off"` in the
   recorded run). Both adapters decode messages against a closed member set,
   so v0.87.1 code failed every run against v1.0.1 with
   `pi_invalid_message_end` (`unknown field "thinkingLevel"`; the Go
   integration gate reported `run.failed`). Both now admit it as an optional
   string and project nothing from it.
2. **`nestedCalls` on a tool result message (new).** `ToolResultMessage`
   gains `nestedCalls?: { calls, complete }`, a record of the calls one tool
   made to other tools. Admitted, never projected.
3. **`parentToolCallId` on tool execution events (new).** A tool call another
   tool made through `ctx.executeTool()` publishes its
   `tool_execution_start`/`_update`/`_end` with the caller's id. The adapters
   run Pi with `--no-extensions`, which at v1.0.1 also disables built-in
   extensions, so no such nested call is expected; the member is admitted as
   an optional string on all three events so one that arrives does not fail
   the run.
4. **A disposition on `prompt`, `steer` and `follow_up` responses.** Each
   success response now carries `data: { disposition }`, `started`, `queued`
   or `handled` for a prompt and `queued` or `handled` for steer and
   follow-up, and the prompt response is written once the prompt is
   dispatched rather than only when it starts a run. The adapters already
   carried response `data` as an opaque member, so no code change; a prompt
   `handled` without a run (an extension command) cannot arise with
   extensions disabled.

Not wire changes: `rpc-entry.ts` is byte-identical; `args.ts` drops empty
`--models` patterns and changes help text only; the `AgentSessionEvent`
union names the same event types at both tags (one more `entry_appended`
emit site, same shape); `AgentToolResult` gains `structuredContent` and
`isError`, which reach the wire only inside `tool_execution_end.result`,
carried opaque. `get_state`, `abort`, `compact`, `set_thinking_level`,
`set_auto_compaction` and the other commands keep their shapes.

The advertised surface is unchanged. The revision moves to
`pi-v1.0.1-oap-v1` with the pin, and the Zig port serves that same revision.

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
- `packages/ai/src/types.ts` — message and content shapes

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
- **Every admitted event kind carries a closed shape, including the ignored
  ones:** `ValidateEvent` gives all 24 `AgentSessionEvent` spellings a required
  member list and an allowed member set, and the sixteen the reducer draws
  nothing from are no exception. `turn_start` and `summarization_retry_finished`
  reach no case of the switch, which leaves `type` their only allowed member, so
  either one carrying a payload is refused. Ignoring an event is a statement
  about what it means, not a licence to accept any object under its name, so a
  port that dispatches those sixteen to a no-op still owes the shape check
  before it discards the frame.

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
| `message_start/end` (`system`) | system prompt and tool declarations as transcript state | observed-only | no core claim | `system-message` |
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
| `compaction_start` / `compaction_end` | the run's compaction: `run.compaction.started` / `.ended` | normalized | native (`run.compaction`) | `compaction`, `threshold-compaction`, `threshold-compaction-failed` |
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

### The store, read at this pin

Everything above is at this pin already; what follows is the same subject read
in the source at `f07218c4d4bbc12bef056a7058c3dd49dfe41abe`, for the reattach
Decision 0039 stages.

**The store lives under the agent directory, keyed by the working directory.**
`getDefaultSessionDirPath` in `packages/coding-agent/src/core/session-manager.ts`
resolves the cwd, strips its leading separator, replaces every remaining `/`,
`\` and `:` with `-`, and joins the result as `--<encoded>--` under
`<agentDir>/sessions/`. The default agent directory is `~/.pi/agent`, so a
session's directory name *is* its cwd. The header carries `cwd` as well, and
`sessionCwdMatches` compares it against the resolved cwd when discovery
filters by it (`findMostRecentSession(sessionDir, cwd?)` returns the most
recent match, or `null`). So a moved project is a different store under the
same home, exactly as for Claude Code — the cwd is in the key twice, once in
the directory name and once in the header.

**A load restores the transcript tree and the context derived from it.**
`_setSessionFile` reads the file with `loadEntriesFromFile` and hands the
entries to `_loadEntries`, which rebuilds the id and parent indexes; the model
context comes from `buildContextEntries(this.getEntries(), this.leafId,
this.byId)`. What a load does *not* restore is the live stream: the
`entry_appended` event is not a comprehensive feed of those writes (above), so
the tree is a reconstruction of the transcript and not a replay of the run that
wrote it.

**What it answers when the store is not there: two different answers, and
neither is a not-found.** Opening a path that does not exist creates it; a
zero-length file is initialised with a valid header; a file that exists, is
non-empty and parses to zero entries throws `Session file is not a valid pi
session: <path>`. Discovery, which is what a reattach would use to find a
session by id, has no error at all — `findMostRecentSession` returns `null`.
Session ids are `uuidv7` and `assertValidSessionId` admits only
`[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?`, so an id that could never have
been written is rejected before any file is touched. A harness that cannot
find what it was given therefore refuses with a *parse* failure or with null
rather than with a named absence, which is the same place 0039's
`unsupported_feature` has to be manufactured as for ACP.

**What the Go adapter does with all of it: it knows the names and sends none
of them.** The adapter sends three commands and no more: `prompt` (always with
`streamingBehavior: "steer"`, which is also how a follow-up rides — `steer` and
`followUp` are two values of the same member on the same `prompt` command, not
two commands), `abort`, and `get_state`. It sends **no session command at
all**: pi creates a session implicitly when the process starts, which is why
`native.CommandNewSession` and `native.CommandSwitchSession` are both declared
in `go/adapter/pi/internal/native/types.go` and neither appears in
`session.go` — a `switch_session` would be the adapter's first *explicit*
session command, and it is the one 0039's reattach needs. `get_entries`,
`get_tree`, `fork`, `clone` and `get_fork_messages` are typed for the same
reason and equally unused. So pi has no reattach today, and the operations
0039's evidence table names for it are reachable and unused. The adapter
advertises `run.resume` and `run.replay` as `degraded` over its own bounded
journal, which is a different capability from reattaching to the harness's
store and should not be read as one.

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
8. **Steering is first-class natively and now advertised:** the adapter sends
   pi's `steer` command and settles the guidance at the turn boundary pi injects
   it, so `session.message.delivery.steer` is `emulated` and the settlement's
   `boundary` is `turn`. Follow-up queues stay codec evidence: `queue` remains
   `unavailable`, and `auto` alone is exposed for submission.
   Two windows around the native call, recorded rather than compensated. A
   `turn_end` that pi emits after answering the `steer` command but that the
   reducer reaches before the adapter records the steer is still that steer's
   boundary: the Go adapter notes how many turns had ended when the response's
   barrier was reduced and settles the steer at once if more have ended since.
   The other window has no remedy on this wire: pi accepts the guidance
   (`success: true`, even after `agent_settled`, as the Zig fixture
   `fake_settled_before_steer` pins) before the adapter can re-check the run, so
   a run that settles during the call yields an `invalid_steer_target` refusal
   for guidance pi did take. No pending steer is recorded for it, so no
   `run.steer.applied` or `run.steer.dropped` ever covers it, and if pi retains it
   the guidance can reach a later run unannounced. pi offers no way to withdraw a
   steer, so the refusal is the honest answer to the request it judged, and the
   unaccounted guidance is this ledger's mismatch.
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
- delivery `auto`: `emulated`; `steer`: `emulated` over the native `steer`
  command, settled at the turn boundary pi injects it; `queue`: `unavailable`
- cancellation: `degraded`; native abort with `agent_settled` authority
- interactions and permissions: `unavailable`; production extensions disabled
  and `InteractiveGates=false`
- reconciliation: `emulated` (`get_state`)
- run resume/replay: `degraded`, bounded process-memory OAP journal only
- transcript reconstruction, fork/tree navigation, and session switching:
  `unavailable` at the OAP boundary despite native codec evidence
- compaction: `run.compaction` and `session.compact` at `native` (see
  *Compaction inside a run* and *Compaction on request*)
- retry, queue controls, and bash passthrough: observed-only, no core claim

## Executable evidence corpus

`fixtures/adapters/pi-v1.0.1/` contains thirteen compact cases, each with exactly
`case.json`, `native.jsonl`, `mapping.json`, `omissions.json`, and
`expected-oap.json`. The manifest and every case pin the tag, commit, tree, and
seven inspected source blobs. Strict inventory checks reject unlisted files;
classification checks account for every native line; canonical cases execute
through the production decoder/reducer and validate exact OAP traces, while
codec-only or injected-extension mismatches are named explicitly. Golden trace
updates require `OAP_UPDATE_PI_CORPUS=1`.

The thirteen cases cover each of the 27 ledger fixture labels exactly once. `message-rejected` is now a distinct executable case driven
through `Session.Submit`; native-control outcome labels require matching decoded
responses or queue observations, while controls outside the advertised surface
remain explicitly command/codec evidence rather than advertised OAP execution
support:
`initialize-minimal`, `message-admitted`, `message-rejected`,
`completed-text`, `streaming-deltas`, `multi-turn-tools`, `tool-completed`,
`tool-failed`, `tool-progress`, `tool-parallel-order`, `steer-queued`,
`steer-injected`, `follow-up-run`, `cancel-settled`, `error-retry`,
`compaction`, `threshold-compaction`, `threshold-compaction-failed`,
`extension-dialog`, `reconcile-state`, `entries-since`,
`switch-session`, `process-exit`, `malformed-command`, `fork-tree`, and
`no-implied-replay`, and `system-message`.

Provenance of the native side at this pin:

- `system-message` is **re-recorded**: its `native.jsonl` is the Pi-to-host
  event stream of one real `prompt` against the released v1.0.1
  `pi-darwin-arm64` binary behind the loopback Responses mock (frames after
  the `prompt` response, less `agent_start`, which the corpus client emits
  itself). The binary ran from `/tmp/oap-pi/pi` with `HOME` and the workspace
  under `/tmp/oap-pi/home`, so the `docs`, `cwd` and session-file sections
  name only those neutral paths. Its assistant messages carry
  `thinkingLevel`, so this case fails in both trees without the change above.
- The other cases are **carried forward unchanged** from
  `fixtures/adapters/pi-v0.87.1`, which this pin removes with the retired
  version: only their `case.json` and `manifest.json` provenance moved to this
  pin, and their `expected-oap.json` differs only in the revision. Their
  recorded frames predate `thinkingLevel`, which is optional, so they still
  decode as v1.0.1 frames would without it.

Separately gated real-process tests use a caller-supplied Pi v1.0.1 executable:
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

## Live-gate outcome (2026-10-03)

Release artifact digests, matching the release's published `SHA256SUMS`:

- `pi-linux-x64.tar.gz`
  = `1940ecabcbd54ddd1a78dd2d587c189c5ced83e775a817855a9f5d1dd799c5f2`
- `pi-darwin-arm64.tar.gz`
  = `de35e0025b136eb37693054ca658c010b6327a12aaff438ae87c4d1c94f99e6c`

The gates were run against the `pi-darwin-arm64` artifact; no Linux host was
available.

- `OAP_PI_SMOKE=1`: **PASS**, 3x.
- `OAP_PI_INTEGRATION=1`: **FAIL** before the fix (`run.failed`,
  `pi_invalid_message_end` on `thinkingLevel`), **PASS** after, 3x back to back.
- `oapx serve agent --backend pi` driven over stdio against the same binary
  and mock: `run.started`, `content.delta`, `run.completed` with the fixture
  text.

## Served by `oapx serve agent --backend pi`

The Zig port (`zig/src/adapter/pi/adapter.zig`) drives the same reducer as the
corpus, one per run. Where it differs from the Go adapter:

- It advertises the Go adapter's revision, with `run.resume` and `run.replay`
  `degraded` as Go does: the `oapx` endpoint keeps a bounded journal of 256
  events per session and answers the replay control from it.
- The child runs with `--no-extensions` and explicit extension arguments
  refuse the open, per P0 policy 4, so no dialog is expected. One that arrives
  anyway is handled as in Go: before `agent_start` it waits and surfaces as
  `user.input` once the run starts, outside a run it is ignored, and one still
  open when the run settles resolves `cancelled` with nothing written to Pi.
  `action.permissions` stays `unavailable` as in Go.
- Like Go, each state request reads `get_state` again and answers the adapter
  projection; a reply naming another native session makes the session
  unusable. As in Go, a reply with no session, a negative count, an unknown
  queue mode or an unknown thinking level is refused at open and on each state
  request; a refused state request leaves the session usable.
- A prompt Pi refuses closes the session without a `run.failed`: the run was
  never announced, so there is no one to report it to.
- Without `--config` it runs `pi` from `PATH` with only `HOME` and `PATH`.

## Model-provider settings at this pin

Pi names a model provider in `models.json` under the agent directory
(`~/.pi/agent/`), which is how an endpoint it does not already ship is added.
Each provider entry carries `baseUrl`, `api` (the wire), `apiKey` and `models`.

| Setting | What it sets |
| --- | --- |
| `models.json` `providers.<id>.baseUrl` | the endpoint |
| `providers.<id>.api` | the wire, e.g. `openai-completions` |
| `providers.<id>.apiKey` | the key: a literal, `$NAME` / `${NAME}` environment interpolation, or a leading `!command` |
| `providers.<id>.models` | the models this provider serves, by id |
| `providers.<id>.modelOverrides` | metadata changes to a catalog or extension model, without replacing its list |
| `--api-key` | a credential for one run, reading over `auth.json` and `models.json` |
| `/login` | interactive; stores a credential in `~/.pi/agent/auth.json` |

Documented at this pin in `packages/coding-agent/docs/models.md` (the compatible
endpoint), `providers.md` (per-provider API-key environment variables) and
`environment-variables.md` (the `PI_*` process and shell variables, which name
the selected provider and model but configure neither).

Credential order at this pin is `--api-key`, then `auth.json`, then `models.json`
`apiKey`, then the provider's own environment variables — so an environment key
is the setting that needs no file at all. `models.json` is reloaded when `/model`
opens, and a `!command` key runs at request time and is not cached.

A provider extension (`pi.registerProvider()`) is also a way in, and it is
deliberately not a setting: it is code loaded into the Pi process, so it is
outside the rule this epic set, which is a harness's own official settings and
nothing patched into it.

## Reasoning level and compaction at v1.0.1

Recorded for [Decision 0045](../decisions/0045-reasoning-level-and-compaction-policy-are-session-settings.md).
Read from the source at `a7229ddc21810d6245105978033b7df645ecc2f7`, under
`packages/coding-agent/src/`.

**Reasoning level.** `--thinking <level>` sets it at launch (`cli/args.ts`), and
the RPC command `set_thinking_level {level}` (`modes/rpc/rpc-types.ts`) changes
it on a live session. Levels are `off`, `minimal`, `low`, `medium`, `high`,
`xhigh` and `max`, OAP's set exactly. `get_state` reports the level, and a
`thinking_level_changed` event publishes a change.

**Compaction.** `core/compaction/compaction.ts`'s `shouldCompact` compacts when
the context exceeds `contextWindow - reserveTokens`, and only while
`compaction.enabled` is true. Both come from `settings.json` in the agent
directory (`core/settings-manager.ts`; `enabled` defaults to true and
`reserveTokens` to 16384, with per-model overrides), and `PI_CODING_AGENT_DIR`
moves that directory (`config.ts`). So a token threshold is set at launch by a
settings file the adapter writes in a private agent directory, as
`reserveTokens = contextWindow - threshold`. On a live session
`set_auto_compaction {enabled}` switches compaction on and off, and nothing
moves the threshold.

## Session settings in the adapter

Decision 0045's settings add `session.reasoning` and
`session.compaction.policy` at `native` with the `session_open` mode, so the
revision moved to `pi-v0.87.1-oap-v2` at that pin (and was `pi-v1.0.1-oap-v1` at this
one until *Compaction inside a run* moved it to `pi-v1.0.1-oap-v2`). After the ready `get_state`, both
trees send `set_auto_compaction` for `auto` or `off`, then
`set_thinking_level`. A second `get_state` confirms the level, and a level Pi
kept elsewhere is refused. `share` and `tokens` are refused before Pi starts,
because the threshold is a `settings.json` reserve in the agent directory, and
that directory also holds Pi's credentials, so the adapter does not move it.

## Compaction inside a run

Recorded for [Decision 0044](../decisions/0044-compaction.md), whose step 3 is
pi's native evidence. This section covers the compactions Pi starts on its
own; *Compaction on request* below covers `session.compact`.

**Where Pi compacts.** `core/agent-session.ts` runs an automatic compaction
through `_runAutoCompaction(reason)` from three places: before a prompt is
sent, at a turn's end (`_dispatchTurnEndBoundary`), and before the next
assistant request (`_compactBeforeNextAssistantResponse`). Each brackets the
work with `compaction_start {reason}` and `compaction_end {reason, result?,
aborted, willRetry, errorMessage?}`, where `reason` is `threshold` or
`overflow` (`manual` is the `compact` command's). A summary that cannot be
written is retried with `summarization_retry_*` events before the end reports
the failure.

**Live order.** The v1.0.1 `pi-darwin-arm64` binary (archive
`de35e0025b136eb37693054ca658c010b6327a12aaff438ae87c4d1c94f99e6c`, the catalog's digest; binary `177717b5c28d7b0b62584982f4489c5e3ccd31d9731e1d8196bcfc44f85e86eb`), run in RPC mode behind the
loopback Responses mock with a 4000-token window and `reserveTokens: 3990`,
emits after the turn:

```
agent_end
compaction_start {"reason":"threshold"}
compaction_end {"reason":"threshold","result":{"summary":…,"estimatedTokensAfter":1547,…},"aborted":false,"willRetry":false}
agent_settled
```

With no summary response queued, the same run retries the summary three
times and ends `compaction_end {"errorMessage":"Auto-compaction failed: …"}`,
still before `agent_settled`. A `compact` command aborted mid-summary ends
`compaction_end {"aborted":true}` and answers `success: false, error:
"Compaction cancelled"`. Because both adapters settle a run at
`agent_settled`, never at `agent_end`, every automatic compaction falls inside
the run that triggered it, as Decision 0044 requires of a `threshold` or
`overflow` compaction.

**Mapping**, in both trees:

| Pi | OAP |
|---|---|
| `compaction_start` `threshold` / `overflow` / `manual` | `run.compaction.started`, reason `threshold` / `overflow` / `requested`, a fresh `compaction_id` |
| `compaction_end` `aborted: true` | `run.compaction.ended` `cancelled` |
| `compaction_end` with a `result`, no `errorMessage` | `completed`; `result.summary` becomes `summary` (an assistant message), `result.estimatedTokensAfter` becomes `history_tokens` |
| any other `compaction_end` | `failed`, error `pi_compaction_failed` carrying `errorMessage` |
| a terminal reached with a compaction open | the compaction ends first: `cancelled` before `run.cancelled`, otherwise `failed` with `pi_compaction_unfinished` |
| an unknown reason, a second `compaction_start`, or an end without a start | `run.failed` `pi_invalid_compaction` |
| `summarization_retry_*` | observed-only |

`history_tokens` is omitted on the start: Pi reports `tokensBefore` only in
the end's result.

**Corpus.** `threshold-compaction` and `threshold-compaction-failed` carry the
recorded `compaction_*` and `summarization_retry_*` frames verbatim, between
the `completed-text` case's `message_end`/`agent_end` and `agent_settled`.
`retry-compaction`'s synthetic compaction pair is now mapped. Both trees
replay all three to the same envelopes.

**Gates.** `TestPiProcessCompactsPastItsThresholdInsideTheRun`
(`OAP_PI_INTEGRATION=1`) drives the binary above through the Go adapter and
asserts one completed `threshold` compaction with a summary inside a completed
run, against two Responses requests (the turn and its summary); it passed 3x.
`oapx serve agent --backend pi`, with `pi` on `PATH` a wrapper carrying the
same agent directory, published the same `run.compaction.started` /
`.ended` pair inside the run under `pi-v1.0.1-oap-v2`.

The advertised surface gains `run.compaction` at `native`, so the revision
moves to `pi-v1.0.1-oap-v2`.

## Compaction on request

`compact {customInstructions?}` (`modes/rpc/rpc-mode.ts`) calls
`AgentSession.compact`, which aborts any agent operation, emits
`compaction_start {"reason":"manual"}`, writes the summary, emits
`compaction_end`, and only then answers. The answer is `success: true` with
the `CompactionResult`, or `success: false` with the error: `"Compaction
cancelled"` after an abort, `"Already compacted"` or `"Nothing to compact
(session too small)"` when there is nothing to do. Pi emits no `agent_start`
or `agent_settled` around it, and never continues the turn.

Both trees serve `session.compact.request` from it:

- On an idle session the request is admitted `started` as a run of its own.
  The adapter emits `run.started` and the run's `requested`
  `run.compaction.started` at admission, sends `compact` with the `focus` as
  `customInstructions`, and adopts Pi's `compaction_start {manual}` as that
  same compaction rather than opening a second one. Opening at admission keeps
  the compaction first in the run even when a cancel lands before Pi's start
  does, which Decision 0044's validator requires.
- `compaction_end` closes it as in *Compaction inside a run*. `compact`'s
  answer settles the run: success completes it with `stop_reason:
  "compacted"` and the summary message as `final_response` (the same message
  the compaction's end carried); a refusal fails it `pi_compaction_failed`
  with Pi's error, unless a cancel was accepted, in which case it is
  `run.cancelled`.
- `run.cancel` sends `abort`, as for a prompt run. An abort Pi takes before
  the compaction has begun aborts nothing, the compaction completes, and the
  run settles `completed`: the cancel was intent.
- Refused: `continue` (`unsupported_feature`, unsatisfiable, field
  `continue`), `queue`, `steer` and `btw` delivery (`unsupported_feature`
  naming the delivery key; Pi's queue is unavailable here), and a busy
  session (`run_active`). A compaction run cannot be steered.

The Go adapter answers the request before Pi does and settles the run from
`compact`'s answer, which the RPC client delivers only after every earlier
event has been reduced. The Zig port writes the command without waiting and
settles the run when its step loop reads the answer.

`TestPiProcessCompactsOnRequestAndOnCancel` (`OAP_PI_INTEGRATION=1`) drives
the v1.0.1 binary after one turn: a requested compaction completes with the
summary, and one cancelled while the summary request is held completes
`cancelled`, each against two Responses requests; it passed 3x.
`oapx serve agent --backend pi` served the same requested compaction under
`pi-v1.0.1-oap-v3`. No corpus case drives the request: the corpus harnesses
replay a prompt's frames only.

`session.compact` goes `native`, so the revision moves to `pi-v1.0.1-oap-v3`.

## Live session settings

Decision 0045's `session.settings.update.request` maps onto the two commands
the open already sends, and both trees serve it:

- `reasoning_level`: `get_state` for the level in force, `set_thinking_level`,
  then `get_state` to confirm. Pi clamps a level the model does not support to
  the nearest it does and answers `success: true` either way, so only the
  confirmation tells; a mismatch sends `set_thinking_level` with the level it
  replaced and refuses the update `unsupported_feature` (unsatisfiable, field
  `reasoning_level`). The response's previous level is the one `get_state`
  reported.
- `compaction_policy`: `set_auto_compaction {enabled}`, `auto` on and `off`
  off, sent only after the level, so a refused level leaves the policy
  untouched. `share` and `tokens` are refused before Pi is asked, as at open.
- Refused: a busy session (`run_active`), a compaction run included, and
  another session's id (`run_not_found`).

`TestPiProcessChangesItsLevelAndCompactionBetweenRuns`
(`OAP_PI_INTEGRATION=1`) drives the v1.0.1 binary (sha256
`177717b5c28d7b0b62584982f4489c5e3ccd31d9731e1d8196bcfc44f85e86eb`) with a
reasoning model on the Responses mock: `high` and `off` are confirmed, `max`
is refused with Pi left at `high`, and the next run asks for effort `high`;
it passed 3x. `oapx serve agent --backend pi` served the same update, the
refusal and the run, and the trace validates.

Both settings add `session_live`, so the revision moves to
`pi-v1.0.1-oap-v4`.

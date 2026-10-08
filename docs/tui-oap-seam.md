# The TUI-to-agent seam, mapped onto agent-control-core

Part of #365. This is #373's deliverable: what `zig/src/tui/` calls today, what
`drafts/agent-control-core.md` says about each call, and what the Zig endpoint
can actually answer. It is a map, not a plan — the step order lives in #375.

**Status, 2026-10-07.** `oapx tui` is the terminal UI over this seam, and what
`oapx` alone starts; `oapx --tui`, the local loop without OAP, was removed once
the parity sweep showed no difference: `TuiRuntime`
takes an injected `RemoteExecution` (`zig/src/tui/oap_execution.zig`) that hosts
`zig/src/adapter/endpoint.zig` with the `oapx` adapter in-process and turns its
envelopes back into `TuiEvent`s. The agent loop itself is the adapter's: a
`LocalLoop` (`zig/src/adapter/oapx/local_loop.zig`) owns the agent, its wrapped tools,
approvals and compaction transcripts, and plugs into a `TuiRuntime` through its `Loop`
interface, so the runtime the TUI holds carries no agent of its own. Runs, streaming, tools, cancel and model switch
cross the boundary as OAP. A `/model refresh` hands the in-process endpoint the
refreshed catalog beside the wire, and the session serves it from its next model
switch under the same revision, which is why the `oapx` adapter advertises
`models.list` as `degraded`. The settings the protocol has no verb for (context
window, output, permission mode, workspace root) travel in the open request's
`metadata.oapx` with the thinking level. When the endpoint advertises
`session.reasoning` with `session_live`, all five change between runs on a
`session.settings.update.request`, the four under `extensions.oapx` beside the
current thinking level. Over `--attach` only the thinking level changes, through
the hub's settings route; the other four stay fixed once the session opens.
Automatic worktrees follow from that: the first turn creates the session's Git
worktree and moves the workspace root to it live, so they are on wherever the
endpoint takes live settings, and off over `--attach`. Tool approvals cross too: in ask mode the adapter's
`action.permission.requested` becomes the TUI's approval prompt, and the answer goes
back as `action.permission.resolve.request` naming the choice the user made: the
`oapx` adapter offers `approve_always` and `reject_always` beside `approve` and
`deny`, and passes an always answer to its loop, which remembers it for the session; an endpoint that does not offer them gets `approve` or `deny`. The model's questions (`user.input.*`) stay off: the
open declines `user_input`, because the TUI has no prompt for them. A run's terminal
event carries `usage.output_tokens`, and the oapx adapter adds the context the run
filled as `extensions.oapx.context_tokens`, which the TUI shows on its context gauge
once the run ends rather than per model call. The output count is a run total, so it
replaces the TUI's estimate only for a run with one assistant message; a run that spoke
before a tool call keeps the per-message estimates rather than counting its tokens twice.
A follow-up queued during a turn is submitted with `delivery: "queue"`, and the turn stays
open until each reservation has been promoted and run, so it reads as one turn as it does
locally; clearing the queue or aborting cancels the reservations. Over `--attach` the hub
link follows each run this terminal submitted, one stream at a time: when the followed run
ends it subscribes to the next queued one from its last delivered sequence, drops events of
runs it did not submit, and ends the turn with a lost stream after three streams in a row
deliver nothing. A queued admission that is not a follow-up behind its own run is still
withdrawn and refused `session_busy`. `/compact` sends `session.compact.request` and `/autocompact`
sends its setting as the session's `compaction_policy` before each turn, when the
endpoint advertises them; the TUI's attach link (`zig/src/tui/hub_link.zig`) sends
the compaction to the hub's submit route and follows the run it starts, and the
policy to its settings
route. `/resume` reopens the saved session over OAP: the TUI halts its execution and opens again with `reopen: true` under the saved session's id, and the `oapx` adapter, which advertises `session.open.reopen`, loads that transcript from `~/.oapx/sessions` into a fresh loop and reports the session recovered; a successful reopen closes the in-process session it left, and a refused one keeps it running. Over `--attach` the hub's `oapx` entry keeps no saved sessions yet, so it does not advertise `session.open.reopen` and a resume there is refused `unsupported_feature`. Reopening the session already open keeps it, unless the saved session's workspace differs from the one it opened with, which is refused. `oapx tui --attach URL` runs the
same execution over a running hub's HTTP wire (`zig/src/tui/hub_link.zig`): each envelope
goes to its route, and each run is followed on its own SSE stream replayed from its first
event, read by polling the socket on the execution's pump thread. A model switch, which
the hub has no route for, is refused to its request, and a model the hub's catalog lacks
leaves the session on the hub's default with a warning. The map that follows predates
this and still describes the server path `oapx serve agent` without `--backend` uses.

Every claim here was read off `origin/main` at `cbfa3b96d6`. The endpoint's own
capability list is the authority on what it can serve, and where that list
disagrees with the TUI's needs, the disagreement is the finding.

## What is already on the other side

When this map was drawn, `zig/src/protocol/oap/server.zig` was the
agent-control-core endpoint `oapx serve agent` ran, `zig/src/protocol/oap/bridge.zig`
translated its envelopes onto the inner agent profile, and the TUI bypassed both.
That endpoint is gone: `oapx serve agent` now serves the `oapx` adapter, the same
loop the TUI runs over the in-process endpoint. The table below is what the removed
endpoint advertised, kept as the starting point the gaps were measured from.

`server.zig` advertises, at `CAPABILITY_REVISION`, exactly this:

| Feature | Level | Note |
| --- | --- | --- |
| `protocol.initialize`, `capabilities`, `session.open` | native | |
| `session.state` | degraded | via `advertised_degradation`; not resumable, no transcript replay |
| `session.model.switch` | native | |
| `models.list` | native | |
| `auth.providers`, `auth.login` | native | dispatched by `auth_adapter.zig`, not by `server.zig` — see G5 |
| `session.message.submit`, `session.message.delivery.auto` | native | auto delivery only |
| `run.streaming`, `run.status` | native | |
| `run.model_selection` | native, scope `run` | |
| `run.cancel` | degraded | cancelling a run also closes its session |
| `content.reasoning` | degraded | outbound preserved, inbound not accepted |

`session.message.delivery.queue`, `session.message.delivery.steer`,
`session.message.delivery.btw`, `action.tools.list`, `action.providers.attach`
and `run.instructions` are **not advertised** — they are absent from
`advertised_features` — and `handleSubmit` refuses any delivery but `auto` with
`unsupported_feature`. A test at `server.zig:3024` pins the typed refusal, so
the refusal is a decision rather than an oversight; the non-advertisement of
`action.tools.list` and `session.message.delivery.queue` is pinned at
`server.zig:2130` and `2131`, while the other four rest on the capability table
alone. Either way it costs the TUI two of its primary mid-run controls, which
is G2.

## The map

`TuiSessionOps` (`zig/src/tui/session.zig`) is the TUI's whole control surface:
seventeen operations. "Direct" marks calls the runtime makes on `local_agent`
without going through the ops table.

| TUI operation | agent-control envelope | Status |
| --- | --- | --- |
| `start` | `protocol.initialize` + `session.open` | covered |
| `submit_turn` | `session.message.submit` (delivery `auto`) | covered |
| `cancel` | `run.cancel.request` | covered, degraded — G1 |
| `switch_model`, `switch_model_exact` | `session.model.switch.request` | covered |
| `current_model` | `session.state.request` | covered |
| `queued_counts` | none | **gap** — G3 |
| `can_steer` | `capabilities` gating | covered |
| `history` | `session.state.request` | covered, degraded — G1 |
| `stream_events` | `content.delta`, `run.status.updated`, terminal run events | covered |
| `steer` | `session.message.submit` (delivery `steer`), settled by `run.steer.applied` or `run.steer.dropped` | covered by the in-process `oapx` adapter — G2 |
| `follow_up` | `session.message.submit` (delivery `queue`) | covered by the in-process `oapx` adapter — G2 |
| `clear_queued_messages` | none | **gap** — G3 |
| `steers_consumed` | none | **gap** — G3 |
| `decide_tool_approval` | `action.permission.*`, `user.input.*` | **gap** — G4, shaped, undispatched |
| `compact` | `session.compact`, `run.compaction`, and `session.settings.update` for the threshold | closed in-process and over `--attach` |
| `resume_session` | `session.state.request`, then submit | **gap** — G1 |
| `replaceMessages` (direct) | `transcript.load` | **gap** — G1 |
| `waitForIdle` (direct, 6 production call sites) | a terminal run event | **gap** — G7 |
| `setTools`, MCP bridge (direct) | `action.tools.provide`, `action.tool_sources.attach` | provide covered; attach **gap** — G8 |
| `models` / login (direct) | `models.list`; auth stays local | covered / G5 |

## Measuring parity

Parity was measured by `scripts/tui-pty-driver.py --scenario all`, which ran
every sweep scenario on `oapx --tui` and again on `oapx tui` and compared the
two runs' saved session records field by field. With no scenario failing and no
record differing, `oapx tui` became the default and `--tui` was removed. The
sweep now runs every scenario on `oapx` alone, and a command in
`zig/src/tui/commands.zig` that no scenario drives still fails it.

## Gaps

Each is a proposal. #375 moves a flow only when its gap is closed or its
recommendation is accepted.

### G1 — cancellation and session state are degraded in a way that conflicts with the TUI

`run.cancel` is `degraded` with the reason that a cancelled run also closes its
session, and `session.state` is `degraded` because sessions are not resumable
and there is no transcript replay. Both are core TUI behaviour: `Esc` and
`Ctrl+C` abort a turn and leave the session usable, and `/resume` restores a
saved session from `~/.oapx/sessions`. The TUI's abort is run-scoped
(`local_agent.abort()`); an endpoint whose cancel tears the session down cannot
serve it without a semantic change the TUI would feel. Filed as #614.

This is agent-side work. Recommendation: run-targeted cancel, and a resumable
session state, before the TUI's cancel and resume flows move.

### G2 — steering and queued follow-ups are unadvertised, and the TUI cannot lose them

`Enter` steers a streaming turn and `Tab` queues a follow-up; both are the TUI's
primary mid-run controls, and queued messages are also what
`/compact` and `/autocompact` rely on to resume. The protocol has
`session.message.delivery.steer` and `session.message.delivery.queue` as common
optional core features, so this is an endpoint gap rather than a protocol one.
Filed as #615.

**Status: both are carried by the in-process `oapx` adapter that `oapx tui`
runs over OAP; `server.zig`'s `handleSubmit` still resolves `auto` only.** A
queued follow-up is a reservation the adapter promotes when the turn ends
(#823). A steer is admitted `steered` against the running run and joins the
loop at its next boundary, after a tool result or at the end of a turn; the
adapter publishes `run.steer.applied` when the loop takes it, and the TUI
shows the message then. A steer still waiting when the run ends is published
`run.steer.dropped` before the terminal. The TUI warns that it was not applied,
except on a cancel, where the composer gets the text back. Over a hub
attachment the steer rides the hub's submit route against the run the link
follows, and its settlement arrives on that run's stream.

### G3 — three queue counters have no envelope

`clear_queued_messages`, `steers_consumed` and `queued_counts` are the TUI asking
about its own queue. `SessionState` carries `session_id`, `status`,
`active_run_id`, `current_model_id` and `updated_at_ms` (`server.zig:849`) and no
queue counters, while `TuiSession`'s `QueuedCounts` is `{steering, follow_up}`.
None of the three is session state the protocol carries, and none should grow an
envelope to carry it.

Recommendation: TUI-local state, derived from the admission responses the TUI
already receives for each submit, and it never crosses the boundary. Filed as
#616.

### G4 — permissions and user input are shaped but not dispatched

`action.permissions` and `user_input` are named as optional core features in the
draft, and the endpoint has **neither in its capability list or its dispatch** —
`server.zig` mentions neither. Both sit in the same position, and it is not the
position a missing optional unit is usually in: the payloads exist and
`envelope.zig` already serialises and round-trips them, so what is missing is
dispatch and advertisement, not a wire format.

- `action.permission.resolve.request` / `.response` and
  `action.permission.requested` / `.resolved` are defined in `types.zig:1090` with
  their wire names at `types.zig:1184`, and `envelope.zig:672` serialises and
  round-trips them.
- `user.input.resolve.request` / `.response` and `user.input.requested` /
  `.resolved` are defined beside them at `types.zig:1088` and carried in
  `envelope.zig`.

So #612 is dispatch and advertisement work. The TUI still cannot move
`decide_tool_approval`, permission modes or the approval prompt until the
endpoint answers them, and it is on the critical path for the tools step.

A discrepancy in the draft is worth recording here, because a control layer
reading only the capability-key list would get it wrong: `drafts/agent-control-core.md:679`
lists the key as `action.permissions`, plural, while lines 584 and 768 and the
Zig wire names are `action.permission.*`, singular. Whoever implements the unit
has to reconcile that, or the capability key will not match what the endpoint
accepts.

### G5 — auth is served, and stays TUI-local anyway

`auth.providers` and `auth.login` are advertised `native` and are genuinely
served: `zig/src/protocol/oap/auth_adapter.zig:173` dispatches
`auth.providers.request`, `auth.login.start.request` and
`auth.login.cancel.request`, and `zig/src/tools/makai.zig:9154` wires the adapter
into the serve path. It is a separate adapter rather than a `server.zig` branch,
which is why a capability list read alone does not show it.

The draft puts auth provider listing and login flows outside the core.
**Decision: auth stays TUI-local and never crosses the boundary.** Credentials
belong to the store that holds them — today the Keychain through
`oauth/storage` — and login is not agent state. The endpoint keeping a working
auth surface is not a problem; the TUI simply does not use it, and #375's step
list no longer moves login.

This is worth recording rather than treating as settled, because it is the one
place where a working endpoint capability is deliberately left on the table. If
a future control layer wants login over the wire, the endpoint already answers.

### G6 — compaction and continue-after-compaction have no verb

The draft's "Richer Controls" puts context compaction controls outside the core,
yet `/compact` and `/autocompact` are user-visible TUI features with no
envelope. `continueFromContext` — resuming a run after a compaction with no new
user message — is a second gap in the same place; an ordinary continue is
already a submit. `run.instructions` is not the answer, since it carries
instructions rather than replacing a history.

**Decision: compaction stays on the direct path for now.** Filed as #613,
proposing an optional compaction unit covering the verb, its correlated
response and its events, plus the continue-after-compaction case. [Decision
0044](../decisions/0044-compaction.md) is that proposal, and is accepted. The
in-process `oapx` adapter compacts on request and at the session's threshold, so
`/compact` and `/autocompact` now cross the seam (#866). `oapx serve agent` keeps
no history between runs, so it has nothing to compact and refuses both.

### G7 — the blocking idle wait

`waitForIdle` is a synchronous join on the agent's run thread: `agent.zig:734`
locks the mutex, checks `is_streaming` or a live thread, and blocks until the
run ends. It appears 31 times in `zig/src/tui/runtime.zig`, but only **six of
those are production code** — `stop`, `submitTurn`, `replaceMessages`,
`history`, `compact` and `resumeSession`. The other 25 are test call sites, so
the work is six joins, not thirty-one.

It is not a protocol call, which is why neither #365 nor #375 names it. Filed
as #617. On an endpoint, "has the run finished" is the terminal run event, so
each of the six has to become an await instead. This is behaviour-visible and
needs its own PR, before any run flow moves.

The six fall into two groups. `history`, `stop` and `replaceMessages` are the
TUI asking whether it may act or read; `submitTurn`, `compact` and
`resumeSession` are "I am about to start work, wait for the current run", which
is what `resumeQueuedMessagesIfIdle` already expresses with `isIdle`. The TUI's
tick runs at 50 ms, so a poll against a terminal-event flag is enough and needs
no second event mechanism. At six call sites this is a comfortably small PR.

### G8 — client tools and attached tool sources

`setTools` and the MCP bridge are `action.tools.provide` and
`action.tool_sources.attach`. The `oapx` adapter serves the first under
Decision 0011: tools supplied at open join the loop's own for the session,
listed with the opener as `execution_owner`, and each call to one is an
interaction the opener settles with `action.call.resolve.request` while the
loop's tool waits. Its limits are 64 tools, names matching
`^[a-zA-Z0-9_-]{1,64}$`, and JSON Schema 2020-12; a definition naming a
`source` is refused, because the loop declares none. Attaching sources stays
unadvertised. The draft says a source may be described and attached at session
open under `+tool-sources`, and that nothing in the protocol *manages* one.
Filed as #618.

## What stays on the TUI side of the line

- **Credentials and login** (G5), on the owner's decision and the draft's own
  section. The endpoint serves `auth.*` today; the TUI does not use it.
- **The queue counters** in G3, derived locally.
- **The title request.** `TuiRuntime.protocol` is an `agent.ProtocolClient` —
  a *model-provider* seam, a different protocol from agent-control, and the TUI
  keeps it regardless of how the control layer moves.
- **Transcript storage.** `~/.oapx/sessions` is the TUI's own; the endpoint
  declines to replay it (G1), so this does not become `transcript.load` unless
  G1 is closed. Over the in-process endpoint the records in it come from the
  endpoint's own loop, handed to the TUI beside the wire through the adapter's
  `Recorder`, because the wire cannot rebuild them: no event says which tool
  calls one assistant message made, or what text the model saw as a tool's
  result. The TUI writes them, so its titles, index and compaction offsets
  stay where they were. An attached hub has no such channel, and the TUI saves
  what it renders from the wire.

## What the endpoint actually puts on the wire

This section pinned the removed endpoint's payloads against
`zig/src/tui/oap_ops_parity.zig`, both read at `cbfa3b96d6`. The `oapx` adapter's
payloads are pinned instead by its own tests in `zig/src/adapter/oapx/adapter.zig`,
which run every envelope through the schema and semantic validators.

## Open against the draft

The map is complete against what exists. Two things it cannot settle, both now
filed: G6's compaction unit shape, and whether `run.cancel` staying
session-scoped is the endpoint's permanent position or a staged degradation.

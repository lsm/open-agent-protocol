# The TUI-to-agent seam, mapped onto agent-control-core

Part of #365. This is #373's deliverable: what `zig/src/tui/` calls today, what
`drafts/agent-control-core.md` says about each call, and what the Zig endpoint
can actually answer. It is a map, not a plan — the step order lives in #375.

Every claim here was read off `origin/main` at `cbfa3b96d6`. The endpoint's own
capability list is the authority on what it can serve, and where that list
disagrees with the TUI's needs, the disagreement is the finding.

## What is already on the other side

`zig/src/protocol/oap/server.zig` is the agent-control-core endpoint.
`zig/src/protocol/oap/bridge.zig` translates its envelopes onto the inner agent
profile that `zig/src/protocol/agent/server.zig` speaks, and
`zig/src/tools/makai.zig` drives the loop. The TUI is the only consumer that
bypasses all of it and calls `agent.Agent` directly.

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
and `run.instructions` are **not advertised**, and `handleSubmit` refuses any
delivery but `auto` with `unsupported_feature`. A test at `server.zig:2131`
pins that they are unadvertised and a test at `server.zig:3024` pins the typed
refusal, so it is a decision, not an oversight — but it is a decision that costs
the TUI two of its primary mid-run controls, which is G2.

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
| `steer` | `session.message.submit` (delivery `steer`) | **gap** — G2, unadvertised |
| `follow_up` | `session.message.submit` (delivery `queue`) | **gap** — G2, unadvertised |
| `clear_queued_messages` | none | **gap** — G3 |
| `steers_consumed` | none | **gap** — G3 |
| `decide_tool_approval` | `action.permission.*`, `user.input.*` | **gap** — G4, shaped, undispatched |
| `compact` | none | **gap** — G6 |
| `resume_session` | `session.state.request`, then submit | **gap** — G1 |
| `replaceMessages` (direct) | `transcript.load` | **gap** — G1 |
| `waitForIdle` (direct, 31 call sites) | a terminal run event | **gap** — G7 |
| `setTools`, MCP bridge (direct) | `action.tools.provide`, `action.tool_sources.attach` | **gap** — G8, unadvertised |
| `models` / login (direct) | `models.list`; auth stays local | covered / G5 |

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
`handleSubmit` currently resolves `auto` only. Filed as #615.

This is agent-side work and it gates two of the TUI's most-used features.

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
`server.zig` mentions neither. Both are more built out than that suggests, though,
and the shape is worth stating precisely because it changes the sizing:

- `action.permission.resolve.request` / `.response` and
  `action.permission.requested` / `.resolved` are defined in `types.zig:1090` with
  their wire names at `types.zig:1184`, and `envelope.zig:672` serialises and
  round-trips them.
- `user.input.resolve.request` / `.response` and `user.input.requested` /
  `.resolved` are defined beside them at `types.zig:1088` and carried in
  `envelope.zig`.

So the missing work is dispatch and advertisement, not a wire format. The TUI
still cannot move `decide_tool_approval`, permission modes or the approval
prompt until the endpoint answers them. Filed as #612.

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
response and its events, plus the continue-after-compaction case. #375's
compaction step waits on that decision.

### G7 — the blocking idle wait is the largest structural obstacle

`waitForIdle` is called 31 times in `zig/src/tui/runtime.zig` and is a
synchronous join on the agent's run thread. It is not a protocol call and no
issue names it. On an endpoint, "has the run finished" is the terminal run
event, so every one of those call sites has to become an await instead. This is
behaviour-visible and needs its own PR, before any run flow moves.

### G8 — client tools and attached tool sources

`setTools` and the MCP bridge are `action.tools.provide` and
`action.tool_sources.attach`, neither advertised and neither dispatched. The
draft says a source may be described and attached at session open under
`+tool-sources`, and that nothing in the protocol *manages* one. Client-provided
tool definitions are #374's subject and gate the TUI's tools step. Filed as
#618.

## What stays on the TUI side of the line

- **Credentials and login** (G5), on the owner's decision and the draft's own
  section. The endpoint serves `auth.*` today; the TUI does not use it.
- **The queue counters** in G3, derived locally.
- **The title request.** `TuiRuntime.protocol` is an `agent.ProtocolClient` —
  a *model-provider* seam, a different protocol from agent-control, and the TUI
  keeps it regardless of how the control layer moves.
- **Transcript storage.** `~/.oapx/sessions` is the TUI's own; the endpoint
  declines to replay it (G1), so this does not become `transcript.load` unless
  G1 is closed.

## Open against the draft

The map is complete against what exists. Two things it cannot settle, both now
filed: G6's compaction unit shape, and whether `run.cancel` staying
session-scoped is the endpoint's permanent position or a staged degradation.

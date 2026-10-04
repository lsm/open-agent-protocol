# Decision 0044: Compaction

Status: accepted 2026-10-04 (the wire surface graduated on the shared validator
in both trees, with its positive, schema-invalid and semantic-invalid fixtures
(#829); both memory reference adapters execute a compaction request as a run
under submit's rules (#837), the hub admits one on its submit op (#839), they
compact on their own past a threshold (#840) and for `overflow` past the
reference window whatever the policy (#855); and pi, the graduating adapter,
publishes its own compactions as the run's `run.compaction` events (#851) and
serves `session.compact` through its `compact` command, both at `native`, with
the `cancelled` outcome exercised against the v1.0.1 binary (#853). The
threshold policy this record defers is taken by Decision 0045. The
hub-served `oapx` adapter compacts from the in-process runtime (#865); `oapx`'s
default endpoint, which reaches its loop over the native agent wire, is handed
to #866)
Date: 2026-10-01
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `compaction` (claim term `+compaction`)
Amends: nothing. Extends
[Decision 0001](0001-agent-control-v0.1-executable-core.md) and
[Decision 0002](0002-admission-before-start.md) without amending either, and
leans on [Decision 0007](0007-queue-delivery.md) and
[Decision 0013](0013-steer.md) for what happens to a message that arrives
while a compaction runs, extending 0013's `invalid_steer_target` reason
vocabulary by one reason (below), so it settles with 0013 rather than ahead
of it
Gated by: [Decision 0003](0003-staged-unit-graduation.md)
Tracks: #613, and gap G6 of [`docs/tui-oap-seam.md`](../docs/tui-oap-seam.md)

## Context

A long session outgrows its context window, and every interactive harness in
this repository answers that the same way: it replaces the older history with
a summary and carries on. OAP has no word for it. `drafts/agent-control-core.md`
lists "context compaction controls" under Richer Controls, outside the core, so
a control layer can neither ask for a compaction nor see one happen.

Two consequences make that more than a missing convenience:

- **The history changes under the control layer's feet.** An endpoint that
  compacts on its own — on a threshold, or when a provider refuses an
  over-long prompt — rewrites the conversation a control layer is rendering,
  and nothing on the wire says it happened. Every harness that compacts
  natively publishes the moment it does; OAP drops it.
- **The first-party client cannot move onto the protocol without it.**
  `oapx`'s own TUI compacts on `/compact`, on a threshold (`/autocompact`),
  and between turns inside a run, and it holds a message typed during a
  compaction and sends it when the compaction ends. #375 moves that TUI onto
  the agent-control endpoint, and its compaction step waits on this decision.

What a compaction needs from a protocol is what a run already has: admission
against a busy session, one-at-a-time execution, cancellation, sequenced
events, and exactly one terminal. This decision makes a requested compaction a
run rather than inventing a second lifecycle beside the one Decisions 0001,
0002 and 0007 already settled.

## Decisions

### Two features, one unit

- `session.compact` — the control layer may ask for a compaction.
- `run.compaction` — the endpoint publishes the compactions it performs,
  whether asked for or not.

They graduate together as `+compaction`, and an endpoint advertises each at its
own level. `run.compaction` without `session.compact` is coherent and common:
an endpoint that compacts on its own and takes no request to. The reverse is
not: an endpoint that accepts `session.compact` must advertise
`run.compaction` at `native` or `emulated`, because the run it admits is made
of those events.

### A requested compaction is a run

`session.compact.request` carries `session_id` and, optionally:

- `delivery`: `auto` (the default) or `queue`, with the meanings
  `session.message.submit.request` gives them. `steer` and `btw` are refused
  with `unsupported_feature`: a compaction is not guidance into a run, and it
  is not a side question.
- `focus`: text the endpoint should weigh when it writes the summary. It is
  advisory, and an endpoint that cannot honour it advertises
  `session.compact` as `degraded`; a request carrying `focus` to such an
  endpoint needs `session.compact` in `allow_degraded_features`, as any
  degraded control does.
- `continue`: when `true`, the run goes on to a model turn once the history is
  replaced, with no new user message. Absent means `false`.
- `allow_degraded_features`, as on submit and open: the keys of degraded
  controls the caller accepts.
- `metadata`, as on submit.

`session.compact.response` carries the members a submit response does — the
required `session_id`, `accepted`, `submission_id`, `requested_delivery`,
`effective_delivery` and `admission`, and the optional `run_id`, `status` and
`delivery_resolution` — and the same rules decide them. On an idle session `auto`
resolves to `start` and the run starts. On a busy session `auto` resolves to
`queue` only where `session.message.delivery.queue` is advertised, and the
compaction becomes a reservation that promotes like any other under Decision
0007; otherwise it is refused with the wire's `run_active`, exactly as a
second submit would be, and a refusal under any other code is the validator's
`illegal_run_transition`.

Reusing the run is the point. A compaction admitted this way is cancelled with
`run.cancel.request`, holds the session's one execution slot while it runs,
publishes its events in the run's sequence, and settles once. Nothing in
Decisions 0001, 0002 or 0007 has to learn a new kind of work.

### What a compaction looks like on the wire

Every compaction an endpoint performs, inside any run, is bracketed by two
sequenced run events:

- `run.compaction.started`: `session_id`, `run_id`, `compaction_id` (unique
  within the session), `reason`, and optionally `history_tokens`, the
  endpoint's estimate of the history it is about to replace.
- `run.compaction.ended`: `session_id`, `run_id`, `compaction_id`, `outcome`
  (`completed`, `failed` or `cancelled`), and optionally `summary` (a
  `message`), `history_tokens` (the estimate after), and `error` (a
  `protocolError`, required when `outcome` is `failed`).

`reason` is `requested` for a compaction a `session.compact.request` admitted,
`threshold` for one the endpoint started because the history crossed its own
limit, and `overflow` for one it started because a provider refused the
history as too long. A `threshold` or `overflow` compaction happens inside
whatever run was executing — usually a submit's, between two of its turns —
and that run carries on afterwards or fails, as the endpoint's loop decides.

An endpoint compacts on its own **only inside a run**. It never self-admits
one: a run exists because a request admitted it, and a compaction is no
exception. A threshold the history crosses while the session is idle — at a
run's settlement, say — is therefore deferred to the next admitted run, where
the compaction is the first thing the run does after `run.started` and before
its first model turn, carrying `reason: "threshold"`. When that next run was
admitted by `session.compact.request`, its `requested` compaction satisfies
the deferral and no separate `threshold` compaction is published: the run
still carries exactly one compaction, and it is the requested one. So an idle endpoint never
changes the history silently: it either waits for the next run, where the
change is sequenced, or the control layer asks for it with
`session.compact.request`. The TUI's pre-turn `/autocompact` check is the
second case today; on the protocol it may be either.

The history is replaced **only** by an `ended` whose `outcome` is
`completed`. A failed or cancelled compaction leaves the history exactly as it
was, so a control layer never has to reconstruct a half-applied replacement.

### How a requested compaction settles

A compaction run settles through the terminals the core already has:

- **Completed, `continue` absent or `false`:** `run.completed` with
  `stop_reason: "compacted"` and `final_response` set to the summary the
  `ended` event carried. A run whose compaction completed but published no
  summary settles with a `final_response` whose content is empty; the
  `stop_reason` is what says what happened.
- **Completed, `continue: true`:** the run proceeds to a model turn on the
  replaced history and settles as an ordinary run would, with that turn's
  `stop_reason`.
- **Failed:** `run.failed`, carrying the `ended` event's `error`.
- **Cancelled:** `run.cancelled`, after an `ended` with `outcome:
  "cancelled"`, under the same accepted-cancel evidence rule every
  cancellation needs.

### The barrier

Every `run.compaction.started` is followed by exactly one
`run.compaction.ended` with the same `compaction_id`, and both precede the
run's terminal. A terminal published while a compaction is open is a protocol
violation, as is a second `started` before the first one's `ended`, an `ended`
with no `started`, and a compaction event after the run's terminal. A
compaction run admitted by `session.compact.request` carries exactly one
compaction with `reason: "requested"`, and it is the first thing the run does
after `run.started`.

### A message that arrives during a compaction

The TUI holds such a message today and sends it when the compaction ends. On
the wire it is an ordinary `session.message.submit.request` against a busy
session, and the units that already exist decide it:

- With `+queue`, it becomes a reservation behind the compaction run and
  promotes when that run settles — **whether it completed, failed or was
  cancelled**, because Decision 0007 promotes in admission order regardless of
  how the earlier run ended. That is the behaviour the TUI implements by hand.
- With `+steer` and a compaction run admitted with `continue: true`, a steer
  is admitted against the run and applied at the first safe boundary after the
  compaction — the start of the model turn that follows it. A compaction run
  without `continue` has no model turn to steer, and a steer against it fails
  before admission with `invalid_steer_target` and a new reason,
  `details.reason: "no_model_turn"`. T4 closes `not_steerable` to the one
  `cancelling` state and reserves extending the vocabulary for a later
  decision; this is that extension. `no_model_turn` is a lifecycle reason and
  ranks with `not_steerable`, after `cross_session` and `unknown_target`; a
  compaction run that is also `cancelling` reports `not_steerable`, because
  accepted cancellation is the stronger fact.
- With neither, it is refused with the wire's `run_active`, as any submit to
  a busy session is. Holding it is the control layer's choice,
  not the endpoint's.

Apart from the one steer reason, no new rule is needed here, and that is the
reason to model the compaction as a run.

### Continuing after a compaction

`continue: true` is the whole answer to "resume after a compaction with nothing
new typed". An ordinary continue — the user types something after a compaction
settled — is an ordinary submit and needs nothing from this unit.

### Capabilities and conformance

`session.compact` and `run.compaction` join the optional core feature keys.
Their support levels follow `drafts/agent-control-core.md`:

- `native` — the harness compacts on request, or publishes its own
  compactions, and the adapter maps that directly.
- `emulated` — the adapter produces the behaviour from something else, such as
  a harness command that compacts with no structured event (see Evidence).
- `degraded` — for `session.compact`, an endpoint that cannot honour `focus`;
  for `run.compaction`, one that knows a compaction happened but not when it
  began, and so cannot publish `started` before the history changed.

The claim term is `+compaction`, and it requires both features at `native` or
`emulated`.

### Validator

Decision 0003's gate applies in full. The validator gains, at least:

- the two envelope pairs in the envelope `oneOf`, with schemas in
  `schema/v0.1/`;
- the barrier, with a diagnostic for each way it breaks — an unpaired
  `started`, an `ended` with no `started`, a terminal while a compaction is
  open, and a compaction event after the terminal;
- that a run admitted by `session.compact.request` opens with exactly one
  `reason: "requested"` compaction, and that one with `continue` absent settles
  `stop_reason: "compacted"` when its compaction completed;
- that `outcome: "failed"` carries `error`;
- the admission rules above, by reusing submit's: a compaction request is held
  to the same delivery resolution and the same busy-session refusal.

The exact diagnostic codes are the graduation PR's to name, together with the
fixtures that prove each one.

## Evidence

Five pinned harnesses compact natively, and `oapx` is a sixth:

| Implementation | Request | Observation | Ledger |
| --- | --- | --- | --- |
| pi 0.87.1 | `compact` RPC command | `compaction_start` / `compaction_end`, with `compaction_end { aborted: true }` when an abort lands during one | [`research/pi-v0.87.1-mapping.md`](../research/pi-v0.87.1-mapping.md), the RPC command list and the event table, where both events are `observed-only` today |
| OpenCode 1.18.32 | `session.compact` (`POST /api/session/:id/compact`) | `compaction.started`, `compaction.delta`, `compaction.ended` on the session event stream | [`research/opencode-v1.18.32-mapping.md`](../research/opencode-v1.18.32-mapping.md) and [`research/opencode-v1.18.29-mapping.md`](../research/opencode-v1.18.29-mapping.md), where `compaction.*` is `observed-only` |
| Codex app-server 0.157.0 | `thread/compact/start` | not recorded in the ledger | [`research/codex-app-server-0.157.0-mapping.md`](../research/codex-app-server-0.157.0-mapping.md), the thread method list |
| Claude Code 2.1.280 | `/compact` as the turn's text, run as a harness command | settles with `local_command: "compact"`, no model turn | [`research/claude-code-agent-sdk-2.1.280-mapping.md`](../research/claude-code-agent-sdk-2.1.280-mapping.md), the slash-command observations |
| Hermes 2026.8.31 | `session.compress` | `status.update` with kind `compacting` | [`research/hermes-v2026.8.31-mapping.md`](../research/hermes-v2026.8.31-mapping.md); the current pin's ledger (2026.9.24) does not record it, so this row is evidence for a re-pin to confirm, not for graduation |
| `oapx` | `/compact` | `compaction_start` / `compaction_end` on the agent event stream, including between turns inside a run; a message typed meanwhile is held and sent when the compaction ends, whether it completed, failed or was cancelled | `zig/src/agent/types.zig`, `zig/src/tui/app.zig` |

The graduating adapter is **pi**: it is the one pinned harness with a
compaction request, a start/end pair, and recorded cancellation semantics, so
it can produce `native` for both features and exercise the `cancelled`
outcome. `oapx`'s endpoint follows, and is the implementation #375 needs.
OpenCode is the natural third. Claude is `emulated` at best — a compaction it
runs settles with no structured event, so `run.compaction` can be produced
only around a request the adapter issued itself. Codex's request exists but
its events are unrecorded at this pin; confirming them is a ledger task, not a
blocker.

## What this decision does not decide

Each of these is handed to a named owner rather than left implied:

- **The threshold policy** — whether a control layer may read or set the point
  at which an endpoint compacts on its own (`/autocompact` in the TUI). The
  events above make such compactions visible; configuring them is a later
  decision, owned by #613.
- **What a transcript looks like after a compaction** — how `transcript.load`
  represents replaced history. That belongs to the staged transcript unit (T6
  in `drafts/staged-units-graduation.md`).
- **Compaction as a provider-profile concern** — whether
  `model-provider-core` should ever see one. It should not, and nothing here
  gives it a reason to.

## Consequences

A control layer can ask for a compaction, cancel it, watch it, and know when
the history it is rendering was replaced and by what. An endpoint that
compacts on its own stops doing it silently. The TUI's hand-held message
becomes ordinary queue or steer behaviour, and #375's compaction step stops
waiting.

The cost is one request pair and two events. Everything else — admission,
ordering, cancellation, settlement and the held message — is borrowed from
units that are already executable, which is why this unit should be smaller to
graduate than its subject suggests.

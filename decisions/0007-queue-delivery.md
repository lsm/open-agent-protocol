# Decision 0007: Queue Delivery

Status: proposed
Date: 2026-09-16
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `queue` (claim term `+queue`)
Amends: [Decision 0001](0001-agent-control-v0.1-executable-core.md) in the one
respect named below; extends
[Decision 0002](0002-admission-before-start.md) without amending it
Gated by: [Decision 0003](0003-staged-unit-graduation.md)
Design: [Staged Units Graduation Plan](../drafts/staged-units-graduation.md),
section "T2. Queue delivery"

## Context

Decision 0001 allowed at most one nonterminal run per session. Decision 0002
admitted the queued *admission shape* — a run identity reserved at admission
with nothing emitted yet — because real harnesses answer truthfully that way,
but it did not admit overlap: the reservation was still the session's only
nonterminal run.

That left the wire able to say `delivery: "queue"` and unable to mean it. Every
adapter rejected an explicit queue request, and OpenCode — whose
`SessionInput.Admitted` carries `delivery: "queue"` with an optional
`promotedSeq`, and whose durable stream publishes admission and promotion as
separate events — advertised `session.message.delivery.queue` as `unavailable`
while emitting queued admissions of its own. Decision 0003's rule is advertise
it or reject it; this was neither.

This decision graduates the overlap: one started run beside an advertised
number of queued reservations.

## Decisions

### One started run, and reservations behind it

A second admission on a session that already has a nonterminal run is legal
only as a reservation — `admission: "queued"`, `effective_delivery: "queue"`,
`status: "queued"` — and only where the descriptor advertises
`session.message.delivery.queue`. Anything else keeps Decision 0001's rule and
is `illegal_run_transition`.

An explicit `queue` is admitted as a reservation whatever the session holds,
including an idle one, where it promotes at once: "run after current work
reaches a safe boundary" is trivially satisfied when there is no current work.
An explicit `queue` never resolves to `start`. An `auto` a busy session turns
into a reservation reports `delivery_resolution: "session_busy"`, so the
resolution is enforced rather than described. Decision 0002's lone reservation
on an idle session predates this unit and reports whatever it observed at
admission; the resolution rule is scoped to the busy session it names.

### One run executes at a time, in admission order

A reservation promotes by emitting `run.started`, and it may do so only when
every earlier-admitted run of the session is terminal. A later-admitted run
that publishes a sequenced event while an earlier one is nonterminal is
`queue_order_violation`.

The rule is about execution, so it has exactly one exception, and the exception
is the reason it can be exact elsewhere: the pre-start terminal of a run that
never started is published when it happens. Such a run has no execution to
interleave, and its release of a queue slot is capacity a subscriber and the
validator both need to see at the moment it occurs. Holding it was the stricter
reading and it bought nothing: it left a legitimate reuse of a freed slot
indistinguishable from an over-admission, and every repair for that — a
deferral, a declared release, a timestamp comparison — was either too weak to
catch the real breach or too broad to spare the conforming adapter.

Run-scoped requests and responses are exempt too. Cancelling a reservation
before promotion necessarily happens while the earlier run is nonterminal; the
rule governs the endpoint's timeline, not the control layer's commands.

### A queue is a promise, and the bound is what makes it checkable

`capabilities.response` gains `limits`: `max_active_runs_per_session`, the wire
projection of `adapter.Descriptor.MaxActiveRunsPerSession`, bounding the
nonterminal set; and `max_queued_runs_per_session`, bounding the queued subset.

A descriptor advertising the queue above `unavailable` must disclose a positive
`max_queued_runs_per_session`. Without one the limit validation never engages,
and an endpoint could advertise queueing, omit `limits`, and refuse every
queued submission with `run_active` while remaining conforming. `degraded` is
held to the same disclosure: the caller opts in and is entitled to the same
promise. One is enough and `1` is an honest answer, but it must be at least
one, and the wire refuses a nonpositive bound outright.

The two bounds have to agree, or the disclosure is satisfied by numbers that
still forbid what the capability claims: one started run fills an active set of
1, so every reservation beside it exceeds that set and the endpoint refuses
every busy submission while passing both rules. Where the queue is available or
`degraded`, `max_active_runs_per_session` — when stated at all — must be at
least `max_queued_runs_per_session + 1`. Absent is not the same as
disclosed-and-incompatible: an endpoint that never states an active bound has
promised nothing about it. A descriptor that fails either rule is
`undisclosed_queue_limit`.

Both are admission bounds. A refresh that lowers one below a session's current
set grandfathers that set — an endpoint cannot shrink it except by cancelling
work, which a descriptor change must never do — and every admission after the
refresh is judged against the new bound.

### The refusal is validated as well as the admission

The ladder gains a fourth rung, the transient failures, beneath capability,
degradation, and unsatisfiability. It yields to every T1 expectation the same
submit carries: a busy `auto` whose controls are unadvertised, degraded without
the opt-in, or unsatisfiable is owed that refusal instead.

Where the rung applies, the wire's code is `run_active` — the daemon's existing
code, adopted as the protocol's; the research draft's `session_busy` name is
superseded. A busy-session refusal under any other code is
`illegal_run_transition`, and a refusal at a reached bound under any other code
is `queue_limit_exceeded`, so neither can hide behind `internal_error`.

The inverse is checked too, or the rule would only ever tighten. A refusal
reporting a bound that was never reached tells a caller to wait for capacity it
never lacked, and is `queue_limit_exceeded` on the `error.response`.

Both directions are judged across the request/response window rather than at
either edge. `Submit` decides atomically at an instant the trace cannot name,
and both edges of that ignorance produce false verdicts: a queue full at the
decision whose reservation terminates before the response makes a correct
`run_active` look stale, and a queue with room at the request that fills before
the response makes a correct refusal look unfounded. So a bound counts as
reached if it was reached anywhere inside the window, and the stale-limit check
fires only when it was unreached throughout — counting, pessimistically, every
other unanswered submit request on the session, because concurrent submits
contend for the last slot and the winner's admission may not have reached the
trace yet. A missed diagnosis leaves one stale refusal unflagged; a false one
convicts an endpoint that did exactly the right thing under contention it could
see and the validator could not.

`run_active` is not the queue's alone. A busy endpoint that cannot defer a
queued `session_mutation` still owes it once the bound has cleared, so the
stale-limit check stands down where another condition explains the code.

### A snapshot states the position it was captured at

`session.state` gains `active_runs`: every nonterminal run in admission order,
each with its status, its 1-based `queue_position` when it is a reservation,
the position it was captured at, and the unresolved interactions it is blocked
on. `active_run_id` keeps naming the started run, or is absent when only
reservations remain. The field is required wherever `active_run_id` cannot
carry the answer — a reservation, an overlap, or, on an endpoint that puts
entries there at all, a run whose recovery path needs an id only an entry
holds — because an absent field reads to a reconnecting client as an empty
queue.

State reads are not serialized with lifecycle publication and should not be. An
interaction can resolve inside an endpoint before capture while the event
carrying it drains after the state response; a run can settle before capture
while its terminal drains afterwards; a run can be admitted after capture while
its admission reaches the trace first. Each accurate reading would be diagnosed
against the trace as it stands. So the snapshot states what it knew:
`active_runs[].as_of_sequence` for the per-run pending set, and
`session.state.as_of` for the session — the submit requests it reflects as
admitted, the runs it has already removed with the sequence of each one's
terminal, and the last model-affecting event it reflects.

`active_runs` is a list of the session's nonterminal runs, and each entry is
held to that. A terminal status there contradicts the membership it is part of:
a snapshot that knows a run settled drops it and names it in `as_of.settled`
rather than listing it as completed. A run the trace has seen start is not
`queued` at any position from its start onwards, though a capture stated before
that position may still call it queued and is judged there. The converse — a
reservation listed as running — is deliberately not diagnosed: a promotion
happens inside the endpoint and its `run.started` may drain after the snapshot,
which is the race the capture positions exist to allow. And `active_run_id`
names the started run or names none: where only reservations remain it is
absent, because a client reading it as the run to follow would follow a run
that has published nothing.

A stated position the trace has not reached is held and reconciled when it
arrives. A position that never exists is not: a snapshot may describe a
position the trace has not yet seen, but not one the run never reaches, and a
settled run's claimed terminal must be the next envelope its domain publishes —
otherwise a snapshot could drop a live run, name the sequence its terminal will
eventually carry, and be vindicated whenever the run happened to end there.
Ordering throughout is read from the trace and from stated positions, never
from `updated_at_ms`, which is optional and can tie inside one millisecond.

### A reservation's model control applies at promotion

Decision 0005 deferred this here. A queued submit's `model_id` is applied where
the run begins, not where it was reserved: a `session_mutation` must not move
the session default while an earlier run is still started, and a `per_run`
snapshot of that default has to follow any earlier-admitted mutation. The mode
is the one retained from admission, whatever a refresh says while the
reservation waits.

`premature_session_mutation` is the check that binds it: while a run admitted
under `session_mutation` is started, a snapshot reports that run's admitted
model, judged at the position the snapshot states. The marker has a genesis
form, `{"run_id": null, "sequence": 0}`, naming the position before the
session's first model-affecting event; without it the one case the marker
exists for — a session opening on model A, captured just before the first
promotion whose `run.started` reaches the trace first — would be the one case
it could not express.

## Evidence

Fixtures (`fixtures/manifest.json`, unit `queue`): 56 traces covering both
admission shapes and their negatives, the capability gate and its conforming
refusal, the degraded opt-in in all three directions, both disclosure failures
and the wire's refusal of a nonpositive bound, the admission bounds and the
grandfathered set, both directions of the refusal including the window races
and the in-flight reservation, the ordering rule and its one exception, and the
state rules — membership, capture positions, settled claims, and the model
authority. `session.message.delivery.queue` carries a negative `gate` fixture
and a negative `honour` fixture, so the corpus-completeness check binds the
unit from here.

Native evidence: OpenCode graduates the key at `native` on
`SessionInput.Admitted{delivery: "queue", promotedSeq}`, pinned in
[the ledger](../research/opencode-v1.18.29-mapping.md), with the reservation,
its promotion after the started run's derived settlement, and its pre-start
cancellation exercised through the production reducer.

Reference execution: `adapter/memory.go` advertises the key `emulated` with
`max_active_runs_per_session: 2` and `max_queued_runs_per_session: 1`, reserves
one run beside the started one, lists both in `active_runs`, promotes on the
terminal, and settles a cancelled reservation pre-start.

## Consequences

- Decision 0001's "at most one nonterminal run per session" becomes "at most
  one started run and an advertised number of queued reservations". Every other
  Decision 0001 and 0002 semantic is unchanged.
- An endpoint that does not advertise the queue is unaffected, and every
  existing fixture validates unchanged.
- The queue's capability gate moves from the submit request to the correlated
  response, because the wire requires an unadvertised queue to be refused and
  diagnosing the request would fail the conduct the protocol mandates. `steer`
  and `btw` keep the request-side gate until their own unit moves them.
- A caller that previously read `run_active` from a busy endpoint may now
  receive a reservation. The bound is disclosed, so the caller can tell which
  endpoints will do this before it submits.
- The hub keeps a reservation out of the run a bare replay cursor resolves
  onto: it has published nothing, and a client resumed onto a run that settles
  pre-start would be stranded.

## What this unit does not admit

- T2's delivery slice through `serve`: ordered delivery across run domains,
  `?follow=session`, the `oap-run-boundary` and `oap-stream-end` signals, and
  the resume cursor's interleave member. A queued admission and the state that
  describes it round-trip through the hub, both codecs, and both clients; a
  subscription reading one run is not yet insulated from a second domain's
  envelopes, so a caller that queues alongside a live subscription reads the
  reservation's settlement from `active_runs`. Those changes land with the
  units that need them, and this decision is amended when they do.
- More than one reservation at a time on any adapter in this repository. The
  wire bounds are per endpoint and the validator enforces whatever is
  disclosed; the reference adapter and OpenCode both disclose one, because that
  is what their settlement models support.
- Explicit `steer` and `btw` requests, `admission: "steered"` and
  `"side_started"`, and concurrent *started* runs. Cancellation scope still
  targets one execution.
- A route that withdraws one queued input on OpenCode. The pin has none, so
  cancelling a reservation there is adapter-local and the server may still
  execute what OAP reports cancelled; the ledger records it rather than
  compensating for it.
- Steer-dependent state: `pending_steers`, `settled_steers`, and the per-entry
  anchor rules that judge them. `active_runs[].admitted_submit_requests` lands
  here with the wire and is held to naming submit requests the trace carries;
  what it disambiguates arrives with the steer unit.

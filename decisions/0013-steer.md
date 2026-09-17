# Decision 0013: Steer

Status: proposed
Date: 2026-09-17
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `steer` (claim term `+steer`; plan sub-unit T4)
Amends: nothing. Extends
[Decision 0001](0001-agent-control-v0.1-executable-core.md) and
[Decision 0002](0002-admission-before-start.md) without amending either, and
sits beside [Decision 0007](0007-queue-delivery.md), which graduated the other
non-`auto` delivery mode
Gated by: [Decision 0003](0003-staged-unit-graduation.md)
Design: [Staged Units Graduation Plan](../drafts/staged-units-graduation.md),
section "T4. Steer"

## Context

A control layer cannot say anything to a run that is already going.

`session.message.submit.request` admits a run. `run.cancel.request` ends one.
Between those two there is nothing: no envelope carries guidance into a run in
flight, and an operator who sees an agent heading the wrong way can only watch
it finish or kill it. Every interactive harness in this repository solves this,
and OAP expresses none of their solutions.

This is the unit with the most native evidence of anything ungraduated, and it
has been staged since the first graduation plan without a decision. The plan
names it the one unit that "adds a new pending lifecycle inside a run", which
is why it was ordered last and why it has stayed there.

## Decisions

### The shape is the one the plan pre-designed

`drafts/staged-units-graduation.md` section T4 is an unusually complete
pre-design — admission, settlement, state, barrier, and the refusal vocabulary,
each with its reasoning. This decision adopts it rather than restating it, and
that section becomes normative on acceptance.

Adopting rather than redesigning is deliberate. The pre-design was written
against four harnesses' behaviour and has been revised as pins moved; a fresh
shape written now would be a worse one arrived at with less evidence. Where
this decision departs from it, it says so below and says why.

The load-bearing parts, for a reader who will not open the plan:

- **Admission** is `delivery: "steer"` with an optional `target_run_id`,
  answered `admission: "steered"`, `effective_delivery: "steer"`, `run_id` set
  to the target, and `target_sequence` naming the target run's last emitted
  sequence at admission. A steer admits no run, so it carries no run controls:
  `model_id`, `instructions`, `tool_choice` and `output_schema` are refused
  before admission, because the target run's admitted controls are
  authoritative until its terminal.
- **Settlement** is two new run-scoped events in the target's sequence domain:
  `run.steer.applied`, carrying the boundary at which the guidance took effect,
  and `run.steer.dropped`, carrying why it did not.
- **The barrier** is that every admitted steer settles before the run's
  terminal. A run that terminates first drops its pending steers with
  `run_terminated`, in the run's sequence, before the terminal.
- **`auto` never resolves to `steer`.** Steering is always explicit.

### Steer is settled, not merely admitted

The single most important thing this unit adds, and the reason it could not be
modelled on `+queue`.

A queued submission gets a run of its own, so the control layer learns it took
effect by watching `run.started` arrive. A steer creates no run and no sequence
domain, so nothing in the existing vocabulary would ever tell the caller
whether its guidance reached the agent. Every native source makes application
observable in a different way and at a different time — Pi at the next turn
boundary, Hermes immediately in the submit result, OpenCode when the steered
input is promoted — so an endpoint cannot be asked to report a uniform moment,
and a caller cannot be asked to infer one.

So the settlement carries `boundary` ∈ `immediate` | `turn` | `tool_result` |
`unknown`, and `unknown` is a first-class value rather than an admission of
defeat. An endpoint that genuinely cannot say when the guidance landed says so,
and a caller that needs to know can refuse to use such an endpoint for steering
— which it cannot do if the field is absent and the answer is guessed.

### A steer is attributable to the caller that sent it

`run.steer.applied` and `run.steer.dropped` carry `request_id`, the envelope id
of the submit request the admission answered, so a settlement is attributable
without the caller ever having seen the admission response. This is the
recovery case the plan spends most of its length on, and it is real: a caller
that lost its response has minted a request id and knows nothing else.

**This has a prerequisite that has not landed, and this decision is blocked on
it.** See Evidence.

### What this decision does not take from the plan

The plan's `settled_steers` surface on `session.state` — a session-level,
bounded, `complete`-flagged history of settled steers that outlives its run's
terminal — is **deferred to its own decision** rather than graduating here.

It is the right answer to a real problem: a caller whose cursor fell outside
the replay window reaches the run's terminal having seen neither the admission
nor the settlement, and absence from `pending_steers` then proves nothing.
But it is also a new session-scoped recovery surface with its own retention
bound, its own completeness marker, and its own validator rules, and it is
reachable only by a caller that has already lost its stream. Graduating it
inside T4 would make the unit's evidence requirement hostage to a surface no
harness has native behaviour for.

Without it, the recovery rule this unit ships is narrower and says so: a caller
whose view of the target run is gap-free may treat absence at the terminal as
conclusive, and a caller whose view has a gap may not. `*adapter.ReplayGap`
already tells a caller which one it is.

## Evidence

Four harnesses, three of them pinned, and the corpus already carries the
frames.

**Pi** has `steer` and `follow_up` as distinct commands, a `queue_update` event
that publishes both queues, `clear_queue` to withdraw before application, and
`set_steering_mode` / `set_follow_up_mode` (`all` | `one-at-a-time`). The
pinned corpus case `native-controls` carries the round trip —
`{"type":"steer","message":"adjust"}`, its response, and the `queue_update`
showing `steering: ["adjust"]` — classified **`required-unmapped`**, which is
this repository's term for a native frame OAP needs and has nowhere to put.
The evidence for this unit is already pinned and already failing to map.

**Hermes** applies a steer immediately and reports it in the submit result:
mid-turn submits return `{"status":"steered"}` under `display.busy_input_mode:
"steer"`, beside `{"status":"redirected"}` and `{"status":"queued"}` for the
other two modes. It also has `subagent.steer`, so steering is not only a
session-level affordance there.

**OpenCode** has `steer | queue` as its *native delivery vocabulary* — the same
two modes OAP is graduating, named the same way, with an omitted default. Its
ledger records the mapping to OAP `auto` as needing one pinned decision.

**Codex** has `turn/steer`, and it is **not** evidence at this pin: the pinned
ledger defers it, and the research that describes it is unpinned. It is named
here so a later re-pin knows to revisit rather than rediscover.

Three independent, pinned implementations with materially different
application semantics is more native evidence than `+models`, `+queue` or
`+tool-sources` had when they graduated. What this unit has lacked is not
evidence but a decision.

**The blocker.** `adapter.Session.Submit` still takes a bare
`protocol.MessageSubmitRequest` (`adapter/adapter.go:23`). The plan assigned
the change to `adapter.SubmitRequest { Request, EnvelopeID }` to **T2**, which
needed it for queue capture markers; T2 graduated as Decision 0007 without it.
So no adapter can populate `request_id` on a steer settlement today, because
no adapter is told the envelope id of the submit it is answering.

That change is a compile-time break across all nine implementations of
`adapter.Session`, and it is meant to be: an adapter that ignores the new
member keeps compiling only because it does not emit correlated events. It is
a prerequisite of this unit and not part of it, and it should land on its own
so the break is reviewable separately from the semantics. **This decision
cannot be accepted until it has.**

## Consequences

The interactive gap closes. An operator watching a run go wrong gains something
between watching and killing, which is the single most requested thing a
control layer can do and the reason three of eight harnesses built it natively
before any protocol asked them to.

`+queue` and `+steer` become the two explicit delivery modes, and `auto` never
resolves to `steer`. Decision 0002 resolves `auto` to `start` or `queue` and
Decision 0007 turns a busy session's `auto` into a reservation, so `auto`
already reaches one of the two; adding `steer` does not change which. A caller
that wants a turn interrupted asks for it. Decision 0007's rule that an
explicit mode never changes meaning extends to `steer` unchanged.

Three adapters gain a capability they currently classify as unavailable
despite native support: Pi's ledger records steering and follow-up queues as
"first-class natively but unavailable in the current OAP surface", OpenCode's
as blocked by "OAP v0.1 admission", and Hermes's busy-input modes as observed
but unclaimed. Each becomes an ordinary advertised capability with disclosed
limits.

Two envelope types are added, both gated on the unit's capability key, so an
endpoint that does not advertise steering neither sends nor receives them and
its surface does not grow.

The run lifecycle gains a pending state that is not an interaction. A steer is
not resolved by a participant and has no responder; it is applied or dropped by
the endpoint. The validator's interaction machinery therefore does not govern
it, and the barrier rule — settle before the terminal — is enforced separately
and for a different reason: an interaction may not outlive its run because
someone owes an answer, and a steer may not because the caller is owed an
outcome.

## What this unit does not admit

Withdrawal. Pi has `clear_queue` and no other pinned harness has anything like
it; a cancel-the-steer envelope on one implementation's evidence would be a
vocabulary three endpoints would have to refuse. A caller that changes its mind
lets the steer settle and submits again.

Steering a queued run. `not_steerable` is not the reason for it —
`target_sequence` has no meaning against a run with no emitted sequence, and
the guidance would bind a run whose controls are not yet fixed. A steer targets
the started run or fails before admission.

Steering modes. Pi's `all` versus `one-at-a-time` and Hermes's three
busy-input modes are endpoint policy, disclosed through capability limits if an
endpoint wants them visible, not a control the wire carries.

Multiple pending steers as an ordered queue with positions. `pending_steers`
lists what is admitted and unsettled; it does not promise an application order,
because the three harnesses apply at three different boundaries and two of them
can coalesce.

# Decision 0002: Admission Before Started

Status: accepted
Date: 2026-09-10
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Amends: [Decision 0001](0001-agent-control-v0.1-executable-core.md) ("One
foreground invocation is one run") in the one respect identified by
[protocol feedback PF-1](../research/protocol-feedback-2026-09.md)
Evidence: eight pinned adapter ledgers; three executable corpus cases pinned
outside canonical validation pending exactly this extension

## Context

Decision 0001 required every accepted submission to resolve immediately to
`admission=started`, `effective_delivery=start`, and forbade every run-scoped
event — including terminals — before `run.started`. Real harnesses break both
halves of that rule in ways adapters cannot normalize away:

- OpenCode admits prompts without a promotion sequence (`admission=queued` is
  the truthful answer), and a 409 conflict or a foreign-aggregate event
  settles the reserved run before any start is observable.
- Hermes reports busy-submit statuses (`steered`/`redirected`/`queued`).
- Claude Code submissions legally settle on a later result than the first one
  after the send (`queued_turn_count`/`still_queued`).
- pi carries first-class steering/follow-up queues.

Three OpenCode corpus cases (`queued-admission`, `message-conflict`,
`foreign-session`) pinned these shapes as recorded v0.1 mismatches. This
decision admits the narrow slice the evidence supports. It does not admit
steer, btw, side runs, or concurrent foreground runs; those deferrals stand.

## Decisions

### Two canonical admission shapes

An accepted `session.message.submit.response` resolves to exactly one of:

1. **started** — `admission="started"`, `effective_delivery="start"`. The
   `run.started` event for the returned `run_id` is emitted atomically with
   the response; no run-scoped event may precede it. This is the only shape
   Decision 0001 admitted; every existing canonical trace keeps it.
2. **queued** — `admission="queued"`, `effective_delivery="queue"`,
   `status="queued"`. The `run_id` is reserved at admission; no run-scoped
   event has been emitted. The reservation holds the session: no second
   submission is accepted while it is nonterminal.

Any other combination — `steered`, `side_started`, `queued` with
`effective_delivery="start"`, `started` with `effective_delivery="queue"`,
or an admission whose `status` contradicts its shape (`started` must report
`status="running"`; `queued` must report `status="queued"`) — remains
rejected in this subset.

### Promotion

A queued run promotes by emitting `run.started` as its first run-scoped
event; every Decision 0001 rule then applies unchanged (contiguous sequence,
deltas, tools, interactions, one absorbing terminal). A run that reported
`admission="started"` emits `run.started` first under the same rules.

### Pre-start settlement

An accepted run may settle before `run.started`: its first and only
run-scoped event is one terminal —

- `run.failed`, or
- `run.cancelled`, behind an accepted `run.cancel.request`/`response` pair as
  in Decision 0001.

`run.started` is then never emitted for that run. Pre-start settlement is
legal under either admission shape: asynchronous harnesses can report
`started` at admission (their promotion evidence) and still fail before the
adapter has observed a start; the response's admission value records what the
harness reported at admission time, while the event trace records what was
actually observed.

A run cannot complete before an observed start: pre-start `run.completed`
remains invalid, because completion without an observed start has no faithful
projection.

Non-terminal run-scoped events before `run.started` remain illegal. A
pre-start-settled run satisfies the terminal requirement with its single
terminal; the "admitted run never emitted `run.started`" rule applies only to
runs that neither started nor settled.

### Unchanged

Sequence domains, the one-terminal invariant, cancellation intent-versus-
settlement, resume/reconciliation/replay separation, capability disclosure,
and all other Decision 0001 semantics are unaffected. The v0.1 schema bundle
required no change: `admission: "queued"`, `effective_delivery: "queue"`, and
`status: "queued"` were already schema vocabulary; this decision makes their
composition canonical.

## Consequences

- The validator accepts the two admission shapes and pre-start terminals, and
  rejects pre-start non-terminal events, pre-start completion, a second
  terminal after a pre-start settlement, mixed admission/delivery/status
  claims, and unstarted-unsettled admissions.
- The three pinned OpenCode mismatch corpus cases become canonically valid
  evidence; the OpenCode conflict-admission path returns an accepted queued
  response with the failure on its stream instead of an error plus a dangling
  stream.
- Adapters that gate admission on start evidence (DeepSeek, Claude Code, pi)
  are unaffected; their `started` admissions remain the high-fidelity path.
- Consumers reconciling from events alone must tolerate a run whose only
  event is a terminal; the submit response carries the admission claim.

## Clarification: why the submission/run split survives at the narrowest fidelity

This section records reasoning this decision already relied upon. It states no
new rule, narrows nothing, and leaves every ruling above exactly as accepted; it
exists because the reasoning was load-bearing and unwritten, and a reader who
reconstructed it differently would reach a different implementation.

The decision's Consequences note that endpoints gating admission on start
evidence — DeepSeek, Claude Code, pi, and every adapter like them — "are
unaffected; their `started` admissions remain the high-fidelity path". For such
an endpoint, `queue`, `steer`, and `btw` are all unavailable, the queued
admission shape can never occur, and admission collapses to started-or-rejected.
The submission/run split then looks like ceremony: two identity domains, a
response shape carrying `admission`, `requested_delivery`, and
`effective_delivery`, and only one answer any of them can ever give. The
temptation is to let those endpoints collapse the identities — return the run as
the submission, or omit the split entirely. Two reasons it stays.

First, the split still carries information even there. A rejected submission
carries a `submission_id` and no `run_id`: `admission="rejected"` is a real
answer about a real thing that happened, and it is precisely the case where no
run exists to name it by. Collapsing submission identity into run identity
leaves that answer with nothing to attach to, so the endpoint has to invent a
run that never ran or answer out of band. The split is not paying for the queued
shape; it is paying for the boundary between "the endpoint took your message"
and "the endpoint started work", which exists at every fidelity because refusal
exists at every fidelity. That a started-or-rejected endpoint only ever uses two
of the split's positions does not make the split unused.

Second, and decisively: uniform trace shape across endpoint fidelities is the
point of the capability model. Capabilities exist so that what an endpoint *can
do* varies while what its traces *look like* does not — a consumer reads one
shape and learns an endpoint's limits from the descriptor, not from the shape of
what arrives. Letting an endpoint collapse identities because its fidelity is
narrow inverts that: it saves one endpoint a little ceremony and charges every
consumer a second shape to parse, a second correlation path to test, and a
branch that can only be exercised against the narrow endpoints. The cost is
borne by everyone who did not choose it, permanently, and it scales with the
number of fidelities, while the saving is one field an adapter sets once. The
capability model is how an endpoint reports being narrow. Structural divergence
is not.

## Deferred work unchanged from Decision 0001

Durable idempotent admission, cross-replica identity mapping, contextual and
provisional capability composition, queue/steer/btw delivery *requests*,
concurrent foreground runs, continuity leases, orphan terminals, and
first-class subagent and background-task lifecycles remain deferred.

# Decision 0041: Cancel Acceptance Is Judged When the Cancel Is Checked

Status: accepted 2026-10-06 (both validators judge the three fixtures it names,
`core-cancel-accepted-after-natural-completion`,
`cancel-accepted-after-late-request` and `core-cancel-late-request-refused`, from
#463; the Go validator no longer refuses a late request; the announcement order
it leaves open is #901)
Date: 2026-09-28
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Amends: [Decision 0001](0001-agent-control-v0.1-executable-core.md), whose
"Cancellation intent is not settlement" paragraph gains the two clauses below
Gated by: [Decision 0003](0003-staged-unit-graduation.md)
Evidence: the pi parity flake on #10, the differential run that surfaced it,
and three fixtures that were needed to say any of this at all

## Context

Decision 0001 says that a `run.cancel.response` "acknowledges whether
cancellation was accepted for processing; it is not a run terminal", that
"natural completion or failure may win a race with cancellation", and that
"cancellation of a completed or failed run returns a typed
`run_already_terminal` error". Those three sentences had to be reconciled with a
validator that read the same words differently, and a real harness does
produce the order that made the difference visible: a pi agent can settle a
turn and answer an abort in either order, so a cancel can be *accepted* while
the run has already completed.

Read as "completed or failed **at the moment the cancel is checked**" — which
is what "cancellation of a completed or failed run" means, since the run's
state is what a cancel request is checked against — the three sentences agree,
and the answer is the acceptance. Read as "at the moment the *answer* is
written", a cancel response becomes a claim about the run's state, which is the
one thing 0001 says it is not.

The validator read it the second way: it raised `illegal_run_transition`
whenever an accepted response arrived for a run that was already terminal with
a terminal type other than `run.cancelled`. Since a machine is
order-sensitive, that made the *legal* order — request while running, natural
completion, then the acceptance — the illegal one, and the illegal order
legal. Six adapters were changed to answer the way 0001 reads, and the
validator then had to change with them.

## Decisions

### Acceptance is judged when the cancel is checked, and a trace shows that as the request's position

An accepted `run.cancel.response` is **illegal only when the request it answers
arrived after the run's non-`run.cancelled` terminal.** A request that arrived
while the run was live may be answered accepted afterwards, even if a natural
completion has since settled the run: the response acknowledges that the cancel
was accepted for processing, and the completion won the race — which 0001 says
it may.

The trace is what makes this decidable, because the request and the terminal
both carry a position: the request's, and the terminal's `sequence`. A judge
compares them. It does not need to know how the two were scheduled, and it does
not depend on where the response lands.

### A cancel response is unordered against the run's stream

A `run.cancel.response` carries no `sequence`, and 0001 makes it not a run
terminal. Its position relative to the run's sequenced stream is therefore
**unconstrained**, in both directions: an acceptance may precede or follow the
completion it lost the race to, and a judge may not read anything into the
order. That is the second clause, and it is the one that makes the first
implementable: the response cannot be the thing that decides legality, because
it is not ordered with the thing legality is about.

### A late cancel is a legal request with a typed answer

0001 already gives a late cancel a defined answer — `run_already_terminal` — so
the *request* is legal and the refusal rides on the answer. Both validators
now record a request that arrived after a non-`run.cancelled` terminal as late,
and neither refuses the request itself. The Go validator used to refuse it at
the request (`cannot cancel a completed or failed run`); it no longer does, so
the two trees judge the same trace the same way, and the refusal is an
`error.response` a client can branch on.

## Consequences

- Three fixtures, because nothing covered either order:
  `core-cancel-accepted-after-natural-completion` (valid — the race, won by
  completion), `cancel-accepted-after-late-request` (semantic-invalid,
  `illegal_run_transition`) and `core-cancel-late-request-refused` (valid — the
  request is legal, the answer is `run_already_terminal`).
- A settled run's recorded status is never rewritten by a cancel response that
  follows it. The response carries the acceptance; the stream carries the
  settlement; neither overwrites the other.
- The pi parity scenario can no longer flake on this: the answer no longer
  depends on how far the reader had got when the harness replied.
- **The announcement order is not settled by this record, and is not policed by
  it.** Go's pi announces `run.status.updated {status: cancelling}` and then
  answers; the Zig pi adapter cannot, because its only flush point is `drain`,
  which the host calls with its own event list, so the announcement reaches the
  stream after the answer. Both orders are legal under the clause above, so the
  two trees differ in output order and in nothing else. Aligning them is an
  endpoint ordering change, not an adapter one, and it is left to whoever owns
  that call.

## What this decision does not admit

- **A second terminal.** A cancel response never settles a run, and never
  rewrites one that has settled.
- **Retroactive acceptance.** A request that arrived after the run settled is
  not accepted later; it is refused, with the code 0001 already names.
- **Ordering between runs.** Unchanged and unconstrained: a subscription may
  interleave two runs' envelopes freely, and this record says nothing about it.

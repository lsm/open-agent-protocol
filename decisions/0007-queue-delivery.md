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

A refresh is how they move at all. `capability_revision` identifies exactly one
descriptor, and every envelope citing it is bound to that snapshot, so a
capabilities response repeating the active revision is claiming to be that same
descriptor: the bounds it states must be the ones already disclosed, and so
must the level it advertises the queue at. A bound that rises underneath a
revision excuses an overflow the disclosed numbers forbid, one that falls
convicts an admission the caller was invited to make, and a level that moves
turns this unit's rules on or off — each of them judging later traffic against
a descriptor nobody holding that revision was ever shown. The diagnosis is that
the revision did not move where the descriptor did
(`stale_capability_revision`), raised on the field that changed, because the
fix is to introduce a new revision for it. An announced change is exempt and
must be: after `capabilities.updated` the revision has already advanced, and
the mandatory refresh that follows is the new descriptor rather than a
contradiction of the old. It is the rule `models.list` is held to under one
revision, applied to the fields this unit owns.

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

Which state condition applies is itself a fact about the window, not about the
instant the request arrived, so it is decided at the response rather than
retained from the request: a submission made on an idle session whose session
or queue fills before its response is owed the answer an identical submission
made a moment later is owed. The rung stays the lowest — it judges only a
response no capability, degradation, or unsatisfiability claimed.

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

These rules are about the session-state document, not about the envelope that
carries it. `session.open.response` is that same document — `SessionOpenResponse`
is `SessionState`, and the schema defines the open response as a state
document — and a reattaching client reads it as its initial state before any
`session.state` exchange exists, so a rule keyed to the response type rather
than to the document is a rule with a hole where reconnection lives. A recovered
open response is also the one place a state document introduces runs rather
than describing runs the trace carries: it joins a session already under way
and is authoritative about its runs by construction, there being no earlier
admission for it to contradict. So the runs it lists are taken from it, and the
document is then held to everything that does not depend on where they came
from — that it lists each of them once, that none of them has already settled,
that it names the one it says is executing and only one, and that its own
status agrees with the rest of it. An open response that names runs without
declaring a recovery declares nothing that could have created them, and its
entries are runs from nowhere like any others.

Taking a run from that document means taking two things from it. Where its
trace picks up is the position the entry states, then the cursor the recovery
resumed from, then the beginning — one question asked of one recovery block,
so both recovery paths read it the same way. The cursor belongs to the run the
recovery names, so a reservation listed beside that run was never at that
position and starts from the beginning; a declared gap states no position at
all, its cursor being the retained boundary the endpoint could not serve from
rather than where the events that follow come from. And what the run *is* is
read from the entry's shape rather than from its status alone: `queued` is a
reservation, `cancelling` says nothing about whether the run began and answers
with the queue place it reports holding, exactly as it does where the trace has
not reached the run's start, and anything else is a run that began before
everything this trace can see. `active_runs` is required only where
`active_run_id` cannot carry the answer, so a reattach holding one started run
says so with the pointer alone; that run is introduced by the document exactly
as an entry would be, or the retained replay that may follow the open response
directly has no admission behind it and no position to resume from. So does the
first `session.state.response` after a reattach, which is the other document a
recovery can introduce its run through.

Introducing a run means more than putting it in the run table. Every accounting
and ordering rule in this unit reads the session's admission order, so a run
recorded only in the table leaves the session looking empty: a second started
admission beside it overlaps nothing and outruns nothing, and the recovered run
it is overlapping is nonterminal the whole time. A recovered run therefore
enters the order, at a position, and the windows of every submission still in
flight are recomputed around it — everything an admission does except being
one. The one run this does not apply to is the placeholder made for a run event
with no admission at all: admission order is exactly what that run does not
have, and the trace has already been convicted for it.

What the document cannot state is the other half of the same rule. A recovered
run did everything it has done so far behind the cursor, so for any state that
predates it the honest position is that the validator knows nothing — not that
there is nothing. The difference is between an empty expected set and no
expectation at all, and every rule in this trace that derives what to expect
from history will derive the empty one and convict the endpoint for answering
the reattach truthfully.

Where the document speaks, it is believed: an entry naming the interactions its
run is blocked on names ones whose requests are behind the cursor, so they are
recorded as pending from before every position this trace can state, and
recorded opaquely — naming an interaction is not describing it, so its
ownership, kind, questions, choices and tool binding are unknown and nothing is
held to them. What this trace does see of them is judged as ever: they are
resolved once, and they leave the pending set where the trace says they do.

Where no document speaks, nothing is concluded. A recovery that introduces its
run through the pointer alone says nothing about what that run is blocked on,
so an interaction id this trace has never carried for it is neither evidence
that the interaction exists nor evidence that it does not. Two more things no
document can ever state: the tool calls a recovered run had open, so a call
whose request is behind the cursor is one that opened before it rather than one
that never opened, and its lifecycle is picked up from what this trace does
see; and the cancel exchange that accepted its cancellation, so a `run.cancelled`
for a reattached run does not owe an acceptance this trace could not have
carried. Everything else a `runState` holds is either stated by the document —
the run's identity, its session, whether it had begun, where its trace resumes,
its status, its place in admission order — or is genuinely about this trace and
so starts empty because it is empty: its terminal, its own sequence cursor, the
position it started at within this trace.

The model such a document states is the session's opening one. An open response
is a snapshot taken before anything can have moved the default, and a capture
marked at genesis is judged against the opening value alone — so without it the
first snapshot of a session answers to nothing, and the first
`session_mutation` behind it stops that value from ever being learned. Only a
model the document states: what an absent member means is a separate question
and is left where it was.

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
held to that. A run that had settled before the read was even requested cannot
be in it under any capture position, so listing it is a stale snapshot rather
than a race; one that settles inside the window may still be listed, because
the snapshot may have been captured before a terminal it could not have seen —
unless the entry's own position says otherwise. An entry stating the sequence
the run settled at, or one past it, has said it read the run as far as the
envelope that ended it, and then listed it as outstanding anyway.
A terminal status there contradicts the membership it is part of:
a snapshot that knows a run settled drops it and names it in `as_of.settled`
rather than listing it as completed. A run the trace has seen start is not
`queued` at any position from its start onwards, though a capture stated before
that position may still call it queued and is judged there. A reservation
cancelled before promotion is listed too, and listed as what it is: the status
goes `queued` to `cancelling` without passing through `running` — Decision 0001
has a run settle failed or cancelled before `run.started` but never completed —
and it keeps the place it still holds until its terminal. Cancelling is the one
nonterminal status that says nothing about whether the run began, so unlike
queued it cannot classify an entry by itself; the trace decides, at the
position the entry states. Where the trace has not reached that position, the
entry answers for itself with the field the classification governs — a queue
position beside the stated position claims the run had not begun there, and its
absence claims it had — and the answer is held against the start when it
arrives, or against the trace ending without one. Reading such an entry as a
reservation outright is a false verdict at one edge of the window, since it
lets a snapshot keep a queue place past a promotion that has not drained, and
refusing to is a false verdict at the other, since it rejects the accurate
entry of a run cancelled after one. An entry stating no position at all claims
no knowledge the trace lacks and is read as the trace stands. Its queue position
is judged there too, and so is `active_run_id`: an entry's status, its position
and the field that names the started run are one description of one moment, and
reading the position off the trace's current idea of which run has started
judges a single snapshot against two moments at once. That shows on both edges
of a promotion inside the window — a snapshot taken before it reports the
reservation, with the place it held and no started run to name, and one taken
after it reports a started run holding no position — and both are accurate.
What an entry cannot do is invent a queue: a run admitted started was never in
one.

A listing is one moment, and in one moment a session has one started run —
the whole of Decision 0001 this unit kept. It also runs them in admission
order, so a reservation admitted first cannot be queued behind a run admitted
after it; a listing that says so describes no moment at all, and no capture
position excuses it. A promotion crossing the window lets
either run be the one the snapshot describes, never both, so a second entry
describing a run as executing describes a moment that never existed and is
diagnosed rather than silently replacing the first.

`status` is read from that same listing. A snapshot listing a started run is
running, or waiting on an interaction that run raised, and which of the two is
the run's own business: session status describes the started run, so the two
agree or one of them is wrong. What counts as waiting is read from the entry
rather than from the trace, because an interaction can be raised or resolved
inside the window and a snapshot is entitled to have caught either edge; the
entry says so with its own status or by naming what it is blocked on, and
naming it is not free, since the pending set is judged against the run's own at
the position the entry states. Evidence read one way is evidence read the
other: a session calling itself running beside an entry that names what it is
blocked on contradicts that entry exactly as much as one calling itself waiting
beside a run that reports nothing. A session holding only reservations is
queued. A reconnecting client reads the three fields at once,
and a snapshot that lists a reservation while calling itself idle, or lists an
executing run while calling itself queued, hands it a session that never
existed and lets whichever field it happens to trust decide what it does. A
listing with no entries keeps whatever status it reports, because an empty
listing is what a closed or errored session carries too and those say something
the runs cannot.

Excusing an omission is not agreeing with it. A snapshot requested before a
promotion may name no started run, and that is the race; but the run did start,
nothing reconciles the omission afterwards, and a snapshot allowed to drop the
run must not also take it away from every snapshot that follows. The exemption
covers the read that could not have seen the start, not the ones after it. A
claimed settlement is different: there the snapshot asserts the run is over and
answers for that assertion on its own terms, so the omission is agreed with
rather than excused.

Which run the validator holds a snapshot to is the same question, so a
reservation is not it. The run a snapshot may not erase is the started one, and
that pointer moves where the run begins rather than where it was admitted: a
reservation recorded as the session's active run turned a correct snapshot of
an idle explicit queue — no started run, because none had started — into an
erasure of a live one. The promotion moves it, and a run that began after the
read was requested is exempt, for the reason every other capture rule is. Where the start has
not arrived yet, the claim is deferred rather than accepted: the position the
entry states may be one the run turns out to be running at, and only its start
decides that. Deciding it at the response instead would let a snapshot name a
position past a promotion that has not drained, call the run queued there, and
never be judged at all. The converse — a
reservation listed as running — is deliberately not diagnosed: a promotion
happens inside the endpoint and its `run.started` may drain after the snapshot,
which is the race the capture positions exist to allow. And `active_run_id`
names the started run or names none: where only reservations remain it is
absent, because a client reading it as the run to follow would follow a run
that has published nothing. Which run that is, is read off the listing the
snapshot carries, not off the set the trace requires it to carry. The two
differ by exactly the run that settled inside the window and was allowed to
stay listed, and judging against the required set would demand that a snapshot
listing such a run as started leave `active_run_id` empty — the same run named
`running` in one field and absent from the other. So a listing that shows a
started run and names none disagrees with itself and is
`session_state_mismatch`.

`admitted_submit_requests` naming a submission the endpoint has not yet
answered is a claim of the same kind, and it is retained rather than waved
through. The response decides it: a refusal means there was never an admission
to reflect, an admission to another run means the snapshot attributed one
session's work to the wrong place, and a response that never comes leaves the
claim resting on nothing. So does an admission the snapshot showed nowhere: a
capture saying it already reflects an admission has to account for the run that
admission created, in the listing or among the runs it says it settled, or the
anchor buys it an exemption from listing the very run it claims to know about.

Accounting for it is what an entry naming that run does, even before the trace
carries the run at all. A snapshot may lead an admission it made: the endpoint
knows the run, the response that will tell the trace about it is still in
flight, and the entry names the submission it came from. That anchor is what
licenses the claim, so such an entry is held and reconciled against the
response — the request must have been admitted, to that run, on that session —
rather than rejected before its own anchor can be read. An entry naming a run
from nowhere, anchoring nothing, is still a run from nowhere.

What the lead buys is time for the one thing the response says: which run that
submission became. It does not buy the entry an exemption from what it says
about itself, and the price of the lead is paid by the entry taking it rather
than by the listing around it. What the entries the trace does carry already
establish is still established: a listing that says a run is executing has
named that run whatever the pending entry turns out to be, and a session with
an executing run in it is not idle.

The entry's own status is not something an admission can change, so every rule
that reads the status alone reaches it. `active_runs` is the nonterminal set
however a run was admitted, so a terminal status there is wrong before the
response arrives and no response could right it. A reservation is listed
`queued`, so an entry that is neither queued nor terminal says its run is
executing — which makes it the started run of that listing, owed by
`active_run_id` and denied by an idle session, and a second one beside a run
the trace already carries as executing describes a moment that never was.

Two statuses settle nothing without the run's own history, and those are the
ones that stand the classification down: `queued`, because whether this run is
a reservation is exactly what the admission will say, and `cancelling`, because
it says nothing about whether the run began and there is no start yet to settle
it against. Under one of those the listing stands down on the two questions the
pending entry could genuinely answer differently — whether the session holds
nothing but reservations, since the pending run may be the started one, and the
session status that follows from it. Its position and pending set are not
judged either: they describe a moment before its run's admission reached the
trace, and the per-entry rules have nothing to compare them with there.

Its place in the listing is local after all, for a reason worth stating: a lead
anchors on a submit request the trace carries unanswered, so the response that
admits its run comes after every admission the trace already has. Whatever its
place in the queue turns out to be, its place in admission order is last, and a
run the trace does carry, listed behind it, is out of order on the listing's own
terms.

Standing a rule down is waiting for the answer, not forgiving the question.
The admission response is that answer, and it settles the entry as well as the
identity: the two are one claim, because what the response says — which run
this submission became, and on what terms — is exactly what the entry's own
rules were missing. A run admitted started was never in a queue, so the queued
status such an entry reported was never true of it at any position and the
place it gave itself was in a queue it never entered. A run admitted queued is
a reservation, so the place it claims is the one the listing left for it. A
cancelling entry asks round 14's question with a run to ask it of, and the
start it waits for settles that in turn: two answers in a chain, each from the
event that carries it.

Where several entries lead, they are settled as a set, because their queue
places are one arrangement rather than several claims. An entry that turns out
to be a reservation takes a place the others cannot have, and one that turns
out to be executing takes none, so every admission that lands narrows what is
left for the entries still outstanding — and the last of them to land
determines all of the places exactly. A range is what an entry is judged
against only while a sibling ahead of it is genuinely unknown, which at the
trace's end means a sibling whose admission never arrived. The listing's own
two fields wait for the whole set, because which run `active_run_id` owed is a
question about all of them at once; where the lead is the listing's only one
that reduces to the single answer it always was.

The identity claim itself is always reconciled. Accepting it at the state
response and never returning to it is what lets a snapshot assert an admission
that did not happen.

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

`premature_session_mutation` is the check that binds it: a snapshot reports the
model installed by the session's last model-affecting run, judged at the
position the snapshot states. That run does not have to still be going — a
`session_mutation` moves the session default, and the default outlives the run
that moved it, so terminality ends the run and not its effect. A snapshot that
stopped answering for a mutation once its run settled could undo it silently,
and the next control-free submission would run against the model nobody chose. A position is a position in this session's history, and it names a promotion:
an anchor on another session's run would let a snapshot borrow a model
authority that says nothing about the session it describes, and an anchor on a
sequence its run did not start at, or on a promotion that never arrives, would
leave the model it reported judged against nothing. And it names a
model-affecting promotion: supplying an anchor is what sets the unanchored
check aside, so a run that applied no `session_mutation` would let a snapshot
point at a control-free start and report any model at all. All four are
`session_state_mismatch`. And it names the last
such event, not merely one of them: with two mutations started, anchoring the
earlier describes a moment that had already passed and reports the model of
that moment as the session default. Later means later in the trace, since which
promotion moved the default last is a question about the order they reached it,
and the capture window answers what counts — a mutation that began after the
read was requested is not one the snapshot had to reflect. The genesis marker is dated the
same way: it names the moment before the session's first model-affecting event,
so one already behind the capture window supersedes it exactly as a later
promotion supersedes an earlier one. Otherwise it is a way back to the opening
model from any point in the session. The marker has a
genesis form, `{"run_id": null, "sequence": 0}`, naming the position before the
session's first model-affecting event; without it the one case the marker
exists for — a session opening on model A, captured just before the first
promotion whose `run.started` reaches the trace first — would be the one case
it could not express.

## Evidence

Fixtures (`fixtures/manifest.json`, unit `queue`): 159 traces covering both
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

Reference execution: `adapter/memory.go` moves to `reference-memory-v4` — T5a
took v3 for the models catalog, and a revision identifies exactly one
descriptor — and advertises the key `emulated` with
`max_active_runs_per_session: 2` and `max_queued_runs_per_session: 1`, reserves
one run beside the started one, lists both in `active_runs`, promotes on the
terminal, and settles a cancelled reservation pre-start.

Both adapters hold a run out of their projection until the admission is
decided and then publish it as `Submit` hands the response back. A run exists
inside an adapter from the moment a submission creates it, and an adapter may
emit its opening events before `Submit` returns, so the honest projection
during the call is the one without the run: nothing published inside the call
reaches the trace ahead of the admission either, since the caller does not hold
the stream yet.

That is as narrow as an adapter can make the window, and it is not closed.
Both frontends build the submit response envelope *after* `Submit` returns, so
a state read taken between the two still describes a run the trace carries no
admission for. No adapter can close this, and the reason is the one already
recorded for the anchor itself: the fact that settles it — the admission
envelope existing — is the frontend's to create, and its id is the frontend's
to mint. An adapter that waited for it would be waiting on something it cannot
observe.

So the boundary belongs to the serving layer, which owns both envelopes and can
either order their creation against each other or stamp the in-flight
submission's id into the entries as the anchor this unit already defines for a
snapshot that leads an admission. The queue unit did not open this window — a
snapshot naming an unadmitted run through `active_run_id` alone had it before
`active_runs` existed — but listing entries made it diagnosable, which is how
it came to light. It is recorded here as a serving-layer limit rather than
closed, because closing it correctly changes where the two codecs build and
order their responses and is not this unit's to change.

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
  pre-start would be stranded. Its overflow recovery reads admission order the
  same way. Those serials are execution order only while runs settle in the
  order they were admitted, and a reservation cancelled before promotion is
  precisely the case where they do not: its terminal carries the higher serial
  while the run it was queued behind is still delivering. A run drained to its
  end that is not the run the hub is on can no longer be a recovery target, so
  it never outranks one that is still going — a cursor there has nothing left
  to give, and the tail the drop lost would be unreachable from it.
- A held envelope is not published, and publication is what the journal
  records. Buffering the reservation's envelopes while deferring the journal
  append is the same claim as keeping its queue slot until `published()`: a
  journalled envelope is replayable, so a caller resuming the reserved run mid
  hold would read its start before the earlier run's terminal and then be
  handed the same envelopes again when the buffer flushed. A cursor into the
  unflushed prefix is future, not replayable, and the stream stays open while a
  terminal is held.
- A native turn that no OAP run can own is quarantined, not reduced onto
  whatever run is started. The pin has no route that withdraws a queued input,
  so a cancelled reservation's turn may still execute; falling back on the
  started run would hand that run another turn's content, reopen its step
  accounting, and settle it on a boundary it never reached. Any other unowned
  turn — a foreign input among them — takes the same path, and suppression
  lifts at the next `prompted` an OAP run does own.
- A run admitted queued is projected as a reservation until its `run.started`
  reaches the trace, on every endpoint here. The identity exists from
  admission, but the turn begins when the endpoint says it does — and on an
  idle session those are not the same instant, because an explicit `queue` is
  admitted as a reservation there too. Projecting the slot instead of the trace
  named a reservation in `active_run_id` and listed it without the queue
  position it held, which is precisely the shape this unit's own state rules
  reject.
- The admission bounds are counted, not inferred from which slot is occupied.
  A session whose only run is an unpromoted reservation is at a queue bound of
  one, and a second submission there is `run_active`; reading the started slot
  instead admitted it and put the queued subset above what the descriptor
  discloses.
- A session that stops being usable settles every run it admitted, not only the
  started one. Otherwise the reservation keeps `Close` returning `run_active`
  while `Cancel` and `State` answer `session_closed`, and the caller can
  neither settle the run nor close the session. Every path that marks a session
  unusable owes this, not only the transport: a foreign durable event, a failed
  settlement fence, an ambiguous admission or cancellation.
- A session status follows the evidence its own entry publishes, which on an
  endpoint that gates includes an unresolved interaction the entry names. Not
  every gate announces a run status: the reference adapter reports one for its
  input gate and not for its permission gate, so reading the run status alone
  had it describe a session running beside a run it had just said was blocked.
- The test kit reads `State` and splices the snapshot into the trace it
  validates. Without that an adapter's projection never faced the rules it is
  written against — every assertion in the kit is about the event stream, and a
  state read that contradicted the run beside it passed all of them.
- A run is projected once its admission response exists, not once it takes its
  slot. The two are the same instant on an endpoint that admits locally and a
  round trip apart on one that does not, and in between the run exists only
  inside the adapter: it holds its slot and counts against the disclosed
  bounds, because that is what the bound is for, but a state read must not name
  a run nobody has been told was accepted. The alternative — projecting it with
  an anchor naming the submit request — is not open to an adapter, which never
  sees the envelope id the anchor is made of.
- A snapshot handed to a caller owns its own backing array. `active_runs` made
  `SessionState` a value with a slice in it, whose entries carry pointers and
  slices of their own, so the by-value recovery snapshots shared all of it with
  the session and a caller editing what it was given reached into the adapter's
  own projection. Every path that hands one out clones, `State` and the
  recovery snapshot alike.
- Shutdown selects the runs it settles from `active_runs`, not from
  `active_run_id`. The two now differ: a reservation is admitted work that owes
  a terminal, so an adapter's Close refuses for it, while `active_run_id` is
  deliberately absent where only reservations remain. A sweep reading the id
  alone would cancel nothing, exhaust its retries, and leave the child process
  and an accepted submission alive — on the reference adapter as well as on
  OpenCode, since both refuse a close for a reservation. `active_run_id`
  remains the fallback for an endpoint that keeps no entries.
- A run's projected status moves with the envelope that reports it, inside the
  publication rather than after it. A state read between a published
  `run.started` and the next envelope would otherwise describe a run the trace
  has seen start as still queued, which is the one direction the entry rules
  reject.
- A held run is projected at what it has published, which is nothing: its
  status stays the reservation's and its stated position stays where the trace
  left it. A promoted reservation can finish natively while the earlier run is
  still outstanding, and copying its own status there would list a settled run
  in a field defined as the session's nonterminal ones. This is the third form
  of the same rule as the queue slot and the journal.
- A reservation is admitted work, so it refuses a session close exactly as a
  started run does. Between a started run's terminal and a reservation's
  promotion the reservation is the session's only nonterminal run, and a close
  that looked only at the started slot would drop an accepted submission
  without publishing anything for it.

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

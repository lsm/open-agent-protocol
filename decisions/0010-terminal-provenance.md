# Decision 0010: Terminal Provenance

Status: accepted 2026-09-17 (an amendment rather than a unit, so the unit
gate does not apply; its evidence is pinned corpus cases that run in CI —
`confirmed-destructive-cancel` and `post-stop-stale-publication` — and
`settled_by` now appears in expectations across several adapters. Nothing in
the record is left pending)
Date: 2026-09-16
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Amends: [Decision 0001](0001-agent-control-v0.1-executable-core.md) ("One
foreground invocation is one run") in the one respect named below; it adds no
terminal and changes no terminal's meaning
Evidence: pinned corpus cases across all eight adapters, listed under Evidence

## Context

Decision 0001 requires every accepted run to settle into exactly one of
`run.completed`, `run.failed`, or `run.cancelled`, and forbids a source that
cannot eventually settle an accepted run from claiming this profile. That rule
is right and stands. What it does not say is how an endpoint came to know the
run settled.

Two cases are routinely different. In the first, the harness reports a
run-scoped terminal and the adapter projects it. In the second, the harness
reports nothing about the run at all, and the adapter concludes the terminal
from evidence of another kind: a session-scoped stop that destroys the run's
context, or the death of the transport the run was executing over. Both produce
a valid terminal. Only the first is a native fact.

The distinction already exists in the adapters, unreadably. Makai's
session-scoped stop path emitted `run.cancelled` with the free-text reason
"Makai confirmed destructive session stop", while its `agent_end` path chose
between "Makai confirmed session-destructive cancellation" and "Makai reported
cancellation" on whether the host had asked. The words "confirmed" and
"reported" were carrying provenance, in prose, in a member the protocol defines
as human-facing and no consumer may parse. A control layer deciding whether to
trust a terminal enough to release a resource, retry, or surface an ambiguity to
its user could not read that distinction, and an adapter author had no place to
put it except the reason string.

This decision gives it a place.

## Decisions

### An optional member on the three terminals

`run.completed`, `run.failed`, and `run.cancelled` payloads accept an optional
`settled_by` with exactly two values:

- `observed` — the endpoint saw a run-scoped native terminal for this run and
  projected it.
- `inferred` — the endpoint concluded this terminal from other evidence, having
  never observed a run-scoped terminal for the run.

Any other value is invalid. The member is schema-optional on all three
terminals and carries no other vocabulary; a third value is a later versioned
decision, not an extension point an endpoint may use today.

### Omission asserts observation

An absent `settled_by` means `observed`. It is not "unknown" and not "declined
to say".

This is the one respect in which Decision 0001 is amended: a terminal that was
silent about its provenance now makes a claim by being silent. The alternative —
absence meaning unknown — would have made the member useless for the case it
exists to serve, because the overwhelming majority of terminals are observed and
would stay silent, leaving a consumer unable to distinguish "observed" from "not
saying". Every canonical trace written before this decision remains valid and
correct under it: those terminals were observed, and their silence now says so.

An endpoint that always observes its terminals never writes the member.

### An inferred terminal is a terminal

`settled_by` is provenance about the endpoint's own knowledge. It is not a
second status, not a confidence score, and not a hedge.

An inferred terminal is as absorbing and as final as an observed one. It ends
the run's sequence domain, satisfies Decision 0001's one-terminal requirement,
frees the session, and forbids every later run-scoped event exactly as an
observed terminal does. A consumer that treats an inferred terminal as
non-terminal, or waits for a later observed one to supersede it, is wrong.

Nothing about terminal arbitration changes. An endpoint that can observe a
terminal still must not race ahead and infer one, and the rule that duplicate
native observations must not produce duplicate portable terminals is untouched.

### What makes a terminal inferred

The test is what the endpoint had to go on, not how the run ended. Stating it
sharply matters more than it might seem: because omission asserts observation,
an endpoint that guesses wrong here does not merely omit a detail, it makes a
false claim.

A terminal is **observed** when the endpoint acted on run-scoped evidence it
could read. That covers the harness's own terminal for the run, and equally a
frame in this run's stream that the endpoint judged fatal — a duplicate tool
call, an unknown required event, a step the harness reported failed, a foreign
or malformed update. The endpoint was watching this run and ruled on what it
saw. A terminal is not inferred merely because the ending was unpleasant or
because the endpoint, rather than the harness, decided the run was over.

A terminal is **inferred** when the endpoint had no such evidence to rule on:

- **Transport loss.** The channel the run was executing over died — a process
  exit, or a frame the codec could not read — with the run still open.
- **A session-scoped stop.** The harness settled or destroyed the session and
  said nothing about this run, whose own settlement is never published or is
  discarded. It makes no difference whether the host asked for the stop.
- **A control call that never landed.** A cancellation, an abort, a permission
  answer, or another reverse-channel write failed, so nothing was ever reported
  back about how the run ended. A call the harness *answered* with an error is
  the opposite case: that answer is run-scoped evidence about this run, and the
  terminal drawn from it is observed. The same call site can therefore produce
  either, and an endpoint that cannot tell the two apart should not be emitting
  the member at all.

The test does not depend on whether the run ever started. A submission that
fails before its start is judged on the same question as any other terminal: a
definite refusal the endpoint received — an HTTP status the server chose to
send, a typed error answering the request — is observed, while a request that
vanished into a dead transport is inferred. Most adapters here settle a
pre-start failure without emitting a terminal at all, so the question rarely
arises; where one does emit it, which Decision 0002 admits as a pre-start
`run.failed`, the member applies on exactly these terms.

### The member is per-terminal, not per-endpoint

`settled_by` describes one terminal, not an adapter's general fidelity. The same
endpoint emits observed terminals on its ordinary path and inferred ones when
its transport dies, and both appear in one session's history. Consumers key on
the member, never on the adapter's identity.

### No cross-check against the capability descriptor

The validator rejects a value outside the enum and does nothing else with it. It
does not require an endpoint that emits `inferred` to have advertised anything,
and it does not reject an `inferred` terminal from an endpoint whose descriptor
suggests it should have observed one.

Three reasons. First, the member reports a fact about one run's history, and
whether an endpoint *can* infer a terminal is not a feature a control layer
selects or gates on — there is no control to withhold and no behavior to refuse.
Second, any such rule would be a rule about when transports are allowed to die,
which is not a property an endpoint can honestly advertise in advance: the
adapters that infer terminals are exactly the ones whose ordinary path observes
them. Third, a cross-check would punish the honest adapter. An endpoint that
declared nothing and inferred silently would pass, while one that reported the
inference truthfully would fail validation — precisely inverting the incentive
this decision exists to create.

Capability disclosure describes effective behavior an endpoint can commit to.
Terminal provenance describes what happened. They are different kinds of claim
and they are not reconciled.

### Reasons describe cause, not epistemics

With provenance carried in `settled_by`, the human-facing `reason` on
`run.cancelled` returns to its own job: saying what caused the cancellation. An
adapter choosing between reason strings to encode how sure it is should now
encode that in `settled_by` and let the reason name the cause.

## Evidence

Three patterns, each re-checkable against a pinned corpus case that runs in CI.

1. **A session-scoped stop settles a run whose terminal is never observed.**
   At the pinned Makai server, `agent_stop` removes the session before the
   detached execution publishes its final `agent_end`, and that publish is then
   discarded. The correlated `agent_stopped` response is the last observable
   cancellation evidence, so the adapter normalizes it into a run terminal it
   never saw. Pinned by
   `fixtures/adapters/makai-agent-67ad514/confirmed-destructive-cancel` and
   `fixtures/adapters/makai-agent-67ad514/post-stop-stale-publication`, whose
   expected `run.cancelled` now carries `settled_by: "inferred"`.

2. **Transport loss settles a started run, from more than one cause, under one
   code.** The Claude adapter settles a started run as `claude_process_exit`
   both when the child exits and when its stdout carries a frame the codec
   cannot read. Neither is a result the CLI reported. Pinned by
   `fixtures/adapters/claude-code-2.1.263/process-exit` (mapping index 8,
   `process_exit`, "transport failure settles the started run") and
   `fixtures/adapters/claude-code-2.1.263/malformed-stdout` (mapping index 8,
   `codec-error`). That one code covers two causes is the point: the cause
   varies, the provenance does not.

3. **The distinction was already being smuggled through free text.** Before this
   decision, `adapter/makai/session.go` chose between the reasons "Makai
   confirmed session-destructive cancellation" and "Makai reported cancellation"
   on whether the host had requested the cancellation, and the session-stop path
   said "Makai confirmed destructive session stop" for a terminal it had
   inferred. The information a consumer needed was present, in a member no
   consumer may parse, and in one case it said "confirmed" of an inference. This
   is the strongest evidence that the need is real: adapter authors were already
   reaching for it and had nowhere to put it.

## Consequences

- The schema bundle gains one optional member with a two-value enum on the three
  terminal payloads. Every existing canonical trace stays valid, and validation
  rejects a value outside the enum at the schema phase, pinned by
  `fixtures/schema-invalid/terminal-provenance-bad-enum.json`.
- Consumers gain a parseable answer to "did the endpoint see this end?" and lose
  the ability to read it out of prose, which they never had.
- Adapters that infer terminals say so. Every adapter in this repository has at
  least one: each settles a started run when its transport dies, and Makai and
  OpenCode additionally settle runs from a session-scoped stop and from an
  ambiguous cancellation or admission. All of those paths now stamp `inferred`.
  What correctly stays silent is the other kind of failure — a step the harness
  reported failed, a tool lifecycle the adapter watched go wrong — where the
  endpoint observed the evidence it acted on in readable frames.
- A reason string is no longer where provenance goes, and the two Makai reasons
  that encoded epistemics now describe cause.
- No terminal was added, no terminal's meaning changed, and the one-terminal
  invariant, the sequence rules, and cancellation intent-versus-settlement are
  all untouched.

## What this decision does not admit

- A third `settled_by` value. `unknown`, `partial`, and per-endpoint confidence
  grades are not admitted; an endpoint that cannot say which of the two applies
  has not established that the run settled at all.
- A capability feature for terminal inference, or any validator rule relating
  `settled_by` to a descriptor.
- A structured cause vocabulary for `reason`. It stays human-facing free text;
  this decision only stops it from being the carrier for provenance.
- `run.orphaned` or any other terminal for a run an endpoint can neither observe
  nor infer. That remains reserved by Decision 0001 for a later negotiated
  revision, and this decision deliberately does not reach it: `settled_by` says
  how a settled run settled, never that a run failed to settle.

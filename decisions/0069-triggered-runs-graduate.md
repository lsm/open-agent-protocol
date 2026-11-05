# Decision 0069: A Run Can Start Without a Submit

Status: accepted 2026-11-05 (the owner decided triggered runs on 2026-10-18, and
Decision 0068 needs them on the wire)
Date: 2026-11-05
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Amends: [Decision 0007](0007-queue-delivery.md), whose only exception to
admission order was a pre-start terminal, and the deferral of background work in
Decisions 0001 and 0002, which PF-4 kept

## Context

A harness can start a turn because something inside it fired, such as a background
job ending. OAP has no run the host did not submit, so every adapter drops such a
turn or ends the session over it. `oapx`'s own loop will do the same under
Decision 0068 unless the wire can carry it.

[`drafts/triggered-runs.md`](../drafts/triggered-runs.md) holds the design and the
evidence. This decision graduates the part that admits such a run. `session.task.*`,
`state.tasks` and the capability keys stay in the draft until a pull request
graduates them.

## Decisions

### `run.started.trigger` admits a run

A `run.started` with a `trigger` member is that run's admission and its start, and
it is the run's sequence 1. Nothing else admits it, and no submit answers it. After
that every rule for a run applies unchanged. The endpoint mints the run id, unique
within the session.

`trigger` has a required `kind`, one of `background_task`, `schedule`, `message`
and `other`, and optional `input`, `task_id`, `source_id` and `extensions`, as the
draft defines them.

### A triggered run starts only on an idle session

A `run.started` with `trigger` while a run of the session is started and
nonterminal is `illegal_run_transition`. A trigger that fires during a run is folded
or held by the harness, and is never a second run.

### A trigger belongs to a run nothing reserved

A `run.started` with `trigger` for a run a submit reserved is
`illegal_run_transition`. So is a `run.started` without `trigger` for a run nothing
reserved, as it already was.

### A triggered run goes ahead of waiting reservations

This is a second exception to Decision 0007's admission order. A triggered run may
start while reservations wait. It enters the order at its start, ahead of every
reservation still queued, and those keep their relative order and promote after it
ends. A reservation's `queue_position` counts reservations only, so it does not
change.

## Consequences

- `schema/v0.1/run.schema.json`, all three validators, `go/protocol`,
  `rust/protocol` and `clients/ts` carry the member.
- A host must expect a run it did not submit. Under Decision 0066 one reaches an
  open subscription with no new request.
- The rest of *One run executes at a time* in the draft stays a proposal until
  an adapter needs it.

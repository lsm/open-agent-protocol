# Decision 0026: A Resume Cursor May Name Its Run

Status: proposed
Date: 2026-09-22
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: daemon transport surface, not a claim term
Relates to: [Decision 0009](0009-compound-open.md), which named this gap and
left it, and [Decision 0002](0002-admission-before-start.md), which makes a
second run admissible inside the settle window of the first

## Context

`serve.After(runID, sequence)` has taken a run since it was written. Both
frontends passed the empty one, and they were its only two call sites, so a
cursor on the wire named a sequence and nothing else. `Hub.Subscribe` resolves
an unqualified cursor onto the session's **current** run.

Sequences are per-run and restart at 1. A cursor is therefore meaningless
without a run, and the run was supplied by whichever one happened to be current
when the request arrived — which is not in general the run the cursor was cut
from. A subscription that overflows on one run and reconnects after a second
run has been admitted replays the wrong run's events under the right sequence
numbers. There is no error: the numbers are valid for the run they land on.

The signals already carry the run. SSE's `oap-overflow` names `run_id`, and
stdio's `oap-overflow` and `oap-stream-failed` both do — recorded at stop time
precisely so the signal survives a newer run being admitted before the consumer
reads it. Only the request acting on the signal could not say which run it
meant.

What existed instead was detection, client-side and optional:
`client.EventsAfter(ctx, runID, after)` holds the run id, never sends it, and
compares it against the daemon's current run to raise `ResumeMismatchError`.
Every host
reimplements that, and a host that does not gets silent corruption rather than
an error.

## Decisions

### A cursor may name its run, on both transports

`GET /sessions/{id}/events` accepts `run_id` beside `after` and
`Last-Event-ID`. The `events` op accepts `run_id` beside `after`. Both pass it
to `serve.After`, which is where it was always going.

### A named run is replayed, whether or not it is current

The adapter `Resume` contract is already run-addressed, and the hub already
drives it with whatever run the option names. Naming a settled run replays that
run's suffix. Refusing one would be a restriction with no motive: a host that
names a run has said exactly which events it wants, and the run it wants is
usually the one that just ended under it.

A run the adapter cannot serve is `run_not_found`, which both frontends already
map, and a retained-but-expired position is still `oap-replay-gap`. Neither
code is new.

### An unqualified cursor keeps resolving onto the current run

This is a compatible addition, not a correction of the default. Every existing
host sends a bare `after` and must keep working. The default is documented as
what it is — a guess that is right whenever the session has not admitted a
second run — rather than silently correct.

### `run_id` without a cursor is `invalid_cursor`

A live subscription is always the current run, so a run on a request with no
cursor names a position that does not exist. It is refused rather than ignored,
because a host that sent it believes it asked for something.

## Evidence

Both transports: a session with two completed runs, resumed at `after=1`.
Naming the first run replays the first run; naming nothing replays the second.
`TestEventsCursorFollowsTheRunItNames` and `TestSSECursorFollowsTheRunItNames`
are the pair, and `TestRunQualifiedCursorMatchesHTTP` asserts the two
transports return the same first envelope for the same request.

The refusals have their own tests on both sides: `run_id` with no cursor is
`invalid_cursor`, and a run the session never had is `run_not_found`.

Before this change the hub already replayed a named run correctly when asked
directly — a probe against `hub.Subscribe(..., serve.After(firstRun, 1))`
returned the first run's suffix while `serve.After("", 1)` returned the
second's. The defect was never in the hub; it was that no request could reach
the parameter.

## Consequences

- A host that reconnects after an overflow can hand back the `run_id` the
  overflow signal gave it, and gets the events it missed rather than a
  different run's.
- `ResumeMismatchError` becomes a fallback for hosts that do not send the run,
  rather than the only defence. The Go and TypeScript clients still detect
  rather than prevent; sending the run is a one-parameter change in each, and
  is not made here.
- `serve/servestdio/parity_test.go` covers the new parameter on both surfaces,
  which is why they land together.

## What this decision does not admit

- **A run on `subscribe`.** [Decision 0009](0009-compound-open.md) refused a
  cursor there and this does not reopen it: a compound open's subscription
  begins before the session has a run.
- **Cross-run cursors.** A cursor still names one run and one position in it.
  Following a run boundary is the host's decision, made by subscribing again.
- **Changing what a bare cursor means.** Named above.

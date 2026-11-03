# Decision 0066: A Subscription Lasts as Long as Its Connection

Status: accepted 2026-11-03
Date: 2026-11-03
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Amends: `drafts/hub.md`, `drafts/triggered-runs.md` and
`drafts/staged-units-graduation.md`

## Context

A hub subscription was specified to end at its run's terminal envelope. The
trees did not agree: Zig stopped there, while Go and Rust stopped only when the
session had no run left to follow, so one reader could cross into a queued run.
Neither shape suits a host that watches a session. It had to resubscribe after
every run, and nothing could carry a run no subscriber asked for, such as a
triggered run.

`drafts/triggered-runs.md` and the T2 design already proposed an opt-in
`?follow=session`. The owner chose to make that the only behaviour rather than
keep two modes.

## Decision

### A subscription's lifetime is its connection's

A subscription ends only when the client ends it, the session closes, it
overflows, a stream fails, or the frontend tears down. A run's terminal is an
ordinary envelope and ends nothing, and neither does a session going idle. The
stream pushes when there is something and stays open when there is not.

Runs reach a subscription in admission order. A client learns that a run
finished from its terminal envelope and tells runs apart by `run_id`.

There is one subscription mode. `?follow=session` is not added, and neither are
`oap-run-boundary` and `oap-stream-end`: they existed because a stream could
stop at a terminal and older clients had to be told. A stream's end already has
its named signals, and ordered replay covers the rest.

### Stdio gains `unsubscribe`, reversing G3

G3 kept stdio without `unsubscribe` because a subscription ended at its run's
terminal, so slots under the ceiling of 64 freed themselves. That premise is
gone, and "the client ends it" needs a form on a pipe that carries every
subscription at once. The stdio `unsubscribe` op ends one subscription; on HTTP
the same act is the client's hangup. Parity between them is stated on the
outcome, which is what the parity job checks: no further lines, and the slot is
free.

One subscription per session per connection was considered and not taken. It
does not help a host watching more sessions than the ceiling, and it still
leaves no way to stop watching a session without closing it.

### An open HTTP stream sends keepalives

A stream may now be quiet for as long as a session is idle, so a client could
not tell quiet from dead, and a hub could not notice a dead client. Every
15 seconds of silence, an HTTP stream writes the SSE comment `: keepalive`. A
failed write ends the subscription. Stdio needs none: the pipe's end is the
signal.

### A cursor resumes across runs

A cursor is `(run_id, sequence)`. Resuming from one delivers the rest of that
run, then every later run of the session in admission order, each from sequence
1, then live events, with nothing lost or repeated. A journal that no longer
holds what the cursor needs is `oap-replay-gap`, as before.

An SSE envelope's `id` becomes `<run_id>:<sequence>`, split at the last colon, so
`Last-Event-ID` names its run. A bare sequence is still accepted and still means
the current run.

## Consequences

Anything that read the end of a stream as "the run finished" must read the
terminal envelope instead: the Go and TypeScript clients, the endpoint pumps
that follow one run, and the Zig terminal UI. This is a behaviour change to the
public Go `serve` and `client` packages.

Parking an idle session may no longer require that it has no subscriber, because
an open client would then keep it from ever parking. The hub holds a
subscription, not the harness, so a parked session keeps its streams.

The trees reach this rule one pull request at a time; `drafts/hub.md` records
which have not in D35.

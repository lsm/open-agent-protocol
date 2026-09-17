# Decision 0009: Compound Open

Status: proposed
Date: 2026-09-16
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `compound-open` (claim term `+compound-open`)
Extends: [Decision 0001](0001-agent-control-v0.1-executable-core.md) and
[Decision 0002](0002-admission-before-start.md) without amending either;
interacts with [Decision 0007](0007-queue-delivery.md) where a compound open's
message is queued rather than started
Gated by: [Decision 0003](0003-staged-unit-graduation.md)
Design: [Compound Open](../drafts/compound-open.md)

## Context

A host driving the daemon can pipeline requests. The canonical opening
sequence is `open`, then `events`, then `submit`, and sent that way the three
are independent: the frontend may serve them in any order, and the
subscription can register after the run has started.

Every other disorder this surface admits reports itself. Resolving an
interaction before the run waits for it is refused; submitting to a session
that does not exist is refused; a subscription that loses the race with the
*open* is refused `unknown_session`. One does not. A subscription that loses
the race with its own `submit` succeeds, and silently omits the run's opening
envelopes — because an `events` request without a cursor is a live
subscription and delivers from wherever it joins.

That is not a defect in the uncursored semantics. "Give me what happens from
now" is a legitimate request, and `TestSSELiveSubscriptionMidRun` pins
deliberately that a second connection joining mid-run is not handed a prefix
it did not ask for. The defect is that a host cannot tell which it got.

Serializing registration in the frontend was rejected while re-cutting
[#19](https://github.com/lsm/open-agent-protocol/issues/19): the stdio serving
loop is the only thing reading the host's input, opening a session calls the
adapter and is arbitrarily slow, and two P1 defects there had exactly the
shape of a loop that stopped to do slow work. A per-session barrier was
rejected for placing new machinery beside the in-flight admission bound, where
a slow open would refuse unrelated requests `busy`. The draft records both.

## Decisions

### The open carries the subscription and the first message

`session.open.request` gains two optional members:

- **`subscribe`** — when true, the endpoint registers the session's
  subscription as part of the open, before the response is produced. The
  subscription cannot be late, because it exists by the time the session does.
- **`message`** — an optional first submission, admitted as part of the open.

They are independent. A host that delivers one message and stops sets
`subscribe` false and supplies `message`; a host that watches a session it
will drive later sets `subscribe` true and omits `message`.

`subscribe` defaults to false, and the default is load-bearing rather than
conservative. An unwanted subscription is not free: over stdio its envelopes
are written into the same output the host must drain, and a host that does not
read them fills the writer's queue and enters the frontend's refusal path.

### `subscribe` carries no cursor

A compound open does not double as the reattach path. There is nothing to
replay: the session is being created, so it has no journal, and an open naming
an existing session id is refused `session_exists` rather than reopening it.
A cursor there could only ever name a position that does not exist.

Reattach stays with `events` and its `after` cursor, which is where a host
that already has a session and a position asks from.

### The response carries two facts, and already had the shape for both

The open response is the session state, unchanged. It is not restructured, no
member is added to it, and `session.open.response` keeps the payload it has
today.

The acknowledgement the draft asks for is already expressible there. A run
admitted by a compound open appears in `state.active_runs` with:

- `run_id` — the run to cancel with or poll for,
- `status` — `running` or `queued`, which is the started-or-queued fact,
- `queue_position` — where it sits when queued, per Decision 0007,
- `admitted_submit_requests` — naming the open request that admitted it.

This is Decision 0006's ruling applied rather than repeated: the state carries
what happened, and a response that enumerated a projection beside it would
drift from the next member added. Two facts, one document, no new vocabulary.

What it does not carry is the submission-level detail a separate
`session.message.submit.response` would: `submission_id`, `message_ids`, and
`effective_delivery` / `delivery_resolution`. Started-versus-queued survives as
`status`; *why* delivery resolved that way does not. That is the price, and it
is recorded here rather than discovered later. A host that needs the
resolution reason submits separately, which it may always do.

### The acknowledgement is not conditional on `subscribe`

The two are different channels. The acknowledgement is the response to a
request: it says the submission was accepted and names the run. The
subscription is the event stream: it says what the agent then did.

A host that delivers a message and stops still needs the first. Without it, it
does not know the message was accepted and holds no run id to cancel with or
to poll `session.state` for. Deliver-and-walk-away must not mean
deliver-and-hope.

### Failure is atomic

A compound open either opens the session and admits the message, or does
neither. When the message cannot be admitted, the session is closed again
before the refusal is sent, reusing the open-rollback path both frontends now
define for an open whose response cannot be delivered.

No partial-failure vocabulary is introduced, because no partial outcome is
reachable. A host that receives a refusal holds no session id it must clean
up, and one that receives a success holds a session and a run.

### A subscription reports where it joined

Alongside the members above, a successful subscription reports the sequence it
begins at, so a host that subscribed separately can see that it joined mid-run
and resubscribe with a cursor. This converts the silence into information
without changing what a live subscription delivers.

It is a named signal on both transports — `oap-subscribed`, emitted first —
and not a response member. The SSE route answers with the stream and no body,
so a named event is the only place the fact can live there at all; putting it
on the stdio response instead would put one fact in two places depending on
which pipe carried it, which is the divergence `serve/servestdio/parity_test.go`
exists to prevent.

A compound open does not emit it: its subscription begins before the session
has a run, so there is no position to have joined after.

### An endpoint that cannot subscribe at open says so

`subscribe` is an optional feature and takes the ladder every optional feature
in this profile takes. An endpoint discloses `session.open.subscribe` with an
effective level; a request electing it against an endpoint that does not
advertise it is refused unadvertised, and one electing it against a `degraded`
disclosure is refused unless the request consents through
`allow_degraded_features`.

An adapter whose event stream does not exist until the first run is the case
this exists for. It discloses `degraded` and a host that consents gets a
subscription that begins when the stream does — which is still ahead of any
separate `events` request, and which the joined-at signal describes.

### A compound open must cite the active revision

A compound open exercises an optional feature, so the validator's existing
rule applies to it unchanged: the envelope must cite the active descriptor
revision. This is not a new rule and is stated only because the request that
carries `subscribe` or `message` is the one it now binds.

## Evidence

The silent case is reachable today and was reached while building the stdio
frontend, not theorized. `SessionOpenRequest.session_id` is optional, so a host
that proposes its own id can pipeline `open`, `events` and `submit` without
waiting for anything.

Piping `examples/oap-stdio-session.ndjson` into `oap serve --stdio` — every
line sent at once, which is exactly the pipelined case — produces:

```
id 4 ok True
id 5 ok False {'code': 'unknown_session', 'message': 'no session "demo"'}
id 6 ok False {'code': 'unknown_session', 'message': 'no session "demo"'}
id 3 ok True
```

The session-scoped ops raced the open that creates the session. Those refusals
are the *loud* half working as designed. The half that would not have been
loud is a subscription that beats the open's response but loses to the run.

## Consequences

- `session.open.request` gains two optional members. Nothing existing changes
  shape, and an open that sets neither behaves exactly as it does today.
- `session.open.response` is untouched.
- The validator learns that a `session.open.request` carrying `message` is an
  admitting request: `admitted_submit_requests` may name it, and the run it
  admitted links to it the way a run links to its submit request today.
- A new capability key, `session.open.subscribe`, joins the descriptor.
- `+compound-open` joins the conformance units.
- The reference adapter implements both members, so the corpus and the golden
  transcript carry a compound open.

## What this unit does not admit

- **A general batch request.** Considered and deferred; the draft records the
  analysis, the three alternatives weighed, and what would revive it. The
  short form: the daemon is a single-user local service where a round trip
  costs microseconds, so the usual case for batching mostly evaporates, and
  the one sequence with a *silent* failure is the one this unit covers.
- **A cursor on `subscribe`.** Named above.
- **Restructuring the open response.** Named above.
- **Ending one subscription on request.** The stdio pipe has no per-stream
  hangup, which [#53](https://github.com/lsm/open-agent-protocol/issues/53)
  records. A unit that lets a host subscribe inside an open is where the
  symmetric question belongs, but it is a wire surface of its own and is not
  settled here.
- **Run-qualified resume cursors.** [#52](https://github.com/lsm/open-agent-protocol/issues/52),
  and unchanged by this unit.

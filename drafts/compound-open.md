# Compound Open

Status: settled by [Decision 0009](../decisions/0009-compound-open.md)
Date: 2026-09-16
Base protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Prompt: the ordering conflict found while re-cutting
[issue #19](https://github.com/lsm/open-agent-protocol/issues/19)

This document proposes two optional members on `session.open.request` — a
subscription flag and a first message — and records why a general batch
request was considered and deferred. It graduates nothing; a decision does
that when the design is built.

## The problem it solves

A host driving the daemon can send several requests without waiting for
their responses. The canonical sequence is `open`, then `events`, then
`submit`. Sent that way and executed concurrently, the subscription can
register after the run has already started, and the host silently misses
the run's opening envelopes: no error, no gap marker, just a hole it may
never notice. `SessionOpenRequest.session_id` is optional, so a host that
proposes its own id can pipeline all three and reach this today.

Ordering them is the host's to get right only if the host can express the
order. Over one connection it cannot: the three requests are independent
and the frontend is free to serve them in any order.

## Why the read loop may not serialize them

The shelved design serialized registration in the stdio read loop, so
`open` registered its session and `events` its subscription before the next
frame was taken. [Decision 0001](../decisions/0001-agent-control-v0.1-executable-core.md)
does not speak to this, but the stdio frontend's own lifecycle does: the
serving loop is the only thing reading the host's input, and a loop that
stops to do slow work cannot observe the host's end of the session. Two P1
defects fixed in `serve/servestdio` had exactly that shape — the loop
waiting for something, the reader blocked behind it, the host's disconnect
unobservable, and `Run` never returning. Opening a session calls the
adapter and is arbitrarily slow. It cannot go back in the loop.

A per-session barrier — later requests waiting on earlier registrations —
was the other candidate and is rejected here. It recovers an ordering the
host already knew, and it places new machinery beside the in-flight
admission bound: a slow `open` would hold admission slots while later
requests for that session waited, so unrelated requests would be refused
`busy` because one session was slow to open.

## What the frontend does instead

Nothing is serialized. What the frontend can promise is narrower than it
first appears, and the narrowing is the reason the next section exists:

- `events` for a session that does not exist yet is refused
  `unknown_session`, the code both codecs already emit. The host learns
  immediately, which covers a subscription that loses the race with the
  *open*.
- `events` carrying an `after` cursor replays from it, and a cursor the
  journal no longer retains produces the `oap-replay-gap` signal naming
  what was lost and where to resume, per `drafts/conformance.md`.
- **`events` without a cursor is a live subscription and does not replay.**
  It delivers from wherever it joins. `TestSSELiveSubscriptionMidRun` pins
  this deliberately: a second connection joining mid-run should not be
  handed a prefix it did not ask for.

That third point is the hole, and it is worth stating plainly rather than
leaving it implicit. The *canonical first* `events` request carries no
cursor, because a host opening a session has nothing to resume from. So a
host that loses the race with its own `submit` gets a successful
subscription that silently omits the run's opening envelopes — the failure
this document set out to eliminate, surviving in exactly the shape a first
subscription takes.

Two things follow. The uncursored semantics are not wrong: "give me what
happens from now" is a legitimate request, and replaying into it would be
worse for the second subscriber the test describes. What is wrong is that a
host cannot tell which it got. So this design proposes, alongside the
members below, that a successful `events` response report the sequence its
subscription begins at, so a host can see it joined mid-run and resubscribe
with a cursor. That converts the silence into information without changing
what a live subscription delivers.

And the compound open below stops being a convenience. It is the only
shape in which the canonical sequence cannot lose the race at all, because
the subscription is created inside the open rather than after it.

## The proposal

Two optional members on `session.open.request`:

- **`subscribe`** — when true, the endpoint registers the session's
  subscription as part of the open, before the response is produced. The
  subscription cannot be late, because it exists by the time the session
  does. Default false: an unwanted subscription is not free, since over
  stdio its envelopes are written into the same output the host must drain,
  and a host that does not read them fills the writer's queue and enters
  the frontend's refusal path.
- **`message`** — an optional first submission, admitted as part of the
  open.

Together they collapse the canonical sequence into one request. The two are
independent: a host that wants to deliver one message and stop sets
`subscribe` false and supplies `message`; a host that wants to watch a
session it will drive later sets `subscribe` true and omits `message`.

### The response carries two facts, not one

The open response is the session state it carries today, plus the admission
acknowledgement for `message` when one was supplied — the run id and
whether it started or queued.

The acknowledgement is **not** conditional on `subscribe`, because the two
are different channels. The acknowledgement is the response to a request:
it says the submission was accepted and names the run. The subscription is
the event stream: it says what the agent then did. A host that delivers a
message and stops still needs the first — without it, it does not know the
message was accepted, and holds no run id to cancel with or to poll
`session.state` for. Deliver-and-walk-away must not mean deliver-and-hope.

### Failure is atomic

A compound open either opens the session and admits the message, or does
neither. When the message cannot be admitted, the session is closed again
before the refusal is sent, reusing the open-rollback path the stdio
subscription slice already defines for an acknowledgement that cannot be
framed. No partial-failure vocabulary is introduced, because no partial
outcome is reachable.

## Batch: considered, deferred

A general batch request — several operations in one request, executed in
order — was considered. It is deferred, not rejected, and the analysis is
recorded here so it need not be redone.

**What it would look like.** One request carrying an ordered list of
operations; one response carrying a per-step result, each correlated to its
step; stop on first error, because the operations that motivate batching
are dependent — `events` needs the session, `submit` needs the session — so
"open failed but submit succeeded" is not a coherent outcome. It would be
protocol vocabulary on both transports, not a stdio convenience; `serve/servestdio/parity_test.go`
holds the two frontends to one surface, and the HTTP binding has the same
race when a client issues requests concurrently.

**Three alternatives were weighed and are recorded for the same reason.**

- *Several responses in order, no wrapper.* Natural over stdio, which can
  emit many lines for one request; awkward over HTTP, where one request has
  one response. Rejected: it is a transport-shaped answer to a semantic
  question.
- *An explicit dependency member* — each request keeps its own id and names
  a request it must not precede. It generalizes to any sequence and leaves
  every response shape unchanged. Rejected for now: the endpoint must hold
  requests awaiting their dependency, which is state plus a new failure
  mode when the dependency never arrives, and it is the least familiar
  shape for an implementer to meet.
- *The compound open above.* Covers the one sequence whose failure is
  silent, adds no new response vocabulary, and is atomic for free.

**Why deferred.** The daemon is a single-user local service — stdio pipes,
or HTTP bound to loopback. The case for batching is amortizing round trips,
and locally a round trip costs microseconds; the argument that makes batch
compelling over a network mostly evaporates here. Meanwhile the one
sequence with a *silent* failure is the one compound open covers. Every
other pipelined disorder this surface admits reports itself: resolving an
interaction before the run waits for it is refused, and submitting to a
session that does not exist is refused. The one disorder that does not
report itself is an uncursored subscription joining mid-run, which is what
the compound open closes. The joined-at sequence above would make it
visible when a host subscribes separately anyway; the decision specified it
and deferred building it to
[#61](https://github.com/lsm/open-agent-protocol/issues/61).

**What would revive it.** A second sequence whose disorder is silent rather
than refused; or OAP carried over a network where round trips are not free.
Either is sufficient; neither is true today.

## Open questions, and how the decision answered them

[Decision 0009](../decisions/0009-compound-open.md) settled all four. They
are kept as asked, because what a design left open is part of the record.


- Whether `subscribe` may carry an `after` cursor, making a compound open
  also the reattach path, or whether reattach stays with `events` alone.
- Whether the joined-at sequence on an `events` response is a new member or
  a reuse of an existing one, and whether the stdio binding reports it on
  the response line or as one of its named signal lines.
- Whether the acknowledgement for `message` is the submit response payload
  verbatim or a projection of it, and how it is named within the open
  response.
- Whether an endpoint that cannot subscribe at open — an adapter whose
  stream is not available until the first run — refuses the flag or
  degrades, and which capability key says so.

**Answered:** no cursor on `subscribe`, and reattach stays with `events`
alone. The joined-at sequence is a named `oap-subscribed` signal on both
transports rather than a response member, and is deferred unbuilt to
[#61](https://github.com/lsm/open-agent-protocol/issues/61). The
acknowledgement for `message` is no new member at all: it rides in the
state document's `active_runs`, which already had the shape, so
`session.open.response` is untouched. And an endpoint that cannot
subscribe at open takes the ladder every optional feature takes, under the
key `session.open.subscribe`.

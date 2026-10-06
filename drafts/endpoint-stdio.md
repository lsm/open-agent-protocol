# OAP Endpoint Binding: Envelopes Over stdio

Status: proposed design
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Reference implementation: `goap serve agent`
Conformance runner: `goap conformance`

This profile may also share one stdio connection with
`open-agent-protocol.model-provider-core`. In that composed mode the host
routes on the envelope's `profile` before decoding either profile's payload;
the two retain independent correlation, scope, and sequence domains. See
[Decision 0027](../decisions/0027-composed-stdio-profiles.md). The agent-only
binding below remains valid without a provider profile.

## What this binding is for

An *endpoint* is one agent loop that speaks OAP natively. It is the role a
harness takes when it stops being translated into OAP by an adapter and starts
emitting OAP itself.

This binding exists because the repository had no answer for an implementer
asking "what do I build, and how do I know it is right". The corpus tests
adapters, which reduce native frames; an endpoint has no native frames.
`adapter/adaptertest` is an in-process Go kit. `client/` and `clients/ts` speak
HTTP+SSE. What was left was hand-assembling a trace and running `goap validate`
over it, which is authored files agreeing with each other.

## What this binding is not

`goap hub --stdio` is a different thing and implementers should not build it.
That frontend exposes a **hub**: thirteen ops, an `adapter` dimension, cursor
replay, and several subscriptions multiplexed over one pipe, each line wrapping
an OAP envelope inside a transport object with its own numeric `id`.

An endpoint implements none of that. It is one agent loop, not a registry of
them, and it carries the envelopes themselves — the payload `servestdio` puts
*inside* its `request` and `event` fields.

[Hub wire](hub.md) is the specification for that other thing, and it is what a
hub — in either tree — is judged against. This binding is not: an endpoint and
a hub share a vocabulary and a correlation idea, and nothing else.

## Framing

- One JSON object per line, encoded as UTF-8 and terminated by `\n`.
- A line is either an **OAP envelope** — carrying `protocol`, `version`,
  `profile`, `type` and `id` — or a **binding control frame**, carrying a
  `control` member and no `protocol` member. Envelopes are the protocol;
  control frames are this transport's own business, and the only one defined
  here is cursor replay. A host that never replays sees nothing but envelopes.
- The host writes request envelopes to the endpoint's **stdin**. The endpoint
  writes response and event envelopes to its **stdout**.
- A line contains exactly one envelope and no literal newline inside it. JSON
  string escapes carry newlines in payload text.
- **stdout carries protocol and nothing else.** Banners, logs, and progress go
  to stderr. An endpoint that prints anything else to stdout is not conformant,
  because the host cannot tell it from a frame.
- Lines are bounded. An endpoint declares a maximum accepted line length and
  fails closed above it; the reference endpoint uses 1 MiB. A frame over the
  bound is a framing defect, not a payload the host can shrink and retry
  transparently, so the endpoint reports it and stops rather than truncating.

## Correlation

OAP envelopes already correlate. This binding adds no transport id.

- A request envelope carries `id`. Its answer carries `in_reply_to` set to that
  `id`.
- Every request receives **exactly one** correlated answer: the matching
  `*.response`, or one `error.response` carrying a typed protocol error.
- Events are not answers. A run event never satisfies a request, and a request
  is never answered only by the events it caused.
- Requests may be pipelined. The host does not have to wait for one answer
  before sending the next request, so an endpoint must not assume its input is
  synchronous with its output.

## Ordering

One pipe carries answers and events interleaved, so the binding promises only
what the protocol promises:

- Run-scoped events of one run arrive in emission order and carry a positive,
  contiguous per-run `sequence`.
- Requests and responses do not consume a sequence.
- **No general ordering is promised between a response and an event.** An
  endpoint that emits a run's first events inside its submit handling may write them before
  the submit's own acknowledgement, and a host that assumes otherwise will
  deadlock against a conformant endpoint. A host reads whichever line arrives
  and dispatches on `in_reply_to`.

Cancellation settlement has its core ordering: an accepted
`run.cancel.response` precedes the `run.cancelled` it confirms, including for an
unstarted queue reservation. The Zig adapter endpoint writes that acknowledgement
before draining cancellation events. Its oapx adapter releases a cancelled
reservation within the request, so a buffered next submit or model switch sees
the freed capacity without an intervening pump. Go's `serve/serveendpoint` holds
the cancelled run's events from the moment it asks the adapter to cancel until
its answer to the cancel is written, the way it holds a steered run behind its
submit response
(`TestAnAcceptedCancelResponseIsWrittenBeforeTheRunCancelledItConfirms`).

## Streaming is implicit

There is no subscribe request. Once a session is open, the endpoint writes that
session's run events to stdout as it produces them.

This is the main simplification the endpoint role buys. A hub needs subscribe
because it fans one session out to several consumers over separate
connections; an endpoint has exactly one consumer — the process holding the
other end of the pipe — and it is already attached. Nothing can be missed, so
nothing needs to be joined.

## How a reader bounds a wait

A reader on this binding has to answer one question on every call: how long
may I wait for the endpoint to say something? The Zig reader takes that as a
budget and normalises it once, up front, into an absolute deadline.

The three shapes a caller can hand it mean the same thing and differ only in
where the deadline comes from:

- **no limit** — wait as long as it takes. There is no deadline, and the
  reader reports that it is waiting without a bound rather than inventing one.
- **a duration** — a relative allowance, turned once into an absolute stamp
  when the budget is built, so a slow read cannot silently restart it.
- **a deadline** — an absolute stamp the caller already computed, carried
  through unchanged.

Whatever the shape, the clock the caller chose is the clock the budget reads,
and the remaining time is handed to each read as a duration on that same
clock. The remainder is carried in nanoseconds and never rounded to
milliseconds, so a sub-millisecond allowance is a very short wait rather than
no wait at all.

A read returns a frame as soon as one is available, and returns nothing when
the budget is spent. Two rules follow from that and are worth stating
separately, because both were wrong at some point:

- **A budget bounds the wait; it does not extend it.** Each read inside a
  budget is given only what is left. Re-deriving the full allowance per read
  turns a bounded wait into an unbounded one, which is the failure this
  shape exists to prevent.
- **Data already in hand is delivered even once the budget is spent.** The
  budget gates the *next* read, not frames that have already been read. A
  complete line sitting in the buffer is returned even if the deadline passed
  while the reader was descheduled, because discarding it would report a
  silent endpoint for one that in fact answered. Blank lines carry no
  information, so once the budget is spent they are dropped and the read
  ends.

The reader treats a read that wakes with no bytes as "nothing right now"
rather than "nothing ever", so a bounded read can return before its deadline
if the reader is woken without data. The deadline is therefore an upper bound
on the wait, not a promise to use all of it.

## What ends a session, and the exit contract

There is no `session.close.request` envelope in v0.1. On this binding the pipe
is the session's lifetime:

- **stdin EOF** is the close. The endpoint stops accepting requests, settles
  what it already admitted, flushes stdout, and exits **0** after every
  co-hosted profile has also completed clean shutdown.
- **SIGINT / SIGTERM** behave as EOF.
- A teardown that cannot deliver what the endpoint already admitted, within a
  bounded window, exits **non-zero**. This is the hung-up host: one that closed
  its stdin but stopped reading its stdout, so the pipe fills and the last
  events cannot be written. Exiting 0 there would report a clean end for a
  session whose host is missing events it was acknowledged for.
- An endpoint stops if its output has made **no progress at all** for long
  enough that no host could still be reading. This bound is what makes the
  previous one reachable: once the pipe is full, an endpoint whose request
  handling writes its own answers cannot observe stdin closing either, because
  it is already blocked before the frame that would carry it. The bound
  governs an output that has not moved, not the pace of a host that is keeping
  up, so it is long — the reference endpoint uses two minutes. A slow host is
  not a gone one.
- A **malformed line** is the host's framing defect. The endpoint writes one
  bounded diagnostic to stderr and exits **non-zero**. It does not attempt to
  resynchronise, because a stream whose framing is in doubt cannot be trusted
  to carry the next boundary.

  Which lines those are is decided **before decoding**, on what the line
  declares itself to be, and the test is the one that governs an unknown
  control: did the frame parse, and was its boundary found?

  1. Not JSON, not a JSON object, or over the length bound — **fatal**. The
     boundary is genuinely in doubt.
  2. A `control` member and no `protocol` member — a control frame. Answered,
     never fatal, even when the endpoint implements no controls.
  3. A `protocol` member and an `id` — an envelope, and its framing is not in
     doubt whatever else is wrong with it. An unknown `type`, a payload that
     does not decode, a missing required member: all of these get a correlated
     `error.response` and the stream carries on. **Not fatal.**
  4. A `protocol` member and no `id` — **fatal**, and for a different reason
     than (1). The line said what it is, so framing is fine; but every response
     this binding defines requires `in_reply_to`, so there is nothing to
     address an answer to. The endpoint cannot answer it, answering something
     uncorrelated in its place would corrupt a stream a host reads by
     correlation, and dropping it silently would leave the host waiting forever
     for a response to a request it believes it sent.
  5. Neither `protocol` nor `control` — **fatal**. The JSON parsed, but nothing
     says what the line is, so no path could answer it.

  The distinction that matters is (3) against (1): an envelope that is merely
  *wrong* is a protocol error, and only a line whose framing or addressability
  is in doubt ends the process.
- An endpoint does not invent terminals. A run still in flight when stdin
  closes ends with the session, unobserved — the host that hung up has by
  definition stopped reading it, and a synthesised `run.failed` written into a
  closing pipe would be a terminal nobody receives and a trace nobody holds. A
  host that needs a settled run drives it to its terminal before closing
  stdin, which is what the conformance script does.

The exit code is part of the binding. A host that pipes a conformant endpoint
can distinguish a clean end from a framing fault without parsing stderr.

## What this binding does not carry

- **No adapter dimension.** One endpoint is one agent loop. There is nothing to
  name and nothing to select.
- **No multiplexed subscriptions.** One consumer, already attached.


## Cursor replay

A host that has fallen behind, or that wants a run's events again, asks for
them from a cursor.

Replay is a **transport control frame**, not an OAP envelope. This is the same
place the HTTP binding puts it: `GET /sessions/{id}/events?after=5` carries its
cursor in the query string, and a reconnecting SSE client carries it in
`Last-Event-ID`. Neither is an envelope, because a cursor is a fact about one
consumer's position in a stream rather than about the agent loop's state. v0.1
defines no replay envelope, and this binding does not invent one.

A control frame is a JSON object carrying a `control` member and no `protocol`
member, which is what distinguishes it from an envelope on the same line. A
host that never replays never sends one and never sees one.

An endpoint that does not recognise a control **answers it** and keeps going:

```json
{"control":"replay.error","id":"r1","code":"unsupported_control","message":"..."}
```

A control frame it has never heard of is not a framing fault. The frame parsed,
its boundary was found, and the only thing in doubt is whether this endpoint
implements it — so the stream is still trustworthy and the host is owed an
answer rather than a dead process. This is what keeps the control vocabulary
extensible: a host speaking a newer binding degrades to one that does not.

**Requesting a replay.** The host writes:

```json
{"control":"replay","id":"r1","session_id":"s1","run_id":"run-1","after":5}
```

`after` is the last sequence the host already holds; delivery resumes at
`after + 1`. `after: 0` replays the run from its first event. `run_id` may be
omitted, in which case the endpoint resolves the cursor onto the session's
current run — the same resolution the HTTP binding applies to a bare
`Last-Event-ID`. Naming the run is better and hosts should: sequences are
per-run, so an unqualified cursor means something different once a newer run
has been admitted.

**Answering it.** The endpoint writes exactly one control frame in reply,
correlated by `id`:

```json
{"control":"replay.accepted","id":"r1","run_id":"run-1","after":5}
{"control":"replay.gap","id":"r1","requested_after":5,"oldest_available":9,"latest_available":21}
{"control":"replay.error","id":"r1","code":"run_not_found","message":"..."}
```

After `replay.accepted`, the run's retained envelopes after the cursor follow
as ordinary envelope lines, and the stream then continues live. `replay.gap`
reports a cursor the endpoint no longer retains and carries the window that is
still available, so the host can ask again from `oldest_available - 1` rather
than guess. A gap is never papered over with a partial stream: an endpoint
that cannot honour a cursor says so instead of inventing continuity.

**Replay re-delivers envelopes the host already has.** That is the point of a
cursor, but it means a host assembling a trace across a replay must deduplicate
by envelope `id`, because the same envelope arriving twice is one event
delivered twice and not two events. A trace that keeps both copies is invalid
for a reason that has nothing to do with the endpoint.

**A run stream that dies says so.** The pipe stays open after a subscription
fails, so a host that simply stopped receiving envelopes cannot tell a dead
stream from a slow agent, and its trace would lack a terminal with nothing to
explain why. Each abnormal ending emits one frame naming the position the host
actually reached:

```json
{"control":"stream.lost","run_id":"run-1","after":12,"code":"overflow","message":"..."}
```

`code` is `overflow` when delivery fell behind, `frame_limit` when an envelope
could not be framed, or `stream_failed` for anything else. `after` is a cursor:
the host replays from it to continue. A clean end needs no such frame, because
the run's terminal envelope is already the marker.

Replay is distinct from reconciliation. `session.state.request` returns
authoritative state — what is true now — while replay returns a journal suffix
— what happened. v0.1 keeps resume, reconciliation, and replay separate, and a
host that wants the first should not be handed the third.

## Conformance

`goap conformance --command "<cmd>"` spawns the command, drives a scripted
session over this binding, assembles every envelope it sent and received into a
trace, and runs that trace through the same validator `goap validate` uses. It
then asserts the exit contract above.

The point of assembling a trace is that the verdict does not come from the
runner's own opinion. The runner drives; the validator judges; they are
different code, and the validator is the one the adapters are already held to.

The runner drives a **process**, not an in-process adapter, so it works against
any binary regardless of implementation language. `goap serve agent` is the
known-good target it is developed against.

**What the script needs from you, and what it does not.** It drives one run,
so it needs a run to be admittable. It does not need that run to succeed:
the terminal check accepts `run.completed`, `run.failed` or `run.cancelled`,
because core requirement 9 is exactly one terminal and not a successful one.
An endpoint with no provider configured, or one pointed at a model that does
not exist, still conforms — it admits the submission and settles the run —
and conformance does not require credentials or a reachable model server.

Naming a model is the one place the script cannot guess. An endpoint is
entitled to have no default model and to refuse a submission that names none.
So the script takes `--model`, and failing that reads the endpoint's own
`models.list` when it advertises one, preferring the entry that declares
itself default. An endpoint that advertises no catalog and holds no default
cannot be driven past submit by any host, which is worth knowing about that
endpoint rather than something the harness should paper over.

A check can also be **skipped**, which is not the same as failing. Cursor
replay is not among the Core Profile Requirements, and this binding says an
endpoint that does not recognise a control answers `unsupported_control` and
keeps going — so an endpoint that answers that way has done what it was told
to, and the runner records the obligation as one this endpoint does not carry.
Failing it would put a requirement in the harness that is in no document.

What the runner drives, and what it does not. It walks discovery
(`protocol.initialize` and `capabilities`), a session, an admitted run with
both scripted gates answered from the stream, a terminal, a cursor replay,
reconciliation, the stale-revision rule, the cancellation disjunction, and the
exit contract. Two of those are driven with deliberately wrong requests — a
revision the endpoint never issued, and a cancel for a run that has already
settled — and those exchanges are kept out of the assembled trace, because the
endpoint's answer is what is under test and the request is a fault the runner
committed on purpose.

`oapx conformance` judges each correlation inside one absolute probe budget
rather than a fresh allowance per frame, so an endpoint that keeps writing
frames nobody asked for cannot hold a correlation open indefinitely;
`--timeout-ms` sets it. Two rules keep such a verdict about the endpoint rather
than about scheduling. A frame it has already parsed out of the reader and into
the waiting correlation is handed to that correlation even when the budget
expires before it is looked at, because the frame is the endpoint's answer
rather than a late one. And a control left unanswered because the budget ran out
is reported as that, naming the budget, rather than as silence: an endpoint that
answered thousands of unrelated frames and then ran out of time has not answered
nothing.

Two numbers are worth stating plainly, because the bound moved and nobody
should have to infer it from a diff. The per-read allowance goes from 300s to
30s, and it stops being re-armed: before, each read was granted the full
allowance again, so a correlation could last as long as the endpoint kept it
going; now one correlation gets 30s in total and raising it is `--timeout-ms`.
The tenfold figure is the per-read one; the change that matters is that the
allowance stops being renewed, because that is what removes the unbounded case.
The 30s is a floor for conformance, not a statement about how long an endpoint
may take — an endpoint that needs longer raises the flag, and the failure names
the budget rather than calling the endpoint silent.

Writing is the other half of a correlation and it carries no budget at all, in
either tree: a write blocks until the child reads it. That is preexisting rather
than something this bound introduced, and it is recorded here because a budget
that covers reads and not writes is easy to over-read as complete.

That bound is `oapx`'s alone for now. `goap conformance` still re-arms
`--timeout` for every line it pulls, so a chatty endpoint holds its
correlations open there indefinitely, and it does not yet report a spent budget
as distinct from silence. Both sides are held to this section, so that
divergence is a gap in the Go runner rather than a difference between the
bindings, and it is closed by the Go runner adopting the same budget.

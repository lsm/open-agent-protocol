# OAP Endpoint Binding: Envelopes Over stdio

Status: proposed design
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Reference implementation: `oap endpoint`
Conformance runner: `oap conformance`

## What this binding is for

An *endpoint* is one agent loop that speaks OAP natively. It is the role a
harness takes when it stops being translated into OAP by an adapter and starts
emitting OAP itself.

This binding exists because the repository had no answer for an implementer
asking "what do I build, and how do I know it is right". The corpus tests
adapters, which reduce native frames; an endpoint has no native frames.
`adapter/adaptertest` is an in-process Go kit. `client/` and `clients/ts` speak
HTTP+SSE. What was left was hand-assembling a trace and running `oap validate`
over it, which is authored files agreeing with each other.

## What this binding is not

`oap serve --stdio` is a different thing and implementers should not build it.
That frontend exposes a **hub**: twelve ops, an `adapter` dimension, cursor
replay, and several subscriptions multiplexed over one pipe, each line wrapping
an OAP envelope inside a transport object with its own numeric `id`.

An endpoint implements none of that. It is one agent loop, not a registry of
them, and it carries the envelopes themselves — the payload `servestdio` puts
*inside* its `request` and `event` fields.

## Framing

- One OAP envelope per line, encoded as UTF-8 JSON, terminated by `\n`.
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
- **No ordering is promised between a response and an event.** An endpoint that
  emits a run's first events inside its submit handling may write them before
  the submit's own acknowledgement, and a host that assumes otherwise will
  deadlock against a conformant endpoint. A host reads whichever line arrives
  and dispatches on `in_reply_to`.

## Streaming is implicit

There is no subscribe request. Once a session is open, the endpoint writes that
session's run events to stdout as it produces them.

This is the main simplification the endpoint role buys. A hub needs subscribe
because it fans one session out to several consumers over separate
connections; an endpoint has exactly one consumer — the process holding the
other end of the pipe — and it is already attached. Nothing can be missed, so
nothing needs to be joined.

## What ends a session, and the exit contract

There is no `session.close.request` envelope in v0.1. On this binding the pipe
is the session's lifetime:

- **stdin EOF** is the close. The endpoint stops accepting requests, settles
  what it already admitted, flushes stdout, and exits **0**.
- **SIGINT / SIGTERM** behave as EOF.
- A **malformed line** — not JSON, not an envelope, or over the length bound —
  is the host's framing defect. The endpoint writes one bounded diagnostic to
  stderr and exits **non-zero**. It does not attempt to resynchronise, because
  a stream whose framing is in doubt cannot be trusted to carry the next
  boundary.
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
- **No cursor replay.** Replay exists for transports that reconnect: an SSE
  client resumes with `Last-Event-ID` after the connection drops. A pipe pair
  does not reconnect — if it breaks, the process is gone and so is the session.
  A host that needs to re-establish agreement after a gap uses
  `session.state.request`, which is reconciliation and returns authoritative
  state rather than a journal suffix. The three are distinct in v0.1 and this
  binding offers the one that its transport can honour.
- **No multiplexed subscriptions.** One consumer, already attached.

An endpoint that is reached over a transport which *does* reconnect should
expose replay there. This binding says nothing about that case.

## Conformance

`oap conformance --command "<cmd>"` spawns the command, drives a scripted
session over this binding, assembles every envelope it sent and received into a
trace, and runs that trace through the same validator `oap validate` uses. It
then asserts the exit contract above.

The point of assembling a trace is that the verdict does not come from the
runner's own opinion. The runner drives; the validator judges; they are
different code, and the validator is the one the adapters are already held to.

The runner drives a **process**, not an in-process adapter, so it works against
any binary regardless of implementation language. `oap endpoint` is the
known-good target it is developed against.

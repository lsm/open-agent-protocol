# OAP Provider Binding: Inference Envelopes Over stdio

Status: draft
Profile: `open-agent-protocol.model-provider-core`
Base protocol: `open-agent-protocol` version `0.1`
License: CC0-1.0 public domain dedication, or the nearest legally valid equivalent in jurisdictions that do not recognize public domain dedication.

This is the transport for
[the model provider profile](model-provider-core.md), and the sibling of
[the endpoint binding](endpoint-stdio.md) at the boundary one layer down. It
says how the profile's eighteen envelope types move over a spawned process, and
it defines the one thing the profile deliberately leaves to a binding: the
channel a caller-held credential crosses on.

Everything in the framing, correlation and exit sections is the endpoint
binding's, restated for a different envelope set rather than reinvented. The
sections that are genuinely new are **Concurrency**, **The credential
channel**, and the parts of **Ordering** that follow from a connection carrying
more than one inference.

## What this binding is for

A caller that wants one vocabulary over many inference vendors, talking to a
process it spawns. The implementation holds the provider configuration and the
credentials; the caller holds the conversation.

It is a **spawn binding**: the caller starts the process and owns its lifetime.
That is what makes a side channel possible at all, and it is why an HTTP binding
needs a different answer for credentials.

## Framing

Identical to the endpoint binding, and deliberately so — a host that has
implemented one has implemented the other.

- One JSON object per line, UTF-8, terminated by `\n`.
- The caller writes request envelopes to the implementation's **stdin**; the
  implementation writes responses and events to its **stdout**.
- **stdout carries protocol and nothing else.** Banners, logs and progress go to
  stderr.
- A line contains exactly one envelope and no literal newline.
- Lines are bounded, the implementation declares its bound, and a frame over it
  is a framing defect rather than a payload to shrink and retry.
- The five-case classification of a malformed line is the endpoint binding's,
  unchanged: framing in doubt is fatal, a declared envelope with a defect is a
  correlated error and not fatal, and an envelope with no `id` is fatal because
  nothing can carry its answer.

Every envelope on this binding carries
`profile: "open-agent-protocol.model-provider-core"`. An implementation that
serves both profiles on one binary serves them on **separate spawns**, not
interleaved on one pipe: the profiles have disjoint envelope sets and separate
scope domains, and a single stream carrying both would oblige every consumer to
implement both to route anything.

## Correlation and ordering

- `in_reply_to` correlates a response to its request, as in the endpoint
  binding.
- Every event scoped to an inference carries `inference_id` on the **envelope**
  and a positive, contiguous per-inference `sequence`. No payload repeats the
  scope.
- Requests and responses consume no sequence.
- **No ordering is promised between a response and an event.** An
  implementation that begins streaming inside its create handling may write
  `inference.started` before the create response, and a caller that assumes
  otherwise deadlocks against a conformant implementation. Dispatch on
  `in_reply_to` and `inference_id`, not on arrival order.

## Concurrency

One connection carries **many inferences at once**, and this is the substantive
difference from the endpoint binding, where one pipe is one agent loop with one
nonterminal run.

- A caller may have any number of inferences in flight, bounded by what the
  implementation advertises.
- Events for different inferences interleave freely on the pipe. The envelope's
  `inference_id` is what separates them, which is why it is on the envelope: a
  caller routes without decoding a payload.
- Each inference has its **own** `sequence` domain, starting at 1. Sequences
  from two inferences are never compared. A caller that ordered one inference's
  events by another's counter would read a collision between numbers that were
  never in the same space.
- The profile's stream-lifecycle errors live here rather than in the profile,
  because multiplexing is this binding's concern: a request naming an
  `inference_id` the implementation does not hold is answered with a correlated
  error, and an implementation never invents an inference to carry one.

## The credential channel

The profile requires a binding that can carry a credential value out of band to
do so, and this binding can. **Tier 1 is mandatory here.** An implementation on
this binding that advertises `credential_grant: "on_envelope"` is not
conformant.

### The channel is a listening socket the implementation names

On `provider.credential.grant.request` the implementation creates a listening
endpoint, and its `provider.credential.grant.response` carries the path in
`channel` before the grant completes. The caller connects, writes the nonce, a
newline, the credential value, and closes. The implementation answers the grant
only after the value arrives.

```
→ provider.credential.grant.request   { provider_id, nonce, ttl_ms? }
← provider.credential.grant.channel   { nonce, channel }          in_reply_to
      caller connects, writes  <nonce> \n <value>  , closes
← provider.credential.grant.response  { accepted, credential_ref } in_reply_to
```

Both answers carry `in_reply_to` naming the grant request. The `nonce` is the
grant's identifier, not the frame's correlator, and a caller with two grants in
flight routes on `in_reply_to` like every other response on this binding.

A nonce already in flight is refused rather than opening a second channel for
it.

`provider.credential.grant.channel` is the one envelope this binding adds to the
profile's set, and it carries no secret.

**A unix domain socket.** The `channel` member is an opaque string the caller
passes to its platform's connect call; this binding does not define its grammar
beyond that it is what the implementation was given by the operating system.

An earlier version of this binding said "a unix socket where the platform has
them, a named pipe where it does not," implying a platform fork. The first
implementation of this section did not have to write one: unix domain sockets
are available on every target it ships, Windows included, from Windows 10 build
17063. One mechanism, one code path, every release target — where a week of
named-pipe work had been budgeted.

A binding for a platform genuinely without them would need another form. None of
the platforms this has been built against is one, so that form is not specified
here rather than guessed at.

**Not a numbered descriptor.** The obvious spawn form — inherit descriptor 3 —
has no meaning on Windows, where an extra stdio slot is an inherited handle
rather than a numbered descriptor. A binding built on numbered descriptors would
make tier 1 unachievable on a platform real implementations ship, which turns
"mandatory where the binding allows it" into a rule any cross-platform
implementer may decline. That is worse than the work.

### The channel's own rules

- **A grant that cannot be announced is refused immediately, not left
  pending.** The deadline below runs from the channel envelope, so a grant whose
  socket could not be created has no deadline at all and nothing expires it.
  That window — between an accepted request and a channel that exists — is where
  a resource leak lives, and every implementation would otherwise invent its own
  answer for it.
- **The socket is created per grant and destroyed when the grant settles**,
  whether it settled with a value, a deadline or a closed connection. It is
  never reused for a second nonce.
- **Permissions are the narrowest the platform offers** — owner-only on a unix
  socket, and the implementation creates it in a directory only the owner may
  traverse. A world-writable socket is a channel any local process can grant on.
- **The implementation accepts exactly one connection.** A second is closed
  without reading.
- **A connection whose first line is not the expected nonce is closed without
  reading further**, and no error envelope is emitted. An error there answers a
  question the sender should not get answered.
- **The value is everything after the first newline**, to the close. It is bytes,
  not JSON, and the implementation does not parse it.
- **The arrival deadline is fixed by this binding at 30 seconds**, from when the
  implementation emits the channel envelope — which is why an unannounced grant
  is refused rather than held. A caller does not choose it: a
  caller-chosen arrival deadline is a caller-chosen duration to hold a half-open
  grant.
- **The envelope stream never blocks on the channel.** An implementation whose
  reader is single-threaded must not perform a blocking accept or read, because
  a silent channel would freeze its ability to answer anything — including the
  cancel a caller reaches for when a grant hangs.
- On deadline or on a connection closed without a value, the grant is answered
  with a **refused `grant.response`** — `accepted: false` and a typed `error` —
  **the nonce is burned**, and a value arriving afterwards is discarded rather
  than bound.

A refusal is the same envelope type as an acceptance, discriminated by
`accepted`, the way `inference.create.response` is. A caller sent one request
and watches one type for its outcome. The alternative — a generic error envelope
— would make a caller watch two types for one request, and the third
alternative, letting a refusal be a channel that never arrives, makes a caller
wait out a deadline to learn something the implementation already knows, which
is the shape the close-without-write rule exists to remove.

### What the caller must not do

- Write the value to stdout, stderr, or any file it did not create for this
  purpose.
- Reuse a nonce.
- Pass the `channel` path to anything but its own connect call.

## Spawning

- The caller spawns the implementation with the environment it intends it to
  have. **This binding defines no environment variable carrying a credential**,
  and an implementation must not read one that a caller sets expecting it to be
  used as a grant — the channel above is the only grant path.
- The implementation's configuration — which providers exist, where they point,
  what credentials the operator has given it — is the operator's and arrives by
  whatever means the implementation already uses. None of it crosses this pipe.
- A caller learns what it may ask for by sending `provider.describe.request`,
  which an implementation answers at any protocol version it supports.

## What ends a connection

The endpoint binding's contract, with inferences in place of runs.

- **stdin EOF** is the close. The implementation stops accepting requests,
  settles every inference it has accepted, flushes stdout, exits **0**.
- **SIGINT / SIGTERM** behave as EOF.
- Settling means a terminal per accepted inference. An implementation that
  cannot deliver the terminals it owes, within a bounded window, exits
  **non-zero** — the hung-up caller case, where exiting 0 would report a clean
  end for a connection missing events it was acknowledged for.
- **Every grant dies with the connection.** A granted credential is
  connection-scoped by the profile, and this is where that becomes concrete:
  process exit is the expiry, and there is no path by which a granted value
  outlives it.
- A malformed line is fatal as classified above, with one bounded diagnostic to
  stderr.

## What this binding does not carry

- **No session, run, turn or tool-call scope.** They have no referent here.
- **No cursor replay.** An inference is short and a dropped connection ends it;
  there is nothing to resume to. A caller that lost a connection starts a new
  inference.
- **No adapter dimension.** One spawn is one implementation.
- **Keepalives are the binding's own.** An implementation that needs one emits a
  control frame, never an envelope, and it consumes no sequence.

## Conformance

An implementation claiming this binding:

1. Emits nothing but envelopes and control frames on stdout.
2. Classifies a malformed line by the five cases and is fatal only for the two
   that warrant it.
3. Carries `inference_id` on the envelope of every scoped frame and in no
   payload.
4. Keeps a separate contiguous `sequence` per inference.
5. Advertises `credential_grant` as `none` or `out_of_band`, never
   `on_envelope`.
6. If it advertises `out_of_band`: creates a per-grant socket with
   owner-only permissions, accepts one connection, enforces the 30-second
   deadline, burns the nonce, and destroys the socket when the grant settles.
7. Never blocks its envelope reader on the credential channel.
8. Settles every accepted inference before exiting 0.

## Open questions

**The 30-second deadline is a number nobody has measured.** It is long enough
for a caller to read a path and connect, and short enough that a hung grant does
not hold a socket for a session. Both halves are guesses.

**One implementation has built the channel and nothing has driven it end to end
over a spawn.** The socket round trip is proved — listen, connect, write
`<nonce>\n<value>`, accept, read — and the grant is answered only after the
value arrives, with the nonce burned however it settles. What has not happened
is a caller spawning an implementation and granting across the pipe, which is
the thing this binding exists for. Until then the socket rules above remain the
likeliest place to be wrong, being the only part with no prior art in this
repository.

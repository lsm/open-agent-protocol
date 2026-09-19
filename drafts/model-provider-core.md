# Open Agent Protocol Model Provider Core

Status: draft
Profile ID: `open-agent-protocol.model-provider-core`
Base protocol: `open-agent-protocol` version `0.1`
License: CC0-1.0 public domain dedication, or the nearest legally valid equivalent in jurisdictions that do not recognize public domain dedication.
Scope: the boundary between an agent loop and a model provider — one inference
call, in both directions.

This is the second profile, and a peer of
[Agent Control Core](agent-control-core.md) rather than a unit inside it.
[Decision 0016](../decisions/0016-model-provider-profile.md) establishes the
boundary, argues why it is a profile, and defers the envelopes to a draft. This
is that draft. [The composition draft](composition.md) places both profiles in
the layering.

Nothing here has a session, a run, a run sequence or an interaction. Admission,
terminal arbitration and per-run ordering are agent-control concepts and have no
referent below the loop.

## Design Rule

**Normalize the shape. Do not normalize away the disagreement.**

Every agent loop that supports more than one vendor writes the same
normalization: OpenAI and Anthropic disagree about request shape, streaming
deltas, tool-call encoding, usage accounting and stop reasons. Presenting one
vocabulary over them is the point of the profile.

But a vendor that claims a wire and then breaks it in one place is the common
case, not the exception, and an implementation that hides which place it is
facing leaves its caller unable to work. So the profile carries a closed set of
**compatibility facts** beside the descriptor, and an implementation states
which it is facing rather than pretending uniformity it does not have.

This is the rule that separates this profile from a lowest-common-denominator
API. The vocabulary is uniform; the honesty about the endpoint is not optional.

## What This Profile Is Not

It is not an inference API. It does not replace a vendor's own SDK for anything
beyond one call, and it takes no position on how a vendor should design theirs.

It is not part of `agent-control-core`, and no envelope, rule or capability of
that profile changes because this exists. An endpoint implementing only agent
control is unaffected. The statement in that draft that direct inference is not
carried on its wire remains exactly true.

It is not a vendor API surface. Batching, embeddings, fine-tuning, files,
assistants, and vendor server-side tools are not an agent loop's lower
boundary. An implementation may use them directly; they do not cross here.

It is not a commitment that this repository will ship an agent loop. The
profile is what an agent loop speaks downward.

It is not a credential channel. A caller-held credential crosses out of band
wherever the binding allows it, and only on one envelope type where no side
channel exists. Members that could carry one instead — header maps — are
non-conformant for that use and policed as far as a validator can. See
Credentials below.

## Identity

Three identifiers, deliberately distinct, because two of them are routinely
conflated in speech:

- **`provider_id`** — an opaque, implementation-scoped id naming a *configured
  vendor endpoint*. Not a registry this project assigns meanings in.
- **`wire`** — the *request shape* spoken to it, from a closed set.
- **`model_id`** — the model, as the provider names it.

A **model reference** carries all three: `provider_id/wire@model_id`, with the
middle component written `other:<wire_id>` when the wire is unnamed.

#### `wire_id`, and why `other` alone is not enough in a reference

`wire` is in the reference because two endpoints may share a wire and **one
endpoint may offer more than one**. For an unnamed wire the first half still
works — `provider_id` disambiguates — and the second half fails completely: every
provider saying `other` produces `<provider>/other@<model>`, so a provider
offering two unnamed shapes emits `p/other@a` and `p/other@b` with nothing
saying they speak differently.

That is not hypothetical. A vendor with two distinct generative APIs under one
provider is the case, and it exists in the registry this profile was mapped
against.

So a descriptor whose `wire` is `other` carries **`wire_id`**: an opaque,
implementation-scoped label, required when an implementation offers more than
one unnamed shape and optional otherwise. The reference then reads
`ollama/other:ollama-chat@llama3`, and a parser takes the component up to the
first `:` as the wire value.

`wire_id` **must be absent when `wire` is named**, and an implementation refuses
one that is not. Otherwise it becomes a shadow vocabulary: a second, unbounded
label riding alongside the closed set, and callers start reading it because it
is there.

**This does not reintroduce the vendor enum, and the difference is the whole
point.** `wire` is a closed set a caller may branch on: reading
`anthropic-messages` tells it something portable about a behaviour family.
`wire_id` is a discriminator, not a description — opaque, endpoint-scoped,
assigned no meanings by this project, exactly like `provider_id`. **A caller must
not branch on it.** Two providers emitting the same `wire_id` string say nothing
to each other; one provider emitting two says only that they differ.

That is the honest place for a vendor name to live: somewhere a caller can tell
things apart and cannot pretend to understand them.

`provider_id` and `wire` are different layers. Two vendor endpoints may share a
wire, and one vendor endpoint may offer more than one. An implementation that
keys its providers by wire loses the distinction the moment a second endpoint
speaks the same shape, which is
[Decision 0017](../decisions/0017-provider-provisioning.md)'s reason for giving
the descriptor both members.

### Wire

A closed set of **named** shapes, plus one escape:

- `openai-responses`
- `anthropic-messages`
- `openai-chat-completions`
- `other`

Adding a named value is a protocol change, not configuration.

#### Why there is an escape, and what it costs

An earlier version of this draft had the three names and no escape, and said a
wire the set does not name is reported `unavailable` rather than approximated to
a neighbour. Mapped against a real registry of eight APIs, that names five.
Azure, Codex and native OpenAI all land on `openai-responses`; Google's
generative API and Ollama land on nothing.

Two consequences, and the second is the one that forced the change:

**Google is not an edge case.** It is two of those eight and a major vendor.

**The profile reasoned from a provider it could not describe.** `ndjson` is in
the framing set below because Ollama is newline-delimited with no SSE parser.
`allows_anonymous` exists because a local Ollama needs no credential. Both
arguments are sound and both cite the one provider the wire set excluded — so
*no provider this profile could express used `ndjson` framing*, while `ndjson`'s
justification rested on a provider it could not serve. A member's reasoning and
its reachability had come apart.

**The escape is not "add `ollama`."** A set that grows one vendor at a time is
what a closed set exists to prevent, and the second category has no natural
bound: `openai-chat-completions` is a shape a dozen vendors implement,
`anthropic-messages` is one vendor's shape that others emulate, and Ollama's is
one vendor's shape that nobody emulates. Mixing de facto standards with specific
vendors in one enum guarantees this recurs.

So the criterion for a named value is stated rather than left to taste:

> A wire is named when **more than one independent implementer speaks it**.
> A shape only its originator implements is `other`.

That is how the three present values arose, it bounds growth, and it gives a
shape a way to graduate later — if Google's becomes widely emulated, it earns a
name, and nothing about the providers already describing themselves as `other`
breaks.

**What `other` costs, explicitly.** A caller never builds a vendor request —
that is the profile's whole point — so `wire` is not how a caller talks to a
provider. What it buys is the ability to reason across providers: that two share
a request-shape family, and therefore a behaviour family and a failure domain.
`other` gives that up. The compatibility facts still describe the endpoint, and
`endpoint` still names it, but a caller learns nothing portable about the shape
underneath and must not infer any.

That is a real loss and it is now explicit rather than silent. `unavailable`
made the provider undescribable; `other` makes it describable with a named gap.

#### Attested: the provider/wire split holds on real endpoints

Three of those eight — Azure, Codex and native OpenAI — are distinct providers
speaking one wire, told apart by `provider_id` and `endpoint`.
[Decision 0017](../decisions/0017-provider-provisioning.md) argued for keeping
`provider_id` and `wire` as separate members on the grounds that an
implementation keying providers by wire loses the distinction the moment two
endpoints share a shape. That was an argument; this is three endpoints where it
happens, in one registry.

### Framing

Streaming framing is a **separate member** from wire, from the closed set
`sse`, `ndjson`, `unary`.

The split is attested. Makai's provider layer serves six of eight APIs through
one SSE parser and parses Ollama as newline-delimited JSON with no SSE parser at
all, read from that tree on 2026-09-17. This repository's own prober assumes the
other way: `provider.RunEvidence` fails any response whose media type is not
`text/event-stream` (`provider/evidence.go`), which is correct for a
compatibility prober and would be wrong for the profile. A profile that folded
framing into wire would exclude a working provider while believing its set
complete.

**`sse` and `ndjson` are attested; `unary` is not.** Until the wire set gained
`other`, `ndjson` was also unreachable — the only provider attesting it was one
the profile could not name. The implementation carried a test asserting that no expressible
provider yields `ndjson`, written to start failing when that stopped being true.
It fired one change later: with `other` in the wire set, Ollama describes itself
and the assertion inverted to one reachable `ndjson` source. The tripwire is
recorded because it did its job, not because it still stands.

All eight of Makai's APIs stream, and their non-streaming call is a facade that opens a stream, drains it
and returns the result rather than a separate framing. This repository's prober
always requests a stream. So `unary` is here because a non-streaming provider is
an ordinary thing to build against, not because either source demonstrates one,
and it must not be cited to Makai's finding — that finding supports the split
and two of the three values.

## Envelope

The base envelope of the protocol, with `profile` set to
`open-agent-protocol.model-provider-core`.

Required: `protocol`, `version`, `profile`, `type`, `id`, `payload`.

Optional: `timestamp_ms`, `in_reply_to`, `sequence`, `extensions`, and one scope
field:

- **`inference_id`** — the scope of one inference call, and a distinct identity
  domain. It is not a session, run, turn, tool call or interaction id, and is
  not interchangeable with any of them.

The id is allocated by the implementation and first appears on an **accepted**
`inference.create.response`. So the envelope scope field is **absent on
`inference.create.request`**, where no id exists yet, **absent on a refused
response**, where no inference exists at all, and **present on every envelope
after an acceptance** — that response included.

Setting it on the response is deliberate: the envelope scope field is how a
consumer routes a frame without decoding its payload, and leaving it off exactly
one frame would make the first frame of every inference the special case.

**No payload carries `inference_id`.** The envelope's scope field is the single
place it appears, and a payload is not self-describing when detached from its
envelope — a trace assembler holding a bare payload does not know which
inference it belongs to, and is not meant to. An earlier version of this draft's
tables listed it as payload content on three envelopes and the schemas required
it on eleven, which is the `action` problem with a copied field instead of a
derived one: two sources of truth, in more definitions, with nothing telling a
consumer which to believe. Where
the id appears in both the envelope and the payload the values must agree, which
is agent control's rule and is unchanged here. The response still consumes no
`sequence` — carrying a scope and consuming an ordering number are different
things.

`session_id`, `run_id`, `turn_id` and `interaction_id` do **not** appear on this
wire. An agent loop that holds all of them keeps the correlation on its own
side; carrying them down would make the provider boundary depend on concepts it
has no use for, and would make a provider implementation harder to write than
the vendor API it wraps.

Every event scoped to one inference carries a positive, contiguous `sequence` in
that inference's own ordering domain. Requests and responses do not consume it.
The guarantee is the one agent control gives and no more: contiguity witnesses
transport loss, never upstream loss.

## Transport

Transport agnostic, like the core. The same envelopes move over in-process
calls, newline JSON over stdio, WebSocket, or HTTP plus SSE.

Keepalives are a **binding concern and not an envelope**. Makai's event union
carries a `keepalive` variant because their transport needs one; a binding that
needs one emits it at the binding layer, where heartbeats already live for
agent control.

## Serving Modes

The two profiles are **independently servable**. That is a property this profile
permits, and it is the practical consequence of making it a peer profile rather
than a unit. It is not a description of any implementation that exists.

One binary may expose either. Started one way it serves `agent-control-core`: a
client drives sessions and runs, and the agent loop is behind it. Started the
other way it serves `model-provider-core`: a caller drives one inference call at
a time, and inference endpoints are behind it. Same process, same transport,
different profile in the envelope.

```
  client ──agent-control-core──▶ [ binary ] ──▶ agent loop ──▶ providers
  caller ──model-provider-core─▶ [ binary ] ─────────────────▶ providers
```

The second mode is the smaller and more immediately useful deployment, and is
why the profile is worth having before any agent loop adopts it. A caller that
wants one vocabulary over many inference vendors, and does not want an agent
loop, gets exactly that — one language to OpenAI-flavour, Anthropic-flavour and
everything else behind it, without embedding a vendor SDK per vendor.

An implementation may serve both at once, one, or neither. Serving one implies
nothing about the other: a provider-profile implementation with no agent loop is
conformant, and so is an agent-control endpoint that reaches its model through a
vendor SDK and speaks this profile nowhere.

### Nothing serves this profile today, and the gap is not plumbing

Stated plainly because the diagram above invites the opposite reading.

Makai is the closest, and the distance is instructive. Read from that tree on
2026-09-17: `makai --oap` serves `agent-control-core` and refuses every other
profile — a single profile constant, a hello that rejects anything else, a
profile mismatch on any envelope mapped to a decode error, and no flag that
widens it. Its other mode hosts auth, provider and agent protocol servers in one
process over a line binding, but those speak that project's own native wire, and
none of its OAP files is reachable from that path.

So the **architecture** is already there — a provider protocol server behind a
line binding — and a serving mode for this profile would be a translation layer
over it rather than new plumbing, the same relationship its OAP bridge has to
its agent loop. What does not exist anywhere is a mode that speaks this profile.
The shape is there; the mode is not, and building it is the work.

[The provider stdio binding](provider-stdio.md) is the transport, and it is the
same shape as [the endpoint binding](endpoint-stdio.md) — raw OAP envelopes, one
per line, the profile distinguishing which vocabulary is in play. What it adds
is what a connection carrying many inferences at once needs, and the credential
channel this profile leaves to a binding.

## Envelope Types

Eighteen, in five groups.

### Discovery

| Type | Direction | Carries |
| --- | --- | --- |
| `provider.describe.request` | caller → implementation | nothing required |
| `provider.describe.response` | implementation → caller | `providers[]`, `capability_revision`, `protocol_versions[]`, `profile_revision?` |
| `provider.models.list.request` | caller → implementation | `provider_id?` |
| `provider.models.list.response` | implementation → caller | `models[]`, `capability_revision` |

`capability_revision` is required on both responses, for the reason agent
control requires it on `capabilities.response` and `models.response`: the whole
content is bound to one descriptor snapshot.

#### The model entry

Each entry in `provider.models.list.response`:

- `model_ref` — `provider_id/wire@model_id`.
- `model_id`, `display_name?`, `provider_id`, `wire`
- `context_window?`, `max_output_tokens?`
- `capabilities` — from `chat`, `streaming`, `tools`, `vision`, `reasoning`,
  `prompt_cache`, `audio_input`, `audio_output`.
- `lifecycle` — `stable`, `preview`, `deprecated`.
- `source` — `discovered` or `fallback`.
- `reasoning_default?` — `off`, `minimal`, `low`, `medium`, `high`, `xhigh`.
- `auth_status` — below.

`source` distinguishes a catalog the implementation read from the provider from
one it fell back to from a built-in list. Merging the two silently is how a
client confidently offers a model that no longer exists; a caller looking at a
fallback catalog is looking at something that may be months stale and should be
told.

#### `auth_status`, and a correction

`auth_status` is on the model entry, from five values: `authenticated`,
`login_required`, `expired`, `failed`, `unknown`.

An earlier version of this draft recorded it as homeless — following credential
acquisition, owned by no profile. That came from a true finding read the wrong
way. The finding is that a provider layer *consumes* a credential at request
time and never learns its status, so it cannot compute one. The conclusion drawn
was that it therefore does not belong on this boundary, and that does not
follow: Makai carries it on their model descriptor, on this boundary, today. A
caller listing models wants to know which it can actually call, and
`login_required` against `authenticated` is exactly that. It is a property of
the catalog entry, not of an in-flight request.

Two of Makai's seven values are excluded: `refreshing` and `login_in_progress`
are transient states of a flow happening elsewhere, and an answer that is stale
before it is read is worse than no answer. The remaining five are stable enough
to describe a model by.

**`auth_status` is not bound to `capability_revision`.** It is read at the
moment the response is built and may be false immediately after — which is
precisely why
[Decision 0014](../decisions/0014-provider-descriptors.md) refuses it on a
*provider descriptor*, whose whole content is fixed for a revision. The
resolution is that a response is generated per request while a descriptor is
fixed per revision, so the volatile fact belongs on the entry in the response
and not in the descriptor. That distinction is the whole of 0014's objection and
it is satisfied, not overridden.

### One inference call

| Type | Direction | Carries |
| --- | --- | --- |
| `inference.create.request` | caller → implementation | the call (below) |
| `inference.create.response` | implementation → caller | `accepted`, `honoured`, or a typed refusal |
| | | `honoured` is `{ include_snapshot }` — the effective values for every request member an implementation may downgrade, currently one |
| `inference.started` | implementation → caller | `model_ref`, `started_at_ms`, `endpoint?` |
| `inference.part.started` | implementation → caller | `part_index`, `part_kind`, and for a tool call its `tool_call_id` and `name` |
| `inference.part.delta` | implementation → caller | `part_index`, `delta`, `snapshot?` |
| `inference.part.ended` | implementation → caller | `part_index`, the kind's content below, `carry?`, `snapshot?` |
| `inference.completed` | implementation → caller | `message`, `stop_reason`, `usage?` |
| `inference.failed` | implementation → caller | `error`, `usage?` |

#### Member names

An earlier version of this draft described the part payloads in prose — "the
complete tool call", "the accumulated string" — and named no members. There was
therefore nothing for a second implementation to agree with, and the first one
invented names because it had to. Naming them is part of what the schemas are
for, and the names belong here first.

| Where | Member | Is |
| --- | --- | --- |
| `part.delta` | `delta` | the increment, a string for every kind |
| `part.ended`, kind `text` or `reasoning` | `text` | the accumulated string |
| `part.ended`, kind `tool_call` | `tool_call` | `{ tool_call_id, name, arguments_json }` |
| `part.ended`, kinds `tool_call` and `reasoning` | `carry` | the opaque value handed back on the next call |
| `part.delta`, `part.ended` | `snapshot` | the accumulated message, an array of `Message` |
| any payload | `error` | a `ProtocolError` |

`arguments_json` rather than `arguments`, because the member is a string
containing JSON and a name that does not say so invites a decoder to treat it as
an object.

**No `action` member on a `ProtocolError`.** The first implementation emitted
one — `retry`, `refresh`, `authenticate`, `report`, `accept` — derived entirely
from `code`, and then argued for its own removal: a derived field on a wire is
two sources of truth that can disagree, with nothing telling a caller which to
believe, in exchange for what a caller gets from a lookup table. The error table
above is that lookup table, and the profile forbids carrying its output.

#### `endpoint` on `inference.started`

Optional, and unset by an implementation that does not need it.

It is derivable today: `model_ref` names a provider, the descriptor names an
endpoint. It stops being derivable the moment an implementation routes
dynamically — failover, regional routing, replicas — and the profile permits
that without saying so, because `endpoint` is published *per descriptor* and
nothing says it is the endpoint every call reaches.

At that point it stops being a debugging convenience. A caller with
data-residency obligations needs to know which region ran the inference, and
"the descriptor said eu-west" is not an answer if the implementation failed over
to us-east. No implementation routes dynamically today, so this is reasoning
rather than evidence — but it is cheap now and cannot be retrofitted, because a
caller cannot ask about a call that has already completed.

#### The terminal agrees with its parts

The draft said `inference.part.ended` carries the accumulated string and
`inference.completed` carries `message`, and said nothing about how the two
relate. Three readings were consistent with it — the terminal is the
concatenation of the deltas, or of the `part.ended` strings, or whatever the
vendor's final message says — and they come apart the moment a vendor normalizes
whitespace or emits an ended string that is not byte-identical to its own
deltas.

Two rules, in order of authority:

1. **`part.ended` is authoritative over the deltas that preceded it.** A delta
   stream is a transport detail; an ended part is a statement about what the
   part was. A consumer that applied every delta and a consumer that took only
   `part.ended` must land in the same place, which means the ended content
   replaces the accumulated buffer for that part rather than being compared with
   it.
2. **`inference.completed.message` is the assembly of the ended parts, in
   `part_index` order**, for an inference that streamed parts. A unary
   implementation emits no part envelopes and its terminal is the whole answer;
   the rule binds what was streamed, not what could have been. A terminal that disagrees with its own parts is
   unreconstructible: a consumer holding the parts cannot tell whether it lost
   something or the implementation changed its mind.

A vendor's own final message that differs from what its parts already said does
not override rule 2. Once a part is closed, its content is what the profile
says it was; the vendor's variant is preserved under `extensions` and does not
silently become `message`. An implementation that can reconcile before closing a
part should — the right place for that is at `part.ended`, not at the terminal.

**The validator enforces this**, unlike the credential rules: a trace that
streams parts and whose `inference.completed.message` is not their assembly is
invalid — `terminal_not_assembly`, checkable from the envelopes alone. A trace
with no part envelopes is outside the rule rather than failing it, which is what
keeps a unary implementation conformant. It is the first rule in this draft
that came from the build *and* falls inside the machinery this project already
has.

**A refusal allocates nothing.** `inference.create.response` carries
`inference_id` only when `accepted` is true; a refused response carries the
typed error and no id, and is correlated by `in_reply_to` alone. An inference
exists if and only if it was accepted.

This follows agent control, where `MessageSubmitResponse.run_id` is present only
on admission and a refused submission produces no run. The alternative — hand
back an id with the refusal — creates an inference that owes a terminal it will
never get, and a caller reading the terminal rule literally parks forever
waiting for one. Delivering the refusal as `inference.failed` instead would work
and was rejected for a different reason: it makes every caller handle a stream
that may consist only of its own failure, to express something the response
already says.

Exactly one terminal per **accepted** inference: `inference.completed` or
`inference.failed`.

#### Refusal time against terminal time

> **Anything decidable from the descriptor and the request alone is a
> create-time refusal, never a terminal. The terminal is for what the provider
> tells you.**

A missing credential, an unsupported snapshot policy, an unknown model, a
malformed reference — all knowable before the request leaves the building. A
rate limit, a provider outage, a credential the store believed was good and
was not — all discovered by attempting, and therefore terminals.

This was found by an implementation writing the wrong one and nothing
complaining. Checking credentials at stream-start produced this:

```
← inference.create.response   accepted=true
← inference.failed    seq=1   credential_missing
```

which satisfies every other rule in this draft — one terminal, contiguous
sequence, correct error class, correct derived action — and is plainly worse
than refusing at create. It allocates an inference, opens a scope, burns a
sequence number and settles it, to say something that was knowable without
touching a provider. A caller that has to tear down a stream to learn its
request was never viable has been told late for no reason.

The rule generalizes what the snapshot ruling decided for one member: refuse
before the tokens are spent, not after. It also puts `credential_missing` and
`credential_expired` at different points in the lifecycle, which reads right —
missing is knowable from configuration, expired is discovered by trying. Same
Authenticate/Refresh split, arriving at different times.

**The distinction is invisible to a conformance check and very visible to a
caller**, which is why it is written down rather than left to taste.

**A consequence worth naming, because it was not designed for and turns out to
matter.** Since a refusal allocates nothing and owes no terminal, an
implementation that answers discovery and refuses every inference is conformant
against every rule except the streaming ones it does not claim. A
discovery-only endpoint is a real endpoint, not a stub, and it is not lying
about anything. That lets an implementation ship the profile in stages and be
honest at each one, which is worth more than it cost.

An aborted call ends as `inference.completed` with `stop_reason: aborted` — cancellation at this boundary is a stop reason, not a
third terminal, because the vendor reports it that way and inventing a terminal
would put the profile's arbitration above the provider's own.

### Cancellation

| Type | Direction | Carries |
| --- | --- | --- |
| `inference.cancel.request` | caller → implementation | `reason?` |
| `inference.cancel.response` | implementation → caller | `accepted` |

As in agent control, the response is intent and not settlement. The terminal is
the settlement.

### Part kinds

`part_kind` is a closed set: `text`, `reasoning`, `tool_call`.

The started/delta/ended triple per part is taken from Makai's event union, which
carries the same triple for text, thinking and tool calls. The `ended` event is
load-bearing rather than decorative: without it a consumer cannot tell a
finished part from a stalled one until the terminal arrives, and a tool call
that is complete is dispatchable immediately.

**The triple is not uniform across kinds, and the payloads are
kind-discriminated.** An earlier version of this draft collapsed the nine
variants into three envelopes with a flat payload and a kind tag. That is lossy,
in two places their union makes explicit:

- `inference.part.started` carries `tool_call_id` and `name` for a `tool_call`,
  and neither for `text` or `reasoning`. A uniform start drops the tool call's
  identity at the moment a consumer needs it to open a pending call.
- `inference.part.ended` carries the complete tool call for a `tool_call`, and
  the accumulated string for `text` and `reasoning`. Those are different types,
  not different values of one type. Both `tool_call` and `reasoning` may also
  carry an opaque `carry` — see The Call.

`part_index` is the correlation, and maps onto their `content_index`.

**Both directions are enforced at the decode boundary, and the second direction
is the load-bearing one.** A `tool_call` start without `tool_call_id` and `name`
is refused, and a `text` or `reasoning` start *carrying* either is refused too.

Refusing the first protects the consumer. Refusing the second protects the
profile: it is how an encoder that kept a flat payload internally and stamped a
kind onto it gets caught at its first frame, rather than shipping a wire that
decodes cleanly and quietly means something else. That is the collapse this
section removed, reappearing as an implementation detail — and the only thing
that catches it is a decoder that objects to a field being present.

It costs four lines at the boundary, measured in an implementation rather than
estimated, which is why it is a requirement and not a suggestion.

**Enforce it at emission too, not only on decode.** An implementation that
refuses to *build* an invalid frame stops a host from constructing one and
discovering it in somebody else's decoder, which is where a wire bug becomes an
interoperability incident rather than a failing test. The same applies to the
other structural invariants here: a delta with no open part, a mismatched part
index, a terminal with a part still open, a second terminal. All are cheap to
refuse at the source and expensive to diagnose from the far end.

### The running snapshot: push and pull are different mechanisms

Every one of Makai's ten part variants carries `partial`, the full running
assistant message rather than the increment, with two modules existing only to
move it. They *also* have a `sync_request`/`sync` pair by which a consumer asks
for the current snapshot and gets it, or gets nothing if the stream is gone.

Deltas alone oblige the consumer to be lossless: apply every increment, in
order, without dropping one, or its reconstruction silently diverges. The
per-inference `sequence` tells a consumer *that* it diverged. A snapshot is how
it recovers.

**Push and pull do not subsume each other and the profile carries both.**

- **Push** costs bandwidth on every stream whether or not anyone diverged, and
  is the only thing that helps a consumer that diverged *without noticing*.
- **Pull** costs a round trip exactly when someone noticed, and requires the
  producer still to be holding the state.

#### Push: `include_snapshot`, per request

`inference.create.request` carries `include_snapshot`, and
`inference.part.delta` and `inference.part.ended` carry an optional `snapshot`
when it is set.

**It is per request, not per provider.** An earlier version of this draft put a
`snapshot_policy` on the descriptor, and that is the wrong axis: what a consumer
can afford to reconstruct is a property of the consumer, and two clients against
one provider can reasonably differ. The descriptor *declares* which policies it
supports — `snapshot_policies`, from `never`, `on_part_end`, `every_delta` — and
the request picks one. Declaring the capability and choosing the value are
different jobs, and the earlier version did both in the same place.

`every_delta` is quadratic in the message, which is a real cost on a long
completion and an obvious one on a slow link. That is why it is chosen rather
than assumed.

#### A snapshot must be able to carry an in-flight tool call

`snapshot` is the accumulated message, and a message's content is content parts.
`ContentPart`'s tool call carries `arguments_json`, whose type says complete
JSON. **Mid-stream a tool call's arguments are not complete JSON** — an
implementation accumulates `{"q":` and then `"zig"}` as separate deltas, and no
value of `arguments_json` expresses the first state.

An earlier version of this draft did not notice, and the first implementation
worked around it by building text-only snapshots — which silently omits a tool
call in flight *and* a completed one earlier in the same message.

**This defeats the snapshot for the part kind where divergence matters most.** A
consumer that loses a text delta renders slightly wrong output. A consumer that
loses a tool-call delta calls a function with the wrong arguments. The mechanism
that exists so a diverged consumer can recover was unavailable for the only case
where divergence is consequential, in both directions — push and pull have the
same hole because they carry the same object.

So a snapshot's tool-call part carries **either** `arguments_json`, when the
arguments are complete, **or** `arguments_partial`: the accumulated fragment as
an opaque string that is **explicitly not valid JSON** and must not be parsed.
Exactly one, never both.

**Within a snapshot, `arguments_partial` being present *is* the openness
marker** — nothing else in an array of messages says a part is still open — so
"only on an open part" is not a separate rule and cannot be written as one.

**The rule that protects something is on the terminal.**
`inference.completed.message` must not contain `arguments_partial` at all. That
is where a fragment would do real damage: a terminal handing a caller
unparseable arguments as if they were the finished call, which is the wrong tool
invocation this whole section exists to prevent. It is also expressible without
any notion of openness, which the snapshot rule is not.

The two alternatives, and why they lose. Omitting in-flight parts and saying so
is cheap and honest and leaves the recovery gap open for exactly the case that
needs it. Emitting snapshots only at part boundaries is what `on_part_end`
already is, and it would make `every_delta` incoherent rather than merely
expensive — a policy whose whole purpose is mid-part recovery cannot be defined
to exclude mid-part state.

#### Pull: `inference.sync`

| Type | Direction | Carries |
| --- | --- | --- |
| `inference.sync.request` | caller → implementation | nothing beyond the scope |
| `inference.sync.response` | implementation → caller | `snapshot`, or absent when the inference is no longer held |

An absent snapshot is an answer, not a failure: the inference has ended or been
released, and the consumer should stop waiting rather than retry.

`never` is always supported and need not be declared: a request asking for no
snapshot cannot fail for want of a capability, because it is the absence of one.
An empty or absent `snapshot_policies` therefore means this provider offers no
snapshots, not that every call is refused.

An implementation may support pull, push, both, or neither, and says which. A
consumer with neither must be lossless, which is a legitimate thing to require
of a consumer on a reliable local transport and a poor thing to require over a
network.

#### Asking for a policy the provider does not offer

**The default is refusal, and downgrade is opt-in.**

An `inference.create.request` whose `include_snapshot` the provider does not
support is refused with `unsupported_feature`, naming the policy, unless the
request also carries `allow_degraded_features` listing
`inference.snapshot` — in which case the implementation downgrades to the best
it offers and reports the effective value in `honoured.include_snapshot`.

`honoured` is an object of effective values, one member per request member an
implementation may downgrade. Today that is `include_snapshot` alone, and it is
an object rather than a bare value so that a later downgradeable member does not
change the shape. It is present whenever the response is an acceptance, carrying
what was asked for when nothing was downgraded — a caller reading it never has
to know whether a downgrade happened to know what it got.

`inference.snapshot` is the degrade key, in the same namespace as agent
control's feature keys.

This is agent control's existing mechanism, not a new one: `session.open`,
`submit` and `action.tools.list` all carry `allow_degraded_features` for exactly
this shape, and a caller that has met one has met this.

The alternative — downgrade silently and report the truth in `honoured` — was
the implementation's provisional choice and is defensible: the caller gets a
working inference and can read what it actually got. It loses on one point that
decides it. **The two behaviours are indistinguishable to a caller that does not
read `honoured`, and a caller that does not read `honoured` is the common case.**
A caller that asked for `every_delta` because it cannot be lossless over a bad
link, and silently got `on_part_end`, finds out by diverging under load. It
should find out before the tokens are spent.

Refusal is also the direction this project's fail-closed discipline already
runs: an adapter that cannot honour an attachment refuses rather than returning
a session that looks like it worked. Opt-in degradation is how a caller that
genuinely does not care says so, once, in the request.

## The Call

Split as the implementations split it, because per-call and per-provider are
different sets and merging them makes both wrong.

### What does not cross, from a real options struct

Makai's `StreamOptions` carries twenty-five fields and is the closest thing
either side has to a complete per-call list. Three are rejected outright and
two are relocated, on their own reading as much as this draft's:

- `cancel_token`, `on_payload_fn`, `on_payload_ctx` and
  `requires_owned_stream_events` are in-process function pointers and
  memory-ownership flags. They are in that struct because it doubles as an
  internal call-options type, which is a design smell on their side rather than
  a protocol shape.
- `http_timeout_ms` and `ping_interval_ms` are transport-shaped and belong to a
  binding.

Naming them is worth the lines because a reader comparing the two lists will
otherwise assume the profile forgot them.

### Per call — `inference.create.request`

- `model_ref` — provider, wire and model.
- `messages` — shared `Message` and `ContentPart` shapes with agent control.
- `tools` — `{ name, description?, input_schema }`, the intersection with agent
  control's `ToolDefinition` rather than a reuse of it; see Shared Vocabulary.
- `tool_choice`.
- `max_output_tokens`.
- sampling controls — `temperature`, `top_p`.
- `output_schema` — structured output.
- `stream` — boolean.
- `reasoning` — `{ enabled?, budget_tokens?, effort? }`. The carry is not here;
  see below.
- `include_snapshot` — `never`, `on_part_end`, `every_delta`.
- `allow_degraded_features` — keys the caller will accept a downgrade on.
- `headers` — non-secret request headers, such as tenancy or routing. Never a
  credential; see Credentials.
- `credential_ref` — names a credential the implementation holds; never a value.
- `metadata` — opaque, and it stays with the endpoint. See below.

**`metadata` does not reach the provider.** An earlier version of this draft
said "opaque, passed through" and did not say through to *what*, which is two
different implementations and an implementer asked rather than guessing. It is
for the endpoint's own use — correlating an inference with the caller's request
id, a tenant, a billing bucket, whatever the operator wants in its logs — and it
is never written into the upstream request.

The reason is the rule `headers` is already governed by: **any member that
passes caller text through to the upstream request is a credential channel,
whatever it is named.** `headers` is one and is kept, because tenancy and
routing metadata genuinely have to accompany a request — so it is policed, with
a named refusal and a validator diagnostic. `metadata` forwarded upstream would
be a second such channel with none of that machinery, opaque by construction and
therefore unpoliceable, and it would be one nobody would think to look at
because the name suggests bookkeeping.

A caller that needs a value to reach the provider uses `headers` and accepts the
policing that comes with it. That is the whole difference between the two
members, and it is why they are not interchangeable despite both being
caller-supplied maps.

One `reasoning` object replaces what Makai carries as seven separate options —
`thinking_enabled`, `thinking_budget_tokens`, `thinking_effort`,
`reasoning_effort`, `reasoning_summary`, `include_reasoning_encrypted`,
`reasoning_enabled`. Their own reading is that the `thinking_*`/`reasoning_*`
split is vendor vocabulary leaking into an options struct rather than two
concepts, and this draft takes it as one.

The carry has no equivalent in the flattened set and exists because dropping it
would be silent: some vendors return reasoning in an opaque encrypted form that
must be handed back verbatim on the next call or the chain breaks. Google's
thought signature is the same shape from a different vendor. It is an opaque
value and is never inspected.

**It needs a return path, and an earlier version of this draft gave it none.** A
caller could send a carry and had no way to obtain one: no response
envelope carried it. A caller driving a multi-turn tool-calling conversation got
a signature-less tool call on turn one, had nothing to send on turn two, and the
chain broke — the exact failure the member exists to prevent. Half a mechanism
is worse than none, because it reads as covered.

So `inference.part.ended` carries an optional opaque `carry` for the `tool_call`
and `reasoning` kinds, and **the same member on the same kinds of content part
in `messages[]` is how it goes back**: verbatim, never inspected, never
interpreted, in both directions.

**A request-level `reasoning.encrypted_carry` was the wrong shape and is
removed.** It was one value per request against one carry per part, and an
implementer building the inbound half asked the question that exposes it: with
several reasoning parts in a conversation, which one does a single member belong
to? Every answer is a placement rule the profile would have had to invent —
attach it to the most recent reasoning block, or the last one replayed, or the
first — and a vendor whose signature belongs to a *specific* block would break
under any of them the moment two blocks are in play.

Symmetry removes the question instead of answering it. A carry arrives on a
part and goes back on the part it came from, so placement is not a rule anyone
has to state and a conversation with six reasoning blocks carries six
signatures, each where it belongs. The profile is proposed, so removing the
request member costs an implementation that has not built the inbound half
nothing — which is exactly the implementation that found this.

This is the shape the outbound half already had. The asymmetry was invisible
while only the outbound half existed, which is the general form: a return path
added to a one-directional member is not finished until something sends one
back.

**A snapshot carries it wherever it can express the part.** A completed tool
call inside a snapshot is expressible two ways — the shared content part or the
snapshot's own form, which exists for `arguments_partial` — and only one of them
admitted a `carry`, so whether the signature survived depended on which encoding
an implementation reached for. Both admit it now. A partial call has no
signature yet and the member is simply absent there, which is what absent
already means.

This is the asymmetry this section removed from the request, reappearing one
level down between two encodings of the same thing. An implementation found the
same shape in its own decoder the same day: a rule refusing a carry where it
cannot belong, enforced on the frame going out and not on the one coming back.
A member that exists in one direction of a round trip and not the other is the
recurring form of this defect, and looking for the other direction is the whole
of the check.

**The terminal assembly carries it too**, and the validator checks that: a
terminal content part must repeat the carry its part ended with. Without that
rule an implementation could stream signed parts and settle with an unsigned
assembly, and a caller that replays the terminal message — which is the obvious
thing to replay, being the complete one — would hand back a history with every
signature stripped. That is the broken chain the member exists to prevent,
arriving through the one envelope a caller trusts most.

**On the part, not on the terminal.** A signature belongs to a specific part,
and a message with three reasoning parts needs three of them. In the
implementation this was found in, the value rides the tool call itself and the
thinking part, which is the same placement.

**An absent `carry` does not say why it is absent, and the answer is a
descriptor fact rather than a member on the part.** A part that ends without one
may come from a provider that never signs, or from one that signed and whose
signature was lost in translation — a live run found exactly the second case,
and the two frames are identical.

No member on `part.ended` distinguishes them, for two reasons. A caller's
*action* is the same either way — do not replay this block, or replay it without
the value — so a member that changes what the caller knows and not what it does
is decoration. And the implementation where this was found could not populate
such a member anyway: its lookup returned nothing, so the endpoint never learned
that it had lost a signature. A field only an implementation that knew could
fill, and which by the rule above should have refused instead of losing it, was
fillable by nobody.

That second reason has since expired — `lsm/makai#347` fixed the lookup, so
that endpoint no longer loses a signature it holds. The first stands alone now,
and it was always the stronger of the two: the caller does the same thing
either way.

The ambiguity is a *discovery* question, and the profile already puts those on
the descriptor. `round_trips_carry` says whether a carry this implementation
emits can be handed back and reach the provider, and two existing rules then do
the work. Clause 13 binds it in both directions: an endpoint that round-trips
must say so, and one that says so must do it. And the create-time rule covers
the caller, because sending a carry to an endpoint whose descriptor
says `false` is decidable from the descriptor and the request alone — so it is
refused at create, rather than discovered a turn later when the vendor rejects a
replayed block.

The member was added while nothing implemented the round trip, which was the
cheapest moment: no implementation had to change behaviour to comply, and the
one carrying the gap was obliged to advertise `false` rather than leave it
silent — the under-claim shape clause 13 names, sitting in the implementation
clause 13 was written against.

What happened next is worth recording, because it is not what that paragraph
predicted. `lsm/makai#347` closed the gap rather than advertising it. The carry
round-trips, and the honest `false` moved down a level: to the built-in
providers that genuinely do not round-trip, which refuse a carry at create
rather than dropping it, while the one that does advertises `true`. So the fact
turned out to be per provider rather than per endpoint — which is where the
profile had already put it — and the create-time rule is enforced rather than
only specified.

This is one vendor's mechanism seen in one implementation, which is thin by this
draft's own standard. It is in because the failure it prevents is silent and the
member is inert for every provider that does not use it — an opaque value nobody
sends costs nothing, and its absence costs a broken chain that looks like a
model error.

### Per provider — `ProviderDescriptor`

- `id`, `display_name?`
- `wire`, `framing`
- `endpoint` — the destination reached.
- `wire_id` — when `wire` is `other`, an opaque label distinguishing this shape
  from another unnamed shape at the same provider. Never branched on.
- `headers` — what the implementation sends from its own configuration, so a
  caller can see what accompanies its prompts. Never resolved from a credential
  store.
- `compatibility` — the twelve facts below.
- `snapshot_policies` — which of `never`, `on_part_end`, `every_delta` the
  request may ask for, and whether `inference.sync` is answered.
- `credential_grant` — `none`, `out_of_band`, `on_envelope`. Whether this
  implementation accepts a caller-held credential for this provider, and by
  which tier.
- `grant_kinds` — `static`, `refreshable`, or both. Which kinds of granted
  credential it can hold without writing them down.
- `allows_anonymous` — this provider needs no credential.
- `round_trips_carry` — whether a `carry` this implementation emits on a
  `tool_call` or `reasoning` part can be handed back on the matching content
  part of the next call and reach the provider. Absent means no.

`round_trips_carry` absent means no, and that is safe here for a reason worth
stating rather than assuming: a caller that reads absent, a caller that reads
`false`, and a caller holding a descriptor older than the field all take the
same action — do not rely on a carry. **Additive absence is safe exactly when
unknown and no imply the same caller action.** Where they diverge — where not
knowing should make a caller probe, degrade loudly, or decline to proceed rather
than quietly assume no — a new member needs a third state or a different shape.
No member in this profile has that property today, and the question belongs to
each addition rather than being settled once.
- `context_window?`, `max_output_tokens?`

`allows_anonymous` is not a nicety. A local Ollama needs no credential, and an
implementation that treats absence-of-credential as an error state refuses a
correctly configured provider.

## Compatibility Facts

**Provisional for 0.1.0.** Every other part of this profile is about frame
shape, sequencing and ownership — things an implementation can be driven to
demonstrate and a suite can check. These twelve are the only part that asserts
something about the *world*: what a named vendor's endpoint actually does. None
has been checked against the vendor it describes, and the one that met a real
options struct needed an undecidable case added on first contact. Treat the
table as a named annex that may change in a way the rest of the profile may not,
and do not read a tagged release shipping alongside it as settling it.

The closed set an implementation states about a provider that claims a wire. It
is Makai's `OpenAICompatOptions` — twelve fields, each one a vendor that broke a
shape while claiming it — of which eleven are carried across whole and one is
re-derived into an adjacent fact, marked below.

An earlier version of this draft promoted six of the twelve and sent the rest to
`extensions`. That split does not survive its own test. The criterion for
belonging in the protocol is that a caller must branch on the fact and cannot
discover it from the endpoint, and all twelve meet it: all twelve appear in live
branch conditions in that tree's OpenAI and Anthropic request builders, and
`parseCapabilities` accepts all twelve from a user-written
`~/.makai/providers.json`. A fact a human has to declare by hand is the
definition of undiscoverable.

| Fact | Values | Makai's name | Why a caller must know |
| --- | --- | --- | --- |
| `max_tokens_field` | `max_tokens`, `max_completion_tokens` | same | One semantic field, two names. |
| `thinking_format` | `openai`, `zai`, `qwen` | same | Three mutually incompatible reasoning encodings behind one API name. A wrong guess silently drops reasoning. |
| `usage_in_streaming` | `always`, `terminal_only`, `never` | *(re-derived — see below)* | Whether usage arrives at all changes what `inference.completed` can promise. |
| `requires_assistant_after_tool_result` | boolean | same | A message-ordering constraint, not a capability. |
| `requires_tool_result_name` | boolean | same | Same class. |
| `requires_thinking_as_text` | boolean | same | Reasoning must be sent back as ordinary text or the request is rejected. |
| `supports_strict_mode` | boolean | same | Whether `output_schema` is enforced or advisory. |
| `supports_store` | boolean | same | Server-side retention of the request. |
| `supports_developer_role` | boolean | same | Whether the `developer` role exists or must be folded into `system`. |
| `supports_reasoning_effort` | boolean | same | Whether the effort control is accepted. |
| `tool_call_id_format` | `unconstrained`, `constrained` | `requires_mistral_tool_ids` | Some endpoints reject tool-call ids that are not in their own format. |
| `cache_ttl_control` | boolean | `supports_anthropic_cache_ttl` | Whether an explicit cache retention is accepted. |

#### Eleven are transcribed; one is re-derived, and that is a weaker claim

`usage_in_streaming` is not the fact the "Makai's name" column implied. Theirs —
`supports_usage_in_streaming` — is a **request-shape** fact: whether an endpoint
accepts the `include_usage` stream option, used in exactly one place, to decide
whether to write that option into the request. This profile's is a
**response-behaviour** fact: when usage arrives. Adjacent, not identical.

The mapping is lossy in the direction nobody expects. It is not that three
values do not fit in a boolean. It is that the boolean does not answer the
question: `true` maps soundly to `always`, and `false` means "do not send the
option," which cannot distinguish `never` from `terminal_only` — an endpoint
that rejects `include_usage` may still report usage in its final chunk. An
implementation holding that boolean must leave the fact unstated in the `false`
case rather than guess, and under the absence rule a reader then assumes the
wire's default.

The response-behaviour fact is the right one to carry, because a caller never
builds a vendor request and has no use for whether an option is accepted — what
it needs to know is what `inference.completed` can promise. But the widening has
a cost worth generalizing:

> **A fact the profile widens has weaker attestation than one it copies, because
> the added values are unattested by construction.**

Two of `usage_in_streaming`'s three values have never been observed by anything.
The other eleven facts are transcriptions of distinctions a real implementation
already draws, and their attestation is exactly as strong as that implementation.
A table cannot show the difference — a re-derived fact and a transcribed fact
look identical in a row — so it is stated here.

#### Two renames

Two are renamed because the fact is general and the vendor is incidental. A
protocol that names Mistral and Anthropic in its member names binds the
vocabulary to two companies, and the next endpoint with a constrained id format
has nowhere to say so. The renames are this draft's proposal and are the part of
this table most likely to be wrong — the semantics are theirs, the names are
mine.

Every fact is optional and its absence means "the wire's own default," never
"unknown." An implementation that states none is exactly as conformant as one
that states all twelve — it has simply promised less.

**There is no free-form quirks map.** The set grows by protocol change. A
free-form map ships the problem to every caller at once: each one writes its own
branch on a key nobody agreed on, and the divergence the profile exists to name
becomes invisible again.

## Credentials

**No credential value crosses this wire.** Not a key, not a token, not a header,
not an environment value. A `ProviderDescriptor` carries no credential member.

This is the constraint
[Decision 0017](../decisions/0017-provider-provisioning.md) sets for provider
provisioning at the agent-control boundary, and it holds here for the same
reason one layer down.

### Selecting a credential

An earlier version of this draft claimed the rule costs a real implementation
nothing, because a provider is constructed without a credential and resolves one
per request from its own store. The first half is right and the second half was
wrong: Makai's per-call options carry `api_key` as well, and it is not
vestigial — Vertex documents a per-call key as one of two accepted sources.

So `inference.create.request` carries an optional **`credential_ref`**: a name
the implementation resolves from its own configured store. The caller says
*which* credential, never *what* it is. `allows_anonymous` on the descriptor
says a provider needs none at all — a local Ollama is correctly configured with
no credential, and an implementation that treats absence as an error refuses it.

### Caller-held credentials: the grant

`credential_ref` alone selects among credentials the implementation already
holds. A caller holding a key the implementation has never seen — bring your own
key, a per-tenant key, Vertex's documented per-call key — cannot introduce one.

An implementation that keeps a native path beside this profile pays nothing for
that gap: it expresses the case natively and lets OAP be the lossy outer wire.
An implementation whose *only* inference wire is this profile cannot do what the
profile cannot express, and dropping a documented provider path is a real loss.

**The rule was always about the channel, not about who owns the key.** Values are
kept off envelopes because envelopes are logged, assembled into traces,
validated, journalled, replayed from a cursor, and persisted by intermediaries.
That says nothing about whether the caller or the operator holds the key.

So the profile admits a caller-held credential, in two tiers, and **the strong
tier is mandatory wherever it is achievable.**

| Type | Direction | Carries |
| --- | --- | --- |
| `provider.credential.grant.request` | caller → implementation | `provider_id`, `nonce`, `ttl_ms?` (the credential's lifetime, not the arrival deadline), and the value *only* in the fallback tier |
| `provider.credential.grant.response` | implementation → caller | `accepted`, then `credential_ref` and `expires_at_ms?`, or a typed `error` |

#### Which tier, and whether at all: `credential_grant`

`ProviderDescriptor.credential_grant` says `none`, `out_of_band` or
`on_envelope`.

An earlier version of this draft required a caller to learn that grants were
unsupported *before* sending a secret, and gave it nothing to read. The only way
to find out was to send the grant request — which, under tier 2, is the secret.
The rule was unsatisfiable by the envelope set carrying it, which is the same
failure as the persistence rule above: right rule, nothing on the wire making it
achievable, and no envelope violated by an implementation that gets it wrong.

It also answers a question a tier-1 caller could not otherwise ask. A caller
that does not know a side channel exists has no way to use it, and would put a
value on the envelope — the exact thing tier 1 exists to prevent.
`out_of_band` tells it to use the channel; `on_envelope` tells it the binding
has none.

#### Tier 1: out of band, and required where the binding allows it

The grant envelope carries a **nonce and nothing secret**. It says a credential
is arriving for this nonce, not here is a credential. The value crosses on a
channel the binding defines, keyed by that nonce; on stdio that is
[a per-grant listening socket](provider-stdio.md). The response returns `credential_ref` and
`inference.create.request` is unchanged.

The reason to prefer this is structural rather than aesthetic. A journal, a
trace assembler, a replay cursor and a proxy need to know nothing about the
grant, because nothing they can see carries a secret. The obligation moves from
*every intermediary that handles the stream* to *each binding specification* — a
small number of documents, written once and reviewed — and the validator rule
becomes unconditional: **any envelope carrying a credential value is invalid**,
with no permitted-but-special case and no fixture for an exception.

**A binding that can carry the value out of band must.** This is a requirement
on bindings, not a preference.

**One mechanism, not a mechanism plus an optimization.** A binding names one
tier-1 channel. The alternative considered and rejected: a spawn binding could
pass the value in an environment variable the nonce names, which needs no
filesystem object and no new platform abstraction. It fails twice. A grant
carries `ttl_ms` and expires *during* a connection, and a caller may hold
credentials for several providers, so repeat mid-connection grants are the
design rather than an edge case and a spawn-fixed channel cannot be the only
form. And offering it *beside* another form moves the cost from one
implementation to every caller, permanently: if an implementation may offer
either, a portable caller implements both and can rely on neither without asking
first. That is the wrong direction for a profile whose premise is that a caller
speaks one language.

**A binding must not assume numbered descriptors exist.** The obvious stdio form
— open descriptor 3 — has no meaning on Windows, where an extra stdio slot is an
inherited handle rather than a numbered descriptor. An implementation shipping
both platforms from one binary would find the grant becoming a
platform-conditional feature, which is not what "mandatory where the binding
allows it" is supposed to mean. If a binding ends up POSIX-only for tier 1, it
must say so, because the consequence is that a whole platform gets the weaker
tier and the mandate quietly becomes optional in practice for anyone shipping
cross-platform.

#### The arrival deadline is not `ttl_ms`

They are different clocks, and an earlier version of this draft had only one.

`ttl_ms` becomes `expires_at_ms`: it is the **credential's** lifetime, and it
starts when the grant succeeds. The deadline for the value to *arrive* runs from
when the request is received, and it is the one that matters when a channel goes
quiet. A caller asking for `ttl_ms: 3600000` because it wants a one-hour
credential is not asking the implementation to wait an hour for the bytes.

**The binding fixes the arrival deadline; a caller does not choose it.** A
caller-chosen arrival deadline is a caller-chosen duration for an implementation
to hold a half-open grant, which is a resource-exhaustion lever with no
legitimate use.

#### The side channel must never block the envelope stream

An implementation whose reader is single-threaded and cooperative — which is an
ordinary way to build one — freezes everything on a blocking read of a silent
channel, **including its ability to answer a cancel**, which is exactly what a
caller reaches for when a grant hangs. Whatever a binding specifies, this
survives.

#### Nonce lifecycle

Four rules. They are grant semantics rather than channel mechanics, so they
belong here and not in a binding.

1. **On the arrival deadline, refuse and keep serving.** The grant request is
   answered with a typed refusal — not a hang, not a silent drop. A caller
   cannot otherwise distinguish a lost credential from a slow implementation.
2. **Burn the nonce.** After the deadline the nonce is dead, and a value
   arriving late for it is discarded rather than bound. This is a security
   property rather than a robustness one, and it is the rule an implementation
   gets wrong by doing the natural thing: leaving the nonce in the map, because
   removing it looks like cleanup rather than correctness. Without it, a secret
   written a second too late is attached to whatever grant claims that nonce
   next.
3. **A value for a nonce that was never issued is discarded silently.** No error
   envelope — an error there answers a question the sender should not get
   answered.
4. **Closing the channel without writing is the deadline arriving early**, and
   draws the same refusal immediately. That is a well-behaved caller saying it
   changed its mind, and making it wait out a timeout punishes the only party
   doing it right.

#### Tier 2: on the envelope, where no side channel exists

HTTP has no clean side channel. A second request is still a request, and its
body is logged by the same things that log everything else. So the fallback
exists, and under it the value rides `provider.credential.grant.request` subject
to four rules:

1. **One type, one place.** No other payload member anywhere carries a
   credential value, including the header maps described below.
2. **Non-journalable.** Never written to a journal, assembled into a trace,
   replayed from a cursor, or persisted.
3. **The validator enforces it.** A trace containing a grant request that
   carries a `value` is invalid — `credential_in_trace` — because a trace
   carrying one was assembled from a stream that recorded a secret. A tier-1
   exchange is not caught by this and must not be: it carries a nonce, a channel
   and a reference, and tracing it is fine.
4. **Gated and refusable.** A caller learns the capability is unavailable before
   it sends a secret, not after — by reading `credential_grant` on the
   descriptor, which is what makes that sentence achievable rather than
   aspirational.

This tier is the floor and is known to be weaker: an exception that every
intermediary must honour is honoured almost everywhere, and the ones that get it
wrong are invisible. It exists so that an HTTP binding is possible at all, not
because it is good.

#### Both tiers: a granted credential must be unable to reach durable storage

A grant is connection-scoped, expires at `expires_at_ms` if one is set, and does
not survive a reconnect. A credential that survives a restart is one the
operator never configured and cannot revoke.

**`expires_at_ms` binds the implementation that issued it, and expiry destroys
the material.** An implementation that publishes an expiry MUST refuse a
`credential_ref` used past it — `credential_expired`, not
`credential_missing` — and MUST discard the credential value at that moment
rather than merely stop honouring the reference.

Both halves are load-bearing and the second is the one an implementation
forgets. **The argument for it is the published lifetime, not a hypothetical
reader.** An implementation that refuses the reference while the plaintext sits
in a table keyed by it has held a secret past the lifetime it announced — which
is a fact about its own published claim, true whether or not anything ever reads
that value again. It does not rest on someone later wiring up a new reader,
which invites the answer that nobody did. An implementation was found in exactly
this state, refusal added and material retained until connection teardown, and
reported it rather than treating the refusal as sufficient.

The code is pinned because the taxonomy is worth nothing if the same situation
produces different codes in different implementations. An endpoint can tell
expired from absent by consulting its own grant table, with no upstream attempt
and no ambiguity, so the caller gets the distinction it can act on: **refresh**
for an expiry it can renew without a human, **authenticate** for a reference
that was never granted.

Until this was written the member stated a fact nothing checked. An
implementation could compute an expiry, return it, and consult it never — which
one did, matching on reference alone, so a caller's own connection was the real
lifetime. That is the `round_trips_carry` shape again: a published claim with no
obligation attached to it.

**That property does not follow from the profile alone, and it asks for more
than a flag.** An earlier version of this draft said a granted credential is
"marked non-persistable at the point it enters the implementation" and that
"every refresh, cache and storage path honours the mark." That underestimates
what it demands of an implementation built the ordinary way, and the first
implementation to attempt it says so.

The demand is structural, in three parts.

**1. A representation with no path to storage, not a flag consulted at each
write.** The wording is load-bearing, confirmed by an implementation building
it: a rule saying "mark it non-persistable" produces a boolean on the existing
type and a set of call sites to remember, and a rule asking for a representation
produces a second store no writer can reach. The first is a rule; the second is
a structure. A mark presumes a field on something that already exists and a set of
paths that can be taught to check it. What is actually required is that a
granted credential be held in a form from which no write is reachable. A flag is
remembered; a representation is checked by the compiler. The property this rule
protects is worth the stronger form, because a single missed call site puts a
caller's key on disk and nothing observable says so.

Assume this mode does not exist in an implementation you are adding the profile
to. A credential store's whole purpose is durability, and "hold this, refresh
it, and never write it down" is a mode such a thing has no reason to have until
a profile asks for it.

**2. A per-call channel that bypasses the store satisfies the rule for what it
can carry.** Where an implementation already passes a per-call credential
straight to the provider without touching its store, that path is safe by
construction rather than by discipline, and the rule is met for the kinds of
credential it can carry. Makai's is one: a caller-supplied key short-circuits
its refresh-and-persist path in the first three lines, so a granted static API
key cannot reach storage there even in principle.

An implementation in that position says which kinds its bypass carries, so a
caller can tell.

**A predicate written to simplify routing can disable a mechanism that exists
for a reason, and that is a distinct hazard from missing a write.** In the
implementation this rule was built against it happened three times, the last
being the worst: a predicate written to keep routing simple reported
not-expired for every ephemeral credential, so the refresh lock was never
reached and a granted OAuth credential refreshed unlocked. Latent until
something grants OAuth credentials, and then concurrent requests race on a
rotating refresh token with nothing coalescing them.

The general form is that adding a credential kind adds a *shape* the existing
predicates were not written to classify, and the cheapest way to make them
compile is to answer the question they were not asking. Each of the three was a
convenience that silently removed a guarantee.

**Find every predicate that routes on credential kind, not only every path that
writes.** This is the part that catches an implementation out, reported from
doing it. Having built the unreachable representation, its routing predicates —
the ones deciding whether a request takes the refresh path or the static-key
path — still read the durable store, so a granted refreshable credential was
invisible to them, took the wrong branch, and failed as an unknown provider:
accepted, held correctly, unusable. Writers and routers are different sets, and
the second is the one nobody goes looking for.

**The test is about the payload, not the mechanism.** Assert that a granted
secret never appears in the bytes a writer would emit, with a configured
credential beside it that still does. That survives a refactor which reorganizes
the storage entirely; a test that checks a flag is consulted does not, and a
flag-checking test is what a rule saying "mark it" would have produced. Prove it
by adding a write into the path that must not write, and watching the assertion
fail.

**3. A refreshable grant must be refused if refreshing means persisting.** This
is the specific hazard and it is near-universal: refresh and persistence are
usually the same code path, because the reason to refresh is to keep a durable
credential usable.

An implementation whose per-call channel carries only a static key has *no path*
for a refresh token except its credential store — the one structure whose job is
to write things down. Accepting a refreshable grant there and writing it down is
a silent violation. Refusing it is conformant, immediately, with no
re-architecture.

So `ProviderDescriptor.credential_grant` is accompanied by **`grant_kinds`** —
`static`, `refreshable`, or both — and a grant of an unlisted kind is refused.
That is the same lesson as `credential_grant` itself: a rule that obliges a
caller to know something must give it somewhere to read it, or the caller finds
out by sending a secret.

#### A correction about how this was found

The first version of this rule cited a specific mechanism — that an
implementation's refresh path would persist a granted credential during ordinary
requests. That mechanism was reported from a call graph rather than a function
body, and it is wrong: the path short-circuits before reaching storage.

The hazard is real and sits one step over, which is the more interesting place:
not that a granted credential takes a persisting path, but that for a refreshable
credential there is no other path to take. The rule is stronger for the
correction, and the correction is recorded because a reader who checks the
original claim would find it false and reasonably distrust the rule built on it.

#### Scope difference, on the record

Makai's per-call `api_key` is per call; a grant is per connection. A caller
grants once and references thereafter, and a per-tenant caller grants per
connection. Nothing appears to be lost, and the difference is recorded here
rather than discovered later.

### `headers` is a credential channel and also a legitimate one

`Authorization: Bearer` is a header, so a header map is a way to defeat every
rule above while each explicitly credential-named field stays absent. An earlier
version of this draft concluded that the map should not exist.

That was wrong, and the argument against it is a configuration format that
already separates the two things the removal assumed were one. Makai's custom
provider file carries `auth` and `headers` as distinct members — `auth` is
`{"env": "<VAR>"}`, or `"none"` for an anonymous endpoint, or absent so the
credential resolves from the platform store by provider id — and the example its
own test fixture reaches for is `X-Tenant`. That is the real case: a corporate
gateway that needs tenancy or routing metadata alongside a credential it
resolves separately. `endpoint` plus the compatibility facts does not express
it, because the fact being expressed is not about the wire format. It is which
tenant, which route, which deployment slot.

**Removal does not close the channel; it moves those deployments off the
profile.** A deployment that cannot be expressed does not stop existing. And
nothing in that configuration format *enforces* the separation either — a user
can write an `Authorization` header into it and it will work — so the property
was always a convention rather than a structure, on both sides.

So the profile keeps headers, in two places that are not the same kind of thing,
and replaces removal with enforcement.

#### On the descriptor: published, not supplied

`ProviderDescriptor.headers` is what the implementation sends to that endpoint
from its own configuration. It is **not caller text** — a descriptor is
published by the implementation, and a caller reads it. Publishing it lets a
caller see what accompanies its prompts, which is the same argument `endpoint`
makes.

An implementation must not publish a header whose value came from its credential
store. That is a rule about what it publishes, not about what a caller may send.

#### On the call: supplied, and policed

`inference.create.request.headers` is caller text, and is where tenancy that
varies per call belongs. Two rules:

1. **A credential in `headers` is non-conformant.** A caller that needs a
   credential uses the grant. An implementation that finds one in a header is
   looking at a configuration error, not an alternative path.
2. **The validator catches what it can.** A header named `Authorization`,
   `Proxy-Authorization`, `X-Api-Key` or `Api-Key`, matched without regard to
   case, or any value that begins with `Bearer ` — also without regard to case —
   after leading spaces and tabs are trimmed, is rejected as
   `credential_in_headers`. The same predicate
   applies to a descriptor's published `headers`, because a descriptor
   publishing `Authorization` is a credential in a trace by a different route.

That check is **incomplete by construction** and cannot be otherwise: a
credential can be called anything. The value-shaped half is a floor rather than
a detector, and deliberately so: it catches `X-Custom: Bearer sk-abc` and does
not catch `X-Custom: sk-abc`, a bare key in a header nobody named. Anything more
aggressive begins rejecting the opaque values a gateway legitimately carries —
tenant ids, request signatures — and a validator that rejects valid frames is
worse than one with a stated gap. So the rule is written as catching
bearer-shaped values specifically, not as catching credentials. The overclaim
would be easy to miss here precisely because this rule *is* mechanically
checkable, and it would hide in the word "credential". It is worth having for the same reason rule 4
is — it turns the common mistake into a caught one — and it must not be
described as closing the channel. What closes the channel is that the grant
exists, so a caller with a credential has somewhere correct to put it.

**The general principle, restated correctly.** Any member that passes caller
text through to the upstream request is a credential channel. The response is
not to delete every such member, because some of them carry things a deployment
genuinely needs. It is to make sure a correct path exists, make the incorrect
path non-conformant, and catch the cases a validator can see.

## Shared Vocabulary

An earlier version of this draft said `ContentPart`, `ToolDefinition`, `Usage`
and `ProtocolError` "mean the same thing on both boundaries and are reused." Two
of those four are wrong, found by writing the types rather than by reading the
sentence again. Sharing has three degrees and the draft now names which applies.

**Reused whole.** `ContentPart`, `Message`, `Usage`, and the tool-call identity
domain. An agent loop sitting between the boundaries must not translate a
content part into a different content part. These reuse verbatim, confirmed in
an implementation.

**Shape shared, code set profile-scoped: `ProtocolError`.** The structure — code,
message, details — is common. The code sets overlap without either being a
subset of the other. `unsupported_feature` and `model_not_found` are defined by
agent control and mandated here, and mean the same thing on both. Around that
overlap each boundary carries codes the other has no referent for: agent control
has `session_not_found`, `run_not_found`,
`run_already_terminal`, `session_busy` and `stale_capabilities`, none of
which has a referent below the loop; this profile has `credential_missing`,
`credential_rejected`, `credential_expired`, `provider_unavailable`,
`resource_exhausted`, `endpoint_error` and `aborted`, none of which belongs
above it. One type carrying both would be the union of
everything, which is what an error code exists to avoid. So an implementation
reuses the shape and defines its own enum, and a reader implementing both should
expect exactly that.

**A subset, and the subset is this profile's: `ToolDefinition`.** Agent control's
carries `execution_owner`, `source`, `features` and `annotations` beside `name`,
`description` and `input_schema`. Below the loop there is no participant to own
execution, no tool source to attribute to, and no capability negotiation — a
provider is handed tool definitions to put in a request and never dispatches
one. So this profile carries `{ name, description?, input_schema }`, which is
the intersection and not a reuse.

That intersection is the part the two must keep agreeing on. Nothing enforces
it today, and an agent-control implementation is free to carry tools as an
opaque array and have no such type at all — one does. If either profile changes
the three shared members, the other has to move with it, and this sentence is
the only thing currently saying so.

Nothing that mentions a session, a run or an interaction crosses down. Where the
two profiles would otherwise diverge, this one yields: the boundary is younger
and has no implementers to protect.

### Stop reasons

A closed set, taken from Makai's and shaped by what the vendors actually report:

`stop`, `length`, `tool_use`, `content_filter`, `error`, `aborted`

Agent control's `run.completed.stop_reason` is a free string, and an agent loop
bridging the two may pass these through unchanged. That is a convenience, not a
requirement — a loop's own stop reason is its own business.

## Errors

Every provider error maps to `ProtocolError`. The vendor's own status code,
error type and message are preserved under `extensions`, never parsed into
control flow by the caller.

**The axis is what the recipient does next, not whether the request is
retriable.** An earlier version of this draft defined six classes from what a
caller needs in order to retry. Retriability is one question a caller asks and
not the only one, and sorting Makai's sixteen codes by it collapses distinctions
that need different responses. Retriability is derivable from the class below
rather than primary.

| Action | Codes | Why it is its own class |
| --- | --- | --- |
| **Retry** | `rate_limited`, `provider_unavailable`, `resource_exhausted`, `endpoint_error` | Transient. Back off and send it again. |
| **Refresh** | `credential_expired` | A credential aged out. A refresh may fix it with no human involved. |
| **Authenticate** | `credential_missing`, `credential_rejected` | No usable credential. A human must log in, or the key is wrong and retrying the refresh loops. |
| **Report** | `invalid_request`, `protocol_violation`, `unsupported_version`, `unsupported_feature`, `model_not_found` | The caller or the peer is broken. Fail loudly; someone reads a log. Retrying cannot help. |
| **Accept** | `aborted` | A normal outcome that happens to travel as a terminal. Not a failure. |

**The set is closed and the schema enforces it.** An earlier version of this
draft listed the codes in prose over an open `code` string, so an implementation
could emit anything and a caller branching on the table was branching on a
convention. `providerError` now constrains `code` to the enum above on every
envelope this profile carries an error on, and a code outside it is
`schema_invalid`. That is what makes the action axis a contract rather than a
recommendation: a caller can exhaust the table.

Two codes were missing from the table when it was only prose, and both were in
use. `unsupported_feature` is named four other places in this draft — it is
what an endpoint returns for a grant it does not accept — and appeared in no
class. `resource_exhausted` is new, and it exists because a real implementation
had nowhere to put it: both of its decoders mapped every JSON parse failure,
including running out of memory, to `protocol_violation`, so a host under memory
pressure told its peer that its frame was malformed. The caller is told to report
a bug when the correct action is to back off. The code is the profile's half of
that fix; whether an implementation can allocate the error frame at all, and so
whether the honest behaviour is to send it or to drop the connection, is the
binding's question and the stdio binding's exit contract is where it belongs.

**`endpoint_error` is the endpoint's own failure, and it is a separate code
inside the same action.** A code is not a member, and the test that keeps a
member out — a caller that cannot act differently does not need it — does not
keep a code out, or this class would collapse to one. Four codes share Retry
because the action is the same and the *cause* is not, and the cause is what a
log reader and an operator need.

The reason it earns its own code rather than reusing `provider_unavailable` is
that reuse is a false statement about a third party. An implementation whose
pump fails to assemble a terminal, reporting the provider as unavailable, fills
a trace with evidence against a vendor that did nothing wrong — and traces are
exactly what someone reads when deciding whether a provider is flaky. An
implementation found doing this had a local `internal_error` outside the
profile's set, which is the same fact arriving the other way: the code existed
because the taxonomy had no place for it, and nothing could catch the divergence
while `code` was an open string.

The three credential states are one retry class and three different actions,
which is the clearest case for the change: "do not blindly retry" is a single
bucket on the old axis and is useless to a caller deciding between prompting a
human, refreshing silently, and giving up.

`protocol_violation` covers what Makai splits into `invalid_sequence`,
`duplicate_sequence` and `sequence_gap`. Those name which invariant broke, which
matters to an implementer and not to a caller, so the invariant belongs in the
error's message and `extensions` rather than in the code. An implementation that
wants them as distinct codes is free to say so in `extensions`; a caller
branching on them would be branching on somebody else's bug.

**Which code a closed payload's violation takes.** A frame carrying a member
the payload does not define is `invalid_request`, not `protocol_violation`, and
the rule that decides it is: **the code names whose mistake it is.** A caller
that sent an unknown member sent a bad request, and `invalid_request` puts the
fault where a log reader will look for it. `protocol_violation` is for the
invariants an implementation breaks after a request was accepted — sequence
gaps, duplicate terminals, an event after settlement. Both are Report and the
caller does the same thing with either, which is again why the distinction is
worth having: the action is for the caller, the code is for whoever has to fix
it.

This needs saying because `schema_invalid` is a validator diagnostic and not an
error code, so an implementation refusing a closed payload has to pick a code
with nothing in the profile telling it which. Two implementations picking
differently for the same frame would make the code useless for exactly the
person the code is for.

**Stream-lifecycle errors are deliberately absent.** Makai carries
`stream_not_found` and `stream_already_exists`, which are state errors on a
multiplexing layer. Multiplexing is a binding concern here, so those are the
binding's to report.

## Version negotiation

The envelope carries `version`, and an implementation states which versions it
speaks in `provider.describe.response` as `protocol_versions[]`.

**`provider.describe.request` must be answered at any version the
implementation supports**, so discovery is never the thing that fails on a
version mismatch. A caller describes first and speaks the highest version both
sides carry.

Makai negotiates the other way, through a rejection: there is no hello, a client
sends at its preferred version, and a server that cannot speak it refuses with
`version_mismatch` and a `supported_versions` list. Their own assessment is that
this is adequate rather than good — it costs a round trip on every mismatch,
gives a client no way to discover capabilities without attempting something, and
populates `supported_versions` on one error code out of sixteen, which makes a
special case wear a general field's clothing. This draft takes the requirement
and not the mechanism.

An envelope at an unsupported version is refused with `unsupported_version`,
carrying `protocol_versions[]`, so the rejection path still works for a caller
that skipped discovery.

#### `profile_revision`, and why the tag creates the need for it

`version` is the base protocol and `profile` is which vocabulary is in play.
Neither says **which revision of a profile** an implementation was built
against, and while every profile moves together that costs nothing.

Tagging `v0.1.0` ends that. It puts
[the stability commitment](../STABILITY.md) sections 4 and 5 in force for
`agent-control-core` and leaves this profile proposed and free to change shape —
and both still travel as `version: "0.1"`. A client built against this draft as
it stands today connects to an implementation built against it plus two changes,
both say `0.1`, and nothing on the wire distinguishes them.

`capability_revision` does not cover it. That is the implementation's own
descriptor snapshot, and two implementations at different profile revisions can
emit the same one — it answers "has this endpoint's configuration changed,"
never "which spec is this."

So `provider.describe.response` carries **`profile_revision`**: an opaque string
naming the revision of `model-provider-core` the implementation was built
against. Only an unfrozen profile needs to populate it; a frozen profile is
pinned by its version and has nothing to add.

**It must name a state, not a stream.** An implementation that tracks this
draft's main branch publishes the commit of the draft it was built against, not
`"main"` — a branch name is the same string for every revision it ever held, so
a field carrying one answers nothing and costs a round trip to discover that. A
tag, a commit, or a dated revision all identify a state and are all acceptable;
the opacity is about the format, not about whether it distinguishes anything. An
implementation with nothing that identifies a state omits the member, which at
least says so, rather than publishing a name that looks like an answer.

**A stale value is the correct output, not a defect to avoid.** Once this draft
moves and an implementation has not re-implemented against it, the commit it
publishes is older than the draft — and that is precisely the fact a caller
needs, because the member answers what the implementation was built against and
never what exists. An implementation that keeps the value fresh by pointing it
at a stream, or omits it to avoid looking behind, has removed the only signal
that would have told a caller the two are out of step.

This is cheap now and expensive later, because whatever expresses it is itself a
schema change — which is the argument for settling it before schemas exist
rather than after.

## Minimum Conformance

An implementation claiming `open-agent-protocol.model-provider-core`:

1. Answers `provider.describe.request` with at least one provider, naming its
   `wire` and `framing`.
2. Answers `provider.models.list.request`, and every `model_ref` it returns
   resolves to a provider it described.
3. Emits exactly one terminal per accepted inference, allocates no
   `inference_id` on a refusal, and — **where it streamed parts** — emits a
   terminal `message` that is the assembly of its ended parts.
4. Emits contiguous per-inference `sequence` on every scoped event, opening at
   1 and never repeating or going back.
5. Emits the started/delta/ended triple for every part it streams, with the
   kind-discriminated payloads on start and end, or declares `stream`
   unsupported and answers unary. Refuses a `tool_call` start missing its
   identity **and** a `text` or `reasoning` start carrying one.
6. Refuses a `credential_ref` past the `expires_at_ms` it published, with
   `credential_expired`, and discards the credential value at that moment.
7. Refuses an `include_snapshot` it does not support unless the request allows
   degradation, honours what it accepted, reports it in `honoured`, and answers
   `inference.sync.request` if it declared it.
8. Carries a credential value out of band if its binding allows it, and only on
   `provider.credential.grant.request` otherwise — never journalling, tracing or
   replaying that pair.
9. Holds a granted credential in a representation from which durable storage is
   unreachable, and refuses a grant whose kind it cannot hold that way —
   publishing which kinds it can in `grant_kinds`. Burns a nonce at its arrival
   deadline, discards a value arriving late or for a nonce never issued, and
   never blocks the envelope stream on the side channel.
10. Publishes `credential_grant` on every descriptor, and refuses a grant with a
   typed `unsupported_feature` where it says `none` rather than accepting and
   ignoring it.
11. Answers `provider.describe.request` at any version it supports, and lists
    `protocol_versions[]`.
12. States a compatibility fact where the provider it reaches diverges from the
   wire it claims, or states none and claims nothing.
13. Serves everything its descriptors claim, and claims everything it can
   serve.
14. Accepts an operator-set destination override for every provider it
   describes, out of band and never from the wire.
15. Can name, for every member of an inbound payload it accepts, what reads it
    on the path that acts on it.

Streaming is required only if advertised. A unary-only implementation is
conformant; a streaming implementation that skips `part.ended` is not.

### An implementation that cannot be repointed cannot be tested

Clause 14 looks like a deployment convenience and is not. There is no way to
conformance-test this profile without pointing an implementation at a controlled
endpoint, and no alternative route to one exists. A real vendor credential buys
compatibility testing, not conformance: the frames are whatever the vendor sent,
and a suite cannot ask for the edge it wants to check. An anonymous local
provider does not help either — reaching a mock still means overriding the
built-in address, because the default is the real daemon's port, not the
suite's. So the override is not a way to reach a test endpoint more
conveniently. It is the only way to reach one at all.

The failure mode is what makes this conformance rather than tooling. An
implementation that builds its destination from a built-in literal and ignores
the override does not fail: the suite runs, the frames validate, and every
request went to the vendor. A green conformance report that tested nothing is
worse than a red one.

**The schema enforces this, and does not merely fail to provide it.**
`inference.create.request` is a closed payload — the member list is fixed and
`additionalProperties` is `false` — so a destination arriving on the wire under
any name is `schema_invalid` at the validator rather than an unknown field an
implementation happens to ignore. The difference matters: ignoring is a property
of one decoder and can change without anyone noticing, while refusing is a
property of the profile that every implementation inherits. A later revision
that wanted a caller-supplied destination would have to add the member, in the
open, against this clause.

**The override is operator configuration, and must never be a member of the
create request.** Every implementation resolves the destination first and
attaches the credential to whatever came out, so a wire-level override is a
caller redirecting a credentialed provider to an address it controls and having
the host attach the real vendor key to it — a credential-exfiltration primitive
handed to precisely the party the grant machinery exists to keep secrets away
from. Environment variables or operator configuration satisfy the conformance
prerequisite completely, and they sit at the same trust boundary that chose the
provider in the first place. Scoping the clause this way costs nothing and not
scoping it gives away everything. This is the same refusal
[Decision 0014](../decisions/0014-provider-descriptors.md) makes of a
caller-supplied `endpoint`, reaching it from the other end.

**An overridden destination suspends the compatibility facts.** The facts
describe a vendor's behaviour, and a mock does not have it. A descriptor that
keeps advertising twelve facts while the destination is a local test server
makes any check of facts-against-behaviour meaningless — the suite would be
validating the mock against the vendor's claims and reporting the mock's gaps as
the implementation's. For 0.1.0 the rule is that **the facts are undefined while
a destination is overridden, and a conformance suite must not check them.** That
is honest and costs no machinery.

The better behaviour, which this draft does not require, is to distinguish the
two kinds of redirect. Makai already does: a base-URL override alone means
"different endpoint, assume nothing", while an explicit proxy assertion means
"same vendor behind a proxy, the vendor's facts still hold" — and the
distinction is load-bearing there, gating assertions about OpenAI's
`max_completion_tokens` and developer role, DeepSeek's thinking-as-text
requirement, and Anthropic's cache TTL. A conformance harness pointing at a mock
is emphatically not a transparent proxy.

The reason it stays a recommendation is stronger than "not yet decided": the
distinction is not inferable. Nothing in a URL says whether the vendor is behind
it, which is why the implementation that has this asks the operator with a
separate flag rather than detecting it. So requiring the distinction would mean
the profile specifying how an implementation is *told* which redirect it is
looking at — and the only place that can live is operator configuration, which
clause 14 has just put off the wire. The stronger form is in tension with 14,
not merely later than it.

The soft form also closes the objection it appears to leave open. A suite that
must not check facts under an override cannot be misled by facts published under
one. What survives is a non-suite caller trusting stale facts after an operator
redirected the provider — and that is inside the operator's trust boundary by
construction, because the operator set the override. The harm that outlives the
soft clause belongs to the party who caused it.

### Consume what you accept

Clause 15 is clause 13 one layer in. Thirteen says serve what you claim;
fifteen says read what you take. A receiver can decode a member, validate it, and drop
it, and every frame in that exchange is correct — the caller's intent was
parsed and discarded, and nothing on the wire distinguishes the endpoint that
honoured a member from the one that threw it away, until the turn where the
absence matters.

Three members reached that state in this draft's first implementation: a
compatibility mapping whose only callers were tests, a reasoning carry read out
of a structure no provider populates, and a `credential_ref` checked at create
and discarded, which left the host unable to tell which inference a grant
belonged to. A fourth pair is `metadata` and the four reasoning options, decoded
in full and then refused — the caller's intent parsed and dropped at two
separate places for two different reasons.

**Unlike clause 13, this one is mechanically checkable, inside an implementation
rather than from a trace.** Strip test code, then ask whether anything outside
the type and codec files reads each decoded member. That is the unreferenced-
function sweep applied one level down, and the same instrument that finds a
function nothing calls finds a field nothing reads.

It needs one split to be usable, and the split is knowable from the profile
rather than guessed: **a member on an inbound payload with no consumer is a
defect; a member on an outbound payload with no consumer is normal**, because
its consumer is at the other end of the wire. Without that distinction the check
reports every descriptor member as dead — `round_trips_carry` has no local
reader by design.

So this is not an open question, and an earlier version of this draft recorded
it as one. What was missing was not an instrument but the generalisation: the
implementation that found all three already had the function-level sweep and had
not turned it on fields.

**The sweep proves a member is not dead. It does not prove the member reaches
its destination, and the difference is a systematic blind spot rather than a
gap in one implementation.** A member with a legitimate reader in cloning,
serialization or tests passes the sweep while never reaching the provider — and
every member of a type that round-trips through its own codec has such a reader.
A fourth case was found this way and not by the probe: a replayed `reasoning`
content part was decoded, cloned, serialized and asserted on, and then dropped
by the one function that turns an OAP message into a provider request, whose
switch handled `text` and fell through everything else. "Has a consumer" was
true and useless.

Reachability from a named sink is a much larger instrument than a reference
sweep, and this draft does not pretend the smaller one covers it. Clause 15
therefore asks an implementer a question rather than pointing at a tool: name
what reads this member **on the path that carries it to the provider**. The
sweep narrows the search and does not answer it.

### Under-claiming is non-conformance, and only one direction is checkable

Over-claiming is caught by exercising the descriptor: ask for each advertised
capability and see whether it is served. A suite can drive that mechanically,
and a failure is a frame it can point at.

The other direction has no such handle. An implementation that supports
`every_delta` and lists no `snapshot_policies` refuses every request for it, and
every one of those refusals is a well-formed `unsupported_feature` frame that
validates against the schema. Nothing in the trace is wrong. The endpoint is
simply lying about itself by omission, and a well-behaved caller never asks for
what the descriptor did not list, so nothing ever discovers it.

Clause 13 is therefore stated as a conformance requirement and not as a
validator rule. No trace can carry the violation, so `validation/provider.go`
will never grow a code for it; this is deliberate, not the class of gap where
prose runs ahead of machinery. What the clause buys is a sentence a conformance
suite can cite when it reports a divergence it found by other means — reading
the implementation, or a maintainer answering "can you do X?" with yes while the
catalogue says no.

Both directions were found the same way, by building against this draft rather
than reading it. Three descriptor divergences surfaced in one session of getting
a first real completion out of an implementation: a host that ignored base-URL
overrides so a conformance run silently addressed the vendor instead of the
local endpoint under test, a catalogue advertising no `snapshot_policies` for
policies the server had implemented all along, and a descriptor whose
credential members understated what the implementation did, so every
credentialed provider refused at create. `providerDescriptor` is
`additionalProperties: false`, and `credential_grant`, `grant_kinds` and
`allows_anonymous` are the members that decide this — a descriptor cannot
carry a member the schema does not name, so a divergence of this kind is
always one of those three saying less than the implementation does.

All three validate. All three are invisible from outside. The base-URL one
matters beyond the implementation that had it: pointing a provider
at a local endpoint is how anyone conformance-tests this profile at all, so an
implementation that cannot be repointed cannot be tested, and the failure mode is
that the test appears to run.

## Open Questions

These are open, and naming them is better than a draft that reads settled.

**How is a vendor API pinned?** Every harness adapter in this repository pins an
upstream commit or tag, and its corpus is hermetic against that pin. A vendor
inference endpoint has neither. It changes without notice, under the same
version string, and a captured stream is evidence of what one endpoint did once.
[Decision 0016](../decisions/0016-model-provider-profile.md) says this has to be
answered here rather than deferred, and this draft does not answer it. Recorded
streams with a capture date and an evidence class are the obvious candidate, and
`provider.EvidenceClass` is an existing attempt at the second half.

**What drives conformance?** Serving Modes above settles the *shape* and
nothing about readiness.

A provider endpoint is now spawnable and answers discovery and real inferences,
so the harness has something to drive. That it was conformant while it still
refused every inference — a discovery-only endpoint is a real endpoint — is what
let the harness be written against discovery first.

One thing the build has settled: **it must be end-to-end, not envelope
fixtures.** The first implementation produced a malformed line — a doubled JSON
key — from a writer composition that no round-trip test could reach, because a
test that serializes and deserializes one envelope constructs the payload itself
and never exercises the composition. It appeared only when a full inference ran
and every emitted line was decoded in order.

A validator that checks envelopes one at a time cannot catch a malformed
envelope produced only by a particular arrangement of writers. Spawning a
binary, driving a scripted inference and handing the assembled trace to the
validator does, because it decodes what an implementation actually emitted
rather than what a test built. That is a stronger argument for the harness shape
than the symmetry argument it was chosen on.

Because a provider-profile implementation is independently servable, the harness
can be the same one in outline: spawn a
binary, drive a scripted inference over a line binding, hand the assembled trace
to the validator. But the existing harness works because there is an endpoint
built to be driven, and there is no counterpart here. Building one is the whole
of the work, not a consequence of the profiles being independently servable —
and an earlier version of this draft drew that conclusion too fast.

**A harness that drives a binary from outside reaches a minority of the wire,
and this is measured rather than estimated.** Of thirteen envelope types one
implementation emits, five are reachable by spawning it and sending scripted
envelopes with no credentials: the two discovery responses, the two grant
answers — which carry nonces and references, not secrets — and
`inference.create.response`. The other eight are the frames only a **live
inference** produces, which is the event stream plus two responses that answer a
request about an inference already running. They exist only inside the
implementation's own emitter and were checked against the schemas only because
its author temporarily printed every outbound frame from a test and piped it
into a validator.

So a green harness does not mean the wire is covered, and anyone reporting
harness results has to say which frames were reached. Closing the gap needs
either a provider the harness can drive or a way to make an implementation
produce a named frame on demand. The second is the cheaper one and is specified
as [the binding's specimen request](provider-stdio.md): an implementation that
can be asked to emit a specimen of each frame it supports, less the few it
declares it withholds, is testable in a way one that cannot is not.
`lsm/makai#347` implements it, behind an explicit flag, so an endpoint does not
answer the control frame unless it was started to.

Compatibility is a second, harder half, and the first implementation has drawn
the line precisely. What a harness can do today: spawn a provider endpoint,
drive discovery, drive a real inference against a **local, anonymous** provider
with no credentials anywhere, and assemble a trace.

That covers the answers a request draws. What it does not cover is an
inference's event stream — the part triple, a terminal that assembles, a
snapshot mid-flight — which is the measurement above rather than a separate gap,
and `inference.sync`, which nothing described drives.

**The grant exchange is reachable, and an earlier version of this paragraph said
it was not.** On a tier-1 binding the exchange carries a nonce, a channel and a
reference and no secret, so a harness can drive a real one without holding a
credential. What it cannot drive is the side channel the value crosses on, which
is the thing the Evidence section names as the part nothing has run. Those are
different gaps and conflating them made the harness look blinder than it is.

What it cannot do is observe a single compatibility fact. The twelve are carried
and mapped; none has been checked against the vendor it describes, because
checking means a real request to a real endpoint with a real credential. No
amount of further implementation changes that — it is a different kind of
evidence, requiring money and a live endpoint, and it expires, because the thing
it tests moves without notice.

So the profile can be conformance-tested and cannot yet be
compatibility-tested, and those two words should not be used
interchangeably about it.

**Where does credential acquisition live?** `auth_status` now has a home — the
model entry, five stable values, not revision-bound — but *acquiring* a
credential still does not. The grant covers a caller handing one over. Nothing
covers the flow that produces one: a device-code login, a browser redirect, a
refresh that needs a human. Makai hosts a full auth protocol on its own identity
domain for exactly this, above the provider layer. It is not in this profile and
it is not in agent control, where the core draft also records it as open.

**Is the grant the right shape for a caller-held credential?** The draft now
admits one, on one envelope type, non-journalable, connection-scoped, validator-
enforced and capability-gated. The reasoning is that the ban was always about
the channel rather than about who owns the key. What is unproven is whether five
rules are the right five: the non-journalable property in particular asks every
binding and every intermediary to make an exception, and an exception that must
be honoured everywhere is exactly the kind of rule that is honoured almost
everywhere. A single binding that logs the grant makes the whole construction
worthless, and the validator rule catches it only in traces this project
assembles.

**Is the compatibility set complete at twelve, and are the two renames right?** Twelve is one implementation's count, and a
second implementation is as likely to add a thirteenth as to agree. The renames
of `requires_mistral_tool_ids` and `supports_anthropic_cache_ttl` to
`tool_call_id_format` and `cache_ttl_control` generalize away a vendor name on
the belief that the underlying fact is general — plausible for both and checked
against neither. The set wants a second implementation before it freezes, which
is the standard
[Decision 0015](../decisions/0015-evidence-from-implementations-we-do-not-control.md)
applies to graduation.

**Does `thinking_format` belong as a fact or a capability?** It is written here
as a fact about the provider. It may be better as a declared support level, the
way agent control reports `native`/`emulated`/`degraded`/`unavailable`.

## Evidence And Standing

This draft is written from two sources and neither makes it executable.

**Makai's provider layer**, read from that tree on 2026-09-17, and read repeatedly against
successive versions of this draft, which it corrected in ten places: the lossy
part collapse, the missing snapshot, an unattested `unary`, a promoted-six
compatibility split that failed its own test, a credential rule whose no-cost
claim was false, a Serving Modes section that described software nobody has
written, an exception-shaped grant where an out-of-band channel does better, a
persistence path that would violate the grant's own rule, an error axis that
collapsed three credential states into one, and an `auth_status` disposition
that was wrong in the way its own earlier finding made it wrong. The facts
below: the per-call and
per-provider split, the event union and its triples, the stop reason set, the
twelve compatibility divergences, the non-SSE framing, the credential-free
construction, and `allows_anonymous`. It is not pinned in `adapter/makai/` and
no Makai maintainer has asserted it, so it is not evidence under
[Decision 0003](../decisions/0003-staged-unit-graduation.md)'s step 3. It is
design input.

**This repository's `provider` package**, which builds real requests for all
three wires and parses each one's stream. It was written as a compatibility
prober and its assumptions show — it requires `text/event-stream` — but the
disagreements it had to encode are the ones the profile must carry.

**Something speaks this profile.** As of 2026-09-17 a spawnable endpoint answers
`provider.describe` and `provider.models.list` over a line binding, on the same
binary that serves agent control, and refuses inference. Driven by hand it
returns three providers across three underlying wires — one `anthropic-messages`
over SSE, one `openai-responses` over SSE, and one `other` over `ndjson` with
`allows_anonymous` — with every `model_ref` resolving to a described provider.

That last row is the case that forced `other` into the wire set, working. And
the session as a whole is the first time the profile's premise has been observed
rather than argued: a caller reading that output knows what it can call, what
needs a credential, what framing to expect and which catalog entries are stale,
without knowing anything about Anthropic, OpenAI or Ollama.

As of the same date that endpoint runs **real inferences** — accepted,
translated from a registered provider's stream, polled rather than drained so a
mid-stream cancel is seen, settled with one terminal. Against a local provider
that is not running it answers `provider_unavailable`, a Retry-class error,
which is the honest answer rather than a contrived one.

**Twenty-two findings** from writing the vocabulary, codec, discovery, grants,
admission, the wire mapping, the inference lifecycle, the compatibility facts
and a spawnable endpoint are already in this draft. From the first pass: `ProtocolError` and
`ToolDefinition` are not shared the way the draft claimed, `reasoning_default`
had silently dropped a value, `opaque` is a reserved word in the implementation
language, the envelope scope rule for `inference.create.response` was
unspecified, and nothing said what happens when a caller asks for a snapshot
policy the provider does not offer. From the second: the grant capability had no member to be
read from, so the rule requiring a caller to learn before sending a secret was
unsatisfiable by the envelope set carrying it; a refused create had no stated
answer to whether it allocates an inference that owes a terminal; an empty
`snapshot_policies` had two readings; and enforcing the part-start asymmetry in
both directions turned out to cost four lines, which moved it from suggestion to
requirement.

From the third: the persistence rule asked for a flag where it needed a
representation, and the mechanism the rule was first justified by turned out not
to exist.

From the fourth: the closed wire set named five of eight registered APIs, and
excluded the one provider two of the draft's own members were justified by. From
the fifth: nothing said how `inference.completed.message` relates to the parts
that preceded it, and three mutually inconsistent readings were all permitted by
the text.

From the sixth: `usage_in_streaming` was
presented as a transcription of an existing fact and is a re-derivation of an
adjacent one, and a reasoning carry could be sent and never obtained.

From the seventh: `other` in a model reference is a
constant, so the component that exists to distinguish carries the same value for
every unnamed wire.

From the eighth: nothing said whether a
condition knowable before the request leaves the building is a refusal or a
terminal, and the worse of the two answers satisfied every other rule. From the
ninth: `ttl_ms` was doing duty for two different clocks — a credential's
lifetime and a channel's arrival deadline — and the nonce had no stated
lifecycle at all. From the tenth, found by an implementer reading their own code
while answering a different question: a snapshot could not represent a tool
call, because `arguments_json` is complete JSON and a tool call in flight is a
partial fragment — so the recovery mechanism was unavailable for the one part
kind where divergence is consequential. And the profile had no way to say which
revision of itself an implementation was built against, which costs nothing
until one profile freezes and the other does not. From the eleventh, which is
the schema work starting: the draft named no payload members at all, describing
them in prose, so there was nothing for a second implementation to agree with
and the first invented names because it had to. From the twelfth, found by
running an encoder against the schemas rather than reading them: every scoped
payload was required to repeat the envelope's `inference_id`, in eleven schema
definitions and three of the draft's own tables.

Of the twenty-two, twenty-one were places the draft was silent or wrong rather
than merely incomplete. **Four were prose ahead of its machinery** — a rule stated in the draft that the
schemas, the validator or the envelope set could not express: the grant gate
with nowhere to be read, the terminal-assembly rule no validator had, the
non-persistable rule no envelope could violate, and a grant refusal the prose
required and the schema forbade. That is an argument for writing prose and
schema in the same pass rather than the schema afterwards, and it is the reason
this draft was wrong in the same way four times.

Three of those — the persistence rule, the grant advertisement and
`grant_kinds` — were rules that no envelope could violate, which is the class
this project's machinery is worst at catching: the validator assembles traces
and checks envelopes, and an implementation writing a caller's key to disk
produces a perfectly valid trace.

**Four of the twenty-two corrected earlier findings from the same source rather
than the draft**, and the pattern in them matters more than the count. Each
superseded claim had been read off a call graph, a type name or a field's
presence, and each correction came from reading the body: the persistence hazard
was real one step over from where it was first placed; `auth_status` belonged
somewhere its own earlier finding had ruled out; `usage_in_streaming` was an
adjacent fact wearing the same name. All four were caught by the reporter,
against source, and all four made the rule stronger than the version built on
the original claim.

**The generalization is not that implementing found problems.** It is that
reading source at one remove — a call graph, a signature, a struct field — was
repeatedly different from reading it, on both sides, by parties with every
reason to be careful. That is recorded here because a reader who checks a
superseded claim will find it false, and because it is the argument for
[Decision 0015](../decisions/0015-evidence-from-implementations-we-do-not-control.md)
arriving from a direction that decision did not anticipate.

**An implementation speaks this profile** — `lsm/makai#341`, twelve commits, with
a deviations ledger naming every place it diverges: the three `other` providers,
`usage_in_streaming` left unstated on `false`, grants advertised as `none`,
keepalive dropped in translation, seven reasoning fields collapsed to one. Stop
reasons appear in that table with no deviation, which is the only row where
"carried across whole" is demonstrated rather than asserted.

It is **first-party** under
[Decision 0018](../decisions/0018-makai-becomes-first-party.md), so it
establishes that the profile is *implementable* and not that it is *right*.
**Nothing this project does not control speaks this profile**, and under
Decision 0015 that is what executable would require.

### What the implementation does and does not establish

It establishes implementability, and that was never the scarce thing. **Schemas
do not make a profile implementable; they make two implementations agree.**

And the implementability it establishes is narrower than it looks: that
implementation was written from this prose **with its author available**.
Twenty-two findings are twenty-two places the prose alone was insufficient, each
resolved by asking. A second implementer gets none of that. So the standing is
*implementable in conversation with the author*, and the findings are the
measurement of the gap rather than a side effect of closing it.

That is the real argument for schemas, and it says which shapes to specify
hardest: **the ones a finding had to fix**, because each is a place the prose
underdetermined and a stranger will underdetermine again.

### What the implementation is not evidence for

**No compatibility fact has been observed.** All twelve are carried and mapped
and none has been checked against the vendor it describes. This is the entry in
this section that has not moved.

Three entries stood here and have since closed. The trail is kept because each
was a different kind of gap and each took a different thing to shut, and because
a section that only ever records what is still missing teaches nothing about how
things stop being missing.

**The tier-1 out-of-band credential path is exercised.** It stood open through
two reasons in turn. The first was that the implementation had no way to hold a
caller-granted credential without writing it down; `lsm/makai#343` built one — a
second store no writer can reach, structural rather than flagged. The second was
that the stdio binding's side channel was unspecified, so there was nothing for
a grant to arrive on; [the stdio binding](provider-stdio.md) now specifies it —
a per-grant socket, the `provider.credential.grant.channel` envelope, a nonce,
an arrival deadline and a burn rule. What remained after both was that nothing
had run the path. As of `lsm/makai#341` the endpoint opens the channel,
advertises the out-of-band tier with the `static` kind, and enforces the arrival
deadline, the burn and the release on expiry; a build whose toolchain reports no
unix-socket support advertises `none` rather than a tier it cannot open, which
is the mandatory-where-achievable rule behaving rather than being asserted.

**All three part kinds are reachable.** The endpoint previously refused `tools`
and `reasoning` controls at create, so a caller could not ask for either and a
well-behaved provider never emitted a `tool_use` or a thinking block; the live
run that produced all three triples over `anthropic-messages` only did so
because the server emitted them unprompted, which a vendor would not do.
`lsm/makai#341` forwards both, so the route by which a caller reaches two thirds
of the part vocabulary now exists. The distinction that made this worth
recording is still worth keeping: the part triple was correct and exercised end
to end the whole time, and what was missing was the route to it — an absence
that reads as tested, and the shape a conformance suite is most likely to
mistake for coverage.

**The `carry` return path carries a value.** It was specified and could not be
populated: the translator read the signature out of a per-message partial whose
content is empty for every provider that emits thinking, so the lookup was
always out of range and the field never appeared, while its unit test
constructed a partial that held one. `lsm/makai#347` closes both halves. The
case the path was added for — a vendor rejecting a replayed thinking block whose
signature is missing, so multi-turn extended thinking cannot work through the
endpoint — is what it now covers.

What none of these three become is *third-party* evidence.
[Decision 0018](../decisions/0018-makai-becomes-first-party.md) makes that
implementation first-party, so each of these closures is this project checking
its own work. They answer "is the profile implementable end to end", which is
what `v0.1.0` waits on. They do not answer
[Decision 0015](../decisions/0015-evidence-from-implementations-we-do-not-control.md)'s
question, and a second implementation is still what would.

### `other` is load-bearing, and tightening its criterion fails badly

Three of that implementation's eight providers describe themselves with `other`,
including the one whose existence attested both `ndjson` framing and
`allows_anonymous`.

So if the criterion for naming a wire ever tightens, those three do not lose
*portability* — that is what `other` costs them by design and they already pay
it. They lose *describability*: the profile stops being able to represent a
provider it represents today. That is a worse failure than the one `other` was
added to prevent, and it is the specific thing to weigh before anyone touches
the criterion.

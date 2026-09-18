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

It is not a credential channel for ordinary traffic. One envelope type carries a
value, under five rules that keep it out of every trace, journal and replay. See
Credentials below.

## Identity

Three identifiers, deliberately distinct, because two of them are routinely
conflated in speech:

- **`provider_id`** — an opaque, implementation-scoped id naming a *configured
  vendor endpoint*. Not a registry this project assigns meanings in.
- **`wire`** — the *request shape* spoken to it, from a closed set.
- **`model_id`** — the model, as the provider names it.

A **model reference** carries all three: `provider_id/wire@model_id`.

`provider_id` and `wire` are different layers. Two vendor endpoints may share a
wire, and one vendor endpoint may offer more than one. An implementation that
keys its providers by wire loses the distinction the moment a second endpoint
speaks the same shape, which is
[Decision 0017](../decisions/0017-provider-provisioning.md)'s reason for giving
the descriptor both members.

### Wire

A closed set. The values are the three this repository's `provider` package
already names and builds real requests for:

- `openai-responses`
- `anthropic-messages`
- `openai-chat-completions`

Adding a value is a protocol change, not configuration. A wire an
implementation reaches but this set does not name is reported as `unavailable`
rather than approximated to a neighbour.

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

**`sse` and `ndjson` are attested; `unary` is not.** All eight of Makai's APIs
stream, and their non-streaming call is a facade that opens a stream, drains it
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

The binding is the same shape as
[the endpoint stdio binding](endpoint-stdio.md) — raw OAP envelopes, one per
line, the profile distinguishing which vocabulary is in play. That binding is
written for agent control, a provider-profile binding is not yet specified, and
nothing in it is agent-control-specific except the envelope set it carries.

## Envelope Types

Fourteen, in four groups.

### Discovery

| Type | Direction | Carries |
| --- | --- | --- |
| `provider.describe.request` | caller → implementation | nothing required |
| `provider.describe.response` | implementation → caller | `providers[]`, `capability_revision` |
| `provider.models.list.request` | caller → implementation | `provider_id?` |
| `provider.models.list.response` | implementation → caller | `models[]`, `capability_revision` |

`capability_revision` is required on both responses, for the reason agent
control requires it on `capabilities.response` and `models.response`: the whole
content is bound to one descriptor snapshot.

### One inference call

| Type | Direction | Carries |
| --- | --- | --- |
| `inference.create.request` | caller → implementation | the call (below) |
| `inference.create.response` | implementation → caller | `inference_id`, `accepted`, `honoured`, or a typed refusal |
| `inference.started` | implementation → caller | `model_ref`, `started_at_ms` |
| `inference.part.started` | implementation → caller | `part_index`, `part_kind`, and for a tool call its `tool_call_id` and `name` |
| `inference.part.delta` | implementation → caller | `part_index`, the increment |
| `inference.part.ended` | implementation → caller | `part_index` |
| `inference.completed` | implementation → caller | `message`, `stop_reason`, `usage?` |
| `inference.failed` | implementation → caller | `error`, `usage?` |

Exactly one terminal per inference: `inference.completed` or
`inference.failed`. An aborted call ends as `inference.completed` with
`stop_reason: aborted` — cancellation at this boundary is a stop reason, not a
third terminal, because the vendor reports it that way and inventing a terminal
would put the profile's arbitration above the provider's own.

### Cancellation

| Type | Direction | Carries |
| --- | --- | --- |
| `inference.cancel.request` | caller → implementation | `inference_id`, `reason?` |
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
  not different values of one type.

`part_index` is the correlation, and maps onto their `content_index`.

### The running snapshot

Every one of Makai's ten part variants carries `partial`, the full running
assistant message rather than the increment. Two modules exist only to move it
across their wire.

Deltas alone oblige the consumer to be lossless: it must apply every increment,
in order, without dropping one, or its reconstruction silently diverges from the
provider's. That is a strong requirement to place on every consumer, and a
snapshot is how an implementation lets a consumer resynchronize instead.

So `inference.part.delta` and `inference.part.ended` carry an optional
`snapshot`: the accumulated message as the implementation holds it. An
implementation declares whether it emits one, and how often, through
`snapshot_policy` on its descriptor — `never`, `on_part_end`, `every_delta`.

It is optional because it is expensive: `every_delta` is quadratic in the
message, which is a real cost on a long completion and an obvious one on a
slow link. It is in the profile rather than left to implementations because a
consumer cannot resynchronize against a mechanism that might not be there, and
because an implementation that already computes the snapshot — as at least one
does — should not have to discard it at the boundary and make every consumer
rebuild it.

**Contiguity does not substitute for this.** The per-inference `sequence`
witnesses transport loss, so a consumer knows *that* it diverged. A snapshot is
how it recovers.

## The Call

Split as the implementations split it, because per-call and per-provider are
different sets and merging them makes both wrong.

### Per call — `inference.create.request`

- `model_ref` — provider, wire and model.
- `messages` — shared `Message` and `ContentPart` shapes with agent control.
- `tools` — shared `ToolDefinition`.
- `tool_choice`.
- `max_output_tokens`.
- sampling controls — `temperature`, `top_p`.
- `output_schema` — structured output.
- `stream` — boolean.
- `reasoning` — `{ enabled?, budget_tokens?, effort? }`.
- `credential_ref` — names a credential the implementation holds; never a value.
- `metadata` — opaque, passed through.

### Per provider — `ProviderDescriptor`

- `id`, `display_name?`
- `wire`, `framing`
- `endpoint` — the destination reached.
- `compatibility` — the twelve facts below.
- `snapshot_policy` — `never`, `on_part_end`, `every_delta`.
- `allows_anonymous` — this provider needs no credential.
- `context_window?`, `max_output_tokens?`

`allows_anonymous` is not a nicety. A local Ollama needs no credential, and an
implementation that treats absence-of-credential as an error state refuses a
correctly configured provider.

## Compatibility Facts

The closed set an implementation states about a provider that claims a wire. It
is Makai's `OpenAICompatOptions` — twelve fields, each one a vendor that broke a
shape while claiming it — carried across whole.

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
| `usage_in_streaming` | `always`, `terminal_only`, `never` | `supports_usage_in_streaming` | Whether usage arrives at all changes what `inference.completed` can promise. |
| `requires_assistant_after_tool_result` | boolean | same | A message-ordering constraint, not a capability. |
| `requires_tool_result_name` | boolean | same | Same class. |
| `requires_thinking_as_text` | boolean | same | Reasoning must be sent back as ordinary text or the request is rejected. |
| `supports_strict_mode` | boolean | same | Whether `output_schema` is enforced or advisory. |
| `supports_store` | boolean | same | Server-side retention of the request. |
| `supports_developer_role` | boolean | same | Whether the `developer` role exists or must be folded into `system`. |
| `supports_reasoning_effort` | boolean | same | Whether the effort control is accepted. |
| `tool_call_id_format` | `opaque`, `constrained` | `requires_mistral_tool_ids` | Some endpoints reject tool-call ids that are not in their own format. |
| `cache_ttl_control` | boolean | `supports_anthropic_cache_ttl` | Whether an explicit cache retention is accepted. |

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

**`headers` is a credential channel and is excluded.** `Authorization: Bearer`
is a header, so a caller-supplied header map defeats the rule while every
explicitly credential-named field stays absent. The general form: **any member
that passes caller text through to the upstream request is a credential
channel, whatever it is named.** Nothing in this profile has that shape.

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
key, a per-tenant key, Vertex's documented per-call key — cannot introduce one,
and an earlier version of this draft recorded that as a permanent limit.

It cannot stay a limit. While an implementation could keep a native path beside
this profile, a profile gap cost nothing: express the case natively and let OAP
be the lossy outer wire. For an implementation whose *only* inference wire is
this profile, whatever the profile cannot express, it cannot do — and dropping a
documented provider path is a real loss, not a cleanup.

**The rule was always about the channel, not about who owns the key.** The
reason to keep credential values off envelopes is that envelopes are logged,
assembled into traces, validated, journalled, replayed from a cursor, and
persisted by intermediaries. A value that rides every request is a value in
every one of those. That argument says nothing about whether the caller or the
operator holds the key.

So the profile admits a credential value in exactly one place, and makes the
constraint checkable rather than advisory:

| Type | Direction | Carries |
| --- | --- | --- |
| `provider.credential.grant.request` | caller → implementation | `provider_id`, the value, `ttl_ms?` |
| `provider.credential.grant.response` | implementation → caller | `credential_ref`, `expires_at_ms?` |

Five rules make it safe, and the fourth is the one that turns intent into
enforcement:

1. **One type, one place.** A credential value appears in
   `provider.credential.grant.request` and in no payload member of any other
   envelope. `inference.create.request` is unchanged: it carries a
   `credential_ref`, and a ref from a grant is indistinguishable at the call
   site from one naming operator configuration.
2. **Non-journalable.** The grant pair must not be written to a journal,
   included in an assembled trace, replayed from a cursor, or persisted by any
   intermediary. A binding that records envelopes records everything except
   these two types.
3. **Connection-scoped, never durable.** A grant lives for the connection that
   made it, expires at `expires_at_ms` if the implementation sets one, and does
   not survive a reconnect. There is deliberately no path by which a granted
   credential reaches storage, because a credential that survives a restart is
   a credential the operator did not configure and cannot revoke.
4. **The validator enforces it.** A trace containing either type is invalid, and
   that is a diagnostic with a fixture, not a sentence in a draft. This is the
   difference between a rule and a hope: the machinery this project already has
   for assembling and validating traces is what makes "a credential never
   reaches a trace" a testable claim.
5. **Gated, and refusable.** The grant is an advertised capability. An
   implementation that does not offer it refuses a grant request with a typed
   `unsupported_feature`, and a caller learns that before it sends a secret
   rather than after. An operator-configured-only deployment is a conformant
   deployment.

Bindings carry the rest of the obligation, and it belongs there because it is
transport-shaped: a binding that can carry a grant must document its
confidentiality requirement — a local pipe, or TLS — and a binding that cannot
meet it does not carry the capability.

**What this does not do.** It does not put credentials on the agent-control
wire. [Decision 0017](../decisions/0017-provider-provisioning.md) refuses a
caller-supplied credential there in any form, and nothing here relaxes it: this
is a different profile at a different boundary, where the credential is actually
consumed rather than passed through a control layer that has no use for it.
Whether the grant shape should also be offered at that boundary is a separate
decision, and the asymmetry is intentional until someone argues it away.

## Shared Vocabulary

`ContentPart`, `ToolDefinition`, `Usage`, `ProtocolError` and the tool-call
identity domain mean the same thing on both boundaries and are reused. An agent
loop sitting between them must not translate a content part into a different
content part.

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

An implementation must distinguish, in the typed code, at least: the request
was rejected as malformed; the credential was refused; the model was not found;
a quota or rate limit was hit; the provider failed transiently; the provider
failed permanently. A caller that cannot tell a rate limit from a bad request
cannot retry correctly, and retry behaviour is the main thing a caller does with
a provider error.

## Minimum Conformance

An implementation claiming `open-agent-protocol.model-provider-core`:

1. Answers `provider.describe.request` with at least one provider, naming its
   `wire` and `framing`.
2. Answers `provider.models.list.request`, and every `model_ref` it returns
   resolves to a provider it described.
3. Accepts `inference.create.request` and emits exactly one terminal per
   inference.
4. Emits contiguous per-inference `sequence` on every scoped event.
5. Emits the started/delta/ended triple for every part it streams, with the
   kind-discriminated payloads on start and end, or declares `stream`
   unsupported and answers unary.
6. Honours its declared `snapshot_policy`.
7. Carries no credential value on any envelope except
   `provider.credential.grant.request`, and never journals, traces or replays
   that pair.
8. Refuses a grant with a typed `unsupported_feature` if it does not advertise
   the capability, rather than accepting and ignoring it.
9. States a compatibility fact where the provider it reaches diverges from the
   wire it claims, or states none and claims nothing.

Streaming is required only if advertised. A unary-only implementation is
conformant; a streaming implementation that skips `part.ended` is not.

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
nothing about readiness. Because a provider-profile implementation is
independently servable, the harness can be the same one in outline: spawn a
binary, drive a scripted inference over a line binding, hand the assembled trace
to the validator. But the existing harness works because there is an endpoint
built to be driven, and there is no counterpart here. Building one is the whole
of the work, not a consequence of the profiles being independently servable —
and an earlier version of this draft drew that conclusion too fast.

Compatibility is a second, harder half. A loopback provider proves the envelopes
and nothing about whether an endpoint honours the wire it claims; a live
provider proves that on one day, for money. The two halves need different
machinery, and only the first is cheap once something exists to drive.

**Where does credential acquisition live?** `auth_status` is deliberately
homeless: [Decision 0014](../decisions/0014-provider-descriptors.md) keeps it
off the descriptor because it moves without the descriptor moving, this profile
does not claim it, and the agent-control draft records acquisition as an open
question. In Makai's tree it sits above the provider layer, not in it — the
provider layer consumes a credential at request time and never learns its
status. So it follows acquisition, and nothing owns acquisition yet.

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

**Makai's provider layer**, read from that tree on 2026-09-17, and read a second
time against this draft's first version, which it corrected in four places — the
lossy part collapse and its missing snapshot, an unattested `unary`, a
promoted-six compatibility split that failed its own test, and a credential rule
whose no-cost claim was false. The facts below: the per-call and
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

**No implementation speaks this profile**, because it did not exist until this
draft. Under Decision 0015 it becomes executable when something outside this
repository speaks it, and Makai doing so is the expected first case and is not
sufficient alone if Makai becomes first-party.

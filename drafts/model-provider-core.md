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

It is not a credential channel. See Credentials below.

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

This is not a hypothetical split. Makai's provider layer serves six of eight
APIs through one SSE parser and parses Ollama as newline-delimited JSON with no
SSE parser at all, read from that tree on 2026-09-17. This repository's own
prober assumes the other way: `provider.RunEvidence` fails any response whose
media type is not `text/event-stream` (`provider/evidence.go`), which is correct
for a compatibility prober and would be wrong for the profile.

A profile that folded framing into wire would exclude a working provider while
believing its set complete.

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

## Envelope Types

Twelve, in three groups.

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

The started/delta/ended triple per part is taken directly from Makai's event
union, which carries the same triple for text, thinking and tool calls. The
`ended` event is load-bearing rather than decorative: without it a consumer
cannot tell a finished part from a stalled one until the terminal arrives, and
a tool call that is complete is dispatchable immediately.

Their thirteen variants map here as nine parts collapsed into three
part-scoped envelopes, plus start, done, error, and a keepalive that becomes a
binding concern.

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
- `metadata` — opaque, passed through.

### Per provider — `ProviderDescriptor`

- `id`, `display_name?`
- `wire`, `framing`
- `endpoint` — the destination reached.
- `compatibility` — the facts below.
- `allows_anonymous` — this provider needs no credential.
- `context_window?`, `max_output_tokens?`

`allows_anonymous` is not a nicety. A local Ollama needs no credential, and an
implementation that treats absence-of-credential as an error state refuses a
correctly configured provider.

## Compatibility Facts

The closed set an implementation states about a provider that claims a wire. It
generalizes Makai's `OpenAICompatOptions`, which is twelve fields, each one a
vendor that broke a shape while claiming it.

| Fact | Values | Why it cannot be hidden |
| --- | --- | --- |
| `max_tokens_field` | `max_tokens`, `max_completion_tokens` | One semantic field, two names. A caller building a request must know which. |
| `reasoning_encoding` | `none`, `openai`, `anthropic`, and named vendor encodings | Three mutually incompatible encodings sit behind one API name. A wrong guess silently drops reasoning. |
| `usage_in_streaming` | `always`, `terminal_only`, `never` | Whether usage arrives at all changes what `inference.completed` can promise. |
| `requires_assistant_after_tool_result` | boolean | A message-ordering constraint, not a capability. A caller that does not know it builds a request the provider rejects. |
| `tool_result_requires_name` | boolean | Same class. |
| `supports_strict_schema` | boolean | Whether `output_schema` is enforced or advisory. |

Every fact is optional and its absence means "the wire's own default," never
"unknown." An implementation that states none is exactly as conformant as one
that states all six — it has simply promised less.

**Anything not in this set travels in `extensions`.** The set grows by protocol
change, because a fact a caller must branch on and cannot discover is not a
fact, and a free-form quirks map is a way of shipping the problem to every
caller at once.

## Credentials

**No credential value crosses this wire.** Not a key, not a token, not a header,
not an environment value.

This is the constraint
[Decision 0017](../decisions/0017-provider-provisioning.md) sets for provider
provisioning at the agent-control boundary, and it holds here for the same
reason one layer down. A `ProviderDescriptor` carries no credential member, and
an `inference.create.request` carries none either: the implementation resolves
the credential on its own side, from its own configured store, at request time.

That is how at least one real implementation already works — Makai constructs a
provider with no credential and resolves it per request from a named store, read
from that tree on 2026-09-17 — so the constraint costs nothing it was not
already paying.

**`headers` is a credential channel and is excluded.** `Authorization: Bearer`
is a header, so a caller-supplied header map defeats the rule while every
explicitly credential-named field stays absent. The general form: **any member
that passes caller text through to the upstream request is a credential
channel, whatever it is named.** Nothing in this profile has that shape, and a
later addition that needs one allowlists names rather than carrying values.

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
5. Emits the started/delta/ended triple for every part it streams, or declares
   `stream` unsupported and answers unary.
6. Carries no credential value on any envelope.
7. States a compatibility fact where the provider it reaches diverges from the
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

**What drives conformance?** `oap conformance` spawns an agent-control endpoint
and drives a scripted session. The equivalent here has to drive an
implementation against providers that cost money to call and cannot be pinned.
A loopback provider proves the envelopes and nothing about compatibility; a live
provider proves compatibility on one day.

**Where does credential acquisition live?** `auth_status` is deliberately
homeless: [Decision 0014](../decisions/0014-provider-descriptors.md) keeps it
off the descriptor because it moves without the descriptor moving, this profile
does not claim it, and the agent-control draft records acquisition as an open
question. In Makai's tree it sits above the provider layer, not in it — the
provider layer consumes a credential at request time and never learns its
status. So it follows acquisition, and nothing owns acquisition yet.

**Is the compatibility set the right six?** It is generalized from one
implementation's twelve. Some of theirs are vendor-specific enough to belong in
`extensions`; some of the six here may prove to be. This wants a second
implementation before the set is frozen, which is the same standard
[Decision 0015](../decisions/0015-evidence-from-implementations-we-do-not-control.md)
applies to graduation.

**Does `reasoning_encoding` belong as a fact or a capability?** It is written
here as a fact about the provider. It may be better as a declared support level,
the way agent control reports `native`/`emulated`/`degraded`/`unavailable`.

## Evidence And Standing

This draft is written from two sources and neither makes it executable.

**Makai's provider layer**, read from that tree on 2026-09-17: the per-call and
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

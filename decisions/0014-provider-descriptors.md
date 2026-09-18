# Decision 0014: Provider Descriptors

Status: proposed
Date: 2026-09-17
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `models` (claim term `+models`) — an additive extension of the unit
[Decision 0006](0006-models-catalog.md) graduated, not a new one
Amends: nothing. Extends
[Decision 0006](0006-models-catalog.md) additively: one optional member on an
existing payload, whose absence means exactly what the catalog meant before it
existed. Caller-supplied provider provisioning is not part of this record; it
is an attachment at session open, symmetric to tool sources, and is deferred to
its own decision
Gated by: [Decision 0003](0003-staged-unit-graduation.md)

## Context

`ModelDescriptor.provider_id` has been on the wire since Decision 0006. It is
an opaque string, and nothing in v0.1 says what one resolves to.

A control layer reading a catalog of eleven models across three vendors gets
eleven ids and eleven `provider_id` strings it cannot interpret. It cannot
group them, cannot tell the reader which vendor is about to see the prompt,
cannot tell a first-party endpoint from a gateway, and cannot tell whether two
models sharing a provider share a failure domain. The field is a join key with
nothing to join to.

This is the same gap Decision 0008 closed for tools. `ToolDefinition.source`
was an opaque id until `ToolSourceDescriptor` gave it something to resolve to,
and the argument was the same: an attribution a consumer cannot resolve is not
an attribution.

Harnesses already treat the provider as client-visible. Hermes's
`session.create` takes `provider?` beside `model?` and returns
`info: {model[, provider], ...}`, so a Hermes client both chooses and observes
one. ACP is "provider-neutral via a `base_url`/`token_key` provider block".
Codex pins a `wire_api` per provider. This repository has a whole package for
it — `provider/`, whose package doc described it as modelling "model-provider
compatibility surfaces independently from OAP harness adapters" until the
comment sweep in #64 removed the sentence (`git show 88186c8:provider/zai.go`),
carrying `Wire`, `BaseURL`, `Path` and an evidence classification.

## Decisions

### `models.response` gains an optional `providers[]`

A list of `ProviderDescriptor`, parallel to `sources[]` on
`action.tools.list.response` and for the same reason:

```
{ "id", "display_name"?, "wire"?, "kind"?, "endpoint"? }
```

`id` is what a `ModelDescriptor.provider_id` resolves to. `wire` is the
request shape the endpoint speaks to that provider, from the closed set the
`provider` package already names: `openai-responses`, `anthropic-messages`,
`openai-chat-completions`. `kind` distinguishes a provider the endpoint
reaches directly from one reached through a gateway or proxy. `endpoint` is
the destination the endpoint reaches, published so a client can tell a direct
provider from a gateway.

It is read-only, and a caller does not supply one. Provisioning a provider is
an attachment at session open, symmetric to `tool_sources`, and belongs in its
own decision — see [the composition draft](../drafts/composition.md). Putting a
writable destination on a descriptor, as an earlier version of this record did,
conflated two different things: a descriptor is what an endpoint publishes
about itself and is fixed for a capability revision, while an attachment is
what a caller asks for and is judged at admission.

Every member but `id` is optional, and the list itself is optional. An endpoint
that publishes no `providers[]` is exactly as conformant as it is today, and a
`provider_id` that resolves to no descriptor is unattributed — which is how
every catalog read before this existed. That is the additive shape
[the stability commitment](../STABILITY.md) section 1 requires: absence carries
a positive meaning rather than "unknown".

### A `provider_id` the catalog publishes must resolve

Where `providers[]` is present, every `provider_id` appearing on a
`ModelDescriptor` must name an entry in it. The validator enforces it as
`unmatched_provider`, the same rule and the same shape Decision 0008 gives a
tool `source` that resolves to nothing.

Without this the addition would be decoration: an endpoint could publish a
provider list that omitted the providers its models actually name, and a
consumer would be no better off than with the bare string. The rule is what
makes the join reliable enough to build on.

### Layering, and what is not here

An earlier version of this record refused wire-carried provider configuration
in every form, citing tool sources as precedent. That was wrong on a checkable
fact: `protocol.ToolSourceAttachment` carries `command`, `args`, `environment`
and `endpoint`, and the v0.1 schema admits all four. It is `oap serve` that
refuses them — "the daemon does not accept a command or arguments from the
wire; name an operator-configured source by id" — while an in-process embedder
passes them to the adapter. A deployment policy was cited as a protocol
decision.

The correction is that provisioning is expressible and belongs in a different
shape from this one. It is an attachment at session open beside `tool_sources`,
inheriting that unit's rules — gated on a capability key, whole or nothing,
refusals naming the entry at fault, the daemon governing the destination
through operator configuration rather than accepting one from the wire, and
credentials never travelling in any deployment.
[The composition draft](../drafts/composition.md) sets that out, and it is its
own decision to write.

This record is the read direction only: what an endpoint publishes about the
providers it reaches. Separating them is what lets this one graduate on the
evidence it has, while provisioning waits for an implementation that accepts a
caller-supplied destination — which no pinned harness does today.

### The `provider` package is the model, not the payload

`provider.Preset` carries `BaseURL`, `Path`, `Model`, `EvidenceClass` and
`SourceURL`. `ProviderDescriptor` carries the first as an optional member and
none of the rest.

The package exists to record what this project has *verified* about a
provider's compatibility, with an evidence class saying how well. That is
research, and it is right that it sits outside the protocol. `EvidenceClass`
and `SourceURL` are provenance claims about this repository's own testing, and
a consumer reading a live catalog has no use for them.

## Evidence

**Hermes** makes the provider a client-visible session parameter:
`session.create {..., model?, provider?, ...}` answered with
`info: {model[, provider], ...}`. A Hermes client that could select a provider
natively and cannot see one over OAP has lost information the harness was
already giving it.

**ACP** is "provider-neutral via a `base_url`/`token_key` provider block",
which is exactly the configuration-side shape this decision declines to carry
and confirms that the configuration lives with the operator.

**Codex** states the principle this decision adopts, in its own ledger:
"Harness conformance and provider compatibility are independent." It pins
`wire_api = "responses"` per provider, which is the `wire` member, and it keeps
the base URL in its configuration file, which is where this leaves it.

**This repository** already separated the two concerns in code. `provider/`'s
package doc described it as modelling "model-provider compatibility surfaces
independently from OAP harness adapters" — that sentence was deleted by the
comment sweep in #64 and is recoverable at
`git show 88186c8:provider/zai.go`; the separation it described is still in
the package's structure and in `cmd/oap providers zai-cn`, which serves the
presets as a CLI query rather than a protocol operation.

**Two adapters already populate `provider_id`, and a first version of this
record said none did.** That claim came from reading the reference adapter and
stopping. OpenCode derives it for every entry in a real served catalog,
splitting the native `provider/model` id shape
(`adapter/opencode/session.go:915`), so the field is in use against a genuine
multi-provider harness today. The reference adapter emits `"reference"` on both
its fixed entries.

**Makai's native wire carries more than this decision proposes to expose.**
`AgentEndEvent` has `provider_id` *and* `api`
(`adapter/makai/internal/native/events.go:38-39` at `906b2a1`) — which provider
served the run and over which wire — and the adapter maps neither. That is the
`wire` member arriving from a harness that already reports it per run, and it is
the strongest single piece of evidence here: a harness volunteering the fact
unprompted, with nowhere for it to go.

Both are `omitempty`, and read against source in the Makai tree this is
load-bearing rather than cosmetic: the fields are not members of the payload struct upstream
but are written from the terminal assistant message, so an `agent_end` with no
terminal assistant message carries neither. A consumer reads the pair as
*present or absent per run*, never as a field guaranteed by the event.

What remains genuinely unproven is `endpoint` itself. No pinned harness
publishes the destination it reaches for a provider — ACP, Codex and Hermes
all take it from operator configuration and never report it back — so that one
member graduates on a native implementation or not at all, and the gate's step
3 is where that is decided. The rest of the descriptor does not wait on it.

## Consequences

A control layer can group a catalog by vendor, show a user which provider is
about to receive their prompt, and tell a direct endpoint from a gateway.

`unmatched_provider` joins `unmatched_tool_source` as a resolution diagnostic,
with the same meaning in the same place, so an implementer who has met one
already knows this one.

No trace valid today becomes invalid. The member is optional, its absence is
the current behaviour, and the resolution rule binds only a catalog that
publishes the list.

The `+models` unit's requirements grow by one conditional clause rather than
gaining a new envelope, so an endpoint claiming `+models` today keeps its claim
without doing anything.

## What this decision does not admit

Credentials over the wire, in any form, under any capability key, for any
deployment. Not a token, not a header, not an environment value. A name that
the operator resolves is the only form a secret takes on this wire.

A caller-supplied `endpoint`, in any form. This record is the read direction
only: `providers[]` is what an endpoint publishes about itself. Naming a
destination from the wire is provisioning, it belongs to the attachment shape
[the composition draft](../drafts/composition.md) sets out, and it is that
decision's to admit or refuse — not this one's.

Provider selection as a run control. `model_id` already selects, and Decision
0005 made it per-run; a model resolves to its provider through the catalog. A
second selector would let a submit name a model and a provider that disagree.

Provider health, quota, latency or cost. All of them change under a catalog
that is fixed for a capability revision, and a descriptor that went stale
between two reads would be worse than no descriptor.

Authentication state, for the same reason and against a real request for it.
The Makai tree carries an `auth_providers_response` with `provider_id`, `name`
and `auth_status` together — read from that live tree, not pinned here: the adapter at `makai-agent-67ad514` models no auth namespace and its
ledger records none, so this is not evidence under Decision 0003's step 3, the
same standing Decision 0013 gives Codex's `turn/steer`. A client that cannot
tell "logged into one vendor, not the other" cannot render a model picker
honestly — which is true of any multi-provider harness. But `auth_status`
changes the moment someone logs in, and a revision-fixed descriptor is the
wrong carrier for something that moves without the descriptor moving. Provider
*identity* is stable and belongs here;
provider *usability* is dynamic and belongs somewhere generated per read.
Splitting them is the cost of getting identity right; carrying both here would
make the catalog either stale or unstable.

[The model-provider-core draft](../drafts/model-provider-core.md) resolves where
the dynamic half goes, and this record's objection survives intact rather than
being overridden: `auth_status` sits on the model *entry* in a response,
explicitly not bound to `capability_revision`, with the two transient values
dropped. A response is generated per request while a descriptor is fixed per
revision, so the volatile fact lives on the thing that is rebuilt each time.
What remains genuinely homeless is credential *acquisition* — the flow that
produces a credential — which neither profile claims and the core draft still
records as open.

A registry of provider ids with meanings assigned by this project. `id` is
opaque and endpoint-scoped, exactly like a tool source id.

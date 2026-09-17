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
existed
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
it — `provider/`, whose doc comment says it "describes model-provider
compatibility surfaces independently from OAP harness adapters", carrying
`Wire`, `BaseURL`, `Path` and an evidence classification.

## Decisions

### `models.response` gains an optional `providers[]`

A list of `ProviderDescriptor`, parallel to `sources[]` on
`action.tools.list.response` and for the same reason:

```
{ "id", "display_name"?, "wire"?, "kind"? }
```

`id` is what a `ModelDescriptor.provider_id` resolves to. `wire` is the
request shape the endpoint speaks to that provider, from the closed set the
`provider` package already names: `openai-responses`, `anthropic-messages`,
`openai-chat-completions`. `kind` distinguishes a provider the endpoint
reaches directly from one reached through a gateway or proxy.

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

### Configuration stays off the wire

**No member of `ProviderDescriptor` carries a base URL, a credential, a
header, a key name, or a path.** A provider is named and characterised, never
configured. This decision adds nothing a control layer can use to point an
endpoint somewhere new.

This is not caution; it is the rule this repository already enforces one layer
down. `serve/attach.go` refuses a command or arguments arriving on the wire
outright — "the daemon does not accept a command or arguments from the wire;
name an operator-configured source by id" — and accepts only the bare `NAME`
allowlist form in an environment block. Tool sources are named on the wire and
configured by the operator. Providers get the identical split, because the
asymmetry would be indefensible: a control layer that may not choose which
binary runs a tool certainly may not choose which endpoint receives the prompt.

The security property is worth stating plainly rather than leaving as an
inference. An endpoint that accepted a provider base URL from its control layer
would let whoever holds the control channel redirect every prompt, every tool
result and every file the agent has read to a host of their choosing, with the
endpoint's own credentials or none. That is a prompt-exfiltration primitive
delivered through a configuration convenience, and no amount of allowlisting on
the receiving side makes it a good trade when the operator's config file
already does the job.

Decision 0004's extension packs remain available to an endpoint whose operators
genuinely want wire-level provider configuration in a controlled deployment. It
belongs in a namespace whose users opted into it, not in core.

### The `provider` package is the model, not the payload

`provider.Preset` carries `BaseURL`, `Path`, `Model`, `EvidenceClass` and
`SourceURL`. `ProviderDescriptor` deliberately carries none of them.

The package exists to record what this project has *verified* about a
provider's compatibility, with an evidence class saying how well — it is
research, and it is right that it sits outside the protocol. What goes on the
wire is the subset a consumer needs to interpret a catalog it is reading now.
Promoting the rest would put deployment facts and provenance claims into an
envelope, and neither is the control layer's business.

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

**This repository** already separated the two concerns in code. `provider/`
models compatibility "independently from OAP harness adapters", and
`cmd/oap providers zai-cn` serves it as a CLI query rather than a protocol
operation.

**Counter-evidence, recorded rather than omitted.** No pinned adapter
currently populates `provider_id` at all. The reference adapter emits
`ProviderID: "reference"` on both catalog entries and no other adapter sets it,
so this decision extends a field that is, today, almost entirely unused. That
is an argument for graduating it alongside adapters that serve a real catalog —
Pi has `get_available_models` and advertises no `models.list` at all — and not
an argument against the shape. The gate's step 3 will not be satisfiable until
at least one native adapter both serves a catalog and attributes it.

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

Provider configuration over the wire, in any form, including a "trusted"
control layer, an allowlisted base URL, or a provider id that an endpoint
resolves to a caller-supplied endpoint.

Provider selection as a run control. `model_id` already selects, and Decision
0005 made it per-run; a model resolves to its provider through the catalog. A
second selector would let a submit name a model and a provider that disagree.

Provider health, quota, latency or cost. All of them change under a catalog
that is fixed for a capability revision, and a descriptor that went stale
between two reads would be worse than no descriptor.

A registry of provider ids with meanings assigned by this project. `id` is
opaque and endpoint-scoped, exactly like a tool source id.

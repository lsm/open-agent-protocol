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
existed. The caller-supplied `endpoint` member is separately gated on a
capability key and is the one part with no native evidence yet
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
reaches directly from one reached through a gateway or proxy. `endpoint` is
the destination, optional and gated — see the layering below.

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

### The vocabulary is layered, and only credentials are forbidden outright

A first version of this decision refused wire-carried provider configuration
in every form, and argued that "this is the rule this repository already
enforces one layer down", citing tool sources. That argument was wrong on a
checkable fact. `protocol.ToolSourceAttachment` carries `command`, `args`,
`environment` and `endpoint`, and `schema/v0.1/session.schema.json` admits all
four. It is `oap serve` that refuses them — `serve/attach.go` returns "the
daemon does not accept a command or arguments from the wire; name an
operator-configured source by id" — while an in-process embedder passes them
straight to the adapter. A deployment policy was cited as though it were a
protocol decision.

The shape tool sources actually have is four layers, and providers take the
same four:

1. **The protocol carries the vocabulary**, base URL included. An optional
   `endpoint` on `ProviderDescriptor`, exactly as `ToolSourceAttachment`
   carries one.
2. **A capability key gates it.** An endpoint that does not advertise
   caller-supplied provider endpoints refuses one with the typed
   `unsupported_feature`, and its surface does not grow.
3. **`oap serve` refuses it as local policy** and resolves provider ids against
   the operator's configuration, exactly as it does for sources. The daemon's
   trust model is unchanged: loopback, single-user, nothing from the wire that
   names a program or a destination.
4. **Credentials never travel.** This is the one absolute, and it already has
   its shape: the daemon accepts only the bare `NAME` allowlist form in an
   attachment's `environment`. Variable names, never values. The wire says
   which secret to use; the operator supplies it.

Layer 4 is not a deployment choice and no capability key unlocks it. Layers 1
through 3 are, and separating them is what lets an embedder or a hosted
control layer do bring-your-own-key and per-session gateways while `oap serve`
stays as strict as it is today.

The exfiltration concern that motivated the first version is real and is
answered by layer 2 rather than by refusing the vocabulary. An endpoint that
lets its control layer choose a destination can have every prompt, tool result
and file the agent has read sent to a host of the caller's choosing. That is a
reason to make it an advertised, refusable capability that most endpoints never
offer — the same answer OAP gives every other dangerous affordance — not a
reason to make it inexpressible for the deployments where the control layer and
the operator are the same party.

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
(`adapter/makai/internal/native/events.go:39`) — which provider served the run
and over which wire — and the adapter maps neither. That is the `wire` member
arriving from a harness that already reports it per run, and it is the
strongest single piece of evidence here: a harness volunteering the fact
unprompted, with nowhere for it to go.

What remains genuinely unproven is the caller-supplied `endpoint` of layer 1.
No pinned harness accepts a provider endpoint from its client — ACP, Codex and
Hermes all take it from operator configuration — so that member graduates on a
native implementation or not at all, and the gate's step 3 is where that is
decided.

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

A caller-supplied `endpoint` from an endpoint that has not advertised it. The
member existing does not make it offerable; layer 2 is what makes it offered,
and an endpoint that stays silent refuses it typed.

Provider selection as a run control. `model_id` already selects, and Decision
0005 made it per-run; a model resolves to its provider through the catalog. A
second selector would let a submit name a model and a provider that disagree.

Provider health, quota, latency or cost. All of them change under a catalog
that is fixed for a capability revision, and a descriptor that went stale
between two reads would be worse than no descriptor.

A registry of provider ids with meanings assigned by this project. `id` is
opaque and endpoint-scoped, exactly like a tool source id.

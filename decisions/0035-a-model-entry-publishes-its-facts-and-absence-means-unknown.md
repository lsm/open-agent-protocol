# Decision 0035: A Model Entry Publishes Its Facts, and Absence Means Unknown

Status: proposed
Date: 2026-09-26
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.model-provider-core`
Relates to: [Decision 0014](0014-provider-descriptors.md),
[Decision 0030](0030-remote-provider-http-binding.md),
[Decision 0034](0034-an-unpublished-catalog-is-unknown.md)
Evidence: [`research/opencode-provider-catalog-mapping.md`](../research/opencode-provider-catalog-mapping.md)

## Context

`provider.models.list.response` is the only place a caller learns what a model
is. Its entry, as [`drafts/model-provider-core.md`](../drafts/model-provider-core.md)
§"The model entry" records it, carries the facts needed to *place* a model:
`model_ref`, `model_id`, `display_name`, `provider_id`, `wire`,
`context_window`, `max_output_tokens`, `capabilities`, `lifecycle`, `source`,
`reasoning_default`, `auth_status`. Per-model protocol selection is already data
here, which is the one thing a shared catalog has to invent a structure for.

Four things a caller routinely needs have no member to arrive on. Price is the
loudest: no cost is published anywhere in the profile, while
[`drafts/conformance.md`](../drafts/conformance.md) already puts "pricing"
outside every current unit "unless a richer profile defines them", and the Zig
internal model carries one a layer below the boundary. Modality coverage stops
at `vision` and `audio_input` / `audio_output`, so a model that also takes video
or a document cannot say so. `reasoningLevel` and `reasoning_default` exist, but
nothing says which levels a model accepts, so a caller asked to choose one has
no set to choose from. `release_date` and `family` are absent, leaving a client
no way to prefer a newer sibling except through `lifecycle`.

Two further gaps belong to the response rather than the model, and neither needs
a catalog to motivate them. A list response cannot say it is a subset, and
[`+models`](../drafts/conformance.md) binds an implementation in both
directions — every id it lists is selectable, and every id it omits is not — so a
provider whose own listing paginates hands the session catalog built from it a
claim of completeness nobody can make. The two catalogs are different envelopes
in different profiles, and the binding is judged on the agent side:
`+models` reads `models.response`, and the validator that emits
`model_not_in_catalog` never sees `provider.models.list.response`. So the
provider profile can publish the fact and cannot repair the consequence, and
this decision stops at publishing it. Nothing states how old a listing is
either, so a `fallback` source is as indistinguishable from yesterday's as from a
year ago.

The empirical shape of what a catalog knows is recorded in the evidence ledger:
at one dated reading, 223 providers and 8179 models, with cost on 7755 of them,
a full modality list on all of them, reasoning options on 4795, and a per-model
protocol override on 335. Kimi's own listing, read without a credential,
publishes a context window and a display name and reports neither an output cap
nor a price, which is why `model_catalog.zig` defaults Kimi's
`max_output_tokens` to 16384 while the shared dataset records 32768 for a
nearby id.

## Decisions

### Presence publishes a fact; absence leaves it unknown

A `modelEntry` member that is absent says the implementation did not learn that
fact. It does not license a default: a caller must not read an absent `cost` as
free, an absent modality as text-only, an absent level set as a single level. A
`cost` whose numbers are all zero is a published fact about a free model; an
absent `cost` is not a quotation.

This is Decision 0034's rule for tool catalogs, applied to model facts: presence
is what publishes, and it is decidable from one response. The distinction is
deliberate — a descriptor is fixed for a revision (Decision 0014) while a list
response is built per request, so a volatile fact belongs on the entry.

### The entry gains the facts a caller cannot otherwise learn

Optional, additive, and each absent by default:

| Member | Shape | Why |
| --- | --- | --- |
| `cost` | `{ input, output, cache_read, cache_write }`, all optional numbers | a caller that shows a rate needs the four numbers; nothing else is carried |
| `input_modalities` | list of `text`, `image`, `audio`, `video`, `document` | completes the media surface `capabilities` starts |
| `output_modalities` | same vocabulary | a model that answers in more than text cannot say so today |
| `reasoning_levels` | list of `reasoningLevel` | the set a model accepts, with `reasoning_default` naming one of them |
| `release_date` | string | lets a client order siblings |
| `family` | string | groups models that differ only in size or speed |

`cost` is a fact, not a quotation and not a promise. No conformance unit judges
it, it is true only of the response that carried it, and an implementation that
quotes a rate it does not honour has published a falsehood rather than a wrong
price. The provider schema carries no cost member today, so nothing here is a
change to a claim already made.

`capabilities` keeps its behavioural members and its three media values are
**superseded** by the two modality lists: a caller reading the lists ignores
`vision`, `audio_input` and `audio_output`. Whether those three are retained as
deprecated aliases or removed outright is left to review; retaining them is the
smaller break, removing them is the smaller vocabulary.

### The listing publishes its own completeness and age

`provider.models.list.response` gains an optional `catalog` member:

```
catalog?: { observed_at_ms?: integer, complete: boolean }
```

`complete: false` publishes a partial listing, and that is all it does here. A
caller told the listing is a subset must read an absent `model_ref` as an id
nobody has heard of rather than as a model that does not exist, and a provider
that has never heard of an id is not thereby unable to serve it: the refusal this
profile already defines for an id it does not serve, `model_not_found`, is
unchanged. An absent `catalog` keeps today's meaning, the listing being the whole
of what this implementation knows. The asymmetry with the rule above is
intentional — an absent per-model fact is unknown, while the listing's
completeness is presumed and must be denied explicitly, because the consumer of
that presumption assumes it.

**What this does not repair.** `+models` judges the agent-control session
catalog on `models.response`, a closed payload the validator reads without ever
seeing the provider's list, so a `complete: false` here does not stand that
binding down and the unit's `model_not_in_catalog` diagnostic is unchanged. A
caller that receives a partial provider listing decides for itself what to
publish in its own catalog, and that decision is judged under its own unit.
Making a partial catalog *disarm* the two-directional binding needs a member on
`models.response` or an amendment to `+models`, and `models.response` is bound to
`capability_revision`, so a completeness flip there would be a capability change
rather than a per-request fact. That is a separate decision in the agent-control
profile, and naming it here is the most this decision does about it.

`observed_at_ms` is when the underlying catalog was read, not when the response
was built, so a `fallback` source can be told from a fresh one.

## What this decision does not admit

- **A catalog source URL, or any upstream endpoint.** Decision 0030 keeps a
  vendor URL off an envelope a caller may read; a client that learned where to
  fetch would be a client holding a second, unversioned path to the provider.
- **Credential hints of any kind**, including the environment variable names a
  catalog carries per provider. A provider resolves its own credentials, and
  `auth_status` already answers the only question a caller may ask.
- **Per-mode variants, per-mode price, and raw body or header overrides.** Body
  overrides on a model entry would be a vendor payload channel this profile does
  not otherwise have; per-call shaping stays on `inference.create.request`.
- **A refresh or force member on `provider.models.list.request`.** A list
  response is already generated per request, so a caller that wants fresher data
  asks again; `observed_at_ms` is how it learns whether asking again would
  differ. A request member whose whole meaning is cache policy would also be the
  first one on this boundary that describes the implementation's internals
  rather than the caller's choice.
- **Any new envelope type, operation or error code.** `model_not_found` stays the
  provider profile's refusal for a model it does not serve, and
  `model_not_in_catalog` stays the `+models` diagnostic; a partial listing is
  still answered with whichever of those the caller's own unit asks for.
- **Any change to `models.response` or to the `+models` rule.** Judged on the
  session catalog, in the other profile, and out of scope here.
- **Pricing as a conformance claim.** The `+models` unit is unchanged, and this
  keeps the line in `drafts/conformance.md` that keeps pricing outside it.

## Consequences

- A caller can render a rate, a full modality list, the accepted reasoning
  levels, and a family's release order from one response instead of shipping its
  own dataset or guessing.
- `source: fallback` becomes actionable: with `observed_at_ms` a client can say
  how stale it is, which was the draft's own argument for publishing `source` at
  all.
- An implementation reading an external catalog can fill these members without
  the protocol having an opinion about where it read them, which is the point:
  the members are carriable facts, and the catalog is one implementation's
  choice.
- The provider profile grows six optional members and one optional response
  member. A responder that publishes none of them is unchanged and still
  conformant, which is what makes the absence rule load-bearing rather than
  decorative.
- The Zig `ai_types.Model` and the Go provider wire stop being the only places
  these facts can exist, so an implementation no longer has to choose between
  publishing them and shipping them privately.

## What accepting this requires

Not part of this proposal, and the reason it is a proposal: edits to
`schema/v0.1/provider.schema.json`, to the model-entry list in
`drafts/model-provider-core.md`, to the `protocol/` types, the Go validator, a
fixture per new judgement, and `clients/ts/src/protocol.ts` — the standing
requirement for a schema change. The same model entry is also written down for
the SDK in `docs/v1-sdk-agent-provider-spec.md`, which carries the Zig model's
`capabilities` and `context_window` beside the wire list. A
`catalog.complete: false` fixture on the provider side, and a "price is published
but not judged" statement in `drafts/conformance.md`, are the two new judgements.
No `models.response` member, no `+models` rule edit and no
`go/validation/models.go` change follows from this, because `+models` never reads
the envelope this decision touches.

## Open questions for review

1. Retain `vision`, `audio_input`, `audio_output` in `modelCapability` as
   superseded aliases, or remove them in the same change?
2. Should `cost` be carried at all, given that the unit that would judge a
   price does not exist, or is an unjudged published fact the right trade?
3. Is `cost`'s flat four-number shape enough, or does a tiered rate
   (`context_over_200k` in the evidence) belong in v0.1 at all?
4. `document` versus `pdf` for the modality name, and whether `video` is worth
   a member before a provider in the conformance corpus needs it.
5. Does the agent side want completeness at all, and if so on which envelope: a
   `models.response` member, which is bound to `capability_revision` and so makes
   a completeness flip a capability change, or an amendment to the `+models` rule
   that has the unit judge a partial catalog on its own terms. That decision
   belongs to `agent-control-core` and this one does not presume its answer.

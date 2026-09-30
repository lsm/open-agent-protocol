# OAP model entry: absence of optional facts

## What changed

`ModelEntry.lifecycle` is now `?ModelLifecycle = null`. The encoder writes
the member only when it is set, and the decoder keeps an absent member
absent instead of defaulting it. Before this, `types.zig:436` defaulted the
field to `.stable` and `envelope.zig:1219` decoded an omitted member as
`orelse .stable`, so a wire that omitted `lifecycle` round-tripped into a
published `"lifecycle":"stable"` the peer never sent.

## What this does not claim

**Not every publisher is corrected.** The OAP provider codec and the
`ModelEntry` type are; nothing else is fixed by this change.

The built-in fallback rows in `populateOapProviderCatalog` are the clearest
case. `BuiltInProvider` (`oap/provider/catalog.zig:120-132`) has no
`lifecycle` member at all, so those rows never stated one — they published
`stable` only because the type supplied the default. This change does not
replace that default with an explicit `stable`, which would republish the
same invented value under a new line. Their entries now omit `lifecycle`,
and a client reads the absence as unknown, per
`drafts/model-provider-core.md:382-390`.

Substituting a row-specific `lifecycle` would need an actual pinned source
per row, and there is none today. If a source appears, the rows should carry
the value it states rather than a value the code prefers.

## Still defaulted, and why it is not fixed here

`ModelEntry.source` defaults to `.discovered` in the same two places
(`types.zig:437`, `envelope.zig:1220`), so a decoded listing that omits
`source` still becomes `discovered` on re-encode. That is the same defect
class, and it is queued as its own correction rather than folded in here,
because provenance and a serving fact are different questions and bundling
them puts both behind one review.

## Contract this follows

`schema/v0.1/provider.schema.json:261-269` requires only `model_ref`,
`model_id`, `provider_id` and `wire` from a published model entry, so
omitting `lifecycle` is already legal and no schema change is needed.

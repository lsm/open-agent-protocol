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
`ModelEntry` type are; nothing else is fixed by the `lifecycle` change, #665.

The rows `populateOapProviderCatalog` serves were the clearest case. They used
to be three built-in fallback rows whose type had no `lifecycle` member, so
they published `stable` only because the type supplied the default. They are
now the models the runtime loaded for each provider that has a key present or
needs none (#355), and loading states no lifecycle either, so their entries
omit it, and a client reads the absence as unknown, per
`drafts/model-provider-core.md:382-390`.

Substituting a row-specific `lifecycle` would need an actual pinned source
per row, and there is none today. If a source appears, the rows should carry
the value it states rather than a value the code prefers.

## `source` now preserves absence too, and it landed first

This section used to say `ModelEntry.source` was still defaulted to
`.discovered` in `types.zig:437` and `envelope.zig:1220`, and that the fix was
queued. That is no longer true and the sentence has been removed rather than
hedged: #673 made `source` omission-preserving and merged first, so its
companion, #665, is the second half of the same change and carries
`lifecycle`.

Both members are absent-tolerant and neither is defaulted:

    lifecycle: ?ModelLifecycle = null,   types.zig:436
    source:    ?ModelSource = null,      types.zig:437

The encoder omits an absent member rather than publishing a value
(`envelope.zig:116-117`), and the decoder leaves it absent rather than
defaulting (`envelope.zig:1219-1220`). An absent key reads as unknown and a
member that is *present* must be a string naming a known value: `optionalEnum`
returns `null` for a missing key and rejects a present `null` or an
unrecognised literal with `DecodeError.InvalidField`
(`zig/src/protocol/oap/envelope.zig:896-899`). That is the same
optional-but-not-nullable rule the three SDK readers now implement.

The publisher and the source are still different questions, and that
distinction is unchanged by any of this. A publisher that falls back to a
built-in catalog row is making a statement about *where the row came from*,
which is what `source` records; a publisher that omits `source` is saying it
did not establish that. Provenance for the loader's own rows continues to
live in `CatalogSnapshot` and is a separate concern from what a wire entry
publishes, which is why #696 carried it on its own rather than bundling it
here.

## Contract this follows

`schema/v0.1/provider.schema.json:261-269` requires only `model_ref`,
`model_id`, `provider_id` and `wire` from a published model entry, so
omitting `lifecycle` is already legal and no schema change is needed.

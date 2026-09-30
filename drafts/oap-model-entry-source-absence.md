# OAP model entry: absence of `source`

## What changed

`ModelEntry.source` is now `?ModelSource = null`. The encoder writes the
member only when it is set, and the decoder keeps an absent member absent.

Before this, `types.zig:437` defaulted the field to `.discovered` and
`envelope.zig:1220` decoded an omitted member as `orelse .discovered`. So a
wire that omitted `source` round-tripped into a published
`"source":"discovered"` the peer never sent — the peer was told the model
had been discovered when all it had said was nothing about its origin.

That matters more than the lifecycle case it parallels. `source` is the
provenance claim: a client reading `discovered` believes a live listing
produced the entry, which is the difference between a fact and a
conclusion. `schema/v0.1/provider.schema.json` requires only `model_ref`,
`model_id`, `provider_id` and `wire` from a published entry, so omission is
already legal and no schema change is needed.

## Serving behaviour: none for the built-in rows

`populateOapProviderCatalog` already sets `.source = .fallback` explicitly
on every built-in row. Those rows state their provenance, so this change
alters nothing about what they publish. It is the *unspecified* case that
changes: an entry that states nothing now publishes nothing.

## What this does not fix

**`lifecycle` is not touched here.** On this base it still defaults to
`.stable` at `types.zig:436`, and its correction is a separate change. The
two are adjacent lines, not one concern, and folding them together would
put a serving-behaviour change and a provenance change behind one review.

**The TypeScript reader is not fixed by this.** In
`sdk/typescript`, `oap_client.ts:412-417` maps the OAP wire into the native
`ModelDescriptor`, defaulting absent `lifecycle` to `stable` and absent
`source` to `dynamic`. `models_types.ts:43-54,87-107` requires both on the
native type. This Zig change therefore does not mean a TS client reads
these absences as unknown — it does not. Blanket-optionalising the native
descriptor would change a deliberately separated contract, which is an
owner decision, so the recommendation is an OAP-facing optional
representation in the SDK with the conversion made visible at the adaptor
boundary. That work is queued and not attempted here.

## Evidence

Three mutations, exit status captured as the raw process exit before any
output filtering, all under `test-unit-protocol`:

- encoder publishing `source` unconditionally → exit 1
- decoder defaulting an omission to `discovered` → exit 1
- encoder dropping a stated `source` → exit 1
- restored → exit 0

All three tests drive `serializeEnvelope` and re-decode its output. None
of them asserts an input string it built itself, which is the shape that
made two of #665's earlier tests worthless.

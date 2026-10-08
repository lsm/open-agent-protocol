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

## Serving behaviour: the served rows omit their source

`populateOapProviderCatalog` used to serve three built-in fallback rows marked
`.fallback`. It now serves the models the runtime loaded for each provider with
a key present (#355), and that list mixes live listings, cached copies of them,
models.dev data and endpoints' declared lists without recording which a model
came from. So those rows omit `source`: the runtime cannot say whether a live
listing produced them, and an absent member says exactly that.

## What this does not fix

**`lifecycle` was not touched by the change this document describes.** When
this document was written, `lifecycle` still defaulted to `.stable` at
`types.zig:436` on the base of the original `source` change, and its
correction was deliberately a separate review: the two are adjacent lines,
but folding them together would have put a serving-behaviour change and a
provenance change behind one review.

That separation held, and it is now history rather than a plan. #673 landed
the `source` half first; this document's companion change, the `lifecycle`
half, is what follows it. After both, both members are
`?ModelLifecycle = null` and `?ModelSource = null`, and the `lifecycle`
correction described in `drafts/oap-model-entry-absence.md` is #665.

**The SDK readers were decided separately, and they have now landed.** When
this was written, `sdk/typescript` still mapped the OAP wire into the native
`ModelDescriptor` with absent `lifecycle` becoming `stable` and absent
`source` becoming `dynamic`, and the recommendation here was an OAP-facing
optional representation in the SDK.

That recommendation was not adopted, and this section is corrected rather
than left as a superseded proposal. The owner chose the shared catalog result
carrying optional `lifecycle` and `source`, with **no separate OAP result
type** and no invented `stable` or `dynamic`. All three readers now implement
that: Rust in #688 and #709, Go in #690 and #710, TypeScript in #705 and
#712. Each distinguishes an absent key from a present value, records absence
as unknown, and refuses a present `null`, a wrong type or an unrecognised
literal as a malformed response.

The native protocol `ModelDescriptor` declared in
`docs/v1-sdk-agent-provider-spec.md` keeps its required members as a separate
contract; only the shared catalog result is optional, which is what the owner
decision selected.

## Evidence

Three mutations, exit status captured as the raw process exit before any
output filtering, all under `test-unit-protocol`:

- encoder publishing `source` unconditionally → exit 1
- decoder defaulting an omission to `discovered` → exit 1
- encoder dropping a stated `source` → exit 1
- restored → exit 0

None of the three asserts an input string it built itself, which is the
shape that made two of #665's earlier tests worthless. Their actual
coverage differs, and it is worth being exact:

1. **Absent source is not published** — decodes a response that states
   `source`, sets the entry's source to `null`, calls `serializeEnvelope`,
   and asserts the output has no `source` key. Serializer output only; it
   does not re-decode that output.
2. **A decoded absence stays absent** — decodes a payload with no `source`
   at all (the *initial* decode), asserts the entry's source is `null`,
   then re-encodes and asserts the output has no `source`. It does not
   decode the re-encoded output.
3. **A stated source is published and survives a decode** — decodes a
   response that states `source`, sets it to `fallback`, asserts
   `serializeEnvelope` emits `"source":"fallback"`, and then decodes that
   output again and requires `fallback` back. **Only this one re-decodes
   the serializer's output.**

So the output round-trip is covered by test 3 alone. Tests 1 and 2 pin the
encode and the initial-decode halves respectively. The mutation evidence
above is real and each mutation was run, but it is evidence for those three
distinct halves — not three re-decode round-trips.

## Reader behaviour across the SDKs: historical, then current

### Historical measurement — before the reader migrations

Measured on the `source` change's original base, which predates every reader
migration: #688, #690 and #705 for `source`, and #709, #710 and #712 for
`lifecycle`. **Kept as evidence of what the gap was**, not as a description of
any current tree; the row for every migrated SDK is now false.

| SDK | absent lifecycle | absent source | mode |
|---|---|---|---|
| TypeScript | invents `stable` (`oap_client.ts:412`) | invents `dynamic` (`:417`) | silent invention, reachable from the default OAP factory |
| Rust | decode error, `missing field lifecycle` | decode error, `missing field source` | hard fail for `list` and `resolve` |
| Go | empty string through a native enum (`oap.go:336-343`) | invents `dynamic` | invention plus an unrepresentable value |
| Python | invents `stable` (`_oap.py:237-246`) | invents `dynamic` | silent invention |

The Rust row was executed, not inspected: through the public
`ClientBuilder`/`oap-protocol-fake` process seam, a payload with `source`
and `lifecycle` present yields `lifecycle=Stable source=Dynamic` and
`lifecycle=Stable source=StaticFallback`, while removing either member
produced `OAP model entry is malformed: missing field \`lifecycle\`` /
`` `source` `` on both `list` and `resolve`. Two `resolve` cells for the
present shapes were unmeasured — a harness artefact, recorded rather than
papered over.

### The owner decision, and what landed

There is no longer an open question here, and this document previously said
otherwise. That was the error: it carried "no answer yet" and "the readers
must accept both shapes before it merges" in a section directly above one
describing the same decision as settled.

The owner chose **shared SDK catalog results carrying optional `lifecycle`
and `source`**, with no separate OAP result type and no invented default. An
absent member reads as unknown; a member that is present must be a string
naming a known value, so a present `null`, a wrong type or an unrecognised
literal is a malformed response. The native envelope contracts keep their own
required members as a separate concern.

Migrated and on main:

| SDK | `source` | `lifecycle` |
|---|---|---|
| Rust | #688 | #709 |
| Go | #690 | #710 |
| TypeScript | #705 | #712 |
| Python | #724 | #724 |

### Python: the gap, and what closed it

Audited on the base of the `source` change, Python had the same defect class in
**both** of its readers, and neither was fixed:

- `sdk/python/src/oap_sdk/_oap.py:244` — `lifecycle=item.get("lifecycle", "stable")`
  invents `stable` for an absent member, and a member present as `null`
  arrives as `None` rather than being refused.
- `sdk/python/src/oap_sdk/_oap.py:246` —
  `"static_fallback" if item.get("source") == "fallback" else "dynamic"`
  invents `dynamic` for an absent member **and** for any unrecognised literal,
  and it accepts the shared aliases `dynamic`/`static_fallback` on the wire
  where the `modelSource` enum permits only `discovered` and `fallback`.
- `sdk/python/src/oap_sdk/models.py:317,321` — the shared reader calls
  `_require_known` on both members, so an absent one is rejected outright
  rather than read as unknown.
- `sdk/python/src/oap_sdk/types.py:395,397` — both members are required on the
  shared descriptor.

The OAP reader's deprecation filter at `_oap.py:233` is already correct: it
compares against the literal `"deprecated"`, so an absent member does not
match and the model stays in the listing.

That audit is kept as the record of what the gap was. **Python is now
migrated in #724**, under the same absence-versus-present-null policy
as the three readers above: both members are optional on the shared
descriptor, both readers tell an absent key from a present value, and a
present `null`, wrong type or unrecognised literal is a malformed response
naming the field. Its sixteen cases cover both seams through a real fake
process each, including every direction of the deprecation filter on the OAP
side.

One asymmetry worth recording because it is easy to assume the wrong way: the
**shared** reader has no client-side deprecation filter at all. It forwards
`include_deprecated` to the server and keeps whatever comes back; the filter
exists only on the OAP seam. Its test asserts that it does *not* drop entries
locally, because asserting that it filters would be asserting behaviour it
never had.

All four SDK readers now implement the owner decision, so no reader is open.

**Validating a member is not the same as validating it before the reader's
filters run.** The OAP reader skips an entry whose `api`, `model_id`,
`lifecycle` or `auth_status` does not match the request. Validating only where
the descriptor is constructed means a row skipped by any of those filters is
never validated at all — `lifecycle="deprecated"` with a present `null` source
then returns an empty list instead of a malformed response. The owner contract
has no filter exemption: a present `null`, wrong type or unrecognised literal
is refused for **every** received entry, and only then may the reader skip it.
The Python change validates once per entry before any local `continue`, and a
negative control reinstates the old ordering to show the cases fail.

**Go had the same ordering and was the last SDK with it.** In
`ModelsService.oapList` (`go/sdk/oap.go:303`), which is where
`ModelsService.List` dispatches when the transport is OAP
(`go/sdk/models.go:142`), the four filters on `wire`, `model_id`, `lifecycle`
and `auth_status` ran before `oapModelSource` and `oapModelLifecycle`, so a row
any of those filters dropped was never validated: `lifecycle` stating
`deprecated` together with `source` stating `null` returned an empty list and no
error under `IncludeDeprecated: false`. Both members are now validated once per received
entry, before the first `continue`, and the deprecation filter reads the
validated value — which also needs a nil check, since `*lifecycle ==
LifecycleDeprecated` would panic on an absent member, the same reason Rust's
comparison needed an explicit `Some(...)`.

Rust and TypeScript were never affected, and in different shapes: Rust
deserialises the whole descriptor before its filter, and TypeScript's `map()`
validates before its `filter()`. Seven Go subtests cover the deprecated, api,
model_id and auth_status filters, each first proving with a valid row that its
filter really skips, then that a present `null` is refused anyway; the negative
control is main's own ordering and fails all seven.

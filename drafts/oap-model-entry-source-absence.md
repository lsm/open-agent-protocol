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

## Reader behaviour across the SDKs, and the unresolved choice

Measured or read on this base:

| SDK | absent lifecycle | absent source | mode |
|---|---|---|---|
| TypeScript | invents `stable` (`oap_client.ts:412`) | invents `dynamic` (`:417`) | silent invention, reachable from the default OAP factory |
| Rust | decode error, `missing field lifecycle` | decode error, `missing field source` | hard fail for `list` and `resolve` |
| Go | empty string through a native enum (`oap.go:336-343`) | invents `dynamic` | invention plus an unrepresentable value |
| Python | invents `stable` (`_oap.py:237-246`) | invents `dynamic` | silent invention |

The Rust row is executed, not inspected: through the public
`ClientBuilder`/`oap-protocol-fake` process seam, a payload with `source`
and `lifecycle` present yields `lifecycle=Stable source=Dynamic` and
`lifecycle=Stable source=StaticFallback`, while removing either member
produces `OAP model entry is malformed: missing field \`lifecycle\`` /
`` `source` `` on both `list` and `resolve`. Two `resolve` cells for the
present shapes are unmeasured — a harness artefact I did not fix, recorded
rather than papered over.

**This PR does not decide what any reader should do about absence.** The
owner has been asked to choose between shared SDK catalog results with
optional `lifecycle`/`source` and separate public OAP result/client types,
with the native wire keeping its validation either way. No answer yet, so
no reader change is proposed here: this PR makes the wire able to be sparse
truthfully, and the readers must be able to accept both shapes before it
merges. Until then this is a wire-side change with a known reader gap, and
that gap is deliberate rather than overlooked.

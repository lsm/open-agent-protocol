# Decision 0004: Extension Packs

Status: accepted 2026-09-16 (38 `extensions` fixtures; step 3 of the gate is
satisfied vacuously and deliberately — this unit specifies the protocol's own
extension seam rather than harness behaviour, so there is no native frame to
pin, and every adapter exercises it passively)
Date: 2026-09-15
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `extensions` (T0)
Extends: [Decision 0001](0001-agent-control-v0.1-executable-core.md) and
[Decision 0002](0002-admission-before-start.md) without amending either
Discipline: [Decision 0003](0003-staged-unit-graduation.md)
Design: [Staged Units Graduation Plan](../drafts/staged-units-graduation.md),
section "T0. Extension packs"

## Context

The protocol's premise is a small core, a set of units the spec graduates on
evidence, and room for anyone else to add their own surface. The first two were
designed; the third was asserted.

In practice `layer.features` is already an open map
(`schema/v0.1/capabilities.schema.json#/$defs/layer`), so a vendor can advertise
`com.example.storage.objects` as `native` today and the fail-closed gate already
treats it exactly like a core key. Advertisement needed no change at all. What a
vendor could not do was say what the key *means*: `manifest.schema.json` pinned
`schemas` at exactly seven core files and `validation.CompileSchemas` took no
parameters and read only the embedded directory. An extension bundle was not
unspecified, it was structurally forbidden — there was no way to add a schema to
a bundle and no way to compile one that included it.

A third-party surface was therefore *tolerated*, not *supported*. Its envelopes
reached the tolerant unknown-type path and were accepted because nobody could
say what they should look like. Nothing distinguished a well-formed vendor call
from a malformed one, which is the difference between an extension and an
unvalidated hole.

Decision 0003 put this unit first, before the five that mint the spec's own
keys, for two reasons. The tolerance step already changed what the validator
compiles, and packs change the same seam, so doing them together avoids
designing that seam twice. And minting ten new capability keys under no stated
namespace rule would make the unprefixed form the precedent a third party
copies; the rule is much cheaper to state before that than to retrofit after.

This is the one unit not gated on ledger evidence, and the exception is
deliberate rather than overlooked: the other six describe harness behaviour,
where a pinned ledger establishes that the behaviour is real. This one describes
the protocol's own extension seam, where the evidence is the seam's state, given
above.

## Decisions

### The unprefixed namespace is the spec's, in its entirety

The rule is stated the way round that leaves the existing vocabulary alone.
Capability keys, envelope `type` values, `error.response` codes, and validator
diagnostics that carry no prefix are spec-owned, whatever their shape:
`content.delta`, `user.input.requested` and `error.response` sit under no common
root, and `unsupported_feature` has no dots at all, so any rule built on a list
of permitted root segments would classify the protocol's own vocabulary as
invalid extensions. An extension name is one carrying a reverse-DNS prefix, as
the layered draft already requires of extension fields.

Enforcement has one home: a pack at load. Every name a pack declares must begin
with its own `id` followed by a dot. A pack cannot declare an unprefixed name,
so it cannot mint into the spec's namespace, and the core vocabulary grows only
by spec change. The validator never has to classify a name it meets on the wire:
a name matching a loaded pack's prefix is validated under that pack, a known
core name under core, and anything else takes the tolerant unknown path exactly
as before.

### The loaded set is prefix-free

The own-prefix rule alone does not make two packs non-overlapping, and
composition needs that separately: `com.example` and `com.example.storage` both
satisfy it while both legally claiming `com.example.storage.read`. No pack `id`
may equal or be a dot-prefix of another, and that is checked across the set
rather than per pack, since neither pack is at fault alone. Prefix-freedom makes
prefix matching a function rather than a search, so ownership is decided without
a precedence rule and a collision is a load refusal naming both ids rather than
a runtime tie-break.

### A pack declares what it defines

A pack is a `pack.json` descriptor plus its schema files and, optionally, its
own fixture manifest in the existing format. The descriptor declares
`capability_keys`, `envelope_types`, `error_codes`, and the `payload_members`
the pack adds to core payloads, rather than leaving them to be inferred from the
schemas, so containment is checked before anything is compiled.

Three declarations make the gate runnable generically rather than per pack:

- **`gates`** binds each declared request and event — and each member added to a
  core payload — to the capability key that must be advertised for it. Every one
  of them must appear or be declared ungated, stated rather than omitted, so a
  missing entry is a load refusal and not a silent hole. A `response` is
  excluded and a gated one is refused: it derives its gate from the request it
  answers, and an entry of its own would be a second, possibly disagreeing,
  source for the same gate.
- **`role`** — `request`, `response`, or `event` — is what lets the validator
  tell a packed request from a packed event, which both omit `in_reply_to` and
  which the format gives no naming convention to separate. They need opposite
  treatment under an unadvertised key: a request is permissible and is retained
  until its correlated response says whether the endpoint refused it, while an
  event or a success response is already the endpoint acting on a capability it
  does not have. A `response` names its request in `replies_to`, which is what
  correlation is.
- **`refusals`** enumerates, per request, the error codes the endpoint may
  answer it with for domain reasons. The honour rule is bounded by that list: a
  refusal under a declared code is a domain refusal the validator cannot
  evaluate and is conforming, and a refusal of a schema-valid packed request on
  an advertising endpoint under any other code is `unhonoured_capability`. The
  vendor is thereby made to enumerate its refusal reasons in advance, which is
  the disclosure move every other honour rule makes.

The stateful validator consumes all three at the dispatch point the core keys
use, so an extension capability is fail-closed in the same machinery as a core
one. That is the claim this unit makes.

### A pack adds vocabulary; it does not amend the protocol

Core validity never depends on a pack. For an envelope carrying only core
vocabulary, loading a pack changes nothing in either mode. For an envelope
carrying a loaded pack's members, core validity is the validity of its *core
projection* — the envelope with that pack's declared members removed — judged
against the core bundle in the mode in force.

Composition therefore patches no core branch. Patching would make an envelope
the core bundle rejects for an extra member valid the moment a pack is loaded,
and a regression guard that only re-ran the core fixtures with and without packs
would never see it. Validation is two passes over the same envelope instead: the
core pass judges the projection against the untouched bundle, and the pack pass
judges each declared member against its own subschema. A pack cannot touch
`required`, so it can make no core member optional and no new one mandatory, and
it cannot restate a member the core payload already defines — a pack that could
narrow `model_id` would change core validity through the door this unit closes
everywhere else.

A contributed branch must pin `type` to a `const` naming its own declared type,
and that is verified at load. The check is not bookkeeping: `oneOf` requires
exactly one match, so a branch broad enough to also match `run.started` would
make every `run.started` match twice and turn a core envelope invalid because a
pack was loaded. With the discriminator pinned and verified, branch selection is
a dispatch on `type` and the invariant holds by construction rather than by a
pack's good behaviour.

### A pack is a document someone else wrote

Pack compilation resolves references through a refusing loader restricted to the
resources actually registered: the core bundle, the pack's own documents, and
the packs it named in `depends_on`. A dependency is satisfied only by a loaded
pack of that exact id and version; ranges are not offered, since the loader
would then be choosing between schemas on a vendor's behalf and a mismatch
caught at load is the fail-closed outcome.

The refusing loader governs references, but the files the descriptor names are
read *before* any reference is resolved, so the same containment is owed to them
first: every `schemas` entry is a relative path, cleaned, joined to the pack
root, and — after resolving symlinks — still beneath it, or the pack fails to
load before a single file is opened. Otherwise a descriptor could name
`/etc/passwd`, `../../.ssh/id_rsa`, or a symlink out of the pack and have the
loader read it as a schema. The same check applies to a pack's fixture manifest.

### A load refusal is not a diagnostic

A pack that declares an unprefixed name fails before any trace is read, in a
phase that had no name and with no diagnostic, because the validator never ran.
The fixture manifest gains kind `load-invalid` with phase `load`: the entry's
`path` names a pack directory, and its `codes` come from a load-error vocabulary
kept deliberately apart from `diagnosticCodes()` — a load error is what the
loader says about a pack, and a diagnostic is what the validator says about a
trace.

For the same reason a pack contributes no diagnostics of its own. A pack ships
schemas and gate metadata, not code, so there is no mechanism by which it could
*produce* one, and admitting a pack-defined diagnostic into a manifest would
load an expectation nothing can ever emit. A pack's fixtures assert only what
the validator can actually say about packed content: a schema-phase failure on
the pack's own branch or member, the gate diagnostics `unavailable_capability`
and `unhonoured_capability` resolved through `gates`, and the load refusals.

### A pack's conformance claim is its own

The executable claim is unchanged for core and gains an independent term per
pack: `+ext:<pack id>/<pack version>`. A pack ships its fixtures in the existing
manifest format and they run under the same runner, so "conformant to a vendor's
extension" is a claim with the same executable meaning as `+queue` — stated by
the vendor, checked by the same tool, and carrying no authority over the core
claim beside it.

The two directions are kept apart deliberately: a pack's fixture may not claim a
core unit, and an `ext:` term is accepted only when a pack of that exact id and
version is loaded. Without that a pack could contribute evidence toward a core
unit, which is the one thing "a pack can never widen a core claim" has to mean.

Corpus completeness applies to a pack over its own `capability_keys`: a pack
whose corpus lacks a negative gate fixture and a negative honour fixture for any
key it declares fails to load. A vendor therefore cannot claim `+ext:`
conformance for a key that nothing could show it dishonouring, which is the same
protection core gets, extended to the surfaces this unit exists for.

## What this unit does not admit

- **Pack-supplied validation rules, and with them pack-defined diagnostics.** A
  pack ships schemas and gate metadata; it cannot ship a stateful rule. Whether
  a declared refusal was *warranted* — whether the object really was missing —
  is outside the seam and stays there.
- **A pack that changes core validity in any direction.** It cannot restate a
  core member, touch `required`, contribute an unpinned branch, or declare an
  unprefixed name. Each is a load refusal.
- **Version ranges in `depends_on`**, and any resolution the loader would have
  to perform on a vendor's behalf.
- **Two packs that nest.** A vendor subdividing its own namespace ships one pack
  declaring both families, not two packs whose ids prefix one another.
- **Advertisement changes.** `layer.features` was already an open map; this unit
  adds no envelope and no capability field.
- **Packed vocabulary in the core claim.** No core fixture claims an `ext:` term
  and no pack fixture claims a core unit.
- **A version bump.** Every addition is additive under the layered draft's
  compatibility rules: `version` stays `0.1`, the profile identifier is
  unchanged, and the seven-schema core bundle inventory is unchanged.

## Consequences

- `validation.CompileSchemas` keeps its signature and its meaning; the variant
  taking options carries both seam changes — the tolerance mode and the packs —
  as one API rather than two.
- The fixture manifest carries three new per-entry fields (`mode`, `packs`, and
  the `load-invalid` kind's pack `path`), and `extensions` joins the known unit
  list as a static addition, as every graduated unit makes. `ext:` terms are
  derived from the packs actually loaded rather than added to that list, so
  nothing is widened for a run that loads no packs — which is every core run.
- `unhonoured_capability` enters the diagnostic vocabulary here: an operation
  within every disclosed constraint refused on an endpoint that advertises the
  governing key, where no unit-specific rule names a defect. The later units
  reuse it; `models.list` and `action.tools.list` are the two core keys already
  known to need it.
- `oap validate` gains a repeatable `-pack`, documented beside `-mode`.
- No harness adapter changes. This unit describes the protocol's own seam, and
  every adapter passively gains the ability to be extended by one.
- The next unit (T1 run controls, Decision 0005) mints its four capability keys
  under a namespace rule that is now stated and checked, and against a gate the
  validator resolves generically.

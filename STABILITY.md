# Stability Commitment: Open Agent Protocol v0.1 Core

Status: in force
Date: 2026-09-17
Applies to: profile `open-agent-protocol.agent-control-core`, protocol version
`0.1`

This document is for people implementing OAP natively — writing an endpoint
that speaks these envelopes, rather than an adapter this repository maintains.
It says what will not change under you, what happens if it must, and what you
are owed before it does.

Every statement here is written so that a later change could be shown to have
broken it. Where a commitment cannot bind yet, it says so and names the
condition, rather than being softened into something unbreakable because it
promises nothing.

## What is covered

The **v0.1 core surface** is exactly three things:

1. The envelope types defined by `schema/v0.1/envelope.schema.json` — 41 of
   them today — together with the payload schemas they reference. Section 1
   says what is fixed about them, and on what condition the set may grow.
2. The lifecycle rules those envelopes obey, as decided in
   [Decision 0001](decisions/0001-agent-control-v0.1-executable-core.md) and
   [Decision 0002](decisions/0002-admission-before-start.md).
3. The Core Profile Requirements section of
   [the conformance draft](drafts/conformance.md), and in that same document
   the per-unit requirements section of every unit marked covered below. Those
   sections are covered despite living in a file headed `Status: draft`: the
   heading describes the document's prose, not the standing of requirements the
   validator already enforces.

Beyond the core, every conformance unit named in the conformance draft falls on
one side of this line or the other. None is left unstated:

| Unit | Covered | On what basis |
| --- | --- | --- |
| `+tools`, `+permissions`, `+user-input` | yes | Their requirements are part of the executable core frozen by Decision 0001, and `fixtures/manifest.json` carries fixtures under each name. |
| `+extensions` | yes | [Decision 0004](decisions/0004-extension-packs.md), accepted. |
| `+run-controls` | yes | [Decision 0005](decisions/0005-run-controls.md), accepted. |
| `+models` | yes | [Decision 0006](decisions/0006-models-catalog.md), accepted. |
| `+queue` | yes | [Decision 0007](decisions/0007-queue-delivery.md), accepted. |
| `+tool-sources` | yes | [Decision 0008](decisions/0008-tool-sources.md), accepted. |
| `+control-tools` | yes | [Decision 0011](decisions/0011-control-layer-provided-tools.md), accepted. |
| `+persistence` | **no** | Its envelope types — `session.list.*`, `transcript.load.*`, `transcript.delta` — exist in no schema file and no Go type. The conformance draft describes a unit that has never been built. |
| `+compound-open` | **no** | [Decision 0009](decisions/0009-compound-open.md) is `proposed`. A `proposed` record binds nothing. |
| `+steer`, `+btw` | **no** | Staged, not graduated. No decision has taken either through the gate. |

A unit joins the covered set when its graduating decision becomes `accepted`,
on the same terms and under the same additive rule. Until then it may change in
any way, including disappearing.

This document does not govern anything outside that surface. In particular it
does not govern the adapters, the Go and TypeScript clients, or the daemon.

## How decisions here become binding

A rule is binding when the decision record carrying it is `accepted`.
[Decision 0003](decisions/0003-staged-unit-graduation.md) defines what
`accepted` means and what evidence it requires: the rule is executable on
`main`, nothing inside the record is still pending, nothing proposed
contradicts it, and it is merged. Decisions 0001 through 0008 and Decision
0011 are accepted. Decisions 0009 and 0010 are `proposed`.

A `proposed` record binds nothing. If you are reading one, you are reading a
position this project may still change.

---

## 1. Existing envelopes never change, and the set grows only for units you have adopted

**Binding: now.**

Every envelope type in the bundle today keeps its name, its required members,
and its meaning for the life of `0.1`. None is removed or renamed.

The set itself is not closed, and saying it were would already be false rather
than merely restrictive: `models.request` and `models.response` entered the
core bundle with [Decision 0006](decisions/0006-models-catalog.md), and
`action.tools.list.*` with the tool catalog. A graduating unit may add envelope
types. What is committed is the condition under which it may:

- a new type arrives only as part of a conformance unit or an extension pack
  ([Decision 0004](decisions/0004-extension-packs.md)), never on its own;
- it is gated on a capability key, and an endpoint that does not advertise that
  key neither sends nor receives it.

An endpoint's own surface therefore does not grow when a unit graduates that it
has not adopted, and that — not a count of types — is the guarantee. The
reciprocal obligation falls on consumers: an envelope type you do not recognise
is treated exactly as an unrecognised capability key under section 3, ignored
rather than refused. `oap validate -mode tolerant` is that rule in executable
form, accepting another unit's envelope type on the common fields alone.

Within an existing type, these changes may happen in `0.1` and are not
breaking:

- adding an **optional** member to an existing payload, where its absence means
  exactly the behaviour that held before it existed;
- adding a capability key, a conformance unit, or a diagnostic code.

The worked shape for the first is an optional member whose omission carries a
positive meaning rather than "unknown". `ToolDefinition.source`, added by
[Decision 0008](decisions/0008-tool-sources.md), is one: it is absent from the
schema's required list, and a tool that omits it is unattributed — which is
exactly how every catalog read before sources existed. An optional addition
whose absence would instead leave a consumer unable to tell old behaviour from
unstated behaviour is not additive, and is treated as breaking.

These changes are **breaking**, will not be made in `0.1`, and require the
process in section 5:

- removing or renaming an envelope type, or changing which members it requires;
- removing a member, or making an optional member required;
- changing the type, meaning, or permitted values of an existing member;
- adding a value to a closed enumeration on an existing member, because the
  schemas are closed and a strict consumer rejects what it has not been told
  about;
- tightening validation so that a trace valid today becomes invalid.

## 2. Conformance is defined in writing, and artifacts answer it

**Binding: now.**

An endpoint is conformant to the core profile when it satisfies the Core
Profile Requirements in [the conformance draft](drafts/conformance.md) and its
traces pass both validation layers described there: structural validation
against the schema bundle, and stateful validation of the relationships across
an ordered trace.

The artifacts that answer the question are in this repository and you can run
them against your own endpoint:

```sh
go run ./cmd/oap validate --format=json your-trace.json    # judge a trace you supply
go run ./cmd/oap conformance --command "your-endpoint"     # drive your endpoint, then judge what it produced
```

`oap conformance` spawns your binary as a process, drives a scripted session
over the stdio binding in [drafts/endpoint-stdio.md](drafts/endpoint-stdio.md),
assembles what crossed the pipe into a trace, and hands that trace to the same
validator `oap validate` uses. Because it spawns a process rather than linking
a library, it does not care what language you wrote your endpoint in. `oap
endpoint` is a reference endpoint in this repository that it is developed
against, so the harness has a known-good target and you can see what one
conformant implementation looks like.

`fixtures/manifest.json` is the normative inventory. It declares for every
fixture whether it is valid, the phase it fails in if not, and the exact
diagnostic codes expected. Validity is never inferred from a filename.

The commitment that makes this load-bearing: **a trace the v0.1 validator
accepts today will be accepted by every later v0.1 validator.** Validation will
not be tightened within `0.1`. New rules land with new units, gated on
capabilities you do not have to advertise.

**The limits, stated rather than left to be discovered.** The harness drives
the stdio binding. An endpoint reached over another transport is still served
by `oap validate` on traces you collect yourself, and defining a binding for
that transport is work this repository has not done.

The harness walks the requirements above that a scripted session can reach,
and a passing report is not a proof that every one of them holds — it is the
absence of the failures the script can produce. A check it reports as skipped
is an obligation your endpoint does not carry, not one it failed.

Running the harness is not certification. You run it, you read the result, and
nothing here records that you passed, so conformance remains self-attested
against a shared validator. No corpus case in this repository was generated
from a third-party endpoint. Neither of these changes any rule above.

## 3. Capability keys are additive, and an unknown key is ignored

**Binding: now.**

Capability keys are only added. An existing key will not be removed within
`0.1`, and will not change meaning.

What to do with a key you do not recognise — stated here for the first time,
and normative:

- **Ignore it.** An unrecognised capability key is not an error. Neither an
  endpoint nor a control layer may refuse a request, fail a session, or close a
  connection because a key it does not know appeared.
- **Infer nothing from it.** An unrecognised key does not imply support,
  non-support, or any level for any feature, including features whose keys
  share a prefix with it.
- **Let it change nothing else.** Handling of the keys you do recognise must be
  identical whether or not unrecognised keys were present alongside them.
- **Do not require it to be understood.** An endpoint may not make correct
  handling of a request conditional on the control layer recognising a key,
  and a control layer may not make a capability response conditional on the
  endpoint recognising one.

This rule exists nowhere else in the repository today. It will be carried into
the core profile draft and enforced by the validator; until it is, this
document is where it is stated, and it binds from here.

## 4. Deprecation runs for at least three releases

**Binding: from the first tagged release.**

This repository has no tags and no published releases today. A window measured
in releases cannot start counting before there is one, and saying otherwise
would be the kind of sentence this document exists to avoid.

A **release** is a tagged commit on `main`, tagged `v0.1.N`, with notes naming
every change to the covered surface since the previous tag.

From the first such tag, the window is: a deprecation is announced in the notes
of release N and in the affected schema and decision records. The deprecated
surface keeps working, unchanged and valid, through releases N+1, N+2 and N+3.
It may be removed no earlier than the fourth release after the announcement,
and only through the process in section 5.

A deprecation is never announced and acted on in the same release.

## 5. A breaking change follows a named process

**Binding: from the first tagged release**, for the release-counted parts; the
round trip in section 6 binds now regardless.

There is no procedure by which a breaking change is made to `0.1` core. If one
becomes necessary, it is made by versioning, not by mutation:

1. A decision record proposes it, and must state why no additive path exists.
   "Additive is uglier" is not such a reason.
2. The round trip in section 6 completes, and the responses are recorded in
   that decision.
3. The change lands as a new core version, `0.2`, with its own schema bundle.
   The `schema/v0.1` bundle stays in the tree and keeps validating `0.1`
   traces.
4. The decision publishes the mapping between the two versions for every
   affected envelope.

**Who bears the migration cost.** Not you, on our schedule. An implementer who
does nothing remains conformant to `0.1`, and `0.1` remains a version this
repository validates and tests rather than a deprecated relic. We carry the
cost of keeping both bundles working and of writing the mapping. What you lose
by not moving is access to whatever `0.2` adds — not your conformance claim.

## 6. A breaking change requires a round trip with native implementers first

**Binding: now.**

No breaking change to `0.1` core lands until every native implementer on the
register below has been sent the proposal and given **at least 30 days** to
respond.

- The register lives at `IMPLEMENTERS.md` in this repository. Anyone shipping a
  native OAP endpoint may add themselves by pull request, with a contact route.
  Being on it costs nothing and commits you to nothing.
- The proposal goes out as a `proposed` decision record, linked from an issue,
  before it is implemented — not alongside an implementation waiting to merge.
- Every response received is recorded verbatim in the decision, including
  responses we disagree with, and the decision states what changed because of
  each.
- Silence is not consent, but it does not block: after 30 days the decision may
  proceed, recording who did not respond.

This does not give any implementer a veto. It gives you the guarantee that you
will find out before rather than after, and that what you say lands in the
record where the next reader sees it.

---

## What this does not cover

Stated plainly, so the covered surface stays meaningful:

- **Units not marked covered above.** A unit whose graduating decision is not
  `accepted` may change in any way, including disappearing. That covers both
  the staged units in [the graduation plan](drafts/staged-units-graduation.md)
  and `+persistence`, which the conformance draft describes but nothing in this
  repository implements.
- **Draft documents.** Everything in `drafts/` other than the requirement
  sections named under "What is covered" is working material.
- **The adapters.** They track third-party harnesses at pinned commits and
  change when those harnesses do. They are evidence that the protocol is
  implementable, not part of the protocol.
- **The Go and TypeScript clients, the `serve` package, and the daemon.**
  These are implementations of the wire, and their APIs may change. The wire
  they speak is what is covered.
- **Diagnostic code strings**, beyond the commitment that a trace valid today
  stays valid. Codes may be added, and their messages may be reworded.

## Holding us to this

Every commitment above is meant to be checkable against the tree rather than
against intent. If you believe one has been broken, open an issue citing the
commitment by section number and the fixture, schema file, or decision record
that shows it. A commitment that turns out to have been broken is a defect to
be fixed or a version bump that should have happened, not a matter of
interpretation.

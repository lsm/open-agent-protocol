# Decision 0012: Retire `+persistence`, and Stage Transcript Load

Status: proposed
Date: 2026-09-17
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `persistence` (claim term `+persistence`), retired; `transcript-load`
(claim term `+transcript-load`), staged
Amends: [the conformance draft](../drafts/conformance.md), by retiring a unit
it describes and naming a narrower one in its place. Amends no accepted
decision: `+persistence` was never taken through
[Decision 0003](0003-staged-unit-graduation.md)'s gate
Gated by: [Decision 0003](0003-staged-unit-graduation.md)

## Context

`drafts/conformance.md` has listed `+persistence` since the first draft. It
names three envelope types — `session.list.*`, `transcript.load.*`,
`transcript.delta` — and seven requirements an implementation must meet to
claim it.

None of those types exists. Not in `schema/v0.1/envelope.schema.json`, not in
`protocol/`, not in the validator, not in a fixture, not in any adapter. The
unit describes behaviour nothing in this repository has ever implemented.

That matters because people read it. A maintainer evaluating a native
implementation reads the unit list to find out where session history goes, and
finds a unit that cannot be implemented, because there is nothing to implement
against. Leaving the listing up while nothing backs it is the fault this
project criticises elsewhere: a document that describes a thing rather than a
thing that exists.

**A first version of this decision retired the unit outright, and its evidence
paragraph was wrong.** It said "no adapter ledger in `research/` records a
persistence mismatch, because no pinned harness has durable sessions... the
Claude, ACP, Codex, DeepSeek, Hermes, OpenCode and Pi ledgers say the same or
less." That claim came from grepping the tree for the three envelope type
names and finding nothing, which establishes that OAP has no persistence
vocabulary and says nothing whatever about what the harnesses have. Reading the
ledgers for the capability rather than the spelling gives the opposite answer
for one of them, and that one is enough to change what this decision should do.

## Decisions

### `+persistence` is retired

The unit is removed from `drafts/conformance.md`: from the additive unit list,
from the example claims, and its requirements section deleted. No endpoint may
claim `+persistence` against v0.1.

It is retired rather than deferred because it was never one unit. It bundled
three capabilities with three different evidence positions, and a claim term
covering all three could not be earned by an endpoint that has one of them.
Splitting it is what lets the part with evidence proceed and the parts without
it wait.

### `transcript-load` is staged, as one envelope pair

`transcript.load.request` / `transcript.load.response`: a cursor-shaped read of
a session's persisted entries. It enters
[the graduation plan](../drafts/staged-units-graduation.md) as a staged unit
and graduates through the ordinary gate — reference execution, validator rules
and fixtures, native evidence, decision record — like `+queue`, `+models`,
`+tool-sources` and `+control-tools` before it.

It is staged and not decided here. This record establishes that the capability
has native evidence and a shape worth designing; it does not design it. The
open questions are named under Consequences so the unit's own decision has to
answer them rather than inherit them.

### `session.list` and `transcript.delta` are not staged

Neither has native evidence in any pinned ledger, and the reason differs.

**`session.list`** — enumerating the sessions an endpoint holds — is a
capability no pinned harness exposes to a client over its control wire. Pi has
a durable session store and discovers sessions from it, but that is the
harness reading its own disk, not a client asking the harness what it holds.

**`transcript.delta`** — live persisted-row sync — is specifically contradicted.
Pi has the closest thing to it, `entry_appended`, and its ledger records that
the event "is not a comprehensive feed of those writes": it is visibly emitted
for extension custom-entry writes and not for every persisted entry. An endpoint
implementing `transcript.delta` from it would advertise a sync that silently
misses rows.

Both may be proposed later by a decision that brings evidence. Neither is
staged on the strength of the old bundle.

## Evidence

**Pi is the counter-example the first version missed.**
`research/pi-v0.85.1-mapping.md` records, in its session state and recovery
section:

> `get_entries` supports `since` — a genuine cursor-shaped transcript
> reconstruction primitive, but reconstruction, not event replay: there is
> no redelivery contract for the live stream.

Its persisted entries carry stable `id`, `parentId` and `timestamp`; the
adapter's `sessionFile` is a durable store; and the ledger classifies
"transcript reconstruction" as `unavailable` **at the OAP boundary despite
native codec evidence** — which is precisely a mismatch recorded for want of
protocol vocabulary, the thing the first version said no ledger recorded.

The distinction the ledger draws is the one this decision adopts.
Reconstruction is a read of what was persisted; replay is redelivery of a live
stream. OAP already has the second — resume, reconciliation and the bounded
journal, all core under Decision 0001 — and has never had the first.

Other harnesses corroborate the shape without matching the primitive. ACP,
Claude and OpenCode ledgers all discuss transcripts; none exposes a cursored
read of persisted entries over its control wire at its pin. One native
implementation is enough to stage a unit and not enough to graduate it, which
is why this stages it.

**The absence of the vocabulary is still checkable:**

```sh
grep -rl "session.list\|transcript.load\|transcript.delta" schema/ protocol/ validation/ fixtures/
```

returns nothing.

## Consequences

`STABILITY.md`'s verdict for `+persistence` does not change — it was already
**no** — but the row now points at a retirement rather than at a unit awaiting
graduation, and the uncovered list names the capabilities instead of the term.

A host that reads the conformance draft to find out whether it can read an
endpoint's transcript gets an answer: not in v0.1 core, staged as
`transcript-load`, and available today only as an extension pack
([Decision 0004](0004-extension-packs.md)) for an endpoint that really has it.

**Four questions `transcript-load`'s decision must answer**, recorded here so
they are inherited as questions rather than as assumptions:

1. What the cursor is. Pi's `since` takes an entry id from a tree, not a
   sequence; OAP's run sequences are per-run and restart, and its transcript
   cursor on `session.state` is an opaque string. A unit that conflates them
   would be unimplementable on either side.
2. What an entry is. Pi's entries include `branch_summary` and `custom_message`
   kinds with `parentId` links — a tree, not a list. Core OAP has no tree
   semantics, so the unit must either flatten, expose the parent links, or
   refuse sessions that branch.
3. Whether the read is bounded, and what a truncated read says. Every other
   recovery surface in v0.1 reports its own limits rather than returning a
   quiet prefix — `*adapter.ReplayGap` over a cursor, `complete` on a bounded
   list — and a transcript read should be held to the same standard.
4. What it means for a harness with no durable store. Most pinned harnesses
   have none, so the unit must be declinable in the ordinary way, with the
   capability key ungated and the refusal typed.

## What this decision does not admit

That reattachment, reconciliation or replay are outside v0.1. They are core,
Decision 0001 defines them, and this record does not touch them. Pi's own
`resume` and bounded journal keep working exactly as they do.

That durable sessions are outside OAP's scope. This says the vocabulary is not
in `0.1` core today and that the three capabilities the old unit bundled have
to be earned separately.

That `transcript-load` is approved. Staging is a place in a queue, not a
verdict. If its decision cannot answer the four questions above, it does not
graduate.

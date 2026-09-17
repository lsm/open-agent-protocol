# Decision 0015: Graduation Evidence Comes From Implementations This Project Does Not Control

Status: proposed
Date: 2026-09-17
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Amends: [Decision 0003](0003-staged-unit-graduation.md), which is accepted.
It clarifies what step 3 of the gate has always required and makes one
implicit word explicit; it relaxes nothing and no graduated unit is
reassessed under it
Gated by: [Decision 0003](0003-staged-unit-graduation.md)

## Context

Step 3 of the gate reads: "At least one native adapter executes the unit
through its production codec and reducer, with a corpus case pinned to the
adapter's existing ledger commit."

It says *adapter* and it never says *whose*. The omission has cost nothing so
far because every native adapter in this repository wraps a third-party
harness at a pinned commit, so "adapter" and "an implementation we do not
control" have been the same set. Pinning is the tell: you pin an upstream
precisely because it is not yours to change.

That equivalence is an accident of who has implemented OAP, not a property of
the sentence. Two things break it. A native OAP *endpoint* is not an adapter
and has no ledger commit to pin, so step 3's wording does not obviously cover
one. And an implementation maintained by this project — a first-party SDK, a
reference endpoint grown into a product — would satisfy a literal reading of
"native adapter executes the unit" while supplying none of the assurance the
step exists to give.

## Decisions

### Step 3 evidence must come from an implementation this project does not control

A unit graduates on evidence from an implementation whose behaviour this
project cannot change to suit the protocol. Third-party harness adapters at a
pinned commit are the canonical form and remain so. A native endpoint
maintained by someone else qualifies on the same terms, with its own provenance
in place of a ledger commit.

The reference adapter does not, and never did: that is step 1, and it is a
separate step precisely because a deterministic implementation we write proves
the wire shape is executable and nothing about whether it fits reality.

### An implementation this project maintains cannot be a unit's only evidence

If the only implementation that executes a unit is one this project controls,
the unit does not graduate. Not as a warning, not as a caveat in the record —
it stays staged.

This is the load-bearing clause and it is deliberately blunt. A softer form
("prefer independent evidence", "note the limitation") fails at the moment it
matters, because by the time anyone is weighing it someone has already written
the decision and the cost of not accepting it is a rewrite. A rule that only
binds when it is cheap does not bind.

### An absence is evidence only when it is suppression, not disinterest

"Nobody else has this" is available as an argument in both directions, and
this decision makes the wrong one easier to reach, so the test goes here.

`+control-tools` graduated partly on absence: one implementation, integrated
two ways, declared empty tool catalogues *because* the protocol had nowhere to
put a caller-executed tool. Makai's adapter marshals `"tools": []any{}`
(`adapter/makai/session.go:179` at `fd7a392`) and Makai's native OAP mode
declares empty in both payloads. That is suppression, and it is pointable — the
empty
array is in the source, and removing the wall changed the behaviour. An
absence of that kind is evidence the capability is wanted and blocked.

The other kind is disinterest: one implementation wants a capability and the
rest have not asked for it. That is evidence about the one, not about the gap.
It may still be a real need and a later harness may prove it, but it cannot
carry a unit on its own, and reading it as suppression is how a protocol
acquires features its implementers did not ask for.

**The discriminator is an artifact, not a missing capability.** Both kinds look
identical from the outside — the capability is not there either way — so the
test is whether the integration left something behind showing it *declined*.
`"tools": []any{}` is such an artifact: something considered the capability,
concluded it could not be carried, and wrote the conclusion into the wire. So
is a typed refusal, or a branch that fails the run with a named reason. Each is
pointable, and each changes when the wall comes down.

An unmodelled namespace is not. The pinned Makai adapter carries no
credential path — not a refusal, not an empty structure, nothing — while the
harness it adapts hosts a full auth protocol on its own identity domain. That
looks like a wall until you notice there is no artifact at it, and silence is
what disinterest looks like too. An integration that never modelled a
capability is indistinguishable from one that never got to it.

So a record claiming suppression must cite the artifact. "The integration does
not do X" is not the claim; "the integration wrote down that it would not do X,
here, and this is the line" is.

### The asymmetry is written down rather than held as practice

This project has followed the rule without stating it. `+control-tools`
graduated on Makai, `+queue` and `+models` on OpenCode, steer is proposed on
Pi, Hermes and OpenCode; no unit has ever graduated on the memory adapter
alone. Writing it down costs nothing today and is the only form that survives
the people currently applying it.

## Evidence

Every accepted unit already meets it. Decisions 0005 through 0008 and 0011 each
cite at least one third-party adapter with a pinned corpus case, and Decision
0011's own acceptance was held until the Makai adapter executed it — the
reference adapter implementing the unit was explicitly not enough. It was
assessed and held on 2026-09-17 for want of exactly that, and accepted the same
day once the adapter and the `tool-bridge-roundtrip` case landed.

Two apparent exceptions prove the rule's shape rather than breaking it.
Decision 0004 records "No harness adapter changes" and Decision 0009's step 3
is satisfied vacuously, because extension packs and compound open both specify
the protocol's own seam and produce no harness frame to pin. Those are units
with no native surface to evidence, not units evidenced by ourselves, and this
decision does not disturb either.

The gap this closes is prospective. No unit has graduated on first-party
evidence, which is why the wording has never been tested.

**Why the rule is about structure and not about care.** Writing
[Decision 0017](0017-provider-provisioning.md) produced an unplanned test of
this. Four claims in successive drafts were wrong — a credential channel left
open, a scope assumption imported from a surface that did not share it, a
predicate that flagged the protocol's most-used selector — and each was found by
a maintainer checking it against a tree this project does not have. None was a
careless claim, and all four were held with the same confidence as the claims
that were right. That is the point: a boundary specified from one side reads as
finished exactly when it is least finished, and the author's confidence does not
track the difference. Step 3 does not ask for an outside implementer because
outsiders are more careful. It asks because the failure mode is structural, and
no amount of care inside one repository detects it.

## Consequences

A unit whose only implementation is ours stays staged however complete its
schema, validator rules and fixtures are. That is a real cost, paid on
purpose: it means a capability this project wants and nobody else has built
waits for someone else to build it.

The eight pinned adapters change character. They have been evidence that the
protocol is implementable; they become the mechanism that keeps it honest about
implementations it does not own, and retiring one is a governance decision
rather than a maintenance one.

`STABILITY.md` section 2's limit gets sharper. It already says conformance is
self-attested because no harness here drives a third-party endpoint. This adds
the converse: an implementation this project maintains passing its own
conformance harness is necessary and is not independent evidence, and the two
must not be conflated in a decision record.

## What this decision does not admit

That first-party implementations are unwelcome or second-class. They are how
the wire gets exercised, and step 1 requires one. What they cannot be is the
sole justification for freezing a shape.

That existing graduations are reopened. Every one already meets this; none is
reassessed.

That a unit can be blocked indefinitely by the absence of a willing third
party. A unit that no one else will implement is telling the project something
about the unit, and the answer is to find out what rather than to lower the
bar.

# Decision 0018: Makai Becomes First-Party, And What That Costs

Status: proposed
Date: 2026-09-17
Protocol: `open-agent-protocol` version `0.1`
Profiles: affects the evidence standing of both
`open-agent-protocol.agent-control-core` and
`open-agent-protocol.model-provider-core`
Amends: nothing. It applies
[Decision 0015](0015-evidence-from-implementations-we-do-not-control.md)
to a change of fact, and records a release-sequencing constraint
Gated by: [Decision 0003](0003-staged-unit-graduation.md)

## Context

Makai becomes the OAP SDK. It will implement
[`model-provider-core`](../drafts/model-provider-core.md) as its only inference
wire — its native provider protocol is deleted rather than bridged — and
`v0.1.0` is not tagged until that profile lands.

Both are the project owner's direction, confirmed 2026-09-17.

This is good for the protocol and expensive for its evidence rule, and the
expense has to be written down at the moment it is incurred rather than
discovered later by someone trying to graduate a unit.

## Decisions

### Makai is first-party from the moment that direction takes effect

[Decision 0015](0015-evidence-from-implementations-we-do-not-control.md) defines
step-3 evidence as coming from "an implementation whose behaviour this project
cannot change to suit the protocol." That is the operative test, and it is not
about repositories, organisations or who writes the commits. It is about whether
a disagreement between the protocol and the implementation gets resolved by
changing the protocol or by changing the implementation.

For every pinned harness the answer is the second: the adapter bends, the
harness does not. For the OAP SDK the answer is the first. So Makai is
first-party under 0015, and stops being able to carry a unit alone.

[Decision 0016](0016-model-provider-profile.md) anticipated exactly this and
said so before it happened — "Makai doing so is the expected first case and is
not sufficient on its own if Makai becomes first-party." That sentence is now
load-bearing rather than cautionary.

### Graduated units are not reassessed, and the reason is not convenience

`+control-tools` graduated on the Makai adapter as third-party evidence.
Decision 0015's own header says no graduated unit is reassessed under it, and
that holds here.

The reason is that **evidence is evaluated at the moment of graduation, with the
standing that held then.** When Decision 0011 was assessed and held on
2026-09-17, and accepted the same day once the adapter executed the unit, Makai
was an implementation this project did not control, and the evidence did the
work the rule asks of it: it established that the wire fit something we could
not bend. A later change in who maintains that implementation does not reach
back and un-establish it.

The alternative — re-deriving every past graduation whenever an implementer's
relationship changes — would make the gate depend on facts that move after the
fact, and would mean no unit was ever finally graduated.

### `model-provider-core` has no prospective third-party implementer today

This is the cost, stated plainly rather than softened.

Eight pinned harnesses give this project third-party evidence at the
agent-control boundary. **None of them speaks the provider profile, and none is
proposed to.** The only implementation expected to speak it is the one that just
became first-party.

So under 0015 the provider profile cannot graduate on the evidence it is
going to have, and it will not be able to for as long as Makai is the only
implementation. That is not a defect in 0015. It is 0015 working: a protocol
written and implemented by the same party has no outside check, and the rule
exists to stop that from being invisible.

What this project owes the profile in exchange is that the draft be good enough
for a second implementer to adopt without negotiating with us — which is the
argument for specifying it fully now, while the one implementation is motivated
to be precise, rather than after.

### `v0.1.0` waits for `model-provider-core` to be implementable, not graduated

The two constraints above would deadlock if read together carelessly: the tag
waits for the profile, the profile cannot graduate without a second
implementation, and no second implementation exists.

They do not deadlock, because they are gates on different things.

`v0.1.0` puts [the stability commitment](../STABILITY.md) sections 4 and 5 in
force for `agent-control-core`. What it waits for is that the provider profile
be **specified and implementable** — a draft, schemas, and an implementation
that speaks it — so that the release is not tagged against a layering with a
named hole in it. It does not wait for the profile to graduate under Decision
0003's step 3, which requires something nobody has yet.

Stated as the condition rather than the intent: `v0.1.0` may be tagged when
`model-provider-core` has a draft, schemas in `schema/`, and at least one
implementation that speaks it end to end. The profile remains proposed at that
point, and the stability commitment does not extend to it — the commitment names
`agent-control-core` and continues to.

## Evidence

**The direction is the project owner's**, confirmed 2026-09-17, and is not a
claim about any tree. The technical facts about Makai's current tree cited in
[the model-provider-core draft](../drafts/model-provider-core.md) were read from
that tree on the same date and remain as they stand there: `makai --oap` serves
agent control only, its other mode speaks its own native wire, and no mode
speaks the provider profile. Nothing in this record asserts that the change has
already happened in code.

**The first-party test is 0015's own words**, not a new standard invented here.
This record applies an existing rule to a changed fact.

**The condition has since arrived, and behaved as this record predicts.** An
implementation now speaks [`model-provider-core`](../drafts/model-provider-core.md)
end to end — `lsm/makai#341`. Under
[Decision 0016](0016-model-provider-profile.md)'s wording that was the event
that would make the profile executable, and it does not, because it is the
implementation this record makes first-party. It establishes implementable and
not right. The profile's own evidence section says so, and says which parts the
implementation is not evidence for at all.

**The absence of a second implementer is checkable.** `research/` carries a
mapping ledger per pinned harness and none of the eight records a provider-
profile surface, because the profile did not exist when they were written.

## Consequences

The provider profile is specified first-party and graduates later or not at all.
Every record about it must say so, and
[the model-provider-core draft](../drafts/model-provider-core.md) already does.

Recruiting a second provider-profile implementation becomes a real project goal
rather than a thing that might happen. It is the only route by which that
profile graduates.

`adapter/makai/` loses its purpose in both directions: it is not needed once
Makai speaks OAP natively, and it is not third-party evidence once Makai is
first-party. Freezing it — keeping the pinned corpus as the historical record of
what `+control-tools` graduated on — preserves the evidence for the graduation
that already happened without implying it can carry another. The disposition
itself is the project owner's and is not taken here.

The design pressure on this repository changes. While Makai was an outside
implementation, a disagreement between draft and tree was information. Once it
is the SDK, the same disagreement can be resolved by changing either side, and
the discipline that produced four corrections to the provider draft in one day
stops being structural and becomes a habit someone has to keep.

## What this decision does not admit

That Decision 0015 should be relaxed because it has become inconvenient. It
became inconvenient the first time it bound anything, which is the test it was
written to pass.

That `+control-tools`, `+queue`, `+models` or any other graduated unit is
reopened.

That `agent-control-core`'s stability commitment extends to
`model-provider-core`. It names one profile and continues to name one.

That Makai's reports about its own tree stop being useful. They remain design
input of exactly the standing they always had — read from a tree, not pinned
here, not step-3 evidence. What changes is that they can no longer become step-3
evidence later.

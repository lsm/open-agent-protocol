# Decision 0012: Persistence Is Not In v0.1 Core

Status: proposed
Date: 2026-09-17
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `persistence` (claim term `+persistence`)
Amends: [the conformance draft](../drafts/conformance.md), by retiring a unit
it describes. Amends no accepted decision: `+persistence` was never taken
through [Decision 0003](0003-staged-unit-graduation.md)'s gate, so nothing
accepted rests on it
Gated by: [Decision 0003](0003-staged-unit-graduation.md)

## Context

`drafts/conformance.md` has listed `+persistence` since the first draft. It
names three envelope types — `session.list.*`, `transcript.load.*`,
`transcript.delta` — and seven requirements an implementation must meet to
claim it.

None of those types exists. Not in `schema/v0.1/envelope.schema.json`, not in
`protocol/`, not in the validator, not in a fixture, not in any adapter. The
unit describes behaviour nothing in this repository has ever implemented, and
`STABILITY.md` already says so in the row that excludes it.

That would be a harmless piece of aspiration if nobody were reading it. Someone
is. The Makai adapter maps a harness with session resume — `agent_start`
carries `resume_session_id`, a permanent alias for `session_id` since their
#198 — and a maintainer evaluating a native implementation reads the unit list
to find out where resume goes. The answer today is that the unit which appears
to cover it cannot be implemented, because there is nothing to implement
against.

Leaving the listing in place while nothing backs it is the fault this project
has criticised elsewhere: a document that describes a thing rather than a thing
that exists.

## Decisions

### `+persistence` is retired from the unit list

The unit is removed from `drafts/conformance.md`: from the additive unit list,
from the example claims, and its requirements section deleted. No endpoint may
claim `+persistence` against v0.1, because the claim has no testable content.

Retiring is not deferring. A deferred unit is one with a design, a place in the
graduation plan, and an order; `+persistence` has a paragraph. Removing it says
what is true — v0.1 core has no persistence vocabulary — instead of implying a
plan that does not exist.

### Reattaching to a session is not persistence, and already works

The three recovery mechanisms Decision 0001 defines — resume, reconciliation,
replay — are core and unaffected. An endpoint that can reattach a host to a
running session, answer `session.state.request` with authoritative state, and
serve a journal suffix from a cursor is doing everything v0.1 asks. Makai's
adapter does all three today at `degraded`, and says so in its descriptor.

What Makai's `resume_session_id` reaches is narrower than it looks: it is an
association key on `agent_start`, and the adapter already normalizes it onto
`session.open`. Opening with a session id the endpoint has seen before is the
surface, and it needs no new envelope.

What v0.1 has no vocabulary for is the other half: **enumerating** sessions an
endpoint holds, and **loading a transcript** for one the host has no journal
of. Those are the two things `session.list` and `transcript.load` were
gesturing at, and they are the two things this decision declines to invent
without evidence.

### The path back is the ordinary gate, not this record

If durable sessions become a unit, they arrive the way `+queue`, `+models`,
`+tool-sources` and `+control-tools` did: a decision record with native
evidence from at least one pinned harness, a reference execution in
`adapter/memory.go`, validator rules, and fixtures covering the failure modes
rather than a round trip. Until a harness in this repository has durable
sessions worth mapping, there is nothing for such a record to be evidence of.

An implementation that needs listing or transcript loading before then has the
extension pack seam ([Decision 0004](0004-extension-packs.md)), which exists
for exactly this: a capability an endpoint really has, named in its own
namespace, without the core pretending to standardize it.

## Evidence

The absence is checkable rather than asserted:

```sh
grep -rl "session.list\|transcript.load\|transcript.delta" schema/ protocol/ validation/ fixtures/
```

returns nothing. The three types appear only in `drafts/conformance.md`, in
`STABILITY.md`'s row excluding them, and in
`drafts/staged-units-graduation.md`'s inventory.

No adapter ledger in `research/` records a persistence mismatch, because no
pinned harness has durable sessions. Makai's ledger classifies "load, resume,
replay, cross-process recovery" as unavailable and its journal as bounded
process memory; the Claude, ACP, Codex, DeepSeek, Hermes, OpenCode and Pi
ledgers say the same or less. A unit with no native evidence anywhere is one
that could not pass step 3 of the gate if it were proposed today.

## Consequences

`STABILITY.md`'s unit table loses a row rather than changing its verdict: the
answer for `+persistence` was already **no**, and is now "there is no such
unit". The covered surface does not move, and no trace valid today becomes
invalid.

A host that reads the conformance draft to find out whether it can list an
endpoint's sessions now gets an answer instead of a requirements section it
cannot test an endpoint against.

Nothing is lost that anyone had. There is no implementation to strand, no
fixture to delete, and no endpoint that has claimed the unit — retiring it
costs exactly the paragraph it removes.

## What this decision does not admit

That reattachment, reconciliation or replay are outside v0.1. They are core,
Decision 0001 defines them, and this record does not touch them.

That durable sessions are outside OAP's scope permanently. This says the
vocabulary is not in `0.1` core and that inventing it without evidence is the
wrong order, not that the question is closed.

That an endpoint may not persist anything. What an endpoint stores is its own
business; what this decision is about is whether v0.1 core gives a host a
standard way to ask about it, and it does not.

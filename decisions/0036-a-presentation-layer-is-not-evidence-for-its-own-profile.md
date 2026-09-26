# Decision 0036: A Presentation Layer Is Not Evidence for Its Own Profile

Status: proposed
Date: 2026-09-26
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.presentation-control`
Extends: [Decision 0003](0003-staged-unit-graduation.md), whose one gate governs
staged units inside a profile and does not reach a profile of its own
Gated by: [Decision 0032](0032-go-and-zig-are-peers.md), for the parity rule
below, and [Decision 0015](0015-evidence-from-implementations-we-do-not-control.md),
for what step 3 asks of the profile
Design: [Presentation Control Profile](../drafts/presentation-control-profile.md)

## Context

The repository has written the boundary a human interface crosses, and has not
implemented it. `drafts/presentation-control-profile.md` specifies targets,
snapshots, typed revision-based updates, semantic intent and affordances. It has
one illustrative example in `examples/presentation-control-session.json`, no schema
in `schema/v0.1/`, no trace in `fixtures/manifest.json`, no entry in
`STABILITY.md`, and no implementation in either tree. That much is a known state.

What makes it a decision rather than a backlog item is that the repository also
has the presentation layer, and it does not speak the profile.
`zig/src/tui/` is about 20,200 lines and imports `agent`, `tools/registry`,
`permission`, `oauth/storage`, `custom_providers` and `model_catalog` directly,
in-process. It never references `protocol/oap`. By the architecture in
`CLAUDE.md` it is a *host* above the agent layer, not a presentation layer
across a control boundary, so the profile has no reference implementation, no
conformance evidence, and no differential peer. Searching `zig/src/tui/` for
`delivery`, `admission`, `unsupported_feature`, `allow_degraded` or `capabilit`
returns one hit, a string literal in a fake-MCP-server fixture: the TUI path
carries none of the fail-closed discipline the decisions spend their length on,
because there is no control layer under it to carry any.

The draft and that state model are nonetheless the same shape. The minimum
session snapshot is `session`, `timeline`, `composer`, `affordances`,
`pending_prompts`, `diagnostics`. `AppState` at `zig/src/tui/state.zig:443` is
`sessions`, `transcript`, `composer`, `approval`, `status`, `tools`,
`permission_mode`, `queue`, `telemetry`. The profile reads as reverse-engineered
from this state and never checked back against it, which is the specific reason
it cannot be reviewed: there is no second reading of the draft to compare.

None of this makes the TUI wrong. The composition rules in `README.md` say OAP
layers are logical boundaries, not deployment sides, so embedding the layers
below the presentation one in one process is legal, and it stays legal. The gap
is narrower and worth stating exactly: the boundary this repository crosses most
in production is the one it has specified least.

Two holes in the draft are already load-bearing for that crossing, and both are
visible by reading the one implementation in this repository against the draft
that describes it.

**There is no epoch.** Revisions are monotonic integers scoped to one target (the
draft's *Presentation Updates* section), and a receiver that does not hold
`base_revision` must discard the changes and request a fresh snapshot. Nothing
distinguishes a receiver that is ahead because it persisted across a control-layer
restart from a control layer whose revision regressed, so a presentation client
that stores state cannot converge after a restart — it re-snapshots, and the next
update mismatches again. `tui/session_store.zig` is 1,642 lines of exactly that
persistence, and it answers the question internally because the wire has nowhere to
put the answer.

**`pending_prompts` and `affordances` are both control-owned and never
reconciled.** `resolve_permission` and `resolve_user_input` are affordance kinds
(the draft's *Affordance* section) and `pending_prompts` is its own field in the
minimum session target state (*Snapshot Shape*). Nothing says which is
authoritative when a prompt exists and its affordance is absent, disabled, or
disagrees with it, and the draft does not say they must agree. `AppState` splits
the same state the same way — `approval` against `tools` and `permission_mode` —
so this ambiguity is live, not hypothetical.

## Decisions

### The gate for a profile is not the gate for a unit

Decision 0003's four steps are unit-shaped: reference execution, validator
rules and fixtures, native evidence, decision record. A profile has no unit to
graduate, so it gets the same four steps with the seams they were drawn for:

1. **Reference execution.** A memory reference backend projects agent-control
   state into presentation snapshots and updates, deterministically, and the
   conformance runner drives a presentation client against it. This is the same
   obligation as 0003 step 1 with a projection in place of a feature, and it is
   what makes the wire shape executable rather than prose.

   The projection lands in **both** memory backends, and the Zig one first.
   `oapx` is the product, the memory backend it already serves is Zig, and
   Decision 0032 makes the two trees peers in which new runtime capability lands
   in the product binary first or in both trees within the same milestone. A Go
   projection alone would leave the product with a profile its own binary cannot
   emit, and would make the presentation traces a Go-only artifact. The two
   projections are compared by `TestMemoryBackendMatchesOapx`, the test that
   already holds the memory backend to `oapx`'s answers, so a shape they disagree
   about is found there rather than by an implementer.
2. **Validator rules and fixtures.** A presentation bundle compiles under the
   same strict and tolerant modes the layered draft already defines, the
   validator enforces the profile's invariants, and `fixtures/manifest.json`
   lists positive and negative traces under the profile's own conformance name.
   Every existing fixture still validates unchanged.
3. **A consumer this project does not control.** An implementation outside this
   repository renders from the profile and submits intent through it.
   [Decision 0015](0015-evidence-from-implementations-we-do-not-control.md)
   proposes making "does not control" explicit and says why. Until it is
   accepted, the step binds on the wording above and nothing 0015 adds.
4. **Decision record.** A decision graduates the profile, cites the traces and
   the consumer, and records what stays deferred.

Step 3 is the one this record exists to make unambiguous: **`zig/src/tui/` cannot
satisfy it, and neither can any client written here.** A profile's evidence is a
consumer that did not help write it, for the same reason 0003 step 3 asks for an
adapter "this project does not control" — a consumer that shares the author's
assumptions proves nothing about whether the wire is learnable from the
specification alone.

Steps 1 and 2 land together, as 0003's gate already requires of a unit. A schema
whose shapes no producer exercises is a guess with a validator attached, and the
fixtures that would prove the guess are hand-written from the guess: they cannot
disagree with it. The pairing is what makes step 2 worth landing, because the
reference projection is what shows a shape is missing before an implementer
finds it.

A profile stalled at step 3 stays draft, exactly as a unit stalled at step 3
stays staged. Steps 1 and 2 landing is not that: the wire is then executable and
checked, and step 3 is what the profile still lacks. Nothing in this record
claims the profile is close to graduated; it claims the profile currently has no
evidence at all, which is a different and more tractable statement.

### What "executable" means here, and where it stops

In 0003 a unit is *executable* when it has graduated, and 0003's own wording is
careful to reserve the word: a unit stalled at step 3 keeps its memory adapter
and its validator rules, and no draft claims it is executable. This record uses
the same discipline for the profile, so the two states are not conflated.

The wire is **executable** once steps 1 and 2 land: a producer emits it, a
validator judges it, and both agree on traces the producer generated. The profile
is **graduated** only at step 4, and until a consumer this project does not
control exists, no draft says the latter. A change that lands the schema alone is
neither — it is a partial step, and this record does not authorise it.

### The presentation bundle is a sibling root, not a core widening

The core bundle is pinned at exactly seven schemas with
`"profile": {"const": "open-agent-protocol.agent-control-core"}` and
`"root_schema": {"const": "envelope.schema.json"}`
(`schema/v0.1/manifest.schema.json`), and its envelope `oneOf` is closed. The
`model-provider-core` profile already faced this and solved it by adding a
sibling root, `schema/v0.1/provider-envelope.schema.json`, outside the
seven-of-seven bound. Presentation-control follows that precedent: a
`presentation-envelope.schema.json` sibling with its own `oneOf`, contributing
`presentation.*` and `intent.*` types without touching the core bundle.

The alternative is rejected on the record 0003 already set for extension packs.
Adding presentation types to the core `oneOf` would make every core-only
implementation compiled from the bundle accept `presentation.snapshot.request`
and `intent.message.submit.request` as valid core envelopes, which is precisely
the condition 0003 called "tolerated rather than supported": its envelopes are
accepted because nobody can say what they should look like, and no implementation
can be wrong about them. It would also break the manifest's seven-of-seven bound
and change the core claim under STABILITY.md, which is a different decision than
this one.

The precedent is a working code path rather than a shape to invent.
`go/validation/schema.go` already special-cases the provider root by filename
while embedding the bundle, and `go/validation/provider.go` carries the parallel
envelope type and its rules. A presentation root is a third case of a seam that
exists, so the schema half of step 2 is the cheapest part of the graduation.

### A presentation layer is not a parity obligation

Under Decision 0032 a capability may ship in one tree first, the gap is visible
as `unavailable`, and it is never approximated. `drafts/cli.md` already places
`run`, `auth` and the TUI in the `oapx` column and leaves `goap` a dash. This
record gives the reason, and gives the shape parity takes at this boundary.

`goap` does not owe a TUI. No second client is written for the Go tree, and a
second one would be an approximation of a product surface by a tree whose job is
to serve Go users the protocol natively — `go/adapter`, `go/validation`,
`go/serve`, `go/client` and `go/conformance` are that job, and
`goap check`, `goap validate` and `goap conformance` are the evidence they
produce. A TUI in Go would exercise none of them.

Parity at the presentation boundary is one client against two control layers, not
two clients. When `goap` serves the profile, the existing TUI binary is the proof
that the boundary is learnable from the specification rather than from one
implementation's source. That is a stronger claim than a second client, and it
is the only one the wire can carry.

### The TUI is not refactored onto the profile until the profile is executable

This is the sequencing decision, and it is the one with real cost, so it is
stated as a constraint rather than left to judgement.

Refactoring twenty thousand lines of state machine onto draft prose moves the
implementation and the specification in the same change. Neither can then be
reviewed against the other: a diff in `tui/state.zig` and a diff in the profile
draft are indistinguishable from a single refactor that happens to be wrong.
Every other boundary in this repository was cut executable first — 0001 froze the
subset and then the adapters followed it, and 0003 required reference execution
to land with the schema.

So the profile lands first, executable, and the TUI is refactored onto it
afterwards as its own change, gated on the profile having step 2 evidence. The
in-process embed is legal in the meantime and stays legal: a control layer may
be in the same process as the presentation layer that renders it, and whether
`oapx`'s TUI eventually crosses a wire or keeps an in-process control layer
underneath is a composition question the profile does not decide.

### An epoch names one target's revision numbering

Every `presentation.snapshot.response` and every `presentation.updated` carries
`epoch`: an opaque identifier naming the revision numbering of one target.

A control layer mints a new epoch whenever it cannot promise that the numbering
continues — on start, or when it rebuilds that target's state. A control layer
that saves its counter may keep one epoch across a restart. **Same epoch means
same numbering**, and that is the whole promise the identifier makes.

A receiver holding a different epoch discards what it holds for that target and
takes the snapshot. An update whose epoch differs from the held one is discarded,
and the receiver re-snapshots the target.

The revision rules key on target **and** epoch. Within one epoch revisions
strictly increase and every update chains from its `base_revision`. They do not
advance by exactly one: the draft says monotonic, and requiring `+1` forbids a
control layer from coalescing several changes into one update. A new epoch may
begin at any revision, and a lower revision in a new epoch is not a regression.

This is what makes the revision discipline sound across a control-layer restart
without making persistence mandatory. Revisions that never reset would oblige
every control layer to persist a counter, and v0.1 carries no persistence
obligation — [Decision 0012](0012-persistence-is-not-in-v0.1-core.md) retires
`+persistence` for exactly that reason. The core already prefers reporting a gap
to faking continuity, and this is that choice at the presentation boundary.

### `pending_prompts` is the one source for what is being asked

`pending_prompts` is authoritative for the content of an outstanding prompt.
Each entry projects one core interaction and carries its `prompt_id`, its `kind`,
its `run_id`, renderable content — the question and the tool call it concerns —
and labelled choices. The core's interaction and choice shapes are reused rather
than restated, so a choice is a labelled thing a reader can render rather than a
bare id.

`intent.permission.resolve.request` and `intent.user_input.resolve.request` join
the minimum profile. Each names one prompt and one of its choices, or the answer
to a `user_input` prompt. A prompt resolves once: a stale or second answer is
refused with a typed `error.response` rather than applied.

A prompt leaves `pending_prompts` when it is resolved, cancelled or expired, and
the timeline keeps its outcome. The transition is visible in the timeline rather
than by the entry's disappearance alone.

Affordances say only whether the presentation can act now, and why not. They never
repeat a prompt's content, and a prompt carries no `affordance_id`.

### A receiver that cannot apply every change does not advance

An update's changes are applied as a set or not at all. A receiver which cannot
apply **every** change in an update discards the whole set, does not advance its
revision, and re-snapshots the target exactly as it would on a `base_revision`
mismatch.

The tolerant compile is not this answer and does not supply it. Tolerant makes an
unknown `change.kind` valid *on the wire*; it says nothing about what the receiver
then *does*, and those are different questions. A receiver that applied the
changes it recognised and advanced to `revision` would hold state that is not that
revision's state, which is the failure the revision discipline exists to prevent.

## Consequences

- `drafts/presentation-control-profile.md` keeps its `Status: draft`. This record
  defines the gate for graduating the profile; it does not graduate it, and a
  reader who takes the title as a graduation has misread it.
- The memory reference adapter grows a presentation projection, which makes it
  the first thing in the tree that can exercise the profile end to end and gives
  `goap conformance` a second claim to drive.
- The four SDKs gain a profile they can target. That is the cheapest answer to
  the gap `drafts/cli.md` records, and it only becomes true once the profile is
  executable — until then a thin client has nothing to be thin against.
- `zig/src/tui/` is not touched by this record, and no test in either tree
  changes by it. The profile is not executable by this record either, which is
  why the status is `proposed` under 0003's first criterion.
- `examples/presentation-control-session.json` does not judge the schema.
  `examples/` is illustrative by project rule — "illustrative JSON bindings for
  the draft protocol, not conformance tests" — so when a schema and the example
  disagree, **the draft decides** and the step-1 producer regenerates the
  example. A schema that breaks the example is a signal to re-read the draft, not
  evidence that the example is stale.
- A reader asking why `goap` has no TUI now has a record saying the omission is
  intended and the boundary is the deliverable, rather than a dash in a table.

## What this decision does not admit

- A second presentation client written to mirror the TUI, in Go or in any other
  language.
- `zig/src/tui/` as reference implementation, conformance evidence, or
  differential peer for the profile it does not implement.
- Presentation envelope types added to the core bundle's `oneOf`, or any change
  to the manifest's seven-of-seven bound.
- `Surface`, `Observation`, `Projection`, draft synchronization or durable
  notification, which the draft's idea pool holds outside the minimum profile and
  this record leaves there.
- The profile becoming executable, which is steps 1 and 2 together.
- The profile graduating, which is step 4 and needs a consumer this repository
  does not control.
- Steps 1 and 2 split across changes. 0003 binds them for a unit and this record
  binds them for a profile: a schema that no producer has emitted is a proposal
  about the wire, and a fixture written from that proposal cannot contradict
  it.

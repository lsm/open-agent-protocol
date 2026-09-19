# Decision 0020: Error Codes Are Declared, Not Enumerated

Status: proposed
Date: 2026-09-19
Protocol: `open-agent-protocol` version `0.1`
Profiles: `open-agent-protocol.agent-control-core`
Amends: nothing yet. It answers the question
[Decision 0019](0019-one-binary.md) left open and names the schema and
validator changes that answering it costs
Gated by: [Decision 0019](0019-one-binary.md),
[Decision 0004](0004-extension-packs.md)

## Context

[Decision 0019](0019-one-binary.md) left three shapes for `ProtocolError.code`
and decided none, while recording that the port has to model one before stage 1
finishes. It argued from a cost it never measured: that closing the set is "the
largest migration", because every harness-specific code in every adapter moves.

That is wrong, and the direction it is wrong in matters. Measured against the
tree, closing the set is roughly a quarter the size of adding a member. And the
measurement turned up a fourth shape that record did not enumerate, which this
project already decided once, built, and is enforcing today one layer over.

## The measurement

Forty-seven distinct codes appear across this repository's agent-control
fixtures, counting the adapter corpora. They divide cleanly:

| | codes | where |
| --- | --- | --- |
| Generic | 15 | the three top-level fixture directories |
| Harness-specific | 32 | adapter corpora only |

The split is total. **No harness-specific code appears in an agent-control
fixture directory, and no generic code needs to move.** Twenty-seven of the 32
carry their harness as a prefix — `claude_api_429`, `hermes_rate_limited`,
`opencode_step_failed` — and five do not: `ENOENT`, `refusal`,
`agent_not_found`, `incomplete_tool`, `tool_failed`. Those five are the
interesting ones, because nothing on the wire marks them as belonging to one
harness and a consumer cannot tell them from core.

What each shape costs, counted as error objects and as files:

| Shape | codes moved | error objects | fixture files |
| --- | --- | --- | --- |
| Close the set, origin to `extensions` | 32 | 39 of 145 | 33 |
| Keep `code` open, add a closed `action` | 0 | 145 | 136 |
| Leave it open | 0 | 0 | 0 |

**Closing the set is the small migration, not the large one.** Every file it
touches is an adapter corpus; not one agent-control fixture changes. Adding a
member is four times larger, because `code` staying open means derivation is
impossible everywhere, so the member is required on every error rather than on
the unusual ones.

## A fourth shape, already built

[Decision 0004](0004-extension-packs.md) is accepted, and a pack descriptor
declares `error_codes`. The validator checks them:
`validation/packstate.go` raises `unhonoured_capability` when "an endpoint
advertising this capability refused a well-formed request under a code its pack
never declared."

So the project has already answered this question once, for extensions, and the
answer was neither of 0019's first two. It was **declare, then check** — an open
set with a contract, rather than a closed set or a second member. Core does not
have it. The only thing core has is `openLevelRefusals` in
`validation/refusal.go`, four hardcoded strings.

## Decisions

### An endpoint declares its error codes, and the validator checks them

The capability descriptor gains `error_codes`: each entry a code and the
**action** a caller should take. It binds to `capability_revision` like every
other descriptor fact, so a code is bound to the snapshot that declared it.

The validator's rule: after a descriptor exists, a code on the wire is either a
core code or one the active descriptor declares. An undeclared code is a
diagnostic, not a decode failure.

This is [Decision 0004](0004-extension-packs.md)'s mechanism applied one layer
down, which is the argument for it beyond cost: one mechanism for "an
identifier this protocol did not define, used legitimately", rather than two
that have to agree.

### The action travels in the declaration, not on every error

0019's second shape put an `action` member on the error. The action is a
property of the **code**, not of the occurrence — `hermes_rate_limited` means
the same thing every time it appears — so the wire repeats per error what the
declaration states once. Measured, that is 145 repetitions against 32
statements.

The objection to a declaration is a round trip, and here there is none.
Discovery already precedes use: `capabilities.response` is mandatory, the
descriptor arrives before any run can produce an error, and
`capability_revision` already binds events to it. **A caller that can receive a
code already holds the declaration that explains it.**

### Codes before discovery come from the core set

`protocol.initialize.request` and `capabilities.request` are exempt from
revision citation so discovery is never blocked, which means an error can
precede any descriptor. Those errors carry a core code. The core set covers the
pre-discovery window, declarations cover everything after it, and neither has a
hole the other has to patch.

### Nothing in the corpus is rewritten, and new fixtures are written instead

This is the scheduling property and the reason it is stated as a decision
rather than left as a consequence. [Decision 0019](0019-one-binary.md) requires
a corpus-rewriting change to happen entirely before the port or entirely after
it, never during, because the port is gated on reproducing bytes that hold
still.

**No fixture in this repository carries both a capability descriptor and a
harness-specific error code.** Not one of 445 fixtures with a
`capabilities.response`, and not one of the 108 corpus cases, which carry no
descriptor at all. So a rule keyed on the descriptor touches nothing that
exists, the 33 files the closed set would have rewritten stay as they are, and
**this change is not corpus-rewriting**. It is not bound by 0019's constraint
and can land during the port.

The same fact is the work item. A rule no fixture can exercise is a rule with no
evidence, so this needs fixtures pairing a descriptor with a declared code and
with an undeclared one. Those are additions, which is the kind the corpus
absorbs without moving a target the port is aiming at.

## Evidence

**Three of the four core codes the validator special-cases are unexercised.**
`openLevelRefusals` decides whether a refusal is attributed to a pending
expectation or set aside. It names `session_exists`, `unknown_adapter`,
`session_closed` and `stale_capabilities`. Only the first appears in any
fixture. Dropping the other three from the map leaves `go test ./...` and
`oap check` green — verified by making that edit and running both.

That is the open-string regime's own defect, in the one place core reasons about
error codes at all: a vocabulary that nothing declares is also a vocabulary
nothing has to exercise, because there is no artifact to check it against. A
declaration is that artifact.

**The five unprefixed harness codes are the argument against leaving it open.**
`refusal`, `tool_failed`, `incomplete_tool`, `agent_not_found` and `ENOENT`
carry no mark of origin. `ENOENT` is an operating system's errno arriving on a
protocol wire through DeepSeek's adapter. Under the status quo it is
indistinguishable from a code this protocol defined, and nothing in the tree can
tell a reader otherwise.

**`incomplete_tool` appears in two adapters' corpora** — Makai's and
OpenCode's — which is the shape that makes "leave it open" expensive later. Two
harnesses have independently chosen one string, and whether they mean the same
thing by it is not recorded anywhere, so a consumer branching on it is guessing
across an agreement nobody made.

## Consequences

A consumer can act on an error it has never seen before, because the descriptor
it already holds says what to do with it.

The eight adapters each declare roughly four codes, in `adapter.go` beside the
descriptor they already build with a fixed `CapabilityRevision`. That is the
whole migration on the implementation side.

`unhonoured_capability`'s sibling rule for core is a new diagnostic code, and
the provider profile is unaffected: its set is closed and stays closed, so the
two profiles keep different regimes for a reason that is now stated — a closed
set derives its action, an open one declares it.

Foreign identifiers stay out of OAP identity, as the conventions already
require. A declared code is still the harness's string; declaring it makes it
checkable, not native.

## What this decision does not admit

Closing `agent-control-core`'s set. The measurement makes it affordable and it
is still wrong: 47 codes is not the ceiling, it is what eight harnesses
happened to need, and the 48th arrives with the ninth.

An `action` member on `ProtocolError`. It restates per occurrence what the
declaration states per code, and 0019's own argument for it — that a second
source of truth which can be checked is not the hazard an unchecked one is —
applies to the declaration just as well, at a quarter of the wire cost.

A declaration that is advisory. An undeclared code is a diagnostic. A rule that
notices and does not complain is the shape this project has removed twice.

## Open questions

**What the action vocabulary is.** `retry`, `refuse`, `reauthenticate`,
`report` is a first guess and not a decision. It wants the same treatment the
provider profile's error set got: derived from what callers actually branch on,
not from what reads well.

**Whether a code may be declared without an action.** Permitting it makes
adoption cheap and makes the contract optional, which is the under-claim shape
this project keeps naming. Refusing it means an adapter cannot declare a code it
has not thought about, which is arguably the point.

**Whether the provider profile gains declarations too.** Its set is closed, so
it does not need them for legitimacy. It might still want them for the action,
which would make one mechanism across both profiles rather than one each.

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

No harness-specific code appears in an agent-control fixture directory.
Twenty-five of the 32 carry their harness as a prefix — `claude_api_429`,
`hermes_rate_limited`, `opencode_step_failed` — and **seven do not**: `ENOENT`,
`refusal`, `agent_not_found`, `incomplete_tool`, `tool_failed`, and Codex's
`native_transport_closed` and `native_turn_failed`.

Those seven are the interesting ones, because nothing on the wire marks them as
belonging to one harness and a consumer cannot tell them from core. The two
Codex codes are the worst case rather than two more of the same: `native_` is a
prefix, so it looks like the convention being followed, and what it names is
*this protocol's own* notion of native. A reader who has learned that a prefix
means a harness will read `native_turn_failed` as core.

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

### The core set is enumerated first, and this lands before the port or after it

An earlier version of this record claimed the change rewrites nothing and is
therefore free of [Decision 0019](0019-one-binary.md)'s rule that a
corpus-rewriting change happens entirely before the port or entirely after it.
That claim was measured against the wrong predicate and is withdrawn.

What was measured: no fixture carries both a capability descriptor and a
*harness-specific* code, where harness-specific meant carrying a harness
prefix. What the rule actually fires on is a code that is **not core**, and
this record never enumerated the core set. It assumed the unprefixed remainder
was core, which is the same reasoning the record rejects two sections up — a
name read as a category.

Measured against the right predicate: **11 distinct codes appear in
descriptor-carrying fixtures, across 100 files.** Of those 11, five are emitted
by no core package: `queue_dropped` (only `adapter/opencode/session.go`),
`internal_error` (whose only Go occurrence is in makai's pinned native
vocabulary), and `other_error`, `provider_error` and `tool_error`, which no
implementation in this tree emits at all. Twenty-six of the 100 carry one.

So the change touches at least 26 existing fixtures and possibly all 100,
depending where the core line falls, and either way **it is corpus-rewriting
and is bound by 0019's constraint.** It goes before the port or after it.

The escape is worse than the constraint. Calling the eleven core because they
appear beside a descriptor promotes `queue_dropped` — one harness's word for a
queue it dropped — into the vocabulary every endpoint must implement, which is
the exact conflation this record exists to undo.

**So the enumeration comes first and is the decidable part.** Until the core
set is written down, every count here is an estimate and no schedule derived
from one is worth holding.

New fixtures are still needed on top: a rule needs a descriptor paired with a
declared code and with an undeclared one. Those are additions. The 26 are not.

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

**The seven unprefixed harness codes are the argument against leaving it open.**
`refusal`, `tool_failed`, `incomplete_tool`, `agent_not_found`, `ENOENT`,
`native_transport_closed` and `native_turn_failed` carry no mark of origin.
`ENOENT` is an operating system's errno arriving on a protocol wire through
DeepSeek's adapter. Under the status quo it is indistinguishable from a code
this protocol defined, and nothing in the tree can tell a reader otherwise.

**Three codes in the corpus are emitted by nothing.** `other_error`,
`provider_error` and `tool_error` appear in descriptor-carrying fixtures and in
no Go source in this tree. They are strings a fixture author wrote, and the
validator accepted them because an open field has nothing to check them
against. That is the status quo working as specified, and it is also the
clearest statement of what it costs: a corpus can assert behaviour for codes no
implementation produces, and nothing notices.

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

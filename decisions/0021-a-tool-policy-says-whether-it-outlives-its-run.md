# Decision 0021: A Tool Policy Says Whether It Outlives Its Run

Status: proposed
Date: 2026-09-19
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `run-controls` (claim term `+run-controls`)
Amends: [Decision 0005](0005-run-controls.md), extending its machine-readable
disclosure without reopening the per-submit discipline it settled
Gated by: [Decision 0003](0003-staged-unit-graduation.md)
Design: [Staged Units Graduation Plan](../drafts/staged-units-graduation.md),
section "T1. Run controls"

## Context

[Decision 0005](0005-run-controls.md) closed with a condition rather than a
prohibition. Under what it does not admit: session-level defaults are out,
controls stay per submit, and "a real consumer finding the per-submit shape too
chatty is the evidence for a later additive unit, not a reason to redesign
now."

That evidence has arrived, in a shape the per-submit discipline handles badly.
A control layer pinning an endpoint's tool set for the life of a session — the
"these three tools, nothing else" configuration every agent SDK exposes at
construction — is stating a session invariant, not a per-turn preference. Under
the frozen shape it must restate the policy on every submission, and nothing
holds it between them: a submission that omits `tool_choice` is a submission
under no policy at all.

The wire cannot currently express the difference, and that is the narrower
defect this decision fixes. `run.model_selection` discloses `mode`, so a caller
knows whether a selection dies with its run or becomes the session default.
`run.tool_selection` discloses `modes`, which is the orthogonal axis: which of
`auto`, `none`, `required` and `named` the endpoint can enforce. There is no
member on which an endpoint can say what happens to a tool policy after its
run settles. An endpoint that discards one and an endpoint that retains one
publish identical descriptors, and a caller omitting `tool_choice` on its next
submission cannot tell whether it is asking for `auto` or for whatever it said
last time.

The moment is cheap. Across the adapters in this repository only the reference
adapter advertises `run.tool_selection` above `unavailable`; Codex declares it
unavailable against its pin, and the six remaining harness adapters omit the
key. The axis is being added before any divergence exists to reconcile.

## Decisions

### `run.tool_selection` discloses the same mode pair

`mode` on `run.tool_selection` is `per_run` or `session_mutation`, carrying the
meanings [Decision 0005](0005-run-controls.md) gave them for
`run.model_selection`: `per_run` leaves whatever the session would otherwise
apply untouched, `session_mutation` changes it and the session state afterwards
reports the native truth.

No schema change is needed. `mode` is already a free-form string on
`featureSupport`, so what this decision adds is the validator rule that reads
it: a `run.tool_selection` advertised above `unavailable` with a `mode` outside
the pair is `undisclosed_selection_modes`, diagnosed at
`/payload/features/run.tool_selection/mode`. The existing `modes` requirement
is untouched and is judged separately, because the two answer different
questions and a descriptor can fail either alone.

### Disclosure is optional here, and that is a rule about rules

[Decision 0005](0005-run-controls.md) required `mode` wherever
`run.model_selection` is advertised, and gave the argument: both of its rules
key on the mode, so a descriptor without one is a descriptor under which a
session default may move, or fail to, with nothing to say so.

That argument does not transfer, and it is worth being precise about why rather
than treating optionality as a compatibility concession. No rule keys on
`run.tool_selection`'s mode. There is no retained tool policy for `per_run` to
leave untouched, no session state member that reports one, and so no pair of
behaviours a caller could be left guessing between. An omitted mode is not an
ambiguity the validator declines to diagnose; it is the only behaviour the
protocol defines. A tool policy applies to the run that carried it, and an
endpoint that says nothing has said exactly that.

The compatibility fact points the same way and is worth recording as
corroboration rather than as the reason: twenty-one fixtures advertise
`run.tool_selection` affirmatively and none carries a mode. Requiring the
member would unconform every endpoint conforming today, in service of a
distinction no rule yet draws.

When the enforcement lands, the requirement lands with it, under 0005's
argument rather than a new one: at that point two behaviours exist, both key on
the mode, and a descriptor without one stops being unambiguous.

### The declaration precedes the enforcement, deliberately

A member nothing enforces invites the objection that it is decoration, and the
objection deserves an answer rather than a deferral.

The alternative is worse in a specific way. Without the member, an endpoint
that retains a tool policy has no way to say so, and a caller discovers the
retention by observing that a later submission behaved in a way it did not ask
for — the failure mode [Decision 0005](0005-run-controls.md) already found and
fixed once, when Codex and the since-removed Makai adapter wrote a per-run
`model_id` into `current_model_id` and a selection made once silently became
the session default. Recording that axis before any endpoint diverges is what keeps the
same fault from being discovered a second time by observation.

The honest limit belongs here rather than in a footnote. An endpoint declaring
`session_mutation` today is making a claim the validator cannot hold it to.
Model selection has a state machine behind its mode — the retained default, the
per-run guard, the observation that settles it on a started run, and the queue
window's mutation flag — and tool selection has none of it. The claim is
inspectable and dated by the capability revision that carries it, which is
strictly more than a caller has today, and it is not yet checkable.

## Evidence

Fixtures (`fixtures/manifest.json`, unit `run-controls`): two, both minimal
capability exchanges, because the rule is decidable from one descriptor.
`valid/controls-tool-selection-session-mutation-mode.json` advertises
`run.tool_selection` with `mode: "session_mutation"` and validates clean.
`semantic-invalid/controls-tool-selection-unknown-application-mode.json`
advertises it with `mode: "per_turn"` and is
`undisclosed_selection_modes` at the mode pointer. Both disclose `modes`, so
the existing requirement is satisfied and only the new rule decides them.

Reference execution: `adapter/memory.go` declares `mode: "per_run"` on
`run.tool_selection`, which is what it has always done — the policy selects
whether the scripted tool is called and is not retained past the run. Its
capability revision moves to `reference-memory-v10` because the descriptor
changed.

Native evidence: none, and none is required. This decision adds a disclosure
axis, not the execution of a control. `run.tool_selection` graduates as
executable by amendment to [Decision 0005](0005-run-controls.md) when a ledger
pins a native adapter's evidence, exactly as that decision provided.

## Consequences

- No fixture changes meaning and no endpoint conforming today stops
  conforming. The rule can only fire on a member no descriptor in the corpus
  carried.
- `reference-memory-v10` replaces `reference-memory-v9` wherever the revision
  is pinned: the adapter, the stdio golden transcript, two Go client tests, and
  two TypeScript client tests.
- The enforcement unit can require the mode rather than introduce it, so the
  retained-policy rules arrive as an additive amendment instead of a
  descriptor break.

## What this unit does not admit

- Enforcement of `session_mutation` for tool policy. It needs a retained policy
  in session state, an observable that reports it so a disagreement is
  diagnosable at all, and the deferred-application rule under a queued
  admission that [Decision 0005](0005-run-controls.md) already deferred to the
  queue unit for the model analogue.
- A session-scoped tool restriction on `session.open.request`. Both tool-shaped
  members there today are additive — `tools` provides control-owned definitions
  and `tool_sources` attaches sources — and neither narrows what the endpoint
  already has. Whether narrowing belongs at open, and whether a caller may
  narrow what an operator configured, is a separate question this decision does
  not prejudge.
- Any ordering rule between tool discovery and session creation.
  `capabilities.response` already carries `tools`, and the validator already
  binds `tool_choice` to that catalog when one was served, so an endpoint with
  a static catalog can be interrogated before a session exists. An endpoint
  whose catalog does not exist until its first turn cannot, and what it owes in
  the interval — whether an unresolvable name is refused later or never — is
  the enforcement unit's question.
- Whether a policy is amendable before it settles. The question only exists
  once a policy can be held across submissions, which is the enforcement unit.

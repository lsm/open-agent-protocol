# Decision 0005: Run Controls

Status: proposed
Date: 2026-09-15
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `run-controls` (claim term `+run-controls`)
Extends: [Decision 0001](0001-agent-control-v0.1-executable-core.md) and
[Decision 0002](0002-admission-before-start.md) without amending either
Gated by: [Decision 0003](0003-staged-unit-graduation.md)
Design: [Staged Units Graduation Plan](../drafts/staged-units-graduation.md),
section "T1. Run controls"

## Context

Four per-submit controls have been on the wire since v0.1 froze:
`model_id`, `instructions`, `tool_choice`, and `output_schema` on
`session.message.submit.request`. None of them was executable. Decision 0003
recorded what that cost: Codex applied `model_id` to `turn/start` and Makai to
`agent_message.model_ref`, both natively per run and neither advertising it,
while every other adapter refused the other three by rejecting the whole
submission as invalid. A caller could not tell an endpoint that would apply a
control from one that would ignore it, and an endpoint that refused one said
only that the submission was bad.

Two smaller faults came with that. Both Codex and Makai wrote the per-run model
into `current_model_id`, so a selection made once silently became the session
default for every later control-free submit. And because `ModelID` was a plain
Go string with `omitempty`, an adapter testing it with `!= ""` could not
distinguish an absent control from `{"model_id": ""}`: an empty id slipped past
the gate entirely and reached a native codec that read it as "the default
model" — a silent selection, which is the exact failure the gate exists to
prevent.

This decision graduates the discipline for all four controls and the execution
of `model_id`, on the Codex and Makai evidence. It is the first unit under
Decision 0003's gate.

## Decisions

### The unit claims a discipline and, separately, execution

`+run-controls` claims two things, because the evidence for the four controls
arrives at different times and the claim must never say more than the evidence
does.

The first is the *discipline*, common to all four: presence-carrying types, the
fail-closed gate, the refusal ladder, and the typed refusals. Every endpoint
implements it whether or not it supports a single control, because refusing an
unadvertised control correctly *is* the discipline — and that has native
evidence today: every adapter in this repository refuses, under the control's
own capability key, each of the four it does not advertise. A generic "invalid
submission" would not do: it tells a caller that something in the request was
wrong, not which control to stop sending, which is the one thing the refusal
exists to say. `adapter.RefuseUnadvertisedControls` is the shared form, so
every endpoint refuses in the same order the ladder ranks.

The second is *execution*, claimed per control and only for the controls the
endpoint advertises above `unavailable`. An endpoint claiming the unit passes
the discipline fixtures for all four and the executable fixtures for each
control it advertises, and nothing for the ones it does not. An endpoint
advertising only `run.model_selection` is fully conformant.

This decision graduates the discipline and `model_id` execution.
`instructions`, `tool_choice`, and `output_schema` keep their frozen shapes and
their reference-adapter execution; each graduates as executable by an amendment
to this decision when a native adapter advertises its key against a pinned
ledger.

### Presence is what the gate judges

`model_id` and `instructions` become `*string` in Go. The schema permits an
empty string, so a plain string cannot tell an absent control from a present
empty one, and the wire's meaning must not depend on a Go convention. A
present-but-empty control is a control: it is judged through the gate like any
other and, once past it, read as a value like any other.

For `model_id` that value is one no catalog can list, so it is a catalog miss
and takes the miss's refusal — `model_not_found` with `details.model_id: ""` —
rather than a second unsatisfiability of its own. Emptiness needs no catalog to
decide, so this refusal is owed from this unit on, whether or not a catalog was
ever served. The models unit adds only what genuinely depends on a catalog: the
bookkeeping that classifies *non-empty* ids a catalog does not list.

### Controls are per submit and fail closed

A control an endpoint has not affirmatively advertised is refused before
admission, before any native write, with `unsupported_feature` naming the
capability key and `details.reason: "unadvertised"`. No submission or run
identity is allocated. A control advertised `degraded` needs its key in the
caller's `allow_degraded_features`, else `capability_degraded`. A control whose
value cannot be honoured is refused as `unsatisfiable` with the detail that
names the offending member.

A request can fail several of these at once, and one `error.response` carries
one code, so the refusals are ranked rather than conjoined: capability, then
degradation, then unsatisfiability, then state. Within a rung the lower
capability key wins; within one rule, the offending member with the lowest JSON
Pointer. The ladder runs from the most permanent failure to the most transient,
which is the order in which a caller can act — what it must stop sending
outranks what it must send differently, which outranks what it may retry.

Ordinary submission validation sits below the whole ladder, so a request that
is malformed *and* carries a control the endpoint cannot apply is answered
with the control. The reason is the same one that orders the rungs: a caller
told only that its submission was invalid fixes the messages, resubmits, and
is refused again for a control it was never told about. Every endpoint
therefore runs the control gate before it validates anything else, and before
it allocates any identity or writes anything native. Among the collected
failures, the answer is chosen by the ladder rather than by the order the
endpoint happened to test them in — an endpoint returning its first defect
would name a different control than the validator names for the same request.

### The gate is judged on the correlated response

The wire makes refusal the required behaviour, so the validator cannot diagnose
the request: doing so would fail the conduct the protocol mandates. It retains
what each control-carrying submission owes and settles it on the correlated
response. An admission where a refusal was owed is diagnosed on the admission;
a refusal under a code, feature, or reason that does not tell the caller what
to change is diagnosed on the `error.response`, because a refusal the caller
cannot act on is no better than none.

The direction runs both ways. A control the validator can see is within every
constraint the endpoint disclosed, refused as unsupported, is diagnosed too:
the caller discards a request that was valid, and re-sending it only confirms
the refusal. That is what makes an advertised key mean something.

### Disclosure is machine-readable

An endpoint-specific constraint left in prose would let any endpoint advertise
a key and refuse everything while a validator, unable to know the constraint,
accepted each refusal as typed and conforming. `FeatureSupport` therefore gains
two members beside `mode`:

- `modes`, the `tool_choice` modes the endpoint can actually enforce. A refusal
  is conforming only for a mode outside the list, and a descriptor advertising
  `run.tool_selection` with no modes at all is `undisclosed_selection_modes`.
- `constraints`, whose `fixed_result` member for `run.structured_output` is the
  exact object every `run.completed` under an accepted schema will carry.
  Declaring it makes a fixed-output endpoint's refusals checkable in both
  directions, and binds it to emitting exactly that object.

`run.model_selection`'s `mode` discloses how a selection is applied: `per_run`
leaves the session default untouched, `session_mutation` changes it and the
session state afterwards reports the native truth. It is required wherever the
key is advertised, under the same argument and the same diagnostic: both rules
key on the mode, so a descriptor without one is a descriptor under which a
session default may move, or fail to, with nothing to say so. A
`run.model_selection` advertised with no `mode`, or with `restart`, which this
phase gives no rules, is `undisclosed_selection_modes` too.

### A per-run selection does not move the session default

An admitted `model_id` under `mode: "per_run"` is authoritative for its run and
for nothing else. `current_model_id` — the model the next `auto` submission
without a `model_id` would use — stays what it was, and stays so after the run's
terminal and across any later capability refresh. Limiting the rule to the run's
lifetime would accept a post-terminal snapshot reporting the run's model and let
the next control-free submit use the wrong one, which is the whole failure the
rule exists to catch.

This is the Codex and Makai fix, and it is verifiable: a snapshot reporting
anything other than the retained default is `unapplied_control`.

### `output_schema` is compiled, never resolved

An admitted `output_schema` must describe a JSON object, be self-contained, and
compile. Both the validator and an adapter compile it with a loader that
refuses every reference outside the document, so an untrusted schema can never
make either read a local path or fetch a URL. The rule is the general one — any
metaschema or compilation failure makes the control unsatisfiable — with the
root-type and external-reference cases as instances rather than an enumeration.
One function does it for both sides, so the validator and the reference adapter
cannot disagree about which schemas are satisfiable.

`run.completed.result` becomes `json.RawMessage` in Go. As a map with
`omitempty` it dropped a valid empty object from the wire, and `{}` is a
conforming structured result under a schema that requires nothing.

### `run.completed` gains `model_id`

The rule that a completion may not name another model needs wire to stand on,
and the member earns its place beyond enforceability: a consumer can see which
model produced the final response without correlating back to the admission,
which matters most where the answer is surprising. The payload is closed, so
this addition relies on the tolerance step that landed before this unit.

## Evidence

Fixtures (`fixtures/manifest.json`, unit `run-controls`): 48 traces covering
each control admitted and applied, the capability gate and its conforming
refusal for every key, the empty-`model_id` condition in all four directions,
the refusal ladder, both directions of unsatisfiability, the degraded opt-in,
the tool policy's positive and negative requirements, the structured result and
its disclosed fixed form, and the per-run default across a terminal and a
capability refresh.

Native evidence: Codex graduates `model_id` at `native` with mode `per_run` on
`turn/start.model`, now pinned in
[the ledger](../research/codex-app-server-8d7cc24-mapping.md) and executed by
the corpus case `model-per-turn` through the production reducer. Makai
advertises the same key at `native`/`per_run` on `agent_message.model_ref`,
which its existing corpus already exercises. Both stop overwriting
`current_model_id`.

Reference execution: `adapter/memory.go` advertises all four, applies each,
discloses its fixed catalog and its `fixed_result`, and refuses what it cannot
honour with the typed errors.

## Consequences

- Every adapter now refuses the controls it cannot apply under the key it
  advertises `unavailable`, rather than rejecting the submission as invalid:
  Codex and Makai refuse three each, the other six refuse all four, and all of
  them do it through `adapter.RefuseUnadvertisedControls` so the order is the
  ladder's.
- Both codecs relay a control refusal through one shared mapping, so a control
  refused over HTTP is refused identically over stdio, and both clients expose
  the details that say what to change.
- `run.started`'s model disagreement moves from `illegal_run_transition` to
  `unapplied_control`. No other fixture in the manifest changes meaning.
- The corpus-completeness check now binds this unit: each of the four keys
  carries a negative gate fixture and a negative honour fixture.
  `run.model_selection`'s honour fixture was planned as a deferral to the
  models unit and did not need one — the wire assigns every catalog miss to
  `model_not_found`, so refusing an advertised `model_id` as
  `unsupported_feature` is wrong whatever the id was. The models unit still
  adds the catalog-dependent half.

## What this unit does not admit

- Session-level defaults or a configuration document. Controls stay per submit.
  A real consumer finding the per-submit shape too chatty is the evidence for a
  later additive unit, not a reason to redesign now.
- Execution of `instructions`, `tool_choice`, or `output_schema` on any native
  adapter. Each graduates by amendment when a ledger pins its evidence. Codex's
  `turn/start` carries a `developerInstructions` parameter at its pin, but no
  fixture exercises it and the ledger does not pin its semantics, so
  `run.instructions` stays `unavailable` there.
- Whether admitted `instructions` took effect. There is no wire observable for
  it — the model's output is not a conformance surface — so the key means "this
  endpoint accepts instructions". Closing that would need an observable such as
  an echo of the effective instructions in session state, which is a design
  question and not something to invent here.
- The `session_mutation` mode's deferred application under a queued admission,
  which needs the queue unit, and `premature_session_mutation` with it.
- Catalog-dependent model rules: a non-empty `model_id` the endpoint does not
  serve is classified by the models unit.

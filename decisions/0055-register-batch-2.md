# Decision 0055: Register Batch 2 — Identity, Scope, Cancel, Recovery Cursor and Name Patterns

Status: accepted 2026-10-19
Date: 2026-10-19
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Amends: [Decision 0001](0001-agent-control-v0.1-executable-core.md),
[Decision 0011](0011-control-layer-provided-tools.md) and
[Decision 0019](0019-one-binary.md)

## Context

The validator rule register being assembled under `drafts/validator-rules/`
found behaviour every tree judged and no text stated: the rules an
implementation of the executable core was expected to know without being told.
Each was recorded as an open item, numbered as in the headings below, rather
than copied from whichever tree had it. [Decision 0054](0054-register-batch-1.md) settled the first
batch. This one settles the second, except input answers, which
[Decision 0056](0056-input-answers-are-judged-against-the-prompt.md) takes on
its own. Each answer is written into the decision it belongs to.

## Decisions

### An envelope id is unique within a trace (OPEN-0001-1)

Correlation names a request by its `id`, so two envelopes sharing one make
every reply to either ambiguous. An envelope `id` is unique within a trace; a
repeat is `duplicate_envelope_id`.

### A response stays in its request's scope (OPEN-0001-3, OPEN-0008-2)

A response, `error.response` included, answers in the scope its request named:
the session, run and interaction a response states are the request's, and an
`error.response` that states a session or run states the request's. An answer
in another scope is `scope_mismatch`. The tool-call resolution pair is held to
the same rule for the binding it carries: the `tool_call_id` of an
`action.call.resolve.request` or `.response` names the tool call the
interaction is bound to, on the envelope and in the payload alike. Decision
0008 already has a session's tool catalog name its session on the envelope;
that is this rule's first instance, and every tree must judge it, including
where the envelope omits the session rather than naming another.

### A run belongs to one session and is admitted once (OPEN-0001-4)

A `run_id` is admitted once, by one admission, into one session, and every
event of that run is scoped to that session. An admission naming a run another
session owns is `scope_mismatch`; a second admission of one run is
`illegal_run_transition`; a run event scoped to another session is
`scope_mismatch`.

### A cancel names a run the trace admitted (OPEN-0001-2)

`run.cancel.request`, and an accepting `run.cancel.response`, name a run the
trace admitted on that session. Admitted means accepted by an admission,
queued reservations included, so cancelling a reservation before it promotes
is valid. A cancel naming a run no admission created is
`illegal_run_transition`.

### An interaction id is requested once per run (OPEN-0001-5)

An `interaction_id` is requested at most once within its run; a second request
under the same id is `duplicate_interaction`. A request replayed from a
retained journal during recovery is the same request delivered again, not a
second one.

### The recovery cursor is a JSON-safe sequence (OPEN-0001-7)

`resume_cursor` names a position in a run's sequence: a JSON integer that
increases monotonically, no greater than 2^53 − 1 so every JSON reader holds it
exactly. A cursor of any other form is `undeclared_replay_gap`, because a
position nobody can read cannot bound the replay.

### `name_pattern` is RE2 and matches the whole name (OPEN-0011-1)

`action.tools.provide`'s `limits.name_pattern` is written in RE2 syntax and
matches a tool name only when it matches all of it, as if anchored at both
ends. Go uses `regexp` and Rust will use `regex`, both RE2; Zig owes an
RE2-compatible implementation. Fixtures for anchoring and for the syntax the
pattern uses judge all three, and until Zig has an engine its gate is a
recorded port gap rather than a divergence.

## Consequences

- Decision 0001's identity, run, cancellation, interaction and recovery
  clauses, Decision 0011's resolution pair and limits clauses, and Decision
  0019's `name_pattern` entry state these answers in their own text.
- Ports follow, each citing its item: the three scope checks only Go has
  (OPEN-0008-2) go to Zig and Rust; Zig gains an RE2-compatible `name_pattern`
  matcher and Rust its provided-tools limits; every tree accepts a cancel of a
  queued reservation and judges the cursor's bound.
- Go matches `name_pattern` unanchored today, so a pattern `foo` admits
  `xfooy`. Go anchors its match, with a fixture whose tool name contains a
  match without equalling one, and so does any reference adapter that checks
  the pattern.

# Decision 0006: Models Catalog

Status: proposed
Date: 2026-09-16
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `models` (claim term `+models`)
Extends: [Decision 0001](0001-agent-control-v0.1-executable-core.md) and
[Decision 0002](0002-admission-before-start.md) without amending either
Gated by: [Decision 0003](0003-staged-unit-graduation.md)
Discharges the catalog deferral in
[Decision 0005](0005-run-controls.md)
Design: [Staged Units Graduation Plan](../drafts/staged-units-graduation.md),
section "T5a. Models catalog"

## Context

Decision 0005 made `model_id` executable and left one thing undecidable. The
gate could refuse an empty id — no catalog can list one, and none is needed to
know it — but it could say nothing about a non-empty id the endpoint does not
serve, because "does not serve" is a claim about a set nobody had published.
The wire already assigned that refusal a code, `model_not_found`, so the shape
was fixed; what was missing was anything to check it against.

The cost was two-sided and neither side was visible. An endpoint could accept a
model it could not run and nothing would say so. An endpoint could refuse a
model it could run perfectly well — under `model_not_found`, or under
`internal_error`, or under no useful detail at all — and a caller would discard
a selection that was valid, with no way to tell the two apart. A capability key
that admits both behaviours promises nothing.

This decision graduates the catalog that makes both checkable, on the OpenCode
evidence. It is the third unit under Decision 0003's gate, moved ahead of queue
delivery for the reason that decision gives: it is the lowest-risk unit with
the most native sources, and it is what makes the first unit's `model_id`
selectable from a real control layer rather than guessable.

## Decisions

### The catalog is a session-scoped query, and it binds in both directions

`models.request`/`models.response` join the envelope `oneOf`, session-scoped
like `session.state.request`, with their payloads in the control-plane schema
file so the bundle's inventory is unchanged. The request is remembered rather
than gated; the served catalog is what the `models.list` gate judges, because
serving a catalog is the act the key governs.

A catalog is a promise with two halves, and a unit that enforced only one would
be worse than none. Every id it lists is selectable: refusing a listed id with
`model_not_found` is `model_not_in_catalog` on the refusal, and it is the more
damaging of the two failures, because the client discards a request that was
valid and re-listing only confirms the id it was just told does not exist.
Every id it omits is not: admitting one is `model_not_in_catalog` on the
admission.

The miss's refusal is held to the code *and* the detail. `model_not_found`
without `details.model_id` tells a caller that some model was missing, not
which one to stop sending, and `internal_error` tells it nothing at all. Both
are diagnosed, so the adapter that swallows a catalog miss behind an
undiagnosable failure is treated exactly as the one that accepts it.

### The rule stands down where selection is not advertised

An endpoint may serve a catalog and apply no per-submit selection — OpenCode is
exactly that endpoint. There the mandatory answer to a `model_id` is Decision
0005's `unsupported_feature` with `details.reason: "unadvertised"`, and
demanding `model_not_found` as well would leave no conforming response. The
control gate wins and this rule stands down. Precedence is a property of the
request, so a catalog arriving later cannot retroactively make a refusal that
was conforming when it was given owe a different code.

### A catalog belongs to the revision it was served under

The catalog is part of the capability snapshot. A `capabilities.updated`, or a
new `capabilities.response` that *changes the active revision*, discards it, so
no admission is judged against a stale list: a newly added model is not falsely
diagnosed, and a removed one is not silently accepted. Within one revision the
catalog may not move — a second response whose descriptors differ in any member
is `unannounced_catalog_change` — because availability that changes with
nothing to announce it is availability no consumer can observe.

Re-fetching capabilities is not what discards it. A revision identifies exactly
one descriptor, so a `capabilities.response` repeating the active revision
repeats that descriptor and licenses no different catalog. Discarding on the
re-fetch instead would hand every endpoint a way out of the stability rule —
answer `capabilities.request` between two listings and the change is never
announced and never diagnosed — which is the whole of what the rule exists to
catch (`models-catalog-mutates-across-refetch`).

That argument has a sharp edge, and it is taken rather than avoided: the same
identity makes a same-revision response that *changes* `models.list` the
defect. Two remedies were available — hold the catalog to the level it was
served under whatever the descriptor later says, or diagnose the descriptor
mutation itself — and this decision takes the second. The reason is which
thing is wrong. The catalog that follows a flipped advertisement is a correct
catalog under the descriptor now in force; what cannot be true is that one
revision named two descriptors. Diagnosing it where the descriptor is
published is also what makes the fix obvious — introduce a new revision, after
which both catalogs are legal — and it keeps one fault to one diagnosis rather
than reporting the mutation and then every catalog that follows it.

So a `capabilities.response` whose revision equals the active one and whose
`models.list` level differs from what that revision advertised is
`unannounced_catalog_change`, raised on the descriptor
(`models-advertisement-mutates-within-revision`). Without it an endpoint could
publish a `native` catalog, re-answer `capabilities.request` at the same
revision with `models.list` `degraded`, and serve a different catalog: the
stability rule binds only a `native` or `emulated` catalog, so the level change
would switch the rule off and both listings would stand. Only the level is
compared, because that is what every rule in this unit keys on; a reworded
`reason` is prose, and diagnosing prose would make the rule noisy without
making it stronger.

A selection made under a revision whose catalog the trace has not served is not
skipped but retained, and the first catalog under that revision settles it,
admissions and refusals alike. Without that an endpoint could accept an
unlisted model in the gap between a refresh and its catalog and have the gap
hide it. Reconciliation runs at `native` and `emulated` only: at `degraded` the
catalog refreshes out of band, which is what `degraded` discloses here, so an
earlier selection may have matched a list that was never served and diagnosing
it would convict an endpoint for a catalog nobody could have read.

### `current_model_id` is judged across the query's window, not at an instant

A catalog overlapping a session mutation is captured at an instant the trace
cannot name: the adapter may read the old model and have the mutation's run
event reach the trace first, and the reverse ordering can show the new value
before the validator has observed the mutation. Both are valid stale or early
reads, and reporting either as a mismatch against a single endpoint value would
be wrong. So the rule takes the set of values the model held between the query
and its response, and diagnoses only a value that was never the session's model
within it.

That window is bounded by the trace, which cannot see a mutation that happened
inside the adapter before capture. `models.response` therefore carries its own
position: `as_of_model_event`, naming the last model-affecting event the
catalog reflects, absent when it reflects none. It is compound — a run and a
sequence — because sequences restart per run, and a bare number could not say
which event an ahead-of-trace catalog meant. Where it is present the value is
judged at that point; where the trace has not reached that point the claim is
held and reconciled when the event arrives, because an endpoint reporting
knowledge the trace lacks is ahead of it, not wrong.

A position is a claim about one session's model, so it can only name that
session's own runs. The run map is the endpoint's, not the session's, so a
catalog for one session naming a run in another is `scope_mismatch` and the
claim it carried is not read: without that check such a position passes the
ordinary window comparison when the foreign run is complete, and escapes
judgement entirely when it is still ahead, because a held claim is revisited
through the run's own session (`models-position-in-another-session`,
`models-held-position-in-another-session`). Ownership is decided when the run
appears rather than when the catalog does, since a catalog may legitimately
name a run the trace has not reached — which is the whole reason positions are
held.

A nonempty `current_model_id` must also name one of the response's own ids. A
picker shown a current model the catalog does not describe could not resolve
it, and re-selecting the same id would be refused by the catalog rule, so the
adapter that serves such a catalog is diagnosed rather than the caller.

### The opt-in is per query, because the query is a request of its own

`models.request` carries `allow_degraded_features`, the same carrier
`session.message.submit.request` has. Without a field to consent on, an
endpoint exposing `models.list` as `degraded` would have to refuse every
catalog request or serve degraded behaviour without consent, and Decision 0005
forbids both — a `degraded` catalog would be one no client could lawfully read.
The rule is Decision 0005's, unchanged: a query omitting the key is refused
`capability_degraded` with `details.feature`, and a response correlated to it
is `degraded_without_optin`.

The parameter threads through all five layers as one value with the same
meaning: `?allow_degraded=<key>`, repeatable, on `GET
/sessions/{id}/models` — a GET carries no body, and a header would hide a
wire-visible field from logs and curl — an `allow_degraded_features` field on
the stdio `models` op, `AllowDegraded(...)` on the Go client,
`allowDegradedFeatures` on the TypeScript one. A call that does not ask for it
is byte-identical to one made before the option existed.

### A served catalog travels with the revision that governs it

`adapter.ModelLister` returns `adapter.Catalog` — the listing and the revision
that governs it — and both codecs stamp the response from it. Both clients
return it beside the listing rather than the payload alone: `client.Catalog`,
`Catalog` in `clients/ts`, mirroring what `Capabilities` already returns.

The revision travels *with* the listing rather than beside it because the
alternative cannot be made correct. Reading the descriptor and then asking for
a catalog is two reads, and on an endpoint whose capabilities can update the
revision moves between them: the daemon then labels a new-revision catalog with
the old descriptor's revision, which is precisely the miscaching the label
exists to prevent. Probing again afterwards and comparing would narrow that
window rather than close it, at the cost of an extra probe per call and a
failure mode — refuse, or retry — for a race the session never had. Only the
session knows which descriptor it served a listing under, so the contract asks
it. That also matches what every adapter here already does with the revision on
the events it emits; a catalog was the one payload the daemon was labelling on
the adapter's behalf. A listing that comes back with no revision is refused by
the hub rather than labelled from elsewhere.

The alternative is worse than it looks. A caller holding only the payload
cannot tell which `models.list` promise it read, cannot cache the listing
against a revision, and cannot invalidate it: probing again answers with
whatever revision is current *now*, which may already be a different one. The
validator says the same thing from its own side — the catalog's gate rejects a
`models.response` that cites no revision — so a client that hid the revision
would be hiding the one field that makes the answer usable.

### A descriptor's `id` is non-empty by schema

Decision 0005 refuses an empty `model_id` unconditionally and owes that refusal
whether or not a catalog was ever served. A catalog that listed an empty id
would therefore offer a picker a value the endpoint is required to reject, and
the two rules would contradict each other on the wire. The constraint belongs
in the schema rather than in a validator rule, so the contradiction is
unrepresentable; `current_model_id` carries it for the same reason.

### `adapter.ModelLister` is optional and discovered by type assertion

The catalog is not added to `Session`, so every implementation written before
this unit compiles unchanged. A session whose adapter does not implement it is
refused under the ordinary gate rather than answered with an empty list: an
endpoint that lists nothing and an endpoint that cannot list are different
answers to the same question, and collapsing them would let a hub advertise a
catalog no adapter serves.

Its one method returns `Catalog`, not the payload alone, for the reason above:
a listing and the revision it belongs to are one answer.

## Evidence

Fixtures (`fixtures/manifest.json`, unit `models`): 30 traces covering the gate
and its conforming refusal, the degraded opt-in in all three directions, the
catalog's internal consistency, `current_model_id` across a mutation window and
ahead of the trace, the catalog-miss classification in both directions and in
every refusal shape, the in-gap selection and its reconciliation, and the
stability rule for ids and for descriptor metadata.

Native evidence: OpenCode graduates the unit at `degraded`. The ledger names
`model.list` and `provider.list` as catalog routes but pins no response shape
for either, and an adapter may not decode a shape no pin covers; what is pinned
is the model on the session record and the `ModelRef` every durable
`session.next.step.started` carries. The adapter serves those as the session's
effective catalog and discloses exactly that, which is why the level is
`degraded` rather than the `native` the plan projected. This makes OpenCode the
first native adapter to exercise the degraded opt-in end to end. The corpus
case `multi-step` now carries `catalog.json`, the catalog the production
reducer projects from its own durable frames, and
[the ledger](../research/opencode-v1.18.29-mapping.md) records the finding and
exactly what raising the level to `native` would need.

Reference execution: `adapter/memory.go` publishes the two ids its model gate
already admits, `default` on the first, deterministic and stable for the
revision. The descriptor moves to `reference-memory-v3`, because a revision
identifies exactly one descriptor.

## Consequences

- Decision 0005's catalog deferral is discharged. `run.model_selection`'s
  honour aspect keeps its own fixture under `run-controls`, and the
  catalog-dependent half it left to this unit —
  `models-listed-selection-false-miss` — lands here, claimed by both units.
  `honourDeferred` stays empty, which is what it should mean: no key is left
  unfalsifiable in its own unit.
- Every adapter that serves a catalog is now bound by it. The eight harness
  adapters other than OpenCode implement no `ModelLister`, so their sessions
  are refused under the key rather than answered, which is the fail-closed
  answer.
- `SessionOpenResponse` gains the `current_model_id` the schema already
  promised it, so the first snapshot a control layer sees can report the
  session's model. The validator reads it as session state and not only as
  catalog evidence: it is the default a `per_run` selection must leave alone,
  so a session that reported its model at open and nowhere else is now guarded
  from the first submission rather than from the first snapshot
  (`models-open-response-guards-the-default`).
- No existing fixture changes meaning.

## What this unit does not admit

- Model resolution, aliases, pricing, and cache refresh. The catalog is a
  listing, not a resolver.
- Provider auth state, which is T5b and stays staged until a consumer needs
  the gate.
- The lower-rung holding the plan describes for a submit whose model condition
  the trace cannot yet judge — a busy session with queueing unavailable, owed
  `model_not_found` rather than `run_active`. The state rung it holds against
  arrives with the queue unit, along with the `queue-busy-*` fixtures the
  plan lists for it; there is nothing to hold against until then.
- Raising OpenCode's `models.list` above `degraded`, which needs a pinned
  response shape for `model.list` in the ledger and a corpus case decoding it
  through the production HTTP client.
- A catalog on any other native adapter. pi's `get_available_models`, Claude's
  `system/init` model list, Makai's `models_request`, and Hermes'
  `model.options` each graduate by amendment when a ledger pins the evidence.

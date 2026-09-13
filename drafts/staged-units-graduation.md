# Staged Units Graduation Plan

Status: proposed design (companion to
[Decision 0003](../decisions/0003-staged-unit-graduation.md))
Date: 2026-09-13
Base protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Prompt: [issue #13](https://github.com/lsm/open-agent-protocol/issues/13)

This document is the per-unit design behind Decision 0003. For each staged
control unit it fixes the wire delta, the validator rules, the reference
adapter behavior, the native evidence and the adapter that graduates first,
the cut through `serve`, the daemon, the stdio frontend, and both clients,
the fixtures, and the exit criteria its own decision will cite. It does not
graduate anything: each unit's decision does that when the gate is met.

The units keep issue #13's labels. The plan's order is Decision 0003's:
T1 run controls, T5a models catalog, T2 queue delivery, T3 tool sources and
control-layer tools, T4 steer, T5b auth state.

## Where v0.1 leaves the staged surface

The wire already carries more than the executable subset accepts:

- `session.message.submit.request` carries `model_id`, `instructions`,
  `tool_choice`, `output_schema`, `allow_degraded_features`, and `metadata`
  ([`schema/v0.1/session.schema.json`](../schema/v0.1/session.schema.json),
  [`protocol/control.go`](../protocol/control.go)). Every executable adapter
  rejects `instructions`, `tool_choice`, and `output_schema` before admission.
  Codex applies `model_id` to `turn/start` and Makai applies it as
  `agent_message.model_ref`, both natively per run and both without
  advertising a model-selection capability; every other adapter rejects it.
- The delivery enums carry `queue`, `steer`, and `btw`; the admission enum
  carries `steered` and `side_started`. Decision 0002 made the queued shape
  canonical for `auto`; explicit `queue` and `steer` requests are rejected by
  every adapter, and the validator's `sessionTrack` holds one active run.
- `ToolDefinition` has `execution_owner` and the `action.call.*` payloads
  carry `interaction_id`, `requested_by`, and `responded_by`, so tool
  ownership is already expressible; no envelope resolves a control-owned call.
- The layered draft names `models.request`/`models.response` and the
  `auth.*` family; none is in the envelope `oneOf`, so the schema rejects them.
- `adapter.Descriptor.MaxActiveRunsPerSession` exists in Go but has no wire
  projection.

## The gate, made concrete

Decision 0003's four steps translate into these exit criteria for every unit:

1. `adapter/memory.go` executes the unit and `adapter/adaptertest` asserts
   it; `oap check` drives the reference path.
2. `validation/state.go` enforces the unit's invariants with new diagnostic
   codes registered in `validation/diagnostic.go` and
   `validation/manifest.go`; `fixtures/manifest.json` lists the unit's
   positive and negative fixtures under a new unit name; every existing
   fixture validates unchanged.
3. One native adapter executes the unit through its production codec and
   reducer with a corpus case pinned to its ledger commit, its descriptor
   advertises the unit at the evidenced level, and its live gate (where one
   exists) exercises it.
4. `serve.Hub`, `serve/servehttp`, the stdio frontend, `client`, and
   `clients/ts` expose the unit in the same change as the hub; the Go and TS
   e2e tests drive it end to end.
5. The unit's decision is accepted; the core draft, the conformance draft, and
   the README move the unit from staged to executable.

## Order and evidence

| Order | Unit | Native evidence in the pinned ledgers | Graduating adapter | Follow-on adapters |
| --- | --- | --- | --- | --- |
| 1 | T1 run controls | Codex `turn/start.model` and Makai `agent_message.model_ref` (native per-run); Claude `set_model` control request and `--model` at spawn; pi model selection commands; ACP `session/set_config_option` model; Hermes `session.create.model`; OpenCode `CreateSession.model`; Makai `agent_start.system_prompt` (session-level) | Codex (`model_id`, native) | Makai (native), Claude (`set_model`, emulated), pi, ACP |
| 2 | T5a models catalog | OpenCode `model.list`/`provider.list`; pi `get_available_models`; Makai `models_request`; Claude `system/init.models` and `list_models`; Hermes `model.options` | OpenCode (native) | pi, Makai, Claude (degraded) |
| 3 | T2 queue delivery | OpenCode `SessionInput.Admitted{delivery:"queue", promotedSeq}`; pi `follow_up` with `queue_update`; Hermes busy `queued` status under `busy_input_mode=queue`; Claude `queued_turn_count`/`still_queued` | OpenCode (native) | pi, Hermes |
| 4 | T3 tool sources | T3a catalog with sources: Claude `system/init` (tools, MCP servers), Codex `mcpToolCall.server`; T3b attach at open: ACP `session/new.mcpServers`, Claude MCP config at spawn and `mcp_set_servers`; T3c control-layer tools: Makai `tool_execute`/`tool_result` bridge, Claude `sdkMcpServers` with `mcp_message`, ACP reverse fs/terminal calls | T3a Claude, T3b ACP, T3c Makai | Codex, Claude |
| 5 | T4 steer | pi `steer` with `queue_update` and injection at a turn boundary; OpenCode `delivery:"steer"` while busy; Hermes `session.steer` and busy `steered` | pi | OpenCode, Hermes; Codex only after a new ledger pin covers `turn/steer` (the pinned Codex ledger defers it; the expected-turn-id detail comes from unpinned research) |
| 6 | T5b auth state | Claude `auth_status` frames; OpenCode `provider.list` | staged | — |

Ledgers: [Codex](../research/codex-app-server-8d7cc24-mapping.md) ·
[Claude Code](../research/claude-code-agent-sdk-2.1.263-mapping.md) ·
[OpenCode](../research/opencode-v1.18.29-mapping.md) ·
[pi](../research/pi-v0.85.1-mapping.md) ·
[Hermes](../research/hermes-v2026.8.31-mapping.md) ·
[ACP](../research/acp-v1.7.0-mapping.md) ·
[Makai](../research/makai-agent-67ad514-mapping.md) ·
[DeepSeek](../research/deepseek-harness-47f9438-mapping.md). DeepSeek
contributes no native evidence to any unit at its pin and advertises each
`unavailable`.

## T1. Run controls

Unit name: `run-controls`. Planned decision: 0004.

### Scope

Graduate the fail-closed control discipline for all four per-submit
controls, and graduate `model_id` as the first control with native evidence.
`instructions`, `tool_choice`, and `output_schema` get executable shapes and
reference-adapter behavior in the same slice; native adapters advertise them
`unavailable` until a ledger pins per-run evidence (today the only per-run
native surfaces are the Codex and Makai model parameters). Nothing here
changes run lifecycle.

### Wire

No new envelope types. Changes to
[`session.schema.json`](../schema/v0.1/session.schema.json) and
[`capabilities.schema.json`](../schema/v0.1/capabilities.schema.json):

- `tool_choice` keeps its permissive schema (`true`), so every envelope
  that validates today still validates. Its executable shape is a typed
  policy over the advertised catalog, enforced by the validator's
  `run-controls` rules and by adapters rather than by the schema: `{
  "mode": "auto" | "none" | "required" | "named", "name"?: string,
  "allowed"?: [string], "disallowed"?: [string] }`, with `name` present
  when and only when `mode` is `named`, and `allowed`/`disallowed` mutually
  exclusive. Precedence is fixed so no two implementations can read one
  policy differently: `allowed` or `disallowed` filters the catalog first,
  then `mode` applies to the filtered set; every entry of `allowed` and
  `disallowed` must name a tool in the advertised catalog (an unknown entry
  is unsatisfiable, not ignored, so a misspelled `disallowed` entry fails
  closed instead of silently blocking nothing), `named` must name a tool
  in the filtered set, and `required` needs a non-empty filtered set;
  otherwise the policy is unsatisfiable and is rejected before admission
  (`unsupported_feature`, `details.reason: "unsatisfiable"`), never
  resolved by choosing one member over another. The catalog a policy is
  judged against is the session's full catalog, including tools the control
  layer provides at open (T3c), so a policy governs those tools the same
  way. In Go,
  `MessageSubmitRequest.ToolChoice` stays `json.RawMessage`;
  `protocol.ToolChoice` (today `Mode` and `Name` only) gains `Allowed
  []string` and `Disallowed []string`, and a strict
  `MessageSubmitRequest.ToolChoicePolicy()` accessor decodes the typed
  shape, rejecting unknown members.
- `output_schema` stays a JSON Schema object. The structured result travels
  in `run.completed.result` (already on the wire) and must validate against
  the admitted schema.
- Capability keys, all optional, gated per submit:

  | Key | Governs | Levels in v0.1 adapters after T1 |
  | --- | --- | --- |
  | `run.model_selection` | `model_id` | Codex and Makai `native` (`per_run`); memory `emulated` (fixed catalog); others `unavailable` until evidence |
  | `run.instructions` | `instructions` | memory `emulated`; others `unavailable` |
  | `run.tool_selection` | `tool_choice` | memory `emulated`; others `unavailable` |
  | `run.structured_output` | `output_schema` | memory `emulated`; others `unavailable` |

  `run.model_selection` is new; the other three are already named in the
  core draft. `FeatureSupport.mode` discloses how an emulated control is
  applied: `per_run` (native per-run parameter), `session_mutation` (a
  serialized native config change applied immediately before the run it
  was requested for starts, which changes the session default), or
  `restart` (not offered in this phase).
- Typed error codes on `error.response`: `unsupported_feature` with
  `details.feature` naming the key and `details.reason` distinguishing the
  two conditions it covers: `unadvertised` (control present, capability
  `unavailable` or absent) and `unsatisfiable` (capability advertised, but
  this request's value of the control cannot be honored, such as a tool
  name outside the catalog or an output schema a fixed-output endpoint
  cannot meet); `capability_degraded` with `details.feature` (capability
  `degraded`, key absent from `allow_degraded_features`);
  `model_not_found` with `details.model_id` (`run.model_selection` advertised,
  id not in the effective catalog). All three already appear in the layered
  draft's vocabulary except `model_not_found`.

### Semantics

- A submit carrying a control the endpoint has not affirmatively advertised
  is rejected before admission, before any native write. No submission or run
  identity is allocated.
- `emulated` controls need no opt-in. `degraded` controls need the key in
  `allow_degraded_features`; otherwise `capability_degraded` wins before
  `unsupported_feature` for a different control on the same request only if
  the degraded control is evaluated first, so evaluation order is fixed:
  `model_id`, `instructions`, `tool_choice`, `output_schema`.
- An admitted `model_id` is authoritative for the run: the submit response
  repeats it in `model_id`, `run.started` repeats it, and `run.completed`
  may not name another model. Absent `model_id`, the response reports the
  effective model when known, as today.
- `current_model_id` in session state is the model the next `auto`
  submission without `model_id` would use. A `per_run` application leaves it
  unchanged; a `session_mutation` application changes it, and the adapter
  reflects the change rather than restoring the previous default.
- A `session_mutation` application runs immediately before the run it was
  requested for starts, never while another run is started, because the
  started run's admitted model is authoritative until its terminal. For a
  submit admitted `start` that is before admission, as today. For a submit
  admitted `queued` (once T2 is executable) the adapter validates the id
  against the catalog at admission but applies the mutation at promotion:
  `current_model_id` keeps reporting the started run's model until then, a
  queued run cancelled or dropped before start never applies its mutation,
  and a mutation that fails at promotion settles the run pre-start as
  `run.failed` with the typed error admission would have produced. An
  adapter that cannot defer the mutation rejects the combination
  (`run_active`) rather than mutating under a started run.
- `output_schema` binds the run's final response: `run.completed.result` is
  present and conforms, or the run fails with `structured_output_failed`.
  A harness that retries structured output natively (Claude Code's
  `error_max_structured_output_retries`) maps exhaustion to that failure.

### Validator

- `session.message.submit.request` with any control invokes the existing
  `feature()` gate with the control's key (diagnostic
  `unavailable_capability` when unadvertised or `unavailable`).
- New diagnostic `unapplied_control`: the submit response's `model_id` (when
  the request carried one) differs from the request; `run.started.model_id`
  differs from the admitted model (today's `illegal_run_transition` case
  moves to this code); `run.completed` under an admitted `output_schema`
  lacks `result`, or carries a `result` that does not validate against the
  admitted schema (the validator compiles the schema with the same
  `jsonschema` engine it already uses for the bundle); an
  `action.call.requested` in a run whose admitted `tool_choice` excludes
  that tool (`mode: "none"`, filtered out by `allowed` or `disallowed`, or
  `named` naming another tool), whichever participant owns the call.
- New diagnostic `unsatisfiable_control`: a `tool_choice` that is not the
  typed policy, carries both `allowed` and `disallowed`, names a tool in
  its own `disallowed` list or outside its own `allowed` list, or, when the
  trace carries a catalog (capabilities or `action.tools.list.response`,
  plus any `tools` provided at open), lists or names a tool outside that
  catalog or is `required` or `named` against an empty filtered set. The
  validator and the reference adapter therefore reject the same policies:
  a policy the unit's rules accept is admitted by the reference, and one
  the reference refuses is diagnosed. The check runs only when the submit
  carries the control, so envelopes that do not use the unit are untouched.
- `runState` gains `controls` (the admitted request's control set) so the
  checks above are keyed off the request, not the response.

### Reference adapter

`adapter/memory.go` advertises `run.model_selection` (`emulated`, catalog of
two fixed ids), `run.instructions`, `run.tool_selection`, and
`run.structured_output` (`emulated`). It echoes the admitted model on the
response and `run.started`; prepends `instructions` to the scripted text so
the effect is observable; and applies every `tool_choice` member: `mode:
"none"` skips the scripted tool, a `disallowed` list naming `scripted_tool`
skips it, an `allowed` list that omits `scripted_tool` skips it, `mode:
"required"` or `mode: "named"` with `name: "scripted_tool"` calls it, and a
`named` choice or list entry naming a tool outside its catalog fails before
admission with `unsupported_feature` (`details.reason: "unsatisfiable"`).
Its structured
result is the fixed object `{"ok": true}`: at admission it compiles the
requested `output_schema` and rejects, before any identity is allocated,
any schema that object does not satisfy (`unsupported_feature`,
`details.feature: "run.structured_output"`, `details.reason:
"unsatisfiable"`); under an accepted schema `run.completed` carries that
object as `result`, so the reference never emits a nonconforming success.
The descriptor's `reason` for `run.structured_output` discloses the fixed
result. Unknown model ids fail with `model_not_found`.

### Native evidence

Codex graduates `model_id` at `native` (`per_run`): its adapter already passes
`request.ModelID` to `turn/start`; T1 adds the capability key, a corpus case
`model-per-turn` (the `turn/start` frame carrying the requested model and
`turn/started` attributed to it), the rejection case for an unadvertised
`instructions`, and a fix for the same `current_model_id` overwrite Makai
has: the Codex session today assigns the per-turn model to
`state.CurrentModelID`, which the `per_run` rule above forbids, so the
session default stays the configured thread model and the per-run model
lives on the admission response and `run.started` only. Makai lands in the
same slice as the second native source: its adapter already sends
`model_ref` on every `agent_message` and echoes it, so T1 adds the key and
a `model-per-message` corpus case, and stops the adapter overwriting
`current_model_id` with a per-run value in the same way. Follow-on
evidence for the other adapters is gated on new ledger entries: Claude
`set_model` (`session_mutation`), pi `set_model` (`session_mutation`), ACP
`session/set_config_option` (`session_mutation`, degraded because
attribution is unsafe under concurrent changes). A `session_mutation`
adapter that also advertises `session.message.delivery.queue` (pi is the
first candidate) must show the deferred application in its corpus: a
queued submit naming another model while a run is started, with the
native mutation frame appearing only after the started run's terminal.

### Surfaces

- `adapter`: new `*adapter.UnsupportedControlError{Feature}` and
  `*adapter.DegradedControlError{Feature}` wrapping `ErrUnsupportedInput`, so
  codecs can emit the typed codes; `ErrModelNotFound`.
- `serve/servehttp` and the stdio frontend: `writeSubmitError` maps the new
  errors to `unsupported_feature`, `capability_degraded`, and
  `model_not_found` with `details`; the current `invalid_submission` mapping
  stays for malformed input.
- `client` and `clients/ts`: no new operations; `ServerError` exposes
  `details`; e2e tests drive a `model_id` submit against the memory adapter
  and assert the echoed model on `run.started`.

### Fixtures

Positive: `controls-model-admitted`, `controls-instructions-emulated`,
`controls-structured-output`. Negative: `controls-unadvertised-model`
(`unavailable_capability`), `controls-model-mismatch` (`unapplied_control`),
`controls-structured-missing-result` (`unapplied_control`),
`controls-structured-nonconforming-result` (`unapplied_control`; `result`
present but invalid against the admitted schema),
`controls-tool-choice-contradictory` (`unsatisfiable_control`; `required`
with the only tool disallowed, and `named` outside its own allowlist),
`controls-tool-choice-unknown-entry` (`unsatisfiable_control`; an
`allowed` list naming a tool outside the catalog the trace carries),
`controls-tool-choice-ignored` (`unapplied_control`; `action.call.requested`
under `mode: "none"`),
`controls-degraded-without-optin` (`error.response` with
`capability_degraded` then no admission; validated as a correct rejection).

### Exit criteria

The five gate items; the Codex and Makai descriptors advertise
`run.model_selection`; the core draft's "Message Submit And Run Admission" section marks `model_id`
executable and the other three "shape frozen, evidence pending".

## T5a. Models catalog

Unit name: `models`. Planned decision: 0005.

### Scope

Graduate `models.request`/`models.response` as a session-scoped control-plane
query. Model resolution, aliases, pricing, and cache refresh stay out.

### Wire

New envelope types `models.request` and `models.response` added to the
envelope `oneOf`, with their payload definitions in
[`capabilities.schema.json`](../schema/v0.1/capabilities.schema.json)
beside the other control-plane payloads (initialize and capabilities), so
the bundle's file inventory and the manifest schema are untouched.

```json
{ "type": "models.request", "payload": { "session_id": "s1" } }
{ "type": "models.response", "in_reply_to": "…", "session_id": "s1",
  "capability_revision": "…",
  "payload": {
    "session_id": "s1",
    "current_model_id": "provider/model-a",
    "models": [
      { "id": "provider/model-a", "display_name": "Model A",
        "provider_id": "provider", "context_window": 200000,
        "features": { "model.reasoning.output": { "level": "native" } } }
    ]
  } }
```

`ModelDescriptor` fields: `id` (required, the value `model_id` accepts),
`display_name`, `provider_id`, `context_window`, `features` (the layered
draft's `model.*` keys as `FeatureSupport`), `default` (boolean; at most one
per response). `current_model_id` repeats session state. Capability key:
`models.list` (already named). The catalog is part of the capability
snapshot: a catalog change is a `capabilities.updated` invalidation on
endpoints that advertise `capabilities.updates`, and a static endpoint may
serve one catalog for its lifetime.

### Semantics

- `models.request` is session-scoped because every native source is a
  process or session surface (pi, Claude, Makai, ACP) or trivially scoped to
  one (OpenCode). The response is the effective catalog for that session.
- A `model_id` accepted under `run.model_selection` must be a listed `id`
  when `models.list` is advertised; otherwise `model_not_found`.
- Degraded catalogs disclose why: Claude's is the recurring `system/init`
  model list (`degraded`, refreshed per turn), Makai's is `unavailable` until
  its failure behavior is exercised.

### Validator

- `models.request` and `models.response` join the request/response
  correlation and scope checks; the response must repeat the request's
  session.
- New diagnostic `model_not_in_catalog`: an admitted `model_id` after a
  `models.response` in the same trace names an id the response did not list.
- The `features` map on a descriptor is collected into the trace's catalog
  for the `model_not_in_catalog` check only; it does not gate run events.

### Reference adapter

`adapter/memory.go` lists its two fixed ids with `default` on the first;
`Models` is deterministic and revision-stable.

### Native evidence

OpenCode graduates first: `model.list` and `provider.list` are pinned native
catalog routes (not yet in the adapter's HTTP client, which today calls only
session, prompt, and event routes); the corpus case `models-list` decodes the
pinned response shape through the production HTTP client and projects
`ModelDescriptor` records with `provider_id` from the provider list.
pi follows with `get_available_models`; Claude at `degraded` from the
`system/init` frame; Makai stays `unavailable` at its pin.

### Surfaces

- `adapter`: optional interface `adapter.ModelLister` on `Session`
  (`Models(ctx) (protocol.ModelsResponse, error)`), discovered by type
  assertion so existing `Session` implementations compile unchanged; the hub
  returns `unsupported_feature` for sessions whose adapter lacks it.
- `serve`: `Session.Models(ctx)`.
- `serve/servehttp`: `GET /sessions/{id}/models` returning `models.response`
  with a daemon-minted correlation id, mirroring `GET
  /adapters/{name}/capabilities`; stdio op `models`.
- `client`: `Session.Models(ctx)`; `clients/ts`: `session.models()`.

### Fixtures

Positive: `models-list-then-select` (catalog, then a submit selecting a
listed id). Negative: `models-select-unlisted` (`model_not_in_catalog`),
`models-response-scope-mismatch` (`scope_mismatch`).

### Exit criteria

The five gate items; OpenCode advertises `models.list` `native`; the
conformance draft's `+models` unit text cites the fixtures.

## T2. Queue delivery

Unit name: `queue`. Planned decision: 0006.

### Scope

Graduate explicit `delivery: "queue"` requests and the `auto` to `queue`
resolution on a busy session, which means admitting a second nonterminal run
per session: one started run plus bounded queued reservations. Decision 0002
already fixed the queued admission shape and pre-start settlement; T2 adds
overlap, ordering, and state.

### Wire

No new envelope types. Additive fields:

- `session.state` gains `active_runs`: an ordered list of every nonterminal
  run, `[{ "run_id", "status", "relationship": "primary", "queue_position"?
  }]`, in admission order, with `queue_position` on queued entries
  (1-based). `active_run_id` keeps naming the started run, or is absent when
  only queued runs remain (session status `queued`).
- `capabilities.response` gains optional `limits`: `{
  "max_active_runs_per_session": int, "max_queued_runs_per_session": int }`,
  the wire projection of `adapter.Descriptor.MaxActiveRunsPerSession` plus
  the queue bound. Absent means one started run and at least one queued run.
- Typed error `run_active` (the daemon's existing code, adopted as the
  protocol code; the research draft's `session_busy` name is superseded): a
  submission that cannot be admitted because the session is busy and no
  advertised busy outcome applies, or the queue bound is reached. It is
  returned before any identity is allocated.

### Semantics

- Explicit `queue` on an idle session is admitted `queued` and promotes
  immediately; "run after current work reaches a safe boundary" is trivially
  satisfied. Explicit `queue` never resolves to `start`.
- `auto` on a busy session resolves to `queue` only when
  `session.message.delivery.queue` is advertised; otherwise `run_active`.
  `delivery_resolution` reports `session_busy` for that resolution.
- Promotion is in admission order: a queued run may emit `run.started` only
  when every earlier-admitted run in the session is terminal. Only one run
  per session is started at a time; Decision 0001's cancellation scope
  therefore still targets one execution.
- A queued run cancels pre-start under Decision 0002; a queued run whose
  harness drops it before start settles `run.failed` with a typed
  `queue_dropped` error.
- Every queued reservation is listed in `active_runs` until its terminal;
  reconnect state preserves the order.
- A queued submit's `session_mutation` control (T1) is applied at
  promotion, not at admission, so the started run's model stays
  authoritative; the T1 semantics fix the failure and cancel paths.
- Delivery order on a session stream is one run domain at a time, in
  admission order: a later-admitted run's envelopes, including a queued
  run's pre-start terminal, are delivered only after every earlier-admitted
  run's terminal. The events themselves are unchanged (timestamps and the
  state snapshot say when a queued run actually settled); this is a rule
  about the ordered timeline a binding presents, and it keeps a single
  `(run, sequence)` cursor sufficient for any consumer.

### Validator

- `sessionTrack.active` becomes an ordered set of nonterminal runs with
  admission indices. A second admission on a session with a nonterminal run
  is legal only when the new admission is `queued` and the trace's descriptor
  advertises `session.message.delivery.queue`; otherwise the existing
  `illegal_run_transition` ("session already has a nonterminal run").
- New diagnostic `queue_order_violation`: `run.started` for a queued run
  while an earlier-admitted run in the session is nonterminal.
- New diagnostic `premature_session_mutation`: when the descriptor discloses
  `run.model_selection` with mode `session_mutation`, a `session.state`
  snapshot taken while a run is started reports a `current_model_id` other
  than that run's admitted model.
- `session.state` snapshots: `active_runs`, when present, must list exactly
  the tracked nonterminal runs in admission order with consistent
  `queue_position`; `active_run_id` must be the started run; otherwise
  `session_state_mismatch`.

### Reference adapter

`adapter/memory.go` advertises `session.message.delivery.queue` `emulated`
with `limits.max_queued_runs_per_session = 1`: a submit while the scripted
run is active reserves a second run (`queued`), lists both in `active_runs`,
and promotes it when the first settles; cancelling the queued run settles it
pre-start.

### Native evidence

OpenCode graduates first: `SessionInput.Admitted` with `delivery: "queue"`
and `promotedSeq` is the strongest native queue shape in the ledgers, and the
three Decision 0002 cases already pin the reservation. New corpus cases:
`queue-explicit-busy` (queue admitted while a run is active, promoted after
the first run's derived settlement), `queue-cancelled-before-promotion`.
Follow-on: pi `follow_up` (with `queue_update` as the queued-state
observation), Hermes `queued` under `busy_input_mode=queue`.

### Surfaces

- `serve`: subscriptions deliver one run domain at a time in admission
  order. The hub already drains every run's adapter stream independently
  and numbers runs by admission serial; T2 adds holding a later-admitted
  run's envelopes until the earlier run's terminal has been delivered, so
  a queued run's pre-start terminal never interleaves with the started
  run's events on any subscription. A subscription's replay cursor already
  carries `(RunID, AfterSequence)`: resume replays that run's retained
  suffix and then continues into later-admitted runs in order, and the hub
  stops assuming the newest admission is the run a bare sequence refers
  to. Both clients' single scalar cursor therefore stays correct: the run
  it names is always the only run in flight on the stream.
- `serve/servehttp`: the SSE `id:` field stays the bare sequence, because
  both v0.1 clients parse it as an unsigned integer (`strconv.ParseUint` in
  Go, `/^\d+$/` in TypeScript) and a qualified id would break them on the
  first event. Run identity for a cursor travels in an additive `?run=`
  query parameter beside `?after=`; a cursor without `run` resolves onto
  the session's started run, exactly today's behavior. `oap-overflow` and
  `oap-replay-gap` both carry `run_id` (overflow already does) so a client
  can resume the right run. The stdio frontend's `events` op gains the same
  optional `run` parameter. `?run=` is still needed even with ordered
  delivery: a drop between one run's terminal and the next run's first
  envelope leaves the client holding the finished run's cursor while the
  next run is already the started one. Stream end changes with it: today
  the daemon ends a stream at a run terminal unless a resubmit already
  landed, and both clients read a terminal as the stream's clean end. Under
  T2 the daemon continues past a terminal into any later-admitted run's
  domain, and when it ends a stream on purpose (a terminal with no
  later-admitted run, or the session closed) it writes an explicit
  `event: oap-stream-end` signal (`run_id`, `last_sequence`) before
  closing; the stdio frontend writes the same named line beside its
  `oap-session-closed`. Both clients already skip named events they do not
  define, so the signal is additive. A v0.1 client driving a session
  alone never has two nonterminal runs on it (a second submit while busy is
  refused `run_active` before any queue exists), so its bare cursors keep
  binding the same run they do today; a session shared with a queue-aware
  client sees the queued run's terminal only after the started run's, in
  its own domain, which the v0.1 clients already handle as a run switch.
- `client` and `clients/ts`: both already track the run of the last
  observed envelope (`EventStream.runID`, `EventStream.runId`) and expose
  `EventsAfter(RunID, LastSequence)` / `eventsAfter`; the changes are to
  send that run as `?run=` on reconnect and to stop treating a terminal
  envelope as the end of the stream. A terminal ends the run; the stream
  ends on `oap-stream-end` (or a closed session). A connection that drops
  after a terminal without that signal is a drop like any other: the
  client resumes with the finished run's cursor and the daemon either
  continues into the later-admitted run or answers with the end signal.
  Against an older daemon, which closes at the terminal with no signal, the
  client bounds that post-terminal resume to one attempt and then reads
  `session.state`: an empty or absent `active_runs` is the clean end,
  anything else keeps resuming. The resume checks change in exactly one
  case to make this work: today a cursor-bearing connection whose first
  envelope names another run fails with `ResumeMismatchError`, and one
  whose first envelope is not `lastSeq+1` fails with `SequenceGapError`.
  Under T2, when the cursor's run is terminal (both clients already track
  `terminal`), the first resumed envelope may instead be sequence 1 of a
  different run: the client adopts that run as the new domain and resets
  its sequence expectation to it. A different run at any other sequence,
  or a different run while the cursor's run is nonterminal, still fails
  as today, so the daemon's answer to `?run=A&after=<A's terminal>` is
  either the end signal or run B from sequence 1 and nothing else. Both
  clients' e2e tests drop the connection between a run's terminal and the
  queued run's first envelope and assert the continuation. Because
  delivery is one run domain at a time, no per-run cursor table is needed:
  a run switch always follows a terminal, and on a live connection both
  clients already accept it.
- Daemon-management listing (`GET /sessions`) reports `active_runs`.

### Fixtures

Positive: `queue-explicit-idle-promoted`, `queue-busy-then-promoted`,
`queue-busy-cancelled-prestart`, `queue-state-active-runs`,
`queue-model-mutation-at-promotion` (a `session_mutation` descriptor, a
queued submit naming another model, `current_model_id` unchanged until the
first run's terminal). Negative:
`queue-promoted-out-of-order` (`queue_order_violation`),
`queue-model-mutation-early` (`premature_session_mutation`),
`queue-overlap-unadvertised` (`illegal_run_transition`),
`queue-state-missing-reservation` (`session_state_mismatch`).

### Exit criteria

The five gate items; OpenCode advertises `session.message.delivery.queue`
`native`; Decision 0001's "at most one nonterminal run per session" is
amended to "at most one started run and an advertised number of queued
reservations".

## T3. Tool sources and control-layer tools

Unit names: `tool-sources` (T3a and T3b) and `control-tools` (T3c). Planned
decision: 0007 (one decision, three sub-units, each independently
advertisable).

### T3a. Catalog with sources

Wire, in [`action.schema.json`](../schema/v0.1/action.schema.json) and
[`capabilities.schema.json`](../schema/v0.1/capabilities.schema.json):

- `ToolSourceDescriptor`: `{ "id", "kind": "native" | "local" | "process" |
  "remote" | "hosted", "display_name"?, "protocol"?, "endpoint"? }`, the
  shape of [`examples/tool-source.json`](../examples/tool-source.json).
  MCP sources are `{ "kind": "process" | "remote", "protocol": "mcp",
  "endpoint": "stdio:…" | "https://…" }`.
- `action.tools.list.response` gains `sources: [ToolSourceDescriptor]`;
  `ToolDefinition` gains `source` (a source `id`, never an inline copy) and
  `features` (per-tool `FeatureSupport` map, as in the example). The
  example is aligned with this contract in the same change: it is now a
  session-scoped request and response pair on the flat envelope, each tool
  carries its source id rather than an inline descriptor copy, and each
  carries `execution_owner`. The
  capability descriptor's `tools` and `layers.*.tools` gain `sources` beside
  them.
- `action.call.*` payloads gain optional `source` (the source `id`), so a
  consumer can attribute a call to an MCP server without parsing names.
- `action.tools.list.request` gains an optional `session_id` payload
  member, and both list envelopes carry the envelope `session_id` when the
  catalog is a session's effective catalog; the response repeats it in its
  payload. Today's empty request stays valid for an endpoint-level catalog
  (a static adapter). A session opened with `tool_sources` or `tools`
  answers session-scoped lists only, so a trace, a stdio consumer, and the
  validator can tie a catalog to the attachment it reflects.

Semantics: a tool naming a source must name one the same response declared,
and a call carrying `source` must name the source the session's catalog
records for that tool, or a declared source when the tool is not in the
catalog (validator diagnostic `unmatched_tool_source` for both); the first
session-scoped
list for a session opened with `tool_sources` or `tools` must declare every
attached source and provided tool (diagnostic `catalog_mismatch`), and the
usual scope agreement applies to both list envelopes (`scope_mismatch`); a
source is described, not managed, by this sub-unit; the harness runs the
client. Capability: `action.tools.list` (existing) with sources present.

Evidence: Claude `system/init` carries the tool list and MCP server list per
turn (`degraded` catalog, per-turn refresh); Codex `mcpToolCall.server` gives
the source on the call but no catalog (`source` on calls only). Claude
graduates T3a; its corpus case `tools-catalog-sources` projects the
`system/init` frame into `action.tools.list.response`.

### T3b. Attachment at session open

Wire: `session.open.request` gains `tool_sources: [ToolSourceDescriptor]`.
A `process` source additionally carries `command`, `args`, and
`environment` (the registry's allowlist form: bare `NAME` forwards from the
endpoint's own environment, `NAME=value` passes literally); a `remote`
source carries only `endpoint` and is capability-gated (`mode: "remote"`).
A bare `NAME` resolves only if the adapter's registry entry allowlists it,
so a wire caller cannot read an ambient credential the operator did not
expose; a `NAME=value` literal is the caller's own secret on a loopback,
single-user wire, exactly as it is for the registry document today.

Semantics: attachment is for the session's lifetime; the open response's
state and the first `action.tools.list.response` reflect the attached
sources; an endpoint that cannot attach at open rejects the open with
`unsupported_feature` (`action.tool_sources.attach`). Runtime attach and
detach are deferred until evidence beyond Claude's `mcp_set_servers` exists.

Evidence: ACP `session/new.mcpServers` is a required array
(`{ name, command, args, env }` entries) that the adapter already fills from
its Go configuration; T3b moves that descriptor onto the wire at open, with
the adapter resolving allowlisted names into ACP's literal `env` values.
Claude spawns with MCP config. ACP graduates T3b with the corpus case
`open-with-tool-sources` (a stdio MCP descriptor passed through
`session/new`) and its live gate against docker/cagent.

### T3c. Control-layer-provided tools

Wire:

- `session.open.request` gains `tools: [ToolDefinition]` whose
  `execution_owner` is the opening participant. Per-submit tool provisioning
  (Makai supplies tools per `agent_start`) is deferred; session-open is what
  Claude and ACP support and what Makai can accept at start.
- New envelope types `action.call.resolve.request` and
  `action.call.resolve.response`, session- and run-scoped, mirroring the
  permission resolve pair: request `{ "interaction_id", "session_id",
  "run_id", "tool_call_id", "requested_by", "responded_by", "started"? |
  "result"? | "error"? }` with exactly one of `started` (an empty object:
  the control participant has begun executing), `result`, or `error`;
  response `{ "interaction_id", "session_id", "run_id", "tool_call_id",
  "accepted" }`. `started` is an acknowledgement, not a resolution: it may
  appear at most once and only before the resolution.
- New capability key `action.tools.provide`: the control layer may supply
  tool definitions at session open and executes their calls. The existing
  `action.tools.execute` keeps the meaning the `+tools` unit gives it
  (normalized harness-side execution), so an old client reading a new
  descriptor and a new client reading an old descriptor both interpret it
  as today; only the new key gates control-owned execution.

Semantics, on the interaction contract Decision 0001 fixed:

- A call to a control-owned tool is an interaction: `action.call.requested`
  carries `interaction_id`, `requested_by` (the agent), `responded_by` and
  `execution_owner` (the control participant). The call stays `requested`
  until execution is evidenced: the adapter emits `action.call.started`
  when the control participant acknowledges with a `started` resolve, or,
  when the participant resolves with `result` or `error` without having
  acknowledged, immediately before the terminal it derives from that
  resolution (the result is the evidence). `started` is never emitted on
  the adapter's own initiative, so durations and cancellation keep the
  meaning the core gives them; a repeated or late acknowledgement is
  answered `accepted: false` and emits nothing. The validator's transition
  table is unchanged.
- Only the declared responder may resolve; one resolution; the adapter emits
  `action.call.completed` or `action.call.failed` from the resolution and
  feeds the result to the harness through its native bridge.
- Run cancel closes pending control-owned calls with
  `action.call.cancelled` before the run terminal, as the memory adapter
  already does for permission gates; an unacknowledged call goes from
  `requested` to `cancelled`, which the transition table already permits.
  Deadlines remain deferred (PF-2); a harness-side timeout settles an
  unacknowledged call as `cancelled` with the harness's code as the reason
  and an acknowledged one as `failed` with that code.
- A control-owned call is never routed to a harness-side executor, and a
  harness-owned call is never resolvable from the control layer
  (`wrong_interaction_responder`).
- Provided tools join the session's catalog: they are listed by
  `action.tools.list.response` with the opener as `execution_owner`, and a
  submit's `tool_choice` (T1) governs them like any other catalog tool. A
  run whose admitted policy excludes a provided tool never emits
  `action.call.requested` for it (`unapplied_control` otherwise); an
  adapter that cannot withhold a provided tool from the harness for one run
  rejects such a policy as unsatisfiable rather than exposing the tool.
- `tools` is accepted whole or not at all: an adapter that cannot provision
  every supplied definition rejects the open with `unsupported_feature`
  (`details.feature: "action.tools.provide"`, `details.reason:
  "unsatisfiable"`) rather than accepting a subset, and no adapter carries
  an unadvertised cardinality limit. The capability key alone therefore
  tells a caller that any well-formed `tools` array is honored.

Evidence: Makai's `tool_execute`/`tool_result` bridge is exactly this
boundary: the adapter's native codec already decodes both frames, every
`agent_message` already carries a `tools` list (empty today), and the ledger
names `tool-bridge-roundtrip` as a required case that the pinned corpus does
not yet contain. Makai graduates T3c by adding that case and advertising
`action.tools.provide`; Claude `sdkMcpServers` with
`mcp_message` reverse control and ACP reverse fs/terminal calls follow.

### Where the MCP client lives

Adapter-side passthrough (T3a and T3b) first: the harness owns the MCP
client; OAP describes, attaches, and observes. Serve-side connector second,
as a `serve` feature on T3c: the hub hosts an MCP client as one more
execution owner, provisions its tools through `session.open.request.tools`,
routes `action.call.requested` whose `execution_owner` is the hub to the MCP
server (acknowledging `started` as it dispatches), and resolves through
`action.call.resolve.request`. The connector
serves adapters with no native MCP support (DeepSeek, pi, memory) and the
HyperNeo-style embedding; it adds no wire vocabulary.

### Validator

- `action.tools.list.response`: `unmatched_tool_source` for a tool naming an
  undeclared source. `action.call.*` payloads carrying `source`: the same
  diagnostic when the trace's catalog lists the named tool under a
  different source, or when the tool is not listed and the source is
  undeclared; the validator keeps the catalog per session (`sessionTrack`
  gains `tools`), so attribution to the wrong MCP server is caught even when
  both sources are declared.
- `action.call.requested` with `execution_owner` equal to a declared control
  participant must carry `interaction_id` and `responded_by`
  (`illegal_tool_transition`); `action.call.started` for such a call must
  be preceded by an `action.call.resolve.request` for its interaction in
  any arm (`illegal_tool_transition` otherwise, so an adapter cannot record
  execution the control participant has not evidenced); its resolution
  follows the interaction rules (`unmatched_interaction`,
  `duplicate_interaction`, `wrong_interaction_responder`,
  `pending_interaction_at_terminal`).
- `action.call.resolve.request` and `.response` join correlation and scope
  checks; the resolution's `tool_call_id` must match the interaction's
  binding (`scope_mismatch`).
- `session.open.request` with `tool_sources` or `tools` invokes the
  `feature()` gate with `action.tool_sources.attach` or
  `action.tools.provide`.

### Reference adapter

`adapter/memory.go` declares two sources (`native` for `scripted_tool`, a
synthetic `process`/`mcp` source), accepts `tool_sources` at open and lists
them, accepts every control-owned `ToolDefinition` supplied at open (no
cardinality limit; all are listed), and after the permission gate calls the
first provided tool in open order that the run's admitted `tool_choice`
selects: `requested` with the opener as owner, `started` when the opener
acknowledges (or immediately before the terminal when it resolves without
acknowledging), then the terminal from `Resolve`; a cancel before the
acknowledgement settles the call `cancelled` from `requested`. The provided
tools join the reference catalog next to `scripted_tool`, so the T1 rules
apply to them unchanged: `mode: "none"` or a filter that excludes them
skips the call, and `named` naming one of them calls that one.
`InteractionResolution` gains a third arm `ToolCall
*protocol.ActionCallResolveRequest` carrying whichever of the three arms
the request used.

### Surfaces

- `adapter`: `OpenRequest` gains `ToolSources` and `Tools`;
  `InteractionResolution.ToolCall`; optional interface `adapter.ToolLister`
  (`Tools(ctx) (protocol.ToolsListResponse, error)`) for the catalog.
- `serve`: `Session.Tools(ctx)`; `Session.Resolve` passes the new arm; the
  optional MCP connector as a separate package `serve/mcpconnect`, out of
  scope for the 0007 decision but designed against it.
- `serve/servehttp`: `GET /sessions/{id}/tools` returning
  `action.tools.list.response`; `POST /sessions/{id}/resolve` accepts
  `action.call.resolve.request`; `POST /adapters/{name}/sessions` forwards
  `tool_sources` and `tools`. Stdio ops `tools` and the extended `resolve`
  and `open`.
- `client` and `clients/ts`: `Open` options for sources and tools;
  `Session.Tools`; `Session.ResolveToolCall` in all three arms
  (acknowledge, result, error), so a control layer can report that it has
  begun executing before it has a result.

### Fixtures

Positive: `tools-catalog-with-sources`, `open-attach-process-source`,
`control-tool-roundtrip` (acknowledgement, then result),
`control-tool-resolved-without-ack` (`started` immediately before the
terminal), `control-tool-cancelled-with-run` (an unacknowledged call
settles `cancelled` from `requested`),
`control-tool-withheld-by-tool-choice` (a provided tool, a submit with
`mode: "none"`, no call), `open-provide-two-tools` (both listed, the
selected one called). Negative:
`tools-unmatched-source` (`unmatched_tool_source`),
`tools-call-source-mismatch` (`unmatched_tool_source`; a call naming one
tool with another tool's declared source),
`control-tool-started-before-ack` (`illegal_tool_transition`),
`control-tool-called-despite-none` (`unapplied_control`),
`control-tool-wrong-owner` (`wrong_interaction_responder`),
`control-tool-pending-at-terminal` (`pending_interaction_at_terminal`),
`open-attach-unadvertised` (`unavailable_capability`).

### Exit criteria

The five gate items per sub-unit; Claude, ACP, and Makai advertise their
sub-unit; the profile draft's feature-gate row for
`action.tool_sources.attach` and a new row for `action.tools.provide` cite
the fixtures; the daemon README documents the credential rule for process
sources.

## T4. Steer

Unit name: `steer`. Planned decision: 0008. This section is a pre-design for
that decision, not a settled shape: it is the one unit that adds a new
pending lifecycle inside a run, and its decision must be written from a pi
corpus case first.

### The question

`steer` injects guidance into an active run. Unlike `queue`, it creates no
run and no sequence domain of its own, so the control layer cannot observe
from `run.started` that its guidance took effect. Every native source makes
the application observable in a different way: pi at the next turn boundary
(`queue_update` shrinks and the injected user message appears in the
stream), OpenCode when the steered input is promoted (`prompted` after
`prompt.admitted`), Hermes immediately (the busy result is `steered`), and,
per unpinned research only, Codex on the `turn/steer` response against an
expected active turn (the pinned Codex ledger defers `turn/steer`, so this
is not evidence until a new pin covers it). pi also lets a
steer be withdrawn before application (`clear_queue`). The protocol
therefore needs a steer settlement, not only a steer admission.

### Proposed shape

- Admission: `session.message.submit.request` with `delivery: "steer"` and
  optional `target_run_id` (additive field). Absent, the target is the
  session's started run; a supplied target must be that run. Any other
  target, or no started run, fails before admission with typed
  `invalid_steer_target` (`details.reason`: `no_active_run`, `terminal`,
  `queued`, `cross_session`, `not_steerable`).
- Response: `admission: "steered"`, `effective_delivery: "steer"`, `run_id`
  set to the target, `status` equal to the target's current status, and the
  `submission_id` that names the pending steer.
- Settlement, two new run-scoped events in the target run's sequence domain:
  `run.steer.applied` `{ "session_id", "run_id", "submission_id",
  "message_ids", "boundary": "immediate" | "turn" | "tool_result" |
  "unknown" }` and `run.steer.dropped` `{ "session_id", "run_id",
  "submission_id", "reason": ProtocolError }`. A harness that applies
  immediately emits `applied` atomically with the response.
- Barrier: every admitted steer settles before the run terminal; a run that
  terminates first drops its pending steers with `run_terminated` before the
  terminal, in the run's sequence.
- `auto` on a busy session never resolves to `steer`; steering is always an
  explicit request (P0.4's rule that explicit modes never change meaning is
  kept, and an accidental steer is worse than a refused submit).

### Validator

`runState` gains `steers` (pending submissions); `steered` admissions are
legal only against a started nonterminal run in the same session with
`session.message.delivery.steer` advertised; `applied`/`dropped` must name a
pending steer once (`unmatched_steer`, `duplicate_steer`); a terminal with a
pending steer is `pending_steer_at_terminal`.

### Reference adapter and evidence

`adapter/memory.go` accepts a steer while waiting at the permission gate and
applies it at the input gate (`boundary: "turn"`), which also exercises the
drop path under cancel. pi graduates: its corpus already records `steer`,
`queue_update`, and injection as codec evidence (`native-controls`,
`steer-injected` labels); the new case executes them through `Submit` with
an advertised `session.message.delivery.steer`. OpenCode and Hermes
follow from their pinned ledgers; Codex follows only once a new ledger pin
covers `turn/steer`.

### Surfaces

No new operations: `submit` carries the delivery; the events are stream
envelopes. The hub's run-qualified cursor from T2 already covers a steer's
events because they live in the target run's domain.

### Questions the 0008 decision must answer from evidence

1. Event naming: `run.steer.applied`/`run.steer.dropped` versus a general
   `run.submission.*` pair that queue promotion could share.
2. Whether the steered messages are echoed as `content.delta` with
   `role: user` in the run stream or referenced by `message_ids` only.
3. Whether `boundary` is worth carrying or `applied` alone suffices.
4. Codex's expected-turn precondition: is `target_run_id` required when the
   harness requires a target, or does the adapter fill it from the started
   run. This is answered by the ledger pin that first covers `turn/steer`,
   not by the current one.

## T5b. Auth state

Unit name: `auth`. Not scheduled. `auth.providers.request`/`.response` as a
read-only listing (`[{ "provider_id", "state": "authenticated" |
"unauthenticated" | "expired" | "unknown", "display_name"? }]`) has evidence
in Claude's `auth_status` frames and OpenCode's `provider.list`. Login flows
(`auth.login.*`) have no pinned evidence and stay in the profile draft. The
unit is designed when a consumer needs to gate a model picker on auth state;
until then `auth_required` on `run.failed` (already a standard code) is the
executable surface.

## Cross-cutting vocabulary

### Capability keys introduced or given executable meaning

| Key | Unit | Note |
| --- | --- | --- |
| `run.model_selection` | T1 | new; `mode` discloses `per_run` or `session_mutation` |
| `run.instructions`, `run.tool_selection`, `run.structured_output` | T1 | existing names, executable gate |
| `models.list` | T5a | existing name |
| `session.message.delivery.queue` | T2 | existing name; `limits` adds the bound |
| `action.tools.list` with sources | T3a | existing name |
| `action.tool_sources.attach` | T3b | existing name; `mode` discloses `session_open` and `remote` |
| `action.tools.provide` | T3c | new; `action.tools.execute` keeps its `+tools` meaning |
| `session.message.delivery.steer` | T4 | existing name |

### Typed error codes on `error.response`

`unsupported_feature`, `capability_degraded` (already in the layered draft);
`model_not_found`, `run_active` (adopted from the daemon), `queue_dropped`
(on `run.failed`), `structured_output_failed` (on `run.failed`),
`invalid_steer_target`, `run_terminated` (steer drop reason). Codes stay open
strings in the schema; the validator does not enumerate them.

### Validator diagnostics added

`unapplied_control`, `unsatisfiable_control`, `model_not_in_catalog`,
`queue_order_violation`, `premature_session_mutation`,
`unmatched_tool_source`, `catalog_mismatch`,
`unmatched_steer`, `duplicate_steer`, `pending_steer_at_terminal`.
Existing codes are reused wherever the
invariant is the same (`unavailable_capability`, `illegal_run_transition`,
`session_state_mismatch`, `scope_mismatch`, `wrong_interaction_responder`,
`pending_interaction_at_terminal`).

### Conformance units

`validation/manifest.go` `knownUnits` gains `run-controls`, `models`,
`queue`, `tool-sources`, `control-tools`, `steer`. The conformance draft's
unit list gains `+run-controls`, `+tool-sources`, and `+control-tools`
beside the existing `+models`, `+queue`, and `+steer`; the executable claim
becomes `open-agent-protocol.agent-control-core/0.1-executable` plus the
graduated units.

### Schema evolution

Additive only: new optional fields (`active_runs`, `limits`,
`tool_sources`, `tools`, `source`, `sources`, `features`, `target_run_id`,
`session_id` on the tools-list request), new envelope types in the
`oneOf` (`models.*`, `action.call.resolve.*`, `run.steer.*`), and new
payload definitions in existing schema files. No existing field's schema
narrows: `tool_choice` keeps its permissive schema and its typed shape is
a validator and adapter rule of the `run-controls` unit, and the models
payloads live in the control-plane schema file so the bundle inventory and
the manifest schema are unchanged. `version` and `profile` are unchanged.

## Deliberately later

Unchanged from Decisions 0001 and 0002 and PF-4: `btw` and side runs;
subagent trees and background tasks (a run-tree design comes first);
artifacts; checkpoints, rewind, branch; compaction markers; durable
idempotent admission; cross-process replay and continuity leases; orphan
terminals; retained-interaction reassociation and deadlines; runtime
tool-source attach and detach; per-submit control-layer tools; model
resolution and aliases; auth login flows; skills at the wire level.

## Open items for the per-unit decisions

- T1: confirm the `tool_choice` shape before any native adapter advertises
  `run.tool_selection`; decide whether `metadata` on the submit request is
  passed through to the harness or reserved.
- T5a: whether `context_window` and `features` are worth requiring, or
  `id` alone is the executable minimum.
- T2: the exact `limits` field names, and whether `queue_position` is
  required or optional on queued `active_runs` entries.
- T3: whether `remote` sources are admitted in the first slice or held until
  a header-free evidence case exists; the resolution response's `accepted`
  semantics when the harness has already timed the call out.
- T4: the four questions listed under the unit.

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
  way. Plain names suffice only because the catalog a policy is judged
  against never carries two tools with the same `name`: the tool
  definition schema does not require it, so T1 itself introduces the rule
  and its diagnostic (`duplicate_tool_name`, on any catalog a
  `tool_choice` is evaluated against), and T3a later extends the same rule
  to every session catalog whatever its sources. In
  Go,
  `MessageSubmitRequest.ToolChoice` stays `json.RawMessage`;
  `protocol.ToolChoice` (today `Mode` and `Name` only) gains `Allowed
  []string` and `Disallowed []string`, and a strict
  `MessageSubmitRequest.ToolChoicePolicy()` accessor decodes the typed
  shape, rejecting unknown members.
- `output_schema` stays a JSON Schema object, and it must describe a JSON
  object: its root `type` is `"object"` (a `type` list may name only
  `"object"`). `run.completed.result` is `type: "object"` in
  `run.schema.json` and `map[string]any` in Go for every adapter, so a
  root-array or scalar schema could never be met by a conforming success;
  such a schema is unsatisfiable and every adapter rejects it before
  admission (`unsupported_feature`, `details.feature:
  "run.structured_output"`, `details.reason: "unsatisfiable"`). This
  matches the native structured-output surfaces, which accept object roots
  only. It is also self-contained: every `$ref` resolves within the
  document (a fragment or a `$id`-anchored subschema), and a reference to
  anything outside it (an absolute URI, a file, an HTTP resource) is
  unsatisfiable and rejected before admission. Adapters and the validator
  compile it with a loader that refuses every reference outside the
  resources they added themselves (`jsonschema.Compiler.UseLoader` with a
  loader that returns an error; the engine's default loader reads local
  files), and the draft 2020-12 metaschema is embedded in the engine, so
  an untrusted schema can never make an adapter or a validator read a
  local path or fetch a URL. The structured result travels in `run.completed.result` (already on
  the wire) and must validate against the admitted schema. In Go,
  `protocol.RunCompletedPayload.Result` is today `map[string]any` with
  `omitempty`, which drops a valid empty object `{}` from the wire and
  would make a conforming run fail the presence check; T1 changes the
  field to `json.RawMessage` (still `omitempty`, which for a raw message
  omits only `nil`), so presence is preserved exactly as emitted and no
  adapter re-encodes the harness's result. The memory adapter's
  `{"ok": true}` and a schema admitting `{}` both round-trip, and the
  T1 fixture `controls-structured-empty-object` (a schema with no required
  members and an empty-object result, valid) pins the behavior.
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

- `session.message.submit.request` with any control is judged through the
  existing `feature()` gate with the control's key, but on the correlated
  response rather than on the request, because the semantics above make
  refusal the required behavior and a conforming refusal must validate:
  the request's unadvertised or `unavailable` controls are remembered, an
  admission correlated to it (`session.message.submit.response`) is
  `unavailable_capability` on the response, and a correlated
  `error.response` must carry `unsupported_feature` with `details.feature`
  naming the control's key (`unavailable_capability` on the error response
  otherwise: the refusal happened, but under a code that does not tell the
  caller what to stop sending). The stale-revision and missing-descriptor
  branches of `feature()` stay on the request, as for every optional
  envelope. Fixtures: `controls-unadvertised-model` (`unavailable_capability`
  on the admission) and the correct rejection
  `controls-unadvertised-model-rejected` (`error.response` with
  `unsupported_feature`, `details.feature: "run.model_selection"`,
  `details.reason: "unadvertised"`, then no admission), which is also the
  shape of the Codex `instructions` rejection T1 keeps.
- New diagnostic `degraded_without_optin`: `feature()` today accepts every
  level but `unavailable`, so a request carrying a control, or an
  explicit non-`auto` delivery, whose key the descriptor advertises
  `degraded` and whose `allow_degraded_features` omits that key is
  remembered, and an admission correlated to it (a
  `session.message.submit.response` rather than an `error.response` with
  `capability_degraded`) is diagnosed on the response; for an `auto`
  request the key is the delivery the admission resolved to (`queued`,
  `steered`), judged on the response. The core's mandatory `auto`
  delivery itself is exempt: Claude, Hermes, and DeepSeek advertise
  `session.message.delivery.auto` at `degraded` today with disclosed
  reasons, every existing corpus admits `auto` without
  `allow_degraded_features` (no fixture carries the field), and a caller
  refused `auto` would have no way to submit at all. Its `degraded` level
  is therefore disclosure the caller reads from the descriptor, not a
  consent gate, and the opt-in rule applies to what a caller elects:
  controls, explicit deliveries, and the delivery an `auto` resolves to
  beyond `start`. The positive fixture `controls-auto-degraded-admitted`
  records that this is legal, so the exemption is stated rather than
  accidental. The rejection path
  is the correct one and stays a positive fixture; this rule catches the
  adapter that admits and executes degraded behavior without consent.
- New diagnostic `unapplied_control`: the submit response's `model_id` (when
  the request carried one) differs from the request; `run.started.model_id`
  differs from the admitted model (today's `illegal_run_transition` case
  moves to this code); `run.completed` under an admitted `output_schema`
  lacks `result`, or carries a `result` that does not validate against the
  admitted schema (the validator compiles the schema with the same
  `jsonschema` engine it already uses for the bundle); an
  `action.call.requested` in a run whose admitted `tool_choice` excludes
  that tool (`mode: "none"`, filtered out by `allowed` or `disallowed`, or
  `named` naming another tool), whichever participant owns the call; and,
  at `run.completed`, a run admitted with `mode: "required"` that emitted
  no `action.call.requested`, or with `mode: "named"` that emitted none
  naming that tool (a run that fails or is cancelled first is not judged,
  since the requirement binds a completed response). An adapter that
  cannot make its harness honor `required` or `named` advertises
  `run.tool_selection` accordingly or rejects the policy as unsatisfiable;
  it never completes the run as if the requirement were met. For a run
  admitted under `per_run` (the mode `runState` retains from admission,
  below, together with `defaultModel`, the session default
  `sessionTrack.currentModel` held at that admission), a `session.state`
  snapshot taken while that run is nonterminal whose `current_model_id`
  differs from that retained default is also `unapplied_control`,
  whether it moved to the run's admitted `model_id` or to any other
  value: the per-run rule leaves the session default untouched, so the
  comparison is against what the default was, not merely against the
  model the run selected; the Codex and Makai overwrite T1 fixes is the
  first case, so the fix is verifiable.
- New diagnostic `unsatisfiable_control`: a `tool_choice` that is not the
  typed policy, carries both `allowed` and `disallowed`, names a tool in
  its own `disallowed` list or outside its own `allowed` list, or, when the
  trace carries a catalog (capabilities or `action.tools.list.response`,
  plus any `tools` provided at open), lists or names a tool outside that
  catalog or is `required` or `named` against an empty filtered set. The
  validator and the reference adapter therefore reject the same policies:
  a policy the unit's rules accept is admitted by the reference, and one
  the reference refuses is diagnosed. The same diagnostic covers an
  `output_schema` whose root is not an object schema or that carries an
  external reference; the validator detects the reference by compiling
  with the refusing loader, never by resolving it. The check runs only
  when the submit carries a control, so envelopes that do not use the unit
  are untouched.
- New diagnostic `duplicate_tool_name`: the catalog a `tool_choice` is
  judged against (the descriptor's effective catalog or the latest
  `action.tools.list.response` for the session) lists two tools with one
  `name`, whatever their owners, so no `named` or list entry can be
  ambiguous. The descriptor's effective catalog is the union of its
  top-level `tools` and every `layers.*.tools`, since a valid descriptor
  may publish its catalog under a layer alone; the validator normalizes
  both into one list (with their `sources`) wherever this plan says "the
  descriptor's `tools`", including the open-time collision and refresh
  checks under T3. Introduced here so the `run-controls` unit is sound before
  T3; T3a applies the same diagnostic to every session catalog.
- `runState` gains `controls` (the admitted request's control set) so the
  checks above are keyed off the request, not the response, and
  `capabilityRevision` with the `run.model_selection` mode and the levels
  of the admitted controls as the descriptor active at admission declared
  them. Every per-run judgement above and under T2 (the `per_run`
  snapshot rule, `premature_session_mutation`, the degraded opt-in) reads
  the run's retained mode and levels, never the current descriptor's: the
  core draft fixes the effective capability revision for an admitted run,
  so a `capabilities.updated` that changes the mode while a controlled run
  is nonterminal, started or still queued, changes how later admissions
  are judged and never how that run is. A queued run's `session_mutation`
  therefore applies at promotion under the mode it was admitted with
  (T2 fixture `queue-mode-refreshed-while-queued`).

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
requested `output_schema` with the refusing loader and rejects, before
any identity is allocated, a schema carrying an external reference and
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
`controls-structured-output`, `controls-structured-empty-object` (a
schema with no required members and an empty-object `result`, valid),
`controls-auto-degraded-admitted` (an `auto` submit admitted without
`allow_degraded_features` on a descriptor whose `auto` delivery is
`degraded`, legal under the exemption above),
`controls-unadvertised-model-rejected` (`error.response` with
`unsupported_feature`, `details.feature: "run.model_selection"`,
`details.reason: "unadvertised"`, then no admission; validated as a
correct rejection). Negative:
`controls-unadvertised-model`
(`unavailable_capability` on the admission), `controls-model-mismatch`
(`unapplied_control`),
`controls-structured-missing-result` (`unapplied_control`),
`controls-structured-nonconforming-result` (`unapplied_control`; `result`
present but invalid against the admitted schema),
`controls-tool-choice-contradictory` (`unsatisfiable_control`; `required`
with the only tool disallowed, and `named` outside its own allowlist),
`controls-tool-choice-unknown-entry` (`unsatisfiable_control`; an
`allowed` list naming a tool outside the catalog the trace carries),
`controls-tool-choice-ignored` (`unapplied_control`; `action.call.requested`
under `mode: "none"`),
`controls-structured-non-object-schema` (`unsatisfiable_control`; a
root-array `output_schema`), `controls-structured-external-ref`
(`unsatisfiable_control`; an `output_schema` with an absolute `$ref`,
which the validator must diagnose without any resource access),
`controls-required-without-call` (`unapplied_control`; `mode: "required"`,
`run.completed` with no call), `controls-named-without-call`
(`unapplied_control`; `named` naming `scripted_tool`, `run.completed` with
no call),
`controls-tool-choice-ambiguous-name` (`duplicate_tool_name`; two native
tools sharing a name in the descriptor, then a `named` policy),
`controls-degraded-without-optin` (`error.response` with
`capability_degraded` then no admission; validated as a correct rejection),
`controls-degraded-admitted-without-optin` (`degraded_without_optin`; the
same request admitted), `controls-per-run-overwrites-default`
(`unapplied_control`; a `per_run` descriptor, a submit with `model_id`,
then a snapshot reporting it as `current_model_id`),
`controls-per-run-changes-default` (`unapplied_control`; the same, but
the snapshot reports a third model that is neither the retained default
nor the admitted one).

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
{ "type": "models.request", "session_id": "s1",
  "payload": { "session_id": "s1" } }
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

`ModelDescriptor` fields: `id` (required, unique within a response, the
value `model_id` accepts),
`display_name`, `provider_id`, `context_window`, `features` (the layered
draft's `model.*` keys as `FeatureSupport`), `default` (boolean; at most one
per response). `current_model_id` repeats session state. Both envelopes
are session-scoped in the envelope `oneOf`: `models.request` extends the
`session` base that requires the top-level `session_id`, as
`session.state.request` does, and the payload's `session_id` must agree
with it. Capability key:
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
  correlation and scope checks: each envelope's top-level `session_id` must
  agree with its payload (`scope_mismatch`), and the response must repeat
  the request's session.
- `models.response` invokes the existing `feature()` gate with
  `models.list` (`unavailable_capability` when the descriptor omits the
  key or advertises it `unavailable`), so a catalog served without being
  advertised fails the gate's rule that nothing is applied without being
  advertised; the request is remembered rather than gated, as T1 and T2
  do for controls and deliveries, and a correlated `error.response` must
  carry `unsupported_feature` with `details.feature: "models.list"`
  (`unavailable_capability` on the error response otherwise), so an
  endpoint that correctly refuses the query is conforming.
- `current_model_id` on `models.response` must equal the effective session
  model the validator tracks (`sessionTrack.currentModel`: the latest
  `session.open.response` or `session.state` value, advanced by a
  `session_mutation` application at the run it applies to), otherwise
  `session_state_mismatch`; a picker is never shown a current model the
  session does not report. A nonempty `current_model_id` must also name
  one of the response's own model ids (`model_not_in_catalog`, with
  `details.field: "current_model_id"`): a picker shown a current model
  the catalog does not describe could not resolve it, and re-selecting
  the same id would be refused by the catalog rule, so the adapter that
  serves such a catalog is diagnosed rather than the caller.
- New diagnostic `model_not_in_catalog`: an admitted `model_id` after a
  `models.response` in the same trace names an id the response did not
  list, or a `models.response` whose own `current_model_id` is not among
  its ids (above). The catalog is stored with the `capability_revision` it was served
  under: a `capabilities.updated` or a new `capabilities.response` that
  changes the active revision discards it, so no admission is judged
  against a stale catalog (a newly added model is not falsely diagnosed,
  a removed one is not silently accepted) until a `models.response` under
  the new revision replaces it; the `feature()` gate already rejects a
  `models.response` citing a stale revision. An admission with `model_id`
  under a revision whose catalog the trace has not yet served is not
  skipped but retained (`sessionTrack.unjudgedModels`: the id and the
  admitting response), and the first `models.response` under that
  revision reconciles them: every retained id the catalog does not list
  is `model_not_in_catalog` on that response, with `details` naming the
  admission, so an adapter cannot accept an unlisted model in the gap
  between a refresh and its catalog, or before its first catalog, and
  have the gap hide it. Reconciliation runs at `native` and `emulated`
  `models.list` only; at `degraded` the catalog refreshes per turn and an
  earlier admission may have matched a list that was never served.
- New diagnostic `unannounced_catalog_change`: a second `models.response`
  under the same `capability_revision` whose catalog differs from the
  stored one in any descriptor, compared whole (`id`, `display_name`,
  `provider_id`, `context_window`, `features`, `default`), not only in
  its set of ids, at `native` or `emulated` `models.list`; the wire rule
  makes a catalog change a capability invalidation, so availability may
  not change without `capabilities.updated`. At `degraded` the
  descriptor's `reason` discloses per-turn refresh (Claude's `system/init`
  list) and the check is skipped, which is what degraded means here.
- New diagnostic `ambiguous_default_model`: a `models.response` with more
  than one descriptor carrying `default: true`, so the at-most-one rule
  above is enforced rather than stated.
- New diagnostic `duplicate_model_id`: a `models.response` with two
  descriptors sharing `id`, so an accepted `model_id` denotes exactly one
  descriptor's metadata.
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
`models-two-defaults` (`ambiguous_default_model`),
`models-request-scope-mismatch` (`scope_mismatch`; envelope and payload
`session_id` differ), `models-response-scope-mismatch` (`scope_mismatch`),
`models-unadvertised` (`unavailable_capability` on the response; a
catalog served unadvertised), the correct rejection
`models-unadvertised-rejected` (`error.response` with
`unsupported_feature`, `details.feature: "models.list"`),
`models-duplicate-id`
(`duplicate_model_id`), `models-current-mismatch`
(`session_state_mismatch`; state reports one model, the catalog another),
`models-current-not-listed` (`model_not_in_catalog`; state and the
response agree on one model, the catalog lists only another),
`models-select-removed-after-refresh` (`model_not_in_catalog`; a model
listed under revision 1, dropped by the revision 2 catalog, then
selected), `models-catalog-mutates-within-revision`
(`unannounced_catalog_change`; two responses under one revision with
different ids), `models-catalog-metadata-mutates-within-revision`
(`unannounced_catalog_change`; the same ids, one descriptor's `default`
and `context_window` changed). Positive as well: `models-refresh-replaces-catalog` (a model
added by the revision 2 catalog is selected after `capabilities.updated`,
and one selected between the refresh and the new catalog is retained and
found listed when that catalog arrives). Negative as well:
`models-select-in-gap-unlisted` (`model_not_in_catalog` on the revision 2
`models.response`; a model accepted between `capabilities.updated` and
the new catalog, which then omits it).

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
  only queued runs remain (session status `queued`). T3c and T4 add
  `pending_interactions` and `pending_steers` to these entries.
- `capabilities.response` gains optional `limits`: `{
  "max_active_runs_per_session": int, "max_queued_runs_per_session": int }`,
  where `max_active_runs_per_session` bounds the nonterminal set, which is
  what `active_runs` lists (started run plus queued reservations; it is
  the wire projection of `adapter.Descriptor.MaxActiveRunsPerSession`,
  whose v0.1 value of 1 therefore means no queue at all), and
  `max_queued_runs_per_session` bounds the queued subset. Both are
  admission bounds: no admission may raise the set above the bound
  advertised at that admission, and a refresh that lowers a bound below
  a session's current set grandfathers that set and admits nothing more
  until it falls below the new bound (validator, below). Absent means no
  bound is advertised: one started run and at least one queued
  reservation, unenforced.
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
- A delivery capability advertised `degraded` needs its key in
  `allow_degraded_features`, for an explicit `queue` and for an `auto`
  that would resolve to it alike; otherwise `capability_degraded` before
  admission, exactly as T1 rules for controls. T4 applies the same to
  `session.message.delivery.steer`.
- Explicit `queue` on a descriptor that omits
  `session.message.delivery.queue` or advertises it `unavailable` is
  refused before admission with `unsupported_feature` (`details.feature`
  naming the delivery key, `details.reason: "unadvertised"`), never
  admitted or silently started; T4 applies the same to `steer`.
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
- The capability gate does not depend on session state. The validator
  today invokes `feature()` with `delivery.<mode>` on the request itself
  for every explicit non-`auto` delivery; T2 moves that judgement to the
  correlated response on the same terms as T1's control gate, so that an
  adapter's required refusal of an unadvertised `queue` validates: the
  request is remembered, a `queued` admission response (from an explicit
  or an `auto` request) on a descriptor that omits
  `session.message.delivery.queue` or advertises it `unavailable` is
  `unavailable_capability` on the response whatever the session held, and
  a correlated `error.response` must carry `unsupported_feature` naming
  the delivery key. No fixture in the current manifest expects
  `unavailable_capability` for a delivery, so the move changes no
  existing fixture's meaning; T4 applies the same shape to `steer`.
  T1's `degraded_without_optin` covers the delivery keys too: a `queued`
  admission on a descriptor advertising the queue capability `degraded`
  without the caller's opt-in is diagnosed on the response.
- The existing `submitResponse` combination table (admission,
  `effective_delivery`, `status`, checked as `illegal_run_transition`) is
  extended so the stated resolutions are enforced, not described: a
  request with explicit `delivery: "queue"` may only be admitted `queued`
  (never `started`, even on an idle session, since the queued run promotes
  immediately instead), and an `auto` request admitted `queued` must carry
  `effective_delivery: "queue"` and `delivery_resolution: "session_busy"`.
  The response already has to repeat the requested delivery
  (`scope_mismatch`), so a resolution cannot be hidden by rewriting it.
- New diagnostic `queue_order_violation`: any sequenced stream event of a
  later-admitted run, not only `run.started` but also a pre-start
  `run.cancelled` or `run.failed` and anything else the adapter emits in
  its domain, appearing while an earlier-admitted run in the session is
  nonterminal. Requests and responses addressed to the queued run are
  exempt (`run.cancel.request` and `.response` are run-scoped envelopes,
  and cancelling a queued run before promotion necessarily happens while
  the earlier run is nonterminal): the rule governs the adapter's
  timeline, not the control layer's commands. This is the one-run-domain
  delivery rule above applied to the trace, so a queued run cancelled
  before promotion has its cancel exchange at once and its terminal after
  the earlier run's terminal in every conforming trace.
- New diagnostic `queue_limit_exceeded`: when the descriptor carries
  `limits`, a `queued` admission that would put the session's nonterminal
  set above `max_active_runs_per_session` or its queued subset above
  `max_queued_runs_per_session`, both checked at the reservation, since
  promotion adds no run; `sessionTrack` keeps both bounds from the
  descriptor and replaces them from every accepted refresh. Absent limits
  enforce nothing here, since absence advertises no bound. The bounds are
  admission bounds, and a refresh that lowers one below a session's
  current set grandfathers that set: the refreshed descriptor is not
  diagnosed for a set it inherited (an adapter cannot shrink the set
  except by cancelling work, which a descriptor change must never do),
  and every admission after the refresh is judged against the new bound,
  so a session over a lowered bound admits nothing until its set falls
  below it. Fixtures: `queue-limit-lowered-by-refresh` (positive; two
  nonterminal runs under `max_active_runs_per_session: 2`, a refresh to
  `1`, both settle, then one new admission) and
  `queue-limit-lowered-then-admitted` (`queue_limit_exceeded`; the same
  refresh, then a `queued` admission while both runs are still
  nonterminal).
- New diagnostic `premature_session_mutation`: when the started run was
  admitted under a descriptor disclosing `run.model_selection` with mode
  `session_mutation` (the mode `runState` retains from admission, T1), a
  `session.state` snapshot taken while that run is started reports a
  `current_model_id` other than its admitted model.
- `session.state` snapshots: `active_runs` is required whenever the
  validator tracks a queued reservation, more than one nonterminal run,
  or, on an endpoint advertising any unit that introduces `active_runs`
  entries (T2 queue, T3c provide, T4 steer), a nonterminal run with a
  pending steer or an unresolved interaction of any kind (permission,
  user input, control-owned call), since the single-run recovery path
  reads the submission or interaction id from the entry's
  `pending_steers` or `pending_interactions` and an omitted field would
  lose it (an absent field would read as an empty queue to a reconnecting
  client); the interaction condition is scoped to those endpoints because
  a v0.1-only endpoint has no `active_runs` at all, so no existing fixture
  changes meaning. When present it must list the tracked nonterminal runs
  in admission
  order with consistent `queue_position`; `active_run_id` must be the
  started run; otherwise `session_state_mismatch`. One reconciliation
  follows from the delivery rule: a queued run that settles before
  promotion has its terminal held until the earlier run's terminal, while
  the snapshot says when it actually settled, so a snapshot may already
  omit a tracked queued run whose terminal has not been delivered. The
  validator then marks that run settled-pending-delivery: its pre-start
  terminal must still arrive, after the earlier run's terminal
  (`queue_order_violation` if earlier, `session_state_mismatch` at the end
  of the trace if never), no later snapshot may list it again, and the
  omission must be true when made: the held terminal's `timestamp_ms`,
  when it is delivered, must be no later than the omitting snapshot's
  `updated_at_ms`, otherwise the snapshot is diagnosed
  `session_state_mismatch` at that point, since it dropped a run that was
  still reserved. Both fields are optional in the schemas, so the
  reconciliation is available only with evidence: an omitting snapshot
  without `updated_at_ms`, or a held terminal without `timestamp_ms`,
  leaves the validator unable to tell an early settlement from a
  premature drop and is diagnosed as the drop (the reference adapter and
  the daemon stamp both, so conforming traces always qualify). A started
  run is never reconciled this way; its terminal is never held.

### Reference adapter

`adapter/memory.go` advertises `session.message.delivery.queue` `emulated`
with `limits` of `max_active_runs_per_session: 2` and
`max_queued_runs_per_session: 1`: a submit while the scripted
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
  suffix and, when the subscription follows the session (below), continues
  into later-admitted runs in order, and the hub stops assuming the newest
  admission is the run a bare sequence refers to. Both clients' single scalar cursor therefore stays correct: the run
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
  T2 a following subscription (`?follow=session`, below) continues past a
  terminal into any later-admitted run's domain, and when the daemon ends
  a stream on purpose (a terminal with no later-admitted run, a terminal
  on a subscription that does not follow, or the session closed) it
  writes an explicit
  `event: oap-stream-end` signal (`run_id`, `last_sequence`) before
  closing, and whenever a stream leaves a run's domain for a later one,
  live or resumed, it first writes `event: oap-run-boundary` (`run_id`,
  `last_sequence`) for the run it is leaving, so a run's terminality is
  stated by the server rather than remembered by the client; the stdio
  frontend writes the same named lines beside its `oap-session-closed`.
  Both clients already skip named events they do not define, so both
  signals are additive. Continuation across run domains is opt-in, not
  assumed: a v0.1 client ends its stream at the terminal of the run it is
  reading (`client/events.go:143-146` returns `io.EOF` after a terminal
  and `clients/ts/src/events.ts:162-165` does the same), so a stream that
  continued into a queued run B would never be read by it and B's events
  would silently vanish from that subscription. A T2-aware client asks
  for continuation with an additive `?follow=session` parameter on the
  subscribe request (a `follow` flag on the stdio subscribe op); without
  it the daemon keeps the v0.1 behavior exactly, ending the stream at the
  terminal of the run the subscription is bound to, preceded by the end
  signal the legacy client skips. A legacy client therefore sees
  precisely what it sees today and never a partial view of a run it did
  not ask for, and one that submits again opens a new stream as it does
  now.
- `client` and `clients/ts`: both already track the run of the last
  observed envelope (`EventStream.runID`, `EventStream.runId`) and expose
  `EventsAfter(RunID, LastSequence)` / `eventsAfter`; the changes are to
  subscribe with `?follow=session`, send that run as `?run=` on
  reconnect, and stop treating a terminal envelope as the end of a
  following stream. A terminal ends the run; the stream
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
  Under T2 the first resumed envelope may instead be sequence 1 of a
  different run when the cursor's run is known terminal, and the client
  then adopts that run as the new domain and resets its sequence
  expectation to it. "Known terminal" has two sources: the in-memory
  `terminal` flag both clients already track, and an `oap-run-boundary`
  event naming the cursor's run at exactly the cursor's sequence. The
  second is what makes the public manual-resume path work: a fresh
  `EventStream` built by `EventsAfter(runA, n)` after a process restart
  holds only the `(run, sequence)` pair, and the daemon's answer to
  `?run=A&after=n` when A is terminal at n is the boundary for A, then B
  from sequence 1, so the client learns terminality from the server
  before the switch. A different run at any other sequence, or a
  different run with neither piece of evidence, still fails as today, so
  the daemon's answer is the end signal, the boundary followed by run B
  from sequence 1, or A's own continuation and nothing else. Both
  clients' e2e tests drop the connection between a run's terminal and the
  queued run's first envelope on the same stream object, and separately
  resume from the terminal cursor with a fresh `EventsAfter`, asserting
  the continuation in both. Because
  delivery is one run domain at a time, no per-run cursor table is needed:
  a run switch always follows a terminal, and on a live connection both
  clients already accept it.
- Daemon-management listing (`GET /sessions`) reports `active_runs`.

### Fixtures

Positive: `queue-explicit-idle-promoted`, `queue-busy-then-promoted`,
`queue-busy-cancelled-prestart` (its pre-start terminal after the first
run's terminal), `queue-state-active-runs`,
`queue-state-reflects-early-settlement` (a snapshot omitting the
cancelled queued run before its held terminal is delivered),
`queue-model-mutation-at-promotion` (a `session_mutation` descriptor, a
queued submit naming another model, `current_model_id` unchanged until the
first run's terminal). Negative:
`queue-promoted-out-of-order` (`queue_order_violation`),
`queue-prestart-terminal-interleaved` (`queue_order_violation`; a queued
run's pre-start `run.cancelled` before the started run's terminal),
`queue-idle-unadvertised` (`unavailable_capability` on the admission;
explicit `queue` on an idle session with the capability unadvertised,
admitted `queued`), the correct rejection
`queue-idle-unadvertised-rejected` (`error.response` with
`unsupported_feature`, `details.feature:
"session.message.delivery.queue"`, then no admission),
`queue-degraded-admitted-without-optin` (`degraded_without_optin`; queue
advertised `degraded`, no opt-in, admitted `queued`), and the correct
rejection `queue-degraded-without-optin` (`error.response` with
`capability_degraded`, then no admission) as a positive fixture,
`queue-model-mutation-early` (`premature_session_mutation`),
`queue-mode-refreshed-while-queued` (positive; a `session_mutation`
descriptor, a queued controlled submit, a `capabilities.updated` to
`per_run` while it waits, then promotion with the mutation reflected in
`current_model_id`: judged under the retained mode, no diagnostic),
`queue-over-limit` (`queue_limit_exceeded`; `max_queued_runs_per_session:
1`, one started run, two queued admissions), `queue-over-active-limit`
(`queue_limit_exceeded`; `max_active_runs_per_session: 1`, a queued
admission while a run is started),
`queue-overlap-unadvertised` (`illegal_run_transition`),
`queue-explicit-admitted-start` (`illegal_run_transition`; explicit
`queue` on an idle session answered `started`),
`queue-auto-without-resolution` (`illegal_run_transition`; an `auto`
request admitted `queued` without `delivery_resolution: "session_busy"`),
`queue-state-missing-reservation` (`session_state_mismatch`),
`queue-state-omits-active-runs` (`session_state_mismatch`; a queued
reservation and a snapshot without `active_runs`),
`state-omits-active-runs-with-pending` (`session_state_mismatch`; a single
started run with a pending steer and a snapshot without `active_runs`),
`state-omits-active-runs-with-pending-permission`
(`session_state_mismatch`; a queue-advertising endpoint, one started run
waiting on a permission, and a snapshot without `active_runs`),
`queue-state-drops-unsettled-run` (`session_state_mismatch`; a snapshot
omits a queued run whose terminal never arrives),
`queue-state-omits-before-settlement` (`session_state_mismatch`; a
snapshot omits a queued run whose held terminal carries a later
`timestamp_ms`), `queue-state-omits-without-timestamps`
(`session_state_mismatch`; the omitting snapshot lacks `updated_at_ms`).

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

Semantics: a source `id` is unique across a session's catalog and across
the `tool_sources` of one open, so a tool's `source` and a call's `source`
resolve to one descriptor and the hub routes to one endpoint even when one
owner serves several sources: a list declaring two sources with one `id`
is invalid (`duplicate_tool_source`), and an attach whose descriptors
collide with each other or with a declared source is rejected at open
(`unsupported_feature`, `details.reason: "unsatisfiable"`,
`details.source`). Likewise `name` is unique across a session's catalog,
whatever the sources, so a policy entry (T1) and a call resolve to one
catalog entry and one `execution_owner`: a list with two entries of one name is invalid
(`duplicate_tool_name`, the diagnostic T1 introduced for the catalog a
policy is judged against, applied here to every session catalog); an
attach or provide whose tools would collide
with the catalog or with each other is rejected at open with
`unsupported_feature` (`details.reason: "unsatisfiable"`, `details.tool`
naming the collision) rather than shadowed or renamed silently; a harness
that namespaces MCP tools (Claude's `mcp__<server>__<tool>`) exposes that
namespaced string as `name`, and `source` carries the attribution so the
name need not encode it. A tool naming a source must name one the same
response declared, and a call carrying `source` must name the source the
session's catalog
records for that tool, or a declared source when the tool is not in the
catalog (validator diagnostic `unmatched_tool_source` for both); every
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
source carries only `endpoint` and is capability-gated (`mode: "remote"`
on `action.tool_sources.attach`; an open attaching a `remote` source on a
descriptor without that mode is rejected, and the validator diagnoses an
admitted one `unavailable_capability`).
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
  `execution_owner` must equal the opening participant: on the daemon its
  declared control participant, in a trace the participant
  `protocol.initialize.request` declared (envelopes carry no sender field,
  so the comparison is against the declared control participant, not a
  per-envelope identity). An open supplying a tool owned by anyone else is
  rejected before the session exists (`unsupported_feature`,
  `details.feature: "action.tools.provide"`, `details.reason:
  "unsatisfiable"`, `details.tool`); otherwise the adapter could admit a
  tool it would later classify as harness-owned or route to a participant
  that never provided it. So is a tool whose `source` names a source
  neither the descriptor nor the same open's `tool_sources` declares
  (`details.source`), for the same reason: a dangling source cannot be
  attributed or routed. Both are adapter rules that the validator enforces
  on the admission, not on the request (validator, below): a refusal is
  conforming and produces no diagnostic, and an open the adapter admitted
  despite either condition is `wrong_tool_owner` or
  `unmatched_tool_source` on the `session.open.response`, so the negative
  fixtures for both are traces of an adapter that admitted what it should
  have refused. Per-submit tool provisioning
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
- `active_runs[]` entries (T2) gain `pending_interactions:
  [interaction_id]` (additive), listing the run's unresolved interactions
  of every kind (permission, user input, control-owned calls). It is the
  state surface a resolver that lost its resolve response reads: an
  interaction absent from the list was resolved, one still present was
  not, so a rejected resolution is re-sent and an accepted one is not.

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
`action.call.resolve.request`. Ownership is an internal provisioning
boundary, not a second wire identity: on the adapter-facing wire the hub
is the declared control participant (the daemon is what sends
`protocol.initialize.request`), so every provided tool the adapter sees,
whether a client supplied it through `POST /adapters/{name}/sessions` or
the connector provisioned it, carries the hub's participant id as
`execution_owner`, and T3c's ownership rule holds without the hub
borrowing a client's identity. Clients supply tools with that id, which
the daemon publishes to them as its own identity on the client-facing
wire, and the daemon rejects any other owner before forwarding the open.
Inside, the hub keeps a provisioning registry (tool name to the client
subscription that supplied it, or to the connector) to route each
`action.call.requested` and to accept a resolution only from the party
that provisioned the tool; a client resolving a connector-backed call is
refused at the hub with `wrong_interaction_responder`, exactly as the
adapter would refuse a foreign responder. The connector
serves adapters with no native MCP support (DeepSeek, pi, memory) and the
HyperNeo-style embedding; it adds no wire vocabulary.

### Validator

- `action.tools.list.request` and `.response` are gated on the key the
  profile names for the catalog. Today `validation/state.go` sends both to
  `feature("tools")`, whose aliases resolve to `action.tools` and never to
  `action.tools.list`, so an endpoint advertising only `action.tools.list`
  would fail with `unavailable_capability`. The gate becomes
  `feature("tools.list")`, accepting `action.tools.list` and, for
  descriptors that advertise the family key today, `action.tools` as an
  alias; the positive fixture `tools-catalog-list-only` carries a
  descriptor advertising `action.tools.list` alone.
- An `action.tools.list.response` correlated to a request that names a
  session (envelope or payload `session_id`) must carry that session on
  both its envelope and its payload; an unscoped response to a scoped
  request is `scope_mismatch`, not an endpoint-level catalog, so an
  adapter cannot evade the lifetime-catalog check by dropping the scope.
- `action.tools.list.response`: `duplicate_tool_name` for two entries
  sharing `name`, whatever their sources.
- `action.tools.list.response` and `session.open.request.tool_sources`:
  `duplicate_tool_source` for two descriptors sharing `id`. For the open,
  the check runs when the open is admitted (`session.open.response`) over
  the union of the attached descriptors and the sources the capability
  descriptor already declares, so an attachment reusing a declared id is
  diagnosed even in a trace that ends at the open or later lists a single
  entry under that id.
- Every accepted `capabilities.response`, initial or refreshed:
  `duplicate_tool_name` across its effective catalog (top-level `tools`
  and every `layers.*.tools`, unioned) and `duplicate_tool_source` across
  its declared `sources`, so a descriptor that is ambiguous on its own is
  diagnosed before any list, open, or call, and a session that never
  lists or selects tools cannot reach a call whose owner or source lookup
  is ambiguous.
- `catalog_mismatch`: `sessionTrack` records the `tool_sources` ids and
  the provided `tools` (name and `execution_owner`) from
  `session.open.request`; every session-scoped
  `action.tools.list.response` for that session must declare every
  recorded source id and list every provided tool under the recorded
  owner, or the diagnostic fires on that response. The comparison is of
  the whole entry, not the identity: an attached source must be listed
  with the `kind`, `protocol`, `endpoint`, and `display_name` it was
  attached with (the attach-only `command`, `args`, and `environment` are
  never listed), and a provided tool with the `name`, `description`,
  `input_schema`, `annotations`, `execution_owner`, and `source` (present
  or absent exactly as supplied) it was supplied with, `features` being
  the adapter's to fill; a redirected endpoint, an altered schema, or a
  tool moved to another declared source is `catalog_mismatch` just as an
  omission is, so attribution and routing cannot change during the
  session. Native
  entries may differ between lists (a harness refreshes its own catalog),
  but the open-time entries never drop out or change, because attachment
  is for the session's lifetime, provisioning is all-or-nothing, and
  runtime attach and detach stay deferred.
- `action.tools.list.response`: `unmatched_tool_source` for a tool naming an
  undeclared source. `action.call.*` payloads carrying `source`: the same
  diagnostic when the trace's catalog lists the named tool under a
  different source, or when the tool is not listed and the source is
  undeclared; the validator keeps the catalog per session (`sessionTrack`
  gains `tools`), so attribution to the wrong MCP server is caught even when
  both sources are declared. As with the models catalog, the session
  catalog is stored with the `capability_revision` it was served under and
  discarded when the active revision changes, so no `tool_choice` or
  sourced call is judged against stale names, owners, or sources until a
  list under the new revision replaces it; the open-time sources and
  provided tools are kept across the refresh, because they are
  session-lifetime facts the next list must still carry. What arrives in
  the gap is not skipped but retained, as T5a retains in-gap model
  selections: once a session has listed its catalog, the list is the
  authority and a gap defers rather than falls back to the descriptor's
  `tools` (a session that never lists is judged against the descriptor's
  effective catalog plus its open-time tools, as T1 rules), so
  `sessionTrack.unjudgedTools` keeps every `tool_choice` admitted, and
  every `action.call.requested` emitted with a `source` or an
  `execution_owner`, while the session has no catalog under the active
  revision, and the first `action.tools.list.response` under that
  revision reconciles them: a retained policy that lists or names a tool
  the catalog does not carry, or that is `required` or `named` against an
  empty filtered set, is `unsatisfiable_control` on that response with
  `details` naming the admission; a retained call whose `name` the
  catalog lists under another source or another owner, or does not list
  while its source is undeclared, is `unmatched_tool_source` or
  `wrong_tool_owner` on that response, naming the call. An adapter
  therefore cannot accept an unlisted policy or route a call under a
  stale attribution in the window between a refresh and its list and
  have the window hide it. Reconciliation runs at `native` and `emulated`
  `action.tools.list`, on the same terms as the models catalog.
- `action.call.requested` with `execution_owner` equal to a declared control
  participant must carry `interaction_id` and `responded_by`
  (`illegal_tool_transition`); `action.call.started` for such a call must
  be preceded by an `action.call.resolve.response` with `accepted: true`
  for its interaction, answering either the acknowledgement or the
  resolution (`illegal_tool_transition` otherwise, so an adapter cannot
  record execution the control participant has not evidenced, and a
  rejected exchange, `accepted: false`, evidences nothing). Acceptance is
  tracked per arm: `action.call.completed` or `.failed` for such a call
  must be derived from an accepted `result` or `error` resolution, so a
  terminal following a rejected result or error response is
  `illegal_tool_transition` even when an earlier acknowledgement was
  accepted. The accepted payload is stored too: the terminal's result
  must equal the accepted `result` and a failure's `error` the accepted
  `error`, compared as canonical JSON, otherwise the new diagnostic
  `resolution_payload_mismatch`, so an adapter that forwards something
  other than the participant's actual outcome to the harness cannot pass
  conformance with an authorized but altered terminal; its resolution
  follows the interaction rules (`unmatched_interaction`,
  `duplicate_interaction`, `wrong_interaction_responder`,
  `pending_interaction_at_terminal`).
- `action.call.resolve.request` and `.response` join correlation and scope
  checks; the resolution's `tool_call_id` must match the interaction's
  binding (`scope_mismatch`); a second `accepted: true` response to a
  `result` or `error`, or to a `started`, for one interaction is
  `duplicate_interaction` whatever the adapter then emits, and so is an
  accepted `started` after an accepted `result` or `error` for the same
  interaction (`interactionState` records the accepted terminal, and
  every later request of any arm must be answered `accepted: false`, as
  the semantics above require of a late acknowledgement), so "one
  resolution", "at most one acknowledgement", and "no acknowledgement
  once resolved" are all enforced at the response, not only at the
  events that follow.
- `session.open.request` with `tool_sources` or `tools` is judged through
  the `feature()` gate with `action.tool_sources.attach` or
  `action.tools.provide` on the correlated response, as T1 and T2 judge
  controls and deliveries: an admitted open (`session.open.response`) on
  a descriptor that omits the key or advertises it `unavailable` is
  `unavailable_capability` on the response, and a correlated
  `error.response` must carry `unsupported_feature` with `details.feature`
  naming that key (`unavailable_capability` on the error response
  otherwise), so the fail-closed refusal the wire requires is itself
  conforming; a `remote` source additionally requires the attach
  capability to disclose `mode: "remote"`, else `unavailable_capability`
  on the admitted open. Each supplied tool's
  `source`, when present, must name a source in the union of the same
  open's `tool_sources` and the sources the capability descriptor
  declares, checked when the open is admitted (`unmatched_tool_source` on
  the `session.open.response`), so a trace that ends after the open cannot
  carry a provided catalog entry that resolves to no source. An adapter
  that refuses such an open, as the wire rule requires, produces no
  diagnostic; the diagnostic names an admission the adapter should have
  refused.
- `session.open.request.tools[*].execution_owner` must be the declared
  control participant (`wrong_tool_owner`, diagnosed on the
  `session.open.response` when the open is admitted, on the same terms as
  the source check). The check runs at supply time, before any interaction
  exists, which `wrong_interaction_responder` cannot cover. The same
  diagnostic fires on an `action.call.requested` whose
  `execution_owner` differs from the owner the session catalog records
  for that `name`, so an adapter cannot route a harness-owned tool to the
  control participant, or a provided tool to the harness, under a
  lifecycle that is otherwise consistent; the existing check that the
  owner does not change mid-lifecycle stays as it is.
- `session.open.request.tools[*].name` must be unique within the array
  and against the descriptor's native `tools`, checked when the open is
  admitted (`session.open.response`), so a collision the adapter should
  have refused is `duplicate_tool_name` even in a trace that only opens
  the session and never lists or selects tools. The check is repeated on
  every revision-changing descriptor (`capabilities.updated` followed by
  the new `capabilities.response`): each open session's retained provided
  tools are compared with the new native `tools`, and its retained
  attached source ids with the newly declared sources, diagnosing
  `duplicate_tool_name` or `duplicate_tool_source` on the descriptor
  envelope, since a post-refresh list is not mandatory and an ambiguous
  catalog would otherwise go unnoticed. The supplied-tool source check is
  rerun at the same point: every retained provided tool's `source` must
  still resolve against the retained attachments plus the new
  descriptor's declared sources (`unmatched_tool_source` on the descriptor
  envelope otherwise), so a refresh cannot leave a session with a provided
  tool whose source resolves nowhere. An adapter's refresh therefore
  keeps declaring any source a retained provided tool references, or the
  refresh is a conformance failure. An adapter whose refresh would
  introduce a colliding native tool must namespace it or keep it out of
  the session's catalog; it never shadows a provided tool.
- `session.state` snapshots: each `active_runs[]` entry's
  `pending_interactions` must equal the validator's set of unresolved
  interactions for that run (`session_state_mismatch` on omission or on a
  resolved interaction still listed), the same check T4 applies to
  `pending_steers`.

### Reference adapter

`adapter/memory.go` declares two sources (`native` for `scripted_tool`, a
synthetic `process`/`mcp` source), accepts `tool_sources` at open and lists
them, accepts every control-owned `ToolDefinition` supplied at open (no
cardinality limit; all are listed; a name colliding with `scripted_tool` or
with another supplied definition rejects the open), and after the
permission gate calls the
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
- `serve`: `Session.Tools(ctx)`; `Session.Resolve` passes the new arm
  under the same publication gate T4 specifies for steer, armed here for
  the interaction's `action.call.started` and terminal rather than for a
  settlement (envelopes before them pass through, those from them on are
  withheld), because a
  participant that resolves synchronously lets the adapter emit
  `action.call.started` and the terminal inside `Resolve`, before the
  binding has written the accepted resolve response, which is the order
  the validator rejects. The hub gates the run's drainer across
  `Resolve`, the binding lifts it with `Session.Published(interactionID)`
  after writing the response. The context-done fallback reconciles as the
  steer fallback does rather than lifting the gate blind: the hub first
  publishes a `session.state.updated` whose `active_runs` entry for the
  run reflects `pending_interactions` after the request was answered:
  the interaction absent only when an accepted `result` or `error`
  settled it, and still present when the request was rejected or when it
  was an accepted `started` acknowledgement, which is not a resolution
  and leaves the call awaiting its terminal, then lifts the gate. The
  accepted response is recorded in
  the hub's trace before the released events, so the validator's
  accepted-response rule holds in every trace, and a resolver that never
  saw the response learns the outcome from state instead of guessing from
  events it cannot attribute to acceptance. The optional
  MCP connector is a separate package `serve/mcpconnect`, out of scope for
  the 0007 decision but designed against it.
- `serve/servehttp`: `GET /sessions/{id}/tools` returning
  `action.tools.list.response`; `POST /sessions/{id}/resolve` accepts
  `action.call.resolve.request` and calls `Session.Published` after
  writing the response; `POST /adapters/{name}/sessions` forwards
  `tool_sources` and `tools`. Stdio ops `tools` and the extended `resolve`
  (with the same post-write release) and `open`.
- `client` and `clients/ts`: `Open` options for sources and tools;
  `Session.Tools`; `Session.ResolveToolCall` in all three arms
  (acknowledge, result, error), so a control layer can report that it has
  begun executing before it has a result.

### Fixtures

Positive: `tools-catalog-with-sources`, `tools-catalog-list-only`,
`tools-catalog-refresh-replaces` (a tool added by the revision 2 list is
selectable after `capabilities.updated`), `open-attach-process-source`,
`control-tool-roundtrip` (acknowledgement, then result),
`control-tool-resolved-without-ack` (`started` immediately before the
terminal), `control-tool-cancelled-with-run` (an unacknowledged call
settles `cancelled` from `requested`),
`control-tool-withheld-by-tool-choice` (a provided tool, a submit with
`mode: "none"`, no call), `open-provide-two-tools` (both listed, the
selected one called). Negative:
`tools-unmatched-source` (`unmatched_tool_source`),
`tools-list-response-unscoped` (`scope_mismatch`; a session-scoped
request answered without `session_id`),
`tools-select-removed-after-refresh` (`unsatisfiable_control`; a tool
dropped by the revision 2 list, then named),
`tools-duplicate-name` (`duplicate_tool_name`),
`tools-duplicate-source-id` (`duplicate_tool_source`; two sources with one
`id` and different endpoints), `descriptor-duplicate-tool-name`
(`duplicate_tool_name`; one name under `tools` and under
`layers.action.tools`), `descriptor-duplicate-source-id`
(`duplicate_tool_source`; two declared sources with one `id`),
`tools-catalog-omits-attachment` (`catalog_mismatch`; an open with a
process source and a provided tool, then a first list omitting the
source), `tools-catalog-drops-attachment-later` (`catalog_mismatch`; a
correct first list, then a second list omitting the provided tool),
`tools-catalog-redirects-source` (`catalog_mismatch`; the attached source
listed with another `endpoint`), `tools-catalog-alters-provided-schema`
(`catalog_mismatch`; the provided tool listed with another
`input_schema`), `tools-catalog-moves-provided-source`
(`catalog_mismatch`; the provided tool listed under another declared
source),
`open-provide-colliding-name` (`error.response` with `unsupported_feature`
then no open; validated as a correct rejection),
`open-provide-wrong-owner` (`error.response` with `unsupported_feature`,
`details.feature: "action.tools.provide"`, `details.reason:
"unsatisfiable"`, then no open; validated as a correct rejection),
`open-provide-wrong-owner-admitted` (`wrong_tool_owner`; a supplied tool
whose `execution_owner` is not the declared control participant, and the
open admitted),
`open-provide-dangling-source` (`error.response` with
`unsupported_feature` and `details.source: "ghost"`, then no open;
validated as a correct rejection),
`open-provide-colliding-name-admitted` (`duplicate_tool_name`; two
supplied tools sharing a name, and the open admitted),
`tools-refresh-collides-with-provided` (`duplicate_tool_name`; a provided
tool `foo`, then a refreshed descriptor whose native tools include
`foo`), `tools-refresh-removes-provided-source` (`unmatched_tool_source`;
a provided tool referencing a descriptor-declared source, then a refresh
that no longer declares it),
`tools-select-in-gap-unlisted` (`unsatisfiable_control` on the revision 2
list; a `named` policy admitted between `capabilities.updated` and the
new list, which then omits the tool), `tools-call-in-gap-wrong-source`
(`unmatched_tool_source` on the revision 2 list; a sourced call emitted
in the gap that the new list attributes to another source), and the
positive `tools-select-in-gap-listed` (the retained policy found listed
when the revision 2 list arrives),
`open-attach-colliding-source-admitted` (`duplicate_tool_source`; an
attached source reusing an id the descriptor declares, and the open
admitted),
`tools-call-source-mismatch` (`unmatched_tool_source`; a call naming one
tool with another tool's declared source),
`control-tool-started-before-ack` (`illegal_tool_transition`),
`control-tool-started-after-rejected-ack` (`illegal_tool_transition`;
`accepted: false`, then `started`),
`control-tool-terminal-after-rejected-result` (`illegal_tool_transition`;
an accepted acknowledgement, a rejected `result`, then `completed`),
`control-tool-result-mismatch` (`resolution_payload_mismatch`; an
accepted result X, then `completed` carrying Y),
`control-tool-error-mismatch` (`resolution_payload_mismatch`; an accepted
error, then `failed` carrying a different one),
`control-tool-state-omits-pending` (`session_state_mismatch`; a snapshot
without the unresolved call in `pending_interactions`),
`control-tool-call-owner-mismatch` (`wrong_tool_owner`; the catalog lists
the tool as harness-owned, the call names the control participant),
`control-tool-called-despite-none` (`unapplied_control`),
`control-tool-wrong-owner` (`wrong_interaction_responder`),
`control-tool-pending-at-terminal` (`pending_interaction_at_terminal`),
`open-attach-unadvertised` (`unavailable_capability` on the admission;
the open admitted), the correct rejections
`open-attach-unadvertised-rejected` and
`open-provide-unadvertised-rejected` (`error.response` with
`unsupported_feature`, `details.feature` naming
`action.tool_sources.attach` or `action.tools.provide`, then no open),
`open-attach-remote-unadvertised` (`unavailable_capability`; a `remote`
source attached on a descriptor whose attach capability lacks `mode:
"remote"`), `open-provide-dangling-source-admitted`
(`unmatched_tool_source` on the `session.open.response`; a provided tool
with `source: "ghost"` that no descriptor or attachment declares, and the
open admitted, the negative counterpart of `open-provide-dangling-source`),
`control-tool-double-accepted-resolution`
(`duplicate_interaction`; two `result` resolutions for one interaction
both answered `accepted: true`), `control-tool-double-accepted-ack`
(`duplicate_interaction`; two `started` acknowledgements for one
interaction both answered `accepted: true`),
`control-tool-ack-after-result-accepted` (`duplicate_interaction`; an
accepted `result`, then a `started` for the same interaction also
answered `accepted: true`).

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
  `queued`, `cross_session`, `not_steerable`). A steer carries no run
  controls: `model_id`, `instructions`, `tool_choice`, and
  `output_schema` on a `delivery: "steer"` submit are rejected before
  admission with `unsupported_feature` (`details.feature` naming the
  control's key, `details.reason: "unsatisfiable"`), because the target
  run's admitted controls are authoritative until its terminal and a
  steer admits no run for new controls to bind. Decision 0003's
  "re-send the controls on each submit" therefore applies to submits that
  admit a run; the validator diagnoses a `steered` admission of a request
  carrying any control as `unsatisfiable_control` (fixtures
  `steer-with-controls-rejected`, a correct rejection, and
  `steer-with-controls-admitted`, `unsatisfiable_control`).
- Response: `admission: "steered"`, `effective_delivery: "steer"`, `run_id`
  set to the target, `target_sequence` (additive, required on a `steered`
  admission) naming the target run's last emitted sequence at admission,
  `status` equal to the target's status after that sequence, which is
  the status after the last target-run envelope the adapter emitted
  before returning, and the `submission_id` that names the pending steer.
  The sequence makes the admission's position in the target's stream an
  explicit fact the adapter states rather than one the hub or the
  validator infers from surrounding transitions.
- Settlement, two new run-scoped events in the target run's sequence domain:
  `run.steer.applied` `{ "session_id", "run_id", "submission_id",
  "request_id", "message_ids", "boundary": "immediate" | "turn" |
  "tool_result" | "unknown" }` and `run.steer.dropped` `{ "session_id",
  "run_id", "submission_id", "request_id", "reason": ProtocolError }`,
  where `request_id` is the envelope `id` of the submit request the
  admission answered, so a settlement is attributable to the caller that
  submitted it without the caller ever having seen the admission. A settlement is never
  observable before the admission that names its `submission_id`: a
  harness that applies immediately emits `applied` as the first envelope
  after the response, never before it (the barrier is specified under
  Surfaces).
- State: the target's `active_runs` entry (T2) gains `pending_steers:
  [{ "submission_id", "request_id" }]` (additive), listing admitted
  steers until they settle, each with the envelope `id` of the submit
  request that admitted it. A submitter that lost the admission response
  therefore recovers by the request id it minted itself: a matching entry
  in `pending_steers` gives it the `submission_id`, and a settlement on
  the stream carrying its `request_id` tells it the outcome. A request id
  found in neither is not conclusive while the target run is nonterminal:
  the submit the caller gave up on may still be in flight in the adapter
  and be admitted after the snapshot was taken (the hub registers a
  pending steer only once the adapter returns, under Surfaces), and a
  resend would then be admitted as a second steer, because envelope ids
  are unique per trace (`duplicate_envelope_id`) and the daemon
  deduplicates nothing: a resend is a new request, not a replay. The
  caller therefore keeps reading state and the stream rather than
  resending, and absence becomes conclusive at the target run's terminal,
  since every pending steer settles before it and a `steered` admission
  is legal only against a nonterminal run: a request id that has appeared
  in neither by then was never admitted, and what to submit to the
  session's next run is a fresh decision. Nothing is ever adopted by
  mistake, because the caller never has to guess which fresh
  `submission_id` is its own; guidance is injected twice only by a caller
  that resends before that bound, which the plan leaves as the caller's
  choice rather than claiming a guarantee the wire does not give. The
  hub's fallback under Surfaces publishes this surface.
- Barrier: every admitted steer settles before the run terminal; a run that
  terminates first drops its pending steers with `run_terminated` before the
  terminal, in the run's sequence.
- `auto` on a busy session never resolves to `steer`; steering is always an
  explicit request (P0.4's rule that explicit modes never change meaning is
  kept, and an accidental steer is worse than a refused submit).

### Validator

`runState` gains `steers` (pending submissions); `steered` admissions are
legal only against a started nonterminal run in the same session with
`session.message.delivery.steer` advertised, only in answer to a request
with `delivery: "steer"` (the `submitResponse` combination table gains
the `steered`/`steer` row and rejects it for `auto`, so "`auto` never
resolves to `steer`" is enforced), and only with a `run_id` equal to the
request's `target_run_id` when one was supplied (`scope_mismatch`) and a
`target_sequence` no greater than the target's tracked cursor at the
response and a `status` equal to the status the target held after
exactly that sequence (`illegal_run_transition` otherwise, on
`/payload/target_sequence` when the trace has not reached the named
position, on `/payload/status` when it has and the status differs; the
validator keeps the target's status history by sequence for this), so
the submitter is never handed a status the target did not hold at the
position the response names: a transition the adapter emitted during
the call precedes the response and lies at or before `target_sequence`,
and one emitted after the adapter returned lies beyond it, whether the
hub published it before or after the response, so the check is exact
without any ordering inference; `applied`/`dropped` must name a
pending steer once (`unmatched_steer`, `duplicate_steer`), and a `steered`
response whose `submission_id` is already pending on that run is
`duplicate_steer` as well, since the pending set could not represent two
steers under one id and the second would never be seen to settle. The
admission's `message_ids` and the admitting request's envelope `id` are
retained with the pending steer: an `applied` settlement must report
exactly those `message_ids` whenever the response supplied them, and
every settlement's `request_id` must equal that envelope id
(`unmatched_steer` in both cases: the settlement does not match the
admission it names), so a submitter is never told that different
guidance was applied than it submitted and can always attribute a
settlement to its own request; a terminal with a pending steer
is `pending_steer_at_terminal`. A settlement whose
`submission_id` no earlier `steered` response in the trace admitted is
`unmatched_steer`, which makes the ordering barrier below a conformance
rule rather than a hub detail. Fixtures: positive `steer-immediate`
(response, then `applied` in the target's sequence), `steer-at-boundary`,
`steer-dropped-at-terminal`, `steer-status-advances-in-flight` (a
`run.status.updated` to `waiting_for_input` between the steer request and
its response, the response naming that transition's sequence as
`target_sequence` and reporting `waiting_for_input`),
`steer-status-multi-transition` (`running` to `waiting_for_input` to
`cancelling` during the call, the response naming the last sequence and
reporting `cancelling`); negative
`steer-status-stale` (`illegal_run_transition`; the same transition
before the response, the response naming its sequence but still
reporting `running`), `steer-target-sequence-ahead`
(`illegal_run_transition`; a `target_sequence` beyond the target's
cursor at the response),
`steer-settled-before-response`
(`unmatched_steer`), `steer-duplicate-settlement` (`duplicate_steer`),
`steer-pending-at-terminal` (`pending_steer_at_terminal`),
`steer-unadvertised` (`unavailable_capability` on the admission; the
request admitted `steered`),
`steer-auto-resolved-to-steer` (`illegal_run_transition`; an `auto`
request answered `steered`), `steer-target-mismatch` (`scope_mismatch`;
`target_run_id` naming one run, the response another),
`steer-status-mismatch` (`illegal_run_transition`; the target is
`waiting_for_input`, the response says `running`),
`steer-duplicate-submission-id` (`duplicate_steer`; two `steered`
responses on one run with the same `submission_id`),
`steer-applied-message-ids-mismatch` (`unmatched_steer`; the admission
returns ids A, the settlement reports ids B),
`steer-settlement-wrong-request-id` (`unmatched_steer`; the settlement's
`request_id` is not the admitting request's envelope id),
`steer-with-controls-rejected` (`error.response` with
`unsupported_feature`, `details.reason: "unsatisfiable"`, then no
admission; validated as a correct rejection),
`steer-with-controls-admitted` (`unsatisfiable_control`; a `steered`
admission of a request carrying `model_id`). `session.state`
snapshots are checked against the same record: each `active_runs[]`
entry's `pending_steers` must equal the pending set in `runState.steers`
for that run, submission and request id alike, so an omitted pending
steer, a wrong `request_id`, or a settled one still listed
is `session_state_mismatch` (fixtures `steer-state-omits-pending`,
`steer-state-retains-settled`); without this the state surface a
submitter recovers the id from could silently lie.

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
envelopes. A steer creates no run and no sequence domain, so the adapter
and hub contract is explicit rather than inherited from `start`:

- `adapter`: for an admission of `steered`, `Session.Submit` returns the
  response and a nil `EventStream`; the settlement events are emitted on
  the target run's existing stream, which the adapter already owns, and
  only after `Submit` has returned. An adapter whose harness settles
  synchronously (Hermes answers `steered` in the busy result) holds the
  settlement and releases it onto the target stream after return, and the
  memory adapter does the same, so an in-process consumer that takes the
  response from `Submit`'s return value and then reads the stream sees
  them in that order. Every target-run envelope the adapter emits before
  returning is readable from the target stream when `Submit` returns
  (emission is synchronous, as the adapters' `emit` under their operation
  lock already is), the response's `target_sequence` is the sequence of
  the last of them (the adapter assigns the target's sequences, so it
  states the boundary exactly), and its `status` is the target's status
  after that envelope, so the hub can order those envelopes ahead of the
  response by sequence alone. A non-nil stream with a `steered` admission
  is a contract violation the hub reports as an adapter error;
  `adaptertest` asserts the nil stream, the settlement on the target's
  stream, that the settlement is not readable before `Submit` returns,
  and that a status transition emitted inside the call is readable at
  return with a sequence at or below the returned `target_sequence`.
- `serve`: `Session.Submit` today calls `adoptRun` for every successful
  admission, which allocates an admission serial, counts a reader, and
  starts a drainer; for `steered` it does none of that. It releases the
  reservation it took before calling the adapter, leaves `runID` and the
  serial table untouched, and returns the response; the target run's
  drainer, already running, delivers `run.steer.applied` or
  `run.steer.dropped` to subscribers in the target's sequence. Because
  that drainer is another goroutine, the hub adds a barrier of its own
  rather than trusting the adapter's: before calling the adapter with
  `delivery: "steer"` it arms a gate on the target run's drainer that
  withholds a steer settlement and, to keep the run's sequence contiguous,
  everything the drainer reads after it; every envelope before the first
  settlement is published as it is read, during the call and after it, so
  the gate never reorders a status transition or any other pre-response
  envelope behind the response. When the adapter returns, the hub drains
  the target stream without blocking and publishes what it finds before
  handing the response to the binding, up to and including the envelope
  whose sequence is the response's `target_sequence`; from the next
  envelope on, and from the first settlement whatever its sequence, the
  gate withholds. The boundary is the one the adapter stated, not one
  inferred from intermediate statuses, so a call during which the target
  moved through several transitions is handled exactly: every envelope
  the adapter emitted before returning is readable at return (the
  adapter contract above) and carries a sequence at or below the
  boundary, so all of them precede the response in the hub's trace,
  while anything beyond the boundary was emitted after the adapter
  returned and follows the response; nothing is reordered within the
  run. A drain that cannot reach `target_sequence` (the adapter returned
  a boundary it had not made readable) and a settlement read before the
  adapter has returned are contract violations reported as adapter
  errors rather than published. After the adapter returns
  the hub registers the pending steer (`submission_id` to target run,
  reflected in `active_runs`). The gate is not lifted inside `Submit`:
  the hub
  cannot know when the response has reached the caller, and an SSE
  subscriber could otherwise see `run.steer.applied` before the submit
  caller learns the `submission_id`. `Session.Submit` returns with the
  gate held, and the binding that makes the response observable lifts it
  with `Session.Published(submissionID)`: `servehttp` after writing and
  flushing the submit response, the stdio frontend after writing the
  response line, an in-process embedder once it has handed the response
  to its own caller. If the context passed to `Submit` ends before
  `Published` is called (the HTTP client disconnected before the response
  was written, or a binding forgot the call), the hub does not simply
  publish the buffered settlement, since the submitter never learned its
  `submission_id`: it first publishes a `session.state.updated` on the
  session stream whose `active_runs` entry for the target lists the steer
  in `pending_steers`, and only then lifts the gate. A snapshot the hub
  mints is not in the adapter's run journal, which the T2 resume path
  replays from, so the hub journals every snapshot it mints, keyed by the
  run domain and sequence it preceded, and interleaves it at that position
  on replay: a subscriber that drops after the fan-out and before receipt
  resumes into the snapshot first and the settlement after it, and the
  `servehttp` e2e test covers exactly that drop. A journaled snapshot
  keeps the envelope `id` it was first published under, and because a
  session-scoped envelope advances no run cursor, a subscriber that
  received the snapshot and dropped before the settlement resumes with a
  cursor that precedes the snapshot's position and is sent it again; the
  resume contract's no-duplicates promise is kept by identity rather
  than by cursor for these interleaved envelopes (client rule below),
  and the e2e test also drops between the snapshot and the settlement
  and asserts the application sees the snapshot once. The resolve fallback
  under T3c uses the same journal. The state snapshot is
  the same surface a reconnecting submitter reads to recover the id, so
  every subscriber learns of the admission before the settlement, the
  target run's subscribers are delayed by at most the request's lifetime,
  and nothing is silently discarded. Subscribers, and any trace the hub
  records, therefore see the admission before the settlement even when an
  adapter emits inside `Submit`; the `servehttp` e2e test subscribes
  before a synchronously settling steer and asserts the settlement arrives
  after the response, two status transitions emitted during the call
  both before it (the response naming the second as `target_sequence`),
  and one emitted after `Submit` returned after it. The same branch
  is where a queued admission (T2) differs from `start`: it adopts the
  queued run's stream under a new serial but does not supersede the
  started run.
- `serve/servehttp` and the stdio frontend: pass the delivery and
  `target_run_id` through and call `Session.Published` after writing the
  response; the response is the same `session.message.submit.response`.

The hub's run-qualified cursor from T2 already covers a steer's
events because they live in the target run's domain. Neither client can
be left deciding stream scope by a type list, because a list cannot name
future additions and the two lists fail in opposite directions: the Go
client positions every frame as run-scoped, so a `session.state.updated`
with no `run_id` and a session-domain sequence would be read as a run
switch and end in a `SequenceGapError` (the daemon publishes no such
event today, so nothing exercises the path), while the TypeScript client
treats any type outside `RUN_EVENT_TYPES` (`clients/ts/src/events.ts`)
as session-scoped and does not advance the cursor, so an unlisted
`run.steer.applied`, or any future additive run event, would make the
next known event raise `SequenceGapError` and a reconnect replay it.
Both clients therefore switch to the wire's own scoping in the T3c
client slice, the first whose fallback mints a snapshot, and T4 reuses
it: an envelope carrying `run_id` is run-scoped and its sequence advances
that run's cursor whatever its type; an envelope without `run_id` but
with `sequence` is session-scoped and is delivered without touching the
run cursor whatever its type. The schema requires `run_id` on every run
event and session events carry none, so the rule is exact for known
types and correct by construction for unknown ones; the type lists stay
for typing only. A session-scoped envelope interleaved in a run's replay
is deduplicated by `id`: each client retains the ids of the
session-scoped envelopes it delivered since the last cursor-advancing
envelope (the set empties whenever the run cursor advances, so it stays
small) and drops one whose `id` it has already delivered, so a hub-minted
snapshot replayed at a position the client had already passed reaches
the application once. Tests interleave a session snapshot between run
events and across a resume, replay it across a drop between the snapshot
and the next run event and assert single delivery, and feed an unknown run-scoped type and an unknown
session-scoped type through both clients. The T4 client slice adds
`run.steer.applied` and `run.steer.dropped` to `EnvelopeType` (typing),
with cursor tests: a steer event advances the cursor, a drop after it
resumes after it, and a steer event is never delivered twice. Both
clients' e2e tests drive a steer against the memory adapter and assert
the settlement event in the target run's sequence.

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

`unapplied_control`, `unsatisfiable_control`, `degraded_without_optin`,
`model_not_in_catalog`,
`ambiguous_default_model`, `duplicate_model_id`,
`unannounced_catalog_change`, `queue_order_violation`,
`queue_limit_exceeded`, `premature_session_mutation`,
`unmatched_tool_source`, `duplicate_tool_source`, `duplicate_tool_name`,
`wrong_tool_owner`, `resolution_payload_mismatch`, `catalog_mismatch`,
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

Additive on the wire, under the layered draft's extension rules (unknown
fields are ignored; minor revisions are additive only): new optional
fields (`active_runs`, `limits`, `tool_sources`, `tools`, `source`,
`sources`, `features`, `target_run_id`, `target_sequence`, `session_id`
on the tools-list request), new envelope types in the `oneOf` (`models.*`,
`action.call.resolve.*`, `run.steer.*`), and new payload definitions in
existing schema files. No existing field's schema narrows: `tool_choice`
keeps its permissive schema and its typed shape is a validator and adapter
rule of the `run-controls` unit, and the models payloads live in the
control-plane schema file so the bundle inventory and the manifest schema
are unchanged. `version` and `profile` are unchanged.

The bundle itself is not additive, and the plan does not claim it is:
every payload object in `capabilities.schema.json`, `session.schema.json`,
and `action.schema.json` is closed (`additionalProperties: false`) and the
envelope `oneOf` is fixed, so a validator compiled from an older bundle
revision rejects a gated addition as small as `limits` on
`capabilities.response`. The Go client's opt-in `WithEnvelopeValidation`
(dev mode, off by default) would fail against an upgraded endpoint for
that reason; the TypeScript client does no runtime schema validation. The
phase therefore begins with a tolerance step that lands before T1 and that
every later unit relies on:

- `validation.CompileSchemas` gains a tolerant variant, used by the Go
  client's dev-mode validation and by `oap validate` when it is pointed at
  a live endpoint rather than a fixture. It compiles the same bundle with
  `additionalProperties: false` lifted from payload objects (unknown
  members are ignored, as the wire rule requires), with extensible leaf
  `enum` constraints inside payloads lifted to their base type (an
  unknown enum value surfaces as a string, as the layered draft's
  extension rule requires, instead of failing the message; a `const` is
  never lifted, as set out below), and with the
  envelope `oneOf` relaxed to "a known `type` must match its branch; an
  unknown `type` must satisfy the common envelope fields only", the same
  forward compatibility both clients already apply to unknown named SSE
  events. Discriminators stay exact: the envelope-level `type`,
  `protocol`, `version`, and `profile`, and every `enum` or `const` that
  guards a conditional or a union branch. `interaction.schema.json` has
  four such guards (`kind: "text"` against `kind: single_choice |
  multi_choice` on `user.input.requested`, `status: "submitted"` against
  `"cancelled"` on its resolution); lifting them would make both branches
  of each `if` match every payload, forbidding and requiring `options` at
  once, so the tolerant compile keeps every `enum`/`const` inside an `if`
  guard exact and lifts the extensible `enum` leaves elsewhere, including
  the outer `question.properties.kind.enum` that those guards compare: an
  unknown `kind: "date"` then matches neither guard, takes neither
  `then`, and surfaces as a string as the extension rule promises, whereas
  exempting the whole property would still reject it. A `const` is never
  lifted: it is a fixed semantic value, not an extensible vocabulary
  (`run.started.status` is `const: "running"`, and a `run.started` with
  `status: "failed"` is malformed under any revision), and the same holds
  for the `protocol`, `version`, and `profile` constants. The step's tests
  validate a payload carrying a new leaf enum value, and a
  `user.input.requested` with an unknown `kind`, against the old bundle
  in both modes (tolerated tolerant, rejected strict), and a `run.started`
  with `status: "failed"` rejected in both. Discriminated unions get an
  explicit fallback rather than exact guards alone: keeping every branch's
  `const` exact would still make `common.schema.json#/$defs/contentPart`
  reject an additive part such as `{ "type": "audio", ... }`, because no
  branch matches, although the layered draft already names `audio` and
  `file` as content kinds. The tolerant compile therefore rewrites each
  `oneOf`/`anyOf` whose branches pin a `const` on one property into the
  same branches plus one fallback branch that accepts an object whose
  discriminator is a string outside the known set (`not: { enum: [known
  values] }`) and that carries only the members every branch requires;
  known branches stay strict, an unknown kind is tolerated as the wire
  rule requires, and a known kind with a malformed body is still
  rejected. The tests add an `audio` content part accepted tolerantly and
  rejected strictly, and a `text` part missing `text` rejected in both,
  and,
  as the regression guard, every fixture in the manifest under the
  tolerant compile, which must accept everything the strict compile
  accepts.
- The stateful validator tolerates on the same terms when it runs in
  tolerant mode, because a schema that admits an unknown envelope is not
  enough on its own: `validation/state.go` advances a run's sequence
  cursor only for the types `isRunEvent` enumerates, so a tolerated
  unknown run envelope at sequence N would be ignored and the next known
  event at N+1 diagnosed as `sequence_gap`. In tolerant mode an envelope
  of unknown `type` is classified by its wire scope, as the T2 client
  change classifies frames: one carrying `run_id` is a run-scoped event
  that enters `runEvent` for the type-independent bookkeeping (scope
  agreement, an accepted admission, sequence contiguity and regression,
  nothing after a terminal, `duplicate_envelope_id`) and for no
  type-specific rule, including the pre-`run.started` rule, which exempts
  pre-start settlements by type and cannot be applied to a type it does
  not know; one carrying `session_id` alone is session-scoped and joins
  the scope checks only; one carrying neither is endpoint or protocol
  scoped and is passed through. Strict mode is unchanged: an unknown
  type fails the schema before the stateful pass sees it. The step's
  tests feed a trace with an additive run envelope at sequence 2 between
  `run.started` and `run.completed` (tolerant: valid, with the cursor
  advanced; strict: the schema rejection and nothing else), and the same
  envelope after the terminal (tolerant: `event_after_terminal`, because
  scope classification makes the domain rules apply).
- Fixture validation stays strict against the bundle at its own revision.
  That is the conformance validator's job and how a misspelled new field is
  caught; each unit extends the bundle in place under `schema/v0.1`.
- `capabilities.response` gains no schema-revision field: a consumer learns
  what an endpoint will emit from the capability keys the units add, and
  the tolerant compile makes the fields those keys imply harmless to a
  client that predates them.

Until the tolerance step lands, an older strict validator is incompatible
with a newer endpoint's additions. No released consumer exists today, so
sequencing the step first is sufficient, and any external consumer that
ships before it must validate tolerantly or not at all. This is the
answer to issue #13's versioning question: the wire stays `0.1` because
nothing existing changes meaning, and forward compatibility is made a
stated property of validators rather than an assumed property of the
bundle.

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

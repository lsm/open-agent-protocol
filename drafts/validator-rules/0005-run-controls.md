# Run controls

Decisions 0005, 0019, 0021, 0022, 0023, 0024, 0031 and 0034: the rows behind
every diagnostic the validators raise for the four run controls (`model_id`,
`instructions`, `tool_choice`, `output_schema`), their gate, their disclosure
and their application. Go names the function in `go/validation/`, Zig in
`zig/src/validation/`, Rust in `rust/validation/src/`. A row whose expectation
is settled on the correlated response names the function that sets it. No open
items. Zig's divergences here are 0019's recorded ones, listed below. Rust's
missing checks (the `run.started` model and the per-run default) are port gaps
recorded in `fixtures/known-gaps.json`.
Format: [README](README.md).

### [0005] Controls are per submit and fail closed

Source: [`decisions/0005-run-controls.md`](../../decisions/0005-run-controls.md#controls-are-per-submit-and-fail-closed).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0005#controls-are-per-submit-and-fail-closed/1` | A submission carrying a control the current descriptor does not affirmatively advertise MUST be refused `unsupported_feature`, naming the key, with `details.reason: "unadvertised"`. | L90–132 | `unavailable_capability` | `controls.go:submitControls` | `semantic.zig:submitControls` | `state/controls.rs:submit_controls` |
| `0005#controls-are-per-submit-and-fail-closed/2` | A control advertised `degraded`, absent from the caller's `allow_degraded_features`, MUST be refused `capability_degraded`. | L90–132 | `degraded_without_optin` | `controls.go:submitControls` | `semantic.zig:submitControls` | `state/controls.rs:submit_controls` |
| `0005#controls-are-per-submit-and-fail-closed/3` | Where several conditions hold, the refusal MUST be the highest on the ladder: capability, then degradation, then unsatisfiability, then state. Within a rung the lower key wins, and within one rule the lowest JSON Pointer. | L90–132 | the highest applicable rung's code | `controls.go:submitControls`<br>`refusal.go:retainedExpectations` | `semantic.zig:submitControls` | `state/controls.rs:submit_controls` |
| `0005#controls-are-per-submit-and-fail-closed/4` | A `steer` submission carrying a control MUST be refused unsatisfiable. | (drafted here: 0005 makes controls per submit; 0013 forbids a steer to change admitted controls) | `unsatisfiable_control` | `controls.go:submitControls` | `semantic.zig:submitControls` | `state/controls.rs:submit_controls` |
| `0005#controls-are-per-submit-and-fail-closed/5` | A control-carrying submission MUST be judged against a current capability descriptor. One sent with none, or after the descriptor was invalidated, is undecidable. | (drafted here: 0001's rule that an optional feature needs a current descriptor, applied to controls) | `unavailable_capability` | `controls.go:controlDescriptor` | `semantic.zig:controlDescriptor` | `state/controls.rs:control_descriptor` |
| `0005#controls-are-per-submit-and-fail-closed/6` | A control-carrying submission MUST cite the active `capability_revision`. | agent-control-core L779–817 (the freshness rule, `agent-control-core#capability-snapshot-freshness/1`, applied to a control-carrying submission) | `stale_capability_revision` | `controls.go:controlDescriptor` | `semantic.zig:controlDescriptor` | `state/controls.rs:control_descriptor` |
| `0005#controls-are-per-submit-and-fail-closed/7` | A `tool_choice` judged against a catalog that lists one name twice MUST be diagnosed, unless the descriptor already carries that ambiguity. | 0008 L153–223 | `duplicate_tool_name` | `controls.go:duplicateToolNames` | `semantic.zig:duplicateToolNames` | `state/controls.rs:duplicate_tool_names` |

### [0005] The gate is judged on the correlated response

Source: [`decisions/0005-run-controls.md`](../../decisions/0005-run-controls.md#the-gate-is-judged-on-the-correlated-response).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0005#the-gate-is-judged-on-the-correlated-response/1` | Where a refusal was owed, the submission MUST NOT be admitted. | L135–147 | the owed code | `controls.go:settleSubmitAdmission` | `semantic.zig:settleSubmitAdmission` | `state/controls.rs:settle_submit_admission` |
| `0005#the-gate-is-judged-on-the-correlated-response/2` | An owed refusal MUST carry the code, feature and reason, or the detail, that tells the caller what to change. | L135–147 | the owed code | `controls.go:settleControlRefusal` | `semantic.zig:settleControlRefusal` | `state/controls.rs:settle_control_refusal` |
| `0005#the-gate-is-judged-on-the-correlated-response/3` | A control that is within every disclosed constraint MUST NOT be refused `unsupported_feature` naming it. | L135–147 | `unsatisfiable_control` | `controls.go:settleControlRefusal` | `semantic.zig:settleControlRefusal` | `state/controls.rs:settle_control_refusal` |
| `0005#the-gate-is-judged-on-the-correlated-response/4` | An admission of a `model_id` MUST report that model. | L135–147 | `unapplied_control` | `controls.go:settleSubmitAdmission` | `semantic.zig:settleSubmitAdmission` | `state/controls.rs:settle_submit_admission` |
| `0005#the-gate-is-judged-on-the-correlated-response/5` | A `run.started` of a run admitted under a `model_id` MUST name that model. | L246–263 | `unapplied_control` | `state.go:runEvent` | `semantic.zig:runEvent` | `state/runs.rs:advance` |
| `0005#the-gate-is-judged-on-the-correlated-response/6` | A `run.started` naming a model MUST name the admitted one. | L246–263 | `unapplied_control` | `state.go:runEvent` | `semantic.zig:runEvent` | `state/runs.rs:advance` |

### [0005] Presence is what the gate judges

Source: [`decisions/0005-run-controls.md`](../../decisions/0005-run-controls.md#presence-is-what-the-gate-judges).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0005#presence-is-what-the-gate-judges/1` | A present, empty `model_id` MUST be refused `model_not_found` with `details.model_id: ""`, whether or not a catalog was served. | L75–87 | `unsatisfiable_control` | `controls.go:unsatisfiable` | `semantic.zig:unsatisfiable` | `state/controls.rs:unsatisfiable` |

### [0022] At this layer `tool_choice` is a filter

Source: [`decisions/0022-tool-choice-is-a-filter-here-and-a-mode-there.md`](../../decisions/0022-tool-choice-is-a-filter-here-and-a-mode-there.md#at-this-layer-tool_choice-is-a-filter).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0022#at-this-layer-tool_choice-is-a-filter/1` | A `tool_choice` that is not the typed filter (`allowed` or `disallowed`, exclusively) MUST be refused unsatisfiable. | L55–71 | `unsatisfiable_control` | `controls.go:unsatisfiable` | `semantic.zig:unsatisfiable` | `state/controls.rs:unsatisfiable` |

### [0023] Only `allowed` is checked against the catalog

Source: [`decisions/0023-a-denylist-does-not-assert-the-catalog.md`](../../decisions/0023-a-denylist-does-not-assert-the-catalog.md#only-allowed-is-checked-against-the-catalog).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0023#only-allowed-is-checked-against-the-catalog/1` | Under a known catalog, an `allowed` entry naming a tool the session's catalog does not list MUST be refused unsatisfiable, `details.tool` naming it. A `disallowed` entry is never a defect. | 0022 L55–71, L46–55 | `unsatisfiable_control` | `controls.go:unsatisfiable` (protocol.ToolChoice.Unsatisfiable) | `semantic.zig:unsatisfiable` | `state/controls.rs:unsatisfiable` |

### [0024] `tool_choice` is judged against the session's catalog

Source: [`decisions/0024-the-filter-judges-the-session-catalog.md`](../../decisions/0024-the-filter-judges-the-session-catalog.md#tool_choice-is-judged-against-the-sessions-catalog).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0024#tool_choice-is-judged-against-the-sessions-catalog/1` | The catalog a `tool_choice` is judged against MUST be the descriptor's tools plus the tools provided to that session, including the tools the same compound open supplies. | L45–66 | `unsatisfiable_control` | `controls.go:toolCatalog` | `semantic.zig:sessionCatalog` | `state/controls.rs:tool_catalog` |

### [0034] Publishing `tools` is what makes the catalog known

Source: [`decisions/0034-an-unpublished-catalog-is-unknown.md`](../../decisions/0034-an-unpublished-catalog-is-unknown.md#publishing-tools-is-what-makes-the-catalog-known).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0034#publishing-tools-is-what-makes-the-catalog-known/1` | The catalog MUST be known only once a `capabilities.response` carries a `tools` member, at the top level or in a layer, and empty counts. Under an unknown catalog, no `allowed` entry is unsatisfiable. | L46–56 | `unsatisfiable_control` (stands down) | `controls.go:toolCatalog`<br>`controls.go:publishesCatalog` | `semantic.zig:collectCatalog` | `state/controls.rs:tool_catalog`<br>`publishes_catalog` |

### [0005] `output_schema` is compiled, never resolved

Source: [`decisions/0005-run-controls.md`](../../decisions/0005-run-controls.md#output_schema-is-compiled-never-resolved).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0005#output_schema-is-compiled-never-resolved/1` | An `output_schema` that does not describe an object, refers outside the document, or fails to compile MUST be refused unsatisfiable, `details.field: "output_schema"`. | L201–213 | `unsatisfiable_control` | `controls.go:unsatisfiable` (CompileOutputSchema) | `semantic.zig:unsatisfiable` | `state/controls.rs:unsatisfiable` (output_schema.rs) |
| `0005#output_schema-is-compiled-never-resolved/2` | A `run.completed` under an admitted `output_schema` MUST carry `result`. | L201–213 | `unapplied_control` | `controls.go:checkCompletedControls` | `semantic.zig:checkCompletedControls` | `state/controls.rs:check_completed_controls` |
| `0005#output_schema-is-compiled-never-resolved/3` | That `result` MUST conform to the admitted schema. | L201–213 | `unapplied_control` | `controls.go:checkCompletedControls` | `semantic.zig:checkCompletedControls` | `state/controls.rs:check_completed_controls` |

### [0005] Disclosure is machine-readable

Source: [`decisions/0005-run-controls.md`](../../decisions/0005-run-controls.md#disclosure-is-machine-readable).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0005#disclosure-is-machine-readable/1` | An `output_schema` that a disclosed `fixed_result` cannot satisfy MUST be refused unsatisfiable. | L150–180 | `unsatisfiable_control` | `controls.go:unsatisfiable` | `semantic.zig:unsatisfiable` | `state/controls.rs:unsatisfiable` |
| `0005#disclosure-is-machine-readable/2` | Under a disclosed `fixed_result`, the `result` MUST equal it by value. | L150–180 | `unapplied_control` | `controls.go:checkCompletedControls` | `semantic.zig:checkCompletedControls` | `state/controls.rs:check_completed_controls` |

### [0022] `mode` becomes `scope`

Source: [`decisions/0022-tool-choice-is-a-filter-here-and-a-mode-there.md`](../../decisions/0022-tool-choice-is-a-filter-here-and-a-mode-there.md#mode-becomes-scope).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0022#mode-becomes-scope/1` | `run.model_selection` advertised affirmatively MUST disclose `scope` as `run` or `session`. | 0005 L150–180, L94–119 | `undisclosed_selection_scope` | `controls.go:checkSelectionModes` | `semantic.zig:checkAttachModes` | `state/controls.rs:check_selection_modes` |

### [0021] Disclosure is optional here, and that is a rule about rules

Source: [`decisions/0021-a-tool-policy-says-whether-it-outlives-its-run.md`](../../decisions/0021-a-tool-policy-says-whether-it-outlives-its-run.md#disclosure-is-optional-here-and-that-is-a-rule-about-rules).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0021#disclosure-is-optional-here-and-that-is-a-rule-about-rules/1` | `run.tool_selection` MAY omit `scope`. One it discloses MUST be `run` or `session`. | L49–62, L65–88, 0022 L94–119 | `undisclosed_selection_scope` | `controls.go:checkSelectionModes` | `semantic.zig:checkAttachModes` | `state/controls.rs:check_selection_modes` |

### [0005] A per-run selection does not move the session default

Source: [`decisions/0005-run-controls.md`](../../decisions/0005-run-controls.md#a-per-run-selection-does-not-move-the-session-default).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0005#a-per-run-selection-does-not-move-the-session-default/1` | After a `model_id` admitted under `scope: "run"`, `current_model_id` MUST stay the retained default, after the run's terminal and across a capability refresh. | L183–198, 0022 L94–119 | `unapplied_control` | `state.go:apply` | `semantic.zig:stateDocument` | — |

### [0005] `run.completed` gains `model_id`

Source: [`decisions/0005-run-controls.md`](../../decisions/0005-run-controls.md#runcompleted-gains-model_id).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0005#runcompleted-gains-model_id/1` | A `run.completed` naming a `model_id` MUST name the admitted model. | L216–221 | `unapplied_control` | `controls.go:checkCompletedControls` | `semantic.zig:checkCompletedControls` | `state/controls.rs:check_completed_controls` |

### [0031] The rule is judged at settlement

Source: [`decisions/0031-a-policy-refusal-is-a-settlement.md`](../../decisions/0031-a-policy-refusal-is-a-settlement.md#the-rule-is-judged-at-settlement).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0031#the-rule-is-judged-at-settlement/1` | A call to a tool the admitted `tool_choice` excludes MUST settle `action.call.failed` with `error.code: "refused_by_policy"`. Any other settlement is diagnosed at the settling envelope. | L70–90, 0022 L55–71, L74–81 | `unapplied_control` | `controls.go:checkCallSettlement` | `semantic.zig:checkCallSettlement` | `state/controls.rs:check_call_settlement` |
| `0031#the-rule-is-judged-at-settlement/2` | A call to a permitted tool MUST NOT settle with `refused_by_policy`. | L70–90 | `unapplied_control` | `controls.go:checkCallSettlement` | `semantic.zig:checkCallSettlement` | `state/controls.rs:check_call_settlement` |
| `0031#the-rule-is-judged-at-settlement/3` | A call to an excluded tool still unsettled at its run's terminal is diagnosed at the terminal. | L70–90 | `unapplied_control` | `controls.go:sweepExcludedCalls` | `semantic.zig:sweepExcludedCalls` | `state/controls.rs:settle_run_controls` |

## Process rules with no diagnostic

- 0005 § The unit claims a discipline and, separately, execution, and 0031 §
  `emulated` means the endpoint enforces the filter, define what an
  advertisement claims. Nothing in a trace distinguishes `native` from
  `emulated`.
- 0019 is process: one binary, port order, and the gate. Its § A rule the port
  cannot reach is recorded, not approximated, lists deliberate Zig omissions
  that touch rows here. Zig compares a `fixed_result` past 2^53 exactly, where
  Go compares by value. Zig also raises `unsatisfiable_control` on
  `output_schema` keywords outside its interpreter that Go admits. Both are
  recorded divergences, not open questions.
- Rust has no `run.started` model check and no per-run-default guard (the rows
  with a Rust `—`). Those are port gaps, which `fixtures/known-gaps.json`
  records against `controls-started-omits-model` and the `controls-per-run-*`
  fixtures, not divergences.
- The `model_id` catalog miss (`model_not_in_catalog` from
  `controls.go:unsatisfiable`) is 0006's rule. 0007 adds when a reservation's
  model applies, which moves when the `run.started` rows above are judged but
  not what they require.

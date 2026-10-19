# Validator rules: run controls

Decisions [0005](../../decisions/0005-run-controls.md),
[0019](../../decisions/0019-one-binary.md),
[0021](../../decisions/0021-a-tool-policy-says-whether-it-outlives-its-run.md),
[0022](../../decisions/0022-tool-choice-is-a-filter-here-and-a-mode-there.md),
[0023](../../decisions/0023-a-denylist-does-not-assert-the-catalog.md),
[0024](../../decisions/0024-the-filter-judges-the-session-catalog.md),
[0031](../../decisions/0031-a-policy-refusal-is-a-settlement.md) and
[0034](../../decisions/0034-an-unpublished-catalog-is-unknown.md).
Each row is one diagnostic site. The Go, Zig and Rust columns name the emitting
function, `—` where that tree has none. A row whose expectation is settled on
the correlated response names the function that sets the expectation. Go files
are under `go/validation/`, Zig's is `zig/src/validation/semantic.zig`, and
Rust's are under `rust/validation/src/`.

The four controls are `model_id` (`run.model_selection`), `instructions`
(`run.instructions`), `tool_choice` (`run.tool_selection`) and `output_schema`
(`run.structured_output`).

## The gate

| id | rule | source | code | Go | Zig | Rust | fixtures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 0005#controls-are-per-submit-and-fail-closed | A submission carrying a control the current descriptor does not affirmatively advertise MUST be refused `unsupported_feature`, naming the key, with `details.reason: "unadvertised"`. | 0005 § Controls are per submit and fail closed | `unavailable_capability` | controls.go:submitControls | submitControls | state/controls.rs:submit_controls | controls-unadvertised-model, controls-unadvertised-instructions, controls-unadvertised-tool-choice, controls-unadvertised-output-schema, controls-unadvertised-wrong-reason |
| 0005#controls-are-per-submit-and-fail-closed/2 | A control advertised `degraded`, absent from the caller's `allow_degraded_features`, MUST be refused `capability_degraded`. | 0005 § Controls are per submit and fail closed | `degraded_without_optin` | controls.go:submitControls | submitControls | state/controls.rs:submit_controls | controls-degraded-admitted-without-optin, controls-degraded-wrong-refusal |
| 0005#controls-are-per-submit-and-fail-closed/3 | Where several conditions hold, the refusal MUST be the highest on the ladder: capability, then degradation, then unsatisfiability, then state. Within a rung the lower key wins, and within one rule the lowest JSON Pointer. | 0005 § Controls are per submit and fail closed | the losing rung's code | controls.go:submitControls (ladder order), refusal.go | submitControls | state/controls.rs:submit_controls | controls-refusal-omits-feature, controls-refusal-names-wrong-feature |
| 0005#controls-are-per-submit-and-fail-closed/4 | A `steer` submission carrying a control MUST be refused unsatisfiable. | drafted here (0005 makes controls per submit; 0013 forbids a steer to change admitted controls) | `unsatisfiable_control` | controls.go:submitControls | submitControls | state/controls.rs:submit_controls | steer-with-model_id-wrong-refusal, steer-with-instructions-wrong-refusal, steer-with-controls-admitted |
| 0005#controls-are-per-submit-and-fail-closed/5 | A control-carrying submission MUST be judged against a current capability descriptor. One sent with none, or after the descriptor was invalidated, is undecidable. | drafted here (0001's rule that an optional feature needs a current descriptor, applied to controls) | `unavailable_capability` | controls.go:controlDescriptor | controlDescriptor | state/controls.rs:control_descriptor | — |
| 0005#controls-are-per-submit-and-fail-closed/6 | A control-carrying submission MUST cite the active `capability_revision`. | 0001 capability revision rule, applied to controls | `stale_capability_revision` | controls.go:controlDescriptor | controlDescriptor | state/controls.rs:control_descriptor | — |
| 0005#the-gate-is-judged-on-the-correlated-response | Where a refusal was owed, the submission MUST NOT be admitted. | 0005 § The gate is judged on the correlated response | the owed code | controls.go:settleSubmitAdmission | settleSubmitAdmission | state/controls.rs:settle_submit_admission | controls-empty-model-id-admitted |
| 0005#the-gate-is-judged-on-the-correlated-response/2 | An owed refusal MUST carry the code, feature and reason, or the detail, that tells the caller what to change. | 0005 § The gate is judged on the correlated response | the owed code | controls.go:settleControlRefusal | settleControlRefusal | state/controls.rs:settle_control_refusal | controls-empty-model-id-wrong-refusal, controls-unsatisfiable-wrong-refusal |
| 0005#the-gate-is-judged-on-the-correlated-response/3 | A control that is within every disclosed constraint MUST NOT be refused `unsupported_feature` naming it. | 0005 § The gate is judged on the correlated response | `unsatisfiable_control` | controls.go:settleControlRefusal | settleControlRefusal | state/controls.rs:settle_control_refusal | controls-instructions-refused, controls-model-refused-as-unsupported, controls-structured-satisfiable-refused, controls-tool-choice-listed-false-refusal |
| 0005#the-gate-is-judged-on-the-correlated-response/4 | An admission of a `model_id` MUST report that model. | 0005 § The gate is judged on the correlated response | `unapplied_control` | controls.go:settleSubmitAdmission | settleSubmitAdmission | state/controls.rs:settle_submit_admission | — |

## Values

| id | rule | source | code | Go | Zig | Rust | fixtures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 0005#presence-is-what-the-gate-judges | A present, empty `model_id` MUST be refused `model_not_found` with `details.model_id: ""`, whether or not a catalog was served. | 0005 § Presence is what the gate judges | `unsatisfiable_control` | controls.go:unsatisfiable | unsatisfiable | state/controls.rs:unsatisfiable | controls-empty-model-id-admitted, controls-empty-model-id-wrong-refusal |
| 0022#at-this-layer-tool_choice-is-a-filter | A `tool_choice` that is not the typed filter (`allowed` or `disallowed`, exclusively) MUST be refused unsatisfiable. | 0022 § At this layer `tool_choice` is a filter | `unsatisfiable_control` | controls.go:unsatisfiable | unsatisfiable | state/controls.rs:unsatisfiable | controls-tool-choice-null |
| 0023#only-allowed-is-checked-against-the-catalog | Under a known catalog, an `allowed` entry naming a tool the session's catalog does not list MUST be refused unsatisfiable, `details.tool` naming it. A `disallowed` entry is never a defect. | 0022 § At this layer `tool_choice` is a filter; 0023 § Only `allowed` is checked against the catalog | `unsatisfiable_control` | controls.go:unsatisfiable (protocol.ToolChoice.Unsatisfiable) | unsatisfiable | state/controls.rs:unsatisfiable | controls-tool-choice-unknown-entry, controls-tool-choice-layer-published-catalog |
| 0024#tool_choice-is-judged-against-the-sessions-catalog | The catalog a `tool_choice` is judged against MUST be the descriptor's tools plus the tools provided to that session, including the tools the same compound open supplies. | 0024 § `tool_choice` is judged against the session's catalog | `unsatisfiable_control` | controls.go:toolCatalog | sessionCatalog | state/controls.rs:tool_catalog | — |
| 0034#publishing-tools-is-what-makes-the-catalog-known | The catalog MUST be known only once a `capabilities.response` carries a `tools` member, at the top level or in a layer, and empty counts. Under an unknown catalog, no `allowed` entry is unsatisfiable. | 0034 § Publishing `tools` is what makes the catalog known | `unsatisfiable_control` (stands down) | controls.go:toolCatalog, publishesCatalog | collectCatalog | state/controls.rs:tool_catalog, publishes_catalog | controls-tool-choice-unpublished-catalog-ignored, controls-tool-choice-published-empty-catalog |
| 0005#controls-are-per-submit-and-fail-closed/7 | A `tool_choice` judged against a catalog that lists one name twice MUST be diagnosed, unless the descriptor already carries that ambiguity. | 0008 § A catalog resolves (the policy-time check defers to the descriptor) | `duplicate_tool_name` | controls.go:duplicateToolNames | duplicateToolNames | state/controls.rs:duplicate_tool_names | controls-tool-choice-ambiguous-name |
| 0005#output_schema-is-compiled-never-resolved | An `output_schema` that does not describe an object, refers outside the document, or fails to compile MUST be refused unsatisfiable, `details.field: "output_schema"`. | 0005 § `output_schema` is compiled, never resolved | `unsatisfiable_control` | controls.go:unsatisfiable (CompileOutputSchema) | unsatisfiable | state/controls.rs:unsatisfiable (output_schema.rs) | controls-structured-non-object-schema, controls-structured-external-ref, controls-structured-keyword-value-mistyped, controls-structured-uncompilable-schema |
| 0005#disclosure-is-machine-readable | An `output_schema` that a disclosed `fixed_result` cannot satisfy MUST be refused unsatisfiable. | 0005 § Disclosure is machine-readable | `unsatisfiable_control` | controls.go:unsatisfiable | unsatisfiable | state/controls.rs:unsatisfiable | controls-structured-fixed-result-refuses-satisfiable, controls-structured-undisclosed-constraint |

## Disclosure

| id | rule | source | code | Go | Zig | Rust | fixtures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 0022#mode-becomes-scope | `run.model_selection` advertised affirmatively MUST disclose `scope` as `run` or `session`. | 0005 § Disclosure is machine-readable; 0022 § `mode` becomes `scope` | `undisclosed_selection_scope` | controls.go:checkSelectionModes | checkAttachModes | state/controls.rs:check_selection_modes | controls-model-scope-missing, controls-model-scope-unknown |
| 0021#disclosure-is-optional-here-and-that-is-a-rule-about-rules | `run.tool_selection` MAY omit `scope`. One it discloses MUST be `run` or `session`. | 0021 § `run.tool_selection` discloses the same mode pair; § Disclosure is optional here; 0022 § `mode` becomes `scope` | `undisclosed_selection_scope` | controls.go:checkSelectionModes | checkAttachModes | state/controls.rs:check_selection_modes | controls-tool-selection-unknown-scope |

## Application

| id | rule | source | code | Go | Zig | Rust | fixtures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 0005#a-per-run-selection-does-not-move-the-session-default | After a `model_id` admitted under `scope: "run"`, `current_model_id` MUST stay the retained default, after the run's terminal and across a capability refresh. | 0005 § A per-run selection does not move the session default; 0022 § `mode` becomes `scope` | `unapplied_control` | state.go:apply | stateDocument | — | controls-per-run-overwrites-default, controls-per-run-changes-default, controls-per-run-overwrites-default-at-terminal, controls-per-run-overwrites-default-after-refresh |
| 0005#the-gate-is-judged-on-the-correlated-response/5 | A `run.started` of a run admitted under a `model_id` MUST name that model. | 0005 § Consequences (`run.started`'s model disagreement is `unapplied_control`) | `unapplied_control` | state.go:runEvent | runEvent | — | controls-started-omits-model |
| 0005#the-gate-is-judged-on-the-correlated-response/6 | A `run.started` naming a model MUST name the admitted one. | 0005 § Consequences | `unapplied_control` | state.go:runEvent | runEvent | — | — |
| 0005#runcompleted-gains-model_id | A `run.completed` naming a `model_id` MUST name the admitted model. | 0005 § `run.completed` gains `model_id` | `unapplied_control` | controls.go:checkCompletedControls | checkCompletedControls | state/controls.rs:check_completed_controls | controls-model-mismatch |
| 0005#output_schema-is-compiled-never-resolved/2 | A `run.completed` under an admitted `output_schema` MUST carry `result`. | 0005 § `output_schema` is compiled, never resolved | `unapplied_control` | controls.go:checkCompletedControls | checkCompletedControls | state/controls.rs:check_completed_controls | controls-structured-missing-result |
| 0005#output_schema-is-compiled-never-resolved/3 | That `result` MUST conform to the admitted schema. | 0005 § `output_schema` is compiled, never resolved | `unapplied_control` | controls.go:checkCompletedControls | checkCompletedControls | state/controls.rs:check_completed_controls | controls-structured-nonconforming-result |
| 0005#disclosure-is-machine-readable/2 | Under a disclosed `fixed_result`, the `result` MUST equal it by value. | 0005 § Disclosure is machine-readable | `unapplied_control` | controls.go:checkCompletedControls | checkCompletedControls | state/controls.rs:check_completed_controls | controls-structured-fixed-result-not-emitted |
| 0031#the-rule-is-judged-at-settlement | A call to a tool the admitted `tool_choice` excludes MUST settle `action.call.failed` with `error.code: "refused_by_policy"`. Any other settlement is diagnosed at the settling envelope. | 0031 § The rule is judged at settlement; 0022 § `none` is an empty allowlist | `unapplied_control` | controls.go:checkCallSettlement | checkCallSettlement | state/controls.rs:check_call_settlement | controls-tool-choice-ignored, controls-tool-choice-refused-other-code, controls-tool-choice-excluded-cancelled, controls-tool-choice-uncatalogued-call, controls-tool-choice-empty-allowlist-call |
| 0031#the-rule-is-judged-at-settlement/2 | A call to a permitted tool MUST NOT settle with `refused_by_policy`. | 0031 § The rule is judged at settlement | `unapplied_control` | controls.go:checkCallSettlement | checkCallSettlement | state/controls.rs:check_call_settlement | controls-tool-choice-permitted-refused |
| 0031#the-rule-is-judged-at-settlement/3 | A call to an excluded tool still unsettled at its run's terminal is diagnosed at the terminal. | 0031 § The rule is judged at settlement | `unapplied_control` | controls.go:sweepExcludedCalls | sweepExcludedCalls | state/controls.rs:settle_run_controls | controls-tool-choice-excluded-unsettled |

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
- The `model_id` catalog miss (`model_not_in_catalog` from
  `controls.go:unsatisfiable`) is 0006's rule. 0007 adds when a reservation's
  model applies, which moves when the `run.started` rows above are judged but
  not what they require.

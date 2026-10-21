# Validation Coverage

Status: audit of `main` at `fd04e53d4a`, 2026-10-18, with Zig cells updated since as ports landed. A ledger, not a plan: it
records what the shared validators enforce and what they do not, and fixes
nothing.

The question for every rule is three answers:

- **Code:** which diagnostic, if any, a trace breaking the rule produces.
- **Go / Zig:** whether `go/validation` and `zig/src/validation` both produce
  it.
- **Fixtures:** whether `fixtures/manifest.json` holds a positive trace and a
  `semantic-invalid` (or `schema-invalid`) trace for it.

## How to read it, and what it cannot see

- **A green Zig gate is coverage only outside the known gaps.**
  `zig/src/validation/semantic_gate.zig` expects every code a fixture declares
  and accepts a disagreement only for a fixture listed under `zig` in
  `fixtures/known-gaps.json`; it fails when a listed fixture agrees or is never
  judged. `fixture_gate.zig` judges the `decode` and `schema` phases, and since
  #1261 the `load-invalid` fixtures too, exempting only the codes the Zig
  loader does not yet refuse. The "Zig" column below is read from the source
  (`semantic.implemented`, `provider_semantic.implemented`, `packs.zig`),
  never from the gate's result.
- **`partial`** means Zig emits the code for some traces and the fixtures it
  misses are listed in `fixtures/known-gaps.json`.
- **Rules are read at the granularity their source numbers them.** A decision's
  `###` rule is one row; a rule whose text holds several obligations is split
  only where the validators split it.
- **Not every rule is a trace rule.** Process rules, binary and packaging rules,
  and the hub's wire, which carries ops rather than envelopes, are marked with
  where they are pinned instead (`goap check`, hub tests, the conformance
  runner), so a reader does not mistake "no code" for "unchecked".
- Counts were computed from the tree, not from memory: the code inventory from
  the constants each validator declares and the lists it implements, the
  fixture counts from the manifest, and the per-decision counts from each
  fixture's `provenance`. 56 of the manifest's 744 fixtures carry no
  provenance; most are Decision 0001's core traces.

Known Go-only codes are being ported to Zig separately and are listed in
[their own section](#go-only-codes-being-ported) rather than ranked here.

## Diagnostic codes

Every code either tree declares, or a fixture expects. "defined, not in implemented list" means Zig declares the constant but the gate treats it as unported.

| code | phase | Go | Zig | agent fixtures | provider fixtures | cited by |
| --- | --- | --- | --- | --- | --- | --- |
| `ambiguous_default_model` | semantic | yes | yes | 1 | 0 | 0006, staged-units-graduation |
| `attachment_field_in_catalog` | semantic | yes | yes | 3 | 0 | 0008, staged-units-graduation |
| `auth_flow_order` | semantic | yes | yes | 1 | 0 | 0029, 0043 |
| `cancel_not_settled` | semantic | yes | yes | 1 | 0 | — |
| `catalog_mismatch` | semantic | yes | yes | 7 | 0 | 0008, 0011, staged-units-graduation |
| `compaction_ended_without_start` | semantic | yes | yes | 1 | 0 | 0044 |
| `compaction_failed_without_error` | semantic | yes | yes | 1 | 0 | 0044 |
| `compaction_open_at_terminal` | semantic | yes | yes | 1 | 0 | 0044 |
| `compaction_run_mismatch` | semantic | yes | yes | 1 | 0 | 0044 |
| `compaction_stop_reason_mismatch` | semantic | yes | yes | 1 | 0 | 0044 |
| `compaction_unpaired` | semantic | yes | yes | 1 | 0 | 0044 |
| `content_after_completion` | semantic | yes | yes | 1 | 0 | 0049 |
| `credential_in_headers` | semantic | yes | yes | 0 | 3 | model-provider-core |
| `credential_in_trace` | semantic | yes | yes | 0 | 1 | provider-stdio |
| `degraded_without_optin` | semantic | yes | yes | 15 | 0 | 0005, 0006, 0007, 0008, 0009, 0028, 0040, 0044, 0045, 0046, 0051, staged-units-graduation |
| `duplicate_envelope_id` | semantic | yes | yes | 1 | 0 | — |
| `duplicate_interaction` | semantic | yes | yes | 4 | 0 | 0011, staged-units-graduation |
| `duplicate_key` | decode | yes | no | 1 | 1 | — |
| `duplicate_message_completion` | semantic | yes | yes | 1 | 0 | 0049 |
| `duplicate_model_id` | semantic | yes | yes | 1 | 0 | 0006, staged-units-graduation |
| `duplicate_provider` | semantic | yes | yes | 2 | 0 | 0014, 0028 |
| `duplicate_response` | semantic | yes | yes | 1 | 0 | — |
| `duplicate_run_terminal` | semantic | yes | yes | 4 | 0 | 0002, endpoint-stdio |
| `duplicate_session_entry` | semantic | yes | yes | 1 | 0 | 0046 |
| `duplicate_steer` | semantic | yes | yes | 2 | 0 | 0013 |
| `duplicate_tool_name` | semantic | yes | yes | 6 | 0 | 0005, 0008, 0011, staged-units-graduation |
| `duplicate_tool_source` | semantic | yes | yes | 7 | 0 | 0008, staged-units-graduation |
| `duplicate_transcript_entry` | semantic | yes | yes | 1 | 0 | 0051 |
| `event_after_terminal` | semantic | yes | yes | 2 | 1 | — |
| `ext_claim_without_pack` | load | yes (load) | yes (load) | 1 | 0 | 0004, staged-units-graduation |
| `illegal_run_transition` | semantic | yes | yes | 30 | 0 | 0002, 0007, 0013, 0041, 0044, conformance, staged-units-graduation |
| `illegal_tool_transition` | semantic | yes | yes | 6 | 0 | 0007, 0011, conformance, staged-units-graduation |
| `malformed_json` | decode | yes | no | 0 | 1 | — |
| `missing_auth_terminal` | semantic | yes | yes | 1 | 0 | 0029, 0043 |
| `missing_response` | semantic | yes | yes | 4 | 0 | 0007, staged-units-graduation |
| `missing_run_started` | semantic | yes | yes | 5 | 0 | 0002, 0044 |
| `missing_run_terminal` | semantic | yes | yes | 2 | 0 | 0002 |
| `model_not_in_catalog` | semantic | yes | yes | 11 | 0 | 0006, 0009, 0028, staged-units-graduation |
| `pack_branch_undeclared_type` | load | yes (load) | yes (load) | 1 | 0 | 0004, staged-units-graduation |
| `pack_branch_unpinned` | load | yes (load) | yes (load) | 1 | 0 | 0004, staged-units-graduation |
| `pack_dependency_missing` | load | yes (load) | yes (load) | 1 | 0 | 0004, staged-units-graduation |
| `pack_external_ref` | load | yes (load) | yes (load) | 2 | 0 | 0004, staged-units-graduation |
| `pack_fixture_claims_core_unit` | load | yes (load) | yes (load) | 1 | 0 | 0004, staged-units-graduation |
| `pack_foreign_prefix` | load | yes (load) | yes (load) | 1 | 0 | 0004, staged-units-graduation |
| `pack_id_collision` | load | yes (load) | yes (load) | 2 | 0 | 0004, staged-units-graduation |
| `pack_member_duplicate` | load | yes (load) | yes (load) | 1 | 0 | 0004, staged-units-graduation |
| `pack_member_target_unknown` | load | yes (load) | yes (load) | 2 | 0 | 0004, staged-units-graduation |
| `pack_refusal_undeclared` | load | yes (load) | yes (load) | 1 | 0 | 0004, staged-units-graduation |
| `pack_reply_target_ambiguous` | load | yes (load) | yes (load) | 1 | 0 | 0004, staged-units-graduation |
| `pack_reply_target_unknown` | load | yes (load) | yes (load) | 1 | 0 | 0004, staged-units-graduation |
| `pack_response_gated` | load | yes (load) | yes (load) | 1 | 0 | 0004, staged-units-graduation |
| `pack_restates_core_member` | load | yes (load) | yes (load) | 1 | 0 | 0004, staged-units-graduation |
| `pack_role_undeclared` | load | yes (load) | yes (load) | 1 | 0 | 0004, staged-units-graduation |
| `pack_schema_path_escape` | load | yes (load) | yes (load) | 1 | 0 | 0004, staged-units-graduation |
| `pack_type_duplicate` | load | yes (load) | yes (load) | 1 | 0 | 0004, staged-units-graduation |
| `pack_ungated_type` | load | yes (load) | yes (load) | 1 | 0 | 0004, staged-units-graduation |
| `pack_unprefixed_name` | load | yes (load) | yes (load) | 1 | 0 | 0004, staged-units-graduation |
| `payload_decode` | decode | yes | no | 0 | 0 | — |
| `pending_interaction_at_terminal` | semantic | yes | yes | 2 | 0 | 0011, staged-units-graduation |
| `pending_steer_at_terminal` | semantic | yes | yes | 2 | 0 | 0013 |
| `pending_tool_at_terminal` | semantic | yes | yes | 4 | 0 | 0005, 0011, 0031, staged-units-graduation |
| `premature_session_mutation` | semantic | yes | yes | 3 | 0 | 0007, staged-units-graduation |
| `queue_limit_exceeded` | semantic | yes | yes | 9 | 0 | 0007, 0009, staged-units-graduation |
| `queue_order_violation` | semantic | yes | yes | 4 | 0 | 0007, staged-units-graduation |
| `resolution_payload_mismatch` | semantic | yes | yes | 2 | 0 | 0011, staged-units-graduation |
| `schema_invalid` | schema | yes | yes (schema gate) | 44 | 16 | 0004, 0005, 0006, 0007, 0008, 0010, 0011, 0013, 0029, 0035, 0040, 0043, 0044, 0045, 0046, 0049, 0050, 0051, model-provider-core, staged-units-graduation |
| `scope_mismatch` | semantic | yes | partial | 15 | 0 | 0006, 0008, 0013, 0044, 0051, conformance, staged-units-graduation |
| `sequence_gap` | semantic | yes | yes | 5 | 1 | 0007, model-provider-core, staged-units-graduation |
| `sequence_regression` | semantic | yes | yes | 1 | 1 | model-provider-core |
| `session_list_order` | semantic | yes | yes | 2 | 0 | 0046 |
| `session_list_over_limit` | semantic | yes | yes | 1 | 0 | 0046 |
| `session_state_mismatch` | semantic | yes | partial | 105 | 0 | 0006, 0007, 0008, 0009, 0011, 0013, 0040, 0045, staged-units-graduation |
| `stale_capability_revision` | semantic | yes | yes | 8 | 0 | 0006, 0007, 0029, 0043, conformance, staged-units-graduation |
| `terminal_not_assembly` | semantic | yes | yes | 0 | 4 | model-provider-core |
| `transcript_gap_mismatch` | semantic | yes | yes | 2 | 0 | 0051 |
| `transcript_over_limit` | semantic | yes | yes | 1 | 0 | 0051 |
| `unannounced_catalog_change` | semantic | yes | yes | 5 | 0 | 0006, staged-units-graduation |
| `unapplied_control` | semantic | yes | yes | 21 | 0 | 0005, 0006, 0031, 0034, 0045, staged-units-graduation |
| `unattributed_call` | semantic | yes | yes | 1 | 0 | 0008, conformance |
| `unavailable_capability` | semantic | yes | yes | 44 | 0 | 0004, 0005, 0006, 0007, 0008, 0009, 0011, 0013, 0040, 0044, 0045, 0046, 0051, staged-units-graduation |
| `undeclared_replay_gap` | semantic | yes | yes | 1 | 0 | — |
| `undisclosed_attach_limit` | semantic | yes | yes | 1 | 0 | 0008, staged-units-graduation |
| `undisclosed_attach_modes` | semantic | yes | yes | 3 | 0 | 0008, conformance, staged-units-graduation |
| `undisclosed_provide_limit` | semantic | yes | yes | 1 | 0 | 0011, staged-units-graduation |
| `undisclosed_queue_limit` | semantic | yes | yes | 2 | 0 | 0007, staged-units-graduation |
| `undisclosed_selection_scope` | semantic | yes | yes | 3 | 0 | 0005, 0021, staged-units-graduation |
| `unhonoured_capability` | semantic | yes | yes | 4 | 0 | 0004, 0006, 0008, 0009, staged-units-graduation |
| `unknown_participant` | semantic | yes | yes | 1 | 0 | — |
| `unmatched_interaction` | semantic | yes | yes | 5 | 0 | 0007, 0011, staged-units-graduation |
| `unmatched_provider` | semantic | yes | yes | 1 | 0 | 0014 |
| `unmatched_response` | semantic | yes | yes | 1 | 0 | — |
| `unmatched_steer` | semantic | yes | yes | 3 | 0 | 0013 |
| `unmatched_tool` | semantic | yes | yes | 1 | 0 | — |
| `unmatched_tool_source` | semantic | yes | yes | 11 | 0 | 0008, 0011, 0048, conformance, staged-units-graduation |
| `unsatisfiable_control` | semantic | yes | yes | 22 | 0 | 0005, 0013, 0019, 0034, staged-units-graduation |
| `wrong_interaction_responder` | semantic | yes | yes | 3 | 0 | 0011, staged-units-graduation |
| `wrong_tool_owner` | semantic | yes | yes | 3 | 0 | 0011, staged-units-graduation |

## Rules by source

Abbreviations: **S** = enforced by the schema (both trees run the schema
gate); **—** = no diagnostic; **pinned elsewhere** names where a non-trace rule
is checked. Fixture counts are positive / negative traces citing the decision.

### Decisions 0001 and 0002: the executable core

| rule | code | Go / Zig | fixtures | note |
| --- | --- | --- | --- | --- |
| 0001 typed identity domains | S | both | core | |
| 0001 one foreground invocation is one run | `illegal_run_transition`, `missing_run_started`, `missing_run_terminal`, `duplicate_run_terminal` | both | core, many | |
| 0001 deterministic run event order | `sequence_gap`, `sequence_regression`, `event_after_terminal` | both | core | |
| 0001 correlated reverse interactions | `unmatched_interaction`, `duplicate_interaction`, `wrong_interaction_responder`, `pending_interaction_at_terminal` | both | core | the responder rule checks the declared responder; `unknown_participant` (declared at initialize) is judged by both |
| 0001 cancellation intent is not settlement | `cancel_not_settled`, `illegal_run_transition` | both | 1 negative | one negative fixture only |
| 0001 resume, reconciliation and replay are separate | `undeclared_replay_gap` | both | 1 negative | |
| 0001 capabilities disclose effective fidelity | `unavailable_capability`, `unhonoured_capability`, `stale_capability_revision` | both | 44 / 4 / 8 negatives | |
| request/response pairing (0001 vocabulary) | `missing_response`, `duplicate_response`, `unmatched_response` | both | 4 / 1 / 1 | |
| 0002 two canonical admission shapes, promotion, pre-start settlement | `illegal_run_transition`, `missing_run_started` | both | 4 / 7 | |

### Decisions 0003, 0004: process and extension packs

| rule | code | Go / Zig | fixtures | note |
| --- | --- | --- | --- | --- |
| 0003 the graduation gate (all rules) | — | — | — | process; pinned by review and by `goap check`'s unit inventory |
| 0004 unprefixed namespace, prefix-free set, a pack declares what it defines, a pack adds and does not amend, a pack is someone else's document | 19 `pack_*` load codes and `ext_claim_without_pack` | both, all 20; per-code status in [`drafts/validator-rules/0004-packs.md`](../drafts/validator-rules/0004-packs.md) | 6 / 23 load-invalid | the Zig fixture gate judges all 23 `load-invalid` fixtures; oapx does not yet apply `pack.schema.json` ([record](../drafts/extension-pack-load-refusals.md#the-descriptor-schema-is-not-applied)) |
| 0004 a load refusal is not a diagnostic | (by construction) | both | — | |
| 0004 a pack's conformance claim is its own | `pack_fixture_claims_core_unit` | both | 1 | |
| 0004 pack members gated on their capability | `unavailable_capability`, `unhonoured_capability` | both (Zig's `packEnvelope` raises `unhonoured_capability` for a refusal outside a typed gate's `refusals`) | 4 / 1 | |

### Decisions 0005, 0019, 0021–0024, 0031, 0034: run controls

| rule | code | Go / Zig | fixtures | note |
| --- | --- | --- | --- | --- |
| 0005 presence is what the gate judges; controls fail closed | `unavailable_capability`, `degraded_without_optin`, `unsatisfiable_control` | both | 17 / 31 | |
| 0005 the gate is judged on the correlated response | `unapplied_control` | both | 18 | |
| 0005 disclosure is machine-readable | `undisclosed_selection_scope` | both | 3 | |
| 0005 a per-run selection does not move the session default | `session_state_mismatch` | Zig partial | in queue/models sets | |
| 0005 `output_schema` is compiled, never resolved | `unsatisfiable_control` | both | | `outputschema.go` emits no code of its own |
| 0005 `run.completed` gains `model_id` | `unapplied_control` (model mismatch) | both | | |
| 0019 `unsatisfiable_control` reaches unported rules | `unsatisfiable_control` | both | 1 | 0019's other rules are process (one binary, port order) |
| 0021 a tool policy discloses whether it outlives its run (proposed) | `undisclosed_selection_scope` | both | 1 | |
| 0022 `tool_choice` is a filter; `none` is an empty allowlist; `mode` is `scope` | `unapplied_control`, `unsatisfiable_control`, S | both | 1 / — | positive only |
| 0023 only `allowed` is checked against the catalog | `unsatisfiable_control` | both | 1 / — | positive only |
| 0024 the filter judges the session catalog | `unsatisfiable_control` | both | 2 / — | positive only |
| 0031 a policy refusal settles `failed` with `refused_by_policy` | `unapplied_control`, `pending_tool_at_terminal` | both | 1 / 6 | judged only when the trace carries `session.open.request`; see [gap 1](#gaps-ranked-by-risk) |
| 0034 publishing `tools` makes the catalog known | `unapplied_control`, `unsatisfiable_control` | both | 2 / 3 | |

### Decisions 0006, 0014, 0028, 0035: models and providers

| rule | code | Go / Zig | fixtures | note |
| --- | --- | --- | --- | --- |
| 0006 the catalog binds selection in both directions | `model_not_in_catalog` | both | 16 / 9 | |
| 0006 stands down where selection is not advertised | `unavailable_capability` | both | 4 | |
| 0006 a catalog belongs to its revision; served with its revision; revision schema-required | `stale_capability_revision`, `unannounced_catalog_change`, S | both | 4 / 5 | |
| 0006 `current_model_id` judged across the window | `session_state_mismatch` | Zig partial | 2 | |
| 0006 per-query opt-in | `degraded_without_optin` | both | 1 | |
| 0006 open response sent whole; empty catalog is a list; descriptor `id` non-empty | S, `session_state_mismatch` | both (S) | 2 schema | |
| 0006 one default, unique ids | `ambiguous_default_model`, `duplicate_model_id` | both | 1 / 1 | |
| 0006 `adapter.ModelLister` is optional | — | — | — | Go API shape; pinned by adapter tests |
| 0014 a published `provider_id` resolves; providers unique | `unmatched_provider`, `duplicate_provider` | both | 1 / 2 | |
| 0028 `session.model.switch` (core) | `model_not_in_catalog`, `session_state_mismatch`, `degraded_without_optin`, `scope_mismatch` | Zig partial on two | 4 / 3 | proposed |
| 0028 `session.provider.attach` (optional) | `duplicate_provider`, `undisclosed_attach_modes`, `unavailable_capability` | both | | proposed; no adapter attaches live |
| 0035 a model entry publishes facts; absence means unknown | S | both | 2 / 3 schema | not a semantic rule |

### Decisions 0007, 0009, 0041: queue, compound open, cancel

| rule | code | Go / Zig | fixtures | note |
| --- | --- | --- | --- | --- |
| 0007 one started run, reservations behind it; admission order | `queue_order_violation`, `illegal_run_transition` | both | 70 / 10 | |
| 0007 the bound is disclosed and kept | `undisclosed_queue_limit`, `queue_limit_exceeded` | both | 2 / 9 | |
| 0007 the refusal is validated as well as the admission | `unavailable_capability`, `degraded_without_optin`, `missing_response` | both | 4 / 2 | |
| 0007 a snapshot states its position; a settled run is named | `session_state_mismatch`, `premature_session_mutation` | Zig partial on `session_state_mismatch`; `premature_session_mutation` both | 82 / 3 | the largest partial gap |
| 0007 a reservation's model control applies at promotion | `unapplied_control` | both | | |
| 0009 open carries subscription and message; ack unconditional; one refusal per election; cites active revision | `session_state_mismatch`, `unavailable_capability`, `degraded_without_optin`, `stale_capability_revision` | Zig partial | 11 / 7 | |
| 0009 failure is atomic | `session_state_mismatch` | Zig partial | | |
| 0009 a transport that carries no stream holds the subscription | — | — | — | hub behaviour; pinned by hub tests (`drafts/hub.md`) |
| 0041 cancel acceptance judged when the cancel is checked; late cancel legal with a typed answer | `illegal_run_transition`, `cancel_not_settled` | both | 2 / 1 | |

### Decisions 0008, 0011, 0048: tool sources and provided tools

| rule | code | Go / Zig | fixtures | note |
| --- | --- | --- | --- | --- |
| 0008 an adapter that does not attach says so; catalog gated on its own key | `unavailable_capability`, `unhonoured_capability` | both | 19 / 12 | |
| 0008 a source is described; attachment shape is not the catalog shape | `attachment_field_in_catalog`, S | both | 3 | |
| 0008 a catalog resolves | `unmatched_tool_source`, `catalog_mismatch`, `duplicate_tool_source`, `duplicate_tool_name` | both | 6 / 4 / 7 / 2 | |
| 0008 attachment bounded by disclosure; modes disclosed as a set | `undisclosed_attach_limit`, `undisclosed_attach_modes` | both | 1 / 3 | |
| 0008 a call is attributed to its source | `unattributed_call` | both | 1 | |
| 0008 attachment lasts the session; the daemon takes no command from the wire | S | both | | hub refusal pinned by hub tests |
| 0011 provided at open, provisioned whole | `catalog_mismatch`, `duplicate_tool_name` | both | 25 / 3 | |
| 0011 ownership checked where stated | `wrong_tool_owner` | both | 3 | needs the open in the trace |
| 0011 a call is an interaction; resolution pair with three arms | `unmatched_interaction`, `duplicate_interaction`, `wrong_interaction_responder`, `resolution_payload_mismatch` | both | 4 / 4 / 2 / 2 | needs the open in the trace |
| 0011 ranked refusal reasons; the endpoint reports the highest | `unmatched_interaction`, `duplicate_interaction`, `wrong_interaction_responder` on a reason other than the highest | both | | needs the open in the trace |
| 0011 terminal derives from an accepted resolution | `illegal_tool_transition`, `pending_tool_at_terminal` | both | 4 / 1 | needs the open in the trace |
| 0011 the capability discloses its limits | `undisclosed_provide_limit` | both | 1 | |
| 0011 recovery reads two lists; recovered call judged on what remains | `session_state_mismatch` | Zig partial | 4 | |
| 0048 a provided tool names a source when listed | `unmatched_tool_source` | both | 2 / 2 | |

### Decisions 0010, 0012, 0013, 0026, 0029, 0043: amendments and auth

| rule | code | Go / Zig | fixtures | note |
| --- | --- | --- | --- | --- |
| 0010 optional `settled_by` on terminals; an inferred terminal is a terminal | S, `duplicate_run_terminal` | both | 1 / 1 schema | |
| 0010 omission asserts observation; what makes a terminal inferred | — | — | — | not decidable from a trace |
| 0012 persistence retired; transcript staged | — | — | — | superseded by 0046 and 0051 rows |
| 0013 steer shape; settled not merely admitted; attributable by `request_id` | `unmatched_steer`, `duplicate_steer`, `pending_steer_at_terminal`, `illegal_run_transition`, `scope_mismatch` | both (scope partial) | 18 / 25 | |
| 0013 `invalid_steer_target` reason ranking | — | — | — | `not_steerable` is read; `no_model_turn` (0044) is in neither validator |
| 0026 a resume cursor may name its run | — | — | — | hub wire; pinned by hub tests |
| 0029 auth flow order and terminal | `auth_flow_order`, `missing_auth_terminal`, `stale_capability_revision` | both | 2 / 3 | |
| 0029 credential boundary | `credential_in_trace` (provider profile) | both | | the agent profile has no credential diagnostic |
| 0043 provider rows carry `auth_kinds` | S | both | 2 / 3 schema | |

### Decisions 0040, 0044, 0045, 0046, 0049, 0050, 0051: session lifecycle

| rule | code | Go / Zig | fixtures | note |
| --- | --- | --- | --- | --- |
| 0040 reopen gated like `subscribe` | `unavailable_capability`, `degraded_without_optin` | both | 7 / 3 | |
| 0040 the reply is the state document, `recovered: true` | `session_state_mismatch` | Zig partial | 2 | |
| 0040 three refusals fail closed; store gone answers `unsupported_feature` | gate refusal only (`unavailable_capability`) | both | | the binding and store refusals are not judged; pinned by hub tests and the conformance runner's fail-closed check |
| 0044 two features, gated | `unavailable_capability`, `degraded_without_optin` | both | 5 / 5 | |
| 0044 a requested compaction is a run, opens with one `requested` compaction | `compaction_run_mismatch`, `missing_run_started` | both | 1 / 1 | |
| 0044 the barrier | `compaction_unpaired`, `compaction_ended_without_start`, `compaction_open_at_terminal` | both | 1 each | one negative fixture per code |
| 0044 settlement: `compacted` stop reason, failed carries error | `compaction_stop_reason_mismatch`, `compaction_failed_without_error` | both | 1 each | |
| 0044 delivery: auto or queue only | `illegal_run_transition` | both | 7 | |
| 0045 two settings, values, where they go, what the state reports | `unapplied_control`, `session_state_mismatch`, `scope_mismatch`, S | Zig partial | 11 / 11, 5 schema | |
| 0046 list gated, newest first, bounded, one entry per session | `session_list_order`, `session_list_over_limit`, `duplicate_session_entry`, `unavailable_capability` | both | 4 / 7 | |
| 0049 a message reports its own usage, once | `duplicate_message_completion`, `content_after_completion` | both | 1 / 2 | |
| 0050 shutdown pair, `choose` lists ≥ 1 run | S | both | 2 / 3 schema | |
| 0050 `shutting_down` after acceptance; interrupted run settles `failed` `inferred`; `recovery.interrupted_runs`; held prompts redelivered in order | — | — | — | **no semantic rule**; see [gap 2](#gaps-ranked-by-risk) |
| 0051 pair gated; page within `limit`; one entry once; gap only after a cursor, resuming at the first entry; session scope | `unavailable_capability`, `degraded_without_optin`, `transcript_over_limit`, `duplicate_transcript_entry`, `transcript_gap_mismatch`, `scope_mismatch` | both | 4 / 7, 4 schema | |
| 0051 512 KiB page, oversize entry alone and `truncated` | — | — | — | the byte bound is not decidable from a page alone; a `truncated` entry not alone is decidable and unchecked |
| 0051 `transcript_cursor` is a cursor the read accepts where advertised | — | — | — | not decidable from a trace |

### Process and policy decisions with no trace rule

0015, 0016 (proposed), 0017 (superseded), 0018, 0020 (proposed: declared
error codes, no diagnostic yet), 0025 (superseded), 0027 (proposed), 0030
(proposed), 0032, 0033 (harness pins: pinned by `goap check`), 0036, 0037
(proposed), 0038, 0039 (direction; its wire is 0040, 0046 and 0051), 0047
(work layer: the work wire is the hub's, pinned by hub and workwire tests),
0052 (proposed).

### Drafts, schema and the feedback ledger

| source | coverage |
| --- | --- |
| `drafts/agent-control-core.md`, `agent-control-profile.md`, `conformance.md` | restate the decisions above; `conformance.md`'s per-unit bullets map onto the rows above |
| `drafts/hub.md` | the hub's own wire (ops, routes, HTTP statuses); out of the trace validator's scope by design, and every rule names its pinning Go and Zig test. Hub answers that carry a protocol payload (`history`, `transcript`) are pinned by field assertions; no test validates them against the schema |
| `drafts/work.md` | work verbs; no validator; pinned by `go/internal/workwire` tests, Zig hub tests and `hub_parity_test` |
| `drafts/endpoint-stdio.md` | framing and the serve-agent loop; pinned by the Go conformance runner (`goap conformance`), not by diagnostics |
| `drafts/model-provider-core.md`, `provider-stdio.md`, `provider-http.md` | provider validator: `credential_in_trace`, `credential_in_headers`, `terminal_not_assembly`, `sequence_gap`, `sequence_regression`, `event_after_terminal` in both trees; the provider corpus holds 9 positive, 11 semantic-invalid, 16 schema-invalid and 2 decode fixtures |
| `drafts/triggered-runs.md`, `harness-initiated-state.md`, `presentation-control-profile.md`, `composition.md`, `cli.md` | proposals or unbuilt; no wire on main to check |
| `schema/v0.1` | both trees run the same schemas; [the manifest](../fixtures/manifest.json) lists the current agent and provider `schema-invalid` fixtures, including Decision 0050 redelivery record shapes |
| `research/protocol-feedback-2026-09.md` | PF-1 → 0002 rows; PF-6 → capability rows; PF-7 → 0001 rows; PF-10 → `unattributed_call`; PF-2, 3, 4, 5, 8, 9, 11, 12 are deferrals or non-changes with no rule to check |

## Emitting surfaces

Which code paths that produce envelopes have their output run through a
validator, and how much of it.

| surface | validated by | what it misses |
| --- | --- | --- |
| Go adapters (`go/adapter/*`) | `adaptertest.AssertProtocolValid*` in 21 test files, and the corpus tests | the trace it builds is `capabilities` + optional `action.tools.list` + the submit + the run's events. It carries no `protocol.initialize` and no `session.open.request`, so every rule keyed on the control participant (0011 ownership and interactions, 0031 settlement) and every participant rule is skipped. A test that wants them builds its own trace, as `go/adapter/claude/provided_test.go` does |
| Zig adapters (`zig/src/adapter/*`) | `codex` and `opencode` corpora run `semantic` and `jsonschema` on what they emit; `acp`, `claude`, `deepseek`, `hermes` and `pi` go through the shared harness, which requires each case's emitted envelopes to equal its expectation exactly (`equalValues`) | validity for those five is transitive: the Go corpus validates every run of the same cases and holds Go's output to the same expectation, so a Zig-only defect shows as a mismatch. What no validator sees is a Zig path no corpus case reaches |
| `oapx` (in-process loop) | `zig/src/adapter/oapx` tests validate selected traces with `semantic` | no corpus; coverage is what the tests chose |
| hubs (`serve`, `oapx hub`) | not validated, by design (`serve` adds no semantics) | hub op answers carrying protocol payloads are not schema-checked |
| `serve agent` (both trees) | the Go conformance runner over raw envelopes | runs on demand and in CI for Go; the Zig `serve agent` is exercised by the parity job |
| provider profile | `ProviderValidator` / `provider_semantic` over `fixtures/provider/` | six semantic codes; provider adapters' live output is checked only where a test records it |
| real-process gates | the shared validator, when a gate runs | never in CI (`OAP_<HARNESS>_SMOKE`), so a harness drift reaches a validator only through a re-recorded corpus |

## Fixtures per unit

| unit | positive | schema-invalid | semantic-invalid | load-invalid | decode |
| --- | --- | --- | --- | --- | --- |
| auth | 2 | 3 | 3 | 0 | 0 |
| capabilities | 1 | 1 | 0 | 0 | 0 |
| carry | 2 | 1 | 1 | 0 | 0 |
| compaction | 5 | 1 | 18 | 0 | 0 |
| compound-open | 10 | 0 | 8 | 0 | 0 |
| control-tools | 26 | 3 | 37 | 0 | 0 |
| core | 148 | 20 | 248 | 0 | 1 |
| credentials | 1 | 1 | 4 | 0 | 0 |
| extensions | 6 | 4 | 5 | 23 | 0 |
| models | 21 | 2 | 36 | 0 | 0 |
| permissions | 1 | 0 | 0 | 0 | 0 |
| provider-core | 8 | 14 | 6 | 0 | 2 |
| queue | 70 | 1 | 107 | 0 | 0 |
| recovery | 6 | 0 | 3 | 0 | 0 |
| run-controls | 20 | 1 | 50 | 0 | 0 |
| session-list | 4 | 3 | 7 | 0 | 0 |
| session-reattach | 8 | 2 | 5 | 0 | 0 |
| session-settings | 7 | 4 | 6 | 0 | 0 |
| session-settings-live | 4 | 1 | 7 | 0 | 0 |
| steer | 18 | 1 | 30 | 0 | 0 |
| tool-sources | 24 | 3 | 54 | 0 | 0 |
| tools | 1 | 0 | 4 | 0 | 0 |
| transcript-load | 4 | 4 | 7 | 0 | 0 |
| user-input | 1 | 0 | 3 | 0 | 0 |

## Go-only codes being ported

Listed for completeness and left to the port already under way: the
decode-phase codes (`malformed_json`, `duplicate_key`, `payload_decode`), and
the partial `scope_mismatch` and `session_state_mismatch`. Every load code is
now judged by both trees.

## Gaps ranked by risk

Ranked by how easily an adapter or endpoint could break the rule today with
no check noticing. Each names the rule, why nothing catches it, and the
smallest fix.

1. **Control-owned call rules are skipped in every Go adapter unit test.**
   0011's ownership, interaction and settlement rules and 0031's
   `refused_by_policy` settlement are keyed on the control participant, which
   only `protocol.initialize.request`'s participant sets, and on the provided
   tools, which only `session.open.request` declares; `adaptertest`'s trace has
   neither (above). A provided-tool adapter that settles a call wrongly passes
   its own tests; #1202 found exactly this only because its test built a full
   trace by hand. Fix: give `adaptertest` an initialize carrying the
   participant and an open carrying the provided tools.
2. **Decision 0050 has no semantic rule while both trees are building it.**
   Only the schema judges shutdown: nothing checks that a submit, open or
   `work.start` after an accepted shutdown is refused `shutting_down`, that an
   interrupted run settles `failed` with `settled_by: "inferred"`, that
   `recovery.interrupted_runs` names those runs, or that held prompts return in
   their admission order. The fixtures are 2 positive and 3 schema-invalid.
3. **Five Zig adapter corpora validate only through Go (demoted on review).**
   `acp`, `claude`, `deepseek`, `hermes` and `pi` hold their output to each
   case's expectation by exact equality, and the Go corpus validates every run
   of those cases, so the corpus output is validated transitively; the first
   draft of this ledger said otherwise. The residual risk is a Zig code path no
   corpus case reaches, and a Zig validity claim that rests on Go, which
   Decision 0032 says neither tree should. Low risk.
4. **No adapter output reaches a validator from a real harness in CI.** Every
   live check is behind an opt-in gate, so upstream drift between pins is
   caught only when someone re-records a corpus. This is by design (Decision
   0033 watches drift rather than following it); it is listed because it
   bounds every row above.
5. **`requested_by` is checked as declared, not as the endpoint.** CLAUDE.md's
   invariant says the agent participant an adapter names as `requested_by`
   must be the endpoint id its initialize response declares; `unknown_participant`
   checks only that the id was declared at all, so naming the
   control layer as the requester passes.
6. **Fixture-thin units.** `permissions` has 1 positive and no semantic-invalid
   fixture, `capabilities` 1 and none, `user-input` 1 and 3, `tools` 1 and 4.
   The rules are enforced by core codes, but a regression in the
   permission-specific path has one trace standing between it and a release.
7. **Single-fixture codes.** 29 codes outside the pack loader have exactly one
   negative fixture, among them all six compaction codes, `cancel_not_settled`,
   `unmatched_tool`, `duplicate_envelope_id`, `content_after_completion`,
   `unattributed_call` and both 0051 page codes.
   One fixture proves the code fires once; it does not pin its boundary.
8. **0051's decidable page rule is unchecked.** A `truncated` entry must come
   alone on its page; both validators accept a `truncated` entry beside others.
   The byte bound itself is not decidable from the page.
9. **0013/0044 steer refusal reasons.** `invalid_steer_target`'s ranked
   reasons are read for `not_steerable` only; `no_model_turn`, which 0044 adds
   for a compaction run without `continue`, is in neither validator.
10. **0040's binding and store refusals.** Only the gate refusal is judged; a
    reopen answered with the wrong refusal code when the binding is missing or
    the harness cannot load the session is caught by hub tests and the Go
    conformance runner, not by a diagnostic.
11. **Zig pack loading refuses eighteen ways of twenty.** Its fixture gate runs
    the `load-invalid` fixtures since #1261, exempting the two codes it does
    not yet refuse, so a pack Go refuses for one of those can still load in
    `oapx`.
12. **Hub answers with protocol payloads are not schema-checked.** `history`
    answers `session.list.response`'s payload and `transcript` answers
    `transcript.load.response`'s; the hub tests assert fields, so a member the
    schema refuses would pass.
13. **`payload_decode` has no fixture**, and 0010's `settled_by` has no
    semantic rule (most of it is not decidable from a trace; listed so the
    absence is a choice on record).

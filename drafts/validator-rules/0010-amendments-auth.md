# Amendments and auth

Decisions 0010, 0012, 0013, 0026, 0029 and 0043: the rows behind every
diagnostic the validators raise for terminal provenance, steering and the
standalone login flow. Go, Zig and Rust name the function in `go/validation/`,
`zig/src/validation/` and `rust/validation/src/state/`. Rust judges terminals,
a steer admitted as a new run and a steer carrying run controls, and has no
other steer or auth check yet. Format:
[README](README.md).

Decision 0013 adopts section T4 of
[`drafts/staged-units-graduation.md`](../../drafts/staged-units-graduation.md#t4-steer)
as normative (0013 L45–48), so most steer rows cite T4's lines as well as the
decision's. T4's `settled_steers` surface is not normative: 0013 defers it to a
decision of its own (L105–110), and neither validator judges it.

No 0012 or 0026 row exists. 0012 retires persistence, and the transcript surface
it staged is ruled by Decision 0051. 0026 is hub wire behaviour, pinned by the
hub tests and [`drafts/hub.md`](../hub.md).

### [0010] An optional member on the three terminals

Source: [`decisions/0010-terminal-provenance.md`](../../decisions/0010-terminal-provenance.md#an-optional-member-on-the-three-terminals).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0010#an-optional-member-on-the-three-terminals/1` | `settled_by` on `run.completed`, `run.failed` and `run.cancelled`, when present, MUST be `observed` or `inferred`. | L48–58 | `schema_invalid` | schema | schema | schema |

### [0010] An inferred terminal is a terminal

Source: [`decisions/0010-terminal-provenance.md`](../../decisions/0010-terminal-provenance.md#an-inferred-terminal-is-a-terminal).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0010#an-inferred-terminal-is-a-terminal/1` | A terminal carrying `settled_by: "inferred"` ends the run exactly as an observed one does: a second terminal is a duplicate, and any later run-scoped event follows a terminal. | L80–84 | `duplicate_run_terminal`, `event_after_terminal` | `state.go:runEvent` | `semantic.zig:runEvent` | `runs.rs:run_event` |

0010's "No cross-check against the capability descriptor" (L136–140) is a rule
about what the validators must not do: neither judges `settled_by` beyond its
enum.

### [0013] The shape is the one the plan pre-designed

Source: [`decisions/0013-steer.md`](../../decisions/0013-steer.md#the-shape-is-the-one-the-plan-pre-designed), adopting [T4](../../drafts/staged-units-graduation.md#t4-steer).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0013#the-shape-is-the-one-the-plan-pre-designed/1` | A `steered` admission MUST answer a request with `delivery: "steer"` and carry `effective_delivery: "steer"`; `auto` never resolves to `steer`. | L57–59, L70; T4 L4554–4560 | `illegal_run_transition` | `steer.go:steerAdmission` | `semantic.zig:steerAdmission` | `state/steer.rs:steer_admission` |
| `0013#the-shape-is-the-one-the-plan-pre-designed/2` | A request with `delivery: "steer"` MUST NOT be admitted as a new run. | L57–60; T4 L4554–4555 | `illegal_run_transition` | `state.go:admitSubmission` | `semantic.zig:admit` | `runs.rs:admit_submission` |
| `0013#the-shape-is-the-one-the-plan-pre-designed/3` | A steer MUST NOT be admitted unless `session.message.delivery.steer` is advertised, with the degraded opt-in where it is `degraded`. | T4 L4556–4557 | `unavailable_capability`, `degraded_without_optin` | `queue.go:deliveryExpectations` | `semantic.zig:deliveryExpectation` | — |
| `0013#the-shape-is-the-one-the-plan-pre-designed/4` | A `steered` admission MUST target a started, nonterminal run of the request's session that is not `cancelling`. | L57–61; T4 L4554–4556 | `illegal_run_transition` | `steer.go:steerAdmission` | `semantic.zig:steerAdmission` | `state/steer.rs:steer_admission` |
| `0013#the-shape-is-the-one-the-plan-pre-designed/5` | A `steered` admission's `run_id` MUST equal the request's `target_run_id` when one was supplied. | L58–59; T4 L4560–4561 | `scope_mismatch` | `steer.go:steerAdmission` | `semantic.zig:steerAdmission` | `state/steer.rs:steer_admission` |
| `0013#the-shape-is-the-one-the-plan-pre-designed/6` | A `steered` admission's `target_sequence` MUST equal the target run's last emitted sequence at the response. | L59–60; T4 L4562–4566 | `illegal_run_transition` | `steer.go:steerAdmission` | `semantic.zig:steerAdmission` | `state/steer.rs:steer_admission` |
| `0013#the-shape-is-the-one-the-plan-pre-designed/7` | A `steered` admission's `status` MUST equal the status the target held after that sequence. | T4 L4563–4567 | `illegal_run_transition` | `steer.go:steerAdmission` | `semantic.zig:steerAdmission` | `state/steer.rs:steer_admission` |
| `0013#the-shape-is-the-one-the-plan-pre-designed/8` | A steer carrying run controls (`model_id`, `instructions`, `tool_choice`, `output_schema`) MUST be refused with `unsupported_feature`, `details.reason: "unsatisfiable"`, naming the control, and MUST NOT be admitted. | L60–63; T4 L4394–4416 | `unsatisfiable_control` | `controls.go:submitControls` | `semantic.zig:submitControls` | `controls.rs:submit_controls` |
| `0013#the-shape-is-the-one-the-plan-pre-designed/9` | A `steered` admission MUST NOT reuse a `submission_id` already pending on its target run. | T4 L4585–4589 | `duplicate_steer` | `steer.go:steerAdmission` | `semantic.zig:steerAdmission` | `state/steer.rs:steer_admission` |
| `0013#the-shape-is-the-one-the-plan-pre-designed/10` | Every admitted steer MUST settle before its run's terminal. | L67–69; T4 L4596–4597 | `pending_steer_at_terminal` | `steer.go:steerTerminal` | `semantic.zig:runEvent` | — |
| `0013#the-shape-is-the-one-the-plan-pre-designed/11` | A steer whose target cannot take guidance MUST be refused with `invalid_steer_target`, judged against the target's state at the response. | T4 L4600–4613, L4679–4685 | `illegal_run_transition` | `steer.go:steerRefusal` | `semantic.zig:steerRefusal` | — |
| `0013#the-shape-is-the-one-the-plan-pre-designed/12` | That refusal's `details.reason` MUST be the highest-ranked condition the target meets: with a `target_run_id` naming a known run, `cross_session`, `terminal`, `not_steerable`, `queued`; otherwise `cross_session`, `unknown_target`, `no_active_run`, `terminal`, `not_steerable`, `queued`. | T4 L4612–4660 | `illegal_run_transition` | `steer.go:steerTarget`, `steer.go:steerRefusal` | `semantic.zig:steerTarget`, `semantic.zig:steerRefusal` | — |
| `0013#the-shape-is-the-one-the-plan-pre-designed/13` | Where the steer delivery is also unadvertised, the delivery gate's refusal answers the request and the target rows above raise nothing. | T4 L4664–4678 | `unavailable_capability` (the delivery gate; this rule adds none) | `steer.go:steerRefusal`, `steer.go:steerAdmission` | `semantic.zig:steerRefusal`, `semantic.zig:steerAdmission` | — |
| `0013#the-shape-is-the-one-the-plan-pre-designed/14` | Each `active_runs[]` entry's `pending_steers` in a `session.state` snapshot MUST equal the steers pending on that run at the snapshot's capture position, submission and request id alike. | T4 L4735–4740 | `session_state_mismatch` | `steer.go:closeSteerSnapshots` | `semantic.zig:closeSteerSnapshots` | — |

The `invalid_steer_target` reason `no_model_turn` both trees also rank (after
`not_steerable`) is Decision 0044's, and is registered with that group.

### [0013] Steer is settled, not merely admitted

Source: [`decisions/0013-steer.md`](../../decisions/0013-steer.md#steer-is-settled-not-merely-admitted).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0013#steer-is-settled-not-merely-admitted/1` | `run.steer.applied` and `run.steer.dropped` MUST cite a current descriptor advertising `session.message.delivery.steer`. | L64–66 | `unavailable_capability`, `stale_capability_revision` | `steer.go:steerSettlement` | `semantic.zig:steerSettlement` | `state/steer.rs:steer_settlement` |
| `0013#steer-is-settled-not-merely-admitted/2` | A settlement MUST name a steer an earlier `steered` admission on that run admitted. | T4 L4583–4585, L4597–4600 | `unmatched_steer` | `steer.go:steerSettlement` | `semantic.zig:steerSettlement` | `state/steer.rs:steer_settlement` |
| `0013#steer-is-settled-not-merely-admitted/3` | A steer MUST settle at most once. | T4 L4583–4585 | `duplicate_steer` | `steer.go:steerSettlement` | `semantic.zig:steerSettlement` | `state/steer.rs:steer_settlement` |
| `0013#steer-is-settled-not-merely-admitted/4` | An `applied` settlement MUST report exactly the `message_ids` the admission returned, when it returned them. | T4 L4589–4594 | `unmatched_steer` | `steer.go:steerSettlement` | `semantic.zig:steerSettlement` | `state/steer.rs:steer_settlement` |
| `0013#steer-is-settled-not-merely-admitted/5` | A settlement's `boundary` MUST be `immediate`, `turn`, `tool_result` or `unknown`. | L86–88 | `schema_invalid` | schema | schema | schema |

### [0013] A steer is attributable to the caller that sent it

Source: [`decisions/0013-steer.md`](../../decisions/0013-steer.md#a-steer-is-attributable-to-the-caller-that-sent-it).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0013#a-steer-is-attributable-to-the-caller-that-sent-it/1` | A settlement MUST carry `request_id`. | L94–96 | `schema_invalid` | schema | schema | schema |
| `0013#a-steer-is-attributable-to-the-caller-that-sent-it/2` | A settlement's `request_id` MUST equal the envelope id of the request its admission answered. | L94–96; T4 L4592–4595 | `unmatched_steer` | `steer.go:steerSettlement` | `semantic.zig:steerSettlement` | `state/steer.rs:steer_settlement` |

### [0029] Decision

Source: [`decisions/0029-authentication-over-agent-control.md`](../../decisions/0029-authentication-over-agent-control.md#decision).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0029#decision/1` | `auth.login.start.response` MUST carry a `flow_id` no earlier flow in the trace used. | L37 | `auth_flow_order` | `authstate.go:authStartResponse` | `semantic.zig:authEnvelope` | — |
| `0029#decision/2` | The start response MUST precede every event, cancel and terminal of its flow. | L48 | `auth_flow_order` | `authstate.go:authFlowFor` | `semantic.zig:authEnvelope` | — |
| `0029#decision/3` | A flow's events and terminal MUST carry a positive, contiguous `sequence` for that flow. | L48–50 | `sequence_gap`, `sequence_regression` | `authstate.go:authSequence` | `semantic.zig:authEnvelope` | — |
| `0029#decision/4` | Nothing for a flow follows its terminal. | L50–51 | `event_after_terminal` | `authstate.go:authFlowFor` | `semantic.zig:authEnvelope` | — |
| `0029#decision/5` | An accepted flow MUST end with exactly one `auth.login.completed`. | L40 | `missing_auth_terminal` | `authstate.go:closeAuthFlows` | `semantic.zig:close` | — |
| `0029#decision/6` | A flow's events and terminal MUST name the provider its start request named. | drafted here, from L35–41 (a flow is started for one `provider_id`); proposed | `scope_mismatch` | `authstate.go:authEvent`, `authstate.go:authCompleted` | `semantic.zig:authEnvelope` | — |
| `0029#decision/7` | `auth.login.cancel.response` MUST name the flow its request named. | drafted here, from L39 (the response acknowledges that cancellation); proposed | `scope_mismatch` | `authstate.go:authCancelResponse` | `semantic.zig:authEnvelope` | — |
| `0029#decision/8` | A request for `auth.providers` or `auth.login` on an endpoint that does not advertise the key MUST be refused with `unsupported_feature`. | L42–44 | `unavailable_capability` | — | — | — |

The successful auth response that fails to repeat its request's
`capability_revision` (fixture `auth-revision-not-repeated`) is the core
revision rule, not a 0029 one.

### [0029] Credential boundary

Source: [`decisions/0029-authentication-over-agent-control.md`](../../decisions/0029-authentication-over-agent-control.md#credential-boundary).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0029#credential-boundary/1` | No auth envelope carries a manual answer: `auth.login.reply.request` does not exist, and no payload admits an `answer`. | L80–83 | `schema_invalid` | schema | schema | schema |

### [0043] Decision

Source: [`decisions/0043-auth-providers-carry-how-they-accept-a-credential.md`](../../decisions/0043-auth-providers-carry-how-they-accept-a-credential.md#decision).

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0043#decision/1` | Every `auth.providers.response` row MUST carry `auth_kinds`, a non-empty list drawn from `api_key`, `oauth` and `none`. | L40–46 | `schema_invalid` | schema | schema | schema |

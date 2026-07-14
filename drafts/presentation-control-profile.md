# Open Agent Protocol Presentation Control Profile

Status: draft
Profile ID: `open-agent-protocol.presentation-control`
Base protocol: `open-agent-protocol` version `0.1`
License: CC0-1.0 public domain dedication, or the nearest legally valid equivalent in jurisdictions that do not recognize public domain dedication.
Scope: the shared protocol boundary between a presentation layer and a
control/orchestration layer.

This profile describes what a GUI, TUI, editor pane, web app, or dashboard can
expect from a control layer. It is the human-interface boundary above
[Agent Control Core](agent-control-core.md). It carries renderable state upward
and user intent downward.

The presentation-control profile is not a widget API, component framework, CSS
contract, or agent-loop protocol. A presentation layer can be React, SwiftUI,
terminal curses, an editor extension, or an in-process view. The profile defines
the semantics crossing the boundary, not how pixels are drawn.

## Design Position

The presentation layer should not need provider SDK events, private adapter
state, agent-control request correlation, or implementation-name checks. It
should receive a presentation-ready state model and send typed user intent.

The control layer should not receive raw click coordinates, DOM state, layout
state, or framework-specific component events. It should receive semantic
intent such as "submit this message", "cancel this run", "select this model",
"toggle this tool", or "approve this permission request".

Principles:

- Presentation renders; control interprets.
- Presentation sends intent, not agent-control commands.
- Control owns policy, capability gating, correlation, routing, and recovery.
- Control returns renderable state, affordances, prompts, and diagnostics.
- Presentation may cache state locally, but canonical session/run state belongs
  to control.
- Feature availability is represented as affordances, not hardcoded UI checks.

## Boundary

```text
Presentation layer <-> Control layer
```

The control layer may then talk to one or more agent loops through
`agent-control-core` or `agent-control`. A single application may combine
presentation and control in one process, but the boundary remains useful for
testing, plugin integration, remote UIs, and swapping presentation surfaces.

## Core Concepts

`Surface`

A presentation instance attached to a control layer. Examples include one
browser tab, editor panel, terminal process, or mobile view.

`View`

A renderable projection of sessions, runs, timeline rows, prompts, controls,
diagnostics, and selection state. It is optimized for presentation, not for
agent-loop execution.

`Intent`

A semantic user or presentation action that the control layer may accept,
reject, transform, queue, or route. Intent is not a promise that the underlying
agent loop will perform the action.

`Affordance`

A control-visible UI capability such as send, cancel, select model, toggle
tool, approve permission, or load more transcript. Affordances include visible,
enabled, support level, reason, current value, and options when applicable.

## Required Envelope

Presentation-control envelopes use the base protocol envelope:

Required fields:

- `protocol`: fixed string `open-agent-protocol`.
- `version`: protocol version, currently `0.1`.
- `profile`: profile identifier, normally
  `open-agent-protocol.presentation-control`.
- `type`: envelope type.
- `id`: opaque envelope ID.
- `payload`: type-specific payload object.

Optional fields:

- `surface_id`: presentation surface ID when applicable.
- `view_id`: view ID when applicable.
- `session_id`: selected or affected session when applicable.
- `run_id`: selected or affected run when applicable.
- `sequence`: scoped ordering number.
- `timestamp_ms`: sender timestamp in Unix milliseconds.
- `in_reply_to`: original request ID for correlated responses.
- `extensions`: extension object for non-profile fields.

## Minimum Surface

Every row in this table is part of the minimum useful presentation-control
profile.

| Kind | Envelope type | Correlated response | Requirement |
| --- | --- | --- | --- |
| command | `presentation.attach.request` | `presentation.attach.response` | Attach a presentation surface and negotiate view preferences. |
| query | `view.snapshot.request` | `view.snapshot.response` | Return current renderable state for the requested view. |
| command | `intent.message.submit.request` | `intent.message.submit.response` | Submit user-authored message intent to the control layer. |
| command | `intent.run.cancel.request` | `intent.run.cancel.response`, or `error.response` if unavailable | Request cancellation through control-layer policy. |
| event | `view.updated` | not a response | Send renderable state updates after accepted intent or agent-control events. |
| event | `affordances.updated` | not a response | Send changed visible/enabled controls derived from capabilities and policy. |

As with agent-control, requests have semantic request/response correlation.
Renderable update events do not replace correlated responses.

## View State Shape

A view snapshot should be renderable without agent-loop-specific knowledge.

Common fields:

- `view_id`
- `surface_id`
- `active_session_id`
- `sessions`
- `timeline`
- `composer`
- `affordances`
- `pending_prompts`
- `notifications`
- `diagnostics`

### Session Summary

- `session_id`
- `title`
- `status`
- `active_run_id`
- `updated_at_ms`
- `unread_count`
- `metadata`

### Timeline Item

Timeline items are presentation projections. They are not required to preserve
raw agent-control event names.

Common fields:

- `item_id`
- `kind`
- `session_id`
- `run_id`
- `status`
- `title`
- `content`
- `created_at_ms`
- `updated_at_ms`
- `source_event_ids`
- `actions`

Common item kinds:

- `message`
- `reasoning`
- `tool_call`
- `tool_result`
- `permission_prompt`
- `user_input_prompt`
- `artifact`
- `checkpoint`
- `compaction`
- `status`
- `error`

### Composer State

- `session_id`
- `draft`
- `delivery`
- `selected_model_id`
- `selected_tools`
- `attachments`
- `enabled`
- `disabled_reason`

### Affordance

- `id`
- `kind`
- `visible`
- `enabled`
- `support`
- `reason`
- `value`
- `options`

Common affordance kinds:

- `send_message`
- `cancel_run`
- `interrupt_run`
- `resume_run`
- `select_model`
- `select_delivery`
- `toggle_tool`
- `resolve_permission`
- `resolve_user_input`
- `load_more_transcript`
- `open_artifact`

## Intent Events

Presentation intent should be typed and semantic.

Common minimum and near-core intents:

| Intent | Meaning |
| --- | --- |
| `intent.message.submit.request` | User wants to submit composer content. |
| `intent.run.cancel.request` | User wants to cancel a visible run. |
| `intent.config.update.request` | User changed selected model, delivery mode, tool policy, or displayable run options. |
| `intent.permission.resolve.request` | User chose an explicit permission option. |
| `intent.user_input.resolve.request` | User answered an agent-requested input prompt. |
| `intent.transcript.load_more.request` | User wants more historical transcript rows. |
| `intent.artifact.open.request` | User wants to open or retrieve an artifact. |

Intent responses acknowledge whether the control layer accepted the intent. They
do not mean the underlying agent loop completed the requested work.

## Control Responsibilities

The control layer owns:

- translating accepted intent into agent-control commands;
- request/response correlation below the control layer;
- canonical session and run state;
- policy and capability gating;
- optimistic update reconciliation;
- retry and reconnect behavior;
- projecting agent-control streams into view updates;
- producing affordances from capabilities, policy, and current state.

## Presentation Responsibilities

The presentation layer owns:

- layout, navigation, focus, keyboard, pointer, and accessibility behavior;
- local draft text and input composition before submit;
- rendering snapshots and updates;
- displaying disabled states and reasons from affordances;
- choosing when to request snapshots, history, or artifact display;
- preserving user-visible ordering and selection state.

## Out Of Profile

The following are intentionally outside presentation-control:

- raw DOM, component, or terminal drawing APIs;
- CSS, themes, layout metrics, and visual design tokens;
- raw provider SDK events;
- raw agent-control request correlation;
- tool execution;
- model provider requests;
- storage engine internals;
- presentation-specific analytics.

## Open Decisions

- Whether `view.updated` should use a small patch language or whole-object
  replacement per section.
- Whether draft composer synchronization belongs in core or an optional unit.
- How much multi-surface coordination should be standardized.
- Whether notification/toast semantics should be part of presentation-control
  or left to extensions.

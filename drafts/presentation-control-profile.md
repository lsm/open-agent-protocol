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

`Target`

A stable domain object the presentation layer wants to present, such as a
session, task, artifact, or dashboard. A target identifies what data is being
requested; it does not identify a page, component, browser tab, or layout.

`Snapshot`

Presentation-ready state for a target at a point in time. A snapshot may
contain sessions, timeline rows, prompts, controls, diagnostics, and selection
state. It is optimized for presentation, not for agent-loop execution.

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

- `session_id`: selected or affected session when applicable.
- `run_id`: selected or affected run when applicable.
- `sequence`: scoped ordering number.
- `timestamp_ms`: sender timestamp in Unix milliseconds.
- `in_reply_to`: original request ID for correlated responses.
- `extensions`: extension object for non-profile fields.

## Minimum Profile

Every row in this table is part of the minimum useful presentation-control
profile.

| Kind | Envelope type | Correlated response | Requirement |
| --- | --- | --- | --- |
| query | `presentation.snapshot.request` | `presentation.snapshot.response` | Return current presentation-ready state for a domain target. |
| command | `intent.message.submit.request` | `intent.message.submit.response` | Submit user-authored message intent to the control layer. |
| command | `intent.run.cancel.request` | `intent.run.cancel.response`, or `error.response` if unavailable | Request cancellation through control-layer policy. |
| command | `intent.permission.resolve.request` | `intent.permission.resolve.response`, or `error.response` if unavailable | Answer one permission prompt. |
| command | `intent.user_input.resolve.request` | `intent.user_input.resolve.response`, or `error.response` if unavailable | Answer one user-input prompt. |
| event | `presentation.updated` | not a response | Send presentation-ready updates for a target after accepted intent or agent-control events. |

As with agent-control, requests have semantic request/response correlation.
Renderable update events do not replace correlated responses, and a rejected
request is answered with exactly one correlated `error.response` carrying a typed
error, never with a response envelope whose fields merely imply the refusal. A
refusal that carries no code and no message leaves a presentation layer unable to
say why, which is the one thing a refusal exists to do.

## Target Shape

A target is an extensible discriminated object. The minimum profile defines:

- `{ "kind": "session", "session_id": "..." }`

The identifier required by a target depends on its `kind`. Unknown target kinds
may be carried through extensions. Richer profiles may define targets such as
tasks, artifacts, or dashboards.

## Snapshot Shape

A snapshot should be renderable without agent-loop-specific knowledge. Its
state shape is discriminated by `target.kind`; the protocol does not define one
universal state object for every target.

Common fields:

- `target`
- `epoch`
- `revision`
- `state`

`epoch` is an opaque identifier naming the revision numbering of one target.
Control mints a new epoch whenever it cannot promise that numbering continues: on
start, or when it rebuilds that target's state. A control layer that saves its
counter may keep one epoch across a restart. **Same epoch means same numbering**,
and that is the whole promise the identifier makes.

`epoch` is what makes the revision discipline sound across a control-layer restart
without making persistence mandatory. Revisions that never reset would oblige
every control layer to persist a counter, and v0.1 carries no persistence
obligation — the same reason the agent control core reports a replay gap rather
than faking continuity. This is that choice at the presentation boundary.

The minimum `session` target state contains:

- `session`
- `timeline`
- `composer`
- `affordances`
- `pending_prompts`
- `diagnostics`

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

Composer state contains control-owned effective configuration and availability.
Unsubmitted text and local attachment selection belong to the presentation
layer and are not part of the minimum snapshot.

- `session_id`
- `delivery`
- `selected_model_id`
- `selected_tools`
- `enabled`
- `disabled_reason`

`delivery` is the selected requested-delivery policy. It may remain `auto`
regardless of the visible session status. Presentation must not resolve `auto`
from its local snapshot because that state may be stale when admission occurs.
Control resolves it authoritatively and returns the requested and effective
delivery in `intent.message.submit.response`.

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

## Presentation Updates

`presentation.updated` carries typed changes against a known snapshot revision.
It must include:

- `target`
- `epoch`
- `base_revision`
- `revision`
- `changes`

Revisions are monotonically increasing integers scoped to one target **and one
epoch**. They describe presentation state consistency and are independent from
envelope `sequence`, which orders messages within a binding-defined stream scope
and is not required for this profile: the revision already orders updates.

Revisions do not advance by exactly one. A control layer may coalesce several
changes into a single update, and an update chains from its `base_revision` rather
than from the previous revision plus one. A new epoch may begin at any revision,
and a lower revision in a new epoch is not a regression.

Each change has a `kind` and kind-specific fields. The minimum session target
uses changes such as:

- `timeline.item.upsert`
- `session.status.set`
- `affordances.replace`
- `pending_prompts.replace`
- `diagnostics.replace`

Changes address domain objects by stable IDs such as `item_id`; they must not
address JSON array indexes or expose an implementation's object paths.

The receiver applies an update only when its local revision equals
`base_revision` **and** its held epoch equals the update's `epoch`. If the
revisions or the epochs do not match, it must discard the incremental changes and
request `presentation.snapshot` for the target. A snapshot whose `epoch` differs
from the one the receiver holds replaces what it holds for that target.

An update's changes are applied as a set or not at all. A receiver which cannot
apply **every** change in an update discards the whole set, does not advance its
revision, and re-snapshots the target exactly as on a `base_revision` mismatch.
Applying the changes it recognises and advancing anyway leaves the receiver
holding state that is not that revision's state, which is the failure the revision
discipline exists to prevent.

A receiver tolerating unknown change kinds on the wire — under the layered
draft's extension rules — has still not applied them, so the rule above applies
unchanged. Tolerating an unknown kind says the envelope was well formed; it does
not say the receiver understood it.

## Intent Events

Presentation intent should be typed and semantic.

Common minimum intents:

| Intent | Meaning |
| --- | --- |
| `intent.message.submit.request` | User wants to submit composer content. |
| `intent.run.cancel.request` | User wants to cancel a visible run. Names its `run_id`; with queue delivery a session may hold more than one nonterminal run, and control must not be able to cancel a run the user never saw. |
| `intent.permission.resolve.request` | User chose an explicit permission option. |
| `intent.user_input.resolve.request` | User answered an agent-requested input prompt. |

Near-core intents, not part of the minimum profile:

| Intent | Meaning |
| --- | --- |
| `intent.config.update.request` | User changed selected model, delivery mode, tool policy, or displayable run options. |
| `intent.transcript.load_more.request` | User wants more historical transcript rows. |
| `intent.artifact.open.request` | User wants to open or retrieve an artifact. |

### Resolving A Prompt

`pending_prompts` is the one source for what is being asked. Each entry projects
one core interaction and carries its `prompt_id`, its `kind`, its `run_id`,
renderable content — the question, and the tool call it concerns — and labelled
choices. The core's interaction and choice shapes are reused rather than restated,
so a choice is a labelled thing a reader can render rather than a bare id.

The two resolve intents name one prompt and one of its choices, or the answer to a
`user_input` prompt. A prompt resolves **once**: a stale or second answer is
refused with a typed `error.response` rather than applied, and the refusal names
the prompt.

A prompt leaves `pending_prompts` when it is resolved, cancelled or expired, and
the timeline keeps its outcome, so the transition is visible in the timeline
rather than by the entry's disappearance alone.

Affordances say only whether the presentation can act now, and why not. They never
repeat a prompt's content, and a prompt carries no `affordance_id`.

Intent responses acknowledge whether the control layer accepted the intent. They
do not mean the underlying agent loop completed the requested work. An accepted
message-submit response reports `requested_delivery`, concrete
`effective_delivery`, and `admission` so Presentation can reconcile automatic
delivery without inferring it from local run state. An `accepted` submit always
carries a non-null `effective_delivery`, and the pair is one the agent control
core admits: `auto` resolves to `start` or to a resolution reason, `queue` to
`queue`, `steer` to `steer`. A refused submit carries `admission: "rejected"` and
no effective delivery.

A submit intent carries an `intent_id` whose job is retry deduplication: the same
`intent_id` receives the same outcome and never starts a second run. A
presentation layer that retries after a lost response therefore learns the first
attempt's outcome instead of submitting twice. This is the only reason the profile
carries an intent identifier, and it is why the identifier belongs to the request
rather than being redundant with the envelope `id`.

## Control Responsibilities

The control layer owns:

- translating accepted intent into agent-control commands;
- request/response correlation below the control layer;
- canonical session and run state;
- policy and capability gating;
- optimistic update reconciliation;
- retry and reconnect behavior;
- projecting agent-control streams into presentation updates;
- producing affordances from capabilities, policy, and current state.

## Presentation Responsibilities

The presentation layer owns:

- layout, navigation, focus, keyboard, pointer, and accessibility behavior;
- local draft text and input composition before submit;
- local attachment selection before submit or upload;
- rendering snapshots and updates;
- displaying disabled states and reasons from affordances;
- choosing when to request snapshots, history, or artifact display;
- preserving user-visible ordering and selection state.

## Out Of Profile

The following are intentionally outside presentation-control:

- raw DOM, component, or terminal drawing APIs;
- CSS, themes, layout metrics, and visual design tokens;
- raw provider SDK events;
- raw agent-control request correlation. An intent response does not carry the
  agent control request it became, because that is the control layer's own
  correlation and a presentation layer has no use for it;
- tool execution;
- model provider requests;
- storage engine internals;
- presentation-specific analytics.

## Resolved Core Decisions

- Updates use typed changes and revision-based recovery, not JSON Patch or
  unconditional whole-snapshot replacement.
- `epoch` names one target's revision numbering, and the revision rules key on
  target and epoch. Revisions are monotonic within an epoch and do not advance by
  exactly one, so a control layer may coalesce changes.
- An update's changes apply as a set; a receiver that cannot apply every one of
  them re-snapshots rather than advancing.
- `pending_prompts` is authoritative for prompt content, and affordances never
  repeat it. A prompt carries no `affordance_id`.
- The two resolve intents are minimum profile, and a prompt resolves once.
- `intent.run.cancel.request` names its `run_id`.
- A rejected request is answered with one correlated `error.response` carrying a
  typed error, never with a response envelope implying the refusal.
- `intent_id` exists for retry deduplication and for nothing else.
- Draft composer synchronization is outside the minimum profile.
- Toasts and other transient notification presentation are UI implementation
  details. Control reports semantic diagnostics and state instead.

## Idea Pool

`Surface`

A surface is a specific presentation instance attached to a control layer, such
as one browser tab, editor panel, terminal process, or mobile view. Surface
identity could help with multi-window coordination, focus state, per-surface
preferences, or remote UI attachment.

Surface identity is intentionally outside the minimum profile for now. A simple
implementation can treat one connection as one presentation instance.

`Observation`

A live binding may need an opaque `observation_id` or `subscription_id` to
resume an update stream, scope sequence numbers, or release server-side
resources. This identifies an observation of a target, not a UI view. Snapshot
queries do not require one, so observation identity is intentionally outside
the minimum profile.

`Projection`

Some applications may need more than one semantic data shape for the same
target. An optional projection hint such as `compact_thread` could select that
shape. This should only be standardized if target kinds cannot express the
actual domain distinction; it must not become a component or layout name.

`Draft Synchronization`

Cross-surface or cross-device draft synchronization may be useful as an
optional profile. It would require surface identity, draft revisions, conflict
resolution, and persistence policy; none are required by the minimum profile.

`Durable Notification`

Some products may expose inbox-like notifications with identity, persistence,
read state, and actions. Such domain notifications are distinct from transient
toasts and may be standardized by a future profile if shared use cases emerge.

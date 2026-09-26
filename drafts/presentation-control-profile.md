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

Identities of domain objects cross the boundary unchanged. A session, a run, an
interaction and the agent-control events a timeline item cites keep the ids the
agent loop gave them, so the presentation layer and the loop beneath it name the
same object. Identities that only correlate a control-layer request with its
response do not cross. Every id a snapshot carries is unique within its target,
which a control layer fronting more than one agent loop has to ensure.

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

The minimum profile assumes every presentation attached to a control layer acts
for one trusted user, the trust model `goap hub` already has on a loopback bind.
Any attached presentation may answer a prompt, and the first answer wins because a
prompt resolves once. A presentation that may watch but not approve needs
presentation identity (*Surface*, in the idea pool below), and a control layer
serving the profile beyond one user waits for it.

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
| query | `presentation.snapshot.request` | `presentation.snapshot.response` | Return current presentation-ready state for a domain target, and subscribe the connection to its updates. |
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

A `presentation.snapshot.request` subscribes the connection it arrived on to that
target's updates until the connection closes. The response and every later update
for the target travel in order on that connection, so the first update a receiver
sees chains from the snapshot it just took; a binding that carries responses and
events on separate channels preserves that order across them. A connection that
never asked for a target receives nothing about it.

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
counter may keep one epoch across a restart — and keeping it obliges carrying
whatever else that epoch promises, which for a control layer that accepts intents
includes the outstanding-intent window below. A layer that cannot carry the window
across a restart mints a new epoch instead. **Same epoch means same numbering**,
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

A snapshot's `timeline` carries every item control still holds. A control layer
that drops an item, as a bounded-memory host may, says so with a
`timeline.item.remove` in the update that drops it, so a receiver that built its
state from updates holds what a fresh snapshot at the same revision carries.
History a host keeps elsewhere is reached through
`intent.transcript.load_more.request`, which stays near-core.

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
- `intent_id`, on any item an intent changed
- `actions`

`intent_id` is how a presentation layer recognises its own work in a snapshot it
did not receive the response for. After an epoch change — or a reconnect, or a
restart — reconciling means finding the item carrying the `intent_id` it sent, and
an item that omitted it could only be matched by comparing text, which fails the
moment the same message is submitted twice.

The minimum profile records one `intent_id` per item, and in the minimum profile
that is sufficient: a submit produces its own message item, a resolve changes one
prompt, and a cancel changes one run's status, so no two intents contend for the
same item. A richer profile whose intents can land on one item — a config update
followed by an interrupt, say — needs a list, because a single field keeps only the
later intent and a retry of the earlier one stops being matchable.

It is recorded on any item the intent **changed**, not only one it produced,
because most intents change an item somebody else produced. A submit produces its
own `message` item, but a resolve changes the `permission_prompt` or
`user_input_prompt` item the agent loop raised, and a cancel changes the run's
status item. An item no intent touched carries no `intent_id`, which is how a
reader tells an intent's work from the loop's.

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

An affordance is advice about the revision it arrived with, and it can be stale by
the time the user acts. Control checks again when an intent arrives: an intent sent
through an affordance that has since become available is accepted, and one that is
not available now is refused with a typed `error.response` saying why. A disabled
affordance is not a promise that an intent would fail, and an enabled one is not a
promise that it will succeed.

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
uses these changes:

- `session.replace`
- `session.status.set`
- `composer.replace`
- `affordances.replace`
- `pending_prompts.replace`
- `diagnostics.replace`
- `timeline.item.upsert`
- `timeline.item.remove`

Every piece of minimum session state can change without a snapshot: the five
replace kinds carry their state whole, and the timeline's upsert and remove
together express any change to it. `session.status.set` is the narrower form of
`session.replace`.

Changes address domain objects by stable IDs such as `item_id`; they must not
address JSON array indexes or expose an implementation's object paths.

Every receiver applies all the minimum change kinds. A
`presentation.snapshot.request` carries `change_kinds`, the kinds the receiver
applies beyond them; absent, it names none. Control sends that connection only
minimum kinds and kinds it named, so a change no named kind can express goes out in
minimum kinds, and state that neither the minimum nor a named kind covers is
outside what that receiver holds, so control sends it no changes for it. This
is what lets a control layer add a change kind without every older receiver
re-snapshotting on each update that carries it.

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

A receiver tolerating unknown change kinds on the wire — under the extension
rules the agent control core's tolerant compile applies — has still not applied
them, so the rule above applies unchanged. Tolerating an unknown kind says the
envelope was well formed; it does not say the receiver understood it.

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
one core interaction and carries its `interaction_id`, its `kind`, its `run_id`,
renderable content — the question, and the tool call it concerns — and labelled
choices. The core's interaction and option shapes are reused rather than restated,
so a choice is a labelled thing a reader can render rather than a bare id, and an
entry is identified by the same `interaction_id` the agent control core uses. The
presentation layer renames nothing: a resolve intent names the `interaction_id` the
core named, so the interface and the loop beneath it are talking about one object
rather than two projections of it.

The two resolve intents name one prompt and one of its choices, or the answer to a
`user_input` prompt. A prompt resolves **once**: a stale or second answer is
refused with a typed `error.response` rather than applied, and the refusal names
the prompt. A retry carrying the same `intent_id` is not a second answer — it is the
same answer arriving twice, and it receives the first one's outcome. Two answers
under different `intent_id`s are two answers, and the second is refused.

A prompt leaves `pending_prompts` when it is resolved, cancelled or expired, and
the timeline keeps its outcome, so the transition is visible in the timeline
rather than by the entry's disappearance alone.

Affordances say only whether the presentation can act now, and why not. They never
repeat a prompt's content, and a prompt carries no `affordance_id`.

Intent responses acknowledge that the control layer accepted the intent. They do
not mean the underlying agent loop completed the requested work. An accepted
message-submit response reports `requested_delivery`, concrete
`effective_delivery`, and `admission` so Presentation can reconcile automatic
delivery without inferring it from local run state.

A submit response therefore always means accepted; a refused submit is a correlated
`error.response`, never a submit response reporting `admission: "rejected"`. The
agent control core already refuses the latter, so this profile inherits the rule
rather than restating a second way to say it.

The admission pair is one the core admits, and there are two of them.
`effective_delivery` is never null on an accepted submit, and under
[Decision 0002](../decisions/0002-admission-before-start.md) `auto` resolves to
either `admission: "started"` with `effective_delivery: "start"`, or
`admission: "queued"` with `effective_delivery: "queue"` and a reserved `run_id`.
`queue` resolves to the queued shape. `steer` is not one of the two:
[Decision 0013](../decisions/0013-steer.md) is proposed, and the core refuses
`steered` in this subset, so this profile cannot offer it either.

### Accepting A Degraded Feature

An affordance may report `support: degraded`. The agent control core admits a
degraded control only on the caller's opt-in, and refuses it otherwise with
`capability_degraded` ([Decision 0005](../decisions/0005-run-controls.md)). At this
boundary the caller is the user, so the opt-in travels on the intent: an intent
that exercises a degraded affordance carries `allow_degraded`, the ids of the
degraded affordances the user accepted. Control forwards it as the core's
`allow_degraded_features` for the capabilities behind them, and refuses an intent
without it with `capability_degraded`. Control never opts in on the user's behalf.

### `intent_id` Is Retry Deduplication Within One Epoch

**Every** intent carries an `intent_id`, not only submit, so a retried resolve or
cancel is deduplicated the same way a retried submit is. Its single job: within one
epoch, the same `intent_id` **that control still remembers** receives the same
outcome and never causes a second effect — a second run, a second resolution, or a
second cancellation. A presentation layer retrying after a lost response learns the
first attempt's outcome instead of acting twice.

The guarantee is bounded by what control remembers, not by the epoch alone. An
intent's effect reaching the timeline is not forgetting it: from then on the item
carrying its `intent_id` is the record, and a duplicate is answered from that item.
An intent control has forgotten — because it evicted the item that recorded it, or
because the epoch changed — is simply re-evaluated as a fresh request, and may now
be refused where the first attempt was accepted, or accepted where it was refused.
A retry is only a retry for as long as the first attempt is still on record.

The promise is scoped to one epoch, because without persistence it cannot survive a
restart. After an epoch change, control has no record of what an earlier epoch's
`intent_id` did, so the deduplication window is gone. A presentation layer
reconciles by finding the timeline item carrying the `intent_id` it sent, which is
why an item an intent changed carries one. It does not assume a carried `intent_id`
is still known. This is the same reason the epoch exists at all: v0.1 carries no
persistence obligation, and a dedup window that silently expired would be worse
than one with a stated boundary.

The window is also bounded on the other side, and that is what keeps it affordable
for control. An accepted intent only needs remembering until its effect is visible
in the timeline, because that is when the item carrying its `intent_id` reflects it
and a later duplicate can be answered from the timeline instead. A refused intent
changed nothing — it is an `error.response` and nothing was admitted — so retrying
it is harmless and needs no record at all, and a retry of one is re-evaluated
against current state. Control's memory is therefore bounded by outstanding
intents rather than growing for the life of the epoch, which matters to `oapx`,
whose session memory is bounded by design.

Answering a duplicate from the timeline assumes control still holds the item. A
control layer that evicts old items, as a bounded-memory host may, announces each
with `timeline.item.remove`, and can no longer recognise a retry that arrives after
the eviction; it answers that retry as a fresh intent. That is a stated limit of this rule rather than a defect: the
deduplication window is bounded at both ends, by the epoch on one side and by what
control still holds on the other, and a presentation layer that retries after a
compaction is asking control about state control has released.

The identifier lives on the request rather than duplicating the envelope `id`,
which addresses one envelope.

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
  typed error, never with a response envelope implying the refusal. A submit
  response always means accepted, as the core already requires.
- `intent_id` is on every intent, its job is retry deduplication, and it is not a
  persistence key. The guarantee binds every intent control still remembers,
  including one whose effect is recorded on a timeline item control still holds,
  and nothing beyond that: a forgotten intent is re-evaluated as a fresh request. A
  retry under the same `intent_id` is not a second answer and receives the first
  outcome; two answers under different ids are two answers.
- Keeping an epoch across a restart obliges carrying the outstanding-intent window
  with it. A control layer that cannot mints a new epoch instead.
- A timeline item an intent **changed** carries that intent's `intent_id`, so a
  presentation layer can recognise its own work in a snapshot it never received the
  response for. A resolve records on the prompt's item, a cancel on the run's status
  item, a submit on the message it produced.
- Control's deduplication memory is bounded by outstanding intents: an accepted one
  is held on its own until its effect is visible in the timeline and by that
  timeline item afterwards, and a refused one changed nothing.
- An affordance is advice about the revision it arrived with. Control checks again
  when an intent arrives, and refuses one that is not available now with a typed
  `error.response`.
- Domain identities cross the boundary unchanged and are unique within their
  target; request correlation does not cross.
- A snapshot request subscribes its connection to that target's updates, in order
  after the response; a connection that never asked for a target hears nothing
  about it.
- Every receiver applies the minimum change kinds, a snapshot request names any it
  applies beyond them, and control sends it no others. Every piece of minimum
  session state can change without a snapshot.
- A snapshot carries every timeline item control still holds, and control that
  drops an item sends `timeline.item.remove`.
- An intent exercising a degraded affordance carries the user's `allow_degraded`
  opt-in; control never opts in for the user.
- The minimum profile assumes one trusted user behind every attached presentation.
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
implementation can treat one connection as one presentation instance. It is what a
read-only viewer would need, since the minimum profile lets any attached
presentation answer a prompt.

`Observation`

A live binding may need an opaque `observation_id` or `subscription_id` to
resume an update stream, scope sequence numbers, or release server-side
resources. This identifies an observation of a target, not a UI view. The
minimum profile subscribes a connection by its snapshot request and ends the
subscription with the connection, so observation identity, and an explicit
subscribe or unsubscribe, is intentionally outside it.

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

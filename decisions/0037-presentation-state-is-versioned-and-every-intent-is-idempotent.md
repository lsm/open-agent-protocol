# Decision 0037: Presentation State Is Versioned, and Every Intent Is Idempotent

Status: proposed
Date: 2026-09-26
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.presentation-control`
Related: [Decision 0036](0036-a-presentation-layer-is-not-evidence-for-its-own-profile.md),
whose gate these rules pass through: they become executable when its steps 1 and
2 land, and not before
Design: [Presentation Control Profile](../drafts/presentation-control-profile.md)

## Context

The presentation-control draft describes snapshots, typed updates against a
revision, and semantic intent, and leaves unwritten the rules that make them safe
to depend on. Reading the TUI against it, as Decision 0036 does, shows two holes
that are already load-bearing.

**There was no epoch.** Revisions were monotonic integers scoped to one target, and
a receiver that does not hold `base_revision` must discard the changes and request a
fresh snapshot. Nothing distinguished a receiver that is ahead because it persisted
across a control-layer restart from a control layer whose revision regressed, so a
presentation client that stores state could not converge after a restart.
`tui/session_store.zig` is 1,642 lines of exactly that persistence, and it answers
the question internally because the wire had nowhere to put the answer.

**`pending_prompts` and `affordances` were both control-owned and never
reconciled.** `resolve_permission` and `resolve_user_input` are affordance kinds,
and `pending_prompts` is its own field of the minimum session state. Nothing said
which is authoritative when a prompt exists and its affordance is absent, disabled
or disagrees with it. `AppState` splits the same state the same way — `approval`
against `tools` and `permission_mode` — so the ambiguity is live.

The same reading finds the rest of the class. Nothing said how updates reach a
presentation layer, a control layer could drop timeline items with no way to say
so, a receiver could not tell control which change kinds it understands, a retried
intent could act twice, a degraded feature had no way to be accepted, and nothing
said who may answer a prompt when two presentations are attached.

Each of these is a rule a producer and a validator have to agree on, so each is
settled here, ahead of the change that makes them executable, rather than left for
that change to guess.

## Decisions

### An epoch names one target's revision numbering

Every `presentation.snapshot.response` and every `presentation.updated` carries
`epoch`: an opaque identifier naming the revision numbering of one target.

A control layer mints a new epoch whenever it cannot promise that the numbering
continues — on start, or when it rebuilds that target's state. A control layer
that saves its counter may keep one epoch across a restart, and keeping it obliges
carrying whatever else that epoch promises, which for a layer that accepts intents
includes their deduplication window. A layer that cannot carry that window across a
restart mints a new epoch instead. **Same epoch means same numbering**, and that is
the whole promise the identifier makes.

A receiver holding a different epoch discards what it holds for that target and
takes the snapshot. An update whose epoch differs from the held one is discarded,
and the receiver re-snapshots the target.

The revision rules key on target **and** epoch. Within one epoch revisions
strictly increase and every update chains from its `base_revision`. They do not
advance by exactly one: the draft says monotonic, and requiring `+1` forbids a
control layer from coalescing several changes into one update. A new epoch may
begin at any revision, and a lower revision in a new epoch is not a regression.

This makes the revision discipline sound across a control-layer restart without
making persistence mandatory. Revisions that never reset would oblige every
control layer to persist a counter, and v0.1 carries no persistence obligation —
[Decision 0012](0012-persistence-is-not-in-v0.1-core.md) retires `+persistence`
for exactly that reason. The core already prefers reporting a gap to faking
continuity, and this is that choice at the presentation boundary.

### Same epoch and revision mean same state

A receiver holding a target at one epoch and revision holds exactly the state a
fresh snapshot at that epoch and revision carries. Three rules keep that true.

**An update applies as a set or not at all.** A receiver which cannot apply
**every** change in an update discards the whole set, does not advance its
revision, and re-snapshots the target exactly as it would on a `base_revision`
mismatch. The tolerant compile is not this answer and does not supply it: it makes
an unknown `change.kind` valid *on the wire* and says nothing about what the
receiver then *does*. A receiver that applied the changes it recognised and
advanced would hold state that is not that revision's state.

**Every piece of minimum session state has a change that carries it whole.** The
minimum change kinds are `session.replace`, `composer.replace`,
`affordances.replace`, `pending_prompts.replace`, `diagnostics.replace`,
`timeline.item.upsert` and `timeline.item.remove`, with `session.status.set` as the
narrower form of `session.replace`. A draft whose composer could change only by
snapshot would leave every receiver's composer stale from the first run onward.

**A snapshot carries every timeline item control still holds, and control that
drops an item says so.** A bounded-memory control layer may evict old items; it
sends a `timeline.item.remove` naming each one, in the update that drops it.
Without the removal, a receiver that built its state from updates would keep items
a fresh snapshot at the same revision no longer carries. History a host keeps
elsewhere is reached through `intent.transcript.load_more.request`, which stays
near-core: a host with nothing beyond what it holds needs no paging.

### Updates follow a snapshot on its connection

A `presentation.snapshot.request` subscribes the connection it arrived on to that
target's updates until the connection closes. The response and every later update
for the target travel in order on that connection, so the first update a receiver
sees chains from the snapshot it just took. A binding that carries responses and
events on separate channels preserves that order across them; otherwise a receiver
would discard the updates that follow its snapshot.

A connection that never asked for a target receives nothing about it, which also
keeps one session's state off every other presentation's connection. Explicit
subscribe and unsubscribe, *Observation* in the draft's idea pool, stays outside
the minimum profile.

### A receiver names the change kinds it applies

A `presentation.snapshot.request` carries `change_kinds`, the change kinds the
receiver can apply; absent, it means the minimum change kinds. Control sends that
connection only kinds it named. A change a narrower named kind cannot express goes
through the whole-state change for the same state, which every piece of minimum
session state has. State that no named kind covers is outside what that receiver
holds, and control sends it no changes for it.

Without this, the all-or-nothing rule turns every new change kind into a snapshot
per update for every older receiver once its control layer upgrades. With it, a
control layer adds a change kind without breaking a receiver that predates it.

### `pending_prompts` is the one source for what is being asked

`pending_prompts` is authoritative for the content of an outstanding prompt.
Each entry projects one core interaction and carries its `interaction_id`, its
`kind`, its `run_id`, renderable content — the question and the tool call it
concerns — and labelled choices. The core's interaction and option shapes are
reused rather than restated, so a choice is a labelled thing a reader can render
rather than a bare id, and the entry carries the same `interaction_id` the core
used. The presentation layer renames nothing, so a resolve intent names the object
the loop below it named.

`intent.permission.resolve.request` and `intent.user_input.resolve.request` join
the minimum profile. Each names one prompt and one of its choices, or the answer
to a `user_input` prompt. A prompt resolves once: a stale or second answer is
refused with a typed `error.response` rather than applied.

A prompt leaves `pending_prompts` when it is resolved, cancelled or expired, and
the timeline keeps its outcome. Affordances say only whether the presentation can
act now, and why not. They never repeat a prompt's content, and a prompt carries no
`affordance_id`.

### Every intent is idempotent while control remembers it

Every intent carries an `intent_id`. Within one epoch a retry under the same id
receives the first attempt's outcome and never causes a second effect — a second
run, a second resolution, a second cancellation — for as long as control
remembers the intent. Control holds an accepted intent on its own until its effect
reaches the timeline, and through the timeline item that carries its `intent_id`
afterwards; reaching the timeline is not forgetting. An intent control has
forgotten, because it evicted that item or because the epoch changed, is
re-evaluated as a fresh request. A refused intent changed nothing and needs no
record.

A timeline item an intent changed carries that intent's `intent_id`, so a
presentation layer that lost a response finds its own work in the next snapshot
instead of comparing text, which fails the moment the same message is submitted
twice.

### A degraded feature needs the user's say-so

An affordance may report `support: degraded`. The agent control core admits a
degraded control only on the caller's opt-in and refuses it otherwise with
`capability_degraded` ([Decision 0005](0005-run-controls.md)). At this boundary
the caller is the user, so the opt-in travels on the intent: an intent that
exercises a degraded affordance carries `allow_degraded`, the ids of the degraded
affordances the user accepted. Control forwards it as the core's
`allow_degraded_features` for the capabilities behind them, and refuses an intent
without it with `capability_degraded`. Control never opts in on the user's
behalf; a degraded result nobody accepted is the silent degradation the core's
rule exists to prevent.

### One trusted user, whichever presentation is attached

The minimum profile assumes every presentation attached to a control layer acts
for one trusted user, the trust model `goap hub` already has on a loopback bind.
Any attached presentation may answer a prompt, and the first answer wins because a
prompt resolves once. A presentation that may watch but not approve — a read-only
viewer, a shared screen — needs presentation identity, which stays in the draft's
idea pool as *Surface*, and a control layer serving the profile beyond one user
waits for it.

## Consequences

- None of these rules is executable until Decision 0036's steps 1 and 2 land, so
  this record stays `proposed` until they do. It can be amended without reopening
  0036's gate, and 0036 without reopening these rules.
- The step-1 projection implements every rule here in both memory backends. The
  step-2 validator checks the ones a trace can show: epoch and revision chaining,
  removal of held items only, only named change kinds on a connection, updates only
  for a target the connection asked for, one resolution per prompt, one effect per
  remembered `intent_id`, and a degraded affordance exercised only with its opt-in.
- `presentation.snapshot.request` gains `change_kinds`, every intent gains
  `allow_degraded`, and the minimum change kinds gain `session.replace`,
  `composer.replace` and `timeline.item.remove`.

## What this decision does not admit

- Applying part of an update.
- Updates for a target the connection never asked for.
- A change kind the receiver did not name.
- Control opting into a degraded feature for the user.
- Per-presentation roles before presentation identity exists.
- Paging as a minimum-profile requirement.

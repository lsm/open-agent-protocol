# Decision 0045: Reasoning Level and Compaction Policy Are Session Settings

Status: proposed
Date: 2026-10-02
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `session-settings` (claim term `+session-settings`)
Amends: nothing. Extends
[Decision 0028](0028-live-model-and-provider-control.md) by applying its
switch rules to two more session defaults, and takes the threshold policy that
[Decision 0044](0044-compaction.md) hands to a later decision
Leans on: [Decision 0035](0035-a-model-entry-publishes-its-facts-and-absence-means-unknown.md)
for the reasoning levels a model accepts, and
[Decision 0040](0040-a-session-reopens-through-its-own-binding.md) for what a
reopened session reports
Gated by: [Decision 0003](0003-staged-unit-graduation.md)
Tracks: #613 (the threshold policy 0044 defers), and #446's open item on the
configuration a reopened session reports

## Context

A control layer that schedules work across harnesses picks more than a model.
It picks how hard the model thinks and when the session compacts its history.
A review service that keeps a pool of harness entries, each with a weight and
a concurrency limit, wants the same two settings per entry. Today it cannot
express either through OAP:

- **There is no feature key for either.** OAP v0.1's keys cover the model
  (`run.model_selection`, `session.model.switch`, `models.list`), instructions,
  delivery, tools, structured output, provider attachment and the open
  elections. The provider profile already has a reasoning vocabulary —
  `reasoningLevel` in `provider.schema.json`, the `reasoning` object in
  `inference.schema.json` — but the agent-control profile never says what
  level a session runs at.
- **Every adapter drops the setting.** Pi's adapter refuses an unknown
  thinking level at open but never sets one. Codex 0.157.0 answers
  `thread/resume` with the `reasoningEffort` the thread last ran under, and the
  adapter decodes and drops it. Hermes and the DeepSeek harness take a
  reasoning effort when the session is created, and nothing passes one in.
- **`oapx` fixes both at open.** `oapx tui` over OAP takes the thinking level
  and the auto-compaction share from `oapx`'s own settings when the session
  opens and refuses a change with `UnavailableOverOap`, because the wire has
  nowhere to put one (`docs/tui-oap-seam.md`).
- **0044 makes a compaction visible but not configurable.** It publishes
  `threshold` compactions and defers "whether a control layer may read or set
  the point at which an endpoint compacts on its own" to a later decision,
  owned by #613. This is that decision.

Both settings have the same shape as the session's model: a default that the
next run starts under, that a control layer may set when the session opens or
change while it lives, and that the session state must report truthfully.
Decision 0028 already settled that shape for the model. This decision reuses
it rather than inventing a second one.

## Decisions

### Two features, one unit

- `session.reasoning` — the session's reasoning level.
- `session.compaction.policy` — when the endpoint compacts the history on its
  own.

They graduate together as `+session-settings`, and an endpoint advertises each
at its own level. Each feature discloses, in its `modes`, where the setting
may be supplied:

- `session_open` — on `session.open.request`.
- `session_live` — on `session.settings.update.request` (below).

An endpoint that takes a setting only when the session starts, as Hermes and
the DeepSeek harness do, discloses `session_open` alone. A request that
supplies a setting through a mode the feature does not disclose is refused
with `unsupported_feature`, `details.feature` naming the key and
`details.field` naming the member.

### The values

`reasoning_level` is a `reasoningLevel` from `provider.schema.json`. That
enum gains `max`, after `xhigh`. Pi 0.87.1 and `oapx` both define a level
above `xhigh`, and the shared enum has no value for it.

`compaction_policy` is an object with one required member, `kind`:

- `{"kind": "auto"}` — the endpoint's own threshold.
- `{"kind": "off"}` — the endpoint never compacts on its own. A history that
  overflows still fails the run, and 0044's `overflow` compactions remain
  allowed, because an `overflow` compaction is the endpoint recovering from a
  provider's refusal, not choosing to compact.
- `{"kind": "share", "share_percent": n}` — compact once the history reaches
  `n` percent (1–100) of the model's context window.
- `{"kind": "tokens", "tokens": n}` — compact once the history reaches `n`
  tokens.

An endpoint that can only switch its own compaction on and off — Pi's
`set_auto_compaction` takes a boolean — advertises `session.compaction.policy`
as `degraded` and refuses `share` and `tokens` with `unsupported_feature` and
`details.field: "compaction_policy"`. It does not round them to `auto`,
because a control layer that asked for a limit must learn that it did not get
one.

### Where the settings go

`session.open.request` gains two optional members, `reasoning_level` and
`compaction_policy`. Each is subject to the feature's `session_open` mode, as
above, and to the ordinary degraded gate: a setting the endpoint advertises as
`degraded` needs its key in `allow_degraded_features`.

A new core command pair changes either setting on a live session:

- `session.settings.update.request` carries `session_id`, at least one of
  `reasoning_level` and `compaction_policy`, and optionally
  `allow_degraded_features`.
- `session.settings.update.response` carries `session_id` and the effective
  value of every setting the request named. It may also carry
  `previous_reasoning_level` and `previous_compaction_policy`.

The update is all-or-nothing. One refused member refuses the request, and
neither setting changes. The endpoint applies the update before it answers and
emits `session.state.updated`. An update that names the values already in
force is a successful idempotent update, as an unchanged model switch is.

The update follows Decision 0028's ordering rules for a model switch:

- It affects runs that start after its response, including the promotion of
  submissions queued before it.
- A running run keeps the reasoning level it started with.
- A caller that needs a barrier waits for the response before its next
  submit.
- A request pipelined with an update has no implied order.

The compaction policy applies at the next point the endpoint would decide
whether to compact. 0044's deferral rule still governs when that point is: an
idle endpoint never compacts on its own, so a lower threshold set between
runs takes effect at the start of the next admitted run.

### What the state reports

The session state document gains `reasoning_level` and `compaction_policy`.
Each reports the value the session actually runs under, never only the value
the caller asked for. An endpoint that cannot learn a setting omits it, and
absence means unknown, as Decision 0035 rules for a model entry.

The state document must report the setting that actually took effect when it
differs from the one supplied, in three cases:

- **A model switch whose target does not accept the current level.** Where
  the target model's catalog entry carries `reasoning_levels` (Decision
  0035), a switch to a model that does not list the session's current level
  succeeds. The level becomes that model's `reasoning_default` (or the
  endpoint's own default, absent one), and the switch's `session.state.updated`
  reports the new level. Refusing the switch would make the level a lock on
  the model. Changing the level silently would hide a change the caller did
  not ask for.
- **A level the catalog rules out.** An open or update that names a level
  absent from the current model's `reasoning_levels` is refused with
  `unsupported_feature`, `details.field: "reasoning_level"`. Without such a
  list, the endpoint may still refuse a level its harness rejects, under the
  same code.
- **A reopen.** A reopened session (Decision 0040) reports the settings it
  resumed under. Codex's `thread/resume` already answers with the
  `reasoningEffort` the thread ran under, and the adapter maps it rather than
  dropping it.

### Capabilities and conformance

`session.reasoning` and `session.compaction.policy` join the optional core
feature keys. Their support levels follow `drafts/agent-control-core.md`:

- `native` — the harness takes the setting directly.
- `emulated` — the adapter produces the setting from a different native
  control. Claude Code's `set_max_thinking_tokens` takes a token budget, not a
  level, so a level maps to a budget the adapter chooses and publishes in the
  feature's `reason`.
- `degraded` — the endpoint honours the setting partially, as Pi's on/off
  compaction does, and refuses the rest rather than approximating it.

The claim term is `+session-settings`. It requires both features at `native`
or `emulated`, with `session_live` disclosed for each. A claim with only
`session_open` is the claim term `+session-settings-open`.

### Validator

Decision 0003's gate applies in full. The validator gains, at least:

- the new envelope pair in the envelope `oneOf`, the two open members, the two
  state members, and the `max` value, with matching edits to `protocol/`, the
  validator, fixtures and `clients/ts/src/protocol.ts`;
- that an update response repeats the effective value of each member the
  request named, and that `session.state.updated` follows a successful update
  before any run admitted after the response starts;
- that a request supplying a setting through a mode the feature does not
  disclose is refused `unsupported_feature` naming the key and the field;
- that `share_percent` lies in 1–100 and `tokens` is positive.

The diagnostic codes are the graduation PR's to name, with a fixture for each.

## Evidence

| Implementation | Reasoning level | Compaction policy | Ledger |
| --- | --- | --- | --- |
| Pi 0.87.1 | `set_thinking_level`, `off` through `max`; `get_state` reports it, and `thinking_level_changed` publishes a change | `set_auto_compaction`, a boolean | [`research/pi-v0.87.1-mapping.md`](../research/pi-v0.87.1-mapping.md); the adapter's `native` types name both commands |
| `oapx` | `ThinkingLevel`, `off` through `max` | `/autocompact auto\|percent\|off` | `zig/src/ai_types.zig`, `zig/src/tui/commands.zig` |
| Codex app-server 0.157.0 | `reasoningEffort` on the `thread/resume` response; the request-side member is not recorded at this pin | not recorded | [`research/codex-app-server-0.157.0-mapping.md`](../research/codex-app-server-0.157.0-mapping.md) |
| Claude Code 2.1.263–2.1.282 | `set_max_thinking_tokens` (a budget) on the TypeScript control surface; 2.1.282's `system/init` adds `per_turn_effort_active` | not recorded | [`research/claude-code-agent-sdk-2.1.263-mapping.md`](../research/claude-code-agent-sdk-2.1.263-mapping.md), [`research/claude-code-agent-sdk-2.1.282-mapping.md`](../research/claude-code-agent-sdk-2.1.282-mapping.md) |
| Hermes 2026.8.31 | `reasoning_effort` on `session.create` only | not recorded | [`research/hermes-v2026.8.31-mapping.md`](../research/hermes-v2026.8.31-mapping.md); the current pin's ledger should confirm it before graduation |
| DeepSeek harness 47f9438 | `reasoningEffort` on `initialize` only | not recorded | [`research/deepseek-harness-47f9438-mapping.md`](../research/deepseek-harness-47f9438-mapping.md) |
| ACP 1.7.0 | a `thought_level` session config option, where the agent offers one, through `session/set_config_option` | not recorded | [`research/acp-v1.7.0-mapping.md`](../research/acp-v1.7.0-mapping.md) |
| OpenCode | per-model `reasoning_options` (a toggle, an effort, a budget) in the provider catalog; the session-side request is not recorded | not recorded | [`research/opencode-provider-catalog-mapping.md`](../research/opencode-provider-catalog-mapping.md) |

The graduating implementations are **Pi**, the one pinned harness with a live
setter for both, and **`oapx`'s endpoint**, which needs this decision so
`oapx tui` over OAP can stop refusing the thinking level and `/autocompact`.
Pi reaches `native` for `session.reasoning` and `degraded` for
`session.compaction.policy`, so `oapx` is the implementation that exercises
`share`. Hermes and the DeepSeek harness are the `session_open`-only case.
Codex's request side is a ledger task, not a blocker.

## What this decision does not decide

- **A per-run override.** A submit cannot carry a reasoning level for one run,
  as `model_id` can. A control layer that wants one updates the setting,
  submits, and updates it back. A run-scoped override, if one is ever
  needed, extends `run.model_selection`'s pattern in a later decision.
- **Budgets beside levels.** `inference.schema.json`'s `reasoning` object
  carries `budget_tokens`, and Claude Code's control is a budget. This
  decision exposes the level only. An endpoint maps a level to a budget and
  says how in its feature `reason`.
- **Other session defaults.** Service tier, sandbox, approval policy and
  working directory are session settings too, and Codex's resume response
  reports several of them. This decision's pair is meant to grow by later
  decisions, one member at a time, each with its own feature key. It does not
  admit them here.
- **The context window and the output limit.** `oapx` fixes both at open, as
  it does the thinking level. They are properties of the model and provider
  rather than of the session's behaviour, and belong with the model catalog
  (Decision 0035).

## Consequences

A control layer can choose, per session, how hard the model thinks and when
the history compacts. It can learn whether the endpoint honoured each choice,
and see both in the session state, including after a reopen. A pool of harness
entries can carry both settings on the wire rather than through each harness's
launch flags. `oapx tui` loses two of its `UnavailableOverOap` refusals.

The cost is one request pair, two open members, two state members and one enum
value. The ordering, the degraded gate, the catalog check and the reporting
rule are all borrowed from Decisions 0028, 0035 and 0040.

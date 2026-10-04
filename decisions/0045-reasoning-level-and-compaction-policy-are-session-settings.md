# Decision 0045: Reasoning Level and Compaction Policy Are Session Settings

Status: accepted 2026-10-04 (both units graduated on the shared validator in
both trees with their fixtures, the open-time settings (#830) and the live
update (#849); both memory reference adapters take a compaction policy at open
(#840) and live (#850); every pinned adapter applies or refuses both settings
at open (#831). Of the two graduating implementations, Claude Code changes
both settings live through `apply_flag_settings`, probed against the 2.1.288
binary (#854), and `oapx` takes the reasoning level at open and live (#856),
its hub-served adapter takes the compaction policy (#865), and `oapx tui` over
OAP sends both (#864, #869). Live gates drive Pi (#857), Codex (#858), Hermes
(#859) and OpenCode (#860) through an update against their real binaries, and
a reopened Codex session reports the level its thread resumed under (#871).
`oapx serve agent` refuses a compaction policy because it keeps no history
between runs. A per-run override, budgets beside levels and further session
defaults stay with later decisions)
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
- **Every harness has both, and every adapter drops them.** Each of the seven
  pinned harnesses takes a reasoning level and a compaction policy at least
  when a session starts (Evidence, below). None of the adapters passes either
  in. Pi's adapter refuses an unknown thinking level at open but never sets
  one. Codex 0.157.0 answers `thread/resume` with the `reasoningEffort` the
  thread last ran under, and the adapter decodes and drops it.
- **`oapx` carries one of the two.** `oapx tui` over OAP sends the thinking
  level in the open request's `metadata.oapx`, fixed for the session, and
  refuses a later change with `UnavailableOverOap`. The auto-compaction share
  neither travels nor applies over OAP. `/autocompact` changes the TUI's local
  state and reports success, but nothing arms it on the endpoint, so the
  setting silently does nothing (`docs/tui-oap-seam.md` lists neither).
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

**`session_open` is required.** Every pinned harness can take both settings
when a session starts, so every adapter in this repository advertises both
features with `session_open`, and an endpoint claiming `+session-settings`
must too. `session_live` is disclosed where the harness can change a setting
on a running session, and only there. An endpoint that takes a setting only
when the session starts discloses `session_open` alone. A request that
supplies a setting through a mode the feature does not disclose is refused
with `unsupported_feature`, `details.feature` naming the key and
`details.field` naming the member.

### The values

`reasoning_level` is a `reasoningLevel` from `provider.schema.json`. That
enum gains `max`, after `xhigh`. Codex, Claude Code, Pi, Hermes and `oapx` all
define a level above `xhigh`, and the shared enum has no value for it.

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

A harness may honour a form in one mode and not the other. Pi takes a token
threshold at launch but only switches compaction on and off live
(`set_auto_compaction`). Such an endpoint refuses the form it cannot honour in
that mode with `unsupported_feature` and `details.field: "compaction_policy"`,
and says which forms each mode takes in the feature's `reason`. It never
rounds a `share` or `tokens` request to `auto`, because a control layer that
asked for a limit must learn that it did not get one.

### Settings a harness reads only at launch

Several harnesses take a setting only from their own configuration when the
process starts:

- Pi's `settings.json`, in an agent directory `PI_CODING_AGENT_DIR` names;
- Hermes's `config.yaml`, under `HERMES_HOME`;
- OpenCode's config, through `OPENCODE_CONFIG_CONTENT`;
- the DeepSeek harness's cordis patch layer;
- cagent's agent YAML.

An adapter that starts one harness process per session sets such a setting by
writing that configuration privately for the process it starts. This counts
as `session_open`, because the setting is the harness's own and the process
is the session's.

It never edits the operator's own configuration. The private directory or
variable is the adapter's own value in the child's environment, never one
inherited from the operator, which keeps the hub's rule that a child inherits
no ambient variable. Where one process
serves several sessions, as the DeepSeek harness's `initialize` does, the
setting binds the process. The adapter then either starts a process per
distinct setting or refuses a second, different setting with
`unsupported_feature`.

Two cases fall outside this rule, and the first implementations met both:

- **The configuration holds more than the setting.** Pi's agent directory also
  holds Pi's credentials. Moving it to a private directory would sign the
  session out, so Pi's adapter refuses a threshold with `unsupported_feature`
  and takes only the on/off switch that the RPC carries.
- **The adapter does not start the harness.** The OpenCode adapter attaches to
  a server its operator already started, so it cannot set what that server
  reads at launch. It advertises `session.compaction.policy` as `unavailable`
  with that reason.

Both are refusals, not approximations, and both say why in the feature's
`reason`. The `session_open` requirement covers what the adapter can reach.

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

- `native` — the harness takes the setting directly, as Claude Code's
  `effortLevel` and Hermes's `threshold_tokens` do.
- `emulated` — the adapter produces the setting from a different native
  control. Examples:
  - Claude Code's token window and DeepSeek's share are approximations the
    harness's own threshold arithmetic still shapes.
  - An ACP agent's level is whichever value of its `thought_level` option
    matches by name.
  - A harness whose level vocabulary is model-defined is mapped through the
    catalog.
- `degraded` — the endpoint honours the setting only partly, and refuses
  what it cannot honour rather than approximating it.

The claim term `+session-settings` requires both features at `native` or
`emulated` with `session_open` disclosed. `+session-settings-live`
additionally requires `session_live` for both.

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

Each row is recorded, source-read at the pinned commit, in a section of the
harness's current ledger titled "Reasoning level and compaction". This
decision is what those sections were recorded for.

| Harness (pin) | Reasoning level at open | … live | Compaction policy at open | … live |
| --- | --- | --- | --- | --- |
| Codex app-server 0.157.0 | `thread/start` `config.model_reasoning_effort` | `turn/start` `effort`, "this turn and subsequent turns" | `config.model_auto_compact_token_limit` (tokens); `config.model_post_turn_compact_threshold_percent` (turn-end share, `0` off) | none |
| Claude Code 2.1.282 | `effort` option (`low`–`max`); thinking off through `thinking` | `apply_flag_settings {effortLevel}`; `set_max_thinking_tokens` (a budget) | `autoCompactEnabled`, `autoCompactWindow` (tokens) through the flag layer | `apply_flag_settings` with the same keys |
| Pi 0.87.1 | `--thinking` (`off`–`max`) | `set_thinking_level` | `settings.json` `compaction.enabled`, `reserveTokens` (threshold = window − reserve) | `set_auto_compaction` (on/off only) |
| Hermes 2026.9.24 | `session.create` `reasoning_effort` | `config.set reasoning` (session scope) | `config.yaml` `compression.enabled`, `threshold` (share), `threshold_tokens` | none |
| DeepSeek harness dsh-v0.1.7-rc.2 | `initialize` `reasoningEffort` (process-wide, opaque per model) | none | `compaction-basic` (mounted by the `sdk` profile's `dsh-base`) `thresholdRatio`, `headroomTokens`, or `disabled` (patch layer) | none |
| OpenCode 1.18.32 | the created session's model `variant` | the prompt's `variant` (V1 route) | `compaction.auto`, `compaction.buffer` (reserve) via `OPENCODE_CONFIG_CONTENT` | none |
| ACP 1.9.1 with cagent 1.143.0 | cagent `thinking_budget` (effort or tokens) | ACP `thought_level` config option, which cagent does not implement | cagent `session_compaction`, `compaction_threshold` (share) | none |
| `oapx` | `ThinkingLevel` (`off`–`max`) | the same | `/autocompact auto\|percent\|off` | the same |

What the table settles:

- **Every harness takes both settings at open.** That is why `session_open` is
  required rather than optional, within what an adapter can reach (see
  "Settings a harness reads only at launch").
- **Live changes are the exception.** A level changes live on Codex (with the
  next turn), Claude Code, Pi, Hermes and OpenCode (with the next prompt). A
  compaction policy changes live only on Claude Code and, as on/off, on Pi.
- **The policy's forms all occur.**
  - `tokens` is native on Codex, Claude Code and Hermes, and emulated as
    `window − tokens` on Pi, OpenCode and the DeepSeek harness.
  - `share` is native on Hermes, cagent and the DeepSeek harness. Codex's
    share triggers only at a turn's end, so its adapter refuses `share`
    rather than approximating it.
  - `off` exists everywhere except Codex, where the source read found no
    switch. On the DeepSeek harness, `off` is a patch layer that disables the
    `compaction-basic` plugin.
- **The level vocabularies are wider than OAP's.** Codex adds `ultra` and
  `persistent`, Hermes adds `ultra`, and the DeepSeek harness and OpenCode use
  model-defined names. OAP carries the seven shared values. A harness with
  more maps the extra ones to no OAP value. A harness with model-defined names
  maps through the model's catalog entry, and refuses a level it cannot map.

The graduating implementations are **Claude Code**, the one harness that
changes both settings live, and **`oapx`'s endpoint**, which needs this
decision so that `oapx tui` over OAP can stop refusing a thinking-level change
and make `/autocompact` reach the endpoint. Every other adapter ships `session_open` support with the
same graduation PR or the one after it, because the requirement above makes
it a conformance obligation and not an option.

The ledger sections are source-read. Codex's three config keys are also
confirmed in the installed 0.157.0 binary. A live setting's effect on a
running session is observed for none of them yet, and a gate that drives
each harness through an update is part of the graduation evidence.

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
launch flags. `oapx tui` stops refusing a thinking-level change, and its
`/autocompact` starts reaching the endpoint instead of silently doing nothing.

The cost is one request pair, two open members, two state members and one enum
value. The ordering, the degraded gate, the catalog check and the reporting
rule are all borrowed from Decisions 0028, 0035 and 0040.

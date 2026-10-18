# Decision 0051: Transcript Load

Status: accepted 2026-10-18 (owner)
Date: 2026-10-18
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `transcript-load` (claim term `+transcript-load`, plan sub-unit T6)
Amends: nothing. Graduates the unit
[Decision 0012](0012-persistence-is-not-in-v0.1-core.md) staged, under
[Decision 0039](0039-a-session-is-oaps-and-a-harness-is-where-it-runs.md)'s
constraint that its entries and cursor are OAP's vocabulary
Gated by: [Decision 0003](0003-staged-unit-graduation.md)
Design: [Staged Units Graduation Plan](../drafts/staged-units-graduation.md),
"T6. Transcript load"

## Context

A host that reopens a session (0040) has nothing to show. Replay stays within
one process's journal, so the conversation before a restart is invisible on
the core wire. `work.read` ([drafts/work.md](../drafts/work.md)) fills part of
the gap for the work layer: user and assistant text, cut at 64 KiB, with an
index cursor. It carries no tool calls, no compaction, and no sign of what it
left out.

The evidence 0012 and 0039 named is still the evidence: Pi's
`get_entries { since }`, and ACP's replay through `session/update` on
`session/load`. Since then `work.read` has grown native readers for Codex
(`thread/turns/list`), Claude Code (its project `<id>.jsonl`), OpenCode
(`GET /api/session/<id>/message`), Pi (its session file) and `oapx` (its saved
sessions). Six harnesses can be read today.

## Decisions

### 1. One request pair, a forward cursor read

`transcript.load.request {session_id, cursor?, limit?}` answers
`transcript.load.response {session_id, entries, next_cursor?, complete, gap?}`.
Entries are oldest first. Without `cursor` the read starts at the oldest entry
the store still holds. `complete: true` means nothing follows the last entry
returned. A session is read whether it is open or only bound, without
resuming it, as `work.read` already does. Most readers start nothing; ACP's
reads through a short-lived agent process that loads the session and exits.

### 2. An entry is OAP's, and the transcript is a list

`{entry_id, kind, at_ms?, run_id?, message?, compaction?, truncated?}`:

| `kind` | carries |
| --- | --- |
| `message` | a `message` (role and parts, as the core already defines them), including tool calls and tool results as parts |
| `compaction` | `{compaction_id?, summary}`: the point where the history the model sees was replaced |

`kind` is open to later decisions, and a host skips a kind it does not know.

**One source per transcript.** A cursor orders entries from one store only,
so its order is that store's order. Where the transcript is the harness's
(Claude's project jsonl, Codex's `thread/turns/list`, Pi, OpenCode, ACP), every
entry comes from the harness, and a harness rewrite such as a Claude
compaction is that store changing: a cursor it invalidates answers the gap in
rule 5. OAP's own records are never interleaved into it. A later decision
that adds an OAP-sourced kind adds it only where the transcript is OAP's own
(`oapx`).

A harness that stores a tree (Pi) is read in file order, as `work.read`
already reads it. A branch the harness left reads along with the one it kept.
An entry may carry `extensions` with the harness's own ids. Those never
become OAP identities.

### 3. The cursor is opaque, and stable for as long as the entry exists

The cursor is an opaque string the endpoint mints and only that endpoint
reads. It names a position after an entry, not a page. It stays valid across
reconnects and restarts for as long as the entry it follows is still in the
store. A host never builds one or compares two.

### 4. Each page is bounded and says so

`limit` is 1 to 500, default 100. A page also stops before 512 KiB of
entries, so one response fits the smallest frame limit it can travel on: the
`serve agent` stdio binding's 1 MiB (`drafts/endpoint-stdio.md`), well under
the hub's 16 MiB. A page that stopped early has `complete: false` and a
`next_cursor`. An entry larger than the page bound is returned alone, cut to
fit: its text parts on a code-point boundary and any other part replaced by
its kind alone, and the entry carries `truncated: true`. No page is a silent prefix.

### 5. A cursor that is gone is an explicit gap

A cursor records the entry it follows and that entry's position in the store.
When the entry is no longer there, the read does not pretend to continue. It
answers from the oldest entry still held, with `gap: {reason, resumed_at}`,
and the reason is decided by position alone:

- `pruned` when the oldest entry still held is past the cursor's position, so
  the store dropped the front of the transcript;
- `rewritten` otherwise: the position is still inside what the store holds but
  the entry is not, so the store changed under the cursor (a Claude
  compaction rewriting its jsonl is this case).

This is the transcript's `*adapter.ReplayGap`. A cursor that does not decode,
or that names another session, is refused `invalid_cursor` and is never a gap.

### 6. Compaction adds an entry, never removes one

A completed compaction (0044) appends a `compaction` entry carrying the
summary. Entries before it stay in the transcript for as long as the store
keeps them: they are what was said, even though the model no longer sees
them. A host draws the boundary from the entry. A store that discards
compacted entries answers a cursor into them with the gap in rule 5. A failed
or cancelled compaction adds nothing, as 0044 already rules.

### 7. `work.read` is a projection of it

Where an adapter serves `transcript.load`, `work.read` is computed from it:
each user `message` entry and the assistant text that follows, with `index`
kept as `work.read`'s own stable counter. `work.read` keeps its shape and
limits. The native readers it already has become the adapters'
`transcript.load` implementations, so there is one reader per harness, not two.

### 8. It is declinable

The capability key is `session.transcript.load`. An adapter with no readable
store advertises it `unavailable`, and a request is refused
`unsupported_feature` naming the key. Hermes is that case at its pin: its
gateway reads a stored session only by resuming it, and a resume can restart
a turn a crash interrupted. DeepSeek is that case until its wire reaches its
store. `oapx serve agent` has no history and refuses it.

### `transcript.delta` stays out

No harness has shown a complete feed of persisted writes, and Pi's
`entry_appended` is recorded as incomplete. A host that wants new entries
reads again from its last cursor.

## Consequences

- Schema, `go/protocol`, the validator, fixtures and `clients/ts` gain the
  pair, the entry, `invalid_cursor` on this request and the gap member.
- Both hubs route it. The page bound comes from the endpoint binding's frame
  limit, the smaller of the two.
- Each adapter's reader moves from `work.read` into its `transcript.load`,
  first `oapx` (the store is ours), then Claude, Codex, Pi, OpenCode and ACP.
- A run cut off before it settled still shows the output it produced, since
  that output is in the store.

## What this decision does not admit

- A replay of events. A transcript is what was persisted, not the stream.
- A write. Seeding a harness from OAP's transcript waits for the native store
  0039 names.
- A cursor that is a harness's entry id or a run sequence.

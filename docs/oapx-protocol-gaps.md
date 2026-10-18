# oapx protocol gaps for a model backend

What `oapx` implements of the OAP surface a control layer needs to use it as a
model backend without losing capabilities, checked against `main` on
2026-10-17. HyperNeo is the first such control layer, and the order below is
its order: the items that let a hub or daemon restart without losing a
session's context come first, because its plan's PR 10 and PR 12 depend on
them.

`docs/oap-alignment.md` is the starting point but is historical: its
*Deferred scope* list predates the hub, the session history, reopen and the
work verbs, and several of its entries have since landed. This document
replaces that list as the record of what is missing.

Status words:

- **implemented**: specified, and `oapx` does it on the path a control layer
  uses, `oapx serve` (the hub) over its own loop or a harness adapter.
- **partial**: specified and implemented, but with a gap the control layer
  would hit, named in the row.
- **specified**: specified and not implemented.
- **missing**: neither specified nor implemented.

"Spec revision" says whether closing the gap needs a decision or schema change
before code, or only implementation.

Paths are relative to the repository root, and every line number is at
`main` as of this document.

## Summary

| # | Feature | Status | Spec revision |
|---|---|---|---|
| 1 | Transcript persistence and load | partial (harness adapters), missing (oapx's own loop under `serve`) | yes, `transcript-load` is staged, not decided |
| 2 | Resume attachment (reattach) | partial: implemented for harness adapters, not for oapx's own loop under `serve` | no |
| 3 | Event replay with cursors | partial: implemented within one hub process, lost on restart | yes, for replay across a restart |
| 4 | Run-scoped cancellation | implemented | no |
| 5 | Admission receipts, stable run and submission identity | partial: stable within a process, not across a restart | no for the receipt; yes for identity across a restart |
| 6 | Negotiated capabilities, `capabilities.updated` | partial: request and response implemented, `capabilities.updated` specified and never emitted | no |
| 7 | queue, steer and btw delivery | partial: queue and steer implemented, btw specified and refused | no (btw needs its unit decided) |
| 8 | Control-layer-provided tools (`action.tools.provide`) | implemented on oapx's own loop only | no |
| 9 | `session.list` | implemented | no |
| 10 | Endpoint and participant identity | partial: endpoint identity implemented, the hub is one participant | yes, for more than one participant |
| 11 | Multi-connection session ownership | missing | yes |
| 12 | Tool sources changed without a hub restart | partial: at open on ACP only; refused by oapx's own loop; no live change | yes, for a live change |
| 13 | Per-session environment and credential grants | missing | yes |

## Restart without losing context

### 1. Transcript persistence and `transcript.load` / `transcript.delta`

**Spec.** `+persistence` is retired. `transcript.load.request` and
`transcript.load.response` are staged as the `transcript-load` unit, with four
questions still open: what the cursor is, what an entry is, whether a read is
bounded, and how an endpoint with no store declines
(`decisions/0012-persistence-is-not-in-v0.1-core.md:60`,
`drafts/staged-units-graduation.md:4184`). Decision 0039 adds that entries
and the cursor are OAP's vocabulary, not a harness's rows, and that the
harness's store is the store of record until OAP has its own
(`decisions/0039-a-session-is-oaps-and-a-harness-is-where-it-runs.md:126`,
`:148`). `transcript.delta` is deliberately not staged, because Pi's evidence
contradicts it (`decisions/0012-persistence-is-not-in-v0.1-core.md:74`).
Neither pair is in `schema/v0.1/envelope.schema.json`.

**Implementation.** No OAP envelope. The nearest surface is the hub's
`work.read` op (`zig/src/hub/stdio.zig:39`, `:919`; `drafts/work.md:215`),
which reads turns rather than OAP entries:

- For Codex, Claude Code, Pi, OpenCode and an ACP agent that advertises
  `loadSession`, it reads the harness's own transcript. That survives a hub
  restart (`zig/src/adapter/codex/adapter.zig:129`,
  `zig/src/adapter/claude/adapter.zig:162`, `zig/src/adapter/pi/adapter.zig:131`,
  `zig/src/adapter/opencode/adapter.zig:146`, `zig/src/adapter/acp/adapter.zig:160`).
- For oapx's own loop, Hermes and DeepSeek, it answers only the turns the hub
  recorded in memory, and `serve` writes no message text to disk
  (`drafts/work.md:41`, `:229`). Those are gone after a restart.

oapx's own loop can save and reload its transcript: it carries a transcript
store and a history loader (`zig/src/adapter/oapx/adapter.zig:106`, `:109`,
`:436`). But only the terminal UI wires them
(`zig/src/tui/oap_execution.zig:186`). `oapx serve` builds the adapter
without either (`zig/src/tools/makai.zig:322`), so a served oapx session
advertises "a session does not outlive the process"
(`zig/src/adapter/oapx/adapter.zig:39`).

**Status.** Partial for harness adapters, through the harness's store and
`work.read`. Missing for oapx's own loop under `serve`, the loop that is
HyperNeo's default for GLM, Kimi and DeepSeek.

**Spec revision.** Yes. `transcript-load` must be decided before an OAP
envelope exists. Wiring the oapx loop's existing transcript store into
`serve` needs no revision, and it is the first step that gives that loop
durable history.

### 2. Resume attachment (session reattach)

**Spec.** `+session-reattach` is executable under Decision 0040: a reopen is a
member of `session.open.request`, gated by `session.open.reopen`, answered
with the state document, with three refusals that fail closed
(`decisions/0040-a-session-reopens-through-its-own-binding.md:79`, `:88`,
`:132`; `drafts/conformance.md:693`). The hub records one binding per open in
its session history, and a restarted hub lists and reopens what a shutdown
left behind (`drafts/hub.md:72`, `:1398`).

**Implementation.** The hub routes a reopen (`zig/src/hub/hub.zig:831`,
`:846`). Each adapter advertises `session.open.reopen`:

- native in Claude Code, Codex, ACP, Hermes, OpenCode and Pi
  (`zig/src/adapter/claude/adapter.zig:27`, `zig/src/adapter/codex/session.zig:1251`,
  `zig/src/adapter/acp/adapter.zig:103`, `zig/src/adapter/hermes/adapter.zig:36`,
  `zig/src/adapter/opencode/adapter.zig:38`, `zig/src/adapter/pi/adapter.zig:69`);
- unavailable in DeepSeek (`zig/src/adapter/deepseek/adapter.zig:35`);
- emulated in oapx's own loop, but only with a history loader
  (`zig/src/adapter/oapx/adapter.zig:64`). Under `serve` it advertises the
  unsaved descriptor without reopen (`:35`, `:180`), for the reason in row 1.

`drafts/hub.md:71` still says a client reconnecting to a restarted hub "finds
no session" because reattach is staged. That row is stale: reattach has
landed.

**Status.** Partial. Implemented for every pinned harness except DeepSeek;
not available on oapx's own loop under `serve`.

**Spec revision.** No. The oapx gap closes with row 1's wiring.

### 3. Event replay with cursors

**Spec.** A cursor is `(run, sequence)` and may name its run; an expired
cursor is `oap-replay-gap` (`decisions/0026-a-resume-cursor-may-name-its-run.md:41`,
`:47`; `drafts/hub.md:1108`).

**Implementation.** The hub's `events` op and the SSE route replay from the
journal (`zig/src/hub/stdio.zig:697`). The journal is process memory, 256
events per session by default (`zig/src/hub/hub.zig:13`, `:46`), and every
adapter advertises `run.replay` as degraded for the same reason, for example
"up to 65536 events across the session's runs are retained in process memory"
(`zig/src/adapter/oapx/adapter.zig:49`).

**Status.** Partial. Correct within one hub process, including gaps. After a
restart every cursor is a gap, because nothing is retained.

**Spec revision.** Yes, for replay across a restart. Today the gap answer is
the honest one, and a durable journal would change what `run.replay` may
promise.

## Runs and admission

### 4. Run-scoped cancellation

**Spec.** `run.cancel` targets a run, and acceptance is judged when the cancel
is checked (`decisions/0041-cancel-acceptance-is-judged-when-the-cancel-is-checked.md:46`).

**Implementation.** oapx's own loop: native, "cancelling a run leaves its
session open" (`zig/src/adapter/oapx/adapter.zig:48`). The historical
conflict, that a cancel closed the session (`docs/oap-alignment.md`,
*Conflicts raised*), no longer holds. For harnesses, Codex is native
(`zig/src/adapter/codex/session.zig:1237`). Claude Code, ACP, Hermes, OpenCode
and Pi are degraded, because each harness settles a cancel asynchronously.
DeepSeek is unavailable (`zig/src/adapter/deepseek/adapter.zig:23`).

**Status.** Implemented.

**Spec revision.** No.

### 5. Admission receipts and stable run and submission identity

**Spec.** Decision 0002: a submit is answered with an admission carrying
`submission_id`, the `run_id` when admitted, `message_ids` and the effective
delivery (`decisions/0002-admission-before-start.md:129`;
`schema/v0.1/session.schema.json:32`).

**Implementation.** Every adapter answers it, and the validator holds them to
it. The ids are minted in process.

**Status.** Partial. Receipts are implemented. A run or submission id is
stable for the life of a hub process; after a restart a reopened session
starts new runs, and nothing maps an old run id to anything.

**Spec revision.** No for the receipt. Yes for identity that survives a
restart, which belongs with row 1's persistence decision.

### 6. Negotiated capabilities, including `capabilities.updated`

**Spec.** `capabilities.request` and `capabilities.response`, and
`capabilities.updated` (`schema/v0.1/envelope.schema.json:102`).

**Implementation.** Request and response are served by every adapter, with a
capability revision checked on every optional feature. Nothing in
`zig/src` emits `capabilities.updated`. The oapx loop says so outright: a
catalog refresh "moves it at the session's next model switch under the same
revision, with no capabilities.updated" (`zig/src/adapter/oapx/adapter.zig:55`).

**Status.** Partial.

**Spec revision.** No. The envelope is specified; emitting it when the
catalog moves is implementation work.

### 7. queue, steer and btw delivery

**Spec.** `queue` by Decision 0007 and `steer` by Decision 0013. `btw` is in
the delivery enum (`schema/v0.1/session.schema.json:7`) and the conformance
draft (`drafts/conformance.md:38`), but its unit is not decided
(`decisions/0007-queue-delivery.md:690`).

**Implementation.** oapx's own loop: `queue` and `steer` native
(`zig/src/adapter/oapx/adapter.zig:42`, `:43`). Harnesses: OpenCode `queue`
native (`zig/src/adapter/opencode/adapter.zig:34`), Pi `steer` emulated
(`zig/src/adapter/pi/adapter.zig:66`). The rest refuse both. No adapter takes
`btw`; OpenCode's refusal is pinned
(`zig/src/adapter/opencode/session.zig:1376`).

**Status.** Partial.

**Spec revision.** No for queue and steer. `btw` needs its unit decided first.

## Tools

### 8. Control-layer-provided tools (`action.tools.provide`, Decision 0011)

**Spec.** `decisions/0011-control-layer-provided-tools.md:81` (provided at
open) and `:139` (a call is an interaction).

**Implementation.** oapx's own loop: native, up to 64 tools
(`zig/src/adapter/oapx/adapter.zig:54`). The hub routes the resolution to the
session (`zig/src/hub/hub.zig:1197`). No harness adapter advertises it, so every one
refuses a provided tool at open, as Decision 0011 requires.

**Status.** Implemented on oapx's own loop, which is where HyperNeo needs it.

**Spec revision.** No.

### 12. Tool sources changed without a hub restart

**Spec.** Decision 0008 describes a source and gates the catalog. A source
can be attached on the wire at open, within the daemon's trust checks
(`drafts/hub.md:185`, `:194`, `:198`). The capability declares the modes it
accepts; no adapter declares `session_live` for it.

**Implementation.** The operator's sources come from the registry document,
read at start. Of the adapters, only ACP attaches a source at open
(`zig/src/adapter/acp/adapter.zig:117`). oapx's own loop refuses an open that
carries one (`zig/src/adapter/oapx/adapter.zig:186`), so its tools are its
workspace tools plus provided tools (row 8).

**Status.** Partial. On ACP a new session can attach a source without a
restart. On oapx's own loop no source can be attached at all. No open session
can change its sources, and changing the operator's list needs a restart.

**Spec revision.** Yes, for a live change to an open session.

## Sessions and identity

### 9. `session.list`

**Spec.** Decision 0046: the list is the host's bindings, paged newest first
on an opaque cursor (`decisions/0046-a-session-list-is-the-hosts-bindings.md:50`,
`:76`; `schema/v0.1/envelope.schema.json:118`).

**Implementation.** The hub's `history` op (`zig/src/hub/stdio.zig:1042`;
`drafts/hub.md:1398`) answers it from the session history, and `sessions`
lists live sessions (`zig/src/hub/stdio.zig:1009`). `work.list` adds each
harness's own sessions where the adapter can list them (`drafts/hub.md:483`).

**Status.** Implemented.

**Spec revision.** No.

### 10. Endpoint and participant identity

**Spec.** `protocol.initialize.response` declares the endpoint id, and every
agent `requested_by` must be it. An open names its participant.

**Implementation.** Every adapter names its endpoint, for example oapx's own
loop (`zig/src/adapter/oapx/adapter.zig:69`). The hub, though, is
one participant: "The daemon is participant `user`" (`drafts/hub.md:220`).

**Status.** Partial. Endpoint identity is implemented. Participant identity is
one fixed value for everything the hub serves.

**Spec revision.** Yes, if the hub is to tell participants apart.

### 11. Multi-connection session ownership

**Spec.** None. The hub is a single-user local service with no auth
(`drafts/hub.md:59`). Fan-out to several subscribers is specified and pinned
(`drafts/hub.md:1670`), but who owns a session among several connections is
not.

**Implementation.** Any connection may address any session. Several may
subscribe to one run.

**Status.** Missing.

**Spec revision.** Yes: an ownership or lease rule, together with row 10.

### 13. Per-session environment and credential grants

**Spec.** An adapter entry's `environment` is an allowlist fixed in the
registry document (`drafts/hub.md:70`). A wire tool-source attachment may
extend its own source's list by name only (`drafts/hub.md:194`, `:198`).
Authentication is an obligation, not a credential channel (Decision 0025).

**Implementation.** As specified: per adapter entry, not per session. A
binding record holds no credential (`drafts/hub.md:72`).

**Status.** Missing as a per-session grant.

**Spec revision.** Yes. A grant would carry a credential reference over the
wire, which Decision 0025 currently rules out.

## Harness coverage

HyperNeo's provider-by-harness matrix: GLM, Kimi and DeepSeek default to
oapx's own loop, with other harnesses optional; Claude stays on the Claude
Agent SDK and does not go through OAP; Codex, Copilot and OpenCode go through
their native CLI or server; Devin goes through ACP. Priority follows that
matrix.

Levels are the adapter's own feature table. "Model switch" is
`session.model.switch`.

| Harness | Adapter | Sessions and resume | Tools | Permissions | Model switch | Missing | HyperNeo priority |
|---|---|---|---|---|---|---|---|
| oapx's own loop | `zig/src/adapter/oapx/adapter.zig:169`, served at `zig/src/tools/makai.zig:322` | live only under `serve`; reopen needs the history loader `serve` does not wire (`:64`, `:180`) | native, plus `action.tools.provide` (`:51`, `:54`); no tool sources (`:186`) | native (`:44`) | native (`:56`) | transcript persistence and reopen under `serve` (rows 1 and 2); tool sources (row 12); `capabilities.updated` (row 6) | **P0** |
| Codex app-server | `zig/src/adapter/codex/adapter.zig:129` | reopen native (`zig/src/adapter/codex/session.zig:1251`); native list and read | degraded, observed families (`:1233`) | native (`:1232`) | none | model switch; models list | P1 |
| Copilot CLI (headless server) | none; the reducer is `zig/src/adapter/copilot/session.zig`, tools and permissions in review (#1166) | not wired | not wired | not wired | not wired; the native `session.model.switchTo` exists | the adapter, its catalog entry, corpus and `--backend` wiring | P1 |
| OpenCode server | `zig/src/adapter/opencode/adapter.zig:146` | reopen native (`:38`); native list and read | native (`:21`) | native (`:20`) | none; models list degraded (`:24`) | model switch | P1 |
| ACP (Devin) | `zig/src/adapter/acp/adapter.zig:160` | reopen native (`:103`); native list and read | degraded, observed only (`:116`) | native (`:119`) | in review (#1163); models list degraded (`:105`) | model switch (#1163); permissions answered at cancel (#1168) | P1 |
| Pi | `zig/src/adapter/pi/adapter.zig:131` | reopen native (`:69`); native list and read | degraded (`:52`) | unavailable (`:51`) | none | permissions, model switch, models list | P2 |
| DeepSeek's own harness | `zig/src/adapter/deepseek/adapter.zig:106` | no reopen (`:35`) | degraded (`:19`) | unavailable (`:18`) | none | reopen, cancel (`:23`), permissions, model switch | P3 (oapx's loop is the default for DeepSeek) |
| Claude Code / Agent SDK | `zig/src/adapter/claude/adapter.zig:162` | reopen native (`:27`); native list and read | degraded (`:43`) | native (`:46`) | none | model switch | P3 (HyperNeo keeps Claude on the Agent SDK, outside OAP) |

## What to do first

1. Wire the oapx loop's transcript store and history loader into `serve`, so a
   served oapx session survives a hub restart and reopens (rows 1 and 2). No
   spec revision.
2. Decide `transcript-load` (row 1), answering Decision 0012's four questions,
   so a control layer can read an OAP session's history in OAP's vocabulary.
3. Decide whether replay may span a restart (row 3), and with it the identity
   of runs across one (row 5).
4. Land the Copilot adapter and the model-switch gaps on the P1 harnesses.

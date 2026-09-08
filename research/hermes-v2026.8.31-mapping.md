# Hermes agent v2026.8.31 mapping ledger

Status: pinned evidence boundary for the seventh production OAP adapter
candidate. This is an implementation input, not an interoperability claim.
No adapter code exists yet.

## Provenance

- Repository: `https://github.com/NousResearch/hermes-agent`
- Release: `v2026.8.31`
- Release commit: `29112bef099274229cadff79cdff7bf7b99c4b77`
- Release commit tree: `daaffc303ae437041b7f76be17c5f61b14f2ce99`

Reproduce the pin:

```sh
git clone https://github.com/NousResearch/hermes-agent.git hermes
git -C hermes checkout 29112bef099274229cadff79cdff7bf7b99c4b77
git -C hermes rev-parse HEAD 'HEAD^{tree}'  # 29112be..., daaffc3...
git -C hermes describe --tags               # v2026.8.31
```

A newer HEAD (`03f3b09222b8f03becb203a6ebb9bac1f927b8b6`) was inspected
during earlier research; it is design signal only. Only the release commit
above is normative for this ledger.

Normative inspected sources:

- `tui_gateway/entry.py`, `transport.py`, `ws.py`, `server.py` — process
  boundary, transport abstraction, JSON-RPC dispatch, response envelopes
- `tui_gateway/event_replay.py` — per-session sequencing and bounded replay
- `tui_gateway/methods_prompt.py`, `methods_session.py`,
  `methods_config.py`, `methods_groups.py` — RPC method inventory
- `gateway/` — the multi-agent gateway (hosted rooms, relay, delivery
  ledger), out of initial adapter scope

## Boundary selection

The adapter boundary is the **tui_gateway JSON-RPC server**:

- stdio (`tui_gateway.entry`, the `hermes --tui` backend): newline-delimited
  JSON-RPC 2.0 in both directions;
- WebSocket (`tui_gateway/ws.py`): byte-identical wire protocol —
  "every RPC method, every slash command, every approval/clarify/sudo flow,
  and every agent event flows through the same handlers whether the client
  is Ink over stdio or an iOS / web client over WebSocket."

Both mounts share `server.dispatch` verbatim, so one adapter covers both;
stdio is the hermetic-test boundary. The FastAPI dashboard (`/api/pty`,
`/api/events`), gateway relay, hosted rooms, and desktop app are out of
scope initially.

## Wire protocol

- **Requests:** `{"jsonrpc":"2.0","id":rid,"method":...,"params":...}` one
  per line; responses `{"jsonrpc":"2.0","id":rid,"result":...}` or
  `{"jsonrpc":"2.0","id":rid,"error":{"code":...,"message":...}}`
  (`_ok`/`_err` in `server.py`; JSON-RPC parse codes like `-32602` plus
  application 5xxx codes).
- **Ready frame:** the server emits a `gateway.ready` event immediately
  after connection accept, carrying the replay epoch — a Makai-like ready
  handshake.
- **Events:** `{"jsonrpc":"2.0","method":"event","params":{ type,
  session_id, seq, payload }}`, stamped with a **per-session monotonic
  `seq`** at the single `write_json` choke point under one lock, so seq
  order matches frame order exactly.
- **Streaming coalescing:** `message.delta`, `reasoning.delta`,
  `thinking.delta` frames are buffered and flushed at ~33 ms (~30 fps);
  every non-streaming frame flushes the buffer ahead of itself, so ordering
  between control/tool/approval frames and deltas is preserved. A
  high-frequency-only coalescing set is enforced in code.

## Sequencing and replay

`tui_gateway/event_replay.py` is the second native replay contract found
(after OpenCode), with honestly-bounded semantics:

- ring buffer of 512 events per session, 64 sessions, oldest-session FIFO
  eviction;
- `session.events.since { session_id, last_seen }` returns
  `{ events, latest_seq, truncated, count, epoch }`;
- `truncated: true` when the requested watermark is older than the retained
  window — the client must refetch history rather than accept a silent gap;
- an opaque per-process `epoch` (random at startup) is carried on
  `gateway.ready` and on every replay response, because seq counters are
  in-process and a gateway restart silently resets them; clients detect
  epoch mismatch and reset watermarks.

This maps directly onto OAP's degraded-replay-with-explicit-gaps model —
including the epoch/restart hazard, which OAP should consider modeling
first-class (restart invalidates cursors).

## RPC surface (verified names)

- **Prompting:** `prompt.submit` (with `truncate_before_user_ordinal`
  truncation, barge-in latch), `prompt.btw`, `prompt.background`,
  `session.steer`, `subagent.steer`, `subagent.interrupt`,
  `session.interrupt`
- **Interactions (reverse channels):** `approval.pending`,
  `approval.received`, `approval.respond`, `clarify.respond`,
  `sudo.respond`, `secret.respond`, `mcp.setup.respond`,
  `preview.act.respond`, `preview.read.respond`, `terminal.read.respond`,
  `window.read.respond`, `tour.respond`
- **Sessions:** `session.create/activate/resume/branch/undo/close/delete/
  list/active_list/most_recent/status/history/save/redirect/title/
  set_hidden/compress/context_breakdown/usage/cwd.set/workspace.move`,
  `session.events.since`, `session.events.stats`
- **Models/profiles/auth:** `model.options`, `model.save_key`,
  `model.disconnect`, `profiles.*`, `auth.json`
- **Groups (multi-agent):** `groups.create/send/approve/promote/demote/
  disband/replicate/stop/retry/state/capabilities/peer.*`
- **Delegation/handoff:** `handoff.request/fail/state`,
  `delegation.pause/status`
- **Attachments:** `clipboard.paste`, `file.attach`, `image.attach*`,
  `pdf.attach`, `image.detach`, `input.detect_drop`
- **Billing/subscription, pets, browser controller, completion, config,
  diagnostics, project trees, spawn trees:** present, out of initial scope

`prompt.btw` is notable: Hermes is the only pinned harness with a native
by-the-way delivery primitive, matching OAP's optional `btw` delivery mode.

## Identity domains

| Native identity | OAP identity | Rule |
|---|---|---|
| gateway process | endpoint | Adapter allocates; epoch identifies the process's seq numbering. |
| `session_id` (params / event) | `session_id` association | Correlation identity. |
| JSON-RPC `id` (rid) | private request correlation | Never an OAP identity. |
| accepted `prompt.submit` | `submission_id` | Adapter allocates. |
| one turn (to settlement frame) | `run_id` | Adapter allocates. |
| event `seq` | native per-session ordering evidence | Per-session, not per-run; restart-reset; epoch-guarded. |
| event frame identity | private | Reconstructed on replay; no stable frame id. |
| approval/clarify/... request ids | interaction identities | Reverse-channel correlation. |
| `subagent.*` targets | child task identities | Distinct steer/interrupt surface per child. |
| row ids / ordinals (history) | transcript navigation | `truncate_before_user_ordinal` uses user-turn ordinals. |

## Lifecycle mapping

| Native observation | OAP meaning | Fidelity | Initial support | Required fixture |
|---|---|---|---|---|
| `gateway.ready` (with epoch) | initialize/ready response | native | emulated descriptor | `initialize-ready` |
| `prompt.submit` result | admission | normalized | emulated | `message-admitted` |
| `prompt.btw` / `prompt.background` | btw / background deliveries | native | degraded | `btw-delivery`, `background-prompt` |
| `session.steer` / `subagent.steer` | steer delivery (run / child scoped) | native | degraded | `steer-run`, `steer-subagent` |
| `message.delta` (coalesced) | `content.delta` | normalized | native with documented coalescing | `streaming-deltas` |
| `reasoning.delta` / `thinking.delta` | reasoning deltas | normalized | degraded | `reasoning-deltas` |
| tool frames | action lifecycle | normalized | degraded until mapped | `tool-lifecycle` |
| `approval.*` round trip | permission interaction | native | degraded pending fixture | `approval-gate` |
| `clarify.respond` / `sudo.respond` / `secret.respond` | typed interactions | native | degraded | `clarify-gate` |
| `session.interrupt` | cancellation intent | normalized | degraded | `interrupt` |
| `subagent.interrupt` | child-targeted cancel | native | degraded | `subagent-interrupt` |
| `turn.error` / settlement frames | run terminal via one arbiter | normalized | degraded | `turn-error`, `completed-turn` |
| `session.events.since` (full window) | replay contiguous suffix | native | degraded (bounded window) | `replay-in-window` |
| `session.events.since` (`truncated:true`) | explicit replay gap | native | degraded | `replay-truncated` |
| epoch change across reconnect | restart invalidation | normalized | explicit reconciliation rule | `epoch-restart` |
| `session.resume` / `session.branch` / `session.undo` | recovery family | native inputs | reclassified separately | `resume`, `branch`, `undo` |
| `session.history` | transcript reconstruction | normalized | degraded | `history` |
| `session.compress` | compaction control | observed-only | no core claim | `compress` |
| `session.status` / `session.usage` | reconciliation data | normalized | emulated | `reconcile-state` |
| transport death before settlement | one `run.failed` | synthesized | transport failure handling | `process-exit` |
| malformed JSON-RPC line | typed protocol error | native | native | `malformed-frame` |

### Terminal arbitration

Settlement evidence arrives as turn-scoped event frames (including
`turn.error` on the failure path) through the same ordered seq stream. One
reducer must own the terminal decision exactly as in the other adapters;
the coalescing rule guarantees control frames are never reordered behind
deltas, so seq order is trustworthy for arbitration.

## P0 mismatches

1. **Seq is per-session and restart-reset:** OAP per-run sequence is
   adapter-owned; the epoch mechanism is the native honesty marker and
   deserves an OAP-level analog decision.
2. **Replay is bounded (512/64) with explicit truncation:** matches OAP's
   degraded replay; the `truncated` flag is the gap contract.
3. **Interaction vocabulary is large and typed** (approval, clarify, sudo,
   secret, MCP setup, preview, terminal, window): OAP's single interaction
   model needs a mapping decision per kind, not one blanket "permission".
4. **`btw` is native here and nowhere else:** OAP's optional delivery mode
   has exactly one native implementer; fixtures must pin its semantics.
5. **Child-scoped steer/interrupt exists natively** (`subagent.*`): OAP
   child-action semantics can be exercised, not just emulated.
6. **Admission response is thin:** the `prompt.submit` result does not
   carry a durable submission identity; adapter allocates and correlates
   via subsequent event frames.
7. **Coalescing is display-motivated but order-preserving:** the adapter
   must document that delta granularity is ~30 fps batches, not raw tokens.
8. **Multi-agent groups/handoff/delegation exceed core OAP:** extension
   decision required before any claim.
9. **No capability negotiation:** descriptor synthesized; ready frame plus
   `groups.capabilities` are the nearest native truth sources.

## Initial capabilities

- initialize/ready: `native` frame, `emulated` descriptor
- session association: `emulated`
- admission: `emulated`
- run identity/sequence: `emulated`
- text/reasoning streaming: `native` (coalesced)
- steer (run and child): `native` candidate pending fixtures
- btw delivery: `native` candidate (unique among pinned harnesses)
- background prompts: `native` candidate
- interactions: `native` candidates (approval/clarify families)
- cancellation: `degraded` (intent vs settlement split)
- replay: `degraded` native (bounded window, explicit truncation, epoch)
- reconciliation: `emulated` (`session.status`, epoch)
- resume/branch/undo: reclassified recovery inputs, `degraded`
- compaction control: observed-only
- groups/handoff/delegation/billing/pets: `unavailable` initially

## Evidence corpus plan

`fixtures/adapters/hermes-v2026.8.31/`, standard five-file cases:
`initialize-ready`, `message-admitted`, `completed-turn`, `turn-error`,
`streaming-deltas`, `reasoning-deltas`, `tool-lifecycle`,
`approval-gate`, `clarify-gate`, `sudo-gate`, `btw-delivery`,
`background-prompt`, `steer-run`, `steer-subagent`, `subagent-interrupt`,
`interrupt`, `replay-in-window`, `replay-truncated`, `epoch-restart`,
`resume`, `branch`, `undo`, `history`, `compress`, `reconcile-state`,
`process-exit`, `malformed-frame`.

A gated live-process test against `tui_gateway.entry` over stdio with a
hermetic provider follows the established pattern.

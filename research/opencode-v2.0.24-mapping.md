# OpenCode v2.0.24 mapping ledger

Status: research for a pin move from v1.18.34. v2 is a new major line with a
new served API, so unlike the v1.18.x moves this is not a delta ledger: every
route and event the adapter reads changes name, and the stream it reads
changes shape. The v1 ledgers stay normative for the v1 pins only. Neither
tree implements this yet; this ledger is the spec the port is written to.

## Provenance

- Repository: `https://github.com/anomalyco/opencode`
- Release: `v2.0.24`, released 2026-10-06
- Commit: `e7a34f09bfd9134dfade5a8ddb843f7030bc9a69`
- Commit tree: `aef0ff8dce5435b3649bdd1fa6feb35015e2b379`

```sh
git clone https://github.com/anomalyco/opencode.git
git -C opencode checkout e7a34f09bfd9134dfade5a8ddb843f7030bc9a69
git -C opencode rev-parse HEAD 'HEAD^{tree}'  # e7a34f0..., aef0ff8...
git -C opencode describe --tags               # v2.0.24
```

v2 publishes no GitHub release binaries. The artifacts come from
`https://opencode.ai/files/bin/2.0.24/<name>`; the darwin-arm64 binary
reports `opencode v2.0.24` and is byte-identical to the Homebrew
`opencode-v2` 2.0.24 build:

| Platform | Kind | Name | SHA-256 | Bytes |
| --- | --- | --- | --- | --- |
| linux-x64 | archive | `opencode-linux-x64.tar.gz` | `8f266b3043f96d6077fc0459bdde72e4199bf88c27e6af573047df8ae3853345` | 90266510 |
| linux-x64 | binary | `opencode` | `d73436b88f2c412abb4e72056c2f6d133d9651f121952ade7fc3fe95fc9f5ddc` | 203671008 |
| darwin-arm64 | archive | `opencode-darwin-arm64.zip` | `64036a08d34959638a67e09137add101259abb4c8f50d2bf75bc7112ff8c8042` | 77410329 |
| darwin-arm64 | binary | `opencode` | `e68cc32cb37b1f3242991c668be3934396825778925cb85670dad61038b05fe7` | 179357360 |

Source blobs this ledger reads:

| Path | Blob |
| --- | --- |
| `packages/schema/src/session-event.ts` | `0cffae27ad631740c4b7b59c09c01440b4b603a6` |
| `packages/schema/src/event.ts` | `db0c8c55735757640a7be6279b878b43bae76908` |
| `packages/schema/src/session-inbox.ts` | `361c0c492ce81aaea18fa31d36d3b00dc8d13c7b` |
| `packages/schema/src/permission.ts` | `e978c9463292bed65d427536ca4e2b4549664aec` |
| `packages/protocol/src/groups/session.ts` | `d5bf2af1db1281785aa996ed0d63c5f61821d830` |
| `packages/protocol/src/groups/event.ts` | `f067ecfe39671513c954bfe46ed4e3f6a658515d` |
| `packages/core/src/session/execution.ts` | `eeed241ae14e30645d4e0904c69d3ebfe919ef38` |
| `packages/core/src/bus.ts` | `30bf10a31363954c800f352ad844416aa9e0dfd1` |
| `packages/server/src/routes.ts` | `c4f746f1d82bfb6e86c8b43221a7c4e63a6b8343` |
| `packages/cli/src/server-process.ts` | `48c036c48bbb2e04d3a3ded26cd2a178b6f26da0` |

## Live probe

2026-10-06, the darwin-arm64 binary above, `opencode serve --hostname
127.0.0.1 --port <p>` with `env -i`, an empty `HOME`, `OPENCODE_PASSWORD` set,
and no credentials or config:

- Every route answers `401` without `Authorization: Basic` for user
  `opencode` and the password. `OPENCODE_SERVER_PASSWORD` is still read as a
  legacy name.
- `GET /api/info` → `{"version":"2.0.24","pid":…,"urls":[…],"paths":{…},
  "capabilities":{…}}`. `/api/health` is gone; this is the readiness probe.
- `POST /api/session {"location":{"directory":<abs>}}` → `{"data":{id,
  projectID, cost, tokens, time, location:{directory}}}`. A session is created
  with no model; `model` is optional in the request.
- `GET /api/session/active` → `{"data":{}}` idle, and
  `{"data":{"<id>":{"type":"running"}}}` while an execution runs.
- `POST /api/session/<id>/interrupt` → `200 {"interrupted":true}` during an
  execution and `200 {"interrupted":false}` idle (v1 answered `204`).
- `POST /api/session/<id>/prompt {"id","text"[,"delivery"]}` → `200
  {"data":{id, sessionID, time:{created}, type:"user", payload:{text},
  delivery}}`. The server keeps the supplied `msg_…` id. A prompt without
  `delivery` is admitted as `steer`.
- `GET /api/session/<id>/inbox` lists inputs admitted but not yet delivered.
- `GET /api/session/<id>/permission` → `{"data":[]}`.

**No credentials is not offline.** The empty-`HOME` server ran every prompt
against OpenCode's hosted free model (`{"id":"exo-free","providerID":
"opencode"}`) and answered. A gate that starts this binary and prompts it
reaches the network whether or not anything is configured, so a gate must
configure an unreachable provider explicitly, or not prompt.

## The durable log is empty under `opencode serve`

`GET /api/experimental/session/:id/log?after=<seq>&follow=<bool>` is
documented as the durable per-session log: replay after an exclusive cursor,
one `{"type":"log.synced","aggregateID","seq"}` marker at the watermark, then
live events when `follow=true`. It replaces v1's `/history` page and
`/event` stream.

Under `opencode serve` it carries nothing but the marker. `Bus.configured`
(`core/src/bus.ts`) defaults `persist` to `false`, and with it off no durable
payload row is written; `server/src/routes.ts` passes
`options.events?.persist`, and `cli/src/server-process.ts` never sets it (only
`server/src/workerd.ts` does). Live: `/log?follow=true`, opened
before a prompt, held open through the whole execution and delivered nothing
after its marker, and once the execution had settled, `/log?after=0` and
`/log?after=5` each answered only `log.synced` at seq 12. Without `after` the
read starts at the watermark.

So the adapter cannot use `/log`, and nothing on the served API replays events.

## Event stream

`GET /api/event` is the one stream: global across sessions and locations,
documented "volatile by contract: a slow consumer overflows and fails the
stream, and events during disconnection are missed." Headers and a
`server.connected` event arrive at once, then a `: heartbeat` comment
periodically. Each frame is one `data:` line:

```json
{"id":"evt_…","created":<ms>,"type":"session.step.ended",
 "location":{"directory":…},"data":{"sessionID":…,…},
 "durable":{"aggregateID":<sessionID>,"seq":9,"version":1}}
```

- The adapter filters on `data.sessionID`; events of other sessions are
  dropped before reduction, not counted as foreign.
- `durable` is present on durable types and absent on ephemeral ones
  (`session.text.delta`, `session.reasoning.delta`, `session.tool.input.delta`,
  `session.tool.progress`, `session.compaction.delta`, `session.usage.updated`,
  `permission.*`, `form.*`).
- `seq` is per session and increasing, but **not contiguous on this stream**:
  internal durable types (`session.usage.recorded`,
  `session.message.content.updated`) take a seq and are not published here.
  Live, seq 11 was skipped. A gap is therefore not evidence of loss.

Because the stream does not replay, loss is detected by disconnection rather
than by seq: when `/api/event` ends or fails while a run is open, the adapter
reconciles from `GET /api/session/active` and the session's messages and
inbox, and reports a gap rather than inventing continuity (the `ReplayGap`
rule).

## Run boundaries

v2 publishes explicit execution boundaries, which v1 lacked:
`session.execution.started`, then exactly one of `session.execution.succeeded`,
`session.execution.failed {error}`, or `session.execution.interrupted
{reason: user|shutdown|superseded|inactivity}`. But an execution is a **busy
period**, not a turn: `execution.ts` reports "one terminal observation per busy
period, covering every coalesced drain". Live, a prompt plus a second prompt
admitted `"delivery":"queue"` during it produced one `execution.started` (14),
`inbox.delivered` for the first (15), its step, `inbox.delivered` for the
second (22), its step, and one `execution.succeeded` (28).

So one OAP run is one admitted input, delimited by deliveries:

| Native | OAP |
| --- | --- |
| `POST …/prompt` → 200 | admission; `delivery:"queue"` when a run is open, else `steer` |
| `session.inbox.enqueued` (our `inboxID`) | observed; the response already carried it |
| `session.inbox.delivered` (our `inboxID`, first in the execution or after a `queue` input) | `run.started` |
| `session.inbox.delivered` of a later `queue` input | settles the previous run `completed` |
| `session.inbox.delivered` of a `steer` input during a run | joins the open run (Decision 0002 steer) |
| `session.execution.succeeded` | settles the open run `completed` |
| `session.execution.failed` | settles the open run `failed` |
| `session.execution.interrupted` after an accepted cancel | `run.cancelled` |
| `session.execution.interrupted` otherwise | `run.failed` naming the reason |
| `session.inbox.cancelled` (our `inboxID`) before delivery | the queued run settles `cancelled` |
| `session.step.started` / `step.ended` / `step.failed` | step bookkeeping; `finish` is the stop reason |
| `session.text.started` / `delta` / `ended` | assistant text; `ended.text` is authoritative |
| `session.reasoning.*` | reasoning, same shape as text |
| `session.tool.input.*`, `tool.called`, `tool.success`, `tool.failed` | the tool lifecycle |
| `session.usage.updated` | ephemeral session totals; the step's `tokens` are the run's usage |

`inbox.delivered` carries only `{sessionID, inboxID}`; the delivery mode is
the one the adapter chose at admission (and `session.inbox.delivery.changed`
if anything moved it), so the adapter keeps the mode per `inboxID`.

Live interrupt: an execution interrupted mid-step publishes
`session.step.failed {error:{type:"aborted",message:"Step interrupted"}}` and
then `session.execution.interrupted {reason:"user"}`; the step failure is part
of the cancellation, not a run failure.

`session.instructions.updated`, `session.renamed`, `project.updated`,
`session.step.streamed` and `session.agent.selected`/`model.selected` are
observed-only.

## Renames from v1

| v1.18.34 | v2.0.24 |
| --- | --- |
| `GET /api/health` | `GET /api/info` |
| `GET /api/session/:id/event` (per session, replays from seq 1) | `GET /api/event` (global, no replay) |
| `GET /api/session/:id/history?after&limit` | none served (`/log` is empty) |
| `POST /api/session/:id/wait` | `POST /api/experimental/session/:id/wait` (no longer needed: executions settle in the stream) |
| `POST /api/session/:id/interrupt` → 204, `continue` | → 200 `{interrupted}`, `resume` |
| `session.next.prompt.admitted` | `session.inbox.enqueued` |
| `session.next.prompted` | `session.inbox.delivered` |
| `session.next.<x>` (step, text, reasoning, tool, shell, compaction, revert, moved, synthetic, retried) | `session.<x>`; `retried` is `session.retry.scheduled` |
| `session.next.model.switched` / `agent.switched` | `session.model.selected` / `session.agent.selected` |
| permission reply field `reply` | `decision` |

## Capabilities

What the port can claim, given the above:

- `run.streaming`: `native` — text and reasoning deltas are on `/api/event`.
- `session.open.reopen`: `native` for an idle session. `GET /api/session/:id`
  answers from a restarted server; with no replay, the reopened transcript
  cursor is the session's message list, not an event seq.
- Approvals stay out of scope, as in v1: `permission.asked` is ephemeral on
  `/api/event`, and `GET /api/session/:id/permission` lists the pending ones, so
  a later unit can serve them with reconciliation on reconnect.
- Native list for `work.list`: `GET /api/session` (with `location.directory`).

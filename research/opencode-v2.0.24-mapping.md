# OpenCode v2.0.24 mapping ledger

Status: the current pin, moved from v1.18.34. v2 is a new major line with a
new served API, so unlike the v1.18.x moves this is not a delta ledger: every
route and event the adapter reads changes name, and the stream it reads
changes shape. The v1 ledgers stay normative for the v1 pins only. Both trees
implement it; "What the adapters do" records how, and where they depart from
the mapping above it.

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
  `delivery` is admitted as `steer`. The id is unique across the server's
  whole store, not per session: reusing one, even in a new session, is
  `409 ConflictError`. So each adapter mints `msg_oap<16 hex>`, a random
  nonce per adapter, then a 16-digit counter. Before that the id was the
  counter alone, so a runtime restarted against a long-lived server had every
  prompt refused.
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
than by seq. Since revision `oap-v3`, both trees reconcile from the session's
record instead of failing every open run:

- When `/api/event` ends, the adapter subscribes again (since revision
  `oap-v5`, retrying as below; before it, once) and waits for
  `server.connected`. A subscribe the server refuses, a stream that ends before
  greeting, and a record or `active` read that fails are retried with backoff
  (100 ms doubling to 2 s) until the request timeout has passed since the loss,
  which covers a server restart. Events that arrive on the new stream are held
  until the reconciliation below has been reduced.
- It then reads `GET /api/session/<id>/message?order=desc` back to the oldest
  open run's input, up to 16 pages of 200. The record carries what the stream
  missed: `user` rows for delivered inputs, `assistant` rows whose `content`
  holds each ended text and reasoning part (text is written at `text.ended`,
  reasoning carries `time.completed`) and each tool call as a `tool` part
  (`id`, `name`, `executed`, and a `state` whose `status` is `pending`,
  `running`, `completed` or `error`, with `input`, and `content` or `error`
  once it ended), with `finish`, `error`, `cost` and
  `tokens` once the step completed, and an `idle` row with `outcome`
  `succeeded`, `failed` or `interrupted` when the execution ended. A shutdown
  interruption writes no `idle` row.
- The record becomes the native events it projects (`inbox.delivered`,
  `text.ended`, `reasoning.ended`, `tool.input.started` and `tool.called` for
  a tool part that is `running` or ended, `tool.success` or `tool.failed` for
  one that ended, `step.ended`, `step.failed`, `execution.*`), in the order
  the parts are stored, and they go through the same reducer. A `pending`
  tool part has not been called yet and is left to the live stream. A part's ordinal is its
  index among parts of its kind in that assistant row, which is how the runner
  allocates them. A part, step, tool call start or end, or delivery the
  reducer has already seen live is not applied twice, and a live repeat of one
  the record supplied is dropped, as is live progress for a call the record
  ended.
- If the newest turn has no `idle` row, `GET /api/session/active` decides: a
  running session is followed on the new stream, and one that is no longer
  running fails with `opencode_execution_interrupted`.
- Only what cannot be reconciled still fails, with `opencode_stream_failed`:
  a server that still refuses the stream or the record when the request
  timeout runs out, or a record that does not hold an open run's input, which
  is not retried. A queued input the record does not show yet stays queued.

What it does not recover: deltas and tool progress sent during the gap (the
ended text and the call's result carry them), and a race in which an execution
ends between the resubscribe and
the record read, whose `execution.*` event can then arrive on the new stream
after the record has already settled that turn.

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
| `session.text.started` / `delta` / `ended` | assistant text: each delta streams, and `ended.text` is authoritative for the final message |
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

## What the adapters do

Both trees, under capability revision `opencode-v2.0.24-oap-v5`:

- **Auth.** `opencode serve` always requires basic auth: without
  `OPENCODE_PASSWORD` it generates a password and prints it. The registry entry
  passes the password through its `environment` allowlist
  (`OPENCODE_PASSWORD`, or the legacy `OPENCODE_SERVER_PASSWORD`), user
  `opencode`; nothing is read from the config file itself.
- **Stream.** `GET /api/event`, filtered to the session's `data.sessionID`.
  The open waits for `server.connected` before it returns, so no prompt can be
  sent before the subscription exists. A frame the codec refuses fails the
  session when it names this session or no session, and is passed over when
  it names another one. Durable `seq` must increase; ephemeral frames carry
  none.
- **Admission.** An `auto` submission on an idle session is sent `steer` and
  answered `started`, as v1 answered it from `promotedSeq`: an idle session
  always starts executing a steer prompt. An `auto` behind an open run is sent
  `queue`, so each OAP run is one turn; an explicit `queue` is answered
  `queued` and starts at its `inbox.delivered`.
- **Settlement.** As the table above: `inbox.delivered` starts a run, and the
  run settles at `session.execution.*` or when the next input is delivered,
  including another client's input, whose turn is then not attributed to this
  session's run. A `step.failed` followed by `execution.failed` settles
  `opencode_step_failed` with the step's error; `execution.failed` alone
  settles `opencode_execution_failed`. A run settled with no `step.ended` has
  stop reason `unknown`.
- **Cancel.** A run whose input is not yet delivered is cancelled by
  `DELETE /api/session/:id/inbox/:inboxID` and settles at
  `session.inbox.cancelled`; a delivered one is interrupted and settles at
  `session.execution.interrupted`. If the delivery wins the race, the run is
  interrupted as soon as it starts.
- **Streaming is `native`** (revision `oap-v2`; `oap-v1` forwarded each part
  whole at its `ended` event). Each `text.delta` and `reasoning.delta` is a
  `content.delta` as it arrives, keyed by `assistantMessageID`, `ordinal` and
  kind. The part's `ended` event goes into the final message whole, and adds
  a `content.delta` only for the text after what the deltas carried: all of
  it when none arrived (they are ephemeral, so a reconnect loses them), and
  none when the deltas are not a prefix of `ended.text`. That last case is a
  mismatch recorded, not compensated: the streamed text and the final message
  then differ, and the final message is the record. An empty delta, or an
  empty remainder, is not forwarded.
- **Reopen** attaches to the session record and follows it from the attach on;
  with no replay there is no stored history to fence, so the transcript
  cursor starts empty and advances with the events seen.
- **Approvals** (revision `oap-v4`). A rule with `effect: "ask"` (config
  `permissions: [{action, resource, effect}]`; the shell tool's action is
  `shell`) makes the runner publish an ephemeral `permission.asked`
  (`packages/schema/src/permission.ts`): `{id: "per_…", sessionID, action,
  resources, save?, metadata?, source?: {type: "tool", messageID, id},
  message?}`, after the gated call's `session.tool.called`. Its scope is the
  `sessionID` in its data, so the stream keeps it for the session although
  its type has no `session.` prefix. Both trees map it to
  `action.permission.requested` for the tool call `source.id` names, titled
  `<action>: <resources>`, with three choices: `once`, `always` (OpenCode saves
  the `save` patterns as a rule) and `reject`. An answer is `POST
  /api/session/:id/permission/:requestID/reply {"decision", "message"?}` →
  `204`, then `permission.replied {requestID, reply}`, and becomes
  `action.permission.resolved` (`resolved` or `rejected`). The OAP `reason`
  travels as `message`: OpenCode feeds it back to the model and the turn
  goes on. A reject with no message fails the tool call `aborted` and ends
  the execution `interrupted` with reason `shutdown` (an interrupt with no
  reason defaults to it, and it writes no `idle` record), which the
  adapters settle as `run.failed` `opencode_permission_declined`. A reply
  the adapter did not send cancels the interaction
  (`opencode_permission_replied_elsewhere`), a run that ends with one open
  cancels it (`run_settled`), and a permission outside a tool call the run
  started fails the run (`opencode_permission_without_tool`), since an OAP
  permission requires a `tool_call_id`. While one is open, `session.state`
  reports the session `waiting_for_input` and lists the open interactions,
  in the order they were asked, as the started run's
  `pending_interactions`. Live against the pinned binary, both
  trees produced the same trace for `once` (the call completes and the run
  ends `run.completed`) and for `reject` (the call fails and the run ends
  `opencode_permission_declined`); the Go gates
  `TestOpenCodeServerAsksPermissionForAToolCallAndRunsItOnceAllowed` and
  `TestOpenCodeServerEndsTheRunAsDeclinedWhenThePermissionIsRejected` pin
  both. A permission pending while the event stream is down is not
  re-announced from `GET /api/session/:id/permission`.

Corpus: `fixtures/adapters/opencode-v2.0.24/` carries fourteen of the v1
cases converted to v2 vocabulary; `history-fence` is dropped because v2
serves no history. `completed-text` is the live trace recorded above, with
the session, inbox and location normalized. `interrupt-idle` is now a cancel
before delivery that settles at `session.inbox.cancelled`. The Go goldens in
`go/adapter/opencode/testdata/port-{goldens,scenarios}.json` were
re-recorded; two scenarios that tested v1's quiescence and history polling
are replaced by an execution failure and an interrupt nobody asked for.

Live, against the pinned darwin-arm64 binary (sha256 `e68cc32c…`): the four
`OAP_OPENCODE_INTEGRATION` gates pass three times, among them
`TestOpenCodeServerRunsATurnToCompletionAgainstALocalProvider`, which
completes a real turn against an in-process OpenAI-compatible fake, so it
reaches no network. `oapx serve agent --backend` against the same binary and
fake completes a turn whose trace both validators pass.

## Native session list

`work.list?include_native=true` asks
`GET /api/session?directory=<dir>&limit=<n>&order=desc&parentID=null`
(`session.list` in `packages/protocol/src/groups/session.ts` at the pinned
commit), which answers `{"data":[Session.Info...],"cursor":{"previous"?,"next"?}}`.
Each row is decoded with the same strict `Session.Info` fields a create or a
get answers. `parentID=null` keeps the root sessions, so a forked child is not
listed as its own work. The native id is `id`, the title `title`, the
directory `location.directory`, and the update time `time.updated` in epoch
milliseconds. A session is running when `GET /api/session/active` names it. The
list follows `cursor.next` with `GET /api/session?cursor=<next>&limit=<rows still
needed>` until it has the rows asked, the page is empty or the cursor stops
moving, up to 16 pages. Live, the cursor alone carries the directory, order and
parent filter (it is base64 JSON of them and an anchor), and a `limit` of 500 was
answered unclipped. A fresh session carries no `title`.
Against the pinned darwin-arm64 binary, a session created in a directory came
back from `oapx serve --stdio`'s `work.list` with `include_native` as an idle
native entry under that directory. Both adapters list, with the same query
and the same strict rows; against the same binary, `goap hub --stdio` and
`oapx serve --stdio` answered the same entries.

## Native session read

`work.read` reads a session OpenCode holds through
`GET /api/session/<id>/message` (`session.messages` in
`packages/protocol/src/groups/message.ts` at the pinned commit):
`limit=200&order=asc` for the first page, then `cursor=<cursor.next>&limit=200`
alone, since the contract forbids combining a cursor with `order`, until no
`next` comes back, a cursor repeats, or 16 pages. The answer is
`{"data":[Session.Message.Info...],"cursor":{"previous"?,"next"?}}`, checked
strictly at the top and read leniently below it, since the message union has
eleven types and a read wants two. A `user` message's `text`, trimmed, is a user
turn at `time.created`; the `text` parts of an `assistant` message's `content`,
joined, are a reply, and the last reply before the next user turn is kept, as
Pi's read does. Reasoning, tool parts and every other message type are left
out. Against the pinned darwin-arm64 binary, a turn completed through the Go
adapter (the server gate) read back as the user's `ping` and the provider's
`pong`, three messages over two pages; the Zig adapter's test serves those same
three recorded messages.

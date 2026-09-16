# OpenCode v1.18.29 mapping ledger

Status: pinned evidence boundary for the sixth production OAP adapter
candidate. This is an implementation input, not an interoperability claim.
No adapter code exists yet.

## Provenance

- Repository: `https://github.com/anomalyco/opencode`
- Release: `v1.18.29`
- Commit: `16747470f976aca3d362ad730bcd3fe82ecc2c9a`
- Commit tree: `6d8cc725d9c0945d7259b78e2f60cdec6c493a26`

Reproduce the pin:

```sh
git clone https://github.com/anomalyco/opencode.git
git -C opencode checkout 16747470f976aca3d362ad730bcd3fe82ecc2c9a
git -C opencode rev-parse HEAD 'HEAD^{tree}'  # 1674747..., 6d8cc72...
git -C opencode describe --tags               # v1.18.29
```

Normative inspected sources:

- `packages/protocol/src/groups/*.ts` — the HTTP API surface (18 groups:
  session, message, event, permission, question, agent, model, provider,
  command, skill, fs, pty, credential, integration, reference, location,
  health, project-copy)
- `packages/schema/src/session-input.ts`, `session-delivery.ts` —
  admission and delivery types
- `packages/schema/src/session-event.ts` — the durable session event
  inventory
- `packages/server/src/routes.ts`, `handlers/session.ts` — server wiring
- `packages/cli/src/commands/handlers/serve.ts` — `opencode serve` process
  boundary

## Boundary selection

The adapter boundary is the **`opencode serve` HTTP + SSE server** with basic
auth (`opencode` / daemon password). Routes are generated from an effect
`HttpApi` definition with an OpenAPI document at `/openapi.json`, so the
wire contract is machine-checkable at this pin. The TUI, desktop app, SDK
packages, and plugin runtime are out of scope.

## Wire protocol

REST over HTTP with JSON, plus two SSE streams:

- `POST /api/session` — create session (optional caller-chosen
  `Session.ID`, agent, model, location)
- `POST /api/session/:id/prompt` — payload
  `{ id?, prompt, delivery?, resume? }`; success returns
  `SessionInput.Admitted`; errors include `ConflictError` and
  `SessionNotFoundError`
- `POST /api/session/:id/interrupt` — "Interrupt active execution owned by
  this OpenCode process. Idle interruption is a no-op."
- `POST /api/session/:id/wait` — documented as "wait for a session agent
  loop to become idle", but **not implemented at this pin**: the handler
  resolves the session and then always raises `OperationUnavailableError`,
  returned as 503 `ServiceUnavailableError` ("Session wait is not available
  yet"); a missing session still answers 404. Upstream pins this in its own
  `httpapi-session` test, and the stub is unchanged on its `dev` branch. The
  adapter must not build settlement on this route.
- `GET /api/session/:id/event?after=N` — SSE: "Replay durable events after
  an aggregate sequence, then continue with new durable events."
- `GET /api/session/:id/history?after=N&limit=` — finite pages of public
  durable events after an exclusive aggregate sequence
- `GET /api/session/:id/message(s)` — projected messages, cursor paginated
- `GET /api/session/:id/context` — messages after the last compaction
- `POST /api/session/:id/compact`, `/revert/stage|clear|commit`
- `GET /api/session/active` — running sessions as
  `{ [sessionID]: { type: "running" } }`
- permissions: `GET /api/session/:id/permission` (pending), `GET
  .../permission/:requestID`, `POST .../permission/:requestID/reply`;
  location-scoped `GET /api/permission/request`
- questions: `GET .../question`, `POST .../question/:requestID/reply`,
  `POST .../question/:requestID/reject`
- `GET /api/event` — server-wide SSE event stream
- catalogs: `agent.list`, `model.list`, `provider.list/get`,
  `command.list`, `skill.list`
- `GET /api/session` — session list with opaque cursor pagination

## The admission contract

`SessionInput.Admitted` is the strongest native admission shape of any
pinned harness:

```text
admittedSeq   NonNegativeInt    durable admission order
id            SessionMessage.ID the admitted input's message identity
sessionID     SessionID
prompt        Prompt
delivery      "steer" | "queue"
timeCreated   DateTimeUtcFromMillis
promotedSeq   NonNegativeInt?   set when a queued input is promoted to execution
```

- delivery is optional on the request; the admitted response always carries
  the effective delivery
- `resume` (default true) admits durably and schedules execution; `false`
  admits without scheduling — an explicit admission/execution split
- `ConflictError` is the typed rejection path
- the event stream independently persists `session.next.prompt.admitted`
  and `session.next.prompted`, so admission and execution are separately
  observable

## Durable session events

Event-sourced with per-session aggregate sequence (`aggregate: "sessionID"`,
`version`) — every SSE event carries `{ aggregateID, seq, version }`
durable metadata plus `id`, `timestamp`, and `sessionID`:

`agent.switched`, `model.switched`, `moved`, `prompted`,
`prompt.admitted`, `context.updated`, `synthetic`, `shell.started`,
`shell.ended`, `step.started`, `step.ended`, `step.failed`,
`text.started`, `text.delta`, `text.ended`, `reasoning.started`,
`reasoning.delta`, `reasoning.ended`, `tool.input.started`,
`tool.input.delta`, `tool.input.ended`, `tool.called`, `tool.progress`,
`tool.success`, `tool.failed`, `retried`, `compaction.started`,
`compaction.delta`, `compaction.ended`, `revert.staged`,
`revert.cleared`, `revert.committed` (durable set; deltas are also defined
in the non-durable set).

This is the first pinned boundary with a **native cursor replay contract**
(`after` → replay then continue), a durable aggregate sequence, and an
explicit version — the closest native analog of OAP's per-run sequence and
replay semantics found so far. It can validate OAP replay rather than
emulate it.

## Identity domains

| Native identity | OAP identity | Rule |
|---|---|---|
| OpenCode process (serve instance) | endpoint | Adapter allocates. |
| `Session.ID` | `session_id` | Caller-suppliable at create; association identity. |
| `SessionMessage.ID` (admitted input) | `submission_id` candidate | Native, durable, echoable — strongest correlation key. |
| one admitted-and-promoted execution | `run_id` | Adapter allocates; promotion (`promotedSeq`, `prompted` event) is the execution start signal. |
| `admittedSeq` / aggregate `seq` | native ordering evidence | Durable per-session aggregate sequence; OAP per-run sequence still adapter-generated but directly derivable. |
| event `id` | private frame identity | Never an OAP event ID. |
| tool `callID` (tool events) | `tool_call_id` | Stable across tool.* events. |
| `Permission.ID` / `Question.ID` | interaction identity | Reverse interaction channels with reply/reject. |
| location / workspace IDs | endpoint scoping | Multi-location server; adapter must pin one location per association. |

## Lifecycle mapping

| Native observation | OAP meaning | Fidelity | Initial support | Required fixture |
|---|---|---|---|---|
| `GET /openapi.json`, `health.get` | initialize/descriptor truth | normalized | emulated | `initialize-minimal` |
| `POST /api/session` | session association | native | native | `session-create` |
| `POST .../prompt` -> `Admitted` | admission; submission identity | native | native | `message-admitted` |
| `ConflictError` on prompt | typed rejection | native | native | `message-conflict` |
| `resume:false` prompt | admit without execute | native | maps to OAP admission/execution split | `admit-without-execute` |
| `session.next.prompt.admitted` event | durable admission observation | native | corroboration | `message-admitted` |
| `session.next.prompted` / `promotedSeq` | execution begins; `run.started` | native | native | `run-start` |
| `text.started/delta/ended` | message lifecycle and `content.delta` | native | native | `streaming-deltas` |
| `reasoning.*` | reasoning content deltas | normalized | degraded | `reasoning-deltas` |
| `tool.input.*` | action argument streaming | normalized | degraded | `tool-input-stream` |
| `tool.called` -> `tool.progress` -> `tool.success`/`tool.failed` | action requested/started/progress/terminal | native | native | `tool-lifecycle` |
| `step.started/ended/failed` | provider-turn boundaries | normalized | observed-only initially | `multi-turn` |
| `shell.started/ended` | shell action observation | normalized | part of tool mapping if hosted | `shell-action` |
| `retried` | retry lifecycle | observed-only | no core claim | `retry` |
| `compaction.*` | context maintenance | observed-only | no core claim | `compaction` |
| `revert.*` | rewind semantics | native input | extension decision required | `revert` |
| permission request + reply | interaction requested/resolved | native | native candidate | `permission-gate` |
| question + reply/reject | interaction requested/resolved | native | native candidate | `question-gate` |
| `POST .../interrupt` (no-op when idle) | cancellation intent | native | degraded until race fixtures | `interrupt-idle`, `interrupt-active` |
| step/text/tool quiescence + `session.wait` | settlement boundary | normalized | native candidate | `settlement` |
| `GET .../event?after=N` | replay then live | native | native | `replay-after-cursor` |
| `GET .../history?after=N` | bounded reconstruction | native | distinct from replay | `history-page` |
| SSE disconnect/reconnect with `after` | reconciliation | native | native candidate | `reconnect` |
| HTTP 404/409 typed errors | typed failures | native | native | `not-found` |
| server exit / SSE error before settlement | one `run.failed` | synthesized | transport failure handling | `process-exit` |

### Terminal arbitration

The durable event set has **no explicit run-terminal event** — no
`session.next.completed`/`failed`. Settlement must be derived:

- `step.failed` / `tool.failed` are child/step-scoped, not run terminals;
- `text.ended` closes a message, not a run;
- the practical boundary is quiescence: the last durable event of the
  promoted execution, corroborated by `session.active` no longer listing
  the session. (`session.wait` reads as the natural signal but is a stub at
  this pin — see the wire protocol note above.)

This is a genuine OAP-relevant discovery: a boundary can have perfect
durable sequencing and still lack an authoritative terminal marker. The
adapter's arbiter must define the terminal rule (e.g. step.ended with no
pending tools and no admitted-but-unpromoted steer, plus active-set
corroboration) and record it as an explicit synthesized-fidelity decision.
Candidate OAP revision: consider whether "terminal marker required" stays a
producer obligation or a documented adapter synthesis.

## Cancellation

`interrupt` is documented as active-execution-scoped and a no-op when idle
— intent-shaped like Claude Code's interrupt, with the idle no-op removing
the destructive-teardown hazard Makai has. Settlement evidence must come
from the event stream (aborted step/text/tool sequences), not from the 204
response.

## P0 mismatches

1. **No explicit run-terminal event:** settlement derived from quiescence
   plus `wait`/`active` corroboration (see above).
2. **Aggregate sequence is per-session, not per-run:** OAP per-run sequence
   derived by the adapter; native `seq` preserved as ordering evidence.
3. **Delivery vocabulary is `steer | queue`** with an omitted-default:
   mapping to OAP `auto` needs one pinned decision (omitted = immediate
   execution).
4. **Permissions and questions are separate channels** with different reply
   shapes — OAP interaction model must represent both or explicitly fold
   one.
5. **Multi-location/multi-session server:** association scope must pin
   location; a serve instance is not one session.
6. **Auth is basic-auth with a daemon password:** the adapter must own
   credential handling; never forward ambient credentials (standing policy).
7. **Event schema versioning exists natively** (`version` per event):
   relationship to OAP capability revision needs one decision.
8. **Revert exceeds core OAP:** stage/clear/commit is a rewind protocol;
   extension decision required before any claim.

## Implementation outcomes (first adapter tranche)

The Go adapter tranche (`adapter/opencode/`, corpus at
`fixtures/adapters/opencode-v1.18.29/`) recorded these additional
discoveries:

1. **OAP v0.1 admission blocks native steer and queue.** The canonical
   stateful validator requires every accepted submission to resolve to one
   started run (`admission=started`, `effective_delivery=start`), and the
   server defaults omitted delivery to `steer`. The adapter therefore maps
   `auto` to native steer (start-when-idle, locally gated to one active
   run) and rejects explicit `steer`/`queue`/`btw` until OAP extends the
   admission model; `delivery.queue`/`delivery.steer` are advertised
   degraded with that reason. This is an OAP-core decision item, not an
   OpenCode defect.
2. **The pinned durable inventory omits `session.next.step.failed`.** The
   event is defined durable (`stepSettlementOptions`, version 2) and
   persisted, but it is absent from `DurableDefinitions`, so the
   per-session SSE filter drops it and a history reader decoding through
   the manifest would fault. The adapter defensively decodes it (fixture
   `step-failure`) and this omission should be reported upstream.
3. **Pre-start terminal failures are not canonically representable.** A
   rejected admission or a foreign-aggregate event settles a run before
   `run.started`; v0.1 canonical traces reject run-scoped events without a
   preceding started run. Those corpus cases are recorded without canonical
   validation and logged as the mismatch evidence.
4. **Queued admissions are real but non-canonical.** `SessionInput.Admitted`
   without `promotedSeq` answers `admission=queued` truthfully; the corpus
   pins the trace (`queued-admission`) outside canonical validation pending
   the same admission-model extension.
5. **Settlement fence:** derived settlement is active-set corroboration plus
   a bounded history read after the triggering sequence, deduplicated by
   durable seq against the subscription prefix — recording the assumption
   that the last durable event of a turn is persisted before the loop goes
   idle. The corroboration polls `session.active` because the `wait` route is
   a stub at this pin. The run coordinator holds a session in the active set
   for one whole drain, and a drain is one agent loop covering every step of
   a turn, so the set does not flap between steps; work recorded mid-turn
   installs a successor entry that keeps the key present. The set is scoped
   to sessions owned by that server process, which matches the adapter's
   single-process boundary.
6. **Deltas are not on the durable stream** (`text.delta` and friends are
   live-only), so first-tranche streaming is full-value boundaries —
   `run.streaming` stays degraded exactly as advertised.
7. **SSE strictness choices:** field parsing follows the SSE spec (unknown
   fields ignored, comments skipped) while bare-CR line endings are
   rejected because the pinned producer emits LF only; response bodies are
   strict-decoded (unknown fields and duplicate keys rejected).
8. **Permissions remain unavailable in v1** — the durable stream carries no
   permission events and the polling surface is unexercised, so no
   interaction mapping is claimed.

### Resolution under decision 0002 (2026-09-10)

Outcomes 1, 3, and 4 above were adjudicated as protocol feedback PF-1 and
resolved by [Decision 0002](../decisions/0002-admission-before-start.md):
an accepted submission may canonically report `admission=queued` with
`effective_delivery=queue` and reserve its run identity at admission, and an
accepted run may settle pre-start with `run.failed` (or `run.cancelled`
behind an accepted cancel) as its only run-scoped event. Consequences for
this adapter:

- the `queued-admission`, `foreign-session`, and `message-conflict` corpus
  cases now execute under canonical v0.1 validation (no exclusions remain);
- the queued reservation response carries `effective_delivery=queue`
  (previously the contradictory `start`);
- every reserved-run failure path (prompt conflict, foreign admission,
  invalid native message identity) returns the accepted queued reservation
  with the failure on its stream, instead of an error paired with a dangling
  stream;
- explicit `queue`/`steer` delivery *requests* remain outside the v0.1
  subset, exactly as before.

### Resolution under the queue unit (2026-09-16)

The queue unit graduates `session.message.delivery.queue`, and this adapter
is where its native evidence lives: `SessionInput.Admitted` carries
`delivery: "queue"` with an optional `promotedSeq`, and admission and
promotion are separately observable on the durable stream
(`session.next.prompt.admitted`, `session.next.prompted`). The adapter now
advertises the key `native` and applies it. Changes at this pin:

- an explicit `queue` request maps to `native.DeliveryQueue`; `steer` and
  `btw` are still refused under their own keys;
- a submission while the started run is nonterminal — explicit `queue` or an
  `auto` the busy session resolves to one — is admitted as a reservation and
  reports `delivery_resolution: "session_busy"` for the `auto` case;
- the reservation's `run.started` is held until the started run's terminal is
  on the wire, even when `session.next.prompted` for it arrives first, so one
  run domain executes at a time in admission order;
- an admission is a reservation until the server's `session.next.prompted`
  begins its turn, including on an idle session: the run identity exists from
  admission, the turn does not, and the state projection follows the trace
  rather than the slot the adapter happens to park the run in;
- the descriptor discloses `max_active_runs_per_session: 2` and
  `max_queued_runs_per_session: 1`, and `Submit` counts against exactly those
  numbers rather than testing whether a slot is occupied. The server queues more than one input
  natively, but settlement here is derived from quiescence over a single
  execution, so a second reservation exceeds what this pin's evidence
  supports and is refused `run_active`;
- the capability revision becomes `opencode-v1.18.29-oap-v2`, since a
  revision identifies exactly one descriptor.

**Recorded assumption: promotion marks the turn boundary.** Once
`session.next.prompted` names a queued input, every later durable event of the
session belongs to that input's turn, and the previous turn's step and text
events are already persisted. The adapter relies on this to route native events
after a promotion into the promoted run rather than the one still finishing;
reducing them into the earlier run would attribute one run's output to another
and leave the promoted run unable to settle. It is the same shape of assumption
as the settlement fence above — that the last durable event of a turn is
persisted before the loop goes idle — and rests on the same property, that the
run coordinator drains one agent loop per turn. The adapter's *publication* of
the promoted run waits for the earlier run's derived terminal even so, because
the terminal is derived from quiescence and lags the boundary. Held envelopes
are withheld from the journal as well as from the stream, since a journalled
envelope is replayable: a caller resuming the reserved run mid hold would
otherwise read its start before the earlier run's terminal and be handed the
same envelopes again at release. The same holds for the state projection: a
held run is listed at the status and position it has published, so a promoted
reservation that finishes natively while the earlier run is still open is still
described as the reservation the trace knows rather than as a settled run in a
field defined as the session's nonterminal ones.

A run whose terminal *is* published leaves the projection, and the projection
says so rather than merely dropping it: `as_of.settled` names the run and the
sequence its terminal carries. This is not the hold above. A state read is
answered synchronously while a terminal leaves through a buffered stream, so
the snapshot can be on the wire before the terminal that removed the run from
it, and the reducer cannot see when a consumer publishes. Rebuilding the
projection after the publication it describes — which this adapter does, under
the mutex that also delivers — orders the adapter's own two steps and narrows
the window without closing it: measured on this adapter, a snapshot reported
the session idle ahead of an undelivered terminal in 300 of 300 settlements
with nothing reading the stream, and in 155 of 300 with a consumer draining as
fast as the reducer wrote. The claim is recorded where an envelope becomes
history, not where a run is marked terminal, so a held terminal makes none and
no snapshot both lists a run and says it let it go.

**New mismatch (P1): no route withdraws one queued input.** The pinned
server's cancellation surface is `POST /api/session/:id/interrupt`, which is
documented as active-execution-scoped. Sending it to cancel a *reservation*
would interrupt the started run instead — the wrong work. The adapter
therefore drops the reservation locally and settles it `run.cancelled`
pre-start without calling the server, and ignores a later promotion for a
run its terminal has already absorbed. The consequence is that the server
may still execute a withdrawn input while OAP reports the run cancelled. That
turn is quarantined: it has no OAP run to own it, since the reservation's run
already settled, and reducing it into whatever run is started would hand that
run another turn's content, reopen its step accounting, and settle it on a
boundary it never reached. Quarantine is the general rule for a turn no OAP
run owns — a foreign input takes the same path — and it lifts at the next
`session.next.prompted` naming an input this adapter admitted. Closing the
mismatch itself needs an upstream route that removes one admitted input by its
`SessionMessage.ID`; until one is pinned, `run.cancel` stays `degraded` and
this is recorded rather than compensated.


## Initial capabilities

- initialize / descriptor: `emulated` (OpenAPI + catalogs)
- session association: `native`
- submission/admission: `native` (`Admitted`, typed conflict rejection)
- run identity: `emulated` (no native run id; promotion-derived)
- run status: `native` (`session.active`; `session.wait` is unimplemented
  at this pin)
- sequencing: `native` durable aggregate sequence (adapter derives per-run)
- text/reasoning/tool input streaming: `native`
- tool lifecycle: `native`
- permissions: `native` candidate pending fixture
- questions: `native` candidate pending fixture
- cancellation: `degraded` (intent no-op-when-idle; settlement derived)
- delivery steer/queue: `native` candidates pending fixtures
- replay (cursor + live continue): `native`
- reconstruction (history pages): `native`
- reconciliation (reconnect with after): `native` candidate
- compaction/retry control: observed-only
- revert: `unavailable` pending extension decision
- model/agent catalogs: `native` (`model.list`, `agent.list`) as *routes*;
  see the models finding below for what the adapter may actually serve

## Models finding (2026-09-16)

The `model` and `provider` groups are part of the pinned route inventory —
they are listed in "Wire protocol" above and their existence is pinned by
`packages/protocol/src/groups/*.ts` — but **no response shape for either
route is pinned at this revision**. The blobs this ledger and the corpus
pin cover the session group, the session events, the input and delivery
types, the server handler, and the core session service; none of them
covers `model.list` or `provider.list`. A decoder written against those
routes would therefore be an adapter decoding a shape nothing here
records, which is the one thing the ledger exists to prevent.

What *is* pinned about which model a session runs:

- `SessionInfo.model` (`ModelRef`: `{ id, providerID, variant? }`) on the
  session record returned by create and get, pinned through
  `session_group_blob` and `core_session_blob`;
- `session.next.step.started.model`, the same `ModelRef` on every durable
  step, pinned through `session_event_blob` and exercised by the existing
  corpus cases;
- `session.next.model.switched`, which is durable but carries only
  `{ timestamp, sessionID, messageID }` — it announces that a switch
  happened and names no model, so it cannot advance a catalog.

The adapter therefore serves, for the `models` unit, the effective models
this session has evidence for: the session record's model first, then each
distinct model a durable step named, projected onto OAP identity as
`provider/id`. That is a truthful catalog of what the session runs and not
the server's own list, and it grows as steps are observed, so
`models.list` is advertised **`degraded`** with that disclosure rather
than `native`. The consequence is the one `degraded` carries everywhere: a
caller must consent through `allow_degraded_features`, or the query is
refused with `capability_degraded`.

Raising this to `native` needs one thing and nothing else: a pin for the
`model.list` (and, for `provider_id` without string-splitting,
`provider.list`) response shape, added to this ledger with its source
blob, plus a corpus case decoding it through the production HTTP client.

Corpus evidence at this pin: `multi-step/catalog.json`, the catalog the
production reducer projects from that case's durable `step.started`
frames.

## Evidence corpus plan

`fixtures/adapters/opencode-v1.18.29/`, standard five-file cases:
`initialize-minimal`, `session-create`, `message-admitted`,
`message-conflict`, `admit-without-execute`, `run-start`,
`streaming-deltas`, `reasoning-deltas`, `tool-input-stream`,
`tool-lifecycle`, `tool-failed`, `multi-turn`, `shell-action`, `retry`,
`compaction`, `revert`, `permission-gate`, `question-gate`,
`interrupt-idle`, `interrupt-active`, `settlement`, `replay-after-cursor`,
`history-page`, `reconnect`, `not-found`, `process-exit`,
`no-implied-run-id`.

A gated live-server test (`opencode serve` on a loopback port with a
hermetic provider) follows the Makai/Codex integration-gate pattern.

## Live-gate finding (2026-09-10)

The gated server gate (`OAP_OPENCODE_INTEGRATION=1`, binary
`opencode` v1.18.29 linux-x64 from the pinned release) was run live and
exposed a blocking transport mismatch.

**The pinned server defers SSE response headers on the session-scoped event
endpoint until the first event exists.** `GET /api/session/<id>/event` on a
fresh, silent session emits **no response headers** until an event occurs —
verified with `curl` (no bytes within 3s; the connection is accepted but no
status line is sent), with and without `Accept: text/event-stream` and with
and without `?after=`. Once a prompt has produced events, the same endpoint
flushes immediately and replays from the cursor.

Because `adapter/opencode/internal/httpapi` `Client.Subscribe` performs a
blocking `http.Client.Do`, `Adapter.Open` (via `client.Subscribe(ctx,
info.ID, -1)`) blocks on a fresh session until the caller context expires:
the gate fails with `subscribe OpenCode session events: context deadline
exceeded`. This is an adapter/server contract mismatch, not a test defect.

The global `GET /event` stream does flush immediately (`server.connected`),
so the obvious substitute is not free: its frames use a different envelope —
`{"id","type","properties"}` flattened, without the session-scoped stream's
`durable`/`data` members — so switching endpoints requires its own decoder
and reconciliation.

This is a verified interoperability defect. **Resolved (2026-09-10):**
`Client.Subscribe` now starts the request concurrently and returns the
subscription after a short establishment grace
(`subscribeEstablishGrace = 250ms`) if headers have not arrived. Immediate
failures — HTTP status errors, refused connections — still return
synchronously from `Subscribe` exactly as before (the pinned
`TestSubscribeSurfacesHTTPErrors` contract is unchanged); only the
deferred-header case becomes asynchronous, and any later failure surfaces
through `Subscription.Done`/`Err`, which the reducer already projects as
transport failure. No change was needed to the global-stream envelope path.

Regression coverage: `TestSubscribeReturnsWhenHeadersAreDeferred` in
`adapter/opencode/internal/httpapi` fails against the old blocking code
(`context deadline exceeded`, matching the live gate error) and passes after.
The live server gate `OAP_OPENCODE_INTEGRATION=1` now passes end to end
against the pinned v1.18.29 binary. The gate remains skip-by-default and
CI-safe.

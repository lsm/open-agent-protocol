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
- `POST /api/session/:id/wait` — wait for the agent loop to become idle
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
  promoted execution, corroborated by `session.wait` (idle) and
  `session.active` no longer listing the session.

This is a genuine OAP-relevant discovery: a boundary can have perfect
durable sequencing and still lack an authoritative terminal marker. The
adapter's arbiter must define the terminal rule (e.g. step.ended with no
pending tools and no admitted-but-unpromoted steer, plus wait-idle
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
5. **Settlement fence:** derived settlement is wait-idle corroboration plus
   a bounded history read after the triggering sequence, deduplicated by
   durable seq against the subscription prefix — recording the assumption
   that the last durable event of a turn is persisted before the loop goes
   idle.
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


## Initial capabilities

- initialize / descriptor: `emulated` (OpenAPI + catalogs)
- session association: `native`
- submission/admission: `native` (`Admitted`, typed conflict rejection)
- run identity: `emulated` (no native run id; promotion-derived)
- run status: `native` (`session.active`, `session.wait`)
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
- model/agent catalogs: `native` (`model.list`, `agent.list`)

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

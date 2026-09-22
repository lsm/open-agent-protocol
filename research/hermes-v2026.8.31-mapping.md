# Hermes agent v2026.8.31 mapping ledger

Status: frozen implementation contract for the seventh production OAP adapter.
Every normative claim below was read from the pinned sources (extraction was
fan-out assisted; every load-bearing claim was re-verified against the tree
before freezing). This is an implementation input, not an interoperability
claim about other Hermes surfaces.

## Provenance

- Repository: `https://github.com/NousResearch/hermes-agent`
- Release: `v2026.8.31`
- Release commit: `29112bef099274229cadff79cdff7bf7b99c4b77`
- Release commit tree: `daaffc303ae437041b7f76be17c5f61b14f2ce99`

Normative source blobs (re-verified at freeze time):

- `tui_gateway/entry.py` — `27fd051b8aff7cb6e6ddd9103eb14c06d7b2c0e8`
- `tui_gateway/transport.py` — `ce93e518a3d5255f9729de80cadb4377747d0d6d`
- `tui_gateway/server.py` — `4e846e36e339172123248fafdac762f204604fd8`
- `tui_gateway/event_replay.py` — `0ec4e2a86b4a4fa75c51b25297cba74d6fd9198e`
- `tui_gateway/_stdin_recovery.py` — `80c77aeb0dcae31afa1d5c21d3abb7e2cc976962`
- `tui_gateway/ws.py` — `988733a9b14e178289b446c11e6eb31f30a4b91b`
- `tui_gateway/methods_prompt.py` — `3525ffcfdd4ed06a27c97092a28cf9a04d63efc8`
- `tui_gateway/methods_session.py` — `485456a87f61918c3c53891bb4c05cebddff0a40`

A newer HEAD (`03f3b09222b8f03becb203a6ebb9bac1f927b8b6`) was inspected during
earlier research; it is design signal only. Only the release commit above is
normative.

## Boundary selection

The adapter boundary is the **tui_gateway JSON-RPC server over stdio**, the
`hermes --tui` backend. The TUI Node client spawns `<python> -m
tui_gateway.entry` with the repo root as cwd and piped stdio
(`ui-tui/src/gatewayClient.ts:487`); the WebSocket mount (`tui_gateway/ws.py`)
routes every frame through the same `server.dispatch` verbatim, so the stdio
boundary is protocol-complete and hermetic. The FastAPI dashboard, gateway
relay, hosted rooms, groups, and the desktop app are out of scope.

### Process lifecycle and teardown

- On start the server emits `gateway.ready` **before reading any input**:
  `{"jsonrpc":"2.0","method":"event","params":{"type":"gateway.ready","payload":{"skin":<skin>,"change_events":true,"replay_epoch":"<uuid4hex>"}}}`
  (`entry.py:454-467`). No `session_id`, therefore never seq-stamped and never
  replayable. `skin` is the full `resolve_skin()` dict.
- **There is no process-level shutdown RPC.** Teardown is stdin EOF — a clean
  peer close (O_NONBLOCK clear) is genuine EOF, breaks the read loop, and the
  process exits 0 after atexit session flush (`entry.py:486-492`,
  `_stdin_recovery.py:99-112`); spurious-EOF recovery fires only when a child
  flipped O_NONBLOCK on the shared description, which is not the adapter's
  teardown path. SIGTERM drains with a 1 s grace then hard-exits
  (`entry.py:76-87`). Adapter `Close` closes stdin and waits for exit 0.
- `session.close` is per-session (5 s settle grace, then teardown with
  `end_reason="tui_close"`); it is not process shutdown.

## Wire protocol

### Outbound (server → adapter)

One JSON object per line: `json.dumps(obj, ensure_ascii=False) + "\n"`, under
one stdout lock, flushed per write (`transport.py:114-180`). UTF-8 non-ASCII
appears unescaped. Frames are JSON-RPC responses
(`{"jsonrpc":"2.0","id":rid,"result":<object>}` or
`{"jsonrpc":"2.0","id":rid,"error":{"code":<int>,"message":<str>[,"data":<any>]}}`)
and event notifications
(`{"jsonrpc":"2.0","method":"event","params":{"type":<str>,"session_id":<sid>[,"payload":<object>],"seq":<int>}}`).
`payload` is omitted entirely when `None` (`server.py:2520-2524`); `seq` is
injected only when `session_id` is non-empty.

### Inbound (adapter → server) and the strictness mismatch

The native reader takes one `readline()`, **strips surrounding whitespace**,
silently skips blank lines, parses JSON and — on parse failure — **answers
`{"jsonrpc":"2.0","error":{"code":-32700,"message":"parse error"},"id":null}`
and continues** (`entry.py:495-505`). Non-dict JSON yields `-32600`; params
must be an object or `null` (`-32602`); unknown method `-32601`; the
`jsonrpc` field itself is never validated; batches are rejected as non-objects
(`server.py:3007-3032`). Handler exceptions surface as `-32000`.

The adapter codec is deliberately narrower and fails closed: bare
LF-terminated object frames only (no CR tolerance, no blank-skip, no
survivable parse errors), duplicate-key and trailing-value rejection, and
typed envelope validation. This is an explicit interoperability mismatch, not
silent normalization — same policy as the DeepSeek ledger.

### Response ordering

`prompt.submit`, `prompt.btw`, `prompt.background`, `session.steer`,
`session.interrupt`, `subagent.steer`, `subagent.interrupt` all run **inline**
on the reader thread (none is in `_LONG_HANDLERS`, 60 base + 18 groups
members). Long handlers (78 methods: `session.resume`, `session.branch`,
`session.list`, `model.options`, …) are pooled and write their own response
asynchronously. Even for inline handlers the run thread races the response
write: turn events may precede or follow the response on the wire. The
adapter must reduce frames in arrival order and must not assume
response-before-events.

## Sequencing and replay

- Every event frame with a non-empty `session_id` is stamped under the replay
  lock with a per-session monotonic `seq` starting at 1, in frame order
  (`event_replay.py:52-75`). Session-less events carry no `seq` and have no
  replay contract.
- Replay ring: 512 events per session, 64 sessions, FIFO eviction.
  `session.events.since {session_id, last_seen:int}` (unknown sid is **not**
  an error) returns
  `{"events":[<bare event params objects>],"latest_seq":<int>,"truncated":<bool>,"count":<int>,"epoch":"<hex>"}`.
  `truncated = last_seen+1 < oldest_retained_seq` — the client must refetch
  history rather than accept a gap. Unknown session →
  `{events:[], latest_seq:0, truncated:false, count:0, epoch}`.
- `epoch` is a per-process uuid4 hex, delivered on `gateway.ready` and on
  every replay response; seq counters are in-process and a restart silently
  resets them. Epoch change ⇒ every watermark and every run association is
  invalid.
- `session.events.stats` → `{"sessions":int,"events":int,"max_per_session":512}`.

## Session identity (dual)

- Runtime `session_id` (the sid used on the wire): `uuid4().hex[:8]`, 8
  lowercase hex chars (`methods_session.py:16`). Minted by `session.create`.
- Durable `stored_session_id` / `session_key`:
  `"%Y%m%d_%H%M%S_" + uuid4().hex[:6]` — the `state.db` key, returned by
  `session.create`/`session.branch`/`session.resume` and carried on
  `session.info` and `session.reclaimed`.
- `session.create {cols?, messages?, title?, parent_session_id?, cwd?, source?,
  profile?, model?, provider?, reasoning_effort?, fast?, close_on_disconnect?,
  hidden?}` → `{session_id, stored_session_id, message_count, messages,
  info:{model[,provider],tools,skills,cwd,branch,project,lazy,
  desktop_contract,profile_name}}`. Persistence is lazy on first prompt.

## Admission model (wire truth)

This is the sharpest divergence from every prior pinned harness, and the
research-stage assumption ("correlate via subsequent event frames") resolves
as follows:

1. `prompt.submit` params: `session_id`, `text` (str, sanitized; non-str
   multimodal parts pass unvalidated), `display_kind` (only `"hidden"`
   whitelisted), `interrupted` (voice barge-in latch), truncation family
   (`truncate_before_user_ordinal`/`_row_id`/`_message_id`,
   `confirm_truncate`, `confirm_empty_truncate`,
   `rebind_survivor_row_ids`), `queued` (forces queue semantics), `surface`.
2. The handler runs inline: under the history lock it sets `running=true`,
   clears the cancel flag, snapshots the prompt into
   `inflight_turn:{assistant,started_at,streaming,updated_at,user}`, starts
   the run daemon thread, and **only then returns** (`methods_prompt.py:908-1049`).
3. The success result is exactly `{"status":"streaming"}` plus optional
   truncation bookkeeping (`survivor_user_row_ids`,
   `survivor_row_id_map`) — **no submission identity of any kind is minted**.
   Nothing echoes the prompt text or a message id. The only correlation
   fields in the event stream are `session_id` and per-session `seq`.
4. Mid-turn (busy) submits return `{"status":"steered"}` (busy_input_mode
   `steer`), `{"status":"redirected"}` (mode `interrupt` + redirect support),
   or `{"status":"queued"}` (enqueue; interrupt mode also interrupts the live
   turn). `display.busy_input_mode` ∈ {`interrupt` (default), `steer`,
   `queue`}. A voice-stop edge returns `{"voice_stopped":true}`.
5. The turn on the wire is delimited by **`message.start`** — a payload-less
   frame (`{"type":"message.start","session_id":sid,"seq":N}`), emitted by
   the run thread (`server.py:12788`) and also on queued-drain,
   auto-continue, loop wakeup, and goal continuation — followed by deltas and
   exactly one terminal `message.complete`. `message.start` is *a turn opened
   on this session*, not proof that a specific submit started it.

**Adapter consequence:** ownership must be established by construction, not
by receipt matching (the inverse of DeepSeek's user-message proof). The
adapter enforces one outstanding OAP submission per session; after an accepted
`{"status":"streaming"}` the next `message.start` on that session opens the
owned run. Both wire orders (response before or after the opening frames) are
legal and must reduce identically. Conservative v1 mapping of busy statuses:
`steered`/`redirected`/`queued` results are **rejected locally before any
native write** unless the adapter advertises those delivery modes; overlap
during an owned open turn is rejected as in the other adapters.

### btw and background (the native side-channel pair)

- `prompt.btw {session_id, text}` → `{"task_id":"btw_<6hex>"}`; runs a
  one-shot side-question against a snapshot of the live working set; session
  history untouched; terminal event `btw.complete
  {task_id, question, text}` (error → `text:"error: …"`). Always accepted,
  even mid-turn. This is the only pinned harness with a native by-the-way
  primitive.
- `prompt.background {session_id, text}` → `{"task_id":"bg_<6hex>"}`; runs a
  full separate agent conversation; terminal event `background.complete
  {task_id, text}`. Always accepted.
- Both mint server identities (unlike `prompt.submit`) and echo them in the
  completion event — task-scoped correlation is exact.

## Event vocabulary (reducer-relevant subset)

Full inventory verified from emit sites; the adapter models this subset and
treats the rest as observed-only or ignorable per the classification policy.

| Type | Payload | Notes |
|---|---|---|
| `gateway.ready` | `{skin, change_events:true, replay_epoch}` | global, pre-input, once |
| `message.start` | *(none)* | a turn opened on the session |
| `message.delta` | `{text[, rendered]}` | text only; post-scrubber agent output, not raw provider tokens |
| `reasoning.delta` | `{text[, verbose:true]}` | distinct channel |
| `thinking.delta` | `{text}` | plain thinking callback |
| `message.interim` | `{text, already_streamed:bool}` | commentary beside tool calls |
| `message.complete` | see Terminal arbitration | the sole settlement frame |
| `error` | `{message}` | non-turn-scoped; never a settlement |
| `tool.start` | `{tool_id, name, context[, args][, args_text]}` | gated by tool-progress mode / UI-required names |
| `tool.complete` | `{tool_id, name, args[, duration_s][, result][, summary][, result_text][, inline_diff][, todos, revision]}` | **no tool.error** — failure rides in `result` |
| `todo.updated` | `{todos, revision}` | |
| `session.usage` | `{usage}` | 1 s ticker, deduped |
| `session.info` | full `_session_info` (model, provider, running, turn_started_at, stored_session_id, usage, …) | emitted at settle and on config change |
| `session.title` | `{session_id, title}` | |
| `status.update` | `{kind, text}` | kinds incl. process/loop/goal/compacting |
| `notification.show` / `notification.clear` | notice fields / `{key}` | |
| `approval.request` | raw approval data + synthesized `choices` | **no `request_id`** — resolved via `approval.respond` registry |
| `clarify.request` | `{question, choices[, multi_select]}` or `{questions:[{qid,question,choices,multi_select}]}` + `request_id` | via `_block` |
| `sudo.request` | `{}` + `request_id` | via `_block`, 120 s |
| `secret.request` | `{prompt, env_var[, metadata]}` + `request_id` | via `_block` |
| `mcp.setup.request` | `{server, action, reason}` + `request_id` | via `_block`, 600 s |
| `terminal/preview.act/preview.read/window.read/tour.request` | passthrough dicts + `request_id` | via `_block`; out of scope v1 |
| `<prefix>.expire` | `{request_id}` | every `_block` kind has an expire sibling |
| `subagent.spawn_requested/start/tool/progress/thinking` | identity + preview fields | on the **parent** sid |
| `subagent.complete` | `status` (`complete`/`timeout`/`error`/`failed`), `summary`, token rollups, `output_tail` | child terminal on parent sid; `cost_usd` dropped at the gateway |
| `subagent.text` | *(never emitted on parent)* | mirrored to the child watch session only |
| `btw.complete` / `background.complete` | `{task_id[, question], text}` | side-channel terminals |
| `session.reclaimed` | `{session_id, stored_session_id, reason∈{idle_timeout,lru_evict,ws_orphan_reap}}` | global |
| `skin.changed`, `pet.changed`, `sessions.changed`, `cron.changed`, `platforms.changed`, `pairing.changed`, `bot_relay.outbox.pending` | various | global, no seq, ignorable |

Gate answer encoding: `clarify.respond`/`sudo.respond`/`secret.respond` store the
`answer`/`password`/`value` member verbatim (`_respond`, `server.py:14032`), and
the clarify tool decodes a `multi_select` answer with
`_parse_multi_select_response` (`tools/clarify_tool.py`), which accepts a JSON
array string, a list, or a comma-separated list of choice labels. A multi-select
answer is therefore written as the JSON array string of the selected labels
(`["a","c"]`); a single-value gate takes the bare label. The adapter never
collapses a multi-select answer to one label, and rejects a multi-option answer
to a single-value gate rather than dropping the extras. A clarify question with
no choices is open-ended and surfaces as an OAP text question (choice questions
require at least one option); a selected option outside the surfaced choices is
rejected before any native call.

Child watch-session mirror: when a client holds a child watch session, the
gateway translates subagent frames into native frames on the child sid
(`subagent.thinking`→`reasoning.delta`, `subagent.text`→`message.delta`,
`subagent.tool`→`tool.start`/`tool.complete` with synthetic
`tool_id:"submirror:<key>:<seq>"`, `subagent.complete`→`message.complete
{text}` — **status-less**, never a parent terminal).

## Terminal arbitration

There are **no turn lifecycle events** on the wire (`"turn.start"` exists only
as an internal compute-host frame). Settlement evidence is exactly one
`message.complete` frame per turn:

- Normal settle (`server.py:13276-13353`): `{text, usage, status}` where
  `status ∈ {"interrupted","error","complete"}` (ternary in that order), plus
  optional `reasoning`, `warning`, `response_previewed`, `billing`+
  `failure_reason`, `rendered`; on `error` also `error` (string — **no code
  field**; the code lives in `error_surface:{layer,code,retryable}`),
  `recoverable:true`.
- Terminal turn error (exception path, `server.py:10434-10495`): same frame
  with `status:"error"`, `error`, `recoverable:true`, optional `partial:true`,
  optional `error_surface`.
- Compute-host error short-circuit: `{text:"Error: …", status:"error"}`.
- Child-mirror variant `{text}` (no status) is child-stream evidence, never a
  parent settlement.
- Interrupt: `session.interrupt` → `{"status":"interrupted"}` (or
  `{"status":"not_interrupted","interrupted":false}` on guard mismatch);
  settlement arrives as the normal `message.complete` with
  `status:"interrupted"`. No dedicated interrupt event.
- Post-settle order: `message.complete` → `session.info` (settled), then
  optional `status.update {kind:goal|loop}` and queued-prompt drain (a fresh
  `message.start`).

OAP mapping: `status:"complete"` → `run.completed` (usage from `usage`);
`"interrupted"` and `"error"` → `run.failed` (interrupt-coded vs
error-surfaced); missing status on a parent stream is a protocol violation;
the settlement frame is absorbing — one terminal per run, nothing after it
belongs to the run.

## Streaming granularity correction

The research-stage ledger claimed ~33 ms delta coalescing as transport truth.
**Correction: the coalescing is WebSocket-transport-only.** The stdio
boundary writes and flushes every frame immediately — one frame, one `seq`,
never merged (`ws.py:55-62` defines `_STREAMING_EVENT_TYPES =
{message.delta, reasoning.delta, thinking.delta}` and
`_TOKEN_COALESCE_S = 0.033` as trailing-edge debounce, but only
`WSTransport` buffers). The stdio honesty note is instead that delta text is
post-scrubber agent output (think-scrubber, context-scrubber, paragraph
prepending), not raw provider tokens.

## Identity domains

| Native identity | OAP identity | Rule |
|---|---|---|
| gateway process (epoch) | endpoint | adapter allocates; epoch guards all seq association |
| runtime `session_id` (8 hex) | `session_id` association | correlation identity on every frame |
| durable `session_key` | adapter-side evidence only | never an OAP identity |
| JSON-RPC `id` | private request correlation | never an OAP identity |
| accepted `prompt.submit` | `submission_id` | adapter-minted (native mints none) |
| `message.start` … `message.complete` span | `run_id` | adapter-allocated; delimited by construction |
| event `seq` | per-session ordering evidence | per-session not per-run; restart-reset; epoch-guarded |
| `task_id` (`btw_*`/`bg_*`) | side-channel correlation | native, echoed in completion events |
| `request_id` (8 hex, `_block` family) | interaction identity | expire siblings carry the same id |
| approval registry entry | interaction identity | no wire `request_id`; resolved via `approval.respond` |
| `tool_id` | `tool_call_id` correlation | native per call; `submirror:` synthetic ids in mirrors |
| `subagent_id` / `child_session_id` | child task identity | parent-frame correlation |

## Supported OAP surface (initial)

- initialize/ready handshake: `native` frame, `emulated` descriptor; no
  capability negotiation (`groups.capabilities` is the nearest native truth,
  out of scope)
- session association via `session.create`: `emulated` (adapter mints/uses
  the runtime sid)
- admission: `degraded` — status-only result, ownership by construction
- text/reasoning streaming: `native` (immediate frames, disclosed
  post-scrubber provenance)
- tools: `degraded` — `tool.start`/`tool.complete` only; failure rides in
  `result`; no native error frame
- interactions: `native` candidates — approval/clarify/sudo/secret with
  expire siblings; per-kind mapping decisions, not one blanket permission
- btw delivery: `native` (unique among pinned harnesses), task-scoped
- background prompts: `native` candidate, side-channel scoped
- steer (session-scoped): `native` candidate (`{"status":"queued"|"rejected"}`)
- subagent steer/interrupt: `native` candidates (registry-scoped)
- cancellation: `degraded` — interrupt is intent; settlement is the
  `status:"interrupted"` terminal
- replay: `degraded` native — bounded window, explicit `truncated`, epoch
  invalidation; no replay of session-less events
- reconciliation: `emulated` from `session.info` (`running`,
  `turn_started_at`) — `session.status` returns pre-rendered text and is not
  a structured source
- resume/branch/undo: recovery family, `degraded`, separately classified
- compaction (`session.compress`): observed-only
- groups/handoff/delegation/billing/pets/voice/browser/desktop: `unavailable`

## P0 mismatches (updated)

1. **Admission carries no identity** (deepest one): correlation is by
   construction (`session_id`+`seq` only). OAP's submission identity must be
   adapter-owned; the dual-order (response vs opening frames) race must
   reduce deterministically.
2. **No turn lifecycle events**: settlement is a message-shaped frame with a
   status field. OAP run terminals project from `message.complete.status`.
3. **Seq is per-session and restart-reset**; epoch is the native honesty
   marker. Candidate OAP-level decision: first-class restart/cursor
   invalidation.
4. **Replay bounded (512/64) with explicit truncation** — matches OAP's
   degraded replay-with-gaps; `truncated` is the gap contract.
5. **Interaction vocabulary is large and typed**, split across two mechanisms
   (`_block` with `request_id`+expire vs the approval registry with neither).
6. **`btw` is native here and nowhere else.**
7. **Child-scoped steer/interrupt exists natively** (`subagent.*`).
8. **Error frames carry no code** — codes live in optional `error_surface`;
   the adapter synthesizes stable codes and discloses the surface.
9. **Busy-submit statuses (`steered`/`redirected`/`queued`)** are a native
   delivery-mode surface richer than OAP's queue/steer/btw optional modes —
   v1 rejects overlap rather than guessing.
10. **No capability negotiation**; descriptor synthesized.
11. **Strictness divergence**: native reader tolerates and continues on
    malformed lines; adapter fails closed.

## Required evidence corpus

`fixtures/adapters/hermes-v2026.8.31/`, standard five-file cases with
provenance pins to the blobs above. Required labels:

- handshake: `ready-epoch`, `ready-before-input`, `malformed-frame`
- admission: `submit-streaming`, `busy-steered`, `busy-queued`,
  `submit-error-codes`
- run lifecycle: `turn-open`, `completed-turn`, `interrupted-turn`,
  `error-turn`, `error-surface`, `partial-error`
- streaming: `text-deltas`, `reasoning-deltas`, `interim`,
  `scrubbed-provenance`
- tools: `tool-lifecycle`, `tool-failure-in-result`
- interactions: `approval-gate`, `approval-choices`, `clarify-gate`,
  `sudo-gate`, `secret-gate`, `expire-sibling`
- side channels: `btw-delivery`, `background-prompt`
- steering: `steer-run`, `steer-rejected`, `subagent-steer`,
  `subagent-interrupt`
- children: `subagent-lifecycle`, `subagent-complete-failed`,
  `child-mirror-not-terminal`
- replay: `replay-in-window`, `replay-truncated`, `replay-unknown-session`,
  `epoch-restart`
- recovery: `resume-live`, `branch`, `undo`
- reconciliation: `settled-session-info`, `usage-ticker`
- teardown/failure: `stdin-eof-exit`, `process-exit`,
  `pre-ready-observation`, `no-turn-lifecycle-events`
- hygiene: `global-events-unsequenced`, `session-reclaimed`

Gated live-process tests follow the established pattern: spawn
`<python> -m tui_gateway.entry` with an isolated environment, verify
ready/epoch, admission through `message.complete`, stdin-EOF teardown;
credential presence never enables live traffic.

## Process gates (implemented)

`adapter/hermes/process_integration_test.go`, skip-by-default:

- `OAP_HERMES_SMOKE=1` + absolute `OAP_HERMES_BIN` (interpreter) and
  `OAP_HERMES_ROOT` (pinned checkout, gateway cwd): credential-free
  ready/epoch handshake through the production process layer,
  dual-identity `session.create`, idle state, clean stdin-EOF teardown.
  No prompt is submitted, so no provider traffic is possible.
- `OAP_HERMES_INTEGRATION=1` adds the hermetic behavioral gate: the only
  reachable provider is an in-process loopback speaking streaming OpenAI
  chat completions, reached through Hermes's own configuration rather than
  the environment. The gate writes `$HOME/.hermes/config.yaml` selecting
  `model.provider: custom:loopback` and declaring that `custom_providers`
  entry (`base_url` = the loopback, `key_env: OPENAI_API_KEY`,
  `api_mode: chat`, model `gpt-5.6-sol`), so the model resolves to the
  custom provider, not to the built-in `openai` one in the pinned catalog.
  For a named provider the environment cannot redirect this gateway; see
  *Integration gate: PASS* below for the run that proved it. The single
  credential is the test-owned fixture key, env-borne in `OPENAI_API_KEY`
  and read through `key_env`. The gate still also exports `OPENAI_BASE_URL`
  at the same loopback. That is not what redirects, and since both name one
  address the gate cannot by itself show the config entry is sufficient;
  the recorded run with only the variable set is what shows it. Asserts
  started admission, non-empty streamed content deltas, `run.completed`
  with the fixture response, the first loopback request's path, model and
  bearer, post-run idle, clean close. `stream=true` is enforced by the mock,
  which refuses any other request with HTTP 400, rather than asserted by the
  gate.
- Optional `OAP_HERMES_SHA256` (64 hex) binds the interpreter artifact
  for exact-artifact evidence; without it a passing gate is runtime
  evidence only.
- The child environment is fully replaced: isolated HOME/XDG dirs, dead
  loopback proxies with loopback-only NO_PROXY, unbuffered stdio, no
  ambient credentials.

Development environment note: no python interpreter exists on this
machine, so the gates were validated structurally (skip-by-default, and
the spawn path proven fail-fast with non-gateway executables:
immediate-exit → handshake EOF error; `/bin/echo` → strict-codec
non-object rejection; digest and checkout guards verified). The
production process path itself is exercised hermetically by the corpus
process cases via the fixture-server re-exec. A live run requires a
prepared interpreter with the pinned checkout's dependencies.

## Review outcomes (tranche closeout)

Independent adversarial review of the tranche surfaced seven findings,
each independently reproduced before fixing (a8ab597, 657629b):

1. **Resolve panicked on schema-legal approval text answers** — the
   answer oneOf's text form indexed `SelectedOptionIDs[0]` unguarded.
   Fixed: per-gate validation, `ErrInvalidResolution`, gate stays
   resolvable.
2. **Batch clarify collapsed to one respond** — N questions answered by
   a single `clarify.respond` carrying only the last answer, `question_id`
   never sent, and `{status:"expired"}` results projected as submitted.
   Fixed: one respond per question with the batch selector; non-ok
   statuses fail the resolution and restore the binding.
3. **Transport-death settlement was non-deterministic** — identical wire
   input settled as completed/failed/error depending on scheduler
   timing (reproduced 1-in-120 on the corpus; probe: 56/3/1 over 60).
   Fixed: the factory relay closes the relay channel after draining
   everything the reader routed, and dispatch drains to that close
   before the failure terminal; responses decoded before retirement are
   delivered rather than dropped.
4. **`Err()` was a first-writer race** (reader EOF vs process exit),
   flipping frozen evidence. Fixed: reader errors — the specific wire
   truth — always win.
5. **The malformed-frame corpus bytes never reached the wire** — the
   fixture builder skipped decode-error frames, so the label proved
   load-time rejection plus a coincidental exit, not the live
   decoder-plus-teardown path. Fixed: corrupt bytes replay verbatim;
   the rule requires the reader's codec error as the surfaced cause.
6. **Six evidence rules were tautological** — decoding literals defined
   inside the test certifies nothing about the fixtures. The pinned
   shapes (steer statuses, registry-scoped subagent control, replay
   window/truncation/unknown-session, ready epochs) moved to the native
   package's shape tests; the rules now assert behavior only.
7. **Submit returned a non-nil stream with admission errors**, against
   the repo convention. Fixed.

Clean bills: lock ordering (no inversion; calls outside `reduceMu`),
native type validation vs the frozen ledger, codec strictness,
handshake/teardown/redaction. Noted as design debt, not defects: tools/
interactions maps grow per session, dead `inputState.answers` field,
per-call context cancellation retires the whole client, `Cancel` ignores
a `not_interrupted` result.

Post-fix verification: full package suite, 10/10 corpus stress,
120/120 malformed-frame stress, race x2, and the full repository battery
green.

## Live-gate findings (2026-09-10)

The pinned checkout was provisioned (release commit
`29112bef099274229cadff79cdff7bf7b99c4b77`, tree
`daaffc303ae437041b7f76be17c5f61b14f2ce99`; `tui_gateway/entry.py` blob
`27fd051b8aff7cb6e6ddd9103eb14c06d7b2c0e8` — all re-verified) with its core
dependencies installed into a virtualenv, and both gates were run live for
the first time.

### Fixed: two strict-decode defects that failed every real turn

1. **`usage` is an extensible status-bar readout.** The pinned gateway adds
   `active_subagents` (server.py:7351), and may add `avg_latency_s`,
   `avg_tps`, `dev_credits_spent_micros`, each guarded so it "must never
   break usage reporting". The adapter strict-decoded the dict, so the first
   real `message.complete` failed the run with
   `unknown field "active_subagents"`. `Usage` now has a tolerant
   `UnmarshalJSON` and models `active_subagents`; the rest of the payload
   stays strict.
2. **`error_surface` carries the failing provider and model.**
   `build_error_surface_*` captures them (`error_surface.py:145-151`) and
   the adapter's struct modelled only `layer`/`code`/`retryable`, so an
   error turn failed the transport with `unknown field "provider"`.
   `ErrorSurface` now models `provider` and `model`.

Both are pinned by `adapter/hermes/internal/native` regression tests that
fail against the old code (`TestMessageCompleteUsageToleratesExtensibleReadouts`,
`TestErrorSurfaceCarriesProviderIdentity`).

### Smoke gate: PASS

`OAP_HERMES_SMOKE=1`: credential-free `gateway.ready` handshake (fresh
replay epoch), dual-identity `session.create`, idle state, clean stdin-EOF
teardown.

### Integration gate: PASS (after a provider-redirection fix)

`OAP_HERMES_INTEGRATION=1` initially reached no provider: the mock saw zero
requests and the run failed `hermes_timeout: Connection error`. Cause: for a
*named* provider, `OPENAI_BASE_URL` is deliberately ignored as stale env
poisoning (`agent/auxiliary_client.py:6200-6219`), so `gpt-5.6-sol` resolved
to the built-in `openai` provider and the gateway dialled the real endpoint.
Removing the gate's dead-proxy guard made that explicit — the request
reached the real `api.openai.com` and returned `HTTP 401` — so the guard was
doing real hermeticity work, but the redirection itself was ineffective.

Fix: the gate now writes a Hermes `config.yaml` into its isolated home
selecting a `custom_providers` entry (`base_url` = loopback mock,
`key_env: OPENAI_API_KEY`, the pinned model id) and sets
`model.provider: custom:loopback`. That is the pinned, supported redirect;
the environment cannot do it. With it the gate **passes** end to end:
started admission, streamed reasoning and text deltas, `run.completed` with
`fixture response`, the exact loopback `POST /v1/chat/completions` with the
pinned model and bearer, post-run idle, clean close. Both gates pass 3x.

The dead-proxy guard is retained and must stay: it is what bounds the child
to loopback.

# Hermes agent v2026.9.24 mapping ledger

Status: the implementation contract for the Hermes adapter pinned to
`v2026.9.24`. It records only what changed against
[`hermes-v2026.8.31-mapping.md`](hermes-v2026.8.31-mapping.md), which stays
the base for everything not named here: boundary selection, process lifecycle,
framing and codec strictness, sequencing and replay, dual session identity,
the admission model, the side channels, and settlement arbitration. Each claim
below was checked by running the pinned gateway, not only by reading it — with
one exception, stated where it applies: **Session reload at v2026.9.24** at the
end of this ledger is read from the source at this pin's commit and observed
nowhere, and says so in its own first paragraph.

## Provenance

- Repository: `https://github.com/NousResearch/hermes-agent`
- Release: `v2026.9.24` (annotated tag object
  `e3dd27ee2d8b011737a4eea8e3eb3d711ab78690`)
- Release commit: `f97608f178d1ffeca59860195ab7da295f7c8e5f`
- Release commit tree: `5849eacde63aaea608ca418821cc84771fce3bec`

Normative source blobs at the release commit:

- `tui_gateway/entry.py` — `aebfbeb8026b4471d00e34072b8efb51cfd58c67`
- `tui_gateway/transport.py` — `5051d6da51ff0f1323e466f049f09323056cfe03`
- `tui_gateway/server.py` — `475d106c4a9bcf7606921d951ee3d4e08ef63642`
- `tui_gateway/event_replay.py` — `b603a67120bd1f9f9b9c66f50331d4467bf47bd4`
- `tui_gateway/_stdin_recovery.py` — `add5e841df4bead308a96bd8b0f0e3c5475bd167`
- `tui_gateway/ws.py` — `b90253e0c0a319ffcb1ed2d689fc759d56458509`
- `tui_gateway/methods_prompt.py` — `aefcf692321dc828672cd30ea336a97fd69eb69e`
- `tui_gateway/methods_session.py` — `a41aeff7b731f99a69dfc8556b5ade04d30a83f7`
- `tui_gateway/server_requests.py` — `5aaa044fe2257e8e818008047308b0d95bbac360`
- `tui_gateway/rpc_dispatch.py` — `17bb684f1371a3089a4d4814ef7f0ce376fd50ae`
- `tui_gateway/prompt_turn.py` — `32cad0497ea2794dc706a21be6d4e14ceffc3bc6`
- `tui_gateway/contracts/` (tree) — `1eceb85deeeb499c3f00231c6fdedd91a1631753`
- `hermes_state.py` — `8ecc3b5c266a2d01185f70f23d1b6f43e9081ed2`
- `hermes_state_sessions.py` — `1c775a358f3193cbe2d8aa53abbaae625500bc17`
- `hermes_state_common.py` — `3d36055e763bef26de9e67e12e4c3af2a67e8514`

The last three are the store itself — the two `hermes_state*` modules and
`hermes_state_common`, which carries the schema they apply — added when the
session-reload section below was written; everything above them was recorded
when the pin moved.

The release ships no binary artifact, so the catalog records no digest; the
interpreter is whatever the operator provisions, bound per run by
`OAP_HERMES_SHA256`. Between the two tags `tui_gateway/` changed in 97 files
(+33,659/−34,488 lines); `server.py` alone lost about 20,000 lines to
extracted modules.

## How the difference was established

1. **The gateway now declares its wire.** `tui_gateway/contracts/` holds one
   pydantic model per method (params and result), per server→client request,
   and per event payload, and the dispatcher enforces them (below). Every
   method, request and event the adapter touches was dumped from the registry
   and compared with the adapter's pinned types.
2. **The v2026.8.31 corpus was validated against those contracts**, frame by
   frame. Twelve of its eighteen cases validate unchanged. The six that do
   not are `interaction-gates` and `interaction-expire` (their event and
   method vocabulary is gone), `ready-handshake` and `malformed-frame`
   (`session.create` now requires `messages`), `pre-ready-observation` (only
   its null `open` host step, which is not a frame), and `subagent-frames`
   (below).
3. **The real gateway was driven** from a Python 3.12 virtualenv holding the
   release checkout installed editable, with a scripted loopback
   OpenAI-chat-completions provider selected through the isolated home's
   `config.yaml`, an isolated `HOME`/XDG tree, dead loopback proxies, and no
   ambient credential. Scenarios: lifecycle turn, malformed input, the four
   gates in one turn, a clarify withdrawn by interrupt, a batch clarify, a
   busy submit plus steer, an interrupt, a tool call, `prompt.btw` and
   `prompt.background`, and a delegation. Every recorded frame is kept
   verbatim; the recorder also tees the host side, so each host frame in a
   recorded case is what the adapter must write, byte for byte.
4. **The adapters were run against it**: the Go process gates (smoke,
   chat-completion turn, and a new approval turn), three passes each, and
   `oapx serve agent --backend hermes` driven over stdio through an approval
   and a clarify to `run.completed`.

## Wire differences against v2026.8.31

### Interactions are server→client JSON-RPC requests

The largest change, and the one that broke both adapters. In v2026.8.31 a gate
was a sequenced `approval.request` / `clarify.request` / `sudo.request` /
`secret.request` event, answered by the `approval.respond` /
`clarify.respond` / `sudo.respond` / `secret.respond` methods, and withdrawn
by `clarify.expire` / `sudo.expire` / `secret.expire`. None of those events
exist any more, and `clarify.respond`, `sudo.respond` and `secret.respond`
answer `-32601`.

Now the gateway sends a JSON-RPC **request** (`server_requests.py`):

```
{"jsonrpc":"2.0","id":"srq-<12 hex>","method":"approval"|"clarify"|"sudo"|"secret","params":{"session_id":<sid>, ...}}
```

and the client answers it with an ordinary response frame carrying the same
id. Requests carry no `seq`; they are not events and the replay ring does not
hold them. Recorded shapes:

| method | params beyond `session_id` | answer `result` |
|---|---|---|
| `approval` | `command`, `description`, `pattern_key`, `pattern_keys`, `allow_permanent`, `allow_session`, `request_id` (uuid4 hex, the approval-queue entry), `choices` ⊆ `once,session,always,deny`; the contract leaves it open (`extra="allow"`) | `{"choice":…}` (optional `all`) |
| `clarify` | `question` + `choices` (+ `multi_select`), or `questions:[{qid,question,choices,multi_select}]` | `{"answer":…}`, or `{"answers":{qid:…}}` for a batch |
| `sudo` | `command` (redacted) | `{"value":…}` |
| `secret` | `env_var`, `prompt`, optional `metadata` | `{"value":…}` |

- **Identity.** The interaction identity is the JSON-RPC id. The v2026.8.31
  `_block` 8-hex `request_id` is gone from clarify, sudo and secret; approval
  keeps a `request_id`, but it names the approval-queue entry and is never
  echoed by the answer.
- **Withdrawal.** A request that times out or is cancelled (interrupt,
  session close, shutdown) produces one sequenced
  `request.cancel {id, method, reason}` event. Recorded: an interrupt while a
  clarify waits emits `request.cancel` with reason `interrupted` before the
  `session.interrupt` reply.
- **A response that loses the race is dropped.** `resolve_response` settles
  under the same lock as timeout and cancel; an answer for an id that is no
  longer open is logged and discarded, with no error back.
- **An error response means no handler.** For approval the queue entry is
  withdrawn (not denied); for the others the tool sees an empty answer.
- **Clarify choices are shown, not bare.** The first choice arrives as
  `"a (Recommended)"`. Answering with the shown string is accepted; the tool
  strips the label before the model sees it (recorded: `user_response:"a"`).
- **A batch clarify is one request.** Answering all questions in one response
  is what the recording does; the per-question `clarify.lock` method exists
  but the adapter does not use it.
- **A stdio client answers by default.** `client.capabilities
  {server_requests:true}` is required only of WebSocket clients
  (`session_transports.py`); the stdio TUI ships with the backend.
- **Other server requests exist** (`tour`, `window.read`, `terminal.read`,
  `preview.read`, `preview.act`, `vault.*`, `display.install.sudo`). In
  v2026.8.31 their `*.request` events were in the observed-only list and went
  unanswered, which left the agent waiting out each deadline. They are now
  requests the adapter can refuse.

### Inbound params are validated

`rpc_dispatch.py` validates every method's params against its contract and
answers an unknown key with code `4000` (recorded:
`invalid params for session.create: bogus: Extra inputs are not permitted`).
The adapter sends only contract members. Parse errors (`-32700`), non-object
requests (`-32600`) and unknown methods (`-32601`) keep their codes
(recorded).

### Payload additions

All additive, all recorded:

- `message.complete`: `persisted_turn {row_ids, complete, user_row_id?,
  final_assistant_row_id?}`; on an interrupted turn `text` is `null`
  (recorded twice). `error_surface` gains `resets_at` and is open.
- `tool.start`: `preview`, `labels`. `tool.complete`: `todos`, `revision`,
  `labels`.
- Subagent payloads: `delegation_id`; `output_tail` is typed. A new
  `subagent.start` event.
- `session.create` result: `messages` (required) and a typed `info`.
- `session.events.since` result: `open_requests`, the requests still unanswered.
- `session.events.stats` result: `bytes`, `max_bytes_per_session`,
  `max_bytes_process`.
- `usage` gains `context_source`, `context_estimated`, cache and cost readouts.
  It was already decoded leniently.
- `approval.respond` answers `{"resolved": <int>}`, where v2026.8.31 answered a
  bool. The adapter no longer calls it.

### Event vocabulary

New events: `request.cancel`, `subagent.start`, `setup.ready`,
`session.control.update`, `connection.request`, `connection.update`,
`display.install.done`, `display.install.log`, `display.lease`,
`display.status`, `browser.controller.cancel`, `browser.controller.command`,
`moa.aggregating`, `moa.phase`, `moa.progress`, `moa.reference`. Retired: the
seven gate events above and `mcp.setup.*`, `preview.read.*`, `preview.act.*`,
`terminal.read.*`, `window.read.*`, `tour.*`. The contracts registry is the
complete list (73 events), and the Go codec's closed vocabulary is now exactly
that list.

### Delegation re-enters the session

`delegate_task` always runs in the background at the top level: the tool
returns `{"status":"dispatched","mode":"background",…}` and the parent turn
completes. When the child finishes, the gateway opens a turn of its own on the
same session, with no `prompt.submit`. The recording shows two consecutive
`message.start` frames. The adapter treats a turn it did not submit as
foreign activity (unchanged policy): the session becomes unusable and the
next submit is refused. Recorded as `delegation-reentry`.

### Behaviour the recordings confirmed unchanged

The `gateway.ready` shape (`heartbeat` is WebSocket-only), stdin-EOF teardown
with exit 0, the `prompt.submit` statuses (`streaming`; `queued` for a busy
submit), `session.interrupt` → `{"status":"interrupted"}` followed by an
`interrupted` settlement, the per-session `seq` starting at 1 on the first
turn (`session.info` takes seq 1 before `message.start`), and the `btw_` /
`bg_` task ids with their completion events.

Two defaults now make auxiliary LLM calls through the same provider:
`approvals.mode: smart` asks a reviewer model before surfacing an approval,
and title generation runs beside the first turn. Both would consume a
scripted provider's responses, so the process gates set `approvals.mode:
manual` and `auxiliary.title_generation.enabled: false`. A busy `prompt.submit`
during a held first turn also produced an extra `interrupted`
`message.complete` ("Stopped waiting for another Hermes process…") in the
probe. The adapter never submits while busy, so this is recorded but not
mapped.

## Findings the recordings exposed that the move did not cause

- **`thinking.delta` is the spinner, not reasoning**, at this tag as at
  v2026.8.31. The recordings are what exposed it;
  [`hermes-thinking-delta-note.md`](hermes-thinking-delta-note.md) holds the
  evidence and the decision, and this corpus follows it: every
  `thinking.delta` frame is observed-only, and `reasoning.delta` still
  projects.
- **A delta can carry no text.** The spinner is cleared with `{"text":""}`,
  which arrived on every recorded turn and, while `thinking.delta` still
  projected, became a `content.delta` with an empty reasoning part that the
  OAP schema rejects — so no real turn validated. Making `thinking.delta`
  observed-only removes that source, but neither `_fire_stream_delta` nor
  `_fire_reasoning_delta` filters an empty string
  (`agent/stream_delivery.py:286,319`), so a provider emitting an empty chunk
  would put the same invalid part on a channel that does project. Both trees
  therefore drop a delta with no text, on `message.delta` and
  `reasoning.delta` alike, and each tree has a test that fails when the drop
  is removed. No recording carries an empty frame on either of those channels.
- **`subagent-frames` was never a recording.** Its progress, tool and complete
  frames omit `goal`, `task_count` and `task_index`, which both tags always
  set. The case is carried forward as-is, because those frames are
  observed-only and their shape did not change between the tags.
  `delegation-reentry` is the recorded subagent evidence.

## Adapter changes

Go (`go/adapter/hermes`) and Zig (`zig/src/adapter/hermes`), in parity:

- A gate is opened from a server request for the owned native session. A
  request for another session, or with a non-string id, is foreign activity. A
  gate that arrives before the turn opens is held and replayed with the
  buffered events, as events already were.
- Resolving writes the response frame (`{"choice"}`, `{"answer"}`,
  `{"answers"}` or `{"value"}`) with the request's id. There is no reply to
  wait for, so the Zig port no longer holds gateway events while an answer is
  in flight.
- `request.cancel` settles the matching gate `cancelled`. In Go, a cancel
  that lands while the answer is being written wins: the gate resolves
  `cancelled` and `Resolve` returns `ErrInteractionNotFound`, because the
  gateway drops an answer that loses that race. A cancel that arrives after the
  write completed cannot be told apart from the gateway having used the answer,
  and is ignored as a cancel for a resolved gate. The Zig port is
  single-threaded and has no such window.
- A server request the adapter does not map is answered
  `{"code":-32601,"message":"method not found"}`, which the gateway reads as
  "no handler" and stops waiting.
- Approval params are decoded leniently, as the contract leaves them open;
  clarify, sudo and secret stay strict. Validation matches Go in both trees:
  choices within the enum, one clarify form, a named `env_var` and `prompt`.
- Go's native types gain the additive members above, and the closed event
  vocabulary follows the contracts registry.
- The capability revision is `hermes-v2026.9.24-oap-v1`, which the served Zig
  backend carries too: the catalog no longer records an
  `oapx_capability_revision` for any harness, and every served backend now
  serves the Go adapter's descriptor. Support levels are unchanged either
  side of the pin move. The `user_input` reason now says the gates are server
  requests withdrawn by `request.cancel`.

## Corpus (`fixtures/adapters/hermes-v2026.9.24`)

Recorded from the release gateway (frames kept as the exact wire lines):

| case | recording | what it pins |
|---|---|---|
| `ready-handshake` | lifecycle turn | real `gateway.ready`, `session.create` with `messages`, `session.info` at seq 1, spinner deltas |
| `malformed-frame` | lifecycle turn up to the first text delta, then the same injected non-object line the old case used | the codec's refusal ends the run |
| `interaction-gates` | approval, clarify, sudo, secret in one turn | server requests and their answers, byte for byte |
| `interaction-expire` | clarify, then `session.interrupt` | `request.cancel`, and an interrupted settlement with `text:null` and `persisted_turn` |
| `clarify-batch` (new) | two-question clarify | one `{"answers":{…}}` response |
| `delegation-reentry` (new) | `delegate_task` | background dispatch, `subagent.*`, the gateway-initiated turn after settlement |

The sudo recording pointed the terminal at a shim, so no real `sudo` ran and
the fixture password went nowhere. The secret came from a throwaway skill in
the isolated home that declares `required_environment_variables`.

Carried forward unchanged (native side, mappings and omissions copied
verbatim; each validates against the v2026.9.24 contracts except as noted,
and its wire did not change): `admission-busy`, `admission-orders`,
`hygiene-globals`, `pre-ready-observation`, `process-exit`,
`reconciliation`, `recovery-journal`, `replay-epoch`,
`settlement-statuses`, `side-channels`, `steering-unavailable`,
`streaming-provenance`, `subagent-frames` (see above), `tool-lifecycle`.
Their regenerated expectations differ from v2026.8.31's only in the
capability revision. Most of these cases inject faults or foreign traffic that
a live gateway cannot be made to produce on demand.

Nothing needed a credential. The v2026.8.31 version is `retired` in the catalog
and its corpus is removed: its gate frames cannot run through an adapter that
no longer speaks them, so it cannot be a floor.

The Go harness now takes the native session id from the case's own
`prompt.submit`. It delivers server requests and checks each answer by id and
result, and it gains a `cancel` control (drive `Cancel`, expect
`session.interrupt`) and an `answers` list for batch resolutions. The Zig
driver does the same at the reducer level. The `recovery-journal` and
`replay-epoch` cases exercise Go's per-subscriber journal and `Resume`; the
Zig driver skips their resume ops because an adapter needs no journal of its
own — persistence is out of v0.1 core (Decision 0012), and the served
endpoint's bounded journal answers `run.resume` and `run.replay`.

## Process gates

`OAP_HERMES_SMOKE` / `OAP_HERMES_INTEGRATION` are unchanged in name and
isolation. The loopback config adds `approvals.mode: manual` and disables
title generation. `TestHermesProcessApprovalAgainstChatMock` is new: the
scripted model asks the terminal to `rm -rf` a path inside the test's temp
dir, the adapter surfaces the approval, answers `deny` through the
server-request response, and the run completes with the denial in the model's
next request. All three gates passed three times against the release checkout.

## Served by `oapx serve agent --backend hermes`

The Zig port (`zig/src/adapter/hermes/adapter.zig`) drives the corpus reducer
behind a `hermes` registry entry. Where it differs from the Go adapter:

- It advertises the Go adapter's revision, with `run.resume` and `run.replay`
  `degraded` as Go does: the `oapx` endpoint keeps a bounded journal of 256
  events per session and answers the replay control from it.

## Model-provider settings at this pin

Hermes names its provider in `~/.hermes/config.yaml` under `model`, as a mapping
of `provider`, `default` (the model), `base_url` and `api_mode`; a fresh install
carries `model: ""` until `hermes setup` or `hermes model` writes the mapping. It
also reads one environment variable per provider, and the dashboard's Models page
writes the same file.

| Setting | What it sets |
| --- | --- |
| `config.yaml` `model.provider` | the provider id, e.g. `openrouter`, `openai`, `glm` |
| `model.default` | the model id |
| `model.base_url` | an endpoint override for that provider |
| `model.api_mode` | the wire the provider is reached with |
| `OPENAI_API_KEY` + `OPENAI_BASE_URL` | a custom OpenAI-compatible endpoint, the documented pair for a gateway or a local server |
| `AI_GATEWAY_API_KEY` + `AI_GATEWAY_BASE_URL` | Vercel AI Gateway (default `https://ai-gateway.vercel.sh/v1`) |
| `OPENROUTER_API_KEY` + `OPENROUTER_BASE_URL` | OpenRouter |
| `GLM_API_KEY` | z.ai / ZhipuAI |
| `HERMES_CODEX_BASE_URL` | routes the `openai-codex` subscription provider through a proxy |

Documented at this pin in `website/docs/user-guide/configuring-models.md` and
`website/docs/reference/environment-variables.md`.

Two shapes matter for a catalog row. A row on the OpenAI-compatible wire is
routable through the documented `OPENAI_API_KEY` + `OPENAI_BASE_URL` pair, which
is the pair the environment reference names for exactly that case; a row on the
Anthropic wire is not, because the reference documents no Anthropic-compatible
pair. `model.base_url` is a secondary override under a provider that exists.

## Session reload at v2026.9.24

Decision 0039's Hermes row cites the v2026.8.31 ledger, so this section is
the reload at the pin the catalog now names. Every line below is read from
the source at this pin's own commit
`f97608f178d1ffeca59860195ab7da295f7c8e5f` — the same commit and tree as
*Provenance* above, and the four files cited here are the ones that list
their own hashes — `methods_session.py`, `hermes_state.py`,
`hermes_state_sessions.py` and `hermes_state_common.py` — each checked with
`git hash-object` against the value recorded there; anything under
`tui_gateway/contracts/` is covered by that directory's tree hash rather than
listed per file. The last paragraph says what all of this does and does not
establish.

**Hermes has a real reload, and its not-found is typed on the wire.** `session.resume` takes a target that may be a
session id *or* a title, and its common payload (`_resume_response` in
`tui_gateway/methods_session.py`) answers `session_id`, `resumed`,
`message_count`, the `messages` (or `messages_omitted` / `hydrating` when the
caller asks not to have them), `info`, `inflight`, `running`, `session_key`,
`started_at` and `status`, with the todo state attached by
`_attach_todo_state`. So a reload answers the messages *and* the identity the
session's own last route used, in `info.model` / `info.provider`.

**A found session is not guaranteed to carry history, and a not-found is not
guaranteed to mean the store lacked it.** `_resume_locate` tries, in order: the
id (`get_session`), then the title (`get_session_by_title`); then, for a lazy
resume of a child whose first database flush has not landed yet, it proceeds
with **empty** history because the live mirror streams the turn and the row
arrives by upgrade time; then a live but unpersisted session
(`_find_live_unpersisted`, answered by `_resume_live_unpersisted` with
`stored_session_id`); and finally, for a profile-scoped resume only,
`_resume_adopt_stranded`, which copies a lineage stranded in the *default*
store into this profile's database and retries the lookup. A profile that
holds neither the row nor the donor answers `4007 session not found`.

**Where the store lives, and what it does when it is gone.** The store is a
SQLite database whose path is `get_hermes_home() / "state.db"`
(`hermes_state.py`: `DEFAULT_DB_PATH`, `_default_db_path`), and a profile's
own database is `<root>/profiles/<name>/state.db` — the profile is derived
from `db_path` alone, per `SessionSessionsMixin` in
`hermes_state_sessions.py`. A *missing* `state.db` is not an error: on open,
`_connect_and_init` calls `_secure_state_db_files(self.db_path,
create_main=True)`, which creates the file mode `0o600`, and then
`_init_schema()` applies `hermes_state_common.SCHEMA_SQL`. So an empty store
is created on demand and the resume for any id finds no row and answers
`4007` — indistinguishable from a session that never existed. A store that is
present but *unusable* is the case with its own answer: where
`_profile_db(...)` yields no database, the methods answer
`_db_unavailable_error(rid, code=5007)`, which is not a not-found. The
not-found code is also not unique to resume: `session.hidden`'s own lookup
answers `4001 session not found` for the same condition, so an adapter has to
carry the code rather than match the message.

**What a reload restores from the row.** `get_session`
(`hermes_state_sessions.py`) is one `SELECT s.*` with the system prompt and
the tool set resolved by hash out of `system_prompts` and `tool_names`, so the
row carries the session's own `model_config` — decoded tolerantly by
`_parse_model_config`, which takes JSON text or a dict and answers `{}` for
anything else — its `cwd`, and the hashes of the prompt and tool set it ran
under. The reply's `info` is deliberately the *session's* model and provider
(`_live_session_identity`), not the profile default: the source records that a
warm reattach reporting `_resolve_model()` flipped the Desktop picker on every
reload and back once the session was dropped. A second store sits beside the
database — pending messages are appended to `HERMES_HOME/sessions/<id>.jsonl`
when `state.db` was replaced under a live process — so a transcript can exist
for a session the database no longer has.

**Whether a typed not-found has to be manufactured: not here.** `4007` is a
typed absence on the wire, so `unknown_session` can be carried from it rather
than inferred from a null or from silence. Of the seven harnesses read at their
pins, three type theirs — this one, OpenCode (`SessionNotFoundError`) and
DeepSeek (`SessionPersistenceNotFoundError`) — and four do not: pi's discovery
answers `null`, the ACP spec says nothing at all, Codex's reload ledger records
`-32602 invalid_request` and calls it "not a not-found code", and Claude Code's
answer is unrecorded. So the typed rows are three of seven, not a majority, and
the four that do not type one are where a refusal has to be manufactured or
where a fixture is missing. That is the count #448's design has to work from.

**What the Go adapter does with all of it: it never asks.** `Open` in
`go/adapter/hermes/adapter.go` always calls `Factory.Start`, which creates a
*fresh* native session, and it takes the caller's OAP id verbatim
(`id := req.SessionID`) while recording no binding between that id and the
native one; `Resume` in `go/adapter/hermes/session.go` replays the adapter's
own bounded journal. So a reopen naming a previous OAP session id would come
back as an **empty** session under the same id, silently, rather than
refusing — the case 0039's binding record has to remove, and the reason this
ledger matters to #448: the reload already exists on the wire, and the adapter
is one `session.resume` away from it. Nothing above was observed on a running
gateway; it is all read from the source at the pin.

**What the adapter does now (#448, capability revision v4).** Both trees bind a
session to the `stored_session_id` `session.create` reports, and a reopen
starts a fresh gateway and calls `session.resume` with it. The reload was then
observed on the pinned gateway against a loopback provider, by
`TestHermesProcessReopensItsStoredSessionWithTheConversation`: the default cold
path answers `status: "idle"`, the stored messages, and `info.model` as the
model the session last ran under, and the next turn's provider request carries
the conversation from before the restart. A `4007` refuses the reopen.

**A reload can restart work, so that reload is refused.** `_resume_cold` and
`_resume_eager` call `_maybe_schedule_auto_continue`
(`tui_gateway/session_auto_continue.py`), which reads the crash marker
`tui_gateway/turn_marker.py` keeps at `HERMES_HOME/desktop/interrupted_turns.json`
and, when the marker is fresh (fifteen minutes by default) and under its
attempt limit, schedules a continuation turn on a background thread and adds
`auto_continue: {attempt, interrupted_at}` to the reply. Decision 0039 says a
reopen restores without restarting work, so a reply carrying `auto_continue`
refuses the reopen as `unsupported_feature` (`session.open.reopen`,
`unsatisfiable`) and the adapter ends that gateway process, which takes the
scheduled turn with it: the continuation first waits for an agent build, and
the gate observed no provider request after the refusal. `session.interrupt`
is not sent: the gateway did not answer it within ten seconds on a session
still building its agent. A reply still `running`, or with a status other
than `idle`, is refused the same way. The marker survives the refusal, so a
reopen succeeds once Hermes retires it as stale. Both exchanges are in the
corpus as `session-reopen` and `session-reopen-auto-continue`.

## Reasoning level and compaction at v2026.9.24

Recorded for [Decision 0045](../decisions/0045-reasoning-level-and-compaction-policy-are-session-settings.md).
Read from the source at `f97608f178d1ffeca59860195ab7da295f7c8e5f`.

**Reasoning level.**

- `session.create` takes `reasoning_effort` (`tui_gateway/methods_session.py`).
- `config.set` with key `reasoning` changes it on a live session
  (`tui_gateway/methods_config_set.py`). That is the session scope unless
  `scope: "global"` is passed. It replaces the running agent's
  `reasoning_config` and emits `session.info`, and `reasoning` is in the
  gateway's `_SESSION_SCOPED_KEYS`.
- Values are parsed by `hermes_constants.parse_reasoning_effort`: `minimal`,
  `low`, `medium`, `high`, `xhigh`, `max` or `ultra`, while `none`, `false` and
  `disabled` switch reasoning off.

**Compaction.** `config.yaml` under `HERMES_HOME`, section `compression`:

- `enabled` (default true);
- `threshold`, a fraction of the context window (default 0.50; a model under
  512K is floored at 0.75);
- `threshold_tokens`, an absolute cap (default 256000). Compression fires at
  the lower of the two thresholds.

`agent/agent_init.py` reads these when an agent is built, so they take effect
for a session created after the file is written. `config.set` has no
compression key, so nothing changes them on a live session.

## Session settings in the adapter

Decision 0045's settings move the revision to `hermes-v2026.9.24-oap-v2`.
`session.reasoning` is `native` with the `session_open` mode. Right after
`session.create`, both trees send `config.set` with key `reasoning` on the
created session, sending `off` as Hermes's `none`. That scope writes the
session's create-time override and the running agent's `reasoning_config`
rather than `config.yaml`. The adapter's factory creates the session before
it sees the open request, so `session.create`'s own `reasoning_effort` is not
used, and `config.set` validates a level where `session.create` would drop an
unknown one silently. `session.compaction.policy` is `unavailable`:
compression lives in `config.yaml` under `HERMES_HOME` beside the gateway's
credentials, and no gateway method sets it.

## Live session settings

Decision 0045's `session.settings.update.request` is served for
`reasoning_level`, by both trees, with the open's own call: `config.set`
`{session_id, key: "reasoning", value}` on the native session, `off` sent as
`none`. `_set_reasoning` (`tui_gateway/methods_config_set.py`) stores the
session's create-time override and replaces the running agent's
`reasoning_config`, then emits `session.info`, which the adapter already
reduces outside a run. Because it replaces the running agent's config, an
update is refused `run_active` while a run is open, as for Claude Code. A
value `parse_reasoning_effort` rejects answers error 4002, and the update is
refused `unsupported_feature` (unsatisfiable, field `reasoning_level`), as at
open. `compaction_policy` stays `unavailable` and is refused unadvertised.

`TestHermesProcessTakesALiveReasoningLevel` (`OAP_HERMES_INTEGRATION=1`) runs
the gateway from a checkout of `v2026.9.24` (commit `f97608f1`) with its locked
dependencies, opens at `low`, runs a turn, updates to `high` and runs another:
the two chat completions requests carry `reasoning_effort` `low` and `high`.
It passed 3x. `oapx serve agent --backend hermes` served the same update and a
run asking for `high`; the trace validates.

`session.reasoning` adds `session_live`, so the revision moves to
`hermes-v2026.9.24-oap-v3`.

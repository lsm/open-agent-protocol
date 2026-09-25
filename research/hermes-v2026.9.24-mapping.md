# Hermes agent v2026.9.24 mapping ledger

Status: the implementation contract for the Hermes adapter pinned to
`v2026.9.24`. It records only what changed against
[`hermes-v2026.8.31-mapping.md`](hermes-v2026.8.31-mapping.md), which stays
the base for everything not named here: boundary selection, process lifecycle,
framing and codec strictness, sequencing and replay, dual session identity,
the admission model, the side channels, and settlement arbitration. Each claim
below was checked by running the pinned gateway, not only by reading it.

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

- **Empty deltas.** `thinking.delta {"text":""}` clears the spinner and
  arrives on every turn in both tags. Both adapters projected it as a
  `content.delta` with an empty reasoning part, which the OAP schema rejects,
  so no real turn validated. Empty deltas now project nothing.
- **`thinking.delta` is the spinner, not reasoning.** It carries a kaomoji and
  a verb (`"(´･_･`) mulling..."`) from `thinking_callback`, in v2026.8.31 as in
  v2026.9.24. Both adapters still map it to reasoning content, as the old
  ledger specified. The wire did not change, so neither did the mapping. It is
  flagged for a separate change.
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
driver does the same at the reducer level.

## Process gates

`OAP_HERMES_SMOKE` / `OAP_HERMES_INTEGRATION` are unchanged in name and
isolation. The loopback config adds `approvals.mode: manual` and disables
title generation. `TestHermesProcessApprovalAgainstChatMock` is new: the
scripted model asks the terminal to `rm -rf` a path inside the test's temp
dir, the adapter surfaces the approval, answers `deny` through the
server-request response, and the run completes with the denial in the model's
next request. All three gates passed three times against the release checkout.

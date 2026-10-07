# The Work Profile

Status: draft, from [Decision 0047](../decisions/0047-a-work-layer-over-sessions.md)
and its owner answers (2026-10-06). The six verbs and the capabilities answer
below are served by `oapx serve`, and by the repository's internal `goap serve`,
over stdio and HTTP.

Profile: `open-agent-protocol.work`, over `open-agent-protocol.agent-control-core`.

## What it is for

A caller that manages many pieces of work (HyperNeo's Neo, a dashboard, a
phone) asks six questions: where is there work, start some, tell it
something, how is it doing, what was said, stop it. The core answers each, but in session and
run terms, across several calls and an event stream. This profile answers them
in one call each, in terms of a piece of work and one of six statuses.

It is served by `serve` (the multi-session endpoint), next to the hub's own
operations, on both transports. It adds no run machinery: every verb is built
from core operations the endpoint already serves, and where a core
operation has no room for what a verb asks (a per-request directory), the
verb says so and refuses it rather than inventing surface.

## Capability keys

Each verb is a key the endpoint advertises in its `GET /work/capabilities`
answer (stdio op `work.capabilities`, no parameters), per adapter:
`{"adapters": [{"adapter", "directory"?, "any_directory", "verbs": [...],
"native": {"list", "read", "search"}}], "unavailable"?: [{"adapter", "message"}]}`.
`native` says whether the adapter lists its harness's own sessions, reads
their transcripts, and passes a `work.list` search term to its harness's own
list; an adapter that could not be probed is named in `unavailable`.

| Key | Built from | Unadvertised when |
| --- | --- | --- |
| `work.list` | the hub's sessions, the bindings (0046), and each adapter's native list (0047 decision 2) | never; an adapter with no native list contributes only its sessions |
| `work.start` | open with a message (D11, closed) | the adapter does not declare `session.message.submit`, or declares it `unavailable` |
| `work.send` | submit, `delivery: auto` | the adapter does not declare `session.message.submit`, or declares it `unavailable`; on a busy session it queues only where the adapter advertises `session.message.delivery.queue` (0007) |
| `work.status` | session state, plus the latest run's terminal envelope | never |
| `work.stop` | `run.cancel` | the adapter does not advertise `run.cancel` (DeepSeek) |
| `work.read` | the harness's own transcript where the adapter reads one, else the turns `serve` records as messages are submitted and runs end | never |

A verb an adapter cannot serve is refused `unsupported_feature` naming the
core feature it rests on, as the core's own refusal does: `work.stop` over
DeepSeek names `run.cancel`, as decision 0047 says. The `work.*` key only says,
in the capabilities answer, that the verb is missing before it is called.

## A work reference

`{"adapter": "...", "session_id": "..."}` for a session this endpoint holds or
has a binding for, or `{"adapter": "...", "native_id": "..."}` for a harness
session no binding names. `work.start` given a `native_id` adopts that
session (0047 decision 3): it resumes it under a new OAP id and submits the
message. A native id `serve` already holds or has a binding for is not adopted
twice; the message goes to that session instead. One the harness lists as
running is refused `run_active`, and one whose harness fails to list its
sessions is refused `backend_failed`, since a failed list is no evidence it is
idle. An adapter that cannot list at all has no such evidence to consult, so
its session is adopted. `work.status`, `work.send`, `work.stop` and
`work.read` take the OAP id from then on.

An adopted session keeps the harness's own posture: the Claude adapter resumes
it with the user's settings and prompt rather than the clean room `serve`
gives a session it starts. Its binding records `"adopted": true`, so every
later reopen, including one after a restart, resumes it the same way.

## Statuses

| Status | When |
| --- | --- |
| `queued` | the session's state is `queued`, or a submit was admitted and no run started |
| `running` | the session's state is `running` |
| `needs_you` | the session's state is `waiting_for_input`, a run in it names a pending interaction, or the active run's latest `run.status.updated` in the journal is `waiting_for_input` (the Claude adapter reports a gate only there) |
| `done` | idle, and the latest run ended `run.completed` |
| `failed` | idle, and the latest run ended `run.failed`, or the session's state is `error` |
| `stopped` | idle, and the latest run ended `run.cancelled` |

An idle session that has never run is `done` with no `last_reply`.

These six apply only to a session `serve` holds, the only kind with core state
and a journal. Two other kinds of entry appear in `work.list` and carry **no
`status`**:

- **Unheld:** a session with a binding (0046) that `serve` does not hold, such
  as one closed or left behind by a restart. It carries `"held": false` and the
  binding's `state` (`live` or `closed`), nothing projected. A `live` one,
  such as a session left behind by a restart, is listed by default; a `closed`
  one only when `include_closed` is set.
- **Native:** a harness session no binding names (0047 decision 2). It carries
  `native_id` and whatever the harness's own list says, nothing projected.
  Codex lists through `thread/list`, Claude Code and Pi from their stores, and
  an ACP agent through `session/list` in the working directory when its
  `initialize` advertises `sessionCapabilities.list`, which takes a
  short-lived agent process per listing; an agent that does not advertise it
  lists nothing.

`work.status`, `work.read` and `work.stop` answer an unheld entry from the
history (its `held: false` entry, the harness's transcript or no turns, nothing to stop) without starting
a harness. `work.send` reopens it through its binding (0040) and then submits,
so a reference survives a restart of `serve`; an adapter without
`session.open.reopen` refuses that, as an open would. A native entry is
taken over only by `work.start` with its `native_id`; every other verb refuses
its id `unknown_session`.

## Operations

### `work.status`

Request `{"ref": <reference>}`. Answer:

```json
{
  "ref": {"adapter": "codex", "session_id": "session-1"},
  "status": "done",
  "directory": "/Users/me/project",
  "title": "...",
  "run_id": "run-3",
  "last_reply": "...",
  "updated_at_ms": 1791300000000
}
```

A session waiting on a prompt answers `"status": "needs_you"` with
`"pending": {"interaction_id": "..."}`, the id the core's resolve takes. A
`kind` and a `summary` of what is asked are a later addition. A piece also
carries `link` when its adapter can name one (`codex://threads/<id>`,
`claude://claude.ai/epitaxy/<local id>`).

`title` is the title `work.start` was given, which `serve` keeps for as long
as it holds the session; a session opened any other way has none, and the
member is absent. `last_reply` is the latest `run.completed`'s `final_response` content,
truncated to 4 KiB. `pending` is present only when `status` is `needs_you`.

### `work.list`

Request `{"directory"?, "adapters"?, "include_closed"?, "include_native"?, "limit"?, "cursor"?, "search"?}`.
Answer `{"groups": [...], "next_cursor"?, "unavailable"?}`, where each group is
`{"directory", "last_activity_ms", "work": [<entry>...]}`, where an entry is a
held session's `work.status` answer or an unheld or native entry as
[Statuses](#statuses) describes,
newest first. A group appears even when it holds no open work, so a caller can
start work in a place it named. `limit` is 1 to 100, default 50, counting
pieces of work; the cursor is opaque, as in 0046.

- `directory` keeps only work whose directory is exactly that one. When it
  names a place no work is in, the first page still answers that place's group,
  empty. An adapter with `any_directory` lists its own sessions for that
  directory; any other adapter lists its working directory's, and the filter
  then applies.
- `adapters` keeps only work from the adapters it names, and only they are
  asked for native lists. An empty or absent list keeps every adapter.
- Order is by last activity, newest first, and pieces with the same time are
  ordered by a key: `s:<session id>` for a held or recorded session and
  `n:<adapter>/<native id>` for a native one. Groups are formed within a page.
- `next_cursor` is present when more work follows the page. A cursor carries
  the position of the page's last piece and how many pieces have been
  answered so far. The next page answers what sorts after that position, so
  work that became newer in between is not repeated. Native lists are asked
  for that many pieces plus one more page and one row, up to 500 per adapter.
- A `limit` out of range, a cursor this list did not issue, or a member of the
  wrong type is refused `invalid_request`. Unknown members are ignored.
- Over HTTP, `GET /work` takes the same members as query parameters, with
  `adapters` comma-separated.
- OpenCode's own list follows the server's cursor (`GET /api/session?cursor=`)
  until it has the rows asked, up to 16 pages.
- `search` hands a term to the native lists that take one, and the answer is
  what they matched. Codex passes it as `thread/list`'s `searchTerm`, and
  OpenCode as `GET /api/session`'s `search`, which matches the title, ignoring
  case, and which the server's cursor carries to later pages; `native.search`
  in `work.capabilities` names the adapters that pass it. A match is answered
  as the work it is: a held session's entry when `serve` holds the harness
  session, the history's entry when one binds it (a closed one only on
  `include_closed`), and a native entry otherwise. Nothing else is listed, since
  `serve` matches nothing itself: held and recorded work the harness did not
  match is left out, and so is every adapter whose list takes no term.
  `search` implies `include_native`, and `directory`, `adapters`, `limit` and
  `cursor` apply as they do to a list.

`work.list` lists; `serve` does not search. Finding work by what was said in
it is the caller's job: a control layer such as HyperNeo keeps its own index,
ranks by its own rules and knows what its user is looking for. OAP's part is to
hand over what such an index needs. Today that is the list and each entry's
`last_reply`; reading what was said is `work.read`. Where a harness's own list
takes a search term, `search` passes one through, but `serve` builds no index
of its own and matches nothing itself.

### `work.start`

Request `{"adapter", "directory"?, "title"?, "message"}`. Opens a session on
`adapter` with `message` as its first message (D11) and answers its
`work.status`.

The core's `openRequest` carries no working directory: a session runs in its
adapter's configured `working_directory`. A `directory` naming another place is
served only by an entry marked `"any_directory": true` (`drafts/hub.md`, the
registry), which builds an adapter for that directory, and must then be
absolute. Over any other entry it is refused `invalid_request` with the
configured `working_directory` in its details, so the caller learns where the
adapter runs from the refusal.

### `work.send`

Request `{"ref", "message"}`. Submits with `delivery: auto`: it starts a run
on an idle session. On a busy one it queues where the adapter advertises
`session.message.delivery.queue` (only OpenCode among the pinned harnesses
today) and is otherwise refused `run_active`, the core's own answer to a busy
`auto` it cannot queue (Decision 0007); the caller waits for the run to end or
stops it. Answers `work.status`.

### `work.stop`

Request `{"ref"}`. Cancels the active run. Answers `work.status`. A session
with no active run answers its status unchanged.

### `work.read`

Request `{"ref", "after"?, "limit"?}`. Answer `{"turns": [...]}`, each turn
`{"index", "role", "text", "run_id"?, "outcome"?, "at_ms"}`, oldest first,
after index `after`, at most `limit` (1 to 500, default 100).

Where the adapter can read the harness's own transcript (Codex through
`thread/turns/list`, Claude Code from its project's `<native id>.jsonl`, Pi
from its session file, reading the file in order, so a branch Pi left reads
along with the one it kept, OpenCode through `GET /api/session/<id>/message`,
an ACP agent through the replay `session/load` sends when its `initialize`
advertises `loadSession`, in a short-lived agent process),
`work.read` answers from it: every user message
and the reply that followed, the conversation before `serve` held the session
included, and still there after `serve` restarts, so `serve` writes no message
text of its own to disk. A turn there carries no `run_id` or `outcome`; its
index is its place in the transcript, and its text is cut at 64 KiB. A session
`serve` no longer holds is read the same way from its binding's native id. A
held session whose harness transcript is still empty, or cannot be read, is answered as below; a
session `serve` no longer holds then answers no turns. Hermes is listed but not
read: its gateway reads a stored session only by resuming it, and a resume can
restart a turn a crash interrupted. A Codex read pages only as
far as `after` and `limit` need, with no cap of its own. A harness's own list and read run off `serve`'s
loop, on a thread of their own: the loop goes on answering other requests and
delivering events, and answers the `work.list` (with `include_native`) or
`work.read` that asked once the harness has answered. The Codex adapter keeps
one app-server for its lists and reads, in both trees, started on first use and
again only if it dies. A call that finds it busy runs on an app-server of its
own, and a call that breaks a reused one is retried once on a fresh one; an
error the app-server answers keeps it.

Otherwise `serve` answers from what it recorded: a `user` turn for each message
a submit admits (from `work.start`, `work.send` or the core's own submit) and an
`assistant` turn when a run ends, with `outcome` (`completed`, `failed`,
`cancelled`) and, for a completed run, its reply text. It keeps the last 512
turns of a session it holds, each cut at 64 KiB on a character boundary; an
index stays stable as old turns drop. That record is in memory only, so it is
gone after a restart, and a session `serve` no longer holds answers no turns.

This is the content a caller's own search indexes (see `work.list`).

## Order of work

1. All six verbs over the sessions `serve` holds (#919).
2. Native lists in `work.list` (Codex `thread/list`, ACP `session/list`,
   OpenCode `GET /api/session`, Hermes `session.list`; Claude and Pi read-only from their
   stores). Hermes rows carry no working directory, so they are grouped under
   an empty `directory` rather than under the adapter's own.
3. Adoption by native reference: `attach`, `resume`, `observe`, refuse.

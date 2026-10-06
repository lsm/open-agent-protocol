# The Work Profile

Status: draft, from [Decision 0047](../decisions/0047-a-work-layer-over-sessions.md)
and its owner answers (2026-10-06). Nothing here is executable yet.

Profile: `open-agent-protocol.work`, over `open-agent-protocol.agent-control-core`.

## What it is for

A caller that manages many pieces of work (HyperNeo's Neo, a dashboard, a
phone) asks five questions: where is there work, start some, tell it
something, how is it doing, stop it. The core answers each, but in session and
run terms, across several calls and an event stream. This profile answers them
in one call each, in terms of a piece of work and one of six statuses.

It is served by `serve` (the multi-session endpoint), next to the hub's own
operations, on both transports. It adds no run machinery: every verb is built
from core operations the endpoint already serves, and where a core
operation has no room for what a verb asks (a per-request directory), the
verb says so and refuses it rather than inventing surface.

## Capability keys

Each verb is a key the endpoint advertises in its `GET /work/capabilities`
answer (stdio op `work.capabilities`), per adapter:

| Key | Built from | Unadvertised when |
| --- | --- | --- |
| `work.find` | the hub's sessions, the bindings (0046), and each adapter's native list (0047 decision 2) | never; an adapter with no native list contributes only its sessions |
| `work.start` | open with a message (D11, closed) | the adapter refuses an open's message |
| `work.send` | submit, `delivery: auto` | never |
| `work.status` | session state, plus the latest run's terminal envelope | never |
| `work.stop` | `run.cancel` | the adapter does not advertise `run.cancel` (DeepSeek) |

A verb an adapter cannot serve is refused `unsupported_feature` naming the
core feature it rests on, as the core's own refusal does: `work.stop` over
DeepSeek names `run.cancel`, as decision 0047 says. The `work.*` key only says,
in the capabilities answer, that the verb is missing before it is called.

## A work reference

`{"adapter": "...", "session_id": "..."}` for a session this endpoint holds or
has a binding for, or `{"adapter": "...", "native_id": "..."}` for a harness
session no binding names. A native reference is only ever answered by
`work.find`; `work.start`, `work.send` and `work.stop` take a native reference
only once adoption (0047 decision 3) lands, and refuse it until then.

## Statuses

| Status | When |
| --- | --- |
| `queued` | the session's state is `queued`, or a submit was admitted and no run started |
| `running` | the session's state is `running` |
| `needs_you` | the session's state is `waiting_for_input`, or a permission or input request is pending |
| `done` | idle, and the latest run ended `run.completed` |
| `failed` | idle, and the latest run ended `run.failed`, or the session's state is `error` |
| `stopped` | idle, and the latest run ended `run.cancelled` |

An idle session that has never run is `done` with no `last_reply`.

These six apply only to a session `serve` holds, the only kind with core state
and a journal. Two other kinds of entry appear in `work.find` and carry **no
`status`**:

- **Unheld:** a session with a binding (0046) that `serve` does not hold, such
  as one closed or left behind by a restart. It carries `"held": false` and the
  binding's `state` (`live` or `closed`), nothing projected. A `live` one,
  such as a session left behind by a restart, is listed by default; a `closed`
  one only when `include_closed` is set.
- **Native:** a harness session no binding names (0047 decision 2). It carries
  `native_id` and whatever the harness's own list says, nothing projected.

`work.status` answers only a held session; for any other it refuses
`unknown_session`, as `state` does.

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
`"pending": {"kind": "permission", "interaction_id": "...", "summary": "..."}`
and no `last_reply`.

`title` is the title `work.start` was given, which `serve` keeps for as long
as it holds the session; a session opened any other way has none, and the
member is absent. `last_reply` is the latest `run.completed`'s `final_response` content,
truncated to 4 KiB. `pending` is present only when `status` is `needs_you`.

### `work.find`

Request `{"text"?, "directory"?, "adapters"?, "include_closed"?, "limit"?, "cursor"?}`.
Answer `{"groups": [...], "next_cursor"?}`, where each group is
`{"directory", "last_activity_ms", "work": [<entry>...]}`, where an entry is a
held session's `work.status` answer or an unheld or native entry as
[Statuses](#statuses) describes,
newest first. A group appears even when it holds no open work, so a caller can
start work in a place it named. `limit` is 1 to 100, default 50, counting
pieces of work; the cursor is opaque, as in 0046.

`text` matches a title, a directory or a last reply, case-insensitively. Full
text over transcripts is out of scope here.

### `work.start`

Request `{"adapter", "directory"?, "title"?, "message"}`. Opens a session on
`adapter` with `message` as its first message (D11) and answers its
`work.status`.

The core's `openRequest` carries no working directory: a session runs in its
adapter's configured `working_directory`. So `directory`, when given, must
name that directory, and any other is refused `unsupported_feature` naming
`work.start.directory`. Starting work in a directory no adapter is configured
for needs the open to carry a directory, which is a core change of its own and
not part of this profile; until then a caller registers one adapter entry per
place.

### `work.send`

Request `{"ref", "message"}`. Submits with `delivery: auto`: it starts a run
on an idle session and queues on a busy one. Answers `work.status`.

### `work.stop`

Request `{"ref"}`. Cancels the active run. Answers `work.status`. A session
with no active run answers its status unchanged.

## Order of work

1. `work.status` and `work.find` over the sessions `serve` holds.
2. `work.start`, `work.send`, `work.stop`.
3. Native lists in `work.find` (Codex `thread/list`, ACP `session/list`,
   OpenCode, Hermes; Claude and Pi read-only from their stores).
4. Adoption by native reference: `attach`, `resume`, `observe`, refuse.

# Triggered Runs and Background Tasks

Status: proposal. The owner decided on 2026-10-18 that OAP support triggered
runs, and that:

- every host accepts them;
- the trigger is a member of `run.started`;
- the trigger kinds are the four below;
- one run executes at a time;
- background tasks, schedules and restarts are designed now rather than
  deferred.

Nothing in the design is left open. What each adapter still has to record
against its pin is listed under *Where it lands*.

Base protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`

A harness can start a turn because something inside it fired, not because the
host submitted a message. Several kinds of trigger do this:

- a background job settles;
- a timer or cron schedule fires;
- a message arrives from a peer or a monitor.

Each of these starts a turn the model answers. OAP has no run the host did not
submit, so today every adapter either drops such a turn or ends the session
over it. This draft makes such a turn a first-class run, a **triggered run**.
It also makes the work behind most triggers, a **background task**, visible,
readable and cancellable from the host, and it says what happens to both
across a close and a reopen.

## Evidence

### Recorded on 2026-10-18

The method and the full probe table are in the
[Claude 2.1.302 ledger](../research/claude-code-agent-sdk-2.1.302-mapping.md#triggered-turns-tasks-and-schedules-recorded-2026-10-18)
and the
[DeepSeek `rc.2` ledger](../research/deepseek-harness-dsh-v0.2.1-rc.2-mapping.md#the-runtimes-own-turn-from-its-session-log).

**Claude Code 2.1.302** (binary digest `5262ce9d…6662d37`, the catalog's), driven
over `stream-json` against the `providertest` loopback.

- **Setup:** HOME and `CLAUDE_CONFIG_DIR` were temporary, a `security`
  stand-in came first on `PATH`, and `sandbox-exec` denied the keychain daemons
  and every non-loopback connection. The initialize declared
  `perTaskStopAffordance`.
- **A background job settles while the session is idle.** A turn follows at
  once. Its `result.origin` is
  `{kind: "task-notification", producer: "session-task", runId}`, and `runId`
  names the run that started the job. The model sees the notice as a
  `<system-reminder>` that begins `[SYSTEM NOTIFICATION - NOT USER INPUT]`.
- **A job settles while a host prompt is queued.** Turn A started a job, then
  held its next model request. The job settled during that request, the host
  sent B while A was still running, and A ended. **The notification's turn ran
  before B**, and B's turn followed.
- **A job settles while a turn is still calling tools.** The notice is
  **folded**: it rides into that turn's next model request, and no further
  turn follows.
- **A one-shot `CronCreate` fires.** It fires about 40 s later into its own
  turn. That turn is bracketed by `command_lifecycle` `started` and
  `completed` frames sharing a `command_uuid`. Its `result` carries **no
  `origin`**, and the model sees the cron's prompt as a plain user message.
- **Stopping a task.** `stop_task` on a running background command answers
  `success`, reports `task_updated {status: "killed"}` and
  `task_notification {status: "stopped"}`, and **starts no turn**.
- **Reading a task's output.** `get_task_output` answers
  `{output, total_bytes, truncated}`, the whole output so far. It works while
  the task runs and after it ends.
- **A session cron survives `--resume`.** A session-only recurring cron is
  still listed by `CronList` after `--resume` of the same session, with the
  same id. A fresh session in the same directory lists none.
- A foreground `Bash` call that runs past a few seconds also emits
  `task_started` and `task_notification`. These frames alone do not mean
  background work.
- **Cancelling crons from the host.** Only the model can delete a cron,
  through `CronDelete`; the SDK has no host control for it.
  - No control request in SDK 0.3.302 deletes a cron, and `CronDelete` is a
    model-only built-in tool that `mcp_call` cannot reach.
  - `stop_task` with the cron's id answers `success` and leaves the cron
    scheduled, as a later `CronList` shows.
  - The Stop hook's `session_crons` and `UserPromptSubmit`'s
    `source: "schedule_wakeup"` only observe.
  - Removing a durable cron's entry from `.claude/scheduled_tasks.json` does
    stop it (recorded), but OAP never writes a harness's own files, so no
    adapter uses that.
- **Messaging a background agent.** `send_task_message` to a running
  background agent answers `Unsupported control request subtype:
  send_task_message` on this connection.

**DeepSeek harness `rc.2`**: the runtime's own session log of the
`background-job` run.

- After `job/settled`, the notice enters the inbox as a `next-turn` splice,
  and `turn/start` opens turn 2.
- The notice is replayed as a `user/message` whose `source` is
  `{kind: "tool-jobs", form: "notice", summary}`, so the turn is attributable
  from the wire.

### Read from source

- **Claude Code** (SDK types 0.2.168 and 0.3.268, and the 2.1.302 binary's
  strings):
  - **Origin kinds:** `human`, `channel`, `peer`, `task-notification` (with
    subkind `scheduled-trigger`, `peer-send-message`, `projects-relay` or
    `session-inbox`), `coordinator`, `unclassified`, `observer`,
    `auto-continuation` and `observer-activity`.
  - **Task statuses:** `pending`, `running`, `paused`, `completed`, `failed`
    and `killed`.
  - **Level frames:** `system/background_tasks_changed` is the full live set,
    with an `ambient` flag.
  - **Controls:** `stop_task`, `get_task_output`, `send_task_message` and
    `background_tasks`.
  - **Schedules:** crons are created through `CronCreate`, `CronDelete` and
    `ScheduleWakeup`.
    - A cron is session-only unless `durable: true`, which writes it to
      `.claude/scheduled_tasks.json` with its `createdBySessionId`.
    - A recurring cron auto-expires after 7 days.
    - At the next start, a missed durable one-shot is surfaced to the model
      with an instruction to ask the user before running it.
    - Stop and SubagentStop hook inputs list `session_crons`.
  - **Interrupts:** without `perTaskStopAffordance`, an interrupt kills the
    session's background tasks.
- **DeepSeek harness `rc.2`** (`docs/subsystems/{jobs,schedule,core}.md` and
  `packages/jobs/tool-jobs`):
  - **Notices:** a busy owner gets a completion notice injected into its
    `next-step` inbox, so it is folded. An idle owner is woken with a follow-up
    turn. A job the model collected, killed, or lost in teardown gets no notice,
    and neither does an **unowned** job.
  - **Lifetime:** jobs and pending notices end with their owning session.
  - **Durable reminders** (`schedule_*` tools) exist, but only in the web host
    composition, not on the stdio SDK server the adapters speak. That host
    restores a cold session to deliver them, and sends only the latest missed
    occurrence.
  - The inbox claims one `next-turn` message per turn, in order.
- **Codex 0.163.0** (`rust-v0.163.0`): a message from another agent with
  `trigger_turn` starts a turn on an idle thread
  (`maybe_start_turn_for_pending_work`). `thread/backgroundTerminals/list`,
  `clean` and `terminate` are experimental requests.
- **Hermes v0.22.2** (`tui_gateway/session_notifications.py`): background
  completions, loop and heartbeat wake-ups, and kanban events each claim an
  idle session and run a turn through `_run_prompt_submit`; a racing user
  prompt wins. `background.complete` and `cron.changed` are events.
- **OpenCode v2.0.32** (`packages/core/src/session/session.ts`,
  `subagent-completion.ts`, `tool/plugin/shell.ts`, `execution/restart.ts`):
  - When a background shell command or a background subagent ends, OpenCode
    writes a synthetic message into the owning session through
    `sessions.synthetic`. That call resumes the session unless `resume: false`
    is passed, which only restart notices do. So a completion starts a turn on
    an idle session.
  - A subagent's message carries `metadata.source: "subagent"`.
  - Background shells still running when the server restarts are reported
    cancelled.
  - The job service is not exposed on the HTTP API the adapter speaks.
- **pi** has no trigger. **ACP** has no turn an agent starts without
  `session/prompt`.

## Triggered runs

### Admission

1. A triggered run is admitted by its own `run.started`, which carries a
   `trigger` member. That event is the run's admission and its start at once,
   and it is the run's sequence 1.
2. After that, every rule of Decisions 0001, 0002, 0007 and 0013 applies
   unchanged except admission, which rule 1 replaces, since a triggered run
   has no submit and no admission response: contiguous sequence, tools, interactions, cancel evidence, steer
   where advertised, and one absorbing terminal. The one exception is
   Decision 0007's admission order, which rule 2 under *One run executes at a
   time* amends.
3. A `run.started` with `trigger` for a run a submit reserved is invalid. So is
   one without `trigger` for a run nothing reserved, which is the conviction
   Decision 0007 already makes.
4. The adapter mints the run id, as it does for a submitted run when the
   harness has no run identity of its own, unique within the session.

### The trigger

| member | meaning |
| --- | --- |
| `kind` | `background_task`, `schedule`, `message`, or `other` |
| `input` | optional content blocks: the notice or prompt the harness gave the model |
| `task_id` | the task that fired, when one did; it names a task this session has announced |
| `source_id` | optional opaque id of a sender, for `message`; scoped to the session, never an OAP identity |
| `extensions` | the native origin, namespaced |

How each harness's native origin maps to a kind:

| harness | native | kind |
| --- | --- | --- |
| Claude | `task-notification`, no subkind | `background_task` |
| Claude | `task-notification` / `scheduled-trigger`; a cron fire (no origin, inside `command_lifecycle`, prompt matching a known cron) | `schedule` |
| Claude | `peer`, `channel`, `observer`, and `task-notification` with `peer-send-message`, `projects-relay` or `session-inbox` | `message` |
| Claude | `coordinator`, `auto-continuation`, `unclassified`, `observer-activity`, or a turn the adapter did not submit and cannot attribute | `other` |
| DeepSeek | `source.kind: "tool-jobs"` | `background_task` |
| Codex | inter-agent mail with `trigger_turn` | `message` |
| Hermes | a background completion | `background_task` |
| Hermes | a loop or heartbeat wake-up | `schedule` |
| Hermes | a kanban or delegation event | `message` |
| OpenCode | a synthetic shell or subagent completion | `background_task` |

An adapter tells a triggered turn from its own submission with the
submission's echo:

- **Claude:** the `user_message_uuid` the adapter stamps on every submit.
- **DeepSeek:** the prompt's `messageId`.
- **Hermes:** whether the adapter sent `prompt.submit`.

A turn without its echo is triggered.

### Every host accepts them

- A host must expect a run it did not submit on any session, and an SDK must
  deliver one rather than drop it.
- `capabilities.response` reports `session.run.triggered` with the usual
  fidelity, so a host can tell whether the harness produces them. It is a
  report, not a switch: no request elects or refuses it. ACP and pi report
  `unavailable`.
- An endpoint never absorbs a triggered turn it can delimit. An adapter that
  cannot tell where an unprompted turn ends falls back to the announced
  closure in
  [harness-initiated-state.md](harness-initiated-state.md#a-harness-that-ends-the-session).

### One run executes at a time

Decision 0007 already allows one started run at a time, with advertised
reservations behind it executing in admission order. A triggered run fits
inside that rule as follows.

1. **It starts only on an idle session.** A triggered run may start only when
   no run of the session is started and nonterminal. A `run.started` with
   `trigger` while another run is executing is `illegal_run_transition`. Every
   harness surveyed starts its own turn only on an idle session.
2. **It may go ahead of waiting reservations.** Claude did exactly that in the
   recording above, so the rule follows the harness.
   - A triggered run may start while reservations wait. It enters the
     admission order at its start, ahead of every reservation still queued.
     Those reservations keep their relative order and promote after it ends.
   - This adds a second exception to Decision 0007's "in admission order",
     beside its pre-start terminal, and the validator does not report it as
     `queue_order_violation`.
   - A reservation's `queue_position` counts reservations only, so it does not
     change.
3. **A trigger that fires during a run is never a second run.** The harness
   either holds the notice or folds it into the executing run.
   - **Folded:** Claude folds a notice that arrives while the turn still calls
     tools, and DeepSeek folds every notice for a busy owner. The notice is
     context inside that run, and no triggered run exists. The task's
     `session.task.ended` still tells the host what happened.
   - **Held:** Claude holds a notice that arrives during the turn's final
     model request. When the run ends, the notice starts a triggered run.
4. **A submit during a triggered run** is admitted like any submit against an
   executing run. Under Decision 0007 that means a reservation where
   `session.message.delivery.queue` is advertised, and `run_active` otherwise.
   Under Decision 0013 it may be a steer where steer is advertised and
   requested.
5. **A submit racing a trigger** is resolved from the harness's own order, and
   the adapter reports one of the legal shapes truthfully. It never reports
   one turn as two runs, or two turns as one.
   - **The trigger's turn began first:** the submit is answered as in 4.
   - **The prompt's turn began first:** the submit is `started`, and the
     trigger is held or folded, as in 3. Hermes makes this the outcome of
     every race, since its triggers claim only an idle session.
   - **The harness merges the prompt and the notice into one turn:** the run
     is the host's, admitted by its submit, and the notice is folded. Claude
     can join queued messages into one prompt.
   - An adapter whose harness takes a prompt into a turn already running must
     not hand the harness a prompt it has answered `run_active`. It holds the
     prompt until it knows the session is idle, or answers the submit after
     the harness has.
   - Each adapter's corpus carries the race.
6. **Cancelling a triggered run** follows Decision 0001. It cancels the run,
   not the task or schedule that fired it, and cancelling a task never settles
   a triggered run that already started. A Claude adapter therefore declares
   `perTaskStopAffordance`, so the interrupt behind `run.cancel` spares
   background tasks.
7. **Interactions** in a triggered run wait for an answer like any run's, and
   `state.active_runs` lists them under `pending_interactions`. A hub session
   with no subscriber keeps the run waiting: the protocol neither refuses
   nor cancels it on its own.
   - A submit that queues behind a triggered run waiting on an interaction
     gets a queued response naming the blocking interaction ids, so a
     returning user's message is not silently stuck behind a question they
     cannot see. That member is a schema change, listed under *Where it
     lands*.
   - A host should surface that interaction first.
   - A timeout is host policy, never a protocol default. At expiry the host
     either answers the interaction (for example, denies it) or cancels the
     run, and either way the outcome is reported, never dropped.
8. **State and recovery.** `state.active_runs` always lists a nonterminal
   triggered run with its `trigger`, even where Decision 0007 would let
   `active_run_id` alone carry a single started run. Without the entry, a host
   that reconnects cannot tell the run from its own. A recovered
   triggered run enters the admission order at the position it started at, as
   Decision 0007 does for any recovered run.
9. **Settings and model switches** during a triggered run follow the same
   rules as during any run.
10. **Wake loops are the harness's to bound.** A triggered run can start work
    whose completion triggers again. DeepSeek bounds this with
    `maxConsecutiveWakes`. OAP adds no bound and reports each run.

## Background tasks

A **background task** is work the harness tracks for a session beyond the tool
call or turn that started it.

### What counts

- **Included:** a command or agent the harness reports as backgrounded. That
  is Claude's `is_backgrounded` at start or in a later `task_updated` patch,
  and a DeepSeek job. Schedules and watchers are included too.
- **Excluded:** a foreground call that blocks its turn. Claude reports one as
  a task, but it is the tool call's lifecycle and stays inside the run.
- **Excluded:** work the harness does not attribute to this session, namely
  DeepSeek's unowned jobs and another session's durable crons. A session's
  task list is that session's, and OAP has no endpoint-wide task list.

### The task

| member | meaning |
| --- | --- |
| `task_id` | an id the endpoint mints, unique within the session like a run id; the harness's own id goes in `extensions`, and the endpoint maps between the two for cancel and output |
| `kind` | `command`, `agent`, `schedule`, or `other`; the native type in `extensions` |
| `label` | the harness's description |
| `status` | `pending`, `running`, `paused`, `completed`, `failed`, or `cancelled` |
| `run_id`, `tool_call_id` | the run and call that created it, when one did |
| `ambient` | true for housekeeping a host should leave out of activity indicators (Claude's `ambient`) |
| `summary`, `cause` | once ended: the harness's summary, and why it ended (the causes under *Events*) |
| `extensions` | the harness's own id and type, namespaced |
| `cancellable` | whether `session.task.cancel` can reach it |
| `schedule` | for a `schedule`: `expression` when the harness has one, `time_zone`, `next_fire_at_ms`, `recurring`, `durable`, `expires_at_ms` |

### Events

These are session-scoped and carry the session's state-domain `sequence`, the
same counter as `session.state.updated`.

| event | carries |
| --- | --- |
| `session.task.started` | the task |
| `session.task.updated` | `task_id` and the members that changed: `status` (`paused` and back), `next_fire_at_ms` after a fire, `label` |
| `session.task.ended` | `task_id`, `status` (`completed`, `failed` or `cancelled`), an optional `summary`, and `cause` (`finished`, `cancelled_by_host`, `cancelled_by_model`, `expired`, `session_closed`) |

A schedule ends when a one-shot fires, when it is deleted, or when it
expires. Each fire of a recurring schedule is a `session.task.updated`, and
the run the fire triggers names it in `trigger.task_id`.

### State and requests

- **`session.state.tasks`** lists the session's tasks, live and ended, each
  with its current status. An ended task carries its `summary` and `cause`.
  - A host that reconnects after a task ended still finds out how it ended,
    because session events are not replayed.
  - Ended tasks are kept until the session closes, up to a bound the
    endpoint chooses. Past the bound the oldest ended tasks are dropped, and
    `tasks_truncated: true` says so.
  - The state document is authoritative, as for runs.
- **`session.task.cancel.request {session_id, task_id}`** answers
  `{outcome: "requested" | "already_ended" | "not_cancellable"}`.
  `not_cancellable` answers a task whose `cancellable` is false, and the
  adapter then sends the harness nothing. That avoids, for example, Claude's
  `stop_task` on a cron, which reports success and does nothing. Otherwise the
  ending arrives as
  `session.task.ended`, because a cancel is intent and not settlement, as for
  runs. A host's cancel starts no triggered run, which matches the Claude
  recording.
- **`session.task.output.request {session_id, task_id, from?}`** answers
  `{output, next, truncated, ended}`.
  - `from` and `next` are byte offsets into the task's UTF-8 output. `next`
    always falls on a code-point boundary, so `output` never ends mid
    character, and a `from` that does not is refused `invalid_request`.
  - `truncated` says bytes before `from` are gone.
  - An adapter over a harness that returns the whole output, as Claude does,
    slices it itself.

### Capabilities

| feature | Claude 2.1.302 | DeepSeek `rc.2` (stdio SDK server) | Codex 0.163.0 |
| --- | --- | --- | --- |
| `session.tasks` | `native`: task frames, `background_tasks_changed` | `emulated`: `job/registered`, `job/settled` | not established: `thread/backgroundTerminals/list` exists (experimental), its notifications were not read |
| `session.task.cancel` | `native`: `stop_task` (commands, agents); not crons, where it answers `success` and does nothing | `unavailable`: `job.kill` is not on this surface | not established: `thread/backgroundTerminals/terminate` exists (experimental) |
| `session.task.output` | `native`: `get_task_output` (commands, monitors) | `unavailable`: `job.follow` is not on this surface | not established |
| `session.task.schedules` | `emulated`: from `CronCreate`/`CronDelete` results and the Stop hook's `session_crons`. Cancel is `unavailable` for every cron, durable or session-only: only the model can delete a cron, through `CronDelete`; the SDK has no host control | not on this surface | none found |

Hermes's task surface is not established beyond its `background.complete`
event. OpenCode's tasks are `emulated` from its synthetic completion messages,
and cancelling them and reading their output are `unavailable`, because the
job service is not on its HTTP API.

Claude refuses `send_task_message` on the SDK connection, and no other
harness is established to let the host message a background agent. So OAP
defines no request for it.

## Schedules created by the host

A host that wants a reminder submits a message when the reminder is due. OAP
already carries that submit, and the host is where the clock and the user
are, so the protocol adds no request for a host to create a schedule inside
the harness. A harness's own schedules are created by its model and reported
as tasks.

A host that needs schedules it can cancel owns them: it disallows the
harness's scheduling tools (Claude's `CronCreate`) and submits on its own
timer.

## Close and reopen

1. **Closing a session ends the tasks bound to the harness's process.**
   - Claude's background commands and agents end, and so do DeepSeek's jobs
     and pending notices.
   - The endpoint reports each as `session.task.ended` with cause
     `session_closed` before the close completes, where it can.
   - Schedules the harness persists are not bound to the process. Claude's
     session and durable crons outlive the close and get no
     `session.task.ended`.
   - A host must not assume a process-bound task outlived a close; the
     reopen's `state.tasks` is authoritative for the rest.
2. **What the harness keeps comes back on reopen** (Decision 0040).
   - The reopen's `state.tasks` lists every task the harness restored.
   - Where the harness keeps its own id across the restart (Claude does), the
     endpoint keeps the same OAP `task_id`, because it records the mapping in
     the session's binding. That adds a member to the binding record
     Decision 0040 enumerates, so it lands with an amendment to 0040.
   - Claude restores a session's crons on `--resume` (recorded) and its
     durable crons from `.claude/scheduled_tasks.json` (read from the binary).
   - Anything absent from `state.tasks` did not survive.
3. **Missed fires catch up after a reopen as triggered runs.**
   - Claude surfaces a missed durable one-shot to the model with an instruction
     to ask the user first, so that run raises an input interaction.
   - That run arrives as a `schedule` triggered run on the idle, reopened
     session.
   - The reopen's response comes first. A catch-up turn the harness starts
     while the reopen is still in flight is held by the adapter, and its
     `run.started` is published only after `session.open.response`. That
     response does not list the run, so Decision 0040's check that a reopen
     without a message lists no run under way still holds.
   - DeepSeek's reminders cannot reach an endpoint: they live only in its web
     host, and its adapter advertises reopen `unavailable` (Decision 0040).
4. **No harness revives a closed OAP session.** A session is OAP's
   (Decision 0039).
   - A harness path that restores a cold session to deliver a reminder must not
     be reachable through an endpoint. That is DeepSeek's web host, which the
     SDK server does not mount.
   - A due reminder for a closed session waits in the harness for the host's
     reopen, or is lost if the harness does not keep it. `state.tasks` after
     the reopen says which.
5. **A durable schedule outlives the close by design.** A host that wants it
   gone cancels it before closing, where the harness allows, or after
   reopening.

## Delivery

Triggered runs start between runs, when the endpoint is reading no stream.
Task events happen both between runs and within one, as when a folded notice
ends a task mid-run. Both need the session-event channel that
[harness-initiated-state.md](harness-initiated-state.md) proposes, with its
open question answered here:

- **Adapters push.** An adapter hands the endpoint each triggered run and
  each session event as it happens: Go through an optional session interface,
  Zig through the matching `contract.zig` member.
- **The endpoint publishes.** It writes them in order on stdio, and
  `session.state.updated` comes from the same channel.
- **The hub** journals a triggered run like any run, so its replay and
  resume are a run's.
- **A subscription that follows the session.** A hub subscription today ends
  at its run's terminal envelope ([hub.md](hub.md)), so nothing in it can
  carry a run nobody subscribed to. This reuses the recorded T2 design in
  [staged-units-graduation.md](staged-units-graduation.md) rather than a new
  one:
  - A host subscribes with `?follow=session` (a `follow` flag on the stdio
    `events` op). A subscription without it keeps today's behaviour exactly.
  - Every line is an `envelope` line, and scope is read from the envelope's
    own members, never from `sequence`: one carrying `run_id` advances that
    run's cursor, one carrying `session_id` without `run_id` is
    session-scoped and touches no run cursor. Session events therefore carry
    their session-domain `sequence`, as
    [harness-initiated-state.md](harness-initiated-state.md) proposal 3
    delivers them.
  - Before leaving a run's domain the hub writes `oap-run-boundary` for it,
    and a triggered run's lines start at its `run.started`.
  - One change to T2: a following subscription does not end at a terminal
    with no later-admitted run, because a trigger can admit one at any time.
    It writes `oap-run-boundary` and stays open until the session closes, it
    overflows, or the client ends it, and only then writes `oap-stream-end`.
    The T2 section records this.
  - Session events are delivered live and not replayed. A host that
    reconnects reads `session.state.tasks`, and resumes a run through the
    run's own cursor and `?run=`.

## Where it lands

Each step below is one or more pull requests.

1. **Session-event channel:** the adapter push in both trees, the endpoint
   publishing, and the hub's following subscription, with `drafts/hub.md`
   updated in the same change and the memory adapters driving it in tests.
2. **Wire and validator:**
   - schema members and events: `run.started.trigger`, `activeRun.trigger`,
     `session.task.*`, `state.tasks`, `session.task.cancel.*`,
     `session.task.output.*`, `tasks_truncated`, the queued submit
     response's blocking interaction ids, and the
     `session.run.triggered`, `session.tasks`, `session.task.schedules`,
     `session.task.cancel` and `session.task.output` capabilities;
   - `protocol/` and `clients/ts/src/protocol.ts`;
   - the validator's rules from the sections above, each with a positive and a
     semantic-invalid fixture;
   - the decisions this draft cannot make on its own, recorded first:
     - graduating triggered runs and background tasks, which Decisions 0001
       and 0002 deferred and PF-4 kept deferred, with PF-4 updated to point
       at it;
     - amending Decision 0007: the second admission-order exception, a
       triggered run always listed in `active_runs`, and a reservation that
       may sit behind a later-admitted triggered run;
     - amending Decision 0040, for the task-id mapping in the binding.
3. **SDKs** (TS, Go, Rust, Python): a session-level stream of triggered runs
   and task events, plus `cancelTask` and `taskOutput`. This lands before any
   real adapter emits, since no host may drop a triggered run.
4. **Claude, both trees:**
   - declare `perTaskStopAffordance`;
   - project tasks and schedules;
   - map `stop_task` and `get_task_output`, but never `stop_task` for a
     cron;
   - report every cron, durable or session-only, `cancellable: false`;
   - make every turn without the adapter's echo a triggered run, using the
     mapping table above;
   - add corpus cases from the recordings above: idle notification, notice
     ahead of a queued prompt, fold, cron fire, stop and output.
5. **DeepSeek, both trees:**
   - project jobs as tasks;
   - make a turn that starts with no reserved run a triggered run;
   - record past `job/settled`, through the runtime's own turn and the submit
     race.
6. **Codex, Hermes and OpenCode:** triggered runs from their wake paths,
   Codex background terminals as tasks, and OpenCode completions as tasks.
   Each pin needs its own recording first.

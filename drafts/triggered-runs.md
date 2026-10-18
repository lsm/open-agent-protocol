# Triggered Runs and Background Tasks

Status: proposal. The owner decided on 2026-10-18 that OAP support triggered
runs, and that:

- every host accepts them;
- the trigger is a member of `run.started`;
- the trigger kinds are the four below;
- one run executes at a time;
- background tasks are designed now, not later.

What remains open is listed at the end.

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
It also makes the work behind most triggers, a **background task**, visible
and cancellable from the host.

## Evidence

- **DeepSeek harness** (`dsh-v0.2.1-alpha.2` and `rc.2`): a `bash` call that
  outlives its timeout moves to a background job. `job/registered` arrives
  inside the run and `job/settled` after it. The runtime then splices a notice
  into the inbox and runs a turn of its own
  ([ledger](../research/deepseek-harness-dsh-v0.2.1-rc.2-mapping.md#the-runtime-starts-a-turn-of-its-own-when-a-job-settles)).
  Both adapters treat it as external activity and close the session.
  - The runtime also has a `job` remote namespace, read from the source of
    `packages/api/job-controller` at `rc.2`:
    - `job.list`, a whole-set roster stream;
    - `job.follow`, an output stream;
    - `job.kill`, "human-initiated cancellation", answering `requested` or
      `already-finished`.
  - That namespace is not on the stdio SDK server the adapters speak.
- **Claude Code** (2.1.263 onward):
  - **Injected turns.** A turn injected after a background task ends carries
    a `result` whose `origin` is `task-notification`, after a fresh
    `system/init` and with no `user` frame
    ([2.1.280 ledger](../research/claude-code-agent-sdk-2.1.280-mapping.md)).
    Both adapters classify it "session-scope observation, not a new run", so
    the host never sees the turn's text.
  - **Tasks.** They have lifecycle frames (`task_started`, `task_updated`,
    `task_notification`), level frames (`system/background_tasks_changed`), a
    `backgroundTasks()` control and a `stop_task` control
    ([2.1.263 ledger](../research/claude-code-agent-sdk-2.1.263-mapping.md)).
  - **Schedules.** The 2.1.302 SDK types name cron summaries in
    `system/init`. A turn fired by a schedule has not been recorded, and
    neither has a host control over schedules.
- [PF-4](../research/protocol-feedback-2026-09.md) deferred background work
  that outlives its run, because a held terminal covered every case then
  recorded. A turn *started by* that work is the case it did not cover.

## Triggered runs

### Admission

1. A triggered run is admitted by its own `run.started`, which carries a
   `trigger` member. That event is the run's admission and its start at once,
   and it is the run's sequence 1.
2. After that, every rule of Decisions 0001, 0002, 0007 and 0013 applies
   unchanged: contiguous sequence, tools, interactions, cancel evidence, steer
   where advertised, and one absorbing terminal.
3. A `run.started` with `trigger` for a run a submit reserved is invalid. So is
   one without `trigger` for a run nothing reserved, which is the conviction
   Decision 0007 already makes.
4. The endpoint mints the run id, unique within the session like any other.

### The trigger

| member | meaning |
| --- | --- |
| `kind` | `background_task`, `schedule`, `message`, or `other` |
| `input` | optional content blocks: the notice the harness gave the model, so a transcript shows why the run exists |
| `task_id` | the background task that fired, when `kind` is `background_task` or `schedule`; it names a task this session has announced |
| `source_id` | optional opaque id of a sender, when `kind` is `message`; scoped to the session, never an OAP identity |
| `extensions` | the native origin (`task-notification`, `job/settled`), namespaced |

### Every host accepts them

- A host must expect a run it did not submit on any session, and an SDK must
  deliver one rather than drop it.
- `capabilities.response` reports `session.triggered_runs` with the usual
  fidelity, so a host can tell whether the harness behind an endpoint produces
  them. It is a report, not a switch: no request elects or refuses it.
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
   `trigger` while another run is executing is `illegal_run_transition`.
2. **It goes ahead of waiting reservations.** A trigger can fire while host
   reservations are queued, and the harness, not the host, decides which turn
   runs next.
   - A triggered run may therefore start while reservations wait. It enters
     the admission order at its start, ahead of every reservation still
     queued, and those reservations keep their relative order and promote
     after it ends.
   - This is the one exception to Decision 0007's "in admission order", and
     the validator does not report it as `queue_order_violation`.
   - A reservation's `queue_position` counts reservations only, so it does not
     change.
3. **A trigger that fires during a run is never a second run.** The harness
   either holds the notice or folds it into the executing run.
   - **Held:** when the run ends, the notice starts a triggered run, or is
     consumed by the next reservation's run if the harness promotes that one
     first.
   - **Folded or consumed:** the notice is context inside a host run and no
     triggered run exists. The task's `session.task.ended` (below) still tells
     the host what happened.
   - Claude Code and DeepSeek both deliver the notice at a turn boundary.
4. **A submit during a triggered run** is admitted like any submit against an
   executing run. Under Decision 0007 that means a reservation where
   `session.message.delivery.queue` is advertised, and `run_active` otherwise.
   Under Decision 0013 it may be a steer where steer is advertised and
   requested.
5. **A submit racing a trigger** is resolved from the harness's own order, and
   the adapter reports one of the legal shapes truthfully. It never reports
   one turn as two runs, or two turns as one.
   - **The trigger's turn began first:** the submit is answered as in 4.
   - **The prompt's turn began first:** the submit is `started` and the
     trigger is held or folded, as in 3.
   - **The harness merges the prompt and the notice into one turn:** the run
     is the host's, admitted by its submit, and the notice is folded.
   - An adapter whose harness takes a prompt into a turn already running must
     not hand the harness a prompt it has answered `run_active`. It holds the
     prompt until it knows the session is idle, or answers the submit after
     the harness has.
   - Each adapter's ledger records which of these its harness does, and its
     corpus carries the race.
6. **Cancelling a triggered run** follows Decision 0001. It cancels the run,
   not the task or schedule that fired it, and cancelling a task (below) never
   settles a triggered run that already started.
7. **Interactions** in a triggered run wait for an answer like any run's, and
   `state.active_runs` lists them under `pending_interactions`. A hub session
   with no subscriber keeps the run waiting rather than refusing.
8. **State and recovery.** `state.active_runs` lists a triggered run with its
   `trigger`, so a host that reconnects can tell it from its own. A recovered
   triggered run enters the admission order at the position it started at, as
   Decision 0007 does for any recovered run.
9. **Settings and model switches** during a triggered run follow the same
   rules as during any run.
10. **Closing the session** ends a triggered run as it ends any run. A trigger
    that fires after the close has no session to run in, and the harness's own
    schedules die with the session.

## Background tasks

A **background task** is session-scoped work the harness tracks beyond a
turn.

### Kinds

| kind | example |
| --- | --- |
| `command` | a backgrounded shell command |
| `agent` | a background subagent |
| `schedule` | a timer or cron entry that fires turns |
| `other` | anything else |

The model creates tasks through the harness's own tools. The host observes
them, and cancels them where the harness allows.

### Session-scoped events

These carry the session's state-domain `sequence`, the same counter as
`session.state.updated`.

| event | members |
| --- | --- |
| `session.task.started` | `task_id`, `kind`, `label`; `run_id` and `tool_call_id` of the call that created it, when one did; for a `schedule`, `next_fire_at_ms` and `recurring` |
| `session.task.updated` | `task_id`, and what changed: a `schedule`'s `next_fire_at_ms` after it fires, or a progress summary |
| `session.task.ended` | `task_id`, `status` (`completed`, `failed` or `cancelled`), and an optional summary |

### State and requests

- `session.state` gains `tasks`: every task not yet ended, so a host that
  reconnects starts from the truth. The state document is authoritative, as
  for runs.
- `session.task.cancel.request {session_id, task_id}` answers
  `{outcome: "requested" | "already_ended"}`. The ending itself arrives as
  `session.task.ended`, because a cancel is intent and not settlement, as for
  runs.

### Capabilities

| feature | DeepSeek (stdio SDK server) | Claude Code |
| --- | --- | --- |
| `session.tasks` (observe) | `emulated`, from `job/registered` and `job/settled` | `native`, from task frames |
| `session.task.cancel` | `unavailable`, since `job.kill` is not on that surface | `native`, through `stop_task` |
| schedules | not recorded | `degraded`: summaries only, from `system/init` |

### How runs and tasks relate

- A task created by a tool call names the call. A run that backgrounds a
  command still ends on its own terminal; the held-terminal rules of PF-4
  stay for children the run itself waits on.
- A triggered run of kind `background_task` or `schedule` names the task in
  `trigger.task_id`, so a host can draw "job X finished, and the model
  answered".
- A notice folded into a host run is still visible as the task's
  `session.task.ended`.

### Delivery

Task events and triggered runs both happen between runs, when the endpoint
is reading no stream. Both need the session-event channel that
[harness-initiated-state.md](harness-initiated-state.md) proposes, with its
open question answered by this draft:

- **Adapters push.** An adapter hands the endpoint each triggered run and
  each session event as it happens: Go through an optional session interface,
  Zig through the matching `contract.zig` member.
- **The endpoint publishes.** It writes them in order on stdio, and
  `state.updated` comes from the same channel.
- **The hub journals and fans out** session events and triggered runs to
  every subscription on the session, between runs and within one.

## Where it lands

Each step below is one or more pull requests.

1. **Session-event channel:** the adapter push in both trees, the endpoint
   publishing, and hub fan-out and journaling, with the memory adapters
   driving it in tests.
2. **Wire and validator:**
   - schema members and events: `run.started.trigger`, `activeRun.trigger`,
     `session.task.*`, `state.tasks`, `session.task.cancel.*`, and the
     `session.triggered_runs`, `session.tasks` and `session.task.cancel`
     capabilities;
   - `protocol/` and `clients/ts/src/protocol.ts`;
   - the validator's rules from the sections above, each with a positive and a
     semantic-invalid fixture.
3. **SDKs** (TS, Go, Rust, Python): a session-level stream of triggered runs
   and task events, and `cancelTask`. This lands before any real adapter
   emits, since no host may drop a triggered run.
4. **DeepSeek, both trees:**
   - project the job events as tasks;
   - make a turn that starts with no reserved run a triggered run;
   - record, past `job/settled`, through the runtime's own turn and through
     the submit race in rule 5, which `background-job` stops short of.
5. **Claude, both trees:**
   - project the task frames as tasks;
   - map `stop_task` to cancel;
   - make an injected non-`human` turn a triggered run
     (`task-notification` is `background_task`, any other origin `other`);
   - record the race.
6. **Survey** the other pinned harnesses (Codex, OpenCode, Hermes, pi, ACP,
   Copilot) for triggers and tasks.

## Still open

- **Recorded order:** what DeepSeek and Claude actually do when a task
  settles while a host reservation waits. Rule 2 assumes either may run first;
  a recording may show one order is fixed.
- **Task output streaming** (`job.follow`, Claude's output files) is not in
  this draft.
- **Host-created tasks** (a host that schedules a reminder through OAP rather
  than through the model) are not in this draft.

# Decision 0068: A Slow Shell Command Becomes a Background Job

Status: accepted 2026-11-05 (the owner chose this, relayed through Neo;
implementation follows separately)
Date: 2026-11-05
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Scope: `oapx`'s own agent loop. A third-party harness's commands keep the
behaviour its harness gives them.
Amends: [Decision 0059](0059-a-background-shell-job-lives-for-its-session.md),
whose rule 1 ended every foreground command with its call and whose rule 3 set
no time limit on a job

## Context

The [event-driven loop](../drafts/event-driven-loop.md) lets a run wait without
holding a thread, but an agent waiting on a slow command is still an agent doing
nothing. Today a foreground `Shell` call holds its turn until the command ends
or its timeout kills it, 30 s by default. The model has to guess in advance
whether to ask for `run_in_background`, and a wrong guess either blocks the turn
or loses the work.

Claude Code already reports a job's end back into the session, as
[`drafts/triggered-runs.md`](../drafts/triggered-runs.md) recorded on 2026-10-18:
folded into the turn when one is calling tools, otherwise as a turn of its own.
The other shape is to push output on a fixed interval. Infinity does that every
10 s (`hydro-project/infinity` at `c1a7b05ef7f2`,
`crates/sandbox-core/src/server.rs`). A wake is a model call, so a timer costs
one call per job per interval whether or not anything happened, which does not
scale to 10,000 agents.

## Decisions

### 1. An agent never sits and waits on a slow command

Every `Shell` command gets a foreground budget of 5 seconds. A command that ends
within it returns its result as it does today, and Decision 0059 rule 1 still
applies to it: its process group ends with the call.

### 2. Past the budget the command becomes a session background job

At 5 seconds the command is not killed and not restarted. It becomes a
background job, and the call returns its `task_id`, its `output_path` and the
output so far. From then on it is a job under Decision 0059 rules 2 to 5, except
as rule 5 below says. The model does not choose this, and no argument opts out.

The call completes normally. It is not a failure, and the result says the
command is still running and that its end will be reported.

`run_in_background: true` still starts a job at once, without the budget.

### 3. A command has a 1 hour limit

Every `Shell` command, moved or started with `run_in_background`, has a limit of
1 hour. It is the default when `timeout_ms` is omitted and the maximum when it is
passed, so a larger value means 1 hour. A smaller `timeout_ms` ends the command
at that time, in the call if the time falls within the budget and as a job if
not. This replaces the 30 s default and the unlimited job of Decision 0059.

A job that reaches its limit is ended the normal way, as Decision 0059 rule 3
says: `SIGTERM` for its process group, then `SIGKILL` for whatever is left. The
limit is one more way a job ends, so the model is told through the notice in rule
4, with a reason that says the job hit its time limit and what that limit was.

### 4. A job's end goes back into the session

This applies to every job, moved or started with `run_in_background`, that exits
or reaches its limit. The result is a notice carrying the `task_id`, the `output_path`, the
status from Decision 0059 rule 4, a bounded tail of the output and, for a job that
reached its limit, the reason.

- If a turn is running and has another model request to make, the notice is
  folded into that request. No further run follows.
- Otherwise a triggered run starts at once, with trigger kind `background_task`
  and the job's `task_id`. A notice that arrives during a run's final model
  request is held, and starts that triggered run when the run ends.
- Notices waiting when a request is built all go into that request.

Admission, ordering and racing submits follow *One run executes at a time* in
`drafts/triggered-runs.md`.

A job ended by `ShellJob {kill: true}`, by a cancel or by its session's close
gives the model no notice and starts no run, as Claude's `stop_task` does not.

Every job's end still reaches the host, whatever the model is told.
`session.task.ended` and `state.tasks` report it as `drafts/triggered-runs.md` says:

| how the job ended | `status` | `cause` |
| --- | --- | --- |
| exited 0 | `completed` | `finished` |
| exited nonzero, or died from a signal nobody here sent | `failed` | `finished` |
| reached its limit | `failed` | `expired` |
| a cancel or `session.task.cancel` | `cancelled` | `cancelled_by_host` |
| `ShellJob {kill: true}` | `cancelled` | `cancelled_by_model` |
| its session closed | `cancelled` | `session_closed` |

### 5. Cancelling a run ends the commands it moved

A cancel of a run ends any command that run moved under rule 2 and that is still
running, because the host asked to stop work that began as a foreground call. A
`run_in_background` job is not ended by a cancel. Once its run has settled a
moved command is like any other job, and only `ShellJob {kill: true}`, a
`session.task.cancel` or its session's close ends it early.

### 6. Nothing wakes on a timer

A job wakes its session once, when it ends. There is no progress notice and no
periodic output. The limit in rule 3 is a timer on the process, not on the model:
it ends the job, and that end is the one wake. The model reads `output_path` when it wants to, with Read or
`ShellJob`.

### 7. `run.completed` does not mean the work is finished

A run's terminal says the agent finished its turn. It says nothing about jobs
that turn left running. A run can complete while its command still runs, and
the command's result then arrives in a later run on the same session. A host
that needs everything finished waits for a session with no active run and no
running task. A host that closes a session after its first terminal ends that
session's jobs, as Decision 0059 rule 3 says.

## Consequences

- This depends on triggered runs. A run with no submit is a conviction under
  Decision 0007 until `run.started` carries `trigger`, so `oapx` adopts that part
  of `drafts/triggered-runs.md`, with `session.task.*` so a host can see a job,
  before the budget lands. A budget without the wake would strand results.
- A subscription outlives a run under Decision 0066, so a triggered run reaches
  an open subscription with no new request. A client that stops reading at a
  run's terminal does not see it.
- `drafts/event-driven-loop.md` says how this fits the tool budget and parking.

## What this decision does not admit

- It does not change Decision 0059 rule 1 for a command that ends within its
  budget, or let what it backgrounded outlive the call.
- It does not keep a job across a restart or across sessions.
- It does not give a third-party harness's adapter a budget.

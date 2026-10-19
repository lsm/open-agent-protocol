# Decision 0059: A Background Shell Job Lives for Its Session

Status: accepted 2026-10-19 (the owner chose this, relayed through Neo, after
background jobs started by `oapx`'s own shell tool were seen to die at the end
of the call that started them)
Date: 2026-10-19
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Scope: `oapx`'s own agent loop. A third-party harness's background work keeps
the lifetime its harness gives it, as `drafts/triggered-runs.md` records.

## Context

`oapx`'s shell tool runs each command in a process group of its own. When the
call returns, it signals that whole group, on success as well as on timeout or
cancel (`zig/src/tools/process_runner.zig`, #723). That rule exists so that a
command that left children behind cannot hold the call's output pipes open
and leave later calls stuck, and it is right for a foreground command.

It also means the tool has no way to start work that outlives the call. A
command that backgrounds a server or a watcher with `&` or `nohup` is ended the
moment the call returns, which is before the turn that asked for it ends. The
tool offered no other way to ask, and no decision said how long such work may
live.

## Decisions

### 1. A foreground command's processes end with its call

A shell call without `run_in_background` keeps today's rule. Its process group
ends when the call returns, on success, timeout or cancel, and anything the
command backgrounded ends with it.

### 2. `run_in_background` starts a job that outlives the call

A shell call with `run_in_background: true` starts its command as a background
job, in a process group of its own, and returns at once with the job's
`task_id` and `output_path`. The job's stdout and stderr both go to that file,
a workspace path under `.oapx/jobs/`, so the model reads the output with the
ordinary Read tool, by byte range while the job runs and whole after it ends.
No separate streaming tool exists.

The job outlives the call, the turn and the run that started it.

### 3. A job ends when it exits, when it is killed, or when its session closes

A job ends at the first of these:

- its command exits, and anything still in its process group is ended with it;
- `ShellJob {task_id, kill: true}` ends it;
- its session closes, whether by a close request, by end of input, or by the
  endpoint's shutdown. A graceful shutdown does not wait for jobs; they end
  when the session closes, the same as a forced one.

A job never survives its session. Ending a job signals its whole process group,
first with `SIGTERM`, then with `SIGKILL` for whatever is still there after a
short grace period, as a cut-short foreground command is ended.

A job does not survive a crash or a restart of `oapx` either, because it
belongs to a process that is gone; Decision 0050's recovery does not restart
it. Its output file stays in the workspace.

### 4. `ShellJob` reports and stops a job

`ShellJob {task_id, kill?}` answers with the job's `status`. The status is
`running`, `exited` (with `exit_code`), `signalled` (with `signal`) or `killed`.
The answer also carries the bytes written to the output file so far and the
`output_path`. `kill: true` ends the job first, as rule 3 describes. A
`task_id` the session never started is an error.

### 5. Background jobs are session state

The job table belongs to the session. A shell tool used outside a session
refuses `run_in_background` rather than running the command in the foreground,
because a job nothing could end would outlive everything.

## Consequences

- The Zig shell tool gains `run_in_background`, the session's job table and the
  `ShellJob` tool, and the `oapx` adapter's session ends its jobs when it is
  destroyed.
- When `drafts/triggered-runs.md` graduates, these jobs are its background
  tasks. They appear through `session.task.*`, a job that settles while the
  session is idle can trigger a run, and a job ended by rule 3's close reports
  `session_closed`. Nothing here is on the wire until then.

## What this decision does not admit

- It does not let a foreground command opt out of rule 1.
- It does not keep a job across sessions or across a restart.

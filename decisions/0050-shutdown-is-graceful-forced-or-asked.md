# Decision 0050: Shutdown Is Graceful, Forced, or Asked

Status: proposed (the owner approved it on 2026-10-18; it becomes accepted
once its wire, hub behaviour and restart recovery land in both trees)
Date: 2026-10-18
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `core`
Amends: [Decision 0007](0007-queue-delivery.md), so a reopen can restore
queued reservations, and [Decision 0040](0040-a-session-reopens-through-its-own-binding.md),
so a reopen can list them and report an interrupted run;
[Decision 0001](0001-agent-control-v0.1-executable-core.md), so a run a
restart cut off settles through a recorded settlement rather than a terminal
envelope in its stream; and Decision 0051 (#1205), by
adding an entry kind. Applies [Decision 0010](0010-terminal-provenance.md):
that settlement is `inferred`. Lands after Decision 0051 (#1205) and the
storage interface, `docs/storage.md` (#1206). Applies to both trees, to
`goap hub` and `oapx hub`, and to `serve agent`

## Context

Today shutdown is one thing. [hub.md](../drafts/hub.md) says stdin EOF,
SIGINT or SIGTERM stops admitting, settles admitted work inside a 5 s window
per stage, closes every session and exits. A run that is still working when
the window ends is cut off, its queued reservations vanish, and after a
restart a reopen lists no run at all (0040). Nobody is told what was lost.

A host needs two kinds of stop. A user closing the TUI wants the running turn
to finish. A user whose agent is stuck wants it gone now. A crash is a third
case that nobody asked for. All three must leave the session in a state the
next start can describe honestly.

## Decisions

### 1. Three ways to stop, one restart rule

- **Graceful is a freeze:** stop dispatching at once, let running runs finish,
  save the whole state, then exit. The next start unfreezes it and carries on.
- **Force:** stop now. For runs it behaves exactly like a crash: none settles
  at the time. The process still ends its harness children so none is
  orphaned, inside the existing 5 s window per stage, which bounds that
  teardown and nothing else.
- **Crash:** the process dies. Nothing is settled at the time.

After force or a crash, the next start applies one rule, from the store
(`docs/storage.md`, #1206):

- **The run that was executing settles `failed` with
  `error.code: "interrupted"`** and `details.by: "restart"`. Its partial
  output stays readable through `transcript.load` (Decision 0051, #1205). It
  never reruns on its own,
  because its tool calls may already have had their effects.
- **Queued prompts that were accepted are delivered again, in their order,
  under new run ids.** An accepted submit is a promise, and a restart does
  not cancel it. A new id, because each adapter keeps its own queue, so the
  only way to keep a queued run from starting is to settle it, and a settled
  run id never runs again. The reopen pairs each new id with the one it
  replaces (rule 5). A crash re-delivers the same way, so one rule covers
  both.
- **A pending interaction settles with its run**, as `run_settled`, because a
  settled run takes no answer (a terminal is absorbing). Where the harness, once
  reopened, asks the question again (Claude resuming a turn that was waiting
  on a permission), it is raised again as a new interaction in a new run,
  which the host sees like any other. The interrupted run is never revived.

The settlement is not published as a `run.failed` envelope in the old run's
stream, because the process that held that stream's sequence is gone and a
made-up sequence would be fake continuity. It is the run's one terminal all
the same, and it carries `settled_by: "inferred"`, because the endpoint
concluded it from the store rather than observing the run end (Decision
0010). It is reported on reopen (rule 5). Where the transcript is OAP's own
(`oapx`), `transcript.load` also gains an `interrupted` entry kind,
`{run_id, settled_by: "inferred"}`, at the point the run stopped; Decision
0051 keeps OAP's records out of a third-party harness's transcript.

### 2. Graceful freezes the queue, and the next start resumes it

From the moment a graceful shutdown is accepted, no queued run starts, so a
follow-up queued after the request does not go out now. Those prompts are
kept with the rest of the session's state, and the next start sends them when
the session reopens. A kept prompt is always sent again: there is no state in
which a prompt is kept but not sent. The shutdown response lists them under
`held`. Holding one settles it `run.cancelled` before start with
`reason: "held_for_restart"`, so a UI can show "will be sent after restart"
rather than a cancellation nobody asked for. The accepted graceful shutdown
is that terminal's evidence, as an accepted cancel exchange is for any other
`run.cancelled`. Its run id is settled, so it takes no `run.cancel`; a host
that does not want the prompt sent cancels the run that carries it after the
reopen, while it is still queued. A submit that arrives after the request is refused
`shutting_down` and never accepted.

Running runs finish, on `oapx`'s own loop and on every third-party harness,
including triggered runs (`drafts/triggered-runs.md`). A run that waits on an
interaction keeps waiting. Graceful has no timeout of its own; the requester
or a second signal turns it into force.

### 3. The request is answered, and asks when it must

`endpoint.shutdown.request {mode?: "graceful" | "force"}`, with no
`session_id`. The hub carries the same payload as a `shutdown` op on stdio and
`POST /shutdown` over HTTP.

`endpoint.shutdown.response`:

| member | meaning |
| --- | --- |
| `outcome` | `choose`, `draining`, `stopping` |
| `active_runs` | every nonterminal run, as `{session_id, run_id, status, trigger?, pending_interactions?}` |
| `held` | the queued runs kept for the next start, as `{session_id, run_id}` |
| `lost` | queued runs that cannot be kept because the session's store is off (rule 6), as `{session_id, run_id}` |

The split is on nonterminal runs, queued ones included, since a queued run is
what graceful holds and an off store loses:

- **No mode, no nonterminal run:** graceful at once, `outcome: "stopping"`.
- **No mode, any nonterminal run:** nothing changes yet. `outcome: "choose"` lists the
  runs, and the requester sends the request again with a mode.
- **`graceful`:** `outcome: "draining"`, or `stopping` when nothing runs.
- **`force`:** `outcome: "stopping"`. Sent during a graceful drain, it
  escalates.

Every subscription on the endpoint then receives
`endpoint.shutdown.started {mode, held, lost}`, endpoint-scoped and unsequenced,
so a second host watching a session learns why its runs stop arriving.

### 4. Signals map to modes

A signal cannot read a `choose` answer, so each signal is a mode, and the
answer goes to stderr as one line listing the active runs.

| source | mode |
| --- | --- |
| first SIGINT or SIGTERM | graceful |
| a second SIGINT or SIGTERM during the drain | force |
| SIGKILL, power loss | crash |
| stdin EOF on stdio | force: the host that could watch the runs has gone |

A TUI or GUI sends the request without a mode and shows the `choose` list.
`serve agent` takes the same request as an envelope and the same signals.

### 5. A reopen after a restart says what happened

Decision 0040 requires a reopen to list no run under way. That changes:

- the reopen response may list the held runs in `state.active_runs` with
  `status: "queued"`, in their original order. Decision 0007's admission order
  takes them as recovered runs at those positions, and the first one starts
  only after the reopen response, as any queued run does;
- it never lists a run as `running`, since no run survives a restart;
- `recovery.interrupted_runs` lists each run that settled under rule 1, as
  `{run_id, cause: "restart", settled_by: "inferred"}`;
- `recovery.redelivered_runs` pairs each held prompt with the run that now
  carries it, as `{previous_run_id, run_id}`, in the original order; each
  `run_id` is also in `state.active_runs` as `queued`.

The validator accepts restored queued runs in a recovered open response
(`recovery.recovered: true`) and nowhere else.

### 6. The rules rely on stores, on by default in `oapx`

Freezing and the crash rule need the session to survive the process.

- `oapx hub` keeps the session history at `~/.oapx/sessions.jsonl` unless
  `--session-history=` turns it off.
- `oapx serve` saving the `oapx` loop's own sessions under `~/.oapx/sessions`
  is off today behind `--save-oapx-sessions`. The owner chose on #1170 to make
  it the default, with `--no-save-oapx-sessions` to opt out; #1207 makes that
  change, and these rules rely on it.
- `goap hub` has no default history: without `--session-history` it keeps
  nothing. `goap` is never released (Decision 0038), so this is recorded as
  its gap rather than closed, and a default `goap hub` reports every queued
  run as lost.

Wherever a session's store is off, nothing of it survives the process, and
that is reported, never silent:

- a shutdown lists the queued runs under `lost` instead of `held`, and the
  stderr line says they are lost;
- after a crash, a reopen of such a session is refused as Decision 0040
  refuses one with no binding or no loadable store, so the host learns the
  session and its queue are gone.

Credentials are never written to either store.

## Consequences

- The store gains run records: admitted (with the prompt, for a queued run),
  started and settled. `docs/storage.md` (#1206) names the
  operations. A prompt is user content, so a binding still never holds a
  credential or an environment value.
- Schema: the shutdown pair and event, `shutting_down`,
  `recovery.interrupted_runs`, `recovery.redelivered_runs`, and the error
  code `interrupted`, with the matching `go/protocol`, validator, fixtures and `clients/ts`.
- hub.md's shutdown section is rewritten: the 5 s window bounds force's
  child teardown only. The process exits 0 once the requested mode completes,
  graceful or force; runs that force cut off are reported by the next start,
  not by the exit code, and a stage that outlives its window still exits
  non-zero as "shutdown stalled". So is `drafts/endpoint-stdio.md`'s exit contract, which today makes
  stdin EOF and the signals settle admitted work and exit 0: EOF becomes force
  and the signals map as rule 4 says.
- #1207 makes `oapx serve` save its `oapx` sessions by default, as rule 6
  relies on.
- The TUI's quit shows the `choose` list.

## What this decision does not admit

- An automatic rerun of an interrupted run.
- A graceful shutdown that dispatches a queued prompt.
- A restart that resumes a run's event stream. Replay stays within one
  process's journal (0001, 0040).
- Answering a pending interaction on the user's behalf at shutdown.

# Triggered Runs

Status: proposal. The owner directed on 2026-10-18 that OAP support this; the
shape below is for an owner decision.
Base protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`

A harness can start a turn because something inside it fired, not because the
host submitted a message. Several kinds of trigger do this:

- a background job settles;
- a timer or cron schedule fires;
- a message arrives from a peer or a monitor.

Each of these starts a turn the model answers. OAP has no run the host did not
submit, so today every adapter either drops such a turn or ends the session
over it. This draft makes such a turn a first-class run: a **triggered run**.

## Evidence

- **DeepSeek harness** (`dsh-v0.2.1-alpha.2` and `rc.2`): a `bash` call that
  outlives its timeout moves to a background job. When the job ends, the
  runtime splices a notice into the inbox and runs a turn of its own
  ([ledger](../research/deepseek-harness-dsh-v0.2.1-rc.2-mapping.md#the-runtime-starts-a-turn-of-its-own-when-a-job-settles)).
  Both adapters treat it as external activity and close the session.
- **Claude Code** (2.1.263 onward): a turn injected after a background task
  ends carries a `result` whose `origin` is `task-notification`, after a fresh
  `system/init` and with no `user` frame
  ([2.1.280 ledger](../research/claude-code-agent-sdk-2.1.280-mapping.md)).
  Both adapters classify it "session-scope observation, not a new run"
  (observed-only), so the host never sees the turn's text.
  The 2.1.302 SDK types name cron summaries in `system/init`; a turn fired by
  a schedule has not been recorded.
- [PF-4](../research/protocol-feedback-2026-09.md) deferred background work
  outliving its run, on the ground that a held terminal covered every case
  then recorded. A turn *started by* that work is the case it did not cover.

## Rule

1. **A triggered run is admitted by its own `run.started`.**
   - `run.started` gains an optional `trigger` member. A `run.started` that
     carries `trigger` admits a run no submit reserved: it is the run's
     admission and its start at once, and it is the run's sequence 1.
   - Every Decision 0001 and 0002 rule then applies unchanged: contiguous
     sequence, tools, interactions, cancel evidence, and one absorbing
     terminal.
   - A `run.started` with `trigger` for a run a submit reserved is invalid,
     and so is one without `trigger` for a run nothing reserved.
2. **One nonterminal run per session still holds.** A triggered run starts
   only while the session has no nonterminal run, a queued reservation
   included.
   - A trigger that fires during a run is the harness's to hold or to fold
     into that run. Claude Code and DeepSeek both deliver the notice at a turn
     boundary, and the next turn consumes it. If that turn is the host's, the
     notice is context inside the host's run and no triggered run exists.
   - A host submit that arrives during a triggered run is admitted like any
     submit against a running session: `queued` where the queue unit is
     advertised, refused otherwise. An adapter that races a submit against a
     trigger decides from the harness's own order. When the trigger's turn
     won, the submit is answered as against a running session; the adapter
     never reports `started` and then delivers someone else's turn.
3. **`trigger` says what fired**, in a closed core set plus extensions:

   | member | meaning |
   | --- | --- |
   | `kind` | `background_task`, `schedule`, `message`, or `other` |
   | `input` | optional content blocks: the notice the harness gave the model, so a transcript shows why the run exists |
   | `source_id` | optional opaque id of the job, schedule or sender, scoped to the session, never an OAP identity |
   | `extensions` | the native origin (`task-notification`, `job/settled`), namespaced |

4. **A host elects to receive them.** The capability is a new optional feature,
   `session.triggered_runs`, with the usual fidelity.
   - A host elects it at open with `session.open.request.triggered_runs: true`,
     on the same ladder as `subscribe` (cite the active revision, opt in to a
     degraded one).
   - Without the election the endpoint emits no triggered run. It absorbs the
     turn: no envelope, the session stays open, and `state` reports `running`
     until the turn ends.
   - An adapter that cannot tell where an unprompted turn ends falls back to
     the closure in
     [harness-initiated-state.md](harness-initiated-state.md#a-harness-that-ends-the-session),
     and must announce it.
   - The election keeps every existing host and trace valid, and it lets the
     validator judge a run nothing submitted.
5. **Everything else is a run.** It can raise permission and input requests,
   and `run.cancel` cancels it. `state.active_runs` lists it, with a
   `trigger` member so a host that reconnects can tell it from its own. The
   hub journals it and fans it out to the session's subscriptions, and replay
   and resume treat it like any run.

## Out of scope

- **Host-managed triggers.** The model creates triggers through the harness's
  own tools (`bash` backgrounding, a cron tool). Listing or cancelling them
  from the host (`session.triggers.*`) would be a later unit.
- **The background job's own lifecycle** (registered, progress, settled) as
  events. `trigger.source_id` lets a later unit link a run to its job.
- **Triggers across a restart.** A schedule the harness persists and fires
  after the session was closed has no session to run in. Reopening is the
  host's act.

## Where it lands

1. **Wire:**
   - `run.schema.json` (`started.trigger`) and `session.schema.json`
     (`openRequest.triggered_runs`, `activeRun.trigger`);
   - `protocol/`, `clients/ts/src/protocol.ts`;
   - the validator: three rules, a run admitted by `trigger`, `trigger` on a
     reserved run, and a triggered run while another is nonterminal or
     without the election, each with a fixture.
2. **Adapter contract:**
   - Go: an optional session interface handing the endpoint each triggered
     run's id, trigger and event stream;
   - Zig: the matching `contract.zig` member;
   - `serveendpoint` and `zig/src/adapter/endpoint.zig` emit those runs;
   - both hubs journal them and fan them out (the session-event delivery that
     [harness-initiated-state.md](harness-initiated-state.md) proposes, item 3).
3. **DeepSeek, both trees:** a turn that starts with no reserved run becomes a
   triggered run (`background_task`, `input` from the inbox splice). This
   needs a recorded corpus case that runs past `job/settled` through the
   runtime's own turn, which `background-job` stops short of.
4. **Claude, both trees:** an injected turn with a non-`human` origin becomes
   a triggered run in place of the observed-only row: `task-notification` maps
   to `background_task`, and any other origin to `other` with the native kind
   in `extensions`.
5. **SDKs** (TS, Go, Rust, Python): elect at open, and expose triggered runs
   as a session-level stream.
6. Survey the other pinned harnesses (Codex, OpenCode, Hermes, pi, ACP,
   Copilot) for their own triggers.

## Questions for the owner

- Should the trigger be a member on `run.started`, or a separate
  `run.triggered` event ahead of it?
- Is opting in at open right, or should every host be required to accept
  triggered runs?
- Is the kind set (`background_task`, `schedule`, `message`, `other`) the
  right one?

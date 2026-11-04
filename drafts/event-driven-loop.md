# Event-Driven Agent Loop

Status: proposal under review. The owner has approved the direction. Stage 0 is
merged, and later stages wait until this draft lands. Two decisions are open,
listed at the end.

Scope: the Zig runtime (`zig/src/agent`, `adapter/oapx`, `hub`, `providers`,
`tools`). This draft changes no wire behaviour. The Parking section names the
protocol questions that parking raises, and those would need their own decision.

## Goal

The number of agents must not set the number of threads or processes. An agent
spends nearly all its time waiting, on a model, a tool or a gate, and waiting
should cost no thread.

The targets are:

- 10,000 agents must work, and 100,000 is the goal.
- The thread count is fixed: a few reactor threads and a small bounded I/O pool.
- An idle agent costs no CPU and causes no wakeups, so nothing polls.
- No process per agent. A tool's subprocess exists only while the tool runs,
  behind a bounded queue, and file tools do not fork.
- Memory allows 100,000 agents, which means parking cold runs to disk.

## Why the current design cannot reach it

A run is a call stack. `Agent` spawns a thread, `agentLoop` spawns a second, and
a model call adds a bridge thread and a provider thread. Measured per agent:

| Phase | Threads per agent |
|---|---:|
| In a model call | 4 |
| In a tool | 2, plus 2 processes per `Shell` call |
| At a gate | 2 |

macOS allows 12,288 threads per task (`kern.num_taskthreads`), 8,000 processes
per user (`kern.maxprocperuid`), and about 16,000 ephemeral ports to one
destination. Those limits were reached in practice:

- **Threads.** A sequential `waiting` run stopped at agent 6,142 with 12,286
  threads and 11.7 GB. That build still polled gates every 1 ms and used 24 cores
  while idle.
- **Ports.** Each model call opens a new connection, because every provider
  creates its own `HttpClient`, and each closed connection stays in `TIME_WAIT`
  for about 30 s. That caps a host at about 540 calls per second. One `zig-loop`
  run at N=1,500 left about 12,000 sockets in `TIME_WAIT`. With a second run
  alongside it the machine reached 14,998, and unrelated programs failed with
  "can't assign requested address".
- **The Hub.** `Hub.pump` walks every session on one thread. At N=1,000 that
  thread is saturated, and 67% of its time goes to saving each streamed event.
- **Memory.** `EventStream` embeds a 1,024-slot ring, about 265 KiB for an
  `AssistantMessageEventStream`, so a parked agent holds several hundred KiB
  even when it does nothing.

### What cheap fixes buy

These were measured at N=1,000 in one window on a shared machine. The baseline
is the loop after the polling fixes (#1588, #1592).

| Scenario | Build | Threads | Memory MiB | Hub cores | Runs/s |
|---|---|---:|---:|---:|---:|
| `hold-model` | no polling | 4,003 | 3,006 | 0.80 | 40.0 |
| `hold-model` | and no outer thread | 3,003 | 2,964 | 0.76 | 40.7 |
| `hold-model` | and ring of 64 | 3,003 | 844 | 0.70 | 40.8 |
| `hold-gate` | no polling | 2,032 | 2,427 | 0.54 | 20.4 |
| `hold-gate` | and no outer thread | 1,028 | 2,328 | 0.54 | 20.3 |
| `hold-gate` | and ring of 64 | 1,025 | 1,023 | 0.57 | 20.0 |
| `working` | no polling | 3,485 | 5,875 | 1.62 | 64.7 |
| `working` | and no outer thread | 2,883 | 5,907 | 1.51 | 68.1 |
| `working` | and ring of 64 | 2,767 | 4,684 | 1.37 | 63.9 |

- Removing polling is the largest single gain. A thousand agents at a gate fell
  from 9-15 cores to under 1.
- Dropping the outer thread saves one thread per agent and almost nothing else.
- Shrinking the ring cuts memory by 58-72% for parked agents and 20% in
  `working`. For a parked agent the ring costs more than its threads do.
- The outer-thread and ring builds were throwaway experiments. They ran the
  benchmark but not the full test suite.
- A smaller fixed ring is not safe to ship. `SessionRuntime.push` drops events on
  a full queue, so 64 slots would lose streamed output under load, and the
  benchmark does not check for that. Stage 0 keeps the 1,024 limit and makes the
  storage grow on demand instead, which saves nearly as much (#1612).
- That change costs no measurable throughput. An early five-run comparison on a
  busy machine suggested 10% lower `working` throughput, so it was rerun quietly:
  ten alternating rounds at N=1,000, swapping the order each round, with a load
  average under 8. The median was 81.2 runs per second with the change and 80.5
  without, with no bad results, and peak memory was 4,727 MiB against 6,105 MiB.

These fixes are worth doing first, but thread count stays linear. They top out at
about 6,100 agents at a gate or 3,000-4,000 in a model call.

## The design

### A run is data

A run becomes a plain value, and one pure function advances it:

```zig
const Phase = enum { idle, model_queued, model, tool_queued, tool, gate, backoff };

const Event = union(enum) {
    submit: []const u8,
    model_admitted,
    delta: []const u8,
    tool_delta: ToolDelta,
    finished,
    failed: Failure,
    timer,
    tool_admitted,
    tool_done: []const u8,
    gate: bool,
    cancel,
    resumed,
};

const Action = union(enum) {
    queue_model,
    send_model,
    queue_tool,
    run_tool,
    open_gate,
    abort_io,
    arm_timer: u32,
    settled,
    release_model,
    release_tool,
};

fn step(run: *Run, event: Event, out: *Actions) !void;
```

`step` does no I/O, so it can be tested by exhaustive script. The production
version also needs a compaction phase, steering and follow-up events, model
switches, tool progress, and several outstanding tool calls in one turn.
`failed` carries the classes `retry.classifyError` already returns: transient,
throttled, context overflow and fatal.

### State that moves

Most of a run's state is already data: `LoopState`, the turn configuration, retry
and withheld-result state, the queues in `Agent`, and `Run` and `Session` in the
adapter. Five things are not:

1. **Callbacks.** `AgentLoopConfig` holds about a dozen function pointers with
   context. A saved run stores which policy applies, and resuming rebinds it.
2. **Message ownership.** Events borrow pointers into the context's messages. That
   is why overflow recovery changes only the outgoing request, and why
   `StreamRetention` and `_retired_messages` exist. A run must own its messages,
   and events must carry copies or indexes. This is the riskiest refactor here,
   because a mistake is a use-after-free.
3. **In-flight I/O.** A socket or a child process cannot be saved, which is why
   only cold runs park.
4. **Session-lifetime processes.** Background jobs and MCP servers outlive a run.
5. **Implicit state.** The permission engine, the credential refresh lock and the
   title thread.

### The reactor

A few reactor threads each own a share of the sessions and block in the kernel
with no timeout, so a run needs no lock and an idle reactor never wakes. Other
threads post commands to a reactor through a queue and a wakeup event. File work,
TLS handshakes and process spawns go to a small bounded pool.

A run stays on one reactor while it is active, because moving live sockets and
child watches is not worth the complexity. Sessions are hashed to reactors, so
one reactor can run hot:

- Per-tick work limits stop a hot share from starving its own runs' timers.
- CPU-heavy work leaves the reactor, so a hot share is mostly I/O dispatch.
- Tool and model budgets are global, so a hot reactor cannot hold more than its
  turn. The prototype split them statically, and that probably cost it on
  `hold-tool`.
- A parked run is plain data, so it can resume on the least loaded reactor. This
  is the one cheap rebalancing point.

Commands arrive through one stdin reader that parses every line. At very high
request rates that single parser may become the limit, and it may need to shard
or do less work before handing a line to a reactor. This was not measured.

The base must be our own thin layer over `kqueue` and `epoll`. In the Zig 0.17.0
standard library:

| Backend | State |
|---|---|
| `Io.Evented` | `Dispatch` on macOS, `Uring` on Linux, `Kqueue` on the BSDs |
| `Io/Dispatch.zig` | `netConnectIp`, `netAccept` and `netListenIp` are unavailable |
| `Io/Uring.zig` | `netConnectIp`, `netAccept` and `netLookup` are unavailable |
| `Io/Kqueue.zig` | 36 `@panic("TODO")` sites, and not selected on macOS |
| `Io/Threaded.zig` | complete, and blocking |

Fibers are ruled out. The standard library's evented backends cannot open a
connection, their fibers reserve 60 MiB of address space each, and existing code
calls a blocking `compat.defaultIo()` throughout. The deciding reason is that a
fiber's state lives on its stack, so it cannot be written to disk, and 100,000
agents depends on parking. A run as a resumable value on a fixed worker pool gives
the lightweight task the goal asks for, and its stack is a plain struct.

## How each await-point moves

| Await-point | Move |
|---|---|
| Provider HTTP | Split each provider into building a request and feeding bytes that return events. SSE parsing is already incremental. The reactor owns the socket, so the in-process `ProtocolServer`, `ProtocolClient` and bridge threads leave the loop's path |
| TLS | Handshake on the bounded pool, then switch the socket to non-blocking and call `std.crypto.tls.Client` only when a whole record is buffered. `readIndirect` changes no state on a partial record |
| `Shell` | Spawn on the pool, then watch the pipe and the exit in the reactor. A child can exit before its watch is registered: `kevent` then returns `ESRCH`, which must count as exited |
| File tools | Run on the pool, with no fork |
| MCP | Move the child's stdio into the reactor, replacing the poll in `mcp_bridge.zig` |
| Background jobs | Replace the watcher thread in `shell.zig` with an exit watch |
| Gates | `phase = .gate`. A resolution is an event, and no thread waits |
| `agentLoopContinue` | Post `resumed` to an idle run |
| Cancel | An event: `step` emits `abort_io`, which closes the socket or kills the child |

The TLS approach was tried against a loopback LibreSSL server on TLS 1.2 and 1.3,
feeding one byte at a time. That test used `no_verification`, so real certificate
validation, partial writes and post-handshake messages are unverified.

## Hub changes

The loop alone cannot reach the goal, because the Hub is the first bottleneck, so
this work comes first and does not wait for the reactor.

`Hub.pump` visits every entry each pass: `drive` pumps it, then `drain` reads it.
`awaitAny` rebuilds a `pollfd` list of every session too. For `oapx`, `pump` also
records each streamed event on that one thread.

These fit the existing loop:

- **Ready queue.** `SessionRuntime.push` marks its session ready and wakes the
  Hub, and `pump` visits only ready sessions, so its cost stops growing with N. A
  session that cannot signal stays on an always-visit list, so other adapters keep
  working unchanged. `expireHolds` and `reclaim` also walk everything, and need a
  deadline queue.
- **Budgeted passes.** A pass handles a bounded amount of work, then returns to
  the stdin loop, so `state` is answered between slices. Under load today, `state`
  replies fall to 13-46 per second at N=1,500, and gates are noticed late.
- **Batched saving.** Events go to a writer thread that appends them in batches,
  with one `fsync` per batch that settles a run. The prototype's journal did this
  per reactor.

Answering `state` on another thread does not fit the existing loop, because all
Hub state belongs to one thread and every entry would need a lock. Budgeted passes
are the near-term answer. Off-thread `state` arrives with the reactor, which owns
its sessions.

The per-session journal, up to 65,536 events in memory, moves to disk with parking.
Replay then reads a file, which has to be designed together with subscription
resume across runs.

`drafts/hub.md` pins how events are saved, how the Hub wakes and how replay works,
with named tests. Each pull request in stages 2 and 7 that changes one of these
updates that draft and its tests in the same change.

## Parking

Parking saves a cold run and frees it. A resident session keeps a small summary,
and an event for a parked run loads it first. A parked run needs no thread or
socket.

**Only cold runs park:** `idle`, `gate` and `backoff`. These resume exactly. A run
in `model` or `tool` is active and stays resident, so nothing restarts a model
call and no tool outcome is ever unknown. This needs no wire change. A run waiting
in `model_queued` or `tool_queued` also stays resident at first.

`idle` wakes on a submit and `gate` on a resolution, but `backoff` wakes only on
time. A parked `backoff` run therefore leaves one entry in its reactor's deadline
queue, and that entry loads it when the retry is due.

A session with a live background job or MCP server does not park, at least at
first.

### Two levels of parking

There are two kinds of park, and one rule decides both, written once in
`drafts/hub.md`:

| Level | What it does | Condition |
|---|---|---|
| Session (#1615) | Stops an idle session's harness and keeps the session in Hub memory | No active run, no pending interaction, no live tool source or job. A third-party adapter parks only if it declares it can report live background work |
| Run (stage 7) | Saves a run to disk and frees it, on the new loop | The run is in `idle`, `gate` or `backoff`, and no tool source or job is live |

A subscriber blocks neither level. Both use one resident summary, so `state` can
answer for a parked session without waking it: its status and its active runs with
their pending interaction ids. A run park adds the saved phase. A pending
interaction stays answerable, and resolving it loads the run first.

A cursor from before a session park gets `oap-replay-gap`. A run park should not
need that, because the journal is on disk by then.

Crash recovery of in-flight runs is a separate subject, and this draft does not
cover it.

One protocol question remains: how replay behaves across a parked run once the
journal is on disk.

### Memory

At about 800 KiB per agent, as measured for `zig-loop` `history`, 100,000 agents
need about 79 GB, which does not fit in 48 GiB. All of these must hold:

1. Cold runs live on disk, about 80 GB for 100,000 real-size runs.
2. A resident session is a summary. The prototype's is about 2 KiB, and after
   stage 0 an empty session is about 26 KiB. The target is under 20 KiB, or about
   2 GB at 100,000.
3. Active runs have a memory budget. For example, 8 GiB holds about 10,000 runs of
   800 KiB. In-flight limits bound how many runs are truly active, but runs queued
   for a model or tool count too, so a large burst can exceed the budget.
4. The journal is not resident.

## Global tool budget

Tools run behind one queue with a global limit, so 100,000 agents never means
100,000 children. A simple foreground `Shell` call costs two processes against
the per-user limit of 8,000, which suggests about 4,000 concurrent calls. Everything
else the user runs shares that limit, so about half of it, 2,000 calls, is the
practical figure. Both are estimates, not bounds:

- A command can spawn any number of descendants, and nothing here limits them.
- A background job (`run_in_background`) returns at once and its process group
  lives until the session closes, so it would release its slot while its processes
  stay. Background jobs need a separate quota, held for the job's lifetime.
- Bounding descendants needs a per-call limit from the operating system, which is
  not designed here.

A process guard should therefore watch the real count and stop admitting tools
near the limit, rather than trusting the arithmetic.

The limit is a real trade-off. With a limit of 64, `working` at N=1,000 fell to 15
runs per second, because a `sleep`-style tool holds a slot while doing nothing.
With 1,024 it reached about 81. The budget must be shared across reactors: a
static split probably explains the prototype's loss on `hold-tool` (see below).
That cause has not been isolated.

File tools have their own limit, so a slow disk cannot hold every pool thread. A
full pool queues work and never blocks a reactor.

## Connections and rate limits

- Each reactor keeps a keep-alive connection pool per host, which removes the
  `TIME_WAIT` ceiling.
- In-flight model calls have a global limit, 4,096 in the prototype. A run past
  the limit waits in `model_queued` and holds no connection.
- HTTP/2 is deferred. The standard library has none, and writing it with HPACK is
  roughly 2,000-3,000 lines. A provider's rate limit arrives long before 4,096
  in-flight calls, so multiplexing buys nothing until one host needs about 10,000.

Rate limits are per provider, not per run. This part is design only and was not
prototyped:

- Each provider has a token bucket under the global limit.
- A 429 with `Retry-After` pauses that provider's whole queue, rather than having
  each run back off, or 100,000 runs would retry together.
- A queued run has not failed and spends no retry.
- Release after a pause is gradual, with jitter.

### Fairness and backpressure

- Model and tool queues are first in, first out. When 100,000 gates opened at
  once, p99 turn time was 370-383 s against a 300 s hold, and no run starved.
- FIFO holds only within a reactor. Fairness across reactors needs shared budgets.
- A reactor must cap the work it does per tick, using a ready queue rather than
  recursive admission.
- A slow subscriber must not stall a reactor. It overflows, as the subscription
  rules already allow.
- Building a large request is CPU work and belongs on the pool. About 1 ms per
  800 KiB is acceptable only as an occasional cost on a reactor.

## Prototype

A throwaway prototype is on branch `proto/async-loop` (`zig/proto/async_loop.zig`,
about 1,300 lines). It is not for merge. It runs the shape above with 4 reactors
and 4 pool threads on `kqueue`, speaks the Hub's stdio operations, and runs under
the concurrent scenarios in `scripts/bench-resources.py`. Models are a scripted
loopback endpoint over real sockets, and tools really run.

### Measurements

All runs were on one shared 18-core Apple Silicon Mac with 48 GiB, against a
scripted model. The baseline includes #1588 and #1592. Each pair ran alternately
in one window, and ranges cover two runs. No run returned a bad tool result.

| N=1,000 | Build | Peak threads | Peak MiB | Hub cores | Runs/s | Reply-to-next-request p99 ms |
|---|---|---:|---:|---:|---:|---:|
| `working` | baseline | 3,527-3,712 | 5,780-5,855 | 1.66-1.72 | 64.0-66.9 | 240 |
| `working` | prototype | 9 | 46-50 | 0.17-0.19 | 79.3-82.7 | 4,177-4,188 |
| `mixed` | baseline | 3,360-3,775 | 5,685-5,766 | 1.33-1.36 | 51.4-52.2 | 4,737-4,955 |
| `mixed` | prototype | 9 | 43-47 | 0.14-0.17 | 62.7-63.1 | 5,179-5,305 |
| `hold-model` | baseline | 4,003 | 3,006 | 0.80-0.82 | 40.6-40.8 | 1,577-1,741 |
| `hold-model` | prototype | 9 | 38-40 | 0.07 | 47.1-47.2 | 299-559 |
| `hold-tool` | baseline | 2,024-2,026 | 2,141 | 1.45-1.53 | 41.4 | 238-239 |
| `hold-tool` | prototype | 9 | 37 | 0.03 | 24.5 | 332-441 |
| `hold-gate` | baseline | 2,030-2,032 | 2,433-2,434 | 0.54-0.55 | 19.3-20.5 | 24,782-27,122 |
| `hold-gate` | prototype | 9 | 37-40 | 0.04-0.05 | 23.8 | 20,853-21,012 |

The prototype ran these with a tool limit of 1,024. Turn p50 in `working` and
`mixed` is about 1.5 s for both, set by the script's waits.

Only the prototype ran above the thread limit. These runs were `hold-gate`, with
every agent parked at a gate, then released through the tool queue:

| N | Mode | Result | Threads | Peak MiB | MiB at gate | Per agent |
|---:|---|---|---:|---:|---:|---:|
| 10,000 | resident | all finished | 9 | 186 | 184 | 18.8 KiB |
| 10,000 | parked | all finished | 9 | 77 | 66 | 6.8 KiB |
| 100,000 | resident | all finished | 9 | 1,020 | 1,001 | 10.3 KiB |
| 100,000 | parked | all finished | 9 | 224 | 209 | 2.1 KiB |

- With 100,000 runs parked, the Hub had zero context switches and zero CPU in
  every sample between polls, across 484 s of samples.
- Every run did real socket model calls, a real `Read` and, after its gate, real
  `Shell` and `Write` calls.
- A self-test suspends and resumes one scripted run at each of 23 points, and each
  ends byte-identical to an uninterrupted run. That script covers the prototype's
  small set of events only.
- Parking 808 KiB took 0.9 ms to save and 1.0 ms to load, with a warm page cache.
- Cancel returned to idle at once in a model call and at a gate, and in 63 ms in
  a tool, with the child gone. The same session then answered normally.
- Real tools all in flight at once are proven only to N=1,000, a deliberate limit
  on a shared machine. At 100,000 they ran through the queue, and the 5,000-process
  guard did not trip, but the true peak was not recorded.

### Where the prototype flatters itself

Read these numbers as a ceiling, not a forecast.

- It has no real permission engine: anything but `Read` simply gates. It also has
  no OAP envelopes or validation, replay, subscriptions, steering, compaction, MCP,
  background jobs, TLS in the run path, credentials, or second provider.
- Its histories are a few short messages, so the 2 KiB and 10 KiB figures say
  little about a real session.
- Its journal is written and never read, and its records are simpler than real
  envelopes.
- It loses in places. `hold-tool` gives 24.5 runs per second against 41.4, and the
  reply-to-next-request p99 in `working` is 4.2 s against 0.24 s.
- At high N, a single Python driver and four model workers set the pace, so runs
  per second there says nothing about the prototype's limit.

## Stages

Each stage has a measured pass condition. The benchmark is
`scripts/bench-resources.py`, comparing alternating runs in one window.

The old path keeps working throughout. A vertical slice runs through the new
driver early behind a per-session opt-in, then each piece widens and its old path
is deleted, so there is no late switch-over. Line counts are changed production
lines, and stages 2-7 total about 8,400-14,500 in 60-100 pull requests.

| Stage | Work | Lines | Pass condition |
|---|---|---:|---|
| 0 | Done: ring grows on demand (#1612), shared HTTP client (#1613) | Merged | Empty sessions 271 to 26 MiB and peak `TIME_WAIT` 8,000 to under 200, at N=1,000 |
| 1 | Retire the unknowns below, on the prototype branch | No product code | Every stop condition below is clear |
| 2 | Hub ready queue, budgeted passes, batched saving, on the existing loop | 800-1,500 | `working` at N=1,500 stops degrading, `state` stays over 500 per second, and an idle Hub takes no wakeups for sessions with nothing to do |
| 3 | Reactor and bounded pool on `kqueue` and `epoll`, unused | 800-1,200 | Unit tests on macOS and Linux, and an idle reactor takes zero wakeups |
| 4 | Extract `step` from `runLoop`, with a blocking driver executing actions | 1,800-3,000 | The differential gate below passes on every scenario |
| 5 | Vertical slice behind a per-session opt-in: one provider, `Shell`, gates, cancel | 1,500-2,500 | Opted-in `working` at N=1,000 matches the old path's traces, uses fixed threads, and handles cancel in each phase |
| 6 | Widen provider by provider, then file tools, MCP, background jobs, tool budget; delete each old path | 2,700-4,800 | Each piece passes the differential gate, and its threads are gone from the phase table |
| 7 | Parking, journal on disk, session summaries, eviction | 800-1,500 | Parked `hold-gate` at N=100,000 on real-size histories, with zero idle wakeups |

**Final acceptance, on Linux:** real tools at N=10,000 with a fixed thread count,
flat idle CPU, and no bad tool results, then parked agents at N=100,000.

The agent limit starts moving at stage 5, and the Hub's bottleneck moves at stage
2 rather than at the end. Stages 4 and 6 carry the most risk.

### Stage 1: retire the unknowns first

These are cheap next to about 14,500 lines, and they decide whether those lines
are justified. They use the prototype, not product code.

| Check | How | Stop or rethink if |
|---|---|---|
| Linux | Add `epoll` to the prototype and run `hold-gate` at N=10,000 on a runner | Threads grow with N, or parked agents cause wakeups, and the cause is not a prototype bug |
| Real TLS | Non-blocking record feeding with full certificate validation, against a real provider endpoint. An unauthenticated request is enough, so no key is needed | The standard client cannot validate a real chain or keep state across partial records. A vendored TLS library would then join stage 6 and the size would need redoing |
| Real sizes | `hold-gate` at N=10,000 with histories near 800 KiB, parking on | A parked session stays over 20 KiB resident, or resume p99 exceeds 1 s, or the run needs more disk than planned |

If Linux or real sizes fail in a way that cannot be fixed, the plan stops at stage
2, because stage 0 and the Hub work stand alone.

### The differential gate

"No behaviour change" needs proof, because scripts written for `step` only test
`step` against itself. Before stage 4, record event traces from the old loop across
the existing scenarios and fixtures: text, tool calls, gates, cancel at each phase,
retries, overflow recovery, steering and compaction. `step` must reproduce each
trace, ignoring only ids and timestamps. Building that harness is part of stage 4's
size, and the same traces gate stages 5 and 6.

Overflow and retry state (#1572, #1578, #1590) is already shaped as data and moves
into `step` directly. Subscription lifetime (decision 0066) fits well, because a
subscription belongs to a connection and a session rather than a run thread, but
its resume across runs must be designed with the on-disk journal.

## Risks

1. The prototype's gains will shrink once real work is added.
2. Six provider files, about 15,000 lines with tests, hold many error-handling
   details, and each split can lose one.
3. The message ownership change can introduce use-after-free bugs.
4. One blocking call left on a reactor stalls every run in its share, so debug
   builds need a detector.
5. Replay across a parked run may turn out to need a wire change.
6. Two drivers coexist from stage 5 until the last piece moves, though the old
   path shrinks at every step.
7. `agent_loop.zig` and `agent.zig` are hot files, so other work there will
   conflict and has to be coordinated.

## Not verified

- Linux. The prototype has no `epoll` path and has never run there.
- Real certificate validation, and real providers.
- 100,000 runs of real size. Only one 808 KiB run was timed.
- Rate-limit pausing, fairness across reactors and per-tick work limits, which are
  design only.
- The cause of the prototype's `hold-tool` loss.
- Isolation. The machine was shared, and other benchmarks ran between these.
- What limits the prototype next. At 100,000 it stayed near 0.25 Hub cores, so it
  was not its limit. Disk and active memory at real run sizes are the expected next
  limits, then the single thread parsing stdin. Neither was measured.
- The Hub stage's gains on the existing loop. Removing the per-event save has been
  measured at 35-40 to 65-67 runs per second at N=1,000, but the ready queue and
  budgeted passes have not been tried.

## Rust

The Rust hub also scales threads linearly: 10,003 threads at 10,000 waiting agents.
`read_run` in `rust/serve/src/live.rs` spawns a thread per run to drive an
`EventStream`. Driving those streams from one fixed executor with wakers, and
removing the 5 ms sleeps in `stdio.rs`, should fix it. That estimate comes from
reading the code and is untested.

## Open decisions

The owner has not decided these, or the plan as a whole.

1. **Disk.** About 80 GB for 100,000 real-size runs, with ageing.
2. **Tool budget.** A default global limit of 1,024 concurrent `Shell` calls,
   configurable. Beyond it, calls queue.

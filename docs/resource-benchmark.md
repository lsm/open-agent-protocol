# Resource benchmark

`scripts/bench-resources.py` shows how each implementation's hub grows with
the number of agents it holds. The goal it measures against is that 10,000
agents should cost about what 10 do, so its headline number is the per-agent
slope, not a total.

```sh
python3 scripts/bench-resources.py --out /tmp/oap-bench
```

That builds `goap`, `oap-rust-hub` and ReleaseSafe `oapx`, runs every scenario
at N = 10, 100, 1,000 and 10,000, prints one Markdown table per target and
writes `results.json` and `results.md`. Narrow a run with `--targets`,
`--scenarios` and `--n` (comma lists), and skip the builds with `--no-build`
plus `--go <goap>`. It needs Python 3 and the three toolchains; it runs on
macOS and Linux.

## In the benchmark report

`--report scaling.jsonl` also writes one JSON line per target and scenario in
the shape of the [micro benchmark reports](performance-baseline.md): the same
`schema_version`, `git_revision`, `host_class`, `target`, `cpu_model`, `mode`
(`scaling`) and `workload` (`go/history`), then every row and the per-agent
slopes. `zig build bench-compare` reads it like the other two modes and prints,
per workload, each slope's baseline and candidate value with its change, and the
largest N each one completed, marked `stops earlier` when the candidate gave up
sooner. Each line names the toolchain that built its hub, and a
pair built by different toolchains is not compared, so both benchmark
workflows pin exact Go, Rust and Zig versions; move all three pins and
re-capture the baseline together. It refuses a report with a `failed` row, or whose smallest N did not
complete, since that is a broken run rather than a measurement.
`scripts/capture-benchmark-baseline.sh` captures it at N = 10, 100 and
1,000, so the Capture Benchmark Baseline and Benchmark Report workflows keep
and compare it with the others.

## Targets

| target | process | adapter |
| --- | --- | --- |
| `zig` | `oapx serve --stdio` | the scripted `memory` adapter |
| `go` | `goap serve --stdio` | the scripted `memory` adapter |
| `rust` | `oap-rust-hub serve --stdio` | the scripted `memory` adapter |
| `zig-loop` | `oapx serve --stdio` | oapx's own agent loop (`oapx` type) against a local scripted provider |

The first three run the same reference script, so they compare hubs and
session bookkeeping like for like. `zig-loop` is the product path: a real loop,
real tool execution and saved transcripts, with the model replaced by a
loopback openai-completions server that answers `ok`, or one `Shell` call that
runs `true`.

Every hub runs with `--session-history` in a fresh temporary directory, with
`HOME` there, an empty environment apart from `PATH`, a throwaway
`OAPX_KEYCHAIN_SERVICE`, and on macOS a sandbox that denies reads under the
real home, Security services and any network but loopback. No model key is
read or needed.

## Scenarios

Each case starts a fresh hub, opens N sessions (`s0` … `sN-1`) and leaves them
open.

| scenario | each agent | step timed |
| --- | --- | --- |
| `empty` | opens | the open |
| `history` | runs three turns to completion, then sits idle | each turn |
| `waiting` | submits, then stops at the first permission request (`zig-loop` opens in `ask` mode and the call is a foreground `Shell`) | submit until the request is pending |
| `jobs` (`zig-loop` only) | runs one turn whose `Shell` call starts `true` with `run_in_background`, which exits at once | the turn |
| `burst` | all N submit back to back, then every run is driven to completion | each submit |

The memory adapter's script raises a permission request and then a user input
request; the driver approves and answers both. Go and Rust have no shell jobs,
so `jobs` runs only on `zig-loop`.

## Flights

The scenarios above drive agents one at a time, so they show what agents cost
sitting still. The flights run only on `zig-loop`, and only when named in
`--scenarios`: they keep all N agents in flight at once.

```sh
python3 scripts/bench-resources.py --targets zig-loop --n 10,100,1000 \
  --scenarios working,mixed,hold-model,hold-tool,hold-gate --rss-cap-mib 12288
```

| scenario | each agent |
| --- | --- |
| `working` | two runs of Read, then `sleep` for 1-5 s in `Shell`, then Write, then closing text |
| `mixed` | as `working`, but every third agent opens in `ask` mode and its gates are held for 2 s, so some agents are in the model, some in a tool and some at a gate |
| `hold-model` | stays in its first model call for `--hold` seconds |
| `hold-tool` | stays in `Shell` for `--hold` seconds |
| `hold-gate` | opens in `ask` mode and each gate is held for `--hold` seconds |

What is real: the hub, the loop, HTTP to the model endpoint, the tools, a
workspace per agent, and the check that each tool result reached the next
request. What is scripted: the model. It reads the turn from the conversation,
waits 500 ms for its first token, then streams 20 chunks 50 ms apart.

The model endpoint is a forked pool of asyncio workers (`--model-workers`) in
its own processes. The driver is one client that writes up to 64 requests at a
time and reads each live session's `state` every `--poll` seconds, because a
stdio hub takes one client and at most 64 subscriptions. So runs are noticed
when polled, a run's latency includes that wait, and the polling is itself load
on the hub. Turn figures come from the model endpoint's clock instead:

- **turn**: from one model request of a run to the next.
- **between**: from a model response ending to the next request, less the
  `sleep` the tool was told to do. With gates it includes the time held.
- **hold threads, cores, switches/s**: the median thread count and the rates
  while at least 98% of agents sit in the held phase.
- **model worker peak cores**, **driver peak cores**: the busiest second of any
  one worker, and of the driver. Read these first: near 1.0, the benchmark
  rather than the hub is the limit.

`results.json` also keeps a timeline sampled every 250 ms. Because tools are
real processes, two per `Shell` call, a flight is skipped when 2N on top of what the user already runs
would pass `--max-user-processes` (5,000), and stopped if the count gets there
or the hub holds `--max-hub-cores` (12) for 5 s. A stopped case kills the hub's
whole process group.

The model client opens a connection per call, and on macOS each closed one holds
an ephemeral port in TIME_WAIT for about 30 s, out of about 16,000 for the whole
machine. Sustained runs at N = 1,000 and above can use them up, and every
program on the machine then fails to connect. So a flight is skipped or stopped
at `--max-time-wait` (12,000) sockets. On a shared machine, run one such case at
a time, at least 35 s apart.

`--loop-binary` runs `zig-loop` on another binary taking `oapx`'s `serve`
arguments.

## Columns

- **peak RSS**: the largest memory reading sampled every 10 ms while the
  scenario ran. **settled RSS**: one sample a second after it finished. The
  reading counts what the hub holds, not only what is resident right now:
  `ri_phys_footprint` on macOS and `VmRSS` plus `VmSwap` on Linux. Plain
  resident size is the wrong number here, because the kernel compresses or
  swaps idle pages out of it: one 10,000-agent run read 1.8 GiB resident
  against an 8.9 GiB footprint.
- **threads**, **FDs**: the OS thread count and open descriptors, settled.
  Goroutines and async tasks are not threads and do not show here.
- **written**: bytes the hub wrote to files, from the kernel's per-process
  counter (`ri_logical_writes` on macOS, `write_bytes` on Linux). macOS counts
  whole pages, so a small append reads as a page; compare it with
  **stored**, the growth in bytes of every file under the case's directory, to
  see write amplification.
- **step p50/p99**: the timed step above, measured from the driver. It includes
  the driver's stdio round trips and its polling of `state`.
- **per agent**: the least-squares slope over the N values that finished,
  in the unit shown. Fixed process cost drops out; a slope that rises with N,
  or a row that stops, is where the target does not scale.

A case is stopped and its row marked when the hub's RSS passes
`--rss-cap-mib` (4 GiB by default) or the case passes `--timeout` (600 s);
the row then records how many steps it reached.

## What it does not measure

- Model throughput or token cost: the provider is scripted and local, and the
  memory adapter has no model at all. Flights script a latency; the others have
  none.
- Real work in commands: the commands run are `true` and, in flights, `sleep`.
- Concurrency inside the hub beyond what one stdio client drives: outside the
  flights the driver issues one request at a time, so `burst` holds N runs
  active at once but does not submit them in parallel.
- Recovery, restart or reopen.
- The file run stores (`zig/src/hub/run_store.zig`, `go/binding/runs.go`,
  `rust/binding/src/runs.rs`): none of the three served hubs calls them yet, so
  their append cost is not on any path this drives. The session history and
  oapx's saved transcripts are.
- Zig shell job watcher threads exit when their command does and are joined
  only when the session ends or the job is killed; an exited, unjoined thread is no longer in the
  thread count, so `jobs` shows its residue in RSS, not in threads.

Numbers depend on the machine and its load; compare slopes from one machine,
not across machines.

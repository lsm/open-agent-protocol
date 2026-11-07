# HTTP Hub descriptor and deadline waits

Use the shared toolchain and `$EVIDENCE` setup in `SKILL.md`, then run from the
repository root.

```sh
zig build --build-file zig/build.zig test-unit-hub -Doptimize=ReleaseSafe -j4
zig build --build-file zig/build.zig -Doptimize=ReleaseSafe -j4
python3 .claude/skills/verify/scripts/hub-http-deadlines.py --output-dir "$EVIDENCE/http-deadlines"
python3 .claude/skills/verify/scripts/hub-budgeted-passes.py --hold-expiry --output-dir "$EVIDENCE/http-held"
```

The first script starts the real HTTP Hub with a native adapter, an isolated
temporary home and a loopback synthetic provider. Listener ports are random.
After ten seconds of startup settling, require zero process context switches
and zero CPU increase during five quiet seconds. Inspect `idle.json`.

It then sends a partial HTTP header and requires the real thirty-second header
deadline to return 408. It opens an idle native session and requires an SSE
keepalive comment within the stream's timeout. The native session has readiness
notifications, so these checks exercise descriptor waits. Inspect the timeout
response, timing, opened envelope and keepalive artifacts. SIGTERM must stop the
Hub with exit 0 and both owned processes must be reaped.

The held-subscription journey additionally observes every memory-adapter gate,
cancels and closes all sessions, and checks an accepted request after hold
expiry. Require its `PASS` output and successful cleanup.

## Whole-stage resource acceptance

Preserve a ReleaseSafe baseline binary from before Stage 2. With the candidate
source and verification scripts committed, build the candidate and run the
alternating resource window with both binaries:

```sh
python3 .claude/skills/verify/scripts/hub-stage2-benchmark.py --baseline "$BASELINE_BINARY" --output-dir "$EVIDENCE/stage2"
```

The script snapshots both binaries, records their hashes and the candidate
revision, and alternates order across three rounds. Each build runs the official
resource benchmark's empty and working cases at N=1,000 and 1,500. Complete
builds and other verification before starting this window.

Require every row to be `ok`, no refused calls or bad tool results, and every
expected output file written. Every candidate working run must exceed 500 state
responses per second. Candidate median working throughput at N=1,500 must reach
its N=1,000 median. Every candidate empty case must record zero CPU increase and
zero process context switches after its explicit ten-second settling interval.
Retain `acceptance.json`, all raw results, logs and binary snapshots. A failed
condition remains a failed window in this record.

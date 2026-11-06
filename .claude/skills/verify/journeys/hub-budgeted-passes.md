# Budgeted Hub passes

Use the toolchain and isolated build setup in `SKILL.md`. Build ReleaseSafe and
run the Hub group from the repository root:

```sh
zig build --build-file zig/build.zig -Doptimize=ReleaseSafe -j4
zig build --build-file zig/build.zig test-unit-hub -Doptimize=ReleaseSafe -j4
```

The public memory-session regression opens 80 sessions, submits to each, and
observes subscriptions. A pass must deliver to at most 64 sessions, then later
passes must reach the last session. This catches a pass that drains every
session before returning to its host. The elapsed budget is checked between
sessions, so one adapter pump can still exceed it.

Keep a ReleaseSafe main binary before changing source. Run the resource
benchmark alternately with that binary and the candidate, in one window:

```sh
python3 scripts/bench-resources.py --targets zig-loop --scenarios working --n 1000,1500 --rss-cap-mib 12000 --timeout 600 --no-build --loop-binary "$BASELINE_BINARY" --out "$EVIDENCE/baseline"
python3 scripts/bench-resources.py --targets zig-loop --scenarios working --n 1000,1500 --rss-cap-mib 12000 --timeout 600 --no-build --out "$EVIDENCE/candidate"
```

Both runs use the real stdio Hub, a scripted loopback model, and real Read,
Shell and Write calls. Require every result to be `ok`, zero bad tool results,
zero refused calls, and every session's expected file to be written. Retain
the JSON reports with their timelines and compare runs per second, state polls,
and response delays. The benchmark owns temporary homes, random listener ports,
provider processes and Hub processes and removes them during cleanup.

This slice bounds session visits and elapsed work between sessions. Readiness
notifications and batched persistence are separate Stage 2 slices. The full
stage still requires over 500 state replies per second at N=1,500 and no
wakeups caused by sessions with no work.

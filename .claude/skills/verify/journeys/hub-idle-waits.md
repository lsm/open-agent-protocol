# Hub descriptor waits

Use the toolchain and isolated build setup in `SKILL.md`, then run from the
repository root.

```sh
zig build --build-file zig/build.zig test-unit-hub test-unit-stdio-host -Doptimize=ReleaseSafe -j4
zig build --build-file zig/build.zig -Doptimize=ReleaseSafe -j4
python3 .claude/skills/verify/scripts/hub-idle-waits.py --output-dir "$EVIDENCE/signals"
python3 .claude/skills/verify/scripts/oapx-session-restart.py --auto-park --output-dir "$EVIDENCE/restart"
python3 scripts/bench-resources.py --targets zig-loop --scenarios empty --n 10,1500 --idle-window 5 --no-build --out "$EVIDENCE/idle"
```

The signal journey keeps stdin open throughout. A quiet Hub must exit 0 on
SIGTERM. Another Hub reaches a real memory-adapter permission gate. Its first
SIGINT must report graceful draining and leave the run visible to `state`.
Its second SIGINT must report forced shutdown and exit 0. Inspect the stderr,
gate states and cleanup records. Every owned child must be reaped.

The restart journey observes asynchronous native reads and a completed model
turn with no further input needed to publish it. Automatic parking must still
happen on its timer, and a subsequent input must restore the old conversation.

The empty benchmark allows `--idle-settle` seconds for startup work and thread
retirement, then samples process CPU and context switches over `--idle-window`
seconds. Both intervals are explicit in the JSON. Require `status` to be `ok`, zero
`idle_switches` and no CPU increase in the native idle case. The idle fields
report process context switches and CPU deltas. Retain
`results.json` and compare alternating baseline and candidate runs in one window.

The Hub tests observe a shared readable handle after a command finishes and
after the final native session closes. Consuming the wake must make the handle
quiet again even with no sessions. Signal tests require a readable descriptor,
draining on count, a monotonically increasing signal count and preserved libc
errno. The parking test retries a refused close at the next idle deadline.

POSIX stdio uses descriptor and deadline waits for notifying sessions. Backends
with fallback obligations and custom stop callbacks use bounded polling.
Windows uses bounded completion/peek waits. HTTP's polling budget changes in
the separate deadline slice.

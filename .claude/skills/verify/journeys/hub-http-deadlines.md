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

# Resource benchmark stall capture

Run on the designated test host with a ReleaseSafe oapx built from the current
tree. This check holds one synthetic model request and exercises the normal
benchmark CLI, its timeout, the retained diagnostics and run directory. It also
checks an unusable capture destination, a timeout without capture and an
ordinary completed case. The failed capture must retain the timeout, hub log
and session history after the benchmark exits. A controlled
loopback connection creates TIME_WAIT state. On Linux an unavailable netstat
fixture verifies that capture uses the portable counter instead of recording
a silent zero.

```sh
python3 scripts/check-bench-stall-capture.py --binary zig/zig-out/bin/oapx --output-dir /tmp/oap-stall-check
```

Require 4/4 checks, exit 0 and `live_stack_captured: true`. The macOS stack uses
`sample`; Linux uses `gdb` and requires permission to attach to the hub. If the
test host restricts ptrace, use `--allow-unavailable-stack` to verify diagnostic
retention and the recorded failure separately. A false live-stack result leaves
Linux stack sampling unverified. All model replies are synthetic, N is one,
and child HOME, state and environment are isolated.

# Rust standalone auth-flow lifecycle

From the repository root, run:

```sh
(cd rust && cargo test -p oap-validation --test auth_lifecycle)
python3 .claude/skills/verify/scripts/rust-auth-lifecycle.py --output-dir /tmp/oap-auth-lifecycle-<unique-name>
```

The helper builds and invokes the public Go validator CLI, Rust `validate`
example and rebuilt `oapx validate` using synthetic trace files only. It never
authenticates, reads credentials or calls providers. Inspect `results.json`
and the retained inputs/stdout/stderr: all three must agree on exact diagnostic
code sets, semantic phase and exit status, with zero disagreements. Direct
`oapx` calls avoid depending on Zig's cached fixture tests.

Observe `auth_flow_order` for events, completion or cancellation before a start
response, and for reuse of either an active or completed flow ID. Gapped or
repeated event/terminal sequences must draw `sequence_gap` or
`sequence_regression`. An event, second completion or cancellation after the
terminal must draw `event_after_terminal`. An accepted flow without completion,
including one whose cancellation was acknowledged, must draw
`missing_auth_terminal` at its start response.

Immediate completion, interactive progress, completed cancellation, a refused
start and two interleaved flows with independent sequence domains remain valid.
Renumbering the three sequence-error traces repairs them.

With no concurrent build or edit in this worktree, run:

```sh
python3 .claude/skills/verify/scripts/rust-auth-lifecycle.py --mutation-check --output-dir /tmp/oap-auth-mutations-<unique-name>
```

Every mutation must produce its exact changed public CLI result; build failures
do not count. Sources are restored in `finally`, the Rust consumer rebuilt, and
the full corpus replayed through all three CLIs. Check source restoration if
Python itself is forcibly killed.

Before merging, integrate main locally and run Go validation, the Rust manifest
test and Zig `test-unit-validation`, then repeat the public replay with zero
unexpected disagreements. Run the shared baseline and inspect its isolated PTY
reply/quit evidence. Fixtures cite register rows `0029#decision/1–5`.
Provider/cancel-response binding and capability gates are outside this port.
Real authentication, credential persistence and live providers remain unverified.

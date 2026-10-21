# Recovery redelivery judgments

From the repository root, run:

```sh
python3 .claude/skills/verify/scripts/redelivery-state.py --output-dir /tmp/oap-redelivery-state-<unique>
```

The helper builds and directly invokes Go's `goap validate`, Rust's public
`validate` example and the rebuilt `oapx validate` on every `redelivery-state-*`
fixture plus the original redelivery wire trace. It checks each exit status,
exact diagnostic codes and phase against the manifest, retaining stdout and
stderr in the output directory. It does not rely on Zig's cached fixture tests.

Observe acceptance of a history-free recovered queue, a repeated redelivery
with stable roots, withdrawal of all held prompts, a crash queue with no shutdown
event, a first carrier whose admission is observed, and held evidence from either
shutdown response or shutdown event. Replaced queued carriers close through the
recovery record, without fabricated terminal events. Ordinary live-session
opens, including between shutdown acceptance and restart, continue to retain
reservations under their existing IDs and leave held evidence for recovery.

Observe `session_state_mismatch` refusals for duplicate predecessors, roots or
new carriers; old carriers still listed as active; missing queued membership;
new order differing from the active queue; missing, extra, reordered or omitted
held pairs; a held order contradicting observed admissions; changed first or
subsequent roots; stale predecessors; forks of existing roots; and reuse of a
withdrawn root; and roots belonging to another pair. Every new fixture cites register row `0050#5/3`.
Also observe refusal of an unpaired restored queued carrier, a restart omitting
all pairs, and a later changed root after an unpaired first response. For that last trace,
the helper requires the diagnostic at the first bad response (index 3). Cursor-resume
fixtures with a running run and reservation remain accepted; these do not declare
a restart. Carrier IDs seen only as old predecessors or withdrawn roots cannot
be reused as fresh carriers. The restored-queue reopen fixture now carries its required pairs.

For repeatable source mutation checks, run with no other builds or edits in this
worktree:

```sh
python3 .claude/skills/verify/scripts/redelivery-state.py --mutation-check --output-dir /tmp/oap-redelivery-mutations-<unique>
```

Use `--language go`, `--language rust` or `--language zig` to run one tree.
Each tree is weakened in turn at pairing, membership/order, held completeness,
root preservation, chain-head, old-carrier, observed-order and closure checks.
Go and Rust mutations replay the public CLI. Zig mutations use the focused
`test-unit-validation` manifest gate to avoid rebuilding the entire runtime for
each edit; the helper requires that the executed target fixture reports the
changed codes. The targeted invalid trace must become accepted, or the targeted
valid chain must become rejected. Compiler errors and malformed output do not
count as mutation catches. Sources are restored in `finally`, the consumer binary is
rebuilt, and all fixtures are replayed again after restoration. An interruption
that kills Python itself requires checking source restoration before continuing.

Before merging, integrate current main locally and run `go test ./go/validation`,
`cargo test -p oap-validation --test manifest` from `rust/`, and
`zig build --build-file zig/build.zig test-unit-validation -Doptimize=ReleaseSafe`,
then rerun the public CLI replay with zero disagreements. The shared skill's
baseline and isolated PTY smoke remain required. This journey proves validator
judgments from wire evidence; it does not exercise live providers or endpoint
persistence across a real process restart.

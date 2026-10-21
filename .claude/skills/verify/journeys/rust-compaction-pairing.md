# Rust per-run compaction pairing

From the repository root, run:

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-validation --test compaction_pairing
python3 .claude/skills/verify/scripts/rust-compaction-pairing.py --output-dir /tmp/oap-compaction-pairing-<unique>
```

The helper builds Go's public validator, Rust's public `validate` example and
ReleaseSafe `oapx`, then directly replays shared synthetic traces and repaired
controls. Inspect the retained input/stdout/stderr and `results.json`: all
three must agree on exact codes, semantic phase and exits. The wrong-end case
also pins diagnostic indices, so moving the fault to a later correct end does
not pass. Direct `oapx validate` avoids cached Zig fixture-test results.

A second start while a pair is open draws `compaction_unpaired` and preserves
the first start. An end with no matching open start in its own run draws
`compaction_ended_without_start`, including a second end; a wrong end must not
close another pair. Completed, failed and cancelled run terminals draw
`compaction_open_at_terminal` while a pair is open. A failed compaction end
without an error draws `compaction_failed_without_error`, independently of
whether that end names the right pair.

Valid controls include requested and threshold compactions, sequential pairs,
interleaved runs with independent pair state, failed ends carrying errors,
and cancelled compaction outcomes. Every invalid shared trace has a repaired
control. Compaction events after a run terminal must retain the core's
`event_after_terminal` result rather than reopening the pair.
All data is synthetic; no endpoint, real transcript, credential, authentication
or provider is accessed.

With no concurrent build or edit in this worktree, run:

```sh
python3 .claude/skills/verify/scripts/rust-compaction-pairing.py --mutation-check --output-dir /tmp/oap-compaction-mutations-<unique>
```

Each mutation must compile and produce its exact changed public result.
Compiler errors and inconclusive failures do not count. Sources are restored
in `finally`, Rust rebuilt and the full corpus replayed through all three
CLIs after restoration. Inspect `mutations.json`; check restoration if the
helper was forcibly killed.

Integrate current main before final Go validation, Rust manifest and Zig
`test-unit-validation` gates and the public replay, with zero unexpected
disagreements. Run the shared Verify baseline and observe the isolated PTY
fixture reply and `/quit` exit 0, with owned children reaped and temporary
state removed.

Only register `0044#the-barrier/4,6–7` and
`0044#what-a-compaction-looks-like-on-the-wire/1` are ported here. Request and
capability gates, requested-run opening/settlement, compaction ID reuse after
an ended pair (row 5), session-wide uniqueness, deferred claims and
admission/lead/active/model anchors remain outside this slice.

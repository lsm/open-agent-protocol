# Rust deferred per-run snapshot claims

From the repository root, run:

```sh
python3 .claude/skills/verify/scripts/rust-deferred-captures.py --output-dir /tmp/oap-deferred-captures-<unique>
cargo test --manifest-path rust/Cargo.toml -p oap-validation --test snapshot_claims
```

The helper rebuilds and invokes Go, Rust and Zig's public validator CLIs on 41
shared and mutated traces. Pending interactions must match the stated position,
including requests and resolutions observed after a snapshot, historical reads
after a resolution, and opaque interactions recovered behind a cursor. Naming
pending interactions without a position fails; a position the run never reaches
fails at trace closure. The same rule applies to open responses and to snapshots
on an endpoint that does not advertise the queue.

A queued or cancelling reservation's claimed position must agree with its
eventual start. Reporting a reservation as executing obliges a later start.
A future settled claim must be answered by the next event in that run, with the
claimed terminal sequence; activity instead, a different sequence or no terminal
fails. Both Decision 0057 advertisement arms remain covered. The control-call
recovery and steer-position fixtures also exercise these shared capture rules.

With no other builds or Rust edits in the worktree, run:

```sh
python3 .claude/skills/verify/scripts/rust-deferred-captures.py --mutation-check --output-dir /tmp/oap-deferred-mutations-<unique>
```

Seven source mutations weaken pending-set equality, trace-closure checks,
queued/cancelling promotion checks, next-event settlement, and interaction opening
and resolution positions. Each must compile and change a public CLI judgment;
compiler errors do not count. Sources are restored in `finally`, the validator
is rebuilt, and all Rust cases are replayed after restoration. The script saves
each trace, raw output, exit status and comparison.

The failing fixture-shaped public API test and CLI reproduction were committed
before the fix. Register rows 0007 captured-at/20–25 and settled/1,3,4 name the
implementation. Admission-leading entries, their anchors and model captures are
separate work. Run the shared baseline, Rust all-features suite and all three
validation gates before merge, with zero unexpected disagreements. Live provider
calls and real endpoint persistence are not exercised by this journey.

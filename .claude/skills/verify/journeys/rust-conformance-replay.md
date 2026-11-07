# Rust conformance replay and fatal framing contracts

Use shared setup from `SKILL.md`, then run

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-conformance --test replay
python3 .claude/skills/verify/scripts/rust-conformance-wire.py --output-dir /tmp/oap-conformance-replay-<unique-name>
```

Require the real rebuilt Rust memory endpoint to accept cursor replay and
re-deliver the run with identical ordered ids, types and sequences. The
collected trace must contain only one copy of the run's completion. Require
twenty-six passing staged checks and EOF exit 0. Two further fresh endpoint
processes must exit nonzero after a malformed line and an envelope without
an id. Require both framing checks to pass.

Public API tests reject changed ids, types, sequences, order and membership,
replay gaps, unexplained controls, absent answers and an empty original run.
Only explicit `unsupported_control` is skipped. Foreign and unsequenced event
floods must leave replay collection bounded. Framing tests check exact fatal
inputs and reject zero exits, spawn failure and an EOF-ignoring chatty peer.

Source, build logs and output are retained. Runtime HOME/data/config are
temporary and process groups are cleaned up. Run the complete baseline too.

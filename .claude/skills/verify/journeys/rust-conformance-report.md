# Complete native Rust conformance report

Use shared setup from `SKILL.md`, then run

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-conformance --test report
python3 .claude/skills/verify/scripts/rust-conformance-wire.py --output-dir /tmp/oap-conformance-report-<unique-name>
```

Require `consumer.txt` to show the standalone public `run` API passing all
twenty-seven checks, including Rust trace validation, EOF and fatal framing.
The same script builds the repository `endpoint_conformance` Cargo example
and drives both rebuilt Rust and Go memory endpoints through its normal
command entry point. Require `rust-report.json` and `go-report.json` to contain
twenty-seven passing checks, `passed: true`, no diagnostics and no serialized
trace. Both commands must exit 0. `refusal-report.json` must contain failed
checks and `passed: false`, and its command must exit 1.

Public API tests ensure an otherwise completed script fails its verdict when
the independent Rust validator finds malformed envelopes. They also check
explicit model/session options, typed spawn failure, a bounded transport
timeout, retained diagnostics and omission of the raw trace from report JSON.

This is the Unix process runner. HOME/data/config are temporary, replies are
synthetic and the script reaps its process groups. It retains source, build
logs, report JSON and stderr. Run the complete baseline and full Rust workspace
tests too. Live providers and other operating systems need separate proof.

# Rust vocabulary and diagnostic foundation

Use the shared setup and verification mutex in `SKILL.md`, then run from the
repository root:

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-protocol --test vocabulary
cargo test --manifest-path rust/Cargo.toml -p oap-validation --lib --test packed_traces
python3 .claude/skills/verify/scripts/rust-vocabulary.py --output-dir /tmp/oap-vocabulary-<unique>
python3 .claude/skills/verify/scripts/rust-packed-traces.py --zig zig/zig-out/bin/oapx --output-dir /tmp/oap-vocabulary-packed-<unique>
python3 .claude/skills/verify/scripts/rust-pack-compiled.py --output-dir /tmp/oap-vocabulary-schemas-<unique>
python3 .claude/skills/verify/scripts/rust-go-parity.py --output-dir /tmp/oap-vocabulary-parity-<unique>
cargo doc --manifest-path rust/Cargo.toml -p oap-protocol --no-deps
cargo test --manifest-path rust/Cargo.toml --workspace --all-features -- --test-threads=1
bash .claude/skills/verify/scripts/check.sh
```

The separate vocabulary consumer must preserve the serialized strings while
distinguishing declared constants, future values, schema-only task types and
empty values. Inspect `results.json` and the actual stdout/stderr. Inspect the
generated `EnvelopeType::is_known` documentation and its round-trip example.

A declared packed event before `run.started` must advance the run cursor without
claiming the core-only event rule. A non-string payload `session_id` must retain
the available conflicting string `run_id`, reporting `scope_mismatch` at index 3.
The native replay compares the real Rust public validator and Go CLI; the signed
regressions fail before the fixes. The diagnostic unit control must point at
`/payload` for both root `oneOf` and `anyOf`, excluding unrelated branch errors.

This stage provides the vocabulary query and strict trace corrections. Future
vocabulary state handling is internal preparation; public tolerant constructors
and schema widening belong to the successor. The root `anyOf` diagnostic path
gets its public tolerant integration check in that successor. Run the complete
Verify baseline, including the sandboxed PTY fixture reply and `/quit`.

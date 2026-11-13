# Explicit fixed-directory ACP configuration in the Rust hub

Use the shared setup, Python and verification mutex in `SKILL.md`. Run the
complete baseline and broader Rust suite under that mutex, with this journey:

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-serve --test acp_registry -- --nocapture
cargo build --manifest-path rust/Cargo.toml -p oap-serve
python3 .claude/skills/verify/scripts/rust-acp-registry.py --binary rust/target/debug/oap-rust-hub --output-dir "$EVIDENCE/acp-registry"
go run ./go/cmd/goap validate "$EVIDENCE/acp-registry/accepted.trace.json"
cargo run --manifest-path rust/Cargo.toml -p oap-validation --example validate -- "$EVIDENCE/acp-registry/accepted.trace.json"
zig/zig-out/bin/oxap validate "$EVIDENCE/acp-registry/accepted.trace.json"
```

The integration test skips the POSIX Python journey on non-Unix hosts or when
python3 is absent. The standalone invocation above must run and report PASS.
Keep signed failing CLI reproduction `756f728755` before the registration fix.

The script drives `oap-rust-hub serve agent` through an explicitly named ACP
entry containing executable, args, environment and an absolute working_directory.
It observes real child cwd, a literal and a resolved synthetic environment value,
the returned descriptor, and a host-named session with native identity in
history. Model listing and text/stream submission must refuse missing degraded
opt-ins. The accepted submission must precede its contiguous sequenced events;
a wrong participant must be refused before the actual permission answer reaches
the native peer. Observe text, permission settlement, terminal, idle state, EOF
closing the session in history, and a reaped child. Nonzero journal_capacity, any_directory, relative directories
and an empty executable must be refused explicitly.

wire.jsonl retains every actual exchange, including intentional refusals.
accepted.trace.json removes only those refused request/error pairs so the actual
successful exchange can be checked by all three validators. Native frames, stderr,
descriptor, history and config refusal diagnostics remain in the output directory.
HOME, config and workspace are temporary; inputs are synthetic and no ports are used.

Only explicitly configured fixed-directory ACP entries are enabled. This journey does not exercise
cancellation or live config projection; their native proofs are in
`rust-acp-cancel.md` and `rust-acp-live-config.md`. Reload is covered by `rust-acp-reload.md`.
This does not claim C8 served parity or complete issue #1438. Read the current descriptor rather than
inferring support from the adapter name.

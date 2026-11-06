# Rust conformance client correlation and wait budgets

Use shared toolchain setup from `SKILL.md`, then run

```sh
python3 .claude/skills/verify/scripts/rust-conformance-wire.py --output-dir /tmp/oap-conformance-client-<unique-name>
cargo test --manifest-path rust/Cargo.toml -p oap-conformance
```

The standalone public consumer must match a capabilities response from the
rebuilt Rust endpoint, retain its request/answer trace and observe exit 0 after
EOF. A synthetic peer emits events continuously while the consumer awaits a
missing response. Inspect the elapsed time and close reason in `consumer.txt`.
That wait must expire after its single 500 ms budget and within one second.

A second peer emits unrelated controls. Its control wait must spend one 500 ms
budget, leave the endpoint open, then receive the response to a subsequent
request. Require exit 0 and PASS. Consumer source and build/output logs are
retained. Temporary HOME/data/config and the process-group cleanup follow the
existing process journey.

The public client tests also exercise buffered replies, event waits receiving
unrelated replies, probe exclusion and a chatty endpoint ignoring EOF. EOF
draining has one total budget. Run the complete baseline too.

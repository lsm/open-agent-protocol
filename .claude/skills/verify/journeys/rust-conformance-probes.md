# Rust conformance refusal and recovery probes

Use shared setup from `SKILL.md`, then run

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-conformance --test probes
python3 .claude/skills/verify/scripts/rust-conformance-wire.py --output-dir /tmp/oap-conformance-probes-<unique-name>
```

Require `consumer.txt` to show twenty-five passing checks against the rebuilt
Rust memory endpoint. After completing a real run, the consumer queries state,
observes stale-capability refusal, checks cancellation availability, sends an
addressable unknown request and confirms the next state request still works,
then checks refusal of an unknown reopen. Require EOF exit 0 and PASS.
Intentional negative probes and their responses must stay outside the trace.

Public API tests cover native, degraded and unavailable reopen support,
degraded-feature consent, unsupported-feature identity, cancellation refusal
or acknowledgment, wrong error codes/types, malformed refusal payloads,
shutdown after a recoverable refusal and explicit skips when there is no
revision or admitted run.

The script retains source, build logs and output, uses temporary HOME/data/config
and cleans up its process group. Run the complete baseline too.

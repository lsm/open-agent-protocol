# Rust conformance bootstrap checks

Use shared setup from `SKILL.md`, then run

```sh
python3 .claude/skills/verify/scripts/rust-conformance-wire.py --output-dir /tmp/oap-conformance-bootstrap-<unique-name>
cargo test --manifest-path rust/Cargo.toml -p oap-conformance --test runner
```

The standalone consumer drives `Runner` against the rebuilt Rust endpoint.
Inspect `consumer.txt` for the six passing initialize, capabilities and open
checks. It must open the requested `bootstrap` session, retain six trace
envelopes and exit 0 after EOF.

A synthetic endpoint then returns an array containing a sentinel in its
initialize response. The consumer must record a failing initialize-decode
check, with type/category details, and keep the sentinel out of the diagnostic.
Require exit 0 and PASS. This exercises staged bootstrap phases. The full-suite
entry point follows the remaining checks.

Public API tests cover a missing revision, unavailable core model switching,
wrong session, refused open, malformed initialize/capabilities/state payloads
and the default session name. Source, build logs and consumer output remain in
the output directory. Runtime HOME/data/config are temporary, and the process
group is cleaned up on every script exit. Run the complete baseline too.

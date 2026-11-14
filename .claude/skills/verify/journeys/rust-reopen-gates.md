# Rust reopen capability outcomes

Use the shared setup and machine-wide mutex in `SKILL.md`, then run from the
repository root:

```sh
cargo test --manifest-path rust/Cargo.toml -p oxap-validation --test reopen_gates --test reopen
python3 .claude/skills/verify/scripts/rust-reopen-gates.py --output-dir /tmp/oap-reopen-gates-<unique>
```

The external Rust consumer and Go CLI must agree in strict and tolerant modes on
phase, code, diagnostic index, multiplicity and exit status. Inspect raw outputs,
generated traces, `results.json` and empty `disagreements.json`. The signed red
regression reproduces the three shared gate gaps and the compound false refusal.

An unadvertised reopen needs `unsupported_feature` naming `session.open.reopen`
with reason `unadvertised`. A degraded reopen without that exact opt-in needs
`capability_degraded` naming the key. Future support levels stay unavailable in
tolerant mode. An admitted gate defect is diagnosed before recovery loading;
a valid native reopen still requires recovery. Ordinary creates elect no reopen
gate, and native reopens may refuse a missing binding or store.

A compound reopen/subscribe can be refused for either elected defect, and
`unknown_session` discharges the whole open. A wrong refusal has one owner: the
winning feature must appear in the diagnostic's expected value. Missing-descriptor
and invalid-admission controls retain each elected gate's diagnostics.

Run the existing `rust-reopen.md` loading journey and its mutation checks, the
shared parity replay, full serial Rust workspace and complete Verify baseline.
Observe the actual sandboxed fixture reply and `/quit`, with child and temporary
state cleanup. All inputs are synthetic; no live session store or provider is
contacted. Query register coverage from the preceding slice must name its real
Rust sites; `go run ./go/cmd/goap check` validates the register.

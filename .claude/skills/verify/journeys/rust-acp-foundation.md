# Rust ACP configuration and capability admission

After the baseline builds ReleaseSafe `oapx`, run from the repository root:

```sh
python3 .claude/skills/verify/scripts/rust-acp-foundation.py --output-dir /tmp/oap-acp-foundation-<unique>
(cd rust && cargo test -p oap-adapter-acp)
```

The standalone Rust library consumer calls the public ACP `Adapter` through the
shared `Adapter` and `Admitter` traits. Its configuration points at a synthetic
executable that would leave a marker if launched. Probing, valid admission and refused open requests
must leave no marker. Valid session creation is exercised in rust-acp-session.md. The consumer observes invalid configuration errors,
participant and compaction refusals, the catalog-derived endpoint version and
corpus, configured MCP source descriptors and the staged capability levels.
Invalid admission and open requests must return the same refusal.

The temporary foundation descriptor uses the catalog's current revision with
`-rust-attachments` appended. It must not reuse Go's full capability revision
while disclosing fewer capabilities. The script wraps the public descriptor in
a real capabilities exchange and runs all three validators over it, retaining
the exchange and each validator's output. This verifies the capability/admission library surface. It does not claim
session or corpus parity. The separate rust-acp-process journey exercises the
already implemented subprocess handshake.

Opening reasoning is covered by `rust-acp-opening-config.md`: its emulated
session_open support is advertised, and agent-specific refusal happens only
after the native option snapshot is known.

Keep the complete baseline, Rust all-features suite, format/clippy/comment
checks and credential-isolated PTY proof from SKILL.md. Use synthetic inputs
only. No provider or native ACP installation is needed.

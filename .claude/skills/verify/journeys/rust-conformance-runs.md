# Rust conformance run admission and streamed gates

Use shared setup from `SKILL.md`, then run

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-conformance --test runs
python3 .claude/skills/verify/scripts/rust-conformance-wire.py --output-dir /tmp/oap-conformance-runs-<unique-name>
```

Require `consumer.txt` to show the real Rust memory endpoint admitting a run
with the switched model, resolving its permission and user-input gates from
the stream and reaching `run.completed`. The consumer checks twenty named
results, one permission resolution, one input resolution, one completion and
EOF exit 0. The consumer then performs five state/refusal checks. This is the
staged runner API. The complete conformance report will follow after the
remaining replay and validation slices.

Public API tests check the first offered permission choice, text and selected
input answers, preserved interaction participants and session/run addressing,
events emitted before admission, foreign-run terminals, failed/cancelled
terminals, rejected/malformed admissions and gate refusals. They also check
that a foreign-run event stream cannot renew the selected run's wait budget,
while unmatched events remain available to other consumers.

The script retains source, build logs and observed output. HOME/data/config
are temporary and process-group cleanup runs on every exit. Run the complete
baseline too.

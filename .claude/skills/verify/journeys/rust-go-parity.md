# Rust and Go validators, fixture by fixture

Lane A's shared check (#1440). It builds `goap` and the Rust `validate` example
once, then judges every manifest fixture with both CLIs, with the fixture's
packs and mode on the Go side and `--provider` on both for provider traces:

```sh
python3 .claude/skills/verify/scripts/rust-go-parity.py --output-dir /tmp/oap-rust-go-<unique-name>
```

Each fixture must draw the same phase, code, envelope and pointer from both
CLIs, and the codes the manifest declares. It skips the `load-invalid` kind,
which the Rust validator cannot load yet, and every id in the Rust list of
`fixtures/known-gaps.json`. A fixture in the Rust list of
`.claude/skills/verify/scripts/rust-location-gaps.json` must agree on its codes and may differ only
in where or how often it reports them. The script fails on any other
disagreement, and on a listed fixture that has come to agree, which must then
be deleted from the list. `disagreements.json` in the output directory holds
each mismatch.

Queue model captures are included in this check. A queued session mutation must
leave the default unchanged until `run.started`. Capture anchors must name that
start on the same session, and must remain valid across delayed delivery and
settlement. Genesis cannot contradict the opening model or supersede a mutation.
Compound opens must admit their message before checking a capture or recording
the opening model; the two `queue-compound-open-model-*` fixtures pin that order.
The `queue_models` regression replays the shared positive and negative fixtures
through the public Rust validator, without the known-gap exemption:

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-validation --test queue_models
```

Queue leads are judged together once their admissions are known, including
reservation classification, queue places, admission order and the session's
executing run. The `queue_leads` regression pins the shared positive and negative
traces. A promotion must remain active across a stale read so a later omission
is diagnosed. Run `cargo test --manifest-path rust/Cargo.toml -p oap-validation
--test queue_leads` before the shared public-consumer comparison above.

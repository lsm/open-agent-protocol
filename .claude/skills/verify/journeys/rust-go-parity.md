# Rust and Go validators, fixture by fixture

Lane A's shared check (#1440). It builds `goap` and a standalone consumer of
the public Rust validator once, then judges manifest fixtures with both consumers.
Both load the fixture's packs and receive its declared validation mode, using the
provider validator for provider traces:

```sh
python3 .claude/skills/verify/scripts/rust-go-parity.py --output-dir /tmp/oap-rust-go-<unique-name>
```

Each fixture must draw the same phase, code, envelope and pointer from both
consumers, and the codes the manifest declares. Packed traces also compare the
diagnostic index. Every `load-invalid` fixture must exit 1 with its declared
load codes through `Validator::with_packs`. Only IDs in the Rust list of
`fixtures/known-gaps.json` are skipped. A fixture in the Rust list of
`.claude/skills/verify/scripts/rust-location-gaps.json` must agree on its codes and may differ only
in where or how often it reports them. The script fails on any other
disagreement. The Go list pins the accepted packed-branch pointer difference
while retaining phase, code, index, envelope identity and multiplicity. It also
fails on a listed fixture that has come to agree, which must then
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

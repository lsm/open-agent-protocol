# Rust staged pack loading

Stage one constructs `pack::PackLoader` with descriptor shape, capability/error/type namespaces,
prefix-freedom, envelope roles, reply targets, refusal declarations and gates
checked, including payload-member declarations after the resource slice. This is explicitly a staged constructor. It does not load schema
documents, payload members, dependencies, references, branches or fixture corpora,
and cannot produce a trace validator. Shared trace parity still skips load-invalid
fixtures until the complete loader is connected.

Run from the repository root:

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-validation --test pack_declarations
cargo doc --manifest-path rust/Cargo.toml -p oap-validation --no-deps
python3 .claude/skills/verify/scripts/rust-pack-declarations.py --output-dir /tmp/oap-pack-declarations-<unique>
```

The journey builds a separate consumer of the public Rust crate, reads positive
pack descriptors and checks each shared declaration refusal against Go's public
CLI and the manifest, plus twelve synthetic shape/gate/namespace refusals, including two-label core roots. Inspect `results.json` and the retained raw outputs. The
consumer labels every result `declarations`; success makes only that staged claim.
All inputs are synthetic, with no endpoints or provider calls.

Run the full Verify baseline and the shared Rust/Go trace parity journey to prove
that ordinary validation still works through the public consumers, including the
sandboxed PTY fixture reply and `/quit`. Keep stage coverage explicit as the
remaining loader phases land.

Inspect the generated `rust/target/doc/oap_validation/pack/struct.PackLoader.html`.
Its staged API contract is included from `rust/validation/PACKS.md`, keeping prose
in an external document while exposing coverage to Rust documentation consumers.

## Contained resources and dependencies

The second slice extends declaration checks to payload members, then
`PackLoader::read_resources` consumes that stage. Schema paths and fixture-manifest
paths resolve beneath the canonical pack root after lexical cleanup and symlink
resolution. Dependencies name a loaded pack at their exact version. Returned
`PackResources` exposes immutable schema documents and contained manifest paths.
Reference ownership, branch compilation and corpus validation remain for stage three.

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-validation --test pack_resources
python3 .claude/skills/verify/scripts/rust-pack-resources.py --output-dir /tmp/oap-pack-resources-<unique>
```

The external consumer compares eighteen shared load refusals plus synthetic
symlink, normalized-path and gate controls against Go's public CLI. Inspect the
raw outputs and `results.json`; resource-stage success is explicitly labelled.
An escaping schema or manifest symlink must yield `pack_schema_path_escape`.
An internal link and a normalized path remain usable, and the shared positive
packs retain their real schema content and manifest locations.

# Rust staged pack loading

The declaration constructor checks descriptor shape, declared namespaces,
prefix freedom, roles, reply targets, refusals and admitted gates, including
payload-member declarations. `read_resources` adds contained resource loading,
exact dependencies and reference ownership. Each surface states its staged
contract. Branch compilation and corpus checks follow in the remaining slices.
Shared trace parity includes load-invalid fixtures once the complete loader is
connected.

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
Reference ownership is checked by the third slice. Compilation and corpus validation follow separately.

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

## Reference ownership

The third slice checks `$ref`, `$dynamicRef` and nested `$id` scopes before
publishing `PackResources`. Own schema documents, member schemas and their
identifiers are available, along with exact declared dependencies and the core
bundle. A globally loaded pack supplies no authority without a dependency.
Identifiers stay in the owning pack namespace.

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-validation --test pack_references
python3 .claude/skills/verify/scripts/rust-pack-resources.py --references --output-dir /tmp/oap-pack-references-<unique>
```

The external consumer includes the shared external-reference and escaping-ID
refusals, both of which must yield `pack_external_ref` and exit 1. It retains the
same positive schema resources. This constructor remains staged while branch
compilation and typed corpus validation are added in separate following slices.

### URI parsing compatibility

```sh
python3 .claude/skills/verify/scripts/rust-pack-uri-compat.py --output-dir /tmp/oap-pack-uri-compat-<unique>
```

Inspect `results.json` and retained raw outputs. Go and Zig accept three raw
pipe/space URI cases that Rust refuses with `pack_external_ref`. Their percent-
encoded controls pass Go and Rust. Zig accepts the two escaped ID controls and
reports the escaped-fragment control unjudged with exit 1. The helper labels
these outcomes separately, as recorded in `drafts/extension-pack-load-refusals.md`,
and checks that Rust's refusal messages render the URI or parse error directly.

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

## Compiled resources

The fourth slice adds `PackResources::compile` and `CompiledPacks`. Contributed
branches resolve only to object nodes and pin their declared type. Registry
preparation and compilation use the existing refusing core retriever. The
composed envelope schema and separate member schemas remain available with
immutable construction data.

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-validation --test pack_compilation
python3 .claude/skills/verify/scripts/rust-pack-compiled.py --output-dir /tmp/oap-pack-compiled-<unique>
```

The external consumer exercises shared load refusals and packed schema fixtures,
including cross-pack references, descriptor members, malformed packed payloads,
core-invalid frames, whole-document branches, missing resources and invalid
schema keywords. Escaped URI controls exercise their own event payloads with
matching and conflicting const values. Inspect the raw outputs and `results.json`.

For a core envelope, the consumer projects declared member values out before
validating the composed schema and validates those values separately. This is
the compiler boundary used by Go. The initial replay compared the unprojected
schema to Go's full validation result and was inconclusive for descriptor members.
The corrected replay must agree on all declared schema/load outcomes. Corpus
checks and complete loader construction follow separately.

## Typed corpus expectations

The fifth slice adds `CompiledPacks::check_corpus_entries`. It parses declared
fixture manifests into typed entries and checks the manifest version, known
kinds, positive and negative expectations, profile/scope rules and the distinct
agent, provider and load-error vocabularies. Optional null fields preserve their
zero/default meaning, including null string-list elements.

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-validation --test pack_corpus_entries
python3 .claude/skills/verify/scripts/rust-pack-corpus-entries.py --output-dir /tmp/oap-pack-corpus-entries-<unique>
```

The external public consumer compares typed field failures, null controls,
positive/negative contradictions, every code represented in the shared agent
and load fixtures, provider-only codes, scope derivation and profile restrictions
against Go's public CLI. Inspect `results.json` and the retained raw outputs.
Results explicitly carry stage `corpus-expectations`. Identity/path policy,
unit ownership, coverage completeness and full construction follow in the sixth
loader slice. Corpus trace execution follows with validator integration.

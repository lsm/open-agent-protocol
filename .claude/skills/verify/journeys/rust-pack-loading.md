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
Results explicitly carry stage `corpus-expectations`. The generated public API
documentation must name the agent mode/provenance and pack/unit deferrals. Twelve
separate controls retain Rust acceptance and complete-Go refusal for deferred
context, identity/path, ownership and coverage policy in `staged-differences.json`.
Inspect each retained Go refusal against its expected reason. These are explicit
staged differences. The sixth slice enforces these policies plus identity
uniqueness and capability completeness through the full constructor. Corpus trace
execution follows with validator integration.

## Complete constructor and corpus policy

The sixth slice adds `PackLoader::load`. It composes all loader phases, checks
agent entry identity/path, mode/provenance, nested pack declarations, required
units and exact owner claims, then negative gate/honour completeness for every
pack capability. Provider entries retain their profile-specific manifest rules.
It preserves the narrower staged operation for existing consumers.

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-validation --test pack_corpus
python3 .claude/skills/verify/scripts/rust-pack-corpus-entries.py --complete --output-dir /tmp/oap-pack-loader-complete-<unique>
```

The complete consumer must agree with Go on every prior typed expectation case,
all twelve recorded staged policy cases and all shared load-invalid fixtures.
Additional controls cover missing manifests, empty corpora, gate-only/honour-only
coverage and their complete positive control, nested packs and absent units on
load-invalid entries, duplicate identity/path, normalized paths and first-entry
owner errors before later expectation errors. Inspect `results.json`, the empty
`staged-differences.json` and raw refusal outputs. In particular, the complete
constructor must refuse both `deferred-nested-packs` and `deferred-empty-units`.
Run the earlier staged replay as well to preserve that public method's contract.
Construction checks manifest declarations. Packed trace execution remains in the
following validator-integration slice.

## Parsed-envelope schema operation

`CompiledPacks::validate_schema` performs projection and schema diagnostics inside
the library. The external consumer now calls this operation for each envelope.

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-validation --test pack_schema_validation
python3 .claude/skills/verify/scripts/rust-pack-compiled.py --output-dir /tmp/oap-pack-schema-operation-<unique>
```

All schema/load verdicts must agree with Go. Member errors must point into the
original payload with escaped tokens, and two invalid members must select the
first escaped pointer. The signed ordering regression fails before the sort.
Four controls retain a primary-diagnostic difference in `diagnostic-differences.json`.
Rust identifies the declared packed branch's missing key or conflicting payload
constant; Go selects unrelated core-branch required-property errors. The ledger
in `drafts/extension-pack-load-refusals.md` records this diagnostic-only difference.
Inspect raw outputs and the new operation's unit controls for input preservation
and auth-answer redaction. Native strict trace semantics are exercised below.
Tolerant mode follows separately.

## Native strict packed traces

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-validation --test packed_traces
python3 .claude/skills/verify/scripts/rust-packed-traces.py --zig zig/zig-out/bin/oapx --output-dir /tmp/oap-native-packed-<unique>
```

The separate public consumer constructs `Validator::with_packs` and compares
validity, phase, code and diagnostic index with Go. Inspect `results.json` and
raw outputs for shared load refusals, packed schemas, exact feature keys,
request-time gate captures across refreshes, typed refusals, scope fallback,
member gates, declared roles, image handling and run sequencing/terminal checks.
The public replay found two Rust reply-selection bugs. Go correctly refuses a
suffix-matched success response without a declared mapping and ignores an event's
`replies_to` when selecting the real response declaration. Signed failing public
regressions precede both fixes, and shared fixtures retain the controls.

Run the shared parity journey and complete Verify baseline as well. The rebuilt
Zig CLI must judge each new `reply-roles` fixture with the pack loaded.
The helper checks their exit status and exact diagnostic codes and retains
`zig-results.json` plus raw outputs. Record any observed Zig difference in the
per-tree gap list. The accepted packed-schema pointer differences
and URI compatibility differences above remain separately pinned.

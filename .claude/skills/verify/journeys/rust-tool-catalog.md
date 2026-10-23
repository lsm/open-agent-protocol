# Rust descriptor tool catalog integrity

Run the public-library tests and the shared public-CLI replay from the root:

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-validation --test tool_catalog
python3 .claude/skills/verify/scripts/rust-tool-catalog.py --mutation-check --output-dir /tmp/oap-tool-catalog-<unique-name>
```

The replay builds Go, Rust and ReleaseSafe Zig and sends each the same synthetic
descriptor traces. Duplicate source IDs across top-level/layer declarations,
duplicate effective tool names and dangling tool sources must fail in the semantic
phase at the descriptor response (index 1). Repairs, case-sensitive identifiers,
cross-layer references, repeated valid descriptors and an omitted descriptor tool
source must pass. Combined faults must report all three codes. This checks
descriptor-local shape only, not list gates, attachment lifetime or call attribution.

The seven source mutations must compile and change the observed Rust verdict;
one changes only the diagnostic index. Compiler failures do not count. The script
restores the original source in `finally`, rebuilds Rust and then replays all cases
through all three validators with zero disagreements. Inputs, raw CLI output,
normalized results and mutation catches remain in the chosen artifact directory.

Run the shared baseline in `SKILL.md` as well, including the isolated terminal
reply and quit journey. No credentials, providers or real authentication are used.

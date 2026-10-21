# Rust model-catalog shape

Run from the repository root:

```sh
(cd rust && cargo test -p oap-validation --test catalog_shape)
python3 .claude/skills/verify/scripts/rust-catalog-shape.py --output-dir /tmp/oap-catalog-shape-<unique-name>
```

The helper rebuilds Go's `goap validate`, Rust's public `validate` example and
Zig's `oapx validate`, and directly observes their diagnostics and exits. Its
retained `results.json`, inputs, stdout and stderr must show zero disagreements
on exact code sets and semantic phase. It does not rely on Zig's fixture cache.

Observe `duplicate_model_id` for repeated IDs, adjacent or separated;
`ambiguous_default_model` for multiple defaults, including defaults surrounding
a nondefault current model; and `model_not_in_catalog` when the current model
is missing from its own response. Combined faults must report all three, even
when a stale revision independently draws `stale_capability_revision`.

Repaired catalogs, empty catalogs without a current model, zero or one default,
a listed current model that is not the default, case-distinct IDs and identical
catalogs in separate responses must pass. No catalog history or model-event
window is needed for these response-local judgments.

With no other build or edit in the worktree, run the repeatable source mutations:

```sh
python3 .claude/skills/verify/scripts/rust-catalog-shape.py --mutation-check --output-dir /tmp/oap-catalog-shape-mutations-<unique-name>
```

Each mutation must produce its exact changed public CLI result; compiler errors
do not count. The stale-gate mutation must retain the stale-revision diagnostic
while losing the three independent shape faults. The helper restores the source in `finally`, rebuilds the Rust consumer,
then replays the complete corpus through all three CLIs. Check restoration if
Python itself is forcibly killed.

Before merging, integrate main locally and run Go validation, the Rust manifest
test and Zig `test-unit-validation`; rerun this journey with zero disagreements.
Run the shared Verify baseline and inspect its isolated PTY reply/quit evidence.
The register rows are `0006#a-catalog-belongs-to-the-revision-it-was-served-under/5–6`
and `0006#current_model_id-is-judged-across-the-querys-window-not-at-an-instant/1`.
Catalog history, model-event windows, switches, held/deferred positions and live
model discovery remain unverified by this journey. Live providers are unverified.

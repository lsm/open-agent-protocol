# Rust transcript response pages

From the repository root, run:

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-validation --test transcript_pages
python3 .claude/skills/verify/scripts/rust-transcript-pages.py --output-dir /tmp/oap-transcript-pages-<unique>
```

The helper builds Go's public validator, Rust's `validate` example and the
ReleaseSafe `oapx` binary. It replays synthetic shared traces, their repairs and
boundary controls through all three CLIs. Inspect `results.json` and retained
input/stdout/stderr: exits, exact diagnostic code sets and semantic phase must
agree, with zero disagreements. Direct `oapx validate` avoids Zig fixture-cache
reuse. No endpoint, provider or transcript store is contacted.

A response exceeding its correlated request's limit draws
`transcript_over_limit`; an omitted limit allows 100 entries, and explicit
limits allow their exact boundary through 500. A repeated `entry_id`, adjacent
or separated, draws `duplicate_transcript_entry`. Repetition across separate
pages is allowed.

A gap without a requested cursor draws `transcript_gap_mismatch`. An empty
page's gap cannot name `resumed_at`; a nonempty page's gap must name its first
entry, including when that member was omitted. Both valid gap reasons remain
accepted. Reversed replies retain each request's own cursor and limit. The
combined-fault page must report all three distinct diagnostics, rather than
stopping after its limit violation. Every invalid trace has a repair control.

With no concurrent edit or build in this worktree, run:

```sh
python3 .claude/skills/verify/scripts/rust-transcript-pages.py --mutation-check --output-dir /tmp/oap-transcript-mutations-<unique>
```

Every source mutation must compile and produce its exact changed public CLI
result; compiler errors and inconclusive failures do not count. The helper
restores the source in `finally`, rebuilds Rust and replays the complete corpus
through all three consumers after restoration. Inspect `mutations.json` too.
Check restoration if the helper is forcibly killed.

Before merging, integrate current main and run Go validation, the Rust manifest
test and Zig `test-unit-validation`, then repeat this public replay. Run the
shared Verify baseline and observe its isolated PTY fixture reply and `/quit`
exit 0, with child cleanup. Register rows are `0051#4/1`, `0051#2/1` and
`0051#5/1–3`. Capability gates, the unimplemented truncated/pruned rules,
admission/lead/active/model anchors, real data, credentials and provider calls
are outside this journey.

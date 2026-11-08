# Rust optional query outcomes

Use the shared setup and machine verification mutex in `SKILL.md`, then run
from the repository root:

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-validation --test query_gates
python3 .claude/skills/verify/scripts/rust-query-gates.py --output-dir /tmp/oap-query-gates-<unique>
```

The separate public Rust consumer and Go CLI replay the six shared model-list,
session-list and transcript-load refusal/degradation fixtures in both modes.
Inspect the generated inputs, raw outputs, `results.json` and empty
`disagreements.json`. Require exact semantic phase, code, diagnostic index,
multiplicity and exit agreement. Signed failing regressions precede the fix.

Unadvertised queries need `unsupported_feature` naming the feature and reason
`unadvertised`. Degraded queries require that feature's exact opt-in, or a
`capability_degraded` refusal naming it. Repair controls must pass, and an opt-in
for a different feature must not excuse admission. Unknown future support levels
remain unavailable in tolerant mode. Reversed replies retain each request's
own expectation, and a wrong response kind cannot discharge the real query.

An advertised, satisfiable model-list query must be honoured. Ordinary refusals
of session-list and transcript reads remain allowed when no gate is violated.
The response-page journeys retain their separate pagination and contents checks.
All inputs are synthetic; no endpoint, transcript store or provider is contacted.

Run the shared Rust/Go manifest replay, full serial Rust workspace suite and
complete Verify baseline, including the actual sandboxed terminal fixture reply
and `/quit` with cleanup. Reopen compound elections and model-switch observation
are separate follow-ups; this journey covers these three query surfaces.

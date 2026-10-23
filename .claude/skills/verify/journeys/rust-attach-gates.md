# Rust attachment gates and refusals

Run from the repository root, with no concurrent Rust builds during mutations:

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-validation --test attach_gates
python3 .claude/skills/verify/scripts/rust-attach-gates.py --mutation-check --output-dir /tmp/oap-attach-gates-<unique-name>
```

The script builds the public Go, Rust and ReleaseSafe Zig validators and replays
the same synthetic traces through each. Require exact diagnostic multiplicity,
semantic phase, envelope index and exit agreement. Descriptor mode defects belong
to the descriptor response; admission/refusal defects belong to the correlated
open response. Empty attachment arrays do not elect the capability.

The journey covers affirmative `session_open` disclosure, each feature's degraded
opt-in, remote-mode disclosure, collisions with declared or same-open source IDs,
and refusals under `max_sources` and `transports`. A disclosed ceiling permits
admission above the ceiling; it does not require refusal. A refusal must identify
the correct source under numeric JSON Pointer ordering, including index 2 versus
index 10. Valid admissions, repaired traces and conforming refusals must pass.

Compound attachment/provided-tool opens must retain every attachment expectation,
discharge the whole open on a conforming refusal, and choose one diagnostic owner
when a refusal is wrong or a defect is admitted. The existing unsupported Rust
Unicode-pattern boundary remains explicit: where a provided-tool limit cannot be
judged, the validator cannot invent an attachment honour failure instead. The
positive compound pattern-boundary fixture must pass in all three validators.

All 23 source mutations must compile and change the observed Rust verdict.
Compilation failure is not a catch. Mutations cover mode/capability/opt-in gates,
collisions, ceiling boundaries, transports, refusal details, compound ownership,
limit-versus-admission semantics and diagnostic location. Sources are restored in
`finally`, Rust is rebuilt, and all 57 traces are replayed after restoration.
Inputs, raw output, normalized results and mutation catches remain in the chosen
artifact directory.

After integrating current main, run the shared baseline in `SKILL.md`, the full
Rust workspace suite, and the existing tool-catalog, tool-list, open-settings and
adapter-prompt consumer journeys. Inspect the isolated PTY's retained screen for
the fixture reply and confirm `/quit` exits 0 with its child reaped. No credentials,
live providers, authentication or GUI tests are needed.

This ports registered attachment admission/refusal rules only. Attachment snapshot
lifetime, catalog reconciliation, provider attachment and the deferred
snapshot/admission/model-capture lanes remain outside this journey.

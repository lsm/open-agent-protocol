# Optional empty text answers (Decision 0056)

From the repository root, with no concurrent Go/Rust/Zig builds or edits in this
worktree during mutations:

```sh
python3 .claude/skills/verify/scripts/optional-empty-input.py --mutation-check --output-dir /tmp/oap-optional-empty-<unique>
```

The helper builds `goap`, the public Rust validation example and ReleaseSafe
`oapx`, then feeds each the same synthetic JSON envelopes. The shared positive
fixture comes from the schema-valid public helper trace that exposed the bug.
Both explicit `required: false` and omitted `required` must accept `text: ""` on
the resolve request and resolved event with exit 0 and no diagnostics.

Required empty text and missing required questions must still fail with semantic
`unmatched_interaction` at request index 6 and event index 8. The isolated request
and event variants pin each boundary independently; their repairs must pass.
Nonempty text, including whitespace and Unicode, is retained without trimming.
Optional questions may be unanswered while another question is answered, since
the unchanged wire schema requires a nonempty answers array. Required single-
and multi-choice questions also remain required.

An answer to a text question must still use the text arm. Choice arms on text
questions, text arms on choice questions and foreign option IDs remain semantic
refusals. Omitting the answer arm remains a schema refusal; allowing empty text
does not weaken the shared schema. These checks do not use serializers, native
harnesses, credentials, providers or real authentication.

Every configured source mutation must compile and change the observed public
verdict. The probes restore the optional-empty bug, weaken required text and
required-answer presence, allow a wrong answer arm, or move the diagnostic index
in each language. Build failures, unrelated diagnostic codes/phases and crashes
are failures, never mutation catches. Sources are restored in `finally`, all
three binaries are rebuilt and the entire corpus is replayed after restoration.
Raw commands' stdout/stderr, fixture inputs, exact diagnostic phase/code/index
multiplicity and exits, and mutation catches remain in the artifact directory.

After integrating current main, run the complete baseline in `SKILL.md`, full
Rust workspace tests and the full Go gates. Inspect the isolated PTY screen for
the fixture reply and confirm `/quit` exits 0 with its child stopped/reaped.
Also run the main-branch adapter answer-helper journey:

```sh
python3 .claude/skills/verify/scripts/adapter-input-answers.py --output-dir /tmp/oap-optional-empty-helper-<unique>
```

Its valid emitted and restored empty-text traces must now exit 0; no semantic
validator-gap exception remains. Keep the separately documented Zig adapter-helper
divergence distinct from this validator correction. This is scoped Decision 0056
verification, not full validator or adapter parity.

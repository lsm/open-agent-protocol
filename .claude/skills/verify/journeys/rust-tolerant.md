# Rust tolerant trace validation

Use the shared setup and verification mutex in `SKILL.md`, then run from the
repository root.

```sh
cargo test --manifest-path rust/Cargo.toml -p oap-validation --test tolerant --test packed_traces
python3 .claude/skills/verify/scripts/rust-tolerant.py --output-dir /tmp/oap-rust-tolerant-<unique>
```

The separate Rust consumer calls `Validator::tolerant` and
`Validator::tolerant_with_packs`. It compares actual results with Go's public CLI
in both modes. Inspect `results.json`, the empty `disagreements.json`, generated
inputs and retained stdout/stderr. Every load-invalid fixture must draw the
declared codes in tolerant mode. Positive agent fixtures with valid schemas outside
the separately listed Rust semantic gaps must pass with their declared packs.
Every shared packed `semantic-invalid` fixture must retain its diagnostics in
both modes.

Additive members, future string enum values and new discriminator variants must
pass tolerant validation. Known constants, required fields, numeric enums and
known discriminator branches must enforce their constraints. Conditions under
`if` must preserve their enum tests. A malformed declared packed type must draw
its schema diagnostic even when the generic unknown-type fallback is available.

Future run statuses and admission values are opaque where their vocabulary is
unknown. Known values still constrain each other, and the status transition table
must engage again after a known status resumes. An opaque admission still owes
a terminal event. Unknown support levels must fail capability gates. Unknown
requests, responses and sequenced events retain correlation, scope, sequence and
terminal checks, including exact diagnostic multiplicity. A non-string scope
field in an extension payload must preserve the other available string ID
checks. The signed regression for mixed scope fields fails before replacing the
struct decoder with the existing reader for individual fields.

The packed-prelude regression uses the strict public constructor too. A declared
packed event before `run.started` must retain sequencing without applying the
rule for core events before `run.started`. Its signed public and unit failures precede the fix.

Run the native strict packed journey, shared Rust/Go manifest replay, full serial
Rust workspace suite and complete Verify baseline as well, including the actual
sandboxed PTY fixture reply and `/quit`. Live provider calls require separate
evidence.

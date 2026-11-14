# Rust tolerant trace validation

Use the shared setup and verification mutex in `SKILL.md`, then run from the
repository root.

```sh
cargo test --manifest-path rust/Cargo.toml -p oxap-validation --test tolerant --test packed_traces
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
The selected branch must report the missing packed payload key at `/payload`
and a wrong core status constant at `/payload/status` in both modes; root
`oneOf` and tolerant `anyOf` must narrow diagnostics consistently.

The schema-only `session.task.*` vocabulary retains generic tolerant scope and
run checks until its semantics graduate under Decision 0069. The public replay
covers all seven types, unadmitted runs, pre-start sequencing and session-only
events; strict-mode controls retain their existing results. Both request and
response scope conflicts must draw the same diagnostic multiplicity as Go.
The signed public regression records the ten disagreements before the fix.

Future run statuses and admission values are opaque where their vocabulary is
unknown. Known values still constrain each other, and the status transition table
must engage again after a known status resumes. An opaque admission still owes
a terminal event. Unknown support levels must fail capability gates. Unknown
requests, responses and sequenced events retain correlation, scope, sequence and
terminal checks, including exact diagnostic multiplicity. A non-string scope
field in an extension payload must preserve the other available string ID
checks. The signed regression for mixed scope fields fails before replacing the
struct decoder with the existing reader for individual fields.

The state flag is first enabled by these public constructors. The named Rust
tests and public replay must exercise foreign requested delivery, all three
admission-shape votes (including future effective delivery), future status
updates/cancel replies and opaque admissions. An opaque admission must preserve
its declared status before `run.started`, keep tracked-run ownership checks,
skip the known started lifecycle while still owing a terminal, and reject
contradictions between remaining known votes. Known status transitions must
re-engage after a future value. Inspect these named controls in `results.json`.
Each state control is replayed through both the core constructor and a loaded
storage pack; the `-loaded` rows must retain the same outcomes. This checks that
both public tolerant constructors actually enable the prepared state handling.

The packed-prelude regression uses the strict public constructor too. A declared
packed event before `run.started` must retain sequencing without applying the
rule for core events before `run.started`. Its signed public and unit failures precede the fix.

Run the native strict packed journey, shared Rust/Go manifest replay, full serial
Rust workspace suite and complete Verify baseline as well, including the actual
sandboxed PTY fixture reply and `/quit`. Live provider calls require separate
evidence.

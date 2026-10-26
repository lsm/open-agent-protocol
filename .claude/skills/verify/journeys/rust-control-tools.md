# Rust control-tool journey

After building the ReleaseSafe `oapx` binary, run the public-library consumer against
all ported control-call/provisioning fixtures and nine consumer variations:

```sh
python3 .claude/skills/verify/scripts/rust-control-tools.py --output-dir /tmp/oap-rust-control-<unique-name>
```

Valid round trips, cancellations, all three resolution arms and conforming
refusals must exit 0. A defect-free, limit-conforming tools open refused by
policy must fail with `undisclosed_provide_limit`, even without a feature in
the error details; a discharged open-level or compound refusal remains valid.
Invalid traces must exit 1 with their exact declared code set. Mutations exercise the interaction's tool binding, event request id,
accepted result, responder, invalid-pattern repair, and canonical JSON equality
versus a changed numeric result, plus compound-open refusal ownership. Each
must reach the semantic phase; a schema rejection is not a pass. The script retains results
and mutated inputs, checks RE2 syntax/matching probes, and builds the internal
Go validator to compare every result with Rust. It requires zero Go/Rust
code-set or phase disagreements and explicitly lists remaining snapshot gaps
for the next port. The Unicode 17 descriptor prefix must pass in all three consumers. The refusal
fixtures for all four scripts (also pinned by the shared manifest in CI), and boundary, negation and whole-name probes for Beria Erfe, Sidetic,
Tai Yo and Tolong Siki must agree across Go, Rust and Zig. The script retains
the three consumers’ results. Other Rust Unicode 16 data differences and Zig
syntax outside its supported subset still conservatively stand down. The baseline
runs this journey too. `name_pattern` matches
the entire name; invalid RE2 is diagnosed on the descriptor at the pattern's pointer.
The syntax probes include signed hex escapes in character classes, which must
be rejected rather than accepted by an integer parser. Backslash-escaped
non-ASCII literals are invalid both inside and outside a class; unescaped
Unicode literals remain valid.

# Exact usage-count validation journey

After changing shared usage schemas or any validator's numeric decoding, run:

```sh
python3 .claude/skills/verify/scripts/usage-count-validation.py --output-dir /tmp/oap-usage-validation-<unique-name>
```

This builds and invokes the real Go validator CLI, Rust public validator example
and Zig `oapx validate` on the shared raw-preserving fixtures. Every count at
every shared-usage location has zero, uint64 max, max+1, negative-zero, decimal
and exponent cases. Additional cases pin exact schema-before-spelling diagnostic
precedence, including decimal max/max+1 and a tiny nonzero fractional part.
Each validator must return the manifest's exact code set, phase and exit status;
the retained results and mismatch report must show zero disagreements.

Positive envelope integral-spelling and arbitrary tool-result-number controls
must still pass, proving the Decision 0065 exception stays usage-only and hub
D31 is unchanged. `scripts/generate-usage-count-fixtures.py --check` verifies
raw literal preservation without rewriting files. Regenerate intentionally
with that command without `--check`; it keeps all existing fixture identities.
Run the complete repository baseline too. No credentials or live provider is used.

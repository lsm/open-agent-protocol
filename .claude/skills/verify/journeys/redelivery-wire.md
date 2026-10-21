## Redelivery wire journey

After changing `recovery.redelivered_runs`, build oapx and run:

```sh
python3 .claude/skills/verify/scripts/redelivery-wire.py --output-dir /tmp/oap-redelivery-wire-<unique-name>
(cd clients/ts && npm ci --ignore-scripts && npm test)
```

The shared positive trace carries two redeliveries and one withdrawn prompt;
all three validators must accept it. Six malformed record shapes must fail
with only `schema_invalid`. The baseline includes this replay plus Go and Rust
protocol round-trip checks. The TypeScript suite checks that `state()` exposes
both record forms. These are wire/schema checks: held-prompt pairing, original
order, root-chain consistency and queued membership remain unimplemented and
are not proven by passing wire validation (register row `0050#5/3`).

Mutation-check six schema constraints, both typed serializers and the TypeScript
schema mirror with no
concurrent Go/Rust/Zig builds:

```sh
python3 .claude/skills/verify/scripts/redelivery-wire.py --mutation-check --output-dir /tmp/oap-redelivery-mutations-<unique-name>
```

The optional mode requires green baseline tests and assertion failures for each
mutation, then restores the schema and protocol sources in `finally`. Run the
baseline afterward to check the restored source.

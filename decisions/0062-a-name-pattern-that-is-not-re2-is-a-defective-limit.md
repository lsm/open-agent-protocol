# Decision 0062: A Name Pattern That Is Not RE2 Is a Defective Limit

Status: accepted 2026-10-19
Date: 2026-10-19
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `control-tools`
Amends: [Decision 0011](0011-control-layer-provided-tools.md)

## Context

Decision 0011 lets `action.tools.provide` declare a `name_pattern` among its
limits, and Decision 0055 makes it RE2 syntax matched against the whole name.
Neither says what a descriptor means when its `name_pattern` is not valid RE2.
Go skipped such a limit entirely, so it was neither enforced nor diagnosed, and
a refusal that honoured it drew `undisclosed_provide_limit`. Zig did not read
the pattern at all. The validator rule register recorded the question as
OPEN-0011-3.

The other readings were to treat the pattern as disclosing no limit, which
surfaces the defect only when something is refused, or to have the schema
reject it, which JSON Schema cannot express.

## Decision

### An uncompilable `name_pattern` makes the descriptor defective (OPEN-0011-3)

What does a `name_pattern` that is not valid RE2 mean? The descriptor that
publishes it is defective: it declares a limit nobody can evaluate, so it has
disclosed nothing a caller can rely on. The validator judges it when the
`capabilities.response` carrying it arrives, as `undisclosed_provide_limit` at
the pattern's pointer, the code 0011 already uses for a limit the caller was
not given. No new code is added.

## Consequences

- Decision 0011's limits clause states the rule in its own text.
- Go stops skipping an uncompilable pattern and diagnoses the descriptor,
  in the anchored-`name_pattern` port. Zig and Rust do the same in their RE2
  ports. One fixture, a descriptor whose `name_pattern` is not RE2, pins all
  three.

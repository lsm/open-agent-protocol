# Model provider profile

Decisions 0016 and 0030: the `model-provider-core` profile, judged by
`go/validation/provider.go` and `zig/src/validation/provider_semantic.zig`
against the provider corpus in `fixtures/provider/`. Rust has no provider
validator. Format: [README](README.md).

This group has no rows. Decision 0016 is proposed, and it leaves the profile's
schemas and validator rules to "the next decision" (0016 L187–195). Decision
0030, also proposed, binds the HTTP transport and states no trace rule. Every
provider rule the trees judge is therefore written only in
[`drafts/model-provider-core.md`](../model-provider-core.md), and each is an
open item in
[`research/validator-divergences.md`](../../research/validator-divergences.md#model-provider-profile-0016-0030)
until a decision adopts it:

| item | draft | code | Go | Zig |
| --- | --- | --- | --- | --- |
| OPEN-0016-1 | the terminal is the assembly of its ended parts (L624–651, L1861–1863) | `terminal_not_assembly` | `assemblyDefect`, `partMismatch` | `Machine.close`, `assemblyDefect` |
| OPEN-0016-2 | a credential header on a call or a descriptor (L1588–1597) | `credential_in_headers` | `credentialHeaders` | `Machine.credentialHeaders` |
| OPEN-0016-3 | a grant request carrying a `value` (L1391–1395) | `credential_in_trace` | `Validate` | `Machine.apply` |
| OPEN-0016-4 | per-inference `sequence` opens at 1 and is contiguous (L260–263, L1864–1865) | `sequence_gap`, `sequence_regression` | `Validate` | `Machine.apply` |
| OPEN-0016-5 | no scoped event after a terminal (L667–668, L1861) | `event_after_terminal` | `Validate` | `Machine.apply` |
| OPEN-0016-6 | a refused `inference.create.response` carries no `inference_id` (L653–656, L1861–1862) | `schema_invalid` | schema | schema |

# Model provider profile

Decisions 0016 and 0030: the `model-provider-core` profile, judged by
`go/validation/provider.go` and `zig/src/validation/provider_semantic.zig`
against the provider corpus in `fixtures/provider/`. Rust has no provider
validator. Format: [README](README.md).

Decision 0016 deferred the profile's schemas and validator rules, and
[Decision 0061](../../decisions/0061-the-provider-profiles-validator-rules-and-schemas.md)
settles them: it adopts the draft's six semantic rules with the codes both
trees raise, and makes the provider schemas on `main` normative. Each row was
an open item, OPEN-0016-1 to 0016-7 in
[`research/validator-divergences.md`](../../research/validator-divergences.md#model-provider-profile-0016-0030),
and its lines are 0061's. Rust has no provider validator yet and owes every row.

| id | rule | lines | code | Go | Zig | Rust |
| --- | --- | --- | --- | --- | --- | --- |
| `0061#a-streamed-terminal-is-the-assembly-of-its-ended-parts-open-0016-1/1` | A streamed `inference.completed.message` MUST be the assembly of its ended parts in `part_index` order, `part.ended` overriding its deltas; a trace with no parts is outside the rule. | L27–31 | `terminal_not_assembly` | `assemblyDefect`, `partMismatch` | `Machine.close`, `assemblyDefect` | — |
| `0061#a-credential-in-headers-is-refused-by-name-and-by-value-open-0016-2/1` | A call's or a descriptor's `headers` MUST NOT carry `Authorization`, `Proxy-Authorization`, `X-Api-Key` or `Api-Key` in any case, nor a value beginning `Bearer ` in any case after leading spaces and tabs. | L35–41 | `credential_in_headers` | `credentialHeaders` | `Machine.credentialHeaders` | — |
| `0061#a-trace-never-carries-a-granted-credential-open-0016-3/1` | A trace MUST NOT carry a `provider.credential.grant.request` with a `value`; that envelope is judged no further. | L45–49 | `credential_in_trace` | `Validate` | `Machine.apply` | — |
| `0061#each-inference-has-its-own-contiguous-sequence-open-0016-4/1` | Every event scoped to one inference MUST carry a `sequence` in that inference's domain, opening at 1, contiguous, never repeating or going back. | L53–57 | `sequence_gap`, `sequence_regression` | `Validate` | `Machine.apply` | — |
| `0061#nothing-follows-an-inferences-terminal-open-0016-5/1` | No event scoped to an inference MAY follow its `inference.completed` or `inference.failed`, a second terminal included. | L61–63 | `event_after_terminal` | `Validate` | `Machine.apply` | — |
| `0061#a-refusal-allocates-no-inference-open-0016-6/1` | A refused `inference.create.response` MUST NOT carry an `inference_id`. | L67–70 | `schema_invalid` | schema | schema | — |
| `0061#the-provider-schemas-on-main-are-normative-open-0016-7/1` | Every provider frame MUST satisfy `provider-envelope.schema.json` and the schemas it references. | L74–79 | `schema_invalid` | schema | schema | — |

# Decision 0065: Usage Counts Have an Exact Wire Spelling

Status: accepted 2026-10-21 (implementation follows in separate changes)
Date: 2026-10-21
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core` and
`open-agent-protocol.model-provider-core`
Amends: [Decision 0063](0063-usage-counts-cache-and-reasoning-tokens.md) and
[Decision 0061](0061-the-provider-profiles-validator-rules-and-schemas.md)
Resolves the contract question in [#1398](https://github.com/lsm/open-agent-protocol/issues/1398);
its implementation gaps remain open there.

## Context

Decision 0063 defines what the six usage counts measure and distinguishes an
unknown count from a measured zero, but leaves their maximum and wire spelling
unstated. The shared usage schema has `type: integer` and `minimum: 0` without
a maximum. Go's typed agent payloads refuse a count above `uint64` maximum,
while Rust and Zig validators accept it. Integral decimal and exponent forms
are also judged differently across validators and SDKs, as #1398 records.

[JSON Schema 2020-12 validation §6.1.1](https://json-schema.org/draft/2020-12/json-schema-validation#section-6.1.1)
judges `integer` by mathematical value, including numbers with zero fractional
part. Thus `1.0` and `1e3` satisfy that type. A JSON Schema `pattern` applies
to strings and does not constrain a numeric token's original spelling.

[Hub D31 (`drafts/hub.md`)](../drafts/hub.md#d31--go-refused-a-schema-valid-sequence-spelled-10-as-malformed_json--fixed)
keeps that schema-integer acceptance for `sequence` and `timestamp_ms`:
Go normalizes their integral decimal/exponent spellings before typed decoding.
D31 remains unchanged. This decision deliberately adds a narrower wire contract
only for usage counts, so independently implemented consumers can retain exact
measurements, distinguish unknown from zero, and reject the same count tokens.
It does not infer a protocol rule from Go's decoder: [Decision 0032](0032-go-and-zig-are-peers.md#a-go-runtime-quirk-is-not-protocol-behaviour)
requires the specification to settle a runtime disagreement. The owner chose
this usage-only rule rather than generalizing either runtime's behavior.

## Decisions

### Only the shared usage counts have this contract

The rule applies to `input_tokens`, `output_tokens`, `total_tokens`,
`cache_read_input_tokens`, `cache_write_input_tokens` and `reasoning_tokens`,
wherever `common.schema.json#/$defs/usage` is referenced. Today those sites are
`content.completed`, `run.completed`, `run.failed`, `run.cancelled`,
`inference.completed` and `inference.failed`, in their `payload.usage` objects.
A nested application object that happens to use one of these names is outside
the rule. `sequence`, `timestamp_ms`, tool-result numbers and every other
numeric field retain their existing contracts, including hub D31.

### Usage counts are canonical unsigned 64-bit numbers

Each supplied count MUST be a JSON number token whose entire original spelling
matches `0|[1-9][0-9]*` and whose exact value is no greater than
`18446744073709551615` (`2^64-1`). A missing member means unknown; `0` means
measured zero. The usage object and each count remain optional as before.

The wire therefore refuses `-0`, decimals including `1.0`, exponent forms
including `1e3`, negatives, null, strings and booleans. Counts are neither
quoted decimal strings nor values normalized from another numeric spelling.
A reader MUST preserve the original numeric token until spelling and range
have been checked exactly; conversion through a floating-point value must not
round it, erase a minus sign, or turn a decimal/exponent token into an accepted
count. This does not change Decision 0063's measurement meanings or authorize
inferring a total from incomplete terms.

### Diagnostics follow value before spelling

A type or numeric-range violation is `schema_invalid`: the shared schema
requires an integer with `minimum: 0` and `maximum: 18446744073709551615`.
This includes null, strings, booleans, nonintegral numbers, negative values and
numbers above the maximum. Apply these value constraints exactly before the
additional spelling check; binary floating-point rounding must not change
which diagnostic applies.

An otherwise schema-valid count whose original token violates the canonical
spelling rule is `payload_decode`. Examples are `-0`, `1.0` and `1e3`.
`1.5` is `schema_invalid` because it is nonintegral; `1.5e1` is
`payload_decode` because its mathematical value is the in-range integer 15.
`1e30` is `schema_invalid` because it exceeds the maximum, despite also having
a noncanonical spelling. Invalid JSON itself retains its existing decode
classification; this rule does not make malformed JSON a schema instance.

### SDK precision signals do not change the wire

SDKs MUST retain absence and measured zero distinctly and MUST NOT round a
valid count or silently discard it. Go and Rust may retain optional `uint64`
counts, and Python may retain optional exact integers. TypeScript MAY keep
optional `number` counters: for a valid count above `Number.MAX_SAFE_INTEGER`,
it omits that numeric counter and attaches a typed, non-fatal signal in
`usage.unrepresentable_counts`, naming the wire field and retaining the exact
decimal string. The response or stream still completes normally; an unsafe
JavaScript number is not an invalid wire count.

For example, an SDK may project input `18446744073709551615` and output `0`
as `{"output":0,"unrepresentable_counts":[{"code":"unrepresentable_count","field":"input_tokens","value":"18446744073709551615"}]}`.
That signal is SDK metadata, not a member added to the OAP usage schema, and
MUST NOT be emitted as an OAP wire count or substituted for the exact number.
Actually invalid counts remain errors; SDKs report `malformed_response`
according to their existing typed response-error APIs.

## Consequences and work owed

This documentation change adopts the contract; it does not change executable
schemas, decoders, SDKs, fixtures or implementation-gap manifests. Separate
implementation changes after this decision merges owe:

- **Shared schema.** Add the exact maximum to all six usage members, retaining
  integer type, nonnegative minimum and optional presence. Schema validation
  covers values; numeric spelling remains an additional payload check.
- **Validators.** Rust and Zig must retain raw usage tokens and enforce exact
  range and canonical spelling at every shared-usage site, with the diagnostics
  above. Go must align its current overflow `payload_decode` with
  `schema_invalid` through the schema maximum and verify all six sites; no
  tree may claim completeness while judging only successful run terminals.
- **Go provider decoding.** Preserve original usage tokens through the
  provider validator's payload decoding instead of rounding through generic
  floating-point objects; enforce the same exact bounds and spelling as the
  agent profile without changing envelope sequence/timestamp normalization.
- **Python SDK.** Check raw usage number tokens so `-0` is refused before the
  JSON decoder turns it into the integer zero. Keep exact optional integers
  for accepted counts and typed errors for invalid ones.
- **Fixtures and consumer evidence.** Cover all six counts at all six shared-
  usage sites, including absent, zero, `2^53-1`, `2^53`, `2^64-1`, `2^64`, `-0`,
  `1.0`, `1e3`, fractional values, null, strings and booleans; pin diagnostic
  and exit parity across Go, Rust and Zig. Retain positive sequence/timestamp
  decimal/exponent cases for hub D31 and arbitrary tool-result numbers to prove
  that this usage-only exception has not spread. Exercise all four SDKs and
  TypeScript's non-fatal exact precision signals through their public APIs.

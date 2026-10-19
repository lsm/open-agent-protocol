# Decision 0063: Usage Counts Cache and Reasoning Tokens

Status: accepted 2026-10-19
Date: 2026-10-19
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core` and
`open-agent-protocol.model-provider-core`
Amends: [Decision 0049](0049-a-message-reports-its-own-usage.md) and
[Decision 0061](0061-the-provider-profiles-validator-rules-and-schemas.md)

## Context

`usage` carried `input_tokens`, `output_tokens` and `total_tokens`, and no text
said what they count. Harnesses do not agree: some report reasoning tokens
inside their output figure and some beside it, and some count cache reads
inside input while others report them apart. The Go opencode adapter summed
only opencode's input and output, so a run on a reasoning model reported about
a sixth of the output it was billed for and none of its 59 thousand cache-read
tokens. The claude adapter carried cache tokens in its own
`com.anthropic.claude-code.cost` extension, which a consumer pricing runs
across harnesses cannot read uniformly. The validator rule register recorded
the question as OPEN-0001-9.

## Decision

### Usage counts cache and reasoning in their own fields (OPEN-0001-9)

What does `usage` count? It gains three optional members, and each member
counts one thing:

- `input_tokens` counts input neither read from nor written to a cache.
- `cache_read_input_tokens` counts input served from a cache.
- `cache_write_input_tokens` counts input written to a cache.
- `output_tokens` counts every generated token, reasoning tokens included.
- `reasoning_tokens` counts the reasoning tokens within `output_tokens`; it
  is part of that figure, not added to it.
- `total_tokens` is `input_tokens` + `cache_read_input_tokens` +
  `cache_write_input_tokens` + `output_tokens`.

An absent member is unknown, not zero, as an absent `usage` already is. A
harness that reports a total but not every term of it still has its total
reported as `total_tokens`, provided it counts the same four kinds; an endpoint
that cannot tell omits `total_tokens` rather than summing only the terms it
has. An adapter maps its harness's own convention into these meanings: a
harness whose input figure includes cached tokens is reported with the cached
part moved out, so an OpenAI-style prompt count less its cached tokens becomes
`input_tokens`. The same `usage` serves both profiles, so `inference.completed` and
`inference.failed` gain the members too.

### The adapter cost extensions are deprecated

An adapter extension that reports cache tokens, such as
`com.anthropic.claude-code.cost`'s `cache_read_input_tokens` and
`cache_creation_input_tokens`, is deprecated. It stays until its consumers
read the core members, and an adapter fills both meanwhile.

## Consequences

- The `usage` schema in `common.schema.json` gains the three members in this
  change, with matching types in Go, Zig, Rust and `clients/ts`, a fixture
  reporting all six and one a negative count breaks.
- `go/internal/provider`'s Anthropic stream backfills an unreported total from
  input and output alone, which this decision's sum overrules; it owes the
  cache terms.
- The three new members are optional in every tree's type, so a measured zero
  is sent as 0 and an unknown count is left out. Go's and Rust's
  `input_tokens`, `output_tokens` and `total_tokens` still drop a zero, which
  the absent-is-unknown rule makes wrong; those types owe the same change.
- Owed: the Go opencode adapter and the Zig opencode reducer, which sum only
  opencode's input and output, report cache and reasoning in core `usage`;
  `go/internal/inferenceserve`, which drops the cache counts
  `go/internal/provider` already holds and totals input and output alone,
  carries them onto the provider wire; the acp adapter, which decodes thought
  and cached tokens but reports them only in `com.agentclientprotocol.turn_usage`,
  fills the core members; the claude adapter, which
  carries cache only in its extension, fills the cache members too; and the
  SDKs' usage types (Go, Rust, Python and TypeScript), which read the old
  three members or vendor names such as `cache_read`, read the new ones.

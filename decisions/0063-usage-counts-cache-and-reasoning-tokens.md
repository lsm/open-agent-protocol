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

An absent member is unknown, not zero, as an absent `usage` already is. An
adapter maps its harness's own convention into these meanings; a harness
whose input figure includes cached tokens is reported with the cached part
moved out, so an OpenAI-style prompt count becomes `input_tokens` minus the
cached tokens it already counted. The same `usage` serves both profiles, so `inference.completed` and
`inference.failed` gain the members too.

### The adapter cost extensions are deprecated

An adapter extension that reports cache tokens, such as
`com.anthropic.claude-code.cost`'s `cache_read_input_tokens` and
`cache_creation_input_tokens`, is deprecated. It stays until its consumers
read the core members, and an adapter fills both meanwhile.

## Consequences

- The `usage` schema in `common.schema.json` gains the three members, with
  matching edits to `protocol/`, the three validators, a fixture and
  `clients/ts/src/protocol.ts`.
- The Go opencode adapter reports cache and reasoning in core `usage`, and the
  claude adapter fills the cache members beside its extension.

# ACP v1.17.0 mapping ledger

This pin builds on the [v1.15.1 ledger](acp-v1.15.1-mapping.md) and its
predecessors. ACP wire version remains 1; ACP v2 is a separate protocol and is
not included in this adapter.

## Source and scope

- ACP release `v1.17.0` and schema release `schema-v1.31.0` both resolve to
  commit `0849001ca66fb975fc01111ddab9093bc8304e98` (both annotated tags,
  `b9f715d9cebea9c83c277a65dece77345a985997` and
  `fada949f064ac9d9b4232ae2eb8b5589a23e4648`).
- Specification tree SHA-256 (64 files, recipe of the v1.7.0 ledger):
  `6012cdb342da0d728e5ccee79d48a1186d3346d961bc260f84d7d21607eb01eb`.
- Schema tree SHA-256 (7 files):
  `56aef9797f56121f619737ccbcf37cd4983b52a5430b55157d0927d6c3bf8f66`.
  The recipes reproduce the v1.15.1 ledger's recorded digests on its commit.

## Schema comparison

The stable v1 generated schema was compared with schema-v1.29.1, excluding
prose, titles, descriptions, examples and generator-specific `x-` hints. The
differences:

- `NewSessionRequest`, `ResumeSessionRequest` and `LoadSessionRequest` gain
  optional nullable `systemPrompt` (`SystemPrompt`: content blocks, a mode of
  `append` or `override`, and per-agent scoping), and `SessionCapabilities`
  gains optional nullable `systemPrompt`
  (`SessionSystemPromptCapabilities` with mode and override capability
  objects). The requests' member is additive and optional: the adapter sends
  none of those requests with `systemPrompt`, and OAP input does not map onto
  it here. The session capability is additive and optional in a result the
  adapter decodes field-selectively (`SessionNewResult` ignores unknown
  members), and initialize's `agentCapabilities` is a `map[string]RawMessage`,
  so an agent advertising `systemPrompt` at either boundary is admitted and
  not projected. No change to an event, request or response the adapter
  projects.
- schema-v1.31.0's remaining entries (AuthAccount `description`,
  `session/options` initial values, tool-call session references under
  `unstable_subagents`) are unstable-gated additions, not stable v1 adapter
  surface. The reduced Rust source types changed only in `agent.rs`
  (AuthAccount description) and `tool_call.rs` (the unstable subagent
  reference).
- The stable v1 metadata registry (`schema/v1/meta.json`) is byte-identical at
  both commits.

The v2 schema was not used as v1 evidence.

## Evidence boundary

The new corpus carries forward the prior corpus unchanged at the native wire
level, with updated pin metadata, tree digests and capability revision
(`acp-v1.17.0-schema-v1.31.0-oap-v7`); it is not a fresh recording from an
agent. The Devin evidence limitations and mapped profile in the base ledger
continue to apply.

The catalog moves the cagent component to `v1.160.0` (commit
`f5cb1d66d67e274c2ec7cd22726614d5fffe98e8`, tree
`b938200392e643ac0424531c13bc5ec19b236fd7`). The process-gate evidence
recorded in the v1.12.0 ledger ran at the cagent version that ledger names;
no new cagent or Devin live test is claimed by this pin update, and cagent
v1.160.0 itself is unverified against the adapter.

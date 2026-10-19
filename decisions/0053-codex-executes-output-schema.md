# Decision 0053: Codex Executes `output_schema`

Status: accepted 2026-10-19 (Codex app-server 0.163.0 advertises
`run.structured_output` natively in both trees; corpus cases
`structured-output` and `structured-output-nonconforming`)
Date: 2026-10-19
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `run-controls` (claim term `+run-controls`)
Amends: [Decision 0005](0005-run-controls.md), graduating the execution of
`output_schema` on a native adapter as it provides
Gated by: [Decision 0003](0003-staged-unit-graduation.md)

## Context

Decision 0005 froze `output_schema`'s shape and executed it only in the
reference adapter: "each graduates as executable by an amendment to this
decision when a native adapter advertises its key against a pinned ledger."

The Codex adapter advertised `run.structured_output` as `unavailable`, saying
its pin exposed no per-turn output schema. No ledger recorded that, and the
pinned source contradicts it: at `rust-v0.163.0`, `TurnStartParams` takes a
non-experimental `outputSchema` that constrains the turn's final assistant
message, and the final `agentMessage` item carries that message whole
([the ledger](../research/codex-app-server-0.163.0-mapping.md#structured-output-at-this-pin)).
A consumer could pass a schema to Claude Code but had to parse prose from
Codex.

## Decisions

1. **Codex executes `output_schema` natively.** Both trees compile an
   admitted schema as Decision 0005 requires, refuse one nothing can satisfy
   as `unsatisfiable` before `turn/start`, and forward it verbatim as
   `turn/start.outputSchema`.
2. **The result is the final message, checked.** `run.completed.result` is
   the `text` of the turn's last completed `agentMessage`, parsed as JSON and
   validated against the schema. A final message that is not conforming JSON
   ends the run `run.failed` with `structured_output_failed`, the code
   [the conformance draft](../drafts/conformance.md) already names, and never
   a completion without its result.
3. **The ladder is unchanged.** The schema refusal ranks within the
   unsatisfiable rung by capability key, so an empty `model_id` is still
   answered `model_not_found` first, and both answer before the adapter's own
   submission validation.

## Consequences

- `run.structured_output` is `native` for Codex; the revision moves to
  `codex-appserver-0.163.0-oap-v2`.
- `instructions` and `tool_choice` remain without native execution on any
  adapter; Decision 0005's condition for them is unchanged.

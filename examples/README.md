# Open Agent Protocol Examples

These files are illustrative JSON bindings for the draft protocol, not
conformance tests. Normative executable traces live in `../fixtures/` and are
listed by `../fixtures/manifest.json`.

`core-run-stream.json`, `capability-refresh.json`, and `core-user-input.json`
are source material for corrected normative fixtures. The broader capability
files, `agent-control-run-stream.json`, and `tool-source.json` use staging
concepts that are not all part of executable v0.1. In particular, nested `scope`/`trace` fields do not
replace the flat core envelope, and `model.content.delta` belongs to a future
model-IO boundary rather than the core `content.delta` stream.

- `core-run-stream.json`: ordered agent-control core stream for a simple
  message/final-response run, including message submission, admission, run
  status, streamed content, terminal completion, and session state.
- `core-user-input.json`: optional `+user-input` prompt flow, separate from
  permission approval.
- `presentation-control-session.json`: revisioned session-target snapshot, user
  intent, typed presentation changes, and affordance changes.
- `agent-capabilities.json`: direct agent-control capability response.
- `degraded-adapter-capabilities.json`: adapter capability response that reports
  feature loss as degradation.
- `capability-refresh.json`: stale capability precondition, typed rejection,
  and descriptor refresh flow.
- `tool-source.json`: session-scoped action/tool discovery (request and
  response) from a generic process-backed tool source, with each tool naming
  its source by id. Staged, not executable v0.1: the tools-list request payload
  is closed with no members today, and neither the list response nor
  `ToolDefinition` admits `session_id`, `sources`, `source`, or `features`, so
  the v0.1 validator rejects this document. It shows the shape the tool-sources
  unit (T3a) introduces — see
  [`drafts/staged-units-graduation.md`](../drafts/staged-units-graduation.md).
- `agent-control-run-stream.json`: ordered agent-control stream for a simple
  model/tool/model
  run.

An extension pack has no illustrative example here, because a pack is
executable rather than illustrative: `../fixtures/packs/storage` is a worked
pack — a descriptor, the branches it contributes, the members it adds to core
payloads, and its own fixture corpus — and the traces that exercise it are
normative fixtures listed in `../fixtures/manifest.json`. Load one with
`goap validate -pack ../fixtures/packs/storage <trace.json>`; see
[Decision 0004](../decisions/0004-extension-packs.md).

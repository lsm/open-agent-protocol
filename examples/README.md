# Open Agent Protocol Examples

These files are illustrative JSON bindings for the draft protocol, not
conformance tests. Normative executable traces live in `../fixtures/` and are
listed by `../fixtures/manifest.json`.

`core-run-stream.json`, `capability-refresh.json`, and `core-user-input.json`
are source material for corrected normative fixtures. The broader capability
files and `agent-control-run-stream.json` use staging concepts that are not all
part of executable v0.1. In particular, nested `scope`/`trace` fields do not
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
- `tool-source.json`: action/tool discovery from a generic process-backed tool
  source.
- `agent-control-run-stream.json`: ordered agent-control stream for a simple
  model/tool/model
  run.
- `oap-serve.json`: adapter registry document for `oap serve` (HTTP + SSE) and
  `oap serve --stdio` (NDJSON on stdin/stdout); one entry per in-repo adapter
  type, with the `environment` allowlist that governs adapter credentials.
- `oap-stdio-session.ndjson`: host-side request script for
  `oap serve --stdio`, runnable as
  `oap serve --stdio < examples/oap-stdio-session.ndjson` — list adapters,
  probe capabilities, open a session, subscribe, read state, and close; the
  daemon's responses and the session-closed signal interleave on stdout.

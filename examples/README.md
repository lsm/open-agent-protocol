# Open Agent Protocol Examples

These fixtures are illustrative JSON bindings for the draft protocol. They are
not conformance tests yet, but they are shaped so future tests can load them as
complete protocol envelopes.

- `core-run-stream.json`: ordered agent-control core stream for a simple
  message/final-response run, including message submission, admission, run
  status, streamed content, terminal completion, and session state.
- `core-user-input.json`: optional `+user-input` prompt flow, separate from
  permission approval.
- `presentation-control-session.json`: view open, view snapshot, user intent,
  renderable view updates, and affordance changes.
- `agent-capabilities.json`: direct agent-control capability response.
- `degraded-adapter-capabilities.json`: adapter capability response that reports
  feature loss as degradation.
- `tool-source.json`: action/tool discovery from a generic process-backed tool
  source.
- `agent-control-run-stream.json`: ordered agent-control stream for a simple
  model/tool/model
  run.

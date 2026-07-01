# Open Agent Protocol Examples

These fixtures are illustrative JSON bindings for the draft protocol. They are
not conformance tests yet, but they are shaped so future tests can load them as
complete protocol envelopes.

- `core-run-stream.json`: ordered UI runtime core stream for a simple
  model/tool/final-response run, including run acknowledgement and transcript
  sync.
- `core-user-input.json`: core user-input prompt flow, separate from permission
  approval.
- `runtime-capabilities.json`: direct runtime capability response.
- `degraded-runtime-capabilities.json`: adapter capability response that reports
  feature loss as degradation.
- `tool-source.json`: Layer 2 tool discovery from a generic process-backed tool
  source.
- `ui-run-stream.json`: ordered UI runtime stream for a simple model/tool/model
  run.

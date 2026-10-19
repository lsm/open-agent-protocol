# Decision 0064: Every Request Waits for the Refresh

Status: accepted 2026-10-19
Date: 2026-10-19
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Amends: [Decision 0055](0055-register-batch-2.md) and
[Decision 0001](0001-agent-control-v0.1-executable-core.md)

## Context

Decision 0055 settled OPEN-0001-8, the window between `capabilities.updated`
and the `capabilities.response` that refreshes the sender's descriptor. As the
owner approved it then, a request in the window was stale only if it cited the
pre-update revision or exercised an optional feature. Porting it showed what
that leaves out: a plain request carrying no revision and using no optional
feature, such as an ordinary submit or a `session.state.request`, passed. Go
and Zig had flagged a plain submit in the window all along. The owner has now
chosen the stricter rule.

## Decision

### Every non-exempt request in the window is stale (OPEN-0001-8)

Between `capabilities.updated` and the `capabilities.response` that carries
the new revision, every request is `stale_capability_revision`, whether or not
it cites a revision or exercises an optional feature. A sender just told that
the endpoint's capabilities changed acts on nothing until it knows what
changed. Exempt, as 0055 already named them, are the requests that discover the
new descriptor, `protocol.initialize.request` and `capabilities.request`, and
the ones that settle work already in flight: `run.cancel.request`,
`action.permission.resolve.request`, `user.input.resolve.request`,
`user.input.cancel.request` and `action.call.resolve.request`.

## Consequences

- Decision 0055's refresh section and Decision 0001's capabilities clause
  state the stricter rule in their own text, as do the core draft and
  conformance requirement 14.
- Go and Zig judge every non-exempt request in the window, and Rust does the
  same. Three fixtures gain `stale_capability_revision` in the porting change:
  a `session.state.request` in the window, a `models.request` citing the new
  revision before the refresh, and a provider attach in the window.

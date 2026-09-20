# Decision 0024: The Filter Judges the Session's Catalog

Status: proposed
Date: 2026-09-20
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `run-controls` (claim term `+run-controls`)
Amends: [Decision 0022](0022-tool-choice-is-a-filter-here-and-a-mode-there.md),
widening the catalog its filter is judged against, and extends
[Decision 0011](0011-control-layer-provided-tools.md) without amending it
Gated by: [Decision 0003](0003-staged-unit-graduation.md)

## Context

Two things in this protocol are called a catalog. The endpoint publishes one on
`capabilities.response.tools`: what the endpoint ships, before any session
exists. A session has another, published by `action.tools.list.response`: what
*this* session can call, which is the endpoint's tools plus whatever the
control layer provided at open under
[Decision 0011](0011-control-layer-provided-tools.md).

`tool_choice` was judged against the first. The validator set its catalog only
from `capabilities.response` and cleared it on `capabilities.updated`, and no
other envelope touched it, so a policy naming a tool the control layer had
provided minutes earlier was refused `unsatisfiable` for naming a tool outside
the catalog. That refusal was reachable in a trace where the session's own
`action.tools.list.response`, earlier in the same trace, listed the tool by
name.

The tree did not agree with itself about this. `adapter/memory.go` builds its
catalog from the scripted tools *and* the session's provided tools, and elects
a tool to call by asking the policy to permit a name from that set — so the
reference adapter already treats a provided tool as filterable, and a trace
exercising it would have been refused by the validator that adapter exists to
satisfy. The validator also already required the session catalog to contain
every provided tool, diagnosing `catalog_mismatch` when a list response omits
one, and refused an open that provides a name the descriptor catalog already
resolves. The descriptor catalog was authoritative enough to reject a provided
tool for colliding with it, and not authoritative enough to contain that tool
afterwards.

## Decisions

### `tool_choice` is judged against the session's catalog

The catalog a policy is judged against is the endpoint's advertised tools plus
the tools the control layer provided to that session. `allowed` naming a
provided tool is satisfiable; `allowed` naming nothing in either set is
unsatisfiable exactly as before.

The catalog is per session, because provisioning is. A tool provided to one
session is not in another's catalog, and a policy naming it there is refused
for the same reason it always was.

A compound open judges its own message against its own tools. Under
[Decision 0009](0009-compound-open.md) one request may provide tools and carry
a submission, and the submission is judged while the request is read — before
the response that records what the session holds. The names the request itself
supplies therefore count for the message it carries, or the rule above would
hold for every run but the first, and exactly the refusal this decision removes
would survive wherever it mattered most.

Nothing about what may be called changes. `Permits` already asked the same
question of the same membership, and the reference adapter already answered it
with the session's tools; this aligns the validator with the behaviour the
adapter had, rather than giving anyone a new capability.

### What this makes possible, and why that matters more than the rule

A control layer can now provision at open every tool a session might need and
reveal them one run at a time: each submission's `allowed` list names the
baseline, and the run where the agent needs more names more. The extra tool
exists for the length of that run and is invisible before and after, because
`run.tool_selection` is scoped to the run under
[Decision 0021](0021-a-tool-policy-says-whether-it-outlives-its-run.md).

That composition is worth stating because it is the reason not to build the
alternative. Provisioning a tool mid-session — carrying `tools` on a submission
rather than only on an open — would need new wire, and no harness studied in
`research/` accepts a tool definition after the open; the Codex ledger records
dynamic client tools as unavailable at its pin. Building it would put a second
fully specified, unimplemented control into the profile, which is the defect
[Decision 0022](0022-tool-choice-is-a-filter-here-and-a-mode-there.md) removed.
The same need is met here by two mechanisms that already execute.

The limit is honest and belongs in the record: the superset must be known at
open. A tool nobody anticipated cannot be revealed, and if that case turns out
to matter in practice, the experience of hitting it is the evidence the
mid-session grant currently lacks.

## Evidence

Fixtures (`fixtures/manifest.json`): `valid/control-tool-revealed-for-one-run.json`
opens a session providing two control-owned tools beside the endpoint's `grep`,
lists them, then runs twice — the first `allowed: ["grep"]`, the second
`allowed: ["grep", "upper"]`, naming a provided tool. Both are admitted and
complete. Under the previous rule the second run was refused
`unsatisfiable_control`; that refusal is what this decision removes, and the
fixture is the trace it was found with.

`valid/compound-open-reveals-its-own-provided-tool.json` is the same rule on
one envelope: an open that provides the tool and carries a message naming it in
`allowed`. It was refused until the request's own names were counted.

Reference execution: `adapter/memory.go` is unchanged. It already composed its
catalog this way, which is why no adapter test moved.

## Consequences

- The validator and the reference adapter agree about what a policy may name.
  They did not before, and the disagreement was only invisible because no
  fixture combined provisioning with a filter.
- `duplicateToolName` now sees provided names too, so a provided tool
  colliding with another provided tool makes the catalog ambiguous the same way
  two advertised tools do.
- The Zig validator composes the same set, so both implementations judge one
  catalog.

## What this unit does not admit

- Provisioning after the open. `tools` stays on `session.open.request`; a
  submission carries a filter, never a definition.
- Any change to `disallowed`, which
  [Decision 0023](0023-a-denylist-does-not-assert-the-catalog.md) settled, or
  to what an unknown catalog means, which
  [Decision 0021](0021-a-tool-policy-says-whether-it-outlives-its-run.md)
  deferred.

# Decision 0034: An Unpublished Catalog Is Unknown

Status: accepted
Date: 2026-09-24
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `run-controls` (claim term `+run-controls`)
Amends: [Decision 0022](0022-tool-choice-is-a-filter-here-and-a-mode-there.md)
and [Decision 0024](0024-the-filter-judges-the-session-catalog.md), stating
when the catalog they judge against is known, and answers the unknown-catalog
question [Decision 0021](0021-a-tool-policy-says-whether-it-outlives-its-run.md)
deferred to the enforcement unit
Gated by: [Decision 0003](0003-staged-unit-graduation.md)

## Context

The validator already has a meaning for an unknown catalog. After
`capabilities.updated` and before the next `capabilities.response` it judges a
`tool_choice` by its lists alone: `allowed` is not checked for membership, and
`Permits` excludes exactly what `allowed` omits or `disallowed` names. What
could not happen is a descriptor declaring its catalog unknown. Every
`capabilities.response` set the catalog known, and a descriptor without
`tools` set it known and empty.

That reading is wrong for an endpoint whose tools exist only per session. The
Claude Code adapter's tools come from the CLI's `system/init`, republished per
turn and absent before the first; its descriptor publishes none, and its
`action.tools.list` is `degraded`. Run through the validator at `main`, with a
descriptor carrying no `tools` and `run.tool_selection` `emulated`:

- `disallowed: ["Bash"]`, then a call to `Read` that completes:
  `unapplied_control`, "a call to a tool the admitted tool_choice excludes
  settled without a policy refusal".
- The same with the session's own `action.tools.list.response` listing `Bash`
  and `Read` before the submit: the same `unapplied_control`, because that
  list never reaches the filter.
- `allowed: ["Read"]`, with or without that list: refused
  `unsatisfiable_control`, "allowed names a tool outside the catalog".

So a denylist excluded every tool and an allowlist could name none. The
adapter could not correct this: no descriptor it can write says "unknown".

## Decisions

### Publishing `tools` is what makes the catalog known

A `capabilities.response` that carries a `tools` member, at the top level or
in any layer, publishes a catalog, and the catalog is known and holds what
those members list, including nothing when they are empty. A descriptor that
carries no `tools` member anywhere publishes none, and the catalog is unknown
until a later `capabilities.response` publishes one.

Under an unknown catalog the existing rules hold unchanged: `allowed` naming
any tool is satisfiable, an empty `allowed` still excludes every tool, and a
call is excluded exactly when `allowed` omits it or `disallowed` names it.
Decision 0024's session catalog is unknown too, since its advertised half is.

### Why presence, not the session list

The alternative was to extend Decision 0024 so that tools the endpoint lists on
`action.tools.list.response` join the session catalog. It does not help the
endpoint that raised this: Claude's list is empty before the first turn and
changes on every turn, so a first-run denylist would still exclude every tool,
and a catalog that grows between runs would make a retained `allowed` flip
from unsatisfiable to satisfiable without any capability revision. Presence of
`tools` is decidable from one envelope, costs an endpoint with a static catalog
nothing, and lets an endpoint that has no catalog before a session say so.

An endpoint with no tools that wants `allowed: ["x"]` refused as unsatisfiable
publishes `"tools": []`.

## Evidence

Fixtures (`fixtures/manifest.json`), all with a descriptor carrying no `tools`
unless stated:

- `valid/controls-tool-choice-unpublished-catalog-disallows-only-named.json`:
  `disallowed: ["Bash"]`; `Read` completes and `Bash` settles
  `refused_by_policy`.
- `valid/controls-tool-choice-unpublished-catalog-allows-by-name.json`:
  `allowed: ["Read"]` is admitted; `Read` completes and `Bash` settles
  `refused_by_policy`.
- `semantic-invalid/controls-tool-choice-unpublished-catalog-ignored.json`:
  `disallowed: ["Bash"]` and `Bash` completes, `unapplied_control`. An unknown
  catalog does not suspend the filter.
- `semantic-invalid/controls-tool-choice-published-empty-catalog.json`:
  `"tools": []` with `disallowed: ["Bash"]` and `Read` completes,
  `unapplied_control`. A published empty catalog stays known.
- `semantic-invalid/controls-tool-choice-layer-published-catalog.json`: `Read`
  published only in a layer, `allowed: ["Bash"]`, `unsatisfiable_control`. A
  layer's `tools` publishes the catalog as the top level's does.

Go (`publishesCatalog` in `go/validation/controls.go`) and Zig
(`collectCatalog` in `zig/src/validation/semantic.zig`) judge presence the same
way. No existing fixture or adapter test moved.

## Consequences

- An endpoint without a descriptor catalog can advertise `run.tool_selection`
  and have a denylist exclude only what it names. The Claude adapter's
  `emulated` advertisement becomes useful rather than all-or-nothing.
- A Go encoder that marshals an empty `Tools` with `omitempty` now publishes
  an unknown catalog rather than an empty one. The difference is only in
  whether `allowed` naming a tool is refused at admission; no adapter in the
  tree relied on it.
- The collision check between a provided tool and the descriptor's catalog
  already skips an unknown catalog, so it skips an unpublished one.

## What this unit does not admit

- Tools listed on `action.tools.list.response` joining the catalog a policy is
  judged against. The session list still binds only `catalog_mismatch`.
- A retained policy's meaning as a catalog grows, which Decision 0023 left to
  the enforcement unit.

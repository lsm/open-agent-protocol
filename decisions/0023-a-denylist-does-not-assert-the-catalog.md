# Decision 0023: A Denylist Does Not Assert the Catalog

Status: proposed
Date: 2026-09-20
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `run-controls` (claim term `+run-controls`)
Amends: [Decision 0022](0022-tool-choice-is-a-filter-here-and-a-mode-there.md),
narrowing one satisfiability rule it inherited unchanged from
[Decision 0005](0005-run-controls.md)
Gated by: [Decision 0003](0003-staged-unit-graduation.md)

## Context

[Decision 0022](0022-tool-choice-is-a-filter-here-and-a-mode-there.md) made
`tool_choice` a filter and kept the membership rule it found: a name in
`allowed` or in `disallowed` that the catalog does not list makes the policy
unsatisfiable wherever the catalog is known. The rule treats the two lists
alike because it was written when they were two spellings of one idea.

They are not one idea. An allowlist is a claim about what exists: writing
`allowed: ["grep"]` says the caller believes `grep` is there, and if it is not,
the policy does not mean what the caller thinks — it admits a smaller set than
intended, possibly none. Telling the caller is the whole point of the rule, and
the refusal is actionable: fix the name or stop sending it.

A denylist claims the opposite, and claims nothing about existence.
`disallowed: ["bash"]` says that if a tool named `bash` is on offer it must not
run. An endpoint that ships no `bash` satisfies that perfectly. Refusing the
policy there answers a question the caller did not ask, and the answer is not
actionable in any useful direction: the caller must delete a name whose whole
purpose is to be harmless when absent.

The cost is portability, and it falls on exactly the callers the protocol
exists for. A control layer driving several endpoints writes one denylist of
what it will not permit anywhere. Under the symmetric rule that list is refused
by every endpoint whose catalog happens to omit one of its entries, so the
caller must maintain one denylist per endpoint, differing only in which absent
tools they are forbidden to mention — and must rewrite them whenever a catalog
changes. A safety-shaped control that gets weaker as it is carried between
endpoints is the wrong shape.

## Decisions

### Only `allowed` is checked against the catalog

A name in `allowed` that a known catalog does not list is unsatisfiable, with
the pointer naming the offending entry, as before. A name in `disallowed` is
never a defect, whatever the catalog holds and whether or not it is known.

Nothing else moves. `Filter` already ignores a `disallowed` entry that matches
nothing, and `Permits` already refuses a tool outside a known catalog, so the
admitted set is what it always was — a policy that was satisfiable stays
satisfiable and filters identically. What changes is only which policies are
refused before admission.

### The asymmetry is the same one the closed-world reading rests on

An allowlist is closed over the catalog it was written against and a denylist
is open over every catalog it may meet. That is why the two lists stay mutually
exclusive: they are not complements, and a policy carrying both would be making
a claim about existence and disclaiming one in the same breath.

This decision records the asymmetry in the satisfiability rule, where it was
missing. It does not settle what a retained policy means when the catalog grows
under it, which needs a policy that outlives its run and is deferred with
that one.

## Evidence

Fixtures (`fixtures/manifest.json`, unit `run-controls`):
`valid/controls-tool-choice-disallows-an-uncatalogued-tool.json` submits
`disallowed: ["absent_tool"]` against a catalog holding only `scripted_tool`
and is admitted and completed. Its allowlist twin,
`semantic-invalid/controls-tool-choice-unknown-entry.json`, is the same trace
with the same name under `allowed`, refused `unsatisfiable_control`. The pair
is the rule: one trace, one member moved, opposite verdicts.

No existing fixture changed verdict, because none had exercised a denylist
naming a tool outside the catalog.

Reference execution: `adapter/memory.go` admits the policy through the same
`Unsatisfiable`, and `TestPublishedCatalogGovernsToolSelection` carries the
case.

## Consequences

- A control layer can write one denylist and send it to every endpoint it
  drives, which is what makes a denylist worth having.
- The Zig validator's `toolChoiceDefect` loses its `disallowed` loop and the
  filtered set it no longer needed, so both implementations judge the same
  policies.
- `drafts/conformance.md` already stated the rule this way, describing only
  `allowed` as catalog-checked. The prose was ahead of the code; this closes
  the gap rather than opening one.

## What this unit does not admit

- Any change to `allowed`. Naming a tool that does not exist stays a defect,
  and [Decision 0021](0021-a-tool-policy-says-whether-it-outlives-its-run.md)'s
  deferral still governs when the catalog is not yet known.
- Granting a tool the endpoint does not offer. A denylist tolerating an absent
  name is not a step toward adding one; that is an additive control with its
  own decision.

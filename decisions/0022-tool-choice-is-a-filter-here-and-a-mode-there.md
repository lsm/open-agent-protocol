# Decision 0022: `tool_choice` Is a Filter Here and a Mode There

Status: proposed
Date: 2026-09-20
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `run-controls` (claim term `+run-controls`)
Amends: [Decision 0005](0005-run-controls.md), narrowing the shape of one
control and replacing one disclosure member, and
[Decision 0021](0021-a-tool-policy-says-whether-it-outlives-its-run.md), which
named the member this decision renames
Gated by: [Decision 0003](0003-staged-unit-graduation.md)

## Context

`tool_choice` is older than this protocol and came from a different layer. The
union `{ auto, none, required, function }` landed in `zig/src/ai_types.zig` on
2026-02-16 in `d734210c`, seven months before the agent-control profile
existed, and `zig/src/providers/` still serialises it to four provider wires:
OpenAI writes the bare string, Anthropic an object with a `type`, and both
Google APIs a `mode` field. That is the layer where it belongs and where it
works: one request, one completion, one decision about whether a tool is
called.

It reached agent-control-core by name before it reached it by design.
`03156615` added `tool_choice` to `session.message.submit.request` on
2026-09-06 with the schema `true` — a reserved key with no shape. Ten days
later [Decision 0005](0005-run-controls.md) gave it one, and took the
vocabulary from the layer that already had it: the same four modes, with
`function` generalised to `named`.

Nothing at this layer asked for them. The eight harness mapping ledgers in
`research/` do not mention `tool_choice` once, and those ledgers are this
repository's record of what each harness does at its wire boundary.
`research/harness-interoperability.md` lists "caller-supplied tool selection"
under P1, as a question the interoperability study raised rather than an
observation it made. Among the adapters, only the reference adapter advertises
`run.tool_selection` above `unavailable`; Codex declares it unavailable and the
six remaining harness adapters omit the key. Decision 0005 said as much at the
time — `tool_choice` keeps a frozen shape and reference-adapter execution, with
native evidence pending — and the evidence has not arrived in the four months
since.

The mismatch is not the missing evidence. It is that the modes mean something
only where a request is one model call. `required` says the completion must
contain a tool use; `named` says which. An agent loop is not one call: it is
many completions, tool executions, and decisions the harness makes between
them, and "this turn must contain a tool call" has no single place to be true.
What a control layer actually wants from a loop is narrower and does transfer:
which tools may be used at all.

## Decisions

### At this layer `tool_choice` is a filter

`tool_choice` on `session.message.submit.request` and on the open message
carries `allowed` or `disallowed`, mutually exclusive as before, and nothing
else. `mode` and `name` leave the agent-control profile, and with them the
constants `auto`, `none`, `required` and `named`.

The rules that survive are the ones about membership: a name in `allowed` or
`disallowed` that the catalog does not list is unsatisfiable when the catalog
is known, and a tool the filter excludes may not be called. The rules that go
are the ones that could only be judged against a single completion — that a
`required` policy is unsatisfiable against an empty filtered set, that a
`named` tool must be permitted by its own policy, and that a run completing
without the demanded call is `unapplied_control`.

Nothing is lost at the layer that had them. `schema/v0.1/inference.schema.json`
still carries `tool_choice` on its create request, and the Zig provider
implementations still send all four values to the APIs that define them.

### `none` is an empty allowlist

Dropping the modes does not cost the ability to say "use no tools". An
`allowed` list with no members admits nothing, which is what `none` meant, and
it says it through the member that already decides what is admitted rather than
through a second mechanism that has to agree with the first. One way to express
a restriction is worth more than two, because two can disagree — a policy of
`mode: "none"` beside a non-empty `allowed` list was always a contradiction the
validator had to be taught to catch.

### `run.tool_selection` stops disclosing `modes`

`modes` on `run.tool_selection` was the set of `tool_choice` modes the endpoint
enforces. With no modes to enforce, the member has nothing to say, and
`undisclosed_selection_modes` stops being raised for it.

`FeatureSupport.modes` itself stays. `action.tool_sources.attach` uses it for a
different set — where a source may be attached — and that disclosure is
untouched, as is `undisclosed_attach_modes`.

### `mode` becomes `scope`

[Decision 0021](0021-a-tool-policy-says-whether-it-outlives-its-run.md) added
`mode` to `run.tool_selection` beside `modes`, which made `run.tool_selection`
the first feature key to carry both and exposed how little the two words
distinguish. That collision is gone once `modes` leaves the key, so this rename
is not a repair. It is a better name, made cheap by a change that was happening
anyway.

`FeatureSupport.mode` becomes `scope`, and its values become `run` and
`session`. What the member reports is how long an admitted control lives, which
is what `scope` says and what `mode` does not. The values follow the key: a
half-renamed `scope: "per_run"` would read worse than either end state, and
`session_mutation` described an effect rather than a scope. The effect is
unchanged and stays where it was already written — under
`scope: "session"` the session default moves and session state afterwards
reports the native truth, exactly as [Decision 0005](0005-run-controls.md)
settled for `session_mutation`.

`scope` is free on the wire: it appears in no schema in `schema/v0.1/`.

A descriptor is judged on the member it carries, so the defect gains its own
code. `undisclosed_selection_scope` replaces `undisclosed_selection_modes` for
a `run.model_selection` or `run.tool_selection` that discloses no scope or one
this protocol gives no rules. `undisclosed_selection_modes` is retired with the
member it was named for. Per-member codes are the existing pattern rather than
a new one: `undisclosed_attach_modes` is already separate.

## Evidence

Fixtures (`fixtures/manifest.json`, unit `run-controls`): the twenty traces
that sent a `tool_choice` with a mode are rewritten to the filter shape or
retired where the mode was the only thing under test. The fixtures asserting
`undisclosed_selection_modes` move to `undisclosed_selection_scope` where the
defect is a scope, and retire where the defect was an undisclosed `modes` list
on `run.tool_selection`.

Reference execution: `adapter/memory.go` drops `Modes` from
`run.tool_selection` and declares `Scope: "run"` on both selection keys. Its
capability revision moves again because the descriptor changed.

Native evidence: none is added and none is removed. The four modes keep the
only implementations they ever had, in `zig/src/providers/`, against the
provider APIs that define them.

## Consequences

- A control layer that wants "these tools and no others" writes one member,
  and the shape no longer suggests it can force a call that no harness in this
  repository can force.
- `run.tool_selection` advertised above `unavailable` now discloses `scope`
  and nothing else, so the key means one thing.
- Every descriptor in the corpus that declared a selection `mode` declares a
  `scope` instead, and the model-selection rules key on the renamed member
  without changing.
- The agent-control profile no longer defines a control whose only executor is
  a reference adapter written to exercise the validator.

## What this unit does not admit

- Granting a tool a session did not start with. A control layer that answers
  "I need something I do not have" by widening the set for one run is
  describing an additive control, and every tool-shaped member on the wire
  today either narrows or attaches a source at open. The escalation case is
  real and is the strongest argument yet for an additive unit, which needs its
  own evidence and its own decision.
- Enforcement of `scope: "session"` for tool policy, which
  [Decision 0021](0021-a-tool-policy-says-whether-it-outlives-its-run.md)
  already deferred and this decision does not advance.
- Any change to `tool_choice` in `schema/v0.1/inference.schema.json` or to the
  provider implementations. The modes stay there, unshaped by the schema and
  executed by the Zig providers, exactly as they are today.

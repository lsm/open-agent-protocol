# Decision 0031: A Policy Refusal Is a Settlement

Status: accepted 2026-09-24 (the validator arbitrates at settlement in Go and
Zig, with six `controls-tool-choice` fixtures; the Claude adapter's `emulated`
advertisement follows separately)
Date: 2026-09-23
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Unit: `run-controls` (claim term `+run-controls`)
Amends: [Decision 0022](0022-tool-choice-is-a-filter-here-and-a-mode-there.md),
moving where one of its surviving rules is judged, and
[Decision 0005](0005-run-controls.md), stating what `emulated` means for
`run.tool_selection`
Gated by: [Decision 0020](0020-error-codes-are-declared.md) for the
classification of the one code it adds
Resolves: [#122](https://github.com/lsm/open-agent-protocol/issues/122)

## Context

Decision 0022 keeps one rule about calls: a tool the filter excludes may not be
called. The validator enforces it at `action.call.requested`
(`checkCallAgainstChoice`, `go/validation/controls.go`, reached from one call
site in `state.go`), and it raises `unapplied_control` there unconditionally.
That rule is only satisfiable by an endpoint that constrains the model before it
acts.

Most harnesses cannot do that. The model decides to call a tool, and the only
lever the harness gives a host is a per-call decision afterwards. Claude Code's
`can_use_tool` is one. #122 found the gap while advertising `run.tool_selection`
on the Claude adapter (#120, withdrawn), and an audit of every shipped adapter
confirmed the shape:

- `memory` is the only adapter that advertises the control affirmatively. It
  honours the control by electing the tool itself from the permitted set, so it
  never requests an excluded tool.
- `codex` advertises it `unavailable`.
- `acp`, `opencode`, `pi`, `deepseek`, `hermes` and `claude` advertise nothing.

Such an endpoint can enforce the filter, but it cannot report enforcing it:

- **Projecting honestly** gives `requested`, `started`, then `failed`, which is
  what the harness did, and the trace is invalid. Reproduced by turning the
  completion in `fixtures/semantic-invalid/controls-tool-choice-ignored.json`
  into a failure: `unapplied_control` at `action.call.requested`, unchanged.
  Dropping `started` as well adds `illegal_tool_transition` and `sequence_gap`,
  so the honest projection must keep it.
- **Suppressing the projection** makes the trace valid by hiding an attempt the
  harness really made, along with the denial result that follows. That is the
  silent compensation the adapter ledgers exist to prevent.

So today the only conforming answer is `unavailable`, even for an endpoint that
enforces the policy. The control is usable only by the reference adapter, which
is close to saying it cannot be implemented by the things it exists for.

## Decisions

### `refused_by_policy` names the outcome

A call the endpoint refused because the admitted `tool_choice` excludes it
settles as `action.call.failed` with `error.code` `refused_by_policy`. The
settlement records that the attempt happened and that the control was applied
to it. Neither half is left for a consumer to infer.

The code is core. It is emitted by this protocol's own policy enforcement, not
reported by a harness. Under Decision 0020 it belongs in the generic set, with
no harness prefix and no origin member, and it is the one code this record adds
to that set.

### The rule is judged at settlement

`unapplied_control` for an excluded tool moves from `action.call.requested` to
the call's settlement:

- A call to an excluded tool that settles as `failed` with `refused_by_policy`
  is valid. The control was applied.
- A call to an excluded tool that settles any other way is `unapplied_control`,
  reported at the settling envelope. That covers `completed`, `cancelled`, and
  a `failed` with any other code. The tool ran, or was stopped for a reason
  that is not the policy. A call cancelled with its run was admitted, not
  refused.
- A call to an excluded tool still unsettled when its run reaches a terminal is
  `unapplied_control`, reported at the terminal.
- A call to a permitted tool that settles with `refused_by_policy` is
  `unapplied_control` too. The endpoint claimed a refusal the policy does not
  support, which misapplies the control as surely as ignoring it.

Deferral is not optional. Narrowing the condition at `requested` cannot work,
because the check runs before the settlement it would need to consult, and
without a code "refused by policy" collapses into "failed at all". Then any
tool error would hide a control that was really ignored.

### `emulated` means the endpoint enforces the filter

For `run.tool_selection`, `native` means the harness constrains the model
before it acts. `emulated` means the endpoint enforces the filter itself, around
a harness that does not. It either elects only permitted tools or refuses
excluded calls and settles them `refused_by_policy`. An endpoint that can do
none of these advertises `unavailable`.

`memory` stays `emulated`, with no descriptor or revision change. It runs no
model, and its election from the permitted set is the endpoint enforcing the
filter, not a harness constraint.

## Consequences

- **Validator.** The check leaves the `requested` branch and becomes a settler
  that arbitrates at every call settlement, `action.call.completed`,
  `action.call.failed` and `action.call.cancelled`, plus a sweep at the run
  terminal for excluded calls still unsettled.
- **Fixtures.** `semantic-invalid/controls-tool-choice-ignored.json` stays
  invalid, with its diagnostic moving to the settling envelope. It gains three
  siblings:
  - a positive trace where the excluded call settles `refused_by_policy`;
  - a semantic-invalid trace where it settles `failed` with another code;
  - a semantic-invalid trace where a permitted call claims `refused_by_policy`.
- **Spec.** `drafts/` states the settlement rule and the meaning of
  `emulated`, beside the text Decision 0022 left for the filter.
- **Claude adapter.** Once the rule lands, the adapter can advertise
  `run.tool_selection` `emulated`. It answers the gate for an excluded tool
  with a denial and projects the refusal as above. That is a separate change,
  against its ledger.
- **`adaptertest`.** The default submit request carries no controls, so an
  adapter test asserting trace validity silently drops the control it meant to
  test. That is what hid #122 from its own test. The default should carry the
  controls the adapter advertises, or the helper should refuse a run admitted
  with controls it did not see. That fix is separate, and #122 records it.

## Not decided

Whether a harness that can refuse only some tools (for example, MCP tools but
not built-ins) may advertise `emulated` for the subset it covers. Nothing in
the tree does that today.

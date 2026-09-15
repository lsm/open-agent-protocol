# Decision 0003: Graduating Staged Control Units

Status: proposed
Date: 2026-09-13
Protocol: `open-agent-protocol` version `0.1`
Profile: `open-agent-protocol.agent-control-core`
Extends: [Decision 0001](0001-agent-control-v0.1-executable-core.md) and
[Decision 0002](0002-admission-before-start.md) without amending either
Design: [Staged Units Graduation Plan](../drafts/staged-units-graduation.md)
Prompt: [issue #13](https://github.com/lsm/open-agent-protocol/issues/13)

## Context

The v0.1 executable core is complete end-to-end: the schema bundle and the
single validator, eight pinned native adapters with executable corpora plus
the memory reference adapter, `oap serve`,
the public `serve` package, the Go and TypeScript clients, and the stdio
frontend in flight. Decision 0001 froze the smallest executable subset and
Decision 0002 graduated queued admission into it on the strength of
[protocol feedback PF-1](../research/protocol-feedback-2026-09.md).

The drafts already answer what the protocol contains next. The
[core draft](../drafts/agent-control-core.md) names the richer controls, the
[profile draft](../drafts/agent-control-profile.md) stages them with feature
gates, the [conformance draft](../drafts/conformance.md) makes them additive
units, and [`examples/tool-source.json`](../examples/tool-source.json) shows
the tool-source shape. Several of the staged controls are already on the wire
(`model_id`, `instructions`, `tool_choice`, `output_schema`,
`allow_degraded_features` on `session.message.submit.request`; `queue`,
`steer`, `btw` in the delivery enums; `steered` and `side_started` in the
admission enum) but none of them is executable under an advertised
capability: Codex applies `model_id` to `turn/start` and Makai to
`agent_message.model_ref`, both natively per run and both without
advertising model selection, and every executable adapter rejects
`instructions`, `tool_choice`, and `output_schema` before admission.
`allow_degraded_features` is weaker still: only the Codex adapter rejects a
request carrying it (`adapter/codex/appserver/session.go:102`), and the
others accept and ignore the member, so the opt-in it expresses is today
neither honoured nor refused.

What is undecided is the order and the discipline by which those units become
executable across protocol, validator, adapters, serve, and clients. This
decision fixes that discipline. It graduates no unit itself: each unit gets
its own decision on the Decision 0002 pattern, and the companion plan carries
the per-unit designs those decisions will freeze.

## Decisions

### One gate for every unit

A staged unit enters the executable set only when all of the following hold,
in this order:

1. **Reference execution.** The memory reference adapter implements the unit
   deterministically. The unit's wire shape is then executable, not prose.
2. **Validator rules and fixtures.** The stateful validator enforces the
   unit's invariants, `fixtures/manifest.json` lists positive and negative
   fixtures under a new conformance-unit name, and every existing fixture
   still validates unchanged.
3. **Native evidence.** At least one native adapter executes the unit through
   its production codec and reducer, with a corpus case pinned to the
   adapter's existing ledger commit and a live gate where the adapter has one.
   The adapter's capability descriptor advertises the unit at the level the
   evidence supports; nothing advertised without evidence, nothing applied
   without being advertised.
4. **Decision record.** A decision document graduates the unit, citing the
   fixtures and the corpus case, and records what stays deferred.

Steps 1 and 2 land together with the unit's schema and Go types. Step 3 may
land in the same change or the next. Step 4 lands last and flips the unit's
status in the drafts from staged to executable. A unit that stalls at step 3
stays staged: the memory adapter and the validator rules are kept, but no
draft claims the unit is executable until a native adapter proves it.

### Vertical cut through every surface

Each unit is specified and reviewed as one vertical slice through the
surfaces v0.1 already has, in this order: `schema/` and `protocol/`;
`validation/` and `fixtures/`; `adapter/memory.go` and `adaptertest`; the
native adapter and its corpus; `serve` (hub), `serve/servehttp`, and the
stdio frontend; the `client` package and `clients/ts`; the drafts and README.
A unit whose slice would leave one of those surfaces unable to express it is
not finished. The plan lists the concrete touch points per unit.

### Order by lifecycle risk, then by evidence

Units graduate in this order, each behind its own decision:

| Order | Unit (issue #13 label) | Planned decision | Why here |
| --- | --- | --- | --- |
| 1 | Run controls: `model_id` first, fail-closed discipline for all four (T1) | 0004 | Already on the wire; touches no run lifecycle; Codex and Makai apply `model_id` natively today without advertising it |
| 2 | Models catalog `models.list` (T5, first half) | 0005 | Pure control-plane query with the widest native evidence; makes `model_id` usable by a picker |
| 3 | Queue delivery: explicit `queue` requests and a second nonterminal run (T2) | 0006 | First change to the one-nonterminal-run invariant; Decision 0002 already made the queued shape canonical |
| 4 | Tool sources, attachment at open, control-layer tools including MCP (T3) | 0007 | Adds a third interaction kind; evidence spans Claude, ACP, Makai, Codex |
| 5 | Steer (T4) | 0008 | The only unit with a genuinely new run-semantics question; pre-design in the plan, evidence first |
| 6 | Auth state (T5, second half) | staged | Read-only listing only; graduates when a consumer needs the gate |

This reorders issue #13's T1 to T5 sequence in one respect: the models
catalog moves ahead of queue delivery because it is the lowest-risk unit with
the most native sources and it is what makes the first unit's `model_id`
selectable from a real control layer. The remainder keeps the issue's order.
Later units may start their step 1 and 2 work while an earlier unit is at
step 3; decisions are still accepted in this order so each amends a settled
invariant.

### Controls are per submit and fail closed

Run controls stay per-submit requests; there is no session-configuration
document, and the control layer re-sends the controls it wants on each
`session.message.submit.request` that admits a run (a steer joins a
started run whose admitted controls are authoritative, and carries none). A control an adapter cannot apply is
rejected before admission with a typed `unsupported_feature` error naming the
capability key, never dropped. A control applied through a native
session-level mutation is advertised `emulated` with a disclosed mode, and
the session state afterwards reflects the native truth (`current_model_id`
reports the new default). Such a mutation runs immediately before the run
it was requested for starts and never while another run is started, so a
queued submit's mutation waits for promotion. A control whose application cannot be confirmed
is `degraded` and requires the caller's `allow_degraded_features` opt-in,
else `capability_degraded` before admission. If a real consumer finds the
per-submit shape too chatty, that report is the evidence for a later additive
session-defaults unit, not a reason to redesign now.

### Where the MCP client lives

Both placements from issue #13 are legal and both are adopted, in sequence.
Adapter-side passthrough comes first: the harness runs its own MCP client and
OAP describes the source, attaches it at session open, and observes its tool
calls. This is what ACP, Claude Code, and Codex already do natively. A
serve-side connector comes second and is not a protocol feature: once
control-layer-provided tools are executable, the hub can host an MCP client as
one more execution owner and provision its tools into sessions on adapters
with no native MCP support of their own. That is a widening, not a
universal: the hub cannot execute a tool the adapter will never call, so
provisioning is limited to adapters that accept
`session.open.request.tools`, emit calls for tools they do not own, and
accept results through the reverse resolution channel. Adapters lacking
that refuse `action.tools.provide` under the ordinary gate — DeepSeek has no
reverse interaction channel (`research/deepseek-harness-47f9438-mapping.md:327`)
and Pi owns tool execution with interaction extensions disabled
(`research/pi-v0.85.1-mapping.md:250-253`). The wire is identical in
both placements; only `execution_owner` differs.

### Skills stay out of the protocol

Skills remain adapter passthrough through registry configuration and the
environment allowlist. The graduation rule for shared primitives applies
unchanged: a wire-level skills surface is designed only when two or more
independent implementations need one.

### No version bump

Every unit is additive under the layered draft's compatibility rules: new
optional fields, new envelope types added to the envelope `oneOf`, new
capability keys, new typed error codes, new validator diagnostics, and new
conformance units. `version` stays `0.1` and the profile identifier stays
`open-agent-protocol.agent-control-core`; claims grow by unit
(`+run-controls`, `+models`, `+queue`, `+tool-sources`, `+control-tools`,
`+steer`). No existing field's schema narrows: the typed `tool_choice`
shape is enforced by the validator's unit rules and by adapters over the
unchanged permissive schema, and the models payloads join the existing
control-plane schema file so the bundle inventory is unchanged.

Additive is a property of the wire, not of the closed v0.1 schema bundle:
its payload objects are `additionalProperties: false` and the envelope
`oneOf` is fixed, so a validator compiled from an older bundle rejects a
gated addition. The phase therefore starts with a tolerance step, before
the first unit, that gives the Go client's dev-mode validation and any
validation of a live endpoint a tolerant compile of the same bundle
(unknown members ignored, unknown envelope types checked against the
common fields only) while fixture validation stays strict. The plan's
schema-evolution section specifies it; no unit graduates before it lands.

### First consumer

The first real consumer is an in-process embedding of `serve.Hub` in the
HyperNeo-style control layer, with the stdio frontend as the second binding
and `oap serve` plus the TypeScript client as the demo path. Unit order above
already weighs this: an embedding host needs model selection and a catalog
before it needs queueing, and needs queueing before it needs steer.

## Consequences

- Each of the next five decisions has a fixed shape: context from the ledgers,
  the unit's invariants, the fixtures and corpus case that prove them, and an
  explicit list of what the unit does not admit.
- Adapters that today apply a staged control without advertising it (Codex
  applies `model_id` to `turn/start`, Makai to `agent_message.model_ref`)
  come into compliance with the first unit: advertise it or reject it.
- The validator grows one unit at a time; no fixture in the current manifest
  changes meaning.
- The stdio frontend, the daemon, and both clients gain the same operations in
  the same change as the hub, so no binding lags the protocol.
- Decision 0001's deferrals not named in the order above stand: `btw` and side
  runs, subagent and background lifecycles, artifacts, checkpoints, rewind,
  branch, compaction, durable admission, cross-process replay, orphan
  terminals, retained-interaction reassociation, and interaction deadlines.

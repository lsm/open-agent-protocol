# P0 Protocol Gaps From Harness Interoperability

Status: research findings requiring resolution
Date: 2026-09-01
Related study: [Harness Interoperability Study](harness-interoperability.md)

This document isolates the protocol gaps that block a credible first adapter
for HyperNeo, Makai, and pi. These are not speculative completeness features.
Each gap was exposed by trying to map an existing harness to
`open-agent-protocol.agent-control-core`.

P0 means the protocol decision and at least one fixture must exist before OAP
is used as the internal backend boundary for HyperNeo. It does not mean every
future profile must be designed first.

## Summary

| Gap | Why it blocks adoption | Required outcome |
| --- | --- | --- |
| P0.1 Agent-control event vocabulary | Core and richer drafts name the same visible stream differently. | One canonical agent-control vocabulary and explicit lower-profile projection rules. |
| P0.2 Adapter identity | Existing harnesses do not consistently expose stable session, run, turn, message, and tool-call IDs. | Rules for generating, preserving, scoping, recovering, and idempotently creating OAP identities. |
| P0.3 Capability scope and late discovery | Real capabilities may vary by session, model, configuration, or become known only after native initialization. | A minimal core descriptor with scope, authority, provisional state, refresh, and degradation rules. |
| P0.4 Admission and delivery mapping | HyperNeo, Makai, and pi admit work using different busy-session semantics. | Deterministic mapping to `auto`, `start`, `queue`, `steer`, and `btw`, including run-ID behavior. |
| P0.5 Terminal outcome normalization | Native streams do not always provide a clean completed, failed, or cancelled event. | A conservative terminal classification algorithm with typed errors and synthesis disclosure. |
| P0.6 Normative adapter fixtures | Prose mappings cannot prove ordering, correlation, recovery, or degradation. | Native-input-to-OAP fixtures for all three harnesses with conformance assertions. |

## P0.1 Canonical Agent-Control Event Vocabulary

### Evidence

The minimum core table and conformance draft use `content.delta`. The richer
agent-control draft and its fixture use `model.content.delta`. The richer draft
also exposes model-stream events that describe a lower model-IO boundary, while
the core promises a control-facing stream.

The harnesses expose three different source shapes:

- HyperNeo forwards Claude Agent SDK message variants and daemon events.
- Makai distinguishes provider message events inside agent events.
- pi emits agent, turn, message, and tool-execution events.

A control layer needs presentation-relevant agent output. It should not need to
know whether that output originated from a provider stream, a buffered SDK
message, or an agent-generated response.

### Gap

OAP does not yet state which event names belong at the agent-control boundary
and which belong only at the model-IO boundary. Two conforming implementations
could emit different event names for the same assistant text.

### Proposed resolution

Use boundary-specific vocabulary:

- Agent-control uses `content.delta` for assistant-visible live content.
- Future model-IO uses `model.content.delta` for model-provider output between
  an agent loop and a model provider.
- An agent loop or adapter projects model output into `content.delta`; the
  projection may merge, suppress, summarize, or relabel lower-level parts when
  required by policy.
- Agent-control tool lifecycle uses `action.call.*`; raw provider tool-call
  fragments remain model-IO events until the loop selects an action.
- Core requires `run.started`, meaningful `run.status.updated` events, and one
  terminal `run.*` event. Turn events remain optional until their core value is
  demonstrated by fixtures.

The richer agent-control draft and examples should be changed to this
vocabulary or explicitly marked as lower-boundary traces.

### Acceptance criteria

- Every minimum-core event has one canonical type name.
- A core consumer can render assistant text without subscribing to model-IO.
- The specification defines whether reasoning is a `content.delta` part,
  unavailable, or degraded; it is never inferred from an implementation name.
- Tool-call proposal and selected action execution are not conflated.
- Conformance rejects `model.content.delta` as a substitute for required core
  `content.delta` unless a future compatibility rule explicitly permits it.

## P0.2 Adapter Identity And Correlation

### Evidence

HyperNeo has daemon session IDs, Claude SDK session IDs, persisted message IDs,
and generated queue/job IDs. A Claude SDK stream does not necessarily provide a
stable native ID for every OAP run or turn.

Makai envelopes provide session or stream identity, message identity,
correlation, and sequence, but its events do not consistently expose the OAP
session/run/turn hierarchy.

pi has durable `AgentSession` state and message identity, but its low-level
agent events do not require explicit run and turn IDs. Its JSONL request ID is
request correlation, not run identity.

### Gap

The drafts say IDs are opaque and stable, but do not tell an adapter:

- when it may reuse a native ID;
- when it must allocate an OAP ID;
- how long a mapping must survive;
- whether queued work receives a run ID before execution;
- how steering identifies the existing run it modifies;
- how duplicate requests avoid duplicate sessions, messages, or runs;
- how a caller identifies a retry when the original response was lost;
- where native IDs may be retained for diagnostics without becoming portable
  identity.

### Proposed resolution

Define identity by semantic scope:

- `session_id` identifies one control-visible session and remains stable across
  reconnect and reopen while that session is recoverable.
- `submission_id` identifies one accepted message-submission operation and is
  stable across retries recognized as the same submission.
- `run_id` identifies one admitted execution lifecycle. It is allocated no
  later than acceptance when admission creates or reserves a run.
- `turn_id` identifies one loop step when the implementation exposes turns. It
  is optional unless an event type requires it.
- `message_id` identifies one canonical transcript message, not a stream frame.
- `tool_call_id` identifies one selected action invocation across requested,
  started, progress, and terminal events.
- Envelope `id` identifies one envelope. `in_reply_to` correlates a response to
  a request and must never substitute for a domain ID.

Side-effecting requests need caller-supplied idempotency independent from
envelope correlation. At minimum, a session-creating `session.open.request` and
`session.message.submit.request` should accept an `idempotency_key` with these
semantics:

- the key is scoped to endpoint identity, operation type, and `session_id` when
  the operation targets an existing session;
- a retry may use a new envelope `id` but repeats the same `idempotency_key`;
- the same key and semantically equivalent request returns the original
  acceptance outcome, `submission_id`, message IDs, and run ID without
  repeating work;
- the same key with a different semantic request fails with a typed
  `idempotency_conflict` error;
- the endpoint advertises or defines the retention window in which it can
  guarantee deduplication;
- without an idempotency key, a caller must assume that retrying after an
  unknown outcome can duplicate work.

The response's `session_id` or `submission_id` remains the endpoint-assigned
identity for the accepted operation. It does not serve as the retry key because
the caller may not have received it. Reopening an existing `session_id` and
cancelling a known `run_id` should be inherently idempotent; the specification
must identify any other command whose safe retry requires a key.

An adapter may reuse a native ID only when its scope and stability satisfy the
OAP rule. Otherwise it allocates an OAP ID and keeps a private durable mapping
for every identity needed after reconnect. Native IDs may be exposed in a
namespaced extension or diagnostic record, but peers must not depend on them.

For delivery:

- `start` and `btw` create a new `run_id`.
- `queue` reserves a new `run_id` if the endpoint can guarantee the queued
  lifecycle; otherwise acceptance must clearly identify a submission that has
  not yet become a run.
- `steer` targets the active `run_id` and does not create a second main run.

The queue rule requires a final decision in the core because the current draft
assumes a `run_id` is returned for queued work.

### Acceptance criteria

- Replaying a correlated response or reconnecting does not create new domain
  IDs.
- Retrying a submit with the same idempotency key does not create a second
  message or run, even when the first response was lost.
- Retrying session creation with the same idempotency key returns the original
  `session_id` rather than creating a second session.
- Reusing an idempotency key with different input fails deterministically.
- Every `run.started` and terminal run event carries the same `run_id`.
- Every started action has exactly one stable `tool_call_id` and one terminal
  action event.
- Native IDs can be inspected without being mistaken for portable OAP IDs.
- Fixtures cover generated IDs, reused native IDs, lost session-open and submit
  responses, idempotency conflict, queueing, steering, and reconnect recovery.

## P0.3 Capability Scope, Authority, And Late Discovery

### Evidence

HyperNeo combines configured daemon policy, provider configuration, and Claude
Agent SDK `system:init` data. The effective tool catalog and some SDK details
may become known only after a query initializes. Capabilities can vary by
session, selected model, permissions, or workspace configuration.

Makai has native provider and tool boundaries but no OAP capability handshake.
Available behavior can depend on the selected provider and the tools supplied
for a run.

pi exposes steering, follow-up, model changes, tools, and session operations,
but no revisioned OAP descriptor. Available commands also differ between its
low-level `Agent`, `AgentSession`, and JSONL RPC surfaces.

The current examples present one broad endpoint snapshot containing model,
action, agent-control, and control-plane features. This is useful for the full
layer model but too ambiguous for a minimum adapter claim: a Claude SDK adapter
may implement agent-control while having no direct model-IO or action-tool
boundary to advertise.

### Gap

The protocol lacks precise answers for:

- which capability keys are mandatory in a core descriptor;
- whether a capability describes the endpoint, session, model, or run;
- how an adapter reports `unknown until native initialization`;
- how late-discovered tools or delivery behavior update the snapshot;
- whether hidden lower layers should be absent or marked unavailable;
- how catalogs participate in revision identity;
- how policy restrictions differ from underlying technical support.

Without these rules, UIs will return to runtime-name checks or adapters will
overclaim support.

### Proposed resolution

Define a small agent-control-core capability descriptor first:

- endpoint identity and OAP profile versions;
- required feature records for session open/state, message submission,
  streaming, status, cancellation, and `auto` delivery;
- optional delivery, content, persistence, model-selection, tool, permission,
  and user-input feature records;
- capability scope: endpoint by default, with explicit session or model
  qualifiers where effective behavior varies;
- support level: `native`, `emulated`, `degraded`, or `unavailable`;
- optional `provisional: true` when the result may be narrowed after native
  initialization;
- degradation records describing information loss, emulation, buffering, or
  policy restriction;
- an opaque revision covering the complete effective descriptor and embedded
  catalogs.

Late discovery should follow the existing refresh mechanism:

1. The adapter returns the effective pre-run snapshot produced by the authority
   rules below.
2. Unknown late-bound behavior is provisional or unavailable, never silently
   assumed native.
3. Native initialization changes effective capabilities.
4. The adapter assigns a new revision and emits `capabilities.updated` when it
   advertised updates.
5. The control layer fetches the new snapshot and recomputes gates.

Effective capabilities are the intersection of these authorities, in order:

1. underlying technical support from the native harness, provider, model, and
   configured resources;
2. semantic fidelity the adapter can preserve or emulate;
3. control and deployment policy for the current endpoint and scope.

Later stages may narrow support but may not elevate behavior that an earlier
stage cannot provide. Policy is the final veto: a technically available feature
forbidden by policy is advertised as unavailable with a policy reason. Adapter
transformation may change technically native support to `emulated` or
`degraded`. `native` is valid only when the underlying behavior exists, the
adapter preserves its OAP semantics, and policy permits it. Effective catalogs
are filtered by the same intersection.

When source reports conflict and the adapter cannot compute a deterministic
intersection, it advertises the feature as provisional and degraded or
unavailable; it must not select the most permissive report. Capability and
degradation records may retain separate technical, adapter, and policy reasons
for diagnostics, while the control layer gates only on the effective result.

An adapter claiming only agent-control should omit unrelated lower-profile
sections or explicitly state that those boundaries are not exposed. It should
not describe hidden Claude SDK internals as degraded model-IO conformance.

### Acceptance criteria

- A minimal core descriptor can be implemented without model-IO or action-tool
  conformance.
- HyperNeo can represent tools learned from `system:init` without pretending
  they were known at connection time.
- Capability scope distinguishes endpoint support from session/model-effective
  support.
- Configured policy can remove but never invent underlying technical support.
- Conflicting capability sources resolve to a deterministic effective result;
  uncertainty never defaults to the most permissive result.
- A descriptor revision changes whenever a gate or embedded effective catalog
  changes.
- The control layer can choose controls using only the descriptor and state,
  with no HyperNeo, Makai, pi, or provider name checks.

## P0.4 Admission And Delivery Mapping

### Evidence

HyperNeo accepts `immediate` or `defer`. Its daemon resolves the actual behavior
using authoritative busy state, manual mode, persistence, and queue policy.
`immediate` therefore does not always mean a fresh run starts immediately.

pi separates `prompt`, `steer`, and `follow_up`. While streaming, steering and
follow-up use different queues and safe boundaries.

Makai has run and stream entry points but does not yet expose the same
control-facing busy-session admission contract.

### Gap

OAP defines delivery names but not enough adapter mapping rules for:

- acceptance versus execution start;
- busy-state races between state display and admission;
- queue ownership and durability;
- steering an active run versus creating a new run;
- reporting an emulated delivery mode;
- what happens when `auto` resolves to an optional mode the caller did not
  explicitly request;
- which IDs and events follow each outcome.

### Proposed resolution

Keep `auto` as the required stale-state-safe request. The authoritative
admission layer resolves it and returns:

- `requested_delivery`;
- concrete `effective_delivery`;
- `admission` such as `started`, `queued`, `steered`, or `side_started`;
- `submission_id`;
- affected or reserved `run_id` according to the identity rules;
- optional `delivery_resolution` suitable for diagnostics;
- any degradation applied during resolution.

Explicit `queue`, `steer`, or `btw` requests must either preserve that semantic
meaning or fail with a typed unsupported/degraded-feature response. They must
not silently fall back to another mode.

For `auto`, endpoint policy may choose any advertised effective mode. If it
chooses emulated or degraded behavior, the capability snapshot and response
must disclose that fact.

Initial mappings should be:

| Harness behavior | OAP request/effective behavior |
| --- | --- |
| HyperNeo default `immediate` policy | request `auto`; adapter/control resolves authoritatively |
| HyperNeo explicit `defer` | request `queue` when queue is advertised |
| pi idle `prompt` | `auto` -> `start` |
| pi `steer` | explicit `steer`, targeting active run |
| pi `follow_up` | explicit `queue` |
| Makai new run | `auto` -> `start` |
| Unsupported side question | `btw` unavailable and typed rejection |

### Acceptance criteria

- A stale UI cannot cause the control layer to mislabel a resolved delivery.
- Submission responses acknowledge admission and never imply completion.
- Explicit modes never silently change meaning.
- Queue and steer fixtures define their run-ID and event behavior.
- HyperNeo and pi can preserve their current user-visible delivery semantics.

## P0.5 Terminal Outcome Normalization

### Evidence

Claude Agent SDK streams provide result, error, interruption, and process-level
signals, but their exact ordering and completeness can differ by failure path.
HyperNeo also has daemon cancellation, process exit, and persisted state that
may be more authoritative than the final SDK message.

Makai distinguishes several lifecycle and error events, but not every path is
already expressed as one OAP terminal run event.

pi emits `agent_end`; cancellation or failure may need to be inferred from the
final agent state, abort signal, or error message rather than a distinct native
terminal event.

### Gap

Core requires exactly one of `run.completed`, `run.failed`, or `run.cancelled`,
but does not define how an adapter classifies incomplete or conflicting native
signals. A permissive adapter may report success after a process failure; a
strict adapter may emit two terminal events.

### Proposed resolution

For each adapter, define the authoritative terminal sources for a run. These
may include the native semantic stream, an adapter task, and a child process or
transport. The adapter collects terminal candidates but does not emit an OAP
terminal event until a finalization barrier is satisfied: every required
source has settled, or the native contract establishes that a source can no
longer change the outcome.

At that barrier, classify the collected signals with this precedence:

1. An authoritative accepted cancellation that terminates work maps to
   `run.cancelled`.
2. An authoritative native failure, protocol failure, adapter exception, or
   unexpected process termination maps to `run.failed`.
3. An authoritative successful native result maps to `run.completed`.
4. End-of-stream without an authoritative outcome maps to `run.failed` with a
   typed `terminal_outcome_unknown` adapter error.

The adapter then uses an atomic per-run terminal guard to emit the selected
outcome exactly once. Signals arriving afterward from sources that the adapter
contract declared non-authoritative become diagnostics, not additional terminal
events. A success candidate must never cross the finalization barrier while a
required process or transport can still report failure.

If a required source does not settle, a declared settlement timeout starts
containment; it does not itself produce a terminal event. The adapter must:

1. stop admitting conflicting work in the affected session or execution scope;
2. request cancellation and terminate or isolate the native source using a
   mechanism that prevents further effects in that scope;
3. establish quiescence through process exit, transport closure plus an
   authoritative remote status, or another adapter-specific guarantee;
4. only then emit `run.failed` with a typed `terminal_settlement_timeout` error.

If the adapter cannot establish quiescence, the run remains nonterminal and the
session remains blocked in recovery/error state. It must not report completion
or admit work that assumes the prior run has stopped. This exposes a necessary
core decision: either conforming adapters must guarantee eventual quiescence,
or the protocol needs an explicit orphaned/abandoned outcome distinct from
ordinary terminal completion. A timeout cannot manufacture that guarantee.

Synthesized terminal events should include or reference:

- a typed error for failures;
- the native signal category used for classification;
- whether the outcome was synthesized;
- degradation when cancellation, usage, final response, or stop reason was
  inferred or unavailable.

`run.completed` must not be used merely because a stream ended. Cancellation
requested but not confirmed remains `cancelling` until a terminal signal or a
defined adapter timeout produces a typed failure.

### Acceptance criteria

- Every `run.started` receives exactly one terminal event.
- Abrupt EOF and adapter exceptions cannot become successful completion.
- Cancellation races have a deterministic outcome.
- A success candidate is not emitted before all required terminal sources
  settle.
- A settlement timeout cannot emit a terminal event until execution quiescence
  is established.
- Conflicting late native events cannot produce a second terminal event.
- Fixtures cover success, native failure, confirmed cancellation, abrupt EOF,
  and cancellation/failure races.

## P0.6 Normative Adapter Mapping Fixtures

### Evidence

The repository has OAP envelope examples, but no fixture starts with a real
harness trace and proves its mapping into OAP. The unresolved issues above are
primarily temporal: admission response versus stream start, capability refresh,
ID allocation, event ordering, cancellation races, and terminal classification.

### Gap

Protocol-only examples can be internally consistent while remaining impossible
to implement faithfully over a real SDK or CLI. Prose also cannot be executed
as conformance evidence.

### Proposed resolution

Add one fixture family per harness. Each case should contain:

- a simplified, license-compatible native input trace or adapter stimulus;
- adapter configuration and initial capability state;
- the expected ordered OAP envelopes;
- ID mapping records or symbolic ID constraints;
- expected capability revisions and degradation records;
- assertions for correlation, ordering, lifecycle, and terminal uniqueness;
- notes identifying information intentionally omitted from OAP.

Minimum cases:

**HyperNeo / Claude Agent SDK**

- pre-run capabilities followed by `system:init` capability refresh;
- configured policy denying a capability reported by `system:init`;
- idle message submission and streamed text;
- tool call lifecycle when the SDK exposes tools;
- explicit defer/queue while another run is active;
- cancellation, abrupt SDK/process failure, and settlement-timeout containment.

**Makai**

- native agent run with provider text stream;
- two turns with tool execution between them;
- opaque native payload transformed into structured OAP content;
- provider or tool failure mapped to one run terminal outcome.

**pi**

- idle `prompt`;
- `steer` during an active run;
- `follow_up` queue;
- abort and terminal classification;
- JSONL request/response correlation independent from run identity.

Fixtures should be normative for the adapter mapping rules they exercise, but
not normative for the harness's private API. They should use reduced traces and
neutral field names where copying upstream source formats would create
licensing or maintenance problems.

### Acceptance criteria

- Every P0 rule is exercised by at least one fixture.
- All three harnesses can produce the same minimum OAP lifecycle.
- Fixture checks are transport independent.
- An implementation can fail a mapping assertion without requiring the actual
  third-party SDK at conformance-test time.
- Native API changes require updating only the adapter input side when OAP
  semantics remain unchanged.

## Resolution Order

The gaps should be resolved in this order:

1. P0.1 vocabulary, because every fixture depends on stable event names.
2. P0.2 identity, because admission and lifecycle assertions depend on IDs.
3. P0.3 capabilities, including late discovery and scope.
4. P0.4 delivery, using the identity and capability rules.
5. P0.5 terminal classification.
6. P0.6 fixtures, written incrementally alongside each decision and completed
   as the acceptance gate for this stage.

The protocol should not begin model-IO, action-tool, registry, or broad
HyperNeo extension design until these agent-control adapter gaps have executable
examples. Those later profiles can proceed once the first vertical slice no
longer depends on implementation-name checks or raw SDK events.

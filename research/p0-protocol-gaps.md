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
| P0.5 Terminal outcome normalization | Native streams do not always provide a clean completed, failed, cancelled, or provably quiescent outcome. | A conservative terminal classifier, including explicit orphaning, typed errors, and synthesis disclosure. |
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
- Core requires `run.started` for execution that begins, meaningful
  `run.status.updated` events, and one terminal `run.*` outcome for every
  admitted run. Turn events remain optional until their core value is
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

- the key is scoped to endpoint identity, stable `caller_identity`, operation
  type, and `session_id` when the operation targets an existing session;
  `caller_identity` is the authenticated principal when authentication exists,
  otherwise it is a binding-established identity for the local caller or trust
  domain that remains stable across reconnect and process restart; tenant or
  organization identity is an additional dimension when applicable, never a
  replacement for caller identity;
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

An unauthenticated binding may use a stable local installation, OS principal,
or single-tenant deployment identity as `caller_identity`; a transient
connection ID is insufficient. If the binding cannot distinguish mutually
untrusted callers with stable identities, the endpoint must isolate them into
separate idempotency namespaces or decline the idempotency conformance claim. A
single trusted in-process or stdio caller may use the stable deployment trust
domain as its identity.

After authentication, structural validation, and current authorization for the
endpoint, operation, and target session, idempotency lookup precedes
capability-revision validation. Authorization failure returns the normal typed
denial without revealing whether a matching key or stored outcome exists. The
lookup itself is internal and non-disclosing. If it finds a record, the endpoint
must reauthorize every resource referenced by the stored request or outcome
before returning replay data or an idempotency conflict. This includes a
`session_id` allocated by an earlier session-creating request even though that ID
was absent from the original request. Stored-resource authorization failure uses
the same non-disclosing denial. Only then does the endpoint apply these rules:

1. If the key identifies a completed or accepted semantically equivalent
   operation, the endpoint returns the stored outcome and original admitted
   capability revision. This is replay, not new admission, so a newer current
   revision does not turn it into `stale_capabilities`.
2. If the key exists for a semantically different operation, the endpoint
   returns `idempotency_conflict`.
3. If the key is unknown or its guaranteed retention has expired, the endpoint
   applies normal current capability-revision validation before admitting work.
4. On new admission, the endpoint atomically commits the key, canonical request,
   admitted revision, allocated domain IDs, response state, and the durable
   session, queue, or runnable admission state. When an external effect cannot
   share that transaction, the commit includes a resumable pending outbox intent
   keyed by the same operation instead of claiming a completed effect.

An acceptance response is exposed only after that commit. Recovery resumes a
stored pending intent using the original domain IDs and never creates a second
intent; replay returns the stored pending or accepted state. A crash before the
commit leaves no replayable acceptance, while a crash after it leaves enough
durable state to finish or report the admitted lifecycle. Implementations may
choose a transaction, write-ahead log, or equivalent mechanism, but the protocol
guarantee is that replay never reports work as admitted when neither its durable
state nor a resumable intent exists.

Envelope IDs, timestamps, and a retried `capability_revision` are excluded from
semantic-equivalence comparison. The original operation's semantic payload and
idempotency scope are not.

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
- `queue` reserves a new `run_id` and accepts responsibility for its complete
  lifecycle. Admission is followed by queued run state. If the item is removed,
  cancelled, or fails before execution, the endpoint emits `run.cancelled` or
  `run.failed` with `started: false`; it does not invent `run.started`.
- `steer` does not create a run. Its request may carry `target_run_id`; when
  omitted, the target is the running primary run, never a side run. A supplied
  target must identify a running, steerable run in the same session's
  `active_runs` collection or the endpoint rejects before admission with typed
  `invalid_steer_target`. The `+steer` capability states whether explicit
  targeting is supported.

An endpoint that cannot guarantee this pre-start terminal lifecycle must not
reserve a `run_id`. It returns only `submission_id`, and the submission does not
become a run until execution is admitted later. OAP should choose one behavior
for the standard queue unit; the proposed default is reservation plus a complete
pre-start terminal lifecycle because it gives control layers stable queue
identity.

### Acceptance criteria

- Replaying a correlated response or reconnecting does not create new domain
  IDs.
- Retrying a submit with the same idempotency key does not create a second
  message or run, even when the first response was lost.
- Retrying session creation with the same idempotency key returns the original
  `session_id` rather than creating a second session.
- Reusing an idempotency key with different input fails deterministically.
- The same key used by different authenticated principals cannot reveal or
  collide with another principal's stored outcome.
- The same key used by different unauthenticated binding caller identities also
  cannot collide or reveal outcomes.
- Revoked endpoint or session authorization prevents replay and does not reveal
  whether a stored idempotency outcome exists.
- Replaying resource creation reauthorizes the stored created resource before
  disclosing its ID or any conflict information.
- Crash-point fixtures prove that idempotency replay cannot return acceptance
  without durable admission state or one resumable intent using the original
  IDs.
- A recognized replay returns its original outcome after capabilities advance;
  an unknown key still undergoes normal stale-revision validation.
- Every `run.started` and terminal run event carries the same `run_id`.
- Every reserved queued run receives one terminal event even when it never
  emits `run.started`.
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
- required feature records for `protocol.initialize`, `capabilities`,
  `session.open`, `session.state`, `session.message.submit`,
  `session.message.delivery.auto`, `run.streaming`, `run.status`, and
  `run.cancel`;
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

Capability evaluation has two distinct steps: field-wise context selection
within each authority source, then restrictive composition across sources. A
qualifier set may contain `session_id`, `model_id`, or both. For each field, take
the matching records from one source that explicitly define that field, then
select those whose qualifier sets are maximal by set inclusion. A combined
session/model record shadows broader values only for fields it defines; omitted
fields continue to inherit from the most-specific broader records that define
them. If matching session-only and model-only records both define a field and
are maximal because no combined value exists, both values apply and use that
field's composition rule.

This selection rule lets a more-specific technical record enable a capability
whose unqualified technical default is unavailable, without making unmatched
contexts permissive. It is a contextual value, not a policy override. After
selection, independently selected technical, adapter, binding, and policy
records compose restrictively: support uses the most restrictive level
(`unavailable`, then `degraded`, then `emulated`, then `native`); degradation
reasons accumulate; boolean permissions intersect; limits use the most
restrictive bound; and catalogs contain only entries allowed by every selected
record, with their constraints intersected. An empty or contradictory
intersection is `unavailable`, and policy remains the final veto.

After field-wise inheritance, optional constraint omission is an identity value,
not an empty value: an absent permission adds no allow or deny decision, an
absent limit adds no bound, an absent catalog adds no filtering, and absent
degradation reasons add none.
Explicit `false`, zero, an empty catalog, or another denying value remains a real
restriction. If adapter, binding, or policy has no matching record, that source
also contributes the identity value. Technical support is different: every
effective feature requires a selected technical-basis record; no matching
technical record means `unavailable`, preventing absence from inventing support.
The technical basis may describe lower-level primitives that an adapter uses to
provide an honestly `emulated` semantic feature.

`provisional` follows the same field-wise rule and supports explicit `false`. A
more-specific explicit `false` shadows a broader provisional `true` from the
same source; omission inherits it. Among equally maximal defining records within
a source, and then across independent sources, the effective value is
provisional if any selected value is `true`. Thus an exact combined record can
resolve a broad unknown, while an unresolved model or session restriction cannot
be accidentally cleared by omission or another authority. The endpoint
publishes the resulting effective record and covers it with the capability
revision, so a peer never has to reproduce selection or private adapter policy
to decide admission.

Late discovery should follow the existing refresh mechanism:

1. The adapter returns the effective pre-run snapshot produced by the authority
   rules below.
2. Unknown late-bound behavior is provisional or unavailable, never silently
   assumed native.
3. Native initialization changes effective capabilities.
4. The adapter assigns a new revision and emits `capabilities.updated` when it
   advertised updates.
5. The control layer fetches the new snapshot and recomputes gates.

`provisional` means discoverable but not yet safe to depend on. Before first
admission, the endpoint identifies every capability the request depends on,
including selected model, tools, delivery, content, and required core lifecycle
features. If any dependency is provisional, the endpoint must do one of these
without releasing the user prompt or permitting tool/model side effects:

1. perform a native bootstrap probe, resolve the provisional records, publish a
   new capability revision, and reapply normal revision validation; or
2. reject with a typed `capability_provisional` error naming the unresolved
   features and indicating that capability refresh or session initialization is
   required.

If a bootstrap probe changes the revision pinned by the request, the request is
rejected as `stale_capabilities` before admission. The caller refreshes and may
retry with the same idempotency key; because no admission occurred, this is a
new attempt rather than replay of an accepted operation.

An adapter whose native SDK emits initialization only after a query object is
created may still conform if it withholds user input from the native message
source until initialization settles. If it cannot prevent execution while
probing, it cannot use that query as a capability bootstrap and must reject
requests that depend on unresolved records.

Effective capabilities are computed through these authorities, in order:

1. underlying technical primitives from the native harness, provider, model,
   and configured resources;
2. semantic behavior the adapter preserves or implements over those primitives;
3. fidelity the selected binding can preserve, including streaming, progress,
   ordering, cancellation, reconnect, and backpressure behavior;
4. control and deployment policy for the current endpoint and scope.

The adapter may add an OAP feature that the native harness does not name when
lower-level primitives are sufficient to implement equivalent semantics. Such a
feature is `emulated`, not `native`, and its record identifies the implementing
adapter behavior and relevant limitations. For example, a session wrapper may
provide durable queueing over a native loop that only accepts immediate work.
An adapter must not emulate a feature when the available primitives cannot meet
its required safety and lifecycle semantics.

After adapter composition, the binding may only preserve or narrow effective
support. Buffered streams, missing bidirectional cancellation, lost progress,
or insufficient reconnect semantics must change the affected feature to
`degraded` or `unavailable`. Policy is the final veto: a technically available
or honestly emulated feature forbidden by policy is advertised as unavailable
with a policy reason.

`native` is valid only when the underlying behavior exists and both adapter and
binding preserve its OAP semantics. Effective catalogs are filtered by the same
composition and policy rules.

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
- No request is admitted while a capability it depends on remains provisional.
- Capability scope distinguishes endpoint support from session/model-effective
  support.
- Overlapping endpoint, session, model, and combined session/model records
  compose to the same most-restrictive effective result for every peer.
- A context-specific record may safely enable support over an unavailable
  default within one authority source, while unmatched contexts remain
  unavailable and independent sources still narrow the result.
- A specific record that changes one field inherits every unrelated broader
  field from the same source, including denials and limits.
- Provisional status is selected and composed deterministically; a specific
  resolved record can shadow only broader defaults from the same source.
- Adapters may add honestly emulated features only when sufficient lower-level
  primitives preserve the required semantics.
- Binding limitations narrow affected capabilities before they are advertised.
- Configured policy can remove but never invent effective support.
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

The `+btw` and `+queue` units create multiple admitted nonterminal lifecycles, so
they also require recoverable canonical state. `session.state.response` and
`session.state.updated` must expose an `active_runs` collection containing every
running, side, and reserved queued run with at least `run_id`, relationship
(`primary` or `side`), `execution_scope_id`, and status (`queued` or a running
status). Queued records appear in deterministic execution order and include
`queue_position`; a side run may include `parent_run_id` for the primary run
whose environment and configuration it shares. Here `active_runs` means all
admitted nonterminal runs, not only those currently executing. The singular
`active_run_id` is insufficient for either optional unit; implementations with
only one nonterminal run may retain it as a core convenience field.

For `auto`, endpoint policy may resolve idle admission to `start` without a
separate `start` capability. `start` is the implicit concrete outcome of core
`auto`, not an explicit request mode. Resolution to optional `queue`, `steer`, or
`btw` behavior requires the corresponding advertised capability. If policy
chooses emulated behavior, the capability snapshot and response must disclose
that fact. It may choose behavior advertised as `degraded` only when the request
explicitly sets `allow_degraded_features` to permit it. Otherwise the endpoint
must reject with `capability_degraded` before admission rather than disclose the
degradation after work has started.

If the session is busy and policy has no advertised `queue`, `steer`, or `btw`
outcome that is otherwise supported, `auto` fails before admission with typed
`session_busy`. If such an outcome exists and is excluded only because it is
degraded and the request did not opt in, `capability_degraded` takes precedence.
Both errors occur before the endpoint allocates a submission or run, releases
input to the harness, or creates side effects. The caller may opt into disclosed
degradation or retry after observing a state change.

Initial mappings should be:

| Harness behavior | OAP request/effective behavior |
| --- | --- |
| HyperNeo default `immediate` policy | request `auto`; adapter/control resolves authoritatively |
| HyperNeo explicit `defer` | request `queue` when queue is advertised |
| pi idle `prompt` | `auto` -> `start` |
| pi `steer` | explicit `steer`, targeting the running primary run |
| pi `follow_up` | explicit `queue` |
| Makai new run | `auto` -> `start` |
| Unsupported side question | `btw` unavailable and typed rejection |

### Acceptance criteria

- A stale UI cannot cause the control layer to mislabel a resolved delivery.
- Submission responses acknowledge admission and never imply completion.
- Explicit modes never silently change meaning.
- Core `auto` may resolve to implicit `start`; every other concrete delivery
  outcome is capability-gated.
- Busy `auto` fails as `session_busy` when no busy outcome is otherwise
  supported; `capability_degraded` takes precedence when the only candidate is
  excluded solely by missing degradation opt-in.
- `auto` never admits degraded behavior without request opt-in.
- Queue and steer fixtures define their run-ID and event behavior.
- Steering defaults to the running primary run and validates any explicit
  `target_run_id` before admission.
- Reconnect state preserves every reserved queued run and its execution order.
- A queued run removed before start receives a terminal event without a false
  `run.started` event.
- Reconnect state for `+btw` lists both the primary run and every active side
  run.
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

Core currently requires exactly one of `run.completed`, `run.failed`, or
`run.cancelled`, but does not define how an adapter classifies incomplete or
conflicting native signals, or a source whose continued execution cannot be
ruled out. A permissive adapter may report success after a process failure; a
strict adapter may emit two terminal events or remain nonterminal forever.

### Proposed resolution

For each adapter, define the authoritative terminal sources for a run. These
may include the native semantic stream, an adapter task, and a child process or
transport. The adapter collects terminal candidates but does not emit an OAP
terminal event until a finalization barrier is satisfied: every required
source has settled, or the native contract establishes that a source can no
longer change the outcome. The barrier is also satisfied when the declared
containment policy is exhausted and the adapter atomically commits to an
orphaned outcome. This exception records that no authoritative outcome can be
known safely; it does not pretend the unsettled source has stopped.

At that barrier, classify the collected signals with this precedence:

1. An authoritative accepted cancellation that terminates work maps to
   `run.cancelled`.
2. An authoritative native failure, protocol failure, adapter exception, or
   unexpected process termination maps to `run.failed`.
3. An authoritative successful native result maps to `run.completed`.
4. End-of-stream without an authoritative outcome triggers containment. If the
   adapter establishes quiescence, it maps to `run.failed` with a typed
   `terminal_outcome_unknown` adapter error; otherwise it maps to
   `run.orphaned`.
5. Any other failure to establish quiescence after containment maps to
   `run.orphaned`.
   This is a terminal OAP observation outcome that explicitly means external
   execution or side effects may continue; it is not completion or ordinary
   failure.

The adapter then uses an atomic per-run terminal guard to emit the selected
outcome exactly once. Signals arriving afterward from sources that the adapter
contract declared non-authoritative become diagnostics, not additional terminal
events. A completed, failed, or cancelled candidate must never cross the
finalization barrier while a required process or transport can still change
that outcome; only the explicit orphaned outcome can close observation while
execution remains uncertain.

Before emitting the run terminal, the adapter must close every started child
lifecycle exposed at the agent-control boundary. In particular, each
`action.call.started` must receive exactly one `action.call.completed`,
`action.call.failed`, `action.call.cancelled`, or `action.call.orphaned` event
using the original `tool_call_id`. The orphaned outcome is valid only when the
adapter cannot establish that the child action has stopped. Confirmed parent
cancellation normally synthesizes
`action.call.cancelled`; process failure, lost native state, or forced
containment normally synthesizes `action.call.failed` with a typed
`parent_run_terminated` error. Synthesized child terminals must be identified as
adapter-generated and emitted before the run terminal so consumers cannot retain
apparently active tools after the run ends. At the agent-control boundary, all
events carrying a `run_id`, including projected action events, use that run as
their ordering scope. The child terminal therefore has a lower run-scoped
`sequence` than the parent terminal. A binding must preserve this order even if
its native action source uses a separate tool-execution scope; preserving only
per-tool order is insufficient for agent-control conformance.

Every started child action is a required source in the parent's finalization
barrier. A native parent-success candidate waits for all such actions to settle;
it cannot synthesize an action failure and still become `run.completed`. If a
child is proven quiescent but its semantic lifecycle cannot be recovered, the
adapter emits `action.call.failed` with typed `child_lifecycle_incomplete` and
the parent becomes `run.failed`. If the child cannot be proven quiescent after
containment, it becomes `action.call.orphaned` and the parent becomes
`run.orphaned` under the negotiated terminal vocabulary.

If the parent becomes orphaned, any child action whose quiescence is also
unknown terminates with `action.call.orphaned`, not a misleading failed or
cancelled outcome. Adopting `run.orphaned` therefore requires the `+tools` unit
to recognize `action.call.orphaned` as a fourth terminal action outcome.

If a required source does not settle, a declared settlement timeout starts
containment; it does not itself produce a terminal event. The adapter must:

1. stop admitting conflicting work in the affected session or execution scope;
2. request cancellation and terminate or isolate the native source using a
   mechanism that prevents further effects in that scope;
3. establish quiescence through process exit, transport closure plus an
   authoritative remote status, or another adapter-specific guarantee;
4. only then emit `run.failed` with a typed `terminal_settlement_timeout` error.

If the adapter cannot establish quiescence after its containment policy is
exhausted, it emits `run.orphaned`. Canonical session state moves to `error`,
sets `execution_safety: "unknown"`, and retains the orphaned run ID. The endpoint
must not admit work into the same execution scope that assumes the source has
stopped. Recovery requires authoritative later reconciliation or a new isolated
scope; acknowledging the warning alone cannot establish safety.

Canonical `session.state.response` and `session.state.updated` represent this
condition with:

- `orphaned_runs`, a collection of records containing `run_id`,
  `execution_scope_id`, `orphaned_at`, `reconciliation_status`, and optional
  `native_outcome`;
- `reconciliation_status` equal to `unreconciled` or `quiescent`;
- `blocked_execution_scopes`, containing every scope with at least one
  unreconciled orphan; and
- `current_execution_scope_id` plus session `execution_safety`, which describes
  that current scope and is `unknown` when it is blocked.

Persisting the orphan record and blocked scope is atomic with committing the
`run.orphaned` terminal. The canonical state query must expose that mutation
immediately, and the endpoint emits `session.state.updated` for live peers. The
terminal run does not remain in `active_runs`.

Later authoritative evidence about an orphaned run never emits another run
terminal. It is recorded as reconciliation state and diagnostics behind the
same atomic terminal guard. Evidence that proves quiescence may clear the
session's execution-safety block according to policy, but the historical run
outcome remains `run.orphaned`; a native completion or failure discovered later
is retained only as the reconciled native outcome.

When reconciliation proves quiescence, the endpoint changes that record to
`quiescent`, stores any known `native_outcome`, removes the execution scope from
`blocked_execution_scopes` only when no unreconciled orphan still references it,
recomputes session `execution_safety` and `status`, and emits
`session.state.updated`. Status is `error` while the current execution scope is
blocked; after its final block clears, status is derived from current
nonterminal runs as `running`, `waiting_for_input`, `queued`, or `idle` rather
than remaining latched in `error`. If policy recovers by creating a new isolated
scope, that scope becomes `current_execution_scope_id`, its safety and status are
computed independently, and the old scope remains listed as blocked until
reconciled. The orphan record remains available as historical safety state and
is not converted to an active or differently terminal run.

Session status aggregation is deterministic for the current execution scope:
`closed` has highest precedence when the session is closed, then `error` when
the current scope is blocked, then `running` when any run is executing, then
`waiting_for_input` when none is executing and any run waits for input, then
`queued` when only queued runs remain, and otherwise `idle`. Runs retained in an
older blocked scope do not affect the new isolated current scope's status; they
remain visible through `blocked_execution_scopes` and `orphaned_runs`.

This fourth terminal is the proposed resolution for adapters over hosted or
remote sources. The alternative would be to require guaranteed eventual
quiescence as a core conformance precondition, excluding systems that cannot
provide it. A timeout alone must never manufacture ordinary failure.

`run.orphaned` changes the terminal vocabulary and therefore must not be emitted
under the current `open-agent-protocol` `0.1` negotiation. The next core
revision that adopts this proposal must include `run.orphaned` in its normative
terminal set, and `protocol.initialize.response` must select that profile
version before a run is admitted. The selected version is fixed for the run.
An endpoint emits `run.orphaned` only to a peer that negotiated a version which
recognizes it.

An endpoint that may lose authoritative control of execution and cannot
guarantee eventual quiescence must not negotiate the older three-terminal core
as fully compatible. It rejects initialization/profile selection with a typed
unsupported-version error instead. An endpoint may serve the older core only
when its adapter and binding can guarantee that every admitted run eventually
reaches a legacy completed, failed, or cancelled terminal. This prevents a
mid-run downgrade from leaving an older peer waiting forever.

The same revision rule applies independently to `+tools`. The feature-unit
revision that adopts this proposal includes `action.call.orphaned` in its
normative terminal set and is selected alongside the core profile during
initialization. If an endpoint can leave an exposed child action non-quiescent,
it must negotiate both the orphan-capable core revision and the orphan-capable
`+tools` revision before admission. It may combine the newer core with an older
`+tools` revision only when its adapter guarantees that every exposed child
action still reaches completed, failed, or cancelled. Selected core and feature
unit versions remain fixed for the run.

Synthesized terminal events should include or reference:

- a typed error for failures;
- the native signal category used for classification;
- whether the outcome was synthesized;
- degradation when cancellation, usage, final response, or stop reason was
  inferred or unavailable.

`run.completed` must not be used merely because a stream ended. Cancellation
requested but not confirmed remains `cancelling`. A cancellation timeout starts
the same containment process described above; it produces a typed failure only
after quiescence is established.

### Acceptance criteria

- Every admitted run receives exactly one terminal event, including a reserved
  queued run that never starts.
- Abrupt EOF and adapter exceptions cannot become successful completion.
- Cancellation races have a deterministic outcome.
- A success candidate is not emitted before all required terminal sources
  settle.
- A settlement timeout cannot emit a terminal event until execution quiescence
  is established or the run is explicitly classified as orphaned.
- Conflicting late native events cannot produce a second terminal event.
- Run-scoped sequencing makes every child action terminal observably precede
  its parent run terminal across multiplexed bindings.
- A parent success waits for every started child; an unrecoverable quiescent
  child fails the parent, while a non-quiescent child orphans it.
- Reconnect state identifies every unreconciled orphan and blocked execution
  scope, and later quiescence updates safety state without replacing the
  historical terminal outcome.
- Reconciliation or selection of a new isolated current scope recomputes session
  status instead of leaving a recovered session latched in `error`.
- Multiple current-scope run states aggregate using one normative status
  precedence.
- `run.orphaned` is emitted only after negotiation selects a core version whose
  terminal vocabulary includes it; incompatible endpoints fail negotiation
  before admitting work.
- `action.call.orphaned` additionally requires an orphan-capable negotiated
  `+tools` revision whenever exposed children can remain non-quiescent.
- Every started action receives one terminal action event before its parent run
  terminal event.
- Fixtures cover success, native failure, confirmed cancellation, abrupt EOF,
  cancellation/failure races, failure with an in-flight action, and an orphaned
  run with an orphaned action and blocked session scope.

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
- a bootstrap probe that withholds user input until provisional dependencies
  settle, followed by stale-revision rejection before admission;
- lost submit response replayed after that capability revision changes;
- configured policy denying a capability reported by `system:init`;
- `auto` resolution rejecting degraded delivery without explicit opt-in;
- idle message submission and streamed text;
- tool call lifecycle when the SDK exposes tools;
- explicit defer/queue while another run is active;
- queued work cancelled before start;
- concurrent primary and `btw` side runs recovered through session state;
- cancellation, unconfirmed-cancellation timeout containment, abrupt
  SDK/process failure with an in-flight action, and settlement-timeout
  containment, including an orphaned outcome when quiescence remains unknown.

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
- a binding that buffers or loses stream/cancellation fidelity and therefore
  narrows the effective capability snapshot;
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

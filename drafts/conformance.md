# Open Agent Protocol Conformance Draft

Status: draft
Base protocol: `open-agent-protocol` version `0.1`
License: CC0-1.0 public domain dedication, or the nearest legally valid equivalent in jurisdictions that do not recognize public domain dedication.
Scope: profile and conformance-unit claims for Open Agent Protocol implementations.

Conformance is profile-based. Implementations should not make a broad
"implements Open Agent Protocol" claim without naming the profile and optional
conformance units they support.

The first conformance target is:

- `open-agent-protocol.agent-control-core`

Its implemented subset is exercised by the claim
`open-agent-protocol.agent-control-core/0.1-executable` and frozen in
[Decision 0001](../decisions/0001-agent-control-v0.1-executable-core.md). The
claim proves the flat envelope, one foreground run per session, `auto -> start`,
correlated requests and reverse interactions, bounded adapter-journal recovery,
and the three v0.1 terminal outcomes. It does not claim durable persistence,
queue/steer/side runs, or orphan terminals.

Conformance units are additive:

- `+persistence`
- `+tools`
- `+permissions`
- `+user-input`
- `+run-controls`
- `+models`
- `+queue`
- `+steer`
- `+btw`

Example claims:

- `open-agent-protocol.agent-control-core`
- `open-agent-protocol.agent-control-core+tools+permissions`
- `open-agent-protocol.agent-control-core+run-controls`
- `open-agent-protocol.agent-control-core+persistence+tools+permissions+user-input+models+steer`

Conformance units are testable units of behavior, not transport names and not
implementation brands. A control layer should still gate controls from
`capabilities`, not from a hardcoded implementation name.

The leading `+` is compact claim syntax for a conformance unit. Unit names can
also be written without the plus sign when listed in reports, manifests, or test
plans.

## Profiles And Units

A profile defines a coherent implementation target. The agent-control core
profile is the smallest control-layer/agent-loop boundary expected to
interoperate on its own.

A conformance unit defines one independently testable optional behavior within a
profile. Units can be implemented and tested independently when their
dependencies are satisfied. This keeps optional capabilities visible without
turning every optional behavior into a separate profile.

## Transport Neutrality

Conformance tests semantic behavior, not transport. JSONL, WebSocket, JSON-RPC,
HTTP/SSE, and in-process bindings can all conform if they preserve the same
logical envelopes and lifecycle rules.

A binding may choose its own wire shape, handshake, heartbeat, batching,
backpressure, and reconnection mechanics. It must preserve:

- event type;
- request/response correlation;
- scoped ordering;
- identifiers;
- payload semantics;
- terminal-event rules;
- extension fields;
- capability and degradation reporting.

Requests initiated across the agent-control boundary must still have semantic
request/response correlation even when the binding carries all envelopes on an
event stream. A successful request receives exactly one correlated `*.response`
envelope. A rejected request receives exactly one correlated `error.response`
envelope with a typed protocol error. Stream events do not replace
request responses.

## Core Profile Requirements

An implementation conforms to `open-agent-protocol.agent-control-core` if it
satisfies all of the following:

1. Emits and accepts valid core envelopes.
2. Supports `protocol.initialize.request` and `protocol.initialize.response`.
3. Supports `capabilities.request` and
   `capabilities.response`, including an opaque `capability_revision`.
4. Supports `session.open.request` and `session.open.response`.
5. Supports `session.state.request`, `session.state.response`, and
   `session.state.updated`.
6. Accepts `session.message.submit.request` and returns
   `session.message.submit.response`.
7. Emits `run.status.updated` for meaningful run lifecycle changes.
8. Streams assistant-visible output through `content.delta`.
9. Emits exactly one terminal run event for every accepted run:
   `run.completed`, `run.failed`, or `run.cancelled`.
10. Supports `run.cancel.request`, or declares cancellation as unavailable in
    capabilities and returns a correlated `error.response` with a typed
    unsupported-feature error if called.
11. Returns correlated `error.response` envelopes for unsupported commands and
    invalid requests.
12. Enables feature gating through capabilities and degradation records rather
    than implementation names.
13. Rejects any request carrying a non-current `capability_revision` with a
    correlated `error.response` whose code is `stale_capabilities`, except that
    `protocol.initialize.request` and `capabilities.request` ignore the field so
    discovery cannot be blocked by a stale revision.
14. Repeats the admitted `capability_revision` on every successful response to
    a request that supplied one.

Core conformance does not require persistence, tools, permissions, user-input
prompts, model listing, queue/steer/btw delivery, checkpointing, artifacts,
auth flows, or a specific transport.

Core conformance requires `auto` message delivery. An implementation that
receives an explicit unsupported delivery mode such as `queue`, `steer`, or
`btw` must return a correlated `error.response` with a typed
unsupported-feature error.

For an accepted `auto` submission, the response must repeat
`requested_delivery: auto` and report a concrete `effective_delivery` of
`start`, `queue`, `steer`, or `btw`. It must not report `auto` as the effective
delivery.

## Optional Conformance Units

### `+tools`

An implementation conforms to `+tools` if it:

- advertises `action.tools.list` and `action.tools.execute` with effective
  support levels other than `unavailable`;
- exposes the effective tool catalog through `capabilities.response` or
  `action.tools.list.response` before the control layer is expected to render
  or apply tool controls;
- represents callable tools with core `ToolDefinition` records;
- emits `action.call.requested` when a tool call is selected;
- emits `action.call.started` when execution begins;
- emits exactly one terminal event for each started tool call:
  `action.call.completed`, `action.call.failed`, or `action.call.cancelled`;
- marks failed tool calls through `action.call.failed` with a typed error.

`action.call.progress` is optional. If progress is degraded, buffered, or
unavailable, the implementation should say so in capabilities or degradation
records.

The `+tools` unit means the implementation reports available/effective tools to the
control layer and emits normalized tool lifecycle events. It does not require
the control layer to send executable tool definitions to the agent loop or
adapter.
Control-layer-provided tools are a separate future unit.

### `+permissions`

An implementation conforms to `+permissions` if it:

- advertises `action.permissions` with an effective support level other than
  `unavailable`;
- emits `action.permission.requested` for operations requiring user approval;
- accepts `action.permission.resolve.request`;
- emits `action.permission.resolved` after the implementation accepts the decision;
- never encodes permission prompts as opaque text-only assistant messages.

Permission prompts ask whether an operation may proceed. They should not be used
for ordinary information gathering from the user.

### `+user-input`

An implementation conforms to `+user-input` if it:

- advertises `user_input` with an effective support level other than
  `unavailable`;
- emits `run.status.updated` with `status: "waiting_for_input"` when a run is
  paused for user input;
- emits `user.input.requested` with stable `interaction_id` and structured
  questions;
- accepts `user.input.resolve.request`;
- accepts `user.input.cancel.request` when the prompt is cancellable;
- emits `user.input.resolved` after submit or cancellation;
- resumes, fails, or cancels the run using normal run lifecycle events.

Draft persistence is not required for `+user-input`. An implementation that
persists draft answers may expose that through extensions or a richer profile.

### `+persistence`

An implementation conforms to `+persistence` if it:

- advertises `session.list` and `transcript.load` with effective support levels
  other than `unavailable`;
- supports `session.list.request` and `session.list.response`;
- supports `transcript.load.request` and `transcript.load.response`;
- returns stable message IDs for persisted messages when available;
- provides cursor behavior for pagination or sync when it advertises cursors;
- emits `transcript.delta` when it advertises live transcript sync;
- can recover canonical session state after reconnect through
  `session.state.request`.

`transcript.delta` is optional unless the implementation claims live transcript
sync. An implementation may support historical transcript loading without live
persisted row deltas.

### `+run-controls`

An implementation conforms to `+run-controls` if it implements the fail-closed
discipline for all four per-submit controls — `model_id`, `instructions`,
`tool_choice`, and `output_schema` — and executes each control it advertises
above `unavailable`. The two halves are separate, and an endpoint that
advertises none of the four still claims the unit by refusing all four
correctly: refusing an unadvertised control *is* the discipline.

The discipline is that an implementation:

- gates each control on its own capability key (`run.model_selection`,
  `run.instructions`, `run.tool_selection`, `run.structured_output`) and
  refuses a control it has not affirmatively advertised *before* admission,
  with `unsupported_feature`, `details.feature` naming the key, and
  `details.reason: "unadvertised"`. No submission or run identity is
  allocated;
- refuses a control it advertises `degraded` whose key the request's
  `allow_degraded_features` omits, with `capability_degraded` and
  `details.feature`;
- refuses a control it cannot honour for this request's value with
  `unsupported_feature`, `details.reason: "unsatisfiable"`, and the detail
  that names the offending member (`details.tool`, `details.field`) — except
  a `model_id` it cannot serve, which is `model_not_found` with
  `details.model_id`, the empty id included;
- reports exactly one refusal when a request fails more than one of these, in
  the order above and, among peers, by the lower capability key and the lower
  JSON Pointer;
- never drops a control it accepted: presence is what the gate judges, so a
  present-but-empty control is a control.

Execution, per advertised control, is that an implementation:

- applies an admitted `model_id` to the run it was requested for, repeats it
  on the admission and on `run.started`, and never attributes the run to
  another model. `run.model_selection`'s `mode` discloses how: `per_run`
  leaves `current_model_id` — the model the next control-free submission
  would use — untouched, and `session_mutation` moves it and reports the
  native truth afterwards;
- accepts `instructions` it advertises. Whether admitted instructions took
  effect is not a wire observable, so the key means "this endpoint accepts
  instructions" rather than a checked promise;
- honours an admitted `tool_choice` over the session's catalog: `allowed` or
  `disallowed` filters it, then `mode` applies to the filtered set. A tool the
  policy excludes is never called, `required` and `named` are met before a
  completed response, and `run.tool_selection`'s `modes` discloses the modes
  the endpoint can actually enforce — a refusal is conforming only for a mode
  outside that list;
- binds an admitted `output_schema` to the run's final response:
  `run.completed.result` is present and conforms, or the run fails with
  `structured_output_failed`. A fixed-output endpoint declares its result as
  `run.structured_output`'s `fixed_result` constraint and then carries exactly
  that object.

Session-level defaults, a configuration document, and any control not named
above are outside this unit.

### `+models`

An implementation conforms to `+models` if it:

- advertises `models.list` with an effective support level other than
  `unavailable`;
- supports `models.request` and `models.response`;
- accepts `model_id` in `session.message.submit.request` when model selection
  is advertised;
- reports the effective model in `session.message.submit.response`,
  `run.started`, or session state when known;
- reports degraded or unavailable model selection explicitly when an adapter can
  only infer or approximate model control.

Model cache refresh, provider auth state, and model alias resolution are outside
this feature unless a richer profile defines them.

### `+queue`, `+steer`, And `+btw`

Delivery mode units are independent. An implementation conforms to one of these
units if it:

- advertises the delivery mode in capabilities;
- accepts `session.message.submit.request` with that explicit `delivery`;
- returns `session.message.submit.response` with matching `requested_delivery`
  and concrete `effective_delivery` unless it returns a correlated
  `error.response`;
- reports the admission result as `queued`, `steered`, or `side_started`;
- preserves normal run status, stream, and terminal-event rules for any run it
  starts or touches.

`btw` means a lightweight side question that uses the same session environment
and configuration but does not block the main run.

## Executable Validation And Fixture Plan

Conformance has two validation layers:

1. Structural validation applies the checked-in JSON Schema bundle to each
   logical envelope.
2. Stateful validation checks relationships across the ordered trace that JSON
   Schema cannot express.

Stateful checks include:

1. Exactly one correlated response for every request.
2. Stable, distinct identity domains and agreement between envelope and payload
   scope IDs.
3. Positive, contiguous run-scoped sequences beginning with `run.started`.
4. Exactly one terminal event for every accepted run and no run-scoped semantic
   event after it.
5. Exactly one terminal action event for every exposed tool call, including a
   call cancelled or denied before execution starts.
6. Closure of all run-scoped tool and interaction lifecycles before the parent
   terminal.
7. Interaction resolution by the declared participant only.
8. Cancellation acknowledgement as nonterminal intent followed by authoritative
   run settlement.
9. Capability gating, degradation disclosure, revision equality, stale-request
   rejection, and refresh before retry.
10. Recovery as either a contiguous adapter-journal suffix or an explicit replay
    gap plus authoritative session state.

`fixtures/manifest.json` is the normative inventory. It declares whether each
fixture is valid or invalid, the expected validation phase and diagnostic codes,
and the conformance units it exercises. Validity must not be inferred from a
filename. Bindings are tested by normalizing native wire messages into these
logical envelopes before validation.

## Error Expectations

Unsupported commands should fail early with a typed protocol error. A
conforming implementation should not silently ignore unsupported commands,
invent private event names for core semantics, or rely on control-layer
hardcoding to avoid unsupported paths.

The minimum error shape requires:

- `code`
- `message`

It may also include:

- `retriable`
- `details`, as an object

The `code` should be stable enough for control-layer behavior. Human-readable
text belongs in `message`.

## Degradation Expectations

Capabilities describe the current effective behavior, not the ideal behavior of
the underlying provider, SDK, or agent loop. Adapters must report destructive
transforms as degradation.

Common examples:

- buffered tool arguments;
- reconstructed streams;
- stripped or summarized reasoning;
- missing tool catalog;
- cooperative-only cancellation;
- non-resumable run state;
- inferred model catalog;
- transcript reconstructed from persisted messages rather than native protocol
  events.

Degradation records should identify the affected feature, the effective support
level, and a reason suitable for control-layer handling, presentation, or
diagnostics.

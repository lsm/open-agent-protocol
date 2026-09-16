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
- `+tool-sources`
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

An implementation may also claim one term per extension pack it implements,
written `+ext:<pack id>/<pack version>` — for example
`open-agent-protocol.agent-control-core+tools+ext:com.example.storage/1.0.0`.
See "Extension Packs" below.

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

### `+tool-sources`

An implementation conforms to `+tool-sources` if it serves a catalog whose
tools are attributed to sources, and, where it advertises attachment, accepts
tool sources at session open. The two halves are separate keys and an
endpoint may claim the unit with either: what the unit requires is that a
capability it advertises is honoured, and that one it does not is refused in
a way the caller can act on.

For the catalog (`action.tools.list`), an implementation:

- gates `action.tools.list.request` and `action.tools.list.response` on that
  key alone. `action.tools` is not an alias: that key means lifecycle
  observation, and an endpoint that observes tool calls without publishing a
  portable catalog advertises the one and not the other;
- refuses a catalog request it has not advertised with
  `unsupported_feature`, `details.feature: "action.tools.list"`, and
  `details.reason: "unadvertised"`, and one it advertises `degraded` whose
  key the request's `allow_degraded_features` omits with
  `capability_degraded` and `details.feature`;
- serves a catalog in which a source `id` is unique, a tool `name` is unique
  whatever its source, and every tool names a `source`, which is a source the
  same response declares. `source` is optional in the schema, because a
  descriptor published by an endpoint outside this unit carries tools with no
  attribution — but an `action.tools.list.response` is this unit's own
  envelope and attribution is the whole of what its key adds, so a served
  catalog that omits it is the flat list the unit replaces. A harness that
  namespaces its MCP tools exposes the namespaced string as `name`; `source`
  carries the attribution, so a consumer never has to parse one out of the
  other;
- answers a request that names a session with that session's effective
  catalog, repeating the session on the response's envelope and in its
  payload. A request names its session in either place — the payload member is
  optional here, because an unscoped list asks for the endpoint's own catalog —
  and an unscoped answer to a request scoped either way is not an
  endpoint-level catalog. An answer to a request naming no session carries no
  session's attachments: an attachment belongs to one session, and a response
  carrying no scope is read as what the endpoint publishes to everyone;
- serves every catalog with the `capability_revision` it was served under, so
  a caller can bind the listing to a descriptor snapshot and discard it when
  `capabilities.updated` reports another. The field is schema-required on
  `action.tools.list.response` for the reason it is on `models.response`: the
  whole content of the envelope belongs to one snapshot. It matters more here
  than there, because a session's catalog is a function of the descriptor
  *and* of the sources that session attached under it, and an endpoint that
  republishes its tools per turn changes what it lists without anyone asking;
- attributes a call it emits with `source` to the source its own catalog
  records for that tool — the session's served catalog where it has served one,
  and otherwise the descriptor's, which is the published attribution until a
  session-scoped list supersedes it. A call that omits `source` for a tool one
  of those catalogs attributes is `unattributed_call`: the member is optional
  on the wire, but an endpoint advertising this key has the answer and is
  publishing it everywhere except where a consumer needs it. A tool neither
  catalog lists is one the endpoint has published no attribution for, and a
  call for it may name none.

For attachment at open (`action.tool_sources.attach`), an implementation:

- accepts `session.open.request.tool_sources` and attaches them for the
  session's lifetime, or refuses the open with `unsupported_feature`,
  `details.feature: "action.tool_sources.attach"`, and
  `details.reason: "unadvertised"`;
- discloses `modes: ["session_open"]` wherever the key is advertised at all,
  and additionally `"remote"` in that set where it accepts a `remote` source;
  the plural is what lets the second be said without erasing the first. An
  open attaching a `remote` source to an endpoint whose set omits it is
  refused `unsatisfiable` with `details.source`; an open attaching anything to
  an endpoint whose set omits `session_open` is refused on the capability
  rung, because a key that discloses no session-open mode offers nothing an
  open can elect;
- refuses an attachment whose `id` collides with another attachment or with a
  source the descriptor already declares, with `details.source` naming it,
  rather than shadowing or renaming one silently;
- discloses in `limits` the constraints it actually has — `max_sources`, the
  `transports` it accepts — because refusing an array that violates none of
  them and carries no defect any rule above names would make the advertised
  key promise nothing. `max_sources` is a positive ceiling and each transport
  names a source kind: a limit no request can satisfy would make refusing every
  request conforming, so the schema refuses both shapes. An array outside a
  disclosed limit may be admitted or refused, but a refusal is
  `unsupported_feature` with `details.reason: "unsatisfiable"` and
  `details.source` naming the entry to drop — "over the limit" is actionable
  only when the caller is told which entry put it there;
- refuses an attachment with no `id` before anything is done with it, because
  everything an endpoint does with an attachment is done by its id: it is the
  collision key above, the name a harness routes the source by, and the id the
  session publishes the source under. An empty one reaches a client as a
  descriptor whose required `id` is empty, which is the endpoint emitting a
  document this schema rejects;
- publishes the attached sources back through the open response, later
  session snapshots, and every session-scoped catalog, as
  `ToolSourceDescriptor` values, one descriptor per `id` in each of them. The
  open response must agree with every member the attachment stated and may
  fill one it left blank — an attachment names a source, it does not claim to
  describe it completely — and what that response publishes is what every
  later snapshot and catalog repeats.
  `command`, `args`, and `environment` are attachment-only and never appear in
  a published source — in any of those, or in the capability descriptor's own
  `sources`, top level or under a layer.

Runtime attach and detach, a catalog served without a session, and
control-layer-provided tools are outside this unit; the last is `+control-tools`.

### `+models`

An implementation conforms to `+models` if it serves a session-scoped catalog
and is bound by it in both directions: every id it lists is selectable, and
every id it omits is not. Executable since
[Decision 0006](../decisions/0006-models-catalog.md).

An implementation:

- advertises `models.list` with an effective support level other than
  `unavailable`, and serves `models.request`/`models.response` under it. A
  catalog served without the key is `unavailable_capability`
  (`models-unadvertised`); a query refused under the key is
  `unhonoured_capability` (`models-list-refused-advertised`). The conforming
  refusal of a query it does not advertise is `unsupported_feature` with
  `details.feature: "models.list"` and `details.reason: "unadvertised"`
  (`models-unadvertised-rejected`, `models-unadvertised-wrong-reason`);
- carries the degraded opt-in per query. A `degraded` catalog served without
  `allow_degraded_features` naming the key is `degraded_without_optin`
  (`models-list-degraded-without-optin`); refusing that query with
  `capability_degraded` and `details.feature` is conforming
  (`models-list-degraded-refused`, `models-list-degraded-optin`);
- serves a catalog that is internally consistent: unique ids
  (`duplicate_model_id`), at most one `default` (`ambiguous_default_model`),
  and a `current_model_id` that is one of its own ids
  (`models-current-not-listed`) and a model the session held while the query
  was in flight (`models-current-mismatch`). A catalog captured ahead of the
  trace names its own position in `as_of_model_event` and is judged there
  (`models-current-model-ahead-of-trace`,
  `models-current-model-mutation-in-flight`);
- keeps the catalog stable within a capability revision, because the catalog
  is part of the capability snapshot: a change with no `capabilities.updated`
  is `unannounced_catalog_change` (`models-catalog-mutates-within-revision`,
  `models-catalog-metadata-mutates-within-revision`). A `degraded` catalog
  discloses out-of-band refresh and is exempt;
- admits a `model_id` the catalog lists and refuses one it omits with
  `model_not_found` and `details.model_id` (`models-list-then-select`,
  `models-unlisted-selection-refused`). Admitting an unlisted id
  (`models-select-unlisted`), answering `model_not_found` for a listed one
  (`models-listed-selection-false-miss`), or refusing an unlisted one under
  another code or without the detail
  (`models-unlisted-selection-wrong-refusal`,
  `models-unlisted-selection-missing-detail`) are each
  `model_not_in_catalog`. A listed selection refused under a code that makes
  no claim about the model — a busy session, another control's gate, an
  ordinary failure — is not a catalog defect and is not judged here: a valid
  selection obliges no admission, only an answer that does not deny the id
  exists. A selection made before the first catalog under the
  active revision is settled by it on the same terms
  (`models-refused-before-catalog`, `models-select-in-gap-unlisted`).

Where `run.model_selection` is not advertised, that unit's capability refusal
wins and this rule stands down (`models-unadvertised-selection-unlisted`): a
catalog can be served by an endpoint that applies no per-submit selection.

Model resolution, aliases, pricing, cache refresh, and provider auth state are
outside this unit unless a richer profile defines them.

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

## Extension Packs

The unprefixed namespace is the spec's, in its entirety: a capability key,
envelope `type`, or `error.response` code carrying no reverse-DNS prefix is
spec-owned, whatever its shape. An extension name carries a reverse-DNS prefix,
and an extension pack may declare names only beneath its own `id`.

A pack is a `pack.json` descriptor plus the schemas it contributes and,
optionally, its own fixture manifest in the format above. The descriptor
declares the pack's capability keys, envelope types with a stated `role`, error
codes, the gate each request and event is judged against, and any members the
pack adds to core payloads with the subschema each must satisfy. The loader
refuses a pack that declares a name outside its own namespace, that is not
prefix-free with the other loaded packs, that leaves a type ungated or a role
unstated, that restates a member a core payload already defines, or whose
schemas reach outside the pack, the core bundle, and its declared dependencies.
A refusal to load is not a validation diagnostic: the validator never ran.

An implementation conforms to `+ext:<pack id>/<pack version>` if it:

- loads that pack at that exact version;
- advertises each of the pack's capability keys it implements with an effective
  support level other than `unavailable`, and refuses an operation gated on an
  unadvertised key with the same typed `unsupported_feature` error naming the
  key in `details.feature` that a core capability is refused with;
- emits and accepts the pack's envelope types as the pack's own schemas define
  them;
- refuses a well-formed request it advertises a key for only under a code the
  pack declared in that request's `refusals`;
- passes the pack's own fixture manifest, which carries a negative gate fixture
  and a negative honour fixture for every capability key the pack declares.

A pack term carries no authority over the core claim beside it. A pack's
fixtures may not claim a core unit, and a core fixture may not claim a pack
term: conformance to an extension is never evidence for the protocol.

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

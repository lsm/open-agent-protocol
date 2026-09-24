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
steer or side runs, or orphan terminals. Queued reservations beside the started
run are the `+queue` unit's, not the core claim's.

Conformance units are additive:

- `+tools`
- `+permissions`
- `+user-input`
- `+run-controls`
- `+tool-sources`
- `+control-tools`
- `+models`
- `+provider-attach`
- `+queue`
- `+compound-open`
- `+steer`
- `+btw`

Example claims:

- `open-agent-protocol.agent-control-core`
- `open-agent-protocol.agent-control-core+tools+permissions`
- `open-agent-protocol.agent-control-core+run-controls`
- `open-agent-protocol.agent-control-core+tools+permissions+user-input+models+steer`

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
6. Supports `session.model.switch.request` and
   `session.model.switch.response`: an accepted switch changes only that
   session's default model, is reflected in canonical state, and leaves an
   already-running run on its admitted model. A missing model is refused with
   `model_not_found` and `details.model_id`.
7. Accepts `session.message.submit.request` and returns
   `session.message.submit.response`.
8. Emits `run.status.updated` for meaningful run lifecycle changes.
9. Streams assistant-visible output through `content.delta`.
10. Emits exactly one terminal run event for every accepted run:
   `run.completed`, `run.failed`, or `run.cancelled`.
11. Supports `run.cancel.request`, or declares cancellation as unavailable in
    capabilities and returns a correlated `error.response` with a typed
    unsupported-feature error if called.
12. Returns correlated `error.response` envelopes for unsupported commands and
    invalid requests.
13. Enables feature gating through capabilities and degradation records rather
    than implementation names.
14. Rejects any request carrying a non-current `capability_revision` with a
    correlated `error.response` whose code is `stale_capabilities`, except that
    `protocol.initialize.request` and `capabilities.request` ignore the field so
    discovery cannot be blocked by a stale revision.
15. Repeats the admitted `capability_revision` on every successful response to
    a request that supplied one.

Core conformance does not require persistence, tools, permissions, user-input
prompts, model listing, queue/steer/btw delivery, checkpointing, artifacts,
auth flows, or a specific transport.

`session.model.switch` is a core operation even when `+models` listing and
per-submit `+run-controls` are absent. A fixed-model endpoint can accept a
switch to the same current model and reject another id; it cannot acknowledge
a switch it did not apply. A successful response is a barrier for later
submits, while concurrently pipelined requests may be admitted in either
order. See [Decision 0028](../decisions/0028-live-model-and-provider-control.md).

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
  another model. `run.model_selection`'s `scope` discloses how long: `run`
  leaves `current_model_id` — the model the next control-free submission
  would use — untouched, and `session` moves it and reports the
  native truth afterwards;
- accepts `instructions` it advertises. Whether admitted instructions took
  effect is not a wire observable, so the key means "this endpoint accepts
  instructions" rather than a checked promise;
- honours an admitted `tool_choice` over the session's catalog: it is a filter,
  carrying `allowed` or `disallowed` and exactly one of them, and a call to a
  tool the filter excludes settles `action.call.failed` with `error.code`
  `refused_by_policy`, a core code, which no other call may carry
  ([Decision 0031](../decisions/0031-a-policy-refusal-is-a-settlement.md)).
  For `run.tool_selection`, `native` means the harness constrains the model
  before it acts, and `emulated` means the endpoint enforces the filter around
  a harness that does not, by electing only permitted tools or by refusing
  excluded calls that way; an endpoint that can do neither advertises
  `unavailable`. An `allowed` list naming a tool the catalog
  does not carry is unsatisfiable wherever the catalog is known, and an empty
  `allowed` admits nothing. The catalog is known once `capabilities.response`
  carries a `tools` member, at the top level or in a layer, even an empty one;
  a descriptor carrying none leaves it unknown, and the filter then excludes
  exactly the tools its lists exclude
  ([Decision 0034](../decisions/0034-an-unpublished-catalog-is-unknown.md));
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
  endpoint-level catalog. The rule binds in both directions: an answer to a
  request naming no session may not name one either, because an attachment
  belongs to one session, and a caller that asked what the endpoint publishes to
  everyone would be handed that session's sources as the answer. A response
  carrying no scope is read as what the endpoint publishes to everyone;
- serves every catalog with the `capability_revision` it was served under, so
  a caller can bind the listing to a descriptor snapshot and discard it when
  `capabilities.updated` reports another. The field is schema-required on
  `action.tools.list.response` for the reason it is on `models.response`: the
  whole content of the envelope belongs to one snapshot. It matters more here
  than there, because a session's catalog is a function of the descriptor
  *and* of the sources that session attached under it, and an endpoint that
  republishes its tools per turn changes what it lists without anyone asking;
- attributes a call it emits with `source` to the source the catalog *in force*
  records for that tool. Exactly one catalog is in force for a session: the
  catalog it has been served under the active capability revision, and
  otherwise the descriptor's, which is the published attribution until a
  session-scoped list supersedes it. A served catalog supersedes wholly, not
  tool by tool — an endpoint that lists without a tool the descriptor once
  mapped has republished its listing without it, and the superseded entry is
  not consulted again for anything. A catalog served under a revision that has
  since moved is not in force either, and leaves no gap when it stops being so:
  `capabilities.updated` discards it, and what takes over is the new
  descriptor's own attribution, which is current rather than stale. A call that
  omits `source` for a tool the catalog in force attributes is
  `unattributed_call`: the member is optional on the wire, but an endpoint
  advertising this key has the answer and is publishing it everywhere except
  where a consumer needs it. A tool that catalog does not list is one the
  endpoint has published no attribution for, and a call for it may name none,
  or name any source the session resolves;
- names on a call exactly the source the catalog in force attributes the tool
  to, and none where no catalog in force does. `source` is a cross-reference,
  and a cross-reference a reader cannot follow is not one, so the rule binds in
  both directions: naming a source no envelope in the trace declares is
  `unmatched_tool_source`, and omitting one the catalog in force records is
  `unattributed_call`. An endpoint whose sources are known before any session
  declares them in its descriptor and may name them from the start; one that
  learns a source from a session may name it once it has served the catalog
  that publishes it, and before that emits the call with no `source` while the
  catalog keeps the attribution exactly. Publishing and attributing are one
  decision — an endpoint may attribute to what it has published, to all of it,
  and to nothing else.

For attachment at open (`action.tool_sources.attach`), an implementation:

- accepts `session.open.request.tool_sources` and attaches them for the
  session's lifetime, or refuses the open with `unsupported_feature`,
  `details.feature: "action.tool_sources.attach"`, and
  `details.reason: "unadvertised"`;
- honours a `capability_revision` on such an open as the exact precondition the
  core profile makes it — a nonempty one that is not current is refused
  `stale_capabilities` with `expected_revision` and `current_revision` — and
  admits one that carries none, evaluating it against current capabilities and
  answering with the revision used for admission. Pinning is the caller's
  choice; requiring it would refuse a request the profile permits. An open
  attaching nothing is not revision-gated at all;
- discloses `modes: ["session_open"]` wherever the key is advertised at all,
  and additionally `"remote"` in that set where it accepts a `remote` source;
  the plural is what lets the second be said without erasing the first. The set
  is judged where it is published: a key advertised affirmatively whose modes
  omit `session_open` — an empty set, or `remote` alone — is
  `undisclosed_attach_modes` on the `capabilities.response` itself, because
  `session_open` is the only application this unit defines and a key no open
  can elect promises nothing. Names outside the vocabulary are tolerated beside
  it, since the vocabulary is additive, and that tolerance is what makes the
  plural a set rather than a costume: disclosing `remote` is an addition, never
  a substitution. An
  open attaching a `remote` source to an endpoint whose set omits it is
  refused `unsatisfiable` with `details.source`; an open attaching anything to
  an endpoint whose set omits `session_open` is refused on the capability
  rung, because a key that discloses no session-open mode offers nothing an
  open can elect;
- refuses an attachment whose `id` collides with another attachment or with a
  source the descriptor already declares, with `details.source` naming it,
  rather than shadowing or renaming one silently;
- refuses an attachment whose `environment` names one variable twice, with
  `details.source` naming the attachment. `uniqueItems` does not cover it —
  `TOKEN=first` and `TOKEN=second` are two strings naming one variable — and a
  source launched with both carries a credential whose value nothing decides;
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

Runtime attach and detach and a catalog served without a session are outside
this unit; control-layer-provided tools are `+control-tools`, below.

### `+control-tools`

An implementation conforms to `+control-tools` if the control layer can supply
tools at session open and execute their calls. The unit is staged, not
executable. [Decision 0011](../decisions/0011-control-layer-provided-tools.md)
carries its wire shape, validator rules and fixtures and is still proposed,
because no adapter executes the unit; under
[Decision 0003](../decisions/0003-staged-unit-graduation.md) no draft claims a
unit executable until a native adapter proves one. What follows is therefore
the shape an implementation will be held to, not a claim one may make today.

An implementation:

- advertises `action.tools.provide` with an effective support level other than
  `unavailable`, and refuses a `session.open.request` carrying `tools` it has
  not advertised with `unsupported_feature`, `details.feature:
  "action.tools.provide"`, and `details.reason: "unadvertised"`. An open
  admitted without the key is `unavailable_capability`;
- provisions the supplied array whole or refuses the open. A refusal is
  `unsupported_feature` with `details.reason: "unsatisfiable"` and the detail
  that names the offending entry: `details.tool` for a foreign
  `execution_owner` or a colliding `name`, `details.source` for a `source`
  neither the descriptor nor the same open declares. A refusal under another
  code, or without that detail, is diagnosed as the defect it failed to name —
  `wrong_tool_owner`, `duplicate_tool_name`, or `unmatched_tool_source` — and so
  is an open admitted despite the defect;
- discloses in `limits` every constraint it actually enforces on a `tools`
  array — `max_tools`, `name_pattern`, `schema_dialect` — because refusing an
  array that carries no defect and violates no disclosed limit is
  `undisclosed_provide_limit`. A `schema_dialect` binds only a definition that
  declares a different one;
- lists every provided tool in each session-scoped catalog, under the owner,
  schema, and source it was supplied with, for the session's lifetime
  (`catalog_mismatch`), and keeps the tool's name unique and its source
  resolvable across every capability refresh;
- opens an interaction for each call to a provided tool: `action.call.requested`
  with `interaction_id`, `requested_by`, `responded_by`, and the control
  participant as `execution_owner` (`illegal_tool_transition` without the
  first two), routed to the owner the catalog records (`wrong_tool_owner`
  otherwise);
- accepts `action.call.resolve.request` in three arms — `started`, `result`,
  `error`, exactly one — and answers each with an
  `action.call.resolve.response`. A valid resolution is accepted; a refusal
  carries the highest reason the request satisfies, in the order
  `unknown_interaction`, `wrong_responder`, `already_resolved`,
  `repeated_acknowledgement`, `late_acknowledgement`. `already_resolved` is a
  terminal the trace carries, or — for a `result` or `error` arm — a resolution
  already accepted; `late_acknowledgement` is a `started` arriving after the
  sender's own resolution was accepted and before its terminal was published.
  Only an `already_resolved` refusal names a settlement, in
  `details.settlement_id`, because it is the only reason that has one: the
  terminal where one exists, and otherwise the accepted
  `action.call.resolve.response` that settled the call. Naming the acceptance
  is what gives the window between a resolution and its terminal a conforming
  refusal at all, which is the window a lost response and its retry land in;
- emits `action.call.started` only once an accepted resolution evidences
  execution, and derives each terminal from an accepted resolution of the
  matching arm, carrying exactly what that resolution stated
  (`resolution_payload_mismatch` otherwise). Every resolve-derived event names
  its request in `request_id`;
- counts unresolved control-owned calls among each `active_runs` entry's
  `pending_interactions`, and reports in `acknowledged_interactions` the subset
  whose `started` it accepted;
- settles a call a reattach recovered on the same terms as any other, even
  though what authorized it is behind the cursor. Its terminal is where it
  leaves the pending set, so a run that terminates after it is not carrying a
  pending interaction and a snapshot taken after it does not list one; an
  acknowledgement accepted after the reattach is reported like any other, and
  one accepted before it is neither known nor required.

Per-submit provisioning, runtime attach and detach of provided tools, and
deadlines on an unanswered call are outside this unit.

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
  discloses out-of-band refresh and is exempt. An accepted
  `session.provider.attach` is the other explicit exception: it invalidates
  the catalog of its own session, even when the endpoint capability revision
  does not change;
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

### `+provider-attach`

An implementation conforms to optional `+provider-attach` if it advertises
`action.providers.attach` with `session_live` mode and accepts
`session.provider.attach.request` for a provider offered by a co-hosted or
operator-configured `model-provider-core` service. Its session-local alias
must be unique, the request is atomic, and success must expand that session's
next model catalog without changing its default model or an active run. An
unknown service/provider or a conflicting alias is refused with a typed
error; an unadvertised operation is refused with `unsupported_feature` and
`details.feature: "action.providers.attach"`. No caller-provided vendor
URL, wire, command, header, or credential is admitted. See
[Decision 0028](../decisions/0028-live-model-and-provider-control.md).

### `+queue`

`+queue` is executable, and
[Decision 0007](../decisions/0007-queue-delivery.md) froze it. An
implementation conforms if it:

- advertises `session.message.delivery.queue` above `unavailable`, and
  discloses a positive `capabilities.response.limits.max_queued_runs_per_session`
  with it — a queue nothing could ever reach promises nothing — and an
  `max_active_runs_per_session`, where it states one at all, of at least the
  queued bound plus the started run;
- admits an explicit `delivery: "queue"` as a reservation
  (`admission: "queued"`, `effective_delivery: "queue"`, `status: "queued"`,
  nothing emitted for it yet) whatever the session holds, and never as a
  started run;
- resolves an `auto` submission on a busy session to the same shape and reports
  `delivery_resolution: "session_busy"`;
- promotes a reservation by emitting `run.started` only when every
  earlier-admitted run of the session is terminal, and publishes nothing else
  in a later-admitted run's domain while an earlier one is nonterminal — except
  the pre-start terminal of a run that never started, which is published when
  it happens;
- refuses what it cannot admit with the wire's `run_active`: a busy session
  where it advertises no busy outcome, and a submission that would put the
  nonterminal set or the queued subset above a disclosed bound. It does not
  report that code for a bound it was not at;
- reports `session.state.active_runs` — every nonterminal run in admission
  order, with `queue_position` on each reservation and `active_run_id` naming
  the started run — wherever `active_run_id` cannot carry the answer, and
  states the position each snapshot was captured at when it reports a run's
  pending set, a removed run, or a session default that a promotion is moving;
- applies a reservation's `model_id` at promotion rather than at admission,
  under the mode retained from its admission.

An endpoint that cannot queue advertises the capability `unavailable` and
refuses an explicit `queue` with `unsupported_feature` naming the key; that
refusal is the discipline, and claiming the unit is not required to make it
conforming.

### `+compound-open`

`+compound-open` is executable, and
[Decision 0009](../decisions/0009-compound-open.md) froze it. An
implementation conforms if it:

- accepts `subscribe` and `message` as optional members of
  `session.open.request`, and serves an open that sets neither exactly as it
  does without this unit;
- advertises `session.open.subscribe` above `unavailable`, refuses an open
  electing it against a descriptor that does not with `unsupported_feature`
  naming the key, and refuses one electing it against a `degraded` disclosure
  with `capability_degraded` unless the request consents through
  `allow_degraded_features`;
- registers the subscription before admitting the message, so a subscription
  the open carries cannot miss the run's opening envelopes — which is the
  race the unit exists to remove, and the only one it removes;
- reports the message's admission in the open response's `active_runs`, whose
  entry names the run, its status, its `admitted_submit_requests` citing the
  open request itself, and `queue_position` where the admission was a
  reservation. It adds no member to `session.open.response`, which this unit
  leaves unchanged;
- fails atomically: an open whose message cannot be admitted admits nothing
  and opens nothing, and a `session_id` the request named is free to open
  again afterwards;
- answers every election one open carries with a single refusal. An open may
  elect `subscribe`, attach tool sources, and carry a message whose controls
  each take their own ladder; a refusal conforming to any one of them answers
  the open, and no other rung is owed a second refusal it could not send.

An endpoint that cannot subscribe at open advertises the capability
`unavailable` and refuses the flag with `unsupported_feature` naming the key;
that refusal is the discipline, and claiming the unit is not required to make
it conforming. `message` is gated by `session.message.submit` rather than by
this unit's key, so an endpoint may admit a message at open while advertising
no subscription there at all.

### `+steer` And `+btw`

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

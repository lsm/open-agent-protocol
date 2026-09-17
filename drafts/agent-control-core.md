# Open Agent Protocol Agent Control Core

Status: draft
Profile ID: `open-agent-protocol.agent-control-core`
Base protocol: `open-agent-protocol` version `0.1`
License: CC0-1.0 public domain dedication, or the nearest legally valid equivalent in jurisdictions that do not recognize public domain dedication.
Scope: the 80/20 agent-control boundary between a control layer and an agent
loop or agent-control endpoint.

This profile is the first conformance target. It defines the smallest useful
surface for a control layer to discover an agent-control endpoint, open a session,
submit messages, stream a run timeline, cancel work, recover state, and receive
a terminal result.

The broader [Agent Control Profile](agent-control-profile.md) is a staging area for
richer controls at the same boundary. Those controls should not be required by
the core unless they prove necessary across multiple independent control layers
and agent-control implementations.

Profile and conformance-unit claims are described in the
[Conformance Draft](conformance.md). The implemented v0.1 subset is frozen by
[Decision 0001](../decisions/0001-agent-control-v0.1-executable-core.md).

## Design Rule

Core means common path, not complete power.

The core should cover:

- one control layer talking to one agent loop or agent-control endpoint;
- one session containing one or more runs;
- user message submission and run admission;
- streamed assistant text and reasoning summaries when available;
- canonical session state and transcript sync after reconnect;
- cancellation;
- final response, usage, and errors;
- transport-agnostic envelopes.

Common optional units add tools, permissions, user-input prompts, model listing,
persistence, queue/steer delivery, and side questions. They are part of the
same protocol family but not required for minimum core conformance.

The core should not require:

- checkpointing, rewind, branch, or fork;
- artifact stores;
- auth login flows;
- model catalog discovery;
- tool catalogs or tool execution events;
- permission prompts;
- user-input prompts that pause an agent turn;
- queued, steering, or side-question delivery;
- external tool-source management;
- subagents, hooks, or background tasks;
- workspace sandbox policy;
- arbitrary query languages or implementation-specific live-query APIs;
- provider-native model stream events.

## Core Envelope

Core events use a flat envelope. It keeps the identifiers the adjacent layers
need directly on the event instead of requiring a separate trace object.

Required fields:

- `protocol`: fixed string `open-agent-protocol`.
- `version`: protocol version, currently `0.1`.
- `profile`: profile identifier, normally
  `open-agent-protocol.agent-control-core`.
- `type`: envelope type such as `session.message.submit.request`.
- `id`: opaque envelope ID.
- `payload`: type-specific payload object.

Optional fields:

- `sequence`: scoped ordering number.
- `timestamp_ms`: sender timestamp in Unix milliseconds.
- `in_reply_to`: original request ID for correlated responses.
- `session_id`: session scope ID when applicable.
- `run_id`: run scope ID when applicable.
- `turn_id`: turn scope ID when applicable.
- `tool_call_id`: tool-call scope ID when applicable.
- `capability_revision`: opaque revision of the endpoint capability snapshot.
  Its meaning depends on envelope direction as defined in Capability Snapshot
  Freshness below.
- `extensions`: extension object for non-core fields.

The flat envelope is normative for v0.1. Nested `scope` or `trace` objects do
not substitute for the direct scope fields. OAP identifiers are opaque but
belong to distinct domains: envelope, participant, endpoint, session,
submission, run, message, tool call, and interaction IDs are not
interchangeable. `in_reply_to` references an envelope request ID only. When a
scope ID appears in both the envelope and payload, the values must agree.
Native identifiers may be retained under namespaced `extensions`, but do not
become portable OAP identities.

Every run-scoped event carries a positive, contiguous `sequence` in one ordering
domain for its `run_id`. Request and response envelopes do not consume that
sequence. Deltas append; snapshots replace the state they identify; terminal
values are final.

Contiguity is an ordering guarantee at the OAP boundary, and nothing more. It is
not a loss detector. The endpoint generates the numbers itself, from the order in
which it emits events, so an unbroken run of them proves only that a consumer
holds every event the endpoint emitted, in emission order. It proves nothing
about production order or completeness below that boundary: when the agent loop
drops, coalesces, reorders, or never surfaces something, the endpoint never
numbers it, and the sequence stays contiguous anyway. A gap therefore does not
appear when the source lost something — a gap means the *transport* lost
something, and that is the only loss the numbering can witness. Consumers must
not read contiguity as evidence that nothing was lost upstream, and must not
wait for a gap that will not come. Fidelity below the boundary is reported
through capabilities (`native`, `emulated`, `degraded`, `unavailable`) and
through the endpoint's own typed diagnostics; it is never inferred from the
numbering.

## Transport

The core is transport agnostic. The same envelopes can move over in-process
calls, JSONL over stdio, WebSocket messages, HTTP plus server-sent events, or
another binding.

Bindings must preserve event type, IDs, scoped ordering, request correlation,
`capability_revision`, payload meaning, terminal event rules, and extension
fields. Connection setup, heartbeats, reconnection, batching, authentication
handshakes, and backpressure are binding concerns.

## What This Protocol Is Not

The scope line above says what this profile covers. This section says what it
excludes, because an unstated boundary reads as an unfilled gap, and an
implementer deciding whether they can move entirely onto OAP needs the
difference.

**Direct model inference is not carried on this wire.** A call that sends a
prompt to a model and streams tokens back — no agent loop, no turns, no tools,
no run — is not agent control, and no envelope here carries it. Every execution
path begins at a session and a submission and mints a run, because a run is the
thing whose lifecycle there is anything to normalize.

This is a statement about the wire, not about the layers. The model provider is
one of OAP's own layers, named as such in this repository's README beside the
control layer, the agent loop and the tool executor, and that README says those
layers may live in one process. An endpoint that speaks to a vendor API
directly — normalizing the OpenAI and Anthropic request and streaming shapes
behind one agent loop, with no third-party harness in between — is an ordinary
OAP endpoint. It has a session, runs and tools like any other, and how it
obtains its tokens is below this boundary and nobody else's business.

What is excluded is exposing that inference call *through* OAP to the control
layer as its own operation. The distinction matters because the two get
confused: a provider layer under an endpoint is in scope by the README's own
model, and a `provider.complete` envelope on the control wire is not.

The reason is what OAP is for. Eight harnesses disagree about terminals,
sequences, cancellation and admission, and this profile exists to make those
comparable. They do not disagree about inference: the vendor wire formats
already settled it, and this repository models that separately and
deliberately in `provider/`, as compatibility surfaces rather than protocol.
A profile that grew to cover inference would be re-normalizing something
already normalized, and would have to answer what run identity means for an
execution with no agent semantics — a question with no good answer.

The practical consequence, stated plainly so nobody discovers it late: **a
harness whose wire exposes direct provider access to its clients cannot move
that surface onto OAP.** It runs OAP for the agent boundary and keeps its own
surface, or a vendor's, for direct inference. That is a boundary rather than a
shortfall, and it is deliberate.

It constrains what the wire exposes and not what an endpoint is built on. An
implementer free to drop their passthrough surface, or one who never had it,
can put the provider layer under an OAP endpoint and have no second wire at
all.

Emulating inference as a tool-less single-submit session is possible and is not
recommended. It buys a session and a run the caller did not want, and the
stream it produces does not resemble what an inference API promises.

**Transport-level authentication is out of scope**, and is named in Bindings
above as a binding concern: how a control layer proves itself to an endpoint
belongs to the transport that carries them. ACP's `authenticate` and
OpenCode's daemon password are both this kind.

Whether *provider* credential acquisition belongs here is a separate and open
question — it is agent-loop state, not transport state — and no unit covers it
today. It has **two** open cases, not one, and a unit designed for the first
leaves the second homeless:

- **Reactive.** A run blocks because a credential is missing or expired. This
  is the case the constraints below are drawn from.
- **Proactive.** A user authenticates with nothing running: no session, no
  run, no blocked execution. In the one harness that exposes this natively it
  is a distinct identity domain — a flow id, with no session or run appearing
  anywhere in the exchange — and it is the case users hit first, because you
  log in and then start working.

Proactive acquisition arrives with provider *discovery*, because they are one
surface: an endpoint that tells a client which providers exist and which are
usable has told it what to authenticate against, and a client that can see it
is logged out and cannot act on it is worse served than one told nothing.
Discovery of provider identity is [Decision 0014](../decisions/0014-provider-descriptors.md);
the usability half is not, for the reason that record gives — a descriptor is
fixed for a capability revision and an auth state is not.

**The usability surface and the waiting condition are one object seen from two
angles, and a unit that treats them as two mechanisms will have to reconcile
them.** The harness that exposes this natively has a seven-value status enum,
and two of those values — refreshing, and login in progress — describe an
operation in flight rather than a property of a provider. That is why they
cannot live on a descriptor: a cached one would report a transition that has
already finished. But it is also what makes the surface load-bearing. A client
asking whether it can use a provider and learning that a login is already
running knows to wait on that flow rather than start a second one, which is
the endpoint-scoped coalescing rule showing at the discovery layer instead of
only inside the waiting layer.

The consequence is a design constraint rather than an observation. If a client
can observe that an acquisition is in flight, it can wait on it; if it cannot,
every client races to start its own and the endpoint is left collapsing them
silently. The harness above does the silent collapse today because its status
surface and its refresh lock were built years apart and never told each other
anything — which is the outcome a unit designing the two separately would
reproduce.

Two facts about expressibility, since the proactive case looks harder than it
is and is harder in a different place than it looks. Session-less *queries* are
already precedented: `capabilities.request` carries no session at all and
`action.tools.list.request` makes one optional for an endpoint-wide listing, so
enumerating providers without a session invents nothing. What has no precedent
is a session-less *stateful exchange* — every multi-turn thing in v0.1 hangs off
a run through an interaction — so a flow identity that outlives no session and
belongs to no run would be the protocol's third identity domain. That is the
part worth designing rather than assuming. No adapter in this repository has a credential path at all; an expired
provider credential becomes `run.failed` like any other provider error.

The evidence is one harness, and it is the weaker kind of one. Seven others
have not asked for this, which is disinterest rather than the suppression that
carried `+control-tools` — there is no empty array anywhere to point at, no
integration declining the capability because the protocol had nowhere to put
it. Under [Decision 0015](../decisions/0015-evidence-from-implementations-we-do-not-control.md)
that cannot carry a unit alone, and it would not become able to by the
implementation becoming first-party.

The constraints below are recorded anyway, because they are what a second
harness's arrival would otherwise cost someone to rediscover:

- **The waiting condition is endpoint-scoped, not run- or session-scoped.** One
  run blocks on exactly one provider, because a run carries a single model
  reference and nothing inside it introduces a second. The sharing is entirely
  across runs, and across sessions: Makai's refresh lock is one object per
  process keyed on provider and user, so N runs in M sessions wait on the same
  thing. This is why the pattern of the three existing resolve pairs does not
  fit. An interaction carries a `run_id`, is resolved once by its declared
  responder, and never outlives its run; two runs blocked on one expired
  credential is one real-world event that model can only express as two, which
  is why every implementation needs a coalescing rule to put it back together.
- **An abandoned acquisition must not wedge the endpoint.** A timed-out entry
  is recovered rather than poisoned, so the next acquirer starts a fresh
  attempt instead of inheriting a dead one. Whatever shape this takes, that
  property belongs in the rule: a login nobody finished should not disable the
  credential for the endpoint's lifetime.
- **Abandonment and failure are indistinguishable to a waiter**, after a
  bounded wait. That is a choice a unit would have to name rather than inherit.
  The existing bound is 30 seconds, chosen for non-interactive token refresh
  and never tuned against a human completing a browser flow; it is evidence
  that a bound is needed, not evidence of what it should be.

## Request And Stream Semantics

Core does not require a transport-level RPC mechanism. It does require semantic
request/response correlation for every request initiated across the
agent-control boundary.

A successful command or query request must receive exactly one correlated
`*.response` envelope. A negative acknowledgement must receive exactly one
correlated `error.response` envelope carrying a typed `ProtocolError`. In both
cases, the response envelope sets `in_reply_to` to the original request ID.

Those response envelopes may be delivered by a direct in-process return value,
on the same JSONL or WebSocket stream as other envelopes, through HTTP response
bodies, over SSE, or by another binding-specific channel. The protocol does not
require JSON-RPC or any other wire RPC shape.

Stream events are separate. Events such as `session.state.updated`,
`run.started`, `run.status.updated`, `content.delta`, and terminal run events
describe lifecycle and output after acceptance. They do not replace the
correlated response to a command or query.

## Minimum Core Surface

Every row in this table is part of minimum core conformance. Optional feature
units such as tools, permissions, user-input prompts, model listing, persistence,
queue, steer, and `btw` are intentionally excluded from this table.

Kinds:

- `command`: asks the agent loop or endpoint to change state or perform work;
- `query`: asks the endpoint to return current negotiated or session state;
- `event`: endpoint-emitted lifecycle, state, or stream output.

| Kind | Core envelope type | Correlated response | Minimum requirement |
| --- | --- | --- | --- |
| command | `protocol.initialize.request` | `protocol.initialize.response` | Negotiate protocol version, profile, and endpoint identity. |
| query | `capabilities.request` | `capabilities.response` | Return revisioned effective capabilities and degradation records for control-layer gating. |
| command | `session.open.request` | `session.open.response` | Open a new or existing session and return its stable `session_id`. |
| query | `session.state.request` | `session.state.response` | Return canonical state for reconnect and recovery. |
| command | `session.message.submit.request` | `session.message.submit.response` | Accept a user-visible message submission and report admission. |
| command | `run.cancel.request` | `run.cancel.response`, or `error.response` if declared unavailable | Accept cancellation or explicitly reject it with a typed unsupported-feature error. |
| event | `session.state.updated` | not a response | Emit when canonical session state changes. |
| event | `run.started` | not a response | Emit when admitted work starts executing as a run. |
| event | `run.status.updated` | not a response | Emit for meaningful run lifecycle changes. |
| event | `content.delta` | not a response | Stream assistant-visible output. |
| event | `run.completed`, `run.failed`, or `run.cancelled` | not a response | Emit exactly one terminal event for every accepted run. |

Each accepted run must end with exactly one terminal run event:
`run.completed`, `run.failed`, or `run.cancelled`. These are the complete v0.1
terminal vocabulary. `run.orphaned` requires a later negotiated revision.
Background or session activity that outlives foreground completion must not be
attributed to the terminal run.

## Core Data Shapes

### Message

Messages are role plus content. `content` may be a string for simple cases or a
list of content parts for structured input/output.

Core roles:

- `system`
- `developer`
- `user`
- `assistant`
- `tool`

Core content parts:

- `text`
- `reasoning`
- `image`
- `tool_call`
- `tool_result`

Audio, arbitrary files, artifact references, citations, and provider-native
parts belong in richer profiles or extension fields.

### Message Submit And Run Admission

`session.message.submit.request` submits user-visible messages to a session. If
the session is idle, the agent loop normally admits the submission by starting a
run. If the session is already active, the requested delivery mode tells the
agent loop how the control layer wants the message handled.

Requested delivery values:

- `auto`: resolve delivery authoritatively at admission time from current
  session state, capabilities, and configured policy;
- `queue`: run the message after current work reaches a safe boundary;
- `steer`: inject guidance into active work at a safe boundary;
- `btw`: answer a lightweight side question in the same environment and
  configuration without blocking the main run.

`auto` is a resolution policy, not a concrete delivery outcome. It exists so a
caller does not need to derive delivery from session state that may be stale by
the time the request is admitted. An idle session normally resolves `auto` to
`start`. An active session may resolve it to `queue`, `steer`, or `btw`
according to authoritative configuration and supported capabilities.

Core conformance requires `auto`. The executable v0.1 subset permits one
nonterminal foreground run per session and resolves `auto` to `start` only.
`queue`, `steer`, and `btw` are optional and must be advertised through
capabilities before a control layer depends on them. An implementation that
receives an unsupported explicit delivery mode should return a typed
unsupported-feature error rather than silently treating it as another mode.

Core fields:

- `session_id`
- `messages`
- `delivery`
- `model_id`
- `instructions`
- `tool_choice`
- `output_schema`
- `allow_degraded_features`
- `metadata`

`model_id`, `instructions`, `tool_choice`, `output_schema`, and non-`auto`
delivery modes are capability gated. An implementation that cannot honor them should
report the feature as degraded or unavailable.

The gate is fail-closed and the refusal is typed. A control an endpoint has
not affirmatively advertised under its key — `run.model_selection`,
`run.instructions`, `run.tool_selection`, `run.structured_output` — is refused
before admission with `unsupported_feature`, `details.feature`, and
`details.reason: "unadvertised"`; no submission or run identity is allocated.
A control advertised `degraded` needs its key in `allow_degraded_features`,
else `capability_degraded`. A control whose value cannot be honored is refused
as `unsatisfiable`, except a `model_id` outside the effective catalog, which
is `model_not_found` with `details.model_id`. A control is never accepted and
ignored, and presence is what the gate judges: a present-but-empty control is
a control.
[Decision 0005](../decisions/0005-run-controls.md) graduates that discipline
for all four controls and the execution of `model_id`, which is applied to the
run it was requested for and reported on the admission and on `run.started`;
under `run.model_selection`'s `per_run` mode it leaves `current_model_id`
unchanged. `instructions`, `tool_choice`, and `output_schema` have frozen
shapes and reference-adapter execution, with native evidence pending: each
becomes executable by an amendment to that decision when an adapter advertises
its key against a pinned ledger.

`tool_choice` is a policy over tools already exposed by the endpoint. The core
submit request does not mean the control layer normally provides executable tool
definitions: control-layer-provided tools stay in the staged `+control-tools`
unit. Attaching a tool *source* at session open is executable under
`+tool-sources` (Decision 0008), which describes and attaches a source the
harness runs; per-run attachment remains staged.

The endpoint must answer an accepted `session.message.submit.request` with
`session.message.submit.response` before or alongside the stream. The response
returns a `submission_id`, `requested_delivery`, `effective_delivery`, the
admission result, accepted message IDs when available, the effective `model_id`
when known, and a `run_id` when the submission starts, queues, steers, or
side-starts a run.

`requested_delivery` repeats the request value. `effective_delivery` is always
the concrete admitted behavior: `start`, `queue`, `steer`, or `btw`; it must
never be `auto`. When the request uses `auto`, the response may include
`delivery_resolution` explaining the choice, such as `session_idle` or
`configured_default`. This lets a control layer reconcile stale state, track
optimistic input, correlate retries, and recover if the stream connection
reconnects before `run.started` arrives.

Core run statuses are `queued`, `running`, `waiting_for_input`, `cancelling`,
`completed`, `failed`, and `cancelled`. The executable subset uses `running`,
`waiting_for_input`, `cancelling`, and terminal states; `queued` belongs to the
optional queue unit. `run.status.updated` should be emitted for meaningful
lifecycle changes so control layers and presentation layers can track status
without inferring it from provider-specific events.

`run.cancel.response` acknowledges cancellation intent; it is not terminal.
Only authoritative settlement emits `run.cancelled`. Completion or failure may
win a race with cancellation. Repeated cancellation must be idempotent, and a
stale cancellation must not affect a later run.

### Session State And Transcript Sync

The core separates live stream events from canonical state.

`session.state.request` returns the current session state:

- `session_id`
- `status`
- `active_run_id`
- `current_model_id`
- `transcript_cursor`
- `updated_at_ms`
- `metadata`

`session.state.updated` broadcasts the same shape when status changes. Core
session statuses are `idle`, `queued`, `running`, `waiting_for_input`, `closed`,
and `error`.

When persistence is advertised, `transcript.load.request` loads persisted
messages for initial history, pagination, or reconnect recovery. A response
should include `sync_cursor` when the endpoint can provide one.
`transcript.delta` is the optional canonical update event for persisted
transcript rows; it is distinct from `content.delta`, which is the live
assistant stream and may arrive before persistence.

Resume, reconciliation, and replay are separate capabilities. Resume restores
an attachment to execution or conversation state. Reconciliation returns an
authoritative state snapshot. Replay returns historical canonical OAP events
from a cursor. A transcript reconstructed after resume is not event replay.
When a requested replay cursor cannot be satisfied, an implementation must
report an explicit gap and return authoritative state rather than silently
claim continuity. The reference implementation's bounded process-memory journal
is degraded replay, not durable persistence.

The core does not define a generic live-query language. A binding or
implementation can offer one, but control layers should not need it for the
common chat/session surface.

### Tools

Tools are optional. When an implementation claims `+tools`, it reports the effective
tool catalog to the control layer through capabilities or
`action.tools.list.response`, then emits tool lifecycle events as calls are
selected and executed.

Core tool definitions use JSON Schema input:

- `name`
- `description`
- `input_schema`
- `execution_owner`, the participant that executes this tool
- `annotations`
- `source`, the id of the `ToolSourceDescriptor` the tool comes from
  (`+tool-sources`, Decision 0008) — never an inline copy of the descriptor,
  so a consumer attributes a tool to an MCP server without parsing its name
- `features`, this one tool's effective support map

`execution_owner` is required on every catalog entry and repeated on every
`action.call.*` payload. It names the participant that runs the call, which is
a different question from the three identities beside it: `requested_by` is who
asked for the call, `responded_by` is who resolved the interaction gating it,
and `source` is where the tool came from. Owner and source in particular do not
collapse into each other — a tool an endpoint bridges from an MCP server has
that server's descriptor as its `source` and the endpoint as its
`execution_owner`, because the endpoint is what the control layer calls and what
answers for the result.

In core the endpoint hosts every tool, so every entry and every call names the
endpoint's own participant id, and a control layer can read the field without
special-casing: one value across the catalog means one party executes
everything. It is still carried rather than implied, because the field is what
makes the mixed case expressible at all, and because a consumer should not have
to know which profile produced a trace to know who ran a call. The mixed case —
tools the control layer supplies and executes itself, so a catalog holds more
than one owner and a call routes by it — is the staged `+control-tools` unit and
is not part of core. A call's `execution_owner` names the owner of the catalog
entry it resolves to; a call claiming an owner the catalog does not give that
tool is not a call the endpoint should honor.

A catalog that carries sources declares them beside its tools, in
`action.tools.list.response.sources` and in the capability descriptor. A source
`id` is unique across a session's catalog and a tool `name` is unique whatever
its source, so a policy, a call, and an attribution each resolve to one entry.
Attaching a source at session open is `action.tool_sources.attach`; the
attachment shape is the only one carrying `command`, `args`, and `environment`,
and a published source never carries them.

The core does not define how tools are hosted. An implementation may execute tools
natively, call out to another process, or bridge an external tool system.
What matters to the control and presentation layers is the effective catalog
and the normalized `action.call.*` lifecycle, not the private hosting mechanism.

### Permissions

Permission prompts are explicit correlated interactions. They carry a stable
`interaction_id`, `requested_by`, `responded_by`, `session_id`, and `run_id`.
A control layer resolves them by sending `action.permission.resolve.request`
with:

- `interaction_id`
- `interaction_id`
- `choice_id`
- `granted`
- `reason`
- `updated_arguments_json`

Only the declared responder may resolve an interaction, and every interaction
has at most one resolution. Tool descriptors and calls identify their execution
owner. Permission and user-input interactions remain distinct even though they
share ownership and correlation rules.

`updated_arguments_json` covers the common case where the control or policy layer
allows a tool call only after narrowing or rewriting its arguments.

### User Input

User-input prompts are separate from permission prompts. Permission asks whether
an operation may proceed; user input asks the person for information the agent
needs to continue.

`user.input.requested` carries:

- `interaction_id`
- `interaction_id`
- `requested_by`
- `responded_by`
- `session_id`
- `run_id`
- `tool_call_id`
- `title`
- `description`
- `questions`
- `allow_cancel`
- `draft_answers`

Each question may be plain text, single choice, or multi choice. A control layer
submits answers through `user.input.resolve.request` or cancels through
`user.input.cancel.request`. Implementations that persist drafts may expose that
through extensions or a richer profile; draft persistence is not required for
core conformance.

### Completion

`run.completed` should include:

- `final_response`
- `stop_reason`
- `usage`
- `duration_ms`

This makes non-streaming consumers and history views useful without replaying the
whole stream.

## Core Capability Keys

Minimum features:

- `protocol.initialize`
- `capabilities`
- `session.state`
- `session.open`
- `session.message.submit`
- `session.message.delivery.auto`
- `run.streaming`
- `run.status`
- `run.cancel`

Common optional core features:

- `endpoint.status`
- `capabilities.updates`
- `models.list` (executable; [Decision 0006](../decisions/0006-models-catalog.md))
- `session.list`
- `session.open.subscribe` (executable; [Decision 0009](../decisions/0009-compound-open.md))
- `transcript.load`
- `transcript.delta`
- `session.message.delivery.queue`
- `session.message.delivery.steer`
- `session.message.delivery.btw`
- `run.model_selection`
- `run.instructions`
- `run.tool_selection`
- `run.structured_output`
- `content.reasoning`
- `content.image`
- `action.tools.list`
- `action.tool_sources.attach`
- `action.tools.execute`
- `action.tools.progress`
- `action.permissions`
- `user_input`

Support levels are:

- `native`
- `emulated`
- `degraded`
- `unavailable`

A descriptor may additionally state semantic fidelity such as submission
receipt type, streaming level, cancellation scope, resume/reconciliation/replay
level, approval scopes, maximum active runs per session, and unknown-event
handling. Boolean feature flags are insufficient when those guarantees differ.
The selected native surface and effective capability revision must remain fixed
for an admitted run.

The control layer should enable controls from capabilities, not from
implementation names.

## Capability Snapshot Freshness

`capabilities.response` is an authoritative snapshot for the endpoint identified
by its descriptor. It must set `capability_revision` to an opaque, non-empty
string. A revision identifies the complete effective descriptor, including
degradation records and catalogs embedded in it. Consumers compare revisions
for equality and must not infer ordering or parse their contents.

A static implementation may use one revision for its lifetime. Core does not
require capabilities to change dynamically.

Any request other than `protocol.initialize.request` or `capabilities.request`
may set `capability_revision` to the revision the sender used when constructing
the request. When supplied, it is an exact precondition. If it does not equal
the endpoint's current revision, the endpoint must reject the request with
`error.response` and code `stale_capabilities`; the error details must include
`expected_revision` and `current_revision`. The sender then obtains a fresh
`capabilities.response`, updates its gates, and decides whether to retry.

Initialization and capability discovery are never revision-gated. A receiver
must ignore `capability_revision` if it appears on either request, so a stale
sender can always initialize or obtain a fresh snapshot.

If any other request omits `capability_revision`, the endpoint evaluates it
against the current capabilities and applies the normal unsupported or degraded
feature rules. A successful response to a pinned request must repeat the
revision used for admission. A successful response to an unpinned request
should set the revision used for admission. This lets the control layer detect
that an unpinned request was admitted under a newer snapshot.

An implementation with dynamic capabilities may advertise
`capabilities.updates`. When advertised, it emits `capabilities.updated` after
the effective descriptor changes. The event sets the envelope's
`capability_revision` to the new revision and carries `previous_revision` plus
an optional `reason` in its payload. It is an invalidation notice, not a
descriptor delta. The receiver obtains the new snapshot with
`capabilities.request`. Dynamic updates and their event are not required for
core conformance.

## Minimum Conformance

An implementation is core-conformant if it can:

1. initialize a connection;
2. return a revisioned capability descriptor;
3. open a session;
4. return canonical session state;
5. accept a message submit and return `session.message.submit.response`;
6. stream assistant text through `content.delta`;
7. emit run status updates for meaningful lifecycle changes;
8. end every accepted run with one terminal run event;
9. cancel a running run or report cancellation as unavailable;
10. return correlated `error.response` envelopes for unsupported commands and
    invalid requests.

A core implementation must reject a request carrying a stale
`capability_revision` with the typed `stale_capabilities` error, except for the
two bootstrap requests that explicitly ignore it. It does not need to support
dynamic capability updates.

Core conformance only requires `auto` delivery. If a control layer asks for
`queue`, `steer`, or `btw` and the endpoint did not advertise that mode, the
implementation must return a correlated `error.response` with a typed
unsupported-feature error.

If an implementation supports tools, it must also emit `action.call.requested`,
`action.call.started`, and a terminal action event. If it supports permission
prompts, it must use the `action.permission.*` events instead of inventing a
private callback.

If an implementation supports user-input prompts, it must use `user.input.*`
events instead of encoding those prompts as permission requests.

## Richer Controls

These are deliberately outside the core for now:

- auth provider listing and login flows;
- model resolve, model metadata caching, and provider auth state;
- session rename, archive, delete, and metadata patching;
- checkpoint, rewind, branch, fork, and file rollback;
- artifact creation, retrieval, hashing, and persistent stores;
- workspace, cwd, environment, sandbox, and network policy;
- runtime tool-source attach and detach, and tool-source management of any
  kind — describing and attaching a source at session open is executable
  under `+tool-sources`, but nothing in this protocol manages one;
- control-layer-provided tool definitions;
- hooks, subagents, background tasks, and task notifications;
- telemetry, cost accounting, rate-limit events, and retry detail;
- context compaction controls;
- full provider-native model stream passthrough;
- durable cross-process replay and full stream-convergence protocols.

Each of these can become an optional profile once the core event model feels
right.

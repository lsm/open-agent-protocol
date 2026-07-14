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
[Conformance Draft](conformance.md).

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
- `extensions`: extension object for non-core fields.

## Transport

The core is transport agnostic. The same envelopes can move over in-process
calls, JSONL over stdio, WebSocket messages, HTTP plus server-sent events, or
another binding.

Bindings must preserve event type, IDs, scoped ordering, request correlation,
payload meaning, terminal event rules, and extension fields. Connection setup,
heartbeats, reconnection, batching, authentication handshakes, and backpressure
are binding concerns.

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
| query | `capabilities.request` | `capabilities.response` | Return effective capabilities and degradation records for control-layer gating. |
| command | `session.open.request` | `session.open.response` | Open a new or existing session and return its stable `session_id`. |
| query | `session.state.request` | `session.state.response` | Return canonical state for reconnect and recovery. |
| command | `session.message.submit.request` | `session.message.submit.response` | Accept a user-visible message submission and report admission. |
| command | `run.cancel.request` | `run.cancel.response`, or `error.response` if declared unavailable | Accept cancellation or explicitly reject it with a typed unsupported-feature error. |
| event | `session.state.updated` | not a response | Emit when canonical session state changes. |
| event | `run.started` | not a response | Emit when admitted work starts executing as a run. |
| event | `run.status.updated` | not a response | Emit for meaningful run lifecycle changes. |
| event | `content.delta` | not a response | Stream assistant-visible output. |
| event | `run.completed`, `run.failed`, or `run.cancelled` | not a response | Emit exactly one terminal event for every started run. |

Each started run must end with exactly one terminal run event:
`run.completed`, `run.failed`, or `run.cancelled`.

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

Delivery modes:

- `auto`: let the agent loop choose the normal behavior for the current state;
- `queue`: run the message after current work reaches a safe boundary;
- `steer`: inject guidance into active work at a safe boundary;
- `btw`: answer a lightweight side question in the same environment and
  configuration without blocking the main run.

Core conformance requires `auto`. `queue`, `steer`, and `btw` are optional and
must be advertised through capabilities before a control layer depends on them.
An implementation that receives an unsupported explicit delivery mode should
return a typed unsupported-feature error rather than silently treating it as
another mode.

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

`tool_choice` is a policy over tools already exposed by the endpoint. The core
submit request does not mean the control layer normally provides executable tool
definitions. Control-layer-provided tools or per-run tool-source attachment
belong in a richer optional unit.

The endpoint must answer an accepted `session.message.submit.request` with
`session.message.submit.response` before or alongside the stream. The response
returns a `submission_id`, the `effective_delivery`, the admission result,
accepted message IDs when available, the effective `model_id` when known, and a
`run_id` when the submission starts, queues, steers, or side-starts a run. This
lets a control layer track optimistic input, correlate retries, and recover if
the stream connection reconnects before `run.started` arrives.

Core run statuses are `queued`, `running`, `waiting_for_input`, `cancelling`,
`completed`, `failed`, and `cancelled`. `run.status.updated` should be emitted
for meaningful lifecycle changes so control layers and presentation layers can
track status without inferring it from provider-specific events.

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
- `annotations`

The core does not define how tools are hosted. An implementation may execute tools
natively, call out to another process, or bridge an external tool system.
What matters to the control and presentation layers is the effective catalog
and the normalized `action.call.*` lifecycle, not the private hosting mechanism.

### Permissions

Permission prompts are explicit events. A control layer resolves them by
sending `action.permission.resolve.request` with:

- `permission_id`
- `choice_id`
- `granted`
- `reason`
- `updated_arguments_json`

`updated_arguments_json` covers the common case where the control or policy layer
allows a tool call only after narrowing or rewriting its arguments.

### User Input

User-input prompts are separate from permission prompts. Permission asks whether
an operation may proceed; user input asks the person for information the agent
needs to continue.

`user.input.requested` carries:

- `input_request_id`
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
- `models.list`
- `session.list`
- `transcript.load`
- `transcript.delta`
- `session.message.delivery.queue`
- `session.message.delivery.steer`
- `session.message.delivery.btw`
- `run.instructions`
- `run.tool_selection`
- `run.structured_output`
- `content.reasoning`
- `content.image`
- `action.tools.list`
- `action.tools.execute`
- `action.tools.progress`
- `action.permissions`
- `user_input`

Support levels are:

- `native`
- `emulated`
- `degraded`
- `unavailable`

The control layer should enable controls from capabilities, not from
implementation names.

## Minimum Conformance

An implementation is core-conformant if it can:

1. initialize a connection;
2. return a capability descriptor;
3. open a session;
4. return canonical session state;
5. accept a message submit and return `session.message.submit.response`;
6. stream assistant text through `content.delta`;
7. emit run status updates for meaningful lifecycle changes;
8. end every started run with one terminal run event;
9. cancel a running run or report cancellation as unavailable;
10. return correlated `error.response` envelopes for unsupported commands and
    invalid requests.

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
- external tool-source attachment and management;
- control-layer-provided tool definitions;
- hooks, subagents, background tasks, and task notifications;
- telemetry, cost accounting, rate-limit events, and retry detail;
- context compaction controls;
- full provider-native model stream passthrough;
- generated JSON Schema bundles and conformance test suites.

Each of these can become an optional profile once the core event model feels
right.

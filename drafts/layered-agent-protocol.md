# Open Agent Protocol Layered Draft

Status: draft
Protocol ID: `open-agent-protocol`
License: CC0-1.0 public domain dedication, or the nearest legally valid equivalent in jurisdictions that do not recognize public domain dedication.
Scope: a shared semantic protocol for agent software layers, agent loops,
model providers, tool executors, execution resources, and independent agent
tooling.

This document defines the portable layered model behind Open Agent Protocol. It
is not a project-specific wire protocol, a project-specific UI API, a
deployment-side model, or a replacement for existing provider, tool, or
agent-control protocols. The first practical conformance target is the
[Agent Control Core](agent-control-core.md). The broader
[Agent Control Profile](agent-control-profile.md) uses these layers as its
source model.

## Goals

The protocol should:

- standardize semantic boundaries between presentation, control, agent loop,
  model provider, action/tool executor, resource provider, and binding layers;
- normalize provider SDKs, agent SDKs, CLI-backed agents, hosted agents, local
  agents, and tool-backed agents without hiding important differences;
- separate presentation intent, control policy, agent loop semantics,
  tool/action semantics, model IO semantics, and transport framing;
- expose capabilities and degradation explicitly at each boundary so adjacent
  layers can disable, label, reroute, or degrade features safely;
- be usable in-process, over stdio, over WebSocket, over HTTP SSE, and through
  other transport bindings;
- let implementers adopt the profiles that match their integration point
  without reinventing the rest.

Non-goals:

- internet-scale agent identity and discovery;
- forcing all implementations onto one transport;
- replacing existing provider, tool-host, or agent-control protocols;
- requiring compatibility with any specific external protocol;
- requiring every agent loop to support checkpoints, branching, external tool
  hosts, or visible reasoning.

## Repository Artifacts

This prose draft explains the protocol semantics. Related profile drafts and
complete JSON examples live beside it:

- [Conformance draft](conformance.md)
- [Agent control core profile](agent-control-core.md)
- [Agent control profile](agent-control-profile.md)
- [Core run stream example](../examples/core-run-stream.json)
- [Agent capability example](../examples/agent-capabilities.json)
- [Degraded adapter capability example](../examples/degraded-adapter-capabilities.json)
- [Tool-source example](../examples/tool-source.json)
- [Agent-control run stream example](../examples/agent-control-run-stream.json)

## Layer Model

OAP layers are logical software layers, not deployment sides. A caller and
callee may be two functions in one process, two modules linked through an SDK,
two processes over stdio, or two services over WebSocket or HTTP. The same
profile should describe the boundary in each deployment.

The main logical layers are:

| Layer | Responsibility | Typical adjacent profile |
| --- | --- | --- |
| Presentation layer | GUI, TUI, editor, web, or dashboard surfaces that render state and capture intent. | Presentation-control |
| Control layer | Policy, orchestration, capability gating, model/tool configuration, request correlation, and routing. | Agent-control |
| Agent loop layer | Orchestrates model calls, parses model outputs, selects actions, coordinates tools/resources, and owns run/turn progression. | Agent-control, model IO, action/tool, workspace, artifact, memory |
| Model provider layer | Model APIs, provider SDKs, hosted model services, and local model runtimes. | Model IO |
| Action/tool execution layer | Local tools, remote tools, process-backed commands, tool hosts, and approval-gated actions. | Action/tool |
| Resource provider layer | Workspaces, file systems, artifact stores, memory systems, context stores, and retrieval indexes. | Workspace, artifact, memory |
| Binding layer | Framing, serialization, ordering, transport, batching, reconnect, and backpressure. | Binding profiles |

Profiles define boundaries between layers. A single implementation may combine
several layers, but it should still preserve the boundary semantics when it
claims a profile.

Initial boundary profiles:

- `agent-control-core`: the first conformance target, covering the smallest
  useful control-layer to agent-loop boundary.
- `agent-control`: richer control-layer to agent-loop behavior.
- `presentation-control`: future profile for renderable state, user intent, and
  presentation-side configuration changes.
- `agent-loop`: future profile for pure loop engines that orchestrate model,
  action, workspace, artifact, and memory profiles.
- `model-io`: normalized model request, response, streaming, usage, reasoning,
  and model tool-call intent between an agent loop and a model provider.
- `action-tool`: normalized tool catalog, tool call, progress, permission, and
  result behavior between an agent loop and a tool/action executor.
- `workspace`: future profile for file-system, working-tree, environment, and
  sandbox operations.
- `artifact`: future profile for large or persistent outputs referenced from
  runs, messages, and tool calls.
- `memory`: future profile for durable memory, semantic retrieval, and context
  stores.
- `binding`: transport and framing profiles such as in-process, JSONL stdio,
  WebSocket, and HTTP/SSE.
- `control-plane`: cross-cutting discovery, capabilities, auth, and
  degradation records for every boundary.

### Presentation Layer

The presentation layer is human-facing.

It owns:

- rendering sessions, transcripts, run state, tool calls, permissions,
  artifacts, errors, and diagnostics;
- capturing user intent;
- presenting model, tool, permission, and agent configuration controls;
- accessibility, layout, keyboard behavior, and visual state.

It does not own:

- provider SDK normalization;
- agent-loop private state;
- tool execution;
- request/response correlation across the agent-control boundary;
- deciding unsupported or degraded agent behavior without capabilities.

### Control Layer

The control layer turns user or system intent into protocol operations.

It owns:

- capability gating;
- policy and orchestration;
- effective model, instruction, tool, permission, and delivery-mode decisions;
- request/response correlation;
- state recovery and reconnect behavior;
- routing to one or more agent loops or agent-control endpoints;
- mapping agent-control streams into presentation-ready state.

It does not own:

- rendering UI widgets;
- private loop memory structures;
- provider-native stream formats;
- tool-host internals.

### Agent Loop Layer

The agent loop layer is the core execution orchestrator.

It owns:

- constructing model requests from session state, instructions, tools, memory,
  workspace context, and user input;
- calling model providers through the model IO profile;
- parsing model responses, including structured tool-call intent;
- deciding which action/tool calls to execute;
- calling tools and actions through the action/tool profile;
- coordinating workspace, artifact, and memory operations through their
  resource profiles;
- advancing turns until the run completes, fails, pauses, or is cancelled.

It does not own:

- rendering presentation state;
- control-layer policy outside its advertised configuration;
- provider-specific model transport details;
- private tool-host execution mechanics;
- durable artifact or memory storage implementations unless it explicitly
  embeds those providers.

An agent loop can be a pure orchestration engine. It can have no native model
API, no native tools, no local workspace, and no private memory store, as long
as it can call the appropriate lower profiles and report effective capabilities
and degradation.

### Adapter Implementations

Adapters are an implementation pattern, not a semantic layer.

An adapter presents an OAP boundary over something that does not natively speak
OAP, such as an agent SDK, hosted agent API, CLI-backed agent, provider-specific
agent protocol, or legacy in-process loop. For example, a Codex SDK adapter or
Anthropic agent SDK adapter can implement the agent-control profile toward a
control layer while translating to the SDK's native calls underneath.

An adapter owns:

- stable OAP IDs for the boundary it implements;
- request/response and stream normalization;
- mapping native events into OAP events;
- capability descriptors and degradation records;
- preserving extension fields where possible.

An adapter must not pretend to add a new protocol layer. If the underlying SDK
does not expose tool catalogs, instruction override, model selection,
checkpoint identity, clean resume, workspace control, artifacts, or memory
state, the adapter reports that gap as unavailable, emulated, or degraded.

### Model Provider Layer

The model provider layer produces model responses for an agent loop.

Examples:

- hosted model APIs;
- provider SDKs;
- local model runtimes;
- REST or streaming response APIs;
- provider-specific tool-call JSON formats.

The model IO profile is the boundary between an agent loop and this layer. It
normalizes message input, content parts, streaming deltas, stop reasons, usage,
reasoning visibility, and model-emitted tool-call intent. It does not execute
tools.

### Action/Tool Execution Layer

The action/tool execution layer performs requested actions.

Examples:

- local function tools;
- process-backed tools;
- remote tool services;
- command execution;
- browser or editor actions;
- approval-gated operations.

The action/tool profile is the boundary between an agent loop and this layer.
It normalizes tool discovery, JSON Schema inputs, invocation, progress,
cancellation, permission requests, terminal results, and tool-produced artifact
references.

### Resource Provider Layer

Resource providers expose stateful execution context used by agent loops and
tools.

Examples:

- workspaces and file systems;
- working-tree state;
- sandbox and environment policy;
- artifact stores;
- memory systems;
- semantic retrieval indexes;
- context stores.

Workspace, artifact, and memory profiles should be separate from action/tool
execution. A tool may use these resources, but resource access should not have
to masquerade as a generic tool call when a more precise resource boundary is
needed.

### Binding Layer

The binding layer defines how messages are framed, serialized, ordered, and transported.
It carries no agent, tool, or model semantics.

Examples:

- typed in-process interfaces;
- JSON lines over stdio;
- WebSocket messages;
- HTTP SSE;
- host-specific envelopes that preserve protocol fields.

The protocol is transport agnostic. A binding may choose request/response,
bidirectional streams, server-sent streams, in-process calls, batching,
heartbeats, reconnect behavior, or backpressure mechanisms. Those choices must
not change event names, payload meaning, trace correlation, scoped ordering, or
terminal event rules.

Bindings should preserve:

- message ID;
- scope ID;
- sequence number where ordering matters;
- timestamp;
- reply correlation;
- payload type;
- extension fields.

### Agent Loop Profile

The agent-loop profile is a future boundary profile for loop engines that need
to be reused below agent-control endpoints or embedded into host applications.

It would own:

- loop configuration;
- turn stepping;
- model request construction;
- model response interpretation;
- action/tool dispatch decisions;
- pause, resume, cancellation, and error handling inside the loop;
- integration points for workspace, artifact, and memory providers.

It should not replace the model IO, action/tool, workspace, artifact, or memory
profiles. Those profiles are the boundaries an agent loop uses to do work.

### Model IO Profile

The model IO profile normalizes the boundary between an agent loop and a model
provider.

It owns:

- message roles;
- content parts;
- model descriptors;
- model capabilities;
- provider request options;
- streaming content events;
- provider stop reasons;
- token usage;
- provider-native tool-call requests.

It does not own:

- agent loop policy;
- executing local tools;
- agent turns;
- session checkpoints;
- presentation state.

### Action/Tool Profile

The action/tool profile normalizes the boundary between an agent loop and
tool/action executors.

It owns:

- tool discovery;
- JSON Schema parameters;
- tool invocation;
- tool progress;
- cancellation;
- terminal tool results;
- artifact references;
- permissions and approval requests;
- external tool-source attachment when supported.

It does not require all tools to come from one tool protocol. Tool-source
protocols are action/tool sources or bindings, not the whole agent-control
profile.

### Workspace, Artifact, And Memory Profiles

Workspace, artifact, and memory profiles are future resource boundaries used by
agent loops and tools.

They should stay separate because they have different semantics:

- workspace profile: file-system, working-tree, environment, sandbox, and
  command-context operations;
- artifact profile: large, binary, durable, or externally referenced outputs;
- memory profile: durable memory, semantic retrieval, summaries, embeddings,
  and context recall.

An implementation may expose a resource through a tool for convenience, but the
resource profile is the portable boundary when a loop needs predictable state,
permissions, identity, cursors, or retrieval semantics.

### Agent-Control Profile

The agent-control profile normalizes session, run, and turn semantics for the
control-layer to agent-loop boundary.

It owns:

- sessions;
- runs;
- turns;
- interrupt and resume;
- checkpoint, rewind, and branch when supported;
- context compaction;
- transcript lifecycle;
- aggregate run status and usage;
- mapping model streams and action executions into a single run timeline.

The agent-control profile may delegate model calls to model IO and tool calls
to action/tool profiles, but it is the only profile that defines run and turn
lifecycle.

### Control Plane

The control plane cuts across all layers and boundary profiles.

It owns:

- endpoint discovery;
- capability advertisement;
- version negotiation;
- auth/provider status;
- model catalog discovery;
- degradation reporting.

The control plane is intentionally separate from run streaming. An adjacent
layer should be able to ask "what can this layer do?" before issuing work.

## Core Envelope

Every binding should be able to represent the same logical envelope. Field
names are normative for the JSON binding.

Required fields:

- `protocol`: fixed string `open-agent-protocol`.
- `version`: protocol version, currently `0.1`.
- `type`: envelope type.
- `id`: opaque envelope ID.
- `scope`: primary scope for ordering and lifecycle.
- `payload`: type-specific payload object.

Optional fields:

- `sequence`: scoped ordering number.
- `timestamp_ms`: sender timestamp in Unix milliseconds.
- `in_reply_to`: original request ID for correlated responses.
- `trace`: cross-scope correlation IDs.
- `extensions`: extension object for non-core fields.

Scope records contain:

- `kind`: `connection`, `session`, `run`, `turn`, `model_stream`,
  `tool_execution`, `auth_flow`, or an extension value.
- `id`: opaque scope ID.

Trace records may contain:

- `session_id`
- `run_id`
- `turn_id`
- `model_stream_id`
- `tool_call_id`
- `parent_id`

ID rules:

- IDs are opaque non-empty strings.
- Bindings may choose stricter formats.
- New JSON bindings should prefer ULIDs for message, run, turn, model stream,
  tool execution, and auth-flow IDs.

Ordering rules:

- `sequence` is scoped, not global.
- A binding may omit `sequence` only when ordering is guaranteed by an
  in-process call stack or when the payload is explicitly unordered.
- A scoped stream must have exactly one terminal success, failure, or
  cancellation event unless the binding is forcibly disconnected.

Extension rules:

- Unknown fields must be ignored.
- Unknown enum values must be surfaced as strings when possible, not rejected.
- Extension fields should use reverse-DNS or package prefixes for public
  extensions, for example `com.example.foo`.

## Standard Content

Message roles:

- `system`
- `developer`
- `user`
- `assistant`
- `tool`

Content part kinds:

- `text`
- `reasoning`
- `image`
- `audio`
- `file`
- `tool_call`
- `tool_result`
- `artifact_ref`

Reasoning visibility is capability-controlled. An implementation that strips,
redacts, or summarizes reasoning must report that degradation. An adjacent
layer must not infer "reasoning unavailable" solely from the absence of
reasoning events.

Minimal content types:

- `text`: `text`, optional `text_signature`.
- `reasoning`: `text`, optional `signature`, optional `visibility`.
- `image`: inline `data` or `uri`, plus `mime_type`.
- `audio`: inline `data` or `uri`, plus `mime_type`.
- `file`: inline `data` or `uri`, optional `mime_type`, optional `name`.
- `tool_call`: `tool_call_id`, `name`, `arguments_json`.
- `tool_result`: `tool_call_id`, `name`, `content`, optional `is_error`.
- `artifact_ref`: `artifact_id`, optional `uri`, `mime_type`, `byte_size`,
  and `sha256`.

Reasoning visibility values:

- `full`
- `summary`
- `redacted`
- `none`

## Shared Primitive Records

Layer-specific events should reuse shared primitive records rather than define
one-off payload shapes. Shared primitives include:

- `ProtocolMessage`: role plus ordered content parts.
- `ModelDescriptor`: provider/model identity, modalities, context limits, and
  model feature support.
- `ToolDescriptor`: name, JSON Schema input, optional output schema, source,
  annotations, and tool-level features.
- `ToolSourceDescriptor`: native, local, process, remote, hosted, or extension
  tool source identity.
- `ArtifactRef`: externally stored output with URI, MIME type, size, and hash
  metadata when available.
- `PermissionRequestPayload`: approval request with explicit choices and a
  stable permission ID.
- `CheckpointRef`: restorable state identity, with room for message-index,
  transcript-snapshot, and state-snapshot implementations.
- `CapabilityDegradation`: cumulative feature loss from provider, adapter,
  agent loop, or binding transforms.

These are intentionally additive. Implementations may include extension fields,
but portable fields should graduate into the shared protocol once two or more
independent implementations need them.

## Event Taxonomy

Event names are dot-separated strings. The first segment identifies the semantic
area or control plane.

### Control Events

| Type | Meaning |
| --- | --- |
| `capabilities.request` | Ask an agent-control endpoint or adapter for its capability descriptor. |
| `capabilities.response` | Return supported layers, features, degradation, and bindings. |
| `models.request` | List or resolve models. |
| `models.response` | Return model descriptors and cache metadata. |
| `auth.providers.request` | List auth providers and auth state. |
| `auth.providers.response` | Return auth provider state. |
| `auth.login.request` | Start login for a provider. |
| `auth.event` | Emit login URL, prompt, progress, success, or error. |
| `auth.login.result` | Terminal login result. |

### Model IO Events

| Type | Meaning |
| --- | --- |
| `model.response.request` | Start one model response, streaming or non-streaming. |
| `model.stream.started` | A model stream has started. |
| `model.content.delta` | Text, reasoning, or multimodal content delta. |
| `model.tool_call.started` | A model began emitting a tool call. |
| `model.tool_call.delta` | Incremental tool-call argument or metadata delta. |
| `model.tool_call.completed` | Tool call arguments are complete and executable. |
| `model.stream.completed` | Model response completed successfully. |
| `model.stream.failed` | Model response failed terminally. |
| `model.stream.cancelled` | Model response was cancelled. |

V1-compatible implementations may skip incremental `model.tool_call.started` and
`model.tool_call.delta` and emit only `model.tool_call.completed`.

### Action/Tool Events

| Type | Meaning |
| --- | --- |
| `action.tools.list.request` | List available tools. |
| `action.tools.list.response` | Return tool descriptors. |
| `action.call.requested` | An agent or model requested a tool/action. |
| `action.call.started` | Execution began. |
| `action.call.delta` | Progress, stdout/stderr, partial result, or status update. |
| `action.call.completed` | Execution succeeded. |
| `action.call.failed` | Execution failed terminally. |
| `action.call.cancel.request` | Request cancellation of an action. |
| `action.call.cancelled` | Execution was cancelled. |
| `action.permission.requested` | Agent loop needs approval before continuing. |
| `action.permission.resolve.request` | Resolve an outstanding permission request. |
| `action.permission.resolved` | Approval was granted or denied. |
| `action.artifact.created` | Tool output was stored outside the transcript. |
| `action.artifact.retrieve.request` | Retrieve artifact content or metadata. |
| `action.artifact.retrieve.response` | Return artifact content or metadata. |

### Agent-Control Events

| Type | Meaning |
| --- | --- |
| `session.list.request` | List known sessions when persistence is supported. |
| `session.list.response` | Return session summaries and pagination metadata. |
| `session.open.request` | Open a new or existing session. |
| `session.open.response` | Session is available. |
| `session.closed` | Session was closed. |
| `transcript.load.request` | Load persisted session transcript messages or events. |
| `transcript.load.response` | Return persisted transcript content. |
| `session.message.submit.request` | Submit user-visible messages to a session. |
| `session.message.submit.response` | Acknowledge submission admission and effective delivery. |
| `run.started` | Run started. |
| `run.status.updated` | Run status changed meaningfully. |
| `turn.started` | A model/tool turn started. |
| `turn.completed` | A model/tool turn completed. |
| `run.interrupt.request` | Request interruption. |
| `run.interrupted` | Run stopped at an interruptible boundary. |
| `run.resume.request` | Resume an interrupted run. |
| `run.cancel.request` | Request terminal run cancellation. |
| `run.cancel.response` | Acknowledge accepted cancellation request. |
| `run.completed` | Run completed successfully. |
| `run.failed` | Run failed terminally. |
| `run.cancelled` | Run was cancelled. |
| `checkpoint.created` | Agent loop created a restorable checkpoint. |
| `checkpoint.restore.request` | Restore a checkpoint. |
| `branch.created` | Agent loop created a branch from a checkpoint or message. |
| `context.compacted` | Agent loop compacted transcript/context. |

Minimum useful agent-control stream:

1. `run.started`
2. zero or more `turn.started`
3. Model IO events and action/tool events correlated by trace IDs
4. matching `turn.completed`
5. exactly one of `run.completed`, `run.failed`, or `run.cancelled`

## Error Model

All terminal failure events and negative acknowledgements should carry a shared
error shape.

Required error fields:

- `code`: stable machine-readable error code.
- `message`: human-readable diagnostic text.

Optional error fields:

- `layer`: `binding`, `model`, `action`, `agent`, or `control`.
- `retriable`: whether retrying may succeed.
- `provider_id`: provider associated with the error, when known.
- `model_id`: model associated with the error, when known.
- `tool_name`: tool associated with the error, when known.
- `details`: structured diagnostic object.

Standard error codes:

- `invalid_request`
- `unsupported_feature`
- `capability_degraded`
- `auth_required`
- `auth_expired`
- `auth_refresh_failed`
- `permission_denied`
- `cancelled`
- `interrupted`
- `timeout`
- `rate_limited`
- `provider_error`
- `tool_error`
- `transport_error`
- `internal_error`

Failure rules:

- Unsupported features should return `unsupported_feature` and include the
  missing capability name in `details.feature`.
- If degraded execution is possible but not allowed by the request, return
  `capability_degraded`.
- Auth challenges should identify `provider_id` when known.
- A single underlying failure should surface as one terminal failure event at
  the highest active scope. For example, a run should not emit both
  `model.stream.failed` and `run.failed` as independent user-visible
  terminal failures for the same cause.

## Capability Descriptor

Capabilities are explicit and declarative. An implementation must describe both
native support and known degradation.

Capability descriptors contain:

- `endpoint`: identity for the OAP boundary, with `id`, `name`, optional
  `version`, and optional `adapter`.
- `protocol_versions`: supported protocol versions.
- `bindings`: supported bindings such as `in_process`, `json_stdio`,
  `websocket`, or `http_sse`.
- `layers`: optional `model`, `action`, `agent_control`, and
  `control_plane` capability sections.
- `degradation`: optional list of known degradation records.

Binding descriptors contain:

- `kind`: binding kind.
- `serialization`: optional serialization such as `json`, `jsonl`, or `binary`.
- `endpoint`: optional endpoint hint.

Feature support uses these levels:

- `native`
- `emulated`
- `degraded`
- `unavailable`

Feature support records contain:

- `level`: support level.
- `reason`: optional explanation.

Layer capability sections contain a `features` map keyed by feature name. The
action layer may also report `tool_sources`; the agent-control layer may also
report delivery modes such as `auto`, `queue`, `steer`, and `btw`.

Degradation records contain:

- `feature`: affected feature key.
- `from`: optional original support level.
- `to`: effective support level.
- `mode`: optional degradation mode such as `stripped`, `summarized`,
  `redacted`, `buffered`, `polling`, `emulated`, or `opaque`.
- `reason`: optional explanation.

Model layer capability examples:

- `model.chat`
- `model.streaming`
- `model.reasoning.input`
- `model.reasoning.output`
- `model.reasoning.encrypted`
- `model.tool_calling`
- `model.tool_calling.incremental_arguments`
- `model.vision`
- `model.audio_input`
- `model.audio_output`
- `model.json_schema`
- `model.prompt_cache`

Action layer capability examples:

- `action.tools.list`
- `action.tools.execute`
- `action.tools.cancel`
- `action.tools.progress`
- `action.tool_sources.attach`
- `action.permissions`
- `action.artifacts`
- `action.hash_anchored_edits`

Agent-control capability examples:

- `session.open`
- `session.list`
- `session.persistence`
- `session.message.submit`
- `session.message.delivery.auto`
- `session.message.delivery.queue`
- `session.message.delivery.steer`
- `session.message.delivery.btw`
- `run.streaming`
- `run.status`
- `run.instructions`
- `run.tool_selection`
- `run.cancel`
- `run.interrupt`
- `run.resume`
- `checkpoint`
- `rewind`
- `branch`
- `context.compaction`
- `transcript.load`

Control plane capability examples:

- `protocol.initialize`
- `capabilities`
- `models.list`
- `models.resolve`
- `auth.providers`
- `auth.login`

## Degradation Flow

Capability degradation is cumulative.

1. The provider or model reports raw capabilities.
2. A provider bridge may remove, emulate, or transform capabilities.
3. An agent loop or adapter may add or remove agent-loop features.
4. A transport binding may remove streaming, cancellation, or progress fidelity.
5. The exposed endpoint returns effective capabilities plus degradation records.

Rules:

- Adjacent layers must gate features on effective capabilities, not
  implementation brand names.
- Bridges must report destructive transforms, especially reasoning stripping,
  tool-call buffering, unavailable tool catalogs, and non-resumable streams.
- Missing events are not a valid capability signal.
- If a requested feature is unavailable, the endpoint should fail early with a
  typed error unless the request explicitly allows degraded execution.
- Event payloads may include `omitted` or `redactions` arrays to explain
  per-event data loss.

Example:

```json
{
  "feature": "model.reasoning.output",
  "from": "native",
  "to": "degraded",
  "mode": "summarized",
  "reason": "adapter exposes reasoning summaries but not raw thinking tokens"
}
```

## Relationship To Existing Protocols

Existing provider, tool-host, and agent-control protocols can be bridged into
Open Agent Protocol, but they are not the internal source of truth.

A full agent-control protocol may already expose sessions, prompts, runs,
resume, and progress. If it does not expose controls relevant to the control or
presentation layer, such as a portable tool catalog, instruction override,
checkpoint identity, rich progress, or clean resume semantics, the bridge must
report those gaps as degradation.

A generic tool or message protocol may be useful underneath an adapter, but it
is not sufficient by itself for the agent-control profile. The adapter still
needs to expose sessions, runs, turns, capabilities, permissions, artifacts,
transcript lifecycle, and terminal event semantics.

Non-streaming or polling systems can be bridged as degraded agent-control
implementations. They should report degraded streaming, interrupt,
cancellation, and progress capabilities when those features are absent.

The most useful borrowed design is not a whole protocol. It is:

- CloudEvents-like envelope discipline;
- OpenTelemetry-like trace/span correlation;
- JSON Schema tool descriptors;
- additive-only schema evolution;
- explicit capability descriptors rather than implementation-name checks.

## Implementation Notes

Project-specific mappings should live outside the core protocol draft. An
implementation can speak the protocol directly, through an adapter, or through
a bridge from another protocol, as long as the effective adjacent-layer behavior
is described by capabilities and degradation records.

## Versioning

Protocol version `0.1` is a draft.

Compatibility rules:

- Minor revisions are additive only.
- Existing event type names and required fields must not change meaning.
- New capabilities may be added without a version bump.
- Unknown capabilities mean "unknown", not "false".
- A breaking change requires a new major version and a dual-stack migration
  period.

## First Conformance Target

The first concrete conformance target is the
[Agent Control Core](agent-control-core.md). An agent loop or adapter is
minimally conformant for that profile if it can:

1. initialize with `protocol.initialize.response`;
2. return `capabilities.response`;
3. open a session with `session.open.response`;
4. accept `session.message.submit.request`;
5. start admitted work with `run.started`;
6. emit normalized text deltas;
7. emit terminal success, failure, or cancellation exactly once per run;
8. report unavailable features as capabilities rather than relying on
   control-layer implementation-name checks.

A lower-level Model IO-only implementation may still be useful if it can:

1. return `capabilities.response`;
2. start a model stream;
3. emit normalized text deltas;
4. emit terminal success, failure, or cancellation exactly once per stream.

A richer coding-agent implementation should additionally support:

1. tool discovery and execution events;
2. interrupt and cancellation;
3. model catalog discovery;
4. session listing and transcript loading when persistence is native;
5. checkpoint, rewind, or branch events when supported;
6. artifact references for large tool output;
7. reasoning visibility metadata.

## Open Decisions

- Final protocol name and package name.
- Whether auth remains in the core control plane or becomes a standard
  extension.
- Whether to add JSON Schema packaging after the prose protocol design settles.
- Checkpoint identity model: message-index checkpoints, state snapshots, or both.
- Whether public conformance tests should require a JSON binding or allow pure
  in-process adapters.

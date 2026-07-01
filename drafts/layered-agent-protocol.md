# Open Agent Protocol Layered Draft

Status: draft
Protocol ID: `open-agent-protocol`
License: CC0-1.0 public domain dedication, or the nearest legally valid equivalent in jurisdictions that do not recognize public domain dedication.
Scope: a shared semantic protocol for agent UIs, runtime adapters, and independent agent tooling.

This document defines the portable layered model behind Open Agent Protocol. It
is not a project-specific wire protocol, a project-specific UI API, or a
replacement for existing provider, tool, or agent-control protocols. The first
practical conformance target is the [UI Runtime Core](ui-runtime-core.md). The
broader [UI Runtime Protocol](ui-runtime-protocol.md) uses these layers as its
source model.

## Goals

The protocol should:

- let one UI or controller talk to multiple agent runtimes through one event model;
- normalize provider SDKs, CLI-backed runtimes, hosted runtimes, local runtimes,
  and tool-backed runtimes without hiding important differences;
- separate agent loop semantics from tool/action semantics, model IO semantics, and transport framing;
- expose capabilities and degradation explicitly so UIs can disable, label, or reroute features safely;
- be usable in-process, over stdio, over WebSocket, over HTTP SSE, and through
  other transport bindings;
- stay small enough that runtimes can implement a useful subset.

Non-goals:

- internet-scale agent identity and discovery;
- forcing all implementations onto one transport;
- replacing existing provider, tool-host, or agent-control protocols;
- requiring compatibility with any specific external protocol;
- requiring every runtime to support checkpoints, branching, external tool
  hosts, or visible reasoning.

## Repository Artifacts

This prose draft explains the protocol semantics. The schema-like draft contract
and complete JSON examples live beside it:

- [TypeScript protocol contract](../types/protocol.ts)
- [Conformance draft](conformance.md)
- [UI runtime core profile](ui-runtime-core.md)
- [UI runtime protocol profile](ui-runtime-protocol.md)
- [UI runtime core TypeScript contract](../types/core.ts)
- [Core run stream example](../examples/core-run-stream.json)
- [Runtime capability example](../examples/runtime-capabilities.json)
- [Degraded adapter capability example](../examples/degraded-runtime-capabilities.json)
- [Tool-source example](../examples/tool-source.json)
- [UI run stream example](../examples/ui-run-stream.json)

## Layer Model

The protocol has three semantic layers plus a binding layer.

### Layer 0: Binding

Layer 0 defines how messages are framed, serialized, ordered, and transported.
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

Layer 0 bindings should preserve:

- message ID;
- scope ID;
- sequence number where ordering matters;
- timestamp;
- reply correlation;
- payload type;
- extension fields.

### Layer 1: Model IO

Layer 1 normalizes model-facing chat, response, and streaming semantics.

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

- executing local tools;
- agent turns;
- session checkpoints;
- UI state.

### Layer 2: Action

Layer 2 normalizes tool and action execution.

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
protocols are Layer 2 sources or bindings, not the whole UI runtime protocol.

### Layer 3: Agent Runtime

Layer 3 normalizes agent loop and session semantics.

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

Layer 3 may delegate model calls to Layer 1 and tool calls to Layer 2, but it is
the only layer that defines run and turn lifecycle.

### Control Plane

The control plane cuts across all layers.

It owns:

- runtime discovery;
- capability advertisement;
- version negotiation;
- auth/provider status;
- model catalog discovery;
- degradation reporting.

The control plane is intentionally separate from run streaming. A UI should be
able to ask "what can this runtime do?" before starting a run.

## Core Envelope

Every binding should be able to represent this logical envelope. Field names are
normative for the JSON binding. The maintained TypeScript draft is
[`types/protocol.ts`](../types/protocol.ts); this excerpt shows the required
shape.

```ts
export interface ProtocolEnvelope<T = unknown> {
  protocol: "open-agent-protocol";
  version: "0.1";
  type: string;
  id: string;
  scope: ProtocolScope;
  sequence?: number;
  timestamp_ms?: number;
  in_reply_to?: string;
  trace?: ProtocolTrace;
  payload: T;
  extensions?: Record<string, unknown>;
}

export interface ProtocolScope {
  kind:
    | "connection"
    | "session"
    | "run"
    | "turn"
    | "model_stream"
    | "tool_execution"
    | "auth_flow";
  id: string;
}

export interface ProtocolTrace {
  session_id?: string;
  run_id?: string;
  turn_id?: string;
  model_stream_id?: string;
  tool_call_id?: string;
  parent_id?: string;
}
```

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

Reasoning visibility is capability-controlled. A runtime that strips, redacts,
or summarizes reasoning must report that degradation. A UI must not infer
"reasoning unavailable" solely from the absence of reasoning events.

Minimal content types:

```ts
export type ContentPart =
  | { type: "text"; text: string; text_signature?: string }
  | { type: "reasoning"; text: string; signature?: string; visibility?: ReasoningVisibility }
  | { type: "image"; data?: string; uri?: string; mime_type: string }
  | { type: "audio"; data?: string; uri?: string; mime_type: string }
  | { type: "file"; data?: string; uri?: string; mime_type?: string; name?: string }
  | { type: "tool_call"; tool_call_id: string; name: string; arguments_json: string }
  | { type: "tool_result"; tool_call_id: string; name: string; content: ContentPart[]; is_error?: boolean }
  | { type: "artifact_ref"; artifact_id: string; uri?: string; mime_type?: string; byte_size?: number; sha256?: string };

export type ReasoningVisibility = "full" | "summary" | "redacted" | "none";
```

## Shared Primitive Records

Layer-specific events should reuse shared primitive records rather than define
one-off payload shapes. The draft TypeScript contract currently defines these
shared primitives:

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
  runtime, or binding transforms.

These are intentionally additive. Implementations may include extension fields,
but portable fields should graduate into the shared contract once two or more
independent runtimes need them.

## Event Taxonomy

Event names are dot-separated strings. The first segment identifies the layer or
control plane.

### Control Events

| Type | Meaning |
| --- | --- |
| `runtime.capabilities.request` | Ask a runtime or adapter for its capability descriptor. |
| `runtime.capabilities.response` | Return supported layers, features, degradation, and bindings. |
| `runtime.models.request` | List or resolve models. |
| `runtime.models.response` | Return model descriptors and cache metadata. |
| `runtime.auth.providers.request` | List auth providers and auth state. |
| `runtime.auth.providers.response` | Return auth provider state. |
| `runtime.auth.login.request` | Start login for a provider. |
| `runtime.auth.event` | Emit login URL, prompt, progress, success, or error. |
| `runtime.auth.login.result` | Terminal login result. |

### Layer 1 Model Events

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

V1-compatible runtimes may skip incremental `model.tool_call.started` and
`model.tool_call.delta` and emit only `model.tool_call.completed`.

### Layer 2 Action Events

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
| `action.permission.requested` | Runtime needs approval before continuing. |
| `action.permission.resolved` | Approval was granted or denied. |
| `action.artifact.created` | Tool output was stored outside the transcript. |
| `action.artifact.retrieve.request` | Retrieve artifact content or metadata. |
| `action.artifact.retrieve.response` | Return artifact content or metadata. |

### Layer 3 Agent Events

| Type | Meaning |
| --- | --- |
| `agent.sessions.list.request` | List known sessions when persistence is supported. |
| `agent.sessions.list.response` | Return session summaries and pagination metadata. |
| `agent.session.open.request` | Open a new or existing session. |
| `agent.session.opened` | Session is available. |
| `agent.session.closed` | Session was closed. |
| `agent.transcript.load.request` | Load persisted session transcript messages or events. |
| `agent.transcript.load.response` | Return persisted transcript content. |
| `agent.run.request` | Start or continue an agent run. |
| `agent.run.started` | Run started. |
| `agent.turn.started` | A model/tool turn started. |
| `agent.turn.completed` | A model/tool turn completed. |
| `agent.run.interrupt.request` | Request interruption. |
| `agent.run.interrupted` | Run stopped at an interruptible boundary. |
| `agent.run.resume.request` | Resume an interrupted run. |
| `agent.run.cancel.request` | Request terminal run cancellation. |
| `agent.run.completed` | Run completed successfully. |
| `agent.run.failed` | Run failed terminally. |
| `agent.run.cancelled` | Run was cancelled. |
| `agent.checkpoint.created` | Runtime created a restorable checkpoint. |
| `agent.checkpoint.restore.request` | Restore a checkpoint. |
| `agent.branch.created` | Runtime created a branch from a checkpoint or message. |
| `agent.context.compacted` | Runtime compacted transcript/context. |

Minimum useful Layer 3 stream:

1. `agent.run.started`
2. zero or more `agent.turn.started`
3. Layer 1 model events and Layer 2 action events correlated by trace IDs
4. matching `agent.turn.completed`
5. exactly one of `agent.run.completed`, `agent.run.failed`, or `agent.run.cancelled`

## Error Model

All terminal failure events and negative acknowledgements should carry a shared
error shape.

```ts
export interface ProtocolError {
  code: ProtocolErrorCode | string;
  message: string;
  layer?: "binding" | "model" | "action" | "agent" | "control";
  retriable?: boolean;
  provider_id?: string;
  model_id?: string;
  tool_name?: string;
  details?: Record<string, unknown>;
}

export type ProtocolErrorCode =
  | "invalid_request"
  | "unsupported_feature"
  | "capability_degraded"
  | "auth_required"
  | "auth_expired"
  | "auth_refresh_failed"
  | "permission_denied"
  | "cancelled"
  | "interrupted"
  | "timeout"
  | "rate_limited"
  | "provider_error"
  | "tool_error"
  | "transport_error"
  | "internal_error";
```

Failure rules:

- Unsupported features should return `unsupported_feature` and include the
  missing capability name in `details.feature`.
- If degraded execution is possible but not allowed by the request, return
  `capability_degraded`.
- Auth challenges should identify `provider_id` when known.
- A single underlying failure should surface as one terminal failure event at
  the highest active scope. For example, an agent run should not emit both
  `model.stream.failed` and `agent.run.failed` as independent user-visible
  terminal failures for the same cause.

## Capability Descriptor

Capabilities are explicit and declarative. A runtime must describe both native
support and known degradation.

```ts
export interface RuntimeCapabilities {
  runtime: {
    id: string;
    name: string;
    version?: string;
    adapter?: string;
  };
  protocol_versions: string[];
  bindings: RuntimeBinding[];
  layers: {
    model?: ModelLayerCapabilities;
    action?: ActionLayerCapabilities;
    agent?: AgentLayerCapabilities;
    control?: ControlPlaneCapabilities;
  };
  degradation?: CapabilityDegradation[];
}

export interface RuntimeBinding {
  kind: "in_process" | "json_stdio" | "websocket" | "http_sse" | string;
  serialization?: "json" | "jsonl" | "binary" | string;
  endpoint?: string;
}

export type SupportLevel = "native" | "emulated" | "degraded" | "unavailable";

export interface FeatureSupport {
  level: SupportLevel;
  reason?: string;
}

export type FeatureMap = Record<string, FeatureSupport>;

export interface ModelLayerCapabilities {
  features: FeatureMap;
}

export interface ActionLayerCapabilities {
  features: FeatureMap;
  tool_sources?: Array<"native" | "local" | "process" | "remote" | "hosted" | string>;
}

export interface AgentLayerCapabilities {
  features: FeatureMap;
}

export interface ControlPlaneCapabilities {
  features: FeatureMap;
}

export interface CapabilityDegradation {
  feature: string;
  from?: SupportLevel;
  to: SupportLevel;
  mode?: "stripped" | "summarized" | "redacted" | "buffered" | "polling" | "emulated" | "opaque";
  reason?: string;
}
```

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

Agent layer capability examples:

- `agent.sessions`
- `agent.sessions.list`
- `agent.session.persistence`
- `agent.run.streaming`
- `agent.run.instructions`
- `agent.run.tool_selection`
- `agent.run.cancel`
- `agent.run.interrupt`
- `agent.run.resume`
- `agent.checkpoints`
- `agent.rewind`
- `agent.branch`
- `agent.context.compaction`
- `agent.transcript.load`

Control plane capability examples:

- `control.capabilities`
- `control.models.list`
- `control.models.resolve`
- `control.auth.providers`
- `control.auth.login`

## Degradation Flow

Capability degradation is cumulative.

1. The provider or model reports raw capabilities.
2. A provider bridge may remove, emulate, or transform capabilities.
3. An agent runtime may add or remove agent-loop features.
4. A transport binding may remove streaming, cancellation, or progress fidelity.
5. The final adapter returns effective capabilities plus degradation records.

Rules:

- UIs must gate features on effective capabilities, not runtime brand names.
- Bridges must report destructive transforms, especially reasoning stripping,
  tool-call buffering, unavailable tool catalogs, and non-resumable streams.
- Missing events are not a valid capability signal.
- If a requested feature is unavailable, the runtime should fail early with a
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
resume, and progress. If it does not expose UI-relevant controls such as a
portable tool catalog, instruction override, checkpoint identity, rich progress,
or clean resume semantics, the bridge must report those gaps as degradation.

A generic tool or message protocol may be useful underneath a runtime adapter,
but it is not sufficient by itself for the UI runtime profile. The adapter still
needs to expose sessions, runs, turns, capabilities, permissions, artifacts,
transcript lifecycle, and terminal event semantics.

Non-streaming or polling controllers can be bridged as degraded runtimes. They
should report degraded streaming, interrupt, cancellation, and progress
capabilities when those features are absent.

The most useful borrowed design is not a whole protocol. It is:

- CloudEvents-like envelope discipline;
- OpenTelemetry-like trace/span correlation;
- JSON Schema tool descriptors;
- additive-only schema evolution;
- explicit capability descriptors rather than runtime-name checks.

## Implementation Notes

Project-specific mappings should live outside the core protocol draft. A runtime
can implement the protocol directly, through an adapter, or through a bridge
from another protocol, as long as the effective UI-facing behavior is described
by capabilities and degradation records.

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
[UI Runtime Core](ui-runtime-core.md). A runtime adapter is minimally conformant
for that profile if it can:

1. return `runtime.capabilities.response`;
2. open a session with `agent.session.opened`;
3. start an agent run with `agent.run.started`;
4. emit normalized text deltas;
5. emit terminal success, failure, or cancellation exactly once per run;
6. report unavailable features as capabilities rather than relying on UI
   runtime-name checks.

A lower-level Layer 1-only implementation may still be useful if it can:

1. return `runtime.capabilities.response`;
2. start a model stream;
3. emit normalized text deltas;
4. emit terminal success, failure, or cancellation exactly once per stream.

A richer coding-agent runtime should additionally support:

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
- Whether to add JSON Schema packaging alongside the TypeScript draft and
  generate TypeScript/Zig/Rust bindings from one source of truth.
- Checkpoint identity model: message-index checkpoints, state snapshots, or both.
- Whether public conformance tests should require a JSON binding or allow pure
  in-process adapters.

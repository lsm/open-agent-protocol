# Open Agent Protocol Draft

Status: draft
License: CC0-1.0 public domain dedication, or the nearest legally valid equivalent in jurisdictions that do not recognize public domain dedication.
Scope: a shared semantic protocol for Makai, NeoKai, and independent agent tooling.

This document defines a portable layered agent protocol. It is not a Makai wire
protocol, a NeoKai UI API, or a replacement for ACP/MCP. It is the common
semantic model that those systems can bind to.

## Goals

The protocol should:

- let one UI or controller talk to multiple agent runtimes through one event model;
- normalize Claude SDK, Codex CLI, Gemini CLI, Makai, MCP-backed tools, and future runtimes without hiding important differences;
- separate agent loop semantics from tool/action semantics, model IO semantics, and transport framing;
- expose capabilities and degradation explicitly so UIs can disable, label, or reroute features safely;
- be usable in-process, over stdio, over WebSocket, over HTTP SSE, and through adapter bridges such as ACP;
- stay small enough that runtimes can implement a useful subset.

Non-goals:

- internet-scale agent identity and discovery;
- forcing all implementations onto one transport;
- replacing MCP as a tool-server protocol;
- requiring ACP compatibility for internal runtime adapters;
- requiring every runtime to support checkpoints, branching, MCP, or visible reasoning.

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
- ACP bridge messages;
- Makai's existing envelope types.

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
- MCP server attachment as one possible tool source.

It does not require all tools to be MCP tools. MCP is a binding/source for this
layer, not the whole layer.

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
normative for the JSON binding.

```ts
export interface ProtocolEnvelope<T = unknown> {
  protocol: "layered-agent-protocol";
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
- Makai can keep 21-character session IDs and 26-character ULIDs.
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
| `agent.session.open.request` | Open a new or existing session. |
| `agent.session.opened` | Session is available. |
| `agent.session.closed` | Session was closed. |
| `agent.run.request` | Start or continue an agent run. |
| `agent.run.started` | Run started. |
| `agent.turn.started` | A model/tool turn started. |
| `agent.turn.completed` | A model/tool turn completed. |
| `agent.run.interrupt.request` | Request interruption. |
| `agent.run.interrupted` | Run stopped at an interruptible boundary. |
| `agent.run.resume.request` | Resume an interrupted run. |
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
  kind: "in_process" | "json_stdio" | "websocket" | "http_sse" | "acp" | string;
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
  tool_sources?: Array<"native" | "mcp" | string>;
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
- `action.mcp.attach`
- `action.permissions`
- `action.artifacts`
- `action.hash_anchored_edits`

Agent layer capability examples:

- `agent.sessions`
- `agent.session.persistence`
- `agent.run.streaming`
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
  tool-call buffering, missing MCP, and non-resumable streams.
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

ACP should be an optional external adapter boundary, not the internal source of
truth. ACP servers can plug into NeoKai or Makai through a bridge that maps ACP
runs and progress events into this taxonomy. The bridge should report ACP event
loss as degradation.

MCP remains the preferred tool-server ecosystem for many tools. This protocol
should represent MCP servers as Layer 2 tool sources, not replace MCP.

A2A can wrap a whole Layer 3 runtime as one opaque collaborating agent. It does
not expose enough internal turn, tool, and reasoning state to be the UI runtime
protocol.

Agent Protocol style REST controllers can be bridged as non-streaming or
polling runtimes. They should report degraded streaming, interrupt, and progress
capabilities when those features are absent.

The most useful borrowed design is not a whole protocol. It is:

- CloudEvents-like envelope discipline;
- OpenTelemetry-like trace/span correlation;
- MCP-style JSON Schema tool descriptors;
- additive-only schema evolution;
- explicit capability descriptors rather than runtime-name checks.

## Makai Binding

Makai can bind its existing architecture without replacing current transports.

Current mapping:

- `protocol/provider/*` maps to Layer 1 Model IO.
- `protocol/tool/*` maps to Layer 2 Action.
- `protocol/agent/*` and `agent/types.zig` map to Layer 3 Agent Runtime.
- `protocol/auth/*` and `protocol/model_catalog_types.zig` map to the control plane.
- `transports/*` and current JSON envelopes are Layer 0 bindings.

Current TypeScript event mapping:

| Makai TS event | Shared event |
| --- | --- |
| `message_start` | `model.stream.started` |
| `text_delta` | `model.content.delta` with `part.type = "text"` |
| `thinking_delta` | `model.content.delta` with `part.type = "reasoning"` |
| `tool_call` | `model.tool_call.completed` |
| `message_end` | `model.stream.completed` |
| provider `error` | `model.stream.failed` |
| `agent_start` | `agent.run.started` or `agent.session.opened`, depending on scope |
| `turn_start` | `agent.turn.started` |
| `turn_end` | `agent.turn.completed` |
| `tool_execution_start` | `action.call.started` |
| `tool_execution_end` | `action.call.completed` or `action.call.failed` |
| `agent_end` | `agent.run.completed` |

Recommended Makai work:

1. Add a control-plane `runtime.capabilities` response to the TypeScript SDK and
   Zig protocol runtime.
2. Define JSON Schema fixtures for normalized provider, action, and agent events.
3. Replace opaque `agent_event: event_json` as a public boundary with typed
   normalized events while preserving the existing internal Zig union.
4. Keep existing Makai envelope sequencing and ID formats as the Makai Layer 0
   binding.
5. Add conformance fixtures that map current Makai events to shared events.

## NeoKai Binding

NeoKai should bind the protocol at the `AgentRuntimeGateway` boundary.

Recommended mapping:

- `AgentRuntimeGateway` exposes Layer 3 operations and control-plane discovery.
- `AgentRuntimeAdapter` maps Claude SDK, Codex CLI, Gemini CLI, ACP servers, or
  Makai into the shared event taxonomy.
- `ProviderBridge` owns Layer 1 model IO transforms.
- MCP attachment lives in Layer 2 as a tool-source capability.
- MessageHub WebSocket remains a Layer 0 transport binding.
- The UI gates checkpoint, rewind, branch, compaction, MCP, and reasoning views
  from `runtime.capabilities.response`.

For third-party adapters, NeoKai should accept either:

- a native adapter that emits this protocol directly; or
- an ACP adapter bridged into this protocol with explicit degradation.

Do not make ACP shape the internal NeoKai event model. ACP compatibility is
valuable at the edge, but the internal model needs richer events for reasoning,
tool progress, checkpoints, branches, and graceful UI degradation.

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

A runtime is minimally conformant if it can:

1. return `runtime.capabilities.response`;
2. start an agent run or model stream;
3. emit normalized text deltas;
4. emit terminal success or failure exactly once per run or stream;
5. report unavailable features as capabilities rather than relying on UI
   runtime-name checks.

A richer coding-agent runtime should additionally support:

1. tool discovery and execution events;
2. interrupt and cancellation;
3. model catalog discovery;
4. checkpoint or transcript persistence if native;
5. artifact references for large tool output;
6. reasoning visibility metadata.

## Open Decisions

- Final protocol name and package name.
- Whether auth remains in the core control plane or becomes a standard
  extension.
- Exact JSON Schema packaging and generated TypeScript/Zig/Rust bindings.
- Checkpoint identity model: message-index checkpoints, state snapshots, or both.
- Whether public conformance tests should require a JSON binding or allow pure
  in-process adapters.

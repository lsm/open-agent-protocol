# Open Agent Protocol UI Runtime Draft

Status: draft
Profile ID: `open-agent-protocol.ui-runtime`
Base protocol: `open-agent-protocol` version `0.1`
License: CC0-1.0 public domain dedication, or the nearest legally valid equivalent in jurisdictions that do not recognize public domain dedication.
Scope: the shared protocol surface between an agent UI/controller and an agent runtime adapter.

This broader profile describes what an agent UI or workbench can expect from a
runtime adapter. The smaller [UI Runtime Core](ui-runtime-core.md) is the
current 80/20 conformance target; this document keeps richer controls visible
while the core settles.

The layered model still matters, but it is supporting structure. The UI runtime
profile is the product-facing contract: it says how a UI discovers what a
runtime can do, opens sessions, starts runs, renders timelines, controls
execution, handles approvals, and degrades safely when a runtime cannot provide
every feature.

## Design Position

Agent UIs should not hardcode provider names, runtime names, or adapter-specific
event streams. They should render and control agent work through a stable
semantic protocol.

The UI runtime profile is broad, but it is not a literal dump of every lower
layer. It includes the model, tool, artifact, permission, session, and run
events that affect the UI. It excludes raw provider frames, external protocol
internals, transport framing details, private runtime memory structures, and
adapter implementation details unless those details are surfaced as explicit
capabilities, degradation records, extension fields, or artifacts.

Principles:

- The UI gates features from capabilities, not runtime names.
- The runtime adapter owns normalization from providers, tools, and agent loops.
- Per-run model, instruction, and tool selection is explicit and capability
  gated.
- The stream is user-experience complete: the UI can render a useful timeline
  from protocol events alone.
- Degradation is explicit and cumulative.
- Unknown capabilities and unknown enum values are preserved, not treated as
  fatal parse errors.
- Existing provider, tool, or agent-control protocols stay behind adapter
  boundaries unless exposed through explicit features.

## Participants

`UI/controller`

Renders sessions, run timelines, messages, reasoning, tool calls, artifacts,
approvals, checkpoints, and errors. Sends user input and control commands.

`Runtime adapter`

Implements this profile. It may wrap a native runtime, CLI, provider SDK,
hosted service, tool environment, or another protocol. It is
responsible for capabilities, degradation records, event normalization, and
stable IDs.

`Agent runtime`

Runs the agent loop. It may be a native runtime, a CLI-backed runtime, a
provider-backed adapter, a hosted agent, or another system.

`Model provider bridge`

Maps provider-native model IO into content, reasoning, tool-call, usage, and
failure events.

`Tool source`

Provides executable tools. Tool sources can be local, process-backed, remote,
or hosted. A tool-source protocol is not the whole UI runtime protocol.

`Artifact store`

Stores large or binary outputs outside the transcript and returns `ArtifactRef`
records the UI can retrieve or display.

## Relationship To The Layered Draft

The UI runtime profile composes the lower layers:

| Source | What the UI sees |
| --- | --- |
| Control plane | capabilities, model catalog, auth state, degradation |
| Layer 3 Agent Runtime | sessions, runs, turns, checkpoints, branches, compaction, transcript loading |
| Layer 2 Action | tools, permissions, progress, results, artifacts |
| Layer 1 Model IO | messages, content parts, reasoning, tool-call requests, usage |
| Layer 0 Binding | transport metadata only, not UI semantics |

The UI profile should be treated as the default implementation target. The
layered draft explains where each event originates and how adapters should map
their internals into the UI-facing stream.

## Required Envelope

Every UI runtime profile event uses the base protocol envelope:

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
```

The maintained draft TypeScript contract is
[`types/protocol.ts`](../types/protocol.ts).

For new implementations, start with the smaller
[`types/core.ts`](../types/core.ts) contract.

## Transport Agnostic Binding

This profile defines semantic commands and events, not a single wire protocol.
The same envelopes can be carried by multiple bindings:

- in-process function calls or typed interfaces;
- JSON lines over stdio;
- WebSocket messages;
- HTTP request/response plus server-sent event streams;
- another transport that preserves the envelope and ordering rules.

Bindings must preserve:

- `id`, `type`, `scope`, `sequence`, `timestamp_ms`, and `payload`;
- `in_reply_to` for request/response correlation;
- `trace` for session, run, turn, model stream, and tool-call correlation;
- terminal event semantics for runs, model streams, and tool executions;
- extension fields, even when the binding does not understand them.

Bindings may add connection management, authentication handshakes, backpressure,
heartbeats, reconnect behavior, or batching. Those are binding concerns. They
must not change the meaning of UI runtime events.

## Example Fixtures

Concrete JSON fixtures are maintained with the draft:

- [Core run stream](../examples/core-run-stream.json)
- [UI run stream](../examples/ui-run-stream.json)
- [Runtime capability descriptor](../examples/runtime-capabilities.json)
- [Degraded adapter capability descriptor](../examples/degraded-runtime-capabilities.json)
- [Tool-source descriptor](../examples/tool-source.json)

## UI Runtime Lifecycle

A typical UI session follows this order:

1. Discover runtime capabilities with `runtime.capabilities.request`.
2. Optionally discover models and auth state with `runtime.models.request` and
   `runtime.auth.providers.request`.
3. Optionally list existing sessions with `agent.sessions.list.request`.
4. Open or create a session with `agent.session.open.request`.
5. Optionally load transcript history with `agent.transcript.load.request`.
6. Start or continue work with `agent.run.request`.
7. Render the ordered run stream until exactly one terminal run event arrives.
8. Send interruption, cancellation, resume, permission, or artifact retrieval
   requests when supported by capabilities.

The UI may skip optional discovery steps if the runtime capability descriptor
marks those features unavailable. The UI must not infer support from runtime
brand names.

## Minimum UI Profile

The minimum UI profile is defined by [UI Runtime Core](ui-runtime-core.md). A
runtime should implement that profile before adopting the richer controls below.

A runtime conforms to the minimum UI runtime profile if it can:

1. return `runtime.capabilities.response`;
2. open a session with `agent.session.opened`;
3. start a run with `agent.run.started`;
4. emit ordered model text deltas with `model.content.delta`;
5. emit exactly one of `agent.run.completed`, `agent.run.failed`, or
   `agent.run.cancelled` for each started run;
6. report unsupported features as unavailable capabilities.

A useful coding-agent UI runtime should additionally support:

1. model catalog discovery;
2. tool listing and tool execution events;
3. permission request and resolution events;
4. artifact creation and retrieval;
5. run cancellation and interruption;
6. session listing and transcript loading;
7. checkpoint, rewind, or branch events when native or emulated;
8. reasoning visibility metadata;
9. context compaction records.

## Feature Gates

The UI should enable, disable, label, or reroute features from the effective
capability descriptor.

| UI feature | Capability keys |
| --- | --- |
| Runtime connection | `control.capabilities` |
| Model picker | `control.models.list`, `control.models.resolve` |
| Provider login UI | `control.auth.providers`, `control.auth.login` |
| Session history | `agent.sessions.list`, `agent.session.persistence`, `agent.transcript.load` |
| Start run | `agent.sessions`, `agent.run.streaming` |
| Instruction override | `agent.run.instructions` |
| Per-run tool selection | `agent.run.tool_selection`, `action.tools.list` |
| Cancel run | `agent.run.cancel` |
| Interrupt and resume | `agent.run.interrupt`, `agent.run.resume` |
| Reasoning pane | `model.reasoning.output` |
| Encrypted reasoning display | `model.reasoning.encrypted` |
| Tool call rendering | `model.tool_calling`, `action.tools.execute` |
| Tool catalog UI | `action.tools.list` |
| Tool-source attachment UI | `action.tool_sources.attach` |
| Tool progress | `action.tools.progress` |
| Approval prompts | `action.permissions` |
| Artifact browser | `action.artifacts` |
| Checkpoint UI | `agent.checkpoints` |
| Rewind UI | `agent.rewind` |
| Branch UI | `agent.branch` |
| Compaction markers | `agent.context.compaction` |

Support levels affect presentation:

- `native`: enable normally.
- `emulated`: enable, but avoid promising runtime-native fidelity.
- `degraded`: enable only when the UI can present the limitation or the user
  has allowed degraded execution.
- `unavailable`: hide or disable the control.
- unknown or absent: treat as unavailable unless a profile-specific rule says
  otherwise.

## UI Timeline Projection

The UI receives protocol events. It may project them into timeline rows, cards,
or transcript messages, but those UI widgets are not separate protocol concepts.

| UI projection | Event sources |
| --- | --- |
| Session opened | `agent.session.opened` |
| Historical transcript | `agent.transcript.load.response` |
| Run header/status | `agent.run.started`, terminal run events |
| Turn boundary | `agent.turn.started`, `agent.turn.completed` |
| Assistant text | `model.stream.started`, `model.content.delta`, `model.stream.completed` |
| Reasoning block | `model.content.delta` with `part.type = "reasoning"` |
| Tool call | `model.tool_call.completed`, `action.call.requested` |
| Tool execution | `action.call.started`, `action.call.delta`, terminal action events |
| Permission prompt | `action.permission.requested`, `action.permission.resolved` |
| Artifact | `action.artifact.created`, `action.artifact.retrieve.response` |
| Checkpoint marker | `agent.checkpoint.created` |
| Branch marker | `agent.branch.created` |
| Compaction marker | `agent.context.compacted` |
| Error row | terminal failure events carrying `ProtocolError` |

Timeline rules:

- `sequence` is scoped. A UI should order events first by stream scope and
  sequence, then by timestamp only as a fallback.
- `trace` correlates model streams, tool calls, turns, runs, and sessions.
- A model stream may complete with `stop_reason = "tool_call"` and be followed
  by Layer 2 action events in the same turn.
- A run may contain multiple turns.
- Terminal failure should be shown once at the highest active scope. Lower-scope
  errors may be retained for inspection but should not create duplicate primary
  failure banners.

## Required UI Commands

These request events are the UI's primary control surface:

| Command | Request event | Expected response or stream effect |
| --- | --- | --- |
| Get capabilities | `runtime.capabilities.request` | `runtime.capabilities.response` |
| List models | `runtime.models.request` | `runtime.models.response` |
| List auth providers | `runtime.auth.providers.request` | `runtime.auth.providers.response` |
| Start login | `runtime.auth.login.request` | `runtime.auth.event`, then `runtime.auth.login.result` |
| List sessions | `agent.sessions.list.request` | `agent.sessions.list.response` |
| Open session | `agent.session.open.request` | `agent.session.opened` |
| Load transcript | `agent.transcript.load.request` | `agent.transcript.load.response` |
| Start or continue run | `agent.run.request` | `agent.run.started`, then run stream |
| Interrupt run | `agent.run.interrupt.request` | `agent.run.interrupted` |
| Resume run | `agent.run.resume.request` | `agent.run.started` or continued run stream |
| Cancel run | `agent.run.cancel.request` | `agent.run.cancelled` |
| Resolve permission | `action.permission.resolved` | Runtime continues or fails the blocked action |
| Retrieve artifact | `action.artifact.retrieve.request` | `action.artifact.retrieve.response` |
| Cancel action | `action.call.cancel.request` | `action.call.cancelled` |

Runtimes may reject unsupported commands with `unsupported_feature`. If degraded
execution is possible but not allowed by the request, they should reject with
`capability_degraded`.

## Session And Transcript Model

A session is the durable UI-visible container for a conversation or workspace
task. A run is one execution attempt inside a session. A turn is one logical
agent loop step inside a run.

Session identity rules:

- `session_id` is opaque and stable within the runtime.
- A UI may create a new session by omitting `session_id` from
  `agent.session.open.request`.
- A UI may reopen a known session by providing `session_id`.
- Session listing requires `agent.sessions.list`.
- Transcript loading requires `agent.transcript.load`.

Transcript loading may return messages, events, or both. A runtime with only
message history can return `ProtocolMessage[]`. A runtime with richer persisted
state can return historical protocol envelopes so the UI can reconstruct tool
progress, artifacts, checkpoints, and compaction markers.

## Run Control

Run control commands are capability-gated.

`agent.run.request`

Starts or continues work in a session. When supported, the request may include
the selected `model_id`, additional `instructions`, a per-run `tools` list, and
`tool_choice`. A runtime that cannot honor instruction override or tool
selection should report `agent.run.instructions` or `agent.run.tool_selection`
as degraded or unavailable.

`agent.run.cancel.request`

Requests terminal cancellation. If cancellation succeeds, the run stream should
end with `agent.run.cancelled`. If the runtime cannot cancel, it should return a
typed failure or report `agent.run.cancel` as unavailable.

`agent.run.interrupt.request`

Requests a stop at an interruptible boundary. If successful, the runtime emits
`agent.run.interrupted` and reports whether the run is resumable.

`agent.run.resume.request`

Resumes a previously interrupted run. If resume is emulated by starting a new
run from transcript state, the runtime must report the degradation.

## Permission Model

Permission prompts are first-class UI events. A runtime should emit
`action.permission.requested` before executing a tool or action that needs user
approval.

Rules:

- `permission_id` is stable and must be used in `action.permission.resolved`.
- Choices are explicit. The UI should not infer grant/deny labels.
- Destructive choices should be marked with `destructive: true`.
- A denied permission should fail or skip the blocked action with a typed error,
  not silently continue as if the action succeeded.

## Artifacts

Large, binary, or persistent outputs should be represented as `ArtifactRef`
records instead of being inlined into message text.

Artifacts can be produced by tools, model output, or runtime operations.
Artifact references should include URI, MIME type, byte size, and hash metadata
when available. Retrieval is capability-gated by `action.artifacts`.

## Degradation Policy

The UI runtime profile depends on honest degradation reporting.

Adapters must report destructive transforms, including:

- reasoning stripped, summarized, or redacted;
- provider tool-call streams buffered into completed calls;
- tool progress collapsed into opaque status messages;
- checkpoint, branch, or resume support emulated by transcript replay;
- external agent-control progress mapped into less precise timeline events;
- tool catalogs hidden behind a remote runtime rather than exposed as portable
  tool descriptors;
- streaming replaced by polling or completed-response events.

The UI should use degradation records to decide whether to show labels,
warnings, disabled states, or alternate controls.

## Adapter Guidance

Native runtime adapters should expose their own sessions, runs, model events,
tool events, artifacts, and control-plane state directly through this profile.

Adapters over an existing agent-control protocol should normalize that protocol
into this profile and report any lost control or visibility as degradation. This
includes missing tool-catalog control, missing prompt or instruction override,
coarse progress events, awkward or unavailable resume, unavailable checkpoints,
and hidden provider/model state.

Adapters over a generic messaging or tool protocol may be useful, but the
adapter still needs to implement the UI runtime profile. A UI should not be
expected to infer sessions, runs, turns, capabilities, permissions, artifacts,
and transcript lifecycle from a generic message pipe alone.

## Out Of Profile

The following are intentionally outside the UI runtime profile:

- raw provider SDK request and response frames;
- raw tool-host protocol traffic;
- raw external agent-control protocol messages;
- private runtime planner state;
- private memory index internals;
- transport-specific framing details;
- UI component layout or visual design;
- internet-scale agent identity and discovery.

These can still appear in extension fields, diagnostic artifacts, or adapter
developer tools when explicitly requested.

## Open Decisions

- Whether this profile should become the top-level `README` entry point.
- Exact JSON Schema packaging for UI runtime envelopes.
- Whether transcript loading should prefer persisted protocol events, messages,
  or a required hybrid shape.
- How much run control should be mandatory beyond terminal cancellation.
- Whether checkpoint identity should standardize message-index checkpoints,
  state snapshots, transcript snapshots, or all three.
- Which degradation records should be mandatory for external agent-control and
  non-streaming REST adapters.

# Open Agent Protocol Agent Control Profile

Status: draft
Profile ID: `open-agent-protocol.agent-control`
Base protocol: `open-agent-protocol` version `0.1`
License: CC0-1.0 public domain dedication, or the nearest legally valid equivalent in jurisdictions that do not recognize public domain dedication.
Scope: the shared protocol boundary between a control/orchestration layer and
an agent loop or agent-control endpoint.

This broader profile describes what a control layer can expect from an agent
loop. The smaller [Agent Control Core](agent-control-core.md) is the current
80/20 conformance target; this document keeps richer controls visible while
the core settles.

The agent-control profile is not a deployment-side protocol. It defines a
logical software boundary. The control layer and agent loop may be
different processes, different machines, two modules in one process, or two
functions in one program. A binding decides transport; the profile defines
semantics.

## Design Position

Control layers should not hardcode provider names, implementation names, or
adapter-specific event streams. They should control agent work through a stable
semantic protocol and expose renderable state to presentation layers.

The agent-control profile is broad, but it is not a literal dump of every
lower layer. It includes the model, tool, artifact, permission, session, and run
events that affect orchestration and presentation. It excludes raw provider
frames, external protocol internals, transport framing details, private loop
memory structures, and adapter implementation details unless those details are
surfaced as explicit capabilities, degradation records, extension fields, or
artifacts.

Principles:

- Logical layers are independent from deployment topology.
- The control layer gates features from capabilities, not implementation names.
- Agent loops own orchestration across lower model, action, workspace,
  artifact, and memory boundaries.
- Adapters are implementation shims for non-OAP SDKs or protocols, not a
  semantic layer.
- Per-run model, instruction, and tool selection is explicit and capability
  gated.
- The stream is presentation-ready: a UI can render a useful timeline from
  protocol events alone, but the UI is not the protocol peer for this boundary.
- Degradation is explicit and cumulative.
- Unknown capabilities and unknown enum values are preserved, not treated as
  fatal parse errors.
- Existing provider, tool, or agent-control protocols stay behind adapter
  boundaries unless exposed through explicit features.

## Logical Layers

`Presentation layer`

Human-facing surfaces such as GUI, TUI, editor panes, web apps, and dashboards.
They render sessions, timelines, messages, reasoning, tool calls, artifacts,
approvals, checkpoints, and errors. They capture user intent and configuration
changes such as model selection, tool toggles, and permission decisions.

The presentation layer may be in the same process as the control layer, but it
is a distinct logical layer. The presentation layer should not need provider SDK
events, implementation brand checks, or private adapter state.

`Control layer`

Policy and orchestration layer. It turns user or system intent into protocol
operations, applies capability gates, selects effective models and tool policy,
resolves permissions, decides `auto`, `queue`, `steer`, or `btw` delivery,
correlates requests and responses, manages reconnect and state recovery, and
talks to one or more agent loops or agent-control endpoints.

The control layer may be embedded in a UI, hosted in a backend, implemented by a
CLI/TUI, or run as a headless orchestration service.

`Agent loop layer`

Core execution orchestrator. It builds model requests, calls model providers,
interprets model responses, dispatches tool/action calls, coordinates
workspace, artifact, and memory resources, and advances turns until a run
completes, fails, pauses, or is cancelled.

A pure agent loop can be reusable orchestration: it does not need to own model
APIs, tool execution, workspace access, artifacts, or memory storage if it can
call those lower boundaries.

`Adapter implementation`

Not a semantic layer. An adapter presents this profile over a non-OAP agent
SDK, hosted agent API, CLI-backed agent, provider-specific protocol, or legacy
loop. It is responsible for capability descriptors, degradation records, event
normalization, stable IDs, and preserving the agent-control lifecycle at the
boundary it exposes.

`Model provider layer`

Model APIs, provider SDKs, hosted services, local model runtimes, and
provider-specific streaming formats. The model IO profile is the boundary
between an agent loop and this layer.

`Action/tool execution layer`

Local tools, remote tools, process-backed commands, browser/editor actions, and
approval-gated operations. The action/tool profile is the boundary between an
agent loop and this layer.

`Resource provider layer`

Workspaces, file systems, working-tree state, artifact stores, memory systems,
semantic retrieval indexes, and context stores. Workspace, artifact, and memory
profiles should be distinct resource boundaries when generic tools are too
opaque.

## Boundary

This profile standardizes the boundary:

```text
Control layer <-> Agent loop
```

The adjacent presentation/control boundary is a separate layer. A presentation
layer sends renderable user intent to the control layer; the control layer
decides which agent-control operations to issue.

An adapter may implement this same boundary on top of a non-OAP SDK or
protocol. The adapter is not an extra layer in the protocol model; it is an
implementation of the agent-control boundary. The loop/lower-provider
boundaries are separate: an agent loop may use model IO, action/tool,
workspace, artifact, memory, storage, or provider-specific profiles underneath,
but those do not replace the agent-control profile.

## Relationship To The Layered Draft

The agent-control profile composes lower semantic layers into one
control-facing boundary:

| Source | What the control layer receives |
| --- | --- |
| Control plane | capabilities, model catalog, auth state, degradation |
| Agent-control semantics | sessions, runs, turns, checkpoints, branches, compaction, transcript loading |
| Action/tool profile | tools, permissions, progress, results, artifacts |
| Model IO profile | messages, content parts, reasoning, tool-call requests, usage |
| Workspace profile | working-tree, file-system, environment, and sandbox state |
| Artifact profile | large or persistent outputs referenced by runs, messages, and tools |
| Memory profile | durable memory, retrieval, summaries, and context recall |
| Binding profile | transport metadata only, not agent-control semantics |

The agent-control profile should be treated as the first implementation
target. The layered draft explains where each event originates and how adapters
should map their internals into the control-facing stream.

## Required Envelope

Every agent-control profile event uses the base protocol envelope:

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
- `extensions`: extension object for non-profile fields.

For new implementations, start with the smaller
[Agent Control Core](agent-control-core.md) profile.

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
must not change the meaning of agent-control events.

## Example Fixtures

Concrete JSON fixtures are maintained with the draft:

- [Core run stream](../examples/core-run-stream.json)
- [Agent-control run stream](../examples/agent-control-run-stream.json)
- [Agent capability descriptor](../examples/agent-capabilities.json)
- [Degraded adapter capability descriptor](../examples/degraded-adapter-capabilities.json)
- [Tool-source descriptor](../examples/tool-source.json)

## Agent-Control Lifecycle

A typical control-layer session follows this order:

1. Initialize the boundary with `protocol.initialize.request`.
2. Discover endpoint capabilities with `capabilities.request`.
3. Optionally discover models and auth state with `models.request` and
   `auth.providers.request`.
4. Optionally list existing sessions with `session.list.request`.
5. Open or create a session with `session.open.request`.
6. Optionally load transcript history with `transcript.load.request`.
7. Submit a user message with `session.message.submit.request`.
8. Render the ordered run stream until exactly one terminal run event arrives.
9. Send interruption, cancellation, resume, permission, or artifact retrieval
   requests when supported by capabilities.

The control layer may skip optional discovery steps if the capability
descriptor marks those features unavailable. The control layer must not infer
support from implementation brand names.

## Minimum Agent-Control Profile

The minimum agent-control profile is defined by
[Agent Control Core](agent-control-core.md). An agent loop or adapter should
implement that profile before adopting the richer controls below.

An implementation conforms to the minimum agent-control profile if it can:

1. initialize with `protocol.initialize.response`;
2. return `capabilities.response`;
3. open a session with `session.open.response`;
4. accept `session.message.submit.request` with
   `session.message.submit.response`;
5. start admitted work with `run.started`;
6. emit ordered model text deltas with `model.content.delta`;
7. emit exactly one of `run.completed`, `run.failed`, or `run.cancelled` for
   each started run;
8. report unsupported features as unavailable capabilities.

A useful coding-agent implementation should additionally support:

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

The control layer should enable, disable, label, or reroute features from the
effective capability descriptor. Presentation layers may use the same
capabilities to show or hide controls.

| Presentation or control feature | Capability keys |
| --- | --- |
| Agent connection | `protocol.initialize`, `capabilities` |
| Model picker | `models.list`, `models.resolve` |
| Provider login flow | `auth.providers`, `auth.login` |
| Session history | `session.list`, `session.persistence`, `transcript.load` |
| Submit message | `session.open`, `session.message.submit`, `run.streaming` |
| Queue message | `session.message.delivery.queue` |
| Steer active run | `session.message.delivery.steer` |
| Side question | `session.message.delivery.btw` |
| Instruction override | `run.instructions` |
| Per-run tool selection | `run.tool_selection`, `action.tools.list` |
| Cancel run | `run.cancel` |
| Interrupt and resume | `run.interrupt`, `run.resume` |
| Reasoning pane | `model.reasoning.output` |
| Encrypted reasoning display | `model.reasoning.encrypted` |
| Tool call rendering | `model.tool_calling`, `action.tools.execute` |
| Tool catalog control | `action.tools.list` |
| Tool-source attachment control | `action.tool_sources.attach` |
| Tool progress | `action.tools.progress` |
| Approval prompts | `action.permissions` |
| Artifact browser | `action.artifacts` |
| Checkpoint control | `checkpoint` |
| Rewind control | `rewind` |
| Branch control | `branch` |
| Compaction markers | `context.compaction` |

Support levels affect presentation:

- `native`: enable normally.
- `emulated`: enable, but avoid promising native fidelity from the underlying
  system.
- `degraded`: enable only when the control layer can handle the limitation or
  the presentation layer can present it to the user.
- `unavailable`: hide or disable the control.
- unknown or absent: treat as unavailable unless a profile-specific rule says
  otherwise.

## Presentation Projection

The agent-control stream is presentation-ready. A control layer may forward
or project protocol events into timeline rows, cards, or transcript messages
for a UI, but those UI widgets are not separate agent-control protocol
concepts.

| Presentation projection | Event sources |
| --- | --- |
| Session opened | `session.open.response` |
| Historical transcript | `transcript.load.response` |
| Run header/status | `run.started`, `run.status.updated`, terminal run events |
| Turn boundary | `turn.started`, `turn.completed` |
| Assistant text | `model.stream.started`, `model.content.delta`, `model.stream.completed` |
| Reasoning block | `model.content.delta` with `part.type = "reasoning"` |
| Tool call | `model.tool_call.completed`, `action.call.requested` |
| Tool execution | `action.call.started`, `action.call.delta`, terminal action events |
| Permission prompt | `action.permission.requested`, `action.permission.resolved` |
| Artifact | `action.artifact.created`, `action.artifact.retrieve.response` |
| Checkpoint marker | `checkpoint.created` |
| Branch marker | `branch.created` |
| Compaction marker | `context.compacted` |
| Error row | terminal failure events carrying `ProtocolError` |

Projection rules:

- `sequence` is scoped. Presentation projections should order events first by
  stream scope and sequence, then by timestamp only as a fallback.
- `trace` correlates model streams, tool calls, turns, runs, and sessions.
- A model stream may complete with `stop_reason = "tool_call"` and be followed
  by action/tool events in the same turn.
- A run may contain multiple turns.
- Terminal failure should be shown once at the highest active scope. Lower-scope
  errors may be retained for inspection but should not create duplicate primary
  failure banners.

## Required Control-Layer Commands

These request events are the control layer's primary agent-control surface:

| Command | Request event | Correlated response or stream effect |
| --- | --- | --- |
| Initialize boundary | `protocol.initialize.request` | `protocol.initialize.response` |
| Get capabilities | `capabilities.request` | `capabilities.response` |
| List models | `models.request` | `models.response` |
| List auth providers | `auth.providers.request` | `auth.providers.response` |
| Start login | `auth.login.request` | `auth.event`, then `auth.login.result` |
| List sessions | `session.list.request` | `session.list.response` |
| Open session | `session.open.request` | `session.open.response` |
| Load transcript | `transcript.load.request` | `transcript.load.response` |
| Submit session message | `session.message.submit.request` | `session.message.submit.response`, then run stream when admitted |
| Interrupt run | `run.interrupt.request` | `run.interrupted` |
| Resume run | `run.resume.request` | `run.started` or continued run stream |
| Cancel run | `run.cancel.request` | `run.cancel.response`, then `run.cancelled` |
| Resolve permission | `action.permission.resolve.request` | `action.permission.resolved`, then agent loop continues or fails the blocked action |
| Retrieve artifact | `action.artifact.retrieve.request` | `action.artifact.retrieve.response` |
| Cancel action | `action.call.cancel.request` | `action.call.cancelled` |

Implementations may reject unsupported commands with `unsupported_feature`. If
degraded execution is possible but not allowed by the request, they should
reject with `capability_degraded`.

## Session And Transcript Model

A session is the durable control-visible container for a conversation or
workspace task. A run is one execution attempt inside a session. A turn is one
logical agent loop step inside a run.

Session identity rules:

- `session_id` is opaque and stable within the agent-control endpoint.
- A control layer may create a new session by omitting `session_id` from
  `session.open.request`.
- A control layer may reopen a known session by providing `session_id`.
- Session listing requires `session.list`.
- Transcript loading requires `transcript.load`.

Transcript loading may return messages, events, or both. An implementation with
only message history can return `ProtocolMessage[]`. An implementation with
richer persisted state can return historical protocol envelopes so the control
layer can reconstruct tool progress, artifacts, checkpoints, and compaction
markers.

## Message Submit And Run Control

Run control commands are capability-gated.

`session.message.submit.request`

Submits user-visible messages to a session. When the session is idle, the agent
loop normally admits the submission by starting a run. When the session is
active, `delivery` can request `queue`, `steer`, or `btw` behavior when those
modes are advertised.

When supported, the request may include the selected `model_id`, additional
`instructions`, `tool_choice`, and structured output preferences. `tool_choice`
is policy over the advertised tool catalog; it is not the default way to
provide executable tool definitions from the control layer. An implementation
that cannot honor instruction override or tool selection should report
`run.instructions` or `run.tool_selection` as degraded or
unavailable.

The response is `session.message.submit.response`. It acknowledges admission,
not completion, and reports the effective delivery mode, admission result, and
run ID when a run was started, queued, steered, or side-started.

`run.cancel.request`

Requests terminal cancellation. If cancellation succeeds, the run stream should
end with `run.cancelled`. If the implementation cannot cancel, it should return a
typed failure or report `run.cancel` as unavailable.

`run.interrupt.request`

Requests a stop at an interruptible boundary. If successful, the implementation emits
`run.interrupted` and reports whether the run is resumable.

`run.resume.request`

Resumes a previously interrupted run. If resume is emulated by starting a new
run from transcript state, the implementation must report the degradation.

## Permission Model

Permission prompts are first-class agent-control events. An implementation should emit
`action.permission.requested` before executing a tool or action that needs user
approval.

Rules:

- `permission_id` is stable and must be used in
  `action.permission.resolve.request`.
- Choices are explicit. The control or presentation layer should not infer
  grant/deny labels.
- Destructive choices should be marked with `destructive: true`.
- A denied permission should fail or skip the blocked action with a typed error,
  not silently continue as if the action succeeded.

## Artifacts

Large, binary, or persistent outputs should be represented as `ArtifactRef`
records instead of being inlined into message text.

Artifacts can be produced by tools, model output, or agent operations.
Artifact references should include URI, MIME type, byte size, and hash metadata
when available. Retrieval is capability-gated by `action.artifacts`.

## Degradation Policy

The agent-control profile depends on honest degradation reporting.

Adapters must report destructive transforms, including:

- reasoning stripped, summarized, or redacted;
- provider tool-call streams buffered into completed calls;
- tool progress collapsed into opaque status messages;
- checkpoint, branch, or resume support emulated by transcript replay;
- external agent-control progress mapped into less precise timeline events;
- tool catalogs hidden behind a remote agent or SDK rather than exposed as portable
  tool descriptors;
- streaming replaced by polling or completed-response events.

The control layer should use degradation records to decide whether to continue,
reroute, degrade, or ask the presentation layer to show labels, warnings,
disabled states, or alternate controls.

## Adapter Guidance

Native OAP agent loops should expose their own sessions, runs, model events,
tool events, artifacts, and control-plane state directly through this profile.

Adapters over an existing SDK, CLI, hosted agent API, or agent-control protocol
should normalize that system into this profile and report any lost control or
visibility as degradation. This includes missing tool-catalog control, missing
prompt or instruction override, coarse progress events, awkward or unavailable
resume, unavailable checkpoints, and hidden provider/model state.

Adapters over a generic messaging or tool protocol may be useful, but the
adapter still needs to implement the agent-control profile. A control layer
should not be expected to infer sessions, runs, turns, capabilities,
permissions, artifacts, and transcript lifecycle from a generic message pipe
alone.

## Out Of Profile

The following are intentionally outside the agent-control profile:

- raw provider SDK request and response frames;
- raw tool-host protocol traffic;
- raw external agent-control protocol messages;
- private planner state;
- private memory index internals;
- transport-specific framing details;
- UI component layout or visual design;
- internet-scale agent identity and discovery.

These can still appear in extension fields, diagnostic artifacts, or adapter
developer tools when explicitly requested.

## Open Decisions

- Whether this profile should become the top-level `README` entry point.
- Exact JSON Schema packaging for agent-control envelopes.
- Whether transcript loading should prefer persisted protocol events, messages,
  or a required hybrid shape.
- How much run control should be mandatory beyond terminal cancellation.
- Whether checkpoint identity should standardize message-index checkpoints,
  state snapshots, transcript snapshots, or all three.
- Which degradation records should be mandatory for external agent-control and
  non-streaming REST adapters.

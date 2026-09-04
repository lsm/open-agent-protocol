# Harness Interoperability Study

Status: research note
Date: 2026-09-02
Scope: OAP adoption across nine agent harnesses, SDKs, CLIs, and UI backends

P0 findings: [Protocol Gaps From Harness Interoperability](p0-protocol-gaps.md)

This note tests the current OAP layer model against nine real agent harnesses
and integration surfaces.
It is implementation research, not a normative protocol specification.

Reviewed revisions:

- HyperNeo (`lsm/neokai`): `153caa5fbc0cdc1b41185458a73758def91bb7c9`
- Makai (`lsm/makai`): `af1d9cb539e5cfea8e93dacb3cb506ba4cd87b7c`
- pi (`earendil-works/pi`): `ee24a9ec54a9602d55dc7ac767c270cec806c291`
- Claude Agent SDK TypeScript (`anthropics/claude-agent-sdk-typescript`):
  `eefd0df14591e213061f62d62d0707be3be313c3`
- Claude Agent SDK Python (`anthropics/claude-agent-sdk-python`):
  `16606a32d26409de70abe424787a88bdb97a9324`
- Codex SDK, CLI, and app-server (`openai/codex`):
  `2c79ee6dacb6deccb7e19ac5acffb3e379bbe895`
- OpenCode (`anomalyco/opencode`):
  `8e0f1c253b6b7292b419505af849d06747c0e049`
- Gemini CLI (`google-gemini/gemini-cli`):
  `4963a4456a886bb6af7dcfb807ad6e3e46ce46fc`
- OpenHands (`OpenHands/OpenHands`):
  `b4428e1f8529fe726039437c8e54a7e7319986eb`
- Cline (`cline/cline`): `6d5a9793fce68e825ed22626f6247970d943e122`

### Additional-harness selection

Claude Agent SDK, Codex, and OpenCode were requested directly. Gemini CLI,
OpenHands, and Cline were selected as the three most-starred additional
open-source coding-agent harnesses in a GitHub repository-star snapshot taken
on 2026-09-01: Gemini CLI 106,761, OpenHands 85,893, and Cline 67,310. The
selection excluded the already requested projects, libraries without a coding
agent harness, and duplicate frontends for the same harness. Stars are only a
reproducible breadth-selection heuristic; they are not evidence of protocol
quality or architectural merit.

## Executive Recommendation

The current OAP layer model fits all nine systems, but they should not adopt it
in the same way:

1. HyperNeo should implement an OAP agent-control endpoint over the Claude Agent
   SDK. Its existing MessageHub can remain a binding and compatibility API.
2. Makai should become the first native validation of the agent-loop, model-IO,
   and action-tool boundaries. Its existing protocols are already close to
   those boundaries.
3. pi should validate two integration forms: an adapter over its JSONL RPC mode
   and a higher-fidelity in-process adapter over `AgentSession` or `Agent`.
4. Claude Agent SDK, Codex SDK/CLI, Gemini stream-JSON, and ACP endpoints should
   validate opaque adapter mappings with explicitly reduced capabilities.
5. Codex app-server, OpenCode's HTTP/SSE service, OpenHands agent-server, and
   Cline's ProtoBus should validate rich, bidirectional presentation/control
   boundaries and reconnect behavior.

OAP should standardize semantic boundaries and portable behavior. It should
not standardize each harness's internal method inventory, executable plugin
model, transport, or product data model. The same product may expose multiple
boundaries with different fidelity; conformance therefore attaches to an
endpoint and negotiated profile, not to a product name.

The next implementation target should be one HyperNeo vertical slice:

```text
Web presentation
    <-> existing web/daemon compatibility API
Control projection and policy
    <-> OAP agent-control-core port
Claude Agent SDK adapter
    <-> Claude Agent SDK
```

That slice should cover initialization, capabilities, session open/state,
message submission, cancellation, and a normalized run stream. It can run
in-process first; changing transport is unnecessary.

## Three Different Integration Shapes

### Native implementation

A component implements an OAP profile as its own boundary. Its internal state
and APIs may use native types, but no non-OAP system is required to explain the
semantics.

Makai is the strongest current native candidate. Its agent, provider, and tool
protocols already correspond to OAP's agent-loop, model-IO, and action-tool
boundaries.

### Adapter implementation

An adapter presents an OAP profile over an SDK, CLI, hosted service, or legacy
API that does not speak OAP. It normalizes IDs, lifecycle events, capabilities,
errors, and degradation. An adapter is an implementation of a boundary, not an
additional semantic layer.

HyperNeo's Claude Agent SDK integration and pi's JSONL RPC mode are adapter
candidates.

### Extension profile

An extension adds portable semantics that are absent from a base profile and
must be understood by both peers. It is not required merely because an
implementation uses a third-party service.

For example, a GitHub or Gmail integration can normally expose standard tools
through the action-tool profile. A GitHub-specific pull-request state model or
a HyperNeo-specific multi-agent workflow lifecycle may justify an extension
profile because generic tool calls do not describe the interoperable state and
behavior.

## Compatibility Matrix

| Concern | HyperNeo | Makai | pi | OAP implication |
| --- | --- | --- | --- | --- |
| Control boundary | Large web/daemon RPC surface | Agent run/stream API | JSONL RPC and `AgentSession` methods | `agent-control-core` is a viable common facade. |
| Loop implementation | Hidden behind Claude Agent SDK | Native agent protocol | Native `Agent`/`agentLoop` | Do not require adapters to expose hidden lower layers. |
| Model boundary | Claude SDK query options and events | Native provider protocol | `streamFn` and model APIs | Makai and pi can validate future model-IO; HyperNeo initially cannot. |
| Tool boundary | SDK tools, MCP servers, daemon policy | Native tool list/execute/progress/cancel | Typed tools with execution events | Common action-tool profile is justified. |
| Session state | Daemon persistence and SDK session IDs | Stream/session envelope IDs | `AgentSession` tree and state | Core needs stable OAP IDs independent of native IDs. |
| Streaming | Raw SDK messages plus daemon events | Structured agent/provider events | Structured agent/session events | Normalize to run, turn, content, action, and terminal events. |
| Capabilities | Inferred from daemon policy and SDK `system:init` | Not negotiated as OAP capabilities | Not negotiated | Initialization, revisioned capabilities, and degradation are real gaps. |
| Delivery while busy | `immediate` or `defer` | Not yet a shared control contract | steer and follow-up queues | OAP `auto`, `queue`, and `steer` cover the common behavior; `btw` remains optional. |
| Product semantics | Spaces, tasks, goals, workflows, multi-agent orchestration | Harness-focused | Session tree and executable extensions | Product semantics belong in focused extension profiles, not core. |
| Transport | MessageHub/WebSocket-like carrier | In-process and protocol transports | JSONL stdio or in-process | Bindings must preserve semantics; no single transport is required. |

The additional systems broaden the same comparison without changing the
original three-system matrix:

| System and boundary | Integration shape | Strongest OAP evidence | Principal gap or intentional omission |
| --- | --- | --- | --- |
| Claude Agent SDK client | Opaque subprocess-backed SDK adapter | Runtime initialization, caller-selected tools and prompt, session resume, streaming, permissions, cancellation | Lower model and tool boundaries are not independently substitutable through the SDK. |
| Codex TypeScript SDK / `exec --json` | Reduced SDK/CLI adapter | Thread identity, turn stream, item lifecycle, cancellation through process control | Much of app-server control, approval, and user-input behavior is absent. |
| Codex app-server | Rich bidirectional control endpoint | Initialize, thread/turn lifecycle, steer preconditions, notifications, server-initiated requests, client-hosted tools | Product-specific configuration and item types exceed minimum core. |
| OpenCode server | HTTP query/command plus SSE event binding | Sessions, canonical messages, status, abort, permissions, questions, model/provider data | HTTP route inventory and UI state are not a portable semantic profile as-is. |
| Gemini SDK / stream JSON | In-process or CLI adapter | Session resume, content/tool/error/result stream, cancellation, usage | Headless SDK rejects interactive confirmation and exposes less control than ACP. |
| Gemini ACP / A2A | External protocol adapters | Initialization capabilities, prompt/cancel, mode/model changes, permissions, session updates | ACP and A2A remain adapter boundaries rather than OAP's internal source of truth. |
| OpenHands agent-server | Remote conversation and durable-event endpoint | Conversation creation, event history, live deltas, execution state, confirmation, replay and fork | Sandbox, repository, canvas, automation, and cloud task models are extensions. |
| Cline ProtoBus / SDK bridge | Presentation-control RPC and stream | Correlated unary/streaming calls, stable task IDs, queue/cancel, full state plus lossy live updates | `ask`/`say`, IDE commands, checkpoints, browser, and provider settings are product projections. |

## HyperNeo

### Current boundaries

HyperNeo's shared MessageHub envelope is defined in
`packages/shared/src/message-hub/protocol.ts`. It provides generic request,
response, event, ping, and pong framing with method names, IDs, correlation,
session scope, timestamps, errors, and channels. This is useful binding
infrastructure, but its method strings are the semantic API.

The user-message path currently crosses several responsibilities:

1. `packages/web/src/hooks/useSendMessage.ts` sends `message.send` with content,
   images, and `deliveryMode`.
2. `packages/daemon/src/lib/rpc-handlers/session-handlers.ts` validates the
   request and delegates to the session manager.
3. `packages/daemon/src/lib/session/message-persistence.ts` expands input,
   decides admission, persists queue state, and creates a Claude SDK message.
4. `packages/daemon/src/lib/agent/query-runner.ts` constructs Claude SDK query
   options and runs the SDK query.
5. `packages/daemon/src/lib/agent/sdk-message-handler.ts` consumes SDK messages,
   updates daemon state, and emits UI-facing events.

The path is functional, but transport, control policy, persistence, Claude SDK
adaptation, and presentation projection are coupled. The web application also
renders SDK-specific messages directly. This makes a second agent backend
expensive because the SDK's types leak through the entire stack.

### OAP placement

The first OAP boundary should be an in-process agent-control port between
HyperNeo's control logic and a Claude Agent SDK adapter:

```text
HyperNeo presentation
    <-> presentation-control projection or existing compatibility RPC
HyperNeo control
    <-> open-agent-protocol.agent-control-core
Claude Agent SDK adapter
    <-> Claude Agent SDK query and event stream
```

MessageHub may carry OAP envelopes unchanged or continue carrying legacy
methods during migration. Replacing MessageHub first would change transport
without fixing semantic coupling.

The adapter should:

- derive a revisioned capability snapshot from HyperNeo policy, configured
  providers, and Claude SDK initialization data;
- assign stable OAP session, run, turn, and action IDs while retaining native
  IDs as adapter metadata;
- map accepted input to `session.message.submit.response` and later lifecycle
  output to stream events;
- map Claude SDK `system:init` to capability or state updates rather than a raw
  timeline item;
- normalize SDK text, reasoning, tool, usage, failure, and cancellation output;
- report unavailable, emulated, or degraded lower-layer behavior honestly.

HyperNeo's `immediate` mode is not a stable synonym for OAP `start`: its effect
depends on whether a session is busy and on daemon policy. During migration,
the UI's default behavior should request `auto`; explicit deferral maps to
`queue`. If exact legacy admission behavior cannot be represented, a narrowly
scoped HyperNeo extension can carry the legacy request until control policy is
refactored.

### What should remain outside core

HyperNeo has a broad RPC surface for spaces, tasks, goals, workflows, agents,
long-horizon execution, evolution, drafts, voice, and live queries. These must
not be copied into agent-control-core.

Likely HyperNeo extension families include:

- `io.hyperneo.space`
- `io.hyperneo.workflow`
- `io.hyperneo.multi-agent`
- `io.hyperneo.long-horizon`

The exact profiles should follow domain boundaries, not mirror every existing
RPC method. Implementation-specific live-query facilities should remain a
HyperNeo API or binding feature unless multiple independent implementations
need the same query semantics.

## Makai

Makai already separates agent, provider, tool, auth, model catalog, and
transport protocols. Relevant sources include:

- `zig/src/protocol/agent/`
- `zig/src/protocol/provider/`
- `zig/src/protocol/tool/`
- `typescript/src/execution_types.ts`

Its TypeScript API exposes agent `run` and `stream` operations and provider
`complete` and `stream` operations. Agent events distinguish agent, turn,
provider-message, and tool-execution lifecycle. Its envelope already carries
version, session or stream identity, message identity, ordering, correlation,
timestamps, and a payload.

This structure validates OAP's lower boundaries. The important gaps are:

- no OAP initialization and revisioned capability negotiation;
- no canonical OAP session-state recovery contract;
- no required OAP run and turn identity across every event;
- no uniform distinction among completed, failed, and cancelled terminal runs;
- several payloads are opaque JSON strings rather than portable structured
  data.

Makai should first expose OAP facades over its existing protocols. It should
then migrate one profile at a time, replacing opaque JSON payloads only where
the OAP shape has stabilized. A wholesale rewrite would provide little
additional validation.

Makai's ability to supply tools with a run is evidence for a standard optional
tool-selection or tool-source unit at the control boundary. The executable
tool callbacks themselves belong to an in-process action-tool binding, not a
Makai-specific semantic extension.

## pi

pi provides two useful abstraction levels.

The low-level agent in `packages/agent` owns messages, tools, prompting,
continuation, cancellation, steering, follow-up queues, and an event stream.
Its events distinguish agent, turn, model-message, and tool-execution
lifecycle. This is a strong candidate for validating the future agent-loop
profile.

`packages/coding-agent/src/core/agent-session.ts` adds persistence, model and
thinking configuration, compaction, branching, extensions, retries, and shell
execution. Its RPC mode in `packages/coding-agent/src/modes/rpc/` exposes JSONL
commands, correlated responses, and a session event stream.

The common agent-control mapping is direct:

| pi operation | OAP operation |
| --- | --- |
| `prompt` | `session.message.submit` with `auto`, resolving to `start` when idle |
| `steer` | `session.message.submit` with `steer` |
| `follow_up` | `session.message.submit` with `queue` |
| `abort` | `run.cancel` |
| `get_state` | `session.state` |
| agent/turn/message/tool events | normalized run/turn/content/action events |

The RPC adapter gaps match the other harnesses: no OAP initialize exchange,
capability revisions, degradation records, stable run/turn IDs, reconnect
cursor, or guaranteed typed terminal distinction. pi does not implement `btw`,
so an adapter should advertise it as unavailable.

pi's executable extension system should not be conflated with OAP semantic
extensions. Its select, confirm, and input requests can map to an optional OAP
user-input profile. Status bars, widgets, titles, and editor manipulation are
pi presentation extensions unless they prove portable across independent
presentations.

## Claude Agent SDK

The direct SDK study confirms and extends the findings from HyperNeo's use of
it. The Python SDK exposes the clearest control surface; its TypeScript SDK and
CLI-facing behavior follow the same subprocess control protocol. The one-shot
`query()` API is intentionally stateless and cannot support interrupts or
follow-up input, while `ClaudeSDKClient` provides bidirectional streaming,
interrupt, model and permission-mode changes, context usage, MCP status, file
rewind, and reconnect operations.

Admission configuration can select an exact tool set, allowed and disallowed
tools, system-prompt replacement or preset, MCP servers, permission mode,
model and fallback model, working directory, budget, maximum turns, resume,
continue, and fork behavior. Initialization later reports effective runtime
metadata and connected tools. This validates two OAP requirements:

- desired configuration belongs in admission or configuration commands, while
  effective configuration and late-discovered capabilities must be reported
  by the accepting endpoint;
- tools require explicit ownership semantics because they may be selected by
  the caller, built into the SDK runtime, supplied by MCP, or implemented by an
  in-process SDK callback.

The message stream includes user and assistant messages, system subtypes, raw
provider stream events, final results, usage, permission denials, and
background-task updates. Tool permissions carry a tool-use identity and
allow, deny, or interrupt outcomes. Hooks cover prompt submission, pre/post
tool use, tool failure, stop, subagents, compaction, notifications, and
permission requests.

These rich variants should not become core event types one-for-one. An adapter
must project them into canonical content, action, input/permission, state, and
terminal semantics. In particular, background tasks use inconsistent terminal
vocabularies across message variants, so OAP terminal classification cannot
trust a single native enum or stream EOF.

## Codex SDK, CLI, And App-Server

Codex demonstrates why an OAP claim must identify a concrete boundary. The
TypeScript SDK starts or resumes threads and runs turns by spawning
`codex exec --json`. Its public stream consists primarily of thread start,
turn start/completion/failure, item start/update/completion, and error events.
That is a useful reduced adapter surface, but it is not equivalent to Codex's
app-server.

App-server is a rich JSON-RPC control protocol. It initializes with client
identity and directional client capabilities, supports thread start/resume/read
and model discovery, and exposes turn start, steer, and interrupt. `turn/start`
returns an explicit turn ID and accepts a client user-message ID. `turn/steer`
requires the expected active turn ID, making stale-target detection part of
the command's semantics rather than a UI convention.

App-server emits thread, turn, item, and content-delta notifications. It also
sends correlated requests in the reverse direction for command and file-change
approval, tool user input, MCP elicitation, additional permissions,
client-hosted dynamic tool execution, authentication refresh, and attestation.
This is direct evidence that OAP needs participant-role and interaction
ownership metadata; “runtime capabilities” alone cannot say which participant
will execute a tool or answer a request.

Thread and turn configuration includes model/provider selection, workspace
roots, approval and sandbox policy, base and developer instructions, dynamic
tools, output schema, reasoning controls, and service tier. Most of these are
optional control units or extensions. Core needs only a way to admit a message,
report what effective configuration was selected, and declare unsupported or
degraded requests without relying on the Codex name.

## OpenCode

OpenCode exposes a typed HTTP API and generated SDK backed by an SSE event
stream. The session service provides create, get, list, children, status,
messages, prompt, asynchronous prompt, abort, fork, revert, summarize, and
configuration operations. Separate services expose models/providers, MCP,
permissions, questions, files, projects, terminal sessions, and global state.

Its internal event manifest and application reducers combine durable session
and message records with live part and status events. Permission requests and
user questions are first-class pending resources with stable IDs, list/reply
or reject commands, and corresponding events. Session permissions can be
changed independently and are merged with agent policy when tools are
resolved.

The OAP mapping is therefore not “copy the HTTP API.” Session create/state,
message submission, abort, canonical transcript items, and normalized run
events map to core and persistence units. Permissions, questions, models,
tools, and MCP map to optional profiles. Revert, terminal, project, provider,
sharing, and TUI-control routes remain extensions or separate product APIs.

OpenCode reinforces a state-convergence requirement: a subscriber may observe
live changes, but durable session/message queries remain the recovery source of
truth. OAP must define how a consumer detects a stream gap and re-establishes a
canonical state rather than assuming an SSE connection is a lossless log.

## Gemini CLI

Gemini CLI offers four relevant surfaces: a native agent loop, an embeddable
SDK, a noninteractive stream-JSON CLI, and ACP/A2A adapters. The SDK creates or
resumes a session, registers caller-supplied tools and skills, accepts an
abort signal, streams model events, schedules selected tool calls, and feeds
tool results back into the loop. Its native event vocabulary includes content,
thought, tool request/response/confirmation, cancellation, error, finish,
retry, context overflow, loop detection, model information, and blocked or
stopped execution.

The CLI's stream-JSON projection is deliberately smaller: initialize, message,
tool use, tool result, error, and result. It includes session and tool-call
identities, delta marking, typed tool outcomes, usage, and a terminal result.
This is a straightforward opaque adapter target, but its capabilities must be
narrower than the interactive CLI. For example, the headless SDK currently
rejects commands requiring interactive confirmation because it has no
interactive permission participant.

Gemini's ACP endpoint adds load-session capability, prompt and cancel,
session-mode and model changes, session updates, permission requests, and tool
call updates. Its A2A server exposes yet another task-oriented boundary. OAP
should learn from those implementations while retaining ACP and A2A as
external adapters. Their presence also confirms that capability and fidelity
are properties of a binding instance, not of “Gemini CLI” in the abstract.

## OpenHands

The current OpenHands frontend communicates with an agent-server through a
typed client plus HTTP/WebSocket-compatible event facilities. Conversation
creation can include an initial message, instructions, plugins, workspace,
parent conversation, agent profile, and sandbox selection. Sending a user
event can start execution. The UI separately reads conversation metadata,
execution status, durable event history, live events, metrics, and server
compatibility information.

OpenHands distinguishes persisted events from live runtime endpoints. Event
history supports pagination; live state includes streaming deltas, action and
observation events, confirmation, full-state and incremental state updates,
usage, pause, errors, and execution states such as running, idle, waiting for
confirmation, paused, error, and stuck. Conversation forking selects an event
as the branch point. Client tools can also let the agent request frontend-owned
behavior such as canvas updates or child-conversation launch.

This validates canonical history plus live projection, explicit waiting states,
and delegated interaction ownership. It also shows what should remain outside
core: cloud sandbox startup tasks, repository and worktree metadata, canvas
extensions, automations, browser state, agent profiles, and child workflow
semantics. These can compose with standard session, tool, artifact, workspace,
and extension profiles.

## Cline

Cline's VS Code UI uses protobuf-defined ProtoBus services over a webview
bridge. The boundary supports correlated unary requests and server-streaming
responses with request IDs, cancellation, stream completion, and sequence
numbers. Task operations include create, show, history, cancel active task,
cancel queued prompt, respond to a pending ask, edit/regenerate, and execute a
quick task. State is exposed both as a latest-state query and a state stream.

The most important design evidence is Cline's explicit delivery model. Partial
message delivery is fire-and-forget so a hidden or reloaded webview cannot stall
the agent loop. Correctness instead comes from a convergent UI replica that
merges updates by message identity and monotonic freshness sequence, rejects
traffic from an older epoch, and reconciles from full state. This is a concrete
counterexample to any OAP design that assumes every live delta is delivered
exactly once.

Cline's `ask` and `say` message taxonomy is presentation-oriented. OAP should
project it into semantic content, permission, user-input, action, status, and
terminal events rather than standardize those labels. Plan/act settings,
provider configuration, checkpoints, browser automation, IDE commands,
worktree behavior, quick wins, and MCP management belong to optional profiles
or Cline extensions.

Its SDK bridge also handles stale terminal events after cancellation, queued
prompts, resumable task phases, and partial-to-final replacement. These cases
strengthen the need for run-target preconditions, terminal atomicity, stable
item identity, and state reconciliation in the OAP adapter fixtures.

### Representative implementation entry points

The conclusions above were traced through these source areas rather than only
through product documentation:

| System | Representative source paths |
| --- | --- |
| Claude Agent SDK | `src/claude_agent_sdk/client.py`, `types.py`, `_internal/query.py`, `_internal/transport/subprocess_cli.py` |
| Codex | `sdk/typescript/src/thread.ts`, `events.ts`, `exec.ts`, `codex-rs/app-server-protocol/src/protocol/v2/` |
| OpenCode | `packages/opencode/src/server/routes/instance/httpapi/groups/`, `packages/schema/src/v1/session.ts`, `permission-v1.ts`, `question-v1.ts`, `session-status-event.ts` |
| Gemini CLI | `packages/sdk/src/session.ts`, `packages/core/src/core/turn.ts`, `packages/core/src/output/types.ts`, `packages/cli/src/acp/` |
| OpenHands | `src/api/agent-server-adapter.ts`, `src/api/conversation-service/`, `src/api/event-service/`, `src/types/agent-server/core/` |
| Cline | `apps/vscode/proto/cline/`, `apps/vscode/src/core/controller/grpc-handler.ts`, `apps/vscode/src/core/controller/ui/subscribeToPartialMessage.ts`, `apps/vscode/src/sdk/` |

## Cross-Harness Core

Across all reviewed boundaries, the portable 80/20 control core remains small:

1. initialize and select a profile with directional, revisioned capabilities;
2. open or recover a session and obtain canonical session state;
3. submit a user message and receive a correlated admission decision with
   stable submission and run identity;
4. observe run status and renderable content updates;
5. receive exactly one normalized terminal outcome for every admitted run;
6. request cancellation, or discover before admission that cancellation is
   unavailable;
7. receive typed errors and explicit degradation instead of product-name
   inference.

Tools, permissions, user input, models, transcript pagination, checkpoints,
branching, workspaces, artifacts, memory, child agents, and product workflows
are recurring and important, but they can be independently negotiated. Two
cross-cutting rules cannot be optional even when their richer feature data is:

- live streams are projections over recoverable canonical state, so ordering,
  deduplication, gap detection, and reconciliation semantics are required;
- interactions declare who owns the request and response, so caller-supplied
  tools, endpoint tools, client-hosted tools, permissions, and user input do not
  become ambiguous when both directions can initiate correlated work.

## Extension Model

The current `extensions` envelope object is a useful escape hatch but is not a
complete extension system. OAP needs a minimal extension-profile contract
before HyperNeo product behavior is represented through it.

An extension profile should declare:

- a stable reverse-DNS identifier;
- a semantic version;
- its base OAP profile or boundary;
- profile dependencies and compatible version ranges;
- capability keys, support levels, and degradation behavior;
- added request, response, and event types;
- added target kinds, intents, affordances, or timeline item kinds when it
  extends presentation-control;
- schemas, examples, and conformance fixtures;
- optional ownership, documentation, and registry metadata.

The envelope's `profile` and `type` pair should identify extension messages.
Small additions to standard messages can remain under reverse-DNS keys in
`extensions`. Supported extension profiles should be advertised through
capabilities. A peer may ignore and preserve an unknown optional extension; a
missing required extension must fail negotiation with a typed error.

### Registry principles

The future registry should catalog specifications, not distribute executable
plugins or become a runtime dependency.

- Unregistered reverse-DNS extensions must remain usable.
- Registration should add discoverability, ownership, version, specification,
  schema, and conformance links.
- `open-agent-protocol.*` should be reserved for standard profiles.
- Repeated independent adoption can promote an extension into an optional
  standard unit or profile.
- A service integration should use standard tools or resources unless peers
  need service-specific interoperable state or lifecycle semantics.

This keeps the barrier low without turning the protocol into an unstructured
bag of vendor fields.

## Protocol Gaps Exposed By The Study

The blocking findings and acceptance criteria are developed separately in
[P0 Protocol Gaps From Harness Interoperability](p0-protocol-gaps.md).

### P0: required for the first adapters

- Stabilize the exact agent-control-core event vocabulary. The core draft uses
  `content.delta`, while richer text still references `model.content.delta`.
- Define adapter ID rules for native session, run, turn, message, and tool-call
  identities.
- Define capability scope, authority, provisional state, late discovery, and
  refresh behavior at the level needed by real adapters.
- Define admission and delivery mapping, including acceptance versus execution,
  busy-session policy, and run-ID behavior for queue and steer.
- Define normalized terminal mapping when a native SDK does not provide a clean
  completed, failed, or cancelled signal.
- Define state convergence after missed, duplicate, stale, or reordered live
  updates.
- Define participant roles and ownership for reverse-direction requests and
  caller-hosted tools.
- Add normative mapping fixtures for every studied boundary family.

### P1: needed for backend interchangeability

- Draft the agent-loop profile using Makai and pi as validation targets.
- Draft model-IO and action-tool profiles from their native boundaries.
- Define the minimal extension-profile descriptor and capability negotiation.
- Clarify standard tool catalog, caller-supplied tool selection, and executable
  tool-source boundaries.
- Define effective configuration snapshots and provenance beyond the minimum
  admission and capability fields.

### P2: useful after the first vertical slice

- Define registry metadata and extension publication workflow.
- Evaluate user-input, persistence, models, permissions, artifacts, workspace,
  and memory as independent optional profiles or conformance units.
- Evaluate promotion criteria from widely used extensions to standard units.

## Proposed HyperNeo Migration

1. Define an internal OAP agent-control endpoint interface and event sink. Keep
   it semantic and transport independent.
2. Implement the Claude Agent SDK adapter for the minimum core lifecycle.
3. Put a compatibility facade in front of it so current MessageHub methods and
   web behavior continue to work.
4. Project normalized OAP state and events into presentation-control snapshots
   and updates for one session screen.
5. Add pi, Makai, and one opaque CLI adapter behind the same control port to
   prove backend interchangeability.
6. Extract native lower boundaries where available: Makai provider and tools,
   and pi's low-level agent loop.
7. Introduce focused HyperNeo extension profiles only for semantics the common
   profiles cannot represent.
8. Retire raw Claude SDK message rendering after the OAP projection covers the
   user-visible timeline.

Success for this stage is not that every HyperNeo RPC becomes OAP. Success is
that one presentation and control path can switch among Claude Agent SDK,
Makai, pi, and another independently implemented endpoint based on capabilities
rather than implementation names.

## Decisions Before Implementation

The research supports these defaults:

- Keep MessageHub as a binding and compatibility mechanism during migration.
- Start with an in-process OAP agent-control port.
- Treat Claude Agent SDK as an opaque agent-control adapter where lower layers
  are not exposed.
- Use Makai and pi to shape native lower-layer profiles.
- Keep executable plugin systems distinct from semantic protocol extensions.
- Permit unregistered extensions; make the registry optional metadata.
- Prefer standard tools and resources over service-specific extensions.

The remaining design question is how much event normalization belongs in
agent-control-core versus optional tools, reasoning, usage, and persistence
units. The mapping fixtures should answer that before additional normative
surface is added.

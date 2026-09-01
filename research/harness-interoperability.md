# Harness Interoperability Study

Status: research note
Date: 2026-09-01
Scope: HyperNeo, Makai, and pi adoption of Open Agent Protocol

P0 findings: [Protocol Gaps From Harness Interoperability](p0-protocol-gaps.md)

This note tests the current OAP layer model against three real agent harnesses.
It is implementation research, not a normative protocol specification.

Reviewed revisions:

- HyperNeo (`lsm/neokai`): `153caa5fbc0cdc1b41185458a73758def91bb7c9`
- Makai (`lsm/makai`): `af1d9cb539e5cfea8e93dacb3cb506ba4cd87b7c`
- pi (`earendil-works/pi`): `ee24a9ec54a9602d55dc7ac767c270cec806c291`

## Executive Recommendation

The current OAP layer model fits all three systems, but they should not adopt
it in the same way:

1. HyperNeo should implement an OAP agent-control endpoint over the Claude Agent
   SDK. Its existing MessageHub can remain a binding and compatibility API.
2. Makai should become the first native validation of the agent-loop, model-IO,
   and action-tool boundaries. Its existing protocols are already close to
   those boundaries.
3. pi should validate two integration forms: an adapter over its JSONL RPC mode
   and a higher-fidelity in-process adapter over `AgentSession` or `Agent`.

OAP should standardize semantic boundaries and portable behavior. It should not
standardize each harness's internal method inventory, executable plugin model,
or product data model.

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
| `prompt` | `session.message.submit` with `auto` or idle start |
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
- Define capability descriptors for delivery, cancellation, content, tools,
  and state recovery at the level needed by real adapters.
- Define normalized terminal mapping when a native SDK does not provide a clean
  completed, failed, or cancelled signal.
- Add normative mapping fixtures for HyperNeo/Claude SDK, Makai, and pi.

### P1: needed for backend interchangeability

- Draft the agent-loop profile using Makai and pi as validation targets.
- Draft model-IO and action-tool profiles from their native boundaries.
- Define transcript/event cursor and reconnect behavior beyond full-state
  recovery.
- Define the minimal extension-profile descriptor and capability negotiation.
- Clarify standard tool catalog, caller-supplied tool selection, and executable
  tool-source boundaries.

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
5. Add pi and Makai adapters behind the same control port to prove backend
   interchangeability.
6. Extract native lower boundaries where available: Makai provider and tools,
   and pi's low-level agent loop.
7. Introduce focused HyperNeo extension profiles only for semantics the common
   profiles cannot represent.
8. Retire raw Claude SDK message rendering after the OAP projection covers the
   user-visible timeline.

Success for this stage is not that every HyperNeo RPC becomes OAP. Success is
that one presentation and control path can switch among Claude Agent SDK, Makai,
and pi endpoints based on capabilities rather than implementation names.

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
units. The first three mapping fixtures should answer that before additional
normative surface is added.

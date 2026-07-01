# Open Agent Protocol Conformance Draft

Status: draft
Base protocol: `open-agent-protocol` version `0.1`
License: CC0-1.0 public domain dedication, or the nearest legally valid equivalent in jurisdictions that do not recognize public domain dedication.
Scope: profile and conformance-unit claims for Open Agent Protocol implementations.

Conformance is profile-based. Implementations should not make a broad
"implements Open Agent Protocol" claim without naming the profile and optional
conformance units they support.

The first conformance target is:

- `open-agent-protocol.ui-runtime-core`

Conformance units are additive:

- `+persistence`
- `+tools`
- `+permissions`
- `+user-input`
- `+models`

Example claims:

- `open-agent-protocol.ui-runtime-core`
- `open-agent-protocol.ui-runtime-core+tools+permissions`
- `open-agent-protocol.ui-runtime-core+persistence+tools+permissions+user-input+models`

Conformance units are testable units of behavior, not transport names and not
runtime brands. A runtime UI should still gate controls from
`runtime.capabilities`, not from a hardcoded implementation name.

The leading `+` is compact claim syntax for a conformance unit. Unit names can
also be written without the plus sign when listed in reports, manifests, or test
plans.

## Profiles And Units

A profile defines a coherent implementation target. The core profile is the
smallest UI/runtime contract expected to interoperate on its own.

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

## Core Profile Requirements

An implementation conforms to `open-agent-protocol.ui-runtime-core` if it
satisfies all of the following:

1. Emits and accepts valid core envelopes.
2. Supports `runtime.initialize.request` and `runtime.initialize.response`.
3. Supports `runtime.capabilities.request` and
   `runtime.capabilities.response`.
4. Supports `session.open.request` and `session.opened`.
5. Supports `session.state.request`, `session.state.response`, and
   `session.state.updated`.
6. Accepts `run.start.request` and returns `run.start.response`.
7. Emits `run.status.updated` for meaningful run lifecycle changes.
8. Streams assistant-visible output through `content.delta`.
9. Emits exactly one terminal run event for every accepted run:
   `run.completed`, `run.failed`, or `run.cancelled`.
10. Supports `run.cancel.request`, or declares cancellation as unavailable in
    capabilities and returns a typed unsupported-feature error if called.
11. Returns typed errors for unsupported commands.
12. Enables feature gating through capabilities and degradation records rather
    than runtime names.

Core conformance does not require persistence, tools, permissions, user-input
prompts, model listing, checkpointing, artifacts, auth flows, or a specific
transport.

## Optional Conformance Units

### `+tools`

An implementation conforms to `+tools` if it:

- advertises `tools` with an effective support level other than `unavailable`;
- represents callable tools with core `ToolDefinition` records;
- emits `tool.call.requested` when a tool call is selected;
- emits `tool.call.started` when execution begins;
- emits `tool.call.completed` exactly once for each started tool call;
- marks failed tool calls through `is_error` or a typed failure extension.

`tool.call.progress` is optional. If progress is degraded, buffered, or
unavailable, the implementation should say so in capabilities or degradation
records.

### `+permissions`

An implementation conforms to `+permissions` if it:

- advertises `permissions` with an effective support level other than
  `unavailable`;
- emits `permission.requested` for operations requiring user approval;
- accepts `permission.resolve.request`;
- emits `permission.resolved` after the runtime accepts the decision;
- never encodes permission prompts as opaque text-only assistant messages.

Permission prompts ask whether an operation may proceed. They should not be used
for ordinary information gathering from the user.

### `+user-input`

An implementation conforms to `+user-input` if it:

- advertises `user_input` with an effective support level other than
  `unavailable`;
- emits `run.status.updated` with `status: "waiting_for_input"` when a run is
  paused for user input;
- emits `user.input.requested` with stable `input_request_id` and structured
  questions;
- accepts `user.input.resolve.request`;
- accepts `user.input.cancel.request` when the prompt is cancellable;
- emits `user.input.resolved` after submit or cancellation;
- resumes, fails, or cancels the run using normal run lifecycle events.

Draft persistence is not required for `+user-input`. A runtime that persists
draft answers may expose that through extensions or a richer profile.

### `+persistence`

An implementation conforms to `+persistence` if it:

- advertises `session.list` and `transcript.load` with effective support levels
  other than `unavailable`;
- supports `session.list.request` and `session.list.response`;
- supports `transcript.load.request` and `transcript.load.response`;
- returns stable message IDs for persisted messages when available;
- provides cursor behavior for pagination or sync when it advertises cursors;
- emits `transcript.delta` when it advertises live transcript sync;
- can recover canonical session state after reconnect through
  `session.state.request`.

`transcript.delta` is optional unless the implementation claims live transcript
sync. A runtime may support historical transcript loading without live persisted
row deltas.

### `+models`

An implementation conforms to `+models` if it:

- advertises `runtime.models.list` with an effective support level other than
  `unavailable`;
- supports `runtime.models.list.request` and `runtime.models.list.response`;
- accepts `model_id` in `run.start.request` when model selection is advertised;
- reports the effective model in `run.start.response`, `run.started`, or
  session state when known;
- reports degraded or unavailable model selection explicitly when an adapter can
  only infer or approximate model control.

Model cache refresh, provider auth state, and model alias resolution are outside
this feature unless a richer profile defines them.

## Fixture-Based Test Plan

Conformance tests should start as fixture-based checks. A fixture is an ordered
set of logical envelopes plus expected outcomes.

Initial checks:

1. Schema validation.
2. Request/response correlation.
3. Event ordering within a scoped sequence.
4. Terminal-event rule: exactly one terminal run event per accepted run.
5. Capability gating for unavailable and degraded features.
6. Degradation reporting for adapter or binding feature loss.
7. Reconnect and state recovery through `session.state.request`.
8. Transcript cursor behavior for pagination and sync.
9. Tool event lifecycle.
10. User-input lifecycle.

These tests should run against any binding by first normalizing binding-specific
wire messages into core envelopes.

## Error Expectations

Unsupported commands should fail early with a typed protocol error. A conforming
runtime should not silently ignore unsupported commands, invent private event
names for core semantics, or rely on UI hardcoding to avoid unsupported paths.

The minimum error shape is:

- `code`
- `message`
- `retriable`
- `details`

The `code` should be stable enough for UI behavior. Human-readable text belongs
in `message`.

## Degradation Expectations

Capabilities describe the current effective behavior, not the ideal behavior of
the underlying provider or runtime. Adapters must report destructive transforms
as degradation.

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
level, and a reason suitable for UI display or diagnostics.

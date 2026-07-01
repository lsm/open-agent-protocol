# Open Agent Protocol UI Runtime Core

Status: draft
Profile ID: `open-agent-protocol.ui-runtime-core`
Base protocol: `open-agent-protocol` version `0.1`
License: CC0-1.0 public domain dedication, or the nearest legally valid equivalent in jurisdictions that do not recognize public domain dedication.
Scope: the 80/20 UI-to-runtime contract for agent applications.

This profile is the first conformance target. It defines the smallest useful
surface for an agent UI to discover a runtime, open a session, start work,
stream a timeline, handle tools and approvals, and receive a terminal result.

The broader [UI Runtime Protocol](ui-runtime-protocol.md) is a staging area for
richer controls. Those controls should not be required by the core unless they
prove necessary across multiple independent UIs and runtimes.

Profile and conformance-unit claims are described in the
[Conformance Draft](conformance.md).

## Design Rule

Core means common path, not complete power.

The core should cover:

- one UI talking to one runtime adapter;
- one session containing one or more runs;
- streamed assistant text and reasoning summaries when available;
- canonical session state and transcript sync after reconnect;
- model-selected tool calls and tool execution results;
- permission prompts;
- user-input prompts that pause an agent turn;
- user steering while a run is active;
- cancellation;
- final response, usage, and errors;
- transport-agnostic envelopes.

The core should not require:

- checkpointing, rewind, branch, or fork;
- artifact stores;
- auth login flows;
- external tool-source management;
- subagents, hooks, or background tasks;
- workspace sandbox policy;
- arbitrary query languages or runtime-specific live-query APIs;
- provider-native model stream events.

## Core Envelope

Core events use a flat envelope. It keeps the identifiers UIs need directly on
the event instead of requiring a separate trace object.

```ts
export interface CoreEnvelope<TPayload = unknown> {
  protocol: "open-agent-protocol";
  version: "0.1";
  profile: "open-agent-protocol.ui-runtime-core";
  type: string;
  id: string;
  sequence?: number;
  timestamp_ms?: number;
  in_reply_to?: string;
  session_id?: string;
  run_id?: string;
  turn_id?: string;
  tool_call_id?: string;
  payload: TPayload;
  extensions?: Record<string, unknown>;
}
```

The maintained draft TypeScript contract is [`types/core.ts`](../types/core.ts).

## Transport

The core is transport agnostic. The same envelopes can move over in-process
calls, JSONL over stdio, WebSocket messages, HTTP plus server-sent events, or
another binding.

Bindings must preserve event type, IDs, scoped ordering, request correlation,
payload meaning, terminal event rules, and extension fields. Connection setup,
heartbeats, reconnection, batching, authentication handshakes, and backpressure
are binding concerns.

## Core Commands

| Command | Event | Required for minimum |
| --- | --- | --- |
| Initialize connection | `runtime.initialize.request` | yes |
| Return initialize result | `runtime.initialize.response` | yes |
| Get capabilities | `runtime.capabilities.request` | yes |
| Return capabilities | `runtime.capabilities.response` | yes |
| Get runtime status | `runtime.status.request` | no |
| Return runtime status | `runtime.status.response` | no |
| List models | `runtime.models.list.request` | no |
| Open session | `session.open.request` | yes |
| Get session state | `session.state.request` | yes |
| Return session state | `session.state.response` | yes |
| List sessions | `session.list.request` | no |
| Load transcript | `transcript.load.request` | no |
| Start run | `run.start.request` | yes |
| Acknowledge run start | `run.start.response` | yes |
| Add input to active run | `run.input.request` | no |
| Acknowledge active-run input | `run.input.response` | only if active-run input is supported |
| Cancel run | `run.cancel.request` | yes |
| Resolve permission | `permission.resolve.request` | only if permissions are supported |
| Resolve user-input prompt | `user.input.resolve.request` | only if user input is supported |
| Cancel user-input prompt | `user.input.cancel.request` | only if user input is supported |

## Core Responses And Stream Events

| Event | Meaning | Required for minimum |
| --- | --- | --- |
| `runtime.status.updated` | Runtime health changed. | no |
| `session.opened` | Session is ready. | yes |
| `session.state.updated` | Canonical session state changed. | yes |
| `transcript.delta` | Canonical transcript rows changed. | no |
| `run.start.response` | Runtime accepted a new run and assigned IDs. | yes |
| `run.input.response` | Runtime accepted active-run input. | only if active-run input is supported |
| `run.started` | Run began. | yes |
| `run.status.updated` | Run status or phase changed. | yes |
| `turn.started` | One agent loop step began. | no |
| `content.delta` | Assistant-visible text or reasoning delta. | yes |
| `tool.call.requested` | Model or runtime requested a tool. | only if tools are supported |
| `tool.call.started` | Tool execution began. | only if tools are supported |
| `tool.call.progress` | Tool execution progress. | no |
| `tool.call.completed` | Tool execution ended. | only if tools are supported |
| `permission.requested` | Runtime needs user approval. | only if permissions are supported |
| `permission.resolved` | Permission result was accepted by runtime. | only if permissions are supported |
| `user.input.requested` | Runtime needs user input before the run can continue. | only if user input is supported |
| `user.input.resolved` | User-input prompt was submitted or cancelled. | only if user input is supported |
| `turn.completed` | One agent loop step ended. | no |
| `run.completed` | Run completed successfully. | yes |
| `run.failed` | Run failed terminally. | yes |
| `run.cancelled` | Run was cancelled terminally. | yes |

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

### Run Start

`run.start.request` starts work in a session.

Core fields:

- `session_id`
- `input`
- `model_id`
- `instructions`
- `tools`
- `tool_choice`
- `output_schema`
- `metadata`

`instructions`, `tools`, `tool_choice`, and `output_schema` are capability
gated. A runtime that cannot honor them should report the feature as degraded or
unavailable.

The runtime must answer an accepted `run.start.request` with
`run.start.response` before or alongside the stream. The response assigns the
`run_id`, echoes the effective `model_id` when known, and may return accepted
input message IDs. This lets a UI render optimistic input, correlate retries,
and recover if the stream connection reconnects before `run.started` arrives.

`run.input.request` uses the same pattern through `run.input.response` when a
runtime supports steering or queued input while a run is active.

Core run statuses are `queued`, `running`, `waiting_for_input`, `cancelling`,
`completed`, `failed`, and `cancelled`. `run.status.updated` should be emitted
for meaningful lifecycle changes so UIs can render status without inferring it
from provider-specific events.

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

`transcript.load.request` loads persisted messages for initial history,
pagination, or reconnect recovery. A response should include `sync_cursor` when
the runtime can provide one. `transcript.delta` is the optional canonical update
event for persisted transcript rows; it is distinct from `content.delta`, which
is the live assistant stream and may arrive before persistence.

The core does not define a generic live-query language. A binding or runtime can
offer one, but UIs should not need it for the common chat/session surface.

### Tools

Core tool definitions use JSON Schema input:

- `name`
- `description`
- `input_schema`
- `annotations`

The core does not define how tools are hosted. A runtime may execute tools
natively, call out to another process, or bridge an external tool system.

### Permissions

Permission prompts are explicit events. A UI resolves them by sending
`permission.resolve.request` with:

- `permission_id`
- `choice_id`
- `granted`
- `reason`
- `updated_arguments_json`

`updated_arguments_json` covers the common case where the UI or policy layer
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

Each question may be plain text, single choice, or multi choice. A UI submits
answers through `user.input.resolve.request` or cancels through
`user.input.cancel.request`. Runtimes that persist drafts may expose that through
extensions or a richer profile; draft persistence is not required for core
conformance.

### Completion

`run.completed` should include:

- `final_response`
- `stop_reason`
- `usage`
- `duration_ms`

This makes non-streaming consumers and history UIs useful without replaying the
whole stream.

## Core Capability Keys

Minimum features:

- `runtime.initialize`
- `runtime.capabilities`
- `session.state`
- `session.open`
- `run.start`
- `run.start.ack`
- `run.streaming`
- `run.status`
- `run.cancel`

Common optional core features:

- `runtime.status`
- `runtime.models.list`
- `session.list`
- `transcript.load`
- `transcript.delta`
- `run.input`
- `run.instructions`
- `run.tool_selection`
- `run.structured_output`
- `content.reasoning`
- `content.image`
- `tools`
- `tools.progress`
- `permissions`
- `user_input`

Support levels are:

- `native`
- `emulated`
- `degraded`
- `unavailable`

The UI should enable controls from capabilities, not from runtime names.

## Minimum Conformance

A runtime is core-conformant if it can:

1. initialize a connection;
2. return a capability descriptor;
3. open a session;
4. return canonical session state;
5. accept a run start and return `run.start.response`;
6. stream assistant text through `content.delta`;
7. emit run status updates for meaningful lifecycle changes;
8. end every started run with one terminal run event;
9. cancel a running run or report cancellation as unavailable;
10. return typed errors for unsupported commands.

If a runtime supports tools, it must also emit tool request/start/completion
events. If it supports permission prompts, it must use the permission events
instead of inventing a private UI callback.

If a runtime supports user-input prompts, it must use `user.input.*` events
instead of encoding those prompts as permission requests.

## Richer Controls

These are deliberately outside the core for now:

- auth provider listing and login flows;
- model resolve, model metadata caching, and provider auth state;
- session rename, archive, delete, and metadata patching;
- checkpoint, rewind, branch, fork, and file rollback;
- artifact creation, retrieval, hashing, and persistent stores;
- workspace, cwd, environment, sandbox, and network policy;
- external tool-source attachment and management;
- hooks, subagents, background tasks, and task notifications;
- telemetry, cost accounting, rate-limit events, and retry detail;
- context compaction controls;
- full provider-native model stream passthrough;
- generated JSON Schema bundles and conformance test suites.

Each of these can become an optional profile once the core event model feels
right.
